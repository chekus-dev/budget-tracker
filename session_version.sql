-- Equivalent to migrations/011_session_version.sql for manual application.
--
-- Bumped on every password change/reset; a session cookie carrying an older
-- value is rejected by requireAuth. Existing rows start at 0, which matches
-- sessions issued before this change, so nobody is signed out on deploy.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS session_version INTEGER NOT NULL DEFAULT 0;
