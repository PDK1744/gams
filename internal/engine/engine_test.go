package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/PDK1744/gams/internal/audit"
	"github.com/PDK1744/gams/internal/auth"
	"github.com/PDK1744/gams/internal/config"
	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/pkg/types"
)

func setupTestEngine(t *testing.T) (*Engine, *types.Tenant, *types.APIKey) {
	t.Helper()
	ctx := context.Background()
	testDB, err := db.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() {
		_ = testDB.Close()
	})

	cfg := config.Default()
	authSvc := auth.NewService(testDB.DB, "test-admin-key", 1*time.Minute)
	eng := New(testDB, authSvc, cfg, "0.1.0")

	// Create tenant
	tenant, err := eng.CreateTenant(ctx, &types.CreateTenantRequest{
		Name:                 "Test Tenant",
		AutoCreateNamespaces: false,
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating test tenant: %v", err)
	}

	// Create API key
	keyResp, err := eng.CreateAPIKey(ctx, tenant.ID, &types.CreateAPIKeyRequest{
		Label: "test-agent",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating test api key: %v", err)
	}

	apiKey := &types.APIKey{
		ID:       keyResp.ID,
		TenantID: tenant.ID,
		Label:    keyResp.Label,
		Status:   keyResp.Status,
	}

	return eng, tenant, apiKey
}

func TestRememberAndIdempotency(t *testing.T) {
	ctx := context.Background()
	eng, tenant, apiKey := setupTestEngine(t)

	req := &types.RememberRequest{
		Content:    "PostgreSQL WAL mode handles high concurrency",
		Confidence: 0.95,
		Evidence: []types.EvidenceInput{
			{
				URI:          "https://example.com/docs",
				Description:  "Database documentation",
				EvidenceType: "url",
			},
		},
		Tags: []string{"database", "architecture"},
	}

	// 1. Initial write
	resp, err := eng.Remember(ctx, apiKey, tenant, req)
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}
	if resp.ClaimID == "" {
		t.Fatalf("expected claim_id to be populated")
	}
	if resp.Version != 1 {
		t.Fatalf("expected version 1, got %d", resp.Version)
	}
	if resp.Status != "created" {
		t.Fatalf("expected status 'created', got %s", resp.Status)
	}

	// 2. Idempotent write (exact same content to same tenant and namespace)
	resp2, err := eng.Remember(ctx, apiKey, tenant, req)
	if err != nil {
		t.Fatalf("idempotent remember failed: %v", err)
	}
	if resp2.ClaimID != resp.ClaimID {
		t.Fatalf("expected identical claim_id, got %s vs %s", resp2.ClaimID, resp.ClaimID)
	}
	if resp2.Version != 1 {
		t.Fatalf("expected version 1 for idempotent write, got %d", resp2.Version)
	}
	if resp2.Status != "idempotent" {
		t.Fatalf("expected status 'idempotent' for idempotent write, got %s", resp2.Status)
	}
}

func TestOptimisticConcurrency(t *testing.T) {
	ctx := context.Background()
	eng, tenant, apiKey := setupTestEngine(t)

	// Create initial claim
	resp, err := eng.Remember(ctx, apiKey, tenant, &types.RememberRequest{
		Content:    "Initial architecture decision",
		Confidence: 0.8,
		Evidence: []types.EvidenceInput{
			{
				URI:          "internal://meeting-notes",
				Description:  "Sprint planning",
				EvidenceType: "agent_assertion",
			},
		},
	})
	if err != nil {
		t.Fatalf("initial write failed: %v", err)
	}

	// Try to update with wrong base_version (expect 409 conflict)
	wrongBaseVersion := int64(999)
	conflictResp, err := eng.Remember(ctx, apiKey, tenant, &types.RememberRequest{
		ClaimID:     &resp.ClaimID,
		Content:     "Updated architecture decision",
		Confidence:  0.85,
		BaseVersion: &wrongBaseVersion,
		Evidence: []types.EvidenceInput{
			{
				URI:          "internal://meeting-notes-2",
				Description:  "Follow up",
				EvidenceType: "agent_assertion",
			},
		},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if conflictResp == nil || conflictResp.Status != "conflict" {
		t.Fatalf("expected conflict response status, got %+v", conflictResp)
	}
	if conflictResp.Conflict == nil || conflictResp.Conflict.CurrentVersion != 1 {
		t.Fatalf("expected conflict current_version=1, got %+v", conflictResp.Conflict)
	}

	// Update with correct base_version
	correctBaseVersion := int64(1)
	updateResp, err := eng.Remember(ctx, apiKey, tenant, &types.RememberRequest{
		ClaimID:     &resp.ClaimID,
		Content:     "Updated architecture decision",
		Confidence:  0.85,
		BaseVersion: &correctBaseVersion,
		Evidence: []types.EvidenceInput{
			{
				URI:          "internal://meeting-notes-2",
				Description:  "Follow up",
				EvidenceType: "agent_assertion",
			},
		},
	})
	if err != nil {
		t.Fatalf("update with correct base version failed: %v", err)
	}
	if updateResp.Version != 2 {
		t.Fatalf("expected version 2, got %d", updateResp.Version)
	}
	if updateResp.Status != "updated" {
		t.Fatalf("expected status 'updated', got %s", updateResp.Status)
	}
}

func TestContradictionProtocol(t *testing.T) {
	ctx := context.Background()
	eng, tenant, apiKey := setupTestEngine(t)

	// Claim 1: "We use MongoDB for storage"
	claim1, err := eng.Remember(ctx, apiKey, tenant, &types.RememberRequest{
		Content:    "We use MongoDB for storage",
		Confidence: 0.9,
		Evidence: []types.EvidenceInput{
			{
				URI:          "file://spec.txt",
				Description:  "Old draft spec",
				EvidenceType: "file",
			},
		},
	})
	if err != nil {
		t.Fatalf("claim1 failed: %v", err)
	}

	// Claim 2: "We use SQLite in WAL mode" (contradicts Claim 1)
	claim2, err := eng.Remember(ctx, apiKey, tenant, &types.RememberRequest{
		Content:     "We use SQLite in WAL mode",
		Confidence:  1.0,
		Contradicts: []string{claim1.ClaimID},
		Evidence: []types.EvidenceInput{
			{
				URI:          "file://gams-project-plan.md",
				Description:  "Final approved architectural spec",
				EvidenceType: "file",
			},
		},
	})
	if err != nil {
		t.Fatalf("claim2 failed: %v", err)
	}

	// Verify claim 1 is now marked contradicted
	inspect1, err := eng.Inspect(ctx, apiKey, tenant, claim1.ClaimID)
	if err != nil {
		t.Fatalf("inspect claim 1 failed: %v", err)
	}
	if inspect1.Claim.Status != "contradicted" {
		t.Fatalf("expected claim 1 status 'contradicted', got %s", inspect1.Claim.Status)
	}
	if inspect1.Claim.ContradictedBy == nil || *inspect1.Claim.ContradictedBy != claim2.ClaimID {
		t.Fatalf("expected contradicted_by = %s, got %v", claim2.ClaimID, inspect1.Claim.ContradictedBy)
	}

	// Recall without include_contradicted -> only claim 2 is returned
	rec1, err := eng.Recall(ctx, apiKey, tenant, &types.RecallRequest{
		Query: "use storage mode",
	})
	if err != nil {
		t.Fatalf("recall failed: %v", err)
	}
	for _, c := range rec1.Claims {
		if c.ClaimID == claim1.ClaimID {
			t.Fatalf("contradicted claim should be excluded by default from recall")
		}
	}

	// Recall with include_contradicted -> claim 1 is included
	rec2, err := eng.Recall(ctx, apiKey, tenant, &types.RecallRequest{
		Query:               "use storage mode",
		IncludeContradicted: true,
	})
	if err != nil {
		t.Fatalf("recall with include_contradicted failed: %v", err)
	}
	foundClaim1 := false
	for _, c := range rec2.Claims {
		if c.ClaimID == claim1.ClaimID {
			foundClaim1 = true
			break
		}
	}
	if !foundClaim1 {
		t.Fatalf("expected claim 1 to be included when include_contradicted=true")
	}
}

func TestCrossNamespaceContradictionAuthorization(t *testing.T) {
	ctx := context.Background()
	eng, tenant, agentA := setupTestEngine(t)

	// Admin creates a key_restricted namespace
	ns, err := eng.CreateNamespace(ctx, tenant.ID, &types.CreateNamespaceRequest{
		Slug:       "private-vault",
		Name:       "Private Vault",
		AccessMode: "key_restricted",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating private namespace: %v", err)
	}

	// Create second agent (agentB) with NO grant to private-vault
	keyRespB, _ := eng.CreateAPIKey(ctx, tenant.ID, &types.CreateAPIKeyRequest{Label: "agent-b"}, "admin", "admin")
	agentB := &types.APIKey{ID: keyRespB.ID, TenantID: tenant.ID, Label: keyRespB.Label, Status: keyRespB.Status}

	// Give agentA write grant to private-vault
	_, _ = eng.CreateGrant(ctx, tenant.ID, ns.Slug, &types.CreateGrantRequest{
		APIKeyID:   agentA.ID,
		Permission: "write",
	}, "admin", "admin")

	// AgentA creates a claim in private-vault
	privateClaim, err := eng.Remember(ctx, agentA, tenant, &types.RememberRequest{
		Namespace:  "private-vault",
		Content:    "Top secret algorithm parameter",
		Confidence: 1.0,
		Evidence: []types.EvidenceInput{
			{URI: "file://vault.txt", Description: "Secret doc", EvidenceType: "file"},
		},
	})
	if err != nil {
		t.Fatalf("agentA remember failed: %v", err)
	}

	// AgentB tries to contradict privateClaim from the default namespace (Finding 1.3)
	// AgentB has NO access to private-vault; this must be rejected with ErrForbidden
	_, err = eng.Remember(ctx, agentB, tenant, &types.RememberRequest{
		Namespace:   "default",
		Content:     "Attempting unauthorized contradiction of private claim",
		Confidence:  1.0,
		Contradicts: []string{privateClaim.ClaimID},
		Evidence: []types.EvidenceInput{
			{URI: "file://unauth.txt", Description: "Rogue assertion", EvidenceType: "agent_assertion"},
		},
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden for cross-namespace unauthorized contradiction, got: %v", err)
	}
}

func TestContradictedClaimReassertion(t *testing.T) {
	ctx := context.Background()
	eng, tenant, apiKey := setupTestEngine(t)

	content := "Original statement about network latency"

	// 1. Initial write
	c1, err := eng.Remember(ctx, apiKey, tenant, &types.RememberRequest{
		Content:    content,
		Confidence: 0.9,
		Evidence: []types.EvidenceInput{
			{URI: "file://net.txt", Description: "Ping logs", EvidenceType: "file"},
		},
	})
	if err != nil {
		t.Fatalf("c1 remember failed: %v", err)
	}

	// 2. Contradict c1
	_, err = eng.Remember(ctx, apiKey, tenant, &types.RememberRequest{
		Content:     "New measurement contradicting latency",
		Confidence:  1.0,
		Contradicts: []string{c1.ClaimID},
		Evidence: []types.EvidenceInput{
			{URI: "file://new-net.txt", Description: "New logs", EvidenceType: "file"},
		},
	})
	if err != nil {
		t.Fatalf("contradiction write failed: %v", err)
	}

	// 3. Re-assert the exact same original statement (Finding 2.3)
	// Because c1 is now contradicted, re-asserting it should create a new active claim,
	// NOT return the old contradicted claim!
	c3, err := eng.Remember(ctx, apiKey, tenant, &types.RememberRequest{
		Content:    content,
		Confidence: 0.95,
		Evidence: []types.EvidenceInput{
			{URI: "file://net3.txt", Description: "Third measurement", EvidenceType: "file"},
		},
	})
	if err != nil {
		t.Fatalf("re-asserting claim failed: %v", err)
	}
	if c3.ClaimID == c1.ClaimID {
		t.Fatalf("expected new claim to be created on re-assertion of contradicted content, got duplicate id %s", c1.ClaimID)
	}

	// Verify c3 is active
	insp3, err := eng.Inspect(ctx, apiKey, tenant, c3.ClaimID)
	if err != nil {
		t.Fatalf("inspect c3 failed: %v", err)
	}
	if insp3.Claim.Status != "active" {
		t.Fatalf("expected re-asserted claim status 'active', got %s", insp3.Claim.Status)
	}
}

func TestInspectAuditTrailScale(t *testing.T) {
	ctx := context.Background()
	eng, tenant, apiKey := setupTestEngine(t)

	// Create target claim
	targetClaim, err := eng.Remember(ctx, apiKey, tenant, &types.RememberRequest{
		Content:    "Target claim for audit inspection",
		Confidence: 1.0,
		Evidence: []types.EvidenceInput{
			{URI: "file://test", Description: "Test", EvidenceType: "file"},
		},
	})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	// Flood audit_log with 150 events for other claims/events in this tenant
	for i := 0; i < 150; i++ {
		otherClaimID := fmt.Sprintf("other-%d", i)
		_ = eng.Audit().Log(ctx, eng.DB().DB, &types.AuditEvent{
			TenantID:   tenant.ID,
			EventType:  audit.EventWrite,
			ActorKeyID: apiKey.ID,
			ActorLabel: apiKey.Label,
			ClaimID:    &otherClaimID,
			OccurredAt: types.NowNano(),
		})
	}

	// Inspect target claim: audit trail MUST not be truncated or lost (Finding 2.1)
	inspectResp, err := eng.Inspect(ctx, apiKey, tenant, targetClaim.ClaimID)
	if err != nil {
		t.Fatalf("inspect failed: %v", err)
	}
	if len(inspectResp.Audit) == 0 {
		t.Fatalf("expected audit events for target claim despite 150 other events, got 0")
	}
	for _, a := range inspectResp.Audit {
		if a.ClaimID == nil || *a.ClaimID != targetClaim.ClaimID {
			t.Fatalf("returned audit event for wrong claim: %v", a.ClaimID)
		}
	}
}

func TestNamespaceAdminCanForget(t *testing.T) {
	ctx := context.Background()
	eng, tenant, agentA := setupTestEngine(t)

	// Create namespace
	ns, _ := eng.CreateNamespace(ctx, tenant.ID, &types.CreateNamespaceRequest{
		Slug:       "shared-project",
		Name:       "Shared Project",
		AccessMode: "key_restricted",
	}, "admin", "admin")

	// Create agentB
	keyB, _ := eng.CreateAPIKey(ctx, tenant.ID, &types.CreateAPIKeyRequest{Label: "admin-agent"}, "admin", "admin")
	agentB := &types.APIKey{ID: keyB.ID, TenantID: tenant.ID, Label: keyB.Label, Status: keyB.Status}

	// AgentA has write permission
	_, _ = eng.CreateGrant(ctx, tenant.ID, ns.Slug, &types.CreateGrantRequest{
		APIKeyID:   agentA.ID,
		Permission: "write",
	}, "admin", "admin")

	// AgentB has admin permission on the namespace
	_, _ = eng.CreateGrant(ctx, tenant.ID, ns.Slug, &types.CreateGrantRequest{
		APIKeyID:   agentB.ID,
		Permission: "admin",
	}, "admin", "admin")

	// AgentA creates a claim
	claim, err := eng.Remember(ctx, agentA, tenant, &types.RememberRequest{
		Namespace:  "shared-project",
		Content:    "Claim written by Agent A",
		Confidence: 1.0,
		Evidence: []types.EvidenceInput{
			{URI: "file://spec", Description: "Spec", EvidenceType: "file"},
		},
	})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	// AgentB (who is NOT the writer, but is namespace admin) forgets the claim (Finding 1.4)
	forgetResp, err := eng.Forget(ctx, agentB, tenant, claim.ClaimID, "cleaned up by namespace admin")
	if err != nil {
		t.Fatalf("expected namespace admin to be able to forget claim, got error: %v", err)
	}
	if forgetResp.ClaimID != claim.ClaimID {
		t.Fatalf("unexpected claim id in forget response: %s", forgetResp.ClaimID)
	}
}

func TestTenantWideNonWriterCannotForgetWithoutAdminGrant(t *testing.T) {
	ctx := context.Background()
	eng, tenant, agentA := setupTestEngine(t)

	// Create tenant_wide namespace
	ns, err := eng.CreateNamespace(ctx, tenant.ID, &types.CreateNamespaceRequest{
		Slug:       "open-collab",
		Name:       "Open Collab Space",
		AccessMode: "tenant_wide",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating namespace: %v", err)
	}

	// Create agentB (regular agent without admin grant)
	keyB, err := eng.CreateAPIKey(ctx, tenant.ID, &types.CreateAPIKeyRequest{Label: "agent-b"}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating key: %v", err)
	}
	agentB := &types.APIKey{ID: keyB.ID, TenantID: tenant.ID, Label: keyB.Label, Status: keyB.Status}

	// AgentA writes a claim
	claim, err := eng.Remember(ctx, agentA, tenant, &types.RememberRequest{
		Namespace:  ns.Slug,
		Content:    "Original memory authored by Agent A",
		Confidence: 0.9,
		Evidence: []types.EvidenceInput{
			{URI: "file://notes.txt", Description: "Agent A notes", EvidenceType: "file"},
		},
	})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	// AgentB tries to forget AgentA's claim: MUST fail with ErrForbidden (CR-001)
	_, err = eng.Forget(ctx, agentB, tenant, claim.ClaimID, "malicious deletion by non-author")
	if err == nil {
		t.Fatalf("expected error when non-author attempts to forget claim in tenant_wide namespace, got nil")
	}
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got: %v", err)
	}

	// Now grant agentB admin permission on open-collab
	_, err = eng.CreateGrant(ctx, tenant.ID, ns.Slug, &types.CreateGrantRequest{
		APIKeyID:   agentB.ID,
		Permission: "admin",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating grant: %v", err)
	}

	// AgentB retries with admin grant: MUST succeed
	forgetResp, err := eng.Forget(ctx, agentB, tenant, claim.ClaimID, "authorized deletion by namespace admin")
	if err != nil {
		t.Fatalf("expected forget to succeed after admin grant issued, got: %v", err)
	}
	if forgetResp.ClaimID != claim.ClaimID {
		t.Fatalf("unexpected claim id in forget response: %s", forgetResp.ClaimID)
	}
}

func TestNamespaceAutoCreateFlag(t *testing.T) {
	ctx := context.Background()
	eng, tenant, apiKey := setupTestEngine(t)

	// 1. auto_create_namespaces is false on tenant: write to non-existent namespace fails with 404
	_, err := eng.Remember(ctx, apiKey, tenant, &types.RememberRequest{
		Namespace:  "non-existent-ns",
		Content:    "Testing namespace auto-create",
		Confidence: 1.0,
		Evidence: []types.EvidenceInput{
			{URI: "file://test", Description: "Test", EvidenceType: "file"},
		},
	})
	if !errors.Is(err, auth.ErrNamespaceNotFound) {
		t.Fatalf("expected ErrNamespaceNotFound, got: %v", err)
	}

	// 2. Set auto_create_namespaces = true
	tTrue := true
	updatedTenant, err := eng.UpdateTenant(ctx, tenant.ID, &types.UpdateTenantRequest{
		AutoCreateNamespaces: &tTrue,
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("updating tenant failed: %v", err)
	}

	// 3. Write to non-existent namespace now succeeds and auto-creates it
	resp, err := eng.Remember(ctx, apiKey, updatedTenant, &types.RememberRequest{
		Namespace:  "auto-created-ns",
		Content:    "Testing namespace auto-create success",
		Confidence: 1.0,
		Evidence: []types.EvidenceInput{
			{URI: "file://test", Description: "Test", EvidenceType: "file"},
		},
	})
	if err != nil {
		t.Fatalf("remember with auto-create failed: %v", err)
	}
	if resp.ClaimID == "" {
		t.Fatalf("expected claim_id to be populated")
	}

	// Verify namespace was created with access_mode='tenant_wide'
	ns, err := eng.GetNamespace(ctx, tenant.ID, "auto-created-ns")
	if err != nil {
		t.Fatalf("getting auto-created namespace failed: %v", err)
	}
	if ns.Slug != "auto-created-ns" || ns.AccessMode != "tenant_wide" {
		t.Fatalf("unexpected namespace fields: %+v", ns)
	}
}

func TestForgetSoftDelete(t *testing.T) {
	ctx := context.Background()
	eng, tenant, apiKey := setupTestEngine(t)

	claim, err := eng.Remember(ctx, apiKey, tenant, &types.RememberRequest{
		Content:    "Transient secret memory",
		Confidence: 0.9,
		Evidence: []types.EvidenceInput{
			{URI: "file://secret", Description: "Secret", EvidenceType: "file"},
		},
	})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	// Forget the claim
	forgetResp, err := eng.Forget(ctx, apiKey, tenant, claim.ClaimID, "decommissioned secret")
	if err != nil {
		t.Fatalf("forget failed: %v", err)
	}
	if forgetResp.ClaimID != claim.ClaimID || forgetResp.ForgottenAt == "" {
		t.Fatalf("unexpected forget response: %+v", forgetResp)
	}

	// Recall should no longer find the claim
	recallResp, err := eng.Recall(ctx, apiKey, tenant, &types.RecallRequest{
		Query: "Transient secret memory",
	})
	if err != nil {
		t.Fatalf("recall failed: %v", err)
	}
	if len(recallResp.Claims) != 0 {
		t.Fatalf("expected 0 claims after forget, got %d", len(recallResp.Claims))
	}

	// Inspect still shows the full historical record and audit trail
	inspectResp, err := eng.Inspect(ctx, apiKey, tenant, claim.ClaimID)
	if err != nil {
		t.Fatalf("inspect failed: %v", err)
	}
	if inspectResp.Claim.Status != "forgotten" {
		t.Fatalf("expected status 'forgotten', got %s", inspectResp.Claim.Status)
	}
	if inspectResp.Claim.ForgottenReason == nil || *inspectResp.Claim.ForgottenReason != "decommissioned secret" {
		t.Fatalf("expected forgotten_reason 'decommissioned secret', got %v", inspectResp.Claim.ForgottenReason)
	}
}

func TestConcurrentResolveOrCreateNamespace(t *testing.T) {
	ctx := context.Background()
	eng, tenant, apiKey := setupTestEngine(t)

	// Ensure auto_create_namespaces is true
	autoc := true
	var err error
	tenant, err = eng.UpdateTenant(ctx, tenant.ID, &types.UpdateTenantRequest{
		AutoCreateNamespaces: &autoc,
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("updating tenant: %v", err)
	}

	const goroutines = 5
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, remErr := eng.Remember(ctx, apiKey, tenant, &types.RememberRequest{
				Content:    fmt.Sprintf("Concurrent memory %d", idx),
				Confidence: 0.9,
				Namespace:  "concurrent-ns-slug",
				Evidence: []types.EvidenceInput{
					{URI: "file://test", Description: "Test", EvidenceType: "file"},
				},
			})
			if remErr != nil {
				errCh <- remErr
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for remErr := range errCh {
		t.Fatalf("concurrent Remember with new namespace slug failed: %v", remErr)
	}

	ns, err := eng.GetNamespace(ctx, tenant.ID, "concurrent-ns-slug")
	if err != nil {
		t.Fatalf("fetching auto-created namespace: %v", err)
	}
	if ns.Slug != "concurrent-ns-slug" {
		t.Fatalf("expected slug concurrent-ns-slug, got %s", ns.Slug)
	}
}

func TestConcurrentEnsureLocalWorkspaceTenant(t *testing.T) {
	ctx := context.Background()
	testDB, err := db.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer testDB.Close()

	cfg := config.Default()
	authSvc := auth.NewService(testDB.DB, "admin", 1*time.Minute)
	eng := New(testDB, authSvc, cfg, "test-version")

	const goroutines = 4
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key, localTenant, provErr := eng.EnsureLocalWorkspaceTenant(ctx)
			if provErr != nil {
				errCh <- provErr
				return
			}
			if key == nil || localTenant == nil || localTenant.ID != "local" {
				errCh <- fmt.Errorf("unexpected local tenant or key: key=%v, tenant=%v", key, localTenant)
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for provErr := range errCh {
		t.Fatalf("concurrent EnsureLocalWorkspaceTenant failed: %v", provErr)
	}
}
