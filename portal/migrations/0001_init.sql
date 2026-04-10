-- Portal user/session/audit schema. Loaded once at first boot.

CREATE TABLE IF NOT EXISTS users (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    username           TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash      TEXT NOT NULL,                                -- bcrypt cost 12
    role               TEXT NOT NULL CHECK (role IN ('admin','user')),
    workspace_path     TEXT NOT NULL,                                -- /srv/siyuan/users/<id>/workspace
    kernel_container   TEXT NOT NULL,                                -- docker container name
    kernel_api_token   TEXT NOT NULL,                                -- 32-char hex; portal injects as Authorization: Token
    kernel_auth_code   TEXT NOT NULL,                                -- kernel Conf.AccessAuthCode (random, never exposed to client)
    kernel_status      TEXT NOT NULL DEFAULT 'stopped'
                         CHECK (kernel_status IN ('starting','running','stopping','stopped','failed')),
    disabled           INTEGER NOT NULL DEFAULT 0,
    created_at         INTEGER NOT NULL,
    last_login_at      INTEGER,
    last_active_at     INTEGER,
    pwd_changed_at     INTEGER NOT NULL,
    quota_bytes        INTEGER                                       -- nullable, per-user workspace cap
);

CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT PRIMARY KEY,                                    -- 32-byte random hex
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
    action     TEXT NOT NULL,                                        -- e.g. 'user.create','login.success','login.fail'
    target     TEXT,
    ip         TEXT,
    detail     TEXT                                                  -- JSON blob
);

CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_audit_at ON audit_log(at);
CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_log(actor_id);
