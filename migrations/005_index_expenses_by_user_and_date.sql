-- 005 — index expenses for the queries the app actually runs.
--
-- Almost every read filters to one user and groups by month via
-- to_char(created_at, 'YYYY-MM'), then orders newest-first. This index
-- serves all of those.

CREATE INDEX IF NOT EXISTS expenses_user_created_idx
    ON expenses (user_id, created_at DESC);
