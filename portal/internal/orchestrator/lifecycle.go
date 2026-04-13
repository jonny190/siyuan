package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/siyuan-note/siyuan/portal/internal/users"
)

// Config holds the orchestrator's global settings, read once from env vars at portal
// startup (see internal/config). Per-user fields live on the users row.
type Config struct {
	// Image is the siyuan-selfhost container image tag.
	Image string

	// Network is the user-defined Docker network all kernel containers attach to.
	// Both the portal and the kernels must be on this network so the portal can reach
	// each kernel by container name (e.g. http://siyuan-user-alice:6806).
	Network string

	// WorkspaceRoot is the host directory under which each user's workspace lives.
	// Per-user path: <WorkspaceRoot>/<userID>/workspace.
	WorkspaceRoot string

	// PUID / PGID are passed into each kernel container so the kernel process runs as
	// the owner of the bind-mounted workspace directory. Must match the host-side chown.
	PUID int
	PGID int

	// OpenAIBaseURL and OpenAIAPIKey are forwarded into every kernel container as
	// SIYUAN_OPENAI_API_BASE_URL / SIYUAN_OPENAI_API_KEY environment variables.
	OpenAIBaseURL string
	OpenAIAPIKey  string

	// MemoryLimitBytes caps kernel memory. A SiYuan kernel typically uses 300-800MB
	// under active editing; 1 GiB is a sensible starting default.
	MemoryLimitBytes int64

	// PidsLimit caps the per-container PID count to contain runaway pandoc/ocr forks.
	PidsLimit int64

	// BootTimeout is how long a request will wait for a cold-started kernel to become
	// ready (responds to /api/system/bootProgress with booted=true) before giving up.
	BootTimeout time.Duration
}

// Orchestrator manages the lifecycle of kernel containers for each user. Safe for
// concurrent use; per-user operations are serialized through a sync.Mutex keyed by user ID
// to avoid racing Start() + Stop() on the same container.
type Orchestrator struct {
	cfg    Config
	docker *DockerClient
	store  *users.Store

	mu    sync.Mutex
	locks map[int64]*sync.Mutex // per-user mutex; guards Start/Stop sequencing
}

// New wires an Orchestrator from its dependencies.
func New(cfg Config, docker *DockerClient, store *users.Store) *Orchestrator {
	if cfg.BootTimeout == 0 {
		cfg.BootTimeout = 30 * time.Second
	}
	return &Orchestrator{
		cfg:    cfg,
		docker: docker,
		store:  store,
		locks:  make(map[int64]*sync.Mutex),
	}
}

// userLock returns the per-user mutex, allocating it on first use.
func (o *Orchestrator) userLock(userID int64) *sync.Mutex {
	o.mu.Lock()
	defer o.mu.Unlock()
	m, ok := o.locks[userID]
	if !ok {
		m = &sync.Mutex{}
		o.locks[userID] = m
	}
	return m
}

// WorkspacePath returns the host filesystem path for a user's workspace directory.
func (o *Orchestrator) WorkspacePath(userID int64) string {
	return filepath.Join(o.cfg.WorkspaceRoot, fmt.Sprintf("%d", userID), "workspace")
}

// ContainerName returns the conventional Docker container name for a user.
func (o *Orchestrator) ContainerName(userID int64) string {
	return fmt.Sprintf("siyuan-user-%d", userID)
}

// KernelURL returns the HTTP base URL the portal's reverse proxy uses to reach the user's
// kernel container over the internal docker network.
func (o *Orchestrator) KernelURL(user *users.User) string {
	return "http://" + user.KernelContainer + ":6806"
}

// Provision prepares the host-side workspace directory for a new user and seeds
// <workspace>/conf/conf.json with the portal-generated api.token and accessAuthCode so
// the kernel will accept the portal's injected Authorization: Token header on first boot.
//
// The kernel container itself is created lazily on first EnsureRunning(). The caller
// (admin handler or bootstrap-admin path) has already written the users row.
//
// Both steps are idempotent: Provision on an existing workspace leaves conf.json
// untouched so user preferences aren't clobbered.
func (o *Orchestrator) Provision(user *users.User) error {
	if err := os.MkdirAll(user.WorkspacePath, 0o755); err != nil {
		return fmt.Errorf("create workspace dir %s: %w", user.WorkspacePath, err)
	}
	// Chown to the PUID/PGID we will pass into the container. If the caller lacks
	// CAP_CHOWN we log and continue — this is best-effort and documented in the README.
	if err := os.Chown(user.WorkspacePath, o.cfg.PUID, o.cfg.PGID); err != nil {
		// chown typically fails when the portal runs as non-root without CAP_CHOWN.
		// We do not treat this as fatal because operators can pre-create the dir.
		fmt.Fprintf(os.Stderr, "portal: chown workspace %s: %v (non-fatal)\n", user.WorkspacePath, err)
	}
	// Seed conf.json so the kernel honors the portal's per-user Authorization token.
	// This MUST happen inside Provision rather than at the caller because otherwise
	// the bootstrap-admin path (which also calls Provision but skips downstream
	// caller logic) would boot a kernel with a random api.token that nobody knows.
	if err := seedConfJSON(user.WorkspacePath, user.KernelAPIToken, user.KernelAuthCode); err != nil {
		return fmt.Errorf("seed conf.json: %w", err)
	}
	return nil
}

// EnsureRunning guarantees that the user's kernel container exists, is running, and has
// finished booting. Safe to call on every request; the per-user lock plus DB-backed
// status field make concurrent callers cheap to serialize.
//
// The caller passes a context whose deadline is honored for both the Docker API calls and
// the bootProgress polling loop.
func (o *Orchestrator) EnsureRunning(ctx context.Context, user *users.User) error {
	lock := o.userLock(user.ID)
	lock.Lock()
	defer lock.Unlock()

	// Before any kernel boots (fresh create, or restarting a stopped container),
	// ensure the on-disk conf.json has the portal's api.token. seedConfJSON is
	// idempotent — a no-op when the file is already correct — so calling it
	// unconditionally is cheap and protects against workspace drift (operator wiped
	// conf.json, DB restored from a different backup, etc.). We skip it for the
	// already-running case because the live kernel has already read the file into
	// memory; rewriting under its feet would do nothing.
	if err := os.MkdirAll(user.WorkspacePath, 0o755); err != nil {
		return fmt.Errorf("ensure workspace dir: %w", err)
	}

	// 1) Inspect. If it's already running we're done.
	info, err := o.docker.InspectContainer(ctx, user.KernelContainer)
	switch {
	case err == nil && info.State.Running:
		// Already running. Still poll bootProgress in case the kernel is partway
		// through Init*; this is a no-op if it's fully booted.
		return o.waitForBoot(ctx, user)

	case err == nil && !info.State.Running:
		// Exists but stopped/exited. Reconcile conf.json before the kernel re-reads
		// it on start, then start.
		if err := seedConfJSON(user.WorkspacePath, user.KernelAPIToken, user.KernelAuthCode); err != nil {
			return fmt.Errorf("reconcile conf.json before restart: %w", err)
		}
		_ = o.store.UpdateKernelStatus(ctx, user.ID, users.StatusStarting)
		if err := o.docker.StartContainer(ctx, user.KernelContainer); err != nil {
			_ = o.store.UpdateKernelStatus(ctx, user.ID, users.StatusFailed)
			return fmt.Errorf("start existing container: %w", err)
		}

	case IsNotFound(err):
		// First-ever start (or container was removed). createContainer does its own
		// conf.json seed. Create then start.
		_ = o.store.UpdateKernelStatus(ctx, user.ID, users.StatusStarting)
		if err := o.createContainer(ctx, user); err != nil {
			_ = o.store.UpdateKernelStatus(ctx, user.ID, users.StatusFailed)
			return err
		}
		if err := o.docker.StartContainer(ctx, user.KernelContainer); err != nil {
			_ = o.store.UpdateKernelStatus(ctx, user.ID, users.StatusFailed)
			return fmt.Errorf("start new container: %w", err)
		}

	default:
		_ = o.store.UpdateKernelStatus(ctx, user.ID, users.StatusFailed)
		return fmt.Errorf("inspect container: %w", err)
	}

	if err := o.waitForBoot(ctx, user); err != nil {
		_ = o.store.UpdateKernelStatus(ctx, user.ID, users.StatusFailed)
		return err
	}
	_ = o.store.UpdateKernelStatus(ctx, user.ID, users.StatusRunning)
	return nil
}

// createContainer issues the Docker CreateContainer call with the user-specific spec. The
// bind mount, env vars, and the kernel CLI args (--workspace, --accessAuthCode, etc.) are
// all filled in here.
//
// As a safety net, we also (re-)seed conf.json here. Provision does this at user-creation
// time, but if a workspace is half-broken for any reason (operator wiped conf.json, older
// portal version never wrote one, etc.) the kernel would boot with a random api.token and
// the portal's Authorization: Token injection would fail. seedConfJSON is idempotent, so
// calling it here is free when the file already exists.
func (o *Orchestrator) createContainer(ctx context.Context, user *users.User) error {
	if err := os.MkdirAll(user.WorkspacePath, 0o755); err != nil {
		return fmt.Errorf("ensure workspace dir: %w", err)
	}
	if err := seedConfJSON(user.WorkspacePath, user.KernelAPIToken, user.KernelAuthCode); err != nil {
		return fmt.Errorf("ensure conf.json: %w", err)
	}

	// Bind mount: host path -> /siyuan/workspace inside the container.
	bind := fmt.Sprintf("%s:/siyuan/workspace", user.WorkspacePath)

	env := []string{
		fmt.Sprintf("PUID=%d", o.cfg.PUID),
		fmt.Sprintf("PGID=%d", o.cfg.PGID),
	}
	if o.cfg.OpenAIBaseURL != "" {
		env = append(env, "SIYUAN_OPENAI_API_BASE_URL="+o.cfg.OpenAIBaseURL)
	}
	if o.cfg.OpenAIAPIKey != "" {
		env = append(env, "SIYUAN_OPENAI_API_KEY="+o.cfg.OpenAIAPIKey)
	}

	// Kernel command line. --accessAuthCode sets Conf.AccessAuthCode, which is how the
	// kernel's WebSocket HandleConnect enforces the Authorization: Token header (see
	// kernel/server/serve.go §A.9). The token itself is written into conf/conf.json by
	// the kernel on first boot; we pass it via --accessAuthCode so the kernel has a
	// non-empty value even before the first API token is configured.
	//
	// Note: we deliberately do NOT expose Conf.Api.Token via CLI because the kernel only
	// accepts it from the persisted conf.json. The admin handler that creates the user
	// is responsible for seeding <workspace>/conf/conf.json with the right api.token
	// before calling Provision.
	cmd := []string{
		"--workspace=/siyuan/workspace",
		"--accessAuthCode=" + user.KernelAuthCode,
	}

	spec := ContainerCreateRequest{
		Image:        o.cfg.Image,
		Cmd:          cmd,
		Env:          env,
		ExposedPorts: map[string]struct{}{"6806/tcp": {}},
		Labels: map[string]string{
			"siyuan-selfhost.user-id":  fmt.Sprintf("%d", user.ID),
			"siyuan-selfhost.username": user.Username,
		},
		HostConfig: &HostConfig{
			Binds:         []string{bind},
			Memory:        o.cfg.MemoryLimitBytes,
			PidsLimit:     o.cfg.PidsLimit,
			RestartPolicy: RestartPolicy{Name: "no"},
			NetworkMode:   o.cfg.Network,
		},
		NetworkingConfig: &NetworkingConfig{
			EndpointsConfig: map[string]EndpointSettings{
				o.cfg.Network: {Aliases: []string{user.KernelContainer}},
			},
		},
	}

	_, err := o.docker.CreateContainer(ctx, user.KernelContainer, spec)
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	return nil
}

// waitForBoot polls /api/system/bootProgress until the kernel reports booted or the
// context deadline fires. A kernel cold-start is ~3-10s; if it takes longer than
// BootTimeout something is genuinely wrong.
func (o *Orchestrator) waitForBoot(ctx context.Context, user *users.User) error {
	bootCtx, cancel := context.WithTimeout(ctx, o.cfg.BootTimeout)
	defer cancel()

	url := o.KernelURL(user) + "/api/system/bootProgress"
	client := &http.Client{Timeout: 3 * time.Second}

	// bootProgress is an unauthenticated endpoint (see kernel/api/router.go) so we do
	// not need to inject the Authorization header here.
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-bootCtx.Done():
			return fmt.Errorf("kernel boot timeout for user %d", user.ID)
		case <-ticker.C:
		}

		req, _ := http.NewRequestWithContext(bootCtx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err != nil {
			continue // kernel not listening yet; keep polling
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}
		// The kernel returns {"code":0,"msg":"...","data":{"progress":N,"details":...}}
		// where progress == 100 and the "booted" flag means init is complete. We accept
		// either "booted":true or progress >= 100.
		var parsed struct {
			Data struct {
				Progress int  `json:"progress"`
				Booted   bool `json:"booted"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			continue
		}
		if parsed.Data.Booted || parsed.Data.Progress >= 100 {
			return nil
		}
	}
}

// Stop gracefully stops a user's kernel container. It first POSTs /api/system/exit to the
// kernel (with the per-user API token) so the kernel can flush its SQL queue and dejavu
// index, then calls Docker stop with a short timeout.
func (o *Orchestrator) Stop(ctx context.Context, user *users.User) error {
	lock := o.userLock(user.ID)
	lock.Lock()
	defer lock.Unlock()

	_ = o.store.UpdateKernelStatus(ctx, user.ID, users.StatusStopping)

	// Best-effort graceful exit. We do not treat failure here as fatal because the
	// container may already be stopped.
	exitURL := o.KernelURL(user) + "/api/system/exit"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, exitURL, nil)
	req.Header.Set("Authorization", "Token "+user.KernelAPIToken)
	client := &http.Client{Timeout: 5 * time.Second}
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}

	// Docker stop with a 10-second graceful window. If the kernel already exited cleanly
	// above, this is a no-op.
	if err := o.docker.StopContainer(ctx, user.KernelContainer, 10); err != nil && !IsNotFound(err) {
		_ = o.store.UpdateKernelStatus(ctx, user.ID, users.StatusFailed)
		return fmt.Errorf("docker stop: %w", err)
	}

	_ = o.store.UpdateKernelStatus(ctx, user.ID, users.StatusStopped)
	return nil
}

// Delete removes the user's kernel container entirely. Called from the admin handler
// after the user row has been soft-deleted / archived. Does not touch the workspace
// directory on disk (archival is the admin handler's responsibility).
func (o *Orchestrator) Delete(ctx context.Context, user *users.User) error {
	lock := o.userLock(user.ID)
	lock.Lock()
	defer lock.Unlock()

	if err := o.docker.RemoveContainer(ctx, user.KernelContainer); err != nil && !IsNotFound(err) {
		return fmt.Errorf("remove container: %w", err)
	}
	_ = o.store.UpdateKernelStatus(ctx, user.ID, users.StatusStopped)
	return nil
}
