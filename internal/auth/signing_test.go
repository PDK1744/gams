package auth_test

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/PDK1744/gams/internal/auth"
)

func TestSigningLifecycle(t *testing.T) {
	secretKey := "gams_tenant-123_abcdef0123456789abcdef0123456789"
	method := "POST"
	path := "/v1/claims"
	body := []byte(`{"content":"test fact","confidence":0.9}`)

	nowUnix := time.Now().Unix()
	timestampStr := strconv.FormatInt(nowUnix, 10)

	sig := auth.ComputeSignature(secretKey, method, path, timestampStr, body)
	if len(sig) != 64 {
		t.Fatalf("expected 64-char hex signature, got %q", sig)
	}

	// Successful verification
	if err := auth.VerifySignature(secretKey, method, path, timestampStr, sig, body); err != nil {
		t.Fatalf("expected signature to verify, got: %v", err)
	}

	// Verification with RFC3339 format
	rfc3339Str := time.Now().Format(time.RFC3339)
	sigRFC := auth.ComputeSignature(secretKey, method, path, rfc3339Str, body)
	if err := auth.VerifySignature(secretKey, method, path, rfc3339Str, sigRFC, body); err != nil {
		t.Fatalf("expected RFC3339 signature to verify, got: %v", err)
	}

	// Tampered body
	tamperedBody := []byte(`{"content":"test fact tampered","confidence":0.9}`)
	if err := auth.VerifySignature(secretKey, method, path, timestampStr, sig, tamperedBody); err == nil {
		t.Fatalf("expected signature verification to fail on tampered body")
	}

	// Tampered path
	if err := auth.VerifySignature(secretKey, method, "/v1/recall", timestampStr, sig, body); err == nil {
		t.Fatalf("expected signature verification to fail on tampered path")
	}

	// Tampered secret
	if err := auth.VerifySignature("different-secret", method, path, timestampStr, sig, body); err == nil {
		t.Fatalf("expected signature verification to fail on wrong secret key")
	}

	// Expired timestamp (> 5 minutes ago)
	expiredUnix := time.Now().Add(-6 * time.Minute).Unix()
	expiredStr := fmt.Sprintf("%d", expiredUnix)
	expiredSig := auth.ComputeSignature(secretKey, method, path, expiredStr, body)
	if err := auth.VerifySignature(secretKey, method, path, expiredStr, expiredSig, body); err == nil {
		t.Fatalf("expected expired timestamp to fail verification")
	}

	// Missing signature or timestamp
	if err := auth.VerifySignature(secretKey, method, path, "", sig, body); err == nil {
		t.Fatalf("expected error on empty timestamp")
	}
	if err := auth.VerifySignature(secretKey, method, path, timestampStr, "", body); err == nil {
		t.Fatalf("expected error on empty signature")
	}
}
