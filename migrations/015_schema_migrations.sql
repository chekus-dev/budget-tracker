-- 015_schema_migrations.sql
--
-- Records which migrations have been applied. The application now reads this
-- table before applying each file: a file whose name is already present is
-- skipped entirely, whether or not its SQL is idempotent.
--
-- This makes the ordering guarantee real — migrations run once, in order,
-- exactly once — and it opens the door to data migrations, which cannot be
-- made idempotent by adding IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS schema_migrations (
    filename   TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);