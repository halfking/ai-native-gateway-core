// Probe-pin routing tests (2026-08-18 glm-5.2 incident follow-up).
//
// A pinned self-check / node-probe request (X-LLM-Pin-Credential, trusted
// callers only) exists to test a node the router currently distrusts. Before
// the fix, the URSM v2 availability filter dropped the pinned candidate
// first, so the pinned gateway round always failed with the gateway's own
// no-candidate error, the probe verdict was "failure", and available=0 was
// written back — a self-sustaining lockout no direct-round success could
// ever clear. These tests pin the regression: the pinned credential must
// survive runtime availability filtering.
package executors

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestPlanCandidatesPinned_UnavailableNodeSurvives: with URSM v2
// authoritative and the pinned node marked unavailable in Redis, the pinned
// variant must still return that node; the legacy variant must keep
// filtering it out.
func TestPlanCandidatesPinned_UnavailableNodeSurvives(t *testing.T) {
	mgr, mr := buildV2Manager(t, api.ModeAuthoritative)
	// Node 1 (the probe target) unavailable, node 2 healthy.
	seedV2Node(t, mr, 1, "m", 1, false)
	seedV2Node(t, mr, 2, "m", 1, true)

	r := NewRouter(nil, nil)
	r.URSMv2 = mgr

	pin := 1
	plain := r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-pin-1",
	)
	for _, c := range plain {
		if c.CredentialID == 1 {
			t.Fatalf("unpinned plan must drop unavailable node 1, got %+v", plain)
		}
	}

	pinned := r.PlanCandidatesPinned(
		context.Background(), candidateSet(), nil, &pin, &provider.Policy{}, nil, "t", "m", "req-pin-2",
	)
	var sawPin bool
	for _, c := range pinned {
		if c.CredentialID == 1 {
			sawPin = true
		}
	}
	if !sawPin {
		t.Fatalf("pinned plan must keep the probe target node 1, got %+v", pinned)
	}
}

// TestPlanCandidatesPinned_AllUnavailableStillReturnsPin: the incident shape
// — every provider of a model locked out. The pinned probe must still get
// its node so the gateway round can execute and (on success) write the
// recovery evidence.
func TestPlanCandidatesPinned_AllUnavailableStillReturnsPin(t *testing.T) {
	mgr, mr := buildV2Manager(t, api.ModeAuthoritative)
	seedV2Node(t, mr, 1, "m", 1, false)
	seedV2Node(t, mr, 2, "m", 1, false)

	r := NewRouter(nil, nil)
	r.URSMv2 = mgr

	pin := 2
	pinned := r.PlanCandidatesPinned(
		context.Background(), candidateSet(), nil, &pin, &provider.Policy{}, nil, "t", "m", "req-pin-3",
	)
	if len(pinned) != 1 || pinned[0].CredentialID != 2 {
		t.Fatalf("pinned plan with all nodes unavailable must return exactly the pinned node, got %+v", pinned)
	}
}

// TestRescuePinnedCandidate unit-covers the helper: present pin is a no-op,
// missing pin is appended, unknown pin changes nothing.
func TestRescuePinnedCandidate(t *testing.T) {
	pre := []provider.Candidate{
		{CredentialID: 1, RawModel: "m"},
		{CredentialID: 2, RawModel: "m"},
	}
	filtered := []provider.Candidate{{CredentialID: 2, RawModel: "m"}}

	if got := rescuePinnedCandidate(pre, filtered, 2); len(got) != 1 {
		t.Fatalf("pin already present must be unchanged, got %+v", got)
	}
	got := rescuePinnedCandidate(pre, filtered, 1)
	if len(got) != 2 || got[0].CredentialID != 2 || got[1].CredentialID != 1 {
		t.Fatalf("missing pin must be appended, got %+v", got)
	}
	if got := rescuePinnedCandidate(pre, filtered, 99); len(got) != 1 {
		t.Fatalf("unknown pin must change nothing, got %+v", got)
	}
}
