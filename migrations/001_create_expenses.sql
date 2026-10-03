-- 001 — expenses: the original table.
--
-- This is where the project started: a single table, no users, no categories.
-- Apply with:
--     psql "$DATABASE_URL" -f migrations/001_create_expenses.sql

CREATE TABLE IF NOT EXISTS expenses (
    id SERIAL PRIMARY KEY,
    description VARCHAR(255) NOT NULL,
    amount NUMERIC(10,2) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
