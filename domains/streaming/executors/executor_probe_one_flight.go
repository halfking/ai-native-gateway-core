package executors

// executor_probe_one_flight.go — P0-D model-scoped single-flight for ProbeSync.
// Redis SET NX PX is the cross-instance lock; memory fallback when Redis is
// down (multi-instance may each probe once — planning §风险可接受).

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	probeOneFlightKeyPrefix  = "llmgw:probe_one:"
	probeOneFlightDefaultTTL = 8 * time.Second
)

// ProbeOneFlight is the (model) single-flight lock around ProbeSync.
// TryAcquire true = this caller owns the probe; false = another request
// already holds it (skip ProbeSync — 上游调用不重复).
type ProbeOneFlight interface {
	TryAcquire(ctx context.Context, model string, ttl time.Duration) bool
}

// MemoryProbeOneFlight is process-local SET NX equivalent.
type MemoryProbeOneFlight struct {
	mu   sync.Mutex
	held map[string]time.Time // model → expiry
}

// NewMemoryProbeOneFlight constructs an empty in-process flight lock.
func NewMemoryProbeOneFlight() *MemoryProbeOneFlight {
	return &MemoryProbeOneFlight{held: make(map[string]time.Time)}
}

// TryAcquire implements ProbeOneFlight.
func (m *MemoryProbeOneFlight) TryAcquire(_ context.Context, model string, ttl time.Duration) bool {
	if m == nil || model == "" {
		return true
	}
	if ttl <= 0 {
		ttl = probeOneFlightDefaultTTL
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if exp, ok := m.held[model]; ok && exp.After(now) {
		return false
	}
	m.held[model] = now.Add(ttl)
	return true
}

// RedisProbeOneFlight uses SET key NX PX ttl.
type RedisProbeOneFlight struct {
	rdb    redis.UniversalClient
	prefix string
}

// NewRedisProbeOneFlight wraps a go-redis client. prefix empty → default.
func NewRedisProbeOneFlight(rdb redis.UniversalClient, prefix string) *RedisProbeOneFlight {
	if prefix == "" {
		prefix = probeOneFlightKeyPrefix
	}
	return &RedisProbeOneFlight{rdb: rdb, prefix: prefix}
}

// TryAcquire implements ProbeOneFlight. Redis errors fail-open (acquire=true)
// so a redis blip does not freeze all no-candidate recovery — same posture as
// planning「Redis 不可用时回落进程内单飞」; callers that want hard fail should
// layer MemoryProbeOneFlight themselves.
func (r *RedisProbeOneFlight) TryAcquire(ctx context.Context, model string, ttl time.Duration) bool {
	if r == nil || r.rdb == nil || model == "" {
		return true
	}
	if ttl <= 0 {
		ttl = probeOneFlightDefaultTTL
	}
	ok, err := r.rdb.SetNX(ctx, r.prefix+model, "1", ttl).Result()
	if err != nil {
		return true // fail-open → local/other instances may still probe
	}
	return ok
}
