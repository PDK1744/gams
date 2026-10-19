Governed Agent Memory Server — Architecture & Implementation Plan

│ Scope: Planning artifact only. No production code, no scaffolding, no config files.
│ Status: Draft v3 — October 2026 — All decisions resolved. Ready for Phase 0.

### All Resolved Decisions

| Q   | Decision                                                                                                                                                |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Q1  | Binary name: gams. Env prefix: GAMS\_. Config file: gams.toml.                                                                                          |
| Q2  | Namespaces are first-class entities with their own table, metadata, and access control.                                                                 |
| Q3  | MCP server name: "gams" (protocol/config identifier); displayName: "GAMS"; description: "Governed Agent Memory Server".                                 |
| Q4  | HTTP/SSE transport ships first (Phase 2). stdio ships in Phase 3 as secondary.                                                                          |
| Q5  | TypeScript SDK: @gams/client (scoped npm); ESM-only (Node ≥ 18); core REST client + mcp submodule adapter in same package.                              |
| Q6  | Python SDK: single gams-client package; asyncio-first with sync wrapper (httpx/openai pattern).                                                         |
| Q7  | Additive-only schema changes post-v1.0.0. schema_version field mandatory in all exports and snapshots.                                                  |
| Q8  | Primary persona: developer-power-user who runs local agents and builds products. SDK quality and zero-config MCP compatibility are both non-negotiable. |
| Q9  | Per-tenant auto_create_namespaces flag (default: false / strict 404). SDKs provide ensure_namespace() helper.                                           |

──────

## Table of Contents

1. License Recommendation
2. Architecture Overview
3. Architecture Decisions (with Reasoning)
4. Data Model
5. API / MCP Contract
6. Component Reference
7. Phased Implementation Plan
8. Risk Register
9. Open Questions
   ──────

## 1. License Recommendation

Recommendation: Apache-2.0

Reasoning: Apache-2.0 satisfies the open-source principle and is the right choice here for the following reasons:

• It is permissive enough for commercial agents and enterprise users to embed the server without legal friction — this matters for adoption since most agents run inside commercial products.
• It includes an explicit patent retaliation clause, which AGPL and MIT lack. This matters for infrastructure software where patent trolls are a real risk.
• It does not trigger copyleft on client-side code. SDK users (TypeScript, Python) do not have to open-source their agent code.

The case for AGPL-3.0 (rejected): AGPL would prevent cloud providers from hosting the server as a managed service without contributing back. That sounds appealing, but in practice it creates friction with enterprise legal teams, slows adoption, and does not meaningfully prevent large
providers from either (a) writing a competing product from scratch, or (b) simply not adopting yours. The structural moat is the data format's portability, not the license.

The case for BSL (rejected): Business Source License (used by HashiCorp, MariaDB) restricts production use for competing hosted services. Rejected because it is not OSI-approved and will alienate the open-source community you need for credibility and contributions.
──────

## 2. Architecture Overview

### 2.1 System Topology

    ┌─────────────────────────────────────────────────────────────────┐
    │                    Agent Ecosystem                               │
    │  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────┐  │
    │  │ Claude Code  │  │   Cursor     │  │  Custom Agent / SDK  │  │
    │  │  (MCP Host)  │  │  (MCP Host)  │  │  (TypeScript/Python) │  │
    │  └──────┬───────┘  └──────┬───────┘  └──────────┬───────────┘  │
    │         │                 │                      │               │
    └─────────┼─────────────────┼──────────────────────┼──────────────┘
              │                 │                      │
              │   HTTP/SSE (primary) ─────────────────┘
              │   stdio (secondary, Phase 3)
              └─────────────────┼────────────────────────
                                │ JSON-RPC 2.0
                                ▼
    ┌─────────────────────────────────────────────────────────────────┐
    │                    GAMS — Single Binary                          │
    │                                                                 │
    │  ┌──────────────────────────────────────────────────────────┐  │
    │  │            MCP Server Layer (HTTP/SSE + stdio)            │  │
    │  │   Streamable HTTP: GET /sse · POST /message              │  │
    │  │   stdio: JSON-RPC 2.0 over stdin/stdout                  │  │
    │  └────────────────────────┬─────────────────────────────────┘  │
    │                           │                                     │
    │  ┌──────────────────────────────────────────────────────────┐  │
    │  │                    Core Engine                            │  │
    │  │  ┌─────────────┐  ┌──────────────┐  ┌───────────────┐   │  │
    │  │  │ Write Path  │  │ Recall Path  │  │  Audit Path   │   │  │
    │  │  │ (remember)  │  │  (recall)    │  │   (log all)   │   │  │
    │  │  └──────┬──────┘  └──────┬───────┘  └───────┬───────┘   │  │
    │  │         │                │                   │            │  │
    │  │  ┌──────▼──────────────────────────────────▼─────────┐   │  │
    │  │  │              Claim Store (SQLite WAL)              │   │  │
    │  │  │  tenants | namespaces | api_keys                  │   │  │
    │  │  │  claims | claim_versions | evidence               │   │  │
    │  │  │  audit_log | recall_index (FTS5)                  │   │  │
    │  │  └───────────────────────────────────────────────────┘   │  │
    │  └──────────────────────────────────────────────────────────┘  │
    │                                                                 │
    │  Config: single TOML file or env vars. No external services.   │
    └─────────────────────────────────────────────────────────────────┘

### 2.2 Data Flow: remember

    Agent → MCP remember(claim)
      → Auth middleware (verify API key, resolve tenant)
      → Input validation (size, encoding, required fields)
      → Idempotency check (hash of canonical content per tenant)
      → Write path:
          BEGIN IMMEDIATE transaction
            INSERT INTO claim_versions (new content + metadata)
            UPDATE claims (bump version pointer, update confidence)
            INSERT INTO audit_log (WRITE event)
            UPDATE recall_index (FTS5 upsert)
          COMMIT
      → Return {claim_id, version, created_at}

### 2.3 Data Flow: recall

    Agent → MCP recall(query, filters)
      → Auth middleware
      → Recall engine:
          1. FTS5 full-text search against recall_index (SQLite built-in)
          2. Filter by: tenant, tags, confidence_min, recency window
          3. Score = weighted sum (text_rank * 0.5 + confidence * 0.3 + recency * 0.2)
          4. Return top-K claims (default K=10, configurable)
      → INSERT INTO audit_log (READ event)
      → Return [{claim_id, content, confidence, evidence, version, ...}]

### 2.4 Data Flow: forget

    Agent → MCP forget(claim_id, reason)
      → Auth middleware
      → Authorization check (only writer or tenant admin may forget)
      → Soft delete: UPDATE claims SET status='forgotten', forgotten_at=now, forgotten_reason=...
          — Version history and audit log are NEVER deleted
      → INSERT INTO audit_log (FORGET event)
      → Remove from FTS5 index
      → Return {claim_id, forgotten_at}

│ Important
│ forget is a soft delete. The claim row, all its versions, and all audit log entries
│ are permanently retained. Only the FTS index entry is removed, and the status field
│ changes. This is required to maintain audit integrity. Physical deletion is a separate,
│ explicitly authorized admin operation (GDPR purge) and is out of scope for v1.
──────

## 3. Architecture Decisions (with Reasoning)

### Decision 1: Concurrent Write Strategy

Chosen: Optimistic Concurrency with SQLite WAL mode + IMMEDIATE transactions

Options considered:

| Approach                     | Pros                             | Cons                                                                 |
| ---------------------------- | -------------------------------- | -------------------------------------------------------------------- |
| Full event sourcing          | Perfect audit trail, time travel | Enormous complexity; projection maintenance; solo-dev unmaintainable |
| Pessimistic locking          | Simple to reason about           | Kills concurrent throughput; blocking under any load                 |
| Optimistic concurrency + WAL | Simple, SQLite-native, auditable | Requires callers to handle version conflicts; WAL throughput limit   |
| Distributed CRDTs            | No conflicts at all              | Requires distributed runtime; absurd scope                           |

Reasoning:

SQLite in WAL (Write-Ahead Log) mode supports one writer and many concurrent readers without blocking. BEGIN IMMEDIATE serializes writers at the database level. This means:

• Concurrent reads are always non-blocking.
• Concurrent writes from multiple agents queue behind each other, but SQLite WAL write serialization is microseconds for these workloads (small JSON blobs, not bulk ingestion).
• We add an optimistic check: each remember call may optionally include the base_version it was built on. If the claim was updated by another writer since base_version, the server returns a 409 Conflict with the current state. The client retries.

Throughput expectation (established practice): SQLite WAL handles 10,000+ write transactions per second on commodity hardware for small-row workloads. Agent memory writes are infrequent (dozens to low hundreds per session). This is not a database stress scenario; it is a ledger scenario.

Why not PostgreSQL? It violates the single-binary, no-required-cloud-services constraint. Go's modernc.org/sqlite (pure Go, no CGo) compiles the SQLite amalgamation directly into the binary. No system library dependency.

Why not event sourcing? It is the philosophically correct answer for an append-only audit log, and we borrow its spirit (immutable versions table, append-only audit log), but a full event sourcing architecture requires projection rebuilds, snapshot management, and event schema versioning —
each a weekend-killing rabbit hole. We get 90% of the benefit (full history, audit trail, point-in-time replay of versions) with 10% of the complexity.
──────

### Decision 2: Recall Ranking Strategy

Chosen: Hybrid — FTS5 BM25 text relevance + confidence weight + recency decay, no external embedding service

Options considered:

| Strategy                           | Pros                                                        | Cons                                                                  |
| ---------------------------------- | ----------------------------------------------------------- | --------------------------------------------------------------------- |
| Pure keyword / BM25                | Zero dependencies, built into SQLite FTS5                   | Misses semantic synonyms                                              |
| Pure semantic (embedding vectors)  | Catches semantic similarity                                 | Requires embedding model/service; violates no-GPU, no-cloud principle |
| Hybrid BM25 + confidence + recency | No external service; captures what matters for agent memory | May miss paraphrase matches                                           |
| LLM reranking                      | Highest quality                                             | Requires model call; breaks offline operation; latency                |

Scoring formula (first principles):

    score = (bm25_rank_normalized * 0.5)
          + (confidence * 0.3)
          + (recency_score * 0.2)

    recency_score = exp(-λ * days_since_update)
      where λ = 0.05 (half-life ~14 days, configurable per tenant)

Reasoning:

The primary recall signal for agent memory is topical relevance, not semantic similarity. An agent asking "what did we decide about the database schema?" will use words like "database," "schema," "decided." BM25 handles this well. The differentiator from a plain vector DB is the confidence
and recency signals: a claim written yesterday with confidence 0.9 should rank above a claim written six months ago with confidence 0.4, even if they are equally text-relevant.

Semantic recall path (deferred to v2): The schema is designed to add a embedding BLOB column to claims and use SQLite-vec (a SQLite extension for vector search) without breaking existing data. This is explicitly v2 and requires the operator to opt in by providing an embedding service. It is
not a v1 constraint.

Opinion flag: I am reasoning from first principles here. The 0.5/0.3/0.2 weights are a starting hypothesis; they should be tunable per-tenant from day one so real usage can inform revision.
──────

### Decision 3: Versioning and Invalidation

Mechanism: Explicit Contradiction Protocol

When new evidence contradicts an existing claim, the system does not automatically modify the old claim. Instead:

1. The writing agent calls remember with a new claim that includes:
   • contradicts: [claim_id_A, claim_id_B] — an array of claim IDs being contradicted.
   • Its own evidence links justifying the contradiction.
2. The write path:
   • Inserts the new claim normally.
   • For each contradicted claim: UPDATE claims SET status='contradicted', contradicted_by=<new_claim_id> and records a version history entry with the contradiction relationship.
   • Inserts audit log entries for each contradiction event.
3. Contradicted claims are excluded from recall results by default. They are visible via an explicit include_contradicted=true flag.

Why not automatic confidence decay or LLM-adjudicated merging?

Automatic decay requires knowing when two claims are about the same thing — a semantic matching problem that requires embeddings or an LLM call, violating the no-external-service constraint. LLM adjudication is expensive, non-deterministic, and impossible to audit. The contradiction
protocol puts the decision in the hands of the agent (or human operator), where it belongs. This is a governance choice, not a technical limitation.

Version history model: Every write to a claim creates a new row in claim_versions. The claims table holds only current-state metadata (a pointer to the current version). Point-in-time queries reconstruct state from the versions table. This is event-sourcing spirit without full event-
sourcing infrastructure.
──────

### Decision 4: Tenant Isolation and Auth — Threat Model

Threat model for a self-hosted, multi-agent setup:

We are defending against:

1. Agent A reading Agent B's memory — the primary multi-tenant threat. An agent operating in a different context (different user, different project) must not be able to see another tenant's claims.
2. A compromised agent key writing to another tenant — a key leaked from one agent cannot be used to pollute another tenant's memory.
3. Replay attacks — a valid request captured on the wire cannot be replayed later.
4. Accidental namespace collisions — two agents that are legitimately different but happen to share a namespace must be explicitly separated.

We are explicitly NOT defending against:

• A malicious server operator (you own the hardware; the server is not the adversary).
• Nation-state-level key compromise (out of scope for local-first infrastructure).
• Agents that share the same API key and choose to share memory (this is a feature, not a threat).

Auth mechanism: Static API Keys + HMAC request signing (v1)

• Each tenant is issued one or more API keys on registration.
• Keys are gams*<tenant_id>*<random_256_bits> — structured for debuggability.
• All requests carry the key in the Authorization: Bearer <key> header.
• Keys are stored as bcrypt hashes in the database (so a DB dump does not expose live keys).
• Request signing: optional HMAC-SHA256 over (method + path + body_hash + timestamp). Timestamp window of ±5 minutes prevents replay. Signing is optional in v1 (required only if the operator enables it in config) to keep SDK integration simple.
• Admin operations (create tenant, list tenants, issue keys) require a separate admin_key set at server startup via env var.

Tenant isolation in SQLite: Every query in the write path and read path includes AND tenant_id = ? as a mandatory predicate. This is enforced in the core engine layer, not in the MCP layer. The MCP layer passes a resolved tenant_id struct; it never builds raw SQL.
──────

### Decision 5: MCP Surface

See Section 5 for full contract. Design philosophy:

• Expose the minimum surface to be useful. Four tools (remember, recall, forget, inspect) plus two resources. Do not expose admin operations via MCP.
• MCP tools are the agent-facing interface. A separate REST/HTTP admin API is for human operators only.
• Primary transport: Streamable HTTP (SSE + POST, MCP 2025-03 spec). This enables GAMS to run as a long-lived network service accessible by multiple agents and processes simultaneously — the correct posture for a memory server. A memory server that can only be attached by one process at a
time (stdio) is architecturally wrong.
• Secondary transport: stdio (Phase 3). Added for local MCP hosts (Claude Code, Cursor) that prefer subprocess attachment. stdio is a convenience layer; it routes through the same core engine. Both transports are first-class citizens after Phase 3.
• A memory server that only speaks stdio cannot serve multiple concurrent agents. HTTP-first is the right default for the product's purpose.
──────

### Decision 6: What Is Explicitly OUT of v1

| Feature                                        | Reason Out                                                                                                        |
| ---------------------------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| Semantic / embedding-based recall              | Requires external embedding service or local model; violates single-binary, no-GPU constraint                     |
| Real-time push notifications / webhooks        | Adds async infrastructure complexity disproportionate to v1 value                                                 |
| Web UI / dashboard                             | A solo developer's time is better spent on the core contract; CLI admin commands suffice                          |
| Multi-node / distributed operation             | SQLite is not a distributed database; this requires a complete architecture change (v3+)                          |
| GDPR / right-to-erasure hard delete            | Requires careful legal review of what "erasure" means in an audit log; defer to v2 with explicit operator tooling |
| Claim merging / deduplication                  | Requires semantic matching; chicken-and-egg with embedding support                                                |
| Fine-grained RBAC (per-claim permissions)      | Tenant-level isolation is sufficient for v1; per-claim ACLs add schema and query complexity                       |
| Streaming recall results                       | Useful but not a blocker; recall sets are small                                                                   |
| SDK for languages beyond TypeScript and Python | Low leverage in v1; Go clients can use the REST API directly                                                      |

──────

## 4. Data Model

### 4.1 Schema (conceptual — not DDL, no code)

#### tenants

| Column                 | Type           | Notes                                                                                                                                                                     |
| ---------------------- | -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------- |
| id                     | TEXT (UUID v7) | Primary key. UUID v7 is time-ordered, debuggable, and lexicographically sortable — better than v4 for database keys.                                                      |
| name                   | TEXT           | Human-readable label                                                                                                                                                      |
| status                 | TEXT           | active                                                                                                                                                                    | suspended |
| recall_decay_lambda    | REAL           | Per-tenant recency decay parameter (default 0.05)                                                                                                                         |
| max_claims             | INTEGER        | Soft cap; 0 = unlimited                                                                                                                                                   |
| auto_create_namespaces | BOOLEAN        | If true, writing to a non-existent namespace slug auto-creates it with access_mode = tenant_wide. Default: false. When false, writing to a missing namespace returns 404. |
| created_at             | INTEGER        | Unix nanoseconds                                                                                                                                                          |
| metadata               | TEXT (JSON)    | Arbitrary operator-defined key/value                                                                                                                                      |

#### namespaces (first-class entity — Q2 resolved)

│ Note
│ Namespaces are the sub-unit of isolation within a tenant. A tenant is a
│ billing/identity boundary (e.g., "acme-corp"). A namespace is a project,
│ environment, or context boundary within that tenant (e.g., "project-alpha",
│ "staging", "user-42-personal"). Claims always belong to exactly one namespace.

| Column              | Type           | Notes                                                                            |
| ------------------- | -------------- | -------------------------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| id                  | TEXT (UUID v7) | Primary key                                                                      |
| tenant_id           | TEXT           | Foreign key → tenants                                                            |
| slug                | TEXT           | URL-safe, human-readable identifier (e.g., project-alpha). Unique within tenant. |
| name                | TEXT           | Display name (may differ from slug)                                              |
| description         | TEXT           | Nullable; human-readable purpose                                                 |
| status              | TEXT           | active                                                                           | archived. Archived namespaces are read-only: recall works, remember returns 403. |
| access_mode         | TEXT           | tenant_wide                                                                      | key_restricted. See access control note below.                                   |
| recall_decay_lambda | REAL           | Nullable; overrides tenant-level decay for this namespace                        |
| max_claims          | INTEGER        | Namespace-level soft cap; 0 = inherit from tenant                                |
| created_at          | INTEGER        | Unix nanoseconds                                                                 |
| updated_at          | INTEGER        | Unix nanoseconds                                                                 |
| metadata            | TEXT (JSON)    | Arbitrary operator-defined key/value                                             |

Namespace access control:

Two modes, controlled by access_mode:

• tenant_wide (default): Any API key in the tenant can read/write this namespace. This is the common case — all agents in a project share memory freely.
• key_restricted: Only API keys explicitly granted access may read/write this namespace. Grants are stored in namespace_grants (see below). Use this when you need agent-level isolation within a shared tenant (e.g., agent A's private memory vs. shared project memory).

#### namespace_grants

| Column       | Type           | Notes                              |
| ------------ | -------------- | ---------------------------------- | ----- | -------------------------------------------------------------------- |
| id           | TEXT (UUID v7) | Primary key                        |
| namespace_id | TEXT           | Foreign key → namespaces           |
| api_key_id   | TEXT           | Foreign key → api_keys             |
| permission   | TEXT           | read                               | write | admin (write implies read; admin implies write + can archive/rename) |
| granted_at   | INTEGER        | Unix nanoseconds                   |
| granted_by   | TEXT           | Admin key id that issued the grant |

│ Important
│ namespace_grants only applies when a namespace's access_mode is key_restricted.
│ When access_mode is tenant_wide, this table is ignored for authorization decisions.
│ This avoids the footgun of a tenant admin forgetting to add grants and
│ accidentally locking themselves out.

#### api_keys

| Column       | Type           | Notes                                  |
| ------------ | -------------- | -------------------------------------- | ------- |
| id           | TEXT (UUID v7) | Primary key                            |
| tenant_id    | TEXT           | Foreign key → tenants                  |
| key_hash     | TEXT           | bcrypt hash of the raw key             |
| label        | TEXT           | Human label (e.g., "claude-code-prod") |
| status       | TEXT           | active                                 | revoked |
| last_used_at | INTEGER        | Unix nanoseconds; updated on each use  |
| expires_at   | INTEGER        | Nullable; 0 = never                    |
| created_at   | INTEGER        | Unix nanoseconds                       |

#### claims

| Column           | Type              | Notes                                                                          |
| ---------------- | ----------------- | ------------------------------------------------------------------------------ | ------------ | --------- |
| id               | TEXT (UUID v7)    | Primary key                                                                    |
| tenant_id        | TEXT              | Foreign key → tenants                                                          |
| namespace_id     | TEXT              | Foreign key → namespaces (not a raw string — Q2 resolved)                      |
| current_version  | INTEGER           | Monotonic version counter; starts at 1                                         |
| content          | TEXT              | The claim statement in plain text                                              |
| content_hash     | TEXT              | SHA-256 of canonical(content + tenant_id + namespace_id); used for idempotency |
| confidence       | REAL              | 0.0–1.0; writer-provided; semantics defined below                              |
| status           | TEXT              | active                                                                         | contradicted | forgotten |
| contradicted_by  | TEXT              | Nullable; claim_id that contradicts this one                                   |
| writer_id        | TEXT              | API key id of the creator                                                      |
| writer_label     | TEXT              | Denormalized label for readability in audit log                                |
| tags             | TEXT (JSON array) | Flat list of string tags for filtering                                         |
| created_at       | INTEGER           | Unix nanoseconds                                                               |
| updated_at       | INTEGER           | Unix nanoseconds                                                               |
| forgotten_at     | INTEGER           | Nullable                                                                       |
| forgotten_reason | TEXT              | Nullable                                                                       |
| metadata         | TEXT (JSON)       | Arbitrary agent-defined key/value                                              |

On confidence: Agent-provided and not validated beyond range-checking. Semantics: 1.0 = verified fact, 0.5 = working assumption, 0.0 = pure speculation. The server treats it as an opaque ranking signal.

│ Note
│ namespace_id replaces the former free-form namespace string column. Agents now
│ reference namespaces by their UUID or slug. The slug is the ergonomic handle; the
│ engine always resolves to UUID internally. This means namespace renames don't break
│ existing claims, and namespace access control is enforced consistently.

#### claim_versions

| Column        | Type              | Notes                                                                 |
| ------------- | ----------------- | --------------------------------------------------------------------- |
| id            | TEXT (UUID v7)    | Primary key                                                           |
| claim_id      | TEXT              | Foreign key → claims                                                  |
| tenant_id     | TEXT              | Denormalized for query efficiency                                     |
| namespace_id  | TEXT              | Denormalized for query efficiency                                     |
| version       | INTEGER           | Monotonic per-claim                                                   |
| content       | TEXT              | Snapshot of content at this version                                   |
| confidence    | REAL              | Snapshot of confidence at this version                                |
| contradicts   | TEXT (JSON array) | Claim IDs this version contradicts (may be empty)                     |
| writer_id     | TEXT              | Who wrote this version                                                |
| written_at    | INTEGER           | Unix nanoseconds                                                      |
| change_reason | TEXT              | Optional; writer-provided description of why this version was written |

#### evidence

| Column        | Type           | Notes                                                             |
| ------------- | -------------- | ----------------------------------------------------------------- | ---- | ----------- | --------------- | ----- |
| id            | TEXT (UUID v7) | Primary key                                                       |
| claim_id      | TEXT           | Foreign key → claims                                              |
| claim_version | INTEGER        | Which version of the claim this evidence supports                 |
| tenant_id     | TEXT           | Denormalized                                                      |
| uri           | TEXT           | Source URI (URL, file path, tool output ID, etc.)                 |
| description   | TEXT           | Human-readable description of the source                          |
| evidence_type | TEXT           | url                                                               | file | tool_output | agent_assertion | human |
| captured_at   | INTEGER        | When the evidence was captured (may differ from claim write time) |
| metadata      | TEXT (JSON)    | e.g., HTTP response code, content hash of URL at capture time     |

#### audit_log

| Column         | Type           | Notes                                                     |
| -------------- | -------------- | --------------------------------------------------------- | ---- | ------ | ---------- | ---------- | ----------- | -------------- | ----------------- | ------------------ | ------------ | ------------- |
| id             | TEXT (UUID v7) | Primary key; time-ordered                                 |
| tenant_id      | TEXT           | Foreign key → tenants                                     |
| namespace_id   | TEXT           | Nullable; denormalized for namespace-scoped audit queries |
| event_type     | TEXT           | WRITE                                                     | READ | FORGET | CONTRADICT | KEY_ISSUED | KEY_REVOKED | TENANT_CREATED | NAMESPACE_CREATED | NAMESPACE_ARCHIVED | GRANT_ISSUED | GRANT_REVOKED |
| actor_key_id   | TEXT           | API key id of the actor                                   |
| actor_label    | TEXT           | Denormalized                                              |
| claim_id       | TEXT           | Nullable; which claim the event is about                  |
| claim_version  | INTEGER        | Nullable; version at time of event                        |
| payload        | TEXT (JSON)    | Event-specific details                                    |
| occurred_at    | INTEGER        | Unix nanoseconds                                          |
| server_version | TEXT           | GAMS binary version that recorded this event              |

│ Caution
│ The audit_log table must never be writable by any code path that handles
│ agent requests. It is append-only and written only by the core engine's
│ internal audit pathway. No MCP tool, no REST endpoint, touches it directly.

#### recall_index (FTS5 virtual table)

| Column         | Notes                                                                                                |
| -------------- | ---------------------------------------------------------------------------------------------------- |
| claim_id       | Linked to claims table                                                                               |
| tenant_id      | For mandatory WHERE filter (not FTS-searched)                                                        |
| namespace_id   | For namespace-scoped queries (not FTS-searched)                                                      |
| content        | The text to be indexed                                                                               |
| tags           | Tokenized tag list included in full-text index                                                       |
| namespace_slug | Included in FTS so agents can query "project-alpha authentication" and get namespace-boosted results |

SQLite FTS5 with the built-in unicode61 tokenizer. No external tokenizer or NLP library required.

### 4.2 Index Strategy

• namespaces(tenant_id, slug) — slug resolution (unique constraint)
• namespaces(tenant_id, status) — list active namespaces
• namespace_grants(namespace_id, api_key_id) — access check hot path
• claims(tenant_id, namespace_id, status, updated_at) — primary recall filter
• claims(tenant_id, namespace_id, content_hash) — idempotency check
• claim_versions(claim_id, version) — history traversal
• evidence(claim_id) — evidence lookup per claim
• audit_log(tenant_id, occurred_at) — chronological audit queries
• audit_log(tenant_id, namespace_id, occurred_at) — namespace-scoped audit
• audit_log(claim_id) — per-claim audit trail
• api_keys(key_hash) — auth hot path

### 4.3 Authorization Flow (with namespace auto-create, Q9 resolved)

    Request arrives with API key
      → bcrypt verify key (LRU-cached; cache TTL = auth.key_cache_ttl_seconds)
      → Resolve tenant_id from key
      → Resolve namespace from request (by slug or id)
          → If namespace not found AND tenant.auto_create_namespaces = true:
                INSERT INTO namespaces (slug, access_mode='tenant_wide', status='active')
                INSERT INTO audit_log (NAMESPACE_CREATED, auto_created=true)
                → Continue with new namespace
          → If namespace not found AND tenant.auto_create_namespaces = false:
                → Return 404 Not Found {"error": "namespace_not_found", "slug": "..."}
      → If namespace.status = 'archived' AND operation is WRITE:
          → Return 403 Forbidden {"error": "namespace_archived"}
      → If namespace.access_mode = 'tenant_wide': ALLOW
      → If namespace.access_mode = 'key_restricted':
          → Check namespace_grants for (namespace_id, api_key_id, permission >= required)
          → If no grant: 403 Forbidden {"error": "namespace_access_denied"}
      → Proceed to engine

│ Tip
│ During development, set auto_create_namespaces = true on the tenant via the admin API
│ to enable frictionless namespace creation. In production, leave it false and use the
│ SDK's ensure_namespace(slug) helper in your agent's startup sequence to create
│ namespaces explicitly before writing to them.
──────

## 5. API / MCP Contract

### 5.1 Transport

Primary: Streamable HTTP (MCP 2025-03 spec)

• GAMS runs as a long-lived HTTP server on a configurable port (default: 8080).
• GET /mcp/sse — Server-Sent Events stream; agent subscribes and receives server-initiated messages.
• POST /mcp/message — Agent sends JSON-RPC 2.0 requests; responses arrive on the SSE stream.
• Multiple agents from multiple processes connect simultaneously. This is the architecture's core value: a shared memory server, not a per-agent sidecar.
• TLS termination is the operator's responsibility (e.g., Caddy, nginx, or Cloudflare Tunnel for external access). GAMS does not own TLS in v1.

Secondary: stdio (Phase 3)

• For local MCP hosts (Claude Code, Cursor) that prefer subprocess attachment.
• GAMS runs as a child process; the MCP host communicates over stdin/stdout.
• Same JSON-RPC 2.0 framing; same tool/resource surface; same engine underneath.
• Limitation: one stdio session = one client. For multi-agent local setups, use the HTTP transport instead.
• Enabled by mcp.transport = "stdio" or "both" in config.

MCP Server Identifier (Q3 resolved):

The MCP initialization handshake (initialize request/response) carries:

    {
      "name":        "gams",
      "displayName": "GAMS",
      "description": "Governed Agent Memory Server",
      "version":     "<semver>",
      "capabilities": {
        "tools":     { "listChanged": false },
        "resources": { "subscribe": false, "listChanged": false }
      }
    }

• name: "gams" — the stable protocol identifier used in configs, logs, and tool schemas.
• displayName: "GAMS" — shown in Claude Code's MCP panel, Cursor's tool list.
• description: "Governed Agent Memory Server" — full human-readable description shown in tool discovery UIs.

Claude Code HTTP config example:

    { "gams": { "url": "http://localhost:8080/mcp/sse" } }

Claude Code stdio config example (Phase 3):

    { "gams": { "command": "gams", "args": ["serve", "--transport", "stdio"] } }

### 5.2 MCP Tools (Agent-Facing)

#### remember

Purpose: Store or update a claim in the memory of the calling agent's tenant.

Input schema:

    {
      content:          string (required) — the claim statement
      confidence:       number [0.0–1.0] (required)
      evidence:         array of {
                          uri: string,
                          description: string,
                          evidence_type: enum (url|file|tool_output|agent_assertion|human),
                          captured_at?: ISO8601 timestamp
                        } (required, min 1 item)
      contradicts?:     array of claim_id strings
      tags?:            array of strings
      namespace?:       string — namespace slug (e.g. "project-alpha"). Must already exist.
                        If omitted, writes to the tenant's default namespace.
      base_version?:    integer — for optimistic concurrency check
      change_reason?:   string
      metadata?:        object
    }

Output schema:

    {
      claim_id:    string (UUID v7)
      version:     integer
      created_at:  ISO8601
      status:      "created" | "updated" | "conflict"
      conflict?:   { current_version: integer, updated_at: ISO8601 }
    }

Error cases: 400 malformed input, 401 invalid key, 403 tenant suspended, 409 base_version conflict (client should re-read and retry).
──────

#### recall

Purpose: Retrieve the most relevant claims for a query.

Input schema:

    {
      query:                 string (required) — natural language or keyword query
      limit?:                integer (default 10, max 100)
      confidence_min?:       number [0.0–1.0] (default 0.0)
      tags?:                 array of strings (AND filter)
      namespace?:            string — namespace slug; if omitted, searches across all
                             namespaces the calling key has access to
      include_contradicted?: boolean (default false)
      include_forgotten?:    boolean (default false)
      recency_window_days?:  integer — exclude claims older than N days (0 = no limit)
    }

Output schema:

    {
      claims: array of {
        claim_id:        string
        content:         string
        confidence:      number
        version:         integer
        status:          string
        score:           number (composite recall score, 0.0–1.0)
        tags:            array of strings
        namespace_slug:  string         — human-readable namespace slug
        namespace_id:    string         — stable UUID; use this for follow-up operations
        evidence:        array of { uri, description, evidence_type, captured_at }
        writer_label:    string
        created_at:      ISO8601
        updated_at:      ISO8601
      }
      total_matched: integer
      query_ms:      integer
    }

──────

#### forget

Purpose: Mark a claim as forgotten (soft delete).

Input schema:

    {
      claim_id: string (required)
      reason:   string (required) — why this claim is being forgotten
    }

Output schema:

    {
      claim_id:    string
      forgotten_at: ISO8601
    }

Authorization: Only the original writer's key or a tenant-admin key may forget a claim.
──────

#### inspect (MCP Tool, not just a Resource)

Purpose: Retrieve a single claim's full history, evidence, and audit trail.

Input schema:

    {
      claim_id: string (required)
    }

Output schema:

    {
      claim:    { ...full claim fields... }
      versions: array of { version, content, confidence, writer_label, written_at, change_reason, contradicts }
      evidence: array of { ...all evidence items across all versions... }
      audit:    array of { event_type, actor_label, occurred_at, payload }
    }

This is the governance tool. It lets an agent (or a human operator) see the full lineage of a claim — who wrote what, when, why, and what evidence backed it.
──────

### 5.3 MCP Resources

MCP Resources (read-only, URI-addressed) are appropriate for data that agents browse rather than query with parameters.

#### gams://claims/{claim_id}

Returns the current state of a single claim (same shape as a single item in recall output). Allows agents to reference a claim by stable URI in their context.

#### gams://namespaces/{namespace_slug}

Returns namespace metadata: name, description, status, access_mode, claim count by status, most recently written claim. Allows agents to verify they're writing to the right namespace before issuing a remember call.

#### gams://tenants/{tenant_id}/summary

Returns aggregate statistics across all namespaces: total claim count by status, write activity in the last 7/30 days, most active namespaces. Intended for operator use; not typically useful in an agent's working context.
──────

### 5.4 Admin REST API (Human Operator, Not MCP)

Thin HTTP API for server administration. Not exposed over MCP. Requires the admin_key credential set at startup.

Tenant management:

| Method | Path                | Purpose                                            |
| ------ | ------------------- | -------------------------------------------------- |
| POST   | /admin/tenants      | Create a tenant (auto-creates a default namespace) |
| GET    | /admin/tenants      | List tenants                                       |
| GET    | /admin/tenants/{id} | Get tenant details                                 |
| PATCH  | /admin/tenants/{id} | Update tenant (status, max_claims, metadata)       |

Namespace management:

| Method | Path                                                  | Purpose                                                   |
| ------ | ----------------------------------------------------- | --------------------------------------------------------- |
| POST   | /admin/tenants/{id}/namespaces                        | Create a namespace                                        |
| GET    | /admin/tenants/{id}/namespaces                        | List namespaces for a tenant                              |
| GET    | /admin/tenants/{id}/namespaces/{slug}                 | Get namespace details                                     |
| PATCH  | /admin/tenants/{id}/namespaces/{slug}                 | Update namespace (name, description, access_mode, status) |
| POST   | /admin/tenants/{id}/namespaces/{slug}/grants          | Issue a key-restricted grant                              |
| DELETE | /admin/tenants/{id}/namespaces/{slug}/grants/{key_id} | Revoke a grant                                            |

Key management:

| Method | Path                              | Purpose                                                          |
| ------ | --------------------------------- | ---------------------------------------------------------------- |
| POST   | /admin/tenants/{id}/keys          | Issue an API key                                                 |
| DELETE | /admin/tenants/{id}/keys/{key_id} | Revoke a key                                                     |
| GET    | /admin/tenants/{id}/keys          | List keys (shows label, status, last_used_at; never the raw key) |

Audit and operations:

| Method | Path                                        | Purpose                                                               |
| ------ | ------------------------------------------- | --------------------------------------------------------------------- |
| GET    | /admin/tenants/{id}/audit                   | Paginated audit log (filterable by namespace, event_type, time range) |
| GET    | /admin/tenants/{id}/namespaces/{slug}/audit | Namespace-scoped audit log                                            |
| GET    | /health                                     | Liveness check (no auth required)                                     |
| GET    | /metrics                                    | Prometheus-format metrics (auth required)                             |

│ Note
│ Tenant creation automatically creates a default namespace with access_mode = tenant_wide.
│ Agents that omit the namespace field in remember / recall write to and search
│ the default namespace. This keeps the zero-config experience intact while enabling
│ namespace isolation when needed.
──────

### 5.5 Agent REST API (Q5 resolved — SDK backing interface)

│ Important
│ This section is the architectural consequence of Q5's decision. The @gams/client SDK
│ uses a core REST client as its primary interface, with MCP as an adapter layer on top.
│ This means GAMS must expose a thin, agent-authenticated REST API in addition to the MCP
│ surface. Both are backed by the same core engine. The REST API is not a duplicate — it is
│ the same operations with simpler framing, suitable for scripts, tests, CI, and non-MCP agents.

All endpoints require Authorization: Bearer <api_key> with a tenant API key (not the admin key).

Base path: /v1/

| Method | Path                   | Purpose                                                          | Mirrors MCP tool                  |
| ------ | ---------------------- | ---------------------------------------------------------------- | --------------------------------- |
| POST   | /v1/claims             | Store or update a claim                                          | remember                          |
| POST   | /v1/recall             | Retrieve relevant claims                                         | recall                            |
| GET    | /v1/claims/{id}        | Get full claim state + history                                   | inspect                           |
| POST   | /v1/claims/{id}/forget | Soft-delete a claim                                              | forget                            |
| GET    | /v1/namespaces         | List accessible namespaces                                       | —                                 |
| POST   | /v1/namespaces         | Create a namespace (if auto_create=false, SDK helper calls this) | —                                 |
| GET    | /v1/namespaces/{slug}  | Get namespace metadata + stats                                   | gams://namespaces/{slug} resource |

Request/response shapes are identical to the MCP tool schemas in §5.2. The only difference is transport framing: JSON over plain HTTP instead of JSON-RPC 2.0 over SSE/stdio.

SDK architecture (TypeScript @gams/client, ESM-only, Node ≥ 18):

    @gams/client
    ├── (default export)   GAMSClient — core REST client
    │     • constructor({ baseUrl, apiKey })
    │     • remember(input): Promise<RememberResult>
    │     • recall(input): Promise<RecallResult>
    │     • forget(claimId, reason): Promise<ForgetResult>
    │     • inspect(claimId): Promise<InspectResult>
    │     • ensureNamespace(slug, opts?): Promise<Namespace>
    │
    └── /mcp              GAMSMCPAdapter — wraps GAMSClient as MCP tool handlers
          • constructor(client: GAMSClient)
          • getToolDefinitions(): MCPToolDefinition[]   — returns JSON Schema for all tools
          • handleToolCall(name, args): Promise<MCPToolResult>
          • (Use this to add GAMS memory to your own MCP server without forking logic)

The /mcp submodule is for developers who are building custom MCP servers and want to delegate memory tool calls to GAMS. It is not what Claude Code uses — Claude Code connects to the GAMS MCP server directly.

SDK architecture (Python gams-client, asyncio-first + sync wrapper, httpx/openai pattern):

    gams_client
    ├── AsyncGAMSClient   — primary, asyncio
    │     • async remember(...)
    │     • async recall(...)
    │     • async forget(...)
    │     • async inspect(...)
    │     • async ensure_namespace(slug, ...)
    │
    ├── GAMSClient        — synchronous wrapper
    │     • Uses asyncio.run() internally
    │     • Same method signatures, no async/await required
    │     • Not safe to use from inside an existing event loop (use AsyncGAMSClient there)
    │
    └── types             — Pydantic v2 models for all request/response shapes

│ Note
│ The Python sync wrapper follows the httpx and openai-python patterns exactly: the sync
│ class delegates to the async class via asyncio.run(). This means zero duplicated logic.
│ The documentation will mirror openai-python's pattern of showing async examples first,
│ with a sync equivalent below. Agent framework users (LangChain, LlamaIndex, PydanticAI)
│ will use AsyncGAMSClient; script authors will use GAMSClient.
──────

## 6. Component Reference

### 6.1 Binary Structure (Go packages — conceptual)

    cmd/gams/          — main entrypoint; flag parsing, config loading, signal handling
    internal/
      config/          — TOML config loader, env var overrides, validation
      db/              — SQLite connection pool, schema migrations, query wrappers
      engine/          — Core: remember, recall, forget, inspect business logic
      namespaces/      — Namespace CRUD, access_mode enforcement, grant management
      mcp/             — MCP protocol implementation: JSON-RPC dispatch, tool registration
      transport/
        http/          — Streamable HTTP transport (SSE + POST); primary
        stdio/         — stdio transport; secondary (Phase 3)
      api/             — Agent REST API handlers (/v1/claims, /v1/recall, /v1/namespaces)
                         Same engine calls as mcp/; different framing only
      admin/           — Admin REST API handlers (/admin/tenants, namespaces, keys, audit)
      auth/            — API key verification, bcrypt LRU cache, tenant+namespace resolution
      audit/           — Append-only audit logger (internal use only)
      scoring/         — Recall scoring pipeline (BM25 normalization, decay, composite)
      migrations/      — Embedded SQL migration files (go:embed)
    pkg/
      types/           — Shared data types (Claim, Evidence, Namespace, AuditEvent, etc.)

│ Note
│ internal/api/ and internal/mcp/ are both thin adapters over the same
│ internal/engine/ functions. They share no logic with each other — they only share
│ the engine. This keeps the surface area clear: change a business rule in one place,
│ both interfaces see it.

Dependency philosophy: Minimize. Justify every external package.

| Package                                   | Justification                                                     |
| ----------------------------------------- | ----------------------------------------------------------------- |
| modernc.org/sqlite                        | Pure-Go SQLite; no CGo; single binary. Well-maintained.           |
| github.com/pelletier/go-toml/v2           | TOML config parsing. Smaller than Viper; no unnecessary sub-deps. |
| golang.org/x/crypto                       | bcrypt. Standard library extension; no alternative.               |
| github.com/google/uuid                    | UUID v7 generation. Pure Go; maintained by Google.                |
| Standard library only for everything else | HTTP server, JSON, SSE, logging (slog, Go 1.21+), testing         |

No web framework. net/http with net/http/httputil is sufficient. SSE is a trivial HTTP response type (Content-Type: text/event-stream; chunked transfer) — no library needed. No ORM. Raw SQL with database/sql.

### 6.2 Configuration

Single TOML file (default: gams.toml in the working directory, overridable by --config flag). All fields overridable by environment variables with GAMS\_ prefix.

Key configuration knobs:

• data.path — SQLite database file path
• server.http_port — MCP HTTP + Admin API port (default: 8080)
• server.admin_key — Admin API credential (required; no default; must be set explicitly)
• server.cors_origins — Allowed origins for browser-based callers
• mcp.transport — http | stdio | both (default: http)
• limits.max_claim_size_bytes — Default 65536
• limits.max_evidence_per_claim — Default 20
• limits.default_recall_limit — Default 10
• auth.signing_required — Boolean; enables HMAC request signing enforcement
• auth.key_cache_ttl_seconds — bcrypt LRU cache TTL (default: 300)

### 6.3 Migration Strategy

Embedded SQL migration files (using go:embed). On startup, GAMS checks a schema_migrations table and applies pending migrations in order. Migrations are numbered sequentially and are append-only (no down migrations in v1 — down migrations are a false safety net that creates more problems
than they solve for a solo-maintained project).
──────

## 7. Phased Implementation Plan

Each phase ends with something demoable and independently useful.
──────

### Phase 0: Foundation (2–3 weekends)

Goal: Binary that boots, creates a SQLite database with the full schema, and responds to health checks.

Work:

• Repo setup: Go module, directory structure, Makefile targets (build, test, lint, release)
• Config loading (TOML + env vars with GAMS\_ prefix)
• SQLite connection with WAL mode and initial schema: tenants, namespaces, namespace_grants, api_keys, claims, claim_versions, evidence, audit_log, recall_index
• Migration runner (numbered migrations, schema_migrations table, no down migrations)
• /health endpoint
• Structured logging with slog

Demo: ./gams serve --config gams.toml, curl /health, observe SQLite file created with all tables. Not exciting, but the schema is now locked in. Every subsequent phase builds on this.
──────

### Phase 1: Core Contract + Namespace Admin + Agent REST API (3–4 weekends)

Goal: remember / recall / forget work via both the admin REST API and the agent REST API (/v1/). Namespaces are fully operational. The REST API is the integration target for Phase 3's SDK.

Work:

• Auth middleware: API key verification (bcrypt + LRU cache), tenant resolution, namespace auto-create logic (auto_create_namespaces flag), namespace access check
• Tenant admin API: create tenant (auto-creates default namespace, sets auto_create_namespaces), list/get/update tenants
• Namespace admin API: create namespace, update access_mode/status, archive namespace, issue/revoke grants
• Agent REST API (/v1/): POST /v1/claims, POST /v1/recall, GET /v1/claims/{id}, POST /v1/claims/{id}/forget, GET /v1/namespaces, POST /v1/namespaces, GET /v1/namespaces/{slug}
• remember write path: namespace slug resolution → namespace_id FK, auto-create branch, idempotency check (per tenant+namespace), claim insert, version insert, evidence insert, audit log
• recall read path: FTS5 search, namespace filter (cross-namespace if omitted), scoring, audit log
• forget soft delete
• Unit tests for engine logic (table-driven, stdlib testing)

Demo: Via curl against /v1/: create a tenant (admin API), store claims in two namespaces, recall scoped to one. Then call GET /v1/namespaces and show both. Set auto_create_namespaces = true on the tenant via admin API and write to a new namespace without creating it first — observe it auto-
created in the response.
──────

### Phase 2: MCP Server — HTTP/SSE Transport (2–3 weekends)

Goal: GAMS is a working MCP server over HTTP/SSE. Multiple agents from different processes can connect simultaneously and share memory.

Work:

• Streamable HTTP transport: GET /mcp/sse (SSE stream) + POST /mcp/message (JSON-RPC 2.0)
• Session management: each SSE connection is a session; POST requests carry a session ID header
• JSON-RPC 2.0 dispatcher (hand-rolled; MCP protocol is simple enough)
• Tool registration: remember, recall, forget, inspect
• Resource registration: gams://claims/{id}, gams://namespaces/{slug}, gams://tenants/{id}/summary
• MCP initialization handshake: name: "gams", displayName: "GAMS", description: "Governed Agent Memory Server"
• CORS handling for browser-based agents
• End-to-end test: two concurrent agent sessions write and recall from the same namespace; verify isolation between namespaces

Demo: Start gams serve. Open two terminal windows, each running a script that acts as an MCP client over HTTP/SSE. Both scripts write claims to the same namespace. Each then recalls and sees each other's claims. This is the first proof that GAMS works as a shared memory server, not a
sidecar.

│ Note
│ This is the first milestone where the product becomes real. Everything before
│ this is infrastructure. The HTTP transport being primary means the demo shows
│ concurrent multi-agent memory — not just one agent's memory, which any
│ single-file solution could do. This is the differentiating screenshot.
──────

### Phase 3: stdio Transport + TypeScript SDK @gams/client (2–3 weekends)

Goal: Claude Code and Cursor can connect via stdio. @gams/client is published to npm with both the core REST client and the /mcp submodule adapter.

Work:

• stdio transport: JSON-RPC 2.0 over stdin/stdout; same dispatcher as HTTP; mcp.transport = "both" runs both simultaneously
• @gams/client (ESM-only, Node ≥ 18):
• GAMSClient (default export) — REST client targeting /v1/. Methods: remember, recall, forget, inspect, ensureNamespace
• @gams/client/mcp submodule — GAMSMCPAdapter wrapping GAMSClient: getToolDefinitions(), handleToolCall(). For developers embedding GAMS memory tools into their own MCP servers.
• Auto-retry on 409 conflict with exponential backoff (3 attempts, 100ms base)
• Full TypeScript types for all inputs and outputs
• ensureNamespace(slug, opts?) — calls POST /v1/namespaces if namespace does not exist; idempotent
• Publish to npm as @gams/client
• Claude Code integration test: configure Claude Code with gams as an MCP server using stdio ("command": "gams", "args": ["serve", "--transport", "stdio"])

Demo: Configure Claude Code to use GAMS via stdio. Ask Claude to remember something. End the session. Start a new session. Ask Claude to recall it. The amnesia problem is visibly solved. This is the README screenshot — not the Phase 2 demo, because Claude Code is the tool the primary
persona uses every day. Then show a second script using @gams/client directly (no model, no MCP) to read the same memory via REST — proving the two interfaces are interoperable.
──────

### Phase 4: Python SDK gams-client + Versioning UX (2 weekends)

Goal: gams-client published to PyPI with asyncio-first + sync wrapper pattern. Contradiction and version history are ergonomic.

Work:

• gams-client Python package (single package, asyncio-first):
• AsyncGAMSClient — primary interface; all methods are async; targets /v1/
• GAMSClient — sync wrapper; delegates to AsyncGAMSClient via asyncio.run(); not safe inside a running event loop (documented clearly)
• Response types as Pydantic v2 models (chosen over plain dataclasses: validation on ingress, .model_dump() for serialization, excellent LangChain/LlamaIndex/PydanticAI integration)
• async ensure_namespace(slug, ...) / ensure_namespace(slug, ...) sync variant
• Publish to PyPI as gams-client
• inspect tool complete: full version history + audit trail in both REST and MCP
• CLI admin command: gams admin claims inspect <id> — human-readable version history in terminal

│ Note
│ Why Pydantic v2 over plain dataclasses? The primary persona builds products with
│ agent frameworks. LangChain, LlamaIndex, and PydanticAI all have first-class Pydantic v2
│ support. Using Pydantic v2 models means GAMS response types compose naturally with those
│ frameworks — no manual conversion. The validation on ingress also catches server-side
│ schema bugs early in the SDK, improving debuggability. Pydantic v2 is a justified
│ dependency; it is not "clever technology."

Demo: Python agent script using AsyncGAMSClient that stores a claim about a codebase decision, contradicts it with new evidence, then uses inspect to print the full lineage in the terminal. Then show the same script using GAMSClient (sync) for comparison. This is the governance story made
concrete and the key differentiator from "vector DB with a nicer README."
──────

### Phase 5: Hardening + Public Release (3–4 weekends)

Goal: Production-grade single-binary release. README, Docker image, documentation. Public commitment to additive-only schema changes post-v1.0.0.

Work:

• Request signing (HMAC-SHA256) implementation + SDK support for both @gams/client and gams-client
• Prometheus metrics: claim count by namespace/status, recall latency histogram, write throughput, auth cache hit rate
• Rate limiting per API key (in-memory token bucket; no Redis required)
• gams admin audit export --tenant <id> --namespace <slug> --format jsonl — JSONL export with mandatory schema_version field in every line (Q7). Format:
{"schema_version": "1.0", "exported_at": "...", "event": {...}}

• gams admin claims export --tenant <id> --namespace <slug> — full claims snapshot (all versions, evidence) as JSONL; also includes schema_version: "1.0" in header record
• Goreleaser config for multi-platform binary releases (Linux amd64/arm64, macOS amd64/arm64, Windows amd64)
• Docker image (distroless base; single binary; /data volume mount for SQLite file)
• README: quickstart in under 5 minutes; both HTTP and stdio MCP config examples; @gams/client and gams-client install snippets
• Documentation site (Markdown; Hugo or VitePress; no custom CMS)
• CHANGELOG entry: explicit "v1.0.0 — additive-only schema changes from here forward. schema_version will increment on breaking export format changes."

Demo: Full end-to-end video: download binary, start server, configure Claude Code (HTTP transport), store memories across three separate sessions, recall across sessions, create a second namespace for a second project, show namespace-scoped recall, run audit export and show the JSONL with
schema_version, run claims export and show the full snapshot. The entire demo runs on a laptop. No cloud, no GPU, no npm install.
──────

### Phase 6 (Future, Not v1): Semantic Recall

• Opt-in embedding pipeline (configurable embedding endpoint; defaults to localhost:11434 for Ollama)
• sqlite-vec extension for vector similarity search
• Hybrid scoring: BM25 + cosine similarity + confidence + recency
• Schema migration adds embedding BLOB column to claims; existing data is unaffected; embeddings generated lazily on next write or via a gams admin embeddings backfill command
• schema_version bumped to "1.1" in exports when embedding data is included
──────

## 8. Risk Register

### High Risk

R1: SQLite write serialization becomes a bottleneck under real multi-agent workloads

• Likelihood: Low for typical use; Medium if operator runs many parallel agents against one instance.
• Impact: Throughput degrades; write latency climbs.
• Mitigation: WAL mode and connection pool tuning buys significant headroom. Documented guidance: "GAMS is optimized for dozens of concurrent agents, not thousands." If this becomes a pain point, the migration path is Turso (libsql, wire-compatible with SQLite) — same schema, no data
migration.
• Early warning signal: Write latency p95 > 100ms under normal load.

R2: MCP spec instability

• Likelihood: Medium. The MCP spec has evolved rapidly (2024-11 → 2025-03).
• Impact: Breaking changes to transport or tool schema require SDK and server updates.
• Mitigation: Abstract the protocol dispatch layer. Support both stdio and HTTP transports. Pin to a specific spec version; document the supported version prominently. The community will signal breaking changes loudly.

R3: modernc.org/sqlite (pure-Go SQLite) maintenance risk

• Likelihood: Low. It is actively maintained and widely used.
• Impact: If abandoned, the alternative is CGo SQLite (mattn/go-sqlite3), which breaks the pure-Go build requirement but is the original, highly maintained library.
• Mitigation: The abstraction layer (internal/db/) means swapping the SQLite driver is a localized change. Keep CGo SQLite as a documented fallback.

### Medium Risk

R4: Confidence semantics drift across agents

• Likelihood: High. Different agents will assign confidence differently.
• Impact: Recall ranking becomes unreliable if confidence values have inconsistent semantics.
• Mitigation: Document confidence semantics prominently. Make the recall weighting configurable. Consider adding a confidence_source field in v2 to track how confidence was derived. This is ultimately a human/agent convention problem, not a technical one.

R5: FTS5 recall quality is poor for short, keyword-sparse claims

• Likelihood: Medium. Short claims ("the API uses REST") may not match well against longer queries.
• Impact: Agents can't find relevant memories; they stop trusting the tool.
• Mitigation: Encourage agents to store rich context in claims (not just conclusions). Document best practices. Provide tags and namespace as structured recall signals. Accept that semantic recall is a v2 necessity and communicate it.

R6: bcrypt key verification latency on high-throughput recall

• Likelihood: Low for typical usage; Medium if recall is called in a tight loop.
• Impact: bcrypt is intentionally slow (cost factor 10 ≈ 100ms). This adds 100ms to every request if keys are verified per-request.
• Mitigation: In-process LRU cache for key verification results, keyed by (key_hash, tenant_id). Cache TTL of 5 minutes. Adds negligible memory overhead; eliminates the per-request bcrypt cost for the common case (key is valid and active).

### Low Risk

R7: Single-developer bus factor

• Likelihood: Inherent.
• Impact: Project abandonment.
• Mitigation: Boring tech, small surface area, and thorough documentation are the mitigation. A developer who has never seen the codebase should be able to understand and contribute within one day. Keep the codebase under 5,000 lines of non-test Go for the v1 scope.

R8: Binary size bloat

• Likelihood: Low. modernc.org/sqlite is the largest dependency (~5MB of generated C-to-Go code).
• Impact: Binary >30MB becomes friction for distribution.
• Mitigation: UPX compression if needed. Realistic expectation: 15–25MB uncompressed, which is acceptable for server infrastructure.
──────

## 9. Decision Log — All Resolved ✅

No open questions remain. All architectural decisions are settled. The plan is ready for Phase 0.

| Q   | Decision                                                                                                     | Impact                                                                        |
| --- | ------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------- |
| Q1  | Binary: gams. Env: GAMS\_. Config: gams.toml.                                                                | Naming consistent throughout.                                                 |
| Q2  | Namespaces are first-class entities (namespaces + namespace_grants tables).                                  | Schema has 9 tables total. Auth flow has two layers.                          |
| Q3  | MCP name: "gams", displayName: "GAMS", description: "Governed Agent Memory Server".                          | Shown in §5.1. Config entries use "gams" key.                                 |
| Q4  | HTTP/SSE primary (Phase 2). stdio secondary (Phase 3).                                                       | Phase ordering reflects this.                                                 |
| Q5  | @gams/client (scoped npm). ESM-only (Node ≥ 18). Core REST client + /mcp submodule adapter.                  | Requires agent REST API (/v1/), added as §5.5.                                |
| Q6  | Single gams-client package. asyncio-first (AsyncGAMSClient) + sync wrapper (GAMSClient). Pydantic v2 models. | See §5.5 for SDK architecture.                                                |
| Q7  | Additive-only post-v1.0.0. schema_version mandatory in all exports/snapshots.                                | schema_version: "1.0" in all JSONL output. Phase 5 adds both export commands. |
| Q8  | Primary persona: developer-power-user who builds products using local agents.                                | SDK quality + zero-config both required.                                      |
| Q9  | Per-tenant auto_create_namespaces flag (default: false). SDKs expose ensureNamespace().                      | auto_create_namespaces column in tenants table. Auth flow updated in §4.3.    |

──────

## Appendix: Risk Register Addition (from Q5/Q6 decisions)

The following risk was not in the original register and is surfaced by the SDK decisions:

R9: Pydantic v2 as a Python SDK dependency

• Likelihood: Low. Pydantic v2 is stable and widely adopted.
• Impact: Any Python user running an older project with Pydantic v1 will face a dependency conflict. Pydantic v1 and v2 cannot coexist in the same environment.
• Mitigation: Require Pydantic ≥ 2.0 explicitly in pyproject.toml. Document the requirement clearly. Since the primary persona is building new agent applications (not maintaining legacy code), this is unlikely to be a practical problem. If it becomes one, the mitigation is offering a gams-
client[no-pydantic] extra that returns raw dicts, but this is a deferred optimization.

R10: Agent REST API (/v1/) adds attack surface alongside MCP

• Likelihood: Low. Both surfaces use the same auth model (Bearer API key).
• Impact: Two HTTP surfaces to secure instead of one. An operator who intends to expose only MCP might accidentally expose the REST API too.
• Mitigation: Single port (8080) serves both. No configuration needed to disable either individually in v1 — both are always up when HTTP transport is active. If an operator wants to block the REST API at the network level, that's done via firewall/reverse proxy, not the application.
Document this clearly. Consider an api.agent_rest_enabled config flag in v2 if demand exists.
──────
End of planning document. No production code was written in the production of this artifact.
Status: v3 — All decisions resolved — Ready for Phase 0.
