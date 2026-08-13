package dispatch

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestStageSecondsHelpers(t *testing.T) {
	t.Parallel()

	now := time.Now()
	later := now.Add(150 * time.Millisecond)
	earlier := now.Add(-10 * time.Millisecond)

	if _, ok := stageSeconds(nil, &later); ok {
		t.Fatal("nil start should be false")
	}
	if _, ok := stageSeconds(&now, nil); ok {
		t.Fatal("nil end should be false")
	}
	if _, ok := stageSeconds(&now, &earlier); ok {
		t.Fatal("negative duration should be false")
	}
	s, ok := stageSeconds(&now, &later)
	if !ok {
		t.Fatal("expected ok")
	}
	if s < 0.1 || s > 0.3 {
		t.Fatalf("stageSeconds got %v, want ~0.15", s)
	}

	s, ok = stageSecondsFrom(now, &later)
	if !ok || s < 0.1 || s > 0.3 {
		t.Fatalf("stageSecondsFrom got %v ok=%v", s, ok)
	}
	if _, ok := stageSecondsFrom(time.Time{}, &later); ok {
		t.Fatal("zero start should be false")
	}
}

func TestResultLabel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		out  ForwardOutcome
		want string
	}{
		{"success", ForwardOutcome{}, "success"},
		{"shutdown", ForwardOutcome{Err: ErrShutdown}, "shutdown"},
		{"pre", ForwardOutcome{Err: errors.New("boom")}, "fail_prefirstbyte"},
		{"post", ForwardOutcome{Err: errors.New("boom"), BytesSent: true}, "fail_postfirstbyte"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resultLabel(tc.out); got != tc.want {
				t.Fatalf("resultLabel=%q want %q", got, tc.want)
			}
		})
	}
}

func TestGetStreamingDuration(t *testing.T) {
	t.Parallel()

	qr := NewQueuedRequest("id", "t", "m", nil, nil)
	if d := qr.GetStreamingDuration(); d != 0 {
		t.Fatalf("empty streaming duration = %v", d)
	}
	start := time.Now()
	end := start.Add(2 * time.Second)
	qr.T8_ResponseStartAt = &start
	qr.T9_ResponseEndAt = &end
	if d := qr.GetStreamingDuration(); d < 2*time.Second || d > 2*time.Second+time.Millisecond {
		t.Fatalf("GetStreamingDuration=%v", d)
	}
}

func TestRecordStageMetricsObservesHistograms(t *testing.T) {
	// Not parallel: mutates global promauto metrics.

	beforeTotal := histogramSampleCount(t, metricStageTotalT0T9, "success")
	beforeQueue := histogramSampleCount(t, metricStageQueueWaitT0T6, "success")
	beforeUp := histogramSampleCount(t, metricStageUpstreamT7T8, "success")
	beforeStream := histogramSampleCount(t, metricStageStreamingT8T9, "success")
	beforeModel := histogramSampleCount(t, metricStageModelQueueT3T4, "success")
	beforeCred := histogramSampleCount(t, metricStageCredQueueT5T6, "success")
	beforeRouting := histogramSampleCount(t, metricStageRoutingT2T5, "success")
	beforeAcquire := histogramSampleCount(t, metricStageAcquireT6T7, "success")
	beforeTotalQ := histogramSampleCount(t, metricStageTotalQueueT1T2, "success")

	qr := NewQueuedRequest("req-stage", "tenant", "gpt-test", nil, nil)
	t0 := qr.T0_ArrivedAt
	t1 := t0.Add(10 * time.Millisecond)
	t2 := t1.Add(40 * time.Millisecond)
	t3 := t2.Add(2 * time.Millisecond)
	t4 := t3.Add(8 * time.Millisecond)
	t5 := t4.Add(2 * time.Millisecond)
	t6 := t5.Add(50 * time.Millisecond)
	t7 := t6.Add(1 * time.Millisecond)
	t8 := t7.Add(48 * time.Millisecond)
	t9 := t8.Add(1850 * time.Millisecond)

	qr.T1_TotalEnqueuedAt = &t1
	qr.T2_TotalDequeuedAt = &t2
	qr.T3_ModelEnqueuedAt = &t3
	qr.T4_ModelDequeuedAt = &t4
	qr.T5_CredEnqueuedAt = &t5
	qr.T6_CredDequeuedAt = &t6
	qr.T7_ForwardStartAt = &t7
	qr.T8_ResponseStartAt = &t8
	qr.T9_ResponseEndAt = &t9

	observeStageMetrics(qr, ForwardOutcome{})

	assertCountInc(t, "total T0T9", metricStageTotalT0T9, "success", beforeTotal)
	assertCountInc(t, "queue T0T6", metricStageQueueWaitT0T6, "success", beforeQueue)
	assertCountInc(t, "upstream T7T8", metricStageUpstreamT7T8, "success", beforeUp)
	assertCountInc(t, "streaming T8T9", metricStageStreamingT8T9, "success", beforeStream)
	assertCountInc(t, "model T3T4", metricStageModelQueueT3T4, "success", beforeModel)
	assertCountInc(t, "cred T5T6", metricStageCredQueueT5T6, "success", beforeCred)
	assertCountInc(t, "routing T2T5", metricStageRoutingT2T5, "success", beforeRouting)
	assertCountInc(t, "acquire T6T7", metricStageAcquireT6T7, "success", beforeAcquire)
	assertCountInc(t, "total-queue T1T2", metricStageTotalQueueT1T2, "success", beforeTotalQ)

	// Sum sanity: known deltas.
	if sum := histogramSampleSum(t, metricStageQueueWaitT0T6, "success"); sum < 0.05 {
		t.Fatalf("queue wait sum=%v, want >= 0.05 (~T0→T6)", sum)
	}
	if sum := histogramSampleSum(t, metricStageUpstreamT7T8, "success"); sum < 0.04 {
		t.Fatalf("upstream sum=%v, want >= 0.04 (~48ms)", sum)
	}
	if sum := histogramSampleSum(t, metricStageStreamingT8T9, "success"); sum < 1.5 {
		t.Fatalf("streaming sum=%v, want >= 1.5 (~1850ms)", sum)
	}
}

func TestRecordStageMetricsSkipsMissingTimestamps(t *testing.T) {
	beforeUp := histogramSampleCount(t, metricStageUpstreamT7T8, "shutdown")
	beforeTotal := histogramSampleCount(t, metricStageTotalT0T9, "shutdown")

	qr := NewQueuedRequest("req-partial", "tenant", "m", nil, nil)
	end := qr.T0_ArrivedAt.Add(5 * time.Millisecond)
	qr.T9_ResponseEndAt = &end

	observeStageMetrics(qr, ForwardOutcome{Err: ErrShutdown})

	afterUp := histogramSampleCount(t, metricStageUpstreamT7T8, "shutdown")
	if afterUp != beforeUp {
		t.Fatalf("upstream should stay %v, got %v", beforeUp, afterUp)
	}
	afterTotal := histogramSampleCount(t, metricStageTotalT0T9, "shutdown")
	if afterTotal != beforeTotal+1 {
		t.Fatalf("total under shutdown count %v → %v, want +1", beforeTotal, afterTotal)
	}
}

func assertCountInc(t *testing.T, name string, hv *prometheus.HistogramVec, result string, before float64) {
	t.Helper()
	got := histogramSampleCount(t, hv, result)
	if got != before+1 {
		t.Fatalf("%s sample count %v → %v, want +1", name, before, got)
	}
}

func histogramSampleCount(t *testing.T, hv *prometheus.HistogramVec, result string) float64 {
	t.Helper()
	m := collectLabeledHistogram(t, hv, result)
	if m == nil || m.Histogram == nil {
		return 0
	}
	return float64(m.Histogram.GetSampleCount())
}

func histogramSampleSum(t *testing.T, hv *prometheus.HistogramVec, result string) float64 {
	t.Helper()
	m := collectLabeledHistogram(t, hv, result)
	if m == nil || m.Histogram == nil {
		return 0
	}
	return m.Histogram.GetSampleSum()
}

func collectLabeledHistogram(t *testing.T, hv *prometheus.HistogramVec, result string) *dto.Metric {
	t.Helper()
	ch := make(chan prometheus.Metric, 64)
	hv.Collect(ch)
	close(ch)
	for m := range ch {
		pb := &dto.Metric{}
		if err := m.Write(pb); err != nil {
			t.Fatalf("metric.Write: %v", err)
		}
		if pb.GetHistogram() == nil {
			continue
		}
		for _, lp := range pb.GetLabel() {
			if lp.GetName() == "result" && lp.GetValue() == result {
				return pb
			}
		}
	}
	return nil
}
