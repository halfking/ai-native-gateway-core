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
	LatEWMA int
	SR5m    float64
	// CachedAt is when this entry was populated from Redis. Observability/
	// staleness hint (the soft-expire decision uses softExpireAt, not this).
	CachedAt     time.Time
	softExpireAt time.Time // 软过期点;超过后 Get 返回 miss
}

// NodeMirror 是节点状态的进程内只读镜像。
// 不变量(设计稿 Decision 2): 任何写入必须先经 Redis Lua 成功;LRU 永远是只读副本。
// generation 单调契约与 apply_decision.lua:25 对齐:
//
//	cur_gen > in_gen or (cur_gen==in_gen and cur_pri>in_pri) → ignored_stale
type NodeMirror struct {
	lru     *LRU[string, NodeView]
	softTTL time.Duration
}

func NewNodeMirror(capacity int, softTTL time.Duration) *NodeMirror {
	return &NodeMirror{lru: NewLRU[string, NodeView](capacity), softTTL: softTTL}
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
	key := nodeMirrorKeyForTenant(v.TenantID, v.CredentialID, v.RawModel)
	v.softExpireAt = time.Now().Add(m.softTTL)
	m.lru.Update(key, func(old NodeView, exists bool) (NodeView, bool) {
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
func (m *NodeMirror) Get(credID int, raw string) (NodeView, bool) {
	return m.GetForTenant("", credID, raw)
}

// GetForTenant returns an unexpired tenant-scoped mirror entry.
func (m *NodeMirror) GetForTenant(tenant string, credID int, raw string) (NodeView, bool) {
	key := nodeMirrorKeyForTenant(tenant, credID, raw)
	v, ok := m.lru.Get(key)
	if !ok {
		return NodeView{}, false
	}
	if time.Now().After(v.softExpireAt) {
		return NodeView{}, false
	}
	return v, true
}

// Peek 返回值但不提升 LRU 顺序(测试与观测用)。
func (m *NodeMirror) Peek(credID int, raw string) (NodeView, bool) {
	return m.lru.Peek(nodeMirrorKey(credID, raw))
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
		CachedAt:       time.Now(),
	})
}
