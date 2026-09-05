package autoroute

import (
	"os"
	"testing"
)

func TestDefaultFeatureFlags_AutoOptimizationV3Disabled(t *testing.T) {
	flags := DefaultFeatureFlags()
	if flags.AutoOptimizationV3Enabled {
		t.Fatal("V3 optimization must be disabled by default")
	}
	if !flags.AutoOptimizationV3ShadowOnly {
		t.Fatal("V3 rollout must default to shadow-only")
	}
	if flags.AutoOptimizationV3VariantPct != 0 {
		t.Fatalf("default V3 variant percentage = %d, want 0", flags.AutoOptimizationV3VariantPct)
	}
	if flags.AutoOptimizationV3Scope != TreatmentScopeTenant {
		t.Fatalf("default V3 scope = %q, want tenant", flags.AutoOptimizationV3Scope)
	}
}

func TestLoadFeatureFlagsFromEnv_AutoOptimizationV3(t *testing.T) {
	env := map[string]string{
		"AUTO_OPTIMIZATION_V3_ENABLED":       "true",
		"AUTO_OPTIMIZATION_V3_SHADOW_ONLY":   "false",
		"AUTO_OPTIMIZATION_V3_VARIANT_PCT":   "37",
		"AUTO_OPTIMIZATION_V3_EXPERIMENT":    "exp-1",
		"AUTO_OPTIMIZATION_V3_VERSION":       "v2",
		"AUTO_OPTIMIZATION_V3_SCOPE":         "request",
		"AUTO_OPTIMIZATION_V3_AUTO_ROLLBACK": "true",
	}
	restore := make(map[string]string)
	had := make(map[string]bool)
	for key, value := range env {
		old, ok := os.LookupEnv(key)
		restore[key], had[key] = old, ok
		if err := os.Setenv(key, value); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for key, old := range restore {
			if had[key] {
				_ = os.Setenv(key, old)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}()

	flags := LoadFeatureFlagsFromEnv()
	if !flags.AutoOptimizationV3Enabled || flags.AutoOptimizationV3ShadowOnly {
		t.Fatalf("unexpected enabled/shadow flags: %+v", flags)
	}
	if flags.AutoOptimizationV3VariantPct != 37 || flags.AutoOptimizationV3Scope != TreatmentScopeRequest {
		t.Fatalf("unexpected percentage/scope: %+v", flags)
	}
	if flags.AutoOptimizationV3Experiment != "exp-1" || flags.AutoOptimizationV3Version != "v2" || !flags.AutoOptimizationV3AutoRollback {
		t.Fatalf("unexpected identity/rollback flags: %+v", flags)
	}
}

func TestLoadFeatureFlagsFromEnv_AutoOptimizationV3InvalidValuesFailClosed(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
		check func(*FeatureFlags) bool
	}{
		{"percentage below range", "AUTO_OPTIMIZATION_V3_VARIANT_PCT", "-1", func(f *FeatureFlags) bool { return f.AutoOptimizationV3VariantPct == 0 }},
		{"percentage above range", "AUTO_OPTIMIZATION_V3_VARIANT_PCT", "101", func(f *FeatureFlags) bool { return f.AutoOptimizationV3VariantPct == 0 }},
		{"invalid scope", "AUTO_OPTIMIZATION_V3_SCOPE", "cluster", func(f *FeatureFlags) bool { return f.AutoOptimizationV3Scope == TreatmentScopeTenant }},
		{"invalid bool", "AUTO_OPTIMIZATION_V3_ENABLED", "sometimes", func(f *FeatureFlags) bool { return !f.AutoOptimizationV3Enabled }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old, had := os.LookupEnv(tt.key)
			if err := os.Setenv(tt.key, tt.value); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if had {
					_ = os.Setenv(tt.key, old)
				} else {
					_ = os.Unsetenv(tt.key)
				}
			}()
			if !tt.check(LoadFeatureFlagsFromEnv()) {
				t.Fatalf("invalid value %q did not fail closed", tt.value)
			}
		})
	}
}
