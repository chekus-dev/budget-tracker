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
// Every migration here is written to be idempotent (`CREATE TABLE IF NOT
// EXISTS`, `ADD COLUMN IF NOT EXISTS`), and there is deliberately no tracking
// table (see migrations/README.md), so re-applying the whole set is a no-op.
// Each file is sent as a single multi-statement Exec, which the simple protocol
// wraps in one implicit transaction — a file either applies entirely or not at
// all. The advisory lock guards the one case idempotency does not cover: two
// instances migrating during a rolling deploy, where `CREATE TABLE IF NOT
// EXISTS` is not race-free and can fail with a duplicate-key error on pg_type.
func applyMigrations(ctx context.Context, db *sql.DB) error {
	// Take a dedicated connection: a session-level advisory lock is tied to the
	// session, and the pool could otherwise hand the unlock to a different one.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	// Best-effort unlock; closing the connection would release it anyway.
	defer conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockID)

	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list embedded migrations: %w", err)
	}
	sort.Strings(names)

	for _, name := range names {
		body, err := migrationFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		start := time.Now()
		if _, err := conn.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
		log.Printf("migration applied: %s (%s)", name, time.Since(start).Round(time.Millisecond))
	}
	return nil
}
