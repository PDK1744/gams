package auth

import (
	"container/list"
	"sync"
	"time"
)

type cacheEntry[K comparable, V any] struct {
	key       K
	value     V
	expiresAt time.Time
	element   *list.Element
}

// LRUCache is a thread-safe, TTL-aware least-recently-used in-memory cache.
type LRUCache[K comparable, V any] struct {
	mu        sync.Mutex
	capacity  int
	ttl       time.Duration
	items     map[K]*cacheEntry[K, V]
	evictList *list.List
}

// NewLRUCache creates an LRUCache with the given capacity and TTL.
func NewLRUCache[K comparable, V any](capacity int, ttl time.Duration) *LRUCache[K, V] {
	if capacity <= 0 {
		capacity = 1000
	}
	return &LRUCache[K, V]{
		capacity:  capacity,
		ttl:       ttl,
		items:     make(map[K]*cacheEntry[K, V]),
		evictList: list.New(),
	}
}

// Get looks up a key. Returns value and true if found and not expired, otherwise false.
func (c *LRUCache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, found := c.items[key]
	if !found {
		var zero V
		return zero, false
	}

	if time.Now().After(entry.expiresAt) {
		c.removeEntry(entry)
		var zero V
		return zero, false
	}

	c.evictList.MoveToFront(entry.element)
	return entry.value, true
}

// Set stores a key-value pair with expiration.
func (c *LRUCache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, found := c.items[key]; found {
		entry.value = value
		entry.expiresAt = time.Now().Add(c.ttl)
		c.evictList.MoveToFront(entry.element)
		return
	}

	for len(c.items) >= c.capacity {
		c.evictOldest()
	}

	entry := &cacheEntry[K, V]{
		key:       key,
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
	}
	entry.element = c.evictList.PushFront(entry)
	c.items[key] = entry
}

// Delete removes a key from the cache.
func (c *LRUCache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, found := c.items[key]; found {
		c.removeEntry(entry)
	}
}

// Clear flushes all entries from the cache.
func (c *LRUCache[K, V]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[K]*cacheEntry[K, V])
	c.evictList.Init()
}

func (c *LRUCache[K, V]) removeEntry(entry *cacheEntry[K, V]) {
	c.evictList.Remove(entry.element)
	delete(c.items, entry.key)
}

func (c *LRUCache[K, V]) evictOldest() {
	elem := c.evictList.Back()
	if elem == nil {
		return
	}
	entry := elem.Value.(*cacheEntry[K, V])
	c.removeEntry(entry)
}
