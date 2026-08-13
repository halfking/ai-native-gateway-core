package credentialfpslot

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
)

func counterValue(t *testing.T, counter interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := counter.Write(metric); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	return metric.GetCounter().GetValue()
}

func TestClientTokenMetrics_RecordOutcomeAndUnknownRatio(t *testing.T) {
	unknownBefore := counterValue(t, clientTokenRequests.WithLabelValues("metrics-tenant", "unknown", "acquired"))
	knownBefore := counterValue(t, clientTokenRequests.WithLabelValues("metrics-tenant", "cursor", "acquired"))

	recordClientTokenRequest("metrics-tenant", "user-a|unknown", "acquired")
	recordClientTokenRequest("metrics-tenant", "user-b|cursor", "acquired")

	if got := counterValue(t, clientTokenRequests.WithLabelValues("metrics-tenant", "unknown", "acquired")); got != unknownBefore+1 {
		t.Fatalf("unknown request counter = %v, want %v", got, unknownBefore+1)
	}
	if got := counterValue(t, clientTokenRequests.WithLabelValues("metrics-tenant", "cursor", "acquired")); got != knownBefore+1 {
		t.Fatalf("known request counter = %v, want %v", got, knownBefore+1)
	}
}

func TestClientTokenMetrics_HolderChange(t *testing.T) {
	before := counterValue(t, holderChanges.WithLabelValues("change-tenant", "cursor"))
	recordClientTokenRequest("change-tenant", "user-c|unknown", "acquired")
	recordClientTokenRequest("change-tenant", "user-c|cursor", "acquired")
	after := counterValue(t, holderChanges.WithLabelValues("change-tenant", "cursor"))
	if after != before+1 {
		t.Fatalf("holder change counter = %v, want %v", after, before+1)
	}
}

func TestClientTokenMetrics_ActiveSlotsTracksClientTypes(t *testing.T) {
	setClientTokenActiveSlots("active-tenant", 42, map[string]int{"cursor": 2, "unknown": 1})

	metric := &dto.Metric{}
	if err := activeSlots.WithLabelValues("active-tenant", "42", "cursor").Write(metric); err != nil {
		t.Fatalf("write active slots metric: %v", err)
	}
	if got := metric.GetGauge().GetValue(); got != 2 {
		t.Fatalf("cursor active slots = %v, want 2", got)
	}
}

func TestNormalizeMetricClientTypeBoundsLabelCardinality(t *testing.T) {
	if got := normalizeMetricClientType("untrusted-client-value"); got != "unknown" {
		t.Fatalf("unrecognized client type = %q, want unknown", got)
	}
}
