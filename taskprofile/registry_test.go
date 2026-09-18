package taskprofile

import (
	"strings"
	"testing"
)

// registry_test.go — pins the embedded defaults against the V3 taxonomy and
// exercises overlay validation / atomic swap semantics.

// TestDefaults_MirrorV3TierMapping pins the embedded baseline to the
// documented autoroute V3 defaults (task_types_v3.go TaskTypeTierMapping +
// MinConfidenceThresholds). If autoroute's taxonomy changes deliberately,
// update both sides in the same commit — the module must not silently drift.
func TestDefaults_MirrorV3TierMapping(t *testing.T) {
	want := map[string]struct {
		tier string
		conf float64
	}{
		"architecture":  {"tier-a", 0.70},
		"audit":         {"tier-a", 0.70},
		"debugging":     {"tier-a", 0.65},
		"coding":        {"tier-b", 0.75},
		"refactoring":   {"tier-b", 0.70},
		"testing":       {"tier-b", 0.75},
		"devops":        {"tier-c", 0.80},
		"documentation": {"tier-c", 0.85},
		"summary":       {"tier-c", 0.85},
		"dependency":    {"tier-c", 0.75},
		// V2 legacy types (dominant in production auto_route_selections).
		"chat":      {"tier-b", 0.80},
		"code":      {"tier-b", 0.75},
		"creative":  {"tier-b", 0.75},
		"reasoning": {"tier-a", 0.70},
		"planning":  {"tier-a", 0.70},
		// R43 (2026-09-18): remaining autoroute.V2 enums. Values mirror
		// autoroute's unknown-type fallback (getDefaultTier: tier-b + 0.70,
		// getFallbacksForTier("tier-b") = [tier-a, tier-c]).
		"agent":                 {"tier-b", 0.70},
		"long_context":          {"tier-b", 0.70},
		"vision":                {"tier-b", 0.70},
		"function_call":         {"tier-b", 0.70},
		"code_audit":            {"tier-b", 0.70},
		"intent_classification": {"tier-b", 0.70},
	}
	version, profiles := Snapshot()
	if version != RegistryVersion {
		t.Fatalf("registry version = %q, want %q", version, RegistryVersion)
	}
	if len(profiles) != len(want) {
		t.Fatalf("registry has %d profiles, want %d", len(profiles), len(want))
	}
	for _, p := range profiles {
		w, ok := want[p.TaskType]
		if !ok {
			t.Errorf("unexpected profile %q", p.TaskType)
			continue
		}
		if p.PreferredTier != w.tier || p.MinConfidence != w.conf {
			t.Errorf("profile %s = (%s, %.2f), want (%s, %.2f)",
				p.TaskType, p.PreferredTier, p.MinConfidence, w.tier, w.conf)
		}
	}
}

func TestSnapshot_SortedAndStable(t *testing.T) {
	_, profiles := Snapshot()
	for i := 1; i < len(profiles); i++ {
		if profiles[i-1].TaskType >= profiles[i].TaskType {
			t.Fatalf("profiles not sorted at %d: %q >= %q", i, profiles[i-1].TaskType, profiles[i].TaskType)
		}
	}
}

func TestIsValidTaskType(t *testing.T) {
	if !IsValidTaskType("coding") || IsValidTaskType("nonexistent") {
		t.Fatal("IsValidTaskType disagrees with the registry")
	}
}

func TestLoadOverlayBytes_HappyPathAndAtomicity(t *testing.T) {
	original := map[string]bool{}
	for _, tt := range TaskTypes() {
		original[tt] = true
	}

	valid := []byte(`{
		"schema_version": 1,
		"version": "2026.10.test-overlay",
		"profiles": {
			"coding": {"task_type": "coding", "description": "overlay", "preferred_tier": "tier-a", "fallback_tiers": ["tier-b"], "min_confidence": 0.6}
		}
	}`)
	version, err := LoadOverlayBytes(valid, "test.json")
	if err != nil {
		t.Fatalf("LoadOverlayBytes: %v", err)
	}
	if version != "2026.10.test-overlay" {
		t.Fatalf("version = %q", version)
	}
	p, ok := Profile("coding")
	if !ok || p.PreferredTier != "tier-a" || p.MinConfidence != 0.6 {
		t.Fatalf("overlay profile not applied: %+v", p)
	}
	// Untouched types keep defaults (overlay merges, not replaces).
	if p, _ := Profile("summary"); p.PreferredTier != TierC {
		t.Fatalf("overlay must merge, summary changed: %+v", p)
	}

	t.Cleanup(func() { _, _ = ReloadOverlay("") }) // restore defaults for other tests

	bad := []string{
		`{"schema_version": 99, "version": "x", "profiles": {"coding": {"task_type":"coding","preferred_tier":"tier-a","min_confidence":0.5}}}`,
		`{"schema_version": 1, "profiles": {"coding": {"task_type":"coding","preferred_tier":"tier-a","min_confidence":0.5}}}`,
		`{"schema_version": 1, "version": "x", "profiles": {}}`,
		`{"schema_version": 1, "version": "x", "profiles": {"coding": {"task_type":"OTHER","preferred_tier":"tier-a","min_confidence":0.5}}}`,
		`{"schema_version": 1, "version": "x", "profiles": {"coding": {"task_type":"coding","preferred_tier":"tier-z","min_confidence":0.5}}}`,
		`{"schema_version": 1, "version": "x", "profiles": {"coding": {"task_type":"coding","preferred_tier":"tier-a","min_confidence":1.5}}}`,
		`not json at all`,
	}
	for i, b := range bad {
		if _, err := LoadOverlayBytes([]byte(b), "bad.json"); err == nil {
			t.Errorf("bad overlay %d accepted: %s", i, b)
		}
	}
	// Atomicity: every rejection above must have left the last good overlay
	// (or defaults) active — never a half-applied state.
	if !IsValidTaskType("coding") {
		t.Fatal("registry corrupted after rejected overlays")
	}
	for tt := range original {
		if !IsValidTaskType(tt) {
			t.Errorf("type %q lost after rejected overlays", tt)
		}
	}
}

func TestReloadOverlay_EmptyPathResetsDefaults(t *testing.T) {
	overlay := []byte(`{"schema_version":1,"version":"tmp","profiles":{"coding":{"task_type":"coding","preferred_tier":"tier-a","min_confidence":0.5}}}`)
	if _, err := LoadOverlayBytes(overlay, "t.json"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(func() { _, _ = ReloadOverlay("") })

	version, err := ReloadOverlay("")
	if err != nil {
		t.Fatalf("ReloadOverlay: %v", err)
	}
	if version != RegistryVersion {
		t.Fatalf("version after reset = %q", version)
	}
	if p, _ := Profile("coding"); p.PreferredTier != TierB {
		t.Fatalf("defaults not restored: %+v", p)
	}
}

func TestValidateReasons(t *testing.T) {
	for _, r := range strings.Split(reasonsCSV(), ", ") {
		if !IsValidReason(r) {
			t.Errorf("declared reason %q rejected", r)
		}
	}
	if IsValidReason("nonsense") {
		t.Error("nonsense reason accepted")
	}
}
