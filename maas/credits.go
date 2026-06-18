package maas

import "math"

// CalcCredits computes billable credits for a completed request.
// Zero rate fields fall back to the matching global base (already discounted).
func CalcCredits(
	promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens int,
	rates ModelRateValues,
) int64 {
	if promptTokens <= 0 && completionTokens <= 0 && cacheReadTokens <= 0 && cacheWriteTokens <= 0 {
		return 0
	}
	numer := float64(promptTokens)*float64(rates.In) +
		float64(completionTokens)*float64(rates.Out) +
		float64(cacheReadTokens)*float64(rates.CacheIn) +
		float64(cacheWriteTokens)*float64(rates.CacheOut)
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
