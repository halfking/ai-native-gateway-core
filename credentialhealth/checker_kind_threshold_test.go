package credentialhealth

import (
	"os"
	"strings"
	"testing"
)

// TestCheckerKindGradientDefaults pins the 2026-08-23 hzx-2 audit
// additions: DefaultCheckerConfig must include a per-error-kind gradient,
// and the gradient must define at least one row for each high-volume
// production kind. A test that fails on a missing row forces the policy
// table to stay in lock-step with the production incident log.
func TestCheckerKindGradientDefaults(t *testing.T) {
	src, err := os.ReadFile("checker.go")
	if err != nil {
		t.Fatalf("read checker source failed: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		// KindThreshold + resolveKindThreshold plumbing.
		"type KindThreshold",
		"resolveKindThreshold",
		// Gradient map lives on Checker.
		"kindThresholds",
		// defaultKindThresholds covers the post-2026-08-23 production kinds.
		"defaultKindThresholds",
		// The Checker must log the dominant kind so dashboards can group
		// degradations by root cause.
		"dominant_kind",
		// Cold-node active prober wiring.
		"ColdProber",
		"ColdNodeActiveProber",
		// Pre-threshold check: cold nodes with ColdProber wired run a real
		// probe instead of returning nil.
		"checker: cold-node probe",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("checker kind gradient missing %q", want)
		}
	}
}

// TestCheckerGradientTableContainsCriticalKinds ensures the per-kind
// policy table doesn't accidentally drop critical kinds during a
// refactor. A missing kind means a new production error class has
// no per-kind threshold and falls back to the global 80%/15min — which
// is exactly what the gradient was added to avoid.
func TestCheckerGradientTableContainsCriticalKinds(t *testing.T) {
	src, err := os.ReadFile("checker.go")
	if err != nil {
		t.Fatalf("read checker source failed: %v", err)
	}
	body := string(src)
	for _, kind := range []string{
		"timeout",
		"stream_timeout",
		"rate_limit",
		"upstream_context_loss",
		"upstream_down",
		"upstream_overloaded",
		"model_not_found",
		"model_deprecated",
		"unsupported_feature",
	} {
		// We match the table key by looking for the kind in a string
		// literal context (the defaultKindThresholds map).
		needle := "\"" + kind + "\":"
		if !strings.Contains(body, needle) {
			t.Fatalf("defaultKindThresholds missing critical kind %q", kind)
		}
	}
}

// TestCheckerMinSampleIsStricterWins pins BUG 1 from the 2026-08-23
// hzx-2 audit follow-up: the per-kind MinSampleSize must be the
// STRICTER (higher) of the two values, not the looser. The original
// implementation silently dropped the rate_limit 8-sample floor by
// picking the minimum of (perKind=8, global=5).
func TestCheckerMinSampleIsStricterWins(t *testing.T) {
	src, err := os.ReadFile("checker.go")
	if err != nil {
		t.Fatalf("read checker source failed: %v", err)
	}
	body := string(src)
	// The minSamples assignment must use the STRICTER (higher) value.
	// The bug was: `if effectiveMinSample > c.minSampleSize { minSamples = c.minSampleSize }`
	// (silently dropped per-kind stricter floor). The fix flips the
	// assignment target so the per-kind wins when it is higher.
	for _, anti := range []string{
		// Old buggy pattern: per-kind stricter floor is dropped.
		`if effectiveMinSample > c.minSampleSize {
			minSamples = c.minSampleSize
		}`,
	} {
		// Whitespace-tolerant match: strip leading whitespace from
		// each line of the anti-pattern before searching the source.
		compact := strings.Join(strings.Fields(anti), "")
		// Also build a stripped version of the source for the search.
		srcCompact := strings.Join(strings.Fields(body), "")
		if strings.Contains(srcCompact, compact) {
			t.Fatalf("checker.go still contains the inverted min-sample pattern: %q", anti)
		}
	}
	// And it must contain the fixed pattern.
	if !strings.Contains(body, "minSamples = effectiveMinSample") {
		t.Fatalf("checker.go missing the fixed min-sample assignment (per-kind stricter should win)")
	}
}
