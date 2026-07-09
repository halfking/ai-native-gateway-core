// domains/hooks/session-inspector/event.go
//
// EventBus 事件类型定义。
// 供 InspectorHook 在检测到 finding / 触发回收时发布到 eventbus.MemoryBus，
// 由 notification / IM / webhooks 等子系统订阅消费。
//
// 事件命名约定：SessionInspector<EventKind>Event
//  - Type() 统一返回 "session_inspector.<kind>"，便于订阅者过滤。
package sessioninspector

import "time"

// SessionInspectorFindingEvent 单次 finding 事件。
//
// Inspector 在每次 Execute 命中 finding 时发布，订阅者可据此：
//   - 推送到 IM（feishu / wechat）
//   - POST 到 webhook
//   - 写入审计日志
//   - 触发自动回收（与 idle.recycle_action=notify_only 配合）
//
// 字段说明：
//   - EventTime: 事件创建时间（独立于 Finding.DetectedAt，后者由 inspector 填入）
//   - Finding.DetectedAt: 命中时刻（来自 inspector）
type SessionInspectorFindingEvent struct {
	SessionID string         `json:"session_id"`
	TenantID  string         `json:"tenant_id"`
	Finding   *Finding       `json:"finding"`
	Source    string         `json:"source"` // "hook" | "worker"
	EventTime time.Time      `json:"event_time"`
	Extra     map[string]any `json:"extra,omitempty"`
}

// Type 实现 eventbus.Event。
func (e *SessionInspectorFindingEvent) Type() string {
	return "session_inspector.finding"
}

// Timestamp 实现 eventbus.Event。
func (e *SessionInspectorFindingEvent) Timestamp() time.Time {
	if e.EventTime.IsZero() {
		return time.Now()
	}
	return e.EventTime
}

// SessionInspectorRecycleEvent 会话回收事件（仅用于 idle.recycle_action=notify_only）。
//
// 后台 worker 检测到不活跃会话但配置为"仅通知"时发布此事件。
// 软关闭模式下，状态变更由 worker 直接 UPDATE 到 session_dim，
// 同时也会发布此事件以通知管理员（保持行为一致）。
type SessionInspectorRecycleEvent struct {
	SessionID    string    `json:"session_id"`
	TenantID     string    `json:"tenant_id"`
	Action       string    `json:"action"`         // "soft_close" | "notify_only"
	Reason       string    `json:"reason"`         // "idle_timeout" | "absolute_max_lifetime" | "manual"
	LastActiveAt time.Time `json:"last_active_at"` // 当时会话的最后活跃时间
	IdleFor      string    `json:"idle_for"`       // 人类可读：例如 "35m"
	Source       string    `json:"source"`         // "worker" | "admin"
	Operator     string    `json:"operator,omitempty"` // 仅 manual 时有值
	EventTime    time.Time `json:"event_time"`
}

// Type 实现 eventbus.Event。
func (e *SessionInspectorRecycleEvent) Type() string {
	return "session_inspector.recycle"
}

// Timestamp 实现 eventbus.Event。
func (e *SessionInspectorRecycleEvent) Timestamp() time.Time {
	if e.EventTime.IsZero() {
		return time.Now()
	}
	return e.EventTime
}

// SessionInspectorStatsEvent 平台级统计事件（低频、用于仪表盘同步）。
//
// 由 SessionLifecycleWorker 在每次 sweep 完成后发布，
// 包含当前活跃/空闲/关闭会话计数与本轮回收数。
// 订阅者可写入 Redis 缓存供 admin UI 读取。
type SessionInspectorStatsEvent struct {
	ActiveSessions  int       `json:"active_sessions"`
	IdleSessions    int       `json:"idle_sessions"`
	ClosedSessions  int       `json:"closed_sessions"`
	RecycledThisRun int       `json:"recycled_this_run"`
	FindingsThisRun int       `json:"findings_this_run"`
	SweepDurationMs int64     `json:"sweep_duration_ms"`
	EventTime       time.Time `json:"event_time"`
}

// Type 实现 eventbus.Event。
func (e *SessionInspectorStatsEvent) Type() string {
	return "session_inspector.stats"
}

// Timestamp 实现 eventbus.Event。
func (e *SessionInspectorStatsEvent) Timestamp() time.Time {
	if e.EventTime.IsZero() {
		return time.Now()
	}
	return e.EventTime
}
