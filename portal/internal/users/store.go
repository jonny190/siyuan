// Package users owns the portal's SQLite-backed user/session/audit store.
//
// Concurrency model: a single *sql.DB is shared across the process. SQLite with WAL mode
// handles concurrent readers just fine; writes serialize inside the driver. The portal is
// low-traffic (human admin actions + login events) so we do not bother with a dedicated
// writer goroutine.
package users

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"
)

// schemaSQL is the idempotent portal schema. Kept inline so the users package is
// self-contained and does not depend on filesystem layout at runtime.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS users (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    username           TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash      TEXT NOT NULL,
    role               TEXT NOT NULL CHECK (role IN ('admin','user')),
    workspace_path     TEXT NOT NULL,
    kernel_container   TEXT NOT NULL,
    kernel_api_token   TEXT NOT NULL,
    kernel_auth_code   TEXT NOT NULL,
    kernel_status      TEXT NOT NULL DEFAULT 'stopped'
                         CHECK (kernel_status IN ('starting','running','stopping','stopped','failed')),
    disabled           INTEGER NOT NULL DEFAULT 0,
    created_at         INTEGER NOT NULL,
    last_login_at      INTEGER,
    last_active_at     INTEGER,
    pwd_changed_at     INTEGER NOT NULL,
    quota_bytes        INTEGER
);

CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at  INTEGER NOT NULL,
    csrf_token  TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    ip          TEXT,
    ua          TEXT
);

CREATE TABLE IF NOT EXISTS audit_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    at         INTEGER NOT NULL,
    actor_id   INTEGER REFERENCES users(id),
    action     TEXT NOT NULL,
    target     TEXT,
    ip         TEXT,
    detail     TEXT
);

CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_audit_at ON audit_log(at);
CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_log(actor_id);
`

const bcryptCost = 12

// Role is a coarse permission level. Admins see the admin UI and can create/delete users.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// KernelStatus is the lifecycle state the orchestrator tracks for each user's kernel container.
type KernelStatus string

const (
	StatusStarting KernelStatus = "starting"
	StatusRunning  KernelStatus = "running"
	StatusStopping KernelStatus = "stopping"
	StatusStopped  KernelStatus = "stopped"
	StatusFailed   KernelStatus = "failed"
)

// User is a row from the users table. Password hash is deliberately retained on the struct
// so the store can return the full row in one query; callers must never echo it back out.
type User struct {
	ID              int64
	Username        string
	PasswordHash    string
	Role            Role
	WorkspacePath   string
	KernelContainer string
	KernelAPIToken  string
	KernelAuthCode  string
	KernelStatus    KernelStatus
	Disabled        bool
	CreatedAt       time.Time
	LastLoginAt     *time.Time
	LastActiveAt    *time.Time
	PwdChangedAt    time.Time
	QuotaBytes      *int64
}

// Session is a row from the sessions table. ID is the random opaque token placed in the
// portal_session cookie.
type Session struct {
	ID        string
	UserID    int64
	ExpiresAt time.Time
	CSRFToken string
	CreatedAt time.Time
	IP        string
	UA        string
}

// Store is the concrete SQLite-backed user store.
type Store struct {
	db *sql.DB
}

// Open returns a ready-to-use Store backed by the SQLite file at path. The schema migration
// runs on first open and is idempotent on subsequent opens.
func Open(path string) (*Store, error) {
	// _journal=WAL: multi-reader friendly; _busy_timeout: avoids SQLITE_BUSY under load;
	// _foreign_keys=on: enforces the ON DELETE CASCADE on sessions.
	dsn := fmt.Sprintf("file:%s?_journal=WAL&_busy_timeout=5000&_foreign_keys=on", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open portal db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping portal db: %w", err)
	}
	// SQLite cannot benefit from a large connection pool; keep it tight.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close releases the underlying DB handle.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

// HashPassword bcrypt-hashes a plaintext password at the portal's chosen cost.
func HashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("bcrypt: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword returns nil if the plaintext matches the stored hash.
func VerifyPassword(hash, plain string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
}

// RandomToken returns a cryptographically-random hex string of the given byte length
// (resulting string is 2*n hex chars).
func RandomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// ErrUserNotFound is returned from GetByUsername / GetByID when no matching row exists.
var ErrUserNotFound = errors.New("user not found")

// ErrUsernameTaken is returned from Create when the username already exists (case-insensitive).
var ErrUsernameTaken = errors.New("username already taken")

// CreateUserArgs is the input to Create. Caller provides plaintext password; the store hashes it.
type CreateUserArgs struct {
	Username        string
	PasswordPlain   string
	Role            Role
	WorkspacePath   string
	KernelContainer string
	KernelAPIToken  string
	KernelAuthCode  string
}

// Create inserts a new user row. Returns the generated ID.
func (s *Store) Create(ctx context.Context, a CreateUserArgs) (int64, error) {
	hash, err := HashPassword(a.PasswordPlain)
	if err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `
        INSERT INTO users (
            username, password_hash, role, workspace_path,
            kernel_container, kernel_api_token, kernel_auth_code,
            kernel_status, disabled, created_at, pwd_changed_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, 'stopped', 0, ?, ?)
    `, a.Username, hash, string(a.Role), a.WorkspacePath,
		a.KernelContainer, a.KernelAPIToken, a.KernelAuthCode,
		now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrUsernameTaken
		}
		return 0, fmt.Errorf("insert user: %w", err)
	}
	return res.LastInsertId()
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

const userCols = `id, username, password_hash, role, workspace_path, kernel_container,
    kernel_api_token, kernel_auth_code, kernel_status, disabled, created_at,
    last_login_at, last_active_at, pwd_changed_at, quota_bytes`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var lastLogin, lastActive sql.NullInt64
	var quota sql.NullInt64
	var disabled int
	var createdAt, pwdChangedAt int64
	if err := row.Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.WorkspacePath,
		&u.KernelContainer, &u.KernelAPIToken, &u.KernelAuthCode, &u.KernelStatus,
		&disabled, &createdAt, &lastLogin, &lastActive, &pwdChangedAt, &quota,
	); err != nil {
		return nil, err
	}
	u.Disabled = disabled != 0
	u.CreatedAt = time.Unix(createdAt, 0)
	u.PwdChangedAt = time.Unix(pwdChangedAt, 0)
	if lastLogin.Valid {
		t := time.Unix(lastLogin.Int64, 0)
		u.LastLoginAt = &t
	}
	if lastActive.Valid {
		t := time.Unix(lastActive.Int64, 0)
		u.LastActiveAt = &t
	}
	if quota.Valid {
		u.QuotaBytes = &quota.Int64
	}
	return &u, nil
}

// GetByUsername looks up a user by case-insensitive username.
func (s *Store) GetByUsername(ctx context.Context, username string) (*User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ? COLLATE NOCASE`, username)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	return u, err
}

// GetByID looks up a user by primary key.
func (s *Store) GetByID(ctx context.Context, id int64) (*User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	return u, err
}

// List returns all users, admin rows first then by username.
func (s *Store) List(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT `+userCols+` FROM users
        ORDER BY CASE role WHEN 'admin' THEN 0 ELSE 1 END, username COLLATE NOCASE
    `)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Count returns the total number of users in the store. Used by bootstrap logic to decide
// whether to create the initial admin from env vars.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// ExecRaw is an escape hatch for the admin handler to run a parameterized UPDATE on the
// users table. It is NOT exported to the outside world because the portal service is the
// only consumer; we intentionally keep it narrow instead of growing a full update API.
func (s *Store) ExecRaw(ctx context.Context, query string, args ...any) (int64, error) {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UpdateKernelStatus writes the current orchestrator state. Called from the lifecycle goroutines.
func (s *Store) UpdateKernelStatus(ctx context.Context, userID int64, status KernelStatus) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET kernel_status = ? WHERE id = ?`,
		string(status), userID)
	return err
}

// TouchLastLogin marks a successful login for audit/idle-reaper purposes.
func (s *Store) TouchLastLogin(ctx context.Context, userID int64) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET last_login_at = ?, last_active_at = ? WHERE id = ?`,
		now, now, userID)
	return err
}

// TouchLastActive bumps only last_active_at. Called from the reverse-proxy middleware.
func (s *Store) TouchLastActive(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET last_active_at = ? WHERE id = ?`,
		time.Now().Unix(), userID)
	return err
}

// SetPassword updates the stored bcrypt hash and bumps pwd_changed_at. Caller is expected
// to also invalidate existing sessions via DeleteSessionsForUser.
func (s *Store) SetPassword(ctx context.Context, userID int64, newPlain string) error {
	hash, err := HashPassword(newPlain)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, pwd_changed_at = ? WHERE id = ?`,
		hash, time.Now().Unix(), userID)
	return err
}

// SetDisabled toggles the disabled flag. Disabled users cannot log in and their container
// will be stopped on the next orchestrator pass.
func (s *Store) SetDisabled(ctx context.Context, userID int64, disabled bool) error {
	v := 0
	if disabled {
		v = 1
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET disabled = ? WHERE id = ?`, v, userID)
	return err
}

// Delete removes a user row. Sessions cascade. Caller is responsible for archiving the
// workspace directory and stopping the container before calling this.
func (s *Store) Delete(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	return err
}

// --- sessions -----------------------------------------------------------------------------

// CreateSessionArgs is input to CreateSession.
type CreateSessionArgs struct {
	UserID   int64
	Duration time.Duration
	IP       string
	UA       string
}

// CreateSession inserts a new session row with a random ID and CSRF token.
func (s *Store) CreateSession(ctx context.Context, a CreateSessionArgs) (*Session, error) {
	id, err := RandomToken(32)
	if err != nil {
		return nil, err
	}
	csrf, err := RandomToken(16)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	exp := now.Add(a.Duration)
	_, err = s.db.ExecContext(ctx, `
        INSERT INTO sessions (id, user_id, expires_at, csrf_token, created_at, ip, ua)
        VALUES (?, ?, ?, ?, ?, ?, ?)
    `, id, a.UserID, exp.Unix(), csrf, now.Unix(), a.IP, a.UA)
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	return &Session{
		ID: id, UserID: a.UserID, ExpiresAt: exp, CSRFToken: csrf,
		CreatedAt: now, IP: a.IP, UA: a.UA,
	}, nil
}

// ErrSessionNotFound is returned from GetSession when no matching unexpired row exists.
var ErrSessionNotFound = errors.New("session not found or expired")

// GetSession returns the session and joined user, or ErrSessionNotFound.
func (s *Store) GetSession(ctx context.Context, id string) (*Session, *User, error) {
	row := s.db.QueryRowContext(ctx, `
        SELECT s.id, s.user_id, s.expires_at, s.csrf_token, s.created_at, s.ip, s.ua,
               `+prefixedUserCols("u")+`
        FROM sessions s
        JOIN users u ON u.id = s.user_id
        WHERE s.id = ? AND s.expires_at > ?
    `, id, time.Now().Unix())

	var sess Session
	var expiresAt, createdAt int64
	var u User
	var lastLogin, lastActive sql.NullInt64
	var quota sql.NullInt64
	var disabled int
	var userCreatedAt, pwdChangedAt int64
	if err := row.Scan(
		&sess.ID, &sess.UserID, &expiresAt, &sess.CSRFToken, &createdAt, &sess.IP, &sess.UA,
		&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.WorkspacePath,
		&u.KernelContainer, &u.KernelAPIToken, &u.KernelAuthCode, &u.KernelStatus,
		&disabled, &userCreatedAt, &lastLogin, &lastActive, &pwdChangedAt, &quota,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrSessionNotFound
		}
		return nil, nil, err
	}
	sess.ExpiresAt = time.Unix(expiresAt, 0)
	sess.CreatedAt = time.Unix(createdAt, 0)
	u.Disabled = disabled != 0
	u.CreatedAt = time.Unix(userCreatedAt, 0)
	u.PwdChangedAt = time.Unix(pwdChangedAt, 0)
	if lastLogin.Valid {
		t := time.Unix(lastLogin.Int64, 0)
		u.LastLoginAt = &t
	}
	if lastActive.Valid {
		t := time.Unix(lastActive.Int64, 0)
		u.LastActiveAt = &t
	}
	if quota.Valid {
		u.QuotaBytes = &quota.Int64
	}
	return &sess, &u, nil
}

// prefixedUserCols produces "u.id, u.username, ..." for JOIN selects.
func prefixedUserCols(alias string) string {
	cols := strings.Split(strings.ReplaceAll(userCols, "\n", " "), ",")
	for i, c := range cols {
		cols[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}

// DeleteSession removes a single session (logout).
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// DeleteSessionsForUser invalidates every session for a user (password reset, disable).
func (s *Store) DeleteSessionsForUser(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// PurgeExpiredSessions is called periodically by a janitor goroutine.
func (s *Store) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- audit log ----------------------------------------------------------------------------

// AuditEntry is one row of the audit_log table.
type AuditEntry struct {
	At      time.Time
	ActorID *int64
	Action  string
	Target  string
	IP      string
	Detail  map[string]any
}

// Audit appends an entry. Errors are logged but not returned — auditing must never block
// the caller's primary action.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	var detailJSON sql.NullString
	if len(e.Detail) > 0 {
		b, err := json.Marshal(e.Detail)
		if err == nil {
			detailJSON = sql.NullString{String: string(b), Valid: true}
		}
	}
	var actorID sql.NullInt64
	if e.ActorID != nil {
		actorID = sql.NullInt64{Int64: *e.ActorID, Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO audit_log (at, actor_id, action, target, ip, detail)
        VALUES (?, ?, ?, ?, ?, ?)
    `, time.Now().Unix(), actorID, e.Action, e.Target, e.IP, detailJSON)
	return err
}

// ListRecentAudit returns the most recent audit rows, newest first.
func (s *Store) ListRecentAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
        SELECT at, actor_id, action, target, ip, detail FROM audit_log
        ORDER BY at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at int64
		var actorID sql.NullInt64
		var target, ip sql.NullString
		var detail sql.NullString
		if err := rows.Scan(&at, &actorID, &e.Action, &target, &ip, &detail); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0)
		if actorID.Valid {
			v := actorID.Int64
			e.ActorID = &v
		}
		e.Target = target.String
		e.IP = ip.String
		if detail.Valid && detail.String != "" {
			_ = json.Unmarshal([]byte(detail.String), &e.Detail)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
