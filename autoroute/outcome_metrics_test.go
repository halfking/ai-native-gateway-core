package autoroute

// outcome_metrics_test.go — 2026-09-09 audit round 3: proves the five
// RoutingOutcomeStats counters are exported as
// llmgw_autoroute_outcome_total{result=stashed|matched|orphan|expired|dropped}
// with scrape-time values read from the same atomics, one series per fixed
// label value (GW-00: no request-level dimensions).
//
// The registry counters are package-level and other tests in this package
// bump them concurrently, so assertions are monotonic (>=) plus a
// deadline-bounded advance check — never exact equality across a Collect.

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// collectOutcomeMetrics runs one Collect pass on the collector and maps
// result label value → counter value.
func collectOutcomeMetrics(t *testing.T) map[string]float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, len(outcomeResultLabels))
	go func() {
		outcomeStatsCollector{}.Collect(ch)
		close(ch)
	}()
	out := map[string]float64{}
	for m := range ch {
		var dtoMetric dto.Metric
		if err := m.Write(&dtoMetric); err != nil {
			t.Fatalf("write metric: %v", err)
		}
		if dtoMetric.GetCounter() == nil {
			t.Fatalf("expected counter value on series %v", dtoMetric.GetLabel())
		}
		labels := dtoMetric.GetLabel()
		if len(labels) != 1 || labels[0].GetName() != "result" {
			t.Fatalf("expected exactly one 'result' label, got %v", labels)
		}
		out[labels[0].GetValue()] = dtoMetric.GetCounter().GetValue()
	}
	return out
}

func TestOutcomeMetrics_SeriesLayoutAndScrapeTimeRead(t *testing.T) {
	registerOutcomeMetrics() // idempotent; normally done by init

	resetOutcomeRegistry(t)
	d, _ := newFeedbackTestDecider(t)

	// One decision stashed under a request id, then its real outcome reported
	// → matched +1. One orphan outcome with no stashed decision → orphan +1.
	ctx := WithRequestID(context.Background(), "req-metrics-match")
	if _, err := d.Decide(ctx, ClassificationSignals{}, 42, "", "", "sess-metrics"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
	ReportRoutingOutcome(RoutingOutcome{RequestID: "req-metrics-match", Success: true})
	ReportRoutingOutcome(RoutingOutcome{RequestID: "req-metrics-orphan", Success: true})

	stashed, matched, orphan, _, _ := RoutingOutcomeStats()
	if stashed == 0 || matched == 0 || orphan == 0 {
		t.Fatalf("precondition: expected stashed/matched/orphan activity, got %d/%d/%d",
			stashed, matched, orphan)
	}

	metrics := collectOutcomeMetrics(t)
	if len(metrics) != len(outcomeResultLabels) {
		t.Fatalf("expected exactly %d series, got %d: %v",
			len(outcomeResultLabels), len(metrics), metrics)
	}
	snapshot := map[string]int64{
		"stashed": stashed, "matched": matched, "orphan": orphan,
	}
	for label, snapVal := range snapshot {
		gotVal, ok := metrics[label]
		if !ok {
			t.Fatalf("missing series for result=%q", label)
		}
		// Scrape-time read of the same atomics: never below the snapshot.
		// Concurrent tests may push it higher between the two reads.
		if gotVal < float64(snapVal) {
			t.Errorf("result=%q: collected %v below snapshot %v", label, gotVal, snapVal)
		}
	}
	for _, label := range outcomeResultLabels {
		if _, ok := metrics[label]; !ok {
			t.Errorf("fixed label value result=%q not exported", label)
		}
	}
}

func TestOutcomeMetrics_AdvancesOnScrape(t *testing.T) {
	registerOutcomeMetrics()

	resetOutcomeRegistry(t)
	d, _ := newFeedbackTestDecider(t)

	ctx := WithRequestID(context.Background(), "req-metrics-advance")
	if _, err := d.Decide(ctx, ClassificationSignals{}, 42, "", "", "sess-metrics-advance"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	// The report happens after the baseline read; the next scrape must
	// observe matched ≥ baseline+1 (deadline-bounded, monotonic under
	// concurrent writers).
	baselineMatched := collectOutcomeMetrics(t)["matched"]
	ReportRoutingOutcome(RoutingOutcome{RequestID: "req-metrics-advance", Success: true})

	deadline := time.Now().Add(2 * time.Second)
	for {
		got := collectOutcomeMetrics(t)["matched"]
		if got >= baselineMatched+1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("matched did not advance on scrape: baseline %v, latest %v",
				baselineMatched, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOutcomeMetrics_RegisteredInDefaultRegistry(t *testing.T) {
	registerOutcomeMetrics()
	// MustRegister on an already-registered collector panics; a clean Gather
	// over the default registry proves the init-time registration stuck.
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather default registry: %v", err)
	}
	var mf *dto.MetricFamily
	for _, f := range families {
		if f.GetName() == outcomeMetricName {
			mf = f
			break
		}
	}
	if mf == nil {
		t.Fatalf("metric %s not found in default registry", outcomeMetricName)
	}
	if len(mf.GetMetric()) == 0 {
		t.Fatalf("metric %s has no series; collector Collect returned nothing", outcomeMetricName)
	}
	// Exactly five fixed label values; the orphan dimension must be
	// individually identifiable.
	seen := map[string]bool{}
	for _, m := range mf.GetMetric() {
		for _, l := range m.GetLabel() {
			if l.GetName() == "result" {
				seen[l.GetValue()] = true
			}
		}
	}
	for _, label := range outcomeResultLabels {
		if !seen[label] {
			t.Errorf("result=%q series missing from registry gather", label)
		}
	}
	if len(seen) != len(outcomeResultLabels) {
		t.Errorf("unexpected extra label values: %v", seen)
	}
}
