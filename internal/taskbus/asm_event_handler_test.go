// asm_event_handler_test.go — asm.event.v1 真实消费 handler 的单测
// （会话治理方案 2026-11 P1-1 第一步）。
package taskbus

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/outbox"
)

// fakeASMDeliverer 记录投递请求；可注入错误模拟 ASM 侧失败。
type fakeASMDeliverer struct {
	mu        sync.Mutex
	delivered []outbox.EventEnvelope
	err       error
}

func (f *fakeASMDeliverer) Deliver(_ context.Context, env outbox.EventEnvelope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delivered = append(f.delivered, env)
	return f.err
}

func (f *fakeASMDeliverer) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.delivered)
}

func testEnvelope() (outbox.EventEnvelope, []byte) {
	env, err := outbox.BuildSessionOpenedEventV1("tenant-1", "sess-1", "user-1")
	if err != nil {
		panic(err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		panic(err)
	}
	return env, raw
}

func TestASMEventBridgeHandler_DeliversEnvelope(t *testing.T) {
	env, raw := testEnvelope()
	fake := &fakeASMDeliverer{}
	h := NewASMEventBridgeHandler(fake)

	task := Task{ID: 7, TaskType: TaskTypeASMEventV1, IdempotencyKey: env.EventID, Payload: raw}
	if err := h.Handle(context.Background(), task); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if fake.calls() != 1 {
		t.Fatalf("deliver calls = %d, want 1", fake.calls())
	}
	got := fake.delivered[0]
	if got.EventID != env.EventID || got.EventType != "session.opened.v1" ||
		got.TenantID != "tenant-1" || got.SessionID != "sess-1" {
		t.Fatalf("delivered envelope = %+v", got)
	}
	if len(got.Payload) == 0 || got.Payload["session_id"] != "sess-1" {
		t.Fatalf("payload not restored: %+v", got.Payload)
	}
}

func TestASMEventBridgeHandler_PropagatesDeliveryError(t *testing.T) {
	_, raw := testEnvelope()
	boom := errors.New("asm 503 event_retryable")
	h := NewASMEventBridgeHandler(&fakeASMDeliverer{err: boom})

	err := h.Handle(context.Background(), Task{ID: 1, Payload: raw})
	if !errors.Is(err, boom) {
		t.Fatalf("want delivery error propagated (dispatcher retries own it), got: %v", err)
	}
}

func TestASMEventBridgeHandler_RejectsMalformedPayload(t *testing.T) {
	fake := &fakeASMDeliverer{}
	h := NewASMEventBridgeHandler(fake)

	if err := h.Handle(context.Background(), Task{ID: 2, Payload: []byte("{not-json")}); err == nil {
		t.Fatal("want decode error for malformed payload")
	}
	// 空 envelope（缺 event_id/event_type）也要拒绝。
	if err := h.Handle(context.Background(), Task{ID: 3, Payload: []byte(`{"event_id":"","event_type":""}`)}); err == nil {
		t.Fatal("want error for empty envelope identity")
	}
	if fake.calls() != 0 {
		t.Fatalf("deliverer must not be called for malformed tasks, calls = %d", fake.calls())
	}
}

func TestRegisterASMTenant_RealAndPlaceholder(t *testing.T) {
	// 真实登记：端到端过 MemoryStore → Dispatcher → handler → deliverer。
	_, raw := testEnvelope()
	fake := &fakeASMDeliverer{}
	store := NewMemoryStore()
	r := NewRegistry()
	RegisterASMTenant(r, fake)
	bus := NewBus(store, r)

	if err := bus.Enqueue(context.Background(), TaskTypeASMEventV1, "evt-1", 1, raw); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := bus.DispatchOnce(context.Background()); err != nil {
		t.Fatalf("DispatchOnce: %v", err)
	}
	if fake.calls() != 1 {
		t.Fatalf("end-to-end deliver calls = %d, want 1", fake.calls())
	}

	// 占位登记：nil deliverer 时 handler 存在但只证明类型有消费者。
	r2 := NewRegistry()
	RegisterASMTenantPlaceholder(r2)
	if !r2.HasHandler(TaskTypeASMEventV1) {
		t.Fatal("placeholder registration must keep the type occupied")
	}
	h, _ := r2.Handler(TaskTypeASMEventV1)
	if err := h.Handle(context.Background(), Task{ID: 9, Payload: raw}); err != nil {
		t.Fatalf("placeholder handler must be a no-op, got: %v", err)
	}
}

// 双写桥语义预告（平迁第 2 步的执行体已在位）：同 idempotency_key 的
// 重复入队在 Store 层去重——at-least-once 下的重复投递由 ASM event_id
// 幂等收敛，与 outbox Dispatcher 的语义一致。
func TestASMEventBridge_IdempotencyKeyDedupAtStore(t *testing.T) {
	_, raw := testEnvelope()
	fake := &fakeASMDeliverer{}
	r := NewRegistry()
	RegisterASMTenant(r, fake)
	bus := NewBus(NewMemoryStore(), r)

	key := "evt-dedup-1"
	for i := 0; i < 2; i++ {
		if err := bus.Enqueue(context.Background(), TaskTypeASMEventV1, key, 1, raw); err != nil {
			t.Fatalf("Enqueue #%d: %v", i+1, err)
		}
	}
	if _, err := bus.DispatchOnce(context.Background()); err != nil {
		t.Fatalf("DispatchOnce: %v", err)
	}
	if fake.calls() != 1 {
		t.Fatalf("deduped tasks delivered = %d, want 1", fake.calls())
	}
	_ = time.Now()
}
