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
	// 2026-09-25 (对健康节点零探测): last_err_code/last_err_detail are written
	// as SQL NULL literals on the parked row — parking the gateway round's
	// code kept direct-verified rows out of the healthy-parked shape and made
	// the stale-state reconciler re-probe a healthy node every tick. The
	// gateway anomaly stays visible via last_gateway_ok = $3. (R65 audit A:
	// NULL literals, not untyped nil params — the pool runs simple protocol.)
	if !strings.Contains(runOneBody, "last_gateway_ok = $3") {
		t.Fatalf("runOne success branch no longer records the actual gateway outcome " +
			"(last_gateway_ok must stay parameterized so a direct-only recovery with a " +
			"gateway anomaly stays operator-visible)")
	}
	hardcoded := regexp.MustCompile(`(?i)last_gateway_ok\s*=\s*TRUE`)
	if hardcoded.MatchString(runOneBody) {
		t.Fatalf("runOne still hardcodes last_gateway_ok = TRUE in the success branch — " +
			"a direct-only recovery with a failing gateway round would be invisible")
	}
	if !strings.Contains(runOneBody, "last_err_code = NULL") ||
		!strings.Contains(runOneBody, "last_err_detail = NULL") {
		t.Fatalf("runOne success branch parks gateway err code/detail into the row " +
			"(must write last_err_code/last_err_detail as SQL NULL so the row lands in " +
			"the healthy-parked shape — see nodeProbeHealthyParkedSQL / " +
			"reconcileStaleNodeProbeStateSQL)")
	}

	// probe_service.go must obey the same doctrine (2026-09-25): the unified
	// queue path regressed to the composite verdict and laddered healthy
	// nodes whenever the gateway round failed.
	psSrc, err := os.ReadFile("probe_service.go")
	if err != nil {
		t.Fatalf("read probe_service.go: %v", err)
	}
	if strings.Contains(string(psSrc), "success := direct.ok && gw.ok") {
		t.Fatalf("ProbeService.Run regressed to the composite direct&&gateway success " +
			"verdict — a direct-verified healthy node must settle terminal even when " +
			"the gateway round failed (2026-09-25 对健康节点零探测)")
	}
	if strings.Contains(string(psSrc), "last_gateway_ok = TRUE") {
		t.Fatalf("mirrorNodeProbeState hardcoded last_gateway_ok = TRUE again — " +
			"the parked row must carry the real gateway outcome (gw.ok)")
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
