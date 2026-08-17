package v2

import (
	"math"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

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

// -----------------------------------------------------------------------------
// 会话优化 v4 T5-lite / P1-6 — UT-UR-06: fast-recovery parameters
// (CoolSeconds 120→30, NodeTTL 60min→15min, backoff cap 3600→1800) become
// hot-configurable via settings_kv (LoadHot) while keeping the spec target
// defaults when no override is wired.
// -----------------------------------------------------------------------------

// TestDefaultConfigFastRecoveryTargets pins the switched boot defaults:
// 30s cool / 15min node TTL / 1800s backoff cap (spec §5 参数总表, §14.5).
func TestDefaultConfigFastRecoveryTargets(t *testing.T) {
	c := DefaultConfig()
	if c.CoolSeconds != 30 {
		t.Fatalf("CoolSeconds=%d, want 30 (v4 T5 target, was 120)", c.CoolSeconds)
	}
	if c.NodeTTL != 15*time.Minute {
		t.Fatalf("NodeTTL=%s, want 15m (v4 T5 target, was 60m)", c.NodeTTL)
	}
	if c.BackoffCapSeconds != 1800 {
		t.Fatalf("BackoffCapSeconds=%d, want 1800 (v4 T5 target, was hard-coded 3600 in lua)", c.BackoffCapSeconds)
	}
	if c.MirrorGraceEnabled {
		t.Fatalf("MirrorGraceEnabled must default to false (§14.3 target gear)")
	}
}

// mapHotSource is the settings_kv stub for the LoadHot contract tests.
type mapHotSource struct {
	ints  map[string]int
	bools map[string]bool
}

func (m mapHotSource) GetInt(key string, def int) int {
	if v, ok := m.ints[key]; ok {
		return v
	}
	return def
}

func (m mapHotSource) GetBool(key string, def bool) bool {
	if v, ok := m.bools[key]; ok {
		return v
	}
	return def
}

// TestLoadHotOverridesFastRecoveryParams pins the runtime half of UT-UR-06:
// live settings_kv values win over the boot defaults, independently per key.
func TestLoadHotOverridesFastRecoveryParams(t *testing.T) {
	base := DefaultConfig()
	src := mapHotSource{
		ints: map[string]int{
			HotKeyCoolSeconds:       45,
			HotKeyNodeTTLSeconds:    300,
			HotKeyBackoffCapSeconds: 600,
		},
		bools: map[string]bool{HotKeyMirrorGrace: true},
	}
	got := LoadHot(base, src)
	if got.CoolSeconds != 45 {
		t.Fatalf("CoolSeconds=%d, want 45", got.CoolSeconds)
	}
	if got.NodeTTL != 300*time.Second {
		t.Fatalf("NodeTTL=%s, want 5m", got.NodeTTL)
	}
	if got.BackoffCapSeconds != 600 {
		t.Fatalf("BackoffCapSeconds=%d, want 600", got.BackoffCapSeconds)
	}
	if !got.MirrorGraceEnabled {
		t.Fatalf("MirrorGraceEnabled=false, want true via settings_kv")
	}
	// The boot config must remain untouched (pure merge).
	if base.CoolSeconds != 30 || base.NodeTTL != 15*time.Minute || base.MirrorGraceEnabled {
		t.Fatalf("LoadHot must not mutate its receiver: %+v", base)
	}
}

// TestLoadHotIgnoresInvalidValues pins the safety fallback: missing, zero,
// or negative values keep the boot default per key — a bad settings_kv row
// can never zero out a safety parameter.
func TestLoadHotIgnoresInvalidValues(t *testing.T) {
	base := DefaultConfig()
	src := mapHotSource{ints: map[string]int{
		HotKeyCoolSeconds:       0,
		HotKeyNodeTTLSeconds:    -5,
		HotKeyBackoffCapSeconds: -1,
	}}
	got := LoadHot(base, src)
	if got.CoolSeconds != base.CoolSeconds || got.NodeTTL != base.NodeTTL || got.BackoffCapSeconds != base.BackoffCapSeconds {
		t.Fatalf("invalid values must keep boot defaults, got %+v", got)
	}
	if got.MirrorGraceEnabled {
		t.Fatalf("MirrorGraceEnabled must keep the boot default when the key is absent")
	}
	if got := LoadHot(base, nil); got.CoolSeconds != base.CoolSeconds || got.NodeTTL != base.NodeTTL ||
		got.BackoffCapSeconds != base.BackoffCapSeconds || got.MirrorGraceEnabled != base.MirrorGraceEnabled {
		t.Fatalf("nil source must be a no-op, got %+v", got)
	}
}

// TestManagerEffectiveConfigRuntimeChange pins the manager-level runtime
// change path: SetHotConfig wires a live source; effectiveConfig() reflects
// the merged values immediately, and detaching reverts to boot values.
func TestManagerEffectiveConfigRuntimeChange(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mgr := New(Dependencies{Redis: rdb, Config: DefaultConfig()})

	if got := mgr.effectiveConfig().CoolSeconds; got != 30 {
		t.Fatalf("no hot source wired: CoolSeconds=%d, want boot default 30", got)
	}
	mgr.SetHotConfig(mapHotSource{ints: map[string]int{HotKeyCoolSeconds: 120}})
	if got := mgr.effectiveConfig().CoolSeconds; got != 120 {
		t.Fatalf("after SetHotConfig: CoolSeconds=%d, want 120", got)
	}
	mgr.SetHotConfig(nil)
	if got := mgr.effectiveConfig().CoolSeconds; got != 30 {
		t.Fatalf("after detach: CoolSeconds=%d, want boot default 30", got)
	}
}
