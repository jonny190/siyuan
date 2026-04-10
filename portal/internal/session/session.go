// Package session provides the portal's cookie-based session middleware, CSRF helpers,
// and a lightweight per-IP login rate limiter.
//
// Sessions are opaque random tokens stored in SQLite (see internal/users). The cookie
// carries only the token; all state lives server-side. Same-site=Strict and HttpOnly are
// applied unconditionally; Secure is applied when the request arrived over HTTPS.
package session

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/siyuan-note/siyuan/portal/internal/proxy"
	"github.com/siyuan-note/siyuan/portal/internal/users"
	"golang.org/x/time/rate"
)

// CookieName is the name of the portal's session cookie. Distinct from the kernel's
// "siyuan" cookie so the two never collide.
const CookieName = "portal_session"

// DefaultDuration is how long a new session lives before expiring. Kept shortish because
// we do not rotate tokens — logging out and back in is the only way to refresh.
const DefaultDuration = 24 * time.Hour

// Middleware loads the session from the cookie, validates it against the store, and
// stashes the authenticated user on the request context via proxy.WithUser.
//
// If loadOnly is true, the middleware allows anonymous requests to pass through
// (used for /login and /static/*). Otherwise anonymous requests are redirected to
// /login with ?next= pointing back at the original URL.
type Middleware struct {
	Store    *users.Store
	LoadOnly bool
}

// Wrap returns an http.Handler that runs the session logic before delegating to next.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := m.resolve(r)
		if user == nil && !m.LoadOnly {
			// Redirect to login, preserving the original target so we can bounce back.
			loginURL := "/login"
			if r.URL.Path != "/" && r.URL.Path != "" {
				loginURL += "?next=" + r.URL.Path
			}
			http.Redirect(w, r, loginURL, http.StatusFound)
			return
		}
		if user != nil {
			r = r.WithContext(proxy.WithUser(r.Context(), user))
		}
		next.ServeHTTP(w, r)
	})
}

// resolve returns the authenticated user or nil. It cleans up any invalid cookie so the
// browser doesn't keep presenting a stale token.
func (m *Middleware) resolve(r *http.Request) *users.User {
	cookie, err := r.Cookie(CookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	_, user, err := m.Store.GetSession(r.Context(), cookie.Value)
	if err != nil || user == nil || user.Disabled {
		return nil
	}
	return user
}

// SetSessionCookie writes the portal_session cookie. Secure is set only if the request
// was TLS; the operator is expected to terminate TLS at an upstream reverse proxy
// (Caddy/Traefik) that sets X-Forwarded-Proto=https, which Go maps onto r.TLS being nil
// + the X-Forwarded-Proto header.
func SetSessionCookie(w http.ResponseWriter, r *http.Request, token string, duration time.Duration) {
	cookie := &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(duration),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isTLS(r),
	}
	http.SetCookie(w, cookie)
}

// ClearSessionCookie emits a cookie with MaxAge=-1 to tell the browser to drop it.
func ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isTLS(r),
	})
}

// isTLS reports whether the original request arrived over HTTPS, honoring the
// X-Forwarded-Proto header from a trusted upstream reverse proxy.
func isTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// ClientIP returns the real client IP from X-Forwarded-For (first entry) or RemoteAddr.
// Used for audit logging and login rate limiting.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return host
}

// --- rate limiter ------------------------------------------------------------------------

// RateLimiter is a lightweight per-key token bucket. Used for /login to stop brute force.
// Buckets are indexed by client IP and garbage-collected after inactivity.
type RateLimiter struct {
	rate  rate.Limit
	burst int
	ttl   time.Duration

	mu      sync.Mutex
	buckets map[string]*bucketEntry
}

type bucketEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter returns a limiter that allows `burst` events immediately, then `ratePerSec`
// per second sustained. Entries older than ttl get garbage-collected on access.
func NewRateLimiter(ratePerSec float64, burst int, ttl time.Duration) *RateLimiter {
	return &RateLimiter{
		rate:    rate.Limit(ratePerSec),
		burst:   burst,
		ttl:     ttl,
		buckets: make(map[string]*bucketEntry),
	}
}

// Allow reports whether a request from the given key (IP) is allowed right now.
func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	entry, ok := rl.buckets[key]
	if !ok {
		entry = &bucketEntry{limiter: rate.NewLimiter(rl.rate, rl.burst)}
		rl.buckets[key] = entry
	}
	entry.lastSeen = now

	// Opportunistic GC: prune any stale buckets on every call. O(N) but N is small
	// (one entry per recent IP). Avoids needing a background goroutine.
	for k, e := range rl.buckets {
		if now.Sub(e.lastSeen) > rl.ttl {
			delete(rl.buckets, k)
		}
	}
	return entry.limiter.Allow()
}

// PurgeExpiredSessionsJob runs periodically to clean up expired session rows. Keeping the
// sessions table small makes the session-resolve query faster.
func PurgeExpiredSessionsJob(ctx context.Context, store *users.Store, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = store.PurgeExpiredSessions(ctx)
		}
	}
}
