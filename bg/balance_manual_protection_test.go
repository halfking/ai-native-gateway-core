package bg

import (
	"os"
	"strings"
	"testing"
)

// TestBalanceManualProtectionContent locks the exact SQL of the migration 721
// manual-balance protection window (2026-09-18). The window is a data-integrity
// contract, not an optimization: loosening it silently re-enables automatic
// overwrites of operator-calibrated balances. Any intentional change to the
// window must land here AND in balance_manual_protection.go together.
func TestBalanceManualProtectionContent(t *testing.T) {
	want := `NOT (
	COALESCE(balance_source, '') = 'manual'
	AND balance_last_checked_at > NOW() - INTERVAL '24 hours'
)`
	if ManualBalanceProtectionPredicate != want {
		t.Fatalf("ManualBalanceProtectionPredicate drifted:\n got: %q\nwant: %q", ManualBalanceProtectionPredicate, want)
	}
	for _, frag := range []string{
		"balance_source",          // must key on the provenance stamp...
		"'manual'",                // ...specifically the manual value
		"balance_last_checked_at", // ...and be freshness-gated
		"24 hours",                // protection window is 24h
		"NOT (",                   // rows inside the window must be EXCLUDED
	} {
		if !strings.Contains(ManualBalanceProtectionPredicate, frag) {
			t.Fatalf("ManualBalanceProtectionPredicate lost fragment %q", frag)
		}
	}
}

// TestBalanceManualProtectionIsWired locks the call sites: both automatic
// balance writers must reference the shared predicate, and must not carry a
// private inline copy that could drift out of sync (the R40 handoff flagged
// exactly this comment-referencing-comment sync as a standing risk).
func TestBalanceManualProtectionIsWired(t *testing.T) {
	wiredFiles := []string{
		"balance_floor_guard.go", // Pass A candidate SELECT
		"credential_probe_v2.go", // cycleAll balance UPDATE
	}
	for _, name := range wiredFiles {
		contents, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(contents)
		if !strings.Contains(src, "ManualBalanceProtectionPredicate") {
			t.Fatalf("%s no longer references ManualBalanceProtectionPredicate — the manual-balance "+
				"24h protection window was dropped or inlined; restore the shared predicate "+
				"(balance_manual_protection.go, migration 721)", name)
		}
		// A private inline copy would defeat the shared constant: the only
		// file allowed to spell out the window is balance_manual_protection.go.
		if strings.Contains(src, "'24 hours'") {
			t.Fatalf("%s contains a raw '24 hours' interval — automatic balance writers must use "+
				"ManualBalanceProtectionPredicate instead of an inline copy (migration 721)", name)
		}
	}
}

// TestManualBalancePredicateWriteTimeCoverage (R42, 2026-09-18 audit) extends
// TestBalanceManualProtectionIsWired beyond the two SELECT/UPDATE consumers:
// the floor guard's write-time UPDATE must also carry the predicate (TOCTOU —
// a probe that passed the candidate SELECT must not clobber a manual value
// patched while the probe was in flight), and both background failure paths
// stamp balance_error behind the same predicate. A raw 24h literal in a
// writer file means the predicate forked again.
func TestManualBalancePredicateWriteTimeCoverage(t *testing.T) {
	floorSrc, err := os.ReadFile("balance_floor_guard.go")
	if err != nil {
		t.Fatalf("read balance_floor_guard.go: %v", err)
	}
	// Pass A SELECT + success UPDATE + failure stamp = 3 consumers.
	if got := strings.Count(string(floorSrc), "+ManualBalanceProtectionPredicate+"); got != 3 {
		t.Errorf("balance_floor_guard.go must consume ManualBalanceProtectionPredicate exactly 3 times (Pass A SELECT, success UPDATE, failure stamp), got %d", got)
	}
	probeSrc, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read credential_probe_v2.go: %v", err)
	}
	// Success UPDATE + failure stamp = 2 consumers.
	if got := strings.Count(string(probeSrc), "+ManualBalanceProtectionPredicate"); got != 2 {
		t.Errorf("credential_probe_v2.go must consume ManualBalanceProtectionPredicate exactly 2 times (success UPDATE, failure stamp), got %d", got)
	}
	for name, src := range map[string]string{
		"balance_floor_guard.go": string(floorSrc),
		"credential_probe_v2.go": string(probeSrc),
	} {
		if strings.Contains(strings.ToLower(src), "interval '24 hours'") {
			t.Errorf("%s still contains a hand-copied 24h predicate literal — consume ManualBalanceProtectionPredicate instead", name)
		}
	}
}
