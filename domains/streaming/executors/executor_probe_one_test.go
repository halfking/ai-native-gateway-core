package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestSelectProbeOneKeepsEarliestCredentialID(t *testing.T) {
	in := []credentialstate.NoCandidatesCandidate{
		{CredentialID: 30, RawModel: "m"},
		{CredentialID: 10, RawModel: "m"},
		{CredentialID: 20, RawModel: "m"},
	}
	got := selectProbeOne(in, true)
	if len(got) != 1 || got[0].CredentialID != 10 {
		t.Fatalf("selectProbeOne = %+v, want single cred 10", got)
	}
	// 前提在场：关闭时原样保留。
	if got := selectProbeOne(in, false); len(got) != 3 {
		t.Fatalf("disabled selectProbeOne len = %d, want 3", len(got))
	}
}

func TestQuotaHeldAll(t *testing.T) {
	qs := "balance_exhausted"
	cands := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, QuotaState: qs, AvailabilityState: "ready", LifecycleStatus: "active", Routable: true},
		{CredentialID: 2, ProviderID: 1, QuotaState: "permanently_exhausted", AvailabilityState: "ready", LifecycleStatus: "active", Routable: true},
	}
	if !quotaHeldAll(cands) {
		t.Fatal("quotaHeldAll want true for all-quota candidates")
	}
	// 同候选叠加 balance:zero（UnavailableReason 用 "; " 拼接）仍算纯额度。
	zero := 0.0
	cands[0].BalanceUSD = &zero
	if !quotaHeldAll(cands) {
		t.Fatal("quotaHeldAll want true for quota+balance:zero (semicolon-joined)")
	}
	// 混入 circuit:open → 不是纯额度耗尽。
	cands[0].CircuitState = "open"
	if quotaHeldAll(cands) {
		t.Fatal("quotaHeldAll want false when circuit:open present")
	}
}
