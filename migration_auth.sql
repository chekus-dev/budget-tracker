-- Run this once against your existing `budget` database.
-- Matches the queries in main.go exactly (users, settings, expenses.user_id).

USE budget;

-- 1. Users table
CREATE TABLE IF NOT EXISTS users (
    id INT AUTO_INCREMENT PRIMARY KEY,
    username VARCHAR(50) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- 2. Add user_id to expenses (existing rows get user_id = 1, so create that
--    user first if you already have expenses you want to keep — see note below).
ALTER TABLE expenses ADD COLUMN user_id INT NOT NULL DEFAULT 1;
ALTER TABLE expenses ADD FOREIGN KEY (user_id) REFERENCES users(id);

-- 3. Settings table — one row per user. user_id is the PRIMARY KEY so
--    `ON DUPLICATE KEY UPDATE` in saveSettingsTx works as an upsert.
CREATE TABLE IF NOT EXISTS settings (
    user_id INT PRIMARY KEY,
    budget_limit DECIMAL(10,2) NOT NULL DEFAULT 0,
    currency VARCHAR(10) NOT NULL DEFAULT 'NGN',
    categories TEXT,
    FOREIGN KEY (user_id) REFERENCES users(id)
);

-- Note: if you already had expenses in the table before adding user_id,
-- step 2's ALTER will fail on the foreign key until a user with id = 1
-- exists. Either:
--   a) register your first account through the app BEFORE running this
--      migration's ALTER statements (so id = 1 already exists), or
--   b) run this INSERT first to create a placeholder id = 1 user, then
--      register normally afterwards and reassign expenses if needed:
--
--      INSERT INTO users (id, username, password_hash) VALUES (1, 'legacy', '');
