package admin

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/PDK1744/gams/internal/engine"
	"github.com/PDK1744/gams/pkg/types"
)

const MaxRequestBodyBytes = 2 << 20 // 2 MB

type Handler struct {
	eng     *engine.Engine
	devMode bool
}

func NewHandler(eng *engine.Engine, devMode bool) *Handler {
	return &Handler{
		eng:     eng,
		devMode: devMode,
	}
}

// RegisterRoutes registers all administrative endpoints on the provided mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	adminAuth := h.authMiddleware

	// Tenants
	mux.Handle("POST /admin/tenants", adminAuth(http.HandlerFunc(h.handleCreateTenant)))
	mux.Handle("GET /admin/tenants", adminAuth(http.HandlerFunc(h.handleListTenants)))
	mux.Handle("GET /admin/tenants/{id}", adminAuth(http.HandlerFunc(h.handleGetTenant)))
	mux.Handle("PATCH /admin/tenants/{id}", adminAuth(http.HandlerFunc(h.handleUpdateTenant)))

	// Namespaces
	mux.Handle("POST /admin/tenants/{id}/namespaces", adminAuth(http.HandlerFunc(h.handleCreateNamespace)))
	mux.Handle("GET /admin/tenants/{id}/namespaces", adminAuth(http.HandlerFunc(h.handleListNamespaces)))
	mux.Handle("GET /admin/tenants/{id}/namespaces/{slug}", adminAuth(http.HandlerFunc(h.handleGetNamespace)))
	mux.Handle("PATCH /admin/tenants/{id}/namespaces/{slug}", adminAuth(http.HandlerFunc(h.handleUpdateNamespace)))

	// Grants
	mux.Handle("POST /admin/tenants/{id}/namespaces/{slug}/grants", adminAuth(http.HandlerFunc(h.handleCreateGrant)))
	mux.Handle("DELETE /admin/tenants/{id}/namespaces/{slug}/grants/{key_id}", adminAuth(http.HandlerFunc(h.handleRevokeGrant)))

	// Keys
	mux.Handle("POST /admin/tenants/{id}/keys", adminAuth(http.HandlerFunc(h.handleCreateKey)))
	mux.Handle("GET /admin/tenants/{id}/keys", adminAuth(http.HandlerFunc(h.handleListKeys)))
	mux.Handle("DELETE /admin/tenants/{id}/keys/{key_id}", adminAuth(http.HandlerFunc(h.handleRevokeKey)))

	// Claims management for administrators
	mux.Handle("POST /admin/tenants/{id}/claims/{claim_id}/forget", adminAuth(http.HandlerFunc(h.handleForgetClaim)))

	// Audit
	mux.Handle("GET /admin/tenants/{id}/audit", adminAuth(http.HandlerFunc(h.handleTenantAudit)))
	mux.Handle("GET /admin/tenants/{id}/namespaces/{slug}/audit", adminAuth(http.HandlerFunc(h.handleNamespaceAudit)))
}

func (h *Handler) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))

		if h.eng.Auth().VerifyAdminKey(token) {
			next.ServeHTTP(w, r)
			return
		}

		// Only bypass admin authentication if NO admin key is configured AND dev mode is enabled.
		// If an admin key is configured, it must always be enforced.
		if !h.eng.Auth().AdminKeyConfigured() && h.devMode {
			slog.Warn("unauthenticated admin request permitted in development mode (server.admin_key not set)",
				"path", r.URL.Path, "method", r.Method, "remote_addr", r.RemoteAddr)
			next.ServeHTTP(w, r)
			return
		}

		writeError(w, http.StatusUnauthorized, "unauthorized: valid admin key required")
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

// Tenant handlers

func (h *Handler) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	var req types.CreateTenantRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}

	tenant, err := h.eng.CreateTenant(r.Context(), &req, "admin", "admin")
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, tenant)
}

func (h *Handler) handleListTenants(w http.ResponseWriter, r *http.Request) {
	tenants, err := h.eng.ListTenants(r.Context())
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tenants)
}

func (h *Handler) handleGetTenant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tenant, err := h.eng.GetTenant(r.Context(), id)
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tenant)
}

func (h *Handler) handleUpdateTenant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req types.UpdateTenantRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}

	tenant, err := h.eng.UpdateTenant(r.Context(), id, &req, "admin", "admin")
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tenant)
}

// Namespace handlers

func (h *Handler) handleCreateNamespace(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	var req types.CreateNamespaceRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}

	ns, err := h.eng.CreateNamespace(r.Context(), tenantID, &req, "admin", "admin")
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ns)
}

func (h *Handler) handleListNamespaces(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	namespaces, err := h.eng.ListNamespaces(r.Context(), tenantID, nil)
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, namespaces)
}

func (h *Handler) handleGetNamespace(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	slug := r.PathValue("slug")
	ns, err := h.eng.GetNamespace(r.Context(), tenantID, slug)
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ns)
}

func (h *Handler) handleUpdateNamespace(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	slug := r.PathValue("slug")
	var req types.UpdateNamespaceRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}

	ns, err := h.eng.UpdateNamespace(r.Context(), tenantID, slug, &req, "admin", "admin")
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ns)
}

// Grant handlers

func (h *Handler) handleCreateGrant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	slug := r.PathValue("slug")
	var req types.CreateGrantRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}

	grant, err := h.eng.CreateGrant(r.Context(), tenantID, slug, &req, "admin", "admin")
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, grant)
}

func (h *Handler) handleRevokeGrant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	slug := r.PathValue("slug")
	keyID := r.PathValue("key_id")

	if err := h.eng.RevokeGrant(r.Context(), tenantID, slug, keyID, "admin", "admin"); err != nil {
		handleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Key handlers

func (h *Handler) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	var req types.CreateAPIKeyRequest
	if r.Body != nil && r.ContentLength > 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			return
		}
	}

	keyResp, err := h.eng.CreateAPIKey(r.Context(), tenantID, &req, "admin", "admin")
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, keyResp)
}

func (h *Handler) handleListKeys(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	keys, err := h.eng.ListAPIKeys(r.Context(), tenantID)
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (h *Handler) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	keyID := r.PathValue("key_id")

	if err := h.eng.RevokeAPIKey(r.Context(), tenantID, keyID, "admin", "admin"); err != nil {
		handleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Claims management handler for administrators

func (h *Handler) handleForgetClaim(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	claimID := r.PathValue("claim_id")

	tenant, err := h.eng.GetTenant(r.Context(), tenantID)
	if err != nil {
		handleError(w, err)
		return
	}

	var req types.ForgetRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}

	resp, err := h.eng.Forget(r.Context(), nil, tenant, claimID, req.Reason)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// Audit handlers

func (h *Handler) handleTenantAudit(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	q := r.URL.Query()

	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	var sincePtr, untilPtr *int64
	if s := q.Get("since"); s != "" {
		if val, err := strconv.ParseInt(s, 10, 64); err == nil {
			sincePtr = &val
		}
	}
	if u := q.Get("until"); u != "" {
		if val, err := strconv.ParseInt(u, 10, 64); err == nil {
			untilPtr = &val
		}
	}

	var eventTypePtr, nsIDPtr, claimIDPtr *string
	if et := q.Get("event_type"); et != "" {
		eventTypePtr = &et
	}
	if ns := q.Get("namespace_id"); ns != "" {
		nsIDPtr = &ns
	}
	if cID := q.Get("claim_id"); cID != "" {
		claimIDPtr = &cID
	}

	events, err := h.eng.Audit().Query(r.Context(), h.eng.DB().DB, engine.AuditFilter(tenantID, nsIDPtr, claimIDPtr, eventTypePtr, sincePtr, untilPtr, limit, offset))
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (h *Handler) handleNamespaceAudit(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	slug := r.PathValue("slug")

	ns, err := h.eng.GetNamespace(r.Context(), tenantID, slug)
	if err != nil {
		handleError(w, err)
		return
	}

	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	events, err := h.eng.Audit().Query(r.Context(), h.eng.DB().DB, engine.AuditFilter(tenantID, &ns.ID, nil, nil, nil, nil, limit, offset))
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
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
	case errors.Is(err, engine.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, engine.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, engine.ErrForbidden):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, engine.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, engine.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		slog.Error("internal server error in admin handler", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}
