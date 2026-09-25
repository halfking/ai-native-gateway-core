package autoroute

import (
	"context"
	"sync"
	"testing"
)

// TestDecider_ConcurrentSharedStubIndex_TierStore pins the stub fixture
// ownership contract: stubIndex.Recommend must hand each caller its own copy
// of the fixture slice. WorkTypeRouteStore.applyTierPolicyWithRoutes writes
// scored[i].Breakdown.RouteTier in place on the slice it receives (the only
// live in-place write on the Decide candidate path), so a stub returning the
// shared backing array turns concurrent Decide calls sharing one stubIndex
// into a data race — the same hazard class stubClassifier.Classify was
// already fixed for via a shallow copy (see decision_test.go).
//
// The long-standing TestDecider_ConcurrentDecide_ThreadSafeRanking shares a
// stubIndex across goroutines too, but wires no WorkTypeRouteStore, so the
// tier write never fires and `go test -race` stays green there — a false
// negative for exactly this hazard.
func TestDecider_ConcurrentSharedStubIndex_TierStore(t *testing.T) {
	cls := &stubClassifier{name: "heuristic", out: &Classification{
		Primary: TaskCode, Confidence: 0.9, Classifier: "heuristic",
	}}
	// One stubIndex shared by every goroutine below.
	idx := &stubIndex{cands: []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "model-a", CredentialID: 1, RawModel: "model-a"}},
		{Candidate: Candidate{CanonicalName: "model-b", CredentialID: 2, RawModel: "model-b"}},
	}}
	routes := NewWorkTypeRouteStore(nil)
	routes.snapshot.Store(&wtRouteSnapshot{byTaskType: map[string][]WorkTypeRoute{
		"code": {
			{CanonicalName: "model-a", Tier: "primary"},
			{CanonicalName: "model-b", Tier: "secondary"},
		},
	}})

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.SetWorkTypeRouteStore(routes)

	const goroutines = 20
	const callsPerGoroutine = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < callsPerGoroutine; i++ {
				dec, err := d.Decide(context.Background(), ClassificationSignals{}, gid, "", "", "")
				if err != nil {
					t.Errorf("goroutine %d iter %d: Decide failed: %v", gid, i, err)
					return
				}
				if dec.ChosenModel != "model-a" {
					t.Errorf("goroutine %d iter %d: ChosenModel=%q want model-a (tier primary)", gid, i, dec.ChosenModel)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}
