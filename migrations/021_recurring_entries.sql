-- Rules that create an expense or income entry automatically on each due date.
-- next_date is the next day an entry is due; anchor_day keeps "the 31st" meaning
-- the last day of shorter months instead of drifting to the 28th for good.
CREATE TABLE IF NOT EXISTS recurring_entries (
  id          BIGSERIAL PRIMARY KEY,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  description VARCHAR(255) NOT NULL,
  amount      NUMERIC(10,2) NOT NULL CHECK (amount > 0),
  category    VARCHAR(50) NOT NULL DEFAULT '',
  kind        VARCHAR(10) NOT NULL DEFAULT 'expense' CHECK (kind IN ('expense','income')),
  frequency   VARCHAR(10) NOT NULL CHECK (frequency IN ('daily','weekly','monthly')),
  anchor_day  SMALLINT NOT NULL CHECK (anchor_day BETWEEN 1 AND 31),
  next_date   DATE NOT NULL,
  active      BOOLEAN NOT NULL DEFAULT TRUE,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_recurring_due  ON recurring_entries (next_date) WHERE active;
CREATE INDEX IF NOT EXISTS idx_recurring_user ON recurring_entries (user_id);
