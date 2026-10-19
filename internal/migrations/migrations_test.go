package migrations_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/PDK1744/gams/internal/migrations"
	_ "modernc.org/sqlite"
)

func TestMigrationsRun(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	defer db.Close()

	// Initial run
	if err := migrations.Run(ctx, db); err != nil {
		t.Fatalf("first migration run failed: %v", err)
	}

	// Verify schema_migrations table contains version 1
	var version int
	var appliedAt int64
	err = db.QueryRowContext(ctx, "SELECT version, applied_at FROM schema_migrations WHERE version = 1").Scan(&version, &appliedAt)
	if err != nil {
		t.Fatalf("failed to query migration version 1: %v", err)
	}
	if version != 1 {
		t.Errorf("expected version 1, got %d", version)
	}
	if appliedAt <= 0 {
		t.Errorf("expected positive applied_at, got %d", appliedAt)
	}

	// Verify that expected tables exist
	expectedTables := []string{
		"tenants",
		"namespaces",
		"namespace_grants",
		"api_keys",
		"claims",
		"claim_versions",
		"evidence",
		"audit_log",
		"recall_index",
	}

	for _, table := range expectedTables {
		var name string
		err := db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE (type='table' OR type='virtual') AND name = ?", table).Scan(&name)
		if err != nil {
			t.Errorf("expected table %q to exist: %v", table, err)
		}
	}

	// Second run should be idempotent
	if err := migrations.Run(ctx, db); err != nil {
		t.Fatalf("second migration run should succeed idempotently: %v", err)
	}
}
