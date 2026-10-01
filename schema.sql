CREATE DATABASE IF NOT EXISTS budget;
USE budget;

-- ============================================================
-- Base table (already existed)
-- ============================================================
CREATE TABLE IF NOT EXISTS expenses (
    id INT AUTO_INCREMENT PRIMARY KEY,
    description VARCHAR(255) NOT NULL,
    amount DECIMAL(10,2) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- Users (new)
-- ============================================================
CREATE TABLE IF NOT EXISTS users (
    id INT AUTO_INCREMENT PRIMARY KEY,
    username VARCHAR(50) UNIQUE NOT NULL,
    email VARCHAR(255) NULL UNIQUE,
    reset_token VARCHAR(64) NULL,
    reset_token_expires DATETIME NULL,
    password_hash VARCHAR(255) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- Settings (new) — one row per user, replaces the old in-memory
-- currentSettings global. Categories are stored as a comma-
-- separated string for simplicity.
-- ============================================================
CREATE TABLE IF NOT EXISTS settings (
    user_id INT PRIMARY KEY,
    budget_limit DECIMAL(10,2) NOT NULL DEFAULT 0,
    currency VARCHAR(10) NOT NULL DEFAULT 'NGN',
    categories VARCHAR(500) NOT NULL DEFAULT 'Food,Transport,Bills',
    theme VARCHAR(10) NOT NULL DEFAULT 'system',
    FOREIGN KEY (user_id) REFERENCES users(id)
);

-- ============================================================
-- Migrating existing expenses to be user-owned
-- ============================================================
-- If you already have rows in `expenses` from before multi-user
-- support, the FK below will fail until every existing row has a
-- valid user_id. Two options:
--
-- Option A — keep the old data, attach it to your own account:
--   1. Register your account through the app first (POST /register),
--      note the id MySQL assigned it, e.g. SELECT id FROM users WHERE username = 'you';
--   2. Then run the ALTER TABLE below with that id as the DEFAULT,
--      e.g. ...DEFAULT 3... instead of DEFAULT 1.
--
-- Option B — starting fresh / don't care about old rows:
--   TRUNCATE TABLE expenses;
--   (then the DEFAULT value below is irrelevant)

ALTER TABLE expenses ADD COLUMN user_id INT NOT NULL DEFAULT 1;
ALTER TABLE expenses ADD FOREIGN KEY (user_id) REFERENCES users(id);
