package types

import "time"

// Tenant represents an account or billing/identity boundary.
type Tenant struct {
	ID                   string         `json:"id"`
	Name                 string         `json:"name"`
	Status               string         `json:"status"` // "active" | "suspended"
	RecallDecayLambda    float64        `json:"recall_decay_lambda"`
	MaxClaims            int64          `json:"max_claims"`
	AutoCreateNamespaces bool           `json:"auto_create_namespaces"`
	CreatedAt            int64          `json:"created_at"` // Unix nanoseconds
	Metadata             map[string]any `json:"metadata,omitempty"`
}

// Namespace represents a logical isolation boundary within a tenant.
type Namespace struct {
	ID                  string         `json:"id"`
	TenantID            string         `json:"tenant_id"`
	Slug                string         `json:"slug"`
	Name                string         `json:"name"`
	Description         *string        `json:"description,omitempty"`
	Status              string         `json:"status"`      // "active" | "archived"
	AccessMode          string         `json:"access_mode"` // "tenant_wide" | "key_restricted"
	RecallDecayLambda   *float64       `json:"recall_decay_lambda,omitempty"`
	MaxClaims           int64          `json:"max_claims"`
	CreatedAt           int64          `json:"created_at"` // Unix nanoseconds
	UpdatedAt           int64          `json:"updated_at"` // Unix nanoseconds
	Metadata            map[string]any `json:"metadata,omitempty"`
}

// NamespaceGrant represents an explicit key grant for key_restricted namespaces.
type NamespaceGrant struct {
	ID          string `json:"id"`
	NamespaceID string `json:"namespace_id"`
	APIKeyID    string `json:"api_key_id"`
	Permission  string `json:"permission"` // "read" | "write" | "admin"
	GrantedAt   int64  `json:"granted_at"`  // Unix nanoseconds
	GrantedBy   string `json:"granted_by"`
}

// APIKey represents an authentication key for an agent or system.
type APIKey struct {
	ID         string  `json:"id"`
	TenantID   string  `json:"tenant_id"`
	KeyHash    string  `json:"-"`
	Label      string  `json:"label"`
	Status     string  `json:"status"` // "active" | "revoked"
	LastUsedAt int64   `json:"last_used_at"`
	ExpiresAt  *int64  `json:"expires_at,omitempty"`
	CreatedAt  int64   `json:"created_at"`
}

// Claim represents a durable piece of knowledge or state.
type Claim struct {
	ID              string         `json:"id"`
	TenantID        string         `json:"tenant_id"`
	NamespaceID     string         `json:"namespace_id"`
	CurrentVersion  int64          `json:"current_version"`
	Content         string         `json:"content"`
	ContentHash     string         `json:"content_hash"`
	Confidence      float64        `json:"confidence"`
	Status          string         `json:"status"` // "active" | "contradicted" | "forgotten"
	ContradictedBy  *string        `json:"contradicted_by,omitempty"`
	WriterID        string         `json:"writer_id"`
	WriterLabel     string         `json:"writer_label"`
	Tags            []string       `json:"tags"`
	CreatedAt       int64          `json:"created_at"`
	UpdatedAt       int64          `json:"updated_at"`
	ForgottenAt     *int64         `json:"forgotten_at,omitempty"`
	ForgottenReason *string        `json:"forgotten_reason,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

// ClaimVersion captures point-in-time claim content and rationale.
type ClaimVersion struct {
	ID           string   `json:"id"`
	ClaimID      string   `json:"claim_id"`
	TenantID     string   `json:"tenant_id"`
	NamespaceID  string   `json:"namespace_id"`
	Version      int64    `json:"version"`
	Content      string   `json:"content"`
	Confidence   float64  `json:"confidence"`
	Contradicts  []string `json:"contradicts"`
	WriterID     string   `json:"writer_id"`
	WriterLabel  string   `json:"writer_label,omitempty"`
	WrittenAt    int64    `json:"written_at"`
	ChangeReason *string  `json:"change_reason,omitempty"`
}

// Evidence links a claim version to sources.
type Evidence struct {
	ID           string         `json:"id"`
	ClaimID      string         `json:"claim_id"`
	ClaimVersion int64          `json:"claim_version"`
	TenantID     string         `json:"tenant_id"`
	URI          string         `json:"uri"`
	Description  string         `json:"description"`
	EvidenceType string         `json:"evidence_type"` // "url"|"file"|"tool_output"|"agent_assertion"|"human"
	CapturedAt   int64          `json:"captured_at"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// AuditEvent represents an append-only audit record.
type AuditEvent struct {
	ID            string         `json:"id"`
	TenantID      string         `json:"tenant_id"`
	NamespaceID   *string        `json:"namespace_id,omitempty"`
	EventType     string         `json:"event_type"`
	ActorKeyID    string         `json:"actor_key_id"`
	ActorLabel    string         `json:"actor_label"`
	ClaimID       *string        `json:"claim_id,omitempty"`
	ClaimVersion  *int64         `json:"claim_version,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
	OccurredAt    int64          `json:"occurred_at"`
	ServerVersion string         `json:"server_version"`
}

// NowNano returns the current Unix timestamp in nanoseconds.
func NowNano() int64 {
	return time.Now().UTC().UnixNano()
}
