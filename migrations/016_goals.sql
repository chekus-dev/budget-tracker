-- 016_goals.sql
--
-- A goal is a commitment: something to do, or something to set aside, by a
-- specific month or year. It is deliberately NOT a savings balance with a
-- running total — that was a different shape and it answers a narrower
-- question. This one answers "what did I say I would do, and did I do it?"
--
-- scope tells the app how to read period:
--   scope='month', period='2026-10'  — a goal for October 2026
--   scope='year',  period='2026'     — a goal for the whole of 2026
--
-- target_amount is nullable on purpose: "save ₦500,000 for a car" has an
-- amount, "read 12 books" does not, and both are equally valid goals. NULL
-- means no amount was given, which is not the same as an amount of zero.
--
-- ON DELETE CASCADE: if the account is deleted, its goals go with it.
--
-- Safe to run more than once.

CREATE TABLE IF NOT EXISTS goals (
    id            SERIAL PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title         VARCHAR(200) NOT NULL,
    note          VARCHAR(500) NOT NULL DEFAULT '',
    target_amount NUMERIC(12, 2),
    scope         VARCHAR(10) NOT NULL CHECK (scope IN ('month', 'year')),
    period        VARCHAR(7)  NOT NULL,
    completed_at  TIMESTAMP,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- The list query is always "this user's goals, grouped by period, newest
-- period first". This index covers it and stays small as completed goals
-- accumulate over years.
CREATE INDEX IF NOT EXISTS goals_user_period_idx
    ON goals (user_id, period DESC, scope DESC);