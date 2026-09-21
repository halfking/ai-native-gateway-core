package bg

import (
	"os"
	"strings"
	"testing"
)

// TestDefaultProbeScannerEnvOverride pins the env-driven interval
// override so operator-tunable cadence changes don't accidentally
// regress to a hard-coded value during a refactor.
func TestDefaultProbeScannerEnvOverride(t *testing.T) {
	const want = "LLM_GATEWAY_DEFAULT_PROBE_SCAN_INTERVAL"
	src, err := os.ReadFile("default_probe_scanner.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if !strings.Contains(string(src), want) {
		t.Errorf("default_probe_scanner.go is missing env override %q", want)
	}
}

// TestDefaultProbeScannerSkipGuards pins the candidate SQL: it MUST
// exclude manual-pinned credentials (source='manual') and MUST require
// the credential to have at least one routable cmb binding. Without
// the cmb EXISTS guard the scanner would loop forever calling the
// helper on freshly-created credentials whose first /v1/models fetch
// hasn't completed yet; without the source<>manual guard the scanner
// would stomp operator pins.
func TestDefaultProbeScannerSkipGuards(t *testing.T) {
	src, err := os.ReadFile("default_probe_scanner.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"c.status = 'active'",
		"c.lifecycle_status = 'active'",
		"COALESCE(c.manual_disabled, FALSE) = FALSE",
		"COALESCE(p.manual_disabled, FALSE) = FALSE",
		"p.enabled = TRUE",
		// Operator pin MUST be preserved across scan passes.
		"COALESCE(c.default_probe_model_source, '') <> 'manual'",
		// Empty default_probe_model — the only eligible state.
		"c.default_probe_model IS NULL OR c.default_probe_model = ''",
		// Must have at least one routable cmb row, otherwise the helper
		// would loop forever on freshly-created creds whose first
		// discovery fetch hasn't completed.
		"EXISTS (",
		"credential_model_bindings cmb",
		"COALESCE(cmb.available, FALSE) = TRUE",
		"COALESCE(pm.available, FALSE) = TRUE",
		// Bounded fan-out per tick — LIMIT guards DB load.
		"LIMIT 100",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("default probe scanner candidate SQL missing %q", want)
		}
	}
}

// TestDefaultProbeScannerReusesAutoFillHelper pins the wiring contract:
// the scanner MUST delegate the actual write to
// modelcatalog.AutoFillDefaultProbeModel (which already implements
// "skip when default_probe_model already non-empty" + "skip when
// source='manual'" atomically inside the UPDATE WHERE clause). A
// direct UPDATE here would duplicate the eligibility logic and is a
// known footgun for regressions.
func TestDefaultProbeScannerReusesAutoFillHelper(t *testing.T) {
	src, err := os.ReadFile("default_probe_scanner.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "modelcatalog.AutoFillDefaultProbeModel") {
		t.Fatal("default probe scanner must call modelcatalog.AutoFillDefaultProbeModel, " +
			"not write credentials.default_probe_model directly")
	}
	// Defensive: the scanner MUST NOT issue its own UPDATE on the
	// credentials.default_probe_model column. The helper is the single
	// writer and duplicating the path here would race with it.
	if strings.Contains(body, "UPDATE credentials\n\t\tSET default_probe_model") {
		t.Fatal("default probe scanner must not write default_probe_model directly; delegate to AutoFillDefaultProbeModel")
	}
}

// TestDefaultProbeScannerStopBeforeStartIsNoop exercises the documented
// defensive ordering: Stop() before Start() must be a silent no-op so
// test setups and partial boot paths can't panic on the
// already-closed-channel / nil-cancel paths.
//
// We pass nil for the db — Stop is a pure lifecycle operation and
// must not touch the database. The lifecycle lock short-circuits
// before the run goroutine is ever spawned.
func TestDefaultProbeScannerStopBeforeStartIsNoop(t *testing.T) {
	s := newDefaultProbeScannerWithInterval(nil, 1)
	s.Stop() // must not panic
}
