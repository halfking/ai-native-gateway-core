package dispatch

import (
	"testing"
	"time"
)

func TestGovernorSpecPopulatesAllFields(t *testing.T) {
	spec := GovernorSpec{
		CredentialID: 42,
		ProviderID:   7,
		Mode:         ModeConcurrency,
		Limit:        16,
		RPMLimit:     60,
		TPMLimit:     0,
		LeaseTTL:     30 * time.Second,
		Backend:      BackendLocal,
		Revision:     1,
	}
	if spec.CredentialID != 42 || spec.ProviderID != 7 || spec.Mode != ModeConcurrency ||
		spec.Limit != 16 || spec.RPMLimit != 60 || spec.TPMLimit != 0 ||
		spec.LeaseTTL != 30*time.Second || spec.Backend != BackendLocal || spec.Revision != 1 {
		t.Fatalf("field round-trip drift: %+v", spec)
	}
}

func TestGovernorSpecModeConstantsRoundTrip(t *testing.T) {
	modes := []string{ModeConcurrency, ModeRPM, ModeTPM, ModeDisabled}
	for _, m := range modes {
		spec := GovernorSpec{Mode: m}
		if spec.Mode != m {
			t.Fatalf("mode drift: got %q want %q", spec.Mode, m)
		}
	}
}

func TestGovernorSpecBackendFieldAcceptsKnownKinds(t *testing.T) {
	cases := []GovernorBackendKind{
		BackendLocal,
		BackendRedisEnforce,
		BackendRedisShadow,
	}
	for _, b := range cases {
		spec := GovernorSpec{Backend: b}
		if spec.Backend != b {
			t.Fatalf("backend drift: got %q want %q", spec.Backend, b)
		}
	}
}

// Stage A contract: we DO NOT validate at construction (the backend
// decides its own semantics). This test pins the "garbage in, garbage out"
// behavior so reviewers understand why validation lives downstream.
func TestGovernorSpecLeavesValidationToBackends(t *testing.T) {
	// Empty mode and unknown backend are accepted; backend is responsible.
	spec := GovernorSpec{Mode: "", Backend: ""}
	if spec.Mode != "" || spec.Backend != "" {
		t.Fatalf("expected zero values to pass through; got %+v", spec)
	}
}