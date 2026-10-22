package ratelimit_test

import (
	"testing"
	"time"

	"github.com/PDK1744/gams/internal/ratelimit"
)

func TestLimiterBurstAndRefill(t *testing.T) {
	// 5 tokens burst, 10 tokens per second refill
	limiter := ratelimit.NewLimiter(10, 5)
	defer limiter.Stop()

	key := "test-agent-key"

	// 1. Burst of 5 requests should all pass
	for i := 0; i < 5; i++ {
		if !limiter.Allow(key) {
			t.Fatalf("request %d in burst should be allowed", i+1)
		}
	}

	// 2. 6th request immediately after should be rejected
	if limiter.Allow(key) {
		t.Fatalf("request 6 exceeding burst should be rejected")
	}

	// 3. Wait 150ms (at 10 rps, 150ms refills 1.5 tokens >= 1 token)
	time.Sleep(150 * time.Millisecond)

	// 4. Request should now be allowed
	if !limiter.Allow(key) {
		t.Fatalf("request after refill should be allowed")
	}

	// 5. Different key has its own bucket
	otherKey := "different-key"
	if !limiter.Allow(otherKey) {
		t.Fatalf("unrelated key should have fresh bucket")
	}
}
