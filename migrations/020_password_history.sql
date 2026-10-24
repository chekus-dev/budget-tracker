-- Remembers each user's last five previous password hashes so a reset or
-- change can reject a reused password. A trigger fills it, so no handler has to
-- remember to: whenever users.password_hash changes, the old hash is archived
-- and anything older than the newest five is pruned.
CREATE TABLE IF NOT EXISTS password_history (
  id            BIGSERIAL PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  password_hash TEXT NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_password_history_user ON password_history (user_id, id DESC);

CREATE OR REPLACE FUNCTION archive_password() RETURNS trigger AS $$
BEGIN
  IF NEW.password_hash IS DISTINCT FROM OLD.password_hash THEN
    INSERT INTO password_history (user_id, password_hash) VALUES (OLD.id, OLD.password_hash);
    DELETE FROM password_history
     WHERE user_id = OLD.id
       AND id NOT IN (SELECT id FROM password_history WHERE user_id = OLD.id ORDER BY id DESC LIMIT 5);
  END IF;
  RETURN NEW;
END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_archive_password ON users;
CREATE TRIGGER trg_archive_password
  BEFORE UPDATE OF password_hash ON users
  FOR EACH ROW EXECUTE FUNCTION archive_password();
