package credential

import (
	"math"
	"testing"
)

func TestGetPresetWeights(t *testing.T) {
	tests := []struct {
		strategy RoutingStrategy
		want     RoutingWeights
	}{
		{
			strategy: StrategyBalanced,
			want:     RoutingWeights{Reliability: 0.4, Speed: 0.3, Intelligence: 0.3},
		},
		{
			strategy: StrategySmartest,
			want:     RoutingWeights{Reliability: 0.2, Speed: 0.1, Intelligence: 0.7},
		},
		{
			strategy: StrategyFastest,
			want:     RoutingWeights{Reliability: 0.3, Speed: 0.6, Intelligence: 0.1},
		},
		{
			strategy: StrategyReliable,
			want:     RoutingWeights{Reliability: 0.7, Speed: 0.2, Intelligence: 0.1},
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.strategy), func(t *testing.T) {
			got := GetPresetWeights(tt.strategy)
			if got != tt.want {
				t.Errorf("GetPresetWeights(%v) = %v, want %v", tt.strategy, got, tt.want)
			}
		})
	}
}

func TestRoutingWeights_Normalize(t *testing.T) {
	tests := []struct {
		name   string
		input  RoutingWeights
		want   RoutingWeights
	}{
		{
			name:  "already normalized",
			input: RoutingWeights{Reliability: 0.4, Speed: 0.3, Intelligence: 0.3},
			want:  RoutingWeights{Reliability: 0.4, Speed: 0.3, Intelligence: 0.3},
		},
		{
			name:  "needs normalization",
			input: RoutingWeights{Reliability: 2.0, Speed: 1.0, Intelligence: 1.0},
			want:  RoutingWeights{Reliability: 0.5, Speed: 0.25, Intelligence: 0.25},
		},
		{
			name:  "zero weights",
			input: RoutingWeights{Reliability: 0, Speed: 0, Intelligence: 0},
			want:  RoutingWeights{Reliability: 1.0 / 3.0, Speed: 1.0 / 3.0, Intelligence: 1.0 / 3.0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := tt.input
			w.Normalize()

			epsilon := 0.0001
			if math.Abs(w.Reliability-tt.want.Reliability) > epsilon ||
				math.Abs(w.Speed-tt.want.Speed) > epsilon ||
				math.Abs(w.Intelligence-tt.want.Intelligence) > epsilon {
				t.Errorf("Normalize() = %v, want %v", w, tt.want)
			}

			// Check sum is 1
			sum := w.Reliability + w.Speed + w.Intelligence
			if math.Abs(sum-1.0) > epsilon {
				t.Errorf("Sum of weights should be 1.0, got %v", sum)
			}
		})
	}
}

func TestScorerWithWeights_SampleWithWeights(t *testing.T) {
	scorer := NewScorerWithWeights(StrategyBalanced)

	// Record some history
	for i := 0; i < 10; i++ {
		scorer.Bandit.RecordSuccess("cred-1", 100)
	}
	scorer.Bandit.RecordFailure("cred-1")

	// Sample multiple times
	samples := make([]float64, 10)
	for i := 0; i < 10; i++ {
		samples[i] = scorer.SampleWithWeights("cred-1")
	}

	// Check that samples are in valid range
	for i, s := range samples {
		if s < 0 || s > 1 {
			t.Errorf("Sample %d out of range [0,1]: %v", i, s)
		}
	}
}

func TestExpectedReliability(t *testing.T) {
	tests := []struct {
		alpha float64
		beta  float64
		want  float64
	}{
		{alpha: 1.0, beta: 1.0, want: 0.5},    // Uniform prior
		{alpha: 10.0, beta: 1.0, want: 10.0 / 11.0}, // High success
		{alpha: 1.0, beta: 10.0, want: 1.0 / 11.0},  // High failure
		{alpha: 5.0, beta: 5.0, want: 0.5},    // Equal
	}

	for _, tt := range tests {
		got := ExpectedReliability(tt.alpha, tt.beta)
		if math.Abs(got-tt.want) > 0.0001 {
			t.Errorf("ExpectedReliability(%v, %v) = %v, want %v", tt.alpha, tt.beta, got, tt.want)
		}
	}
}

func TestReliabilityPosterior(t *testing.T) {
	tests := []struct {
		successes int64
		failures  int64
		wantAlpha float64
		wantBeta  float64
	}{
		{successes: 0, failures: 0, wantAlpha: 1.0, wantBeta: 1.0},   // No data
		{successes: 10, failures: 0, wantAlpha: 11.0, wantBeta: 1.0}, // All success
		{successes: 0, failures: 10, wantAlpha: 1.0, wantBeta: 11.0}, // All failure
		{successes: 5, failures: 5, wantAlpha: 6.0, wantBeta: 6.0},   // Balanced
	}

	for _, tt := range tests {
		alpha, beta := ReliabilityPosterior(tt.successes, tt.failures)
		if alpha != tt.wantAlpha || beta != tt.wantBeta {
			t.Errorf("ReliabilityPosterior(%v, %v) = (%v, %v), want (%v, %v)",
				tt.successes, tt.failures, alpha, beta, tt.wantAlpha, tt.wantBeta)
		}
	}
}

func TestConfidenceInterval(t *testing.T) {
	// Test with Beta(10, 10) - should be centered around 0.5
	alpha, beta := 10.0, 10.0
	lower, upper := ConfidenceInterval(alpha, beta)

	// Check that interval contains the mean
	mean := ExpectedReliability(alpha, beta)
	if lower > mean || upper < mean {
		t.Errorf("Confidence interval [%v, %v] should contain mean %v", lower, upper, mean)
	}

	// Check that interval is within [0, 1]
	if lower < 0 || upper > 1 {
		t.Errorf("Confidence interval should be in [0, 1], got [%v, %v]", lower, upper)
	}

	// Check that interval is reasonable width (not too wide, not too narrow)
	width := upper - lower
	if width < 0.05 || width > 0.5 {
		t.Errorf("Confidence interval width seems unreasonable: %v", width)
	}
}

func TestThompsonSamplingDecay(t *testing.T) {
	// Start with Beta(11, 3) - 10 successes, 2 failures
	alpha, beta := 11.0, 3.0
	decayFactor := 0.9

	newAlpha, newBeta := ThompsonSamplingDecay(alpha, beta, decayFactor)

	// Should move towards prior Beta(1, 1)
	if newAlpha >= alpha {
		t.Errorf("Decay should reduce alpha from %v, got %v", alpha, newAlpha)
	}
	if newBeta >= beta {
		t.Errorf("Decay should reduce beta from %v, got %v", beta, newBeta)
	}

	// Check that prior is preserved
	expectedAlpha := 1.0 + (alpha-1.0)*decayFactor
	expectedBeta := 1.0 + (beta-1.0)*decayFactor

	epsilon := 0.0001
	if math.Abs(newAlpha-expectedAlpha) > epsilon {
		t.Errorf("Expected alpha=%v, got %v", expectedAlpha, newAlpha)
	}
	if math.Abs(newBeta-expectedBeta) > epsilon {
		t.Errorf("Expected beta=%v, got %v", expectedBeta, newBeta)
	}
}

func TestBenchmarkIntelligenceRank(t *testing.T) {
	tests := []struct {
		modelName string
		maxRank   int // Maximum acceptable rank
	}{
		{"gpt-4o", 10},                 // Should be top tier
		{"claude-3.5-sonnet", 10},      // Should be top tier
		{"gpt-4-turbo", 10},            // Should be top tier
		{"gemini-1.5-pro", 10},         // Should be top tier
		{"llama-3.3-70b", 20},          // Should be good
		{"claude-3-haiku", 30},         // Should be mid-tier
		{"qwen-2.5-72b", 30},           // Should be mid-tier
		{"unknown-model-xyz", 100},     // Unknown should get default
	}

	for _, tt := range tests {
		rank := BenchmarkIntelligenceRank(tt.modelName)
		if rank > tt.maxRank {
			t.Errorf("BenchmarkIntelligenceRank(%v) = %v, want <= %v", tt.modelName, rank, tt.maxRank)
		}
		if rank < 1 || rank > 100 {
			t.Errorf("Rank should be in [1, 100], got %v for %v", rank, tt.modelName)
		}
	}
}

func TestBenchmarkIntelligenceRank_CaseInsensitive(t *testing.T) {
	rank1 := BenchmarkIntelligenceRank("GPT-4O")
	rank2 := BenchmarkIntelligenceRank("gpt-4o")
	rank3 := BenchmarkIntelligenceRank("Gpt-4O")

	if rank1 != rank2 || rank2 != rank3 {
		t.Errorf("Ranks should be case-insensitive, got %v, %v, %v", rank1, rank2, rank3)
	}
}

func TestSnapshotScore(t *testing.T) {
	scorer := NewBanditScorer()

	// Record some history
	for i := 0; i < 10; i++ {
		scorer.RecordSuccess("cred-1", 100)
	}
	for i := 0; i < 2; i++ {
		scorer.RecordFailure("cred-1")
	}
	scorer.RecordRateLimitHit("cred-1")
	scorer.UpdateQuota("cred-1", 800, 1000)

	snapshot := scorer.SnapshotScore("cred-1")

	// Check basic fields
	if snapshot.CredentialID != "cred-1" {
		t.Errorf("CredentialID should be 'cred-1', got %v", snapshot.CredentialID)
	}

	if snapshot.TotalRequests != 12 {
		t.Errorf("TotalRequests should be 12, got %v", snapshot.TotalRequests)
	}

	expectedSuccessRate := 10.0 / 12.0
	epsilon := 0.0001
	if math.Abs(snapshot.SuccessRate-expectedSuccessRate) > epsilon {
		t.Errorf("SuccessRate should be ~%v, got %v", expectedSuccessRate, snapshot.SuccessRate)
	}

	// AvgLatencyMs = 1000 / 12 (only successes contribute to latency)
	expectedAvgLatency := 1000.0 / 12.0
	if math.Abs(snapshot.AvgLatencyMs-expectedAvgLatency) > 1.0 {
		t.Errorf("AvgLatencyMs should be ~%v, got %v", expectedAvgLatency, snapshot.AvgLatencyMs)
	}

	if snapshot.RateLimitHits != 1 {
		t.Errorf("RateLimitHits should be 1, got %v", snapshot.RateLimitHits)
	}

	if snapshot.QuotaRemaining == nil || *snapshot.QuotaRemaining != 800 {
		t.Errorf("QuotaRemaining should be 800, got %v", snapshot.QuotaRemaining)
	}

	// Check that score components are in valid range
	if snapshot.Reliability < 0 || snapshot.Reliability > 1 {
		t.Errorf("Reliability should be in [0,1], got %v", snapshot.Reliability)
	}
	if snapshot.Speed < 0 || snapshot.Speed > 1 {
		t.Errorf("Speed should be in [0,1], got %v", snapshot.Speed)
	}
	if snapshot.Intelligence < 0 || snapshot.Intelligence > 1 {
		t.Errorf("Intelligence should be in [0,1], got %v", snapshot.Intelligence)
	}
	if snapshot.CombinedScore < 0 || snapshot.CombinedScore > 1 {
		t.Errorf("CombinedScore should be in [0,1], got %v", snapshot.CombinedScore)
	}
}

func TestContainsIgnoreCase(t *testing.T) {
	tests := []struct {
		s      string
		substr string
		want   bool
	}{
		{"gpt-4o", "gpt", true},
		{"GPT-4O", "gpt", true},
		{"Gpt-4O", "GPT", true},
		{"claude-3-opus", "opus", true},
		{"claude-3-opus", "OPUS", true},
		{"llama-3", "mistral", false},
		{"", "", true},
		{"test", "", true},
		{"", "test", false},
	}

	for _, tt := range tests {
		got := containsIgnoreCase(tt.s, tt.substr)
		if got != tt.want {
			t.Errorf("containsIgnoreCase(%q, %q) = %v, want %v", tt.s, tt.substr, got, tt.want)
		}
	}
}

func TestScorerWithWeights_DifferentStrategies(t *testing.T) {
	strategies := []RoutingStrategy{
		StrategyBalanced,
		StrategySmartest,
		StrategyFastest,
		StrategyReliable,
	}

	for _, strategy := range strategies {
		t.Run(string(strategy), func(t *testing.T) {
			scorer := NewScorerWithWeights(strategy)

			// Record history for a high-intelligence, fast, reliable credential
			for i := 0; i < 20; i++ {
				scorer.Bandit.RecordSuccess("cred-1", 50) // Fast
			}
			scorer.Bandit.GetScore("cred-1").IntelligenceRank = 1 // Smart

			// Record history for a low-intelligence, slow, unreliable credential
			for i := 0; i < 10; i++ {
				scorer.Bandit.RecordSuccess("cred-2", 2000) // Slow
			}
			for i := 0; i < 10; i++ {
				scorer.Bandit.RecordFailure("cred-2") // Unreliable
			}
			scorer.Bandit.GetScore("cred-2").IntelligenceRank = 80 // Not smart

			// Sample both
			score1 := scorer.SampleWithWeights("cred-1")
			score2 := scorer.SampleWithWeights("cred-2")

			// Cred-1 should generally score higher regardless of strategy
			if score1 <= score2 {
				t.Logf("Warning: cred-1 (good) scored %v, cred-2 (bad) scored %v - may happen occasionally due to sampling", score1, score2)
			}
		})
	}
}
