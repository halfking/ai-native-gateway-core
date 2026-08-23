package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestIsPriorityBucketEligible(t *testing.T) {
	cases := []struct {
		name string
		c    provider.Candidate
		want bool
	}{
		{"priority true, quota ok", provider.Candidate{Priority: true, QuotaState: "ok"}, true},
		{"priority true, quota empty", provider.Candidate{Priority: true, QuotaState: ""}, true},
		{"priority true, quota exhausted", provider.Candidate{Priority: true, QuotaState: "permanently_exhausted"}, false},
		{"priority true, balance exhausted", provider.Candidate{Priority: true, QuotaState: "balance_exhausted"}, false},
		{"priority false, quota ok", provider.Candidate{Priority: false, QuotaState: "ok"}, false},
		{"priority false, quota empty", provider.Candidate{Priority: false, QuotaState: ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPriorityBucketEligible(tc.c); got != tc.want {
				t.Fatalf("isPriorityBucketEligible = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStablePartitionPriority(t *testing.T) {
	cands := []provider.Candidate{
		{CredentialID: 1, Priority: false, QuotaState: "ok"},
		{CredentialID: 2, Priority: true, QuotaState: "ok"},
		{CredentialID: 3, Priority: false, QuotaState: "ok"},
		{CredentialID: 4, Priority: true, QuotaState: "permanently_exhausted"},
		{CredentialID: 5, Priority: true, QuotaState: "ok"},
	}
	got := stablePartitionPriority(cands)
	// Priority-eligible candidates (2, 5) must come before non-eligible (1, 3, 4).
	// Relative order within each group is preserved.
	wantIDs := []int{2, 5, 1, 3, 4}
	for i, w := range wantIDs {
		if got[i].CredentialID != w {
			t.Fatalf("position %d: got credID=%d, want %d (full order: %v)", i, got[i].CredentialID, w, credIDs(got))
		}
	}
}

func TestStablePartitionPriorityAllStandard(t *testing.T) {
	cands := []provider.Candidate{
		{CredentialID: 1, Priority: false, QuotaState: "ok"},
		{CredentialID: 2, Priority: false, QuotaState: "ok"},
	}
	got := stablePartitionPriority(cands)
	if len(got) != 2 || got[0].CredentialID != 1 || got[1].CredentialID != 2 {
		t.Fatalf("order should be unchanged: %v", credIDs(got))
	}
}

func TestStablePartitionPriorityAllPriority(t *testing.T) {
	cands := []provider.Candidate{
		{CredentialID: 1, Priority: true, QuotaState: "ok"},
		{CredentialID: 2, Priority: true, QuotaState: "ok"},
	}
	got := stablePartitionPriority(cands)
	if len(got) != 2 || got[0].CredentialID != 1 || got[1].CredentialID != 2 {
		t.Fatalf("order should be unchanged: %v", credIDs(got))
	}
}

func TestStablePartitionPriorityEmptyAndSingle(t *testing.T) {
	if got := stablePartitionPriority(nil); len(got) != 0 {
		t.Fatalf("nil input should return empty, got %v", got)
	}
	single := []provider.Candidate{{CredentialID: 1, Priority: true, QuotaState: "ok"}}
	if got := stablePartitionPriority(single); len(got) != 1 || got[0].CredentialID != 1 {
		t.Fatalf("single input should be unchanged: %v", got)
	}
}

func TestCompareCandidatePriorityPriorityBucketFirst(t *testing.T) {
	prio := provider.Candidate{CredentialID: 2, Priority: true, QuotaState: "ok", BillingMode: "per_token"}
	standard := provider.Candidate{CredentialID: 1, Priority: false, QuotaState: "ok", BillingMode: "free"}
	// Even though standard has a better billing round (free=1 vs per_token=2),
	// the priority-eligible candidate must sort first.
	if !CompareCandidatePriority(prio, standard) {
		t.Fatal("priority-eligible candidate must sort before standard regardless of billing round")
	}
	if CompareCandidatePriority(standard, prio) {
		t.Fatal("standard candidate must not sort before priority-eligible")
	}
}

func credIDs(cs []provider.Candidate) []int {
	ids := make([]int, len(cs))
	for i, c := range cs {
		ids[i] = c.CredentialID
	}
	return ids
}
