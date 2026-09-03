package cache

import (
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

// NodeView 是节点状态的 LRU 镜像条目。字段是 domains/ursm/v2/api.NodeView
// 的子集(只取镜像需要的字段)。generation + source_priority 用于单调裁决。
//
// 2026-07-27 (M2): extended with LatEWMA + SR5m so the mirror can serve the
// FilterAndScore hot path (which scores on price 0.4 + latency 0.4 +
// stability 0.2·SR5m). Without these the LRU hit could only answer
// availability, not scoring, so every request still hit Redis — defeating
// the purpose. CachedAt lets the scorer detect a stale entry.
//
// 2026-07-28 (audit): TenantID added so the mirror can scope entries to
// the calling tenant — afb13c9ea introduced the tenant-aware key format
// (NodeKeyForTenant) and this field lets the LRU key stay aligned.
type NodeView struct {
	TenantID       string
	CredentialID   int
	RawModel       string
	Available      bool
	Reason         string
	Generation     int64
	SourcePriority int
	FailStreak     int
	CoolUntil      time.Time
	// Scoring fields (M2): mirror what FilterAndScore reads so a cache hit
	// can produce the same Score as a fresh Redis read.
	LatEWMA   int
	SR5m      float64
	Samples5m int
	// HealthStatus (会话优化 v4 T5 / P1-5, UT-UR-12) carries the rich
	// node-health enum (api.HealthStatus*) from the Redis "health" field so
	// a mirror hit surfaces the same value a fresh read would. DISPLAY-ONLY:
	// nothing in the scoring/eligibility path reads it.
	HealthStatus string
	// CachedAt is when this entry was populated from Redis. Observability/
	// staleness hint (the soft-expire decision uses softExpireAt, not this).
	CachedAt     time.Time
	softExpireAt time.Time // 软过期点;超过后 Get 返回 miss
}

// NodeMirrorShards is the number of independent LRU shards backing a
// NodeMirror. Sharding was added in M3 (2026-07-28, ursm/v2 concurrency
// audit) so that the FilterAndScore hot path — which calls Get on every
// seed — does not serialise all readers behind a single mutex. The exact
// value is not part of the public API: it just splits the working set
// across enough shards to make contention rare (16 is the same default
// hashicorp/golang-lru v2 uses for its sharded LRU).
const NodeMirrorShards = 16

// NodeMirror 是节点状态的进程内只读镜像。
// 不变量(设计稿 Decision 2): 任何写入必须先经 Redis Lua 成功;LRU 永远是只读副本。
// generation 单调契约与 apply_decision.lua:25 对齐:
//
//	cur_gen > in_gen or (cur_gen==in_gen and cur_pri>in_pri) → ignored_stale
//
// M3 (2026-07-28): the underlying LRU is now a sharded array of size
// NodeMirrorShards. Each shard has its own sync.Mutex so Get / Peek /
// Update on distinct (tenant, credential_id, raw_model) keys — which the
// FilterAndScore hot path routinely fans out into — can proceed in
// parallel. Sharding is deterministic per cache key (FNV-1a) so the
// same key always lands in the same shard, preserving the
// (single-shard) generation-CAS invariant that applyToLRU relies on.
//
// Tenant scoping (afb13c9ea, 2026-07-28 audit): the cache key is the
// tenant-aware `nodeMirrorKeyForTenant(tenant, cred, raw)` so multi-tenant
// deployments don't see each other's mirror entries. Legacy callers
// without TenantID keep working via the `*("", credID, raw)` legacy
// overloads — those entries are scoped under the empty-tenant key,
// which never collides with a real tenant id.
type NodeMirror struct {
	shards  [NodeMirrorShards]*LRU[string, NodeView]
	softTTL time.Duration
	prefix  string
}

func NewNodeMirror(capacity int, softTTL time.Duration) *NodeMirror {
	return NewNodeMirrorWithPrefix(capacity, softTTL, "ursm:v2:")
}

// NewNodeMirrorWithPrefix constructs a mirror whose internal key namespace
// matches the configured URSM Redis prefix. This prefix is process-local cache
// namespacing only; Redis remains the authoritative store.
func NewNodeMirrorWithPrefix(capacity int, softTTL time.Duration, prefix string) *NodeMirror {
	if capacity <= 0 {
		capacity = 1
	}
	if prefix == "" {
		prefix = "ursm:v2:"
	}
	// Distribute total capacity evenly across shards. Round up per shard so
	// total >= requested capacity (overshoot is bounded by shard count).
	per := (capacity + NodeMirrorShards - 1) / NodeMirrorShards
	if per < 1 {
		per = 1
	}
	m := &NodeMirror{softTTL: softTTL, prefix: prefix}
	for i := 0; i < NodeMirrorShards; i++ {
		m.shards[i] = NewLRU[string, NodeView](per)
	}
	return m
}

// Prefix returns the Redis namespace mirrored by this process-local cache.
// It is exposed for diagnostics and contract tests; Redis remains authoritative.
func (m *NodeMirror) Prefix() string {
	if m == nil {
		return ""
	}
	return m.prefix
}

// shardOf returns a deterministic shard index for the given cache key.
// FNV-1a 64 is fast, allocation-free, and stable across processes (no rand
// seed) — same key always lands on the same shard so the per-shard CAS in
// applyToLRU is meaningful.
func shardOf(key string) int {
	const offset uint64 = 14695981039346656037
	const prime uint64 = 1099511628211
	h := offset
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= prime
	}
	return int(h % uint64(NodeMirrorShards))
}

func (m *NodeMirror) shard(key string) *LRU[string, NodeView] {
	if m == nil {
		return nil
	}
	return m.shards[shardOf(key)]
}

// applyToLRU 是唯一的 LRU 写入口,write-back 与 read-back 共用。
// 用 LRU.Update 在锁内原子执行单调比较 + 写入, 杜绝 Peek+Put 的 TOCTOU。
//
// 拒绝条件(对应 lua 的 ignored_stale):
//
//	incoming.gen < existing.gen                          → 拒绝
//	incoming.gen == existing.gen && incoming.pri <= pri  → 拒绝
//
// 其余情况接受(覆盖)。
func (m *NodeMirror) applyToLRU(v NodeView) {
	key := nodeMirrorKeyForTenantWithPrefix(m.prefix, v.TenantID, v.CredentialID, v.RawModel)
	v.softExpireAt = time.Now().Add(m.softTTL)
	l := m.shard(key)
	if l == nil {
		return
	}
	l.Update(key, func(old NodeView, exists bool) (NodeView, bool) {
		if !exists {
			return v, true // 首次写入,接受
		}
		if v.Generation < old.Generation {
			return v, false // 迟到旧 gen,拒绝
		}
		if v.Generation == old.Generation && v.SourcePriority < old.SourcePriority {
			return v, false // 同 gen 但 priority 不更高(严格小于,与 apply_decision.lua:25 对齐:相等时接受幂等刷新)
		}
		return v, true // 更新或更高 priority,接受
	})
}

// Get retains the legacy non-tenant lookup for tests and operator tooling.
// New callers should prefer GetForTenant.
func (m *NodeMirror) Get(credID int, raw string) (NodeView, bool) {
	return m.GetForTenant("", credID, raw)
}

// GetForTenant returns an unexpired tenant-scoped mirror entry.
// 软过期返回 miss(触发上层回源 Redis).
func (m *NodeMirror) GetForTenant(tenant string, credID int, raw string) (NodeView, bool) {
	if m == nil {
		return NodeView{}, false
	}
	key := nodeMirrorKeyForTenantWithPrefix(m.prefix, tenant, credID, raw)
	v, ok := m.shard(key).Get(key)
	if !ok {
		return NodeView{}, false
	}
	if time.Now().After(v.softExpireAt) {
		return NodeView{}, false
	}
	return v, true
}

// Peek 返回值但不提升 LRU 顺序(测试与观测用).
// 测试仍依赖非 tenant 形式 — 保留 overload 行为.
func (m *NodeMirror) Peek(credID int, raw string) (NodeView, bool) {
	return m.PeekForTenant("", credID, raw)
}

// PeekForTenant 是 GetForTenant 的不提升顺序对应物.
func (m *NodeMirror) PeekForTenant(tenant string, credID int, raw string) (NodeView, bool) {
	if m == nil {
		return NodeView{}, false
	}
	key := nodeMirrorKeyForTenantWithPrefix(m.prefix, tenant, credID, raw)
	return m.shard(key).Peek(key)
}

// InvalidateForTenant drops one cached node after a successful authoritative
// Redis transition. This is intentionally unconditional: Redis owns the
// generation and a local cache entry must never outlive an admin/request/probe
// write just because that write was initiated by this process.
func (m *NodeMirror) InvalidateForTenant(tenant string, credID int, raw string) bool {
	if m == nil {
		return false
	}
	key := nodeMirrorKeyForTenantWithPrefix(m.prefix, tenant, credID, raw)
	return m.shard(key).Delete(key)
}

// Invalidate retains the legacy non-tenant helper for operator tooling.
func (m *NodeMirror) Invalidate(credID int, raw string) bool {
	return m.InvalidateForTenant("", credID, raw)
}

// ApplyFromAPI backfills the mirror from an authoritative api.NodeView just
// read from Redis (M2). It is the read path's only write entrypoint;
// applyToLRU enforces the generation-monotonic contract so a stale Redis
// snapshot can never overwrite a newer LRU entry. Safe to call on a
// zero-value view (only Generation>0 writes pin an entry).
func (m *NodeMirror) ApplyFromAPI(v api.NodeView) {
	if m == nil {
		return
	}
	m.applyToLRU(NodeView{
		TenantID:       v.TenantID,
		CredentialID:   v.CredentialID,
		RawModel:       v.RawModel,
		Available:      v.Available,
		Reason:         v.Reason,
		Generation:     v.Generation,
		SourcePriority: v.SrcPriority,
		FailStreak:     v.FailStreak,
		CoolUntil:      v.CoolUntil,
		LatEWMA:        v.LatEWMA,
		SR5m:           v.SR5m,
		Samples5m:      v.Samples5m,
		HealthStatus:   v.HealthStatus,
		CachedAt:       time.Now(),
	})
}
