package streaming

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus metrics for the pre-streamed overload exhaustion path.
//
// 2026-08-08: added so that an empty-200 (pre-streamed request that exhausts
// its retries) is observable from /metrics without log scraping. Operators
// can now set a real alert on a sudden rise of empty-200s, instead of
// noticing only after a client reports empty replies. The counter is the
// minimum useful signal: per-credential label so the empty-200 can be
// attributed to a specific provider/credential pair.
var (
	// prewarmedExhaustionFramesTotal counts pre-streamed requests that
	// exhausted per-credential retries and wrote a SSE error frame.
	// Labels: real_kind (the actual upstream error kind, e.g.
	// "upstream_overloaded") and code (the historical envelope code,
	// always "model_not_found" for the exhaustion branch).
	prewarmedExhaustionFramesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_prewarmed_exhaustion_frames_total",
			Help: "Total pre-streamed SSE error frames written by the exhausted-all-candidates branch (handler.go:3707).",
		},
		[]string{"real_kind", "code"},
	)

	// prewarmedExhaustionByCredential counts the same frames, attributed
	// to the upstream credential so operators can spot a single
	// provider's degradation even when the gateway aggregate looks fine.
	prewarmedExhaustionByCredential = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_prewarmed_exhaustion_by_credential_total",
			Help: "Pre-streamed exhaustion frames grouped by upstream credential for per-provider attribution.",
		},
		[]string{"provider_id", "credential_id", "model"},
	)
)

// recordPrewarmedExhaustion is the single integration point for the two
// counters above. Called from handler.go:3707 exactly once per exhausted
// pre-streamed request.
//
// reason is the .LastKind on the surface error (e.g. "upstream_overloaded",
// "rate_limit", "concurrent"); code is the historical envelope code, which
// handler.go:3707 currently always sets to "model_not_found" — that is the
// "code-vs-kind mismatch" this fix exposed, so the two are tracked
// separately.
func recordPrewarmedExhaustion(reason, code, providerID, credentialID, model string) {
	prewarmedExhaustionFramesTotal.WithLabelValues(reason, code).Inc()
	prewarmedExhaustionByCredential.WithLabelValues(providerID, credentialID, model).Inc()
}
