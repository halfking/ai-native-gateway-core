package admin

// Credential display state is intentionally derived from the orthogonal
// persistence axes. Runtime routing keeps using the original columns; this
// value is the single operator-facing state used by admin APIs and UI.
type CredentialDisplayState string

const (
	CredentialStateActive         CredentialDisplayState = "active"
	CredentialStateCooling        CredentialDisplayState = "cooling"
	CredentialStateDegraded       CredentialDisplayState = "degraded"
	CredentialStateRateLimited    CredentialDisplayState = "rate_limited"
	CredentialStateUnreachable    CredentialDisplayState = "unreachable"
	CredentialStateAuthFailed     CredentialDisplayState = "auth_failed"
	CredentialStateSuspended      CredentialDisplayState = "suspended"
	CredentialStateQuotaExhausted CredentialDisplayState = "quota_exhausted"
	CredentialStateDisabled       CredentialDisplayState = "disabled"
	CredentialStateDeleted        CredentialDisplayState = "deleted"
	CredentialStateUnknown        CredentialDisplayState = "unknown"
)

var credentialStatuses = map[string]struct{}{
	"active": {}, "cooling": {}, "degraded": {}, "quarantine": {},
	"quota_expired": {}, "disabled": {}, "deleted": {},
}

func isCredentialStatus(value string) bool {
	_, ok := credentialStatuses[value]
	return ok
}

type credentialStateInput struct {
	Status          string
	LifecycleStatus string
	Availability    string
	QuotaState      string
	HealthStatus    string
	ManualDisabled  bool
}

type credentialDisplayState struct {
	State  CredentialDisplayState
	Reason string
}

// deriveCredentialDisplayState applies the documented precedence from most
// terminal/operator-controlled to least severe transient condition.
func deriveCredentialDisplayState(in credentialStateInput) credentialDisplayState {
	if in.Status == "deleted" || in.LifecycleStatus == "retired" {
		return credentialDisplayState{CredentialStateDeleted, "deleted"}
	}
	if in.ManualDisabled || in.Status == "disabled" || in.LifecycleStatus == "disabled" {
		return credentialDisplayState{CredentialStateDisabled, "manual_disabled"}
	}
	if in.Availability == "auth_failed" || in.HealthStatus == "unreachable" && in.Availability == "auth_failed" {
		return credentialDisplayState{CredentialStateAuthFailed, "auth_failed"}
	}
	if in.QuotaState == "balance_exhausted" || in.QuotaState == "permanently_exhausted" || in.QuotaState == "periodic_exhausted" || in.Status == "quota_expired" {
		return credentialDisplayState{CredentialStateQuotaExhausted, in.QuotaState}
	}
	if in.Availability == "suspended" {
		return credentialDisplayState{CredentialStateSuspended, "suspended"}
	}
	if in.Availability == "rate_limited" {
		return credentialDisplayState{CredentialStateRateLimited, "rate_limited"}
	}
	if in.Availability == "unreachable" || in.HealthStatus == "unreachable" {
		return credentialDisplayState{CredentialStateUnreachable, "unreachable"}
	}
	if in.Availability == "cooling" || in.Status == "cooling" {
		return credentialDisplayState{CredentialStateCooling, "cooling"}
	}
	if in.Availability == "degraded" || in.Status == "degraded" || in.HealthStatus == "warning" {
		return credentialDisplayState{CredentialStateDegraded, "degraded"}
	}
	if in.Status == "active" || in.LifecycleStatus == "active" || in.Availability == "ready" || in.HealthStatus == "healthy" {
		return credentialDisplayState{CredentialStateActive, "active"}
	}
	return credentialDisplayState{CredentialStateUnknown, "unknown"}
}
