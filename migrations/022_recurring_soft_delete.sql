-- Deleting a recurring entry hides it for 30 days so the Undo toast can bring it back.
ALTER TABLE recurring_entries ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
DROP INDEX IF EXISTS idx_recurring_due;
CREATE INDEX IF NOT EXISTS idx_recurring_due ON recurring_entries (next_date) WHERE active AND deleted_at IS NULL;
