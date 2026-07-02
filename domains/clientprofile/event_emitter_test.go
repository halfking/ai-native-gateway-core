package clientprofile

import (
	"context"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// fakeBus 记录已发布的 events，模拟 EventBus。
type fakeBus struct {
	events []analysis.AnalysisEvent
	err    error
}

func (f *fakeBus) Publish(_ context.Context, evt analysis.AnalysisEvent) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, evt)
	return nil
}

// payloadOf 帮助测试把 evt.Payload 断言成 map[string]any 并取得。
func payloadOf(t *testing.T, evt analysis.AnalysisEvent) map[string]any {
	t.Helper()
	m, ok := evt.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload is not map[string]any: %T", evt.Payload)
	}
	return m
}

func TestEventEmitter_EmitSessionStarted(t *testing.T) {
	bus := &fakeBus{}
	e := NewEventEmitter(bus, nil)
	sc := newTestSessionContext()
	if err := e.EmitSessionStarted(context.Background(), sc, "hash-1"); err != nil {
		t.Fatalf("EmitSessionStarted: %v", err)
	}
	if len(bus.events) != 1 {
		t.Fatalf("events = %d, want 1", len(bus.events))
	}
	evt := bus.events[0]
	if evt.Type != analysis.EventSessionClosed {
		t.Errorf("type = %s, want session.closed", evt.Type)
	}
	if evt.TenantID != sc.TenantID {
		t.Errorf("tenant_id mismatch: %s vs %s", evt.TenantID, sc.TenantID)
	}
	if got := payloadOf(t, evt)["identity_hash"]; got != "hash-1" {
		t.Errorf("identity_hash = %v", got)
	}
}

func TestEventEmitter_EmitRequestCompleted(t *testing.T) {
	bus := &fakeBus{}
	e := NewEventEmitter(bus, nil)
	sc := newTestSessionContext()
	if err := e.EmitRequestCompleted(context.Background(), sc, "hash-1", true, 100, 250); err != nil {
		t.Fatalf("EmitRequestCompleted: %v", err)
	}
	evt := bus.events[0]
	if evt.Type != analysis.EventRequestCompleted {
		t.Errorf("type = %s", evt.Type)
	}
	p := payloadOf(t, evt)
	if p["tokens_used"] != 100 {
		t.Errorf("tokens_used = %v", p["tokens_used"])
	}
	if p["latency_ms"] != int64(250) {
		t.Errorf("latency_ms = %v", p["latency_ms"])
	}
	if p["success"] != true {
		t.Errorf("success = %v", p["success"])
	}
}

func TestEventEmitter_EmitApprovalDecided(t *testing.T) {
	bus := &fakeBus{}
	e := NewEventEmitter(bus, nil)
	sc := newTestSessionContext()
	if err := e.EmitApprovalDecided(context.Background(), sc, "hash-1", true, "approval-1"); err != nil {
		t.Fatalf("EmitApprovalDecided: %v", err)
	}
	evt := bus.events[0]
	if evt.Type != analysis.EventApprovalDecided {
		t.Errorf("type = %s", evt.Type)
	}
	p := payloadOf(t, evt)
	if p["approved"] != true {
		t.Errorf("approved = %v", p["approved"])
	}
	if p["approval_id"] != "approval-1" {
		t.Errorf("approval_id = %v", p["approval_id"])
	}
}

func TestEventEmitter_EmitFailureDetected(t *testing.T) {
	bus := &fakeBus{}
	e := NewEventEmitter(bus, nil)
	sc := newTestSessionContext()
	if err := e.EmitFailureDetected(context.Background(), sc, "hash-1", "upstream timeout"); err != nil {
		t.Fatalf("EmitFailureDetected: %v", err)
	}
	evt := bus.events[0]
	if evt.Type != analysis.EventFailureDetected {
		t.Errorf("type = %s", evt.Type)
	}
	p := payloadOf(t, evt)
	if p["success"] != false {
		t.Errorf("success = %v, want false", p["success"])
	}
	if p["reason"] != "upstream timeout" {
		t.Errorf("reason = %v", p["reason"])
	}
}

func TestEventEmitter_EmitToolCompleted(t *testing.T) {
	bus := &fakeBus{}
	e := NewEventEmitter(bus, nil)
	sc := newTestSessionContext()
	if err := e.EmitToolCompleted(context.Background(), sc, "hash-1", "web_search", true); err != nil {
		t.Fatalf("EmitToolCompleted: %v", err)
	}
	evt := bus.events[0]
	if evt.Type != analysis.EventToolCompleted {
		t.Errorf("type = %s", evt.Type)
	}
	if payloadOf(t, evt)["tool_name"] != "web_search" {
		t.Errorf("tool_name = %v", payloadOf(t, evt)["tool_name"])
	}
}

func TestEventEmitter_NilBus(t *testing.T) {
	e := NewEventEmitter(nil, nil)
	sc := newTestSessionContext()
	if err := e.EmitSessionStarted(context.Background(), sc, "h"); err != nil {
		t.Errorf("nil bus EmitSessionStarted: %v", err)
	}
	if err := e.EmitRequestCompleted(context.Background(), sc, "h", true, 1, 1); err != nil {
		t.Errorf("nil bus EmitRequestCompleted: %v", err)
	}
}

func TestEventEmitter_NilSessionContext(t *testing.T) {
	bus := &fakeBus{}
	e := NewEventEmitter(bus, nil)
	if err := e.EmitSessionStarted(context.Background(), nil, "h"); err != nil {
		t.Errorf("nil sc: %v", err)
	}
	if len(bus.events) != 0 {
		t.Errorf("nil sc should not publish: %d events", len(bus.events))
	}
}

func TestEventEmitter_PublishError(t *testing.T) {
	bus := &fakeBus{err: errors.New("bus down")}
	e := NewEventEmitter(bus, nil)
	sc := newTestSessionContext()
	err := e.EmitRequestCompleted(context.Background(), sc, "h", true, 1, 1)
	if err == nil {
		t.Fatal("expected publish error")
	}
}

func TestInferTaskType_Code(t *testing.T) {
	sc := newTestSessionContext()
	sc.ClientIR = newTestIR("write a function to do this", "sure, here is the code")
	got := InferTaskType(sc)
	if got != TaskTypeCode {
		t.Errorf("InferTaskType = %s, want code", got)
	}
}

func TestInferTaskType_Chat(t *testing.T) {
	sc := newTestSessionContext()
	sc.ClientIR = newTestIR("hello there", "hi friend")
	got := InferTaskType(sc)
	if got != TaskTypeChat {
		t.Errorf("InferTaskType = %s, want chat", got)
	}
}

func TestInferTaskType_Reasoning(t *testing.T) {
	sc := newTestSessionContext()
	sc.ClientIR = newTestIR("explain why this works", "because the protocol requires it")
	got := InferTaskType(sc)
	if got != TaskTypeReasoning {
		t.Errorf("InferTaskType = %s, want reasoning", got)
	}
}

func TestInferTaskType_Unknown(t *testing.T) {
	sc := newTestSessionContext()
	sc.ClientIR = newTestIR("foo bar baz", "qux")
	got := InferTaskType(sc)
	if got != TaskTypeUnknown {
		t.Errorf("InferTaskType = %s, want unknown", got)
	}
}

func TestInferTaskType_NilIR(t *testing.T) {
	sc := newTestSessionContext()
	sc.ClientIR = nil
	if got := InferTaskType(sc); got != TaskTypeUnknown {
		t.Errorf("nil IR should yield unknown, got %s", got)
	}
}
