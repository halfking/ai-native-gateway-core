package integrity

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// captureRecorder records every Event passed to Record. Used to assert
// the detector's branching without touching the DB.
type captureRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (c *captureRecorder) Record(_ context.Context, ev Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
	return nil
}

func (c *captureRecorder) byType(t AnomalyType) []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Event
	for _, e := range c.events {
		if e.AnomalyType == t {
			out = append(out, e)
		}
	}
	return out
}

func TestDetector_NilSafe(t *testing.T) {
	var d *Detector
	d.Observe(context.Background(), Candidate{}) // must not panic
	var d2 = (*Detector)(nil)
	d2.Observe(context.Background(), Candidate{})
}

// Mismatch fires when case-insensitive comparison fails.
func TestDetector_ModelMismatch(t *testing.T) {
	rec := &captureRecorder{}
	d := NewDetector(rec)
	d.Observe(context.Background(), Candidate{
		RequestID:     "r1",
		OutboundModel: "glm-5.2",
		RespModel:     "glm-5.1",
		ProviderCode:  "test",
	})
	if got := rec.byType(AnomalyModelMismatch); len(got) != 1 {
		t.Fatalf("expected 1 mismatch, got %d", len(got))
	}
	if got := rec.byType(AnomalyModelMismatch)[0].Severity; got != SeverityHigh {
		t.Fatalf("severity = %s, want %s", got, SeverityHigh)
	}
}

// Case-insensitive match does NOT fire.
func TestDetector_ModelMismatch_CaseInsensitive(t *testing.T) {
	rec := &captureRecorder{}
	d := NewDetector(rec)
	d.Observe(context.Background(), Candidate{
		OutboundModel: "GLM-5.2",
		RespModel:     "glm-5.2",
	})
	if got := rec.byType(AnomalyModelMismatch); len(got) != 0 {
		t.Fatalf("expected 0, got %d", len(got))
	}
}

// Missing client/outbound/resp fields disable the check (no false positives).
func TestDetector_ModelMismatch_DisabledWhenEmpty(t *testing.T) {
	rec := &captureRecorder{}
	d := NewDetector(rec)
	d.Observe(context.Background(), Candidate{RespModel: "glm-5.1"})
	if got := rec.byType(AnomalyModelMismatch); len(got) != 0 {
		t.Fatalf("expected 0, got %d", len(got))
	}
}

func TestDetector_FinishRefusal(t *testing.T) {
	rec := &captureRecorder{}
	d := NewDetector(rec)
	d.Observe(context.Background(), Candidate{
		FinishReason:  "content_filter",
		OutboundModel: "glm-5.2",
	})
	if got := rec.byType(AnomalyFinishRefusal); len(got) != 1 {
		t.Fatalf("expected 1 refusal, got %d", len(got))
	}
}

func TestDetector_FinishTruncation(t *testing.T) {
	rec := &captureRecorder{}
	d := NewDetector(rec)
	d.Observe(context.Background(), Candidate{
		FinishReason:  "length",
		OutboundModel: "glm-5.2",
		ChunkCount:    42,
		ChunksSent:    42,
	})
	if got := rec.byType(AnomalyFinishTruncation); len(got) != 1 {
		t.Fatalf("expected 1 truncation, got %d", len(got))
	}
}

func TestDetector_FinishReason_UnknownIsIgnored(t *testing.T) {
	rec := &captureRecorder{}
	d := NewDetector(rec)
	d.Observe(context.Background(), Candidate{FinishReason: "stop"})
	if got := rec.byType(AnomalyFinishRefusal); len(got) != 0 {
		t.Fatalf("stop should be benign, got %d", len(got))
	}
	if got := rec.byType(AnomalyFinishTruncation); len(got) != 0 {
		t.Fatalf("stop should be benign, got %d", len(got))
	}
}

func TestDetector_TokenArith_OpenAIMismatch(t *testing.T) {
	rec := &captureRecorder{}
	d := NewDetector(rec)
	pt, ct, tot := 10, 20, 99
	d.Observe(context.Background(), Candidate{
		PromptTokens:     &pt,
		CompletionTokens: &ct,
		TotalTokens:      &tot,
	})
	if got := rec.byType(AnomalyTokenArithFail); len(got) != 1 {
		t.Fatalf("expected 1 arith fail, got %d", len(got))
	}
}

func TestDetector_TokenArith_OpenAIOK(t *testing.T) {
	rec := &captureRecorder{}
	d := NewDetector(rec)
	pt, ct, tot := 10, 20, 30
	d.Observe(context.Background(), Candidate{
		PromptTokens:     &pt,
		CompletionTokens: &ct,
		TotalTokens:      &tot,
	})
	if got := rec.byType(AnomalyTokenArithFail); len(got) != 0 {
		t.Fatalf("expected 0, got %d", len(got))
	}
}

func TestDetector_TokenArith_AnthropicOK(t *testing.T) {
	rec := &captureRecorder{}
	d := NewDetector(rec)
	it, ot, tot := 10, 20, 30
	d.Observe(context.Background(), Candidate{
		InputTokens:  &it,
		OutputTokens: &ot,
		TotalTokens:  &tot,
	})
	if got := rec.byType(AnomalyTokenArithFail); len(got) != 0 {
		t.Fatalf("expected 0, got %d", len(got))
	}
}

func TestDetector_TokenArith_ZeroValuesSkipped(t *testing.T) {
	rec := &captureRecorder{}
	d := NewDetector(rec)
	tot := 0
	d.Observe(context.Background(), Candidate{TotalTokens: &tot})
	if got := rec.byType(AnomalyTokenArithFail); len(got) != 0 {
		t.Fatalf("expected 0, got %d", len(got))
	}
}

func TestDetector_EmptyResponse(t *testing.T) {
	rec := &captureRecorder{}
	d := NewDetector(rec)
	d.Observe(context.Background(), Candidate{
		IsStream:      true,
		ChunkCount:    2,
		FinishReason:  "stop",
		OutboundModel: "glm-5.2",
	})
	if got := rec.byType(AnomalyEmptyResponse); len(got) != 1 {
		t.Fatalf("expected 1 empty, got %d", len(got))
	}
}

func TestDetector_EmptyResponse_NonStreamSkipped(t *testing.T) {
	rec := &captureRecorder{}
	d := NewDetector(rec)
	d.Observe(context.Background(), Candidate{
		IsStream:     false,
		ChunkCount:   2,
		FinishReason: "stop",
	})
	if got := rec.byType(AnomalyEmptyResponse); len(got) != 0 {
		t.Fatalf("non-stream should be skipped, got %d", len(got))
	}
}

func TestDetector_RepeatedContent(t *testing.T) {
	rec := &captureRecorder{}
	d := NewDetector(rec)
	// Two identical 256-byte blocks back to back.
	block := strings.Repeat("a", 256)
	d.Observe(context.Background(), Candidate{
		TextContent:   block + block,
		OutboundModel: "glm-5.2",
	})
	if got := rec.byType(AnomalyRepeatedContent); len(got) != 1 {
		t.Fatalf("expected 1 repeated, got %d", len(got))
	}
}

func TestDetector_RepeatedContent_ShortSkipped(t *testing.T) {
	rec := &captureRecorder{}
	d := NewDetector(rec)
	// Only one full block — not enough.
	d.Observe(context.Background(), Candidate{TextContent: strings.Repeat("a", 100)})
	if got := rec.byType(AnomalyRepeatedContent); len(got) != 0 {
		t.Fatalf("expected 0, got %d", len(got))
	}
}

func TestDetector_AllSignals_NilRecorder(t *testing.T) {
	d := NewDetector(nil)
	// Must not panic.
	d.Observe(context.Background(), Candidate{
		OutboundModel: "a", RespModel: "b",
		FinishReason: "length",
		ChunkCount:   1, IsStream: true,
		TextContent: strings.Repeat("x", 600),
	})
}
