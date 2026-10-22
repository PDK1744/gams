/**
 * Core type definitions for @gams/client.
 * Aligned with the GAMS protocol specification.
 */

export type EvidenceType = 'url' | 'file' | 'tool_output' | 'agent_assertion' | 'human';

export interface EvidenceItem {
  uri: string;
  description: string;
  evidence_type: EvidenceType;
  captured_at?: string | number;
  metadata?: Record<string, any>;
}

export interface EvidenceRecord {
  id?: string;
  claim_id?: string;
  claim_version?: number;
  tenant_id?: string;
  uri: string;
  description: string;
  evidence_type: EvidenceType;
  captured_at: string | number;
  metadata?: Record<string, any>;
}

export interface RememberInput {
  content: string;
  confidence: number;
  evidence: EvidenceItem[];
  contradicts?: string[];
  tags?: string[];
  namespace?: string;
  claim_id?: string;
  base_version?: number;
  change_reason?: string;
  metadata?: Record<string, any>;
}

export interface ConflictInfo {
  current_version: number;
  updated_at: string;
}

export interface RememberResult {
  claim_id: string;
  version: number;
  created_at: string;
  status: 'created' | 'updated' | 'conflict' | 'idempotent';
  conflict?: ConflictInfo;
}

export interface RecallInput {
  query: string;
  limit?: number;
  confidence_min?: number;
  tags?: string[];
  namespace?: string;
  include_contradicted?: boolean;
  include_forgotten?: boolean;
  recency_window_days?: number;
}

export interface RecallClaim {
  claim_id: string;
  content: string;
  confidence: number;
  version: number;
  status: string;
  score: number;
  tags: string[];
  namespace_slug: string;
  namespace_id: string;
  evidence: EvidenceItem[];
  writer_label: string;
  created_at: string;
  updated_at: string;
}

export interface RecallResult {
  claims: RecallClaim[];
  total_matched: number;
  query_ms: number;
}

export interface ForgetResult {
  claim_id: string;
  forgotten_at: string;
}

export interface Claim {
  id: string;
  tenant_id: string;
  namespace_id: string;
  current_version: number;
  content: string;
  content_hash: string;
  confidence: number;
  status: string;
  writer_id?: string;
  writer_label?: string;
  tags?: string[];
  created_at: string | number;
  updated_at: string | number;
  forgotten_at?: string | number | null;
  forgotten_reason?: string | null;
  contradicted_by?: string | null;
  metadata?: Record<string, any>;
}

export interface ClaimVersion {
  id?: string;
  claim_id?: string;
  tenant_id?: string;
  namespace_id?: string;
  version: number;
  content: string;
  confidence: number;
  writer_id?: string;
  writer_label?: string;
  written_at: string | number;
  change_reason?: string | null;
  contradicts: string[];
}

export interface AuditEvent {
  id?: string;
  tenant_id?: string;
  namespace_id?: string;
  event_type: string;
  actor_key_id?: string;
  actor_label?: string;
  claim_id?: string;
  claim_version?: number;
  occurred_at: string | number;
  server_version?: string;
  payload?: Record<string, any>;
}

export interface InspectResult {
  claim: Claim;
  versions: ClaimVersion[];
  evidence: (EvidenceItem | EvidenceRecord)[];
  audit: AuditEvent[];
}

export type AccessMode = 'tenant_wide' | 'key_restricted';

export interface Namespace {
  id: string;
  tenant_id: string;
  slug: string;
  name: string;
  description?: string;
  status: string;
  access_mode: AccessMode;
  created_at: string;
  updated_at: string;
}

export interface EnsureNamespaceOptions {
  name?: string;
  description?: string;
  accessMode?: AccessMode;
}

export interface RetryPolicy {
  maxAttempts?: number;
  baseDelayMs?: number;
  maxDelayMs?: number;
}

export interface GAMSClientOptions {
  baseUrl?: string;
  apiKey: string;
  timeoutMs?: number;
  retryPolicy?: RetryPolicy;
  signRequests?: boolean;
}

// MCP Adapter Types
export interface MCPToolDefinition {
  name: string;
  description: string;
  inputSchema: Record<string, any>;
}

export interface MCPTextContent {
  type: 'text';
  text: string;
}

export interface MCPToolResult {
  content: MCPTextContent[];
  isError?: boolean;
}
