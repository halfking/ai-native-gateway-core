package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

// TestNodeStatePrefetchDropped_ClosedEnum pins the label contract: reason is a
// two-value closed set, and anything else lands in "unknown" rather than
// creating a new series.
//
// Why it matters here specifically: the drop reason is computed from state
// fields, and a future refactor that passes, say, a model name or an error
// string as the reason would silently explode cardinality. The project's
// cardinality guard (TestNoHighCardinalityLabels) only checks LABEL NAMES, not
// label VALUES, so nothing else would catch it.
func TestNodeStatePrefetchDropped_ClosedEnum(t *testing.T) {
	before := testutil.ToFloat64(NodeStatePrefetchDroppedTotal.WithLabelValues(PrefetchDropReasonStale))

	RecordNodeStatePrefetchDropped(PrefetchDropReasonStale)
	RecordNodeStatePrefetchDropped(PrefetchDropReasonUnstamped)
	// Values that must NOT become their own series.
	RecordNodeStatePrefetchDropped("gpt-5.6-terra")
	RecordNodeStatePrefetchDropped("credential-126")
	RecordNodeStatePrefetchDropped("")

	after := testutil.ToFloat64(NodeStatePrefetchDroppedTotal.WithLabelValues(PrefetchDropReasonStale))
	if after != before+1 {
		t.Fatalf("stale counter moved %v -> %v, want +1", before, after)
	}

	unknown := testutil.ToFloat64(NodeStatePrefetchDroppedTotal.WithLabelValues("unknown"))
	if unknown < 3 {
		t.Fatalf("unknown bucket = %v, want >= 3: every out-of-enum reason must be "+
			"normalised into it, not create a new series", unknown)
	}
}

// TestNodeStatePrefetchDropped_Preheated pins that both enum series exist at
// 0 from process start, so a dashboard's numerator is not "absent until the
// first event".
//
// ⚠️ Asserts on "the series is DISCOVERABLE", not on "its value is 0": the
// collectors are package-level, so a sibling test in this package that bumps
// the counter makes an absolute-zero assertion order-dependent. The property
// that matters for a dashboard is that the series resolves at all — and
// WithLabelValues panics on an undeclared label, so a successful call IS the
// assertion. A previous cut of this test asserted ==0 and failed purely
// because of test ordering.
func TestNodeStatePrefetchDropped_Preheated(t *testing.T) {
	for _, reason := range []string{PrefetchDropReasonStale, PrefetchDropReasonUnstamped} {
		c := NodeStatePrefetchDroppedTotal.WithLabelValues(reason)
		if c == nil {
			t.Fatalf("reason=%s did not resolve to a series", reason)
		}
		var m dto.Metric
		if err := c.Write(&m); err != nil {
			t.Fatalf("reason=%s is not writable (not a registered counter): %v", reason, err)
		}
		if m.GetCounter() == nil {
			t.Fatalf("reason=%s did not yield a counter sample", reason)
		}
	}
}

// TestNodeStatePrefetchAge_RejectsNegative guards the one place the histogram
// could be fed a nonsense value: a negative age would silently corrupt the
// cumulative buckets and make every quantile downstream meaningless.
//
// Compares the counter DELTA across the call rather than an absolute count:
// the histogram is package-level, so absolute assertions here would be
// order-dependent against sibling tests in this package.
func TestNodeStatePrefetchAge_RejectsNegative(t *testing.T) {
	read := func() uint64 {
		var m dto.Metric
		h := NodeStatePrefetchAgeSeconds.(prometheus.Histogram)
		if err := h.Write(&m); err != nil {
			t.Fatalf("write: %v", err)
		}
		return m.GetHistogram().GetSampleCount()
	}
	before := read()
	ObserveNodeStatePrefetchAge(-1)
	if after := read(); after != before {
		t.Fatalf("negative observation was recorded: sample count %d -> %d", before, after)
	}
	ObserveNodeStatePrefetchAge(0.006)
	if after := read(); after != before+1 {
		t.Fatalf("a non-negative observation was dropped: sample count %d -> %d, want %d",
			before, after, before+1)
	}
}

// TestNodeStatePrefetch_BucketsCoverTheGuard pins the bucket layout against the
// production guard bound.
//
// A histogram whose buckets all sit ABOVE the guard cannot show a sample
// approaching the guard, which is exactly the signal needed to retune it. The
// guard is 5s today; the top bucket must therefore exceed 5s, and the bottom
// must be far below the ~6ms typical gap or every steady-state sample would
// collapse into the first bucket and the distribution would be unreadable.
func TestNodeStatePrefetch_BucketsCoverTheGuard(t *testing.T) {
	var m dto.Metric
	h, ok := NodeStatePrefetchAgeSeconds.(prometheus.Histogram)
	if !ok {
		t.Fatalf("NodeStatePrefetchAgeSeconds is %T, want a prometheus.Histogram",
			NodeStatePrefetchAgeSeconds)
	}
	if err := h.Write(&m); err != nil {
		t.Fatalf("write: %v", err)
	}
	bs := m.GetHistogram().GetBucket()
	if len(bs) == 0 {
		t.Fatal("no buckets declared")
	}
	lo := bs[0].GetUpperBound()
	hi := bs[len(bs)-1].GetUpperBound()
	t.Logf("age histogram buckets: %.4fs .. %.2fs（%d 档）", lo, hi, len(bs))
	if lo > 0.01 {
		t.Fatalf("lowest bucket %.4fs is too high: the common router->gate gap is "+
			"~6ms, so steady-state samples would all collapse into bucket 1", lo)
	}
	if hi <= 5 {
		t.Fatalf("highest bucket %.2fs does not exceed the 5s guard: a histogram "+
			"capped at/below the guard cannot show samples approaching it", hi)
	}
}
