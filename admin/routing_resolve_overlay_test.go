package admin

import (
	"testing"
	"time"

	v2api "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

// TestApplyURSMOverlayPairsViewsByKey covers the 2026-08-18 production
// incident: FilterAndScore sorts views by score before returning them, so
// they arrive in a different order than the candidates. The previous
// index-based pairing matched the wrong NodeView to each candidate (log:
// "routing resolve: ursm v2 view out of order") and skipped the overlay
// for every misaligned row — an available node could inherit another
// node's unavailable runtime state, or lose the overlay entirely.
func TestApplyURSMOverlayPairsViewsByKey(t *testing.T) {
	now := time.Now()
	candidates := []resolveCandidate{
		{CredentialID: 35, ModelName: "glm-5.2", Routable: true, RuntimeRoutable: true},
		{CredentialID: 18, ModelName: "z-ai/glm-5.2", Routable: true, RuntimeRoutable: true},
		{CredentialID: 8, ModelName: "z-ai/glm-5.2", Routable: true, RuntimeRoutable: true},
	}
	// Deliberately shuffled relative to the candidate order, mirroring the
	// sort-by-score contract of FilterAndScore.
	views := []v2api.NodeView{
		{CredentialID: 8, RawModel: "z-ai/glm-5.2", Available: true, SR5m: 0.97, LatP95Ms: 1200},
		{CredentialID: 35, RawModel: "glm-5.2", Available: false, Reason: "in_cool_until"},
		{CredentialID: 18, RawModel: "z-ai/glm-5.2", Available: true, SR5m: 0.99},
	}

	applyURSMOverlay(candidates, views, now)

	if candidates[0].Available {
		t.Fatalf("cred 35 should inherit its own unavailable view, got available")
	}
	if candidates[0].Routable || candidates[0].RuntimeRoutable {
		t.Fatalf("cred 35 should be unroutable after overlay")
	}
	if candidates[0].BlockReason != "in_cool_until" {
		t.Fatalf("cred 35 block_reason = %q, want in_cool_until", candidates[0].BlockReason)
	}
	// candidates[1] is cred 18 (SR5m 0.99), candidates[2] is cred 8 (0.97).
	if got := candidates[1].SuccessRate; got != 0.99 {
		t.Fatalf("cred 18 success rate = %v, want 0.99", got)
	}
	if got := candidates[2].SuccessRate; got != 0.97 {
		t.Fatalf("cred 8 success rate = %v, want 0.97", got)
	}
	for i := 1; i <= 2; i++ {
		if !candidates[i].Routable || !candidates[i].Available {
			t.Fatalf("available candidate %d lost routable/available after overlay", i)
		}
	}
}

// TestApplyURSMOverlayWritesBackMutations guards the loop-copy fix: the
// previous implementation iterated `for i, c := range candidates` and
// mutated the copy, so neither the defaults nor the overlay ever reached
// the emitted slice. Every candidate must now carry non-zero runtime
// fields after the overlay runs.
func TestApplyURSMOverlayWritesBackMutations(t *testing.T) {
	candidates := []resolveCandidate{
		{CredentialID: 22, ModelName: "glm-5.2", Routable: true, RuntimeRoutable: true},
		{CredentialID: 7, ModelName: "glm-5.2", Routable: true, RuntimeRoutable: true},
	}
	// One view, one miss: the miss must keep the T4 defaults (available,
	// closed circuit) instead of the zero values the discarded copy left
	// behind (Available=false, CircuitState="").
	views := []v2api.NodeView{
		{CredentialID: 22, RawModel: "glm-5.2", Available: true, FailStreak: 0},
	}

	applyURSMOverlay(candidates, views, time.Now())

	if !candidates[1].Available {
		t.Fatalf("Redis-miss candidate should keep default Available=true (T4 contract), got false")
	}
	if candidates[1].CircuitState != "closed" {
		t.Fatalf("Redis-miss candidate should keep default circuit closed, got %q", candidates[1].CircuitState)
	}
	if candidates[1].SuccessRate != 0.9 {
		t.Fatalf("Redis-miss candidate should keep default success rate 0.9, got %v", candidates[1].SuccessRate)
	}
	if !candidates[0].Available || candidates[0].CircuitState != "closed" {
		t.Fatalf("overlaid candidate lost runtime defaults: available=%v circuit=%q",
			candidates[0].Available, candidates[0].CircuitState)
	}
}

// TestApplyURSMOverlayCoolUntilFlipsCircuit verifies a future cool_until
// marks the candidate as circuit-open and unroutable with the canonical
// block reason, while an expired cool_until does not.
func TestApplyURSMOverlayCoolUntilFlipsCircuit(t *testing.T) {
	now := time.Now()
	candidates := []resolveCandidate{
		{CredentialID: 1, ModelName: "m", Routable: true, RuntimeRoutable: true},
		{CredentialID: 2, ModelName: "m", Routable: true, RuntimeRoutable: true},
	}
	views := []v2api.NodeView{
		{CredentialID: 1, RawModel: "m", Available: true, CoolUntil: now.Add(10 * time.Minute)},
		{CredentialID: 2, RawModel: "m", Available: true, CoolUntil: now.Add(-1 * time.Minute)},
	}

	applyURSMOverlay(candidates, views, now)

	if candidates[0].CircuitState != "open" || candidates[0].Routable {
		t.Fatalf("future cool_until should open the circuit and block routing; circuit=%q routable=%v",
			candidates[0].CircuitState, candidates[0].Routable)
	}
	if candidates[0].BlockReason != "node_in_cool_until" {
		t.Fatalf("block_reason = %q, want node_in_cool_until", candidates[0].BlockReason)
	}
	if candidates[1].CircuitState != "closed" || !candidates[1].Routable {
		t.Fatalf("expired cool_until should keep the node routable; circuit=%q routable=%v",
			candidates[1].CircuitState, candidates[1].Routable)
	}
}

// TestApplyURSMOverlayKeepsSQLVetoReason verifies that a candidate already
// vetoed by the SQL view keeps the view's block_reason — the runtime
// overlay must not overwrite schema-level reasons with runtime ones.
func TestApplyURSMOverlayKeepsSQLVetoReason(t *testing.T) {
	candidates := []resolveCandidate{
		{CredentialID: 22, ModelName: "glm-5.2", Routable: false, RuntimeRoutable: false, BlockReason: "availability_suspended"},
	}
	views := []v2api.NodeView{
		{CredentialID: 22, RawModel: "glm-5.2", Available: false, Reason: "last_err_429"},
	}

	applyURSMOverlay(candidates, views, time.Now())

	if candidates[0].BlockReason != "availability_suspended" {
		t.Fatalf("SQL veto reason must be preserved, got %q", candidates[0].BlockReason)
	}
	if candidates[0].Routable {
		t.Fatalf("SQL-vetoed candidate must stay unroutable")
	}
}

func TestURSMViewKeyIsCollisionFree(t *testing.T) {
	if ursmViewKey(12, "m") == ursmViewKey(1, "2m") {
		t.Fatalf("key collision between (12,\"m\") and (1,\"2m\")")
	}
}
