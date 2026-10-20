package audit

import (
	"context"
	"testing"

	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/pkg/types"
)

func TestAuditLogAndQuery(t *testing.T) {
	ctx := context.Background()
	testDB, err := db.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer testDB.Close()

	tenantID := types.NewUUIDv7()
	now := types.NowNano()

	_, err = testDB.ExecContext(ctx,
		`INSERT INTO tenants (id, name, status, recall_decay_lambda, max_claims, auto_create_namespaces, created_at, metadata)
		 VALUES (?, 'Test Tenant', 'active', 0.05, 0, 0, ?, '{}')`,
		tenantID, now)
	if err != nil {
		t.Fatalf("inserting tenant: %v", err)
	}

	logger := NewLogger("0.1.0")

	// 1. Log an event
	evt := &types.AuditEvent{
		TenantID:   tenantID,
		EventType:  EventWrite,
		ActorKeyID: "key-123",
		ActorLabel: "agent-1",
		Payload: map[string]any{
			"action": "remember",
		},
	}

	if err := logger.Log(ctx, testDB.DB, evt); err != nil {
		t.Fatalf("logging event: %v", err)
	}

	// 2. Query events
	events, err := logger.Query(ctx, testDB.DB, Filter{
		TenantID: tenantID,
	})
	if err != nil {
		t.Fatalf("querying events: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].EventType != EventWrite {
		t.Errorf("expected EventWrite, got %s", events[0].EventType)
	}
	if events[0].ServerVersion != "0.1.0" {
		t.Errorf("expected server version 0.1.0, got %s", events[0].ServerVersion)
	}
	if events[0].Payload["action"] != "remember" {
		t.Errorf("expected payload action=remember, got %v", events[0].Payload["action"])
	}
}
