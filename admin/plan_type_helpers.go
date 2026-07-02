package admin

// Steps 3-5 minimal implementation for plan_type standardization.
// This file contains the helper functions and will be integrated into
// provider_credential.go, pricing.go, and routing.go via edits.

// validPlanTypes mirrors migrations/136 CHECK constraint.
var validPlanTypes = map[string]bool{
	"token": true, "token_plan": true, "code_plan": true,
	"agent_plan": true, "monthly": true, "free": true,
}

func isValidPlanType(s string) bool { return validPlanTypes[s] }

// deriveBillingModeSQL is the CASE expression used in multiple UPDATEs.
const deriveBillingModeSQL = `CASE WHEN $1 = 'token' THEN 'per_token' ELSE $1 END`
