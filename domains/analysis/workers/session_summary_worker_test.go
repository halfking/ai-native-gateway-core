package workers

import (
	"context"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

type fakeSummarizer struct {
	called  int
	err     error
	lastKey string
	lastTnt string
}

func (f *fakeSummarizer) GenerateSummary(_ context.Context, tenantID, sessionKey string) (any, error) {
	f.called++
	f.lastTnt = tenantID
	f.lastKey = sessionKey
	if f.err != nil {
		return nil, f.err
	}
	return nil, nil
}

func TestSessionSummaryWorker_NameAndSubscribed(t *testing.T) {
	w := NewSessionSummaryWorker(&fakeSummarizer{}, nil)
	if w.Name() != "session_summary_worker" {
		t.Fatalf("Name = %q", w.Name())
	}
	subs := w.SubscribedTypes()
	if len(subs) != 1 || subs[0] != analysis.EventSessionClosed {
		t.Fatalf("SubscribedTypes = %v", subs)
	}
}

func TestSessionSummaryWorker_IgnoresNonClosed(t *testing.T) {
	summ := &fakeSummarizer{}
	w := NewSessionSummaryWorker(summ, nil)
	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		Type:      analysis.EventRequestCompleted,
		SessionID: "s1", TenantID: "t1",
	})
	if summ.called != 0 {
		t.Fatal("should not call summarizer on request.completed")
	}
}

func TestSessionSummaryWorker_NilSummarizerNoop(t *testing.T) {
	w := NewSessionSummaryWorker(nil, nil)
	if err := w.Handle(context.Background(), analysis.AnalysisEvent{
		Type:      analysis.EventSessionClosed,
		SessionID: "s1", TenantID: "t1",
	}); err != nil {
		t.Fatalf("nil summarizer should noop, got %v", err)
	}
	p, f := w.Stats()
	if p != 0 || f != 0 {
		t.Fatalf("stats should be zero, got (%d,%d)", p, f)
	}
}

func TestSessionSummaryWorker_SkipsMissingIDs(t *testing.T) {
	summ := &fakeSummarizer{}
	w := NewSessionSummaryWorker(summ, nil)
	// 缺 tenant
	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		Type: analysis.EventSessionClosed, SessionID: "s1",
	})
	// 缺 session_key
	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		Type: analysis.EventSessionClosed, TenantID: "t1",
	})
	if summ.called != 0 {
		t.Fatalf("should skip events missing ids, got %d calls", summ.called)
	}
}

func TestSessionSummaryWorker_ExtractsSessionKeyFromPayload(t *testing.T) {
	summ := &fakeSummarizer{}
	w := NewSessionSummaryWorker(summ, nil)
	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		Type:     analysis.EventSessionClosed,
		TenantID: "t1",
		Payload:  map[string]any{"session_key": "from-payload"},
	})
	if summ.called != 1 || summ.lastKey != "from-payload" || summ.lastTnt != "t1" {
		t.Fatalf("expected call with payload-extracted key, got called=%d key=%q tnt=%q",
			summ.called, summ.lastKey, summ.lastTnt)
	}
}

func TestSessionSummaryWorker_ErrorDoesNotPropagate(t *testing.T) {
	summ := &fakeSummarizer{err: errors.New("llm unavailable")}
	w := NewSessionSummaryWorker(summ, nil)
	if err := w.Handle(context.Background(), analysis.AnalysisEvent{
		Type:      analysis.EventSessionClosed,
		SessionID: "s1", TenantID: "t1",
	}); err != nil {
		t.Fatalf("error should not propagate: %v", err)
	}
	p, f := w.Stats()
	if p != 1 {
		t.Fatalf("processed = %d, want 1", p)
	}
	if f != 1 {
		t.Fatalf("failed = %d, want 1", f)
	}
}
