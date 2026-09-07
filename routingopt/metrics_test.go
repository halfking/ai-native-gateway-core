package routingopt

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// detachFeedbackCountersForTest 清除注入的 fn（仅测试使用），避免包内
// 测试之间通过包级 Prometheus 状态互相污染。
func detachFeedbackCountersForTest() {
	feedbackWrites.fn.Store(nil)
}

// =============================================================================
// metrics.go — hook 耗时直方图 Snapshot
// =============================================================================

func TestMetricHookLatencySnapshot(t *testing.T) {
	before := Snapshot()

	// 两个可分辨的耗时：≥2ms 与 ≥3ms（CI 计时抖动下 sum 下界 4ms 依然成立）。
	recordHookLatency("preclassify", time.Now().Add(-2*time.Millisecond))
	recordHookLatency("preclassify", time.Now().Add(-3*time.Millisecond))
	recordHookLatency("recommend", time.Now().Add(-5*time.Millisecond))

	after := Snapshot()

	if got := after.PreClassify.Count - before.PreClassify.Count; got != 2 {
		t.Fatalf("preclassify count delta = %d, want 2", got)
	}
	if sum := after.PreClassify.SumMs - before.PreClassify.SumMs; sum < 4 {
		t.Fatalf("preclassify sum delta = %v ms, want >= 4", sum)
	}
	if avg := after.PreClassify.AvgMs; avg <= 0 {
		t.Fatalf("preclassify avg = %v, want > 0", avg)
	}
	if got := after.Recommend.Count - before.Recommend.Count; got != 1 {
		t.Fatalf("recommend count delta = %d, want 1", got)
	}
	if sum := after.Recommend.SumMs - before.Recommend.SumMs; sum < 4 {
		t.Fatalf("recommend sum delta = %v ms, want >= 4", sum)
	}
	// postclassify 未 observe：快照必须保持零值语义。
	if after.PostClassify.Count != before.PostClassify.Count {
		t.Fatalf("postclassify should be untouched, delta %d", after.PostClassify.Count-before.PostClassify.Count)
	}
}

func TestMetricRealOptimizerHooksObserveLatency(t *testing.T) {
	o := NewRealOptimizerWithOptions(nil, Options{}) // 全部 hook 短路，nil pool 安全
	before := Snapshot()

	if _, err := o.PreClassify(context.Background(), "signals"); err != nil {
		t.Fatalf("PreClassify: %v", err)
	}
	if _, err := o.PostClassify(context.Background(), "code", 0.5); err != nil {
		t.Fatalf("PostClassify: %v", err)
	}
	cands := []ModelCandidate{{CanonicalName: "m"}}
	if _, err := o.RecommendModel(context.Background(), cands, RoutingContext{}); err != nil {
		t.Fatalf("RecommendModel: %v", err)
	}

	after := Snapshot()
	if after.PreClassify.Count <= before.PreClassify.Count {
		t.Fatal("PreClassify must observe its latency histogram")
	}
	if after.PostClassify.Count <= before.PostClassify.Count {
		t.Fatal("PostClassify must observe its latency histogram")
	}
	if after.Recommend.Count <= before.Recommend.Count {
		t.Fatal("RecommendModel must observe its latency histogram")
	}
}

// =============================================================================
// AttachFeedbackCounters — Wave 2 注入口（假 fn 验证读数与映射）
// =============================================================================

func TestMetricAttachFeedbackCounters(t *testing.T) {
	t.Cleanup(detachFeedbackCountersForTest)

	// enqueued=10, dropped=3, flushed=6 → ok=6, dropped=3, error=10-6-3=1
	AttachFeedbackCounters(func() FeedbackCounters {
		return FeedbackCounters{Enqueued: 10, Dropped: 3, Flushed: 6}
	})

	snap := Snapshot()
	if snap.FeedbackWrites.OK != 6 || snap.FeedbackWrites.Dropped != 3 || snap.FeedbackWrites.Error != 1 {
		t.Fatalf("feedback writes snapshot = %+v, want ok=6 error=1 dropped=3", snap.FeedbackWrites)
	}

	// Prometheus 侧：collector 在 scrape 时投影三个 result 标签。
	want := `# HELP llmgw_routingopt_feedback_writes_total Routing feedback batch writes by outcome. ok=flushed to routing_feedback_log, dropped=discarded before flush, error=enqueued-flushed-dropped (saturated at 0). Values are read from the batch writer's atomic counters at scrape time.
# TYPE llmgw_routingopt_feedback_writes_total counter
llmgw_routingopt_feedback_writes_total{result="dropped"} 3
llmgw_routingopt_feedback_writes_total{result="error"} 1
llmgw_routingopt_feedback_writes_total{result="ok"} 6
`
	if err := testutil.CollectAndCompare(feedbackWrites, strings.NewReader(want)); err != nil {
		t.Fatalf("feedback writes collector mismatch: %v", err)
	}
}

func TestMetricAttachFeedbackCountersNilIsNoOp(t *testing.T) {
	t.Cleanup(detachFeedbackCountersForTest)
	detachFeedbackCountersForTest()

	// 未注入时 Collect 不产出任何 metric（且不 panic）。
	if n := testutil.CollectAndCount(feedbackWrites); n != 0 {
		t.Fatalf("unattached collector emitted %d metrics, want 0", n)
	}
	AttachFeedbackCounters(nil) // 显式 nil 也必须是安全 no-op
	if n := testutil.CollectAndCount(feedbackWrites); n != 0 {
		t.Fatalf("nil fn collector emitted %d metrics, want 0", n)
	}
}

func TestMetricFeedbackErrorCountSaturates(t *testing.T) {
	t.Cleanup(detachFeedbackCountersForTest)

	// 恶意/损坏的计数器组合（flushed+dropped > enqueued）不得产生负值。
	AttachFeedbackCounters(func() FeedbackCounters {
		return FeedbackCounters{Enqueued: 2, Dropped: 5, Flushed: 9}
	})
	if got := Snapshot().FeedbackWrites.Error; got != 0 {
		t.Fatalf("saturating error count = %d, want 0", got)
	}
}

// =============================================================================
// exploration / cache / weighted_accuracy 计数与 gauge
// =============================================================================

func TestMetricExplorationAndCacheCounters(t *testing.T) {
	before := Snapshot()

	recordExplorationRequest()
	recordExplorationRequest()
	RecordCacheHit()
	RecordCacheHit()
	RecordCacheHit()
	RecordCacheMiss()

	after := Snapshot()
	if got := after.ExplorationRequests - before.ExplorationRequests; got != 2 {
		t.Fatalf("exploration delta = %d, want 2", got)
	}
	if got := after.CacheHits - before.CacheHits; got != 3 {
		t.Fatalf("cache hits delta = %d, want 3", got)
	}
	if got := after.CacheMiss - before.CacheMiss; got != 1 {
		t.Fatalf("cache miss delta = %d, want 1", got)
	}
	// 命中率 = 3/4
	if diff := after.CacheHitRate - 0.75; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cache hit rate = %v, want 0.75", after.CacheHitRate)
	}
}

func TestMetricWeightedAccuracyGauge(t *testing.T) {
	SetWeightedAccuracy(0.87)
	if got := Snapshot().WeightedAccuracy; got != 0.87 {
		t.Fatalf("weighted accuracy = %v, want 0.87", got)
	}
	if got := testutil.ToFloat64(weightedAccuracy); got != 0.87 {
		t.Fatalf("prometheus gauge = %v, want 0.87", got)
	}
}

func TestMetricCacheCountersZeroDivision(t *testing.T) {
	// 只 miss 不 hit 时命中率 = 0/(0+n) 不得出现 NaN/Inf。
	RecordCacheMiss()
	snap := Snapshot()
	if snap.CacheHitRate < 0 || snap.CacheHitRate > 1 {
		t.Fatalf("cache hit rate out of range: %v", snap.CacheHitRate)
	}
}

// =============================================================================
// RealOptimizer: RunAdaptiveMaintenance nil-pool 安全
// =============================================================================

func TestRoutingOptRunAdaptiveMaintenanceNilPoolNoPanic(t *testing.T) {
	// nil pool：AdaptParameters 早退（无激活 state），维护流程必须安静返回。
	o := NewRealOptimizer(nil)
	o.RunAdaptiveMaintenance(context.Background())
}

// =============================================================================
// recommender.go ε-greedy 探索计数接线
// =============================================================================

func TestRoutingOptRecommenderIncrementsExplorationCounter(t *testing.T) {
	r := NewModelRecommender(nil)
	// exploration_rate=0.2（clamp 上限）。期望 300 次里命中约 60 次；
	// P(命中 ≤ 5) 在 0.8^300 量级，实际不可能发生。
	r.SetActiveStateForTest(testOptimizationState(0.2))
	cands := []ModelCandidate{
		{CanonicalName: "a", Score: 90, Cost: 0.5, Latency: 0.5, Availability: 0.9},
		{CanonicalName: "b", Score: 80, Cost: 0.1, Latency: 0.1, Availability: 0.9},
	}

	before := Snapshot().ExplorationRequests
	for i := 0; i < 300; i++ {
		if _, err := r.Recommend(context.Background(), cands, RoutingContext{}); err != nil {
			t.Fatalf("Recommend: %v", err)
		}
	}
	if got := Snapshot().ExplorationRequests - before; got <= 5 {
		t.Fatalf("exploration counter delta = %d over 300 requests at rate 0.2, want > 5", got)
	}
}

// =============================================================================
// 并发：observe / 计数 / gauge / 注入 / 快照全部无锁竞争（-race 验证）
// =============================================================================

func TestMetricConcurrentSnapshotNoRace(t *testing.T) {
	t.Cleanup(detachFeedbackCountersForTest)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				recordHookLatency("preclassify", time.Now().Add(-time.Millisecond))
				recordExplorationRequest()
				RecordCacheHit()
				SetWeightedAccuracy(float64(j%100) / 100)
				AttachFeedbackCounters(func() FeedbackCounters {
					return FeedbackCounters{Enqueued: uint64(j), Flushed: uint64(j)}
				})
				_ = Snapshot()
			}
		}()
	}
	wg.Wait()
}
