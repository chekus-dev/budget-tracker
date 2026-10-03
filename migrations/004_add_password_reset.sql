-- 004 — email address and password-reset tokens.
--
-- Email is required by the forgot-password flow: it is the address the reset
-- link is sent to. The token columns hold one in-flight reset request per
-- user (the token is single-use and cleared on success).
--
-- Both token columns are nullable on purpose — a user who has never
-- requested a reset has neither.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS email VARCHAR(255) UNIQUE,
    ADD COLUMN IF NOT EXISTS reset_token VARCHAR(64),
    ADD COLUMN IF NOT EXISTS reset_token_expires TIMESTAMP;
