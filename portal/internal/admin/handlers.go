// Package admin exposes the portal's login, logout, and admin-UI HTTP handlers.
//
// Admin actions (create/delete/reset/disable users) check session.user.role == "admin"
// and a matching CSRF token from the form. The HTML pages are rendered with stdlib
// html/template from internal/web/templates.
package admin

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/siyuan-note/siyuan/portal/internal/orchestrator"
	"github.com/siyuan-note/siyuan/portal/internal/proxy"
	"github.com/siyuan-note/siyuan/portal/internal/session"
	"github.com/siyuan-note/siyuan/portal/internal/users"
)

//go:embed templates/*.html
var templateFS embed.FS

// Handlers groups the admin/login HTTP handlers with their dependencies.
type Handlers struct {
	Store     *users.Store
	Orch      *orchestrator.Orchestrator
	RateLimit *session.RateLimiter

	templates *template.Template
}

// New wires the Handlers with pre-parsed templates. Returns an error if templates can't
// be loaded — the portal should refuse to start in that case.
func New(store *users.Store, orch *orchestrator.Orchestrator, rl *session.RateLimiter) (*Handlers, error) {
	tmpl, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Handlers{Store: store, Orch: orch, RateLimit: rl, templates: tmpl}, nil
}

// render executes a single template, writing an HTML 500 on error.
func (h *Handlers) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("portal: render %s: %v", name, err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// --- login / logout ------------------------------------------------------------------------

// Login handles GET /login (show form) and POST /login (verify credentials).
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.render(w, "login.html", map[string]any{
			"Next":  r.URL.Query().Get("next"),
			"Error": r.URL.Query().Get("error"),
		})
	case http.MethodPost:
		h.handleLogin(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handlers) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := session.ClientIP(r)
	if !h.RateLimit.Allow(ip) {
		http.Redirect(w, r, "/login?error=rate", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	next := r.FormValue("next")
	if username == "" || password == "" {
		http.Redirect(w, r, "/login?error=creds", http.StatusSeeOther)
		return
	}

	user, err := h.Store.GetByUsername(r.Context(), username)
	if err != nil || user == nil {
		// Generic "invalid credentials" error to avoid username enumeration.
		h.auditFailedLogin(r.Context(), ip, username)
		http.Redirect(w, r, "/login?error=creds", http.StatusSeeOther)
		return
	}
	if user.Disabled {
		h.auditFailedLogin(r.Context(), ip, username)
		http.Redirect(w, r, "/login?error=disabled", http.StatusSeeOther)
		return
	}
	if err := users.VerifyPassword(user.PasswordHash, password); err != nil {
		h.auditFailedLogin(r.Context(), ip, username)
		http.Redirect(w, r, "/login?error=creds", http.StatusSeeOther)
		return
	}

	// Success: mint a session, set the cookie, redirect.
	sess, err := h.Store.CreateSession(r.Context(), users.CreateSessionArgs{
		UserID:   user.ID,
		Duration: session.DefaultDuration,
		IP:       ip,
		UA:       r.UserAgent(),
	})
	if err != nil {
		log.Printf("portal: create session: %v", err)
		http.Error(w, "login failed", http.StatusInternalServerError)
		return
	}
	_ = h.Store.TouchLastLogin(r.Context(), user.ID)
	actorID := user.ID
	_ = h.Store.Audit(r.Context(), users.AuditEntry{
		ActorID: &actorID, Action: "login.success", Target: user.Username, IP: ip,
	})

	session.SetSessionCookie(w, r, sess.ID, session.DefaultDuration)

	// Redirect target: ?next= if it's a safe local path; "/" otherwise.
	dest := "/"
	if next != "" && len(next) < 256 && next[0] == '/' && (len(next) < 2 || next[1] != '/') {
		dest = next
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (h *Handlers) auditFailedLogin(ctx context.Context, ip, username string) {
	_ = h.Store.Audit(ctx, users.AuditEntry{
		Action: "login.fail", Target: username, IP: ip,
	})
}

// Logout handles POST /logout. Deletes the session row and clears the cookie.
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if cookie, err := r.Cookie(session.CookieName); err == nil && cookie.Value != "" {
		_ = h.Store.DeleteSession(r.Context(), cookie.Value)
	}
	session.ClearSessionCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// --- admin page ----------------------------------------------------------------------------

// usernameRE restricts usernames to a safe filesystem- and container-name-friendly alphabet.
var usernameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// AdminPage handles GET /admin (render the user list) and POST /admin (process a single
// admin action). All admin routes require the session user to be a role=admin.
func (h *Handlers) AdminPage(w http.ResponseWriter, r *http.Request) {
	actor := proxy.UserFromContext(r.Context())
	if actor == nil || actor.Role != users.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if r.Method == http.MethodPost {
		h.handleAdminAction(w, r, actor)
		return
	}

	all, err := h.Store.List(r.Context())
	if err != nil {
		log.Printf("portal: list users: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	audit, _ := h.Store.ListRecentAudit(r.Context(), 50)

	// Surface the session's CSRF token via a cookie so the admin form can echo it back.
	// Rendering the token directly in the template is fine too; we do both for clarity.
	csrf := h.csrfFor(r)
	h.render(w, "admin.html", map[string]any{
		"Actor": actor,
		"Users": all,
		"Audit": audit,
		"CSRF":  csrf,
		"Flash": r.URL.Query().Get("flash"),
		"Error": r.URL.Query().Get("error"),
	})
}

// csrfFor returns the CSRF token attached to the current session row. If the session is
// missing for some reason (shouldn't happen after middleware) we return an empty string
// and the admin form will fail the check.
func (h *Handlers) csrfFor(r *http.Request) string {
	cookie, err := r.Cookie(session.CookieName)
	if err != nil {
		return ""
	}
	sess, _, err := h.Store.GetSession(r.Context(), cookie.Value)
	if err != nil {
		return ""
	}
	return sess.CSRFToken
}

// handleAdminAction dispatches POST /admin based on the "action" form field. Each action
// validates its CSRF token against the session.
func (h *Handlers) handleAdminAction(w http.ResponseWriter, r *http.Request, actor *users.User) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !h.checkCSRF(r) {
		http.Error(w, "csrf mismatch", http.StatusForbidden)
		return
	}

	action := r.FormValue("action")
	switch action {
	case "create":
		h.adminCreateUser(w, r, actor)
	case "delete":
		h.adminDeleteUser(w, r, actor)
	case "disable":
		h.adminToggleDisabled(w, r, actor, true)
	case "enable":
		h.adminToggleDisabled(w, r, actor, false)
	case "reset_password":
		h.adminResetPassword(w, r, actor)
	default:
		http.Redirect(w, r, "/admin?error=unknown_action", http.StatusSeeOther)
	}
}

func (h *Handlers) checkCSRF(r *http.Request) bool {
	supplied := r.FormValue("csrf")
	if supplied == "" {
		return false
	}
	expected := h.csrfFor(r)
	return expected != "" && supplied == expected
}

func (h *Handlers) adminCreateUser(w http.ResponseWriter, r *http.Request, actor *users.User) {
	username := r.FormValue("username")
	password := r.FormValue("password")
	role := users.Role(r.FormValue("role"))
	if role != users.RoleAdmin && role != users.RoleUser {
		role = users.RoleUser
	}
	if !usernameRE.MatchString(username) {
		http.Redirect(w, r, "/admin?error=username_invalid", http.StatusSeeOther)
		return
	}
	if len(password) < 8 {
		http.Redirect(w, r, "/admin?error=password_short", http.StatusSeeOther)
		return
	}

	// Generate the per-user tokens BEFORE inserting so we can roll back cleanly if
	// container provisioning fails.
	apiToken, err := users.RandomToken(32)
	if err != nil {
		http.Redirect(w, r, "/admin?error=random", http.StatusSeeOther)
		return
	}
	authCode, err := users.RandomToken(32)
	if err != nil {
		http.Redirect(w, r, "/admin?error=random", http.StatusSeeOther)
		return
	}

	// We don't know the user ID yet because we haven't inserted, so we pass placeholder
	// paths and rewrite them once we have the ID.
	id, err := h.Store.Create(r.Context(), users.CreateUserArgs{
		Username:        username,
		PasswordPlain:   password,
		Role:            role,
		WorkspacePath:   "",
		KernelContainer: "",
		KernelAPIToken:  apiToken,
		KernelAuthCode:  authCode,
	})
	if err != nil {
		if errors.Is(err, users.ErrUsernameTaken) {
			http.Redirect(w, r, "/admin?error=username_taken", http.StatusSeeOther)
			return
		}
		log.Printf("portal: create user: %v", err)
		http.Redirect(w, r, "/admin?error=db", http.StatusSeeOther)
		return
	}

	// Fill in the ID-derived fields now that we have it.
	user, err := h.Store.GetByID(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, "/admin?error=db", http.StatusSeeOther)
		return
	}
	user.WorkspacePath = h.Orch.WorkspacePath(id)
	user.KernelContainer = h.Orch.ContainerName(id)
	if err := h.updateDerivedFields(r.Context(), user); err != nil {
		log.Printf("portal: update derived fields: %v", err)
		http.Redirect(w, r, "/admin?error=db", http.StatusSeeOther)
		return
	}

	// Provision the workspace directory on the host. This also seeds
	// <workspace>/conf/conf.json with the per-user tokens so the kernel accepts
	// the portal's Authorization: Token header on first boot. The kernel
	// container itself is created lazily on first login.
	if err := h.Orch.Provision(user); err != nil {
		log.Printf("portal: provision user %d: %v", id, err)
		http.Redirect(w, r, "/admin?error=provision", http.StatusSeeOther)
		return
	}

	actorID := actor.ID
	_ = h.Store.Audit(r.Context(), users.AuditEntry{
		ActorID: &actorID, Action: "user.create", Target: username, IP: session.ClientIP(r),
	})
	http.Redirect(w, r, "/admin?flash=created", http.StatusSeeOther)
}

// updateDerivedFields stores the workspace_path and kernel_container on the user row.
// We don't have a general-purpose update method on the store; use a direct UPDATE here
// to keep the store's API surface small.
func (h *Handlers) updateDerivedFields(ctx context.Context, user *users.User) error {
	_, err := h.Store.ExecRaw(ctx,
		`UPDATE users SET workspace_path = ?, kernel_container = ? WHERE id = ?`,
		user.WorkspacePath, user.KernelContainer, user.ID)
	return err
}

func (h *Handlers) adminDeleteUser(w http.ResponseWriter, r *http.Request, actor *users.User) {
	id, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin?error=bad_id", http.StatusSeeOther)
		return
	}
	if id == actor.ID {
		http.Redirect(w, r, "/admin?error=no_self_delete", http.StatusSeeOther)
		return
	}
	target, err := h.Store.GetByID(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, "/admin?error=not_found", http.StatusSeeOther)
		return
	}

	// Stop + delete the container, then delete the row. Workspace archival is handled
	// out of band: the file tree is simply left on disk for the operator to archive.
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_ = h.Orch.Stop(stopCtx, target)
	_ = h.Orch.Delete(stopCtx, target)
	cancel()
	_ = h.Store.DeleteSessionsForUser(r.Context(), id)
	_ = h.Store.Delete(r.Context(), id)

	actorID := actor.ID
	_ = h.Store.Audit(r.Context(), users.AuditEntry{
		ActorID: &actorID, Action: "user.delete", Target: target.Username, IP: session.ClientIP(r),
	})
	http.Redirect(w, r, "/admin?flash=deleted", http.StatusSeeOther)
}

func (h *Handlers) adminToggleDisabled(w http.ResponseWriter, r *http.Request, actor *users.User, disable bool) {
	id, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin?error=bad_id", http.StatusSeeOther)
		return
	}
	if id == actor.ID && disable {
		http.Redirect(w, r, "/admin?error=no_self_disable", http.StatusSeeOther)
		return
	}
	target, err := h.Store.GetByID(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, "/admin?error=not_found", http.StatusSeeOther)
		return
	}
	if err := h.Store.SetDisabled(r.Context(), id, disable); err != nil {
		http.Redirect(w, r, "/admin?error=db", http.StatusSeeOther)
		return
	}
	if disable {
		// Also invalidate sessions and stop the container.
		_ = h.Store.DeleteSessionsForUser(r.Context(), id)
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_ = h.Orch.Stop(stopCtx, target)
		cancel()
	}
	action := "user.enable"
	if disable {
		action = "user.disable"
	}
	actorID := actor.ID
	_ = h.Store.Audit(r.Context(), users.AuditEntry{
		ActorID: &actorID, Action: action, Target: target.Username, IP: session.ClientIP(r),
	})
	http.Redirect(w, r, "/admin?flash="+action, http.StatusSeeOther)
}

func (h *Handlers) adminResetPassword(w http.ResponseWriter, r *http.Request, actor *users.User) {
	id, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin?error=bad_id", http.StatusSeeOther)
		return
	}
	newPassword := r.FormValue("new_password")
	if len(newPassword) < 8 {
		http.Redirect(w, r, "/admin?error=password_short", http.StatusSeeOther)
		return
	}
	target, err := h.Store.GetByID(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, "/admin?error=not_found", http.StatusSeeOther)
		return
	}
	if err := h.Store.SetPassword(r.Context(), id, newPassword); err != nil {
		http.Redirect(w, r, "/admin?error=db", http.StatusSeeOther)
		return
	}
	_ = h.Store.DeleteSessionsForUser(r.Context(), id)

	actorID := actor.ID
	_ = h.Store.Audit(r.Context(), users.AuditEntry{
		ActorID: &actorID, Action: "user.reset_password", Target: target.Username, IP: session.ClientIP(r),
	})
	http.Redirect(w, r, "/admin?flash=reset", http.StatusSeeOther)
}
