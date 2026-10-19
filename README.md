# GAMS — Governed Agent Memory Server

> Persistent, evidence-linked, versioned, per-tenant memory infrastructure for AI agents.

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-1.21+-00ADD8.svg)](https://go.dev)

GAMS provides durable, auditable memory as infrastructure:
- **Local-first, single-binary distribution**: Built with Go and pure-Go embedded SQLite (CGo-free, zero required cloud dependencies or GPU).
- **Narrow core contract**: `remember`, `recall`, `forget`, and `inspect`.
- **First-class governance**: Every claim carries tenant isolation, namespace boundaries, source evidence links, confidence scores, full version history, and an append-only audit trail.
- **Cross-model portability**: Portable across models, agent frameworks, and MCP hosts (Claude Code, Cursor, custom agents).

---

## Getting Started (Phase 0: Foundation)

### Prerequisites
- Go 1.21+ (built and tested on Go 1.26)
- Make (optional)

### Build from Source
```bash
make build
```
This generates the standalone `gams` binary in the project root.

### Run Server
```bash
./gams serve --config gams.toml
```
By default, the server binds to `http://localhost:8080` and initializes an embedded SQLite database (`gams.db`) with WAL mode and initial schema migrations.

### Health Check
```bash
curl http://localhost:8080/health
```
Response:
```json
{
  "status": "ok",
  "version": "0.1.0",
  "database": "connected"
}
```

---

## Configuration

Configuration is managed via a single TOML file (`gams.toml`) or environment variables with the `GAMS_` prefix.

| TOML Key | Environment Variable | Default | Description |
|---|---|---|---|
| `data.path` | `GAMS_DATA_PATH` | `gams.db` | SQLite database file path |
| `server.http_port` | `GAMS_SERVER_HTTP_PORT` | `8080` | Port for HTTP/SSE & REST endpoints |
| `server.admin_key` | `GAMS_SERVER_ADMIN_KEY` | `""` | Admin API secret key |
| `server.cors_origins` | `GAMS_SERVER_CORS_ORIGINS` | `["*"]` | Allowed CORS origins |
| `mcp.transport` | `GAMS_MCP_TRANSPORT` | `http` | Transport protocol: `http`, `stdio`, or `both` |
| `limits.max_claim_size_bytes` | `GAMS_LIMITS_MAX_CLAIM_SIZE_BYTES` | `65536` | Maximum claim content size (bytes) |
| `limits.max_evidence_per_claim` | `GAMS_LIMITS_MAX_EVIDENCE_PER_CLAIM` | `20` | Max evidence links per claim |
| `limits.default_recall_limit` | `GAMS_LIMITS_DEFAULT_RECALL_LIMIT` | `10` | Default claim limit for recall |
| `auth.signing_required` | `GAMS_AUTH_SIGNING_REQUIRED` | `false` | Enforce HMAC request signatures |
| `auth.key_cache_ttl_seconds` | `GAMS_AUTH_KEY_CACHE_TTL_SECONDS` | `300` | In-memory bcrypt verification cache TTL |

---

## Development

```bash
make test    # Run unit and integration tests with race detector
make lint    # Run vet linter
make clean   # Remove compiled binaries and test database files
```

---

## License

Licensed under the [Apache License, Version 2.0](LICENSE).
