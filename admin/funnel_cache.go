package admin

import (
	"strings"
	"sync"
	"time"
)

type funnelCacheEntry struct {
	payload   map[string]interface{}
	expiresAt time.Time
}

type funnelCache struct {
	mu    sync.RWMutex
	items map[string]funnelCacheEntry
	ttl   time.Duration
}

var globalFunnelCache = &funnelCache{
	items: make(map[string]funnelCacheEntry),
	ttl:   2 * time.Minute,
}

func funnelCacheKey(model, window string) string {
	return model + "|" + window
}

func (c *funnelCache) get(key string) (map[string]interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.items[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.payload, true
}

func (c *funnelCache) set(key string, payload map[string]interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = funnelCacheEntry{payload: payload, expiresAt: time.Now().Add(c.ttl)}
}

func (c *funnelCache) invalidateModel(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := model + "|"
	for k := range c.items {
		if strings.HasPrefix(k, prefix) {
			delete(c.items, k)
		}
	}
}
