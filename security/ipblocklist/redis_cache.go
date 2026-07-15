package ipblocklist

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache wraps Redis + in-memory fallback for hot blocklist checks.
type Cache struct {
	rdb    *redis.Client
	store  Store
	ttl    time.Duration
	mu     sync.RWMutex
	local  map[string][]Entry // scope -> entries
	localV int64
}

func NewCache(rdb *redis.Client, store Store, ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &Cache{rdb: rdb, store: store, ttl: ttl, local: map[string][]Entry{}}
}

func scopeRedisKey(scope string) string {
	switch scope {
	case ScopeCollect:
		return redisKeyCollect
	case ScopeOps:
		return redisKeyOps
	default:
		return redisKeyGlobal
	}
}

// Reload refreshes Redis SET members from PostgreSQL.
func (c *Cache) Reload(ctx context.Context, scope string) error {
	entries, err := c.store.ListActive(ctx, scope)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.local[scope] = entries
	c.localV++
	c.mu.Unlock()

	if c.rdb == nil {
		return nil
	}
	key := scopeRedisKey(scope)
	pipe := c.rdb.Pipeline()
	pipe.Del(ctx, key)
	for _, e := range entries {
		pipe.SAdd(ctx, key, fmt.Sprintf("%d:%s", e.ID, e.IPOrCIDR))
	}
	pipe.Set(ctx, redisVersionKey+":"+scope, strconv.FormatInt(time.Now().UnixNano(), 10), c.ttl*10)
	_, err = pipe.Exec(ctx)
	return err
}

// IsBlocked checks Redis/local cache first, then store.
func (c *Cache) IsBlocked(ctx context.Context, ip net.IP, scope string) (bool, *Entry, error) {
	if ip == nil {
		return false, nil, nil
	}
	scopes := []string{ScopeGlobal}
	if scope != "" && scope != ScopeGlobal {
		scopes = append(scopes, scope)
	}
	for _, sc := range scopes {
		if blocked, entry := c.matchLocal(ip, sc); blocked {
			return true, entry, nil
		}
	}
	// Fallback to DB if cache empty.
	return c.store.IsBlocked(ctx, ip, scope)
}

func (c *Cache) matchLocal(ip net.IP, scope string) (bool, *Entry) {
	c.mu.RLock()
	entries := c.local[scope]
	c.mu.RUnlock()
	for i := range entries {
		if MatchIP(entries[i].IPOrCIDR, ip) {
			e := entries[i]
			return true, &e
		}
	}
	return false, nil
}

// Invalidate bumps local version and optionally reloads.
func (c *Cache) Invalidate(ctx context.Context, scope string) error {
	return c.Reload(ctx, scope)
}
