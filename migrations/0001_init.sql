CREATE TABLE users (
    id                  INTEGER PRIMARY KEY,
    username            TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash       TEXT NOT NULL,
    is_admin            INTEGER NOT NULL DEFAULT 0,
    is_active           INTEGER NOT NULL DEFAULT 1,
    invite_id           INTEGER REFERENCES invites(id) ON DELETE SET NULL,
    created_at          INTEGER NOT NULL,
    password_changed_at INTEGER NOT NULL,
    last_login_at       INTEGER
);

CREATE TABLE invites (
    id          INTEGER PRIMARY KEY,
    code_hash   TEXT NOT NULL UNIQUE,
    code_hint   TEXT NOT NULL,
    note        TEXT,
    max_uses    INTEGER NOT NULL CHECK (max_uses >= 1),
    uses        INTEGER NOT NULL DEFAULT 0 CHECK (uses >= 0),
    expires_at  INTEGER NOT NULL,
    revoked_at  INTEGER,
    created_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at  INTEGER NOT NULL,
    CHECK (uses <= max_uses)
);

CREATE TABLE invite_uses (
    id         INTEGER PRIMARY KEY,
    invite_id  INTEGER NOT NULL REFERENCES invites(id) ON DELETE CASCADE,
    user_id    INTEGER REFERENCES users(id) ON DELETE SET NULL,
    used_at    INTEGER NOT NULL
);

CREATE TABLE sessions (
    id           INTEGER PRIMARY KEY,
    token_hash   TEXT NOT NULL UNIQUE,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token   TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    ip           TEXT,
    user_agent   TEXT
);
CREATE INDEX idx_sessions_user ON sessions(user_id);

CREATE TABLE rate_limits (
    bucket       TEXT NOT NULL,
    key          TEXT NOT NULL,
    window_start INTEGER NOT NULL,
    count        INTEGER NOT NULL,
    PRIMARY KEY (bucket, key)
);

CREATE TABLE asf_connections (
    id              INTEGER PRIMARY KEY,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    url_enc         TEXT NOT NULL,
    url_hint        TEXT NOT NULL,
    url_fingerprint TEXT NOT NULL,
    password_enc    TEXT,
    enabled         INTEGER NOT NULL DEFAULT 1,
    status          TEXT NOT NULL DEFAULT 'untested',
    status_message  TEXT,
    asf_version     TEXT,
    last_tested_at  INTEGER,
    last_ok_at      INTEGER,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    UNIQUE (user_id, name)
);
CREATE INDEX idx_connections_user ON asf_connections(user_id);
CREATE INDEX idx_connections_fp ON asf_connections(url_fingerprint);

CREATE TABLE bots (
    id            INTEGER PRIMARY KEY,
    connection_id INTEGER NOT NULL REFERENCES asf_connections(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    present       INTEGER NOT NULL DEFAULT 1,
    last_seen_at  INTEGER NOT NULL,
    skip_count      INTEGER,
    skip_checked_at INTEGER,
    UNIQUE (connection_id, name)
);

CREATE TABLE playtime_tasks (
    id               INTEGER PRIMARY KEY,
    user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    connection_id    INTEGER NOT NULL REFERENCES asf_connections(id) ON DELETE CASCADE,
    bot_id           INTEGER NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    app_id           INTEGER NOT NULL,
    app_name         TEXT,
    target_seconds   INTEGER NOT NULL CHECK (target_seconds > 0),
    elapsed_seconds  REAL NOT NULL DEFAULT 0,
    status           TEXT NOT NULL,
    note             TEXT,
    request_key      TEXT NOT NULL,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    started_at       INTEGER,
    finished_at      INTEGER,
    last_accrued_at  INTEGER,
    stop_reason      TEXT,
    last_error       TEXT,
    UNIQUE (user_id, request_key)
);
CREATE INDEX idx_tasks_user ON playtime_tasks(user_id, status);
CREATE INDEX idx_tasks_bot ON playtime_tasks(bot_id, status);

CREATE TABLE bot_play_state (
    bot_id                INTEGER PRIMARY KEY REFERENCES bots(id) ON DELETE CASCADE,
    managed               INTEGER NOT NULL DEFAULT 0,
    prev_farming_paused   INTEGER NOT NULL DEFAULT 0,
    applied_appids        TEXT NOT NULL DEFAULT '',
    applied_at            INTEGER,
    confirmed             INTEGER NOT NULL DEFAULT 0,
    last_online           INTEGER NOT NULL DEFAULT 0,
    last_playing_possible INTEGER NOT NULL DEFAULT 0,
    failing_since         INTEGER,
    last_error            TEXT,
    updated_at            INTEGER NOT NULL
);

CREATE TABLE user_prefs (
    user_id               INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    default_connection_id INTEGER REFERENCES asf_connections(id) ON DELETE SET NULL,
    default_minutes       INTEGER NOT NULL DEFAULT 120
);

CREATE TABLE audit_log (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER REFERENCES users(id) ON DELETE SET NULL,
    ip         TEXT,
    action     TEXT NOT NULL,
    category   TEXT NOT NULL,
    target     TEXT,
    outcome    TEXT NOT NULL,
    detail     TEXT,
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_audit_user ON audit_log(user_id, created_at);
CREATE INDEX idx_audit_cat ON audit_log(category, created_at);

CREATE TABLE scheduler_state (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    holder        TEXT,
    lease_until   INTEGER NOT NULL DEFAULT 0,
    last_tick_at  INTEGER,
    last_tick_ms  INTEGER,
    last_error    TEXT,
    ticks         INTEGER NOT NULL DEFAULT 0
);
INSERT INTO scheduler_state (id) VALUES (1);

CREATE TABLE app_names (
    app_id     INTEGER PRIMARY KEY,
    name       TEXT,
    fetched_at INTEGER NOT NULL
);
