package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestDispatchCandidateAllowedKeepsUnavailablePinnedProbeCandidate(t *testing.T) {
	candidate := provider.Candidate{AvailabilityState: "cooling"}
	if dispatchCandidateAllowed(candidate, nil) {
		t.Fatal("unavailable unpinned candidate must be filtered")
	}
	pin := 17
	if !dispatchCandidateAllowed(candidate, &pin) {
		t.Fatal("router-rescued pinned probe candidate must reach dispatch")
	}
}
