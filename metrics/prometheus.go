package metrics

import (
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/kaixuan/llm-gateway-go/pkg/logger"
)

// PrometheusRecorder 是 Prometheus 实现
type PrometheusRecorder struct {
	// Circuit Breaker
	circuitRequests     *prometheus.CounterVec
	circuitSuccesses    prometheus.Counter
	circuitFailures     prometheus.Counter
	circuitStateChanges *prometheus.CounterVec
	circuitTrips        prometheus.Counter
	circuitLatency      prometheus.Histogram
	circuitState        *prometheus.GaugeVec
	circuitErrorRate    prometheus.Gauge

	// Adapter
	adapterConversions        *prometheus.CounterVec
	adapterConversionDuration *prometheus.HistogramVec
	adapterFailures           *prometheus.CounterVec
	adapterTokens             *prometheus.CounterVec
	adapterActive             *prometheus.GaugeVec

	// Scheduler
	schedulerSelections           prometheus.Counter
	schedulerSelectionsByProvider *prometheus.CounterVec
	schedulerWeight               *prometheus.GaugeVec
	schedulerCurrentWeight        *prometheus.GaugeVec
	schedulerEffectiveWeight      *prometheus.GaugeVec
	schedulerSelectionDuration    prometheus.Histogram
	schedulerAvailableCredentials prometheus.Gauge

	// Safety
	safetyChecks        *prometheus.CounterVec
	safetyBlocked       *prometheus.CounterVec
	safetyActions       *prometheus.CounterVec
	safetyRuleHits      *prometheus.CounterVec
	safetyCheckDuration *prometheus.HistogramVec
	safetyWhitelistHits prometheus.Counter
	safetyRulesCount    *prometheus.GaugeVec

	// Pool
	poolUtilization        *prometheus.GaugeVec
	poolRequests           *prometheus.CounterVec
	poolCapacity           *prometheus.GaugeVec
	poolActiveCredentials  *prometheus.GaugeVec
	poolHealthyCredentials *prometheus.GaugeVec

	// ShadowWrite (P0-2)
	//
	// shadowWriteFailed  : best-effort hook (attachmentmirror / sessionv2mirror)
	//                      that failed AFTER the primary request_logs INSERT
	//                      succeeded. Counter is monotonic — missing rows are
	//                      observable through Prometheus rate().
	//
	// ringBufferDropped  : entries overwritten because the in-memory fallback
	//                      buffer hit its configured capacity. CRITICAL because
	//                      these rows are LOST, not deferred. Counter is
	//                      monotonic.
	//
	// rawAuditFailed     : raw audit JSONL write/rotate/sync failed. CRITICAL
	//                      because audit JSONL is the only immutable local copy
	//                      before cross-machine replication (P2-2).
	shadowWriteFailed   *prometheus.CounterVec
	ringBufferDropped   prometheus.Counter
	rawAuditWriteFailed prometheus.Counter

	// streamSynthDoneTotal (P1 hot-patch 2026-08-06): counts streams
	// where the gateway had to inject "data: [DONE]\n\n" because the
	// upstream closed without one (MiniMax API ~13% rate as of
	// 2026-07-28). Excludes streams where upstream sent [DONE]
	// naturally. See Recorder interface for classifier notes.
	streamSynthDoneTotal prometheus.Counter

	// URSMv2Shadow (P0-3)
	//
	// ursmv2ShadowResult counts what the URSM v2 sidecar did with each
	// request outcome. result = "recorded" (URSM v2 accepted the write)
	// | "skipped" (ModeOff / ShadowDoubleWrite off) | "failed" (Redis
	// write error). Operators diff against legacy credentialstate write
	// counts after a 7-day shadow run to confirm < 1% drift before
	// cutover (audit §7.1 R-7.1).
	ursmv2ShadowResult *prometheus.CounterVec

	// malformedSSEFrameTotal (2026-08-29): counts SSE frames with invalid
	// JSON rejected by the validation layer. Labels: provider (to avoid
	// model name cardinality), stage ("first_frame" before any client
	// output | "mid_stream" after chunks sent). High rates indicate
	// unstable upstreams (minimax-m3, glm-5.2) sending incomplete JSON.
	// Triggers investigation when rate(malformed_sse_frame_total[5m]) > threshold.
	malformedSSEFrameTotal *prometheus.CounterVec

	// successEmptyResponseTotal (2026-08-29): counts requests marked as
	// successful but returned no content (empty response body or zero tokens).
	// Label: provider_id. Helps identify providers with high empty response
	// rates (e.g., NVIDIA NIM ~13%). Uses provider_id instead of model to
	// avoid high cardinality (GW-00 label constraint).
	successEmptyResponseTotal *prometheus.CounterVec

	// journalSnapshotStoredTotal (2026-08-29): counts journal snapshots
	// successfully written to JournalSnapshotStore. No labels to keep
	// cardinality minimal (tenant_id is forbidden per GW-00).
	journalSnapshotStoredTotal prometheus.Counter

	// journalSnapshotAppliedTotal (2026-08-29): counts journal snapshot
	// Apply() operations. Label: success (true|false). No tenant_id per GW-00.
	journalSnapshotAppliedTotal *prometheus.CounterVec

	// journalSnapshotDeduplicatedTotal (2026-08-29): counts journal snapshots
	// rejected due to deduplication. Label: reason
	// (already_completed|version_conflict|not_claimed). No tenant_id per GW-00.
	journalSnapshotDeduplicatedTotal *prometheus.CounterVec

	logger logger.Logger
}

// NewPrometheusRecorder 创建 Prometheus Recorder
func NewPrometheusRecorder() *PrometheusRecorder {
	recorder := &PrometheusRecorder{
		// Circuit Breaker
		circuitRequests: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_circuit_requests_total",
				Help: "Total circuit breaker requests by state",
			},
			[]string{"state"},
		),
		circuitSuccesses: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "llm_gateway_circuit_successes_total",
				Help: "Total successful requests",
			},
		),
		circuitFailures: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "llm_gateway_circuit_failures_total",
				Help: "Total failed requests",
			},
		),
		circuitStateChanges: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_circuit_state_transitions_total",
				Help: "Total state transitions",
			},
			[]string{"from", "to"},
		),
		circuitTrips: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "llm_gateway_circuit_trips_total",
				Help: "Total circuit breaker trips",
			},
		),
		circuitLatency: promauto.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "llm_gateway_circuit_request_duration_seconds",
				Help:    "Circuit breaker request latency",
				Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 2, 5},
			},
		),
		circuitState: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llm_gateway_circuit_state",
				Help: "Current circuit breaker state",
			},
			[]string{"state"},
		),
		circuitErrorRate: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: "llm_gateway_circuit_error_rate",
				Help: "Current error rate",
			},
		),

		// Adapter
		adapterConversions: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_adapter_conversions_total",
				Help: "Total adapter conversions",
			},
			[]string{"provider", "direction"},
		),
		adapterConversionDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "llm_gateway_adapter_conversion_duration_seconds",
				Help:    "Adapter conversion latency",
				Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05},
			},
			[]string{"provider"},
		),
		adapterFailures: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_adapter_conversion_failures_total",
				Help: "Total adapter conversion failures",
			},
			[]string{"provider", "reason"},
		),
		adapterTokens: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_adapter_tokens_total",
				Help: "Total tokens processed",
			},
			[]string{"provider", "type"},
		),
		adapterActive: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llm_gateway_adapter_active",
				Help: "Active adapters",
			},
			[]string{"provider"},
		),

		// Scheduler
		schedulerSelections: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "llm_gateway_scheduler_selections_total",
				Help: "Total scheduler selections",
			},
		),
		// GW-00: label 从 credential_id（高基数，随部署无限增长）改为
		// provider_id（低基数，按 provider 聚合）。这 4 个指标此前无生产
		// 调用方（scheduler 用自己的内存计数器），改动零生产破坏。
		schedulerSelectionsByProvider: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_scheduler_selections_by_provider",
				Help: "Selections by provider (low-cardinality)",
			},
			[]string{"provider_id"},
		),
		schedulerWeight: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llm_gateway_scheduler_weight",
				Help: "Configured weight",
			},
			[]string{"provider_id"},
		),
		schedulerCurrentWeight: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llm_gateway_scheduler_current_weight",
				Help: "Current weight",
			},
			[]string{"provider_id"},
		),
		schedulerEffectiveWeight: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llm_gateway_scheduler_effective_weight",
				Help: "Effective weight",
			},
			[]string{"provider_id"},
		),
		schedulerSelectionDuration: promauto.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "llm_gateway_scheduler_selection_duration_seconds",
				Help:    "Scheduler selection latency",
				Buckets: []float64{0.00001, 0.00005, 0.0001, 0.0005, 0.001},
			},
		),
		schedulerAvailableCredentials: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: "llm_gateway_scheduler_available_credentials",
				Help: "Available credentials count",
			},
		),

		// Safety
		safetyChecks: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_safety_checks_total",
				Help: "Total safety checks",
			},
			[]string{"type"},
		),
		safetyBlocked: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_safety_blocked_total",
				Help: "Total blocked content",
			},
			[]string{"severity"},
		),
		safetyActions: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_safety_actions_total",
				Help: "Total safety actions",
			},
			[]string{"action"},
		),
		safetyRuleHits: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_safety_rule_hits_total",
				Help: "Total rule hits",
			},
			[]string{"rule_id", "rule_name", "severity"},
		),
		safetyCheckDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "llm_gateway_safety_check_duration_seconds",
				Help:    "Safety check latency",
				Buckets: []float64{0.00001, 0.00005, 0.0001, 0.0005, 0.001, 0.005},
			},
			[]string{"type"},
		),
		safetyWhitelistHits: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "llm_gateway_safety_whitelist_hits_total",
				Help: "Total whitelist hits",
			},
		),
		safetyRulesCount: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llm_gateway_safety_rules_count",
				Help: "Safety rules count",
			},
			[]string{"enabled"},
		),

		// Pool
		poolUtilization: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llm_gateway_pool_utilization",
				Help: "Pool utilization ratio",
			},
			[]string{"pool_id"},
		),
		poolRequests: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_pool_requests_total",
				Help: "Total pool requests",
			},
			[]string{"pool_id", "status"},
		),
		poolCapacity: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llm_gateway_pool_capacity",
				Help: "Pool capacity",
			},
			[]string{"pool_id"},
		),
		poolActiveCredentials: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llm_gateway_pool_active_credentials",
				Help: "Active credentials in pool",
			},
			[]string{"pool_id"},
		),
		poolHealthyCredentials: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llm_gateway_pool_healthy_credentials",
				Help: "Healthy credentials count",
			},
			[]string{"pool_id"},
		),

		// ShadowWrite (P0-2)
		shadowWriteFailed: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_shadow_write_failed_total",
				Help: "Best-effort hook writes that failed AFTER the primary request_logs INSERT succeeded (label = hook kind)",
			},
			[]string{"kind"},
		),
		ringBufferDropped: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "llm_gateway_ringbuffer_dropped_total",
				Help: "In-memory fallback buffer entries overwritten because the ring hit its capacity (lost rows)",
			},
		),
		rawAuditWriteFailed: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "llm_gateway_rawaudit_write_failed_total",
				Help: "Raw audit JSONL write/rotate/sync failures (immutable local audit pipeline)",
			},
		),

		// P1 hot-patch 2026-08-06: stream synthesized [DONE] counter.
		streamSynthDoneTotal: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "llm_gateway_stream_synthesized_done_total",
				Help: "Streams where the gateway had to inject a trailing 'data: [DONE]\\n\\n' because the upstream closed without one (MiniMax API ~13% rate as of 2026-07-28).",
			},
		),

		// URSMv2Shadow (P0-3)
		ursmv2ShadowResult: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_ursm_v2_shadow_records_total",
				Help: "URSM v2 shadow sidecar outcomes during cutover comparison (label = recorded|skipped|failed)",
			},
			[]string{"result"},
		),

		// 2026-08-29: malformed SSE frame counter
		malformedSSEFrameTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_malformed_sse_frame_total",
				Help: "SSE frames with invalid JSON rejected by validation layer (labels: provider, stage=first_frame|mid_stream). High rates indicate unstable upstreams.",
			},
			[]string{"provider", "stage"},
		),

		// 2026-08-29: success empty response counter
		successEmptyResponseTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_success_empty_response_total",
				Help: "Requests marked as successful but returned no content (empty response body or zero tokens). Label: provider_id.",
			},
			[]string{"provider_id"},
		),

		// 2026-08-29: journal snapshot lifecycle counters
		journalSnapshotStoredTotal: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "gateway_journal_snapshot_stored_total",
				Help: "Journal snapshots successfully written to JournalSnapshotStore.",
			},
		),
		journalSnapshotAppliedTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_journal_snapshot_applied_total",
				Help: "Journal snapshot Apply() operations. Label: success (true|false).",
			},
			[]string{"success"},
		),
		journalSnapshotDeduplicatedTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_journal_snapshot_deduplicated_total",
				Help: "Journal snapshots rejected due to deduplication. Label: reason (already_completed|version_conflict|not_claimed).",
			},
			[]string{"reason"},
		),

		logger: logger.New("metrics"),
	}
	for _, result := range []string{"recorded", "skipped", "failed"} {
		recorder.ursmv2ShadowResult.WithLabelValues(result).Add(0)
	}
	return recorder
}

// Circuit Breaker methods
func (p *PrometheusRecorder) RecordCircuitRequest(state string) {
	p.circuitRequests.WithLabelValues(state).Inc()
	if p.logger.Enabled(slog.LevelDebug) {
		p.logger.Debug("circuit request recorded",
			"state", state,
		)
	}
}

func (p *PrometheusRecorder) RecordCircuitSuccess() {
	p.circuitSuccesses.Inc()
}

func (p *PrometheusRecorder) RecordCircuitFailure() {
	p.circuitFailures.Inc()
}

func (p *PrometheusRecorder) RecordCircuitStateChange(from, to string) {
	p.circuitStateChanges.WithLabelValues(from, to).Inc()
	if p.logger.Enabled(slog.LevelDebug) {
		p.logger.Debug("circuit state change recorded",
			"from", from,
			"to", to,
		)
	}
}

func (p *PrometheusRecorder) RecordCircuitTrip() {
	p.circuitTrips.Inc()
}

func (p *PrometheusRecorder) ObserveCircuitLatency(duration time.Duration) {
	p.circuitLatency.Observe(duration.Seconds())
}

func (p *PrometheusRecorder) SetCircuitState(state string) {
	// Reset all states
	p.circuitState.WithLabelValues("closed").Set(0)
	p.circuitState.WithLabelValues("open").Set(0)
	p.circuitState.WithLabelValues("half_open").Set(0)
	// Set current state
	p.circuitState.WithLabelValues(state).Set(1)
}

func (p *PrometheusRecorder) SetCircuitErrorRate(rate float64) {
	p.circuitErrorRate.Set(rate)
}

// Adapter methods
func (p *PrometheusRecorder) RecordAdapterConversion(provider, direction string, duration time.Duration) {
	p.adapterConversions.WithLabelValues(provider, direction).Inc()
	p.adapterConversionDuration.WithLabelValues(provider).Observe(duration.Seconds())
}

func (p *PrometheusRecorder) RecordAdapterFailure(provider, reason string) {
	p.adapterFailures.WithLabelValues(provider, reason).Inc()
}

func (p *PrometheusRecorder) RecordAdapterTokens(provider, tokenType string, count int) {
	p.adapterTokens.WithLabelValues(provider, tokenType).Add(float64(count))
}

func (p *PrometheusRecorder) SetAdapterActive(provider string, active bool) {
	val := 0.0
	if active {
		val = 1.0
	}
	p.adapterActive.WithLabelValues(provider).Set(val)
}

// Scheduler methods
func (p *PrometheusRecorder) RecordSchedulerSelection(providerID string, duration time.Duration) {
	p.schedulerSelections.Inc()
	p.schedulerSelectionsByProvider.WithLabelValues(providerID).Inc()
	p.schedulerSelectionDuration.Observe(duration.Seconds())
}

func (p *PrometheusRecorder) UpdateSchedulerWeight(providerID string, weight int) {
	p.schedulerWeight.WithLabelValues(providerID).Set(float64(weight))
}

func (p *PrometheusRecorder) UpdateSchedulerCurrentWeight(providerID string, weight int) {
	p.schedulerCurrentWeight.WithLabelValues(providerID).Set(float64(weight))
}

func (p *PrometheusRecorder) UpdateSchedulerEffectiveWeight(providerID string, weight int) {
	p.schedulerEffectiveWeight.WithLabelValues(providerID).Set(float64(weight))
}

func (p *PrometheusRecorder) SetSchedulerAvailableCredentials(count int) {
	p.schedulerAvailableCredentials.Set(float64(count))
}

// Safety methods
func (p *PrometheusRecorder) RecordSafetyCheck(checkType string, duration time.Duration) {
	p.safetyChecks.WithLabelValues(checkType).Inc()
	p.safetyCheckDuration.WithLabelValues(checkType).Observe(duration.Seconds())
}

func (p *PrometheusRecorder) RecordSafetyAction(action, severity string) {
	p.safetyActions.WithLabelValues(action).Inc()
	if action == "block" {
		p.safetyBlocked.WithLabelValues(severity).Inc()
	}
}

func (p *PrometheusRecorder) RecordSafetyRuleHit(ruleID, ruleName, severity string) {
	p.safetyRuleHits.WithLabelValues(ruleID, ruleName, severity).Inc()
}

func (p *PrometheusRecorder) RecordSafetyWhitelistHit() {
	p.safetyWhitelistHits.Inc()
}

func (p *PrometheusRecorder) SetSafetyRulesCount(enabled bool, count int) {
	enabledStr := "false"
	if enabled {
		enabledStr = "true"
	}
	p.safetyRulesCount.WithLabelValues(enabledStr).Set(float64(count))
}

// Pool methods
func (p *PrometheusRecorder) UpdatePoolUtilization(poolID string, utilization float64) {
	p.poolUtilization.WithLabelValues(poolID).Set(utilization)
}

func (p *PrometheusRecorder) RecordPoolRequest(poolID, status string) {
	p.poolRequests.WithLabelValues(poolID, status).Inc()
}

func (p *PrometheusRecorder) SetPoolCapacity(poolID string, capacity int) {
	p.poolCapacity.WithLabelValues(poolID).Set(float64(capacity))
}

func (p *PrometheusRecorder) SetPoolActiveCredentials(poolID string, count int) {
	p.poolActiveCredentials.WithLabelValues(poolID).Set(float64(count))
}

func (p *PrometheusRecorder) SetPoolHealthyCredentials(poolID string, count int) {
	p.poolHealthyCredentials.WithLabelValues(poolID).Set(float64(count))
}

// P0-2 ShadowWrite methods.
//
// The three counters cover the "soft write" failure modes that the
// audit identified as having no observability today. Each call site
// is documented in the caller's commit; the metric name + help text
// are the contract.
//
// Important: these counters are intentionally MONOTONIC (no reset).
// A rate(window) > 0 means rows are being lost / miswritten; a
// rate(window) == 0 means we're healthy. Operators should alert on
// rate() > 0 over a non-trivial window, not on the absolute counter
// value (which grows forever).

// RecordShadowWriteFailure counts a single failed best-effort hook
// write. kind is the hook's tag (e.g. "attachment", "session_v2").
//
// Kind values MUST be kept in sync with the alerting rules in
// deploy/monitoring/grafana-alerts/shadow-write-failures.yaml so the
// Grafana queries resolve to a non-empty time series.
func (p *PrometheusRecorder) RecordShadowWriteFailure(kind string) {
	p.shadowWriteFailed.WithLabelValues(kind).Inc()
}

// RecordRingBufferDropped counts the number of entries the
// in-memory fallback ring buffer overwrote because it was at
// capacity. These rows are LOST (not deferred to disk / replay).
// The caller passes the count from one ring-buffer push.
func (p *PrometheusRecorder) RecordRingBufferDropped(count uint64) {
	if count == 0 {
		return
	}
	p.ringBufferDropped.Add(float64(count))
}

// RecordRawAuditWriteFailure counts one write/rotate/sync failure
// from the raw audit JSONL pipeline. Single failure already matters
// because JSONL is the only immutable local copy before
// cross-machine replication lands (P2-2).
func (p *PrometheusRecorder) RecordRawAuditWriteFailure() {
	p.rawAuditWriteFailed.Inc()
}

// RecordStreamSynthesizedDone (P1 hot-patch 2026-08-06) increments the
// counter when domains/streaming/stream.go has to inject a trailing
// "data: [DONE]\n\n" because the upstream closed without one.
// Excludes streams where upstream sent [DONE] naturally.
//
// Counters are incremented on the hot path; never block. Used by
// operator dashboards to split "real stream interruptions" from
// "minimax-style expected-no-[DONE]" without touching the existing
// isBenignEOF classification in executor_chat.go:975 / handler.go:5609.
func (p *PrometheusRecorder) RecordStreamSynthesizedDone() {
	p.streamSynthDoneTotal.Inc()
}

// RecordURSMv2ShadowResult (P0-3) classifies each shadow sidecar
// call into one of three buckets so operators can run the 7-day
// drift comparison (audit §7.1):
//
//   - "recorded": URSM v2 accepted the write (Mode != Off, sidecar on)
//   - "skipped":  sidecar decided not to write (ModeOff, ShadowDoubleWrite off)
//   - "failed":   URSM v2 Redis write returned an error
//
// result values are validated upstream; unknown values will create
// new Prometheus time series. Keep this list stable.
func (p *PrometheusRecorder) RecordURSMv2ShadowResult(result string) {
	p.ursmv2ShadowResult.WithLabelValues(result).Inc()
}

// RecordMalformedSSEFrame (2026-08-29) increments the counter when the
// SSE frame validation layer rejects a frame with invalid JSON.
//
//   - provider: upstream provider name (e.g., "anthropic", "openai")
//   - stage: "first_frame" (before any client output, resumable retry) |
//            "mid_stream" (after chunks sent, frame skipped)
//
// High rates indicate unstable upstreams (minimax-m3, glm-5.2) sending
// incomplete JSON (bare "{", truncated objects). Operators monitor
// rate(malformed_sse_frame_total[5m]) to detect provider issues.
func (p *PrometheusRecorder) RecordMalformedSSEFrame(provider, stage string) {
	p.malformedSSEFrameTotal.WithLabelValues(provider, stage).Inc()
}

// RecordSuccessEmptyResponse (2026-08-29) increments the counter when a
// request is marked as successful but returned no content (empty response
// body or zero tokens). This helps identify providers with high empty
// response rates (e.g., NVIDIA NIM ~13%).
//
//   - providerID: provider identifier (low cardinality)
//
// Note: model and tenant_id are intentionally excluded per GW-00 label
// cardinality constraints. Use provider_id aggregation instead.
func (p *PrometheusRecorder) RecordSuccessEmptyResponse(model, providerID, tenantID string) {
	p.successEmptyResponseTotal.WithLabelValues(providerID).Inc()
}

// RecordJournalSnapshotStored (2026-08-29) increments the counter when a
// journal snapshot is successfully written to the JournalSnapshotStore.
// No labels per GW-00 (tenant_id is forbidden).
func (p *PrometheusRecorder) RecordJournalSnapshotStored(tenantID string) {
	p.journalSnapshotStoredTotal.Inc()
}

// RecordJournalSnapshotApplied (2026-08-29) increments the counter when a
// journal snapshot Apply() operation completes. success=true for clean apply,
// success=false for any Apply() error. No tenant_id per GW-00.
func (p *PrometheusRecorder) RecordJournalSnapshotApplied(tenantID string, success bool) {
	successStr := "false"
	if success {
		successStr = "true"
	}
	p.journalSnapshotAppliedTotal.WithLabelValues(successStr).Inc()
}

// RecordJournalSnapshotDeduplicated (2026-08-29) increments the counter when
// a journal snapshot is rejected due to deduplication. No tenant_id per GW-00.
//
//   - reason: "already_completed" (durable receipt found) |
//             "version_conflict" (hash mismatch) |
//             "not_claimed" (failed to acquire lease)
func (p *PrometheusRecorder) RecordJournalSnapshotDeduplicated(tenantID, reason string) {
	p.journalSnapshotDeduplicatedTotal.WithLabelValues(reason).Inc()
}
