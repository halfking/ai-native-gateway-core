package workers

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain/analysis"
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
	snap := w.Snapshot()
	if len(snap) != 0 {
		t.Fatalf("session.closed should be ignored, got %v", snap)
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
	if snap := w.Snapshot(); len(snap) != 0 {
		t.Fatalf("no content should not be counted, got %v", snap)
	}
}

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
			EventID: "ev-" + string(c.want),
			Type:    analysis.EventRequestCompleted,
			Payload: map[string]any{"user_content": c.content},
		}
		if err := w.Handle(context.Background(), evt); err != nil {
			t.Fatalf("Handle(%q): %v", c.content, err)
		}
	}
	snap := w.Snapshot()
	if snap[analysis.IntentChat] != 2 {
		t.Errorf("chat count = %d, want 2", snap[analysis.IntentChat])
	}
	if snap[analysis.IntentCode] != 2 {
		t.Errorf("code count = %d, want 2", snap[analysis.IntentCode])
	}
	if snap[analysis.IntentReasoning] != 2 {
		t.Errorf("reasoning count = %d, want 2", snap[analysis.IntentReasoning])
	}
	if snap[analysis.IntentUnclassified] != 1 {
		t.Errorf("unclassified count = %d, want 1", snap[analysis.IntentUnclassified])
	}
}

func TestIntentWorker_ConcurrentSafe(t *testing.T) {
	w := NewIntentWorker(nil)
	const N = 200
	done := make(chan struct{})
	go func() {
		for i := 0; i < N; i++ {
			evt := analysis.AnalysisEvent{
				EventID: "ev",
				Type:    analysis.EventRequestCompleted,
				Payload: map[string]any{"user_content": "hello"},
			}
			_ = w.Handle(context.Background(), evt)
		}
		close(done)
	}()
	<-done
	if got := w.Snapshot()[analysis.IntentChat]; got != N {
		t.Fatalf("concurrent handle: chat count = %d, want %d", got, N)
	}
}
