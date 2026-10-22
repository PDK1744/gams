package ratelimit

import (
	"sync"
	"time"
)

// bucket implements an in-memory token bucket.
type bucket struct {
	mu         sync.Mutex
	capacity   float64
	refillRate float64
	tokens     float64
	lastRefill time.Time
}

func newBucket(capacity, refillRate float64) *bucket {
	now := time.Now()
	return &bucket{
		capacity:   capacity,
		refillRate: refillRate,
		tokens:     capacity,
		lastRefill: now,
	}
}

func (b *bucket) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.refillRate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.lastRefill = now
	}

	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true
	}

	return false
}

// Limiter manages per-key token buckets for rate limiting.
type Limiter struct {
	mu         sync.RWMutex
	rps        float64
	burst      float64
	buckets    map[string]*bucket
	lastAccess map[string]time.Time
	stopCh     chan struct{}
}

// NewLimiter creates a new thread-safe token bucket rate limiter.
func NewLimiter(rps, burst int) *Limiter {
	l := &Limiter{
		rps:        float64(rps),
		burst:      float64(burst),
		buckets:    make(map[string]*bucket),
		lastAccess: make(map[string]time.Time),
		stopCh:     make(chan struct{}),
	}

	// Background reaper for idle keys (> 10 minutes inactive)
	go l.reapLoop()
	return l
}

// Allow reports whether a request for the given key is permitted under the rate limit.
func (l *Limiter) Allow(key string) bool {
	b := l.getOrCreateBucket(key)
	return b.take()
}

func (l *Limiter) getOrCreateBucket(key string) *bucket {
	l.mu.RLock()
	b, exists := l.buckets[key]
	l.mu.RUnlock()
	if exists {
		l.mu.Lock()
		l.lastAccess[key] = time.Now()
		l.mu.Unlock()
		return b
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Double check
	if b, exists = l.buckets[key]; exists {
		l.lastAccess[key] = time.Now()
		return b
	}

	b = newBucket(l.burst, l.rps)
	l.buckets[key] = b
	l.lastAccess[key] = time.Now()
	return b
}

func (l *Limiter) reapLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case now := <-ticker.C:
			l.mu.Lock()
			for key, lastTime := range l.lastAccess {
				if now.Sub(lastTime) > 10*time.Minute {
					delete(l.buckets, key)
					delete(l.lastAccess, key)
				}
			}
			l.mu.Unlock()
		}
	}
}

// Stop terminates the background bucket reaper.
func (l *Limiter) Stop() {
	select {
	case <-l.stopCh:
	default:
		close(l.stopCh)
	}
}
