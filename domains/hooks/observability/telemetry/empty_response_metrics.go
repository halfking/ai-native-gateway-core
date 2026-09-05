package telemetry

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// successResponseBodyMissingTotal (audit-24h-20260829-r5 §5.5) counts
// non-streaming requests that the gateway classified as Success=true but
// whose persisted ResponseBody is empty / missing. The pre-existing
// `has_response_body` slog label in client.go:1481 only fires on the
// request_logs_bodies_hot failure path; success-with-empty-body is a
// distinct, observability-blind failure mode (the client saw an HTTP 200
// with content, but the audit row is bodyless — usually a transformer
// truncation, a stream-reassembly bug, or a redaction side-effect).
//
// The hook fires inside persistRequestLog AFTER the row INSERT/UPDATE
// commits and BEFORE releaseBodies() zeroes the bodies; the entry's
// ResponseBody pointer is still valid at that point.
//
// Label cardinality is bounded:
//   - protocol ∈ {chat, responses, anthropic_messages}    (request_mode)
//   - stream   ∈ {non_stream, stream}                     (derived from
//     StreamChunkCount; the non-empty stream bucket is informational,
//     not an alert source)
//
// The non_stream × success-but-empty cell is the alertable surface.
// The stream bucket is recorded for ratio calculation but never
// alerted on (stream chunk count > 0 already proves success).
var successResponseBodyMissingTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "telemetry_success_response_body_missing_total",
	Help: "Successful (Success=true) request_logs rows whose ResponseBody is empty/missing at INSERT/UPDATE commit. protocol=request_mode (bounded); stream=non_stream|stream (derived from StreamChunkCount). The non_stream bucket is the alertable surface — a healthy gateway with N streaming + M non-streaming successes should have ZERO in non_stream. A non-zero rate suggests a body-loss regression in the audit pipeline.",
},
	[]string{"protocol", "stream"},
)

// pre-init label values so dashboards observe a stable surface from
// process start (audit-24h-20260828-r3 P2-3 convention).
var successResponseBodyMissingLabelValues = []struct{ protocol, stream string }{
	{"chat", "non_stream"},
	{"chat", "stream"},
	{"responses", "non_stream"},
	{"responses", "stream"},
	{"anthropic_messages", "non_stream"},
	{"anthropic_messages", "stream"},
}

func init() {
	for _, v := range successResponseBodyMissingLabelValues {
		successResponseBodyMissingTotal.WithLabelValues(v.protocol, v.stream)
	}
}

// emptyResponseGateEnabled flips from false → true the first time
// RegisterEmptyResponseGate is called. recordEmptyResponseBody returns
// early while false so test code can construct RequestLogEntry values
// without observing the metric side effect unless the production wiring
// path explicitly opted in.
var emptyResponseGateEnabled = false

// RegisterEmptyResponseGate wires the success-and-empty-response-body
// classification hook on the given Client. Must be called after the
// Client is constructed (before its worker drains), once per process.
//
// Keeping the gate behind an explicit opt-in (rather than auto-registering
// in init) preserves the package's "metrics are pure observers, business
// logic opts in" boundary. A future test may flip the flag without
// affecting production wiring.
func RegisterEmptyResponseGate(c *Client) {
	if c == nil {
		return
	}
	emptyResponseGateEnabled = true
	c.AddOnRequestLogPersisted(recordEmptyResponseBody)
}

// recordEmptyResponseBody is the persistRequestLog onPersisted hook.
// Cheap, non-blocking, panic-safe (the persistRequestLog wrapper
// already recovers, but we add a defensive recover for hook re-entrancy).
func recordEmptyResponseBody(entry *RequestLogEntry) {
	if entry == nil || !emptyResponseGateEnabled {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("telemetry recordEmptyResponseBody panic",
				"request_id", entry.RequestID)
		}
	}()

	stream := "non_stream"
	if entry.StreamChunkCount != nil && *entry.StreamChunkCount > 0 {
		stream = "stream"
	}

	if !entry.Success {
		return
	}
	if hasMeaningfulResponseBody(entry.ResponseBody) {
		return
	}

	protocol := "unknown"
	if entry.RequestMode != nil && *entry.RequestMode != "" {
		protocol = *entry.RequestMode
	}
	successResponseBodyMissingTotal.WithLabelValues(protocol, stream).Inc()

	slog.Warn("telemetry success_response_body_missing",
		"request_id", entry.RequestID,
		"tenant_id", entry.TenantID,
		"protocol", protocol,
		"stream", stream,
		"stream_chunk_count", intValueOrZero(entry.StreamChunkCount),
	)
}

// hasMeaningfulResponseBody returns true when the response body is
// present and contains more than trivial whitespace. The classifier
// treats "" and all-whitespace as missing, since either signal means
// the audit row cannot answer the question "what did the upstream
// actually return to the client?"
func hasMeaningfulResponseBody(body *string) bool {
	if body == nil {
		return false
	}
	for _, r := range *body {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return true
		}
	}
	return false
}

// intValueOrZero returns *entry as int, or 0 when entry is nil. Local
// helper — telemetry package already has intValueOrZero for *int in
// other files but it lives in private scope. Duplicated here rather
// than exported.
func intValueOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
