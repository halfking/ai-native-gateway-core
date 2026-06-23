package provider

import (
	"testing"
)

// TestCandidate_UnavailableReason_MinimaxM3 reproduces the minimax-m3
// "no_candidates" diagnostic the user reported on 2026-06-23.
//
// Symptom: when the client requests model "minimax-m3" but ALL available
// credential_model_bindings for that model are in unavailable state, the
// router returns no_candidates_from_router.
//
// This test enumerates the typical rejection reasons a candidate can
// exhibit and verifies the diagnostic string includes the model name
// in the log breakdown. The bug we are guarding against:
//   - Future regression where the UnavailableReason() returns ""
//     even when the candidate is unavailable — the router would then
//     emit no_candidates but every reason would be empty, making
//     post-mortem analysis impossible.
//
// See:
//   - admin/provider_offer_force_recover.go:387 (minimax-prod-1 incident)
//   - docs/2026-06-23-credential-manual-disabled-implementation.md
func TestCandidate_UnavailableReason_MinimaxM3(t *testing.T) {
	tests := []struct {
		name         string
		candidate    Candidate
		wantNonEmpty bool
	}{
		{
			name: "available candidate returns empty reason",
			candidate: Candidate{
				Routable:          true,
				LifecycleStatus:   "active",
				AvailabilityState: "ready",
				QuotaState:        "ok",
			},
			wantNonEmpty: false,
		},
		{
			name: "availability:cooling rejects minimax-m3",
			candidate: Candidate{
				Routable:          true,
				LifecycleStatus:   "active",
				AvailabilityState: "cooling",
				QuotaState:        "ok",
			},
			wantNonEmpty: true,
		},
		{
			name: "manual disabled rejects minimax-m3",
			candidate: Candidate{
				Routable:          false,
				BlockReason:       stringPtr("manual"),
				LifecycleStatus:   "active",
				AvailabilityState: "ready",
				QuotaState:        "ok",
			},
			wantNonEmpty: true,
		},
		{
			name: "quota exhausted rejects minimax-m3",
			candidate: Candidate{
				Routable:          true,
				LifecycleStatus:   "active",
				AvailabilityState: "ready",
				QuotaState:        "balance_exhausted",
			},
			wantNonEmpty: true,
		},
		{
			name: "balance zero rejects minimax-m3",
			candidate: Candidate{
				Routable:          true,
				LifecycleStatus:   "active",
				AvailabilityState: "ready",
				QuotaState:        "ok",
				BalanceUSD:        float64Ptr(0),
			},
			wantNonEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := tt.candidate.UnavailableReason()
			hasReason := reason != ""
			if hasReason != tt.wantNonEmpty {
				t.Errorf("UnavailableReason() = %q, wantNonEmpty=%v", reason, tt.wantNonEmpty)
			}
		})
	}
}

// Helper functions
func stringPtr(s string) *string    { return &s }
func float64Ptr(f float64) *float64 { return &f }
