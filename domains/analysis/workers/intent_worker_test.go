package workers

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain/analysis"
	"github.com/kaixuan/llm-gateway-go/domains/assets"
)

func TestIntentWorker_NameAndSubscribed(t *testing.T) {
	w := NewIntentWorker(nil)
	if w.Name() != "intent_worker" {
		t.Fatalf("Name = %q", w.Name())
	}
	if got := w.SubscribedTypes(); len(got) != 1 || got[0] != analysis.EventRequestCompleted {
		t.Fatalf("SubscribedTypes = %v, want [request.completed]", got)
	}
}

func TestIntentWorker_SkipsNonCompleted(t *testing.T) {
	w := NewIntentWorker(nil)
	evt := analysis.AnalysisEvent{Type: analysis.EventSessionClosed, Payload: map[string]any{
		"user_content": "hello world",
	}}
	if err := w.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(w.Snapshot()) != 0 {
		t.Fatal("session.closed should be ignored")
	}
}

func TestIntentWorker_SkipsMissingContent(t *testing.T) {
	w := NewIntentWorker(nil)
	cases := []struct {
		name    string
		payload any
	}{
		{"nil payload", nil},
		{"empty map", map[string]any{}},
		{"non-string content", map[string]any{"user_content": 123}},
		{"missing key", map[string]any{"other": "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evt := analysis.AnalysisEvent{Type: analysis.EventRequestCompleted, Payload: c.payload}
			if err := w.Handle(context.Background(), evt); err != nil {
				t.Fatalf("Handle: %v", err)
			}
		})
	}
	if len(w.Snapshot()) != 0 {
		t.Fatal("no content should not be counted")
	}
}

// TestIntentWorker_ClassifiesAllKinds PR-V4-10：按 tenant 分桶。
func TestIntentWorker_ClassifiesAllKinds(t *testing.T) {
	w := NewIntentWorker(nil)
	cases := []struct {
		content string
		want    analysis.IntentKind
	}{
		{"hello world", analysis.IntentChat},
		{"你好", analysis.IntentChat},
		{"write a function in code", analysis.IntentCode},
		{"declare var foo", analysis.IntentCode},
		{"explain why this works", analysis.IntentReasoning},
		{"because the system requires it", analysis.IntentReasoning},
		{"random gibberish content", analysis.IntentUnclassified},
	}
	for _, c := range cases {
		evt := analysis.AnalysisEvent{
			EventID:  "ev-" + string(c.want),
			TenantID: "t1",
			Type:     analysis.EventRequestCompleted,
			Payload:  map[string]any{"user_content": c.content},
		}
		if err := w.Handle(context.Background(), evt); err != nil {
			t.Fatalf("Handle(%q): %v", c.content, err)
		}
	}
	bucket := w.Snapshot()["t1"]
	if bucket[analysis.IntentChat] != 2 {
		t.Errorf("chat = %d, want 2", bucket[analysis.IntentChat])
	}
	if bucket[analysis.IntentCode] != 2 {
		t.Errorf("code = %d, want 2", bucket[analysis.IntentCode])
	}
	if bucket[analysis.IntentReasoning] != 2 {
		t.Errorf("reasoning = %d, want 2", bucket[analysis.IntentReasoning])
	}
	if bucket[analysis.IntentUnclassified] != 1 {
		t.Errorf("unclassified = %d, want 1", bucket[analysis.IntentUnclassified])
	}
}

func TestIntentWorker_ConcurrentSafe(t *testing.T) {
	w := NewIntentWorker(nil)
	const N = 200
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			evt := analysis.AnalysisEvent{
				EventID:  "ev",
				TenantID: "t1",
				Type:     analysis.EventRequestCompleted,
				Payload:  map[string]any{"user_content": "hello"},
			}
			_ = w.Handle(context.Background(), evt)
		}()
	}
	wg.Wait()
	if got := w.Snapshot()["t1"][analysis.IntentChat]; got != N {
		t.Fatalf("concurrent: chat = %d, want %d", got, N)
	}
}

// fakeStore 用于 FlushAndReset / Flusher 测试。
type fakeStore struct {
	mu    sync.Mutex
	calls []fakeIncCall
	errOn map[string]bool // key "tenant:kind" → true 时返回 err
}

type fakeIncCall struct {
	tenant string
	kind   analysis.IntentKind
	delta  int64
}

func (f *fakeStore) Increment(_ context.Context, tenant string, kind analysis.IntentKind, delta int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errOn != nil && f.errOn[tenant+":"+string(kind)] {
		return errors.New("fake increment failure")
	}
	f.calls = append(f.calls, fakeIncCall{tenant, kind, delta})
	return nil
}

func (f *fakeStore) Get(_ context.Context, _ string) ([]assets.IntentAggregate, error) {
	return nil, nil
}

func TestIntentWorker_FlushAndReset_NilStoreJustResets(t *testing.T) {
	w := NewIntentWorker(nil)
	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		TenantID: "t1", Type: analysis.EventRequestCompleted,
		Payload: map[string]any{"user_content": "hello"},
	})
	if err := w.FlushAndReset(context.Background(), nil); err != nil {
		t.Fatalf("FlushAndReset nil store: %v", err)
	}
	if len(w.Snapshot()) != 0 {
		t.Fatal("counts should be reset")
	}
}

func TestIntentWorker_FlushAndReset_PersistsAndResets(t *testing.T) {
	w := NewIntentWorker(nil)
	store := &fakeStore{}
	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		TenantID: "t1", Type: analysis.EventRequestCompleted,
		Payload: map[string]any{"user_content": "hello"},
	})
	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		TenantID: "t1", Type: analysis.EventRequestCompleted,
		Payload: map[string]any{"user_content": "write code"},
	})
	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		TenantID: "t2", Type: analysis.EventRequestCompleted,
		Payload: map[string]any{"user_content": "hi"},
	})
	if err := w.FlushAndReset(context.Background(), store); err != nil {
		t.Fatalf("FlushAndReset: %v", err)
	}
	if len(store.calls) != 3 {
		t.Fatalf("want 3 Increment calls, got %d: %+v", len(store.calls), store.calls)
	}
	// 重复 flush 不应再产生 Increment
	_ = w.FlushAndReset(context.Background(), store)
	if len(store.calls) != 3 {
		t.Fatalf("after reset, no new calls expected; got %d", len(store.calls))
	}
}

func TestIntentWorker_FlushAndReset_EmptyTenantSkipped(t *testing.T) {
	w := NewIntentWorker(nil)
	store := &fakeStore{}
	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		TenantID: "", Type: analysis.EventRequestCompleted,
		Payload: map[string]any{"user_content": "hello"},
	})
	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		TenantID: "t1", Type: analysis.EventRequestCompleted,
		Payload: map[string]any{"user_content": "hi"},
	})
	_ = w.FlushAndReset(context.Background(), store)
	if len(store.calls) != 1 || store.calls[0].tenant != "t1" {
		t.Fatalf("only t1 should be flushed, got %+v", store.calls)
	}
}

func TestIntentWorker_FlushAndReset_PartialFailure(t *testing.T) {
	w := NewIntentWorker(nil)
	store := &fakeStore{errOn: map[string]bool{"t1:code": true}}
	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		TenantID: "t1", Type: analysis.EventRequestCompleted,
		Payload: map[string]any{"user_content": "write code"},
	})
	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		TenantID: "t2", Type: analysis.EventRequestCompleted,
		Payload: map[string]any{"user_content": "hi"},
	})
	err := w.FlushAndReset(context.Background(), store)
	if err == nil {
		t.Fatal("expected first-error from t1:code")
	}
	// errOn 命中时 fakeStore 在 append 前 return，所以只有 t2:chat 进 calls
	if len(store.calls) != 1 {
		t.Fatalf("want 1 successful call (t1 failed), got %d", len(store.calls))
	}
	if store.calls[0].tenant != "t2" {
		t.Fatalf("the successful call should be t2, got %s", store.calls[0].tenant)
	}
}
