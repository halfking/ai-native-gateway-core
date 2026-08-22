// Unified Auto-Orchestration Plugin — Workflow C (Observability & Audit Log)
// Tracing helper tests. TDD: contract for trace.go.
//
// These helpers are intentionally thin wrappers over the gateway's existing
// OTel plumbing (see internal/observability/tracer.go). The contract is:
//   - TraceIDFromContext returns the lowercase 32-char hex TraceID from the
//     active OTel span, or "" if there is no valid span.
//   - CorrelationID / CausationID flow through context.Value so the audit
//     emitter can stamp every event with the causal chain that produced it.
package observ

import (
	"context"
	"strings"
	"testing"

	oteltrace "go.opentelemetry.io/otel/trace"
)

// TestTraceIDFromContext_NoSpanReturnsEmpty verifies the empty-path.
func TestTraceIDFromContext_NoSpanReturnsEmpty(t *testing.T) {
	if got := TraceIDFromContext(context.Background()); got != "" {
		t.Errorf("TraceIDFromContext(bg)=%q want empty", got)
	}
}

// TestTraceIDFromContext_WithSpanContext verifies a valid span context
// produces the expected 32-char lowercase hex TraceID.
func TestTraceIDFromContext_WithSpanContext(t *testing.T) {
	tid := oteltrace.TraceID{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10}
	sid := oteltrace.SpanID{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11}
	sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: oteltrace.FlagsSampled,
	})
	ctx := oteltrace.ContextWithSpanContext(context.Background(), sc)

	got := TraceIDFromContext(ctx)
	want := "0123456789abcdeffedcba9876543210"
	if got != want {
		t.Errorf("TraceIDFromContext=%q want %q", got, want)
	}
	if len(got) != 32 {
		t.Errorf("TraceID hex length=%d want 32", len(got))
	}
	if strings.ToLower(got) != got {
		t.Errorf("TraceID not lowercase: %q", got)
	}
}

// TestCorrelationID_RoundTrip verifies WithCorrelationID + FromCorrelationID.
func TestCorrelationID_RoundTrip(t *testing.T) {
	id := "corr-12345"
	ctx := WithCorrelationID(context.Background(), id)
	if got := CorrelationIDFromContext(ctx); got != id {
		t.Errorf("CorrelationIDFromContext=%q want %q", got, id)
	}
}

// TestCausationID_RoundTrip verifies WithCausationID + FromCausationID.
func TestCausationID_RoundTrip(t *testing.T) {
	id := "cause-abcde"
	ctx := WithCausationID(context.Background(), id)
	if got := CausationIDFromContext(ctx); got != id {
		t.Errorf("CausationIDFromContext=%q want %q", got, id)
	}
}

// TestCorrelationAndCausation_Independent verifies the two context keys do
// not collide: setting one does not leak into the other.
func TestCorrelationAndCausation_Independent(t *testing.T) {
	ctx := WithCorrelationID(context.Background(), "corr-1")
	ctx = WithCausationID(ctx, "cause-1")

	if got := CorrelationIDFromContext(ctx); got != "corr-1" {
		t.Errorf("CorrelationID=%q want corr-1", got)
	}
	if got := CausationIDFromContext(ctx); got != "cause-1" {
		t.Errorf("CausationID=%q want cause-1", got)
	}

	// And the empty-path reads still return "" when the key is absent.
	bg := context.Background()
	if got := CorrelationIDFromContext(bg); got != "" {
		t.Errorf("empty CorrelationIDFromContext=%q want empty", got)
	}
	if got := CausationIDFromContext(bg); got != "" {
		t.Errorf("empty CausationIDFromContext=%q want empty", got)
	}
}

// TestNewCorrelationID_Unique verifies the helper produces non-empty unique
// IDs (re-uses NewEventID's crypto-random source).
func TestNewCorrelationID_Unique(t *testing.T) {
	const n = 256
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := NewCorrelationID()
		if id == "" {
			t.Fatal("empty correlation id")
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate after %d generations: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}
