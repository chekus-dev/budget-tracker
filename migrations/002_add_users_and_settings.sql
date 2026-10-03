-- 002 — users and settings; expenses become user-owned.
--
-- Settings keeps one row per user. user_id is the PRIMARY KEY because the
-- app's upsert in saveSettingsTx() does ON CONFLICT (user_id) DO UPDATE.

-- ============================================================
-- Users. Email and password-reset columns arrive in migration 004.
-- ============================================================
CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    username VARCHAR(50) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- Settings — one row per user, replacing the old in-memory global.
-- Categories are a comma-separated string for simplicity.
-- ============================================================
CREATE TABLE IF NOT EXISTS settings (
    user_id INTEGER PRIMARY KEY REFERENCES users(id),
    budget_limit NUMERIC(10,2) NOT NULL DEFAULT 0,
    currency VARCHAR(10) NOT NULL DEFAULT 'NGN',
    categories VARCHAR(500) NOT NULL DEFAULT 'Food,Transport,Bills',
    theme VARCHAR(10) NOT NULL DEFAULT 'system'
);

-- ============================================================
-- Attach expenses to users.
--
-- This adds a NOT NULL column with no default, which only works because
-- migration 001 created `expenses` empty and this sequence runs in order
-- against a fresh database. A table that already had rows would need a
-- backfill first (in MySQL this step used DEFAULT 1 to work around that).
-- ============================================================
ALTER TABLE expenses
    ADD COLUMN IF NOT EXISTS user_id INTEGER NOT NULL REFERENCES users(id);
