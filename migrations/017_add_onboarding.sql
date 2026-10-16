-- 017_add_onboarding.sql
--
-- Records when an account finished the first-run welcome, so the wizard can be
-- shown exactly once. NULL means "has not finished it yet" and is the only
-- state that sends someone to /welcome.
--
-- The backfill is the whole point of this file. Anyone who already has an
-- account has by definition been using the app without a welcome, and must not
-- be marched through one on their next visit — so every existing row is marked
-- done. Note this is the deliberate opposite of 007_add_terms_acceptance.sql,
-- which left its existing rows NULL on purpose so as not to invent consent
-- nobody gave. The difference is what the column means: terms_accepted_at is a
-- claim about something the user did, and backfilling it would be a lie;
-- onboarded_at is only a record of whether we have shown them a screen, and
-- for an existing account the answer is a truthful "no need to".
--
-- A data migration like this can only run here because 015 added the
-- schema_migrations ledger: this file is applied once, in order, and never
-- again. Without the ledger the UPDATE would re-run on every boot and sweep up
-- each new account that happened to be mid-wizard at the time — which is
-- exactly the failure the ledger exists to prevent.

ALTER TABLE users ADD COLUMN IF NOT EXISTS onboarded_at TIMESTAMP;

UPDATE users SET onboarded_at = created_at WHERE onboarded_at IS NULL;
