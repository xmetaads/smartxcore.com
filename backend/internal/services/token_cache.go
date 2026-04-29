package services

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// tokenCache trades a tiny memory footprint for skipping a Postgres
// roundtrip on every heartbeat. At 1000 machines × 1 heartbeat / 60s
// the cache turns ~17 SELECT-by-token queries per second into zero.
//
// Entries auto-expire after ttl so a deleted-then-restored machine
// (or a freshly-rotated token) doesn't keep getting served from cache.
// Misses fall through to the DB and re-populate the cache on success.
type tokenCacheEntry struct {
	machineID uuid.UUID
	storedAt  time.Time
}

type tokenCache struct {
	mu  sync.RWMutex
	m   map[string]tokenCacheEntry
	ttl time.Duration
}

func newTokenCache(ttl time.Duration) *tokenCache {
	return &tokenCache{
		m:   make(map[string]tokenCacheEntry),
		ttl: ttl,
	}
}

func (c *tokenCache) get(token string) (uuid.UUID, bool) {
	c.mu.RLock()
	e, ok := c.m[token]
	c.mu.RUnlock()
	if !ok {
		return uuid.Nil, false
	}
	if time.Since(e.storedAt) > c.ttl {
		return uuid.Nil, false
	}
	return e.machineID, true
}

func (c *tokenCache) put(token string, machineID uuid.UUID) {
	c.mu.Lock()
	c.m[token] = tokenCacheEntry{machineID: machineID, storedAt: time.Now()}
	c.mu.Unlock()
}

func (c *tokenCache) drop(token string) {
	c.mu.Lock()
	delete(c.m, token)
	c.mu.Unlock()
}

// runJanitor sweeps expired entries every interval. Bounded by
// process lifetime — call from a goroutine and tear down via ctx.
func (c *tokenCache) sweep() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	dropped := 0
	for k, v := range c.m {
		if now.Sub(v.storedAt) > c.ttl {
			delete(c.m, k)
			dropped++
		}
	}
	return dropped
}
