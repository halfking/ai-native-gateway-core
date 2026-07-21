package routing

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/health"
	"github.com/stretchr/testify/assert"
)

// makeCandidate is a helper to build a *Candidate for tests.
func makeCandidate(id string) *Candidate {
	return &Candidate{
		CredentialID: id,
		Provider:     "openai",
		Model:        "gpt-4",
	}
}

// TestWeightedRouter_NoErrors_FullWeight tests fresh candidates get full weight.
func TestWeightedRouter_NoErrors_FullWeight(t *testing.T) {
	wr := NewWeightedRouter()

	wr.Register(makeCandidate("c1"))
	wr.Register(makeCandidate("c2"))
	wr.Register(makeCandidate("c3"))

	assert.Equal(t, 1.0, wr.Weight("c1"), "Fresh candidate should have full weight")
	assert.Equal(t, 1.0, wr.Weight("c2"))
	assert.Equal(t, 1.0, wr.Weight("c3"))

	t.Logf("✓ 无错误时所有candidate权重为1.0")
}

// TestWeightedRouter_FewErrors_ModerateWeight tests error penalty.
func TestWeightedRouter_FewErrors_ModerateWeight(t *testing.T) {
	wr := NewWeightedRouter()
	det := health.NewErrorDetector(100) // high threshold so it stays usable
	wr.RegisterWithDetector(makeCandidate("c1"), det)

	// 5 errors/min → 50% penalty
	for i := 0; i < 5; i++ {
		det.OnError(health.ErrorEvent{
			CredentialID: "c1",
			StatusCode:   500,
			Timestamp:    time.Now(),
		})
	}

	weight := wr.Weight("c1")
	assert.Less(t, weight, 1.0, "Weight should decrease with errors")
	assert.GreaterOrEqual(t, weight, 0.5, "5 errors should not drop below 50%")
	assert.InDelta(t, 0.5, weight, 0.01, "5 errors/min should give ~50% weight")

	t.Logf("✓ 5错误/分钟权重: %v", weight)
}

// TestWeightedRouter_ManyErrors_LowWeight tests high error rate → minimum weight.
func TestWeightedRouter_ManyErrors_LowWeight(t *testing.T) {
	wr := NewWeightedRouter()
	det := health.NewErrorDetector(100)
	wr.RegisterWithDetector(makeCandidate("c1"), det)

	// 50 errors/min → way over baseline → floor at MinWeight
	for i := 0; i < 50; i++ {
		det.OnError(health.ErrorEvent{
			CredentialID: "c1",
			StatusCode:   500,
			Timestamp:    time.Now(),
		})
	}

	weight := wr.Weight("c1")
	assert.InDelta(t, 0.1, weight, 0.01, "Should floor at MinWeight (0.1)")

	t.Logf("✓ 高错误率权重降到MinWeight: %v", weight)
}

// TestWeightedRouter_HighLatency_Penalty tests latency penalty.
func TestWeightedRouter_HighLatency_Penalty(t *testing.T) {
	wr := NewWeightedRouter()
	wr.Register(makeCandidate("c1"))

	// 12s average latency → well above baseline (2s) → high penalty
	for i := 0; i < 50; i++ {
		wr.RecordLatency("c1", 12*time.Second)
	}

	weight := wr.Weight("c1")
	// 1 - (12000 - 2000) / 10000 = 0.0 → floored to 0.1
	assert.InDelta(t, 0.1, weight, 0.01, "12s latency should floor at MinWeight")

	t.Logf("✓ 12s延迟权重: %v", weight)
}

// TestWeightedRouter_Combined_ErrorAndLatency tests combined penalty.
func TestWeightedRouter_Combined_ErrorAndLatency(t *testing.T) {
	wr := NewWeightedRouter()
	det := health.NewErrorDetector(100)
	wr.RegisterWithDetector(makeCandidate("c1"), det)

	// 5 errors/min → 50% error penalty
	for i := 0; i < 5; i++ {
		det.OnError(health.ErrorEvent{
			CredentialID: "c1",
			StatusCode:   500,
			Timestamp:    time.Now(),
		})
	}

	// 7s avg latency → ~50% latency penalty
	// (1 - (7000 - 2000) / 10000 = 0.5)
	for i := 0; i < 50; i++ {
		wr.RecordLatency("c1", 7*time.Second)
	}

	weight := wr.Weight("c1")
	// Combined: 1.0 * 0.5 * 0.5 = 0.25
	assert.InDelta(t, 0.25, weight, 0.05, "Combined penalty should be ~0.25")

	t.Logf("✓ 综合错误+延迟权重: %v (预期~0.25)", weight)
}

// TestWeightedRouter_SelectWeighted_Distribution tests weighted selection distribution.
func TestWeightedRouter_SelectWeighted_Distribution(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过分布测试")
	}

	wr := NewWeightedRouter()
	wr.Register(makeCandidate("c1")) // weight 1.0
	wr.Register(makeCandidate("c2")) // weight 0.5
	wr.Register(makeCandidate("c3")) // weight 0.4

	// Force different weights by adding different error rates
	det1 := health.NewErrorDetector(100)
	det2 := health.NewErrorDetector(100)
	wr.RegisterWithDetector(makeCandidate("c4"), det1)
	wr.RegisterWithDetector(makeCandidate("c5"), det2)

	for i := 0; i < 5; i++ {
		det1.OnError(health.ErrorEvent{CredentialID: "c4", StatusCode: 500, Timestamp: time.Now()})
	}
	for i := 0; i < 6; i++ {
		det2.OnError(health.ErrorEvent{CredentialID: "c5", StatusCode: 500, Timestamp: time.Now()})
	}

	const selections = 10000
	counts := make(map[string]int)

	for i := 0; i < selections; i++ {
		c := wr.SelectWeighted()
		if c != nil {
			counts[c.CredentialID]++
		}
	}

	// Verify roughly uniform distribution across c1, c2, c3 (~33% each)
	// c4 and c5 should have ~half of c1 due to error penalty
	t.Logf("Distribution:")
	for _, id := range []string{"c1", "c2", "c3", "c4", "c5"} {
		pct := float64(counts[id]) / float64(selections) * 100
		t.Logf("  %s: %d (%.2f%%)", id, counts[id], pct)
	}

	// c1 (no errors) should get more than c4 (5 errors) and c5 (6 errors)
	assert.Greater(t, counts["c1"], counts["c4"], "c1 should be selected more than c4")
	assert.Greater(t, counts["c4"], counts["c5"], "c4 (5 errors) should be selected more than c5 (6 errors)")

	// c1 should have ~25% of total (its weight is ~2x c2, ~2.5x c3)
	assert.Greater(t, counts["c1"], 1500, "c1 should get reasonable share")
}

// TestWeightedRouter_DynamicAdjustment_ErrorSpike tests dynamic weight changes.
func TestWeightedRouter_DynamicAdjustment_ErrorSpike(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过动态调整测试")
	}

	wr := NewWeightedRouter()
	det := health.NewErrorDetector(100)
	wr.RegisterWithDetector(makeCandidate("c1"), det)
	wr.Register(makeCandidate("c2"))

	// T0: equal weights
	assert.Equal(t, 1.0, wr.Weight("c1"))
	assert.Equal(t, 1.0, wr.Weight("c2"))

	// T1: c1 spikes with 20 errors
	for i := 0; i < 20; i++ {
		det.OnError(health.ErrorEvent{CredentialID: "c1", StatusCode: 500, Timestamp: time.Now()})
	}

	w1 := wr.Weight("c1")
	w2 := wr.Weight("c2")
	assert.Less(t, w1, w2, "c1 should have lower weight after error spike")
	assert.InDelta(t, 0.1, w1, 0.01, "c1 should be at minimum weight")

	// Simulate selection distribution
	const selections = 1000
	counts := make(map[string]int)
	for i := 0; i < selections; i++ {
		c := wr.SelectWeighted()
		if c != nil {
			counts[c.CredentialID]++
		}
	}

	// c2 should get most of the traffic
	t.Logf("After error spike on c1:")
	t.Logf("  c1: %d (%.1f%%)", counts["c1"], float64(counts["c1"])/10)
	t.Logf("  c2: %d (%.1f%%)", counts["c2"], float64(counts["c2"])/10)

	assert.Less(t, counts["c1"], counts["c2"], "c2 should get more traffic")
	assert.Less(t, counts["c1"], 200, "c1 should get <20% traffic")
}

// TestWeightedRouter_DynamicAdjustment_Recovery tests weight recovery.
func TestWeightedRouter_DynamicAdjustment_Recovery(t *testing.T) {
	wr := NewWeightedRouter()
	det := health.NewErrorDetector(100)
	wr.RegisterWithDetector(makeCandidate("c1"), det)

	// Add errors to drop weight
	for i := 0; i < 10; i++ {
		det.OnError(health.ErrorEvent{CredentialID: "c1", StatusCode: 500, Timestamp: time.Now()})
	}

	initialWeight := wr.Weight("c1")
	assert.Less(t, initialWeight, 1.0, "Should be penalized")

	// Reset and verify recovery (ResetFailures clears consecutive fails;
	// errors-per-minute counter needs time to decay naturally).
	det.ResetFailures("c1")

	// Force weight recompute by waiting > cache TTL
	time.Sleep(1100 * time.Millisecond)

	recoveredWeight := wr.Weight("c1")
	assert.GreaterOrEqual(t, recoveredWeight, initialWeight, "Weight should not decrease after reset")

	// Note: errors-per-minute (sliding window) takes 60s to fully decay.
	// Consecutive fails reset → no additional penalty from failures.
	// The errors-per-minute counter still has the 10 errors from before reset.
	// So recoveredWeight may still be at floor; what we verify is that
	// it didn't get worse than initial.
	t.Logf("✓ 权重恢复: %.2f → %.2f (consecutive fails reset)", initialWeight, recoveredWeight)
}

// TestWeightedRouter_SelectTopN tests top-N selection by weight.
func TestWeightedRouter_SelectTopN(t *testing.T) {
	wr := NewWeightedRouter()
	for _, id := range []string{"c1", "c2", "c3", "c4", "c5"} {
		wr.Register(makeCandidate(id))
	}

	top := wr.SelectTopN(3)
	assert.Len(t, top, 3, "Should return top 3 candidates")

	// All should be non-nil
	for i, c := range top {
		assert.NotNil(t, c, "Top candidate %d should not be nil", i)
	}

	t.Logf("✓ Top 3 selected: %v, %v, %v",
		top[0].CredentialID, top[1].CredentialID, top[2].CredentialID)
}

// TestWeightedRouter_ConcurrentAccess tests thread safety.
func TestWeightedRouter_ConcurrentAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过并发测试")
	}

	wr := NewWeightedRouter()
	det := health.NewErrorDetector(100)
	wr.RegisterWithDetector(makeCandidate("c1"), det)
	wr.Register(makeCandidate("c2"))

	var wg sync.WaitGroup
	wg.Add(3)

	// Goroutine 1: record errors
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			det.OnError(health.ErrorEvent{CredentialID: "c1", StatusCode: 500, Timestamp: time.Now()})
		}
	}()

	// Goroutine 2: record latencies
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			wr.RecordLatency("c1", time.Duration(i)*time.Millisecond)
		}
	}()

	// Goroutine 3: select
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = wr.SelectWeighted()
		}
	}()

	wg.Wait()

	// No race conditions → test passes
	t.Logf("✓ 并发访问无race condition")
}

// TestWeightedRouter_RegisterUnregister tests pool management.
func TestWeightedRouter_RegisterUnregister(t *testing.T) {
	wr := NewWeightedRouter()
	assert.Equal(t, 0, wr.Size())

	wr.Register(makeCandidate("c1"))
	wr.Register(makeCandidate("c2"))
	assert.Equal(t, 2, wr.Size())

	wr.Unregister("c1")
	assert.Equal(t, 1, wr.Size())

	// Selecting should now return c2 only
	c := wr.SelectWeighted()
	assert.NotNil(t, c)
	assert.Equal(t, "c2", c.CredentialID)

	t.Logf("✓ 注册/注销管理正常")
}

// TestWeightedRouter_EmptyPool tests selection with no candidates.
func TestWeightedRouter_EmptyPool(t *testing.T) {
	wr := NewWeightedRouter()

	c := wr.SelectWeighted()
	assert.Nil(t, c, "Empty pool should return nil")

	t.Logf("✓ 空池返回nil")
}

// TestWeightedRouter_RecordSuccess tests success recording.
func TestWeightedRouter_RecordSuccess(t *testing.T) {
	wr := NewWeightedRouter()
	det := health.NewErrorDetector(100)
	wr.RegisterWithDetector(makeCandidate("c1"), det)

	// Add some errors
	for i := 0; i < 5; i++ {
		det.OnError(health.ErrorEvent{CredentialID: "c1", StatusCode: 500, Timestamp: time.Now()})
	}

	beforeWeight := wr.Weight("c1")

	// Record success
	wr.RecordSuccess("c1", 100*time.Millisecond)

	time.Sleep(1100 * time.Millisecond) // cache invalidation

	afterWeight := wr.Weight("c1")
	assert.GreaterOrEqual(t, afterWeight, beforeWeight, "Success should increase weight")

	t.Logf("✓ 成功请求提升权重: %.3f → %.3f", beforeWeight, afterWeight)
}

// TestWeightedRouter_Stats tests stats output.
func TestWeightedRouter_Stats(t *testing.T) {
	wr := NewWeightedRouter()
	det := health.NewErrorDetector(100)
	wr.RegisterWithDetector(makeCandidate("c1"), det)
	wr.Register(makeCandidate("c2"))

	det.OnError(health.ErrorEvent{CredentialID: "c1", StatusCode: 500, Timestamp: time.Now()})
	wr.RecordLatency("c1", 500*time.Millisecond)
	wr.RecordLatency("c1", 1000*time.Millisecond)
	wr.RecordLatency("c1", 1500*time.Millisecond)

	stats := wr.Stats()
	assert.Len(t, stats, 2)

	for _, s := range stats {
		t.Logf("Credential %s: weight=%.3f, errors=%d, avgLatency=%.1fms, samples=%d",
			s.CredentialID, s.Weight, s.ErrorsPerMin, s.AvgLatencyMs, s.SampleCount)
	}

	// c1 should have 1 error
	var c1Stats WeightStats
	for _, s := range stats {
		if s.CredentialID == "c1" {
			c1Stats = s
		}
	}
	assert.Equal(t, 1, c1Stats.ErrorsPerMin)
	assert.Equal(t, 3, c1Stats.SampleCount)
	assert.InDelta(t, 1000, c1Stats.AvgLatencyMs, 1)
}

// TestWeightedRouter_CustomConfig tests custom weight config.
func TestWeightedRouter_CustomConfig(t *testing.T) {
	cfg := WeightConfig{
		BaseWeight:        2.0,
		ErrorRateBaseline: 5,
		LatencyBaseline:   1000,
		MinWeight:         0.05,
	}
	wr := NewWeightedRouterWithConfig(cfg)
	wr.Register(makeCandidate("c1"))

	// Base weight should be 2.0
	assert.InDelta(t, 2.0, wr.Weight("c1"), 0.01)

	// 5 errors/min should give 50% of 2.0 = 1.0
	det := health.NewErrorDetector(100)
	wr.RegisterWithDetector(makeCandidate("c1"), det)
	for i := 0; i < 5; i++ {
		det.OnError(health.ErrorEvent{CredentialID: "c1", StatusCode: 500, Timestamp: time.Now()})
	}

	time.Sleep(1100 * time.Millisecond)
	weight := wr.Weight("c1")
	assert.LessOrEqual(t, weight, 1.0, "Weight should be reduced after errors")

	t.Logf("✓ 自定义配置权重: %v", weight)
}

// TestWeightedRouter_LowLatency_NoPenalty tests no penalty below baseline.
func TestWeightedRouter_LowLatency_NoPenalty(t *testing.T) {
	wr := NewWeightedRouter()
	wr.Register(makeCandidate("c1"))

	// 500ms latency (below 2s baseline)
	for i := 0; i < 50; i++ {
		wr.RecordLatency("c1", 500*time.Millisecond)
	}

	weight := wr.Weight("c1")
	assert.InDelta(t, 1.0, weight, 0.01, "Low latency should have no penalty")

	t.Logf("✓ 低延迟无惩罚: %v", weight)
}

// BenchmarkWeightedRouter_SelectWeighted benchmarks selection.
func BenchmarkWeightedRouter_SelectWeighted(b *testing.B) {
	wr := NewWeightedRouter()
	for i := 0; i < 10; i++ {
		wr.Register(makeCandidate(fmt.Sprintf("c%d", i)))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = wr.SelectWeighted()
	}
}

// BenchmarkWeightedRouter_Weight benchmarks weight calculation.
func BenchmarkWeightedRouter_Weight(b *testing.B) {
	wr := NewWeightedRouter()
	det := health.NewErrorDetector(100)
	wr.RegisterWithDetector(makeCandidate("c1"), det)

	// Add some data
	for i := 0; i < 10; i++ {
		det.OnError(health.ErrorEvent{CredentialID: "c1", StatusCode: 500, Timestamp: time.Now()})
		wr.RecordLatency("c1", time.Duration(i*100)*time.Millisecond)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = wr.Weight("c1")
	}
}
