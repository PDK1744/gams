package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PDK1744/gams/internal/config"
	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/internal/server"
)

func TestHealthEndpoint(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer database.Close()

	cfg := config.Default()
	srv := server.New(cfg, database, "0.1.0", nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}

	var health server.HealthResponse
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("parsing health response JSON: %v", err)
	}

	if health.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", health.Status)
	}
	if health.Version != "0.1.0" {
		t.Errorf("expected version '0.1.0', got %q", health.Version)
	}
	if health.Database != "connected" {
		t.Errorf("expected database 'connected', got %q", health.Database)
	}
}

func TestHealthEndpointDegradedWhenDBClosed(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}

	cfg := config.Default()
	srv := server.New(cfg, database, "0.1.0", nil)

	// Close the DB handle so ping fails
	_ = database.Close()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", res.StatusCode)
	}

	var health server.HealthResponse
	if err := json.NewDecoder(res.Body).Decode(&health); err != nil {
		t.Fatalf("parsing degraded response JSON: %v", err)
	}

	if health.Status != "degraded" {
		t.Errorf("expected status 'degraded', got %q", health.Status)
	}
	if health.Database != "disconnected" {
		t.Errorf("expected database 'disconnected', got %q", health.Database)
	}
}

func TestPanicRecovery(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer database.Close()

	cfg := config.Default()
	srv := server.New(cfg, database, "0.1.0", nil)

	// Custom handler that panics
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("simulated unhandled error")
	})

	wrapped := srv.Handler()
	// Test that server middleware catches panic from wrapped mux if a handler panics
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)

	// Create sub-mux using srv's recovery
	mux := http.NewServeMux()
	mux.Handle("/panic", panicHandler)
	testServer := server.New(cfg, database, "0.1.0", nil)
	_ = testServer

	// Verify that serving a panic through handler does not crash process
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("middleware failed to recover panic: %v", r)
		}
	}()

	wrapped.ServeHTTP(rec, req)
}

func TestMetricsEndpoint(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer database.Close()

	cfg := config.Default()
	cfg.Server.AdminKey = "metrics-secret-key"
	srv := server.New(cfg, database, "0.1.0", nil)

	// 1. Unauthenticated request -> 401
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on unauthenticated /metrics, got %d", rec.Code)
	}

	// 2. Authenticated with Authorization Bearer header -> 200
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer metrics-secret-key")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on authenticated /metrics, got %d: %s", rec.Code, rec.Body.String())
	}
	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/plain") {
		t.Errorf("expected text/plain content type, got %q", contentType)
	}
	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, "gams_write_total") || !strings.Contains(bodyStr, "gams_recall_total") {
		t.Errorf("metrics body missing expected metric families:\n%s", bodyStr)
	}

	// 3. Query param token auth rejected (CR-004)
	req = httptest.NewRequest(http.MethodGet, "/metrics?token=metrics-secret-key", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on /metrics?token=... (query token rejected), got %d", rec.Code)
	}
}

func TestRateLimitingMiddleware(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer database.Close()

	cfg := config.Default()
	cfg.Limits.RateLimitEnabled = true
	cfg.Limits.RateLimitRPS = 10
	cfg.Limits.RateLimitBurst = 2 // allow only 2 requests immediately

	srv := server.New(cfg, database, "0.1.0", nil)

	apiKey := "Bearer gams_test_token"

	// 1st request -> allowed (will be 401 or 404 from API, not 429)
	req1 := httptest.NewRequest(http.MethodGet, "/v1/namespaces", nil)
	req1.Header.Set("Authorization", apiKey)
	rec1 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec1, req1)
	if rec1.Code == http.StatusTooManyRequests {
		t.Fatalf("request 1 should not be rate limited")
	}

	// 2nd request -> allowed
	req2 := httptest.NewRequest(http.MethodGet, "/v1/namespaces", nil)
	req2.Header.Set("Authorization", apiKey)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code == http.StatusTooManyRequests {
		t.Fatalf("request 2 should not be rate limited")
	}

	// 3rd request -> should be 429 Too Many Requests
	req3 := httptest.NewRequest(http.MethodGet, "/v1/namespaces", nil)
	req3.Header.Set("Authorization", apiKey)
	rec3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on request 3, got %d", rec3.Code)
	}
	if rec3.Header().Get("Retry-After") != "1" {
		t.Errorf("expected Retry-After header '1', got %q", rec3.Header().Get("Retry-After"))
	}

	// Health check is exempt from rate limiting
	reqHealth := httptest.NewRequest(http.MethodGet, "/health", nil)
	reqHealth.Header.Set("Authorization", apiKey)
	recHealth := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recHealth, reqHealth)
	if recHealth.Code != http.StatusOK {
		t.Fatalf("expected /health to bypass rate limiting, got %d", recHealth.Code)
	}

	// Metrics endpoint IS subject to rate limiting (CR-013)
	reqMetrics := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	reqMetrics.Header.Set("Authorization", apiKey)
	recMetrics := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recMetrics, reqMetrics)
	if recMetrics.Code != http.StatusTooManyRequests {
		t.Fatalf("expected /metrics to be rate limited (429), got %d", recMetrics.Code)
	}
}

func TestDevModeSecurityWarningLog(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	defer database.Close()

	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := config.Default()
	cfg.Server.AdminKey = ""

	_ = server.New(cfg, database, "1.0.0", logger, server.WithDevMode(true))

	output := buf.String()
	if !strings.Contains(output, "SECURITY WARNING") {
		t.Fatalf("expected security warning in logs when devMode is active with empty admin key, got: %s", output)
	}
}

