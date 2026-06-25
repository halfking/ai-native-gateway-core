// Package audit 实现请求审计领域 (Hook)。
// 阶段: PostResponse (异步批量写入)
package audit

import "time"

// Event 审计事件
type Event struct {
	RequestID  string
	TenantID   string
	SessionID  string
	Stage      string // "auth" / "routing" / "transform" / "response"
	Action     string // "allow" / "deny" / "modify"
	StatusCode int
	LatencyMs  int64
	Error      string
	Metadata   map[string]any
	CreatedAt  time.Time
}

// Sink 审计存储接口
type Sink interface {
	Write(events []*Event) error
	Close() error
}

// InMemorySink 内存 sink（用于测试）
type InMemorySink struct {
	events []*Event
}

// NewInMemorySink 创建内存 sink
func NewInMemorySink() *InMemorySink {
	return &InMemorySink{events: make([]*Event, 0)}
}

// Write 批量写入
func (s *InMemorySink) Write(events []*Event) error {
	s.events = append(s.events, events...)
	return nil
}

// Close 关闭
func (s *InMemorySink) Close() error { return nil }

// Events 返回所有事件（测试用，返回副本）
func (s *InMemorySink) Events() []*Event {
	out := make([]*Event, len(s.events))
	copy(out, s.events)
	return out
}
