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
	// 2026-10-15 审计：held 的键是 params.ClientModel —— 客户端可控字符串，
	// 而原实现只覆写不删除，于是 Redis 缺席（fpSlotRedis==nil → main.go 不
	// 注入 RedisProbeOneFlight）时这条进程内回落会随不同 model 串单调增长，
	// 构成无界内存增长。现惰性剪枝：只删已过期项，代价 O(过期数)，
	// 且全部在持锁期间完成，不引入新的竞态窗口。
	for k, exp := range m.held {
		if !exp.After(now) {
			delete(m.held, k)
		}
	}
	if _, ok := m.held[model]; ok {
		// 剪枝后仍在表内 ⇒ 未过期 ⇒ 已有持有者。
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
