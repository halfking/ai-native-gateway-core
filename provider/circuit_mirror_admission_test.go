package provider

import "testing"

// DB circuit state is an asynchronous, lossy, cross-instance observation. It
// cannot remove the candidate before the local breaker can claim a recovery
// probe (or after a process restart has reset the local breaker).
func TestCandidateCircuitMirrorDoesNotControlAdmission(t *testing.T) {
	for _, state := range []string{"", "closed", "open", "half_open"} {
		t.Run(state, func(t *testing.T) {
			c := Candidate{ProviderID: 36, CredentialID: 63, RawModel: "grok-4.6",
				Routable: true, LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: state}
			if !c.IsAvailable() || c.UnavailableReason() != "" {
				t.Fatalf("observational circuit mirror %q removed a healthy candidate: %s", state, c.UnavailableReason())
			}
			if c.CircuitState != state {
				t.Fatal("admission must preserve the diagnostic mirror")
			}
		})
	}
}

func TestCandidateCircuitMirrorDoesNotBypassAuthoritativeBlocks(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Candidate)
		want   string
	}{
		{"routing", func(c *Candidate) { c.Routable = false }, "routing_blocked"},
		{"manual", func(c *Candidate) { c.Routable = false; c.BlockReason = stringPtr("manual") }, "routing_blocked:manual"},
		{"lifecycle", func(c *Candidate) { c.LifecycleStatus = "disabled" }, "lifecycle:disabled"},
		{"auth", func(c *Candidate) { c.AvailabilityState = "auth_failed" }, "availability:auth_failed"},
		{"cooling", func(c *Candidate) { c.AvailabilityState = "cooling" }, "availability:cooling"},
		{"rate_limited", func(c *Candidate) { c.AvailabilityState = "rate_limited" }, "availability:rate_limited"},
		{"unreachable", func(c *Candidate) { c.AvailabilityState = "unreachable" }, "availability:unreachable"},
		{"suspended", func(c *Candidate) { c.AvailabilityState = "suspended" }, "availability:suspended"},
		{"balance_quota", func(c *Candidate) { c.QuotaState = "balance_exhausted" }, "quota:balance_exhausted"},
		{"permanent_quota", func(c *Candidate) { c.QuotaState = "permanently_exhausted" }, "quota:permanently_exhausted"},
		{"periodic_quota", func(c *Candidate) { c.QuotaState = "periodic_exhausted" }, "quota:periodic_exhausted"},
		{"zero_balance", func(c *Candidate) { c.BalanceUSD = float64Ptr(0) }, "balance:zero"},
	}
	for _, tc := range cases {
		for _, mirror := range []string{"closed", "open"} {
			t.Run(tc.name+"/"+mirror, func(t *testing.T) {
				c := Candidate{Routable: true, LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: mirror}
				tc.mutate(&c)
				if c.IsAvailable() || c.UnavailableReason() != tc.want {
					t.Fatalf("authoritative block changed by mirror: got %q, want %q", c.UnavailableReason(), tc.want)
				}
			})
		}
	}
}
