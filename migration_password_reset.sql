-- Adds email (required for password reset) and reset-token fields to users.
USE budget;

ALTER TABLE users
    ADD COLUMN email VARCHAR(255) NULL UNIQUE AFTER username,
    ADD COLUMN reset_token VARCHAR(64) NULL,
    ADD COLUMN reset_token_expires DATETIME NULL;

-- Existing users will have NULL email until they set one (see /account/set-email).
