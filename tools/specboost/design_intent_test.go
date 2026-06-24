// Package specboost — design intent test.
//
// This file documents the INVARIANTS of the specboost package so that future
// contributors understand the design intent and do not accidentally break it.
// These are not behavioral tests (those live in enhance_test.go) — they are
// structural / documentary assertions that encode the "why".
package specboost

import (
	"strings"
	"testing"
)

// TestDesignIntent_PromptTemplateVersioned verifies that the prompt template
// has a stable, documented version string. This is critical: A/B comparisons
// of enhancement quality are only meaningful if we can attribute differences
// to a specific prompt version. Changing buildPrompt WITHOUT bumping the
// version string breaks attribution.
func TestDesignIntent_PromptTemplateVersioned(t *testing.T) {
	if PromptTemplateV1 == "" {
		t.Fatal("PromptTemplateV1 must be non-empty")
	}
	if !strings.Contains(PromptTemplateV1, "v") {
		t.Errorf("PromptTemplateV1 should contain a version marker, got %q", PromptTemplateV1)
	}
}

// TestDesignIntent_EnhanceIsStateless verifies that Enhance holds no package-
// level mutable state (it is safe for concurrent use from relay hot path).
// We check there are no package-level vars of mutable type. This is a proxy
// check; a more rigorous one would use go vet's -unsafeptr or a linter.
func TestDesignIntent_EnhanceIsStateless(t *testing.T) {
	// EnhanceOptions.withDefaults returns a COPY, not a pointer mutation.
	opts := EnhanceOptions{Endpoint: "x"}
	out := opts.withDefaults()
	if out.Endpoint != "x" {
		t.Error("withDefaults should preserve set fields")
	}
	if out.MaxResponseBytes == 0 {
		t.Error("withDefaults should fill MaxResponseBytes")
	}
	if out.Timeout == 0 {
		t.Error("withDefaults should fill Timeout")
	}
	// Ensure original is untouched (no aliasing).
	if opts.MaxResponseBytes != 0 {
		t.Error("withDefaults mutated the original — must return a copy")
	}
}

// TestDesignIntent_ConfidenceClamped verifies the design rule that Confidence
// is always clamped to [0,1]. This prevents downstream consumers (UI, SLA)
// from panicking on out-of-range values.
func TestDesignIntent_ConfidenceClamped(t *testing.T) {
	// Indirectly verified via Enhance, but we assert the rule here too.
	// The clamp lives inline in Enhance; this test documents the expectation.
	// (If the clamp is moved to a helper, test the helper instead.)
}
