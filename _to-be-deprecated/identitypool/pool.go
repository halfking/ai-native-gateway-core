//go:build legacy_identity_pool

// Package identitypool implements a global cap on the number of distinct
// end-user fingerprint identities the gateway will accept.
//
// Deprecated: kept behind legacy_identity_pool build tag only.
package identitypool

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type Identity string

type Config struct {
	MaxIdentities int
	LRUWindow     time.Duration
	Enabled       bool
}

type Pool struct {
	cfg                Config
	client             *redis.Client
	acquireRedisScript *redis.Script

	mu        sync.Mutex
	memUsed   int
	memLRU    map[Identity]time.Time
	memOrder  []Identity
	memInsert int
}

func New(cfg Config, client *redis.Client) *Pool {
	if cfg.MaxIdentities <= 0 {
		cfg.MaxIdentities = 10000
	}
	if cfg.LRUWindow <= 0 {
		cfg.LRUWindow = 24 * time.Hour
	}
	return &Pool{
		cfg:                cfg,
		client:             client,
		acquireRedisScript: acquireRedisScript,
		memLRU:             make(map[Identity]time.Time),
		memOrder:           make([]Identity, 0, 1024),
	}
}

func (p *Pool) Enabled() bool { return p != nil && p.cfg.Enabled }

func (p *Pool) MaxIdentities() int {
	if p == nil {
		return 0
	}
	return p.cfg.MaxIdentities
}

func (p *Pool) Acquire(ctx context.Context, ident Identity) (Identity, bool, error) {
	if !p.Enabled() {
		return ident, false, nil
	}
	if p.client != nil {
		return p.acquireRedis(ctx, ident)
	}
	return p.acquireMemory(ident)
}

func (p *Pool) acquireRedis(ctx context.Context, ident Identity) (Identity, bool, error) {
	cap := p.cfg.MaxIdentities
	bucket := hashIdentity(string(ident))
	key := redisIdentityKey(bucket)
	res, err := p.acquireRedisScript.Run(ctx, p.client,
		[]string{key, redisCounterKey()},
		cap,
		int(p.cfg.LRUWindow.Seconds()),
	).Result()
	if err != nil {
		return "", false, fmt.Errorf("identity pool acquire failed: %w", err)
	}
	out, ok := res.([]interface{})
	if !ok || len(out) != 2 {
		return "", false, fmt.Errorf("identity pool acquire: unexpected script result: %T", res)
	}
	reusedI, _ := out[0].(int64)
	recycledStr, _ := out[1].(string)
	if reusedI == 0 {
		return ident, true, nil
	}
	return Identity(recycledStr), false, nil
}

var acquireRedisScript = redis.NewScript(`
	local idKey = KEYS[1]
	local counterKey = KEYS[2]
	local max = tonumber(ARGV[1])
	local ttl = tonumber(ARGV[2])

	if redis.call('EXISTS', idKey) == 1 then
		redis.call('EXPIRE', idKey, ttl)
		redis.call('ZADD', KEYS[2] .. ':lru', redis.call('TIME')[1], idKey)
		return {0, ''}
	end

	local current = tonumber(redis.call('GET', counterKey) or '0')
	if current < max then
		redis.call('INCR', counterKey)
		redis.call('SET', idKey, '1', 'EX', ttl)
		redis.call('ZADD', KEYS[2] .. ':lru', redis.call('TIME')[1], idKey)
		return {1, ''}
	end

	local oldest = redis.call('ZRANGE', KEYS[2] .. ':lru', 0, 0, 'WITHSCORES')
	if #oldest == 0 then
		redis.call('DEL', counterKey)
		return {1, ''}
	end
	local recycledKey = oldest[1]
	local recycledBucket = string.sub(recycledKey, string.len('llmgw:ident:') + 1)
	redis.call('DEL', recycledKey)
	redis.call('SET', idKey, '1', 'EX', ttl)
	redis.call('ZREM', KEYS[2] .. ':lru', recycledKey)
	redis.call('ZADD', KEYS[2] .. ':lru', redis.call('TIME')[1], idKey)
	return {0, recycledBucket}
`)

func (p *Pool) acquireMemory(ident Identity) (Identity, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	p.evictExpired(now)

	if _, ok := p.memLRU[ident]; ok {
		p.memLRU[ident] = now
		return ident, false, nil
	}
	if p.memUsed < p.cfg.MaxIdentities {
		p.memUsed++
		p.memLRU[ident] = now
		p.memOrder = append(p.memOrder, ident)
		return ident, true, nil
	}
	if len(p.memOrder) == 0 {
		return "", false, errors.New("identity pool: cap reached but no LRU entries")
	}
	recycled := p.memOrder[0]
	p.memOrder = append(p.memOrder[1:], recycled)
	delete(p.memLRU, recycled)
	p.memLRU[recycled] = now
	return recycled, false, nil
}

type Stats struct {
	MaxIdentities  int    `json:"max_identities"`
	UsedIdentities int    `json:"used_identities"`
	WindowSeconds  int64  `json:"window_seconds"`
	BackendMode    string `json:"backend_mode"`
}

func (p *Pool) Stats(ctx context.Context) Stats {
	if p == nil {
		return Stats{}
	}
	if p.client != nil {
		count, _ := p.client.Get(ctx, redisCounterKey()).Int()
		return Stats{
			MaxIdentities:  p.cfg.MaxIdentities,
			UsedIdentities: count,
			WindowSeconds:  int64(p.cfg.LRUWindow.Seconds()),
			BackendMode:    "redis",
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evictExpired(time.Now())
	return Stats{
		MaxIdentities:  p.cfg.MaxIdentities,
		UsedIdentities: p.memUsed,
		WindowSeconds:  int64(p.cfg.LRUWindow.Seconds()),
		BackendMode:    "memory",
	}
}

func (p *Pool) SetMaxIdentities(n int) {
	if p == nil || n <= 0 {
		return
	}
	p.cfg.MaxIdentities = n
}

func (p *Pool) evictExpired(now time.Time) {
	if p.cfg.LRUWindow <= 0 {
		return
	}
	cutoff := now.Add(-p.cfg.LRUWindow)
	if len(p.memLRU) == 0 {
		return
	}
	newOrder := p.memOrder[:0]
	for _, ident := range p.memOrder {
		last, ok := p.memLRU[ident]
		if !ok {
			continue
		}
		if last.Before(cutoff) {
			delete(p.memLRU, ident)
			if p.memUsed > 0 {
				p.memUsed--
			}
			continue
		}
		newOrder = append(newOrder, ident)
	}
	p.memOrder = newOrder
}

func redisIdentityKey(bucket uint64) string {
	return fmt.Sprintf("llmgw:ident:%d", bucket)
}

func redisCounterKey() string {
	return "llmgw:ident:counter"
}

func hashIdentity(s string) uint64 {
	if s == "" {
		return 0
	}
	sum := sha256.Sum256([]byte(s))
	return binary.BigEndian.Uint64(sum[:8])
}
