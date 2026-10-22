package httptransport_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PDK1744/gams/internal/config"
	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/internal/mcp"
	"github.com/PDK1744/gams/internal/server"
	httptransport "github.com/PDK1744/gams/internal/transport/http"
	"github.com/PDK1744/gams/pkg/types"
)

func setupTestServer(t *testing.T) (*server.Server, *types.Tenant, string) {
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
	cfg.Server.CORSOrigins = []string{"*"}

	srv := server.New(cfg, database, "0.1.0", nil)

	tenant, err := srv.Engine().CreateTenant(ctx, &types.CreateTenantRequest{
		Name: "HTTP Transport Tenant",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating tenant: %v", err)
	}

	keyResp, err := srv.Engine().CreateAPIKey(ctx, tenant.ID, &types.CreateAPIKeyRequest{
		Label: "transport-test-key",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating api key: %v", err)
	}

	return srv, tenant, keyResp.Key
}

func parseSSEEvents(reader *bufio.Reader) (string, string, error) {
	var eventName string
	var dataLines []string

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if eventName != "" || len(dataLines) > 0 {
				return eventName, strings.Join(dataLines, "\n"), nil
			}
			continue
		}

		if strings.HasPrefix(line, ":") {
			// Comment / heartbeat
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			eventName = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		}
	}
}

func TestSSEAuth(t *testing.T) {
	srv, _, apiKey := setupTestServer(t)

	// 1. Missing auth
	req := httptest.NewRequest(http.MethodGet, "/mcp/sse", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing auth, got %d", w.Code)
	}

	// 2. Invalid auth
	req = httptest.NewRequest(http.MethodGet, "/mcp/sse", nil)
	req.Header.Set("Authorization", "Bearer invalid-key")
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid auth, got %d", w.Code)
	}

	// 3. Query param auth rejected (CR-003)
	req = httptest.NewRequest(http.MethodGet, "/mcp/sse?api_key="+apiKey, nil)
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for query param api_key auth, got %d", w.Code)
	}

	// 4. Valid Authorization Bearer header
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/mcp/sse", nil)
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("connecting to SSE: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for SSE, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}

	reader := bufio.NewReader(resp.Body)
	evName, evData, err := parseSSEEvents(reader)
	if err != nil {
		t.Fatalf("parsing initial SSE event: %v", err)
	}
	if evName != "endpoint" {
		t.Errorf("expected event 'endpoint', got %q", evName)
	}
	if !strings.HasPrefix(evData, "/mcp/message?session_id=") {
		t.Errorf("expected endpoint data with session_id, got %q", evData)
	}

	cancel()
}

func TestMCPMessageValidation(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	// 1. Missing session ID
	req := httptest.NewRequest(http.MethodPost, "/mcp/message", strings.NewReader(`{"jsonrpc":"2.0"}`))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing session ID, got %d", w.Code)
	}

	// 2. Non-existent session ID
	req = httptest.NewRequest(http.MethodPost, "/mcp/message?session_id=non-existent", strings.NewReader(`{"jsonrpc":"2.0"}`))
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for invalid session ID, got %d", w.Code)
	}
}

func TestStreamableHTTPLifecycle(t *testing.T) {
	srv, _, apiKey := setupTestServer(t)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Connect SSE
	sseReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/mcp/sse", nil)
	sseReq.Header.Set("Authorization", "Bearer "+apiKey)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("connecting SSE: %v", err)
	}
	defer sseResp.Body.Close()

	reader := bufio.NewReader(sseResp.Body)
	evName, evData, err := parseSSEEvents(reader)
	if err != nil {
		t.Fatalf("reading endpoint event: %v", err)
	}
	if evName != "endpoint" {
		t.Fatalf("expected 'endpoint' event, got %q", evName)
	}

	sessionID := strings.TrimPrefix(evData, "/mcp/message?session_id=")
	if sessionID == "" {
		t.Fatalf("empty session ID extracted from endpoint %q", evData)
	}

	// 2. Send initialize JSON-RPC request over POST /mcp/message
	initPayload := `{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "initialize",
		"params": {
			"protocolVersion": "2024-11-05",
			"capabilities": {},
			"clientInfo": {"name": "test-agent", "version": "1.0"}
		}
	}`

	postReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp/message", strings.NewReader(initPayload))
	postReq.Header.Set("Mcp-Session-Id", sessionID)
	postReq.Header.Set("Content-Type", "application/json")
	postResp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("sending post message: %v", err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", postResp.StatusCode)
	}

	// 3. Read initialize response from SSE stream
	evName, evData, err = parseSSEEvents(reader)
	if err != nil {
		t.Fatalf("reading SSE response: %v", err)
	}
	if evName != "message" {
		t.Errorf("expected 'message' event, got %q", evName)
	}

	var rpcResp mcp.Response
	if err := json.Unmarshal([]byte(evData), &rpcResp); err != nil {
		t.Fatalf("unmarshaling rpc response: %v; data=%s", err, evData)
	}
	if rpcResp.Error != nil {
		t.Fatalf("unexpected RPC error: %+v", rpcResp.Error)
	}

	// 4. Send initialized notification (should not produce any response on SSE)
	notifyPayload := `{
		"jsonrpc": "2.0",
		"method": "notifications/initialized"
	}`
	postReq, _ = http.NewRequest(http.MethodPost, ts.URL+"/mcp/message", strings.NewReader(notifyPayload))
	postReq.Header.Set("Mcp-Session-Id", sessionID)
	postResp, err = http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("sending notification: %v", err)
	}
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted for notification, got %d", postResp.StatusCode)
	}

	// 5. Send tools/call remember request
	rememberPayload := `{
		"jsonrpc": "2.0",
		"id": 2,
		"method": "tools/call",
		"params": {
			"name": "remember",
			"arguments": {
				"content": "GAMS uses SSE for server-to-agent streaming",
				"confidence": 1.0,
				"evidence": [{"uri": "doc://arch", "description": "Architecture spec", "evidence_type": "file"}]
			}
		}
	}`
	postReq, _ = http.NewRequest(http.MethodPost, ts.URL+"/mcp/message", strings.NewReader(rememberPayload))
	postReq.Header.Set("Mcp-Session-Id", sessionID)
	postResp, err = http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("sending remember tool call: %v", err)
	}
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", postResp.StatusCode)
	}

	evName, evData, err = parseSSEEvents(reader)
	if err != nil {
		t.Fatalf("reading remember response from SSE: %v", err)
	}
	if evName != "message" {
		t.Errorf("expected 'message' event, got %q", evName)
	}

	var remRPCResp mcp.Response
	if err := json.Unmarshal([]byte(evData), &remRPCResp); err != nil {
		t.Fatalf("unmarshaling remember response: %v", err)
	}
	if remRPCResp.Error != nil {
		t.Fatalf("remember RPC error: %+v", remRPCResp.Error)
	}

	// 6. Send tools/call recall request
	recallPayload := `{
		"jsonrpc": "2.0",
		"id": 3,
		"method": "tools/call",
		"params": {
			"name": "recall",
			"arguments": {
				"query": "SSE streaming"
			}
		}
	}`
	postReq, _ = http.NewRequest(http.MethodPost, ts.URL+"/mcp/message", strings.NewReader(recallPayload))
	postReq.Header.Set("Mcp-Session-Id", sessionID)
	postResp, err = http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("sending recall tool call: %v", err)
	}
	postResp.Body.Close()

	evName, evData, err = parseSSEEvents(reader)
	if err != nil {
		t.Fatalf("reading recall response from SSE: %v", err)
	}

	var recRPCResp mcp.Response
	if err := json.Unmarshal([]byte(evData), &recRPCResp); err != nil {
		t.Fatalf("unmarshaling recall response: %v", err)
	}
	if recRPCResp.Error != nil {
		t.Fatalf("recall RPC error: %+v", recRPCResp.Error)
	}
}

func TestSessionManager_Cleanup(t *testing.T) {
	sm := httptransport.NewSessionManager(10 * time.Millisecond)
	defer sm.Close()

	actor := &types.APIKey{ID: "key-1", TenantID: "tenant-1"}
	tenant := &types.Tenant{ID: "tenant-1", Status: "active"}

	sess := sm.CreateSession(actor, tenant)
	if _, ok := sm.Get(sess.ID); !ok {
		t.Fatalf("expected session to exist")
	}

	time.Sleep(30 * time.Millisecond)
	sm.CleanupStale()

	if _, ok := sm.Get(sess.ID); ok {
		t.Fatalf("expected session to be cleaned up after idle ttl")
	}
}

func TestCORSPreflight(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodOptions, "/mcp/message", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type, Mcp-Session-Id")

	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204 No Content for CORS preflight, got %d", w.Code)
	}

	allowHeaders := w.Header().Get("Access-Control-Allow-Headers")
	if !strings.Contains(allowHeaders, "Mcp-Session-Id") {
		t.Errorf("expected Mcp-Session-Id in Access-Control-Allow-Headers, got %q", allowHeaders)
	}
}

func TestBatchAndSessionCompatibility(t *testing.T) {
	srv, tenant, apiKey := setupTestServer(t)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Connect SSE
	sseReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/mcp/sse", nil)
	sseReq.Header.Set("Authorization", "Bearer "+apiKey)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("connecting sse: %v", err)
	}
	defer sseResp.Body.Close()

	reader := bufio.NewReader(sseResp.Body)
	_, evData, err := parseSSEEvents(reader)
	if err != nil {
		t.Fatalf("reading endpoint event: %v", err)
	}

	sessionID := strings.TrimPrefix(evData, "/mcp/message?session_id=")

	// 2. Test camelCase sessionId query parameter
	camelPostReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp/message?sessionId="+sessionID, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	camelResp, err := http.DefaultClient.Do(camelPostReq)
	if err != nil {
		t.Fatalf("post with sessionId failed: %v", err)
	}
	camelResp.Body.Close()
	if camelResp.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202 Accepted for sessionId param, got %d", camelResp.StatusCode)
	}
	_, _, _ = parseSSEEvents(reader) // read ping response

	// 3. Test empty batch []
	emptyBatchReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp/message?session_id="+sessionID, strings.NewReader(`[]`))
	emptyBatchResp, err := http.DefaultClient.Do(emptyBatchReq)
	if err != nil {
		t.Fatalf("post empty batch failed: %v", err)
	}
	emptyBatchResp.Body.Close()
	if emptyBatchResp.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202 Accepted for empty batch, got %d", emptyBatchResp.StatusCode)
	}

	_, emptyBatchData, err := parseSSEEvents(reader)
	if err != nil {
		t.Fatalf("reading empty batch error response: %v", err)
	}
	var errRPC mcp.Response
	_ = json.Unmarshal([]byte(emptyBatchData), &errRPC)
	if errRPC.Error == nil || errRPC.Error.Code != mcp.CodeInvalidRequest {
		t.Errorf("expected CodeInvalidRequest (-32600) for empty batch, got: %+v", errRPC)
	}

	// 4. Test multi-request batch returns single JSON array response
	batchPayload := `[
		{"jsonrpc":"2.0","id":10,"method":"ping"},
		{"jsonrpc":"2.0","id":11,"method":"ping"}
	]`
	batchReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp/message?session_id="+sessionID, strings.NewReader(batchPayload))
	batchResp, err := http.DefaultClient.Do(batchReq)
	if err != nil {
		t.Fatalf("post batch failed: %v", err)
	}
	batchResp.Body.Close()
	if batchResp.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202 Accepted for batch request, got %d", batchResp.StatusCode)
	}

	_, batchData, err := parseSSEEvents(reader)
	if err != nil {
		t.Fatalf("reading batch responses: %v", err)
	}
	var batchResponses []mcp.Response
	if err := json.Unmarshal([]byte(batchData), &batchResponses); err != nil {
		t.Fatalf("expected batch response to unmarshal as JSON array, got error: %v, data: %s", err, batchData)
	}
	if len(batchResponses) != 2 {
		t.Errorf("expected 2 responses in batch array, got %d", len(batchResponses))
	}

	// 5. Test mismatched Authorization header on POST
	mismatchedKeyResp, _ := srv.Engine().CreateAPIKey(ctx, tenant.ID, &types.CreateAPIKeyRequest{
		Label: "other-agent",
	}, "admin", "admin")

	badAuthReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp/message?session_id="+sessionID, strings.NewReader(`{"jsonrpc":"2.0","id":20,"method":"ping"}`))
	badAuthReq.Header.Set("Authorization", "Bearer "+mismatchedKeyResp.Key)
	badAuthResp, err := http.DefaultClient.Do(badAuthReq)
	if err != nil {
		t.Fatalf("post with bad auth failed: %v", err)
	}
	badAuthResp.Body.Close()
	if badAuthResp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for mismatched auth header, got %d", badAuthResp.StatusCode)
	}
}
