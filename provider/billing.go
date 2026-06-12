package provider

import "strings"

// BillingRound classifies candidates for routing priority.
// Round 1: subscription / prepaid plans and free pool — tried first.
// Round 2: pay-as-you-go (按量) — only after round-1 credentials are saturated.
func BillingRound(mode string) int {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "free", "token_plan", "code_plan", "agent_plan", "monthly":
		return 1
	default:
		// token, per_token, per_request, empty → PAYG
		return 2
	}
}

func IsPreferredPlanBilling(mode string) bool {
	return BillingRound(mode) == 1
}

func IsPayAsYouGoBilling(mode string) bool {
	return BillingRound(mode) == 2
}
