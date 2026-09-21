package bg

import (
	"os"
	"strings"
	"testing"
)

// R39 (2026-09-17) regression pins: the durable probe-queue path must carry
// the same gateway-side isolation as the legacy runOne ladder. 20fb4c7a4 put
// the guard on the legacy tick path only; a wrong-key instance could still
// poison shared node_probe_state via the queue path (mirrorNodeProbeState
// advanced consecutive_failures unconditionally and processBatch claimed
// work with the decrypt circuit tripped). Source-scan style, same as
// node_probe_gateway_side_test.go.

func probeServiceSource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("probe_service.go")
	if err != nil {
		t.Fatalf("read probe_service.go: %v", err)
	}
	return string(src)
}

func probeQueueWorkerSource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("probe_queue_worker.go")
	if err != nil {
		t.Fatalf("read probe_queue_worker.go: %v", err)
	}
	return string(src)
}

func TestMirrorNodeProbeStateGatewaySideDoesNotAdvanceCF(t *testing.T) {
	src := probeServiceSource(t)
	const fn = "func (w *NodeProbeWorker) mirrorNodeProbeState("
	start := strings.Index(src, fn)
	if start < 0 {
		t.Fatalf("mirrorNodeProbeState not found")
	}
	end := strings.Index(src[start:], "\nfunc ")
	if end < 0 {
		t.Fatalf("cannot find end of mirrorNodeProbeState")
	}
	body := src[start : start+end]

	if !strings.Contains(body, "gatewaySide = isGatewaySideProbeError(*c)") {
		t.Errorf("mirrorNodeProbeState must derive the gateway-side flag from firstErrCode")
	}
	if !strings.Contains(body, "CASE WHEN $10::boolean THEN node_probe_state.consecutive_failures ELSE $3 END") {
		t.Errorf("mirrorNodeProbeState failure branch must not advance consecutive_failures for gateway-side errors")
	}
}

func TestQueuePathPacesGatewaySideAtFixedDelay(t *testing.T) {
	src := probeServiceSource(t)
	const marker = "if isGatewaySideProbeError(errCode) {\n\t\tbackoff = nodeProbeGatewaySideRetryDelay\n\t}"
	if !strings.Contains(src, marker) {
		t.Errorf("queue path must pace gateway-side retries at the fixed gateway-side delay, overriding ladder and featured multiplier")
	}
	// The override must sit after the featured-multiplier block so the fixed
	// delay cannot be scaled back down.
	override := strings.Index(src, marker)
	featured := strings.Index(src, "probe.featured_backoff_multiplier")
	if override < 0 || featured < 0 || override < featured {
		t.Errorf("gateway-side delay override must come after the featured-multiplier block")
	}
}

func TestProcessBatchGatesOnDecryptCircuit(t *testing.T) {
	src := probeQueueWorkerSource(t)
	const fn = "func (w *ProbeQueueWorker) processBatch("
	start := strings.Index(src, fn)
	if start < 0 {
		t.Fatalf("processBatch not found")
	}
	end := strings.Index(src[start:], "\nfunc ")
	if end < 0 {
		t.Fatalf("cannot find end of processBatch")
	}
	body := src[start : start+end]

	circuit := strings.Index(body, "decryptCircuitTripped()")
	claim := strings.Index(body, "w.cfg.Queue.Claim(")
	if circuit < 0 {
		t.Fatalf("processBatch must consult the decrypt circuit before claiming work")
	}
	if claim < 0 {
		t.Fatalf("processBatch must still claim work when the circuit is closed")
	}
	if circuit > claim {
		t.Errorf("decrypt circuit check must precede Queue.Claim")
	}
}
