package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PDK1744/gams/internal/config"
	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/internal/mcp"
	"github.com/PDK1744/gams/internal/server"
	"github.com/PDK1744/gams/pkg/types"
)

// mcpTestClient acts as an independent agent communicating over Streamable HTTP (SSE + POST).
type mcpTestClient struct {
	baseURL    string
	apiKey     string
	sessionID  string
	sseResp    *http.Response
	reader     *bufio.Reader
	cancel     context.CancelFunc
	nextReqID  atomic.Int64
	httpClient *http.Client
}

func connectMCPClient(t *testing.T, baseURL, apiKey string) *mcpTestClient {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/mcp/sse", nil)
	if err != nil {
		cancel()
		t.Fatalf("creating sse request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("connecting sse: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		cancel()
		resp.Body.Close()
		t.Fatalf("sse connection failed with status: %d", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	evName, evData, err := readSSEEvent(reader)
	if err != nil {
		cancel()
		resp.Body.Close()
		t.Fatalf("reading endpoint event: %v", err)
	}

	if evName != "endpoint" {
		cancel()
		resp.Body.Close()
		t.Fatalf("expected endpoint event, got %q", evName)
	}

	sessionID := strings.TrimPrefix(evData, "/mcp/message?session_id=")
	if sessionID == "" {
		cancel()
		resp.Body.Close()
		t.Fatalf("failed to extract session ID from endpoint %q", evData)
	}

	c := &mcpTestClient{
		baseURL:    baseURL,
		apiKey:     apiKey,
		sessionID:  sessionID,
		sseResp:    resp,
		reader:     reader,
		cancel:     cancel,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	return c
}

func (c *mcpTestClient) Close() {
	if c.cancel != nil {
		c.cancel()
	}
	if c.sseResp != nil && c.sseResp.Body != nil {
		c.sseResp.Body.Close()
	}
}

func (c *mcpTestClient) Call(t *testing.T, method string, params any) *mcp.Response {
	t.Helper()

	reqID := c.nextReqID.Add(1)
	reqObj := mcp.Request{
		JSONRPC: "2.0",
		ID:      reqID,
		Method:  method,
	}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshaling params: %v", err)
		}
		reqObj.Params = b
	}

	payload, err := json.Marshal(reqObj)
	if err != nil {
		t.Fatalf("marshaling rpc request: %v", err)
	}

	postReq, err := http.NewRequest(http.MethodPost, c.baseURL+"/mcp/message", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("creating post request: %v", err)
	}
	postReq.Header.Set("Mcp-Session-Id", c.sessionID)
	postReq.Header.Set("Content-Type", "application/json")

	postResp, err := c.httpClient.Do(postReq)
	if err != nil {
		t.Fatalf("executing post request: %v", err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(postResp.Body)
		t.Fatalf("expected 202 Accepted, got %d: %s", postResp.StatusCode, string(body))
	}

	// Read response on SSE stream
	evName, evData, err := readSSEEvent(c.reader)
	if err != nil {
		t.Fatalf("reading response from sse stream: %v", err)
	}
	if evName != "message" {
		t.Fatalf("expected 'message' sse event, got %q", evName)
	}

	var rpcResp mcp.Response
	if err := json.Unmarshal([]byte(evData), &rpcResp); err != nil {
		t.Fatalf("unmarshaling rpc response: %v; body=%s", err, evData)
	}

	return &rpcResp
}

func (c *mcpTestClient) Notify(t *testing.T, method string, params any) {
	t.Helper()

	reqObj := mcp.Request{
		JSONRPC: "2.0",
		Method:  method,
	}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshaling params: %v", err)
		}
		reqObj.Params = b
	}

	payload, err := json.Marshal(reqObj)
	if err != nil {
		t.Fatalf("marshaling notification: %v", err)
	}

	postReq, err := http.NewRequest(http.MethodPost, c.baseURL+"/mcp/message", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("creating notification request: %v", err)
	}
	postReq.Header.Set("Mcp-Session-Id", c.sessionID)
	postReq.Header.Set("Content-Type", "application/json")

	postResp, err := c.httpClient.Do(postReq)
	if err != nil {
		t.Fatalf("sending notification: %v", err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted for notification, got %d", postResp.StatusCode)
	}
}

func readSSEEvent(reader *bufio.Reader) (string, string, error) {
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
			// keepalive comment
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			eventName = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		}
	}
}

// TestPhase2Demo implements the full end-to-end multi-agent scenario described in gams-project-plan.md:
// "Two concurrent agent sessions write and recall from the same namespace; verify isolation between namespaces."
// "Both scripts write claims to the same namespace. Each then recalls and sees each other's claims.
// This is the first proof that GAMS works as a shared memory server, not a sidecar."
func TestPhase2Demo(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer database.Close()

	cfg := config.Default()
	adminKey := "super-secret-admin-key"
	cfg.Server.AdminKey = adminKey
	cfg.Server.CORSOrigins = []string{"*"}

	srv := server.New(cfg, database, "0.1.0", nil)

	// Admin setup
	t.Log("Step 1: Admin creates tenant 'Multi-Agent Labs'")
	tenant, err := srv.Engine().CreateTenant(ctx, &types.CreateTenantRequest{
		Name:                 "Multi-Agent Labs",
		AutoCreateNamespaces: false,
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating tenant: %v", err)
	}

	descCollab := "Shared context across all autonomous agents"
	descPrivate := "Private working memory for Agent Alpha"

	t.Log("Step 2: Admin creates shared namespace 'project-collab' and isolated namespace 'agent-alpha-private'")
	_, err = srv.Engine().CreateNamespace(ctx, tenant.ID, &types.CreateNamespaceRequest{
		Slug:        "project-collab",
		Name:        "Shared Collaboration Space",
		Description: &descCollab,
		AccessMode:  "tenant_wide",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating collab namespace: %v", err)
	}

	_, err = srv.Engine().CreateNamespace(ctx, tenant.ID, &types.CreateNamespaceRequest{
		Slug:        "agent-alpha-private",
		Name:        "Agent Alpha Private Workspace",
		Description: &descPrivate,
		AccessMode:  "tenant_wide",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating private namespace: %v", err)
	}

	t.Log("Step 3: Admin issues API keys for Agent Alpha and Agent Beta")
	keyAlphaResp, err := srv.Engine().CreateAPIKey(ctx, tenant.ID, &types.CreateAPIKeyRequest{
		Label: "agent-alpha",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating key for alpha: %v", err)
	}

	keyBetaResp, err := srv.Engine().CreateAPIKey(ctx, tenant.ID, &types.CreateAPIKeyRequest{
		Label: "agent-beta",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating key for beta: %v", err)
	}

	// Start test HTTP server
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	t.Log("Step 4: Agent Alpha and Agent Beta connect concurrently via Streamable HTTP (GET /mcp/sse)")
	clientAlpha := connectMCPClient(t, ts.URL, keyAlphaResp.Key)
	defer clientAlpha.Close()

	clientBeta := connectMCPClient(t, ts.URL, keyBetaResp.Key)
	defer clientBeta.Close()

	t.Logf("Agent Alpha connected with Session ID: %s", clientAlpha.sessionID)
	t.Logf("Agent Beta connected with Session ID: %s", clientBeta.sessionID)

	if clientAlpha.sessionID == clientBeta.sessionID {
		t.Fatalf("expected unique session IDs, got identical: %s", clientAlpha.sessionID)
	}

	t.Log("Step 5: Both agents execute MCP initialize handshake and initialized notification")
	initAlpha := clientAlpha.Call(t, "initialize", mcp.InitializeParams{
		ProtocolVersion: "2024-11-05",
		ClientInfo:      mcp.ClientInfo{Name: "agent-alpha-cli", Version: "1.0"},
	})
	if initAlpha.Error != nil {
		t.Fatalf("Alpha initialize failed: %+v", initAlpha.Error)
	}
	clientAlpha.Notify(t, "notifications/initialized", nil)

	initBeta := clientBeta.Call(t, "initialize", mcp.InitializeParams{
		ProtocolVersion: "2024-11-05",
		ClientInfo:      mcp.ClientInfo{Name: "agent-beta-cli", Version: "1.0"},
	})
	if initBeta.Error != nil {
		t.Fatalf("Beta initialize failed: %+v", initBeta.Error)
	}
	clientBeta.Notify(t, "notifications/initialized", nil)

	t.Log("Step 6: Agent Alpha writes an architectural claim to 'project-collab'")
	alphaRememberResp := clientAlpha.Call(t, "tools/call", mcp.CallToolParams{
		Name: "remember",
		Arguments: json.RawMessage(`{
			"content": "Architecture decision: Event streaming pipeline uses NATS JetStream",
			"confidence": 0.95,
			"evidence": [
				{
					"uri": "https://wiki.internal/rfc/0042",
					"description": "RFC 42: Event Streaming Selection",
					"evidence_type": "url"
				}
			],
			"namespace": "project-collab",
			"tags": ["architecture", "messaging", "nats"]
		}`),
	})
	if alphaRememberResp.Error != nil {
		t.Fatalf("Alpha remember call failed: %+v", alphaRememberResp.Error)
	}

	var alphaRemResult struct {
		Content []mcp.TextContent `json:"content"`
		IsError bool              `json:"isError"`
	}
	remBytes, _ := json.Marshal(alphaRememberResp.Result)
	_ = json.Unmarshal(remBytes, &alphaRemResult)
	if alphaRemResult.IsError || len(alphaRemResult.Content) == 0 {
		t.Fatalf("Alpha remember returned tool error: %+v", alphaRemResult)
	}
	var alphaClaim types.RememberResponse
	_ = json.Unmarshal([]byte(alphaRemResult.Content[0].Text), &alphaClaim)
	t.Logf("Agent Alpha stored claim %s (v%d)", alphaClaim.ClaimID, alphaClaim.Version)

	t.Log("Step 7: Agent Beta writes an infrastructure claim to 'project-collab'")
	betaRememberResp := clientBeta.Call(t, "tools/call", mcp.CallToolParams{
		Name: "remember",
		Arguments: json.RawMessage(`{
			"content": "Infrastructure decision: Persistent key-value cache uses SQLite in WAL mode",
			"confidence": 0.90,
			"evidence": [
				{
					"uri": "https://wiki.internal/infra/storage",
					"description": "Infra Storage Evaluation",
					"evidence_type": "url"
				}
			],
			"namespace": "project-collab",
			"tags": ["architecture", "storage", "sqlite"]
		}`),
	})
	if betaRememberResp.Error != nil {
		t.Fatalf("Beta remember call failed: %+v", betaRememberResp.Error)
	}

	var betaRemResult struct {
		Content []mcp.TextContent `json:"content"`
		IsError bool              `json:"isError"`
	}
	remBytes, _ = json.Marshal(betaRememberResp.Result)
	_ = json.Unmarshal(remBytes, &betaRemResult)
	if betaRemResult.IsError || len(betaRemResult.Content) == 0 {
		t.Fatalf("Beta remember returned tool error: %+v", betaRemResult)
	}
	var betaClaim types.RememberResponse
	_ = json.Unmarshal([]byte(betaRemResult.Content[0].Text), &betaClaim)
	t.Logf("Agent Beta stored claim %s (v%d)", betaClaim.ClaimID, betaClaim.Version)

	t.Log("Step 8: Shared Memory Proof — Agent Beta recalls Agent Alpha's claim from 'project-collab'")
	betaRecallResp := clientBeta.Call(t, "tools/call", mcp.CallToolParams{
		Name: "recall",
		Arguments: json.RawMessage(`{
			"query": "NATS JetStream event streaming",
			"namespace": "project-collab"
		}`),
	})
	if betaRecallResp.Error != nil {
		t.Fatalf("Beta recall failed: %+v", betaRecallResp.Error)
	}
	var betaRecallResult struct {
		Content []mcp.TextContent `json:"content"`
	}
	recBytes, _ := json.Marshal(betaRecallResp.Result)
	_ = json.Unmarshal(recBytes, &betaRecallResult)
	var betaRecallData types.RecallResponse
	_ = json.Unmarshal([]byte(betaRecallResult.Content[0].Text), &betaRecallData)

	if betaRecallData.TotalMatched == 0 || len(betaRecallData.Claims) == 0 {
		t.Fatalf("Beta expected to find Alpha's claim, found 0 results")
	}
	if betaRecallData.Claims[0].ClaimID != alphaClaim.ClaimID {
		t.Errorf("Beta expected claim %s, got %s", alphaClaim.ClaimID, betaRecallData.Claims[0].ClaimID)
	}
	t.Logf("PASS: Agent Beta successfully recalled claim written by Agent Alpha (writer: %s)", betaRecallData.Claims[0].WriterLabel)

	t.Log("Step 9: Shared Memory Proof — Agent Alpha recalls Agent Beta's claim from 'project-collab'")
	alphaRecallResp := clientAlpha.Call(t, "tools/call", mcp.CallToolParams{
		Name: "recall",
		Arguments: json.RawMessage(`{
			"query": "SQLite WAL persistent cache",
			"namespace": "project-collab"
		}`),
	})
	if alphaRecallResp.Error != nil {
		t.Fatalf("Alpha recall failed: %+v", alphaRecallResp.Error)
	}
	var alphaRecallResult struct {
		Content []mcp.TextContent `json:"content"`
	}
	recBytes, _ = json.Marshal(alphaRecallResp.Result)
	_ = json.Unmarshal(recBytes, &alphaRecallResult)
	var alphaRecallData types.RecallResponse
	_ = json.Unmarshal([]byte(alphaRecallResult.Content[0].Text), &alphaRecallData)

	if alphaRecallData.TotalMatched == 0 || len(alphaRecallData.Claims) == 0 {
		t.Fatalf("Alpha expected to find Beta's claim, found 0 results")
	}
	if alphaRecallData.Claims[0].ClaimID != betaClaim.ClaimID {
		t.Errorf("Alpha expected claim %s, got %s", betaClaim.ClaimID, alphaRecallData.Claims[0].ClaimID)
	}
	t.Logf("PASS: Agent Alpha successfully recalled claim written by Agent Beta (writer: %s)", alphaRecallData.Claims[0].WriterLabel)

	t.Log("Step 10: Namespace Isolation Verification — Agent Alpha writes secret to 'agent-alpha-private'")
	clientAlpha.Call(t, "tools/call", mcp.CallToolParams{
		Name: "remember",
		Arguments: json.RawMessage(`{
			"content": "Secret credentials: Alpha internal API token rotates every 6 hours",
			"confidence": 1.0,
			"evidence": [{"uri": "secret://internal", "description": "Token policy", "evidence_type": "file"}],
			"namespace": "agent-alpha-private",
			"tags": ["secret"]
		}`),
	})

	t.Log("Step 11: Namespace Isolation Verification — Agent Beta queries for secret scoped to 'project-collab'")
	betaIsolatedRecall := clientBeta.Call(t, "tools/call", mcp.CallToolParams{
		Name: "recall",
		Arguments: json.RawMessage(`{
			"query": "secret credentials token rotates",
			"namespace": "project-collab"
		}`),
	})
	var isoResult struct {
		Content []mcp.TextContent `json:"content"`
	}
	isoBytes, _ := json.Marshal(betaIsolatedRecall.Result)
	_ = json.Unmarshal(isoBytes, &isoResult)
	var isoData types.RecallResponse
	_ = json.Unmarshal([]byte(isoResult.Content[0].Text), &isoData)

	if isoData.TotalMatched != 0 {
		t.Fatalf("Namespace isolation failed: Agent Beta saw private claim in 'project-collab' query! Matched: %d", isoData.TotalMatched)
	}
	t.Log("PASS: Agent Beta cannot recall private claim from 'project-collab' (Namespace Isolation Verified)")

	t.Log("Step 12: MCP Resource Browsing — Read namespace and tenant summary resources")
	resResp := clientBeta.Call(t, "resources/read", mcp.ReadResourceParams{
		URI: "gams://namespaces/project-collab",
	})
	if resResp.Error != nil {
		t.Fatalf("Beta read namespace resource failed: %+v", resResp.Error)
	}

	summaryResp := clientAlpha.Call(t, "resources/read", mcp.ReadResourceParams{
		URI: fmt.Sprintf("gams://tenants/%s/summary", tenant.ID),
	})
	if summaryResp.Error != nil {
		t.Fatalf("Alpha read tenant summary resource failed: %+v", summaryResp.Error)
	}

	t.Log("Step 13: Governance Inspection — Agent Beta inspects full audit history and evidence of Alpha's claim")
	inspectResp := clientBeta.Call(t, "tools/call", mcp.CallToolParams{
		Name:      "inspect",
		Arguments: json.RawMessage(fmt.Sprintf(`{"claim_id": "%s"}`, alphaClaim.ClaimID)),
	})
	if inspectResp.Error != nil {
		t.Fatalf("inspect failed: %+v", inspectResp.Error)
	}
	var inspectResult struct {
		Content []mcp.TextContent `json:"content"`
	}
	inspBytes, _ := json.Marshal(inspectResp.Result)
	_ = json.Unmarshal(inspBytes, &inspectResult)
	var inspectData types.InspectResponse
	_ = json.Unmarshal([]byte(inspectResult.Content[0].Text), &inspectData)

	if inspectData.Claim.ID != alphaClaim.ClaimID {
		t.Errorf("inspect claim ID mismatch: %s vs %s", inspectData.Claim.ID, alphaClaim.ClaimID)
	}
	if len(inspectData.Evidence) == 0 {
		t.Errorf("expected evidence in inspect response, got 0")
	}
	if len(inspectData.Audit) == 0 {
		t.Errorf("expected audit trail entries in inspect response, got 0")
	}
	t.Logf("PASS: Full governance history inspected (Versions: %d, Evidence items: %d, Audit events: %d)",
		len(inspectData.Versions), len(inspectData.Evidence), len(inspectData.Audit))

	t.Log("Phase 2 Demo: Concurrent Multi-Agent Shared Memory completed successfully!")
}
