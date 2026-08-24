package dispatch

import (
	"context"
	"testing"
	"time"
)

// BenchmarkRequestStageTimestamps measures the per-request cost of the
// ten-stage lifecycle timestamps: construction, stage writes across N
// failover attempts, the waterfall read path, and the detached extraction
// that streaming/executors performs once per completed request.
//
// Baseline before the [10]time.Time storage change (pointer fields):
//
//	attempts=1: 1159 ns/op   1487 B/op   25 allocs/op
//	attempts=3: 1608 ns/op   1727 B/op   35 allocs/op
//
// The attempts sub-benchmark exercises the failover re-enqueue path where
// T3–T7 are rewritten per attempt.
func BenchmarkRequestStageTimestamps(b *testing.B) {
	for _, attempts := range []int{1, 3} {
		b.Run(lifecycleName(attempts), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				qr := NewQueuedRequest("bench-req", "bench-tenant", "model-a", context.Background(), nil)
				runBenchmarkLifecycle(qr, attempts)
			}
		})
	}
}

func lifecycleName(attempts int) string {
	switch attempts {
	case 1:
		return "attempts=1"
	default:
		return "attempts=3"
	}
}

func runBenchmarkLifecycle(qr *QueuedRequest, attempts int) {
	qr.SetT1_TotalEnqueued()
	qr.SetT2_TotalDequeued()
	for i := 0; i < attempts; i++ {
		qr.SetT3_ModelEnqueued()
		qr.SetT4_ModelDequeued()
		qr.SetT5_CredEnqueued()
		qr.SetT6_CredDequeued()
		qr.SetT7_ForwardStart()
	}
	qr.setStage(ReqStageResponseStart, time.Now()) // mirrors journey.go markFirstSemanticByte
	qr.SetT9_ResponseEnd()

	// Read path 1: waterfall serialization (format + duration consumers).
	sinkWaterfall = buildWaterfallRequest(qr, ForwardOutcome{})

	// Read path 2: detached extraction mirroring
	// streaming/executors.extractQueueTimestamps.
	t0, t1, t2, t3, t4, t5, t6, t7, t8, t9 := qr.StageTimestamps()
	sinkStages = [10]*time.Time{t0, t1, t2, t3, t4, t5, t6, t7, t8, t9}
}

var (
	sinkWaterfall WaterfallRequest
	sinkStages    [10]*time.Time
)
