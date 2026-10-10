package main

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPostgresConfigDisablesNamedPreparedStatements(t *testing.T) {
	config, err := postgresConfig("postgres://user:pass@localhost:5432/budget?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if config.DefaultQueryExecMode != pgx.QueryExecModeExec {
		t.Fatalf("query execution mode = %v, want QueryExecModeExec", config.DefaultQueryExecMode)
	}
}

func TestSessionVersionMigrationIsEmbedded(t *testing.T) {
	migration, err := migrationFS.ReadFile("migrations/011_session_version.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(migration), "ADD COLUMN IF NOT EXISTS session_version") {
		t.Fatal("session-version migration does not add the required column")
	}
}
