package clientprofile

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

func TestRetryableWorker_NilInner(t *testing.T) {
	r := NewRetryableWorker(nil, 3, time.Millisecond, nil)
	if r != nil {
		t.Fatal("NewRetryableWorker(nil) should return nil")
	}
}

func TestRetryableWorker_NameAndSubscribed(t *testing.T) {
	store := newFakeStore()
	agg := NewAggregator(store, nil)
	w := NewProfileWorker(agg, nil)
	r := NewRetryableWorker(w, 1, time.Millisecond, nil)
	if r.Name() != w.Name() {
		t.Errorf("Name = %q, want %q", r.Name(), w.Name())
	}
	if got := r.SubscribedTypes(); len(got) != len(w.SubscribedTypes()) {
		t.Errorf("SubscribedTypes len = %d, want %d", len(got), len(w.SubscribedTypes()))
	}
}

func TestRetryableWorker_Defaults(t *testing.T) {
	store := newFakeStore()
	agg := NewAggregator(store, nil)
	w := NewProfileWorker(agg, nil)
	r := NewRetryableWorker(w, 0, 0, nil) // 触发默认值
	if r == nil {
		t.Fatal("constructor returned nil")
	}
	if r.maxRetries != 1 {
		t.Errorf("maxRetries = %d, want 1", r.maxRetries)
	}
	if r.backoff != 100*time.Millisecond {
		t.Errorf("backoff = %v, want 100ms", r.backoff)
	}
}

func TestRetryableWorker_Handle_NoFailure(t *testing.T) {
	store := newFakeStore()
	agg := NewAggregator(store, nil)
	w := NewProfileWorker(agg, nil)
	w.SetAsync(false)
	r := NewRetryableWorker(w, 3, time.Millisecond, nil)
	evt := analysis.AnalysisEvent{
		Type:    analysis.EventRequestCompleted,
		Payload: map[string]any{"identity_hash": longHash("1"), "model": "gpt-4"},
	}
	if err := r.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if store.eventCount() != 1 {
		t.Errorf("event count = %d, want 1", store.eventCount())
	}
}

// flakyStore 模拟偶发失败的 store
type flakyStore struct {
	*fakeStore
	failuresLeft atomic.Int32
}

func (s *flakyStore) SaveEvent(ctx context.Context, e *ClientBehaviorEvent) error {
	if s.failuresLeft.Load() > 0 {
		s.failuresLeft.Add(-1)
		return errors.New("transient db error")
	}
	return s.fakeStore.SaveEvent(ctx, e)
}

func TestRetryableWorker_Handle_RetriesThenSucceeds(t *testing.T) {
	inner := newFakeStore()
	flaky := &flakyStore{fakeStore: inner, failuresLeft: atomic.Int32{}}
	flaky.failuresLeft.Store(2) // 前两次失败，第三次成功
	agg := NewAggregator(flaky, nil)

	w := NewProfileWorker(agg, nil)
	w.SetAsync(false)
	r := NewRetryableWorker(w, 5, time.Millisecond, nil)

	evt := analysis.AnalysisEvent{
		Type:    analysis.EventRequestCompleted,
		Payload: map[string]any{"identity_hash": longHash("retry"), "model": "gpt-4"},
	}
	if err := r.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle after retries: %v", err)
	}
	// 应当最终成功 → 至少一条事件落 store
	if inner.eventCount() < 1 {
		t.Errorf("event count = %d, want >= 1", inner.eventCount())
	}
	// flaky 应消耗掉 2 次失败额度
	if flaky.failuresLeft.Load() != 0 {
		t.Errorf("failuresLeft = %d, want 0", flaky.failuresLeft.Load())
	}
}

func TestRetryableWorker_Handle_ExhaustsRetries(t *testing.T) {
	inner := newFakeStore()
	flaky := &flakyStore{fakeStore: inner, failuresLeft: atomic.Int32{}}
	flaky.failuresLeft.Store(10) // 永远失败
	agg := NewAggregator(flaky, nil)

	w := NewProfileWorker(agg, nil)
	w.SetAsync(false)
	r := NewRetryableWorker(w, 3, time.Millisecond, nil)

	evt := analysis.AnalysisEvent{
		Type:    analysis.EventRequestCompleted,
		Payload: map[string]any{"identity_hash": longHash("exhaust"), "model": "gpt-4"},
	}
	err := r.Handle(context.Background(), evt)
	if err == nil {
		t.Fatal("expected exhaustion error")
	}
}

func TestRetryableWorker_Handle_ContextCancelStops(t *testing.T) {
	inner := newFakeStore()
	flaky := &flakyStore{fakeStore: inner, failuresLeft: atomic.Int32{}}
	flaky.failuresLeft.Store(10)
	agg := NewAggregator(flaky, nil)

	w := NewProfileWorker(agg, nil)
	w.SetAsync(false)
	r := NewRetryableWorker(w, 5, 50*time.Millisecond, nil) // 退避 50ms

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	_ = r.Handle(ctx, analysis.AnalysisEvent{
		Type:    analysis.EventRequestCompleted,
		Payload: map[string]any{"identity_hash": longHash("cancel")},
	})
}
