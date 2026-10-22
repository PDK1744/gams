package metrics_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PDK1744/gams/internal/metrics"
)

func TestMetricsRegistry(t *testing.T) {
	reg := metrics.Default()

	reg.RecordWrite(10*time.Millisecond, true)
	reg.RecordWrite(25*time.Millisecond, false)
	reg.RecordRecall(5*time.Millisecond, true)
	reg.RecordRecall(200*time.Millisecond, false)
	reg.RecordRateLimitExceeded()
	reg.RecordAuthCacheHit()
	reg.RecordAuthCacheMiss()

	out, err := reg.Render(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error rendering metrics: %v", err)
	}

	expectedSubstrings := []string{
		"gams_write_total{status=\"success\"}",
		"gams_write_total{status=\"error\"}",
		"gams_recall_total{status=\"success\"}",
		"gams_recall_total{status=\"error\"}",
		"gams_rate_limit_exceeded_total",
		"gams_auth_cache_hits_total",
		"gams_auth_cache_misses_total",
		"gams_write_duration_seconds_bucket",
		"gams_recall_duration_seconds_bucket",
		"gams_write_duration_seconds_count",
		"gams_recall_duration_seconds_count",
	}

	for _, sub := range expectedSubstrings {
		if !strings.Contains(out, sub) {
			t.Errorf("expected rendered metrics to contain %q, but got:\n%s", sub, out)
		}
	}
}
