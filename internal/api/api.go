package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/PDK1744/gams/internal/auth"
	"github.com/PDK1744/gams/internal/engine"
	"github.com/PDK1744/gams/pkg/types"
)

const MaxRequestBodyBytes = 2 << 20 // 2 MB

type contextKey string

const (
	apiKeyCtxKey  contextKey = "gams_api_key"
	tenantCtxKey  contextKey = "gams_tenant"
	isAdminCtxKey contextKey = "gams_is_admin"
)

type Handler struct {
	eng *engine.Engine
}

func NewHandler(eng *engine.Engine) *Handler {
	return &Handler{
		eng: eng,
	}
}

// RegisterRoutes registers all agent /v1/ REST endpoints.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	agentAuth := h.authMiddleware

	mux.Handle("POST /v1/claims", agentAuth(http.HandlerFunc(h.handleRemember)))
	mux.Handle("POST /v1/recall", agentAuth(http.HandlerFunc(h.handleRecall)))
	mux.Handle("GET /v1/claims/{id}", agentAuth(http.HandlerFunc(h.handleInspect)))
	mux.Handle("POST /v1/claims/{id}/forget", agentAuth(http.HandlerFunc(h.handleForget)))
	mux.Handle("GET /v1/namespaces", agentAuth(http.HandlerFunc(h.handleListNamespaces)))
	mux.Handle("POST /v1/namespaces", agentAuth(http.HandlerFunc(h.handleCreateNamespace)))
	mux.Handle("GET /v1/namespaces/{slug}", agentAuth(http.HandlerFunc(h.handleGetNamespaceDetail)))
}

func (h *Handler) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "missing or malformed Authorization header")
			return
		}

		rawKey := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))

		// Allow server admin key to access /v1/ operations (e.g. administrative claim cleanup)
		if h.eng.Auth().VerifyAdminKey(rawKey) {
			ctx := context.WithValue(r.Context(), isAdminCtxKey, true)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		apiKey, tenant, err := h.eng.Auth().VerifyAPIKey(r.Context(), rawKey)
		if err != nil {
			if errors.Is(err, auth.ErrTenantSuspended) {
				writeError(w, http.StatusForbidden, "tenant suspended")
				return
			}
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}

		// Enforce or verify request signing if required or signature headers are provided
		var bodyBytes []byte
		if r.Body != nil {
			var err error
			bodyBytes, err = io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes))
			if err != nil {
				writeError(w, http.StatusBadRequest, "reading request body: "+err.Error())
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		timestamp := r.Header.Get(auth.HeaderTimestamp)
		signature := r.Header.Get(auth.HeaderSignature)

		if h.eng.Config().Auth.SigningRequired || signature != "" {
			reqURI := r.URL.RequestURI()
			if reqURI == "" {
				reqURI = r.URL.Path
			}
			err := auth.VerifySignature(rawKey, r.Method, reqURI, timestamp, signature, bodyBytes)
			if err != nil && r.URL.Path != reqURI {
				// Retry with URL path if RequestURI failed (e.g. client signed path without query)
				err = auth.VerifySignature(rawKey, r.Method, r.URL.Path, timestamp, signature, bodyBytes)
			}
			if err != nil {
				if errors.Is(err, auth.ErrTimestampExpired) {
					writeError(w, http.StatusUnauthorized, "request timestamp expired or invalid")
					return
				}
				writeError(w, http.StatusUnauthorized, "invalid or missing request signature")
				return
			}
		}

		ctx := context.WithValue(r.Context(), apiKeyCtxKey, apiKey)
		ctx = context.WithValue(ctx, tenantCtxKey, tenant)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return err
	}
	return nil
}

func getIdentity(ctx context.Context) (*types.APIKey, *types.Tenant, bool) {
	apiKey, _ := ctx.Value(apiKeyCtxKey).(*types.APIKey)
	tenant, _ := ctx.Value(tenantCtxKey).(*types.Tenant)
	isAdmin, _ := ctx.Value(isAdminCtxKey).(bool)
	return apiKey, tenant, isAdmin
}

func (h *Handler) handleRemember(w http.ResponseWriter, r *http.Request) {
	apiKey, tenant, isAdmin := getIdentity(r.Context())
	if isAdmin {
		writeError(w, http.StatusBadRequest, "remember requires an agent API key for tenant scoping")
		return
	}

	var req types.RememberRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}

	resp, err := h.eng.Remember(r.Context(), apiKey, tenant, &req)
	if err != nil {
		if errors.Is(err, engine.ErrConflict) && resp != nil {
			writeJSON(w, http.StatusConflict, resp)
			return
		}
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleRecall(w http.ResponseWriter, r *http.Request) {
	apiKey, tenant, isAdmin := getIdentity(r.Context())
	if isAdmin {
		writeError(w, http.StatusBadRequest, "recall requires an agent API key for tenant scoping")
		return
	}

	var req types.RecallRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}

	resp, err := h.eng.Recall(r.Context(), apiKey, tenant, &req)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleInspect(w http.ResponseWriter, r *http.Request) {
	apiKey, tenant, isAdmin := getIdentity(r.Context())
	claimID := r.PathValue("id")
	if claimID == "" {
		writeError(w, http.StatusBadRequest, "claim id is required")
		return
	}

	if isAdmin {
		// Resolve tenant from claim for administrator inspection
		var tenantID string
		err := h.eng.DB().QueryRowContext(r.Context(), "SELECT tenant_id FROM claims WHERE id = ?", claimID).Scan(&tenantID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "claim not found")
				return
			}
			handleError(w, err)
			return
		}
		tenant, err = h.eng.GetTenant(r.Context(), tenantID)
		if err != nil {
			handleError(w, err)
			return
		}
		apiKey = nil
	}

	resp, err := h.eng.Inspect(r.Context(), apiKey, tenant, claimID)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleForget(w http.ResponseWriter, r *http.Request) {
	apiKey, tenant, isAdmin := getIdentity(r.Context())
	claimID := r.PathValue("id")

	var req types.ForgetRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}

	if isAdmin {
		// Resolve tenant from claim for administrator deletion
		var tenantID string
		err := h.eng.DB().QueryRowContext(r.Context(), "SELECT tenant_id FROM claims WHERE id = ?", claimID).Scan(&tenantID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "claim not found")
				return
			}
			handleError(w, err)
			return
		}
		var err2 error
		tenant, err2 = h.eng.GetTenant(r.Context(), tenantID)
		if err2 != nil {
			handleError(w, err2)
			return
		}
	}

	resp, err := h.eng.Forget(r.Context(), apiKey, tenant, claimID, req.Reason)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleListNamespaces(w http.ResponseWriter, r *http.Request) {
	apiKey, tenant, isAdmin := getIdentity(r.Context())
	if isAdmin {
		writeError(w, http.StatusBadRequest, "listing namespaces via /v1/ requires an agent API key")
		return
	}

	namespaces, err := h.eng.ListNamespaces(r.Context(), tenant.ID, apiKey)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, namespaces)
}

func (h *Handler) handleCreateNamespace(w http.ResponseWriter, r *http.Request) {
	apiKey, tenant, isAdmin := getIdentity(r.Context())
	if isAdmin {
		writeError(w, http.StatusBadRequest, "creating namespaces via /v1/ requires an agent API key")
		return
	}

	var req types.CreateNamespaceRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}

	// Agents create tenant_wide namespaces within their tenant
	req.AccessMode = "tenant_wide"
	ns, err := h.eng.CreateNamespace(r.Context(), tenant.ID, &req, apiKey.ID, apiKey.Label)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, ns)
}

func (h *Handler) handleGetNamespaceDetail(w http.ResponseWriter, r *http.Request) {
	apiKey, tenant, isAdmin := getIdentity(r.Context())
	if isAdmin {
		writeError(w, http.StatusBadRequest, "getting namespace details via /v1/ requires an agent API key")
		return
	}
	slug := r.PathValue("slug")

	// First verify namespace read access
	ns, err := h.eng.GetNamespace(r.Context(), tenant.ID, slug)
	if err != nil {
		handleError(w, err)
		return
	}
	if err := h.eng.Auth().CheckNamespaceAccess(r.Context(), apiKey, ns, "read"); err != nil {
		writeError(w, http.StatusForbidden, "access denied to namespace")
		return
	}

	detail, err := h.eng.GetNamespaceDetail(r.Context(), tenant.ID, slug)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, detail)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func handleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrNotFound), errors.Is(err, auth.ErrNamespaceNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, engine.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, engine.ErrForbidden), errors.Is(err, auth.ErrNamespaceArchived), errors.Is(err, auth.ErrNamespaceAccessDenied):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, engine.ErrUnauthorized), errors.Is(err, auth.ErrInvalidAPIKey):
		writeError(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, engine.ErrInvalidInput), errors.Is(err, engine.ErrMaxClaimsLimit):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		slog.Error("internal server error in api handler", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}
