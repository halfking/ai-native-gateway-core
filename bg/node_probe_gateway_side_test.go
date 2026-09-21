// bg/node_probe_gateway_side_test.go — 2026-09-17 incident regression tests.
//
// Incident: a dev gateway instance (252 llmgo-252-dev) sharing the production
// database with a mismatched LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY decrypted
// every legacy-envelope credential to "cannot decrypt: unknown format" →
// probeDirect returned endpoint_build → updateBindingAvailability wrote
// credential_model_bindings.available=FALSE (probe_endpoint_build, 5min
// cooldown) on healthy production credentials, fighting the well-configured
// instances' successful probes every cycle. The four credentials the operator
// observed (apigpt "gpt key", apiclaude 130dao-cache/nocahce-1x, hzx-2,
// minimax-prod-v2) oscillated available → 5min cooldown → recover → fail
// for weeks.
//
// These tests pin the three guards that make a misconfigured instance inert:
//
//  1. updateBindingAvailability refuses the shared-state write for
//     gateway-side errCodes (no UPDATE is issued at all);
//  2. the tick/sync paths additionally skip updateObservedState;
//  3. the instance-level decrypt circuit pauses drainDue after N
//     consecutive decrypt failures and half-opens after the cooldown.
package bg

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIsGatewaySideProbeError(t *testing.T) {
	cases := []struct {
		code string
		want bool
	}{
		{"endpoint_build", true},
		{"request_build", true},
		{"probe_endpoint_build", true},
		{"probe_request_build", true},
		// Upstream health signals must NOT be classified gateway-side.
		{"http_401", false},
		{"http_500", false},
		{"timeout", false},
		{"network_error", false},
		{"connection_error", false},
		{"dns_error", false},
		{"none", false},
		{"", false},
		{"gateway_probe_failed", false},
	}
	for _, tc := range cases {
		if got := isGatewaySideProbeError(tc.code); got != tc.want {
			t.Errorf("isGatewaySideProbeError(%q) = %v, want %v", tc.code, got, tc.want)
		}
	}
}

// TestUpdateBindingAvailabilityRefusesGatewaySideError pins the shared-state
// guard in source (same style as node_probe_manual_gate_test.go — w.db is a
// concrete *pgxpool.Pool so the write path itself cannot be pgxmock'd):
// updateBindingAvailability must consult isGatewaySideProbeError and return
// BEFORE the UPDATE when available=false, so a misconfigured instance sharing
// the production DB can never flip credential_model_bindings.available.
func TestUpdateBindingAvailabilityRefusesGatewaySideError(t *testing.T) {
	body := sourceBetween(t, nodeProbeSource(t),
		"func (w *NodeProbeWorker) updateBindingAvailability",
		"func (w *NodeProbeWorker) updateCredentialHealth")

	guard := sourceBetween(t, body,
		"if !available && isGatewaySideProbeError(reason) && !w.credentialSpecificDecryptFailure(errDetail) {",
		"if available {")
	if !strings.Contains(guard, "return") {
		t.Errorf("guard must return before any SQL for gateway-side errCodes")
	}
	if !strings.Contains(body, "!w.credentialSpecificDecryptFailure(errDetail)") {
		t.Errorf("guard must carry the R40 credential-specific decrypt exemption (R39 §三#3)")
	}
	if !strings.Contains(body, "available = FALSE") {
		t.Errorf("the unavailability UPDATE must still exist for genuine upstream errors")
	}
}

// TestDecryptCircuitTripsAndHalfOpens pins the instance-level decrypt circuit:
// below the threshold nothing is blocked; at/above the threshold drainDue is
// paused; after the cooldown the circuit half-opens exactly one pick.
func TestDecryptCircuitTripsAndHalfOpens(t *testing.T) {
	w := &NodeProbeWorker{}

	if w.decryptCircuitTripped() {
		t.Fatalf("circuit must be closed with zero failures")
	}

	for i := 0; i < decryptTripThreshold; i++ {
		w.recordDecryptFailure()
	}
	// First tripped read logs and blocks.
	if !w.decryptCircuitTripped() {
		t.Fatalf("circuit must be open at threshold=%d", decryptTripThreshold)
	}
	// Still blocked immediately after (cooldown not elapsed).
	if !w.decryptCircuitTripped() {
		t.Fatalf("circuit must stay open within the cooldown")
	}

	// Simulate cooldown elapse by rewinding the trip stamp.
	past := time.Now().Add(-decryptTripCooldown - time.Second).Unix()
	w.decryptTrippedAt.Store(past)
	if w.decryptCircuitTripped() {
		t.Fatalf("circuit must half-open after the cooldown")
	}
	// Half-open grants ONE pick: the stamp was bumped, so the next read is
	// blocked again until the next cooldown elapses.
	if !w.decryptCircuitTripped() {
		t.Fatalf("half-open must allow exactly one pick")
	}

	// A successful decrypt resets everything.
	w.resetDecryptFailures()
	if w.decryptFailures.Load() != 0 || w.decryptTrippedAt.Load() != 0 {
		t.Fatalf("resetDecryptFailures must clear counter and trip stamp")
	}
	if w.decryptCircuitTripped() {
		t.Fatalf("circuit must be closed after a successful decrypt")
	}
}

// TestDrainDueHonorsDecryptCircuit pins that a tripped circuit makes drainDue
// a no-op — no cycle() call, no database access.
func TestDrainDueHonorsDecryptCircuit(t *testing.T) {
	w := &NodeProbeWorker{}
	for i := 0; i < decryptTripThreshold; i++ {
		w.recordDecryptFailure()
	}
	if !w.decryptCircuitTripped() {
		t.Fatalf("precondition: circuit must be open")
	}
	// drainDue with a tripped circuit must not touch the DB (w.db is nil
	// here — a pick attempt would panic on nil pool inside pickDueAtomically).
	w.drainDue(context.Background()) // must return without picking
}

// TestRunOneGatewaySideSkipsSharedStateWrites is a source-scan pin (same
// style as node_probe_manual_gate_test.go): the tick path's failure branch
// and the failure ladder must consult isGatewaySideProbeError so a
// gateway-side error neither writes availability nor advances
// consecutive_failures.
func TestRunOneGatewaySideSkipsSharedStateWrites(t *testing.T) {
	src := nodeProbeSource(t)

	tick := sourceBetween(t, src,
		"if !direct.ok {\n\t\tif isGatewaySideProbeError(direct.errCode) && !w.credentialSpecificDecryptFailure(direct.errDetail) {",
		"if w.invalidateCandidateCache != nil {\n\t\tw.invalidateCandidateCache(credID)")
	if !strings.Contains(tick, "not updating availability") {
		t.Errorf("tick path must log the skipped availability update for gateway-side errors")
	}

	sync := sourceBetween(t, src,
		"isGatewaySideProbeError(res.direct.errCode)",
		"w.updateURSMv2ProbeState(ctx, tenantID")
	if !strings.Contains(sync, "gateway-side direct probe error (sync)") {
		t.Errorf("sync path must log the skipped availability update for gateway-side errors")
	}
	if !strings.Contains(sync, "w.updateBindingAvailability(ctx, j.credID, j.model, false, res.direct.errCode, 1, res.direct.errDetail)") ||
		!strings.Contains(sync, "w.updateObservedState(ctx, j.credID, j.model, false, res.direct.errCode") {
		t.Errorf("sync path must keep the genuine-upstream-error write branch")
	}
	if !strings.Contains(sync, "!w.credentialSpecificDecryptFailure(res.direct.errDetail)") {
		t.Errorf("sync path suppression must carry the R40 credential-specific decrypt exemption (R39 §三#3)")
	}

	ladder := sourceBetween(t, src,
		"gatewaySide := isGatewaySideProbeError(direct.errCode)",
		"// Persist audit row.")
	if !strings.Contains(ladder, "CASE WHEN $10::boolean THEN node_probe_state.consecutive_failures ELSE $3 END") {
		t.Errorf("failure ladder must not advance consecutive_failures for gateway-side errors")
	}
	if !strings.Contains(ladder, "backoff = nodeProbeGatewaySideRetryDelay") {
		t.Errorf("failure ladder must pace gateway-side retries at the fixed delay")
	}
}

// TestResolveDirectTargetDecryptFeedsCircuit pins the counter wiring in the
// source: the DecryptAny error branch must record a failure and the success
// return must reset it.
func TestResolveDirectTargetDecryptFeedsCircuit(t *testing.T) {
	src := nodeProbeSource(t)
	body := sourceBetween(t, src,
		"pt, _, err := secret.DecryptAny(s, w.keyring, w.encKey)",
		"func directProbeEndpoint")
	if !strings.Contains(body, "w.recordDecryptFailure()") {
		t.Errorf("DecryptAny error branch must call recordDecryptFailure")
	}
	if !strings.Contains(body, "w.resetDecryptFailures()") {
		t.Errorf("successful decrypt must call resetDecryptFailures")
	}
}

// TestURSMv2FailureWriteCarriesGatewaySideGuard (R42, 2026-09-18 audit) pins
// that all three probe paths gate their shared-URSM failure write with the
// same predicate as the binding guards. The URSM v2 node key lives in Redis
// shared cluster-wide; an unconditional failure write from a misconfigured
// instance (bad encryption key → endpoint_build) locks the node out of
// routing everywhere — the R39 P1-1 poisoning channel via a second surface.
// Success writes stay unconditional (recovery signal, 2026-08-18 fix).
func TestURSMv2FailureWriteCarriesGatewaySideGuard(t *testing.T) {
	probeSrc := nodeProbeSource(t)

	// The predicate helper itself must carry both halves: gateway-side
	// classification AND the R40 credential-specific decrypt exemption.
	helper := sourceBetween(t, probeSrc,
		"func (w *NodeProbeWorker) ursmFailureWritable",
		"func (w *NodeProbeWorker) updateURSMv2ProbeState")
	if !strings.Contains(helper, "!isGatewaySideProbeError(errCode)") ||
		!strings.Contains(helper, "w.credentialSpecificDecryptFailure(errDetail)") {
		t.Errorf("ursmFailureWritable must combine gateway-side classification with the R40 decrypt exemption")
	}

	// Tick path (runOne): the URSM write sits inside the ok-or-writable gate.
	runOne := sourceBetween(t, probeSrc,
		"func (w *NodeProbeWorker) runOne",
		"func isMissingBindingErr")
	if !strings.Contains(runOne, "if direct.ok || w.ursmFailureWritable(direct.errCode, direct.errDetail) {\n\t\tw.updateURSMv2ProbeState(") {
		t.Errorf("runOne must gate the URSM v2 write with ok || ursmFailureWritable")
	}

	// Sync path (ProbeSync): same gate on the res.direct fields.
	if !strings.Contains(probeSrc, "if res.direct.ok || w.ursmFailureWritable(res.direct.errCode, res.direct.errDetail) {\n\t\t\t\tw.updateURSMv2ProbeState(") {
		t.Errorf("ProbeSync must gate the URSM v2 write with ok || ursmFailureWritable")
	}

	// Queue path (probe_service.go): same gate via the worker handle.
	qSrc, err := os.ReadFile("probe_service.go")
	if err != nil {
		t.Fatalf("read probe_service.go: %v", err)
	}
	if !strings.Contains(string(qSrc), "if direct.ok || s.worker.ursmFailureWritable(direct.errCode, direct.errDetail) {\n\t\ts.worker.updateURSMv2ProbeState(") {
		t.Errorf("probe_service queue path must gate the URSM v2 write with ok || ursmFailureWritable")
	}
}
