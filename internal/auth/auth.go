package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/PDK1744/gams/internal/metrics"
	"github.com/PDK1744/gams/pkg/types"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidAPIKey         = errors.New("invalid or expired API key")
	ErrTenantSuspended       = errors.New("tenant is suspended")
	ErrNamespaceArchived     = errors.New("namespace is archived")
	ErrNamespaceAccessDenied = errors.New("access denied to namespace")
	ErrNamespaceNotFound     = errors.New("namespace not found")
)

type CachedIdentity struct {
	APIKey         *types.APIKey
	Tenant         *types.Tenant
	lastRecordedAt atomic.Int64
}

// Service provides API key verification, admin key checks, and namespace access control.
type Service struct {
	db       *sql.DB
	adminKey string
	cache    *LRUCache[string, *CachedIdentity]
}

// NewService creates an authentication service with LRU caching.
func NewService(db *sql.DB, adminKey string, cacheTTL time.Duration) *Service {
	return &Service{
		db:       db,
		adminKey: adminKey,
		cache:    NewLRUCache[string, *CachedIdentity](1000, cacheTTL),
	}
}

// AdminKeyConfigured returns true if an admin key has been explicitly configured.
func (s *Service) AdminKeyConfigured() bool {
	return strings.TrimSpace(s.adminKey) != ""
}

// VerifyAdminKey verifies if the provided key matches the configured admin key.
func (s *Service) VerifyAdminKey(rawKey string) bool {
	if s.adminKey == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(s.adminKey), []byte(rawKey)) == 1
}

// GenerateAPIKey generates a new structured API key: gams_<tenant_id>_<hex(32)> and its bcrypt hash.
func GenerateAPIKey(tenantID string) (string, string, error) {
	randBytes := make([]byte, 32)
	if _, err := rand.Read(randBytes); err != nil {
		return "", "", fmt.Errorf("generating random key bytes: %w", err)
	}
	rawKey := fmt.Sprintf("gams_%s_%s", tenantID, hex.EncodeToString(randBytes))
	sha := sha256.Sum256([]byte(rawKey))
	hashBytes, err := bcrypt.GenerateFromPassword(sha[:], bcrypt.DefaultCost)
	if err != nil {
		return "", "", fmt.Errorf("hashing API key: %w", err)
	}
	return rawKey, string(hashBytes), nil
}

// InvalidateKeyCache removes an API key from cache.
func (s *Service) InvalidateKeyCache(rawKey string) {
	h := sha256.Sum256([]byte(rawKey))
	cacheKey := hex.EncodeToString(h[:])
	s.cache.Delete(cacheKey)
}

// InvalidateTenantCache removes all cached identities belonging to the specified tenant.
func (s *Service) InvalidateTenantCache(tenantID string) {
	s.cache.DeleteWhere(func(key string, identity *CachedIdentity) bool {
		return identity != nil && identity.Tenant != nil && identity.Tenant.ID == tenantID
	})
}

// ClearCache clears all cached keys.
func (s *Service) ClearCache() {
	s.cache.Clear()
}

// VerifyAPIKey verifies a bearer API key, checking expiration and tenant active status.
func (s *Service) VerifyAPIKey(ctx context.Context, rawKey string) (*types.APIKey, *types.Tenant, error) {
	rawKey = strings.TrimSpace(rawKey)
	if rawKey == "" {
		return nil, nil, ErrInvalidAPIKey
	}

	h := sha256.Sum256([]byte(rawKey))
	cacheKey := hex.EncodeToString(h[:])

	if cached, ok := s.cache.Get(cacheKey); ok {
		metrics.Default().RecordAuthCacheHit()
		// Verify tenant and key status from cached identity
		if cached.APIKey.Status != "active" {
			s.cache.Delete(cacheKey)
			return nil, nil, ErrInvalidAPIKey
		}
		if cached.APIKey.ExpiresAt != nil && *cached.APIKey.ExpiresAt > 0 && *cached.APIKey.ExpiresAt < types.NowNano() {
			s.cache.Delete(cacheKey)
			return nil, nil, ErrInvalidAPIKey
		}
		if cached.Tenant.Status != "active" {
			return nil, nil, ErrTenantSuspended
		}
		// Debounced last_used_at update
		s.maybeRecordKeyUsage(cached)
		return cached.APIKey, cached.Tenant, nil
	}

	metrics.Default().RecordAuthCacheMiss()

	// Reject immediately if the key does not follow the structured gams_<tenant_id>_<token> format.
	// This prevents unauthenticated clients from triggering cross-tenant database scans and CPU-heavy bcrypt comparisons.
	if !strings.HasPrefix(rawKey, "gams_") {
		return nil, nil, ErrInvalidAPIKey
	}
	trimmed := strings.TrimPrefix(rawKey, "gams_")
	lastUnderscore := strings.LastIndex(trimmed, "_")
	if lastUnderscore <= 0 || lastUnderscore == len(trimmed)-1 {
		return nil, nil, ErrInvalidAPIKey
	}
	tenantID := trimmed[:lastUnderscore]
	token := trimmed[lastUnderscore+1:]
	if len(token) < 16 {
		return nil, nil, ErrInvalidAPIKey
	}

	query := `SELECT id, tenant_id, key_hash, label, status, last_used_at, expires_at, created_at
	          FROM api_keys WHERE tenant_id = ? AND status = 'active'`
	rows, err := s.db.QueryContext(ctx, query, tenantID)
	if err != nil {
		return nil, nil, fmt.Errorf("querying api keys: %w", err)
	}
	defer rows.Close()

	var candidates []types.APIKey
	for rows.Next() {
		var k types.APIKey
		var keyHash string
		var expiresAt sql.NullInt64
		if err := rows.Scan(&k.ID, &k.TenantID, &keyHash, &k.Label, &k.Status, &k.LastUsedAt, &expiresAt, &k.CreatedAt); err != nil {
			return nil, nil, fmt.Errorf("scanning api key: %w", err)
		}
		if expiresAt.Valid {
			k.ExpiresAt = &expiresAt.Int64
		}
		k.KeyHash = keyHash
		candidates = append(candidates, k)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterating api keys: %w", err)
	}
	_ = rows.Close()

	var matchedKey *types.APIKey
	sha := sha256.Sum256([]byte(rawKey))
	for i := range candidates {
		k := &candidates[i]
		if err := bcrypt.CompareHashAndPassword([]byte(k.KeyHash), sha[:]); err == nil {
			matchedKey = k
			break
		}
	}

	if matchedKey == nil {
		return nil, nil, ErrInvalidAPIKey
	}

	// Check expiration
	if matchedKey.ExpiresAt != nil && *matchedKey.ExpiresAt > 0 && *matchedKey.ExpiresAt < types.NowNano() {
		return nil, nil, ErrInvalidAPIKey
	}

	// Fetch tenant
	tenant, err := s.getTenant(ctx, matchedKey.TenantID)
	if err != nil {
		return nil, nil, err
	}
	if tenant.Status != "active" {
		return nil, nil, ErrTenantSuspended
	}

	// Cache verified identity
	cachedIdentity := &CachedIdentity{
		APIKey: matchedKey,
		Tenant: tenant,
	}
	cachedIdentity.lastRecordedAt.Store(types.NowNano())
	s.cache.Set(cacheKey, cachedIdentity)

	s.maybeRecordKeyUsage(cachedIdentity)

	return matchedKey, tenant, nil
}

// maybeRecordKeyUsage throttles/debounces updates to last_used_at in the database (at most once every 60s per key).
func (s *Service) maybeRecordKeyUsage(identity *CachedIdentity) {
	now := types.NowNano()
	last := identity.lastRecordedAt.Load()
	if now-last < int64(60*time.Second) {
		return
	}
	if !identity.lastRecordedAt.CompareAndSwap(last, now) {
		return
	}
	go func(keyID string, recordedAt int64) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = s.db.ExecContext(ctx, "UPDATE api_keys SET last_used_at = ? WHERE id = ?", recordedAt, keyID)
	}(identity.APIKey.ID, now)
}

func (s *Service) getTenant(ctx context.Context, tenantID string) (*types.Tenant, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, status, recall_decay_lambda, max_claims, auto_create_namespaces, created_at, metadata
		 FROM tenants WHERE id = ?`, tenantID)

	var t types.Tenant
	var metaJSON string
	if err := row.Scan(&t.ID, &t.Name, &t.Status, &t.RecallDecayLambda, &t.MaxClaims, &t.AutoCreateNamespaces, &t.CreatedAt, &metaJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("tenant %q not found: %w", tenantID, ErrInvalidAPIKey)
		}
		return nil, fmt.Errorf("querying tenant %q: %w", tenantID, err)
	}
	if metaJSON != "" && metaJSON != "{}" {
		_ = json.Unmarshal([]byte(metaJSON), &t.Metadata)
	}
	return &t, nil
}

// CheckNamespaceAccess verifies permissions for a given namespace and API key.
// permission may be "read", "write", or "admin".
func (s *Service) CheckNamespaceAccess(ctx context.Context, apiKey *types.APIKey, ns *types.Namespace, requiredPerm string) error {
	if ns.Status == "archived" && (requiredPerm == "write" || requiredPerm == "admin") {
		return ErrNamespaceArchived
	}

	if ns.AccessMode == "tenant_wide" {
		if requiredPerm == "admin" {
			if apiKey == nil {
				return ErrNamespaceAccessDenied
			}
			var perm string
			err := s.db.QueryRowContext(ctx,
				`SELECT permission FROM namespace_grants WHERE namespace_id = ? AND api_key_id = ?`,
				ns.ID, apiKey.ID).Scan(&perm)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrNamespaceAccessDenied
				}
				return fmt.Errorf("querying namespace grant: %w", err)
			}
			if perm != "admin" {
				return ErrNamespaceAccessDenied
			}
		}
		return nil
	}

	if ns.AccessMode == "key_restricted" {
		if apiKey == nil {
			return ErrNamespaceAccessDenied
		}
		var perm string
		err := s.db.QueryRowContext(ctx,
			`SELECT permission FROM namespace_grants WHERE namespace_id = ? AND api_key_id = ?`,
			ns.ID, apiKey.ID).Scan(&perm)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNamespaceAccessDenied
			}
			return fmt.Errorf("querying namespace grant: %w", err)
		}

		if permSatisfies(perm, requiredPerm) {
			return nil
		}
		return ErrNamespaceAccessDenied
	}

	return nil
}

func permSatisfies(granted, required string) bool {
	switch granted {
	case "admin":
		return true
	case "write":
		return required == "read" || required == "write"
	case "read":
		return required == "read"
	default:
		return false
	}
}
