package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/PDK1744/gams/internal/migrations"
	_ "modernc.org/sqlite"
)

// DB wraps a standard sql.DB pool configured specifically for SQLite WAL operation.
type DB struct {
	*sql.DB
}

// Open initializes the SQLite database connection, sets recommended WAL pragmas,
// configures the connection pool, and runs all pending schema migrations.
func Open(ctx context.Context, dbPath string) (*DB, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("database path cannot be empty")
	}

	// If not an in-memory database, ensure parent directory exists
	if dbPath != ":memory:" && !filepath.IsAbs(dbPath) {
		dir := filepath.Dir(dbPath)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("creating database directory %q: %w", dir, err)
			}
		}
	}

	// Open database handle with modernc.org/sqlite driver
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database at %q: %w", dbPath, err)
	}

	// Configure pool
	if dbPath == ":memory:" {
		conn.SetMaxOpenConns(1)
	} else {
		maxConns := runtime.NumCPU() * 2
		if maxConns < 4 {
			maxConns = 4
		}
		conn.SetMaxOpenConns(maxConns)
		conn.SetMaxIdleConns(maxConns)
		conn.SetConnMaxLifetime(1 * time.Hour)
	}

	// Apply WAL pragmas and foreign keys
	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA foreign_keys = ON;",
	}

	for _, pragma := range pragmas {
		if _, err := conn.ExecContext(ctx, pragma); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("executing pragma %q: %w", pragma, err)
		}
	}

	// Verify connectivity
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	// Run migrations
	if err := migrations.Run(ctx, conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("running initial migrations: %w", err)
	}

	return &DB{DB: conn}, nil
}
