package settings

import "testing"

// TestStreamContinuationKeyIsRegisteredInSpecs pins that the P0-3 kill-switch
// key is declared as a settings spec — GetPlatformBool falls back silently
// when a key has no Spec, so an unregistered key would make the toggle
// unflippable (the switch would read the default forever). Same shape as the
// r1027 models.list_only_routable pin (domains/streaming/models_routable_test.go).
func TestStreamContinuationKeyIsRegisteredInSpecs(t *testing.T) {
	found := false
	for _, sp := range StreamContinuationSpecs() {
		if sp.Key == KeyStreamContinuationEnabled {
			found = true
			if sp.Type != TypeBool {
				t.Errorf("spec %s: Type = %q, want %q", KeyStreamContinuationEnabled, sp.Type, TypeBool)
			}
			if sp.Scope != ScopePlatform {
				t.Errorf("spec %s: Scope = %q, want %q", KeyStreamContinuationEnabled, sp.Scope, ScopePlatform)
			}
			if !sp.HotReload {
				t.Errorf("spec %s: HotReload = false, want true (stream loop must observe flips without restart)", KeyStreamContinuationEnabled)
			}
			break
		}
	}
	if !found {
		t.Fatalf("settings key %q is not declared in StreamContinuationSpecs(); GetPlatformBool would silently fall back to the default and the kill switch would never engage", KeyStreamContinuationEnabled)
	}
	// The spec must also be reachable through the aggregate registration
	// point — PlatformSpecs() is what the registry loads.
	aggregate := false
	for _, sp := range PlatformSpecs() {
		if sp.Key == KeyStreamContinuationEnabled {
			aggregate = true
			break
		}
	}
	if !aggregate {
		t.Fatalf("settings key %q is missing from PlatformSpecs(); the registry would never load it", KeyStreamContinuationEnabled)
	}
}
