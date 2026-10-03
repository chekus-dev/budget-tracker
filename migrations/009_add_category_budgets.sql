-- 009_add_category_budgets.sql
--
-- A per-category spending limit: "₦40,000 on Food, ₦15,000 on Transport".
--
-- Why a table rather than another joined string on settings: settings already
-- stores categories as a comma-separated string, and that works because a
-- category list is read and written whole. A category *and its limit* is a
-- pair, and packing pairs into a string means inventing a separator — which
-- only works until a category name contains one. The settings form already
-- has to strip commas out of category names for exactly that reason. A table
-- has no separator to escape.
--
-- The key is (user_id, category) with no surrogate id, so a limit is
-- identified by what it means rather than by a number the app has to track.
-- That also makes the save path a plain upsert, and makes "remove the limit
-- for Food" the same operation as "set Food's limit to zero".
--
-- No foreign key to a categories table, because there isn't one: categories
-- live as a string on settings and as text on each expense. A limit for a
-- category that has since been deleted therefore survives — deliberately. Its
-- expenses still carry that category, and the home screen would otherwise
-- lose the bar for spending that is still happening.

CREATE TABLE IF NOT EXISTS category_budgets (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category VARCHAR(50) NOT NULL,
    limit_amount NUMERIC(10,2) NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, category)
);
