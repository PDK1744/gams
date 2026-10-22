package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/PDK1744/gams/internal/auth"
	"github.com/PDK1744/gams/internal/config"
	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/internal/server"
	"github.com/PDK1744/gams/pkg/types"
)

func setupAgentServer(t *testing.T) (*server.Server, *types.Tenant, string) {
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
	cfg.Server.AdminKey = "test-admin"

	srv := server.New(cfg, database, "0.1.0", nil)

	// Create tenant
	tenant, err := srv.Engine().CreateTenant(ctx, &types.CreateTenantRequest{
		Name: "Agent Test Tenant",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating tenant: %v", err)
	}

	// Create API Key
	keyResp, err := srv.Engine().CreateAPIKey(ctx, tenant.ID, &types.CreateAPIKeyRequest{
		Label: "agent-test-key",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating api key: %v", err)
	}

	return srv, tenant, keyResp.Key
}

func doAgentRequest(srv *server.Server, method, path, apiKey string, body any) *httptest.ResponseRecorder {
	var bodyReader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader([]byte{})
	}

	req := httptest.NewRequest(method, path, bodyReader)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestAgentAuth(t *testing.T) {
	srv, _, apiKey := setupAgentServer(t)

	// 1. Missing Authorization header -> 401
	rec := doAgentRequest(srv, http.MethodPost, "/v1/claims", "", types.RememberRequest{})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	// 2. Invalid API key -> 401
	rec = doAgentRequest(srv, http.MethodPost, "/v1/claims", "gams_invalid_fake", types.RememberRequest{})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	// 3. Valid key with bad body -> 400 (not 401!)
	rec = doAgentRequest(srv, http.MethodPost, "/v1/claims", apiKey, types.RememberRequest{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	// 4. Oversized request body > 2MB rejected (Finding 1.5)
	hugeContent := string(make([]byte, 3<<20)) // 3 MB
	rec = doAgentRequest(srv, http.MethodPost, "/v1/claims", apiKey, map[string]string{"content": hugeContent})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on oversized request body, got %d", rec.Code)
	}
}

func TestRememberRecallInspectForgetFlow(t *testing.T) {
	srv, _, apiKey := setupAgentServer(t)

	// 1. Remember claim
	remReq := types.RememberRequest{
		Content:    "We decided on SQLite WAL mode with immediate transactions",
		Confidence: 1.0,
		Evidence: []types.EvidenceInput{
			{
				URI:          "file://gams-project-plan.md",
				Description:  "Section 3 Architecture Decisions",
				EvidenceType: "file",
			},
		},
		Tags: []string{"architecture", "sqlite"},
	}

	rec := doAgentRequest(srv, http.MethodPost, "/v1/claims", apiKey, remReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var remResp types.RememberResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &remResp); err != nil {
		t.Fatalf("unmarshaling remember response: %v", err)
	}
	if remResp.ClaimID == "" || remResp.Version != 1 || remResp.Status != "created" {
		t.Fatalf("unexpected remember response: %+v", remResp)
	}

	// 2. Recall claim
	recReq := types.RecallRequest{
		Query: "decided SQLite WAL",
	}
	rec = doAgentRequest(srv, http.MethodPost, "/v1/recall", apiKey, recReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var recResp types.RecallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &recResp); err != nil {
		t.Fatalf("unmarshaling recall response: %v", err)
	}
	if len(recResp.Claims) != 1 {
		t.Fatalf("expected 1 recalled claim, got %d", len(recResp.Claims))
	}
	if recResp.Claims[0].ClaimID != remResp.ClaimID {
		t.Fatalf("unexpected claim returned: %s vs %s", recResp.Claims[0].ClaimID, remResp.ClaimID)
	}
	if recResp.Claims[0].Score <= 0 {
		t.Fatalf("expected positive score, got %f", recResp.Claims[0].Score)
	}
	if len(recResp.Claims[0].Evidence) != 1 {
		t.Fatalf("expected 1 evidence item attached to recalled claim, got %d", len(recResp.Claims[0].Evidence))
	}

	// 3. Inspect claim
	rec = doAgentRequest(srv, http.MethodGet, "/v1/claims/"+remResp.ClaimID, apiKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var inspResp types.InspectResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &inspResp); err != nil {
		t.Fatalf("unmarshaling inspect response: %v", err)
	}
	if inspResp.Claim.ID != remResp.ClaimID {
		t.Fatalf("expected inspected claim id match")
	}
	if len(inspResp.Versions) != 1 || len(inspResp.Evidence) != 1 || len(inspResp.Audit) == 0 {
		t.Fatalf("incomplete inspection data: %+v", inspResp)
	}

	// 4. Forget claim
	rec = doAgentRequest(srv, http.MethodPost, "/v1/claims/"+remResp.ClaimID+"/forget", apiKey, types.ForgetRequest{
		Reason: "decision superseded",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var forgetResp types.ForgetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &forgetResp); err != nil {
		t.Fatalf("unmarshaling forget response: %v", err)
	}
	if forgetResp.ClaimID != remResp.ClaimID || forgetResp.ForgottenAt == "" {
		t.Fatalf("unexpected forget response: %+v", forgetResp)
	}

	// 5. Subsequent recall should NOT return the forgotten claim
	rec = doAgentRequest(srv, http.MethodPost, "/v1/recall", apiKey, recReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &recResp)
	if len(recResp.Claims) != 0 {
		t.Fatalf("expected 0 claims after forget, got %d", len(recResp.Claims))
	}

	// 6. Server admin key can authenticate to /v1/claims/{id}/forget (Finding 1.4)
	rec = doAgentRequest(srv, http.MethodPost, "/v1/claims/"+remResp.ClaimID+"/forget", "test-admin", types.ForgetRequest{
		Reason: "admin forget via /v1/",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from admin forget via /v1/, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAgentNamespaceEndpoints(t *testing.T) {
	srv, _, apiKey := setupAgentServer(t)

	// 1. List namespaces (initially only 'default')
	rec := doAgentRequest(srv, http.MethodGet, "/v1/namespaces", apiKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var namespaces []types.Namespace
	_ = json.Unmarshal(rec.Body.Bytes(), &namespaces)
	if len(namespaces) != 1 || namespaces[0].Slug != "default" {
		t.Fatalf("expected only default namespace, got %+v", namespaces)
	}

	// 2. Create namespace via /v1/namespaces
	rec = doAgentRequest(srv, http.MethodPost, "/v1/namespaces", apiKey, types.CreateNamespaceRequest{
		Slug: "agent-project",
		Name: "Agent Project",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// 3. Get namespace detail with live stats
	rec = doAgentRequest(srv, http.MethodGet, "/v1/namespaces/agent-project", apiKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var detail types.NamespaceDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("unmarshaling namespace detail: %v", err)
	}
	if detail.Slug != "agent-project" || detail.Stats.ClaimCount != 0 {
		t.Fatalf("unexpected detail: %+v", detail)
	}
}

func TestRequestSigning(t *testing.T) {
	srv, _, apiKey := setupAgentServer(t)

	// Enable signing_required
	srv.Engine().Config().Auth.SigningRequired = true
	t.Cleanup(func() {
		srv.Engine().Config().Auth.SigningRequired = false
	})

	body := []byte(`{"query":"test"}`)
	path := "/v1/recall"

	// 1. Missing signature when required -> 401
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on missing signature, got %d", rec.Code)
	}

	// 2. Valid signature -> 200
	nowTs := types.NowNano() / 1e9 // seconds
	tsStr := string(rune(0))
	_ = tsStr
	tsStr = bytes.NewBuffer(nil).String()
	tsStr = timeNowSecStr()
	sig := auth.ComputeSignature(apiKey, http.MethodPost, path, tsStr, body)

	req = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.HeaderTimestamp, tsStr)
	req.Header.Set(auth.HeaderSignature, sig)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on valid signature, got %d: %s", rec.Code, rec.Body.String())
	}

	// 3. Tampered signature -> 401
	req = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.HeaderTimestamp, tsStr)
	req.Header.Set(auth.HeaderSignature, "bad-signature-hex")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on bad signature, got %d", rec.Code)
	}

	_ = nowTs
}

func timeNowSecStr() string {
	return strconv.FormatInt(time.Now().Unix(), 10)
}

