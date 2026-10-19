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
		Addr:              fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.HTTPPort),
		Handler:           s.wrapMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // Disabled at server level to accommodate SSE and streaming responses
		IdleTimeout:       120 * time.Second,
	}

	return s
}

func (s *Server) wrapMiddleware(handler http.Handler) http.Handler {
	return s.recoverMiddleware(s.loggingMiddleware(handler))
}

func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("panic recovered in HTTP handler", "error", rec, "path", r.URL.Path)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "internal server error",
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rec *statusRecorder) WriteHeader(code int) {
	rec.statusCode = code
	rec.ResponseWriter.WriteHeader(code)
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rec, r)
		duration := time.Since(start)

		s.logger.Debug("http request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.statusCode,
			"duration_ms", duration.Milliseconds(),
		)
	})
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
		"host", s.cfg.Server.Host,
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

