package executors

import (
	"testing"

	met "github.com/kaixuan/llm-gateway-go/metrics" //nolint:depguard // routing credential observability counters
	"github.com/kaixuan/llm-gateway-go/provider"
	dto "github.com/prometheus/client_model/go"
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

// TestPlanCandidatesWeightedPromotionStaysInPriorityBucket locks the
// interaction between stablePartitionPriority and the weighted
// first-attempt lottery: when the tier bucket mixes a priority-eligible
// candidate with a much heavier standard one, the lottery must run
// inside the priority prefix only. Without the gate, position values
// landing in the standard candidate's weight range would promote it to
// index 0 and it would receive the first attempt over the priority one.
func TestPlanCandidatesWeightedPromotionStaysInPriorityBucket(t *testing.T) {
	router := NewRouter(nil, nil)
	policy := provider.DefaultPolicy()
	pool := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "model-x", Tier: 1, Routable: true, Priority: true, QuotaState: "ok", Weight: 1},
		{CredentialID: 2, ProviderID: 2, RawModel: "model-x", Tier: 1, Routable: true, Weight: 999},
	}
	// Each PlanCandidates call advances the per-identity rotation counter,
	// sweeping the weighted position across the full range.
	for i := 0; i < 64; i++ {
		planned := router.PlanCandidates(pool, nil, policy, nil)
		if len(planned) == 0 || planned[0].CredentialID != 1 {
			t.Fatalf("iteration %d: first choice = %v, want priority credential 1", i, credIDs(planned))
		}
	}
}

// TestPlanCandidatesWeightedPromotionUnchangedWithoutPriority verifies the
// gate is inert for an all-standard bucket: the classic weight-driven
// rotation still promotes the heavy candidate most of the time.
func TestPlanCandidatesWeightedPromotionUnchangedWithoutPriority(t *testing.T) {
	router := NewRouter(nil, nil)
	policy := provider.DefaultPolicy()
	pool := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "model-y", Tier: 1, Routable: true, Weight: 1},
		{CredentialID: 2, ProviderID: 2, RawModel: "model-y", Tier: 1, Routable: true, Weight: 999},
	}
	heavyFirst := 0
	for i := 0; i < 64; i++ {
		planned := router.PlanCandidates(pool, nil, policy, nil)
		if len(planned) > 0 && planned[0].CredentialID == 2 {
			heavyFirst++
		}
	}
	if heavyFirst == 0 {
		t.Fatal("all-standard bucket: weighted rotation never promoted the heavy candidate — the lottery is broken")
	}
}

// TestApplyProtocolAffinityPreservesOrderWithinSameProtocol locks the
// affinity contract: within one protocol rank the incoming order (priority
// bucket / sticky pin / weighted first attempt / tier rounds) must survive.
// The old SuccessRate fallback re-ranked same-protocol candidates and
// erased those orderings in the default single-protocol case.
func TestApplyProtocolAffinityPreservesOrderWithinSameProtocol(t *testing.T) {
	ordered := []provider.Candidate{
		{CredentialID: 1, Protocol: "openai-completions", Priority: true, QuotaState: "ok", SuccessRate: 0.5},
		{CredentialID: 2, Protocol: "openai-completions", SuccessRate: 0.99},
	}
	got := applyProtocolAffinity(ordered, []string{"openai-completions"})
	if got[0].CredentialID != 1 {
		t.Fatalf("incoming first candidate must stay first within same protocol; got %v", credIDs(got))
	}
}

func TestApplyProtocolAffinityStillGroupsPreferredProtocolFirst(t *testing.T) {
	ordered := []provider.Candidate{
		{CredentialID: 1, Protocol: "anthropic-messages"},
		{CredentialID: 2, Protocol: "openai-completions"},
		{CredentialID: 3, Protocol: "anthropic-messages"},
	}
	got := applyProtocolAffinity(ordered, []string{"openai-completions"})
	wantProto := []string{"openai-completions", "anthropic-messages", "anthropic-messages"}
	for i, w := range wantProto {
		if got[i].Protocol != w {
			t.Fatalf("position %d: protocol = %q, want %q", i, got[i].Protocol, w)
		}
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

func TestClassifyPrioritySelection(t *testing.T) {
	prio := func(id int) provider.Candidate {
		return provider.Candidate{CredentialID: id, Priority: true, QuotaState: "ok"}
	}
	std := func(id int) provider.Candidate {
		return provider.Candidate{CredentialID: id, Priority: false, QuotaState: "ok"}
	}
	cases := []struct {
		name string
		in   []provider.Candidate
		want string
	}{
		{"empty list", nil, "no_priority_candidates"},
		{"first is priority", []provider.Candidate{prio(1), std(2)}, "priority_only"},
		{"priority later in list", []provider.Candidate{std(1), prio(2)}, "spillover_to_non_priority"},
		{"all standard", []provider.Candidate{std(1), std(2)}, "no_priority_candidates"},
		{"priority flag but quota exhausted", []provider.Candidate{
			{CredentialID: 1, Priority: true, QuotaState: "permanently_exhausted"},
			std(2),
		}, "no_priority_candidates"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPrioritySelection(tc.in); got != tc.want {
				t.Fatalf("classifyPrioritySelection = %q, want %q", got, tc.want)
			}
		})
	}
}

// readPrioritySelectionCounter reads the global counter via the
// dto.Metric Write hook; prometheus/testutil is not vendored in this repo.
func readPrioritySelectionCounter(t *testing.T, outcome string) float64 {
	t.Helper()
	var m dto.Metric
	if err := met.RoutingPriorityCandidatesSelectedTotal.WithLabelValues(outcome).Write(&m); err != nil {
		t.Fatalf("read llmgw_routing_priority_candidates_selected_total{outcome=%q}: %v", outcome, err)
	}
	return m.GetCounter().GetValue()
}

func TestPlanCandidatesRecordsPrioritySelectionMetric(t *testing.T) {
	router := NewRouter(nil, nil)
	policy := provider.DefaultPolicy()

	prioBefore := readPrioritySelectionCounter(t, "priority_only")
	spillBefore := readPrioritySelectionCounter(t, "spillover_to_non_priority")
	noneBefore := readPrioritySelectionCounter(t, "no_priority_candidates")

	// Zero weights keep the weighted promotion inert, so the stable
	// priority partition alone decides the first attempt.
	priorityPool := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "model-x", Tier: 1, Routable: true, Priority: true, QuotaState: "ok"},
		{CredentialID: 2, ProviderID: 2, RawModel: "model-x", Tier: 1, Routable: true},
	}
	planned := router.PlanCandidates(priorityPool, nil, policy, nil)
	if len(planned) == 0 || planned[0].CredentialID != 1 {
		t.Fatalf("first choice = %v, want priority credential 1", credIDs(planned))
	}
	if got := readPrioritySelectionCounter(t, "priority_only"); got != prioBefore+1 {
		t.Fatalf("priority_only counter = %v, want %v", got, prioBefore+1)
	}

	// Pinning the standard candidate via sticky overrides the partition
	// while the priority candidate remains in the pool → spillover.
	sticky := 2
	planned = router.PlanCandidates(priorityPool, &sticky, policy, nil)
	if len(planned) == 0 || planned[0].CredentialID != sticky {
		t.Fatalf("sticky first choice = %v, want credential %d", credIDs(planned), sticky)
	}
	if got := readPrioritySelectionCounter(t, "spillover_to_non_priority"); got != spillBefore+1 {
		t.Fatalf("spillover_to_non_priority counter = %v, want %v", got, spillBefore+1)
	}

	// A pool without priority-eligible candidates records the baseline.
	standardPool := []provider.Candidate{
		{CredentialID: 3, ProviderID: 3, RawModel: "model-y", Tier: 1, Routable: true},
		{CredentialID: 4, ProviderID: 4, RawModel: "model-y", Tier: 1, Routable: true},
	}
	planned = router.PlanCandidates(standardPool, nil, policy, nil)
	if len(planned) == 0 {
		t.Fatal("standard pool planned to empty")
	}
	if got := readPrioritySelectionCounter(t, "no_priority_candidates"); got != noneBefore+1 {
		t.Fatalf("no_priority_candidates counter = %v, want %v", got, noneBefore+1)
	}

	// The two later plans must not have moved the priority_only counter.
	if got := readPrioritySelectionCounter(t, "priority_only"); got != prioBefore+1 {
		t.Fatalf("priority_only counter drifted to %v, want %v", got, prioBefore+1)
	}
}

func TestPlanCandidatesSkipsPrioritySelectionMetricWhenDisabled(t *testing.T) {
	router := NewRouter(nil, nil)
	router.PriorityRoutingEnabled = false
	policy := provider.DefaultPolicy()

	prioBefore := readPrioritySelectionCounter(t, "priority_only")
	spillBefore := readPrioritySelectionCounter(t, "spillover_to_non_priority")
	noneBefore := readPrioritySelectionCounter(t, "no_priority_candidates")

	priorityPool := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "model-x", Tier: 1, Routable: true, Priority: true, QuotaState: "ok"},
		{CredentialID: 2, ProviderID: 2, RawModel: "model-x", Tier: 1, Routable: true},
	}
	if planned := router.PlanCandidates(priorityPool, nil, policy, nil); len(planned) == 0 {
		t.Fatal("planned to empty with priority routing disabled")
	}

	for _, outcome := range []string{"priority_only", "spillover_to_non_priority", "no_priority_candidates"} {
		before := prioBefore
		switch outcome {
		case "spillover_to_non_priority":
			before = spillBefore
		case "no_priority_candidates":
			before = noneBefore
		}
		if got := readPrioritySelectionCounter(t, outcome); got != before {
			t.Fatalf("counter %q = %v after disabled-flag plan, want unchanged %v", outcome, got, before)
		}
	}
}

func credIDs(cs []provider.Candidate) []int {
	ids := make([]int, len(cs))
	for i, c := range cs {
		ids[i] = c.CredentialID
	}
	return ids
}
