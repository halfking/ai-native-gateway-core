package executors

import "time"

const predictiveTTFBReason = "predictive_ttfb_above_threshold"

// PredictiveSkipper applies the O-1 pre-skip policy using the existing TTFB
// history. A zero threshold disables the policy. The tracker owns freshness;
// Get returns nil for missing or expired observations.
type PredictiveSkipper struct {
	TTFB       TTFBRecorder
	Threshold  time.Duration
	MinSamples int
}

// NewPredictiveSkipper constructs the default-off O-1 policy.
func NewPredictiveSkipper(ttfb TTFBRecorder, threshold time.Duration, minSamples int) *PredictiveSkipper {
	if minSamples < 1 {
		minSamples = 1
	}
	return &PredictiveSkipper{TTFB: ttfb, Threshold: threshold, MinSamples: minSamples}
}

func (p *PredictiveSkipper) ShouldSkip(credentialID int) (PredictiveTTFBDecision, bool) {
	if p == nil || p.TTFB == nil || p.Threshold <= 0 {
		return PredictiveTTFBDecision{}, false
	}
	stats := p.TTFB.Get(credentialID)
	if stats == nil || stats.SampleCount < p.MinSamples || stats.AvgTTFB <= p.Threshold {
		return PredictiveTTFBDecision{}, false
	}
	return PredictiveTTFBDecision{
		Reason:      predictiveTTFBReason,
		AvgTTFB:     stats.AvgTTFB,
		SampleCount: stats.SampleCount,
	}, true
}

// predictiveDecisionForCandidate consumes the request-local decision budget
// before asking the skipper. This keeps the execution loop from scanning all
// candidates for a slow history entry after a normal candidate was found.
func predictiveDecisionForCandidate(skipper PredictiveTTFBSkipper, decided *bool, candidateCount, credentialID int) (PredictiveTTFBDecision, bool) {
	if skipper == nil || decided == nil || *decided || candidateCount <= 1 {
		return PredictiveTTFBDecision{}, false
	}
	*decided = true
	return skipper.ShouldSkip(credentialID)
}

var _ PredictiveTTFBSkipper = (*PredictiveSkipper)(nil)
