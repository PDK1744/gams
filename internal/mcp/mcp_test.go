package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/PDK1744/gams/internal/config"
	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/internal/mcp"
	"github.com/PDK1744/gams/internal/server"
	"github.com/PDK1744/gams/pkg/types"
)

func setupMCPEngine(t *testing.T) (*mcp.Dispatcher, *types.Tenant, *types.APIKey) {
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

	tenant, err := srv.Engine().CreateTenant(ctx, &types.CreateTenantRequest{
		Name: "MCP Test Tenant",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating tenant: %v", err)
	}

	keyResp, err := srv.Engine().CreateAPIKey(ctx, tenant.ID, &types.CreateAPIKeyRequest{
		Label: "mcp-agent-key",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating api key: %v", err)
	}

	actor, _, err := srv.Engine().Auth().VerifyAPIKey(ctx, keyResp.Key)
	if err != nil {
		t.Fatalf("verifying api key: %v", err)
	}

	dispatcher := mcp.NewDispatcher(srv.Engine(), "0.1.0")
	return dispatcher, tenant, actor
}

func TestInitialize(t *testing.T) {
	dispatcher, tenant, actor := setupMCPEngine(t)
	ctx := context.Background()

	req := &mcp.Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0"}}`),
	}

	resp := dispatcher.Dispatch(ctx, actor, tenant, req)
	if resp == nil || resp.Error != nil {
		t.Fatalf("expected successful response, got error: %v", resp)
	}

	initRes, ok := resp.Result.(mcp.InitializeResult)
	if !ok {
		t.Fatalf("expected InitializeResult, got %T", resp.Result)
	}

	if initRes.ServerInfo.Name != "gams" {
		t.Errorf("expected server name 'gams', got %q", initRes.ServerInfo.Name)
	}
	if initRes.ServerInfo.DisplayName != "GAMS" {
		t.Errorf("expected displayName 'GAMS', got %q", initRes.ServerInfo.DisplayName)
	}
	if initRes.ServerInfo.Description != "Governed Agent Memory Server" {
		t.Errorf("expected description 'Governed Agent Memory Server', got %q", initRes.ServerInfo.Description)
	}
}

func TestNotification(t *testing.T) {
	dispatcher, tenant, actor := setupMCPEngine(t)
	ctx := context.Background()

	req := &mcp.Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}

	resp := dispatcher.Dispatch(ctx, actor, tenant, req)
	if resp != nil {
		t.Errorf("expected nil response for notification, got: %v", resp)
	}
}

func TestPing(t *testing.T) {
	dispatcher, tenant, actor := setupMCPEngine(t)
	ctx := context.Background()

	req := &mcp.Request{
		JSONRPC: "2.0",
		ID:      "req-ping",
		Method:  "ping",
	}

	resp := dispatcher.Dispatch(ctx, actor, tenant, req)
	if resp == nil || resp.Error != nil {
		t.Fatalf("expected successful response, got: %v", resp)
	}
}

func TestToolsList(t *testing.T) {
	dispatcher, tenant, actor := setupMCPEngine(t)
	ctx := context.Background()

	req := &mcp.Request{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
	}

	resp := dispatcher.Dispatch(ctx, actor, tenant, req)
	if resp == nil || resp.Error != nil {
		t.Fatalf("expected tools/list success, got: %v", resp)
	}

	toolsRes, ok := resp.Result.(mcp.ToolsListResult)
	if !ok {
		t.Fatalf("expected ToolsListResult, got %T", resp.Result)
	}

	expectedTools := map[string]bool{
		"remember": false,
		"recall":   false,
		"forget":   false,
		"inspect":  false,
	}

	for _, tool := range toolsRes.Tools {
		expectedTools[tool.Name] = true
	}

	for name, found := range expectedTools {
		if !found {
			t.Errorf("expected tool %q in tools/list", name)
		}
	}
}

func TestToolsCall_Lifecycle(t *testing.T) {
	dispatcher, tenant, actor := setupMCPEngine(t)
	ctx := context.Background()

	// 1. Remember a claim
	rememberArgs := `{
		"content": "PostgreSQL is used as the primary database in production",
		"confidence": 0.95,
		"evidence": [
			{
				"uri": "https://wiki.internal/db",
				"description": "DB Architecture doc",
				"evidence_type": "url"
			}
		],
		"tags": ["database", "prod"]
	}`

	callReq := &mcp.Request{
		JSONRPC: "2.0",
		ID:      10,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"remember","arguments":` + rememberArgs + `}`),
	}

	resp := dispatcher.Dispatch(ctx, actor, tenant, callReq)
	if resp == nil || resp.Error != nil {
		t.Fatalf("remember tool failed: %v", resp)
	}

	callRes, ok := resp.Result.(*mcp.CallToolResult)
	if !ok {
		t.Fatalf("expected *CallToolResult, got %T", resp.Result)
	}
	if callRes.IsError {
		t.Fatalf("tool returned error: %s", callRes.Content[0].Text)
	}

	var remResp types.RememberResponse
	if err := json.Unmarshal([]byte(callRes.Content[0].Text), &remResp); err != nil {
		t.Fatalf("failed unmarshaling remember response: %v", err)
	}
	if remResp.ClaimID == "" || remResp.Status != "created" {
		t.Fatalf("unexpected remember response: %+v", remResp)
	}
	claimID := remResp.ClaimID

	// 2. Recall the claim
	recallArgs := `{"query":"PostgreSQL database"}`
	recallReq := &mcp.Request{
		JSONRPC: "2.0",
		ID:      11,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"recall","arguments":` + recallArgs + `}`),
	}

	resp = dispatcher.Dispatch(ctx, actor, tenant, recallReq)
	if resp == nil || resp.Error != nil {
		t.Fatalf("recall tool failed: %v", resp)
	}

	callRes, ok = resp.Result.(*mcp.CallToolResult)
	if !ok || callRes.IsError {
		t.Fatalf("recall tool returned error: %+v", callRes)
	}

	var recResp types.RecallResponse
	if err := json.Unmarshal([]byte(callRes.Content[0].Text), &recResp); err != nil {
		t.Fatalf("unmarshaling recall response: %v", err)
	}
	if recResp.TotalMatched == 0 || len(recResp.Claims) == 0 {
		t.Fatalf("expected claims in recall, got 0")
	}
	if recResp.Claims[0].ClaimID != claimID {
		t.Errorf("expected claim ID %q, got %q", claimID, recResp.Claims[0].ClaimID)
	}

	// 3. Inspect the claim
	inspectArgs := `{"claim_id":"` + claimID + `"}`
	inspectReq := &mcp.Request{
		JSONRPC: "2.0",
		ID:      12,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"inspect","arguments":` + inspectArgs + `}`),
	}

	resp = dispatcher.Dispatch(ctx, actor, tenant, inspectReq)
	if resp == nil || resp.Error != nil {
		t.Fatalf("inspect tool failed: %v", resp)
	}

	callRes, ok = resp.Result.(*mcp.CallToolResult)
	if !ok || callRes.IsError {
		t.Fatalf("inspect tool returned error: %+v", callRes)
	}

	var inspResp types.InspectResponse
	if err := json.Unmarshal([]byte(callRes.Content[0].Text), &inspResp); err != nil {
		t.Fatalf("unmarshaling inspect response: %v", err)
	}
	if inspResp.Claim.ID != claimID {
		t.Errorf("inspect expected claim ID %q, got %q", claimID, inspResp.Claim.ID)
	}

	// 4. Forget the claim
	forgetArgs := `{"claim_id":"` + claimID + `","reason":"migrated to CockroachDB"}`
	forgetReq := &mcp.Request{
		JSONRPC: "2.0",
		ID:      13,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"forget","arguments":` + forgetArgs + `}`),
	}

	resp = dispatcher.Dispatch(ctx, actor, tenant, forgetReq)
	if resp == nil || resp.Error != nil {
		t.Fatalf("forget tool failed: %v", resp)
	}

	callRes, ok = resp.Result.(*mcp.CallToolResult)
	if !ok || callRes.IsError {
		t.Fatalf("forget tool returned error: %+v", callRes)
	}

	var forgetResp types.ForgetResponse
	if err := json.Unmarshal([]byte(callRes.Content[0].Text), &forgetResp); err != nil {
		t.Fatalf("unmarshaling forget response: %v", err)
	}
	if forgetResp.ClaimID != claimID || forgetResp.ForgottenAt == "" {
		t.Errorf("unexpected forget response: %+v", forgetResp)
	}
}

func TestResources_Lifecycle(t *testing.T) {
	dispatcher, tenant, actor := setupMCPEngine(t)
	ctx := context.Background()

	// 1. List resources
	reqList := &mcp.Request{
		JSONRPC: "2.0",
		ID:      20,
		Method:  "resources/list",
	}
	resp := dispatcher.Dispatch(ctx, actor, tenant, reqList)
	if resp == nil || resp.Error != nil {
		t.Fatalf("resources/list failed: %v", resp)
	}
	resList, ok := resp.Result.(mcp.ResourcesListResult)
	if !ok || len(resList.Resources) == 0 {
		t.Fatalf("expected resources in list, got %+v", resp.Result)
	}

	// 2. List resource templates
	reqTemplates := &mcp.Request{
		JSONRPC: "2.0",
		ID:      21,
		Method:  "resources/templates/list",
	}
	resp = dispatcher.Dispatch(ctx, actor, tenant, reqTemplates)
	if resp == nil || resp.Error != nil {
		t.Fatalf("resources/templates/list failed: %v", resp)
	}
	tplList, ok := resp.Result.(mcp.ResourceTemplatesListResult)
	if !ok || len(tplList.ResourceTemplates) < 3 {
		t.Fatalf("expected at least 3 resource templates, got %+v", resp.Result)
	}

	// 3. Store a claim to read via resource
	remArgs := `{"content":"Resource test fact","confidence":0.8,"evidence":[{"uri":"file://test","description":"test","evidence_type":"file"}]}`
	remReq := &mcp.Request{
		JSONRPC: "2.0",
		ID:      22,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"remember","arguments":` + remArgs + `}`),
	}
	remResp := dispatcher.Dispatch(ctx, actor, tenant, remReq)
	callRes := remResp.Result.(*mcp.CallToolResult)
	var remData types.RememberResponse
	_ = json.Unmarshal([]byte(callRes.Content[0].Text), &remData)
	claimID := remData.ClaimID

	// 4. Read claim resource
	readClaimReq := &mcp.Request{
		JSONRPC: "2.0",
		ID:      23,
		Method:  "resources/read",
		Params:  json.RawMessage(`{"uri":"gams://claims/` + claimID + `"}`),
	}
	resp = dispatcher.Dispatch(ctx, actor, tenant, readClaimReq)
	if resp == nil || resp.Error != nil {
		t.Fatalf("read claim resource failed: %v", resp)
	}
	readRes, ok := resp.Result.(*mcp.ReadResourceResult)
	if !ok || len(readRes.Contents) == 0 {
		t.Fatalf("expected resource contents, got %+v", resp.Result)
	}

	// 5. Read namespace resource
	readNsReq := &mcp.Request{
		JSONRPC: "2.0",
		ID:      24,
		Method:  "resources/read",
		Params:  json.RawMessage(`{"uri":"gams://namespaces/default"}`),
	}
	resp = dispatcher.Dispatch(ctx, actor, tenant, readNsReq)
	if resp == nil || resp.Error != nil {
		t.Fatalf("read namespace resource failed: %v", resp)
	}

	// 6. Read tenant summary resource
	readTenantReq := &mcp.Request{
		JSONRPC: "2.0",
		ID:      25,
		Method:  "resources/read",
		Params:  json.RawMessage(`{"uri":"gams://tenants/` + tenant.ID + `/summary"}`),
	}
	resp = dispatcher.Dispatch(ctx, actor, tenant, readTenantReq)
	if resp == nil || resp.Error != nil {
		t.Fatalf("read tenant summary resource failed: %v", resp)
	}
}

func TestInvalidMethodAndRequest(t *testing.T) {
	dispatcher, tenant, actor := setupMCPEngine(t)
	ctx := context.Background()

	// Nil request
	resp := dispatcher.Dispatch(ctx, actor, tenant, nil)
	if resp == nil || resp.Error == nil || resp.Error.Code != mcp.CodeInvalidRequest {
		t.Errorf("expected CodeInvalidRequest for nil request, got %v", resp)
	}

	// Invalid jsonrpc version
	req := &mcp.Request{
		JSONRPC: "1.0",
		ID:      1,
		Method:  "ping",
	}
	resp = dispatcher.Dispatch(ctx, actor, tenant, req)
	if resp == nil || resp.Error == nil || resp.Error.Code != mcp.CodeInvalidRequest {
		t.Errorf("expected CodeInvalidRequest for bad jsonrpc version, got %v", resp)
	}

	// Unknown method
	req = &mcp.Request{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "unknown/method",
	}
	resp = dispatcher.Dispatch(ctx, actor, tenant, req)
	if resp == nil || resp.Error == nil || resp.Error.Code != mcp.CodeMethodNotFound {
		t.Errorf("expected CodeMethodNotFound for unknown method, got %v", resp)
	}
}
