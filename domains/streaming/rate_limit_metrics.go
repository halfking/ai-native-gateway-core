package streaming

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var gatewayRateLimitRejectionsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llm_gateway_rate_limit_rejections_total",
		Help: "Gateway API-key rate-limit rejections by admission reason.",
	},
	[]string{"reason"},
)

func recordGatewayRateLimitRejection(outcome rateLimitOutcome) {
	if !outcome.Blocked {
		return
	}
	reason := outcome.Reason
	if reason == "" {
		reason = "unknown"
	}
	gatewayRateLimitRejectionsTotal.WithLabelValues(reason).Inc()
}
