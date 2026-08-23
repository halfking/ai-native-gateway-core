package admin

import "testing"

func TestQuotaAllowsPriorityRouting(t *testing.T) {
	tests := []struct {
		quota string
		want  bool
	}{
		{quota: "", want: true},
		{quota: "ok", want: true},
		{quota: "OK", want: true},
		{quota: "unknown", want: false},
		{quota: "balance_exhausted", want: false},
	}
	for _, tc := range tests {
		if got := quotaAllowsPriorityRouting(tc.quota); got != tc.want {
			t.Fatalf("quotaAllowsPriorityRouting(%q) = %v, want %v", tc.quota, got, tc.want)
		}
	}
}

func TestResolveCandidateSortRespectsPriorityFlag(t *testing.T) {
	candidates := []resolveCandidate{
		{CredentialID: 1, Priority: false, ManualPriority: 5, QuotaState: "ok"},
		{CredentialID: 2, Priority: true, ManualPriority: 99, QuotaState: "ok"},
		{CredentialID: 3, Priority: true, ManualPriority: 10, QuotaState: "balance_exhausted"},
	}
	sortResolveCandidatesStable(candidates)
	if candidates[0].CredentialID != 2 {
		t.Fatalf("priority+ok first, got credential %d", candidates[0].CredentialID)
	}
	if candidates[1].CredentialID != 1 {
		t.Fatalf("manual_priority tie after priority tier, got credential %d", candidates[1].CredentialID)
	}
	if candidates[2].CredentialID != 3 {
		t.Fatalf("exhausted priority last, got credential %d", candidates[2].CredentialID)
	}
}
