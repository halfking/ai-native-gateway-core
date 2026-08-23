package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// 2026-08-23 (245 incident, minimax-m3): the sanitizeJSONField /
// sanitizeRawJSONField paths in client.go silently NULL out JSONB-bound
// fields when their bytes are not valid UTF-8 (the upstream sometimes
// emits such bytes — minimax-m3 was the canonical reproducer on env 245).
// The lost columns included request_body / response_body / outbound_body /
// routing_attempts / attachments — i.e. the very fields the /request-logs
// UI relies on. Until now the only signal was a slog.Warn, which never
// reached Prometheus and so could not be alerted on.
//
// The counters below mirror the discard/rescue log lines at
// client.go:2604 / 2630, plus an additional `required_field_guard` series
// for the EmitRequestLogUpdate best-effort write guard.
//
// Label cardinality is bounded:
//   - outcome ∈ {discarded, rescued}              (two outcomes, fixed set)
//   - field   ∈ {request_body, response_body,      (eleven fields sanitized
//                 outbound_body, compression_meta, by client.go:2578-2587
//                 discard_events, outbound_msg_hashes,  + auto_decision)
//                 quality_fix_actions, tool_calls,
//                 attachments, routing_attempts,
//                 auto_decision}
//   - source  ∈ {string_field, json_field,         (three sanitize helpers)
//                raw_json_field}
//   - stage   ∈ {sanitize, required_field_guard}   (two stages)
//
// All label values are pre-initialised at boot so dashboards observe a
// stable surface from process start.
var sanitizeEventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "telemetry_sanitize_events_total",
	Help: "Telemetry request_log sanitisation events (outcome × field × source × stage). outcome=discarded means the field was NULLed out — for the JSONB columns that drive /request-logs this is the actual loss signal; outcome=rescued means the field was truncated but a usable prefix is retained. Mirrors slog.Warn calls in client.go:sanitizeJSONField / sanitizeRawJSONField.",
},
	[]string{"outcome", "field", "source", "stage"},
)

// sanitizeFieldLabels is the exhaustive pre-init list. Keep in sync
// with the call sites in client.go:sanitizeRequestLogEntry and the new
// required-field guard.
var sanitizeFieldLabels = []string{
	"request_body",
	"response_body",
	"outbound_body",
	"compression_meta",
	"discard_events",
	"outbound_msg_hashes",
	"quality_fix_actions",
	"tool_calls",
	"attachments",
	"routing_attempts",
	"auto_decision",
}

// sanitizeOutcomeLabels / sanitizeStageLabels / sanitizeSourceLabels.
//
// outcome:
//
//	discarded — sanitizeUTF8JSON returned ""; the JSONB column was NULLed
//	            out. This is the SEVERE signal — for request_body / outbound_body
//	            it means /request-logs UI sees nothing. alertable.
//
//	rescued   — sanitizeUTF8JSON returned a non-empty truncated string; the
//	            truncated prefix is KEPT in the column so auditors still see
//	            something. Non-alerting by itself, but worth tracking per
//	            (model, field) to spot upstream regressions.
var (
	sanitizeOutcomeLabels = []string{"discarded", "rescued"}
	sanitizeStageLabels   = []string{"sanitize", "required_field_guard"}
	sanitizeSourceLabels  = []string{"string_field", "json_field", "raw_json_field"}
)

func init() {
	for _, field := range sanitizeFieldLabels {
		for _, outcome := range sanitizeOutcomeLabels {
			for _, source := range sanitizeSourceLabels {
				for _, stage := range sanitizeStageLabels {
					sanitizeEventsTotal.WithLabelValues(outcome, field, source, stage).Add(0)
				}
			}
		}
	}
}

// incSanitizeEvent is the single Inc seam for sanitisation telemetry.
// Keeps the label string literals in one place so a future addition is
// a one-line edit here.
func incSanitizeEvent(outcome, field, source, stage string) {
	sanitizeEventsTotal.WithLabelValues(outcome, field, source, stage).Inc()
}
