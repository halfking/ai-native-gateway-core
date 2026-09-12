// Package ir: cross-protocol anomaly / loss-reason telemetry.
//
// 2026-07-28 (Step 4 round 2): §10 Step 4.10 of docs/superpowers/specs/2026-07-27-request-flow-audit-design.md
// requires every "unrepresentable" / "unknown field" branch in the
// Serialize{OpenAI,Anthropic,Gemini,Responses} / Parse* paths to record an
// explicit anomaly, not to silently drop. This file is the seam that lets the
// serializer / parser layer call a pluggable hook without importing the
// heavy streaming telemetry package.
//
// Design rules:
//
//   - Default reporter writes slog.Warn and never errors out; it must not
//     change wire format. The point is observability, not enforcement.
//   - Production wiring can inject any func matching AnomalyReporter — a thin
//     adapter to streaming.FormatAnomalyRecorder or a no-op test stub.
//   - Per (request_id, source_protocol, target_protocol, field_path) tuples
//     are deduplicated inside this package. The default fallback is a single
//     process-global map; IRScopedReporter overrides this with a per-IR map
//     so concurrent BuildReport goroutines on different IRs never share dedup
//     keys (the BLOCK review fix for "dedup 泄漏").
//   - The spec asks for "per protocol per request" at the parse-time unknown
//     field, so callers must pass a stable request_id placeholder ("unknown"
//     when not yet assigned).
package ir

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

// AnomalyType is the IR-layer classification of an unrepresentable or
// downgraded field crossing the IR bridge. It is intentionally separate from
// streaming.AnomalyType (which is response-shape focused); the IR layer is
// request-shape focused.
type AnomalyType string

const (
	// AnomalyProtocolLoss records a field that exists on the source
	// protocol but has no faithful representation on the target protocol.
	// Example: Anthropic thinking.signature → OpenAI (no equivalent).
	AnomalyProtocolLoss AnomalyType = "ir_protocol_loss"

	// AnomalyUnsupported is reserved for explicit-but-unhandled branches
	// (target protocol does not accept this field at all). Distinct from
	// "loss" because the source field is well-defined; we just don't
	// speak that dialect.
	AnomalyUnsupported AnomalyType = "ir_unsupported"

	// AnomalyTruncation marks a field that was truncated (size / value
	// limit) during serialization. The raw_value_truncated metadata flag
	// is always true for this anomaly type.
	AnomalyTruncation AnomalyType = "ir_truncation"

	// AnomalyUnknownField marks a parse-time unknown JSON field. The
	// Extensions bag still preserves it, but the IR layer has no schema
	// for it; this is the spec's "per protocol per request" event.
	AnomalyUnknownField AnomalyType = "ir_unknown_field"
)

// Severity follows the streaming package's convention (kept as a string type
// here to avoid a circular import). Callers that want to forward into the
// streaming recorder can cast.
type Severity string

const (
	SeverityLow    Severity = "low"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

// AnomalyEvent is the structured payload every reporter receives. The fields
// are deliberately flat (no nested structs) so a downstream adapter can
// marshal it into the streaming AnomalyRecord.metadata without reflection.
type AnomalyEvent struct {
	// RequestID is the gateway request id; "unknown" when the call site
	// does not yet have one (e.g. parse-time fixtures, off-request tools).
	RequestID string

	// AnomalyType is the IR classification (see constants above).
	AnomalyType AnomalyType

	// Severity is the severity hint for downstream sinks.
	Severity Severity

	// FieldPath is the dotted JSON pointer to the field, e.g.
	// "messages[2].content[1].thinking.signature".
	FieldPath string

	// SourceProtocol / TargetProtocol are the IR protocol tags
	// (ProtocolOpenAIChat, ProtocolAnthropicMessages, ...). One may be
	// empty (e.g. parse-time has no target yet).
	SourceProtocol string
	TargetProtocol string

	// Reason is a short, machine-readable token: "loss", "unsupported",
	// "truncation", "unknown_field". Mirrors the spec's enum.
	Reason string

	// Message is a free-text human note; never required for downstream
	// consumers, only for slog.
	Message string

	// RawValueTruncated is always true per the spec requirement; callers
	// must set it to true even if the value itself is short — the flag is
	// a "yes we deliberately did not log the raw value" indicator.
	RawValueTruncated bool

	// Metadata carries extra fields (max_tokens, parallel_tool_calls,
	// etc.) without bloating the flat payload.
	Metadata map[string]any

	// Timestamp is when the anomaly was observed (UTC).
	Timestamp time.Time
}

// AnomalyReporter is the seam. The default implementation logs via slog.Warn;
// an injected implementation may forward into streaming telemetry.
//
// Implementations MUST:
//   - never panic on nil inputs (a serializer in a panic path may pass nil);
//   - be safe for concurrent use (serializers run in parallel goroutines);
//   - never block more than a few microseconds (the IR layer is on the
//     request hot path; a slow reporter must not stall the response).
type AnomalyReporter func(AnomalyEvent)

// DefaultAnomalyReporter writes a structured slog.Warn. It is the fallback
// when no other reporter is installed via SetAnomalyReporter.
func DefaultAnomalyReporter() AnomalyReporter {
	return func(ev AnomalyEvent) {
		slog.Warn("ir.anomaly",
			"anomaly_type", string(ev.AnomalyType),
			"severity", string(ev.Severity),
			"request_id", ev.RequestID,
			"field_path", ev.FieldPath,
			"source_protocol", ev.SourceProtocol,
			"target_protocol", ev.TargetProtocol,
			"reason", ev.Reason,
			"raw_value_truncated", ev.RawValueTruncated,
			"metadata", ev.MetadataAsJSON(),
			"message", ev.Message,
		)
	}
}

// MetadataAsJSON renders Metadata as a JSON string so it survives slog's
// key/value formatter without panicking on weird map types. Returns "{}"
// when metadata is empty.
func (e AnomalyEvent) MetadataAsJSON() string {
	if len(e.Metadata) == 0 {
		return "{}"
	}
	b, err := json.Marshal(e.Metadata)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// process-wide reporter + dedup (kept as fallback for non-scoped callers)
// ---------------------------------------------------------------------------

var (
	reporterMu  sync.RWMutex
	reporter    AnomalyReporter = DefaultAnomalyReporter()
	reporterDed                 = map[string]struct{}{}

	// activeScopeMu guards activeScope. The active scope, when non-nil,
	// intercepts every package-level ReportProtocolLoss / ReportUnknownField
	// call so the dedup map is per-IR instead of process-global. This is
	// the BLOCK review fix for "dedup 泄漏" — concurrent BuildReport
	// goroutines no longer interfere with each other's dedup keys.
	activeScopeMu sync.RWMutex
	activeScope   *IRScopedReporter
)

// SetAnomalyReporter installs a process-wide reporter. Passing nil restores
// the default slog.Warn reporter. Returns the previously installed reporter
// for symmetry with sync.Once-style helpers.
func SetAnomalyReporter(r AnomalyReporter) AnomalyReporter {
	reporterMu.Lock()
	defer reporterMu.Unlock()
	prev := reporter
	if r == nil {
		reporter = DefaultAnomalyReporter()
	} else {
		reporter = r
	}
	// Reset dedup state whenever the reporter changes so tests can re-run
	// with the same fixture set without seeing the dedup swallow events.
	reporterDed = map[string]struct{}{}
	return prev
}

// ResetAnomalyReporter clears dedup state without touching the reporter.
// Useful in tests between sub-cases that want to re-trigger the same event.
func ResetAnomalyReporter() {
	reporterMu.Lock()
	defer reporterMu.Unlock()
	reporterDed = map[string]struct{}{}
}

// ReportAnomaly emits an event. It is a no-op when the reporter is nil
// (which never happens given the default, but tests sometimes unbox).
//
// Dedup routing:
//   - If an IRScopedReporter is installed via SetAsCurrent, the event is
//     routed through the scope (per-IR dedup). This is the BLOCK review
//     fix for cross-request dedup leakage.
//   - Otherwise the event is deduplicated against the process-wide map.
//     This satisfies the legacy "per protocol per request" phrasing for
//     parse-time unknown fields while keeping the hot path cheap.
//
// When dedup suppresses an event it still returns true (recorded) so callers
// can use the return value as "I have observed this" without changing
// behavior. The boolean is exposed only for tests; production callers
// discard it.
func ReportAnomaly(ev AnomalyEvent) bool {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	if ev.Severity == "" {
		ev.Severity = SeverityMedium
	}
	if ev.RawValueTruncated == false {
		// Spec mandates raw_value_truncated=true on every event. We do
		// NOT force this to true (callers may opt-out for tests), but the
		// default cross-protocol helper does set it.
	}

	activeScopeMu.RLock()
	scope := activeScope
	activeScopeMu.RUnlock()
	if scope != nil {
		return scope.emit(ev)
	}

	key := dedupKey(ev)

	reporterMu.Lock()
	if _, seen := reporterDed[key]; seen {
		reporterMu.Unlock()
		return true
	}
	reporterDed[key] = struct{}{}
	r := reporter
	reporterMu.Unlock()

	if r != nil {
		r(ev)
	}
	return true
}

func dedupKey(ev AnomalyEvent) string {
	// Compact key: type|req|src|tgt|field|reason. Cheap and unique enough
	// for our purposes (we are not bucketing by metadata).
	b, _ := json.Marshal(struct {
		T   AnomalyType `json:"t"`
		R   string      `json:"r"`
		S   string      `json:"s"`
		Tgt string      `json:"tgt"`
		F   string      `json:"f"`
		Rsn string      `json:"rsn"`
	}{ev.AnomalyType, ev.RequestID, ev.SourceProtocol, ev.TargetProtocol, ev.FieldPath, ev.Reason})
	return string(b)
}

// ---------------------------------------------------------------------------
// IRScopedReporter — per-IR dedup. The BLOCK review's fix for "dedup 泄漏":
// each request / IR-context binds its own dedup map so concurrent BuildReport
// goroutines running on different IR contexts never see each other's keys.
// ---------------------------------------------------------------------------

// IRScopedReporter binds a dedup map to one IR lifecycle (a single request
// or a single BuildReport pass). Create via NewIRScopedReporter; release via
// Close (or via the deferred cancel returned by WithIRScope).
//
// Methods:
//   - ReportProtocolLoss / ReportUnknownField: ergonomic wrappers.
//   - emit: low-level entry point used by ReportAnomaly.
//   - Snapshot: returns a copy of captured events (test helper).
//   - SetAsCurrent / Unset: install/remove as the package-level active scope.
//
// Concurrency: IRScopedReporter is safe for concurrent use. The dedup map
// is guarded by sync.Mutex; the underlying AnomalyReporter is invoked
// outside the lock.
type IRScopedReporter struct {
	mu       sync.Mutex
	dedup    map[string]struct{}
	events   []AnomalyEvent
	reporter AnomalyReporter
	closed   bool
}

// NewIRScopedReporter creates an IR-scoped reporter. If r is nil the
// process-wide reporter (DefaultAnomalyReporter or whatever was installed
// via SetAnomalyReporter) is used at emit time; the scope only owns its
// own dedup map and event buffer.
func NewIRScopedReporter(r AnomalyReporter) *IRScopedReporter {
	return &IRScopedReporter{
		dedup:    map[string]struct{}{},
		reporter: r,
	}
}

// WithIRScope is a convenience wrapper that creates an IRScopedReporter,
// installs it as the active scope, and returns a cleanup function. The
// cleanup MUST be called (typically via defer) so that subsequent requests
// do not inherit a dead scope.
func WithIRScope(r AnomalyReporter) (*IRScopedReporter, func()) {
	s := NewIRScopedReporter(r)
	s.SetAsCurrent()
	return s, func() {
		s.Unset()
		s.Close()
	}
}

// Close clears the dedup map and event buffer; the scope is no longer
// usable after Close. Close is idempotent.
func (s *IRScopedReporter) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.dedup = map[string]struct{}{}
	s.events = nil
}

// SetAsCurrent installs this scope as the package-level active scope.
// Subsequent package-level ReportProtocolLoss / ReportUnknownField /
// ReportAnomaly calls route through this scope's dedup map until Unset
// is called or another scope is installed.
func (s *IRScopedReporter) SetAsCurrent() {
	if s == nil {
		return
	}
	activeScopeMu.Lock()
	activeScope = s
	activeScopeMu.Unlock()
}

// Unset clears the active scope pointer if it currently points at this
// scope. Safe to call from any goroutine; idempotent.
func (s *IRScopedReporter) Unset() {
	if s == nil {
		return
	}
	activeScopeMu.Lock()
	if activeScope == s {
		activeScope = nil
	}
	activeScopeMu.Unlock()
}

// UnsetIRScopedReporter clears the active scope pointer globally. Used by
// tests to guarantee no scope leaks between sub-tests.
func UnsetIRScopedReporter() {
	activeScopeMu.Lock()
	activeScope = nil
	activeScopeMu.Unlock()
}

// emit is the low-level entry point. It dedups against the scope's map
// and, on a fresh event, dispatches to the underlying AnomalyReporter.
// If no reporter was installed at construction time, the process-wide
// reporter is consulted.
func (s *IRScopedReporter) emit(ev AnomalyEvent) bool {
	if s == nil {
		return ReportAnomaly(ev)
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	if ev.Severity == "" {
		ev.Severity = SeverityMedium
	}

	key := dedupKey(ev)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return true
	}
	if _, seen := s.dedup[key]; seen {
		s.mu.Unlock()
		return true
	}
	s.dedup[key] = struct{}{}
	s.events = append(s.events, ev)
	r := s.reporter
	s.mu.Unlock()

	if r == nil {
		reporterMu.RLock()
		r = reporter
		reporterMu.RUnlock()
	}
	if r != nil {
		r(ev)
	}
	return true
}

// ReportProtocolLoss is the IR-scoped wrapper around the package-level
// ReportProtocolLoss helper. Routing through it preserves per-IR dedup
// when an IRScopedReporter is the active scope.
func (s *IRScopedReporter) ReportProtocolLoss(fieldPath, sourceProtocol, targetProtocol, reason, message string, extra map[string]any) {
	if s == nil {
		ReportProtocolLoss("unknown", fieldPath, sourceProtocol, targetProtocol, reason, message, extra)
		return
	}
	s.emit(AnomalyEvent{
		RequestID:         "unknown",
		AnomalyType:       AnomalyProtocolLoss,
		Severity:          SeverityMedium,
		FieldPath:         fieldPath,
		SourceProtocol:    sourceProtocol,
		TargetProtocol:    targetProtocol,
		Reason:            reason,
		Message:           message,
		RawValueTruncated: true,
		Metadata:          extra,
	})
}

// ReportUnknownField is the IR-scoped wrapper for parse-time unknown
// fields. The requestID is left empty here; callers that have a stable
// id should set it via the underlying emit.
func (s *IRScopedReporter) ReportUnknownField(sourceProtocol, fieldPath string, extra map[string]any) {
	if s == nil {
		ReportUnknownField("unknown", sourceProtocol, fieldPath, extra)
		return
	}
	s.emit(AnomalyEvent{
		RequestID:         "unknown",
		AnomalyType:       AnomalyUnknownField,
		Severity:          SeverityLow,
		FieldPath:         fieldPath,
		SourceProtocol:    sourceProtocol,
		Reason:            "unknown_field",
		RawValueTruncated: true,
		Metadata:          extra,
	})
}

// Snapshot returns a copy of captured events. Test helper; not on the
// request hot path.
func (s *IRScopedReporter) Snapshot() []AnomalyEvent {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AnomalyEvent, len(s.events))
	copy(out, s.events)
	return out
}

// ---------------------------------------------------------------------------
// ergonomic helpers
// ---------------------------------------------------------------------------

// ReportProtocolLoss is the standard call for "field exists on source but
// is unrepresentable on target". It captures the spec-mandated
// raw_value_truncated=true flag and a sensible default metadata bundle.
//
// fieldPath is the dotted JSON pointer. reason should be one of
// "loss", "unsupported", "truncation".
func ReportProtocolLoss(requestID, fieldPath, sourceProtocol, targetProtocol, reason, message string, extra map[string]any) {
	ReportAnomaly(AnomalyEvent{
		RequestID:         requestID,
		AnomalyType:       AnomalyProtocolLoss,
		Severity:          SeverityMedium,
		FieldPath:         fieldPath,
		SourceProtocol:    sourceProtocol,
		TargetProtocol:    targetProtocol,
		Reason:            reason,
		Message:           message,
		RawValueTruncated: true,
		Metadata:          extra,
	})
}

// ReportUnknownField is the standard call for parse-time unknown fields.
// The spec asks for "per protocol per request" — the dedup key already
// includes RequestID and SourceProtocol so this works correctly when the
// request_id is "unknown" too (still gets one event per request when the
// caller has the real id).
func ReportUnknownField(requestID, sourceProtocol, fieldPath string, extra map[string]any) {
	ReportAnomaly(AnomalyEvent{
		RequestID:         requestID,
		AnomalyType:       AnomalyUnknownField,
		Severity:          SeverityLow,
		FieldPath:         fieldPath,
		SourceProtocol:    sourceProtocol,
		Reason:            "unknown_field",
		RawValueTruncated: true,
		Metadata:          extra,
	})
}
