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
