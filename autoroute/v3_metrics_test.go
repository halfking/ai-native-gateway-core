package autoroute

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// approxEqual compares floats with a small absolute tolerance, enough for
// summed dollar amounts in tests.
func approxEqual(got, want float64) bool {
	const epsilon = 1e-9
	return got > want-epsilon && got < want+epsilon
}

// TestV3MetricsCollectibleInIsolatedRegistry verifies the acceptance criterion
// that all V3 metrics can be collected from a registry other than the process
// default one.
func TestV3MetricsCollectibleInIsolatedRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newV3Metrics(reg)

	m.recordClassification(&Classification{Primary: TaskType("reasoning")}, "heuristic", "direct", 5*time.Millisecond)
	m.recordCache(true)
	m.recordCache(false)
	m.recordCost("standard", 0.01, 0.03)
	m.recordFeedback("reasoning", true)

	want := map[string]int{
		"llmgw_autoroute_classification_duration_seconds": 1,
		"llmgw_autoroute_classification_total":           1,
		"llmgw_autoroute_classification_cache_total":     2, // hit + miss
		"llmgw_autoroute_cost_dollars_total":             1,
		"llmgw_autoroute_cost_saved_dollars_total":       1,
		"llmgw_autoroute_classification_feedback_total":  1,
	}
	for name, expected := range want {
		count, err := testutil.GatherAndCount(reg, name)
		if err != nil {
			t.Fatalf("gather %s: %v", name, err)
		}
		if count != expected {
			t.Errorf("metric %s: expected %d series in isolated registry, got %d", name, expected, count)
		}
	}
}

func TestV3MetricsRecordSemantics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newV3Metrics(reg)

	t.Run("classification duration and total", func(t *testing.T) {
		m.recordClassification(&Classification{Primary: TaskType("code")}, "heuristic", "cache", 10*time.Millisecond)
		if got := testutil.ToFloat64(m.classificationTotal.WithLabelValues("code", "heuristic")); got != 1 {
			t.Errorf("classification_total{code,heuristic} = %v, want 1", got)
		}
		mfs, err := reg.Gather()
		if err != nil {
			t.Fatalf("gather: %v", err)
		}
		for _, mf := range mfs {
			if mf.GetName() != "llmgw_autoroute_classification_duration_seconds" {
				continue
			}
			for _, metric := range mf.GetMetric() {
				if metric.GetLabel()[0].GetValue() != "heuristic" {
					continue
				}
				if got := metric.GetHistogram().GetSampleCount(); got != 1 {
					t.Errorf("classification_duration_seconds{heuristic,cache} sample count = %d, want 1", got)
				}
			}
		}
	})

	t.Run("nil result is ignored", func(t *testing.T) {
		before, err := testutil.GatherAndCount(reg)
		if err != nil {
			t.Fatalf("gather before: %v", err)
		}
		m.recordClassification(nil, "heuristic", "cache", time.Millisecond)
		after, err := testutil.GatherAndCount(reg)
		if err != nil {
			t.Fatalf("gather after: %v", err)
		}
		if after != before {
			t.Errorf("nil classification changed series count: before=%d after=%d", before, after)
		}
	})

	t.Run("cache hit and miss outcomes", func(t *testing.T) {
		m.recordCache(true)
		m.recordCache(true)
		m.recordCache(false)
		if got := testutil.ToFloat64(m.classificationCache.WithLabelValues("hit")); got != 2 {
			t.Errorf("cache outcome hit = %v, want 2", got)
		}
		if got := testutil.ToFloat64(m.classificationCache.WithLabelValues("miss")); got != 1 {
			t.Errorf("cache outcome miss = %v, want 1", got)
		}
	})

	t.Run("cost accounting", func(t *testing.T) {
		m.recordCost("standard", 0.02, 0.05)
		m.recordCost("standard", 0.01, 0.00) // no saving, still counted as spend
		if got := testutil.ToFloat64(m.costTotal.WithLabelValues("standard")); !approxEqual(got, 0.03) {
			t.Errorf("cost_dollars_total{standard} = %v, want ~0.03", got)
		}
		if got := testutil.ToFloat64(m.costSavedTotal); !approxEqual(got, 0.03) {
			t.Errorf("cost_saved_dollars_total = %v, want ~0.03", got)
		}
	})

	t.Run("negative actual cost is skipped", func(t *testing.T) {
		m.recordCost("standard", -1, 0.10)
		if got := testutil.ToFloat64(m.costTotal.WithLabelValues("standard")); !approxEqual(got, 0.03) {
			t.Errorf("cost_dollars_total{standard} = %v, want unchanged ~0.03", got)
		}
		// baseline - actual with negative actual would inflate savings; it must not.
		if got := testutil.ToFloat64(m.costSavedTotal); !approxEqual(got, 0.03) {
			t.Errorf("cost_saved_dollars_total = %v, want unchanged ~0.03", got)
		}
	})

	t.Run("feedback labels", func(t *testing.T) {
		m.recordFeedback("reasoning", true)
		m.recordFeedback("reasoning", false)
		if got := testutil.ToFloat64(m.feedbackTotal.WithLabelValues("reasoning", "true")); got != 1 {
			t.Errorf("feedback{reasoning,true} = %v, want 1", got)
		}
		if got := testutil.ToFloat64(m.feedbackTotal.WithLabelValues("reasoning", "false")); got != 1 {
			t.Errorf("feedback{reasoning,false} = %v, want 1", got)
		}
	})

	t.Run("package-level helpers are nil-safe", func(t *testing.T) {
		// The package-level wrappers delegate to the default-registry
		// instance; invoking them must not panic.
		recordClassificationMetrics(&Classification{Primary: TaskType("creative")}, "heuristic", "direct", time.Millisecond)
		recordClassificationCacheMetric(true)
		recordCostMetrics("lite", 0.001, 0.002)
		recordClassificationFeedback("creative", true)
	})
}

func TestNewV3MetricsNilRegistryDoesNotRegister(t *testing.T) {
	m := newV3Metrics(nil)
	if m == nil {
		t.Fatal("newV3Metrics(nil) returned nil")
	}
	// Recording against an unregistered instance must still work (the
	// collectors exist; they are simply not scraped anywhere).
	m.recordCache(true)
	if got := testutil.ToFloat64(m.classificationCache.WithLabelValues("hit")); got != 1 {
		t.Errorf("unregistered instance cache hit = %v, want 1", got)
	}
}

func TestV3MetricNamesUseAutoroutePrefix(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newV3Metrics(reg)
	// Vec metrics only expose a family once a series exists, so touch one
	// series of each; the plain Counter always appears.
	m.recordClassification(&Classification{Primary: TaskType("qa")}, "heuristic", "direct", time.Millisecond)
	m.recordCache(false)
	m.recordCost("lite", 0.001, 0.002)
	m.recordFeedback("qa", true)

	want := map[string]bool{
		"llmgw_autoroute_classification_duration_seconds": false,
		"llmgw_autoroute_classification_total":           false,
		"llmgw_autoroute_classification_cache_total":     false,
		"llmgw_autoroute_cost_dollars_total":             false,
		"llmgw_autoroute_cost_saved_dollars_total":       false,
		"llmgw_autoroute_classification_feedback_total":  false,
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if len(mfs) == 0 {
		t.Fatal("isolated registry gathered no metric families; fixed-name counters should always be present")
	}
	for _, mf := range mfs {
		if _, ok := want[mf.GetName()]; !ok {
			t.Errorf("unexpected metric family %s", mf.GetName())
		}
		want[mf.GetName()] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("metric %s missing from isolated registry gather", name)
		}
	}
}
