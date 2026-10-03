-- 006_add_soft_delete.sql
--
-- Deleting an expense now hides it instead of removing the row, so the undo
-- toast has something to bring back. Every query that reads expenses must
-- therefore filter on deleted_at IS NULL; the partial index below matches that
-- filter so the added predicate costs nothing at read time.
--
-- Rows are really deleted later: clearHandler (a confirmed month reset) and the
-- nightly purge in autoMonthlyReset.
--
-- Safe to run more than once.

ALTER TABLE expenses ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMP;

-- Partial index: only live rows are indexed, which keeps it small and lets the
-- planner use it for the common "this user, this month" lookups.
CREATE INDEX IF NOT EXISTS expenses_live_user_created_idx
    ON expenses (user_id, created_at DESC)
    WHERE deleted_at IS NULL;

-- Migration 005's full index is now redundant: it covers the same
-- (user_id, created_at DESC) lookups, and every query in the app filters to
-- live rows, which the partial index above serves at least as well. Keeping
-- both would maintain two overlapping indexes on every insert for no benefit.
-- Order matters — the partial index is created first, so drop second.
DROP INDEX IF EXISTS expenses_user_created_idx;
