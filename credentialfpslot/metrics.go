package credentialfpslot

import (
	"strconv"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	slotAcquireTotal     *prometheus.CounterVec
	slotAcquireFailures  *prometheus.CounterVec
	slotReleaseTotal     prometheus.Counter
	slotReleaseFailures  prometheus.Counter
	slotUtilization      *prometheus.GaugeVec
	slotSaturationEvents prometheus.Counter
	slotPreemptEvents    prometheus.Counter
	slotReclaimEvents    prometheus.Counter
	clientTokenRequests  *prometheus.CounterVec
	holderChanges        *prometheus.CounterVec
	activeSlots          *prometheus.GaugeVec
	unknownRatio         prometheus.Gauge
	pinAge               *prometheus.HistogramVec

	registerMetricsOnce sync.Once
	clientTokenStateMu  sync.Mutex
	clientTokenTotals   uint64
	clientTokenUnknown  uint64
	clientTokenHolders  = make(map[string]string)
	knownClientTypes    = make(map[string]map[string]struct{})
)

func registerMetrics() {
	registerMetricsOnce.Do(func() {
		slotAcquireTotal = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmgw_fpslot_acquire_total",
				Help: "Total number of fingerprint slot acquire attempts",
			},
			[]string{"outcome"}, // success, saturated, redis_error
		)

		slotAcquireFailures = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmgw_fpslot_acquire_failures_total",
				Help: "Total number of fingerprint slot acquire failures by reason",
			},
			[]string{"reason"}, // saturated, redis_error, script_error
		)

		slotReleaseTotal = prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "llmgw_fpslot_release_total",
				Help: "Total number of fingerprint slot release calls",
			},
		)

		slotReleaseFailures = prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "llmgw_fpslot_release_failures_total",
				Help: "Total number of fingerprint slot release failures",
			},
		)

		slotUtilization = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llmgw_fpslot_utilization_ratio",
				Help: "Fingerprint slot utilization ratio (used/limit) per credential",
			},
			[]string{"credential_id"},
		)

		slotSaturationEvents = prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "llmgw_fpslot_saturation_events_total",
				Help: "Total number of slot saturation events (all slots occupied)",
			},
		)

		slotPreemptEvents = prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "llmgw_fpslot_preempt_events_total",
				Help: "Total number of slot preemption events (LRU eviction)",
			},
		)

		slotReclaimEvents = prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "llmgw_fpslot_reclaim_events_total",
				Help: "Total number of background reclaim events",
			},
		)

		clientTokenRequests = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_client_token_requests_total",
				Help: "Client-token fingerprint slot acquisition outcomes",
			},
			[]string{"tenant_id", "client_type", "outcome"},
		)
		holderChanges = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_client_token_holder_changes_total",
				Help: "Client-token holder changes for one user across client types",
			},
			[]string{"tenant_id", "client_type"},
		)
		activeSlots = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "gateway_client_token_active_slots",
				Help: "Active fingerprint slots grouped by tenant, credential, and client type",
			},
			[]string{"tenant_id", "credential_id", "client_type"},
		)
		unknownRatio = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "gateway_client_token_unknown_ratio",
			Help: "Ratio of client-token requests classified as unknown",
		})
		pinAge = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "gateway_client_token_pin_age_seconds",
				Help:    "Age of reused client-token pins in seconds",
				Buckets: []float64{60, 300, 900, 3600, 21600, 86400},
			},
			[]string{"tenant_id", "client_type"},
		)

		prometheus.MustRegister(
			slotAcquireTotal,
			slotAcquireFailures,
			slotReleaseTotal,
			slotReleaseFailures,
			slotUtilization,
			slotSaturationEvents,
			slotPreemptEvents,
			slotReclaimEvents,
			clientTokenRequests,
			holderChanges,
			activeSlots,
			unknownRatio,
			pinAge,
		)
	})
}

func init() {
	registerMetrics()
}

// recordAcquireSuccess records a successful slot acquisition
func recordAcquireSuccess() {
	if slotAcquireTotal != nil {
		slotAcquireTotal.WithLabelValues("success").Inc()
	}
}

// recordAcquireSaturated records a slot acquisition failure due to saturation
func recordAcquireSaturated() {
	if slotAcquireTotal != nil {
		slotAcquireTotal.WithLabelValues("saturated").Inc()
	}
	if slotAcquireFailures != nil {
		slotAcquireFailures.WithLabelValues("saturated").Inc()
	}
	if slotSaturationEvents != nil {
		slotSaturationEvents.Inc()
	}
}

// recordAcquireRedisError records a slot acquisition failure due to Redis error
func recordAcquireRedisError() {
	if slotAcquireTotal != nil {
		slotAcquireTotal.WithLabelValues("redis_error").Inc()
	}
	if slotAcquireFailures != nil {
		slotAcquireFailures.WithLabelValues("redis_error").Inc()
	}
}

// recordReleaseSuccess records a successful slot release
func recordReleaseSuccess() {
	if slotReleaseTotal != nil {
		slotReleaseTotal.Inc()
	}
}

// recordReleaseFailure records a slot release failure
func recordReleaseFailure() {
	if slotReleaseFailures != nil {
		slotReleaseFailures.Inc()
	}
}

// recordPreempt records a slot preemption event
func recordPreempt() {
	if slotPreemptEvents != nil {
		slotPreemptEvents.Inc()
	}
}

// recordReclaim records a background reclaim event
func recordReclaim() {
	if slotReclaimEvents != nil {
		slotReclaimEvents.Inc()
	}
}

// updateUtilization updates the slot utilization gauge for a credential
func updateUtilization(credentialID int, used, limit int) {
	if slotUtilization != nil && limit > 0 {
		ratio := float64(used) / float64(limit)
		slotUtilization.WithLabelValues(strconv.Itoa(credentialID)).Set(ratio)
	}
}

func recordClientTokenRequest(tenantID, holder, outcome string) {
	clientType, userKey := splitClientToken(holder)
	key := tenantID + "\x00" + userKey
	clientTokenStateMu.Lock()
	clientTokenTotals++
	if clientType == "unknown" {
		clientTokenUnknown++
	}
	if previous, ok := clientTokenHolders[key]; ok && previous != clientType {
		holderChanges.WithLabelValues(tenantID, clientType).Inc()
	}
	clientTokenHolders[key] = clientType
	ratio := float64(clientTokenUnknown) / float64(clientTokenTotals)
	unknownRatio.Set(ratio)
	clientTokenStateMu.Unlock()

	clientTokenRequests.WithLabelValues(tenantID, clientType, outcome).Inc()
}

func recordClientTokenPinAge(tenantID, holder string, ageSeconds float64) {
	clientType, _ := splitClientToken(holder)
	pinAge.WithLabelValues(tenantID, clientType).Observe(ageSeconds)
}

func setClientTokenActiveSlots(tenantID string, credentialID int, counts map[string]int) {
	key := tenantID + "\x00" + strconv.Itoa(credentialID)
	clientTokenStateMu.Lock()
	known, ok := knownClientTypes[key]
	if !ok {
		known = make(map[string]struct{})
		knownClientTypes[key] = known
	}
	for clientType := range counts {
		known[clientType] = struct{}{}
	}
	clientTypes := make([]string, 0, len(known))
	for clientType := range known {
		clientTypes = append(clientTypes, clientType)
	}
	clientTokenStateMu.Unlock()

	for _, clientType := range clientTypes {
		activeSlots.WithLabelValues(tenantID, strconv.Itoa(credentialID), clientType).Set(float64(counts[clientType]))
	}
}

func splitClientToken(holder string) (clientType, userKey string) {
	parts := strings.SplitN(holder, "|", 2)
	if len(parts) != 2 {
		return "unknown", holder
	}
	if parts[1] == "" {
		return "unknown", parts[0]
	}
	return normalizeMetricClientType(parts[1]), parts[0]
}

func normalizeMetricClientType(clientType string) string {
	switch clientType {
	case "cursor", "claude-code", "opencode", "zcode", "codex", "roocode",
		"vscode", "copilot", "windsurf", "zed", "jetbrains", "unknown":
		return clientType
	default:
		return "unknown"
	}
}
