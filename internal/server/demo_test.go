package server_test

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

// TestPhase1Demo replicates the exact scenario outlined in Phase 1 Demo of gams-project-plan.md:
// "Via curl against /v1/: create a tenant (admin API), store claims in two namespaces,
// recall scoped to one. Then call GET /v1/namespaces and show both. Set auto_create_namespaces = true
// on the tenant via admin API and write to a new namespace without creating it first —
// observe it auto-created in the response."
func TestPhase1Demo(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer database.Close()

	cfg := config.Default()
	adminKey := "super-secret-admin-key"
	cfg.Server.AdminKey = adminKey

	srv := server.New(cfg, database, "0.1.0", nil)

	// Step 1: Create a tenant via Admin API
	t.Log("Step 1: Admin creates tenant 'Demo Corp' (with auto_create_namespaces=false)")
	rec := execReq(srv, http.MethodPost, "/admin/tenants", adminKey, types.CreateTenantRequest{
		Name:                 "Demo Corp",
		AutoCreateNamespaces: false,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("failed to create tenant: %d: %s", rec.Code, rec.Body.String())
	}
	var tenant types.Tenant
	_ = json.Unmarshal(rec.Body.Bytes(), &tenant)

	// Step 2: Issue an API key for Demo Corp
	t.Log("Step 2: Admin issues API key for Demo Corp")
	rec = execReq(srv, http.MethodPost, "/admin/tenants/"+tenant.ID+"/keys", adminKey, types.CreateAPIKeyRequest{
		Label: "demo-agent",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("failed to create api key: %d: %s", rec.Code, rec.Body.String())
	}
	var keyResp types.CreateAPIKeyResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &keyResp)
	agentKey := keyResp.Key

	// Step 3: Admin creates two namespaces: "frontend" and "backend"
	t.Log("Step 3: Admin creates namespaces 'frontend' and 'backend'")
	rec = execReq(srv, http.MethodPost, "/admin/tenants/"+tenant.ID+"/namespaces", adminKey, types.CreateNamespaceRequest{
		Slug: "frontend",
		Name: "Frontend Services",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("failed to create frontend namespace: %d: %s", rec.Code, rec.Body.String())
	}

	rec = execReq(srv, http.MethodPost, "/admin/tenants/"+tenant.ID+"/namespaces", adminKey, types.CreateNamespaceRequest{
		Slug: "backend",
		Name: "Backend Services",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("failed to create backend namespace: %d: %s", rec.Code, rec.Body.String())
	}

	// Step 4: Agent writes claims in both namespaces via /v1/claims
	t.Log("Step 4: Agent stores claims in 'frontend' and 'backend'")
	rec = execReq(srv, http.MethodPost, "/v1/claims", agentKey, types.RememberRequest{
		Namespace:  "frontend",
		Content:    "The client uses React 19 with Server Components",
		Confidence: 0.95,
		Evidence: []types.EvidenceInput{
			{URI: "file://package.json", Description: "Frontend dependencies", EvidenceType: "file"},
		},
		Tags: []string{"frontend", "react"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("failed to remember frontend claim: %d: %s", rec.Code, rec.Body.String())
	}
	var frontendClaim types.RememberResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &frontendClaim)

	rec = execReq(srv, http.MethodPost, "/v1/claims", agentKey, types.RememberRequest{
		Namespace:  "backend",
		Content:    "The server uses Go 1.22 with SQLite WAL mode",
		Confidence: 1.0,
		Evidence: []types.EvidenceInput{
			{URI: "file://go.mod", Description: "Backend dependencies", EvidenceType: "file"},
		},
		Tags: []string{"backend", "golang"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("failed to remember backend claim: %d: %s", rec.Code, rec.Body.String())
	}

	// Step 5: Recall scoped strictly to "frontend" namespace
	t.Log("Step 5: Agent recalls scoped strictly to 'frontend'")
	rec = execReq(srv, http.MethodPost, "/v1/recall", agentKey, types.RecallRequest{
		Query:     "uses Server Components",
		Namespace: "frontend",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("failed to recall from frontend: %d: %s", rec.Code, rec.Body.String())
	}
	var recallResp types.RecallResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &recallResp)

	if len(recallResp.Claims) != 1 {
		t.Fatalf("expected exactly 1 claim from frontend recall, got %d", len(recallResp.Claims))
	}
	if recallResp.Claims[0].NamespaceSlug != "frontend" {
		t.Fatalf("expected recalled claim from 'frontend', got %s", recallResp.Claims[0].NamespaceSlug)
	}
	if recallResp.Claims[0].ClaimID != frontendClaim.ClaimID {
		t.Fatalf("mismatched claim recalled: expected %s, got %s", frontendClaim.ClaimID, recallResp.Claims[0].ClaimID)
	}

	// Step 6: Agent lists namespaces via GET /v1/namespaces
	t.Log("Step 6: Agent lists namespaces via GET /v1/namespaces (shows default, frontend, backend)")
	rec = execReq(srv, http.MethodGet, "/v1/namespaces", agentKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("failed to list namespaces: %d: %s", rec.Code, rec.Body.String())
	}
	var namespaces []types.Namespace
	_ = json.Unmarshal(rec.Body.Bytes(), &namespaces)

	slugs := make(map[string]bool)
	for _, ns := range namespaces {
		slugs[ns.Slug] = true
	}
	if !slugs["default"] || !slugs["frontend"] || !slugs["backend"] {
		t.Fatalf("expected default, frontend, backend in namespace list, got: %+v", slugs)
	}

	// Step 7: Attempt writing to a non-existent namespace before auto_create -> should return 404
	t.Log("Step 7: Agent attempts writing to non-existent 'analytics' namespace (auto_create=false) -> 404")
	rec = execReq(srv, http.MethodPost, "/v1/claims", agentKey, types.RememberRequest{
		Namespace:  "analytics",
		Content:    "Analytics pipeline uses ClickHouse",
		Confidence: 0.9,
		Evidence: []types.EvidenceInput{
			{URI: "file://docs.md", Description: "Analytics spec", EvidenceType: "file"},
		},
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when auto_create_namespaces is false, got %d: %s", rec.Code, rec.Body.String())
	}

	// Step 8: Admin sets auto_create_namespaces = true on the tenant
	t.Log("Step 8: Admin updates tenant auto_create_namespaces = true")
	tTrue := true
	rec = execReq(srv, http.MethodPatch, "/admin/tenants/"+tenant.ID, adminKey, types.UpdateTenantRequest{
		AutoCreateNamespaces: &tTrue,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("failed to update tenant: %d: %s", rec.Code, rec.Body.String())
	}

	// Step 9: Agent writes to 'analytics' without creating it first -> auto-created!
	t.Log("Step 9: Agent writes to 'analytics' without prior creation -> auto-created in response!")
	rec = execReq(srv, http.MethodPost, "/v1/claims", agentKey, types.RememberRequest{
		Namespace:  "analytics",
		Content:    "Analytics pipeline uses ClickHouse",
		Confidence: 0.9,
		Evidence: []types.EvidenceInput{
			{URI: "file://docs.md", Description: "Analytics spec", EvidenceType: "file"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("failed to remember to auto-created namespace: %d: %s", rec.Code, rec.Body.String())
	}

	// Step 10: Verify 'analytics' is now in GET /v1/namespaces
	t.Log("Step 10: Verify 'analytics' is now present in GET /v1/namespaces")
	rec = execReq(srv, http.MethodGet, "/v1/namespaces", agentKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("failed to list namespaces: %d", rec.Code)
	}
	var updatedNamespaces []types.Namespace
	_ = json.Unmarshal(rec.Body.Bytes(), &updatedNamespaces)

	analyticsFound := false
	for _, ns := range updatedNamespaces {
		if ns.Slug == "analytics" {
			analyticsFound = true
			if ns.AccessMode != "tenant_wide" {
				t.Fatalf("expected auto-created namespace to be tenant_wide, got %s", ns.AccessMode)
			}
			break
		}
	}
	if !analyticsFound {
		t.Fatalf("expected 'analytics' namespace to be present after auto-creation")
	}

	t.Log("Phase 1 Demo completed successfully!")
}

func execReq(srv *server.Server, method, path, token string, body any) *httptest.ResponseRecorder {
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
