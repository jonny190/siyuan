// Package config loads the portal's runtime configuration from environment variables.
// Everything is env-only: the portal has no config file of its own because all the
// "persistent" state lives in the SQLite user store.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config groups every runtime knob the portal exposes to the operator.
type Config struct {
	// DBPath is where the portal SQLite file lives.
	DBPath string

	// Listen is the HTTP listen address (e.g. ":8080"). TLS terminates at an upstream
	// reverse proxy (Caddy/Traefik); the portal itself runs plain HTTP on an internal
	// docker network.
	Listen string

	// DockerHost tells the orchestrator how to reach the Docker daemon. Supported:
	// - unix:///var/run/docker.sock (direct mount)
	// - tcp://docker-proxy:2375 (recommended: via tecnativa/docker-socket-proxy)
	DockerHost string

	// DockerNetwork is the name of the user-defined network both the portal and every
	// kernel container share. Portal reaches kernels by container name over this net.
	DockerNetwork string

	// SiYuanImage is the container image tag for the stripped kernel.
	SiYuanImage string

	// WorkspaceRoot is the host-side base directory for per-user workspaces:
	// <WorkspaceRoot>/<userID>/workspace.
	WorkspaceRoot string

	// PUID / PGID are passed into each kernel container and used to chown the workspace
	// directory. Must match whichever UID the stripped kernel image runs as.
	PUID int
	PGID int

	// OpenAIBaseURL / OpenAIAPIKey are forwarded into every kernel container so the AI
	// settings UI talks to the operator's local inference endpoint out of the box.
	OpenAIBaseURL string
	OpenAIAPIKey  string

	// KernelMemoryLimitBytes caps each kernel's RAM. 1 GiB default.
	KernelMemoryLimitBytes int64

	// KernelPidsLimit caps each kernel's PIDs to contain runaway subprocess forks.
	KernelPidsLimit int64

	// IdleDuration is the inactivity window after which a running kernel gets reaped.
	// Zero disables the reaper.
	IdleDuration time.Duration

	// BootstrapAdminUser and BootstrapAdminPassword seed an initial admin account on
	// first boot IF the users table is empty. Logged with a warning to remove after
	// the first login.
	BootstrapAdminUser     string
	BootstrapAdminPassword string
}

// Load reads env vars into a Config, applying defaults and validating required fields.
func Load() (*Config, error) {
	c := &Config{
		DBPath:                 env("PORTAL_DB_PATH", "/var/lib/portal/portal.db"),
		Listen:                 env("PORTAL_LISTEN", ":8080"),
		DockerHost:             env("PORTAL_DOCKER_HOST", "unix:///var/run/docker.sock"),
		DockerNetwork:          env("PORTAL_DOCKER_NETWORK", "siyuan-net"),
		SiYuanImage:            env("PORTAL_SIYUAN_IMAGE", "siyuan-selfhost:latest"),
		WorkspaceRoot:          env("PORTAL_WORKSPACE_ROOT", "/srv/siyuan/users"),
		OpenAIBaseURL:          os.Getenv("PORTAL_OPENAI_BASE_URL"),
		OpenAIAPIKey:           os.Getenv("PORTAL_OPENAI_API_KEY"),
		BootstrapAdminUser:     os.Getenv("PORTAL_BOOTSTRAP_ADMIN_USER"),
		BootstrapAdminPassword: os.Getenv("PORTAL_BOOTSTRAP_ADMIN_PASSWORD"),
	}

	puid, err := envInt("PORTAL_SIYUAN_PUID", 1000)
	if err != nil {
		return nil, err
	}
	pgid, err := envInt("PORTAL_SIYUAN_PGID", 1000)
	if err != nil {
		return nil, err
	}
	c.PUID = puid
	c.PGID = pgid

	memBytes, err := envInt64("PORTAL_KERNEL_MEMORY_BYTES", 1024*1024*1024) // 1 GiB
	if err != nil {
		return nil, err
	}
	c.KernelMemoryLimitBytes = memBytes

	pids, err := envInt64("PORTAL_KERNEL_PIDS_LIMIT", 512)
	if err != nil {
		return nil, err
	}
	c.KernelPidsLimit = pids

	idle, err := envDuration("PORTAL_IDLE_DURATION", 15*time.Minute)
	if err != nil {
		return nil, err
	}
	c.IdleDuration = idle

	return c, nil
}

// env returns the env var value if set, otherwise the fallback.
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envInt parses an int env var with a fallback. Returns an error on bad input so
// misconfiguration fails fast instead of silently reverting to the default.
func envInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q: %w", key, v, err)
	}
	return n, nil
}

// envInt64 is the int64 equivalent.
func envInt64(key string, fallback int64) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an int64, got %q: %w", key, v, err)
	}
	return n, nil
}

// envDuration parses a Go duration string (e.g. "15m", "1h"). Zero is allowed and
// interpreted by the caller (the reaper treats zero as "disabled").
func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration, got %q: %w", key, v, err)
	}
	return d, nil
}
