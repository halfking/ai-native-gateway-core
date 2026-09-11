package main

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

// fixtureCanonical replicates the junk shape the pre-2026-09-10 seeder
// produced, plus the legitimate short rows that must NOT be flagged.
func fixtureCanonical() []CanonicalRow {
	return []CanonicalRow{
		{ID: 1, Name: "claude-opus-5", Status: "active", Source: "discovery"},
		{ID: 2, Name: "opus-5", Status: "active", Source: "provider_refresh"}, // junk (manual-refresh seeding)
		{ID: 3, Name: "grok-4.6", Status: "active", Source: "discovery"},      // target
		{ID: 4, Name: "4.6", Status: "active", Source: "discovery"},           // junk
		{ID: 5, Name: "glm-5.2", Status: "active", Source: "discovery"},       // legit short row
		{ID: 6, Name: "kimi-k2-cn", Status: "active", Source: "discovery"},    // legit short row
		{ID: 7, Name: "gpt-5", Status: "active", Source: "taxonomy"},          // curated: out of scope
		{ID: 8, Name: "old-junk", Status: "deprecated", Source: "discovery"},
		{ID: 9, Name: "deepseek-v3.2", Status: "active", Source: "discovery"}, // punctuation twin of id 10
		{ID: 10, Name: "deepseek-v3-2", Status: "active", Source: "discovery"},
	}
}

func fixtureAliases() []AliasRow {
	return []AliasRow{
		// the old pipeline's self-variants on the junk rows — must NOT disqualify
		{ID: 101, CanonicalID: 2, RawName: "opus-5", Status: "active"},
		{ID: 102, CanonicalID: 2, RawName: "opus_5", Status: "active"},
		{ID: 103, CanonicalID: 4, RawName: "4-6", Status: "active"},
		// post-apply shape: alias already pointing at the right row
		{ID: 105, CanonicalID: 1, RawName: "claude-opus-5", Status: "active"},
	}
}

func fixtureRefs() []ProviderModelRow {
	junkOpus := int64(2)
	junk46 := int64(4)
	goodClaude := int64(1)
	goodGlm := int64(5)
	return []ProviderModelRow{
		{ID: 9001, ProviderID: 36994, RawModelName: "claude/opus-5", CanonicalID: &junkOpus, StandardizedName: "opus-5", Available: true},
		{ID: 9002, ProviderID: 36994, RawModelName: "grok/4.6", CanonicalID: &junk46, StandardizedName: "4.6", Available: true},
		{ID: 9003, ProviderID: 7, RawModelName: "z-ai/glm-5.2", CanonicalID: &goodGlm, StandardizedName: "glm-5.2", Available: true},
		// already partially fixed: canonical_id correct, standardized_name stale
		{ID: 9004, ProviderID: 7, RawModelName: "claude-opus-5", CanonicalID: &goodClaude, StandardizedName: "opus-5", Available: true},
	}
}

func fixtureCorpus() []string {
	return []string{
		"claude/opus-5",
		"grok/4.6",
		"z-ai/glm-5.2",
		"zhipu/kimi-k2-cn",
		"deepseek/deepseek-v3.2",
	}
}

func suspectByName(d *Diagnosis, name string) *Suspect {
	for i := range d.Suspects {
		if d.Suspects[i].Row.Name == name {
			return &d.Suspects[i]
		}
	}
	return nil
}

func TestNormalizeSelfVariant(t *testing.T) {
	cases := map[string]string{
		"opus-5":  "opus-5",
		"Opus_5":  "opus-5",
		"4.6":     "4-6",
		"4-6":     "4-6",
		" GLM-5 ": "glm-5",
	}
	for in, want := range cases {
		if got := normalizeSelfVariant(in); got != want {
			t.Errorf("normalizeSelfVariant(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDiagnoseDetectsClassicJunk is the core detection case: the two junk
// rows from the 2026-09-10 incident are fixable with the right targets,
// while legitimate short rows, punctuation twins, curated rows and
// deprecated rows are never flagged.
func TestDiagnoseDetectsClassicJunk(t *testing.T) {
	d := Diagnose(fixtureCanonical(), fixtureAliases(), fixtureRefs(), fixtureCorpus(), modelname.AutoLinkThreshold)

	s := suspectByName(d, "opus-5")
	if s == nil {
		t.Fatalf("opus-5 not detected as suspect; suspects=%+v", d.Suspects)
	}
	if s.Verdict != VerdictFixable {
		t.Errorf("opus-5 verdict = %q, want fixable", s.Verdict)
	}
	if s.Target == nil || s.Target.Name != "claude-opus-5" {
		t.Fatalf("opus-5 target = %v, want claude-opus-5", s.Target)
	}
	if s.Target.Score < modelname.AutoLinkThreshold {
		t.Errorf("opus-5 target score = %.3f, want ≥ %.2f", s.Target.Score, modelname.AutoLinkThreshold)
	}
	if len(s.Refs) != 2 {
		t.Errorf("opus-5 refs = %d, want 2 (pm 9001 via canonical_id + pm 9004 via stale standardized_name)", len(s.Refs))
	}

	s = suspectByName(d, "4.6")
	if s == nil {
		t.Fatalf("4.6 not detected as suspect")
	}
	if s.Verdict != VerdictFixable || s.Target == nil || s.Target.Name != "grok-4.6" {
		t.Errorf("4.6 = %q target %v, want fixable → grok-4.6", s.Verdict, s.Target)
	}

	// legitimate rows must not be reported at all
	for _, name := range []string{
		"glm-5.2",       // short but holds its own name (0.99 base-exact beats any rival)
		"kimi-k2-cn",    // same, with no junk evidence
		"deepseek-v3.2", // punctuation twin pair — spelling variants are not junk
		"deepseek-v3-2",
		"claude-opus-5",
		"grok-4.6",
		"gpt-5",    // curated source
		"old-junk", // already deprecated
	} {
		if s := suspectByName(d, name); s != nil {
			t.Errorf("%q must not be a suspect, got verdict %q", name, s.Verdict)
		}
	}
	if len(d.Suspects) != 2 {
		t.Errorf("suspects = %d (%v), want exactly opus-5 and 4.6", len(d.Suspects), d.Suspects)
	}
}

// TestDiagnoseWithheldOnForeignAlias: junk evidence plus an active alias
// spelling something else → withheld for manual review, while sibling junk
// rows stay fixable.
func TestDiagnoseWithheldOnForeignAlias(t *testing.T) {
	aliases := append(fixtureAliases(), AliasRow{ID: 110, CanonicalID: 2, RawName: "opus-five", Status: "active"})
	d := Diagnose(fixtureCanonical(), aliases, fixtureRefs(), fixtureCorpus(), modelname.AutoLinkThreshold)

	s := suspectByName(d, "opus-5")
	if s == nil || s.Verdict != VerdictWithheld {
		t.Fatalf("opus-5 = %+v, want withheld-foreign-alias", s)
	}
	if s.Target == nil || s.Target.Name != "claude-opus-5" {
		t.Errorf("withheld row still reports its suggested target, got %v", s.Target)
	}
	if s := suspectByName(d, "4.6"); s == nil || s.Verdict != VerdictFixable {
		t.Errorf("4.6 = %+v, want fixable (its aliases are self-variants only)", s)
	}
}

// TestDiagnoseBeatsRule2Trap documents why the pair test scores the target
// with the suspect removed: with the junk rows left in the catalog, the
// matcher's Rule 2 prefers the bare base row and "claude/opus-5" resolves
// to junk "opus-5".
func TestDiagnoseBeatsRule2Trap(t *testing.T) {
	full := []string{"claude-opus-5", "opus-5", "grok-4.6", "4.6"}
	if got := modelname.BestStandardModelMatch("claude/opus-5", full); got == nil || got.Name != "opus-5" {
		t.Fatalf("precondition: with junk in catalog the matcher returns %v, want opus-5", got)
	}
	d := Diagnose(fixtureCanonical(), fixtureAliases(), fixtureRefs(), fixtureCorpus(), modelname.AutoLinkThreshold)
	if s := suspectByName(d, "opus-5"); s == nil || s.Target == nil || s.Target.Name != "claude-opus-5" {
		t.Errorf("diagnosis target = %v, want claude-opus-5", s)
	}
}

// TestDiagnoseLegitRowSurvivesVendorQualifiedRaw: for the vendor-qualified
// raw 'anthropic/claude-opus-5' the junk row 'opus-5' scores 0.9 by token
// containment, but the real row scores 0.99 base-exact and must win — the
// real row is not flagged even though a rival clears the gate.
func TestDiagnoseLegitRowSurvivesVendorQualifiedRaw(t *testing.T) {
	corpus := append(fixtureCorpus(), "anthropic/claude-opus-5")
	d := Diagnose(fixtureCanonical(), fixtureAliases(), fixtureRefs(), corpus, modelname.AutoLinkThreshold)
	if s := suspectByName(d, "claude-opus-5"); s != nil {
		t.Errorf("claude-opus-5 must not be flagged (its own score beats the rival), got %+v", s)
	}
	if s := suspectByName(d, "opus-5"); s == nil || s.Verdict != VerdictFixable {
		t.Errorf("opus-5 must stay fixable, got %+v", s)
	}
}

// TestDiagnoseIdempotentOnRemediatedDB replays the post-apply state: junk
// rows deprecated, references and aliases re-pointed. A second run must
// find nothing fixable.
func TestDiagnoseIdempotentOnRemediatedDB(t *testing.T) {
	canonical := fixtureCanonical()
	for i := range canonical {
		if canonical[i].ID == 2 || canonical[i].ID == 4 {
			canonical[i].Status = "deprecated"
		}
	}
	goodClaude := int64(1)
	goodGrok := int64(3)
	goodGlm := int64(5)
	aliases := []AliasRow{
		{ID: 201, CanonicalID: 1, RawName: "opus-5", Status: "active"},
		{ID: 202, CanonicalID: 1, RawName: "opus_5", Status: "active"},
		{ID: 203, CanonicalID: 3, RawName: "4-6", Status: "active"},
	}
	refs := []ProviderModelRow{
		{ID: 9001, ProviderID: 36994, RawModelName: "claude/opus-5", CanonicalID: &goodClaude, StandardizedName: "claude-opus-5", Available: true},
		{ID: 9002, ProviderID: 36994, RawModelName: "grok/4.6", CanonicalID: &goodGrok, StandardizedName: "grok-4.6", Available: true},
		{ID: 9003, ProviderID: 7, RawModelName: "z-ai/glm-5.2", CanonicalID: &goodGlm, StandardizedName: "glm-5.2", Available: true},
	}
	d := Diagnose(canonical, aliases, refs, fixtureCorpus(), modelname.AutoLinkThreshold)
	for _, s := range d.Suspects {
		if s.Verdict == VerdictFixable {
			t.Errorf("post-apply state must have no fixable suspects, got %q (%+v)", s.Row.Name, s)
		}
	}
}

// TestDiagnoseAmbiguousTargetsRelegatedToReview: when the top evidence
// score is shared by DIFFERENT target rows (junk "free" tying at 0.99 with
// "glm-5.2:free", "minimax-m3:free", …) the corpus disagrees about where
// the name belongs — the row is reported as review, never auto-remediated.
// `free` is on OperatorWhitelist, so this test clears the whitelist to
// exercise the tie→review path in isolation.
func TestDiagnoseAmbiguousTargetsRelegatedToReview(t *testing.T) {
	saved := OperatorWhitelist
	OperatorWhitelist = nil
	defer func() { OperatorWhitelist = saved }()

	canonical := append(fixtureCanonical(),
		CanonicalRow{ID: 11, Name: "glm-5.2:free", Status: "active", Source: "provider_refresh"},
		CanonicalRow{ID: 12, Name: "minimax-m3:free", Status: "active", Source: "provider_refresh"},
		CanonicalRow{ID: 13, Name: "free", Status: "active", Source: "provider_refresh"},
	)
	corpus := append(fixtureCorpus(), "openrouter/free", "z-ai/glm-5.2:free", "minimax/minimax-m3:free")
	d := Diagnose(canonical, fixtureAliases(), fixtureRefs(), corpus, modelname.AutoLinkThreshold)

	s := suspectByName(d, "free")
	if s == nil {
		t.Fatal("free not detected as suspect")
	}
	if s.Verdict != VerdictReview {
		t.Errorf("free verdict = %q, want review (tied targets)", s.Verdict)
	}
	if s.Target == nil {
		t.Errorf("review row still reports its best-effort target, got nil")
	}
	// the twin rows themselves must not be flagged
	for _, name := range []string{"glm-5.2:free", "minimax-m3:free"} {
		if s := suspectByName(d, name); s != nil {
			t.Errorf("%q must not be a suspect, got verdict %q", name, s.Verdict)
		}
	}
	if plans := BuildApplyPlan(d, modelname.AutoLinkThreshold); len(plans) != 2 {
		t.Errorf("apply plans = %d, want 2 (the review row must not produce a plan)", len(plans))
	}
}

// TestDiagnoseWhitelistOverridesReview replays the production `free` case
// (OpenRouter free-pool pseudo-model, targets tied at 0.99) WITH the
// operator whitelist in place: the row stays reported — as whitelisted,
// with the recorded rationale — but is never remediated, and it must never
// appear as a redirect target for any other suspect.
func TestDiagnoseWhitelistOverridesReview(t *testing.T) {
	if _, ok := OperatorWhitelist["free"]; !ok {
		t.Fatalf("precondition: `free` must be on OperatorWhitelist")
	}
	canonical := append(fixtureCanonical(),
		CanonicalRow{ID: 11, Name: "glm-5.2:free", Status: "active", Source: "provider_refresh"},
		CanonicalRow{ID: 12, Name: "minimax-m3:free", Status: "active", Source: "provider_refresh"},
		CanonicalRow{ID: 13, Name: "free", Status: "active", Source: "provider_refresh"},
	)
	corpus := append(fixtureCorpus(), "openrouter/free", "z-ai/glm-5.2:free", "minimax/minimax-m3:free")
	d := Diagnose(canonical, fixtureAliases(), fixtureRefs(), corpus, modelname.AutoLinkThreshold)

	s := suspectByName(d, "free")
	if s == nil {
		t.Fatal("whitelisted row must still be reported, not silently dropped")
	}
	if s.Verdict != VerdictWhitelisted {
		t.Errorf("free verdict = %q, want whitelisted", s.Verdict)
	}
	if s.WhitelistReason == "" {
		t.Error("whitelisted row must carry its rationale")
	}
	if s.Target == nil {
		t.Error("whitelisted row still reports its evidence target for transparency")
	}
	if plans := BuildApplyPlan(d, modelname.AutoLinkThreshold); len(plans) != 2 {
		t.Errorf("apply plans = %d, want 2 (whitelisted row must not produce a plan)", len(plans))
	}
	for _, p := range BuildApplyPlan(d, modelname.AutoLinkThreshold) {
		if p.TargetID == 13 {
			t.Errorf("redirect plan targets the whitelisted row: %+v", p)
		}
		for _, r := range p.RefRedirects {
			if r.ToCanonicalID == 13 {
				t.Errorf("redirect lands on the whitelisted row: %+v", r)
			}
		}
	}
}

// TestWhitelistRowNeverServesAsRemediationTarget pins the structural half
// of the whitelist invariant: a whitelisted row must never become a
// remediation target — not via a suspect's aggregated suggestion, not via
// an apply-time per-reference redirect, even when the row itself produces
// no evidence (and is therefore absent from the suspects). The synthetic
// whitelist entry on "glm-5.2" turns junk "5.2" (strict token suffix of
// glm-5.2's base) into a suspect whose ONLY gate-passing rival is the
// whitelisted row — with the exclusion in place it must produce no
// evidence at all instead of a poisoned "fixable → whitelisted row" plan.
func TestWhitelistRowNeverServesAsRemediationTarget(t *testing.T) {
	saved := OperatorWhitelist
	defer func() { OperatorWhitelist = saved }()
	OperatorWhitelist = map[string]string{"glm-5.2": "synthetic operator pin for the invariant test"}

	canonical := append(fixtureCanonical(),
		CanonicalRow{ID: 14, Name: "5.2", Status: "active", Source: "discovery"}, // junk suffix of glm-5.2
	)
	d := Diagnose(canonical, fixtureAliases(), fixtureRefs(), fixtureCorpus(), modelname.AutoLinkThreshold)

	if s := suspectByName(d, "5.2"); s != nil {
		t.Errorf("suspect whose only gate-passing rival is whitelisted must not be flagged with a poisoned target, got verdict=%q target=%v", s.Verdict, s.Target)
	}
	for _, s := range d.Suspects {
		if s.Target == nil {
			continue
		}
		if _, wl := OperatorWhitelist[strings.ToLower(s.Target.Name)]; wl {
			t.Errorf("suspect %q suggests whitelisted target %q", s.Row.Name, s.Target.Name)
		}
	}
	for _, n := range d.gateCatalogNames {
		if _, wl := OperatorWhitelist[strings.ToLower(n)]; wl {
			t.Errorf("whitelisted row %q must not be in the apply gate catalog", n)
		}
	}
}

// TestBuildApplyPlanRedirectsAliasesAndDeprecates checks the plan contents:
// gated redirects, alias upserts (junk name + self-variants), and
// deprecation allowed when nothing is skipped.
func TestBuildApplyPlanRedirectsAliasesAndDeprecates(t *testing.T) {
	d := Diagnose(fixtureCanonical(), fixtureAliases(), fixtureRefs(), fixtureCorpus(), modelname.AutoLinkThreshold)
	plans := BuildApplyPlan(d, modelname.AutoLinkThreshold)
	if len(plans) != 2 {
		t.Fatalf("plans = %d, want 2 (opus-5, 4.6)", len(plans))
	}

	byName := map[string]RowPlan{}
	for _, p := range plans {
		byName[p.Suspect.Row.Name] = p
	}

	p := byName["opus-5"]
	if p.TargetName != "claude-opus-5" || p.TargetID != 1 {
		t.Errorf("opus-5 plan target = %q/%d, want claude-opus-5/1", p.TargetName, p.TargetID)
	}
	if len(p.RefRedirects) != 2 {
		t.Fatalf("opus-5 redirects = %d, want 2 (pm 9001 full + pm 9004 standardized_name-only)", len(p.RefRedirects))
	}
	var fullRedirect, stdRedirect *RefRedirect
	for i := range p.RefRedirects {
		r := &p.RefRedirects[i]
		switch r.ProviderModelID {
		case 9001:
			fullRedirect = r
		case 9004:
			stdRedirect = r
		}
	}
	if fullRedirect == nil || fullRedirect.KeepCanonicalID {
		t.Errorf("pm 9001 must be a full redirect, got %+v", fullRedirect)
	}
	if stdRedirect == nil || !stdRedirect.KeepCanonicalID {
		t.Errorf("pm 9004 must keep its (already correct) canonical_id, got %+v", stdRedirect)
	}
	if !p.CanDeprecate || p.BlockedReason != "" {
		t.Errorf("opus-5 with only gate-passing refs must be deprecatable, blocked=%q", p.BlockedReason)
	}
	if len(p.RefSkips) != 0 {
		t.Errorf("no ref may be skipped in the base fixture, got %+v", p.RefSkips)
	}
	upserts := map[string]bool{}
	for _, a := range p.AliasUpserts {
		upserts[a.RawName] = true
		if a.ToCanonicalID != 1 {
			t.Errorf("alias %q target = %d, want 1", a.RawName, a.ToCanonicalID)
		}
	}
	for _, want := range []string{"opus-5", "opus_5"} {
		if !upserts[want] {
			t.Errorf("alias upserts missing %q, got %v", want, upserts)
		}
	}

	p = byName["4.6"]
	if p.TargetName != "grok-4.6" || p.TargetID != 3 {
		t.Errorf("4.6 plan target = %q/%d, want grok-4.6/3", p.TargetName, p.TargetID)
	}
	if len(p.RefRedirects) != 1 || p.RefRedirects[0].ProviderModelID != 9002 {
		t.Errorf("4.6 redirects = %+v, want pm 9002 only", p.RefRedirects)
	}
	if !p.CanDeprecate || p.BlockedReason != "" {
		t.Errorf("4.6 must be fully deprecatable, blocked=%q", p.BlockedReason)
	}
}

// TestBuildApplyPlanGateBlocksStrandedReferences: a junk row referenced by
// a raw name that does NOT confidently match any surviving standard row
// keeps that reference (skip) and is not deprecated.
func TestBuildApplyPlanGateBlocksStrandedReferences(t *testing.T) {
	junkOpus := int64(2)
	refs := append(fixtureRefs(), ProviderModelRow{
		ID: 9005, ProviderID: 42, RawModelName: "acme/opus-5", CanonicalID: &junkOpus, StandardizedName: "opus-5", Available: true,
	})
	d := Diagnose(fixtureCanonical(), fixtureAliases(), refs, fixtureCorpus(), modelname.AutoLinkThreshold)
	plans := BuildApplyPlan(d, modelname.AutoLinkThreshold)
	var plan *RowPlan
	for i := range plans {
		if plans[i].Suspect.Row.Name == "opus-5" {
			plan = &plans[i]
		}
	}
	if plan == nil {
		t.Fatal("opus-5 plan missing")
	}
	if plan.CanDeprecate {
		t.Errorf("deprecation must be blocked while pm 9005 fails the gate")
	}
	found := false
	for _, skip := range plan.RefSkips {
		if skip.ProviderModelID == 9005 {
			found = true
		}
	}
	if !found {
		t.Errorf("pm 9005 must be in RefSkips, got %+v", plan.RefSkips)
	}
	if len(plan.RefRedirects) != 2 {
		t.Errorf("the two confident refs must still redirect, got %+v", plan.RefRedirects)
	}
}

func TestIsStrictTokenSuffixExport(t *testing.T) {
	if !modelname.IsStrictTokenSuffix("opus-5", "claude-opus-5") {
		t.Error("opus-5 should be a strict token suffix of claude-opus-5")
	}
	if modelname.IsStrictTokenSuffix("claude-opus-5", "claude-opus-5") {
		t.Error("equal names are not a STRICT suffix")
	}
	if modelname.IsStrictTokenSuffix("4.6", "glm-4.6-preview") {
		t.Error("4.6 is not a token suffix of glm-4.6-preview")
	}
	prefix, base := modelname.SplitVendorPrefix("z-ai/glm-5.2")
	if prefix != "z-ai" || base != "glm-5.2" {
		t.Errorf("SplitVendorPrefix = (%q,%q)", prefix, base)
	}
	if got := modelname.JoinVendorBase(prefix, base); got != "z-ai-glm-5.2" {
		t.Errorf("JoinVendorBase = %q", got)
	}
}
