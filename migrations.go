package main

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationLockID is an arbitrary, stable advisory-lock key. It only has to be
// distinct from other advisory locks in the database; nothing else in this app
// takes one.
const migrationLockID int64 = 0x627564676574 // "budget"

// applyMigrations runs every migrations/*.sql against db, in filename order.
//
// Why this runs on every boot rather than once by hand: /healthz returns 200
// even when the database is unusable (see render.yaml), so a deploy against an
// unmigrated database looks healthy in the Render dashboard while every expense
// page errors. Applying the schema in-process removes that failure mode, and it
// does not depend on `psql` existing in the runtime image — the same binary
// that serves requests carries its own schema.
//
// Tracking: schema_migrations records each filename that has been applied. A
// file whose name is present is skipped entirely, whether or not its SQL is
// idempotent. This is what makes the ordering guarantee real — a migration runs
// once, in order, exactly once — and it opens the door to data migrations
// (backfills, column rewrites), which cannot be made idempotent with IF NOT
// EXISTS and would otherwise re-run on every boot.
//
// Existing databases that predate this change: on the first boot after
// deploying, every migration re-applies once (harmless — the historical files
// are all written idempotently) and gets recorded. Subsequent boots skip them.
//
// All migrations run in one transaction under a transaction-scoped advisory
// lock. This makes the schema update atomic and keeps the lock valid when
// DATABASE_URL uses a transaction pooler.
func applyMigrations(ctx context.Context, db *sql.DB) error {
	// Keep one logical connection for the transaction. Poolers pin it to one
	// server connection until Commit or Rollback.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}

	// Bootstrap the ledger before reading it. Deliberately not a migration
	// file: the loop below must read this table before any file has been
	// applied, and a file that creates its own ledger would have to be
	// exempted from the loop that maintains it. Creating it here keeps the
	// bootstrap inside the advisory lock, so two instances racing on a fresh
	// database cannot both try to create it.
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename   TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list embedded migrations: %w", err)
	}
	sort.Strings(names)

	type appliedMigration struct {
		name    string
		elapsed time.Duration
	}
	applied := make([]appliedMigration, 0, len(names))
	skipped := 0

	for _, name := range names {
		// Already recorded — skip the file entirely, regardless of whether its
		// SQL would be a no-op. This is what allows non-idempotent migrations
		// (UPDATE, DELETE, ALTER ... SET NOT NULL after a backfill) to exist
		// alongside the idempotent history.
		var already bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE filename = $1)`,
			name,
		).Scan(&already); err != nil {
			return fmt.Errorf("check %s: %w", name, err)
		}
		if already {
			skipped++
			continue
		}

		body, err := migrationFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}

		start := time.Now()
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (filename) VALUES ($1)`,
			name,
		); err != nil {
			return fmt.Errorf("record %s: %w", name, err)
		}
		applied = append(applied, appliedMigration{name: name, elapsed: time.Since(start).Round(time.Millisecond)})
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}

	for _, migration := range applied {
		log.Printf("migration applied: %s (%s)", migration.name, migration.elapsed)
	}
	log.Printf("migrations: %d skipped, %d applied", skipped, len(applied))
	return nil
}