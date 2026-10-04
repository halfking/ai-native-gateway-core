package handoff

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/secretmask"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

// Audit 244 (R89-FH) — the three redaction layers this repository ships are
// three independent rule sets, and no test anywhere compares them.
//
//   - sanitize.PatternDetector — the main request/response path (reversible).
//     Wired as the single detector instance at cmd/gateway/goal_control.go:546,
//     feeding inbound sanitize AND outbound output check.
//   - secretmask.MaskSecrets   — summarizer/compression digest only, one-way.
//     Its own package doc states it does not touch the forwarded body.
//   - redactResumeSensitive    — the handoff resume packet (this file's package).
//
// This test does NOT assert a policy. Widening the detector's rules is a
// product decision (pending decision 106): a wider net also masks more of the
// user's own text, and the detector is reversible while the others are not. So
// the mismatch is REPORTED, never enforced.
//
// What is asserted is only that the report itself is trustworthy:
//
//	1. the table is a real cross-layer table, not one layer measured three
//	   times — proved in both directions (see Test244LayerProbeIsNotOneLayer);
//	2. the case list cannot be silently trimmed (req244RequiredCases);
//	3. a MISS is a fact about the rules, not a broken probe
//	   (Test244WideningRuleShrinksGap).
func Test244RedactionLayerRuleSurface(t *testing.T) {
	for _, set := range req244RuleSets(t) {
		layers := req244Layers(t, set.detector)
		t.Logf("=== rule set: %s ===", set.name)
		t.Log("case                 detector secretmask handoff   <- HIT/MISS per layer")
		for _, c := range req244Cases() {
			row := make([]string, 0, len(layers))
			for _, l := range layers {
				if l.hit(c.text) {
					row = append(row, "HIT ")
				} else {
					row = append(row, "MISS")
				}
			}
			t.Logf("%-20s %s", c.name, strings.Join(row, " "))
		}
	}

	// Guard against the failure mode where the case list is edited down until
	// the table looks clean. A MISS-count == 0 assertion would be
	// self-satisfying; pinning the names is not.
	got := map[string]bool{}
	for _, c := range req244Cases() {
		got[c.name] = true
	}
	for _, name := range req244RequiredCases {
		if !got[name] {
			t.Fatalf("req244RequiredCases names %q but the case list does not contain it; "+
				"a trimmed case list would make this test vacuously quiet", name)
		}
	}
}

type req244RuleSet struct {
	name     string
	detector *sanitize.PatternDetector
}

// req244RuleSets enumerates BOTH rule sets the gateway can run with.
//
// Audit 245 corrected audit 244 here: the first version of this table measured
// only sanitize.NewPatternDetector(), the built-in fallback. Production does not
// construct that directly — cmd/gateway/goal_control.go:517-526 calls
// NewPatternDetectorFromFile on a relative path and falls back to the built-in
// set with only a slog.Warn. The two sets do not agree (ak- is caught only by
// the built-in set; IBAN only by the file set), so a table pinned to one of them
// describes a system that may not be the one running.
//
// A census whose denominator is one of two possible states of the subject is
// itself the defect this repo keeps re-learning. Both are printed.
func req244RuleSets(t *testing.T) []req244RuleSet {
	t.Helper()
	fromFile, err := sanitize.NewPatternDetectorFromFile(filepath.Join("..", "..", "..", "configs", "sensitive_patterns.yaml"))
	if err != nil {
		t.Fatalf("production rule set unavailable: %v", err)
	}
	return []req244RuleSet{
		{"built-in fallback (NewPatternDetector)", sanitize.NewPatternDetector()},
		{"configs/sensitive_patterns.yaml (production wiring)", fromFile},
	}
}

type req244Layer struct {
	name string
	hit  func(string) bool
}

func req244Layers(t *testing.T, detector *sanitize.PatternDetector) []req244Layer {
	t.Helper()
	return []req244Layer{
		{"detector", func(s string) bool {
			frags, err := detector.Detect(context.Background(), s)
			if err != nil {
				t.Fatalf("detector: %v", err)
			}
			return len(frags) > 0
		}},
		{"secretmask", func(s string) bool { return secretmask.MaskSecrets(s) != s }},
		{"handoff", func(s string) bool { return redactResumeSensitive(s) != s }},
	}
}

type req244Case struct{ name, text string }

// req244Cases is written by hand from published provider key formats, NOT
// harvested from the code tree. Deriving the denominator from the tree would
// inherit whichever layer this audit happened to look at first (audit 242).
func req244Cases() []req244Case {
	return []req244Case{
		{"openai_sk_plain", "sk-abcdefghij0123456789abcdefghij0123456789ab"},
		{"openai_sk_proj", "sk-proj-T3BlbkFJc2VjcmV0S2V5MTIzNDU2Nzg5MA"},
		{"anthropic_sk_ant", "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"},
		{"openrouter_sk_or", "sk-or-v1-abcdefghij0123456789abcdefghij0123456789"},
		{"aws_akia", "AKIAIOSFODNN7EXAMPLE"},
		{"github_ghp", "ghp_abcdefghij0123456789abcdefghij0123"},
		{"slack_xoxb", "xoxb-1234567890-abcdefghijklmnop"},
		{"google_aiza", "AIzaSyA1234567890abcdefghijklmnopqrstuv"},
		{"generic_ak", "ak-abcdefghij0123456789"},
		{"cn_phone", "call 13800138000"},
		{"email", "mail a@b.com"},
		{"id_card", "id 110101199003077894"},
		{"us_phone_e164", "call +14155552671"},
		{"uk_phone_e164", "call +442071838750"},
		{"us_ssn", "ssn 123-45-6789"},
		{"iban", "iban GB82WEST12345698765432"},
	}
}

var req244RequiredCases = []string{
	"openai_sk_plain", "openai_sk_proj", "anthropic_sk_ant", "openrouter_sk_or",
	"aws_akia", "github_ghp", "slack_xoxb", "google_aiza", "generic_ak",
	"cn_phone", "email", "id_card", "us_phone_e164", "uk_phone_e164",
	"us_ssn", "iban",
}

// Test244LayerProbeIsNotOneLayer proves the three probes measure different
// things. A one-directional control ("the detector fires") is not enough: if
// all three probes secretly reduced to the same check, the table would look
// like a three-layer comparison while being a one-layer measurement repeated.
//
// Every control below sits on a sample where the layers DISAGREE, because a
// sample every layer agrees on cannot observe a degenerate probe: on a
// three-way HIT, collapsing two probes into one still yields HIT, and on a
// three-way MISS, still MISS. An earlier revision of this test used aws_akia
// (all HIT) and cn_phone (secretmask+handoff both MISS) and passed with a
// deliberately degenerate handoff probe — the control was unobservable.
//
//	- aws_akia: all three HIT — the layers do agree where they overlap;
//	- cn_phone: detector HIT, both others MISS — divergence exists;
//	- openai_sk_proj: secretmask MISS but handoff HIT — pins each probe to its
//	  own rule set, and is the control that a degenerate probe actually trips.
func Test244LayerProbeIsNotOneLayer(t *testing.T) {
	for _, set := range req244RuleSets(t) {
		layers := req244Layers(t, set.detector)
		byName := map[string]req244Layer{}
		for _, l := range layers {
			byName[l.name] = l
		}

		where := "rule set [" + set.name + "]: "

		if !byName["detector"].hit("AKIAIOSFODNN7EXAMPLE") ||
			!byName["secretmask"].hit("AKIAIOSFODNN7EXAMPLE") ||
			!byName["handoff"].hit("AKIAIOSFODNN7EXAMPLE") {
			t.Fatalf("%scontrol failed: aws_akia must be seen by all three layers", where)
		}

		if !byName["detector"].hit("call 13800138000") {
			t.Fatalf("%scontrol failed: detector must see cn_phone", where)
		}
		if byName["secretmask"].hit("call 13800138000") {
			t.Fatalf("%scontrol failed: secretmask is expected NOT to see a bare phone number; "+
				"if it now does, the layers converged and this test's premise needs revisiting", where)
		}
		if byName["handoff"].hit("call 13800138000") {
			t.Fatalf("%scontrol failed: handoff is expected NOT to see a bare phone number; "+
				"if it now does, the layers converged and this test's premise needs revisiting", where)
		}

		// The discriminating pair: this sample is invisible to secretmask and
		// visible to handoff, so swapping one probe for the other is observable.
		const proj = "sk-proj-T3BlbkFJc2VjcmV0S2V5MTIzNDU2Nzg5MA"
		if byName["secretmask"].hit(proj) {
			t.Fatalf("%scontrol failed: secretmask is expected NOT to see openai_sk_proj", where)
		}
		if !byName["handoff"].hit(proj) {
			t.Fatalf("%scontrol failed: handoff must see openai_sk_proj — it is the only layer "+
				"covering the sk- family with hyphens, and this is the control that catches a "+
				"handoff probe that has silently collapsed onto the secretmask probe", where)
		}
	}
}

// Test244WideningRuleShrinksGap is the discriminating control for the whole
// test: a MISS must be a property of the rule set, not an artefact of the
// probe.
//
// It runs a locally-widened rule set — the detector's own shape with the
// character class extended to include '-' — and asserts the gap measurably
// shrinks. If this test could pass while every probe returned MISS, the table
// in Test244RedactionLayerRuleSurface would be unreadable.
func Test244WideningRuleShrinksGap(t *testing.T) {
	const proj = "sk-proj-T3BlbkFJc2VjcmV0S2V5MTIzNDU2Nzg5MA"

	// The production shape, copied as a literal rather than derived from the
	// detector: sharing the constant with the expectation would let a rule
	// change move the ruler and the reading at the same time.
	narrow := regexp.MustCompile(`(sk|ak)-[a-zA-Z0-9]{16,}`)
	wide := regexp.MustCompile(`(sk|ak)-[a-zA-Z0-9_-]{16,}`)

	if narrow.MatchString(proj) {
		t.Fatalf("premise broken: the narrow rule is expected NOT to match %q", proj)
	}
	if !wide.MatchString(proj) {
		t.Fatalf("control failed: the widened rule is expected to match %q", proj)
	}
	// Asserted for EVERY rule set the gateway can run with, not just one.
	// The point of the control is that the gap is a fact about the rules;
	// pinning it to a single rule set would let the other one drift unnoticed.
	for _, set := range req244RuleSets(t) {
		if req244Layers(t, set.detector)[0].hit(proj) {
			t.Fatalf("control failed: rule set [%s] is expected to MISS openai_sk_proj; "+
				"if it now matches, the gap in the table is stale and pending decision 106 "+
				"needs revisiting", set.name)
		}
	}
}
