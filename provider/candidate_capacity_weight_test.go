package provider

import (
	"testing"
)

// TestApplyCapacityWeightedLB pins the 2026-08-11 capacity-weighted
// load-balancing contract: Candidate.Weight is derived from the credential's
// effective concurrency capacity so promoteWeightedCandidate distributes
// session-first / error-triggered re-selection across healthy same-level
// nodes in proportion to their capacity.
//
// Effective-limit formula mirrors domains/providerprofile/adapters.go
// GetModelScale: auto-tuned preferred, capped by manual hard cap; manual
// used when auto unset; default weight kept when both unknown.
func TestApplyCapacityWeightedLB(t *testing.T) {
	intp := func(v int) *int { return &v }

	cases := []struct {
		name    string
		weight  int // manual weight from mo.weight (SQL COALESCE default 100)
		manual  *int
		auto    *int
		wantMin int // inclusive lower bound on the derived weight
		wantMax int // inclusive upper bound on the derived weight
		wantEq  int // when >0, the derived weight must equal exactly this
		note    string
	}{
		{
			name:   "auto_only_becomes_weight",
			weight: 100, auto: intp(40),
			wantEq: 40,
			note:   "auto-tuned limit preferred when manual unset",
		},
		{
			name:   "manual_caps_auto",
			weight: 100, manual: intp(10), auto: intp(40),
			wantEq: 10,
			note:   "manual hard cap clamps the auto limit down",
		},
		{
			name:   "manual_only_when_auto_unset",
			weight: 100, manual: intp(25), auto: nil,
			wantEq: 25,
			note:   "falls back to manual when auto is nil/0",
		},
		{
			name:   "unknown_capacity_keeps_default",
			weight: 100, manual: nil, auto: nil,
			wantEq: 100,
			note:   "both unknown -> default weight, never excluded",
		},
		{
			name:   "zero_capacity_floored",
			weight: 100, manual: intp(0), auto: intp(0),
			wantEq: 100,
			note:   "explicit zero capacity -> default, not 0 (promoteWeightedCandidate skips 0)",
		},
		{
			name:   "explicit_manual_weight_respected",
			weight: 250, manual: intp(40), auto: intp(8),
			wantEq: 250,
			note:   "operator-set non-default weight overrides capacity derivation",
		},
		{
			name:   "huge_capacity_clamped_to_max",
			weight: 100, auto: intp(5000),
			wantEq: capacityWeightMax,
			note:   "clamp prevents one giant credential monopolising rotation",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cand := &Candidate{Weight: tc.weight, ConcurrencyLimit: tc.manual}
			applyCapacityWeightedLB(cand, tc.auto)
			got := cand.Weight
			if got <= 0 {
				t.Fatalf("%s: derived weight must be > 0 (promoteWeightedCandidate skips <=0), got %d", tc.name, got)
			}
			if tc.wantEq > 0 && got != tc.wantEq {
				t.Fatalf("%s: weight = %d, want exactly %d (%s)", tc.name, got, tc.wantEq, tc.note)
			}
			if tc.wantMin > 0 && got < tc.wantMin {
				t.Fatalf("%s: weight = %d, want >= %d", tc.name, got, tc.wantMin)
			}
			if tc.wantMax > 0 && got > tc.wantMax {
				t.Fatalf("%s: weight = %d, want <= %d", tc.name, got, tc.wantMax)
			}
		})
	}
}

// TestApplyCapacityWeightedLB_NilSafe guards against the nil-pointer panic
// the helper could introduce if a future caller passes a nil candidate.
func TestApplyCapacityWeightedLB_NilSafe(t *testing.T) {
	applyCapacityWeightedLB(nil, nil) // must not panic
}
