package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed sql/*.sql
var migrationFS embed.FS

type migrationFile struct {
	version int
	name    string
	path    string
}

// Run executes all pending SQL migrations embedded in the binary in sequential order.
func Run(ctx context.Context, db *sql.DB) error {
	// 1. Ensure schema_migrations table exists
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	);`
	if _, err := db.ExecContext(ctx, createTableSQL); err != nil {
		return fmt.Errorf("creating schema_migrations table: %w", err)
	}

	// 2. Query already applied migration versions
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("querying applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return fmt.Errorf("scanning migration version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading migration versions: %w", err)
	}

	// 3. Find, validate, and sort embedded migration files
	entries, err := fs.ReadDir(migrationFS, "sql")
	if err != nil {
		return fmt.Errorf("reading embedded migrations dir: %w", err)
	}

	var files []migrationFile
	seenVersions := make(map[int]string)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".sql") {
			return fmt.Errorf("unexpected non-sql file in migrations directory: %q", entry.Name())
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) < 2 || parts[1] == "" {
			return fmt.Errorf("invalid migration filename %q: expected format {version}_{description}.sql", entry.Name())
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil || version <= 0 {
			return fmt.Errorf("invalid migration version prefix in %q: must be a positive integer", entry.Name())
		}
		if prevFile, exists := seenVersions[version]; exists {
			return fmt.Errorf("duplicate migration version %d found in %q and %q", version, prevFile, entry.Name())
		}
		seenVersions[version] = entry.Name()

		files = append(files, migrationFile{
			version: version,
			name:    entry.Name(),
			path:    "sql/" + entry.Name(),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].version < files[j].version
	})

	// 4. Execute pending migrations in a transaction per migration
	for _, file := range files {
		if applied[file.version] {
			continue
		}

		content, err := migrationFS.ReadFile(file.path)
		if err != nil {
			return fmt.Errorf("reading embedded migration %q: %w", file.name, err)
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("beginning tx for migration %d: %w", file.version, err)
		}

		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("executing migration %d (%s): %w", file.version, file.name, err)
		}

		recordSQL := `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?);`
		if _, err := tx.ExecContext(ctx, recordSQL, file.version, time.Now().UTC().UnixNano()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("recording applied migration %d: %w", file.version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %d: %w", file.version, err)
		}
	}

	return nil
}
