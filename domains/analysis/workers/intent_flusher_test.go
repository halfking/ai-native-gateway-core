package workers

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain/analysis"
)

func TestIntentFlusher_FlushesPeriodically(t *testing.T) {
	w := NewIntentWorker(nil)
	store := &fakeStore{}
	flusher := NewIntentFlusher(w, store, 20*time.Millisecond, nil)

	// 灌 3 条 chat 事件 → 1 个 Increment(t1, chat, 3)
	for i := 0; i < 3; i++ {
		_ = w.Handle(context.Background(), analysis.AnalysisEvent{
			TenantID: "t1", Type: analysis.EventRequestCompleted,
			Payload: map[string]any{"user_content": "hello"},
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		flusher.Run(ctx)
		close(done)
	}()

	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done

	store.mu.Lock()
	defer store.mu.Unlock()
	// ≥1 tick 后至少 1 个 Increment call，且累计 delta 应该 ≥3
	totalDelta := int64(0)
	for _, c := range store.calls {
		totalDelta += c.delta
	}
	if totalDelta < 3 {
		t.Fatalf("want ≥3 total delta after ≥1 tick, got %d (calls=%d)", totalDelta, len(store.calls))
	}
}

func TestIntentFlusher_FinalFlushOnExit(t *testing.T) {
	w := NewIntentWorker(nil)
	store := &fakeStore{}
	// 长 interval，确保运行期间不触发 tick，只测退出时的 final flush
	flusher := NewIntentFlusher(w, store, 10*time.Second, nil)

	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		TenantID: "t1", Type: analysis.EventRequestCompleted,
		Payload: map[string]any{"user_content": "hi"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		flusher.Run(ctx)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.calls) != 1 {
		t.Fatalf("final flush should fire once with delta=1, got %d calls", len(store.calls))
	}
}
