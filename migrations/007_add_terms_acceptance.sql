-- 007_add_terms_acceptance.sql
--
-- Records *when* a user accepted the Terms and Conditions, rather than only
-- refusing to create the account without a tick in the box.
--
-- Why a timestamp and not a boolean: consent is only meaningfully provable if
-- you can say which version of the terms someone agreed to and when. The date
-- is the part that answers "when"; a future terms change would add a version
-- column and compare it against this.
--
-- Deliberately left NULL for existing rows. They registered before there was
-- anything to accept, and backfilling a timestamp would invent consent that
-- was never given. The signup path is the only thing that reads it.
--
-- Safe to run more than once.

ALTER TABLE users ADD COLUMN IF NOT EXISTS terms_accepted_at TIMESTAMP;
