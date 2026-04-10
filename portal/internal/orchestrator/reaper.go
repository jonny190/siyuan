package orchestrator

import (
	"context"
	"log"
	"time"

	"github.com/siyuan-note/siyuan/portal/internal/users"
)

// Reaper stops idle kernel containers to reclaim memory. A SiYuan kernel takes ~300-800MB
// of RAM at rest; with more than a handful of users the reaper is mandatory. Idle is
// defined as "no activity on the reverse-proxy for IdleMinutes minutes".
//
// The portal's reverse-proxy middleware is responsible for calling Store.TouchLastActive
// on every request — without that signal, the reaper would stop containers in the middle
// of an editing session.
type Reaper struct {
	orch         *Orchestrator
	store        *users.Store
	idleDuration time.Duration
	interval     time.Duration
}

// NewReaper returns a Reaper that checks every interval and stops kernels whose
// last_active_at is older than idleDuration.
func NewReaper(orch *Orchestrator, store *users.Store, idleDuration, interval time.Duration) *Reaper {
	if interval == 0 {
		interval = time.Minute
	}
	return &Reaper{orch: orch, store: store, idleDuration: idleDuration, interval: interval}
}

// Run blocks until ctx is canceled, running one pass per interval.
func (r *Reaper) Run(ctx context.Context) {
	if r.idleDuration <= 0 {
		log.Printf("portal: idle reaper disabled (idle duration = 0)")
		return
	}
	log.Printf("portal: idle reaper started (idle=%s, interval=%s)", r.idleDuration, r.interval)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.pass(ctx)
		}
	}
}

// pass does one sweep: list users, stop anyone running + idle.
func (r *Reaper) pass(ctx context.Context) {
	usersList, err := r.store.List(ctx)
	if err != nil {
		log.Printf("portal: reaper list users: %v", err)
		return
	}

	threshold := time.Now().Add(-r.idleDuration)
	for _, u := range usersList {
		if u.KernelStatus != users.StatusRunning {
			continue
		}
		if u.LastActiveAt == nil {
			// Never active since last boot — include in the reap to avoid strays.
			log.Printf("portal: reaping never-active kernel for user %d (%s)", u.ID, u.Username)
		} else if u.LastActiveAt.After(threshold) {
			continue
		}

		log.Printf("portal: reaping idle kernel for user %d (%s)", u.ID, u.Username)
		if err := r.orch.Stop(ctx, u); err != nil {
			log.Printf("portal: reaper stop user %d: %v", u.ID, err)
		}
	}
}
