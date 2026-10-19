# Senior Engineering Code Review

**Repository**: `github.com/PDK1744/gams`  
**Commit Reviewed**: `5207ceabcb5ea05b99b71b10e311e1038d5f40bd` (`5207cea`)  
**Commit Subject**: `feat: complete phase 0 foundation`  
**Review Date**: 2026-10-19  
**Reviewer**: Senior Staff Software Engineer  
**Status**: Needs Attention (1 Critical, 2 High, 4 Medium findings)

---

## 1. Executive Summary

This commit introduces the initial Phase 0 foundation for the Governed Agent Memory Server (GAMS). The repository adheres to standard, idiomatic Go project layout (`cmd/`, `internal/`, `pkg/`), enforces pure-Go SQLite without CGO dependencies via `modernc.org/sqlite`, defines the full 9-table schema with FTS5 search index, implements structured logging with `log/slog`, and includes comprehensive tests for configuration, migrations, database pragmas, and HTTP health checks.

While the foundation is well-structured and all tests currently pass, there is **one critical bug in how SQLite PRAGMAs are applied across Go's connection pool**, an inverted condition in directory creation for absolute database paths, and several security and operational design considerations that should be addressed before proceeding with Phase 1.

---

## 2. Findings by Severity

```
+-----------------------------------------------------------------------------------------+
| SEVERITY | SUMMARY                                                | LOCATION            |
+-----------------------------------------------------------------------------------------+
| CRITICAL | SQLite pool drops per-connection PRAGMAs (FKs, timeout) | internal/db/db.go   |
| HIGH     | Inverted path check skips directory creation on abs path | internal/db/db.go   |
| HIGH     | Wildcard CORS default (*) and unguarded blank admin key | internal/config/    |
| MEDIUM   | Fixed WriteTimeout kills planned SSE / MCP streaming   | internal/server/    |
| MEDIUM   | Default HTTP listener binds to 0.0.0.0 (all interfaces)  | internal/server/    |
| MEDIUM   | Config loader silently swallows invalid env var values   | internal/config/    |
| MEDIUM   | FTS5 recall_index lacks synchronization mechanism       | migrations/sql/     |
| LOW      | Migration file parsing edge cases & duplicate versions   | migrations/         |
| LOW      | Redundant signal registration in main.go                | cmd/gams/main.go    |
| LOW      | Missing HTTP recovery and request logging middleware    | internal/server/    |
+-----------------------------------------------------------------------------------------+
```

---

### 🔴 Critical Findings

#### CRIT-1: SQLite Connection Pool Drops Per-Connection PRAGMAs (`foreign_keys`, `busy_timeout`)
- **File**: `internal/db/db.go` (Lines 58–70)
- **Problem**:
  In `db.Open()`, the connection pool max connections are set (`runtime.NumCPU() * 2`), and then SQLite PRAGMAs are executed via `conn.ExecContext(ctx, pragma)`:
  ```go
  pragmas := []string{
      "PRAGMA journal_mode = WAL;",
      "PRAGMA busy_timeout = 5000;",
      "PRAGMA synchronous = NORMAL;",
      "PRAGMA foreign_keys = ON;",
  }
  for _, pragma := range pragmas {
      if _, err := conn.ExecContext(ctx, pragma); err != nil { ... }
  }
  ```
  `conn.ExecContext()` borrows a single connection from Go's `database/sql` pool, executes the PRAGMA on that single connection, and returns it to the pool.
  
  In SQLite:
  - **`PRAGMA foreign_keys = ON;` is strictly per-connection.** Newly created connections opened by the pool default to `foreign_keys = 0` (disabled). Foreign key constraints (`ON DELETE CASCADE`, references to `tenants`, `namespaces`, `api_keys`) will be silently bypassed when queries run on any other pooled connection.
  - **`PRAGMA busy_timeout = 5000;` is strictly per-connection.** Subsequent connections default to `0ms`. Under concurrent write contention, queries on those connections immediately fail with `SQLITE_BUSY` (`database is locked`) instead of waiting.
  - **`PRAGMA synchronous = NORMAL;` is per-connection.**
- **Remedy**:
  Configure PRAGMAs in the DSN connection string so that SQLite configures every connection automatically upon creation:
  ```go
  // DSN parameter format supported by modernc.org/sqlite:
  dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)
  conn, err := sql.Open("sqlite", dsn)
  ```
  Alternatively, use `modernc.org/sqlite.RegisterConnectionHook` to execute PRAGMAs on every new underlying connection.

---

### 🟠 High Findings

#### HIGH-1: Inverted Check Skips Parent Directory Creation for Absolute Database Paths
- **File**: `internal/db/db.go` (Lines 29–36)
- **Problem**:
  ```go
  // If not an in-memory database, ensure parent directory exists
  if dbPath != ":memory:" && !filepath.IsAbs(dbPath) {
      dir := filepath.Dir(dbPath)
      if dir != "." && dir != "" {
          if err := os.MkdirAll(dir, 0755); err != nil {
              return nil, fmt.Errorf("creating database directory %q: %w", dir, err)
          }
      }
  }
  ```
  The check `!filepath.IsAbs(dbPath)` ensures that `os.MkdirAll` is **only** called if `dbPath` is relative. When a production or containerized deployment supplies an absolute path (e.g. `/var/lib/gams/gams.db` or `/data/gams.db`), the parent directory is never created, causing `sql.Open` or first write to fail with `no such file or directory`.
- **Remedy**:
  Remove `!filepath.IsAbs(dbPath)` and tighten directory permissions to `0750`:
  ```go
  if dbPath != ":memory:" && !strings.HasPrefix(dbPath, "file:") {
      dir := filepath.Dir(dbPath)
      if dir != "." && dir != "" {
          if err := os.MkdirAll(dir, 0750); err != nil {
              return nil, fmt.Errorf("creating database directory %q: %w", dir, err)
          }
      }
  }
  ```

---

#### HIGH-2: Permissive Default CORS (`*`) and Unguarded Blank Admin Key
- **Files**: `internal/config/config.go` (Line 56), `gams.toml` (Line 18), `cmd/gams/main.go` (Lines 60–62)
- **Problem**:
  1. Default `CORSOrigins` is configured to `["*"]`. For a private memory server storing sensitive tenant memories, claims, and credentials, wildcard CORS allows any malicious script in an operator's local browser to issue cross-origin requests to localhost.
  2. If `admin_key` is empty, `cmd/gams/main.go` logs a warning and proceeds. Without an explicit dev mode switch (e.g., `--dev` flag or `GAMS_ENV=development`), an unconfigured production deployment will run with completely unprotected administrative endpoints.
- **Remedy**:
  - Set default `cors_origins` to empty or restrict to explicit origin configurations.
  - Require an explicit `--dev` flag or environment variable check if `admin_key` is empty; fail fast on startup otherwise.

---

### 🟡 Medium Findings

#### MED-1: Fixed Server `WriteTimeout` Kills Planned SSE / MCP Streaming
- **File**: `internal/server/server.go` (Lines 50–52)
- **Problem**:
  ```go
  ReadTimeout:  15 * time.Second,
  WriteTimeout: 15 * time.Second,
  IdleTimeout:  60 * time.Second,
  ```
  The project plan specifies supporting MCP over HTTP/SSE. A global `WriteTimeout` of 15 seconds will forcibly terminate any SSE stream or long-lived agent response after 15 seconds.
- **Remedy**:
  Use Go 1.20+ `http.ResponseController.SetWriteDeadline(time.Time{})` inside streaming/SSE handlers, or configure `WriteTimeout: 0` on the server and apply per-route timeouts via `http.TimeoutHandler` on standard REST endpoints.

---

#### MED-2: Server Binds to All Network Interfaces (`0.0.0.0`)
- **File**: `internal/server/server.go` (Line 48)
- **Problem**:
  `Addr: fmt.Sprintf(":%d", cfg.Server.HTTPPort)` binds to all available network interfaces. In local agent setups or development workstations, this unintentionally exposes the server to the local network.
- **Remedy**:
  Add `Host` / `bind_address` to `ServerConfig` (defaulting to `127.0.0.1` for local safety, configurable to `0.0.0.0` for containers).

---

#### MED-3: Config Loader Silently Swallows Invalid Environment Variables
- **File**: `internal/config/config.go` (Lines 105–141)
- **Problem**:
  In `applyEnvOverrides`, conversion errors are silently ignored:
  ```go
  if val := os.Getenv("GAMS_SERVER_HTTP_PORT"); val != "" {
      if port, err := strconv.Atoi(val); err == nil {
          cfg.Server.HTTPPort = port
      }
  }
  ```
  If an operator configures `GAMS_SERVER_HTTP_PORT=9090a` or invalid numbers for limits, the error is swallowed and the default value is quietly retained without indication.
- **Remedy**:
  Have `applyEnvOverrides(cfg) error` return descriptive parsing errors when values cannot be parsed.

---

#### MED-4: FTS5 `recall_index` Table Synchronization
- **File**: `internal/migrations/sql/0001_initial_schema.sql` (Lines 142–150)
- **Problem**:
  The schema creates the FTS5 virtual table `recall_index`, but no SQLite triggers are defined to keep `recall_index` synchronized with additions, updates, or deletions in the `claims` table.
- **Remedy**:
  Either:
  1. Add standard SQLite triggers (`AFTER INSERT`, `AFTER UPDATE`, `AFTER DELETE` on `claims`) in migration 0001 to guarantee search index consistency at the storage layer; or
  2. Clearly document in the service architecture that FTS index updates are orchestrated in application service transactions during Phase 1.

---

### 🟢 Low / Clean Code Observations

1. **Migration File Discovery Edge Cases**:
   - In `internal/migrations/migrations.go` (Lines 66–70), files lacking an underscore are skipped silently via `continue`. Consider strict naming verification (e.g. `^\d{4}_.*\.sql$`) and validating that migration version integers are strictly unique before execution to avoid partial transaction failures.
2. **Signal Registration Redundancy**:
   - In `cmd/gams/main.go` (Line 81): `signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)`. On Linux/Unix, `os.Interrupt` is already `syscall.SIGINT`.
3. **HTTP Middleware**:
   - `internal/server/server.go` currently attaches routes directly to `http.NewServeMux()` without request logging or panic recovery middleware. Introducing standard middleware will protect the server process from crashing on unhandled panics in handlers.
4. **Timestamp Consistency**:
   - `pkg/types/types.go` uses Unix nanoseconds (`NowNano()`). Note that SQLite's built-in date/time functions (`datetime()`, `strftime()`) expect seconds or milliseconds. Consider whether milliseconds (`time.Now().UnixMilli()`) would better align with standard SQLite datetime functions and JSON ecosystem conventions.

---

## 3. Positive Highlights

- **Clean Project Layout**: Standard Go structure separates CLI entrypoints (`cmd/gams`), private internal packages (`internal/`), and exported domain types (`pkg/types`).
- **Domain Modeling**: `pkg/types/types.go` is well-modeled with explicit JSON tags, nullable pointer types (`*string`, `*int64`, `*float64`), and clean documentation.
- **Pure-Go SQLite**: Choosing `modernc.org/sqlite` provides frictionless cross-compilation and eliminates CGO toolchain requirements.
- **Graceful Shutdown**: `cmd/gams/main.go` properly listens for termination signals and allows an orderly 5-second drain period for HTTP and database connections.
- **Test Quality**: Tests make proper use of `t.TempDir()`, isolated `:memory:` databases, and verify degraded service states (e.g., verifying 503 response when database is closed).

---

## 4. Recommended Action Plan Prior to Phase 1

1. **Fix SQLite DSN PRAGMAs**: Update `internal/db/db.go` to inject `_pragma` connection arguments directly into the DSN.
2. **Fix Directory Creation**: Remove `!filepath.IsAbs(dbPath)` in `internal/db/db.go`.
3. **Tighten Security Defaults**: Restrict default CORS origins and add a safety check preventing server startup with an empty `admin_key` outside development mode.
4. **Prepare Server for Streaming**: Refactor HTTP timeout management in `internal/server/server.go` to accommodate MCP Server-Sent Events.
