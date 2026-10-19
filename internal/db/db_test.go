package db_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/PDK1744/gams/internal/db"
)

func TestOpenAndPragmas(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_gams.db")

	database, err := db.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	defer database.Close()

	// Verify WAL mode
	var journalMode string
	if err := database.QueryRowContext(ctx, "PRAGMA journal_mode;").Scan(&journalMode); err != nil {
		t.Fatalf("querying journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("expected journal_mode 'wal', got %q", journalMode)
	}

	// Verify Foreign Keys
	var foreignKeys int
	if err := database.QueryRowContext(ctx, "PRAGMA foreign_keys;").Scan(&foreignKeys); err != nil {
		t.Fatalf("querying foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("expected foreign_keys 1, got %d", foreignKeys)
	}

	// Test FTS5 insertion and querying
	insertFTS := `INSERT INTO recall_index (claim_id, tenant_id, namespace_id, content, tags, namespace_slug) 
	              VALUES ('c1', 't1', 'n1', 'authentication token secret jwt', '["auth", "security"]', 'auth-project');`
	if _, err := database.ExecContext(ctx, insertFTS); err != nil {
		t.Fatalf("inserting into FTS5 recall_index: %v", err)
	}

	var matchClaimID string
	queryFTS := `SELECT claim_id FROM recall_index WHERE recall_index MATCH 'jwt' LIMIT 1;`
	if err := database.QueryRowContext(ctx, queryFTS).Scan(&matchClaimID); err != nil {
		t.Fatalf("querying FTS5 recall_index: %v", err)
	}
	if matchClaimID != "c1" {
		t.Errorf("expected claim_id 'c1', got %q", matchClaimID)
	}
}
