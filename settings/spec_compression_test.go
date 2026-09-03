package settings

import "testing"

func TestCompressionSpecs_DefaultsEnableAutomaticCompression(t *testing.T) {
	specs := CompressionSpecs()
	byKey := make(map[string]*Spec, len(specs))
	for _, spec := range specs {
		byKey[spec.Key] = spec
	}

	if got := byKey["compression.enabled"]; got == nil || got.Default != true {
		t.Fatalf("compression.enabled default = %#v, want true", got)
	}
	if got := byKey["compression.mode"]; got == nil || got.Default != "smart" {
		t.Fatalf("compression.mode default = %#v, want smart", got)
	}
	if got := byKey["handoff.enabled"]; got != nil {
		t.Fatalf("handoff.enabled must be owned by HandoffSpecs, got duplicate %#v", got)
	}
	fraction := byKey["compression.window_fraction"]
	if fraction == nil || fraction.Default != 0.80 {
		t.Fatalf("compression.window_fraction default = %#v, want 0.80", fraction)
	}
	wantEnv := map[string]string{
		"compression.strategy_runner_enabled": "LLM_GATEWAY_COMPRESSION_STRATEGY_RUNNER_ENABLED",
		"compression.selector_mode":           "LLM_GATEWAY_COMPRESSION_SELECTOR",
		"compression.runner_mode":             "LLM_GATEWAY_COMPRESSION_RUNNER_MODE",
		"compression.selector_spec":           "LLM_GATEWAY_COMPRESSION_SELECTOR_SPEC",
		"compression.adaptive_target_ratio":   "LLM_GATEWAY_COMPRESSION_TARGET_RATIO",
	}
	runner := byKey["compression.runner_mode"]
	if runner == nil || runner.Default != "sequential" {
		t.Fatalf("compression.runner_mode default = %#v, want sequential", runner)
	}
	if runner == nil || len(runner.Options) != 2 || runner.Options[0] != "sequential" || runner.Options[1] != "parallel" {
		t.Fatalf("compression.runner_mode options = %#v, want sequential/parallel", runner)
	}
	for key, want := range wantEnv {
		if got := byKey[key]; got == nil || got.EnvName != want {
			t.Errorf("%s EnvName = %#v, want %q", key, got, want)
		}
	}
}

func TestHandoffSpecs_DefaultToTransparentWithoutUpstreamRewrite(t *testing.T) {
	byKey := make(map[string]Spec)
	for _, spec := range HandoffSpecs() {
		byKey[spec.Key] = spec
	}
	if got := byKey["handoff.enabled"].Default; got != false {
		t.Fatalf("handoff.enabled default = %#v, want false", got)
	}
	if got := byKey["handoff.client_mode"].Default; got != "transparent" {
		t.Fatalf("handoff.client_mode default = %#v, want transparent", got)
	}
}

func TestProductionSettingsSpecsHaveUniqueKeys(t *testing.T) {
	seen := make(map[string]string)
	for _, spec := range PlatformSpecs() {
		if owner, ok := seen[spec.Key]; ok {
			t.Fatalf("duplicate platform spec %q, first owner %s", spec.Key, owner)
		}
		seen[spec.Key] = "PlatformSpecs"
	}
	for _, spec := range AutoControlSpecs() {
		if owner, ok := seen[spec.Key]; ok {
			t.Fatalf("duplicate production spec %q between %s and AutoControlSpecs", spec.Key, owner)
		}
		seen[spec.Key] = "AutoControlSpecs"
	}
}
