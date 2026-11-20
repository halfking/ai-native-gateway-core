package analysis

import (
	"context"
	"time"
)

// EventType 异步分析事件类型。
//
// 与 domains/eventbus.MemoryBus 的事件类型正交；本包事件专供 analysis 域消费。
type EventType string

const (
	EventRequestCompleted EventType = "request.completed"
	EventSessionClosed    EventType = "session.closed"
	// EventSessionClosedSummary 是会话关闭事件的**摘要链专用**扇出类型
	// （2026-11-20 会话分析闭环审计轮）。背景：bus 是「一行事件一个消费者」
	// 的认领模型（FOR UPDATE SKIP LOCKED + processed_at 终态），而
	// clientprofile.ProfileWorker 也订阅 session.closed（画像「总会话数」）
	// ⇒ 同一行事件被画像 worker 先认领并标记终态时，摘要链永远看不到
	// （252 活体实录：注入探针 worker=client_profile_worker attempts=0，
	// 摘要未生成）。扇出改为两行：本类型行归摘要链独占，legacy
	// session.closed 行归画像 worker，互不抢认领。
	EventSessionClosedSummary EventType = "session.closed.summary"
	EventToolCompleted        EventType = "tool.completed"
	EventApprovalDecided      EventType = "approval.decided"
	EventFailureDetected      EventType = "failure.detected"
)

// AnalysisEvent 异步层统一事件。
//
// Payload 是与 Type 对应的强类型负载；消费者按 Type 做 type switch。
// 本包不规定 Payload 的具体类型，由发布方在 domains/analysis/bus 中定义。
type AnalysisEvent struct {
	EventID    string
	Type       EventType
	TenantID   string
	SessionID  string
	RequestID  string
	OccurredAt time.Time
	Payload    any
	ClaimID    string
}

// Worker 异步 worker 接口。
//
// 由 domains/analysis/workers/* 实现；bus 启动时按 SubscribedTypes 路由。
type Worker interface {
	Name() string
	SubscribedTypes() []EventType
	Handle(ctx context.Context, evt AnalysisEvent) error
}
