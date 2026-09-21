package admin

import (
	"encoding/json"
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

func funnelCacheKey(scope, model, window string) string {
	return scope + "|" + model + "|" + window
}

func cloneFunnelPayload(payload map[string]interface{}) map[string]interface{} {
	if payload == nil {
		return nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	var clone map[string]interface{}
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return nil
	}
	return clone
}

func (c *funnelCache) get(key string) (map[string]interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.items[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	clone := cloneFunnelPayload(entry.payload)
	return clone, clone != nil
}

func (c *funnelCache) set(key string, payload map[string]interface{}) {
	clone := cloneFunnelPayload(payload)
	if clone == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = funnelCacheEntry{payload: clone, expiresAt: time.Now().Add(c.ttl)}
}

func (c *funnelCache) invalidateModel(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.items {
		parts := strings.SplitN(k, "|", 3)
		if len(parts) == 3 && parts[1] == model {
			delete(c.items, k)
		}
	}
}
