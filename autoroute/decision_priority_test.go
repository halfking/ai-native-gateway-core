package autoroute

import (
	"context"
	"testing"
	"time"
)

// TestDecide_ExplicitDefaultPromotesWinner (M2, AUTO-02):
// When UseExplicitDefault is on and Resolve hits a model present in the
// candidate pool, that model is promoted to winner and RoutingSource reflects
// explicit_default. See 22 §22.4 / §22.9 AUTO-02.
func TestDecide_ExplicitDefaultPromotesWinner(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{UseExplicitDefault: true})
	defer func() { SetGlobalFeatureFlagsForTest(old) }()

	cls := &stubClassifier{name: "heuristic", out: &Classification{
		Primary: TaskCode, Confidence: 0.9, Classifier: "heuristic",
	}}
	// Pool order: cheap-model first (would win on score), target-model second.
	idx := &stubIndex{cands: []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "cheap-model", CredentialID: 1}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "target-model", CredentialID: 2}, Breakdown: ScoringBreakdown{Composite: 60}},
	}}

	drs := &DefaultRoutingStore{}
	drs.snapshot.Store(&defaultRoutingSnapshot{
		byTask: map[string][]DefaultRouting{
			"code": {{TaskType: "code", Profile: "smart", CanonicalModel: "target-model", Tier: RoutingPrimary, Priority: 100}},
		},
	})

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.SetDefaultRoutingStore(drs)

	dec, err := d.Decide(context.Background(), ClassificationSignals{}, 0, "", "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if dec.ChosenModel != "target-model" {
		t.Fatalf("explicit default should promote target-model, got %s", dec.ChosenModel)
	}
	if dec.RoutingSource != "explicit_default" {
		t.Fatalf("routing source: got %q, want explicit_default", dec.RoutingSource)
	}
}

// TestDecide_ExplicitDefaultMissesFallsBackToImplicit (AUTO-02 neg):
// When the configured default model is NOT in the live pool, we fall back to
// implicit tag scoring (do NOT fabricate a candidate). RoutingSource=implicit_tag.
func TestDecide_ExplicitDefaultMissesFallsBackToImplicit(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{UseExplicitDefault: true})
	defer func() { SetGlobalFeatureFlagsForTest(old) }()

	cls := &stubClassifier{name: "heuristic", out: &Classification{Primary: TaskCode, Confidence: 0.9}}
	idx := &stubIndex{cands: []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "cheap-model", CredentialID: 1}, Breakdown: ScoringBreakdown{Composite: 90}},
	}}
	drs := &DefaultRoutingStore{}
	drs.snapshot.Store(&defaultRoutingSnapshot{
		byTask: map[string][]DefaultRouting{
			"code": {{TaskType: "code", Profile: "smart", CanonicalModel: "unavailable-model"}},
		},
	})

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.SetDefaultRoutingStore(drs)

	dec, err := d.Decide(context.Background(), ClassificationSignals{}, 0, "", "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if dec.ChosenModel != "cheap-model" {
		t.Fatalf("miss should keep implicit winner, got %s", dec.ChosenModel)
	}
	if dec.RoutingSource != "implicit_tag" {
		t.Fatalf("routing source: got %q, want implicit_tag", dec.RoutingSource)
	}
}

// TestDecide_FlagOffSkipsExplicitDefault: with UseExplicitDefault=false,
// DefaultRoutingStore is never consulted even if wired.
func TestDecide_FlagOffSkipsExplicitDefault(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{UseExplicitDefault: false})
	defer func() { SetGlobalFeatureFlagsForTest(old) }()

	cls := &stubClassifier{name: "heuristic", out: &Classification{Primary: TaskCode, Confidence: 0.9}}
	idx := &stubIndex{cands: []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "cheap-model", CredentialID: 1}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "target-model", CredentialID: 2}, Breakdown: ScoringBreakdown{Composite: 60}},
	}}
	drs := &DefaultRoutingStore{}
	drs.snapshot.Store(&defaultRoutingSnapshot{
		byTask: map[string][]DefaultRouting{
			"code": {{TaskType: "code", Profile: "smart", CanonicalModel: "target-model"}},
		},
	})

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.SetDefaultRoutingStore(drs)

	dec, _ := d.Decide(context.Background(), ClassificationSignals{}, 0, "", "", "")
	if dec.ChosenModel != "cheap-model" {
		t.Fatalf("flag off: implicit winner should stand, got %s", dec.ChosenModel)
	}
	if dec.RoutingSource != "implicit_tag" {
		t.Fatalf("flag off: source should be implicit_tag, got %s", dec.RoutingSource)
	}
}

// TestDecide_OverridePinBeatsExplicitDefault (AUTO-03):
// priority order ban > pin > explicit_default > implicit_tag.
// A pin on a third model must win over the explicit default.
func TestDecide_OverridePinBeatsExplicitDefault(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{UseExplicitDefault: true})
	defer func() { SetGlobalFeatureFlagsForTest(old) }()

	cls := &stubClassifier{name: "heuristic", out: &Classification{Primary: TaskCode, Confidence: 0.9}}
	idx := &stubIndex{cands: []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "cheap-model", CredentialID: 1}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "target-model", CredentialID: 2}, Breakdown: ScoringBreakdown{Composite: 60}},
		{Candidate: Candidate{CanonicalName: "pinned-model", CredentialID: 3}, Breakdown: ScoringBreakdown{Composite: 30}},
	}}

	drs := &DefaultRoutingStore{}
	drs.snapshot.Store(&defaultRoutingSnapshot{
		byTask: map[string][]DefaultRouting{
			"code": {{TaskType: "code", Profile: "smart", CanonicalModel: "target-model"}},
		},
	})

	ors := NewOverrideStore(nil)
	ors.snapshot.Store(&overrideSnapshot{
		byTaskProfile: map[string][]Override{
			"code|smart": {{Mode: OverridePin, ModelChosen: "pinned-model"}},
		},
	})

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.SetDefaultRoutingStore(drs)
	d.SetOverrideStore(ors)

	dec, err := d.Decide(context.Background(), ClassificationSignals{}, 0, "", "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if dec.ChosenModel != "pinned-model" {
		t.Fatalf("pin should beat explicit default, got %s", dec.ChosenModel)
	}
	if dec.RoutingSource != "override_pin" {
		t.Fatalf("routing source: got %q, want override_pin", dec.RoutingSource)
	}
}

// TestDecide_OverrideBanRemovesExplicitDefault (AUTO-03 neg):
// A ban on the explicit-default model removes it; we fall back to implicit.
func TestDecide_OverrideBanRemovesExplicitDefault(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{UseExplicitDefault: true})
	defer func() { SetGlobalFeatureFlagsForTest(old) }()

	cls := &stubClassifier{name: "heuristic", out: &Classification{Primary: TaskCode, Confidence: 0.9}}
	idx := &stubIndex{cands: []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "cheap-model", CredentialID: 1}, Breakdown: ScoringBreakdown{Composite: 90}},
		{Candidate: Candidate{CanonicalName: "target-model", CredentialID: 2}, Breakdown: ScoringBreakdown{Composite: 60}},
	}}
	drs := &DefaultRoutingStore{}
	drs.snapshot.Store(&defaultRoutingSnapshot{
		byTask: map[string][]DefaultRouting{
			"code": {{TaskType: "code", Profile: "smart", CanonicalModel: "target-model"}},
		},
	})
	ors := NewOverrideStore(nil)
	ors.snapshot.Store(&overrideSnapshot{
		byTaskProfile: map[string][]Override{
			"code|smart": {{Mode: OverrideBan, ModelChosen: "target-model"}},
		},
	})

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.SetDefaultRoutingStore(drs)
	d.SetOverrideStore(ors)

	dec, err := d.Decide(context.Background(), ClassificationSignals{}, 0, "", "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if dec.ChosenModel != "cheap-model" {
		t.Fatalf("ban should remove target-model, got %s", dec.ChosenModel)
	}
}

// TestPromoteCanonical_Helper: unit-test the promotion helper directly.
func TestPromoteCanonical_Helper(t *testing.T) {
	cands := []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "a"}},
		{Candidate: Candidate{CanonicalName: "b"}},
		{Candidate: Candidate{CanonicalName: "c"}},
	}
	out := promoteCanonical(cands, "b")
	if out[0].Candidate.CanonicalName != "b" {
		t.Fatalf("b should be first, got %s", out[0].Candidate.CanonicalName)
	}
	if len(out) != 3 || out[1].Candidate.CanonicalName != "a" || out[2].Candidate.CanonicalName != "c" {
		t.Fatalf("order broken: %v", []string{out[1].Candidate.CanonicalName, out[2].Candidate.CanonicalName})
	}
	// not found → unchanged
	out2 := promoteCanonical(cands, "zzz")
	if out2[0].Candidate.CanonicalName != "a" {
		t.Fatal("missing model should leave order unchanged")
	}
	// already first → unchanged
	out3 := promoteCanonical(cands, "a")
	if out3[0].Candidate.CanonicalName != "a" {
		t.Fatal("already-first should stay first")
	}
}

// keep the time import used (some stubs above reference durations indirectly)
var _ = time.Minute
