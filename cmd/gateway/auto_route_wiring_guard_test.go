package main

import (
	"os"
	"strings"
	"testing"
)

// TestAutoRouteWiringNotGatedOnDataPlaneMode guards the 2026-09-14 O5 fix:
// the autoroute decision engine (InitFeatureFlags → decider → SetAutoRoute)
// is REQUEST-PATH infrastructure and must be wired in BOTH full and
// data-plane modes. It originally lived inside `if !bgDataPlaneOnly { ... }`,
// so a permanent data-plane instance (LLM_GATEWAY_BG_MODE=data-plane, e.g.
// the 245 canary) had no decider; maybeResolveAuto took the decider==nil
// branch and rewrote model="auto" to autoFallbackModel()'s default —
// hardcoded "claude-sonnet-4.5", whose credentials are dead — producing a
// guaranteed 503 no_candidate for every auto request (audit O5, 2026-09-14).
func TestAutoRouteWiringNotGatedOnDataPlaneMode(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	const splitMarker = "CHECKPOINT: after concurrencyAutoScaleUp.Start, before NewIndex"
	splitIdx := strings.Index(text, splitMarker)
	if splitIdx < 0 {
		t.Fatal("main.go: concurrencyAutoScaleUp checkpoint marker missing; the O5 three-way split may have been refactored — re-evaluate this guard")
	}
	const wireCall = "chatHandler.SetAutoRoute(decider)"
	wireIdx := strings.Index(text[splitIdx:], wireCall)
	if wireIdx < 0 {
		t.Fatal("main.go: chatHandler.SetAutoRoute(decider) not found after the split marker")
	}
	engineRegion := text[splitIdx : splitIdx+wireIdx]

	// The decision engine must sit in an UNCONDITIONAL block: no data-plane
	// gate may open between the A/B split and SetAutoRoute. (A reopen for the
	// full-only writers is allowed only AFTER SetAutoRoute.)
	if strings.Contains(engineRegion, "if !bgDataPlaneOnly") {
		t.Fatal("O5 regression: the autoroute decision engine is gated behind !bgDataPlaneOnly again — data-plane instances would serve model=\"auto\" from the dead default")
	}

	// The engine block itself must be entered unconditionally: after the split
	// marker's closing brace there must be a bare `{` block (the A/B split).
	afterMarker := text[splitIdx+strings.Index(text[splitIdx:], "\n"):]
	if !strings.Contains(afterMarker[:600], "}\n\t\t{") && !strings.Contains(afterMarker[:800], "}\n\t\t{") {
		// The exact whitespace varies with gofmt; accept any closing brace
		// followed by a bare opening brace within the split comment window.
		if !strings.Contains(text[splitIdx:splitIdx+1200], "}\n") {
			t.Fatal("O5 regression: the A (full-only workers) / B (decision engine) block split is gone")
		}
	}

	// The full-only maintenance writers (trimmers / feedback analyzer) must
	// stay behind the data-plane gate: they open a NEW gate after SetAutoRoute.
	afterWire := text[splitIdx+wireIdx:]
	const trimmerMarker = "bg.NewAuditTrimmer(dbConn.Pool())"
	trimmerIdx := strings.Index(afterWire, trimmerMarker)
	if trimmerIdx < 0 {
		t.Fatal("main.go: AuditTrimmer wiring missing")
	}
	trailerRegion := afterWire[:trimmerIdx]
	lastGate := strings.LastIndex(trailerRegion, "if !bgDataPlaneOnly")
	if lastGate < 0 {
		t.Fatal("O5 regression: full-only maintenance writers (trimmers/feedback analyzer) are no longer gated on !bgDataPlaneOnly and would double-run on blue-green candidate instances")
	}
}
