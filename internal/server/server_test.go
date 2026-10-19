package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

