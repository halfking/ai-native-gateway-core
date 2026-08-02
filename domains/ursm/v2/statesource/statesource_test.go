// Package statesource_test covers the routing_state_source (S-3) field
// added in Step 5 round 1 of the request-flow-audit design
// (docs/superpowers/specs/2026-07-27-request-flow-audit-design.md).
//
// These tests pin:
//   - RecordRoutingStateSource increments the per-source counter and is
//     safe to call from many goroutines concurrently (the router path is
//     one PlanCandidates call per request, many in parallel).
//   - Snapshot returns the cumulative counts observed since the package
//     was loaded. A Reset hook is provided so test setup is deterministic
//     even when package init is involved.
//   - The enum constants declared by the package are exactly the source
//     labels router.go must record — guards against typo drift.
package statesource_test

import (
	"sort"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/statesource"
)

func TestRecordRoutingStateSource_Increments(t *testing.T) {
	statesource.ResetForTest()
	statesource.RecordRoutingStateSource(statesource.StateSourceNodeMirrorHit)
	statesource.RecordRoutingStateSource(statesource.StateSourceNodeMirrorHit)
	statesource.RecordRoutingStateSource(statesource.StateSourceNodeMirrorMiss)
	statesource.RecordRoutingStateSource(statesource.StateSourceFallback)

	snap := statesource.Snapshot()
	if got := snap[statesource.StateSourceNodeMirrorHit]; got != 2 {
		t.Fatalf("hit count = %d, want 2", got)
	}
	if got := snap[statesource.StateSourceNodeMirrorMiss]; got != 1 {
		t.Fatalf("miss count = %d, want 1", got)
	}
	if got := snap[statesource.StateSourceFallback]; got != 1 {
		t.Fatalf("fallback count = %d, want 1", got)
	}
	if got := snap[statesource.StateSourceAuthoritative]; got != 0 {
		t.Fatalf("authoritative should be untouched, got %d", got)
	}
}

func TestRecordRoutingStateSource_Concurrent(t *testing.T) {
	statesource.ResetForTest()
	const goroutines = 32
	const each = 200

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				statesource.RecordRoutingStateSource(statesource.StateSourceNodeMirrorStale)
			}
		}()
	}
	wg.Wait()
	snap := statesource.Snapshot()
	if got := snap[statesource.StateSourceNodeMirrorStale]; got != int64(goroutines*each) {
		t.Fatalf("stale count = %d, want %d", got, goroutines*each)
	}
}

func TestRecordRoutingStateSource_AllEnumConstants(t *testing.T) {
	// Every constant declared by the package must be acceptable as the
	// argument to RecordRoutingStateSource without panic, and the
	// snapshot must reflect the increment. This guards against typo
	// drift between the router call sites and the metric label set.
	statesource.ResetForTest()
	all := []statesource.RoutingStateSource{
		statesource.StateSourceNodeMirrorHit,
		statesource.StateSourceNodeMirrorMiss,
		statesource.StateSourceNodeMirrorStale,
		statesource.StateSourceFallback,
		statesource.StateSourceOff,
		statesource.StateSourceCanary,
		statesource.StateSourceAuthoritative,
		statesource.StateSourceSkipped,
	}
	for _, s := range all {
		statesource.RecordRoutingStateSource(s)
	}
	snap := statesource.Snapshot()
	if len(snap) != len(all) {
		keys := make([]string, 0, len(snap))
		for k := range snap {
			keys = append(keys, string(k))
		}
		sort.Strings(keys)
		t.Fatalf("snapshot keys=%v want all %d sources", keys, len(all))
	}
	for _, s := range all {
		if got := snap[s]; got != 1 {
			t.Fatalf("%s count = %d, want 1", s, got)
		}
	}
}

func TestSnapshot_KeysAreStable(t *testing.T) {
	// The map key is the typed RoutingStateSource; ensure the type
	// serializes as a string with the exact value the metric label
	// depends on. We don't compare against a literal because the metric
	// label is owned by the production code, but we DO assert that the
	// recorded source is observable through Snapshot unchanged.
	statesource.ResetForTest()
	statesource.RecordRoutingStateSource(statesource.StateSourceAuthoritative)
	snap := statesource.Snapshot()
	if _, ok := snap[statesource.StateSourceAuthoritative]; !ok {
		t.Fatalf("authoritative source missing from snapshot keys")
	}
}
