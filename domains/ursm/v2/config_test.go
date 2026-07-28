package v2

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

// TestLoadFromEnv_ShadowDoubleWrite (P0-3) pins the env-loading
// contract for URSM_V2_SHADOW_DOUBLE_WRITE. Truthy (1/true/yes,
// case-insensitive) flips the knob on; anything else leaves it off.
// The flag must NEVER turn on by accident from a misspelled env
// value, because accidental double-write would silently load URSM v2
// Redis namespace with production traffic and break the cutover
// comparison.
func TestLoadFromEnv_ShadowDoubleWrite(t *testing.T) {
	cases := []struct {
		envValue string
		want     bool
	}{
		{"", false},       // unset → default off
		{"0", false},      // explicit falsey
		{"false", false},  // explicit falsey
		{"no", false},     // explicit falsey
		{"off", false},    // explicit falsey (NOT in the truthy list)
		{"1", true},       // truthy
		{"true", true},    // truthy
		{"TRUE", true},    // truthy (case-insensitive)
		{"yes", true},     // truthy
		{"YES", true},     // truthy (case-insensitive)
		{"random", false}, // unknown → ignored, default off
	}
	for _, tc := range cases {
		t.Run(tc.envValue, func(t *testing.T) {
			t.Setenv("URSM_V2_SHADOW_DOUBLE_WRITE", tc.envValue)
			cfg := LoadFromEnv()
			if cfg.ShadowDoubleWrite != tc.want {
				t.Fatalf("env=%q → ShadowDoubleWrite=%v, want %v", tc.envValue, cfg.ShadowDoubleWrite, tc.want)
			}
		})
	}
}

// TestLoadFromEnv_ShadowDoubleWrite_DoesNotChangeMode confirms the
// P0-3 safety invariant: enabling ShadowDoubleWrite via env MUST NOT
// implicitly promote Mode to Authoritative. Operators control mode
// and the shadow-double-write flag independently.
func TestLoadFromEnv_ShadowDoubleWrite_DoesNotChangeMode(t *testing.T) {
	t.Setenv("URSM_V2_SHADOW_DOUBLE_WRITE", "true")
	cfg := LoadFromEnv()
	if cfg.ShadowDoubleWrite != true {
		t.Fatalf("expected ShadowDoubleWrite=true, got %v", cfg.ShadowDoubleWrite)
	}
	if cfg.Mode != api.ModeOff {
		t.Fatalf("expected Mode=ModeOff (default), got %v — ShadowDoubleWrite env must NOT silently promote Mode", cfg.Mode)
	}
}

// TestDefaultConfig_ShadowDoubleWriteOff pins the default-off contract
// at the package level so a future refactor that flips the default
// trips this test.
func TestDefaultConfig_ShadowDoubleWriteOff(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ShadowDoubleWrite {
		t.Fatal("DefaultConfig() must ship with ShadowDoubleWrite=false; opt-in only")
	}
}
