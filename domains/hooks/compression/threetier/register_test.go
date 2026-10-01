// Package threetier - register_test.go (2026-09-30, 修订审计三十二轮 §四A A-G1)
//
// Proves the init()-time registration actually wires threetier's validator
// into the compression package, and that the adapter surfaces
// DetectMisalignment verdicts verbatim.
package threetier

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

// TestInitRegistersVerifierIntoCompression: importing this package (which
// every production binary does via security/sanitize's activation import)
// must leave the hook registered in compression.
func TestInitRegistersVerifierIntoCompression(t *testing.T) {
	if !compression.ThreeTierCheckRegistered() {
		t.Fatal("threetier init() did not register the verifier into compression")
	}
}

// TestVerifySessionState_AdaptsDetectMisalignment checks the adapter returns
// nil for consistent states and human-readable reasons for regressed ones.
func TestVerifySessionState_AdaptsDetectMisalignment(t *testing.T) {
	if got := VerifySessionState(nil); got != nil {
		t.Fatalf("nil state must be consistent, got %v", got)
	}
	if got := VerifySessionState(&compression.SessionState{}); got != nil {
		t.Fatalf("empty state must be consistent, got %v", got)
	}

	bad := &compression.SessionState{
		RawTokenEstimate: 100,
		CompressedTokens: 200, // regression: compressed exceeds raw
	}
	got := VerifySessionState(bad)
	if len(got) != 1 {
		t.Fatalf("expected 1 misalignment, got %v", got)
	}
	if !strings.Contains(got[0], "compressed tokens exceed source tokens") {
		t.Fatalf("reason does not surface DetectMisalignment verdict: %q", got[0])
	}
}
