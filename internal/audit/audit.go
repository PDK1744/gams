package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PDK1744/gams/pkg/types"
)

// EventType constants as defined in Section 4.1.
const (
	EventWrite             = "WRITE"
	EventRead              = "READ"
	EventForget            = "FORGET"
	EventContradict        = "CONTRADICT"
	EventKeyIssued         = "KEY_ISSUED"
	EventKeyRevoked        = "KEY_REVOKED"
	EventTenantCreated     = "TENANT_CREATED"
	EventTenantUpdated     = "TENANT_UPDATED"
	EventNamespaceCreated  = "NAMESPACE_CREATED"
	EventNamespaceArchived = "NAMESPACE_ARCHIVED"
	EventGrantIssued       = "GRANT_ISSUED"
	EventGrantRevoked      = "GRANT_REVOKED"
)

// Execer represents a database execution interface compatible with *sql.DB and *sql.Tx.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Logger provides append-only audit logging.
type Logger struct {
	serverVersion string
}

// NewLogger creates a new audit Logger.
func NewLogger(serverVersion string) *Logger {
	return &Logger{
		serverVersion: serverVersion,
	}
}

// Log writes an immutable event to the audit_log table.
func (l *Logger) Log(ctx context.Context, exec Execer, event *types.AuditEvent) error {
	if event.ID == "" {
		event.ID = types.NewUUIDv7()
	}
	if event.OccurredAt == 0 {
		event.OccurredAt = types.NowNano()
	}
	if event.ServerVersion == "" {
		event.ServerVersion = l.serverVersion
	}

	payloadJSON := "{}"
	if event.Payload != nil {
		b, err := json.Marshal(event.Payload)
		if err != nil {
			return fmt.Errorf("marshaling audit payload: %w", err)
		}
		payloadJSON = string(b)
	}

	query := `INSERT INTO audit_log (
		id, tenant_id, namespace_id, event_type, actor_key_id, actor_label,
		claim_id, claim_version, payload, occurred_at, server_version
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := exec.ExecContext(ctx, query,
		event.ID,
		event.TenantID,
		event.NamespaceID,
		event.EventType,
		event.ActorKeyID,
		event.ActorLabel,
		event.ClaimID,
		event.ClaimVersion,
		payloadJSON,
		event.OccurredAt,
		event.ServerVersion,
	)
	if err != nil {
		return fmt.Errorf("writing audit log entry: %w", err)
	}

	return nil
}

// Filter defines parameters for querying audit entries.
type Filter struct {
	TenantID    string
	NamespaceID *string
	ClaimID     *string
	EventType   *string
	Since       *int64
	Until       *int64
	Limit       int
	Offset      int
}

// Query retrieves chronological audit records matching filter criteria.
func (l *Logger) Query(ctx context.Context, db *sql.DB, f Filter) ([]types.AuditEvent, error) {
	var conditions []string
	var args []any

	conditions = append(conditions, "tenant_id = ?")
	args = append(args, f.TenantID)

	if f.NamespaceID != nil && *f.NamespaceID != "" {
		conditions = append(conditions, "namespace_id = ?")
		args = append(args, *f.NamespaceID)
	}
	if f.ClaimID != nil && *f.ClaimID != "" {
		conditions = append(conditions, "claim_id = ?")
		args = append(args, *f.ClaimID)
	}
	if f.EventType != nil && *f.EventType != "" {
		conditions = append(conditions, "event_type = ?")
		args = append(args, *f.EventType)
	}
	if f.Since != nil && *f.Since > 0 {
		conditions = append(conditions, "occurred_at >= ?")
		args = append(args, *f.Since)
	}
	if f.Until != nil && *f.Until > 0 {
		conditions = append(conditions, "occurred_at <= ?")
		args = append(args, *f.Until)
	}

	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	query := fmt.Sprintf(`SELECT id, tenant_id, namespace_id, event_type, actor_key_id, actor_label,
		       claim_id, claim_version, payload, occurred_at, server_version
		FROM audit_log
		WHERE %s
		ORDER BY occurred_at DESC
		LIMIT ? OFFSET ?`, strings.Join(conditions, " AND "))

	args = append(args, limit, f.Offset)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying audit log: %w", err)
	}
	defer rows.Close()

	var events []types.AuditEvent
	for rows.Next() {
		var e types.AuditEvent
		var nsID, claimID sql.NullString
		var claimVer sql.NullInt64
		var payloadStr string

		err := rows.Scan(
			&e.ID, &e.TenantID, &nsID, &e.EventType, &e.ActorKeyID, &e.ActorLabel,
			&claimID, &claimVer, &payloadStr, &e.OccurredAt, &e.ServerVersion,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning audit event: %w", err)
		}

		if nsID.Valid {
			e.NamespaceID = &nsID.String
		}
		if claimID.Valid {
			e.ClaimID = &claimID.String
		}
		if claimVer.Valid {
			e.ClaimVersion = &claimVer.Int64
		}
		if payloadStr != "" && payloadStr != "{}" {
			_ = json.Unmarshal([]byte(payloadStr), &e.Payload)
		}

		events = append(events, e)
	}

	return events, rows.Err()
}
