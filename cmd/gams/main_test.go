package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PDK1744/gams/internal/auth"
	"github.com/PDK1744/gams/internal/config"
	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/internal/engine"
	"github.com/PDK1744/gams/internal/server"
	"github.com/PDK1744/gams/pkg/types"
)

func TestRunVersion(t *testing.T) {
	if err := run([]string{"version"}); err != nil {
		t.Fatalf("run version failed: %v", err)
	}
}

func TestRunHelp(t *testing.T) {
	if err := run([]string{"help"}); err != nil {
		t.Fatalf("run help failed: %v", err)
	}
}

func TestRunInvalidFlag(t *testing.T) {
	if err := run([]string{"serve", "--invalid-flag"}); err == nil {
		t.Fatalf("expected error for invalid flag, got nil")
	}
}

func TestRunAdminCommands(t *testing.T) {
	if err := run([]string{"admin"}); err != nil {
		t.Fatalf("run admin failed: %v", err)
	}
	if err := run([]string{"admin", "help"}); err != nil {
		t.Fatalf("run admin help failed: %v", err)
	}
	if err := run([]string{"admin", "unknown"}); err == nil {
		t.Fatalf("expected error for unknown admin command, got nil")
	}
	if err := run([]string{"admin", "claims"}); err == nil {
		t.Fatalf("expected error for admin claims without subcommands, got nil")
	}
	if err := run([]string{"admin", "claims", "inspect"}); err == nil {
		t.Fatalf("expected error for admin claims inspect without claim id, got nil")
	}
}

func TestRunAdminClaimsInspectDirectDB(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_admin.db")
	configPath := filepath.Join(tmpDir, "gams.toml")

	tomlContent := fmt.Sprintf(`
[server]
admin_key = "test_admin_key"

[data]
path = %q

[auth]
key_cache_ttl_seconds = 300
`, dbPath)

	if err := os.WriteFile(configPath, []byte(tomlContent), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	ctx := context.Background()
	database, err := db.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer database.Close()

	cfg, _ := config.Load(configPath)
	authSvc := auth.NewService(database.DB, cfg.Server.AdminKey, 300*time.Second)
	eng := engine.New(database, authSvc, cfg, Version)

	tenant, err := eng.CreateTenant(ctx, &types.CreateTenantRequest{
		Name:                 "Test Tenant",
		AutoCreateNamespaces: true,
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating tenant: %v", err)
	}

	keyRes, err := eng.CreateAPIKey(ctx, tenant.ID, &types.CreateAPIKeyRequest{
		Label: "cli-agent",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("issuing key: %v", err)
	}

	actor, _, err := authSvc.VerifyAPIKey(ctx, keyRes.Key)
	if err != nil {
		t.Fatalf("verifying key: %v", err)
	}

	remRes, err := eng.Remember(ctx, actor, tenant, &types.RememberRequest{
		Content:    "PostgreSQL WAL achieves high concurrency",
		Confidence: 0.95,
		Tags:       []string{"postgres", "storage"},
		Evidence: []types.EvidenceInput{
			{
				URI:          "https://postgresql.org/docs/wal",
				Description:  "Postgres WAL docs",
				EvidenceType: "url",
			},
		},
	})
	if err != nil {
		t.Fatalf("remembering claim: %v", err)
	}

	// 1. Inspect via Direct DB (text format)
	err = run([]string{"admin", "claims", "inspect", remRes.ClaimID, "--config", configPath, "--format", "text"})
	if err != nil {
		t.Fatalf("admin claims inspect text failed: %v", err)
	}

	// 2. Inspect via Direct DB (json format)
	err = run([]string{"admin", "claims", "inspect", remRes.ClaimID, "--config", configPath, "--format", "json"})
	if err != nil {
		t.Fatalf("admin claims inspect json failed: %v", err)
	}

	// 3. Inspect non-existent claim
	err = run([]string{"admin", "claims", "inspect", "non-existent-claim-id", "--config", configPath})
	if err == nil {
		t.Fatalf("expected error for non-existent claim, got nil")
	}
}

func TestRunAdminClaimsInspectRemoteHTTP(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_http_admin.db")
	ctx := context.Background()

	database, err := db.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer database.Close()

	cfg := config.Default()
	cfg.Server.AdminKey = "adminkey12345"
	cfg.Data.Path = dbPath

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	srv := server.New(cfg, database, Version, logger)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Provision tenant and memory
	authSvc := auth.NewService(database.DB, cfg.Server.AdminKey, 300*time.Second)
	eng := engine.New(database, authSvc, cfg, Version)

	tenant, err := eng.CreateTenant(ctx, &types.CreateTenantRequest{
		Name:                 "Remote HTTP Tenant",
		AutoCreateNamespaces: true,
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating tenant: %v", err)
	}

	keyRes, err := eng.CreateAPIKey(ctx, tenant.ID, &types.CreateAPIKeyRequest{
		Label: "remote-agent",
	}, "admin", "admin")
	if err != nil {
		t.Fatalf("creating key: %v", err)
	}

	actor, _, err := authSvc.VerifyAPIKey(ctx, keyRes.Key)
	if err != nil {
		t.Fatalf("verifying key: %v", err)
	}

	remRes, err := eng.Remember(ctx, actor, tenant, &types.RememberRequest{
		Content:    "Rust is preferred for high performance systems",
		Confidence: 0.98,
		Tags:       []string{"rust", "perf"},
		Evidence: []types.EvidenceInput{
			{
				URI:          "doc://rust-lang-benchmarks",
				Description:  "Performance comparison benchmark",
				EvidenceType: "file",
			},
		},
	})
	if err != nil {
		t.Fatalf("remembering claim: %v", err)
	}

	// Inspect via HTTP using admin key
	err = run([]string{"admin", "claims", "inspect", remRes.ClaimID, "--server", ts.URL, "--admin-key", "adminkey12345", "--format", "json"})
	if err != nil {
		t.Fatalf("admin claims inspect via remote server failed: %v", err)
	}
}

