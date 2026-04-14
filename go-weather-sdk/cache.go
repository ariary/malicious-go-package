package weather

import (
	"sync"
	"time"
)

type cacheEntry struct {
	value     any
	expiresAt time.Time
}

type ttlCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	ttl     time.Duration
}

func newTTLCache(ttl time.Duration) *ttlCache {
	c := &ttlCache{
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
	}
	go c.evictLoop()
	return c
}

func (c *ttlCache) get(key string) (any, bool) {
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expiresAt) {
		return nil, false
	}
	return e.value, true
}

func (c *ttlCache) set(key string, value any) {
	c.mu.Lock()
	c.entries[key] = cacheEntry{value: value, expiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

func (c *ttlCache) evictLoop() {
	ticker := time.NewTicker(c.ttl)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		c.mu.Lock()
		for k, e := range c.entries {
			if now.After(e.expiresAt) {
				delete(c.entries, k)
			}
		}
		c.mu.Unlock()
	}
}
