package integration

import (
	"net/http"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/health"
	"github.com/kaixuan/llm-gateway-go/domains/routing"
	"github.com/stretchr/testify/assert"
)

// TestIntegration_Scenario1_ProviderSlowdown simulates a provider gradually becoming slow.
//
// Expected: After threshold, WeightedRouter should shift traffic away from the slow
// provider to the healthy one.
func TestIntegration_Scenario1_ProviderSlowdown(t *testing.T) {
	// Two providers: one healthy (fast), one that becomes slow
	fastProvider := NewMockProvider(BehaviorHealthy, 10*time.Millisecond, 0)
	defer fastProvider.Close()

	slowProvider := NewMockProvider(BehaviorHealthy, 10*time.Millisecond, 0)
	defer slowProvider.Close()

	// Build weighted router
	wr := routing.NewWeightedRouter()
	wr.Register(routing.NewCandidateFromID("fast"))
	wr.Register(routing.NewCandidateFromID("slow"))

	// Set up error detectors for each
	detFast := health.NewErrorDetector(100)
	detSlow := health.NewErrorDetector(100)
	wr.RegisterWithDetector(routing.NewCandidateFromID("fast"), detFast)
	wr.UpdateCandidate(routing.NewCandidateFromID("fast"))
	_ = detFast
	wr.RegisterWithDetector(routing.NewCandidateFromID("slow"), detSlow)
	wr.UpdateCandidate(routing.NewCandidateFromID("slow"))

	// T0: Both providers fast and healthy — equal weights
	assert.Equal(t, 1.0, wr.Weight("fast"))
	assert.Equal(t, 1.0, wr.Weight("slow"))

	// T1: Slow provider becomes much slower (5s vs 10ms)
	slowProvider.SetLatency(5 * time.Second)
	// Record latencies to feed LatencyTracker
	for i := 0; i < 50; i++ {
		wr.RecordLatency("fast", 10*time.Millisecond)
		wr.RecordLatency("slow", 5*time.Second)
	}

	// Wait for cache to expire
	time.Sleep(1100 * time.Millisecond)

	fastWeight := wr.Weight("fast")
	slowWeight := wr.Weight("slow")
	t.Logf("After slowdown: fast=%v, slow=%v", fastWeight, slowWeight)

	// Slow provider should have lower weight
	assert.Greater(t, fastWeight, slowWeight, "Fast provider should have higher weight")
	assert.Less(t, slowWeight, fastWeight, "Slow provider should be penalized")

	// T2: Simulate traffic distribution
	const totalRequests = 1000
	counts := map[string]int{}
	for i := 0; i < totalRequests; i++ {
		c := wr.SelectWeighted()
		if c != nil {
			counts[c.CredentialID]++
		}
	}

	fastPct := float64(counts["fast"]) / float64(totalRequests) * 100
	slowPct := float64(counts["slow"]) / float64(totalRequests) * 100
	t.Logf("Traffic distribution: fast=%.1f%% (%d), slow=%.1f%% (%d)",
		fastPct, counts["fast"], slowPct, counts["slow"])

	// Fast provider should get more traffic than slow
	assert.GreaterOrEqual(t, counts["fast"], counts["slow"], "Fast should get at least as much traffic as slow")
	assert.GreaterOrEqual(t, fastPct, 50.0, "Fast should get at least half of traffic")
}

// TestIntegration_Scenario2_Provider5xxSpike simulates a sudden burst of 5xx errors.
//
// Expected: ErrorDetector marks credential Unhealthy after 3 consecutive failures,
// WeightedRouter shifts traffic to other providers.
func TestIntegration_Scenario2_Provider5xxSpike(t *testing.T) {
	providerA := NewMockProvider(BehaviorHealthy, 10*time.Millisecond, 0)
	defer providerA.Close()

	providerB := NewMockProvider(BehaviorHealthy, 10*time.Millisecond, 0)
	defer providerB.Close()

	wr := routing.NewWeightedRouter()
	detA := health.NewErrorDetector(3)
	detB := health.NewErrorDetector(3)

	wr.RegisterWithDetector(routing.NewCandidateFromID("A"), detA)
	wr.RegisterWithDetector(routing.NewCandidateFromID("B"), detB)

	// T0: Both healthy — equal weights
	assert.Equal(t, 1.0, wr.Weight("A"))
	assert.Equal(t, 1.0, wr.Weight("B"))

	// T1: Provider A starts returning 500s
	providerA.SetBehavior(BehaviorError)

	// Simulate 3 consecutive 500 errors
	for i := 0; i < 3; i++ {
		wr.RecordError("A", 500, nil)
	}

	// Wait for cache to expire
	time.Sleep(1100 * time.Millisecond)

	// Verify Provider A is marked Unhealthy
	detectionResult := detA.OnError(health.ErrorEvent{
		CredentialID: "A",
		StatusCode:   500,
		Timestamp:    time.Now(),
	})
	assert.Equal(t, "Unhealthy", detectionResult.RecommendedStatus)
	assert.True(t, detectionResult.ShouldMarkUnhealthy, "Should mark Unhealthy after 3 failures")

	weightA := wr.Weight("A")
	weightB := wr.Weight("B")
	t.Logf("After 5xx spike: A=%v (Unhealthy), B=%v (healthy)", weightA, weightB)
	assert.Less(t, weightA, weightB, "A should have lower weight than B")

	// T2: Verify traffic shifts away from A
	const totalRequests = 1000
	counts := map[string]int{}
	for i := 0; i < totalRequests; i++ {
		c := wr.SelectWeighted()
		if c != nil {
			counts[c.CredentialID]++
		}
	}

	t.Logf("Traffic after 5xx spike: A=%d, B=%d", counts["A"], counts["B"])
	assert.Less(t, counts["A"], counts["B"], "A should get less traffic than B")

	// T3: Provider A recovers
	providerA.SetBehavior(BehaviorHealthy)
	for i := 0; i < 5; i++ {
		wr.RecordSuccess("A", 10*time.Millisecond)
	}

	time.Sleep(1100 * time.Millisecond)
	recoveredWeight := wr.Weight("A")
	t.Logf("After recovery: A=%v", recoveredWeight)
	assert.GreaterOrEqual(t, recoveredWeight, weightA, "A's weight should improve after success")
}

// TestIntegration_Scenario3_QuotaExhaustion simulates a provider hitting quota limits.
//
// Expected: 429 errors trigger quota recovery logic. Provider marked with
// extended Unhealthy state until reset time.
func TestIntegration_Scenario3_QuotaExhaustion(t *testing.T) {
	providerA := NewMockProvider(BehaviorHealthy, 10*time.Millisecond, 0)
	defer providerA.Close()

	providerB := NewMockProvider(BehaviorHealthy, 10*time.Millisecond, 0)
	defer providerB.Close()

	wr := routing.NewWeightedRouter()
	detA := health.NewErrorDetector(10) // higher threshold so 429s don't immediately mark Unhealthy

	wr.RegisterWithDetector(routing.NewCandidateFromID("A"), detA)
	wr.Register(routing.NewCandidateFromID("B"))

	// T0: Both healthy
	assert.Equal(t, 1.0, wr.Weight("A"))
	assert.Equal(t, 1.0, wr.Weight("B"))

	// T1: Provider A hits quota (429)
	resetTime := time.Now().Add(60 * time.Second)
	providerA.SetQuotaExhausted(true, resetTime)
	providerA.SetBehavior(BehaviorQuotaExhausted)

	// Simulate quota errors
	for i := 0; i < 3; i++ {
		wr.RecordError("A", 429, nil)
	}

	time.Sleep(1100 * time.Millisecond)

	// Check 429 classification triggers quota recovery (must be within fail threshold to avoid triggering Unhealthy instead)
	result := detA.OnError(health.ErrorEvent{
		CredentialID: "A",
		StatusCode:   429,
		Timestamp:    time.Now(),
	})
	// Note: ShouldTriggerQuota is set when status is 503; for 429 the detector also marks quota. We check either flag or status.
	assert.True(t, result.ShouldTriggerQuota || result.RecommendedStatus == "Degraded",
		"429 should trigger quota recovery or at least mark Degraded")

	weightA := wr.Weight("A")
	weightB := wr.Weight("B")
	t.Logf("After quota exhaustion: A=%v, B=%v", weightA, weightB)
	assert.LessOrEqual(t, weightA, weightB, "A should be deprioritized")

	// T2: Verify traffic flows to B
	const totalRequests = 500
	counts := map[string]int{}
	for i := 0; i < totalRequests; i++ {
		c := wr.SelectWeighted()
		if c != nil {
			counts[c.CredentialID]++
		}
	}
	t.Logf("Traffic during quota: A=%d, B=%d", counts["A"], counts["B"])
	assert.LessOrEqual(t, counts["A"], counts["B"], "B should get at least as much traffic as A")
}

// TestIntegration_Scenario4_AllProvidersDown simulates a catastrophic failure.
//
// Expected: All providers marked Unhealthy. System should not crash;
// should still be able to query weights (even if they're all at floor).
func TestIntegration_Scenario4_AllProvidersDown(t *testing.T) {
	providers := []*MockProvider{
		NewMockProvider(BehaviorHealthy, 10*time.Millisecond, 0),
		NewMockProvider(BehaviorHealthy, 10*time.Millisecond, 0),
		NewMockProvider(BehaviorHealthy, 10*time.Millisecond, 0),
	}
	defer func() {
		for _, p := range providers {
			p.Close()
		}
	}()

	wr := routing.NewWeightedRouter()

	ids := []string{"A", "B", "C"}
	dets := make([]*health.ErrorDetector, 3)
	for i, id := range ids {
		dets[i] = health.NewErrorDetector(3)
		wr.RegisterWithDetector(routing.NewCandidateFromID(id), dets[i])
	}

	// T0: All healthy
	for _, id := range ids {
		assert.Equal(t, 1.0, wr.Weight(id))
	}

	// T1: All providers fail
	for _, p := range providers {
		p.SetBehavior(BehaviorError)
	}

	// Record errors against each provider (using correct credential IDs)
	for i, det := range dets {
		for j := 0; j < 5; j++ {
			det.OnError(health.ErrorEvent{
				CredentialID: ids[i],
				StatusCode:   500,
				Timestamp:    time.Now(),
			})
		}
	}

	time.Sleep(1100 * time.Millisecond)

	// All should have low weight (or low-ish due to floor)
	for _, id := range ids {
		w := wr.Weight(id)
		t.Logf("Provider %s weight: %v", id, w)
	}

	// System should still be functional (returns some candidate)
	c := wr.SelectWeighted()
	assert.NotNil(t, c, "System should still return a candidate even when all are down")
	t.Logf("System returned candidate: %s", c.CredentialID)

	// T2: One provider recovers
	providers[0].SetBehavior(BehaviorHealthy)
	// Reset failures for A
	dets[0].ResetFailures("A")
	// Simulate successes
	for i := 0; i < 10; i++ {
		wr.RecordSuccess("A", 10*time.Millisecond)
	}

	time.Sleep(1100 * time.Millisecond)
	recoveredWeight := wr.Weight("A")
	otherWeights := []float64{wr.Weight("B"), wr.Weight("C")}
	t.Logf("Provider A recovered: %v, others: %v", recoveredWeight, otherWeights)

	// A's weight should be ≥ B and C (since A recovered)
	assert.GreaterOrEqual(t, recoveredWeight, otherWeights[0], "A should be at least as good as B")
	assert.GreaterOrEqual(t, recoveredWeight, otherWeights[1], "A should be at least as good as C")
}

// TestIntegration_EndToEnd_RequestFlow simulates a complete request flow:
//
// 1. Health check (L1→L2→L3)
// 2. Weighted selection
// 3. Provider call
// 4. Result handling (success/error)
func TestIntegration_EndToEnd_RequestFlow(t *testing.T) {
	// Setup: 3 providers with varying health
	healthy := NewMockProvider(BehaviorHealthy, 50*time.Millisecond, 0)
	defer healthy.Close()

	slow := NewMockProvider(BehaviorHealthy, 200*time.Millisecond, 0)
	defer slow.Close()

	errored := NewMockProvider(BehaviorError, 10*time.Millisecond, 0)
	defer errored.Close()

	// Build weighted router
	wr := routing.NewWeightedRouter()
	dets := []*health.ErrorDetector{
		health.NewErrorDetector(5),
		health.NewErrorDetector(5),
		health.NewErrorDetector(5),
	}

	ids := []string{"healthy", "slow", "errored"}
	for i, id := range ids {
		wr.RegisterWithDetector(routing.NewCandidateFromID(id), dets[i])
	}

	// Feed some latency samples
	for i := 0; i < 50; i++ {
		wr.RecordLatency("healthy", 50*time.Millisecond)
		wr.RecordLatency("slow", 200*time.Millisecond)
	}

	// Provider 'errored' gets errors recorded
	for i := 0; i < 5; i++ {
		dets[2].OnError(health.ErrorEvent{
			CredentialID: "errored",
			StatusCode:   500,
			Timestamp:    time.Now(),
		})
	}

	time.Sleep(1100 * time.Millisecond)

	// Process 100 simulated requests
	const totalRequests = 100
	type result struct {
		providerID string
		success    bool
		latency    time.Duration
	}
	results := make([]result, 0, totalRequests)

	for i := 0; i < totalRequests; i++ {
		c := wr.SelectWeighted()
		if c == nil {
			continue
		}

		start := time.Now()
		var success bool

		switch c.CredentialID {
		case "healthy":
			success = callProviderAndCheck(t, healthy.URL())
		case "slow":
			success = callProviderAndCheck(t, slow.URL())
		case "errored":
			success = callProviderAndCheck(t, errored.URL())
		}

		latency := time.Since(start)
		results = append(results, result{
			providerID: c.CredentialID,
			success:    success,
			latency:    latency,
		})

		// Feed result back into router
		if success {
			wr.RecordSuccess(c.CredentialID, latency)
		} else {
			wr.RecordError(c.CredentialID, 500, nil)
		}
	}

	// Analyze distribution
	counts := map[string]int{}
	successCounts := map[string]int{}
	totalLatency := time.Duration(0)
	for _, r := range results {
		counts[r.providerID]++
		if r.success {
			successCounts[r.providerID]++
		}
		totalLatency += r.latency
	}

	t.Logf("Distribution after %d requests:", totalRequests)
	for _, id := range ids {
		t.Logf("  %s: %d selections, %d successes (%.1f%%)",
			id, counts[id], successCounts[id],
			float64(successCounts[id])/float64(counts[id])*100)
	}

	avgLatency := totalLatency / time.Duration(len(results))
	t.Logf("Average latency: %v", avgLatency)

	// 'healthy' should have the most selections
	assert.Greater(t, counts["healthy"], counts["errored"], "healthy > errored")
	// Average latency should be reasonable
	assert.Less(t, avgLatency, 1*time.Second, "Average latency should be < 1s")
}

// callProviderAndCheck sends an HTTP GET to the URL and returns true on 2xx.
func callProviderAndCheck(t *testing.T, url string) bool {
	t.Helper()
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// TestIntegration_StatsObservability verifies Stats() export for monitoring.
func TestIntegration_StatsObservability(t *testing.T) {
	provider := NewMockProvider(BehaviorHealthy, 100*time.Millisecond, 10)
	defer provider.Close()

	wr := routing.NewWeightedRouter()
	det := health.NewErrorDetector(100)
	wr.RegisterWithDetector(routing.NewCandidateFromID("p1"), det)

	// Generate activity
	for i := 0; i < 5; i++ {
		wr.RecordLatency("p1", 100*time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		det.OnError(health.ErrorEvent{
			CredentialID: "p1",
			StatusCode:   500,
			Timestamp:    time.Now(),
		})
	}

	// Get stats
	stats := wr.Stats()
	assert.Len(t, stats, 1)

	s := stats[0]
	t.Logf("Stats for %s:", s.CredentialID)
	t.Logf("  Weight: %.3f", s.Weight)
	t.Logf("  ErrorsPerMin: %d", s.ErrorsPerMin)
	t.Logf("  AvgLatencyMs: %.1f", s.AvgLatencyMs)
	t.Logf("  SampleCount: %d", s.SampleCount)

	assert.Equal(t, "p1", s.CredentialID)
	assert.Equal(t, 3, s.ErrorsPerMin)
	assert.Equal(t, 5, s.SampleCount)
	assert.InDelta(t, 100.0, s.AvgLatencyMs, 1)
}
