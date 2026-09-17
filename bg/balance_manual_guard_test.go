package bg

import (
	"os"
	"strings"
	"testing"
)

// TestManualBalanceGuardPredicateLockstep (R42, 2026-09-18 audit) pins the
// migration-721 manual-protection predicate to its consumption sites. The
// predicate used to live as two hand-copied SQL literals ("keep in sync"
// comments only) and the floor guard's write-time UPDATE carried no
// predicate at all — a probe that passed the candidate SELECT still clobber
// -ed a manual value patched while the probe was in flight (TOCTOU). Every
// automatic balance write must consume the shared constant; a raw 24h
// literal in a writer file means the predicate forked again.
func TestManualBalanceGuardPredicateLockstep(t *testing.T) {
	constDef, err := os.ReadFile("balance_manual_guard.go")
	if err != nil {
		t.Fatalf("read balance_manual_guard.go: %v", err)
	}
	if !strings.Contains(string(constDef), "manualBalanceGuardSQL = ") {
		t.Fatalf("shared predicate constant missing from balance_manual_guard.go")
	}
	// The predicate must actually protect against manual rows.
	if !strings.Contains(string(constDef), `'manual'`) || !strings.Contains(string(constDef), "24 hours") {
		t.Fatalf("manualBalanceGuardSQL lost its manual-24h semantics: %s", string(constDef))
	}

	floorSrc, err := os.ReadFile("balance_floor_guard.go")
	if err != nil {
		t.Fatalf("read balance_floor_guard.go: %v", err)
	}
	// Pass A SELECT + success UPDATE + failure-error stamp = 3 consumers.
	if got := strings.Count(string(floorSrc), "+manualBalanceGuardSQL+"); got != 3 {
		t.Errorf("balance_floor_guard.go must consume manualBalanceGuardSQL exactly 3 times (Pass A SELECT, success UPDATE, failure stamp), got %d", got)
	}

	probeSrc, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read credential_probe_v2.go: %v", err)
	}
	// Success UPDATE + failure-error stamp = 2 consumers.
	if got := strings.Count(string(probeSrc), "+manualBalanceGuardSQL"); got != 2 {
		t.Errorf("credential_probe_v2.go must consume manualBalanceGuardSQL exactly 2 times (success UPDATE, failure stamp), got %d", got)
	}

	// The predicate must not fork back into hand-copied literals.
	for name, src := range map[string]string{
		"balance_floor_guard.go": string(floorSrc),
		"credential_probe_v2.go": string(probeSrc),
	} {
		if strings.Contains(strings.ToLower(src), "interval '24 hours'") {
			t.Errorf("%s still contains a hand-copied 24h predicate literal — consume manualBalanceGuardSQL instead", name)
		}
	}
}
