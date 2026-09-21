// bg/node_probe_manual_gate_test.go — 2026-09-08 self-check audit.
//
// Pins the credential-level manual_disabled gates on the node-probe
// legacy paths so a kill-switch rollback
// (LLM_GATEWAY_PROBE_QUEUE_ENABLED=false) cannot resurrect the
// "probe manually-disabled credentials forever" behaviour:
//
//  1. pickDueAtomically (legacy tick scan) must apply the SAME
//     automaticProbeEligibilityExistsSQL gate the queue pump
//     (pumpDueStatesSQL) and ProbeQueue.Enqueue enforce — manual
//     probes stay possible via admin/queue, automatic scans must
//     skip retired credentials;
//  2. resolveDirectTarget must honour c.manual_disabled in BOTH the
//     strict gate and the loose evidence-collection retry — operator
//     disable intent is a human-intent gate and must never be
//     bypassed, while status/lifecycle gates stay relaxable in the
//     loose round (2026-08-18 glm-5.2 contract).
package bg

import (
	"os"
	"strings"
	"testing"
)

func nodeProbeSource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatalf("read node_probe.go: %v", err)
	}
	return string(src)
}

// sourceBetween returns the slice of src between the definitions of
// startMarker and endMarker (exclusive), for scoping assertions to a
// single function body.
func sourceBetween(t *testing.T, src, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(src, startMarker)
	if start < 0 {
		t.Fatalf("start marker %q not found", startMarker)
	}
	rest := src[start+len(startMarker):]
	end := strings.Index(rest, endMarker)
	if end < 0 {
		t.Fatalf("end marker %q not found after %q", endMarker, startMarker)
	}
	return rest[:end]
}

func TestPickDueAtomicallyAppliesAutomaticEligibilityGate(t *testing.T) {
	body := sourceBetween(t, nodeProbeSource(t),
		"func (w *NodeProbeWorker) pickDueAtomically",
		"func (w *NodeProbeWorker) runOne")
	for _, marker := range []string{
		"automaticProbeEligibilityExistsSQL(\"node_probe_state.credential_id\")",
		"FOR UPDATE SKIP LOCKED",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("pickDueAtomically missing %q — legacy tick scan would probe "+"manually-disabled/retired credentials", marker)
		}
	}
}

func TestResolveDirectTargetHonoursCredentialManualDisabled(t *testing.T) {
	body := sourceBetween(t, nodeProbeSource(t),
		"func (w *NodeProbeWorker) resolveDirectTarget",
		"func (w *NodeProbeWorker)")

	gate := "COALESCE(c.manual_disabled, FALSE) = FALSE"
	count := strings.Count(body, gate)
	if count < 2 {
		t.Fatalf("resolveDirectTarget must gate c.manual_disabled in BOTH the strict "+
			"query and the loose evidence-collection retry; found %d occurrences of %q", count, gate)
	}
	// Strict gate keeps its runtime status/lifecycle filters.
	if !strings.Contains(body, "c.status IN ('active', 'cooling', 'degraded')") ||
		!strings.Contains(body, "c.lifecycle_status = 'active'") {
		t.Fatal("resolveDirectTarget strict gate lost its status/lifecycle filters")
	}
	// Loose retry still relaxes status/lifecycle (2026-08-18 contract) but
	// keeps provider intent gates. Bound the slice to the loose query
	// statement itself (up to its Scan terminator) so later error-handling
	// prose cannot false-positive the status-filter check.
	looseStart := strings.LastIndex(body, "looseErr = w.db.QueryRow")
	if looseStart < 0 {
		t.Fatal("loose retry query not found in resolveDirectTarget")
	}
	looseEndRel := strings.Index(body[looseStart:], ", credID, model).Scan")
	if looseEndRel < 0 {
		t.Fatal("loose retry query Scan terminator not found")
	}
	loose := body[looseStart : looseStart+looseEndRel]
	if !strings.Contains(loose, gate) {
		t.Fatal("resolveDirectTarget loose retry lost the credential manual_disabled gate")
	}
	if strings.Contains(loose, "c.status IN") {
		t.Fatal("loose retry unexpectedly re-added status filters — 2026-08-18 evidence-collection contract broken")
	}
}

func TestPumpDueStatesSQLKeepsEligibilityGate(t *testing.T) {
	// pumpDueStatesSQL returns the RENDERED SQL (the eligibility helper is
	// invoked at call time), so the manual_disabled marker must appear
	// literally in its output.
	sql := pumpDueStatesSQL()
	if !strings.Contains(sql, "manual_disabled") {
		t.Fatal("pumpDueStatesSQL lost the automatic eligibility gate (manual_disabled marker missing)")
	}
	if !strings.Contains(sql, "nps.paused = FALSE") || !strings.Contains(sql, "nps.next_retry_at <= now()") {
		t.Fatal("pumpDueStatesSQL lost its due-row filters")
	}
}
