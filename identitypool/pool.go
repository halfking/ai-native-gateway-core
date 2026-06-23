// Package identitypool implements a global cap on the number of distinct
// end-user fingerprint identities the gateway will accept.
//
// Three-layer architecture (this package implements Layer 0 — global cap):
//
//	Layer 0 (this package) — global cap on TOTAL distinct identities
//	                        across all credentials/providers. Once the cap
//	                        is reached, new users MUST reuse an existing
//	                        fingerprint (round-robin among least-recently-used).
//	Layer 1 (credentialfpslot) — per-credential fingerprint pool size. Each
//	                        credential can simulate at most N virtual users.
//	Layer 2 (limiter) — per-credential in-flight REQUEST concurrency. Releases
//	                        immediately when the request completes.
//
// Layers are independent. A user may be admitted by Layer 0 but find no
// free slot in Layer 1 (saturated at credential level); the request then
// fails over to another credential or returns 429.
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

// Identity is the canonical key for a distinct end-user. The gateway derives
// this from the request fingerprint (X-Device-Seed, User-Agent, IP, etc.) —
// see identity/ package for extraction.
//
// Stored as a stable string so that two requests from the same physical user
// (same browser/device) hash to the same identity and share the same
// fingerprint slot — even if their IP address rotates or cookies change.
type Identity string

// Config controls pool behaviour.
type Config struct {
	// MaxIdentities is the global cap. 0 = unlimited (use Redis INCR counter
	// without bounds check). Default 10000 if unset.
	MaxIdentities int
	// LRUWindow is how long an identity is "remembered" before its slot is
	// recycled. Default 24h — matches the credentialfpslot slot TTL so the
	// two systems evict in lock-step. Should be >= the longest fp_slot TTL.
	LRUWindow time.Duration
	// Enabled switches the cap on/off entirely. When false, all identities
	// pass through (no Redis touch).
	Enabled bool
}

// Pool owns the global identity cap.
//
// Backed by Redis when available; falls back to in-memory storage for
// unit tests and local development.
//
// Acquisition is two-phase:
//  1. Try to reserve a new identity (atomic INCR + check). If the cap
//     would be exceeded, roll back and fall into the recycle path.
//  2. If reservation fails (cap reached), recycle the least-recently-used
//     existing identity. The recycled slot's TTL is refreshed, and the
//     caller receives the recycled Identity string. The new request is
//     then treated by upstream as if it were that previous user.
type Pool struct {
	cfg                Config
	client             *redis.Client
	acquireRedisScript *redis.Script

	// In-memory fallback state (only used when client == nil).
	mu        sync.Mutex
	memUsed   int
	memLRU    map[Identity]time.Time
	memOrder  []Identity // FIFO list of insertion order (acts as a coarse LRU)
	memInsert int
}

// New creates a pool. client may be nil (memory fallback, used by tests).
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

// Enabled reports whether the pool is enforcing the cap.
func (p *Pool) Enabled() bool { return p != nil && p.cfg.Enabled }

// MaxIdentities returns the configured cap.
func (p *Pool) MaxIdentities() int {
	if p == nil {
		return 0
	}
	return p.cfg.MaxIdentities
}

// Acquire reserves a slot for the given end-user identity. Returns:
//   - acquired=true  → fresh slot reserved (new identity counted toward the cap)
//   - acquired=false → reused existing identity (cap reached, this user
//     was assigned the LRU recycled slot)
//
// The returned Identity string is what the caller should pass downstream
// as the request's holder — even on recycle, it is the recycled identity
// (not the new fingerprint), so the user appears as the recycled user to
// upstream providers.
//
// For requests with no fingerprint (anonymous), pass the empty string and
// a fresh synthetic identity will be minted.
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

	// Phase 1: try to reserve a fresh slot.
	// Hash the identity into a stable 64-bit bucket so that the same end-user
	// always re-acquires the same slot on repeat visits (stickiness across
	// session boundaries).
	bucket := hashIdentity(string(ident))

	// SETNX with TTL acts as our reservation primitive: if the key exists,
	// this is a known identity (no counter increment). If it does not exist,
	// we INCR a global counter atomically and SET the key — but only if the
	// counter has not yet exceeded the cap.
	key := redisIdentityKey(bucket)
	res, err := p.acquireRedisScript.Run(ctx, p.client,
		[]string{key, redisCounterKey()},
		cap,
		int(p.cfg.LRUWindow.Seconds()),
	).Result()
	if err != nil {
		return "", false, fmt.Errorf("identity pool acquire failed: %w", err)
	}

	// Script returns: { reused: 0|1, recycled_to: "" | identity_string }
	out, ok := res.([]interface{})
	if !ok || len(out) != 2 {
		return "", false, fmt.Errorf("identity pool acquire: unexpected script result: %T", res)
	}
	reusedI, _ := out[0].(int64)
	recycledStr, _ := out[1].(string)

	if reusedI == 0 {
		// Fresh slot reserved — caller is the canonical holder.
		return ident, true, nil
	}
	// Cap reached and the script picked a recycled identity.
	return Identity(recycledStr), false, nil
}

// acquireRedisScript atomically reserves or recycles an identity slot.
//
// KEYS[1] = per-identity key (llmgw:ident:<bucket>)
// KEYS[2] = global counter key   (llmgw:ident:counter)
//
// ARGV[1] = max identities
// ARGV[2] = LRU window (seconds)
//
// Returns: { reused: 0|1, recycled_to: "<identity_string>" }
//
// If KEYS[1] exists → mark as reused (TTL refresh), return {0, ""}
// Else if counter < max → INCR counter, SET KEYS[1] with TTL, return {1, ""}
// Else (cap reached) → pop the oldest entry from a sorted set tracking
//
//	insertion order, recycle it: assign to KEYS[1].
//	Return {0, recycled_identity_string}.
var acquireRedisScript = redis.NewScript(`
	local idKey = KEYS[1]
	local counterKey = KEYS[2]
	local max = tonumber(ARGV[1])
	local ttl = tonumber(ARGV[2])

	-- Case 1: identity already known — refresh TTL and return.
	if redis.call('EXISTS', idKey) == 1 then
		redis.call('EXPIRE', idKey, ttl)
		-- bump "last used" timestamp in sorted set for LRU tracking
		redis.call('ZADD', KEYS[2] .. ':lru', redis.call('TIME')[1], idKey)
		return {0, ''}
	end

	-- Case 2: new identity. Try to reserve a fresh slot.
	local current = tonumber(redis.call('GET', counterKey) or '0')
	if current < max then
		redis.call('INCR', counterKey)
		redis.call('SET', idKey, '1', 'EX', ttl)
		redis.call('ZADD', KEYS[2] .. ':lru', redis.call('TIME')[1], idKey)
		return {1, ''}
	end

	-- Case 3: cap reached. Recycle the least-recently-used identity.
	-- ZRANGEBYSCORE with WITHSCORES finds the entry with the smallest score.
	local oldest = redis.call('ZRANGE', KEYS[2] .. ':lru', 0, 0, 'WITHSCORES')
	if #oldest == 0 then
		-- LRU tracking empty even though counter says cap reached: this can
		-- happen if all identities have already expired. Reset counter and
		-- try again on next request.
		redis.call('DEL', counterKey)
		return {1, ''}
	end
	local recycledKey = oldest[1]
	local recycledBucket = string.sub(recycledKey, string.len('llmgw:ident:') + 1)
	-- Reset TTL on the recycled slot; caller will see the recycled identity.
	redis.call('DEL', recycledKey)
	redis.call('SET', idKey, '1', 'EX', ttl)
	redis.call('ZREM', KEYS[2] .. ':lru', recycledKey)
	redis.call('ZADD', KEYS[2] .. ':lru', redis.call('TIME')[1], idKey)
	return {0, recycledBucket}
`)

func redisIdentityKey(bucket uint64) string {
	return fmt.Sprintf("llmgw:ident:%016x", bucket)
}

func redisCounterKey() string {
	return "llmgw:ident:counter"
}

// hashIdentity returns a stable 64-bit bucket key for an identity string.
// Uses SHA-256 so the output is uniformly distributed and resistant to
// collision attacks that could let an attacker deliberately cluster identities.
func hashIdentity(s string) uint64 {
	h := sha256.Sum256([]byte(s))
	return binary.BigEndian.Uint64(h[:8])
}

// ── In-memory fallback ────────────────────────────────────────────────

func (p *Pool) acquireMemory(ident Identity) (Identity, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	p.memPurgeLocked(now)

	// Case 1: known identity — refresh LRU.
	if last, ok := p.memLRU[ident]; ok && now.Sub(last) < p.cfg.LRUWindow {
		p.memLRU[ident] = now
		return ident, false, nil
	}

	// Case 2: new identity — reserve if under cap.
	if p.memUsed < p.cfg.MaxIdentities {
		p.memLRU[ident] = now
		p.memOrder = append(p.memOrder, ident)
		p.memUsed++
		return ident, true, nil
	}

	// Case 3: cap reached — recycle the oldest inserted identity.
	if len(p.memOrder) == 0 {
		// Should not happen given memUsed > 0, but be defensive.
		p.memUsed = 0
		p.memLRU[ident] = now
		p.memOrder = append(p.memOrder, ident)
		p.memUsed++
		return ident, true, nil
	}
	recycled := p.memOrder[p.memInsert%len(p.memOrder)]
	p.memInsert++
	delete(p.memLRU, recycled)
	p.memLRU[ident] = now
	p.memOrder = append(p.memOrder, ident)
	return recycled, false, nil
}

func (p *Pool) memPurgeLocked(now time.Time) {
	for id, last := range p.memLRU {
		if now.Sub(last) > p.cfg.LRUWindow {
			delete(p.memLRU, id)
			p.memUsed--
		}
	}
	// memOrder grows monotonically; periodic compaction would be nice but
	// the per-request cost is O(1) lookup so we skip it for now.
}

// Stats returns a snapshot for monitoring dashboards.
type Stats struct {
	MaxIdentities  int    `json:"max_identities"`
	UsedIdentities int    `json:"used_identities"`
	RecycledTotal  int64  `json:"recycled_total"`
	WindowSeconds  int    `json:"window_seconds"`
	BackendMode    string `json:"backend_mode"` // "redis" or "memory"
}

func (p *Pool) Stats(ctx context.Context) Stats {
	s := Stats{
		MaxIdentities: p.cfg.MaxIdentities,
		WindowSeconds: int(p.cfg.LRUWindow.Seconds()),
	}
	if p.client != nil {
		s.BackendMode = "redis"
		v, err := p.client.Get(ctx, redisCounterKey()).Int64()
		if err == nil {
			s.UsedIdentities = int(v)
		}
	} else {
		s.BackendMode = "memory"
		p.mu.Lock()
		defer p.mu.Unlock()
		p.memPurgeLocked(time.Now())
		s.UsedIdentities = p.memUsed
	}
	return s
}

// ErrDisabled indicates the pool is not enabled.
var ErrDisabled = errors.New("identity pool disabled")
