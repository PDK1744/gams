package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	HeaderTimestamp = "X-GAMS-Timestamp"
	HeaderSignature = "X-GAMS-Signature"
	MaxTimestampSkew = 300 * time.Second // 5 minutes
)

var (
	ErrMissingSignature = errors.New("missing request signature or timestamp headers")
	ErrTimestampExpired = errors.New("request timestamp outside acceptable window (±5 minutes)")
	ErrInvalidSignature = errors.New("request signature verification failed")
)

// ComputeBodyHash returns the lowercase hex-encoded SHA-256 hash of body bytes.
func ComputeBodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// BuildCanonicalString constructs the canonical signing string:
// ${METHOD}\n${PATH}\n${TIMESTAMP}\n${BODY_HASH}
func BuildCanonicalString(method, requestURI, timestamp, bodyHash string) string {
	return fmt.Sprintf("%s\n%s\n%s\n%s", strings.ToUpper(method), requestURI, timestamp, bodyHash)
}

// ComputeSignature calculates the HMAC-SHA256 hex signature over the canonical payload.
func ComputeSignature(secretKey, method, requestURI, timestamp string, body []byte) string {
	bodyHash := ComputeBodyHash(body)
	canonical := BuildCanonicalString(method, requestURI, timestamp, bodyHash)
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature validates that timestamp is within the ±5 minute window and matches signature.
func VerifySignature(secretKey, method, requestURI, timestamp, signature string, body []byte) error {
	if strings.TrimSpace(timestamp) == "" || strings.TrimSpace(signature) == "" {
		return ErrMissingSignature
	}

	// Parse timestamp (accept Unix seconds integer or RFC3339)
	var ts time.Time
	if unixSec, err := strconv.ParseInt(timestamp, 10, 64); err == nil {
		ts = time.Unix(unixSec, 0)
	} else if parsed, err := time.Parse(time.RFC3339, timestamp); err == nil {
		ts = parsed
	} else {
		return fmt.Errorf("%w: invalid timestamp format", ErrTimestampExpired)
	}

	now := time.Now()
	diff := now.Sub(ts)
	if diff < 0 {
		diff = -diff
	}
	if diff > MaxTimestampSkew {
		return ErrTimestampExpired
	}

	expected := ComputeSignature(secretKey, method, requestURI, timestamp, body)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(strings.ToLower(signature))) != 1 {
		return ErrInvalidSignature
	}

	return nil
}
