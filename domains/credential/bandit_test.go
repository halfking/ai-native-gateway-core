package credential

import (
	"math"
	"testing"
	"time"
)

func TestBanditScorer_BasicOperations(t *testing.T) {
	scorer := NewBanditScorer()

	// Test initial score
	score := scorer.GetScore("cred-1")
	if score.Alpha != 1.0 || score.Beta != 1.0 {
		t.Errorf("Initial score should have Alpha=1, Beta=1, got Alpha=%v, Beta=%v", score.Alpha, score.Beta)
	}

	// Test record success
	scorer.RecordSuccess("cred-1", 100)
	score = scorer.GetScore("cred-1")
	if score.Alpha != 2.0 {
		t.Errorf("After success, Alpha should be 2.0, got %v", score.Alpha)
	}
	if score.SuccessRequests != 1 {
		t.Errorf("SuccessRequests should be 1, got %v", score.SuccessRequests)
	}
	if score.TotalLatencyMs != 100 {
		t.Errorf("TotalLatencyMs should be 100, got %v", score.TotalLatencyMs)
	}

	// Test record failure
	scorer.RecordFailure("cred-1")
	score = scorer.GetScore("cred-1")
	if score.Beta != 2.0 {
		t.Errorf("After failure, Beta should be 2.0, got %v", score.Beta)
	}
	if score.FailureRequests != 1 {
		t.Errorf("FailureRequests should be 1, got %v", score.FailureRequests)
	}
}

func TestBanditScorer_RateLimitPenalty(t *testing.T) {
	scorer := NewBanditScorer()

	// First 429
	scorer.RecordRateLimitHit("cred-1")
	score := scorer.GetScore("cred-1")
	if score.RateLimitPenalty != 3.0 {
		t.Errorf("First 429 should result in penalty=3.0, got %v", score.RateLimitPenalty)
	}

	// Second 429 (immediate)
	scorer.RecordRateLimitHit("cred-1")
	score = scorer.GetScore("cred-1")
	if math.Abs(score.RateLimitPenalty-6.0) > 0.001 {
		t.Errorf("Second 429 should result in penalty=6.0, got %v", score.RateLimitPenalty)
	}

	// Third 429
	scorer.RecordRateLimitHit("cred-1")
	score = scorer.GetScore("cred-1")
	if math.Abs(score.RateLimitPenalty-9.0) > 0.001 {
		t.Errorf("Third 429 should result in penalty=9.0, got %v", score.RateLimitPenalty)
	}

	// Fourth 429 (should cap at 10)
	scorer.RecordRateLimitHit("cred-1")
	score = scorer.GetScore("cred-1")
	if score.RateLimitPenalty != 10.0 {
		t.Errorf("Penalty should cap at 10.0, got %v", score.RateLimitPenalty)
	}
}

func TestBanditScorer_QuotaTracking(t *testing.T) {
	scorer := NewBanditScorer()

	// Update quota
	scorer.UpdateQuota("cred-1", 500, 1000)
	score := scorer.GetScore("cred-1")

	if score.QuotaRemaining == nil || *score.QuotaRemaining != 500 {
		t.Errorf("QuotaRemaining should be 500, got %v", score.QuotaRemaining)
	}
	if score.QuotaTotal == nil || *score.QuotaTotal != 1000 {
		t.Errorf("QuotaTotal should be 1000, got %v", score.QuotaTotal)
	}
}

func TestBanditScorer_Sample(t *testing.T) {
	scorer := NewBanditScorer()

	// Record some history
	for i := 0; i < 10; i++ {
		scorer.RecordSuccess("cred-1", 100)
	}
	scorer.RecordFailure("cred-1")

	// Sample multiple times
	samples := make([]float64, 100)
	for i := 0; i < 100; i++ {
		samples[i] = scorer.Sample("cred-1")
	}

	// Check that samples are in valid range
	for i, s := range samples {
		if s < 0 || s > 1 {
			t.Errorf("Sample %d out of range [0,1]: %v", i, s)
		}
	}

	// Check that there's variance (Thompson Sampling is stochastic)
	min := samples[0]
	max := samples[0]
	for _, s := range samples {
		if s < min {
			min = s
		}
		if s > max {
			max = s
		}
	}
	if max-min < 0.01 {
		t.Errorf("Expected variance in samples, got range [%v, %v]", min, max)
	}
}

func TestBanditScorer_SpeedScore(t *testing.T) {
	scorer := NewBanditScorer()

	// Fast credential (100ms average)
	for i := 0; i < 10; i++ {
		scorer.RecordSuccess("fast", 100)
	}

	// Slow credential (2000ms average)
	for i := 0; i < 10; i++ {
		scorer.RecordSuccess("slow", 2000)
	}

	fastScore := scorer.GetScore("fast")
	slowScore := scorer.GetScore("slow")

	fastSpeed := scorer.speedScore(fastScore)
	slowSpeed := scorer.speedScore(slowScore)

	if fastSpeed <= slowSpeed {
		t.Errorf("Fast credential should have higher speed score, got fast=%v, slow=%v", fastSpeed, slowSpeed)
	}
}

func TestBanditScorer_HeadroomFactor(t *testing.T) {
	scorer := NewBanditScorer()

	// 50% quota remaining
	scorer.UpdateQuota("cred-1", 500, 1000)
	score1 := scorer.GetScore("cred-1")
	headroom1 := scorer.headroomFactor(score1)

	// 10% quota remaining
	scorer.UpdateQuota("cred-2", 100, 1000)
	score2 := scorer.GetScore("cred-2")
	headroom2 := scorer.headroomFactor(score2)

	if headroom1 <= headroom2 {
		t.Errorf("Higher quota remaining should have higher headroom factor, got 50%%=%v, 10%%=%v", headroom1, headroom2)
	}
}

func TestBanditScorer_RateLimitFactor(t *testing.T) {
	scorer := NewBanditScorer()

	// No 429s
	score1 := scorer.GetScore("cred-1")
	factor1 := scorer.rateLimitFactor(score1)

	// Multiple 429s
	scorer.RecordRateLimitHit("cred-2")
	scorer.RecordRateLimitHit("cred-2")
	scorer.RecordRateLimitHit("cred-2")
	score2 := scorer.GetScore("cred-2")
	factor2 := scorer.rateLimitFactor(score2)

	if factor1 <= factor2 {
		t.Errorf("No 429s should have higher factor than multiple 429s, got clean=%v, penalized=%v", factor1, factor2)
	}
}

func TestBanditScorer_Reset(t *testing.T) {
	scorer := NewBanditScorer()

	scorer.RecordSuccess("cred-1", 100)
	scorer.RecordRateLimitHit("cred-1")

	// Reset specific credential
	scorer.Reset("cred-1")
	score := scorer.GetScore("cred-1")

	if score.Alpha != 1.0 || score.Beta != 1.0 {
		t.Errorf("After reset, should have default values, got Alpha=%v, Beta=%v", score.Alpha, score.Beta)
	}
	if score.RateLimitHits != 0 {
		t.Errorf("After reset, RateLimitHits should be 0, got %v", score.RateLimitHits)
	}
}

func TestBanditScorer_ResetAll(t *testing.T) {
	scorer := NewBanditScorer()

	scorer.RecordSuccess("cred-1", 100)
	scorer.RecordSuccess("cred-2", 200)

	scorer.ResetAll()
	allScores := scorer.GetAllScores()

	if len(allScores) != 0 {
		t.Errorf("After ResetAll, should have no scores, got %v", len(allScores))
	}
}

func TestBanditScorer_ConcurrentAccess(t *testing.T) {
	scorer := NewBanditScorer()

	// Simulate concurrent access
	done := make(chan bool)

	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				credID := "cred-1"
				scorer.RecordSuccess(credID, int64(j*10))
				scorer.Sample(credID)
			}
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	score := scorer.GetScore("cred-1")
	if score.TotalRequests != 1000 {
		t.Errorf("Expected 1000 total requests, got %v", score.TotalRequests)
	}
}

func TestSampleBeta_Distribution(t *testing.T) {
	scorer := NewBanditScorer()

	// Test Beta(2, 2) - should be centered around 0.5
	samples := make([]float64, 1000)
	for i := 0; i < 1000; i++ {
		samples[i] = scorer.sampleBeta(2.0, 2.0)
	}

	// Calculate mean
	sum := 0.0
	for _, s := range samples {
		sum += s
	}
	mean := sum / float64(len(samples))

	// Beta(2, 2) has mean = 2/(2+2) = 0.5
	expectedMean := 0.5
	if math.Abs(mean-expectedMean) > 0.05 {
		t.Errorf("Beta(2,2) mean should be ~0.5, got %v", mean)
	}
}

func TestSampleGamma_Distribution(t *testing.T) {
	scorer := NewBanditScorer()

	// Test Gamma(2, 1) - should have mean = shape * scale = 2
	samples := make([]float64, 1000)
	for i := 0; i < 1000; i++ {
		samples[i] = scorer.sampleGamma(2.0, 1.0)
	}

	// Calculate mean
	sum := 0.0
	for _, s := range samples {
		sum += s
	}
	mean := sum / float64(len(samples))

	// Gamma(2, 1) has mean = 2
	expectedMean := 2.0
	if math.Abs(mean-expectedMean) > 0.2 {
		t.Errorf("Gamma(2,1) mean should be ~2.0, got %v", mean)
	}
}

func TestBanditScorer_PenaltyDecay(t *testing.T) {
	scorer := NewBanditScorer()

	// Record a 429
	scorer.RecordRateLimitHit("cred-1")
	score := scorer.GetScore("cred-1")
	initialPenalty := score.RateLimitPenalty

	// Manually set LastRateLimitHit to simulate time passing
	score.LastRateLimitHit = time.Now().Add(-3 * time.Minute)
	scorer.mu.Lock()
	scorer.scores["cred-1"] = score
	scorer.mu.Unlock()

	// Record another 429 (should decay first)
	scorer.RecordRateLimitHit("cred-1")
	score = scorer.GetScore("cred-1")

	// After 3 minutes (1.5 decay steps), penalty should have decayed by ~1.5
	// Initial: 3.0, after decay: ~1.5, then +3.0 = ~4.5
	expectedPenalty := initialPenalty - 1.5 + 3.0
	if math.Abs(score.RateLimitPenalty-expectedPenalty) > 0.5 {
		t.Errorf("Expected penalty ~%v after decay, got %v", expectedPenalty, score.RateLimitPenalty)
	}
}
