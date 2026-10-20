package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	rec = doRequest(srv, http.MethodGet, "/admin/tenants/"+tenant.ID+"/audit", adminKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var events []types.AuditEvent
	_ = json.Unmarshal(rec.Body.Bytes(), &events)
	if len(events) < 2 { // TENANT_CREATED and NAMESPACE_CREATED for default
		t.Fatalf("expected at least 2 audit events, got %d", len(events))
	}
}
