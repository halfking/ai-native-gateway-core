package prefix

import (
	"os"
	"strings"
	"testing"
)

// design_intent_test.go encodes the NON-NEGOTIABLE invariants of the prefix
// package. These guard against a future edit that silently breaks cache hit
// rates or — worse — changes conversation semantics.
//
// Pattern follows security/armor/design_intent_test.go.

// TestNoConversationReorder_Documented: the most dangerous regression would
// be reordering turns WITHIN history (changes meaning). The package doc must
// call this out.
func TestNoConversationReorder_Documented(t *testing.T) {
	src := mustRead(t, "prefix.go")
	if !strings.Contains(src, "ORIGINAL RELATIVE ORDER is preserved") {
		t.Error("prefix.go must document that within-class relative order is preserved")
	}
}

// TestConservativePassthrough_Documented: Stabilize MUST pass through
// unrecognized bodies unchanged (never break a request). Documented invariant.
func TestConservativePassthrough_Documented(t *testing.T) {
	src := mustRead(t, "prefix.go")
	if !strings.Contains(src, "returns the ORIGINAL bytes unchanged") {
		t.Error("prefix.go must document the conservative pass-through rule")
	}
}

// TestIdempotent_Documented: relay calls Stabilize every request; it must be
// idempotent (byte-stable on already-stable input).
func TestIdempotent_Documented(t *testing.T) {
	src := mustRead(t, "prefix.go")
	if !strings.Contains(src, "idempotent") {
		t.Error("prefix.go must document that Stabilize is idempotent")
	}
}

// TestNoCacheControlInjection_Documented: this package does NOT inject
// cache_control markers (that's sessions.CacheInjector). The boundary must be
// explicit so nobody duplicates the logic.
func TestNoCacheControlInjection_Documented(t *testing.T) {
	src := mustRead(t, "prefix.go")
	if !strings.Contains(src, "does NOT inject cache_control") {
		t.Error("prefix.go must document that it does NOT inject cache_control")
	}
}

// TestStabilityOrder_CommentPresent: the SystemClass < ToolClass < HistoryClass
// < TailClass ordering is the single most important constant. Guard its doc.
func TestStabilityOrder_CommentPresent(t *testing.T) {
	src := mustRead(t, "prefix.go")
	if !strings.Contains(src, "Lower numbers are MORE stable") {
		t.Error("prefix.go must document the stability ordering invariant")
	}
}

// TestReport_NoSecrets_Documented: Report is logged; it must never contain
// prompt content.
func TestReport_NoSecrets_Documented(t *testing.T) {
	src := mustRead(t, "prefix.go")
	if !strings.Contains(src, "never contain prompt content") {
		t.Error("prefix.go must document that Report never contains prompt content")
	}
}

// TestDomainBoundary_Documented: OWNS / does NOT own list.
func TestDomainBoundary_Documented(t *testing.T) {
	src := mustRead(t, "prefix.go")
	if !strings.Contains(src, "OWNS") || !strings.Contains(src, "does NOT own") {
		t.Error("prefix.go must document the domain boundary (OWNS / does NOT own)")
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
