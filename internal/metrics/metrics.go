package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Standard latency histogram buckets (in seconds)
var DefaultLatencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0}

// Histogram tracks distributions of values across predefined buckets.
type Histogram struct {
	buckets []float64
	counts  []atomic.Uint64
	sum     atomic.Uint64 // stored as float64 bits using math.Float64bits / math.Float64frombits
	count   atomic.Uint64
}

// NewHistogram creates a thread-safe histogram with given buckets.
func NewHistogram(buckets []float64) *Histogram {
	return &Histogram{
		buckets: buckets,
		counts:  make([]atomic.Uint64, len(buckets)),
	}
}

// Observe records a value into the histogram.
func (h *Histogram) Observe(val float64) {
	h.count.Add(1)

	// Atomic float addition for sum
	for {
		oldBits := h.sum.Load()
		newSum := math.Float64frombits(oldBits) + val
		newBits := math.Float64bits(newSum)
		if h.sum.CompareAndSwap(oldBits, newBits) {
			break
		}
	}

	for i, b := range h.buckets {
		if val <= b {
			h.counts[i].Add(1)
		}
	}
}

// Registry manages all in-process Prometheus metrics.
type Registry struct {
	mu sync.RWMutex

	WriteSuccessTotal atomic.Uint64
	WriteErrorTotal   atomic.Uint64

	RecallSuccessTotal atomic.Uint64
	RecallErrorTotal   atomic.Uint64

	RateLimitExceededTotal atomic.Uint64

	AuthCacheHitsTotal   atomic.Uint64
	AuthCacheMissesTotal atomic.Uint64

	RecallDuration *Histogram
	WriteDuration  *Histogram
}

var (
	defaultRegistry     *Registry
	defaultRegistryOnce sync.Once
)

// Default returns the singleton global metrics registry.
func Default() *Registry {
	defaultRegistryOnce.Do(func() {
		defaultRegistry = &Registry{
			RecallDuration: NewHistogram(DefaultLatencyBuckets),
			WriteDuration:  NewHistogram(DefaultLatencyBuckets),
		}
	})
	return defaultRegistry
}

// RecordWrite observes a write operation status and duration.
func (r *Registry) RecordWrite(duration time.Duration, success bool) {
	if success {
		r.WriteSuccessTotal.Add(1)
	} else {
		r.WriteErrorTotal.Add(1)
	}
	r.WriteDuration.Observe(duration.Seconds())
}

// RecordRecall observes a recall operation status and duration.
func (r *Registry) RecordRecall(duration time.Duration, success bool) {
	if success {
		r.RecallSuccessTotal.Add(1)
	} else {
		r.RecallErrorTotal.Add(1)
	}
	r.RecallDuration.Observe(duration.Seconds())
}

// RecordRateLimitExceeded records when a client is rate limited.
func (r *Registry) RecordRateLimitExceeded() {
	r.RateLimitExceededTotal.Add(1)
}

// RecordAuthCacheHit records an auth cache hit.
func (r *Registry) RecordAuthCacheHit() {
	r.AuthCacheHitsTotal.Add(1)
}

// RecordAuthCacheMiss records an auth cache miss.
func (r *Registry) RecordAuthCacheMiss() {
	r.AuthCacheMissesTotal.Add(1)
}

// Render formats all metrics in Prometheus text format (0.0.4).
func (r *Registry) Render(ctx context.Context, db *sql.DB) (string, error) {
	var sb strings.Builder

	// Write metrics
	sb.WriteString("# HELP gams_write_total Total number of write operations\n")
	sb.WriteString("# TYPE gams_write_total counter\n")
	sb.WriteString(fmt.Sprintf("gams_write_total{status=\"success\"} %d\n", r.WriteSuccessTotal.Load()))
	sb.WriteString(fmt.Sprintf("gams_write_total{status=\"error\"} %d\n", r.WriteErrorTotal.Load()))

	// Recall metrics
	sb.WriteString("# HELP gams_recall_total Total number of recall operations\n")
	sb.WriteString("# TYPE gams_recall_total counter\n")
	sb.WriteString(fmt.Sprintf("gams_recall_total{status=\"success\"} %d\n", r.RecallSuccessTotal.Load()))
	sb.WriteString(fmt.Sprintf("gams_recall_total{status=\"error\"} %d\n", r.RecallErrorTotal.Load()))

	// Rate limiting metrics
	sb.WriteString("# HELP gams_rate_limit_exceeded_total Total number of rate-limited requests\n")
	sb.WriteString("# TYPE gams_rate_limit_exceeded_total counter\n")
	sb.WriteString(fmt.Sprintf("gams_rate_limit_exceeded_total %d\n", r.RateLimitExceededTotal.Load()))

	// Auth cache metrics
	sb.WriteString("# HELP gams_auth_cache_hits_total Total number of API key verification cache hits\n")
	sb.WriteString("# TYPE gams_auth_cache_hits_total counter\n")
	sb.WriteString(fmt.Sprintf("gams_auth_cache_hits_total %d\n", r.AuthCacheHitsTotal.Load()))

	sb.WriteString("# HELP gams_auth_cache_misses_total Total number of API key verification cache misses\n")
	sb.WriteString("# TYPE gams_auth_cache_misses_total counter\n")
	sb.WriteString(fmt.Sprintf("gams_auth_cache_misses_total %d\n", r.AuthCacheMissesTotal.Load()))

	// Write duration histogram
	sb.WriteString("# HELP gams_write_duration_seconds Write duration in seconds\n")
	sb.WriteString("# TYPE gams_write_duration_seconds histogram\n")
	renderHistogram(&sb, "gams_write_duration_seconds", r.WriteDuration)

	// Recall duration histogram
	sb.WriteString("# HELP gams_recall_duration_seconds Recall query duration in seconds\n")
	sb.WriteString("# TYPE gams_recall_duration_seconds histogram\n")
	renderHistogram(&sb, "gams_recall_duration_seconds", r.RecallDuration)

	// Claims gauge by status (queried from DB if available)
	if db != nil {
		sb.WriteString("# HELP gams_claims_total Current number of claims in database by status\n")
		sb.WriteString("# TYPE gams_claims_total gauge\n")

		rows, err := db.QueryContext(ctx, "SELECT status, COUNT(*) FROM claims GROUP BY status")
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var status string
				var count int64
				if err := rows.Scan(&status, &count); err == nil {
					sb.WriteString(fmt.Sprintf("gams_claims_total{status=\"%s\"} %d\n", status, count))
				}
			}
		}
	}

	return sb.String(), nil
}

func renderHistogram(sb *strings.Builder, name string, h *Histogram) {
	for i, b := range h.buckets {
		sb.WriteString(fmt.Sprintf("%s_bucket{le=\"%g\"} %d\n", name, b, h.counts[i].Load()))
	}
	sb.WriteString(fmt.Sprintf("%s_bucket{le=\"+Inf\"} %d\n", name, h.count.Load()))
	sumVal := math.Float64frombits(h.sum.Load())
	sb.WriteString(fmt.Sprintf("%s_sum %f\n", name, sumVal))
	sb.WriteString(fmt.Sprintf("%s_count %d\n", name, h.count.Load()))
}
