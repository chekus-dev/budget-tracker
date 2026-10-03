-- 003 — give each expense a category.
--
-- Empty string means uncategorised; the app displays those as
-- "Uncategorized" rather than storing that word.

ALTER TABLE expenses
    ADD COLUMN IF NOT EXISTS category VARCHAR(50) NOT NULL DEFAULT '';
