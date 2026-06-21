package settings

import (
	"os"
	"strings"
	"testing"
)

// TestPlatformScopedSettings_AreIntentionallyGlobal verifies that settings
// declared with ScopePlatform are intentionally system-global and should
// NOT be changed to ScopeTenant without understanding the multi-tenant
// implications.
//
// Reference: docs/multi-tenant-standards.md §2.1 (design intent documentation)
func TestPlatformScopedSettings_AreIntentionallyGlobal(t *testing.T) {
	specFiles := []string{
		"spec_compression.go",
		"spec_passthrough.go",
	}
	for _, f := range specFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("cannot read %s: %v", f, err)
		}
		src := string(data)
		if !strings.Contains(src, "ScopePlatform") {
			t.Errorf("%s must contain ScopePlatform declarations", f)
		}
		if strings.Contains(src, "ScopeTenant") {
			t.Errorf("%s should be platform-only but contains ScopeTenant", f)
		}
	}
}

// TestRateLimitSpec_HasBothScopes verifies that spec_ratelimit.go contains
// both platform-level and tenant-level override settings.
func TestRateLimitSpec_HasBothScopes(t *testing.T) {
	data, err := os.ReadFile("spec_ratelimit.go")
	if err != nil {
		t.Fatalf("cannot read spec_ratelimit.go: %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "ScopePlatform") {
		t.Error("spec_ratelimit.go must have platform-level defaults")
	}
	if !strings.Contains(src, "ScopeTenant") {
		t.Error("spec_ratelimit.go must have tenant-level overrides")
	}
}

// TestSpecFiles_Exist verifies that all expected spec files are present.
// Adding a new spec file requires updating this test to maintain coverage.
func TestSpecFiles_Exist(t *testing.T) {
	expected := []string{
		"spec_compression.go",
		"spec_passthrough.go",
		"spec_ratelimit.go",
		"specs.go",
		"store_db.go",
	}
	for _, f := range expected {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("expected file %s does not exist: %v", f, err)
		}
	}
}
