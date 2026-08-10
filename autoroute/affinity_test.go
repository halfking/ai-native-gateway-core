package autoroute

import (
	"math"
	"testing"
	"time"
)

func approx(t *testing.T, got, want, tol float64, label string) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s: got %.4f, want %.4f (tol %.4f)", label, got, want, tol)
	}
}

// The central safety property: a tiny sample must not move affinity much,
// no matter how good or bad the observed reward is.
func TestShrinkAffinity_SmallSampleStaysNearNeutral(t *testing.T) {
	// n=1 with a perfect reward: w = 1/31 ≈ 0.032 → ~51.6, not ~100.
	got := ShrinkAffinity(1.0, 1)
	if got > 55 {
		t.Errorf("n=1 perfect reward moved affinity to %.2f; expected to stay near neutral", got)
	}
	// Symmetrically, one catastrophic result must not tank a model.
	got = ShrinkAffinity(0.0, 1)
	if got < 45 {
		t.Errorf("n=1 zero reward moved affinity to %.2f; expected to stay near neutral", got)
	}
}

func TestShrinkAffinity_Table(t *testing.T) {
	cases := []struct {
		name   string
		reward float64
		n      int
		want   float64
	}{
		// w = n/(n+30); affinity = (w*r + (1-w)*0.5) * 100
		{"zero samples is exactly neutral", 0.9, 0, 50},
		{"n=30 is half weight", 1.0, 30, 75},        // 0.5*1 + 0.5*0.5 = 0.75
		{"n=30 bad is half weight", 0.0, 30, 25},    // 0.5*0 + 0.5*0.5 = 0.25
		{"n=270 approaches observed", 1.0, 270, 95}, // w=0.9
		{"neutral reward stays neutral", 0.5, 1000, 50},
		{"large n converges to reward", 0.8, 30000, 79.97},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			approx(t, ShrinkAffinity(tc.reward, tc.n), tc.want, 0.1, "affinity")
		})
	}
}

func TestShrinkAffinity_RejectsGarbageInput(t *testing.T) {
	if got := ShrinkAffinity(math.NaN(), 100); got != AffinityNeutral {
		t.Errorf("NaN reward: got %.2f, want neutral", got)
	}
	if got := ShrinkAffinity(math.Inf(1), 100); got != AffinityNeutral {
		t.Errorf("Inf reward: got %.2f, want neutral", got)
	}
	// Out-of-range rewards are clamped, not propagated.
	if got := ShrinkAffinity(5.0, 30000); got > 100.01 {
		t.Errorf("reward>1 produced affinity %.2f, want <=100", got)
	}
	if got := ShrinkAffinity(-5.0, 30000); got < -0.01 {
		t.Errorf("reward<0 produced affinity %.2f, want >=0", got)
	}
}

func TestEffectiveAffinity_MinSampleFloor(t *testing.T) {
	now := time.Now()
	// An extreme stored affinity with too few samples must read as neutral.
	r := AffinityRecord{Affinity: 95, SampleCount: AffinityMinSamples - 1, LastSampledAt: now}
	if got := r.EffectiveAffinity(now); got != AffinityNeutral {
		t.Errorf("below min samples: got %.2f, want neutral %.2f", got, AffinityNeutral)
	}
	// At the threshold it starts to count (clamped to the max deviation).
	r.SampleCount = AffinityMinSamples
	if got := r.EffectiveAffinity(now); got == AffinityNeutral {
		t.Error("at min samples affinity should take effect, got neutral")
	}
}

func TestClampAffinity_BoundsDeviation(t *testing.T) {
	if got := ClampAffinity(100); got != AffinityNeutral+AffinityMaxDeviation {
		t.Errorf("got %.2f, want %.2f", got, AffinityNeutral+AffinityMaxDeviation)
	}
	if got := ClampAffinity(0); got != AffinityNeutral-AffinityMaxDeviation {
		t.Errorf("got %.2f, want %.2f", got, AffinityNeutral-AffinityMaxDeviation)
	}
	if got := ClampAffinity(60); got != 60 {
		t.Errorf("in-range value was altered: got %.2f, want 60", got)
	}
	if got := ClampAffinity(math.NaN()); got != AffinityNeutral {
		t.Errorf("NaN: got %.2f, want neutral", got)
	}
}

func TestDecayAffinity_OnlyAfterStalePeriod(t *testing.T) {
	now := time.Now()

	// Fresh data is untouched.
	fresh := now.Add(-1 * time.Hour)
	if got := DecayAffinity(90, fresh, now); got != 90 {
		t.Errorf("fresh row decayed: got %.2f, want 90", got)
	}

	// Exactly at the boundary is still untouched.
	at := now.Add(-AffinityStaleAfter)
	if got := DecayAffinity(90, at, now); got != 90 {
		t.Errorf("row at stale boundary decayed: got %.2f, want 90", got)
	}

	// One day past the boundary sheds 5% of the deviation: 50 + 40*0.95 = 88.
	oneDayStale := now.Add(-AffinityStaleAfter - 24*time.Hour)
	approx(t, DecayAffinity(90, oneDayStale, now), 88.0, 0.01, "1 day stale")

	// Decay is monotone toward neutral and never overshoots past it.
	prev := 90.0
	for d := 1; d <= 400; d++ {
		s := now.Add(-AffinityStaleAfter - time.Duration(d)*24*time.Hour)
		got := DecayAffinity(90, s, now)
		if got > prev+1e-9 {
			t.Fatalf("decay not monotone at day %d: %.4f > %.4f", d, got, prev)
		}
		if got < AffinityNeutral-1e-9 {
			t.Fatalf("decay overshot neutral at day %d: %.4f", d, got)
		}
		prev = got
	}

	// Below-neutral scores decay upward toward neutral, not further down.
	approx(t, DecayAffinity(10, oneDayStale, now), 12.0, 0.01, "below-neutral decay")

	// A zero timestamp means "unknown", so leave the value alone.
	if got := DecayAffinity(90, time.Time{}, now); got != 90 {
		t.Errorf("zero LastSampledAt decayed: got %.2f, want 90", got)
	}
}

// A dead worker must degrade to "no opinion" rather than pinning routing.
func TestDecayAffinity_ConvergesToNeutralWhenWorkerDies(t *testing.T) {
	now := time.Now()
	longDead := now.Add(-AffinityStaleAfter - 365*24*time.Hour)
	got := DecayAffinity(90, longDead, now)
	if math.Abs(got-AffinityNeutral) > 1.0 {
		t.Errorf("after a year unsampled, affinity %.2f should be ~neutral", got)
	}
}

func TestUpdateEMA(t *testing.T) {
	// No history seeds directly from the first observation.
	if got := UpdateEMA(0, 0.8); got != 0.8 {
		t.Errorf("seeding: got %.4f, want 0.8", got)
	}
	// Blend: 0.15*1.0 + 0.85*0.5 = 0.575
	approx(t, UpdateEMA(0.5, 1.0), 0.575, 1e-9, "blend")
	// A single bad window must not erase a long good history.
	if got := UpdateEMA(0.9, 0.0); got < 0.7 {
		t.Errorf("one bad window dropped EMA to %.4f; too reactive", got)
	}
	// Garbage input leaves the EMA untouched.
	if got := UpdateEMA(0.7, math.NaN()); got != 0.7 {
		t.Errorf("NaN input changed EMA to %.4f", got)
	}
	// Repeated identical observations converge to that value.
	ema := 0.2
	for i := 0; i < 500; i++ {
		ema = UpdateEMA(ema, 0.9)
	}
	approx(t, ema, 0.9, 0.01, "convergence")
}

func TestAffinityConfidence(t *testing.T) {
	if got := AffinityConfidence(0); got != 0 {
		t.Errorf("n=0: got %.4f, want 0", got)
	}
	approx(t, AffinityConfidence(30), 0.5, 1e-9, "n=K is half confidence")
	if AffinityConfidence(10000) <= 0.99 {
		t.Errorf("large n should approach 1, got %.4f", AffinityConfidence(10000))
	}
	// Confidence must increase monotonically with evidence.
	prev := -1.0
	for _, n := range []int{1, 5, 10, 30, 100, 1000} {
		c := AffinityConfidence(n)
		if c <= prev {
			t.Fatalf("confidence not monotone at n=%d: %.4f <= %.4f", n, c, prev)
		}
		prev = c
	}
}

func TestShouldExplore_RatioAndDeterminism(t *testing.T) {
	if ShouldExplore("req-1", 0) {
		t.Error("ratio 0 must never explore")
	}
	if !ShouldExplore("req-1", 1.0) {
		t.Error("ratio 1 must always explore")
	}
	if ShouldExplore("", 0.5) {
		t.Error("empty request id must not explore")
	}

	// Same input, same answer — the recorded `explore` flag must be truthful.
	first := ShouldExplore("req-stable", 0.5)
	for i := 0; i < 50; i++ {
		if ShouldExplore("req-stable", 0.5) != first {
			t.Fatal("ShouldExplore is not deterministic")
		}
	}

	// Distribution should land near the requested ratio over many ids.
	const n = 20000
	count := 0
	for i := 0; i < n; i++ {
		if ShouldExplore("request-id-"+itoa(i), AffinityDefaultExploreRatio) {
			count++
		}
	}
	got := float64(count) / n
	if math.Abs(got-AffinityDefaultExploreRatio) > 0.02 {
		t.Errorf("explore ratio %.4f deviates from target %.4f", got, AffinityDefaultExploreRatio)
	}
}

// Explore bucketing must not correlate with A/B strategy assignment, or the
// two experiments would confound each other.
func TestShouldExplore_IndependentOfStrategyAssignment(t *testing.T) {
	const n = 20000
	var exploreAndBaseline, explores, baselines int
	for i := 0; i < n; i++ {
		id := "req-" + itoa(i)
		e := ShouldExplore(id, 0.5)
		b := AssignStrategy(id) == StrategyBaseline
		if e {
			explores++
		}
		if b {
			baselines++
		}
		if e && b {
			exploreAndBaseline++
		}
	}
	if explores == 0 || baselines == 0 {
		t.Skip("degenerate buckets; nothing to compare")
	}
	// Under independence, P(explore ∧ baseline) ≈ P(explore)*P(baseline).
	expected := (float64(explores) / n) * (float64(baselines) / n)
	actual := float64(exploreAndBaseline) / n
	if math.Abs(actual-expected) > 0.02 {
		t.Errorf("explore and strategy appear correlated: joint %.4f vs expected %.4f", actual, expected)
	}
}

func TestComputeRoutingReward_UnknownsAreNeutralNotZero(t *testing.T) {
	// Success with no baselines available: latency/cost/health all neutral 0.5.
	// 0.45*1 + 0.20*0.5 + 0.15*0.5 + 0.10*0.5 + 0.10*1 = 0.775
	got := ComputeRoutingReward(RewardInput{Success: 1, HealthComponent: -1})
	approx(t, got, 0.775, 1e-9, "success without baselines")

	// The same call must score strictly better than an outright failure.
	fail := ComputeRoutingReward(RewardInput{Success: 0, HealthComponent: -1})
	if got <= fail {
		t.Errorf("success (%.4f) must outscore failure (%.4f)", got, fail)
	}
}

func TestComputeRoutingReward_Ordering(t *testing.T) {
	base := RewardInput{
		Success: 1, LatencyMs: 1000, P95BaselineMs: 2000,
		CostUSD: 0.001, P75BaselineCost: 0.002,
		HealthComponent: 0.9, RetryRatio: 0,
	}
	good := ComputeRoutingReward(base)

	slower := base
	slower.LatencyMs = 4000 // beyond the cohort p95
	if ComputeRoutingReward(slower) >= good {
		t.Error("a slower request must score lower")
	}

	pricier := base
	pricier.CostUSD = 0.01
	if ComputeRoutingReward(pricier) >= good {
		t.Error("a more expensive request must score lower")
	}

	retried := base
	retried.RetryRatio = 1.0
	if ComputeRoutingReward(retried) >= good {
		t.Error("heavy retrying must score lower")
	}

	unhealthy := base
	unhealthy.HealthComponent = 0.1
	if ComputeRoutingReward(unhealthy) >= good {
		t.Error("a worse session health must score lower")
	}
}

func TestComputeRoutingReward_AlwaysInRange(t *testing.T) {
	inputs := []RewardInput{
		{},
		{Success: 1, LatencyMs: 1 << 30, P95BaselineMs: 1, CostUSD: 1e9, P75BaselineCost: 1e-9, RetryRatio: 10},
		{Success: -5, HealthComponent: -100, RetryRatio: -3},
		{Success: 99, HealthComponent: 99, RetryRatio: 99},
		{Success: 0.5, LatencyMs: -10, P95BaselineMs: -10, CostUSD: -1, P75BaselineCost: -1},
	}
	for i, in := range inputs {
		got := ComputeRoutingReward(in)
		if got < 0 || got > 1 || math.IsNaN(got) {
			t.Errorf("input %d produced out-of-range reward %.4f", i, got)
		}
	}
}

func TestComputeRoutingReward_ZeroWeightsDoNotDivideByZero(t *testing.T) {
	got := ComputeRoutingRewardWithWeights(RewardInput{Success: 1}, RoutingRewardWeights{})
	if got != 0.5 {
		t.Errorf("all-zero weights: got %.4f, want neutral 0.5", got)
	}
}

// A latency baseline equal to the model's own latency would make every model
// score identically; guard that the cohort baseline actually discriminates.
func TestComputeRoutingReward_CohortBaselineDiscriminates(t *testing.T) {
	const cohortP95 = 2000
	fast := ComputeRoutingReward(RewardInput{Success: 1, LatencyMs: 200, P95BaselineMs: cohortP95, HealthComponent: -1})
	slow := ComputeRoutingReward(RewardInput{Success: 1, LatencyMs: 1900, P95BaselineMs: cohortP95, HealthComponent: -1})
	if fast <= slow {
		t.Errorf("cohort baseline failed to separate fast (%.4f) from slow (%.4f)", fast, slow)
	}
}

func TestShouldAttributeSession(t *testing.T) {
	cases := []struct {
		model, total int
		want         bool
	}{
		{10, 10, true},   // sole model
		{8, 10, true},    // exactly at the 80% threshold
		{79, 100, false}, // just below
		{1, 20, false},   // must not inherit a session it barely touched
		{0, 10, false},
		{5, 0, false}, // no division by zero
		{-1, 10, false},
	}
	for _, tc := range cases {
		if got := ShouldAttributeSession(tc.model, tc.total); got != tc.want {
			t.Errorf("ShouldAttributeSession(%d,%d) = %v, want %v", tc.model, tc.total, got, tc.want)
		}
	}
}

func TestParseAffinityMode_UnknownFallsBackToShadow(t *testing.T) {
	cases := map[string]AffinityMode{
		"off":     AffinityOff,
		"on":      AffinityOn,
		"shadow":  AffinityShadow,
		"":        AffinityShadow,
		"ON":      AffinityShadow, // case-sensitive by design
		"enabled": AffinityShadow,
		"typo":    AffinityShadow,
	}
	for in, want := range cases {
		if got := ParseAffinityMode(in); got != want {
			t.Errorf("ParseAffinityMode(%q) = %q, want %q", in, got, want)
		}
	}
}
