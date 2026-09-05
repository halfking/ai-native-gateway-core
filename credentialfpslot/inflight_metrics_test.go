package credentialfpslot

import (
	"context"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

func gaugeValue(t *testing.T, gauge interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := gauge.Write(metric); err != nil {
		t.Fatalf("write gauge: %v", err)
	}
	return metric.GetGauge().GetValue()
}

func TestFiniteLeaseInFlightGaugeBalancesIdempotentRelease(t *testing.T) {
	manager, _ := newTestManager(t, Config{Enabled: true, DefaultLimit: 1})
	ctx := context.Background()
	before := gaugeValue(t, inFlightLeases.WithLabelValues("tenant-inflight", "91"))
	limit := 1
	lease := acquireSuccess(t, manager, ctx, 91, &limit, "holder", "tenant-inflight")
	if got := gaugeValue(t, inFlightLeases.WithLabelValues("tenant-inflight", "91")); got != before+1 {
		t.Fatalf("in-flight gauge after acquire = %v, want %v", got, before+1)
	}

	manager.Release(ctx, lease)
	manager.Release(ctx, lease)
	if got := gaugeValue(t, inFlightLeases.WithLabelValues("tenant-inflight", "91")); got != before {
		t.Fatalf("in-flight gauge after idempotent release = %v, want %v", got, before)
	}
}
