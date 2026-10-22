package auth

import (
	"context"
	"testing"
	"time"

	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/pkg/types"
)

func setupTestDB(t *testing.T) *db.DB {
	t.Helper()
	ctx := context.Background()
	testDB, err := db.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() {
		_ = testDB.Close()
	})
	return testDB
}

func TestVerifyAdminKey(t *testing.T) {
	svc := NewService(nil, "secret-admin-key", 1*time.Minute)

	if !svc.VerifyAdminKey("secret-admin-key") {
		t.Errorf("expected valid admin key to pass")
	}
	if svc.VerifyAdminKey("wrong-key") {
		t.Errorf("expected invalid admin key to fail")
	}
	if svc.VerifyAdminKey("") {
		t.Errorf("expected empty admin key to fail")
	}

	emptySvc := NewService(nil, "", 1*time.Minute)
	if emptySvc.VerifyAdminKey("") {
		t.Errorf("expected empty configured admin key to fail verification")
	}
}

func TestAPIKeyLifecycleAndAuth(t *testing.T) {
	testDB := setupTestDB(t)
	ctx := context.Background()
	svc := NewService(testDB.DB, "admin", 1*time.Minute)

	tenantID := types.NewUUIDv7()
	now := types.NowNano()

	// Insert tenant
	_, err := testDB.ExecContext(ctx,
		`INSERT INTO tenants (id, name, status, recall_decay_lambda, max_claims, auto_create_namespaces, created_at, metadata)
		 VALUES (?, 'Test Tenant', 'active', 0.05, 0, 0, ?, '{}')`,
		tenantID, now)
	if err != nil {
		t.Fatalf("inserting tenant: %v", err)
	}

	// Generate key
	rawKey, keyHash, err := GenerateAPIKey(tenantID)
	if err != nil {
		t.Fatalf("generating api key: %v", err)
	}

	keyID := types.NewUUIDv7()
	_, err = testDB.ExecContext(ctx,
		`INSERT INTO api_keys (id, tenant_id, key_hash, label, status, last_used_at, expires_at, created_at)
		 VALUES (?, ?, ?, 'test-key', 'active', 0, NULL, ?)`,
		keyID, tenantID, keyHash, now)
	if err != nil {
		t.Fatalf("inserting api key: %v", err)
	}

	// 1. Verify valid key
	key, tenant, err := svc.VerifyAPIKey(ctx, rawKey)
	if err != nil {
		t.Fatalf("unexpected error verifying key: %v", err)
	}
	if key.ID != keyID || tenant.ID != tenantID {
		t.Fatalf("mismatched key or tenant: got key=%s, tenant=%s", key.ID, tenant.ID)
	}

	// 2. Second verify should hit cache
	keyCached, tenantCached, err := svc.VerifyAPIKey(ctx, rawKey)
	if err != nil {
		t.Fatalf("unexpected error verifying cached key: %v", err)
	}
	if keyCached.ID != keyID || tenantCached.ID != tenantID {
		t.Fatalf("mismatched cached identity")
	}

	// 3. Invalid key checks (malformed and non-existent tenant keys)
	invalidKeys := []string{
		"random-bearer-token",
		"gams_single",
		"gams_tenantwithouttoken_",
		"gams_nonexistenttenant_0123456789abcdef0123456789abcdef",
	}
	for _, badKey := range invalidKeys {
		if _, _, err := svc.VerifyAPIKey(ctx, badKey); err == nil {
			t.Fatalf("expected error for invalid key %q", badKey)
		}
	}

	// 4. Test expired key
	expiredKeyID := types.NewUUIDv7()
	past := now - int64(10*time.Second)
	rawExpKey, expHash, _ := GenerateAPIKey(tenantID)
	_, _ = testDB.ExecContext(ctx,
		`INSERT INTO api_keys (id, tenant_id, key_hash, label, status, last_used_at, expires_at, created_at)
		 VALUES (?, ?, ?, 'expired-key', 'active', 0, ?, ?)`,
		expiredKeyID, tenantID, expHash, past, now)

	_, _, err = svc.VerifyAPIKey(ctx, rawExpKey)
	if err == nil {
		t.Fatalf("expected error for expired key")
	}

	// 5. Test suspended tenant
	_, _ = testDB.ExecContext(ctx, "UPDATE tenants SET status = 'suspended' WHERE id = ?", tenantID)
	svc.ClearCache()
	_, _, err = svc.VerifyAPIKey(ctx, rawKey)
	if err != ErrTenantSuspended {
		t.Fatalf("expected ErrTenantSuspended, got %v", err)
	}
}

func TestCheckNamespaceAccess(t *testing.T) {
	testDB := setupTestDB(t)
	ctx := context.Background()
	svc := NewService(testDB.DB, "admin", 1*time.Minute)

	tenantID := types.NewUUIDv7()
	keyID := types.NewUUIDv7()
	now := types.NowNano()

	_, err := testDB.ExecContext(ctx,
		`INSERT INTO tenants (id, name, status, recall_decay_lambda, max_claims, auto_create_namespaces, created_at, metadata)
		 VALUES (?, 'Test Tenant', 'active', 0.05, 0, 0, ?, '{}')`,
		tenantID, now)
	if err != nil {
		t.Fatalf("inserting tenant: %v", err)
	}

	_, err = testDB.ExecContext(ctx,
		`INSERT INTO api_keys (id, tenant_id, key_hash, label, status, last_used_at, expires_at, created_at)
		 VALUES (?, ?, 'dummyhash', 'test-key', 'active', 0, NULL, ?)`,
		keyID, tenantID, now)
	if err != nil {
		t.Fatalf("inserting api key: %v", err)
	}

	apiKey := &types.APIKey{ID: keyID, TenantID: tenantID}

	// 1. Tenant-wide namespace
	nsTenantWide := &types.Namespace{
		ID:         types.NewUUIDv7(),
		TenantID:   tenantID,
		Slug:       "shared",
		Status:     "active",
		AccessMode: "tenant_wide",
	}
	if err := svc.CheckNamespaceAccess(ctx, apiKey, nsTenantWide, "read"); err != nil {
		t.Errorf("tenant_wide read failed: %v", err)
	}
	if err := svc.CheckNamespaceAccess(ctx, apiKey, nsTenantWide, "write"); err != nil {
		t.Errorf("tenant_wide write failed: %v", err)
	}

	// 2. Archived namespace
	nsArchived := &types.Namespace{
		ID:         types.NewUUIDv7(),
		TenantID:   tenantID,
		Slug:       "old",
		Status:     "archived",
		AccessMode: "tenant_wide",
	}
	if err := svc.CheckNamespaceAccess(ctx, apiKey, nsArchived, "read"); err != nil {
		t.Errorf("archived read should succeed, got: %v", err)
	}
	if err := svc.CheckNamespaceAccess(ctx, apiKey, nsArchived, "write"); err != ErrNamespaceArchived {
		t.Errorf("expected ErrNamespaceArchived for write, got: %v", err)
	}

	// 3. Key-restricted namespace without grant
	nsRestricted := &types.Namespace{
		ID:         types.NewUUIDv7(),
		TenantID:   tenantID,
		Slug:       "private",
		Status:     "active",
		AccessMode: "key_restricted",
	}
	_, err = testDB.ExecContext(ctx,
		`INSERT INTO namespaces (id, tenant_id, slug, name, status, access_mode, created_at, updated_at)
		 VALUES (?, ?, ?, 'Private', 'active', 'key_restricted', ?, ?)`,
		nsRestricted.ID, tenantID, nsRestricted.Slug, now, now)
	if err != nil {
		t.Fatalf("inserting restricted namespace: %v", err)
	}
	if err := svc.CheckNamespaceAccess(ctx, apiKey, nsRestricted, "read"); err != ErrNamespaceAccessDenied {
		t.Errorf("expected ErrNamespaceAccessDenied without grant, got: %v", err)
	}

	// 4. Grant write permission
	grantID := types.NewUUIDv7()
	_, err = testDB.ExecContext(ctx,
		`INSERT INTO namespace_grants (id, namespace_id, api_key_id, permission, granted_at, granted_by)
		 VALUES (?, ?, ?, 'write', ?, 'admin')`,
		grantID, nsRestricted.ID, apiKey.ID, types.NowNano())
	if err != nil {
		t.Fatalf("inserting grant: %v", err)
	}

	// Write grant should satisfy read and write
	if err := svc.CheckNamespaceAccess(ctx, apiKey, nsRestricted, "read"); err != nil {
		t.Errorf("read should be allowed with write grant: %v", err)
	}
	if err := svc.CheckNamespaceAccess(ctx, apiKey, nsRestricted, "write"); err != nil {
		t.Errorf("write should be allowed with write grant: %v", err)
	}
	// But not admin
	if err := svc.CheckNamespaceAccess(ctx, apiKey, nsRestricted, "admin"); err != ErrNamespaceAccessDenied {
		t.Errorf("expected ErrNamespaceAccessDenied for admin with write grant, got: %v", err)
	}
}

func TestAPIKeyExpiration(t *testing.T) {
	testDB := setupTestDB(t)
	ctx := context.Background()
	svc := NewService(testDB.DB, "admin", 1*time.Minute)

	tenantID := types.NewUUIDv7()
	now := types.NowNano()

	// Insert tenant
	_, err := testDB.ExecContext(ctx,
		`INSERT INTO tenants (id, name, status, recall_decay_lambda, max_claims, auto_create_namespaces, created_at, metadata)
		 VALUES (?, 'Expiry Tenant', 'active', 0.05, 0, 0, ?, '{}')`,
		tenantID, now)
	if err != nil {
		t.Fatalf("inserting tenant: %v", err)
	}

	// 1. Expired Key (expired 1 hour ago)
	expiredRawKey, expiredHash, err := GenerateAPIKey(tenantID)
	if err != nil {
		t.Fatalf("generating expired key: %v", err)
	}
	expiredAt := now - int64(1*time.Hour)
	_, err = testDB.ExecContext(ctx,
		`INSERT INTO api_keys (id, tenant_id, key_hash, label, status, last_used_at, expires_at, created_at)
		 VALUES (?, ?, ?, 'expired-key', 'active', 0, ?, ?)`,
		types.NewUUIDv7(), tenantID, expiredHash, expiredAt, now)
	if err != nil {
		t.Fatalf("inserting expired key: %v", err)
	}

	_, _, err = svc.VerifyAPIKey(ctx, expiredRawKey)
	if err != ErrInvalidAPIKey {
		t.Fatalf("expected ErrInvalidAPIKey for expired key, got: %v", err)
	}

	// 2. Future Valid Key (expires in 10 minutes)
	futureRawKey, futureHash, err := GenerateAPIKey(tenantID)
	if err != nil {
		t.Fatalf("generating future key: %v", err)
	}
	futureExpiry := now + int64(10*time.Minute)
	futureKeyID := types.NewUUIDv7()
	_, err = testDB.ExecContext(ctx,
		`INSERT INTO api_keys (id, tenant_id, key_hash, label, status, last_used_at, expires_at, created_at)
		 VALUES (?, ?, ?, 'future-key', 'active', 0, ?, ?)`,
		futureKeyID, tenantID, futureHash, futureExpiry, now)
	if err != nil {
		t.Fatalf("inserting future key: %v", err)
	}

	key, _, err := svc.VerifyAPIKey(ctx, futureRawKey)
	if err != nil {
		t.Fatalf("expected valid future key to succeed, got: %v", err)
	}
	if key.ID != futureKeyID {
		t.Fatalf("expected key id %s, got %s", futureKeyID, key.ID)
	}

	// 3. Cached Key Expiration Eviction
	transientRawKey, transientHash, err := GenerateAPIKey(tenantID)
	if err != nil {
		t.Fatalf("generating transient key: %v", err)
	}
	transientExpiry := now + int64(10*time.Minute)
	transientKeyID := types.NewUUIDv7()
	_, err = testDB.ExecContext(ctx,
		`INSERT INTO api_keys (id, tenant_id, key_hash, label, status, last_used_at, expires_at, created_at)
		 VALUES (?, ?, ?, 'transient-key', 'active', 0, ?, ?)`,
		transientKeyID, tenantID, transientHash, transientExpiry, now)
	if err != nil {
		t.Fatalf("inserting transient key: %v", err)
	}

	// First verify succeeds and populates cache
	key, _, err = svc.VerifyAPIKey(ctx, transientRawKey)
	if err != nil {
		t.Fatalf("expected initial verify to succeed, got: %v", err)
	}
	if key.ID != transientKeyID {
		t.Fatalf("expected key id %s, got %s", transientKeyID, key.ID)
	}

	// Simulate expiration on the cached key without relying on race-sensitive wall-clock sleeps
	expiredNano := types.NowNano() - int64(1*time.Second)
	key.ExpiresAt = &expiredNano

	// Second verify hits cache, detects expiration, evicts from cache, and returns ErrInvalidAPIKey
	_, _, err = svc.VerifyAPIKey(ctx, transientRawKey)
	if err != ErrInvalidAPIKey {
		t.Fatalf("expected expired cached key to return ErrInvalidAPIKey, got: %v", err)
	}
}

func TestInvalidateTenantCache(t *testing.T) {
	testDB := setupTestDB(t)
	ctx := context.Background()
	svc := NewService(testDB.DB, "admin", 1*time.Minute)

	tA := types.NewUUIDv7()
	tB := types.NewUUIDv7()
	now := types.NowNano()

	_, _ = testDB.ExecContext(ctx, `INSERT INTO tenants (id, name, status, recall_decay_lambda, max_claims, auto_create_namespaces, created_at, metadata) VALUES (?, 'Tenant A', 'active', 0.05, 0, 0, ?, '{}')`, tA, now)
	_, _ = testDB.ExecContext(ctx, `INSERT INTO tenants (id, name, status, recall_decay_lambda, max_claims, auto_create_namespaces, created_at, metadata) VALUES (?, 'Tenant B', 'active', 0.05, 0, 0, ?, '{}')`, tB, now)

	keyA, hashA, err := GenerateAPIKey(tA)
	if err != nil {
		t.Fatalf("GenerateAPIKey A: %v", err)
	}
	keyB, hashB, err := GenerateAPIKey(tB)
	if err != nil {
		t.Fatalf("GenerateAPIKey B: %v", err)
	}

	_, _ = testDB.ExecContext(ctx, `INSERT INTO api_keys (id, tenant_id, key_hash, label, status, last_used_at, expires_at, created_at) VALUES ('kA', ?, ?, 'kA', 'active', 0, NULL, ?)`, tA, hashA, now)
	_, _ = testDB.ExecContext(ctx, `INSERT INTO api_keys (id, tenant_id, key_hash, label, status, last_used_at, expires_at, created_at) VALUES ('kB', ?, ?, 'kB', 'active', 0, NULL, ?)`, tB, hashB, now)

	// Populate cache for both
	_, _, err = svc.VerifyAPIKey(ctx, keyA)
	if err != nil {
		t.Fatalf("verify A failed: %v", err)
	}
	_, _, err = svc.VerifyAPIKey(ctx, keyB)
	if err != nil {
		t.Fatalf("verify B failed: %v", err)
	}

	// Invalidate tenant A only
	svc.InvalidateTenantCache(tA)

	// Tenant A key hash removed in DB to prove cache miss requires re-querying DB
	_, _ = testDB.ExecContext(ctx, `UPDATE api_keys SET status = 'revoked' WHERE id = 'kA'`)
	_, _, err = svc.VerifyAPIKey(ctx, keyA)
	if err != ErrInvalidAPIKey {
		t.Fatalf("expected tenant A to miss cache and see revoked status, got %v", err)
	}

	// Tenant B should still be in cache (even if we temporarily break DB connection, or change status)
	// Changing DB status to revoked without clearing cache would still hit cache:
	_, _ = testDB.ExecContext(ctx, `UPDATE api_keys SET status = 'revoked' WHERE id = 'kB'`)
	kRecB, _, err := svc.VerifyAPIKey(ctx, keyB)
	if err != nil {
		t.Fatalf("expected tenant B to hit cache, got %v", err)
	}
	if kRecB.ID != "kB" {
		t.Fatalf("expected cached key B")
	}
}
