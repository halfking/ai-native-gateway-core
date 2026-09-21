package streaming

// preflightCompress is intentionally model-agnostic. It records the receipt-time
// estimate but does not trim the body: the supplier context window is unavailable
// until model candidates have been resolved. Candidate-aware compression happens
// later in the executor using provider.Candidate.ContextWindow and an 80% target.
//
// The gateway admission ceiling is promptBudgetDefaultTokens (2M by default).
// It is not a substitute for the supplier's context window and must not be used
// as a compression window.
func preflightCompress(body []byte, _ string) (newBody []byte, applied bool, estTokens int) {
	if len(body) == 0 {
		return body, false, 0
	}
	return body, false, estimateTokens(body)
}
