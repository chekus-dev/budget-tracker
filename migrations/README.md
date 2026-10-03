# Database migrations

PostgreSQL schema for the Budget Tracker, as an ordered sequence. Each file
builds on the one before it, so **they must be applied in order**.

| # | File | What it does |
|---|------|--------------|
| 001 | `001_create_expenses.sql` | The original `expenses` table |
| 002 | `002_add_users_and_settings.sql` | `users` + `settings`, and `expenses.user_id` |
| 003 | `003_add_expense_category.sql` | `expenses.category` |
| 004 | `004_add_password_reset.sql` | `users.email` and the reset-token columns |
| 005 | `005_index_expenses_by_user_and_date.sql` | Index for the per-user, per-month reads |
| 006 | `006_add_soft_delete.sql` | `expenses.deleted_at`, and a partial index over live rows |

Running all six against an empty database produces the complete schema the
app expects.

## Applying them

**The app does this for you.** On startup the binary runs every file here, in
order, before it begins serving — the SQL is embedded in the executable (see
`migrations.go`), so there is no separate step at deploy time and no need for
`psql` to be installed. Because every statement is idempotent, this is a no-op
on an already-current database. The manual routes below are for inspecting or
repairing a database, and for local work where you would rather see the SQL run
in front of you.

**Supabase SQL Editor** — paste and run each file in numeric order. Easiest
for a hosted database.

**psql** — from the project root, with `DATABASE_URL` set:

```bash
for f in migrations/*.sql; do
    echo "applying $f"
    psql "$DATABASE_URL" -f "$f" || break
done
```

The `|| break` stops the loop on the first failure rather than ploughing on
and reporting a confusing cascade of errors.

## Two things to know

**There is no migrations tracking table.** Nothing records which files have
already run, so it is on you not to re-run or skip one. To compensate, every
statement here is idempotent (`CREATE TABLE IF NOT EXISTS`,
`ADD COLUMN IF NOT EXISTS`), so re-running a file does nothing rather than
erroring halfway through. Idempotency is exactly what makes it safe for the app
to apply the whole set on every boot; that same startup path takes a PostgreSQL
advisory lock (`migrationLockID` in `migrations.go`) so that two instances
racing during a rolling deploy cannot collide — `CREATE TABLE IF NOT EXISTS` is
not race-free on its own. If this sequence grows much further, switch to a real
tool — [`golang-migrate`](https://github.com/golang-migrate/migrate) or
[`goose`](https://github.com/pressly/goose) both integrate with a Go project and
add the tracking table for you.

**Migration 002 will not work on a table that already has rows.** It adds
`expenses.user_id` as `NOT NULL` with no default, which is fine because 001
creates the table empty and this sequence runs against a fresh database. A
database with existing expenses would need the column added as nullable, the
rows backfilled, and then the constraint applied. This is deliberately
different from the original MySQL version, which used `DEFAULT 1` as a
workaround.

## Final schema

The state after all six migrations. This is the contract with `main.go` —
the queries there name every column explicitly.

```sql
users
    id                  SERIAL PRIMARY KEY
    username            VARCHAR(50) UNIQUE NOT NULL
    password_hash       VARCHAR(255) NOT NULL
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
    email               VARCHAR(255) UNIQUE           -- nullable
    reset_token         VARCHAR(64)                   -- nullable
    reset_token_expires TIMESTAMP                     -- nullable

settings
    user_id             INTEGER PRIMARY KEY REFERENCES users(id)
    budget_limit        NUMERIC(10,2) NOT NULL DEFAULT 0
    currency            VARCHAR(10) NOT NULL DEFAULT 'NGN'
    categories          VARCHAR(500) NOT NULL DEFAULT 'Food,Transport,Bills'
    theme               VARCHAR(10) NOT NULL DEFAULT 'system'

expenses
    id                  SERIAL PRIMARY KEY
    description         VARCHAR(255) NOT NULL
    amount              NUMERIC(10,2) NOT NULL
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
    user_id             INTEGER NOT NULL REFERENCES users(id)
    category            VARCHAR(50) NOT NULL DEFAULT ''
    deleted_at          TIMESTAMP                     -- nullable

    INDEX expenses_live_user_created_idx (user_id, created_at DESC)
        WHERE deleted_at IS NULL
```

`created_at` is `TIMESTAMP` *without* time zone on purpose. The app treats
time as a naive wall clock — month strings like `"2026-10"`, `time.Now()` in
the monthly-reset goroutine — and a timezone-aware column would let an
expense drift into the wrong month bucket.

`deleted_at` is the undo-toast mechanism: a delete sets it rather than removing
the row, and **every** query that reads `expenses` filters on
`deleted_at IS NULL`. If you add a new one, add that predicate — a query that
forgets it will silently resurrect deleted expenses, which is the kind of bug
that looks like a data-corruption report rather than a missing clause. The
`expenses_user_created_idx` from migration 005 was replaced by a partial index
matching the new predicate, so the extra condition costs nothing.

Rows are removed for real in two places: `clearHandler`, after the user
confirms a month reset, and the nightly purge in `autoMonthlyReset`, which
deletes anything soft-deleted more than 30 days ago.

## Adding a migration

Create the next number with a short description, keep it idempotent, and
check the column list in the "Final schema" below still matches what the Go
code expects. The queries in `main.go` name every column explicitly, so a
column rename means a code change in the same breath.
