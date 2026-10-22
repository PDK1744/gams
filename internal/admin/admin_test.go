package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PDK1744/gams/internal/config"
	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/internal/server"
	"github.com/PDK1744/gams/pkg/types"
)

func setupTestServer(t *testing.T) (*server.Server, string) {
	t.Helper()
	ctx := context.Background()
	database, err := db.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})

	cfg := config.Default()
	cfg.Server.AdminKey = "admin-secret-key"

	srv := server.New(cfg, database, "0.1.0", nil)
	return srv, cfg.Server.AdminKey
}

func doRequest(srv *server.Server, method, path, token string, body any) *httptest.ResponseRecorder {
	var bodyReader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader([]byte{})
	}

	req := httptest.NewRequest(method, path, bodyReader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestAdminAuth(t *testing.T) {
	srv, adminKey := setupTestServer(t)

	// 1. Without auth header -> 401
	rec := doRequest(srv, http.MethodGet, "/admin/tenants", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	// 2. With wrong key -> 401
	rec = doRequest(srv, http.MethodGet, "/admin/tenants", "wrong-key", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	// 3. With valid key -> 200
	rec = doRequest(srv, http.MethodGet, "/admin/tenants", adminKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// 4. In dev mode with configured adminKey: unauthenticated request MUST still be rejected (Finding 1.2)
	devSrv := server.New(&config.Config{
		Data:   config.DataConfig{Path: ":memory:"},
		Server: config.ServerConfig{AdminKey: "secret", Host: "127.0.0.1", HTTPPort: 8080},
		Limits: config.LimitsConfig{MaxClaimSizeBytes: 65536, MaxEvidencePerClaim: 20, DefaultRecallLimit: 10},
		Auth:   config.AuthConfig{KeyCacheTTLSeconds: 300},
		MCP:    config.MCPConfig{Transport: "http"},
	}, srv.Engine().DB(), "0.1.0", nil, server.WithDevMode(true))

	rec = doRequest(devSrv, http.MethodGet, "/admin/tenants", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when adminKey is configured even in devMode, got %d", rec.Code)
	}

	// 5. In dev mode with EMPTY adminKey: permitted (Finding 1.2)
	devEmptyKeySrv := server.New(&config.Config{
		Data:   config.DataConfig{Path: ":memory:"},
		Server: config.ServerConfig{AdminKey: "", Host: "127.0.0.1", HTTPPort: 8080},
		Limits: config.LimitsConfig{MaxClaimSizeBytes: 65536, MaxEvidencePerClaim: 20, DefaultRecallLimit: 10},
		Auth:   config.AuthConfig{KeyCacheTTLSeconds: 300},
		MCP:    config.MCPConfig{Transport: "http"},
	}, srv.Engine().DB(), "0.1.0", nil, server.WithDevMode(true))

	rec = doRequest(devEmptyKeySrv, http.MethodGet, "/admin/tenants", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when adminKey is empty and devMode is true, got %d", rec.Code)
	}
}

func TestAdminTenantLifecycle(t *testing.T) {
	srv, adminKey := setupTestServer(t)

	// 1. Create Tenant
	createReq := types.CreateTenantRequest{
		Name:                 "Acme Corp",
		AutoCreateNamespaces: false,
	}
	rec := doRequest(srv, http.MethodPost, "/admin/tenants", adminKey, createReq)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var tenant types.Tenant
	if err := json.Unmarshal(rec.Body.Bytes(), &tenant); err != nil {
		t.Fatalf("unmarshaling tenant: %v", err)
	}
	if tenant.Name != "Acme Corp" || tenant.Status != "active" {
		t.Fatalf("unexpected tenant: %+v", tenant)
	}

	// 2. Get Tenant
	rec = doRequest(srv, http.MethodGet, "/admin/tenants/"+tenant.ID, adminKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// 3. Update Tenant
	newName := "Acme Corporation"
	tTrue := true
	rec = doRequest(srv, http.MethodPatch, "/admin/tenants/"+tenant.ID, adminKey, types.UpdateTenantRequest{
		Name:                 &newName,
		AutoCreateNamespaces: &tTrue,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var updatedTenant types.Tenant
	_ = json.Unmarshal(rec.Body.Bytes(), &updatedTenant)
	if updatedTenant.Name != newName || !updatedTenant.AutoCreateNamespaces {
		t.Fatalf("tenant update failed: %+v", updatedTenant)
	}

	// 4. List Tenants
	rec = doRequest(srv, http.MethodGet, "/admin/tenants", adminKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var tenants []types.Tenant
	_ = json.Unmarshal(rec.Body.Bytes(), &tenants)
	if len(tenants) != 1 || tenants[0].ID != tenant.ID {
		t.Fatalf("unexpected tenant list: %+v", tenants)
	}
}

func TestAdminNamespaceAndKeyManagement(t *testing.T) {
	srv, adminKey := setupTestServer(t)

	// Create tenant
	rec := doRequest(srv, http.MethodPost, "/admin/tenants", adminKey, types.CreateTenantRequest{
		Name: "Namespace Tenant",
	})
	var tenant types.Tenant
	_ = json.Unmarshal(rec.Body.Bytes(), &tenant)

	// Verify default namespace was auto-created
	rec = doRequest(srv, http.MethodGet, "/admin/tenants/"+tenant.ID+"/namespaces", adminKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var namespaces []types.Namespace
	_ = json.Unmarshal(rec.Body.Bytes(), &namespaces)
	if len(namespaces) != 1 || namespaces[0].Slug != "default" {
		t.Fatalf("expected 1 default namespace, got %+v", namespaces)
	}

	// Create custom namespace
	rec = doRequest(srv, http.MethodPost, "/admin/tenants/"+tenant.ID+"/namespaces", adminKey, types.CreateNamespaceRequest{
		Slug:       "restricted-ns",
		Name:       "Restricted Namespace",
		AccessMode: "key_restricted",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Issue API Key
	rec = doRequest(srv, http.MethodPost, "/admin/tenants/"+tenant.ID+"/keys", adminKey, types.CreateAPIKeyRequest{
		Label: "agent-1",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var keyResp types.CreateAPIKeyResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &keyResp)
	if keyResp.Key == "" || keyResp.ID == "" {
		t.Fatalf("expected key to be generated: %+v", keyResp)
	}

	// Issue Grant for restricted namespace
	rec = doRequest(srv, http.MethodPost, "/admin/tenants/"+tenant.ID+"/namespaces/restricted-ns/grants", adminKey, types.CreateGrantRequest{
		APIKeyID:   keyResp.ID,
		Permission: "write",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for grant, got %d: %s", rec.Code, rec.Body.String())
	}

	// Revoke Grant
	rec = doRequest(srv, http.MethodDelete, "/admin/tenants/"+tenant.ID+"/namespaces/restricted-ns/grants/"+keyResp.ID, adminKey, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for grant revoke, got %d", rec.Code)
	}

	// Revoke API Key
	rec = doRequest(srv, http.MethodDelete, "/admin/tenants/"+tenant.ID+"/keys/"+keyResp.ID, adminKey, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for key revoke, got %d", rec.Code)
	}

	// Create namespace under non-existent tenant returns 404 (CR-006)
	rec = doRequest(srv, http.MethodPost, "/admin/tenants/non-existent-tenant-uuid/namespaces", adminKey, types.CreateNamespaceRequest{
		Slug: "orphan-ns",
		Name: "Orphan Namespace",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when creating namespace for non-existent tenant, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminForgetClaim(t *testing.T) {
	srv, adminKey := setupTestServer(t)
	ctx := context.Background()

	// Create tenant and claim
	tResp, _ := srv.Engine().CreateTenant(ctx, &types.CreateTenantRequest{Name: "Forget Corp"}, "admin", "admin")
	kResp, _ := srv.Engine().CreateAPIKey(ctx, tResp.ID, &types.CreateAPIKeyRequest{Label: "agent"}, "admin", "admin")

	apiKey := &types.APIKey{ID: kResp.ID, TenantID: tResp.ID, Label: kResp.Label, Status: kResp.Status}
	remResp, err := srv.Engine().Remember(ctx, apiKey, tResp, &types.RememberRequest{
		Content:    "Claim to be forgotten by admin",
		Confidence: 1.0,
		Evidence: []types.EvidenceInput{
			{URI: "file://test", Description: "Test", EvidenceType: "file"},
		},
	})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	// Revoke agent key so only admin can forget the claim
	_ = srv.Engine().RevokeAPIKey(ctx, tResp.ID, kResp.ID, "admin", "admin")

	// Admin forgets claim via POST /admin/tenants/{id}/claims/{claim_id}/forget (Finding 1.4)
	rec := doRequest(srv, http.MethodPost, "/admin/tenants/"+tResp.ID+"/claims/"+remResp.ClaimID+"/forget", adminKey, types.ForgetRequest{
		Reason: "administrative purge",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from admin forget, got %d: %s", rec.Code, rec.Body.String())
	}

	var forgetResp types.ForgetResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &forgetResp)
	if forgetResp.ClaimID != remResp.ClaimID || forgetResp.ForgottenAt == "" {
		t.Fatalf("unexpected forget response: %+v", forgetResp)
	}
}

func TestAdminAuditEndpoints(t *testing.T) {
	srv, adminKey := setupTestServer(t)

	// Create tenant
	rec := doRequest(srv, http.MethodPost, "/admin/tenants", adminKey, types.CreateTenantRequest{
		Name: "Audit Tenant",
	})
	var tenant types.Tenant
	_ = json.Unmarshal(rec.Body.Bytes(), &tenant)

	// Query tenant audit
	rec = doRequest(srv, http.MethodGet, "/admin/tenants/"+tenant.ID+"/audit?limit=200", adminKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if pageLimit := rec.Header().Get("X-Page-Limit"); pageLimit != "100" {
		t.Fatalf("expected X-Page-Limit=100 when requested 200, got %s", pageLimit)
	}
	if hasMore := rec.Header().Get("X-Has-More"); hasMore != "false" {
		t.Fatalf("expected X-Has-More=false, got %s", hasMore)
	}
	var events []types.AuditEvent
	_ = json.Unmarshal(rec.Body.Bytes(), &events)
	if len(events) < 2 { // TENANT_CREATED and NAMESPACE_CREATED for default
		t.Fatalf("expected at least 2 audit events, got %d", len(events))
	}
}

func TestAdminExportEndpoints(t *testing.T) {
	srv, adminKey := setupTestServer(t)
	ctx := context.Background()

	// 1. Create tenant and key
	tResp, _ := srv.Engine().CreateTenant(ctx, &types.CreateTenantRequest{Name: "Export Corp"}, "admin", "admin")
	kResp, _ := srv.Engine().CreateAPIKey(ctx, tResp.ID, &types.CreateAPIKeyRequest{Label: "exporter"}, "admin", "admin")

	apiKey := &types.APIKey{ID: kResp.ID, TenantID: tResp.ID, Label: kResp.Label, Status: kResp.Status}
	_, err := srv.Engine().Remember(ctx, apiKey, tResp, &types.RememberRequest{
		Content:    "Exportable claim statement",
		Confidence: 0.85,
		Evidence: []types.EvidenceInput{
			{URI: "doc://export", Description: "Export verification item", EvidenceType: "file"},
		},
		Tags: []string{"backup", "export"},
	})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	// 2. Audit Export
	rec := doRequest(srv, http.MethodGet, "/admin/tenants/"+tResp.ID+"/audit/export", adminKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from audit export, got %d: %s", rec.Code, rec.Body.String())
	}
	auditLines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(auditLines) == 0 {
		t.Fatalf("expected non-empty audit export lines")
	}
	for i, line := range auditLines {
		var record types.AuditExportRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("unmarshaling audit export line %d: %v", i, err)
		}
		if record.SchemaVersion != "1.0" {
			t.Errorf("line %d: expected schema_version 1.0, got %q", i, record.SchemaVersion)
		}
		if record.Event.TenantID != tResp.ID {
			t.Errorf("line %d: expected tenant %s, got %s", i, tResp.ID, record.Event.TenantID)
		}
	}

	// 3. Claims Snapshot Export
	rec = doRequest(srv, http.MethodGet, "/admin/tenants/"+tResp.ID+"/claims/export", adminKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from claims export, got %d: %s", rec.Code, rec.Body.String())
	}
	claimLines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(claimLines) < 2 {
		t.Fatalf("expected at least header + 1 claim record, got %d lines", len(claimLines))
	}

	// First line must be header
	var header types.ClaimsExportHeader
	if err := json.Unmarshal([]byte(claimLines[0]), &header); err != nil {
		t.Fatalf("unmarshaling header line: %v", err)
	}
	if header.SchemaVersion != "1.0" || header.RecordType != "header" || header.ClaimsCount != 1 {
		t.Fatalf("unexpected header record: %+v", header)
	}

	// Second line must be claim
	var claimRec types.ClaimsExportRecord
	if err := json.Unmarshal([]byte(claimLines[1]), &claimRec); err != nil {
		t.Fatalf("unmarshaling claim line: %v", err)
	}
	if claimRec.SchemaVersion != "1.0" || claimRec.RecordType != "claim" {
		t.Fatalf("unexpected claim record: %+v", claimRec)
	}
	if claimRec.Claim.Content != "Exportable claim statement" {
		t.Errorf("expected claim content 'Exportable claim statement', got %q", claimRec.Claim.Content)
	}
	if len(claimRec.Versions) != 1 || len(claimRec.Evidence) != 1 {
		t.Errorf("expected 1 version and 1 evidence, got %d versions, %d evidence", len(claimRec.Versions), len(claimRec.Evidence))
	}
}
