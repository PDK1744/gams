package stdiotransport_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/PDK1744/gams/internal/config"
	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/internal/mcp"
	"github.com/PDK1744/gams/internal/server"
	stdiotransport "github.com/PDK1744/gams/internal/transport/stdio"
	"github.com/PDK1744/gams/pkg/types"
)

func setupStdioTest(t *testing.T) (*stdiotransport.Handler, *types.Tenant, *types.APIKey) {
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
		Name: "Stdio Test Tenant",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating tenant: %v", err)
	}

	keyResp, err := srv.Engine().CreateAPIKey(ctx, tenant.ID, &types.CreateAPIKeyRequest{
		Label: "stdio-agent",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating api key: %v", err)
	}

	actor, _, err := srv.Engine().Auth().VerifyAPIKey(ctx, keyResp.Key)
	if err != nil {
		t.Fatalf("verifying api key: %v", err)
	}

	dispatcher := mcp.NewDispatcher(srv.Engine(), "0.1.0")
	handler := stdiotransport.NewHandler(dispatcher, actor, tenant, nil)
	return handler, tenant, actor
}

func TestStdioInitializeAndNotification(t *testing.T) {
	handler, _, _ := setupStdioTest(t)

	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"claude-code","version":"1.0"}}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"

	inReader := strings.NewReader(input)
	outBuf := &bytes.Buffer{}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := handler.Run(ctx, inReader, outBuf)
	if err != nil {
		t.Fatalf("handler.Run failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(outBuf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 response line (initialize only, notification silent), got %d lines: %v", len(lines), lines)
	}

	var resp mcp.Response
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("unmarshaling initialize response: %v", err)
	}

	if resp.Error != nil {
		t.Fatalf("initialize response error: %+v", resp.Error)
	}
}

func TestStdioToolsLifecycle(t *testing.T) {
	handler, _, _ := setupStdioTest(t)

	// Pipe to simulate live interactive stream
	inR, inW := io.Pipe()
	outBuf := &bytes.Buffer{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- handler.Run(ctx, inR, outBuf)
	}()

	// 1. Remember claim
	rememberReq := `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"remember","arguments":{"content":"Claude Code remembers this fact via stdio","confidence":1.0,"evidence":[{"uri":"local://test","description":"unit test","evidence_type":"file"}]}}}` + "\n"
	_, _ = inW.Write([]byte(rememberReq))

	time.Sleep(100 * time.Millisecond)

	// 2. Recall claim
	recallReq := `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"recall","arguments":{"query":"Claude Code stdio"}}}` + "\n"
	_, _ = inW.Write([]byte(recallReq))

	time.Sleep(100 * time.Millisecond)
	_ = inW.Close()

	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("handler.Run exited with error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for handler to complete")
	}

	scanner := bufio.NewScanner(outBuf)
	var responses []mcp.Response
	for scanner.Scan() {
		var r mcp.Response
		if err := json.Unmarshal(scanner.Bytes(), &r); err == nil {
			responses = append(responses, r)
		}
	}

	if len(responses) != 2 {
		t.Fatalf("expected 2 responses, got %d: %s", len(responses), outBuf.String())
	}

	if responses[0].ID != float64(10) && responses[0].ID != int64(10) && responses[0].ID != 10 {
		t.Errorf("expected ID 10 on first response, got %v", responses[0].ID)
	}
	if responses[1].ID != float64(11) && responses[1].ID != int64(11) && responses[1].ID != 11 {
		t.Errorf("expected ID 11 on second response, got %v", responses[1].ID)
	}
}

func TestStdioBatchRequests(t *testing.T) {
	handler, _, _ := setupStdioTest(t)

	// Batch request with 2 ping calls
	batchInput := `[{"jsonrpc":"2.0","id":100,"method":"ping"},{"jsonrpc":"2.0","id":101,"method":"ping"}]` + "\n"
	inReader := strings.NewReader(batchInput)
	outBuf := &bytes.Buffer{}

	err := handler.Run(context.Background(), inReader, outBuf)
	if err != nil {
		t.Fatalf("handler.Run failed: %v", err)
	}

	trimmed := strings.TrimSpace(outBuf.String())
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		t.Fatalf("expected JSON array response for batch, got: %s", trimmed)
	}

	var batchResp []mcp.Response
	if err := json.Unmarshal([]byte(trimmed), &batchResp); err != nil {
		t.Fatalf("failed unmarshaling batch response: %v", err)
	}
	if len(batchResp) != 2 {
		t.Fatalf("expected 2 responses in batch, got %d", len(batchResp))
	}
}

func TestStdioContentLengthFraming(t *testing.T) {
	handler, _, _ := setupStdioTest(t)

	body := `{"jsonrpc":"2.0","id":200,"method":"ping"}`
	framed := "Content-Length: " + string(rune(len(body)+'0')) + "\r\n\r\n" + body + "\n"
	// Calculate actual length string
	framed = "Content-Length: 42\r\n\r\n" + body + "\n"

	inReader := strings.NewReader(framed)
	outBuf := &bytes.Buffer{}

	err := handler.Run(context.Background(), inReader, outBuf)
	if err != nil {
		t.Fatalf("handler.Run failed: %v", err)
	}

	var resp mcp.Response
	if err := json.Unmarshal(bytes.TrimSpace(outBuf.Bytes()), &resp); err != nil {
		t.Fatalf("failed parsing response: %v, raw: %s", err, outBuf.String())
	}
	if resp.Error != nil {
		t.Fatalf("ping returned error: %+v", resp.Error)
	}
}

func TestStdioContentLengthOOMProtection(t *testing.T) {
	handler, _, _ := setupStdioTest(t)

	// Hostile content length trying to allocate 500MB
	framed := "Content-Length: 524288000\r\n\r\n{}"
	inReader := strings.NewReader(framed)
	outBuf := &bytes.Buffer{}

	err := handler.Run(context.Background(), inReader, outBuf)
	if err == nil {
		t.Fatalf("expected error for oversized Content-Length, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds maximum limit") {
		t.Errorf("expected error message about payload limit, got: %v", err)
	}
}

func TestStdioHeaderLoopProtection(t *testing.T) {
	handler, _, _ := setupStdioTest(t)

	// Stream with 60 header lines without empty line terminator
	var sb strings.Builder
	sb.WriteString("Content-Length: 42\r\n")
	for i := 0; i < 60; i++ {
		sb.WriteString("X-Random-Header: test\r\n")
	}
	sb.WriteString("\r\n{}")

	inReader := strings.NewReader(sb.String())
	outBuf := &bytes.Buffer{}

	err := handler.Run(context.Background(), inReader, outBuf)
	if err == nil {
		t.Fatalf("expected error for header loop without terminator, got nil")
	}
	if !strings.Contains(err.Error(), "exceeded maximum header line limit") {
		t.Errorf("expected error message about header line limit, got: %v", err)
	}
}
