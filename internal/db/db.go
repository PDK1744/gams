package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/PDK1744/gams/internal/migrations"
	_ "modernc.org/sqlite"
)


// DB wraps a standard sql.DB pool configured specifically for SQLite WAL operation.
type DB struct {
	*sql.DB
}

// Open initializes the SQLite database connection, configures DSN connection-level pragmas
// (ensuring all pooled connections have WAL, busy_timeout, foreign_keys enabled),
// configures the connection pool, and runs all pending schema migrations.
func Open(ctx context.Context, dbPath string) (*DB, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("database path cannot be empty")
	}

	// If not an in-memory database, ensure parent directory exists (supporting both relative and absolute paths)
	if dbPath != ":memory:" && !strings.HasPrefix(dbPath, "file::memory:") {
		cleanPath := strings.TrimPrefix(dbPath, "file:")
		if idx := strings.Index(cleanPath, "?"); idx != -1 {
			cleanPath = cleanPath[:idx]
		}
		dir := filepath.Dir(cleanPath)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0750); err != nil {
				return nil, fmt.Errorf("creating database directory %q: %w", dir, err)
			}
		}
	}

	// Build DSN with connection-level pragmas so EVERY connection in the pool inherits them
	dsn := buildDSN(dbPath)

	// Open database handle with modernc.org/sqlite driver
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database with DSN %q: %w", dsn, err)
	}

	// Configure connection pool
	if dbPath == ":memory:" || strings.Contains(dbPath, ":memory:") {
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

// buildDSN appends essential SQLite pragmas to the connection string.
// Applying these as DSN parameters ensures every connection created by the pool
// receives foreign_keys, busy_timeout, and WAL mode.
func buildDSN(dbPath string) string {
	pragmas := "_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	if dbPath != ":memory:" && !strings.Contains(dbPath, ":memory:") {
		pragmas += "&_pragma=journal_mode(WAL)"
	}

	separator := "?"
	if strings.Contains(dbPath, "?") {
		separator = "&"
	}
	return dbPath + separator + pragmas
}

