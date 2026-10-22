# GAMS — Governed Agent Memory Server

> Persistent, evidence-linked, versioned, per-tenant memory infrastructure for AI agents.

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-1.21+-00ADD8.svg)](https://go.dev)
[![TypeScript SDK](https://img.shields.io/badge/npm-@gams/client-CB3837.svg)](sdks/client-ts)
[![Python SDK](https://img.shields.io/badge/pypi-gams--client-3776AB.svg)](sdks/client-py)

GAMS provides durable, governed, auditable memory as infrastructure:
- **Local-first, single-binary distribution**: Pure-Go embedded SQLite WAL (`modernc.org/sqlite`), CGo-free, zero cloud dependencies, zero GPU requirements.
- **Narrow core contract**: `remember`, `recall`, `forget`, and `inspect`.
- **First-class governance**: Every claim carries tenant isolation, namespace boundaries, source evidence links, confidence scores, full version history, explicit contradiction tracking, and an append-only audit trail.
- **Dual MCP transports**: Runs as a long-lived shared network server (Streamable HTTP / SSE) for multi-agent teams or as a subprocess (`stdio`) for local MCP hosts (Claude Code, Cursor).
- **Production hardened**: In-memory token bucket rate limiting, HMAC-SHA256 request signing, Prometheus metrics, and JSONL snapshots with stable schema versioning (`1.0`).

---

## 5-Minute Quickstart

### 1. Run with Docker
```bash
docker run -d \
  --name gams \
  -p 8080:8080 \
  -v gams-data:/data \
  gams:latest
```

### 2. Or Build & Run Standalone Binary
```bash
make build
./gams serve --config gams.toml
```

### 3. Verify Health
```bash
curl http://localhost:8080/health
```
```json
{
  "status": "ok",
  "version": "1.0.0",
  "database": "connected"
}
```

---

## MCP Host Configuration

GAMS integrates directly with standard MCP hosts like **Claude Code** and **Cursor**.

### Option A: Streamable HTTP (Recommended for Multi-Agent & Team Setups)
Connect multiple agents concurrently to a shared GAMS instance:
```json
{
  "mcpServers": {
    "gams": {
      "url": "http://localhost:8080/mcp/sse",
      "headers": {
        "Authorization": "Bearer <YOUR_API_KEY>"
      }
    }
  }
}
```

### Option B: Subprocess `stdio` (Zero-Config Local Attachment)
Direct subprocess execution without needing a background server running:
```json
{
  "mcpServers": {
    "gams": {
      "command": "gams",
      "args": ["serve", "--transport", "stdio"]
    }
  }
}
```
*(When no API key is specified under `stdio`, GAMS automatically provisions and binds to a persistent local workspace tenant).*

---

## Agent System Prompt & Rule Configuration

By default, an LLM agent has access to tools but may not know *when* to proactively query or persist memories without explicit prompting. To enable autonomous memory usage across any AI harness or model (Antigravity, Claude Code, Cursor, Windsurf, custom LangChain/LlamaIndex agents), add the following protocol to your agent's system prompt or workspace instructions (`GEMINI.md`, `CLAUDE.md`, `.cursorrules`, or `AGENTS.md`):

```markdown
# Agent Memory Protocol (GAMS)

You have access to persistent, evidence-backed memory via GAMS tools: `remember`, `recall`, `inspect`, and `forget`.

## Autonomous Memory Guidelines

1. **Before Commencing Work / Answering Complex Questions:**
   - Proactively search memory using `recall` when approaching architectural decisions, project conventions, performance configurations, or debugging tricky components.
   - Ground reasoning in prior verified claims when relevant.

2. **After Resolving Problems / Establishing Decisions:**
   - Proactively persist key takeaways with `remember` when you:
     - Solve an elusive bug or establish a non-obvious workaround.
     - Finalize an architectural pattern, API contract, or configuration rule.
     - Identify important testing or deployment caveats.
   - Always link concrete source evidence (e.g., file paths/URIs, documentation, or commit references) to the claim.
   - Assign appropriate tags and an accurate confidence score (0.0 to 1.0).

3. **Handling Evolving Knowledge & Inconsistencies:**
   - When a past claim becomes obsolete or superseded by a new decision, use `remember` with `contradicts: ["<old_claim_id>"]` to maintain an explicit lineage and prevent hallucinating outdated guidance.
   - If a claim was erroneous or should be discarded, soft-delete it with `forget` providing a concise audit reason.
```

---

## SDK Quickstarts

### TypeScript (`@gams/client`)

Install the ESM-only package:
```bash
npm install @gams/client
```

```typescript
import { GAMSClient } from '@gams/client';

const client = new GAMSClient({
  baseUrl: 'http://localhost:8080',
  apiKey: 'gams_tenant_...',
  signRequests: true, // optional HMAC-SHA256 request signing
});

// Store an evidence-backed claim
const claim = await client.remember({
  content: 'Database connection pool max size set to 25 connections',
  confidence: 0.95,
  evidence: [
    {
      uri: 'file://config/database.ts#L14',
      description: 'Production pool sizing commit',
      evidence_type: 'file',
    },
  ],
  tags: ['database', 'performance'],
  namespace: 'infrastructure',
});

// Retrieve claims ranked by BM25 text relevance, confidence, and recency
const results = await client.recall({
  query: 'database pool size',
  namespace: 'infrastructure',
});
console.log(results.claims);
```

Embedding into custom MCP servers:
```typescript
import { GAMSMCPAdapter } from '@gams/client/mcp';

const adapter = new GAMSMCPAdapter(client);
const tools = adapter.getToolDefinitions();
const result = await adapter.handleToolCall('recall', { query: 'pool size' });
```

---

### Python (`gams-client`)

Install from PyPI:
```bash
pip install gams-client
```

Async (primary, recommended for LangChain, LlamaIndex, PydanticAI):
```python
import asyncio
from gams_client import AsyncGAMSClient, EvidenceItem

async def main():
    async with AsyncGAMSClient(api_key="gams_tenant_...", sign_requests=True) as client:
        # Store claim
        res = await client.remember(
            content="API gateway rate limits capped at 1000 req/s",
            confidence=0.9,
            evidence=[
                EvidenceItem(
                    uri="https://wiki.internal/gateway",
                    description="Architecture decision record",
                    evidence_type="url",
                )
            ],
            tags=["gateway", "ratelimit"],
        )

        # Recall
        memories = await client.recall(query="gateway rate limit")
        for item in memories.claims:
            print(f"[{item.score:.2f}] {item.content}")

asyncio.run(main())
```

Synchronous wrapper (for scripts and notebooks):
```python
from gams_client import GAMSClient, EvidenceItem

client = GAMSClient(api_key="gams_tenant_...")
client.remember(
    content="Production deployment window is Tuesday 10am UTC",
    confidence=1.0,
    evidence=[EvidenceItem(uri="doc://release-policy", description="SRE Policy", evidence_type="file")],
)
```

---

## Admin CLI & Governance Tooling

The single `gams` binary provides administrative commands for lineage inspection and auditable snapshots.

### 1. Inspect Full Claim Lineage
Trace complete version history, contradictions, supporting evidence, and actor audit logs:
```bash
# Direct local SQLite database query
gams admin claims inspect <claim-id> --config gams.toml

# Or query a remote server via Admin API
gams admin claims inspect <claim-id> --server http://localhost:8080 --admin-key <secret>
```

### 2. Export Immutable Audit Log (JSONL)
Exports chronological governance records with mandatory `schema_version: "1.0"`:
```bash
gams admin audit export --tenant <tenant-id> [--namespace <slug>] > audit.jsonl
```

### 3. Export Complete Claims Snapshot (JSONL)
Creates a complete snapshot of all claims, historical versions, and evidence:
```bash
gams admin claims export --tenant <tenant-id> [--namespace <slug>] > claims_snapshot.jsonl
```

---

## Production Operations & Security

### HMAC-SHA256 Request Signing
When enabled (`auth.signing_required = true`), all requests must include cryptographic headers preventing tampering and replay attacks:
- `X-GAMS-Timestamp`: Current Unix timestamp in seconds (must be within ±5 minutes of server time).
- `X-GAMS-Signature`: Hex-encoded HMAC-SHA256 signature calculated over:
  `${METHOD}\n${PATH}\n${TIMESTAMP}\n${BODY_HASH}`

### Prometheus Metrics
GAMS exposes standard Prometheus metrics at `GET /metrics` (protected by admin key when configured via `Authorization: Bearer <admin_key>`):
```bash
curl -H "Authorization: Bearer <ADMIN_KEY>" http://localhost:8080/metrics
```
- `gams_write_total`: Total write attempts by status (`success`, `error`).
- `gams_recall_total`: Total recall queries by status (`success`, `error`).
- `gams_rate_limit_exceeded_total`: Number of rate-limited requests.
- `gams_auth_cache_hits_total` / `gams_auth_cache_misses_total`: bcrypt auth cache efficiency.
- `gams_write_duration_seconds` / `gams_recall_duration_seconds`: Response latency histograms.
- `gams_claims_total`: Current claim volume gauge grouped by status (`active`, `contradicted`, `forgotten`).

### Token Bucket Rate Limiting
In-memory token bucket rate limiting prevents runaway loops from malfunctioning agents:
```toml
[limits]
rate_limit_enabled = true
rate_limit_rps = 100      # 100 tokens added per second
rate_limit_burst = 200    # Maximum burst headroom
```

---

## Configuration Reference

Configuration is managed via `gams.toml` or environment variables prefixed with `GAMS_`:

| TOML Key | Environment Variable | Default | Description |
|---|---|---|---|
| `data.path` | `GAMS_DATA_PATH` | `gams.db` | SQLite database file path |
| `server.host` | `GAMS_SERVER_HOST` | `127.0.0.1` | Network bind address (`0.0.0.0` for containers) |
| `server.http_port` | `GAMS_SERVER_HTTP_PORT` | `8080` | Port for HTTP/SSE, REST, and `/metrics` |
| `server.admin_key` | `GAMS_SERVER_ADMIN_KEY` | `""` | Admin credential for tenant management & metrics |
| `server.cors_origins` | `GAMS_SERVER_CORS_ORIGINS` | `[]` | Allowed CORS origins for browser agents |
| `mcp.transport` | `GAMS_MCP_TRANSPORT` | `http` | Transport mode: `http`, `stdio`, or `both` |
| `limits.max_claim_size_bytes` | `GAMS_LIMITS_MAX_CLAIM_SIZE_BYTES` | `65536` | Maximum claim content size (64 KB) |
| `limits.max_evidence_per_claim` | `GAMS_LIMITS_MAX_EVIDENCE_PER_CLAIM` | `20` | Max evidence links per version |
| `limits.default_recall_limit` | `GAMS_LIMITS_DEFAULT_RECALL_LIMIT` | `10` | Default recall candidate limit |
| `limits.rate_limit_enabled` | `GAMS_LIMITS_RATE_LIMIT_ENABLED` | `true` | Enable token bucket rate limiter |
| `limits.rate_limit_rps` | `GAMS_LIMITS_RATE_LIMIT_RPS` | `100` | Refill rate (tokens per second) |
| `limits.rate_limit_burst` | `GAMS_LIMITS_RATE_LIMIT_BURST` | `200` | Maximum burst capacity |
| `auth.signing_required` | `GAMS_AUTH_SIGNING_REQUIRED` | `false` | Enforce HMAC-SHA256 request signatures |
| `auth.key_cache_ttl_seconds` | `GAMS_AUTH_KEY_CACHE_TTL_SECONDS` | `300` | Bcrypt verification LRU cache TTL |
| — | `GAMS_UNSAFE_DEVMODE` | `false` | Enable dev mode (`--dev` flag); permits empty admin key (never use in production) |

---

## Testing & Verification

```bash
# Run all Go unit and race detection tests
make test

# Run TypeScript SDK test suite
make test-sdk-ts

# Run Python SDK test suite
make test-sdk-py

# Run complete test matrix
make test-all
```

---

## Stability Commitment

> **Additive-Only Schema Evolution Post-v1.0.0**: Database migrations and API models will strictly remain backward-compatible and additive. Breaking changes to JSONL export structures will increment the mandatory `schema_version` field.

---

## License

Licensed under the [Apache License, Version 2.0](LICENSE).
