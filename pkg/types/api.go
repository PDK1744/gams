package types

import (
	"time"

	"github.com/google/uuid"
)

// FormatISO8601 converts a Unix nanosecond timestamp to RFC3339 formatted string.
func FormatISO8601(nano int64) string {
	if nano <= 0 {
		return ""
	}
	return time.Unix(0, nano).UTC().Format(time.RFC3339Nano)
}

// ParseISO8601 parses an RFC3339 string into Unix nanoseconds.
func ParseISO8601(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return 0, err
		}
	}
	return t.UTC().UnixNano(), nil
}

// NewUUIDv7 generates a time-ordered UUID v7 string.
func NewUUIDv7() string {
	id, err := uuid.NewV7()
	if err != nil {
		// Fallback to NewRandom if clock error occurs
		return uuid.New().String()
	}
	return id.String()
}

// EvidenceInput is the wire format for evidence items in remember & recall.
type EvidenceInput struct {
	URI          string `json:"uri"`
	Description  string `json:"description"`
	EvidenceType string `json:"evidence_type"` // url | file | tool_output | agent_assertion | human
	CapturedAt   string `json:"captured_at,omitempty"`
}

// RememberRequest represents the payload for POST /v1/claims (remember).
type RememberRequest struct {
	ClaimID      *string         `json:"claim_id,omitempty"`
	Content      string          `json:"content"`
	Confidence   float64         `json:"confidence"`
	Evidence     []EvidenceInput `json:"evidence"`
	Contradicts  []string        `json:"contradicts,omitempty"`
	Tags         []string        `json:"tags,omitempty"`
	Namespace    string          `json:"namespace,omitempty"`
	BaseVersion  *int64          `json:"base_version,omitempty"`
	ChangeReason *string         `json:"change_reason,omitempty"`
	Metadata     map[string]any  `json:"metadata,omitempty"`
}

// ConflictInfo provides details about a concurrency version conflict.
type ConflictInfo struct {
	CurrentVersion int64  `json:"current_version"`
	UpdatedAt      string `json:"updated_at"`
}

// RememberResponse represents the output schema for POST /v1/claims.
type RememberResponse struct {
	ClaimID   string        `json:"claim_id"`
	Version   int64         `json:"version"`
	CreatedAt string        `json:"created_at"`
	Status    string        `json:"status"` // "created" | "updated" | "conflict"
	Conflict  *ConflictInfo `json:"conflict,omitempty"`
}

// RecallRequest represents the input parameters for POST /v1/recall.
type RecallRequest struct {
	Query               string   `json:"query"`
	Limit               int      `json:"limit,omitempty"`
	ConfidenceMin       float64  `json:"confidence_min,omitempty"`
	Tags                []string `json:"tags,omitempty"`
	Namespace           string   `json:"namespace,omitempty"`
	IncludeContradicted bool     `json:"include_contradicted,omitempty"`
	IncludeForgotten    bool     `json:"include_forgotten,omitempty"`
	RecencyWindowDays   int      `json:"recency_window_days,omitempty"`
}

// RecalledClaim represents an individual claim item in recall output.
type RecalledClaim struct {
	ClaimID       string          `json:"claim_id"`
	Content       string          `json:"content"`
	Confidence    float64         `json:"confidence"`
	Version       int64           `json:"version"`
	Status        string          `json:"status"`
	Score         float64         `json:"score"`
	Tags          []string        `json:"tags"`
	NamespaceSlug string          `json:"namespace_slug"`
	NamespaceID   string          `json:"namespace_id"`
	Evidence      []EvidenceInput `json:"evidence"`
	WriterLabel   string          `json:"writer_label"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
}

// RecallResponse represents the output payload for POST /v1/recall.
type RecallResponse struct {
	Claims       []RecalledClaim `json:"claims"`
	TotalMatched int             `json:"total_matched"`
	QueryMS      int64           `json:"query_ms"`
}

// ForgetRequest represents the payload for POST /v1/claims/{id}/forget.
type ForgetRequest struct {
	Reason string `json:"reason"`
}

// ForgetResponse represents the output for POST /v1/claims/{id}/forget.
type ForgetResponse struct {
	ClaimID     string `json:"claim_id"`
	ForgottenAt string `json:"forgotten_at"`
}

// InspectResponse represents the full inspection output for GET /v1/claims/{id}.
type InspectResponse struct {
	Claim    *Claim         `json:"claim"`
	Versions []ClaimVersion `json:"versions"`
	Evidence []Evidence     `json:"evidence"`
	Audit    []AuditEvent   `json:"audit"`
}

// NamespaceStats holds aggregate metrics for a namespace.
type NamespaceStats struct {
	ClaimCount        int64            `json:"claim_count"`
	ClaimsByStatus    map[string]int64 `json:"claims_by_status"`
	MostRecentClaimAt *string          `json:"most_recent_claim_at,omitempty"`
}

// NamespaceDetailResponse combines namespace fields with live statistics.
type NamespaceDetailResponse struct {
	Namespace
	Stats NamespaceStats `json:"stats"`
}

// Admin API request and response structures

// CreateTenantRequest is the payload for POST /admin/tenants.
type CreateTenantRequest struct {
	Name                 string         `json:"name"`
	RecallDecayLambda    *float64       `json:"recall_decay_lambda,omitempty"`
	MaxClaims            int64          `json:"max_claims,omitempty"`
	AutoCreateNamespaces bool           `json:"auto_create_namespaces"`
	Metadata             map[string]any `json:"metadata,omitempty"`
}

// UpdateTenantRequest is the payload for PATCH /admin/tenants/{id}.
type UpdateTenantRequest struct {
	Name                 *string        `json:"name,omitempty"`
	Status               *string        `json:"status,omitempty"`
	RecallDecayLambda    *float64       `json:"recall_decay_lambda,omitempty"`
	MaxClaims            *int64         `json:"max_claims,omitempty"`
	AutoCreateNamespaces *bool          `json:"auto_create_namespaces,omitempty"`
	Metadata             map[string]any `json:"metadata,omitempty"`
}

// CreateNamespaceRequest is the payload for POST /admin/tenants/{id}/namespaces and POST /v1/namespaces.
type CreateNamespaceRequest struct {
	Slug              string         `json:"slug"`
	Name              string         `json:"name"`
	Description       *string        `json:"description,omitempty"`
	AccessMode        string         `json:"access_mode,omitempty"` // "tenant_wide" | "key_restricted"
	RecallDecayLambda *float64       `json:"recall_decay_lambda,omitempty"`
	MaxClaims         int64          `json:"max_claims,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
}

// UpdateNamespaceRequest is the payload for PATCH /admin/tenants/{id}/namespaces/{slug}.
type UpdateNamespaceRequest struct {
	Name              *string        `json:"name,omitempty"`
	Description       *string        `json:"description,omitempty"`
	AccessMode        *string        `json:"access_mode,omitempty"`
	Status            *string        `json:"status,omitempty"` // "active" | "archived"
	RecallDecayLambda *float64       `json:"recall_decay_lambda,omitempty"`
	MaxClaims         *int64         `json:"max_claims,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
}

// CreateGrantRequest is the payload for POST /admin/tenants/{id}/namespaces/{slug}/grants.
type CreateGrantRequest struct {
	APIKeyID   string `json:"api_key_id"`
	Permission string `json:"permission"` // "read" | "write" | "admin"
}

// CreateAPIKeyRequest is the payload for POST /admin/tenants/{id}/keys.
type CreateAPIKeyRequest struct {
	Label     string `json:"label"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
}

// CreateAPIKeyResponse returns the generated raw API key upon creation.
type CreateAPIKeyResponse struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenant_id"`
	Label     string `json:"label"`
	Key       string `json:"key"` // Raw API key string; only returned once
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
}

// NamespaceActivity represents claim volume activity for a namespace.
type NamespaceActivity struct {
	NamespaceSlug string `json:"namespace_slug"`
	ClaimCount    int64  `json:"claim_count"`
}

// TenantSummary represents aggregate statistics across all namespaces for an operator.
type TenantSummary struct {
	TenantID             string              `json:"tenant_id"`
	TenantName           string              `json:"tenant_name"`
	TotalClaims          int64               `json:"total_claims"`
	ClaimsByStatus       map[string]int64    `json:"claims_by_status"`
	WritesLast7Days      int64               `json:"writes_last_7_days"`
	WritesLast30Days     int64               `json:"writes_last_30_days"`
	MostActiveNamespaces []NamespaceActivity `json:"most_active_namespaces"`
}
