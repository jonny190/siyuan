// Command portal is the SiYuan self-host multi-user gateway.
//
// It sits in front of a fleet of stripped SiYuan kernel containers (one per user),
// handles login/session state, spawns and tears down containers on demand via the
// Docker API, and reverse-proxies authenticated traffic to the correct kernel with a
// per-user Authorization: Token header injected into every request.
//
// See the plan file for the full architecture write-up:
//
//	~/.claude/plans/fuzzy-booping-rocket.md
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/siyuan-note/siyuan/portal/internal/admin"
	"github.com/siyuan-note/siyuan/portal/internal/config"
	"github.com/siyuan-note/siyuan/portal/internal/orchestrator"
	"github.com/siyuan-note/siyuan/portal/internal/proxy"
	"github.com/siyuan-note/siyuan/portal/internal/session"
	"github.com/siyuan-note/siyuan/portal/internal/users"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("portal: starting")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("portal: load config: %v", err)
	}
	log.Printf("portal: listen=%s docker=%s image=%s workspaces=%s idle=%s",
		cfg.Listen, cfg.DockerHost, cfg.SiYuanImage, cfg.WorkspaceRoot, cfg.IdleDuration)

	// Ensure the DB directory exists before Open() tries to create the file inside it.
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		log.Fatalf("portal: create db dir: %v", err)
	}

	store, err := users.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("portal: open store: %v", err)
	}
	defer store.Close()

	dockerClient, err := orchestrator.NewDockerClient(cfg.DockerHost)
	if err != nil {
		log.Fatalf("portal: docker client: %v", err)
	}

	// Best-effort: ensure the siyuan-net network exists. If the admin pre-created it
	// via docker-compose this is a no-op; otherwise we create it. Failing here is only
	// fatal if the docker API is unreachable, which we want to know about immediately.
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := dockerClient.NetworkCreate(bootCtx, cfg.DockerNetwork, true); err != nil {
		log.Printf("portal: network create %q: %v (continuing; may already exist)", cfg.DockerNetwork, err)
	}
	bootCancel()

	orch := orchestrator.New(orchestrator.Config{
		Image:            cfg.SiYuanImage,
		Network:          cfg.DockerNetwork,
		WorkspaceRoot:    cfg.WorkspaceRoot,
		PUID:             cfg.PUID,
		PGID:             cfg.PGID,
		OpenAIBaseURL:    cfg.OpenAIBaseURL,
		OpenAIAPIKey:     cfg.OpenAIAPIKey,
		MemoryLimitBytes: cfg.KernelMemoryLimitBytes,
		PidsLimit:        cfg.KernelPidsLimit,
		BootTimeout:      30 * time.Second,
	}, dockerClient, store)

	// Bootstrap the initial admin user if the table is empty and env vars are set.
	bootstrapCtx, bootstrapCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := maybeBootstrapAdmin(bootstrapCtx, store, orch, cfg); err != nil {
		log.Fatalf("portal: bootstrap admin: %v", err)
	}
	bootstrapCancel()

	rateLimiter := session.NewRateLimiter(1.0/6.0, 5, 10*time.Minute) // 5 burst, 1 per 6s sustained
	handlers, err := admin.New(store, orch, rateLimiter)
	if err != nil {
		log.Fatalf("portal: admin handlers: %v", err)
	}
	proxyHandler := proxy.New(orch, store)

	// --- routing ------------------------------------------------------------------------

	// Public routes: /login, /logout don't need session-backed auth.
	mux := http.NewServeMux()
	mux.HandleFunc("/login", handlers.Login)
	mux.HandleFunc("/logout", handlers.Logout)

	// Protected routes: /admin and everything else. We use two wrapped handlers because
	// /admin requires a role check and delegates to handlers.AdminPage; everything else
	// goes to the reverse proxy.
	authed := &session.Middleware{Store: store, LoadOnly: false}

	mux.Handle("/admin", authed.Wrap(http.HandlerFunc(handlers.AdminPage)))
	// Fall-through: every other path proxies to the user's kernel.
	mux.Handle("/", authed.Wrap(proxyHandler))

	// --- background jobs ---------------------------------------------------------------

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	go session.PurgeExpiredSessionsJob(rootCtx, store, 10*time.Minute)

	reaper := orchestrator.NewReaper(orch, store, cfg.IdleDuration, time.Minute)
	go reaper.Run(rootCtx)

	// --- HTTP server + graceful shutdown -----------------------------------------------

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
		// IdleTimeout is deliberately long because WebSocket connections keep the
		// underlying TCP socket alive for the entire editor session.
		IdleTimeout: 2 * time.Hour,
	}

	go func() {
		log.Printf("portal: listening on %s", cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("portal: server: %v", err)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM. We give the server 10s to drain existing
	// requests (including in-flight WebSocket sessions) before exiting. The kernel
	// containers themselves are left running — shutting them down on portal restart
	// would reset every user's workspace session, which is bad UX during rolling
	// deploys.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	log.Printf("portal: shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("portal: shutdown: %v", err)
	}
	rootCancel()
	log.Printf("portal: goodbye")
}

// maybeBootstrapAdmin creates the initial admin if the users table is empty and both
// PORTAL_BOOTSTRAP_ADMIN_USER and PORTAL_BOOTSTRAP_ADMIN_PASSWORD are set. This avoids a
// chicken-and-egg problem where you can't log in to create the first user.
//
// Once the admin exists we log a bright warning so the operator knows to remove the
// env vars from their compose file — leaving the plaintext password in docker-compose.yml
// is a liability.
func maybeBootstrapAdmin(ctx context.Context, store *users.Store, orch *orchestrator.Orchestrator, cfg *config.Config) error {
	if cfg.BootstrapAdminUser == "" || cfg.BootstrapAdminPassword == "" {
		return nil
	}
	count, err := store.Count(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		// Already bootstrapped. Repeating the warning every boot nags the operator.
		return nil
	}

	log.Printf("portal: bootstrapping initial admin user %q from env", cfg.BootstrapAdminUser)

	apiToken, err := users.RandomToken(32)
	if err != nil {
		return err
	}
	authCode, err := users.RandomToken(32)
	if err != nil {
		return err
	}

	id, err := store.Create(ctx, users.CreateUserArgs{
		Username:        cfg.BootstrapAdminUser,
		PasswordPlain:   cfg.BootstrapAdminPassword,
		Role:            users.RoleAdmin,
		WorkspacePath:   "", // filled in below
		KernelContainer: "",
		KernelAPIToken:  apiToken,
		KernelAuthCode:  authCode,
	})
	if err != nil {
		return err
	}

	user, err := store.GetByID(ctx, id)
	if err != nil {
		return err
	}
	user.WorkspacePath = orch.WorkspacePath(id)
	user.KernelContainer = orch.ContainerName(id)
	if _, err := store.ExecRaw(ctx,
		`UPDATE users SET workspace_path = ?, kernel_container = ? WHERE id = ?`,
		user.WorkspacePath, user.KernelContainer, id); err != nil {
		return err
	}

	if err := orch.Provision(user); err != nil {
		log.Printf("portal: bootstrap provision: %v (non-fatal)", err)
	}

	log.Printf("portal: ============================================================")
	log.Printf("portal: initial admin %q created. REMOVE PORTAL_BOOTSTRAP_ADMIN_USER", cfg.BootstrapAdminUser)
	log.Printf("portal: and PORTAL_BOOTSTRAP_ADMIN_PASSWORD from your environment")
	log.Printf("portal: after logging in for the first time.")
	log.Printf("portal: ============================================================")
	return nil
}
