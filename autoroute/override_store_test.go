package autoroute

// override_store_test.go — P7.6 unit tests for OverrideStore.
// We don't hit a real DB; we directly manipulate the atomic
// pointer to inject snapshots, then verify the post-override
// behaviour (FilterBanned, PromotePins) on synthetic candidates.

import (
	"testing"
	"time"
)

// makeSnapshot is a tiny test helper to build an overrideSnapshot
// without going through Reload.
func makeSnapshot(overrides ...Override) *overrideSnapshot {
	snap := &overrideSnapshot{
		byTaskProfile: map[string][]Override{},
		LoadedAt:      time.Now(),
	}
	for _, o := range overrides {
		key := overrideKey(o.TaskType, o.Profile)
		snap.byTaskProfile[key] = append(snap.byTaskProfile[key], o)
	}
	return snap
}

func newStoreWith(overrides ...Override) *OverrideStore {
	s := NewOverrideStore(nil)
	s.snapshot.Store(makeSnapshot(overrides...))
	return s
}

func TestOverrideStore_FilterBanned_NoBans(t *testing.T) {
	s := newStoreWith()
	cands := []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "gpt-4o"}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "claude"}, Breakdown: ScoringBreakdown{Composite: 80}},
	}
	out := s.FilterBanned(cands, "code", "smart")
	if len(out) != 2 {
		t.Errorf("expected 2 candidates (no bans), got %d", len(out))
	}
}

func TestOverrideStore_FilterBanned_RemovesBanned(t *testing.T) {
	s := newStoreWith(
		Override{TaskType: "code", Profile: "smart", Mode: OverrideBan, ModelChosen: "gpt-4o"},
	)
	cands := []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "gpt-4o"}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "claude"}, Breakdown: ScoringBreakdown{Composite: 80}},
		{Candidate: Candidate{CanonicalName: "gemini"}, Breakdown: ScoringBreakdown{Composite: 70}},
	}
	out := s.FilterBanned(cands, "code", "smart")
	if len(out) != 2 {
		t.Errorf("expected 2 candidates after ban, got %d", len(out))
	}
	for _, c := range out {
		if c.Candidate.CanonicalName == "gpt-4o" {
			t.Error("gpt-4o should have been banned")
		}
	}
}

func TestOverrideStore_FilterBanned_DifferentTaskType(t *testing.T) {
	// Ban is for "code" only — should NOT filter "chat" candidates
	s := newStoreWith(
		Override{TaskType: "code", Profile: "smart", Mode: OverrideBan, ModelChosen: "gpt-4o"},
	)
	cands := []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "gpt-4o"}, Breakdown: ScoringBreakdown{Composite: 90}},
	}
	out := s.FilterBanned(cands, "chat", "smart")
	if len(out) != 1 {
		t.Errorf("ban scoped to 'code' should not affect 'chat', got %d", len(out))
	}
}

func TestOverrideStore_FilterBanned_DifferentProfile(t *testing.T) {
	s := newStoreWith(
		Override{TaskType: "code", Profile: "smart", Mode: OverrideBan, ModelChosen: "gpt-4o"},
	)
	cands := []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "gpt-4o"}, Breakdown: ScoringBreakdown{Composite: 90}},
	}
	// cost_first profile should NOT see the smart ban
	out := s.FilterBanned(cands, "code", "cost_first")
	if len(out) != 1 {
		t.Errorf("ban scoped to 'smart' should not affect 'cost_first', got %d", len(out))
	}
}

func TestOverrideStore_PromotePins_PinMovesToTop(t *testing.T) {
	s := newStoreWith(
		Override{TaskType: "code", Profile: "smart", Mode: OverridePin, ModelChosen: "gemini"},
	)
	cands := []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "gpt-4o"}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "claude"}, Breakdown: ScoringBreakdown{Composite: 80}},
		{Candidate: Candidate{CanonicalName: "gemini"}, Breakdown: ScoringBreakdown{Composite: 70}},
	}
	out := s.PromotePins(cands, "code", "smart")
	if len(out) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(out))
	}
	if out[0].Candidate.CanonicalName != "gemini" {
		t.Errorf("pinned model should be first, got %s", out[0].Candidate.CanonicalName)
	}
	// Order of unpinned should be preserved
	if out[1].Candidate.CanonicalName != "gpt-4o" || out[2].Candidate.CanonicalName != "claude" {
		t.Errorf("unpinned order broken: %s, %s", out[1].Candidate.CanonicalName, out[2].Candidate.CanonicalName)
	}
}

func TestOverrideStore_PromotePins_MultiplePins(t *testing.T) {
	s := newStoreWith(
		Override{TaskType: "code", Profile: "smart", Mode: OverridePin, ModelChosen: "claude"},
		Override{TaskType: "code", Profile: "smart", Mode: OverridePin, ModelChosen: "gemini"},
	)
	cands := []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "gpt-4o"}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "claude"}, Breakdown: ScoringBreakdown{Composite: 80}},
		{Candidate: Candidate{CanonicalName: "gemini"}, Breakdown: ScoringBreakdown{Composite: 70}},
	}
	out := s.PromotePins(cands, "code", "smart")
	if out[0].Candidate.CanonicalName != "claude" {
		t.Errorf("first pin (claude) should be first, got %s", out[0].Candidate.CanonicalName)
	}
	if out[1].Candidate.CanonicalName != "gemini" {
		t.Errorf("second pin (gemini) should be second, got %s", out[1].Candidate.CanonicalName)
	}
	if out[2].Candidate.CanonicalName != "gpt-4o" {
		t.Errorf("non-pinned (gpt-4o) should be last, got %s", out[2].Candidate.CanonicalName)
	}
}

func TestOverrideStore_PromotePins_NoPins(t *testing.T) {
	s := newStoreWith()
	cands := []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "a"}},
		{Candidate: Candidate{CanonicalName: "b"}},
	}
	out := s.PromotePins(cands, "code", "smart")
	if len(out) != 2 {
		t.Errorf("expected 2 candidates unchanged, got %d", len(out))
	}
}

func TestOverrideStore_PromotePins_OnlyPinPresent(t *testing.T) {
	// Pin is for a model that isn't in the candidates list
	s := newStoreWith(
		Override{TaskType: "code", Profile: "smart", Mode: OverridePin, ModelChosen: "missing-model"},
	)
	cands := []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "gpt-4o"}},
		{Candidate: Candidate{CanonicalName: "claude"}},
	}
	out := s.PromotePins(cands, "code", "smart")
	// No candidate is in the pin set, so order is preserved
	if len(out) != 2 {
		t.Errorf("expected 2 candidates unchanged (no pin matches), got %d", len(out))
	}
}

func TestOverrideStore_FilterBannedThenPromotePins(t *testing.T) {
	// Combined: ban one, pin another, then verify final order
	s := newStoreWith(
		Override{TaskType: "code", Profile: "smart", Mode: OverrideBan, ModelChosen: "gpt-4o"},
		Override{TaskType: "code", Profile: "smart", Mode: OverridePin, ModelChosen: "gemini"},
	)
	cands := []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "gpt-4o"}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "claude"}, Breakdown: ScoringBreakdown{Composite: 80}},
		{Candidate: Candidate{CanonicalName: "gemini"}, Breakdown: ScoringBreakdown{Composite: 70}},
	}
	filtered := s.FilterBanned(cands, "code", "smart")
	if len(filtered) != 2 {
		t.Errorf("after ban, expected 2, got %d", len(filtered))
	}
	final := s.PromotePins(filtered, "code", "smart")
	if len(final) != 2 {
		t.Fatalf("after promote, expected 2, got %d", len(final))
	}
	if final[0].Candidate.CanonicalName != "gemini" {
		t.Errorf("gemini (pinned) should be first, got %s", final[0].Candidate.CanonicalName)
	}
	if final[1].Candidate.CanonicalName != "claude" {
		t.Errorf("claude (unpinned) should be second, got %s", final[1].Candidate.CanonicalName)
	}
}

func TestOverrideStore_GetBans(t *testing.T) {
	s := newStoreWith(
		Override{TaskType: "code", Profile: "smart", Mode: OverrideBan, ModelChosen: "gpt-4o"},
		Override{TaskType: "code", Profile: "smart", Mode: OverrideBan, ModelChosen: "claude"},
		Override{TaskType: "code", Profile: "smart", Mode: OverridePin, ModelChosen: "gemini"}, // pin, not ban
	)
	bans := s.GetBans("code", "smart")
	if len(bans) != 2 {
		t.Errorf("expected 2 bans, got %d", len(bans))
	}
	if !bans["gpt-4o"] || !bans["claude"] {
		t.Error("expected gpt-4o and claude to be banned")
	}
	if bans["gemini"] {
		t.Error("gemini is a pin, not a ban")
	}
}

func TestOverrideStore_GetPins(t *testing.T) {
	s := newStoreWith(
		Override{TaskType: "code", Profile: "smart", Mode: OverridePin, ModelChosen: "first"},
		Override{TaskType: "code", Profile: "smart", Mode: OverridePin, ModelChosen: "second"},
		Override{TaskType: "code", Profile: "smart", Mode: OverrideBan, ModelChosen: "ban-model"},
	)
	pins := s.GetPins("code", "smart")
	if len(pins) != 2 {
		t.Errorf("expected 2 pins, got %d", len(pins))
	}
	// Pins should be in insertion order (oldest first)
	if pins[0] != "first" || pins[1] != "second" {
		t.Errorf("expected [first, second], got %v", pins)
	}
}

func TestOverrideStore_EmptySnapshot(t *testing.T) {
	// A never-Reloaded store returns empty results, not nil maps
	s := NewOverrideStore(nil)
	if s.GetBans("code", "smart") == nil {
		t.Error("GetBans should return empty map, not nil")
	}
	if s.GetPins("code", "smart") == nil {
		t.Error("GetPins should return empty slice, not nil")
	}
}

func TestOverrideStore_OverrideKeyIsolatesTaskAndProfile(t *testing.T) {
	// Same model can be banned for one task/profile and pinned for another
	s := newStoreWith(
		Override{TaskType: "code", Profile: "smart", Mode: OverrideBan, ModelChosen: "gpt-4o"},
		Override{TaskType: "chat", Profile: "smart", Mode: OverridePin, ModelChosen: "gpt-4o"},
	)
	bans := s.GetBans("code", "smart")
	pins := s.GetPins("chat", "smart")
	if !bans["gpt-4o"] {
		t.Error("gpt-4o should be banned on code/smart")
	}
	if len(pins) != 1 || pins[0] != "gpt-4o" {
		t.Errorf("gpt-4o should be pinned on chat/smart, got %v", pins)
	}
}
