-- Admin account management, audit history, and user messaging.
--
-- Admin access is granted to the comma-separated user IDs in ADMIN_USER_IDS.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMP,
    ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMP,
    ADD COLUMN IF NOT EXISTS admin_note TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS admin_actions (
    id         SERIAL PRIMARY KEY,
    admin_id   INTEGER NOT NULL,
    target_id  INTEGER NOT NULL,
    action     VARCHAR(50) NOT NULL,
    note       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS admin_actions_target_created_idx
    ON admin_actions (target_id, created_at DESC);
CREATE INDEX IF NOT EXISTS admin_actions_created_idx
    ON admin_actions (created_at DESC);

CREATE TABLE IF NOT EXISTS admin_notes (
    id         SERIAL PRIMARY KEY,
    target_id  INTEGER NOT NULL,
    admin_id   INTEGER NOT NULL,
    body       TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS admin_notes_target_created_idx
    ON admin_notes (target_id, created_at DESC);

CREATE TABLE IF NOT EXISTS user_messages (
    id         SERIAL PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    admin_id   INTEGER NOT NULL,
    subject    VARCHAR(200) NOT NULL DEFAULT '',
    body       TEXT NOT NULL,
    is_system  BOOLEAN NOT NULL DEFAULT FALSE,
    read_at    TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS user_messages_user_created_idx
    ON user_messages (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS user_messages_unread_idx
    ON user_messages (user_id)
    WHERE read_at IS NULL;
