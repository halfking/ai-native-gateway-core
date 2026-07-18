package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
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
	schedulerSelections             prometheus.Counter
	schedulerSelectionsByCredential *prometheus.CounterVec
	schedulerWeight                 *prometheus.GaugeVec
	schedulerCurrentWeight          *prometheus.GaugeVec
	schedulerEffectiveWeight        *prometheus.GaugeVec
	schedulerSelectionDuration      prometheus.Histogram
	schedulerAvailableCredentials   prometheus.Gauge

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
}

// NewPrometheusRecorder 创建 Prometheus Recorder
func NewPrometheusRecorder() *PrometheusRecorder {
	return &PrometheusRecorder{
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
		schedulerSelectionsByCredential: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llm_gateway_scheduler_selections_by_credential",
				Help: "Selections by credential",
			},
			[]string{"credential_id"},
		),
		schedulerWeight: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llm_gateway_scheduler_weight",
				Help: "Configured weight",
			},
			[]string{"credential_id"},
		),
		schedulerCurrentWeight: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llm_gateway_scheduler_current_weight",
				Help: "Current weight",
			},
			[]string{"credential_id"},
		),
		schedulerEffectiveWeight: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llm_gateway_scheduler_effective_weight",
				Help: "Effective weight",
			},
			[]string{"credential_id"},
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
				Help: "Healthy credentials in pool",
			},
			[]string{"pool_id"},
		),
	}
}

// Circuit Breaker methods
func (p *PrometheusRecorder) RecordCircuitRequest(state string) {
	p.circuitRequests.WithLabelValues(state).Inc()
}

func (p *PrometheusRecorder) RecordCircuitSuccess() {
	p.circuitSuccesses.Inc()
}

func (p *PrometheusRecorder) RecordCircuitFailure() {
	p.circuitFailures.Inc()
}

func (p *PrometheusRecorder) RecordCircuitStateChange(from, to string) {
	p.circuitStateChanges.WithLabelValues(from, to).Inc()
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
func (p *PrometheusRecorder) RecordSchedulerSelection(credentialID string, duration time.Duration) {
	p.schedulerSelections.Inc()
	p.schedulerSelectionsByCredential.WithLabelValues(credentialID).Inc()
	p.schedulerSelectionDuration.Observe(duration.Seconds())
}

func (p *PrometheusRecorder) UpdateSchedulerWeight(credentialID string, weight int) {
	p.schedulerWeight.WithLabelValues(credentialID).Set(float64(weight))
}

func (p *PrometheusRecorder) UpdateSchedulerCurrentWeight(credentialID string, weight int) {
	p.schedulerCurrentWeight.WithLabelValues(credentialID).Set(float64(weight))
}

func (p *PrometheusRecorder) UpdateSchedulerEffectiveWeight(credentialID string, weight int) {
	p.schedulerEffectiveWeight.WithLabelValues(credentialID).Set(float64(weight))
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
