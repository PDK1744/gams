package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/PDK1744/gams/internal/admin"
	"github.com/PDK1744/gams/internal/api"
	"github.com/PDK1744/gams/internal/auth"
	"github.com/PDK1744/gams/internal/config"
	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/internal/engine"
	"github.com/PDK1744/gams/internal/mcp"
	"github.com/PDK1744/gams/internal/metrics"
	"github.com/PDK1744/gams/internal/ratelimit"
	httptransport "github.com/PDK1744/gams/internal/transport/http"
	stdiotransport "github.com/PDK1744/gams/internal/transport/stdio"
	"github.com/PDK1744/gams/pkg/types"
)

// Server encapsulates the HTTP server for GAMS.
type Server struct {
	cfg        *config.Config
	db         *db.DB
	version    string
	httpServer *http.Server
	logger     *slog.Logger
	engine     *engine.Engine
	sessionMgr *httptransport.SessionManager
	limiter    *ratelimit.Limiter
	devMode    bool
}

// Option configures Server instances.
type Option func(*Server)

// WithDevMode configures development mode behavior.
func WithDevMode(dev bool) Option {
	return func(s *Server) {
		s.devMode = dev
	}
}

// HealthResponse represents the payload returned by the /health endpoint.
type HealthResponse struct {
	Status   string `json:"status"`
	Version  string `json:"version"`
	Database string `json:"database"`
}

// New creates a new HTTP Server instance.
func New(cfg *config.Config, database *db.DB, version string, logger *slog.Logger, opts ...Option) *Server {
	if logger == nil {
		logger = slog.Default()
	}

	isDev := os.Getenv("GAMS_UNSAFE_DEVMODE") == "true" || os.Getenv("GAMS_DEV") == "true"

	s := &Server{
		cfg:     cfg,
		db:      database,
		version: version,
		logger:  logger,
		devMode: isDev,
	}

	for _, opt := range opts {
		opt(s)
	}

	if s.devMode && strings.TrimSpace(s.cfg.Server.AdminKey) == "" {
		s.logger.Error("SECURITY WARNING: GAMS is running in UNSAFE DEVELOPMENT MODE with an empty admin key; administrative endpoints are unauthenticated!")
	}

	// Initialize Auth, Engine, and Handlers
	keyTTL := time.Duration(cfg.Auth.KeyCacheTTLSeconds) * time.Second
	if keyTTL <= 0 {
		keyTTL = 300 * time.Second
	}
	authSvc := auth.NewService(database.DB, cfg.Server.AdminKey, keyTTL)
	eng := engine.New(database, authSvc, cfg, version)
	s.engine = eng

	adminHandler := admin.NewHandler(eng, s.devMode)
	apiHandler := api.NewHandler(eng)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	adminHandler.RegisterRoutes(mux)
	apiHandler.RegisterRoutes(mux)

	if cfg.MCP.Transport == "" || cfg.MCP.Transport == "http" || cfg.MCP.Transport == "both" {
		mcpDispatcher := mcp.NewDispatcher(eng, version)
		s.sessionMgr = httptransport.NewSessionManager(30 * time.Minute)
		mcpHandler := httptransport.NewHandler(eng, mcpDispatcher, s.sessionMgr, authSvc, s.logger)
		mcpHandler.RegisterRoutes(mux)
	}

	if cfg.Limits.RateLimitEnabled {
		s.limiter = ratelimit.NewLimiter(cfg.Limits.RateLimitRPS, cfg.Limits.RateLimitBurst)
	}

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

// Engine returns the underlying core engine instance.
func (s *Server) Engine() *engine.Engine {
	return s.engine
}

func (s *Server) wrapMiddleware(handler http.Handler) http.Handler {
	return s.recoverMiddleware(s.loggingMiddleware(s.corsMiddleware(s.rateLimitMiddleware(s.writeTimeoutMiddleware(handler)))))
}

func (s *Server) writeTimeoutMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Non-SSE handlers receive an explicit 30s write deadline to protect against slow clients (CR-007)
		if r.URL.Path != "/mcp/sse" && !strings.HasSuffix(r.URL.Path, "/export") {
			rc := http.NewResponseController(w)
			_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
			defer func() {
				_ = rc.SetWriteDeadline(time.Time{})
			}()
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.limiter == nil || !s.cfg.Limits.RateLimitEnabled {
			next.ServeHTTP(w, r)
			return
		}

		// Bypass rate limiting for internal health checks only (metrics is rate limited)
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		key := r.Header.Get("Authorization")
		if key == "" {
			key = r.Header.Get("Mcp-Session-Id")
		}
		if key == "" {
			key = r.RemoteAddr
		}

		if !s.limiter.Allow(key) {
			metrics.Default().RecordRateLimitExceeded()
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "rate_limit_exceeded",
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.cfg.Server.CORSOrigins) > 0 {
			origin := r.Header.Get("Origin")
			allowed := false
			for _, o := range s.cfg.Server.CORSOrigins {
				if o == "*" || o == origin {
					allowed = true
					break
				}
			}
			if allowed {
				if origin != "" {
					w.Header().Set("Access-Control-Allow-Origin", origin)
				} else {
					w.Header().Set("Access-Control-Allow-Origin", "*")
				}
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Mcp-Session-Id")
				w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")
				w.Header().Set("Access-Control-Max-Age", "86400")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
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

func (rec *statusRecorder) Flush() {
	if f, ok := rec.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
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

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	// Verify admin authentication if admin key is configured (query token rejected to prevent proxy log leakage - CR-004)
	if s.engine.Auth().AdminKeyConfigured() {
		token := ""
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimPrefix(authHeader, "Bearer ")
		}
		if !s.engine.Auth().VerifyAdminKey(token) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized: valid admin key required for metrics in Authorization header"})
			return
		}
	}

	out, err := metrics.Default().Render(r.Context(), s.db.DB)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "rendering metrics"})
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(out))
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
	if s.sessionMgr != nil {
		s.sessionMgr.Close()
	}
	if s.limiter != nil {
		s.limiter.Stop()
	}
	return s.httpServer.Shutdown(ctx)
}

// RunStdio executes the MCP server over standard I/O streams using the provided raw key (optional).
// If apiKey is empty, it bootstraps or binds to the local workspace tenant for zero-config operation.
func (s *Server) RunStdio(ctx context.Context, in io.Reader, out io.Writer, apiKey string) error {
	var actor *types.APIKey
	var tenant *types.Tenant

	if apiKey != "" {
		var err error
		actor, tenant, err = s.engine.Auth().VerifyAPIKey(ctx, apiKey)
		if err != nil {
			return fmt.Errorf("verifying stdio api key: %w", err)
		}
	} else {
		var err error
		actor, tenant, err = s.engine.EnsureLocalWorkspaceTenant(ctx)
		if err != nil {
			return fmt.Errorf("bootstrapping local workspace tenant for stdio: %w", err)
		}
	}

	dispatcher := mcp.NewDispatcher(s.engine, s.version)
	handler := stdiotransport.NewHandler(dispatcher, actor, tenant, s.logger)
	return handler.Run(ctx, in, out)
}

// SessionManager returns the MCP session manager if HTTP transport is enabled.
func (s *Server) SessionManager() *httptransport.SessionManager {
	return s.sessionMgr
}

// Handler returns the underlying http.Handler for testing.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}
