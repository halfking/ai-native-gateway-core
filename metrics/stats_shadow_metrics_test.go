package metrics

import (
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

func TestStatsShadowMetrics(t *testing.T) {
	readCounter := func(counter interface{ Write(*dto.Metric) error }) float64 {
		metric := &dto.Metric{}
		if err := counter.Write(metric); err != nil {
			t.Fatalf("counter.Write: %v", err)
		}
		return metric.GetCounter().GetValue()
	}

	comparison := statsShadowComparisons.WithLabelValues("usage_summary", "exact")
	beforeComparison := readCounter(comparison)
	RecordStatsShadowComparison("usage_summary", "exact")
	if got := readCounter(comparison); got != beforeComparison+1 {
		t.Fatalf("comparison count = %v, want %v", got, beforeComparison+1)
	}

	dropped := statsShadowDropped.WithLabelValues("usage_summary")
	beforeDropped := readCounter(dropped)
	RecordStatsShadowDropped("usage_summary")
	if got := readCounter(dropped); got != beforeDropped+1 {
		t.Fatalf("dropped count = %v, want %v", got, beforeDropped+1)
	}

	ObserveStatsShadowDuration("usage_summary", time.Millisecond)
}
