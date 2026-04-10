// Package proxy is the portal's reverse proxy layer. It sits in front of every user's
// kernel container and rewrites requests to:
//
//  1. Route to the correct container by looking up the session cookie's user record.
//  2. Inject Authorization: Token <user.KernelAPIToken> so the kernel grants admin.
//  3. Strip any client-supplied Cookie: siyuan=... to prevent bypass via forged cookies.
//
// WebSocket upgrades (/ws) are handled transparently by httputil.ReverseProxy — since
// Go 1.12 the stdlib proxy honors HTTP/1.1 Upgrade and hijacks the underlying conn,
// provided the Director does not strip Upgrade/Connection headers.
//
// The kernel-side patch in kernel/server/serve.go (§A.9) honors Authorization: Token on
// the /ws endpoint so this injection works for both HTTP and WS.
package proxy

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"

	"github.com/siyuan-note/siyuan/portal/internal/orchestrator"
	"github.com/siyuan-note/siyuan/portal/internal/users"
)

// Proxy is the portal reverse proxy. It resolves the logged-in user from context (placed
// there by the session middleware), ensures the user's kernel is running, and proxies the
// request to http://<container>:6806 with the per-user API token injected.
type Proxy struct {
	orch  *orchestrator.Orchestrator
	store *users.Store

	mu       sync.RWMutex
	proxies  map[int64]*httputil.ReverseProxy // one ReverseProxy per user; safe to cache
}

// New returns a ready-to-serve Proxy.
func New(orch *orchestrator.Orchestrator, store *users.Store) *Proxy {
	return &Proxy{
		orch:    orch,
		store:   store,
		proxies: make(map[int64]*httputil.ReverseProxy),
	}
}

// contextKey is a private type for request-scoped values (to avoid collision with other
// middlewares that might also use the string "user").
type contextKey int

const userKey contextKey = iota

// WithUser stashes the authenticated user on the request context. The session middleware
// calls this before handing off to the proxy handler.
func WithUser(ctx context.Context, u *users.User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// UserFromContext retrieves the authenticated user, or nil if none.
func UserFromContext(ctx context.Context) *users.User {
	u, _ := ctx.Value(userKey).(*users.User)
	return u
}

// ServeHTTP is the reverse-proxy entry point. It expects the session middleware to have
// already stashed the user on the request context; anonymous requests are a 401 because
// the router should have redirected them to /login before they reached here.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	if user.Disabled {
		http.Error(w, "account disabled", http.StatusForbidden)
		return
	}

	// Ensure the kernel is up before we forward the request. The orchestrator serializes
	// concurrent callers and no-ops when the container is already running + booted.
	ensureCtx, cancel := context.WithTimeout(r.Context(), 30*1000*1000*1000) // 30s
	defer cancel()
	if err := p.orch.EnsureRunning(ensureCtx, user); err != nil {
		log.Printf("portal: ensure running user=%d: %v", user.ID, err)
		// Show a friendly interstitial. The kernel-boot path can take 3-10s on cold
		// starts, and some clients will retry automatically.
		http.Error(w, "workspace is starting, please retry in a few seconds", http.StatusServiceUnavailable)
		return
	}

	// Update last_active_at asynchronously so we don't add latency to the hot path.
	go func(id int64) {
		_ = p.store.TouchLastActive(context.Background(), id)
	}(user.ID)

	rp := p.proxyFor(user)
	rp.ServeHTTP(w, r)
}

// proxyFor lazily builds and caches an httputil.ReverseProxy keyed by user ID. The cache
// entry is rebuilt if the kernel container name changes (shouldn't happen in practice
// because we derive it from the user ID).
func (p *Proxy) proxyFor(user *users.User) *httputil.ReverseProxy {
	p.mu.RLock()
	rp, ok := p.proxies[user.ID]
	p.mu.RUnlock()
	if ok {
		return rp
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if rp, ok := p.proxies[user.ID]; ok {
		return rp
	}

	target, err := url.Parse(p.orch.KernelURL(user))
	if err != nil {
		// KernelURL is constructed from a sanitized container name; a parse error here
		// is a bug. Construct a poison-pill proxy that always returns 500 so we don't
		// crash the portal.
		return &httputil.ReverseProxy{
			Director: func(r *http.Request) {},
			ModifyResponse: func(resp *http.Response) error {
				return fmt.Errorf("invalid kernel URL for user %d", user.ID)
			},
		}
	}

	// IMPORTANT: we do not override the Transport. The default http.Transport honors
	// HTTP/1.1 Upgrade correctly for WebSocket proxying. Setting a custom Transport
	// without copying DefaultTransport's fields is a common way to silently break WS.
	rp = &httputil.ReverseProxy{
		Director:       makeDirector(target, user),
		ModifyResponse: modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("portal: proxy error user=%d path=%s: %v", user.ID, r.URL.Path, err)
			http.Error(w, "upstream error", http.StatusBadGateway)
		},
	}
	p.proxies[user.ID] = rp
	return rp
}

// makeDirector returns the Director function that rewrites each incoming request to
// point at the kernel. It captures target and user so the returned closure has no
// per-call allocations beyond the URL rewrite itself.
func makeDirector(target *url.URL, user *users.User) func(*http.Request) {
	// We deliberately store a copy of the token to avoid races if a future code path
	// rotates user.KernelAPIToken while a proxy is mid-request.
	authHeader := "Token " + user.KernelAPIToken

	return func(req *http.Request) {
		// Rewrite Scheme/Host/Path to the kernel target. The path stays the same — a
		// request for /api/block/getBlockInfo at the portal becomes a request for
		// /api/block/getBlockInfo at the kernel.
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host

		// Inject the per-user API token. Overwrites any client-supplied Authorization
		// so a malicious client can't try to impersonate another user.
		req.Header.Set("Authorization", authHeader)

		// Strip any client-side cookie for the kernel's session store — it is now
		// vestigial under the portal model, but leaving it in would be a bypass vector
		// if the cookie store accidentally authenticated on stale state.
		req.Header.Del("Cookie")

		// Standard reverse-proxy hygiene: set X-Forwarded-* so the kernel logs the real
		// client IP instead of the portal container IP.
		if req.Header.Get("X-Forwarded-Proto") == "" {
			req.Header.Set("X-Forwarded-Proto", "https")
		}

		// IMPORTANT: Upgrade / Connection headers are preserved as-is so that WebSocket
		// upgrades pass through. httputil.ReverseProxy's default Director does this
		// automatically; we just need to not break it.
		//
		// The kernel's WebSocket HandleConnect reads the Authorization header directly
		// from the upgrade request (see kernel/server/serve.go, patched in §A.9).
	}
}

// modifyResponse is a small hook to let the portal rewrite kernel responses if needed.
// Currently it only strips the kernel's Set-Cookie for the "siyuan" session cookie so the
// kernel's vestigial session store never reaches the client.
func modifyResponse(resp *http.Response) error {
	cookies := resp.Header["Set-Cookie"]
	if len(cookies) == 0 {
		return nil
	}
	kept := cookies[:0]
	for _, c := range cookies {
		// Drop exactly the kernel's session cookie name. Any other Set-Cookie (e.g.
		// publish-auth-<id>) passes through untouched because it belongs to user-visible
		// features the portal does not intermediate.
		if !startsWithIgnoreCase(c, "siyuan=") {
			kept = append(kept, c)
		}
	}
	resp.Header["Set-Cookie"] = kept
	return nil
}

func startsWithIgnoreCase(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		a, b := s[i], prefix[i]
		if 'A' <= a && a <= 'Z' {
			a += 'a' - 'A'
		}
		if 'A' <= b && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}
