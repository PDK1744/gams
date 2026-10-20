package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PDK1744/gams/internal/config"
	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/internal/mcp"
	"github.com/PDK1744/gams/internal/server"
	"github.com/PDK1744/gams/pkg/types"
)

func TestStdioZeroConfigBootstrap(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer database.Close()

	cfg := config.Default()
	srv := server.New(cfg, database, "0.1.0", nil)

	inBuf := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"cursor","version":"1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"remember","arguments":{"content":"Zero config local memory working","confidence":1.0,"evidence":[{"uri":"local://test","description":"stdio test","evidence_type":"agent_assertion"}]}}}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"recall","arguments":{"query":"Zero config local memory"}}}` + "\n",
	)
	outBuf := &bytes.Buffer{}

	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// Run with empty apiKey -> tests auto-bootstrap of Local Workspace tenant
	if err := srv.RunStdio(runCtx, inBuf, outBuf, ""); err != nil {
		t.Fatalf("RunStdio failed: %v", err)
	}

	scanner := bufio.NewScanner(outBuf)
	var responses []mcp.Response
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r mcp.Response
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("failed unmarshaling line %q: %v", line, err)
		}
		responses = append(responses, r)
	}

	if len(responses) != 3 {
		t.Fatalf("expected 3 responses (init, remember, recall), got %d: %s", len(responses), outBuf.String())
	}

	// 1. Initialize result
	var initRes mcp.InitializeResult
	b, _ := json.Marshal(responses[0].Result)
	_ = json.Unmarshal(b, &initRes)
	if initRes.ServerInfo.Name != "gams" {
		t.Errorf("expected server name 'gams', got %q", initRes.ServerInfo.Name)
	}

	// 2. Remember result
	var remRes mcp.CallToolResult
	b, _ = json.Marshal(responses[1].Result)
	_ = json.Unmarshal(b, &remRes)
	if remRes.IsError || len(remRes.Content) == 0 {
		t.Fatalf("remember returned error: %+v", remRes)
	}
	var remData types.RememberResponse
	_ = json.Unmarshal([]byte(remRes.Content[0].Text), &remData)
	if remData.ClaimID == "" {
		t.Errorf("expected claim_id in remember response")
	}

	// 3. Recall result
	var recRes mcp.CallToolResult
	b, _ = json.Marshal(responses[2].Result)
	_ = json.Unmarshal(b, &recRes)
	if recRes.IsError || len(recRes.Content) == 0 {
		t.Fatalf("recall returned error: %+v", recRes)
	}
	var recData types.RecallResponse
	_ = json.Unmarshal([]byte(recRes.Content[0].Text), &recData)
	if recData.TotalMatched != 1 {
		t.Errorf("expected 1 match in recall, got %d", recData.TotalMatched)
	}
}

func TestStdioSubprocessExecution(t *testing.T) {
	// Find or build binary
	binPath := filepath.Join("..", "..", "gams")
	if _, err := os.Stat(binPath); err != nil {
		t.Skip("gams binary not built at", binPath)
	}

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "stdio_test.db")
	confPath := filepath.Join(tempDir, "gams.toml")

	confContent := `
[data]
path = "` + dbPath + `"

[server]
host = "127.0.0.1"
http_port = 8089
admin_key = "test-admin"

[mcp]
transport = "stdio"
`
	if err := os.WriteFile(confPath, []byte(confContent), 0644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "serve", "--config", confPath, "--transport", "stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("creating stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("creating stdout pipe: %v", err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting gams stdio subprocess: %v", err)
	}

	// 1. Send initialize
	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"claude-code","version":"1.0"}}}` + "\n"
	if _, err := stdin.Write([]byte(initReq)); err != nil {
		t.Fatalf("writing initialize to stdin: %v", err)
	}

	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading response from stdout: %v; stderr: %s", err, stderr.String())
	}

	var resp mcp.Response
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); err != nil {
		t.Fatalf("stdout contained invalid JSON line: %q, stderr: %s", line, stderr.String())
	}
	if resp.Error != nil {
		t.Fatalf("initialize returned error: %+v", resp.Error)
	}

	// 2. Send ping
	pingReq := `{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n"
	if _, err := stdin.Write([]byte(pingReq)); err != nil {
		t.Fatalf("writing ping: %v", err)
	}

	line, err = reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading ping response: %v; stderr: %s", err, stderr.String())
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); err != nil {
		t.Fatalf("stdout contained invalid JSON on ping: %q", line)
	}

	_ = stdin.Close()
	_ = cmd.Wait()
}

func TestStdioSuspendedLocalTenantRejected(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer database.Close()

	cfg := config.Default()
	srv := server.New(cfg, database, "0.1.0", nil)

	// Pre-create local tenant with suspended status
	_, err = database.DB.ExecContext(ctx,
		`INSERT INTO tenants (id, name, status, recall_decay_lambda, max_claims, auto_create_namespaces, created_at, metadata)
		 VALUES ('local', 'Local Workspace', 'suspended', 0.05, 0, 1, 1000, '{}')`)
	if err != nil {
		t.Fatalf("inserting suspended tenant: %v", err)
	}

	inBuf := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
	outBuf := &bytes.Buffer{}

	err = srv.RunStdio(ctx, inBuf, outBuf, "")
	if err == nil {
		t.Fatalf("expected error when local workspace tenant is suspended, got nil")
	}
	if !strings.Contains(err.Error(), "forbidden") && !strings.Contains(err.Error(), "suspended") {
		t.Errorf("expected forbidden/suspended error, got: %v", err)
	}
}
