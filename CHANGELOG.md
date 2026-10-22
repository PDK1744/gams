# Changelog

All notable changes to the Governed Agent Memory Server (GAMS) will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [1.0.0] - 2026-10-22

### Public Stability Commitment
> **v1.0.0 — Additive-only schema changes from here forward. `schema_version` will increment on breaking export format changes.**

This marks the official production-grade v1.0.0 release of GAMS. The SQLite database schema, REST API (`/v1/`), MCP server tools, and snapshot export formats are now stable.

### Added
- **HMAC-SHA256 Request Signing**:
  - Optional cryptographic request signing via `X-GAMS-Timestamp` and `X-GAMS-Signature` headers with ±5-minute timestamp replay protection.
  - Server configuration option: `auth.signing_required = true` (or `GAMS_AUTH_SIGNING_REQUIRED=true`).
  - First-class SDK support in TypeScript (`@gams/client` via `signRequests: true`) and Python (`gams-client` via `sign_requests=True`).
- **Prometheus Metrics Endpoint (`/metrics`)**:
  - Standard Prometheus text format (0.0.4) metrics for monitoring server operations.
  - Metrics tracked: write throughput (`gams_write_total`), recall query volume (`gams_recall_total`), rate limit denials (`gams_rate_limit_exceeded_total`), API key cache hit rate (`gams_auth_cache_hits_total`, `gams_auth_cache_misses_total`), latency histograms (`gams_recall_duration_seconds`, `gams_write_duration_seconds`), and live claim volume gauges (`gams_claims_total`).
- **Token Bucket Rate Limiting**:
  - In-memory per-API-key token bucket rate limiting (no Redis or external dependencies required).
  - Configurable rates and burst headroom (`limits.rate_limit_enabled`, `limits.rate_limit_rps`, `limits.rate_limit_burst`).
  - Returns HTTP 429 Too Many Requests with `Retry-After: 1` upon limit exhaustion. Internal health and metrics checks are exempt.
- **Audit & Claims Snapshot Export CLI & REST**:
  - `gams admin audit export --tenant <id> [--namespace <slug>] [--format jsonl]`: JSONL export of immutable audit trail with mandatory `schema_version: "1.0"` on every record.
  - `gams admin claims export --tenant <id> [--namespace <slug>]`: Full claims snapshot (current state, all historical versions, linked evidence) as JSONL with header record and `schema_version: "1.0"`.
  - Supports both direct SQLite WAL database reading (`--config`) and remote HTTP queries (`--server` + `--admin-key`).
  - Admin endpoints: `GET /admin/tenants/{id}/audit/export` and `GET /admin/tenants/{id}/claims/export`.
- **Packaging & Containerization**:
  - Multi-stage distroless `Dockerfile` targeting `gcr.io/distroless/static-debian12:nonroot` with `/data` persistent storage volume.
  - `.goreleaser.yaml` configuration for cross-platform binary builds (Linux amd64/arm64, macOS amd64/arm64, Windows amd64).
  - Static pure-Go compilation with zero CGo and zero external system dependencies.

---

## [0.4.0] - 2026-10-21
### Added
- Python SDK `gams-client` with `AsyncGAMSClient` and synchronous `GAMSClient` wrapper.
- Pydantic v2 data models with validation and serialization.
- Claim lineage inspection and governance UX (`gams admin claims inspect <id>`).
- Exponential conflict backoff retry with dynamic `base_version` re-fetching.

## [0.3.0] - 2026-10-20
### Added
- Standard I/O (`stdio`) transport for subprocess attachment (Claude Code, Cursor).
- TypeScript SDK `@gams/client` (ESM-only, Node ≥ 18) with REST client and `@gams/client/mcp` adapter.
- Zero-config local workspace tenant auto-provisioning.

## [0.2.0] - 2026-10-19
### Added
- MCP Server over Streamable HTTP (`GET /mcp/sse`, `POST /mcp/message`).
- Multi-client concurrent session management and JSON-RPC 2.0 dispatcher.
- Tools: `remember`, `recall`, `forget`, `inspect`.
- Resources: `gams://claims/{id}`, `gams://namespaces/{slug}`, `gams://tenants/{id}/summary`.

## [0.1.0] - 2026-10-18
### Added
- Initial core engine with SQLite WAL and 9-table schema migrations.
- Tenant and namespace administrative REST API.
- Agent REST API (`/v1/claims`, `/v1/recall`, `/v1/namespaces`).
- FTS5 hybrid BM25 + confidence + recency scoring pipeline.
- Append-only audit logger and bcrypt LRU authentication cache.
