package armor

import (
	"os"
	"strings"
	"testing"
)

// design_intent_test.go encodes the NON-NEGOTIABLE design invariants of the
// armor package. These are NOT unit tests of behavior — they are guardrails
// that fail the build if a future edit silently weakens the security posture.
//
// Pattern follows maas/design_intent_test.go (read source, assert substring).

// TestV1ObserveOnly_Documented enforces that judge.go carries the v1 hard
// rule in a comment so future maintainers cannot "fix" it by accident.
func TestV1ObserveOnly_Documented(t *testing.T) {
	src := mustRead(t, "judge.go")
	if !strings.Contains(src, "v1 hard rule") && !strings.Contains(src, "v1 ALWAYS") {
		t.Error("judge.go must document the v1 observe-only rule in a comment")
	}
}

// TestNormalizeForcesObserve_CodePresent: the Normalize function MUST contain
// an explicit assignment p.Mode = ModeObserve (not a conditional). If someone
// refactors this away, the test breaks.
func TestNormalizeForcesObserve_CodePresent(t *testing.T) {
	src := mustRead(t, "policy.go")
	if !strings.Contains(src, "p.Mode = ModeObserve") {
		t.Error("policy.go Normalize must explicitly force p.Mode = ModeObserve (v1 hard rule)")
	}
}

// TestResolveDecision_Downgrade_CodePresent: the resolveDecision function
// must contain the observe-mode downgrade branch.
func TestResolveDecision_Downgrade_CodePresent(t *testing.T) {
	src := mustRead(t, "judge.go")
	if !strings.Contains(src, "return DecisionWarn // observe") {
		t.Error("judge.go resolveDecision must contain the observe-mode downgrade comment")
	}
}

// TestApiKey_NeverLogged: the httpJudge struct must document that the API key
// is never logged, and the code must never format the key into a string.
func TestApiKey_NeverLogged(t *testing.T) {
	judgeSrc := mustRead(t, "judge.go")
	// CRITICAL: never format h.apiKey into a string (leak risk)
	if strings.Contains(judgeSrc, `h.apiKey")`) || strings.Contains(judgeSrc, "Sprintf(h.apiKey)") {
		t.Error("judge.go: apiKey must not be formatted into a string (leak risk)")
	}
	// Positive: documented as never logged
	if !strings.Contains(judgeSrc, "never logged") && !strings.Contains(judgeSrc, "MUST NOT be logged") {
		t.Error("judge.go must document that apiKey is never logged")
	}
}

// TestDecisionStrings_AuditStable: the Decision.String() values are part of
// the audit event schema (error_kind=armor_warn etc.). Document the contract.
func TestDecisionStrings_AuditStable(t *testing.T) {
	src := mustRead(t, "judge.go")
	if !strings.Contains(src, "NEVER change") && !strings.Contains(src, "depend on them verbatim") {
		t.Error("judge.go must document that Decision.String() values are audit-stable")
	}
}

// TestCheckTypes_AuditStable: check-type strings appear in audit event tags
// (armor_warn:prompt_inject). They must be lowercase + underscore.
func TestCheckTypes_AuditStable(t *testing.T) {
	src := mustRead(t, "policy.go")
	if !strings.Contains(src, CheckPromptInject) {
		t.Error("policy.go must reference CheckPromptInject")
	}
	for _, c := range AllChecks {
		if c != strings.ToLower(c) {
			t.Errorf("check type %q must be lowercase for audit stability", c)
		}
	}
}

// TestDomainBoundary_Documented: the package doc comment must state what
// armor OWNS vs does NOT own (so future contributors don't bleed session/
// routing/audit logic in here).
func TestDomainBoundary_Documented(t *testing.T) {
	src := mustRead(t, "judge.go")
	if !strings.Contains(src, "armor OWNS") || !strings.Contains(src, "armor does NOT own") {
		t.Error("judge.go package doc must state armor's domain boundary (OWNS / does NOT own)")
	}
}

func mustRead(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("cannot read %s: %v", name, err)
	}
	return string(b)
}
