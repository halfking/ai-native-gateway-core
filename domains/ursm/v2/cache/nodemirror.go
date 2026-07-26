package cache

import (
	"time"
)

// NodeView 是节点状态的 LRU 镜像条目。字段是 domains/ursm/v2/api.NodeView
// 的子集(只取镜像需要的字段)。generation + source_priority 用于单调裁决。
type NodeView struct {
	CredentialID   int
	RawModel       string
	Available      bool
	Reason         string
	Generation     int64
	SourcePriority int
	FailStreak     int
	CoolUntil      time.Time
	softExpireAt   time.Time // 软过期点;超过后 Get 返回 miss
}

// NodeMirror 是节点状态的进程内只读镜像。
// 不变量(设计稿 Decision 2): 任何写入必须先经 Redis Lua 成功;LRU 永远是只读副本。
// generation 单调契约与 apply_decision.lua:25 对齐:
//   cur_gen > in_gen or (cur_gen==in_gen and cur_pri>in_pri) → ignored_stale
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
//   incoming.gen < existing.gen                          → 拒绝
//   incoming.gen == existing.gen && incoming.pri <= pri  → 拒绝
// 其余情况接受(覆盖)。
func (m *NodeMirror) applyToLRU(v NodeView) {
	key := nodeMirrorKey(v.CredentialID, v.RawModel)
	v.softExpireAt = time.Now().Add(m.softTTL)
	m.lru.Update(key, func(old NodeView, exists bool) (NodeView, bool) {
		if !exists {
			return v, true // 首次写入,接受
		}
		if v.Generation < old.Generation {
			return v, false // 迟到旧 gen,拒绝
		}
		if v.Generation == old.Generation && v.SourcePriority <= old.SourcePriority {
			return v, false // 同 gen 但 priority 不更高,拒绝
		}
		return v, true // 更新或更高 priority,接受
	})
}

// Get 返回未软过期的镜像条目。软过期返回 miss(触发上层回源 Redis)。
func (m *NodeMirror) Get(credID int, raw string) (NodeView, bool) {
	key := nodeMirrorKey(credID, raw)
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
