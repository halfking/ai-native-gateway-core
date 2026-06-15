package maas

import "math"

// CalcCredits computes billable credits for a completed request.
// rateIn/rateOut of 0 fall back to basePer1M.
func CalcCredits(promptTokens, completionTokens int, rateIn, rateOut, basePer1M int64) int64 {
	if promptTokens <= 0 && completionTokens <= 0 {
		return 0
	}
	base := basePer1M
	if base <= 0 {
		base = 10000
	}
	in := rateIn
	if in <= 0 {
		in = base
	}
	out := rateOut
	if out <= 0 {
		out = base
	}
	numer := float64(promptTokens)*float64(in) + float64(completionTokens)*float64(out)
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
