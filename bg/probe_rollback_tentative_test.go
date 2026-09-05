package bg

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestProbeTentativeRevertAfter(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"empty defaults to 15m", "", 15 * time.Minute},
		{"go duration", "30m", 30 * time.Minute},
		{"bare seconds", "900", 15 * time.Minute},
		{"zero disables", "0", 0},
		{"off disables", "off", 0},
		{"false disables", "false", 0},
		{"disabled disables", "disabled", 0},
		{"OFF disables case-insensitive", "OFF", 0},
		{"negative falls back to default", "-5m", 15 * time.Minute},
		{"garbage falls back to default", "soon", 15 * time.Minute},
	}
	const key = "LLM_GATEWAY_PROBE_TENTATIVE_REVERT_AFTER"
	t.Setenv(key, "") // ensure defined for all cases
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(key, tc.env)
			if got := probeTentativeRevertAfter(); got != tc.want {
				t.Fatalf("probeTentativeRevertAfter() = %v, want %v (env=%q)", got, tc.want, tc.env)
			}
		})
	}
}

// TestProbeSyncMarksTentativeRestore pins the smart-fallback marking contract
// (需求 6 bullet 6): the ProbeSync success branch must call
// MarkTentativeRestore behind the revert-window guard, AFTER the availability
// restore so the retry still sees the node, and BEFORE the gateway round.
// String-level contract test in the style of probe_rollback_contract_test.go
// (SQL behavior is covered by migration 351 + the rollback worker's one-shot
// WHERE guard).
func TestProbeSyncMarksTentativeRestore(t *testing.T) {
	source, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatalf("read node_probe.go: %v", err)
	}
	text := string(source)
	start := strings.Index(text, "func (w *NodeProbeWorker) ProbeSync")
	if start < 0 {
		t.Fatal("ProbeSync not found")
	}
	body := text[start:]
	end := strings.Index(body, "\nfunc ")
	if end >= 0 {
		body = body[:end]
	}
	markIdx := strings.Index(body, "MarkTentativeRestore(markCtx")
	if markIdx < 0 {
		t.Fatal("ProbeSync success branch must call MarkTentativeRestore (smart-fallback marking entry)")
	}
	availIdx := strings.Index(body, "updateBindingAvailability(ctx, j.credID, j.model, true, \"\")")
	if availIdx < 0 {
		t.Fatal("ProbeSync success branch must restore availability")
	}
	gwIdx := strings.Index(body, "res.gateway = w.probeGateway(ctx")
	if gwIdx < 0 {
		t.Fatal("ProbeSync success branch must run the gateway round")
	}
	if !(availIdx < markIdx && markIdx < gwIdx) {
		t.Fatalf("marking order broken: availability=%d, mark=%d, gateway=%d — mark must sit between restore and gateway round", availIdx, markIdx, gwIdx)
	}
	if !strings.Contains(body, "probeTentativeRevertAfter()") {
		t.Fatal("marking must be guarded by the revert-window env (0/off disables)")
	}
}
