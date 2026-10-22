package engine_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/PDK1744/gams/internal/auth"
	"github.com/PDK1744/gams/internal/config"
	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/internal/engine"
	"github.com/PDK1744/gams/pkg/types"
)

func setupBenchEngine(b *testing.B) (*engine.Engine, *types.Tenant, *types.APIKey, func()) {
	dir := b.TempDir()
	dbPath := filepath.Join(dir, "bench.db")
	ctx := context.Background()

	database, err := db.Open(ctx, dbPath)
	if err != nil {
		b.Fatalf("failed to open bench db: %v", err)
	}

	cfg := config.Default()
	cfg.Data.Path = dbPath
	authSvc := auth.NewService(database.DB, "bench-admin", 5*time.Minute)
	eng := engine.New(database, authSvc, cfg, "bench-1.0.0")

	tenant, err := eng.CreateTenant(ctx, &types.CreateTenantRequest{
		Name:                 "Bench Tenant",
		AutoCreateNamespaces: true,
	}, "admin", "admin")
	if err != nil {
		b.Fatalf("failed to create tenant: %v", err)
	}

	keyResp, err := eng.CreateAPIKey(ctx, tenant.ID, &types.CreateAPIKeyRequest{
		Label: "bench-key",
	}, "admin", "admin")
	if err != nil {
		b.Fatalf("failed to create api key: %v", err)
	}

	actor, _, err := authSvc.VerifyAPIKey(ctx, keyResp.Key)
	if err != nil {
		b.Fatalf("failed to verify api key: %v", err)
	}

	cleanup := func() {
		_ = database.Close()
	}

	return eng, tenant, actor, cleanup
}

func BenchmarkRemember(b *testing.B) {
	eng, tenant, actor, cleanup := setupBenchEngine(b)
	defer cleanup()
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := eng.Remember(ctx, actor, tenant, &types.RememberRequest{
			Content:    fmt.Sprintf("Benchmark claim item %d for performance measurement", i),
			Confidence: 0.95,
			Namespace:  "default",
			Evidence: []types.EvidenceInput{
				{
					URI:          fmt.Sprintf("file://bench/doc_%d.go", i),
					Description:  "Benchmark test evidence source file",
					EvidenceType: "file",
				},
			},
			Tags: []string{"bench", "perf"},
		})
		if err != nil {
			b.Fatalf("Remember failed at %d: %v", i, err)
		}
	}
}

func BenchmarkRecall(b *testing.B) {
	eng, tenant, actor, cleanup := setupBenchEngine(b)
	defer cleanup()
	ctx := context.Background()

	// Seed 500 diverse claims
	for i := 0; i < 500; i++ {
		_, err := eng.Remember(ctx, actor, tenant, &types.RememberRequest{
			Content:    fmt.Sprintf("Database connection pool configuration setting %d with max connections 25 and timeout 5000ms", i),
			Confidence: 0.9,
			Namespace:  "default",
			Evidence: []types.EvidenceInput{
				{
					URI:          fmt.Sprintf("file://config/db_%d.ts", i),
					Description:  "Database config file",
					EvidenceType: "file",
				},
			},
			Tags: []string{"database", "performance"},
		})
		if err != nil {
			b.Fatalf("seeding failed at %d: %v", i, err)
		}
	}

	queries := []string{
		"database connection pool",
		"max connections timeout",
		"performance configuration",
		"database setting",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := queries[i%len(queries)]
		res, err := eng.Recall(ctx, actor, tenant, &types.RecallRequest{
			Query:     q,
			Limit:     10,
			Namespace: "default",
		})
		if err != nil {
			b.Fatalf("Recall failed: %v", err)
		}
		if len(res.Claims) == 0 {
			b.Fatalf("expected matches for query %q", q)
		}
	}
}

func BenchmarkInspect(b *testing.B) {
	eng, tenant, actor, cleanup := setupBenchEngine(b)
	defer cleanup()
	ctx := context.Background()

	remResp, err := eng.Remember(ctx, actor, tenant, &types.RememberRequest{
		Content:    "Target claim for inspect benchmarking with multiple versions and evidence items",
		Confidence: 1.0,
		Namespace:  "default",
		Evidence: []types.EvidenceInput{
			{
				URI:          "file://inspect/target.go",
				Description:  "Initial target commit",
				EvidenceType: "file",
			},
		},
		Tags: []string{"inspect", "bench"},
	})
	if err != nil {
		b.Fatalf("initial remember failed: %v", err)
	}

	// Add 3 versions
	for v := 1; v <= 3; v++ {
		baseVer := int64(v)
		_, err = eng.Remember(ctx, actor, tenant, &types.RememberRequest{
			ClaimID:     &remResp.ClaimID,
			BaseVersion: &baseVer,
			Content:     fmt.Sprintf("Updated claim version %d for inspect benchmarking", v+1),
			Confidence:  0.95,
			Namespace:   "default",
			Evidence: []types.EvidenceInput{
				{
					URI:          fmt.Sprintf("file://inspect/patch_v%d.go", v),
					Description:  "Version patch",
					EvidenceType: "file",
				},
			},
			Tags: []string{"inspect", "bench"},
		})
		if err != nil {
			b.Fatalf("version update failed: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := eng.Inspect(ctx, actor, tenant, remResp.ClaimID)
		if err != nil {
			b.Fatalf("Inspect failed: %v", err)
		}
		if len(resp.Versions) != 4 {
			b.Fatalf("expected 4 versions, got %d", len(resp.Versions))
		}
	}
}
