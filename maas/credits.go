package maas

import "math"

// TokenUsage aggregates token counts charged by CalcCredits* helpers.
// It is the granular multimodal counterpart of the legacy 4-tuple
// (promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens).
// Empty fields (zero values) are treated as absent and contribute nothing
// to the billable amount; this keeps the legacy 4-tuple callers viable
// without modification.
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
	ImageTokens      int
	AudioTokens      int
	VideoTokens      int
}

// Any reports whether any token counter is non-zero.
func (t TokenUsage) Any() bool {
	return t.PromptTokens > 0 || t.CompletionTokens > 0 ||
		t.CacheReadTokens > 0 || t.CacheWriteTokens > 0 ||
		t.ImageTokens > 0 || t.AudioTokens > 0 || t.VideoTokens > 0
}

// CalcCredits computes billable credits for a completed request.
// Zero rate fields fall back to the matching global base (already discounted).
func CalcCredits(
	promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens int,
	rates ModelRateValues,
) int64 {
	return CalcCreditsMultimodal(TokenUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
	}, rates)
}

// CalcCreditsMultimodal computes billable credits using the full multimodal
// token breakdown. It is the canonical billing entry point; the four-tuple
// wrapper above preserves backward compatibility for code paths that have
// not yet been migrated to surface image/audio/video counters.
//
// The billing formula charges each token bucket at its own rate, then sums
// and rounds up per 1M tokens. Missing rate fields fall back to the input
// rate so text-only models continue to behave identically even after this
// change.
func CalcCreditsMultimodal(u TokenUsage, rates ModelRateValues) int64 {
	if !u.Any() {
		return 0
	}
	imageRate := rates.Image
	if imageRate <= 0 {
		imageRate = rates.In
	}
	audioRate := rates.Audio
	if audioRate <= 0 {
		audioRate = rates.In
	}
	videoRate := rates.Video
	if videoRate <= 0 {
		videoRate = rates.In
	}
	numer := float64(u.PromptTokens)*float64(rates.In) +
		float64(u.CompletionTokens)*float64(rates.Out) +
		float64(u.CacheReadTokens)*float64(rates.CacheIn) +
		float64(u.CacheWriteTokens)*float64(rates.CacheOut) +
		float64(u.ImageTokens)*float64(imageRate) +
		float64(u.AudioTokens)*float64(audioRate) +
		float64(u.VideoTokens)*float64(videoRate)
	if numer <= 0 {
		return 0
	}
	return int64(math.Ceil(numer / 1_000_000.0))
}

// InsufficientCreditsError is returned when a tenant cannot cover a charge.
type InsufficientCreditsError struct {
	TenantID  string
	Required  int64
	Available int64
}

func (e *InsufficientCreditsError) Error() string {
	return "insufficient credits for tenant " + e.TenantID
}
