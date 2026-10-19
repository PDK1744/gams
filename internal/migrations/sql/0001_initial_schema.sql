-- 0001_initial_schema.sql
-- Governed Agent Memory Server (GAMS) Initial Schema
-- Enforces 9-table data model with locked-in additive-only contract post-v1.0.0

-- 1. Tenants
CREATE TABLE IF NOT EXISTS tenants (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    recall_decay_lambda REAL NOT NULL DEFAULT 0.05,
    max_claims INTEGER NOT NULL DEFAULT 0,
    auto_create_namespaces BOOLEAN NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    metadata TEXT NOT NULL DEFAULT '{}'
);

-- 2. Namespaces (first-class entity)
CREATE TABLE IF NOT EXISTS namespaces (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    slug TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL DEFAULT 'active',
    access_mode TEXT NOT NULL DEFAULT 'tenant_wide',
    recall_decay_lambda REAL,
    max_claims INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    metadata TEXT NOT NULL DEFAULT '{}',
    UNIQUE (tenant_id, slug)
);

CREATE INDEX IF NOT EXISTS idx_namespaces_tenant_slug ON namespaces(tenant_id, slug);
CREATE INDEX IF NOT EXISTS idx_namespaces_tenant_status ON namespaces(tenant_id, status);

-- 3. API Keys
CREATE TABLE IF NOT EXISTS api_keys (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    key_hash TEXT NOT NULL,
    label TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    last_used_at INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER,
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);
CREATE INDEX IF NOT EXISTS idx_api_keys_tenant ON api_keys(tenant_id);

-- 4. Namespace Grants (access control for key_restricted namespaces)
CREATE TABLE IF NOT EXISTS namespace_grants (
    id TEXT PRIMARY KEY,
    namespace_id TEXT NOT NULL REFERENCES namespaces(id) ON DELETE CASCADE,
    api_key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    permission TEXT NOT NULL,
    granted_at INTEGER NOT NULL,
    granted_by TEXT NOT NULL,
    UNIQUE (namespace_id, api_key_id)
);

CREATE INDEX IF NOT EXISTS idx_namespace_grants_lookup ON namespace_grants(namespace_id, api_key_id);

-- 5. Claims
CREATE TABLE IF NOT EXISTS claims (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    namespace_id TEXT NOT NULL REFERENCES namespaces(id) ON DELETE CASCADE,
    current_version INTEGER NOT NULL DEFAULT 1,
    content TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 1.0,
    status TEXT NOT NULL DEFAULT 'active',
    contradicted_by TEXT,
    writer_id TEXT NOT NULL,
    writer_label TEXT NOT NULL,
    tags TEXT NOT NULL DEFAULT '[]',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    forgotten_at INTEGER,
    forgotten_reason TEXT,
    metadata TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_claims_recall ON claims(tenant_id, namespace_id, status, updated_at);
CREATE INDEX IF NOT EXISTS idx_claims_idempotency ON claims(tenant_id, namespace_id, content_hash);

-- 6. Claim Versions
CREATE TABLE IF NOT EXISTS claim_versions (
    id TEXT PRIMARY KEY,
    claim_id TEXT NOT NULL REFERENCES claims(id) ON DELETE CASCADE,
    tenant_id TEXT NOT NULL,
    namespace_id TEXT NOT NULL,
    version INTEGER NOT NULL,
    content TEXT NOT NULL,
    confidence REAL NOT NULL,
    contradicts TEXT NOT NULL DEFAULT '[]',
    writer_id TEXT NOT NULL,
    written_at INTEGER NOT NULL,
    change_reason TEXT,
    UNIQUE (claim_id, version)
);

CREATE INDEX IF NOT EXISTS idx_claim_versions_traverse ON claim_versions(claim_id, version);

-- 7. Evidence
CREATE TABLE IF NOT EXISTS evidence (
    id TEXT PRIMARY KEY,
    claim_id TEXT NOT NULL REFERENCES claims(id) ON DELETE CASCADE,
    claim_version INTEGER NOT NULL,
    tenant_id TEXT NOT NULL,
    uri TEXT NOT NULL,
    description TEXT NOT NULL,
    evidence_type TEXT NOT NULL,
    captured_at INTEGER NOT NULL,
    metadata TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_evidence_claim ON evidence(claim_id);

-- 8. Audit Log (append-only)
CREATE TABLE IF NOT EXISTS audit_log (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    namespace_id TEXT,
    event_type TEXT NOT NULL,
    actor_key_id TEXT NOT NULL,
    actor_label TEXT NOT NULL,
    claim_id TEXT,
    claim_version INTEGER,
    payload TEXT NOT NULL DEFAULT '{}',
    occurred_at INTEGER NOT NULL,
    server_version TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_log_tenant_time ON audit_log(tenant_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_audit_log_namespace_time ON audit_log(tenant_id, namespace_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_audit_log_claim ON audit_log(claim_id);

-- 9. Recall Index (SQLite FTS5 virtual table)
CREATE VIRTUAL TABLE IF NOT EXISTS recall_index USING fts5(
    claim_id UNINDEXED,
    tenant_id UNINDEXED,
    namespace_id UNINDEXED,
    content,
    tags,
    namespace_slug,
    tokenize = 'unicode61'
);
