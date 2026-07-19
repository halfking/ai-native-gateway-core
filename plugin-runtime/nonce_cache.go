package pluginruntime

import (
	"sync"
	"time"
)

// NonceCache 记录已见的 context token nonce，防止 maxAge 窗口内重放。
// TTL 应略大于 VerifyContext 的 maxAge；过期后 key 可再次使用。
type NonceCache struct {
	ttl  time.Duration
	mu   sync.Mutex
	seen map[string]time.Time
}

func NewNonceCache(ttl time.Duration) *NonceCache {
	return &NonceCache{ttl: ttl, seen: map[string]time.Time{}}
}

// SeenFirst 记录 key。返回 true=首次见到（接受），false=已存在（重放，拒绝）。
// 并发安全：同一 key 只有一个调用者拿到 true。
func (c *NonceCache) SeenFirst(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if t, ok := c.seen[key]; ok && now.Sub(t) < c.ttl {
		return false
	}
	c.evictLocked(now)
	c.seen[key] = now
	return true
}

func (c *NonceCache) evictLocked(now time.Time) {
	for k, t := range c.seen {
		if now.Sub(t) >= c.ttl {
			delete(c.seen, k)
		}
	}
}
