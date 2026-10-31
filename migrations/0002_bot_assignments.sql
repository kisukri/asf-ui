CREATE TABLE bot_assignments (
    bot_id      INTEGER PRIMARY KEY REFERENCES bots(id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    assigned_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    assigned_at INTEGER NOT NULL
);
CREATE INDEX idx_bot_assignments_user ON bot_assignments(user_id);
