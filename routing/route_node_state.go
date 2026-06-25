package routing

import (
	"sync"
	"time"
)

// RouteNodeState 描述一个路由节点（credential + 模型组合）的健康状态。
//
// V3.1 设计要点：
//   - 状态维度是 (CredentialID, Model)，**不**依附于会话
//   - 失败计数反映节点本身的健康度，而非使用它的会话的健康度
//   - 同一会话切换到另一个 credential，旧 credential 上的状态保留
//   - 同一 credential 切到不同 model，是两个不同的 RouteNodeState
//   - 同会话切模型，旧 model 状态保留；只清空 session_pref
//
// 字段语义详见 docs/2026-06-26-session-routing-redesign.md §3.5。
type RouteNodeState struct {
	// CredentialID 是 credentials 表的主键。
	CredentialID int `json:"credential_id"`
	// Model 是 credential 匹配后的模型名（cand.RawModel），不是客户端请求模型。
	// 同 credential 对不同客户端模型暴露为不同 model 时，各自独立。
	Model string `json:"model"`

	// SuccessCount / FailureCount 是节点生命周期的累计计数。
	SuccessCount int64 `json:"success_count"`
	FailureCount int64 `json:"failure_count"`

	// SlideWindow 是 5 分钟滑动窗口内的请求记录（仅保留 kind=credential-level 的失败
	// 和所有成功）。每次 IsUsable / ConsecutiveFailureStreak 调用都会 prune。
	SlideWindow []RouteNodeRecord `json:"slide_window"`

	// LastSuccessAt / LastFailureAt 用于监控和 UI 展示。
	LastSuccessAt time.Time `json:"last_success_at"`
	LastFailureAt time.Time `json:"last_failure_at"`

	// Disabled 表示节点已被会话级禁用，DisabledUntil 之前 PlanCandidates 会跳过它。
	// 连续 3 次失败触发；冷却到期自动恢复。
	Disabled       bool      `json:"disabled"`
	DisabledUntil  time.Time `json:"disabled_until"`
	DisabledReason string    `json:"disabled_reason,omitempty"`
}

// RouteNodeRecord 是滑动窗口中的一条记录。
type RouteNodeRecord struct {
	RequestID string    `json:"request_id"`
	Success   bool      `json:"success"`
	ErrorKind string    `json:"error_kind,omitempty"` // 仅失败时有值
	Timestamp time.Time `json:"timestamp"`
}

// 默认参数（V3 校准），可以从 config 注入，但这里给出编译期常量便于单测。
const (
	DefaultRouteNodeWindowSeconds    = 300  // 5 分钟滑动窗口
	DefaultRouteNodeFailStreakLimit  = 3    // 连续失败阈值
	DefaultRouteNodeDisabledCooldown = 300  // 禁用后冷却
)

// RouteNodeConfig 是 RouteNodeState 的运行参数，允许外部注入覆盖默认值。
type RouteNodeConfig struct {
	WindowSeconds    time.Duration // 滑动窗口大小，默认 5min
	FailStreakLimit  int           // 连续失败阈值，默认 3
	DisabledCooldown time.Duration // 禁用后冷却时间，默认 5min
}

// DefaultRouteNodeConfig 返回 V3 校准后的默认参数。
func DefaultRouteNodeConfig() RouteNodeConfig {
	return RouteNodeConfig{
		WindowSeconds:    DefaultRouteNodeWindowSeconds * time.Second,
		FailStreakLimit:  DefaultRouteNodeFailStreakLimit,
		DisabledCooldown: DefaultRouteNodeDisabledCooldown * time.Second,
	}
}

// PruneOldRecords 删除超过窗口的旧记录。
// 注意：调用方需持有锁（或确保 state 不被并发修改）。
func (n *RouteNodeState) PruneOldRecords(now time.Time, window time.Duration) {
	if n == nil || window <= 0 {
		return
	}
	cutoff := now.Add(-window)
	i := 0
	for i < len(n.SlideWindow) {
		if n.SlideWindow[i].Timestamp.Before(cutoff) {
			i++
			continue
		}
		break
	}
	if i > 0 {
		n.SlideWindow = append([]RouteNodeRecord(nil), n.SlideWindow[i:]...)
	}
}

// ConsecutiveFailureStreak 返回滑动窗口末尾的连续失败次数（最新 N 条均为失败）。
// 调用前会先 prune 过期记录。
func (n *RouteNodeState) ConsecutiveFailureStreak(now time.Time, cfg RouteNodeConfig) int {
	if n == nil {
		return 0
	}
	n.PruneOldRecords(now, cfg.WindowSeconds)
	streak := 0
	for i := len(n.SlideWindow) - 1; i >= 0; i-- {
		if !n.SlideWindow[i].Success {
			streak++
		} else {
			break
		}
	}
	return streak
}

// IsUsable 判定该节点当前是否可用于路由选择。
//
// 规则（V3.1）：
//   1. Disabled 且未到 DisabledUntil → 不可用
//   2. Disabled 且已到 DisabledUntil → 自动恢复（重置计数和状态）
//   3. 滑动窗口末尾连续失败 ≥ FailStreakLimit → 不可用（即使未显式 Disabled）
//   4. 否则可用
func (n *RouteNodeState) IsUsable(now time.Time, cfg RouteNodeConfig) bool {
	if n == nil {
		return true // nil state 表示无记录，视作可用
	}

	// 冷却恢复
	if n.Disabled && !now.Before(n.DisabledUntil) {
		n.Disabled = false
		n.DisabledUntil = time.Time{}
		n.DisabledReason = ""
		n.FailureCount = 0
	}

	// 仍在冷却中
	if n.Disabled && now.Before(n.DisabledUntil) {
		return false
	}

	// 检查连续失败
	return n.ConsecutiveFailureStreak(now, cfg) < cfg.FailStreakLimit
}

// RecordSuccess 记录一次成功调用。
//
// 行为：
//   - 增加 SuccessCount / LastSuccessAt
//   - 如果之前 Disabled（异常情况）→ 自动恢复
//   - SlideWindow 追加成功记录
//
// 调用方需要持有锁（或 RouteNodeStore 的写锁）。
func (n *RouteNodeState) RecordSuccess(now time.Time, requestID string, cfg RouteNodeConfig) {
	if n == nil {
		return
	}
	n.SuccessCount++
	n.LastSuccessAt = now
	// 成功调用：自动从 Disabled 恢复
	if n.Disabled {
		n.Disabled = false
		n.DisabledUntil = time.Time{}
		n.DisabledReason = ""
		n.FailureCount = 0
	}
	n.SlideWindow = append(n.SlideWindow, RouteNodeRecord{
		RequestID: requestID,
		Success:   true,
		Timestamp: now,
	})
	n.PruneOldRecords(now, cfg.WindowSeconds)
}

// RecordFailure 记录一次 credential-level 失败。
//
// 行为：
//   - 增加 FailureCount / LastFailureAt
//   - SlideWindow 追加失败记录
//   - 如果连续失败 ≥ FailStreakLimit → 触发 Disabled + DisabledUntil
//
// 调用方需要持有锁（或 RouteNodeStore 的写锁）。
//
// 返回值：是否触发了"进入 Disabled"状态（true=刚禁用）。
func (n *RouteNodeState) RecordFailure(now time.Time, requestID string, errorKind string, cfg RouteNodeConfig) (justDisabled bool) {
	if n == nil {
		return false
	}
	n.FailureCount++
	n.LastFailureAt = now
	n.SlideWindow = append(n.SlideWindow, RouteNodeRecord{
		RequestID: requestID,
		Success:   false,
		ErrorKind: errorKind,
		Timestamp: now,
	})
	n.PruneOldRecords(now, cfg.WindowSeconds)

	streak := n.ConsecutiveFailureStreak(now, cfg)
	if streak >= cfg.FailStreakLimit && !n.Disabled {
		n.Disabled = true
		n.DisabledUntil = now.Add(cfg.DisabledCooldown)
		n.DisabledReason = "consecutive_failures"
		return true
	}
	return false
}

// routeNodeStatePool 减少频繁创建 RouteNodeState 时的 GC 压力。
// sync.Pool 中的对象可能被并发复用，使用前需要重置。
var routeNodeStatePool = sync.Pool{
	New: func() any {
		return &RouteNodeState{
			SlideWindow: make([]RouteNodeRecord, 0, 16),
		}
	},
}

// acquireRouteNodeState 从池中获取一个已重置的 RouteNodeState。
func acquireRouteNodeState(credID int, model string) *RouteNodeState {
	s := routeNodeStatePool.Get().(*RouteNodeState)
	s.CredentialID = credID
	s.Model = model
	s.SuccessCount = 0
	s.FailureCount = 0
	s.SlideWindow = s.SlideWindow[:0]
	s.LastSuccessAt = time.Time{}
	s.LastFailureAt = time.Time{}
	s.Disabled = false
	s.DisabledUntil = time.Time{}
	s.DisabledReason = ""
	return s
}

// releaseRouteNodeState 归还到池中。
// 注意：归还前应该清空引用，避免外部继续持有切片。
func releaseRouteNodeState(s *RouteNodeState) {
	if s == nil {
		return
	}
	// 清空可能的指针引用（当前结构没有指针字段，但保留扩展性）
	s.SlideWindow = s.SlideWindow[:0]
	routeNodeStatePool.Put(s)
}