package admin

func isValidBillingMode(value string) bool {
	switch value {
	case "per_token", "free", "token_plan", "code_plan", "agent_plan":
		return true
	default:
		return false
	}
}
