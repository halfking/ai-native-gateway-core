package autoroute

import (
	"math"
	"testing"
)

func TestComputeFeedback_SuccessCase(t *testing.T) {
	// Fully successful, fast, cheap, no drift → high quality
	sig := ComputeFeedback(FeedbackInput{
		Success:        1.0,
		LatencyMs:      200,
		P95BaselineMs:  2000, // 10% of baseline → latency_score ≈ 0.9
		CostUSD:        0.001,
		P75BaselineCost: 0.01, // 10% of baseline → cost_score ≈ 0.9
		DriftFlag:      false,
	})

	if sig.SuccessScore != 1.0 {
		t.Errorf("SuccessScore = %f, want 1.0", sig.SuccessScore)
	}
	if sig.QualityScore < 0.85 {
		t.Errorf("QualityScore = %f, expected >= 0.85 for ideal request", sig.QualityScore)
	}
}

func TestComputeFeedback_FailureCase(t *testing.T) {
	// Failed request → quality should be low regardless of latency/cost
	sig := ComputeFeedback(FeedbackInput{
		Success:        0.0,
		LatencyMs:      100,
		P95BaselineMs:  2000,
		CostUSD:        0.001,
		P75BaselineCost: 0.01,
		DriftFlag:      false,
	})

	if sig.SuccessScore != 0.0 {
		t.Errorf("SuccessScore = %f, want 0.0", sig.SuccessScore)
	}
	// 0.4*0 + 0.3*0.95 + 0.2*0.9 + 0.1*1 = 0 + 0.285 + 0.18 + 0.1 = 0.565
	// Quality is still moderate because latency/cost/drift are good,
	// but the success weight (0.4) ensures failures don't score high.
	if sig.QualityScore > 0.7 {
		t.Errorf("QualityScore = %f, expected < 0.7 for failed request", sig.QualityScore)
	}
}

func TestComputeFeedback_PartialSuccess(t *testing.T) {
	// Stream interrupted → 0.5 success
	sig := ComputeFeedback(FeedbackInput{
		Success:       0.5,
		LatencyMs:     1000,
		P95BaselineMs: 2000,
		DriftFlag:     false,
	})

	if sig.SuccessScore != 0.5 {
		t.Errorf("SuccessScore = %f, want 0.5", sig.SuccessScore)
	}
}

func TestComputeFeedback_UnknownBaselines(t *testing.T) {
	// No baseline data → latency/cost scores should default to 0.5
	sig := ComputeFeedback(FeedbackInput{
		Success:        1.0,
		LatencyMs:      5000,
		P95BaselineMs:  0, // unknown
		CostUSD:        1.0,
		P75BaselineCost: 0, // unknown
		DriftFlag:      false,
	})

	if sig.LatencyScore != 0.5 {
		t.Errorf("LatencyScore = %f, want 0.5 (unknown baseline)", sig.LatencyScore)
	}
	if sig.CostScore != 0.5 {
		t.Errorf("CostScore = %f, want 0.5 (unknown baseline)", sig.CostScore)
	}
}

func TestComputeFeedback_DriftPenalty(t *testing.T) {
	// Same metrics, but drift=true should lower quality
	noDrift := ComputeFeedback(FeedbackInput{
		Success:   1.0,
		DriftFlag: false,
	})
	withDrift := ComputeFeedback(FeedbackInput{
		Success:   1.0,
		DriftFlag: true,
	})

	if withDrift.QualityScore >= noDrift.QualityScore {
		t.Errorf("drift should lower quality: drift=%f >= no-drift=%f",
			withDrift.QualityScore, noDrift.QualityScore)
	}
	// Difference should be exactly 0.1 (the drift weight)
	diff := noDrift.QualityScore - withDrift.QualityScore
	if math.Abs(diff-0.1) > 0.001 {
		t.Errorf("quality diff = %f, want 0.1 (drift weight)", diff)
	}
}

func TestComputeFeedback_LatencyNormalization(t *testing.T) {
	tests := []struct {
		name     string
		latency  int
		baseline int
		want     float64
	}{
		{"instant", 0, 2000, 1.0},
		{"half_baseline", 1000, 2000, 0.5},
		{"at_baseline", 2000, 2000, 0.0},
		{"over_baseline", 3000, 2000, 0.0}, // clamped
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sig := ComputeFeedback(FeedbackInput{
				Success:       0, // neutralise
				LatencyMs:     tt.latency,
				P95BaselineMs: tt.baseline,
			})
			if math.Abs(sig.LatencyScore-tt.want) > 0.001 {
				t.Errorf("LatencyScore = %f, want %f", sig.LatencyScore, tt.want)
			}
		})
	}
}

func TestDetectSessionDrift(t *testing.T) {
	tests := []struct {
		name     string
		prev     TaskType
		curr     TaskType
		wantDrift bool
	}{
		{"same_task", TaskReasoning, TaskReasoning, false},
		{"empty_prev", "", TaskReasoning, false},
		{"chat_to_reasoning", TaskChat, TaskReasoning, true},
		{"reasoning_to_chat", TaskReasoning, TaskChat, true},
		{"chat_to_vision_override", TaskChat, TaskVision, false},
		{"chat_to_long_context", TaskChat, TaskLongContext, false},
		{"chat_to_agent", TaskChat, TaskAgent, false},
		{"code_to_creative", TaskCode, TaskCreative, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectSessionDrift(tt.prev, tt.curr)
			if got != tt.wantDrift {
				t.Errorf("DetectSessionDrift(%s→%s) = %v, want %v",
					tt.prev, tt.curr, got, tt.wantDrift)
			}
		})
	}
}

func TestClamp01(t *testing.T) {
	tests := []struct {
		in, want float64
	}{
		{-1, 0},
		{0, 0},
		{0.5, 0.5},
		{1, 1},
		{2, 1},
	}
	for _, tt := range tests {
		got := clamp01(tt.in)
		if got != tt.want {
			t.Errorf("clamp01(%f) = %f, want %f", tt.in, got, tt.want)
		}
	}
}
