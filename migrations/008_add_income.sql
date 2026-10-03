-- 008_add_income.sql
--
-- Every entry gains a kind: 'expense' (money out) or 'income' (money in).
--
-- Why a column on the existing table rather than a second table: an income
-- entry needs everything an expense entry already has — a date, an amount, a
-- description, a category, soft delete, undo, search, the archive, the
-- exports. A separate table would duplicate every one of those code paths,
-- and two copies of a path drift apart.
--
-- The cost of that choice is real and worth naming: every aggregate that means
-- "money spent" now has to say so explicitly, and forgetting one would quietly
-- count income as spending. Three things hold that line. The DEFAULT below
-- means every existing row is already correct the moment the column lands. The
-- CHECK means a typo cannot invent a third kind. And the reads are all in one
-- file, where a missing filter is visible next to its neighbours.
--
-- The inline CHECK is deliberate: ALTER TABLE ... ADD COLUMN IF NOT EXISTS
-- skips the whole statement when the column is already there, constraint
-- included, so this stays re-runnable without a DO block.
--
-- 'expense' as the default rather than NULL: the column is NOT NULL, so a
-- caller that forgets to set it creates an expense, which is the safe failure.
-- The old MySQL schema used DEFAULT 1 for user_id for the same reason.

ALTER TABLE expenses
    ADD COLUMN IF NOT EXISTS kind VARCHAR(10) NOT NULL DEFAULT 'expense'
        CHECK (kind IN ('expense', 'income'));

-- The home screen and the report both ask "this user's spending in this
-- window", now with a kind in front of it. The existing index on
-- (user_id, created_at) can no longer serve that as tightly.
CREATE INDEX IF NOT EXISTS idx_expenses_user_kind_date
    ON expenses (user_id, kind, created_at DESC);
