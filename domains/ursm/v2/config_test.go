package v2

import (
	"math"
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
	if cfg.Mode != api.ModeAuthoritative {
		t.Fatalf("expected Mode=ModeAuthoritative (default), got %v — ShadowDoubleWrite env must NOT change the direct-start mode", cfg.Mode)
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

func TestLoadFromEnv_ModeAndCanaryPercent(t *testing.T) {
	cases := []struct {
		name        string
		mode        string
		percent     string
		wantMode    api.RolloutMode
		wantPercent int
	}{
		{"defaults", "", "", api.ModeAuthoritative, 0},

		{"shadow", "shadow", "", api.ModeShadow, 0},
		{"canary zero", "canary", "0", api.ModeCanary, 0},
		{"canary hundred", "canary", "100", api.ModeCanary, 100},
		{"invalid percent keeps default", "canary", "nope", api.ModeCanary, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("URSM_V2_MODE", tc.mode)
			t.Setenv("URSM_V2_CANARY_PERCENT", tc.percent)
			cfg := LoadFromEnv()
			if cfg.Mode != tc.wantMode || cfg.CanaryPercent != tc.wantPercent {
				t.Fatalf("LoadFromEnv() = mode=%q percent=%d, want mode=%q percent=%d", cfg.Mode, cfg.CanaryPercent, tc.wantMode, tc.wantPercent)
			}
		})
	}
}

func TestLoadFromEnv_ShadowSampleRate(t *testing.T) {
	cases := []struct {
		value string
		want  float64
	}{
		{"", 0.01},
		{"0", 0},
		{"0.25", 0.25},
		{"1", 1},
		{"-0.1", 0.01},
		{"1.1", 0.01},
		{"invalid", 0.01},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("URSM_V2_SHADOW_SAMPLE_RATE", tc.value)
			got := LoadFromEnv().ShadowSampleRate
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("ShadowSampleRate=%v, want %v", got, tc.want)
			}
		})
	}
}
