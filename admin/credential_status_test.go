package admin

import "testing"

func TestDeriveCredentialDisplayState(t *testing.T) {
	cases := []struct {
		name string
		in   credentialStateInput
		want CredentialDisplayState
	}{
		{"deleted wins", credentialStateInput{Status: "deleted", ManualDisabled: true, Availability: "auth_failed"}, CredentialStateDeleted},
		{"retired is terminal", credentialStateInput{LifecycleStatus: "retired"}, CredentialStateDeleted},
		{"manual disabled wins", credentialStateInput{Status: "active", ManualDisabled: true, Availability: "auth_failed"}, CredentialStateDisabled},
		{"auth failed", credentialStateInput{Availability: "auth_failed"}, CredentialStateAuthFailed},
		{"quota wins transient", credentialStateInput{QuotaState: "balance_exhausted", Availability: "cooling"}, CredentialStateQuotaExhausted},
		{"suspended", credentialStateInput{Availability: "suspended"}, CredentialStateSuspended},
		{"rate limited", credentialStateInput{Availability: "rate_limited"}, CredentialStateRateLimited},
		{"unreachable", credentialStateInput{HealthStatus: "unreachable"}, CredentialStateUnreachable},
		{"cooling", credentialStateInput{Status: "cooling"}, CredentialStateCooling},
		{"degraded", credentialStateInput{HealthStatus: "warning"}, CredentialStateDegraded},
		{"active", credentialStateInput{Status: "active", LifecycleStatus: "active", Availability: "ready", HealthStatus: "healthy"}, CredentialStateActive},
		{"unknown", credentialStateInput{}, CredentialStateUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveCredentialDisplayState(tc.in).State; got != tc.want {
				t.Fatalf("state = %q, want %q", got, tc.want)
			}
		})
	}
}
