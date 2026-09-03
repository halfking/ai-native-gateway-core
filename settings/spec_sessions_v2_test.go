package settings

import "testing"

// TestSessionsV2FeatureFlags validates that the Sessions V2 feature flag
// configuration is correctly defined.
func TestSessionsV2FeatureFlags(t *testing.T) {
	specs := SessionsV2Specs()

	// Should have all expected flags
	expectedKeys := []string{
		"sessions_v2.enabled",
		"sessions_v2.shadow_write",
		"sessions_v2.rollout_percent",
		"sessions_v2.l3_read",
		"sessions_v2.dual_read",
		"sessions_v2.primary_read",
		"sessions_v2.write_timeout_ms",
		"sessions_v2.read_timeout_ms",
		"sessions_v2.compression_enabled",
		"sessions_v2.turn_logs_retention_hours",
		"sessions_v2.request_bodies_full",
		"sessions_v2.turns_list_routing",
	}

	if len(specs) != len(expectedKeys) {
		t.Errorf("expected %d specs, got %d", len(expectedKeys), len(specs))
	}

	// Verify all specs are valid
	for _, spec := range specs {
		if spec.Key == "" {
			t.Errorf("spec with empty key")
		}
		if spec.Category != CategorySession {
			t.Errorf("spec %s: expected category %s, got %s",
				spec.Key, CategorySession, spec.Category)
		}
		if spec.Scope != ScopePlatform {
			t.Errorf("spec %s: expected scope %s, got %s",
				spec.Key, ScopePlatform, spec.Scope)
		}
		if spec.Description == "" {
			t.Errorf("spec %s: missing description", spec.Key)
		}
		if spec.Default == nil {
			t.Errorf("spec %s: missing default value", spec.Key)
		}

		// Validate default values match type
		if err := spec.Validate(spec.Default); err != nil {
			t.Errorf("spec %s: invalid default value: %v", spec.Key, err)
		}
	}

	// Test specific constraints
	rolloutSpec := findSpec(specs, "sessions_v2.rollout_percent")
	if rolloutSpec == nil {
		t.Fatal("rollout_percent spec not found")
	}
	if rolloutSpec.Min == nil || *rolloutSpec.Min != 0 {
		t.Errorf("rollout_percent: expected min 0")
	}
	if rolloutSpec.Max == nil || *rolloutSpec.Max != 100 {
		t.Errorf("rollout_percent: expected max 100")
	}
	if rolloutSpec.Default != 0 {
		t.Errorf("rollout_percent: expected default 0, got %v", rolloutSpec.Default)
	}

	// Full request/response bodies remain the safe default. Operators must
	// explicitly opt in to summary storage by setting this flag to false.
	bodiesFullSpec := findSpec(specs, "sessions_v2.request_bodies_full")
	if bodiesFullSpec == nil {
		t.Fatal("request_bodies_full spec not found")
	}
	if bodiesFullSpec.Default != true {
		t.Errorf("request_bodies_full: expected default true, got %v", bodiesFullSpec.Default)
	}

	// The list routing flag is a platform-scoped hot-reload enum and must
	// default to the legacy tree path until operators explicitly opt in.
	routingSpec := findSpec(specs, "sessions_v2.turns_list_routing")
	if routingSpec == nil {
		t.Fatal("turns_list_routing spec not found")
	}
	if routingSpec.Type != TypeEnum || routingSpec.Scope != ScopePlatform || !routingSpec.HotReload {
		t.Fatalf("turns_list_routing metadata mismatch: type=%v scope=%v hot_reload=%v", routingSpec.Type, routingSpec.Scope, routingSpec.HotReload)
	}
	if routingSpec.Default != "tree" {
		t.Errorf("turns_list_routing: expected default tree, got %v", routingSpec.Default)
	}
	if len(routingSpec.Options) != 3 || routingSpec.Options[0] != "tree" || routingSpec.Options[1] != "dual" || routingSpec.Options[2] != "v2" {
		t.Errorf("turns_list_routing: unexpected options %#v", routingSpec.Options)
	}

	// Test hot reload flag
	for _, spec := range specs {
		if !spec.HotReload {
			t.Errorf("spec %s: should support hot reload", spec.Key)
		}
	}
}

func findSpec(specs []*Spec, key string) *Spec {
	for _, s := range specs {
		if s.Key == key {
			return s
		}
	}
	return nil
}
