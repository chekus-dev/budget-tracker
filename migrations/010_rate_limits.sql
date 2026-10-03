-- Rename to match your migration numbering (e.g. 011_rate_limits.sql).
--
-- Shared counters for the login and password-reset rate limiters, so every
-- instance of the app sees the same failures. `bucket` names the limiter
-- (login-user, login-ip, reset-email, reset-ip); `key` is a SHA-256 of the
-- username, email or IP, so no readable personal data is stored here.
-- TIMESTAMPTZ, not TIMESTAMP, so the window means the same instant everywhere.
CREATE TABLE IF NOT EXISTS rate_limits (
    bucket   TEXT        NOT NULL,
    key      TEXT        NOT NULL,
    count    INTEGER     NOT NULL,
    reset_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (bucket, key)
);

-- Lets the once-a-minute sweep find expired windows without scanning the table.
CREATE INDEX IF NOT EXISTS rate_limits_reset_at_idx ON rate_limits (reset_at);
