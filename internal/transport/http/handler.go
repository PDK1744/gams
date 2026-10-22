package httptransport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/PDK1744/gams/internal/auth"
	"github.com/PDK1744/gams/internal/engine"
	"github.com/PDK1744/gams/internal/mcp"
)

// Handler implements the Streamable HTTP transport for MCP (SSE + POST).
type Handler struct {
	engine     *engine.Engine
	dispatcher *mcp.Dispatcher
	sessionMgr *SessionManager
	authSvc    *auth.Service
	logger     *slog.Logger
}

// NewHandler initializes a new MCP HTTP transport handler.
func NewHandler(eng *engine.Engine, dispatcher *mcp.Dispatcher, sessionMgr *SessionManager, authSvc *auth.Service, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		engine:     eng,
		dispatcher: dispatcher,
		sessionMgr: sessionMgr,
		authSvc:    authSvc,
		logger:     logger,
	}
}

// RegisterRoutes mounts the MCP transport endpoints on the provided ServeMux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /mcp/sse", h.HandleSSE)
	mux.HandleFunc("POST /mcp/message", h.HandleMessage)
}

// HandleSSE establishes a persistent Server-Sent Events stream for an MCP client session.
func (h *Handler) HandleSSE(w http.ResponseWriter, r *http.Request) {
	// 1. Authenticate via Bearer header exclusively (query parameter auth rejected to prevent proxy log leakage - CR-003)
	rawKey := ""
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		rawKey = strings.TrimPrefix(authHeader, "Bearer ")
	}

	if rawKey == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized: Bearer API key required in Authorization header"})
		return
	}

	actor, tenant, err := h.authSvc.VerifyAPIKey(r.Context(), rawKey)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid api key"})
		return
	}

	if tenant.Status != "active" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "tenant suspended"})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "streaming unsupported"})
		return
	}

	// 2. Allocate and register session
	session := h.sessionMgr.CreateSession(actor, tenant)
	defer h.sessionMgr.Remove(session.ID)

	// 3. Set SSE response headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// 4. Send initial endpoint event directing client where to POST JSON-RPC messages
	endpointURI := fmt.Sprintf("/mcp/message?session_id=%s", session.ID)
	if _, err := fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpointURI); err != nil {
		return
	}
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	// 5. Stream loop
	for {
		select {
		case <-r.Context().Done():
			return
		case <-session.Done():
			return
		case msg, ok := <-session.Outbound():
			if !ok {
				return
			}
			lines := strings.Split(string(msg), "\n")
			_, _ = fmt.Fprintf(w, "event: message\n")
			for _, l := range lines {
				_, _ = fmt.Fprintf(w, "data: %s\n", l)
			}
			_, _ = fmt.Fprintf(w, "\n")
			flusher.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// HandleMessage accepts JSON-RPC 2.0 messages from MCP clients and queues responses for SSE.
func (h *Handler) HandleMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		sessionID = r.URL.Query().Get("session_id")
	}
	if sessionID == "" {
		sessionID = r.URL.Query().Get("sessionId")
	}

	if sessionID == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "missing Mcp-Session-Id header, session_id, or sessionId query parameter",
		})
		return
	}

	session, ok := h.sessionMgr.Get(sessionID)
	if !ok {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "session not found or expired",
		})
		return
	}
	session.Touch()

	// If Authorization header is provided, verify it matches the bound session identity
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		rawKey := strings.TrimPrefix(authHeader, "Bearer ")
		actor, _, err := h.authSvc.VerifyAPIKey(r.Context(), rawKey)
		if err != nil || actor.ID != session.Actor.ID {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "authorization header does not match bound session actor",
			})
			return
		}
	}

	// Enforce 2MB request body cap
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "failed to read request body",
		})
		return
	}

	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) == 0 {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "empty request body",
		})
		return
	}

	// Parse JSON-RPC 2.0 (single or batch)
	if trimmed[0] == '[' {
		var reqs []mcp.Request
		if err := json.Unmarshal([]byte(trimmed), &reqs); err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"error": map[string]any{
					"code":    mcp.CodeParseError,
					"message": "parse error: invalid JSON array",
				},
			})
			return
		}

		if len(reqs) == 0 {
			errResp := &mcp.Response{
				JSONRPC: "2.0",
				Error: &mcp.RPCError{
					Code:    mcp.CodeInvalidRequest,
					Message: "invalid request: empty batch array",
				},
			}
			if b, err := json.Marshal(errResp); err == nil {
				if err := session.Send(b); err != nil {
					h.logger.Warn("failed to send mcp error response", "session_id", session.ID, "error", err)
					if errors.Is(err, ErrBufferFull) {
						session.Close()
					}
				}
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}

		var responses []*mcp.Response
		for i := range reqs {
			resp := h.dispatcher.Dispatch(session.Context(), session.Actor, session.Tenant, &reqs[i])
			if resp != nil {
				responses = append(responses, resp)
			}
		}

		if len(responses) > 0 {
			respBytes, err := json.Marshal(responses)
			if err == nil {
				if err := session.Send(respBytes); err != nil {
					h.logger.Warn("failed to send mcp batch response to session", "session_id", session.ID, "error", err)
					if errors.Is(err, ErrBufferFull) {
						session.Close()
					}
				}
			}
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	var req mcp.Request
	if err := json.Unmarshal([]byte(trimmed), &req); err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"error": map[string]any{
				"code":    mcp.CodeParseError,
				"message": "parse error: invalid JSON",
			},
		})
		return
	}

	resp := h.dispatcher.Dispatch(session.Context(), session.Actor, session.Tenant, &req)
	if resp != nil {
		respBytes, err := json.Marshal(resp)
		if err == nil {
			if err := session.Send(respBytes); err != nil {
				h.logger.Warn("failed to send mcp response to session", "session_id", session.ID, "error", err)
				if errors.Is(err, ErrBufferFull) {
					session.Close()
				}
			}
		}
	}

	w.WriteHeader(http.StatusAccepted)
}
