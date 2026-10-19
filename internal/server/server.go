package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/PDK1744/gams/internal/config"
	"github.com/PDK1744/gams/internal/db"
)

// Server encapsulates the HTTP server for GAMS.
type Server struct {
	cfg        *config.Config
	db         *db.DB
	version    string
	httpServer *http.Server
	logger     *slog.Logger
}

// HealthResponse represents the payload returned by the /health endpoint.
type HealthResponse struct {
	Status   string `json:"status"`
	Version  string `json:"version"`
	Database string `json:"database"`
}

// New creates a new HTTP Server instance.
func New(cfg *config.Config, database *db.DB, version string, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}

	s := &Server{
		cfg:     cfg,
		db:      database,
		version: version,
		logger:  logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.HTTPPort),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return s
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	dbStatus := "connected"
	httpStatus := http.StatusOK

	if err := s.db.PingContext(ctx); err != nil {
		s.logger.Error("database ping failed during health check", "error", err)
		dbStatus = "disconnected"
		httpStatus = http.StatusServiceUnavailable
	}

	resp := HealthResponse{
		Status:   "ok",
		Version:  s.version,
		Database: dbStatus,
	}
	if httpStatus != http.StatusOK {
		resp.Status = "degraded"
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(resp)
}

// Start begins listening on the configured HTTP port.
func (s *Server) Start() error {
	s.logger.Info("starting HTTP server",
		"port", s.cfg.Server.HTTPPort,
		"version", s.version,
		"db", s.cfg.Data.Path,
	)
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server failure: %w", err)
	}
	return nil
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down HTTP server")
	return s.httpServer.Shutdown(ctx)
}

// Handler returns the underlying http.Handler for testing.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}
