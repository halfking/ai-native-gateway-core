// Unified Auto-Orchestration Plugin — Workflow C (Observability & Audit Log)
//
// trace.go implements thin tracing helpers built on top of the gateway's
// existing OTel plumbing (internal/observability/tracer.go). It does NOT
// initialise the global tracer provider — that is the application's
// responsibility at startup — and it does NOT start spans itself. The
// helpers only:
//  1. Expose the active OTel TraceID as a lowercase 32-char hex string so
//     audit events can carry it without importing the OTel API at every
//     call site.
//  2. Propagate orchestration-specific correlation and causation IDs
//     through context.Value. These complement OTel Baggage but stay
//     separate because they are an orchestration-domain concept rather
//     than a transport-level one.
//
// IDs never enter high-cardinality metric labels (see metrics.go and
// design §9.2).
package observ

import (
	"context"

	oteltrace "go.opentelemetry.io/otel/trace"
)

// ctxKey is an unexported type used as the context.Value key so this
// package cannot collide with any other package's context plumbing.
type ctxKey struct{ name string }

var (
	correlationCtxKey = ctxKey{"orchestration.correlation"}
	causationCtxKey   = ctxKey{"orchestration.causation"}
)

// TraceIDFromContext returns the active OTel TraceID as a 32-char lowercase
// hex string, or "" if the context carries no valid span. Callers use it to
// stamp audit events so they can be cross-referenced with distributed traces
// in the observability backend.
func TraceIDFromContext(ctx context.Context) string {
	sc := oteltrace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

// WithCorrelationID returns a child context tagged with the orchestration
// correlation id. The audit emitter pulls this via CorrelationIDFromContext
// to stamp the AuditEvent.CorrelationID field.
func WithCorrelationID(parent context.Context, id string) context.Context {
	if id == "" {
		return parent
	}
	return context.WithValue(parent, correlationCtxKey, id)
}

// CorrelationIDFromContext returns the correlation id previously attached
// with WithCorrelationID, or "" if none is present.
func CorrelationIDFromContext(ctx context.Context) string {
	return stringFromCtx(ctx, correlationCtxKey)
}

// WithCausationID returns a child context tagged with the orchestration
// causation id (the event/decision that *caused* the current action).
func WithCausationID(parent context.Context, id string) context.Context {
	if id == "" {
		return parent
	}
	return context.WithValue(parent, causationCtxKey, id)
}

// CausationIDFromContext returns the causation id previously attached with
// WithCausationID, or "" if none is present.
func CausationIDFromContext(ctx context.Context) string {
	return stringFromCtx(ctx, causationCtxKey)
}

// NewCorrelationID returns a fresh crypto-random id suitable for either a
// correlation or a causation id. Implemented via NewEventID so the entropy
// source is shared and the call site stays simple.
func NewCorrelationID() string {
	return NewEventID()
}

// stringFromCtx safely extracts a string value previously stored under key,
// returning "" if absent or wrong-typed (context.Value returns interface{}).
func stringFromCtx(ctx context.Context, key ctxKey) string {
	if ctx == nil {
		return ""
	}
	v, ok := ctx.Value(key).(string)
	if !ok {
		return ""
	}
	return v
}
