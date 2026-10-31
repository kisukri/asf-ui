ALTER TABLE bot_play_state ADD COLUMN mode TEXT NOT NULL DEFAULT 'play';
ALTER TABLE bot_play_state ADD COLUMN wait_reason TEXT;
