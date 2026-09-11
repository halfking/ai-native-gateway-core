package bg

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestNodeProbeLadderDrivenByDirectRoundOnly locks the 2026-09-10
// hzx-2/minimax-prod-v2 incident fix: the node-probe failure ladder and
// node_probe_runs.success must be driven by the DIRECT round only.
//
// Production evidence (154, 2026-09-10 12:56–13:01): cred 42
// MiniMax-M2.7-highspeed recorded direct 200 ×5 against the node's own
// decrypted key while every gateway round returned upstream 401
// invalid_key. The gateway round is a composite E2E request routed through
// this gateway by model name, so its failure is not always attributable to
// the probed node (stale key serving on the hot path, sibling credentials,
// gateway-wide faults). The old `success := direct.ok && gw.ok` laddered the
// healthy node to consecutive_failures=5 within minutes of a manual
// force-enable — the exact "强制启用后立刻被降级" symptom.
func TestNodeProbeLadderDrivenByDirectRoundOnly(t *testing.T) {
	contents, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatalf("read node_probe.go: %v", err)
	}
	src := string(contents)

	if strings.Contains(src, "success := direct.ok && gw.ok") {
		t.Fatalf("runOne still computes success from the composite direct&&gateway round — " +
			"see TestNodeProbeLadderDrivenByDirectRoundOnly: the failure ladder must be " +
			"direct-round only (2026-09-10 hzx-2 incident)")
	}

	// Scope the structural check to runOne so unrelated SQL strings elsewhere
	// in the file cannot false-positive (same approach as the recover()
	// regression test in credential_recovery_test.go).
	start := strings.Index(src, "func (w *NodeProbeWorker) runOne(")
	if start < 0 {
		t.Fatalf("could not locate runOne in node_probe.go")
	}
	end := strings.Index(src[start:], "\nfunc ")
	if end < 0 {
		t.Fatalf("could not locate end of runOne")
	}
	runOneBody := src[start : start+end]

	// The success branch must carry the REAL gateway outcome, not a hardcoded
	// TRUE/NULL pair that hides a composite failure behind a green ladder.
	if !strings.Contains(runOneBody, "last_gateway_ok = $3") ||
		!strings.Contains(runOneBody, "last_err_code = $4") ||
		!strings.Contains(runOneBody, "last_err_detail = $5") {
		t.Fatalf("runOne success branch no longer records the actual gateway outcome " +
			"(last_gateway_ok/last_err_code/last_err_detail must stay parameterized so a " +
			"direct-only recovery with a gateway anomaly stays operator-visible)")
	}
	hardcoded := regexp.MustCompile(`(?i)last_gateway_ok\s*=\s*TRUE`)
	if hardcoded.MatchString(runOneBody) {
		t.Fatalf("runOne still hardcodes last_gateway_ok = TRUE in the success branch — " +
			"a direct-only recovery with a failing gateway round would be invisible")
	}

	// probeRecovered (sync path) and emitSyncAudit must stay direct-only too:
	// these were already aligned with the doctrine and must not regress.
	if !strings.Contains(src, "func probeRecovered(direct nodeProbeRoundResult) bool {\n\treturn direct.ok\n}") {
		t.Fatalf("probeRecovered must keep direct-only recovery semantics")
	}
	if strings.Contains(src, "success := direct.ok && gw.ok // emitSyncAudit") {
		t.Fatalf("emitSyncAudit regressed to composite success")
	}
}
