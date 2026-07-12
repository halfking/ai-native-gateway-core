package sessionforensics_test

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionforensics"
)

func TestAutoSummaryHook_NilSafe(t *testing.T) {
	var h *sessionforensics.AutoSummaryHook
	if h.Enqueue(context.Background(), "gw_x", "msg") {
		t.Error("nil hook should return false")
	}
	_ = h // avoid unused
}

func TestAutoSummaryHook_BasicEnqueue(t *testing.T) {
	svc := sessionforensics.NewService(sessionforensics.ServiceConfig{})
	hook := sessionforensics.NewAutoSummaryHook(svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook.Start(ctx)
	defer hook.Stop()

	// 应该入队成功
	if !hook.Enqueue(ctx, "gw_test_enq_001", "hello world") {
		t.Fatal("first Enqueue should succeed")
	}
	// 同一 session 在 cooldown 内再次 Enqueue 应该被跳过
	if hook.Enqueue(ctx, "gw_test_enq_001", "hello world") {
		t.Error("second Enqueue within cooldown should be skipped")
	}
	// 等 cooldown 过（缩短 cooldown）
	hook.SetCooldown(50 * time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	if !hook.Enqueue(ctx, "gw_test_enq_001", "second message") {
		t.Error("Enqueue after cooldown should succeed again")
	}
}

func TestAutoSummaryHook_EmptySessionIDRejected(t *testing.T) {
	svc := sessionforensics.NewService(sessionforensics.ServiceConfig{})
	hook := sessionforensics.NewAutoSummaryHook(svc, nil)
	if hook.Enqueue(context.Background(), "", "x") {
		t.Error("empty sessionID should be rejected")
	}
}

func TestAutoSummaryHook_QueueFull(t *testing.T) {
	svc := sessionforensics.NewService(sessionforensics.ServiceConfig{})
	hook := sessionforensics.NewAutoSummaryHook(svc, nil)
	// 不启动 worker，让 queue 满
	for i := 0; i < 1024+10; i++ {
		hook.Enqueue(context.Background(),
			string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+string(rune('a'+(i/676)%26)),
			"test")
	}
	t.Log("queue full behavior does not panic")
}

func TestAutoSummaryHook_StopGracefulShutdown(t *testing.T) {
	svc := sessionforensics.NewService(sessionforensics.ServiceConfig{})
	hook := sessionforensics.NewAutoSummaryHook(svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook.Start(ctx)
	// Stop 应立即返回（worker 退出）
	done := make(chan struct{})
	go func() { hook.Stop(); close(done) }()
	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() took too long")
	}
}
