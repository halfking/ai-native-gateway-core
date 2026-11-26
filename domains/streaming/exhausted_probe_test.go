package streaming

// exhausted_probe_test.go —— 需求驱动复探钩子的钉测。
//
// 钉的是**接线**（不是「函数写对」）：钩子必须在候选解析之后被调用一次，
// 且必须异步、不阻塞、且不受客户端取消影响。

import (
	"context"
	"testing"
	"time"
)

func TestNotifyExhaustedNodes_InvokesTriggerWithModelAndTenant(t *testing.T) {
	h := &ChatHandler{}
	called := make(chan [2]string, 1)
	h.SetExhaustedNodeProbeTrigger(func(ctx context.Context, model, tenantID string) {
		called <- [2]string{model, tenantID}
	})
	h.notifyExhaustedNodes(context.Background(), "claude-sonnet-5-5", "default")
	select {
	case got := <-called:
		if got[0] != "claude-sonnet-5-5" {
			t.Errorf("model = %q, want claude-sonnet-5-5", got[0])
		}
		if got[1] != "default" {
			t.Errorf("tenant = %q, want default", got[1])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("需求驱动复探钩子未被调用")
	}
}

// TestNotifyExhaustedNodes_NilTriggerIsInert 未接线 ⇒ 静默无动作（行为与
// 上线前逐字节相同）。
func TestNotifyExhaustedNodes_NilTriggerIsInert(t *testing.T) {
	h := &ChatHandler{}
	// 不 panic 即为通过（没有钩子 ⇒ 直接返回）。
	h.notifyExhaustedNodes(context.Background(), "m", "default")
}

// TestNotifyExhaustedNodes_EmptyModelSkips 客户端没给模型 ⇒ 不触发（避免
// 拿空模型去扫库）。
func TestNotifyExhaustedNodes_EmptyModelSkips(t *testing.T) {
	h := &ChatHandler{}
	called := make(chan struct{}, 1)
	h.SetExhaustedNodeProbeTrigger(func(ctx context.Context, model, tenantID string) {
		called <- struct{}{}
	})
	h.notifyExhaustedNodes(context.Background(), "", "default")
	select {
	case <-called:
		t.Fatal("空模型不该触发复探")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestNotifyExhaustedNodes_SurvivesClientCancel 客户端断开（请求 ctx 被取消）
// **不得**取消后台探测 ⇒ 钩子拿到的 ctx 仍可用。
func TestNotifyExhaustedNodes_SurvivesClientCancel(t *testing.T) {
	h := &ChatHandler{}
	got := make(chan error, 1)
	h.SetExhaustedNodeProbeTrigger(func(ctx context.Context, model, tenantID string) {
		got <- ctx.Err()
	})
	reqCtx, cancel := context.WithCancel(context.Background())
	h.notifyExhaustedNodes(reqCtx, "m", "default")
	cancel() // 客户端立刻断开
	select {
	case err := <-got:
		if err != nil {
			t.Errorf("钩子收到的 ctx 已取消（%v）—— 后台探测会被客户端断开带死", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("需求驱动复探钩子未被调用")
	}
}
