package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// StreamRecoveryL2ShadowTotal records observational L2 prefix-alignment
// verdicts. It is deliberately limited to protocol and a fixed result enum:
// request, tenant, provider, model, and error identifiers are not labels.
//
// Shadow mode does not alter recovery behavior. A result of unavailable means
// no ordinary follow-up attempt existed to evaluate; it is not an alignment
// miss and must be reported separately from the evaluable sample set.
var StreamRecoveryL2ShadowTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "gateway",
	Subsystem: "survival",
	Name:      "l2_shadow_total",
	Help:      "L2 shadow prefix-alignment verdicts from pre-existing recovery attempts.",
}, []string{"protocol", "result"})

const (
	StreamRecoveryL2ShadowResultAligned        = "aligned"
	StreamRecoveryL2ShadowResultMiss           = "miss"
	StreamRecoveryL2ShadowResultUnavailable    = "unavailable"
	StreamRecoveryL2ShadowResultUndecided      = "undecided"
	StreamRecoveryL2ShadowResultBufferOverflow = "buffer_overflow"
)

var streamRecoveryL2ShadowResults = [...]string{
	StreamRecoveryL2ShadowResultAligned,
	StreamRecoveryL2ShadowResultMiss,
	StreamRecoveryL2ShadowResultUnavailable,
	StreamRecoveryL2ShadowResultUndecided,
	StreamRecoveryL2ShadowResultBufferOverflow,
}

var streamRecoveryL2ShadowProtocols = map[string]bool{
	"openai_chat":      true,
	"openai_responses": true,
	"anthropic":        true,
}

func init() {
	for protocol := range streamRecoveryL2ShadowProtocols {
		for _, result := range streamRecoveryL2ShadowResults {
			StreamRecoveryL2ShadowTotal.WithLabelValues(protocol, result).Add(0)
		}
	}
	for _, result := range streamRecoveryL2ShadowResults {
		StreamRecoveryL2ShadowTotal.WithLabelValues("unknown", result).Add(0)
	}
}

// RecordStreamRecoveryL2Shadow increments the low-cardinality shadow verdict
// counter. Unknown inputs are normalized rather than becoming new series.
func RecordStreamRecoveryL2Shadow(protocol, result string) {
	if !streamRecoveryL2ShadowProtocols[protocol] {
		protocol = "unknown"
	}
	if !isStreamRecoveryL2ShadowResult(result) {
		result = StreamRecoveryL2ShadowResultUndecided
	}
	StreamRecoveryL2ShadowTotal.WithLabelValues(protocol, result).Inc()
}

func isStreamRecoveryL2ShadowResult(result string) bool {
	for _, allowed := range streamRecoveryL2ShadowResults {
		if result == allowed {
			return true
		}
	}
	return false
}
