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
	EventToolCompleted    EventType = "tool.completed"
	EventApprovalDecided  EventType = "approval.decided"
	EventFailureDetected  EventType = "failure.detected"
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
}

// Worker 异步 worker 接口。
//
// 由 domains/analysis/workers/* 实现；bus 启动时按 SubscribedTypes 路由。
type Worker interface {
	Name() string
	SubscribedTypes() []EventType
	Handle(ctx context.Context, evt AnalysisEvent) error
}
