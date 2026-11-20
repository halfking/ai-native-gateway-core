package main

// 2026-11-20 会话分析闭环审计轮：session.closed 事件桥接钉测。
//
// ★ 这道门存在的理由：analysis.EventSessionClosed 此前全仓零生产发布者
//   （唯一 emitter 零调用）⇒ 三条订阅 hook 空转。桥接是本轮接上的「唯一
//   事件源」，它的形状错误（类型/租户/会话键/确定性 EventID）不会让任何
//   现有测试变红——只会让闭链在生产上静默空转或重复消费。

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/analysis/bus"
)

type fakePublisher struct {
	events []analysis.AnalysisEvent
}

func (f *fakePublisher) Publish(_ context.Context, evt analysis.AnalysisEvent) error {
	f.events = append(f.events, evt)
	return nil
}
func (f *fakePublisher) Close() error { return nil }

var _ bus.Publisher = (*fakePublisher)(nil)

func TestSessionClosedEventBridgeEventShape(t *testing.T) {
	fake := &fakePublisher{}
	bridge := sessionClosedEventBridge{pub: fake}

	lastActive := time.Unix(1700000000, 0)
	err := bridge.PublishSessionClosed(context.Background(),
		"tenant-a", "gw-sess-1", "idle", lastActive, 35*time.Minute)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(fake.events) != 1 {
		t.Fatalf("published %d events, want 1", len(fake.events))
	}
	evt := fake.events[0]
	if evt.Type != analysis.EventSessionClosed {
		t.Fatalf("event type = %q, want %q（类型错 ⇒ 订阅方永远收不到）", evt.Type, analysis.EventSessionClosed)
	}
	if evt.TenantID != "tenant-a" || evt.SessionID != "gw-sess-1" {
		t.Fatalf("tenant/session 键错误: %q/%q（worker 靠它提取 session_key）", evt.TenantID, evt.SessionID)
	}
	payload, ok := evt.Payload.(map[string]any)
	if !ok || payload["reason"] != "idle" {
		t.Fatalf("payload 缺 reason（审计面需要「为什么关」）")
	}

	// 确定性 EventID：同 (tenant, session, lastActive) 两次发布必须同 ID
	// （多实例并发扫描 ⇒ PGPublisher ON CONFLICT (event_id) DO NOTHING 去重）。
	fake2 := &fakePublisher{}
	bridge2 := sessionClosedEventBridge{pub: fake2}
	_ = bridge2.PublishSessionClosed(context.Background(),
		"tenant-a", "gw-sess-1", "idle", lastActive, 36*time.Minute)
	if fake.events[0].EventID == "" || fake.events[0].EventID != fake2.events[0].EventID {
		t.Fatalf("EventID 不确定（同会话同 lastActive 两次发布 ID 不同）⇒ 多实例去重失效")
	}
	// 不同 lastActive ⇒ 不同 ID（同一会话多次开合各自成事件）。
	_ = bridge2.PublishSessionClosed(context.Background(),
		"tenant-a", "gw-sess-1", "idle", lastActive.Add(time.Hour), 2*time.Hour)
	if fake2.events[0].EventID == fake2.events[1].EventID {
		t.Fatalf("不同 lastActive 产出同 EventID ⇒ 第二次开合的关闭会被 ON CONFLICT 吃掉")
	}
}

func TestSessionAnalyticsAutoResolverPassesThrough(t *testing.T) {
	res := sessionAnalyticsAutoResolver{}
	if got := res.ResolveModel("summary"); got != "auto" {
		t.Fatalf("auto 应透传为字面 \"auto\"（网关自路由），got %q", got)
	}
}
