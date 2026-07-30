package main

import (
	"sync"
	"time"
)

// ttlCache stores rendered documents per cache key so repeated Pi polls do not
// reach the providers again inside the TTL window.
type ttlCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	value     []byte
	storedAt  time.Time
	expiresAt time.Time
}

func newTTLCache() *ttlCache {
	return &ttlCache{entries: map[string]cacheEntry{}}
}

// get returns a fresh entry plus the time it was produced.
func (c *ttlCache) get(key string) ([]byte, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, time.Time{}, false
	}
	return entry.value, entry.storedAt, true
}

func (c *ttlCache) put(key string, value []byte, ttl time.Duration) time.Time {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{value: value, storedAt: now, expiresAt: now.Add(ttl)}
	return now
}

func (c *ttlCache) invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// rateLimiter bounds how often a caller may force a refresh.
type rateLimiter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{last: map[string]time.Time{}}
}

// allow reports whether key may act now, recording the attempt when permitted.
func (r *rateLimiter) allow(key string, interval time.Duration) bool {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if previous, ok := r.last[key]; ok && now.Sub(previous) < interval {
		return false
	}
	r.last[key] = now
	return true
}
