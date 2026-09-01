package admin

import "testing"

// TestClassifyDiagnoseErrorKind (2026-09-01 P0-3 24h-audit round2): the two
// diagnose aggregation loops (diagnoseProvider + doDiagnose) previously used
// diverging substring switches (Contains(kind, "401") was dead code —
// error_kind never stores raw HTTP literals — and Contains(kind, "model")
// over-captured model_deprecated). The shared exact-match classifier must
// place every errorsx ErrorKind value in the intended bucket and never
// match a raw HTTP status literal.
func TestClassifyDiagnoseErrorKind(t *testing.T) {
	cases := []struct {
		kind          string
		auth          bool
		rateLimit     bool
		timeout       bool
		modelNotFound bool
	}{
		// auth family
		{"auth", true, false, false, false},
		{"auth_revoked", true, false, false, false},
		// rate-limit / admission family
		{"rate_limit", false, true, false, false},
		{"concurrent", false, true, false, false},
		{"quota", false, true, false, false},
		{"quota_periodic", false, true, false, false},
		{"quota_balance", false, true, false, false},
		{"quota_permanent", false, true, false, false},
		{"fp_slot_saturated", false, true, false, false},
		{"circuit_open", false, true, false, false},
		// timeout family
		{"timeout", false, false, true, false},
		{"stream_timeout", false, false, true, false},
		// model family
		{"model_not_found", false, false, false, true},
		{"model_deprecated", false, false, false, true},
		{"no_available_channel", false, false, false, true},
		// everything else → OtherErrors (all four false)
		{"transient", false, false, false, false},
		{"network", false, false, false, false},
		{"upstream_down", false, false, false, false},
		{"empty_response", false, false, false, false},
		{"other", false, false, false, false},
		{"", false, false, false, false},
	}
	for _, tc := range cases {
		auth, rateLimit, timeout, modelNotFound := classifyDiagnoseErrorKind(tc.kind)
		if auth != tc.auth || rateLimit != tc.rateLimit ||
			timeout != tc.timeout || modelNotFound != tc.modelNotFound {
			t.Errorf("classifyDiagnoseErrorKind(%q) = (%v,%v,%v,%v), want (%v,%v,%v,%v)",
				tc.kind, auth, rateLimit, timeout, modelNotFound,
				tc.auth, tc.rateLimit, tc.timeout, tc.modelNotFound)
		}
	}

	// Regression guard for the removed dead code: raw HTTP status literals
	// must NEVER match any bucket (error_kind stores symbolic kinds only).
	for _, literal := range []string{"401", "403", "404", "429"} {
		auth, rateLimit, timeout, modelNotFound := classifyDiagnoseErrorKind(literal)
		if auth || rateLimit || timeout || modelNotFound {
			t.Errorf("classifyDiagnoseErrorKind(%q) matched a bucket; raw HTTP literals must classify as OtherErrors", literal)
		}
	}
}
