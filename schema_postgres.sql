-- Budget Tracker — PostgreSQL schema
--
-- Fresh-install schema for the hosted PostgreSQL database. Run this once in the
-- Supabase SQL Editor (Project → SQL Editor → New query), or with psql:
--
--     psql "$DATABASE_URL" -f schema_postgres.sql
--
-- There is no CREATE DATABASE / USE statement here, unlike the old MySQL
-- schema.sql. PostgreSQL selects the database through the connection string,
-- and Supabase already provides the `postgres` database.

-- ============================================================
-- Users
-- ============================================================
CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    username VARCHAR(50) UNIQUE NOT NULL,
    email VARCHAR(255) UNIQUE,
    reset_token VARCHAR(64),
    reset_token_expires TIMESTAMP,
    session_version INTEGER NOT NULL DEFAULT 0,
    password_hash VARCHAR(255) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- Settings — one row per user.
--
-- user_id is the PRIMARY KEY, which is what the ON CONFLICT (user_id)
-- upsert in saveSettingsTx() depends on. Categories are stored as a
-- comma-separated string for simplicity, as before.
-- ============================================================
CREATE TABLE IF NOT EXISTS settings (
    user_id INTEGER PRIMARY KEY REFERENCES users(id),
    budget_limit NUMERIC(10,2) NOT NULL DEFAULT 0,
    currency VARCHAR(10) NOT NULL DEFAULT 'NGN',
    categories VARCHAR(500) NOT NULL DEFAULT 'Food,Transport,Bills'
);

-- ============================================================
-- Expenses
--
-- created_at is TIMESTAMP *without* time zone on purpose: the app treats
-- time as a naive wall clock (month strings like "2026-10", time.Now() in
-- the monthly-reset goroutine). This keeps an expense from drifting into
-- the wrong month bucket.
--
-- deleted_at implements the undo toast: a delete sets this instead of removing
-- the row, and every read filters it out. See migrations/006_add_soft_delete.sql
-- for the same change applied to a database that already has data.
-- ============================================================
CREATE TABLE IF NOT EXISTS expenses (
    id SERIAL PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id),
    description VARCHAR(255) NOT NULL,
    amount NUMERIC(10,2) NOT NULL,
    category VARCHAR(50) NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP
);

-- The app filters and groups expenses by month using
-- to_char(created_at, 'YYYY-MM'); this index covers that plus the
-- "newest first" ordering on the home page.
--
-- Partial, so it only holds live rows. That matches the deleted_at IS NULL
-- predicate every query now carries, and keeps the index from growing with
-- rows the user has already deleted.
CREATE INDEX IF NOT EXISTS expenses_live_user_created_idx
    ON expenses (user_id, created_at DESC)
    WHERE deleted_at IS NULL;
