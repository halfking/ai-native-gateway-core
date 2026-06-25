// Package audit 实现请求审计领域 (Hook)。
// 阶段: PostResponse (异步批量写入)
//
// Event 与 Sink 接口在 audit.go 中定义（来自 audit/ 旧包迁移）。
// 本文件仅保留 InMemorySink 及其附加方法，适配 audit.go 中的统一 Sink 接口。
package audit

import "context"

// InMemorySink 内存 sink（用于测试）。
//
// 实现了 audit.Sink 接口的全部三个方法：Emit / Write / Close。
// 既支持 BatchWriter 的批量写入，也支持源审计代码的单条 Emit。
type InMemorySink struct {
	events []*Event
}

// NewInMemorySink 创建内存 sink
func NewInMemorySink() *InMemorySink {
	return &InMemorySink{events: make([]*Event, 0)}
}

// Write 批量写入（适配 BatchWriter 调用方式）
func (s *InMemorySink) Write(events []*Event) error {
	s.events = append(s.events, events...)
	return nil
}

// Emit 单条写入（适配源审计代码 EmitCredentialSwitch 等）
func (s *InMemorySink) Emit(_ context.Context, event Event) {
	s.events = append(s.events, &event)
}

// Close 关闭
func (s *InMemorySink) Close() error { return nil }

// Events 返回所有事件（测试用，返回副本）
func (s *InMemorySink) Events() []*Event {
	out := make([]*Event, len(s.events))
	copy(out, s.events)
	return out
}
