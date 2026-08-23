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
//   - field   ∈ sanitizeFieldLabels                (must stay in sync with
//                                                   the sanitize* helpers in
//                                                   client.go:sanitizeRequestLogEntry
//                                                   and EmitRequestLogUpdate /
//                                                   EmitRequestLogInsert; audit P2-3)
//   - source  ∈ {string_field, json_field,         (three sanitize helpers)
//                raw_json_field}
//   - stage   ∈ {sanitize, required_field_guard}   (two stages)
//
// All label values are pre-initialised at boot so dashboards observe a
// stable surface from process start.
var sanitizeEventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "telemetry_sanitize_events_total",
	Help: "Telemetry request_log sanitisation events (outcome × field × source × stage). outcome=discarded covers two distinct paths (audit P1-2): (a) sanitizeUTF8JSON returned \"\" for a JSONB-bound field — the column was NULLed, which is the SEVERE /request-logs UI loss signal (source=json_field|raw_json_field, stage=sanitize); (b) EmitRequestLogUpdate/Insert dropped an entry whose RequestID was empty — orphan UPSERT guard (source=string_field, stage=required_field_guard). Use source/stage labels to disambiguate. outcome=rescued means the field was truncated but a usable JSON prefix is retained. Mirrors slog.Warn calls in client.go:sanitizeJSONField / sanitizeRawJSONField.",
},
	[]string{"outcome", "field", "source", "stage"},
)

// sanitizeFieldLabels is the exhaustive pre-init list. Keep in sync
// with the call sites in client.go:sanitizeRequestLogEntry and the new
// required-field guard. Adding a new label value here without a matching
// call site in client.go creates dead-but-pre-init series (benign waste);
// adding a new call site without extending this list causes the series
// to appear lazily on first .Inc() (audit P2-3 — prefer the former).
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
	"request_id",
}

// sanitizeOutcomeLabels / sanitizeStageLabels / sanitizeSourceLabels.
//
// outcome:
//
//	discarded — two semantic paths (audit P1-2):
//	           (1) sanitizeUTF8JSON returned ""; the JSONB column was NULLed
//	               out (stage=sanitize, source=json_field|raw_json_field).
//	               For request_body / outbound_body this is the SEVERE
//	               signal — /request-logs UI sees nothing. alertable.
//	           (2) EmitRequestLogUpdate/Insert dropped an entry whose
//	               RequestID was empty (stage=required_field_guard,
//	               source=string_field). Orphan UPSERT prevention.
//
//	rescued   — sanitizeUTF8JSON returned a non-empty truncated string; the
//	            truncated prefix is KEPT in the column so auditors still see
//	            something. Non-alerting by itself, but worth tracking per
//	            (field, source, stage); model-level comparisons come from
//	            request_logs_hot/request_wal_hot because this metric has no model label.
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
