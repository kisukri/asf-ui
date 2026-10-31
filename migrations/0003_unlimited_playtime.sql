CREATE TABLE playtime_tasks_new (
    id               INTEGER PRIMARY KEY,
    user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    connection_id    INTEGER NOT NULL REFERENCES asf_connections(id) ON DELETE CASCADE,
    bot_id           INTEGER NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    app_id           INTEGER NOT NULL,
    app_name         TEXT,
    target_seconds   INTEGER NOT NULL CHECK (target_seconds >= 0),
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
INSERT INTO playtime_tasks_new SELECT * FROM playtime_tasks;
DROP TABLE playtime_tasks;
ALTER TABLE playtime_tasks_new RENAME TO playtime_tasks;
CREATE INDEX idx_tasks_user ON playtime_tasks(user_id, status);
CREATE INDEX idx_tasks_bot ON playtime_tasks(bot_id, status);

UPDATE user_prefs SET default_minutes = 0 WHERE default_minutes = 120;
