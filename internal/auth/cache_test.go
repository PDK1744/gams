package auth

import (
	"strconv"
	"testing"
	"time"
)

func TestLRUCacheBasic(t *testing.T) {
	cache := NewLRUCache[string, string](3, 100*time.Millisecond)

	cache.Set("k1", "v1")
	cache.Set("k2", "v2")
	cache.Set("k3", "v3")

	val, found := cache.Get("k1")
	if !found || val != "v1" {
		t.Fatalf("expected v1, got %v, found=%v", val, found)
	}

	// Insert 4th element, should evict k2 (least recently used, since k1 was accessed)
	cache.Set("k4", "v4")

	if _, found := cache.Get("k2"); found {
		t.Fatalf("expected k2 to be evicted")
	}

	if val, found := cache.Get("k1"); !found || val != "v1" {
		t.Fatalf("expected k1 to be present, got %v", val)
	}
	if val, found := cache.Get("k3"); !found || val != "v3" {
		t.Fatalf("expected k3 to be present, got %v", val)
	}
	if val, found := cache.Get("k4"); !found || val != "v4" {
		t.Fatalf("expected k4 to be present, got %v", val)
	}
}

func TestLRUCacheTTL(t *testing.T) {
	cache := NewLRUCache[string, int](10, 20*time.Millisecond)

	cache.Set("expireMe", 42)
	val, found := cache.Get("expireMe")
	if !found || val != 42 {
		t.Fatalf("expected 42, got %v, found=%v", val, found)
	}

	time.Sleep(30 * time.Millisecond)
	if _, found := cache.Get("expireMe"); found {
		t.Fatalf("expected item to be expired")
	}
}

func TestLRUCacheConcurrent(t *testing.T) {
	cache := NewLRUCache[string, int](50, 1*time.Second)
	done := make(chan struct{})

	for i := 0; i < 10; i++ {
		go func(worker int) {
			for j := 0; j < 100; j++ {
				key := strconv.Itoa((worker*100 + j) % 60)
				cache.Set(key, j)
				cache.Get(key)
			}
			done <- struct{}{}
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}
