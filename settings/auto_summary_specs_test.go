package settings

import "testing"

// TestAutoSummarySpecs_AllFiveKeysPresent (2026-08-06) — guards the
// shape of the auto_summary spec set. Operators tune these in the admin
// UI; removing or renaming a key would silently break the live reload
// chain in admin/auto_summary_generator.go.
func TestAutoSummarySpecs_AllFiveKeysPresent(t *testing.T) {
	specs := AutoSummarySpecs()
	byKey := make(map[string]*Spec, len(specs))
	for _, spec := range specs {
		byKey[spec.Key] = spec
	}

	wantKeys := []string{
		"auto_summary.rolling_turn_gate",
		"auto_summary.map_reduce_threshold",
		"auto_summary.chunk_approx_chars",
		"auto_summary.default_rate_per_min",
		"auto_summary.default_worker_slots",
	}
	for _, k := range wantKeys {
		sp := byKey[k]
		if sp == nil {
			t.Fatalf("missing key %q", k)
		}
		if sp.Type != TypeInt {
			t.Errorf("%s.Type = %v, want TypeInt", k, sp.Type)
		}
		if sp.Scope != ScopePlatform {
			t.Errorf("%s.Scope = %v, want ScopePlatform", k, sp.Scope)
		}
		if !sp.HotReload {
			t.Errorf("%s.HotReload = false, want true", k)
		}
		if sp.Min == nil || sp.Max == nil {
			t.Errorf("%s missing Min/Max", k)
		}
		// Default is any; numeric defaults surface as int under the spec.
		if d, ok := sp.Default.(int); !ok || d <= 0 {
			t.Errorf("%s.Default = %v (%T), want positive int", k, sp.Default, sp.Default)
		}
	}
}

// TestAutoSummarySpecs_DefaultsMatchCodeConstants (2026-08-06) — the
// hardcoded constants in admin/auto_summary_generator.go are the
// last-resort fallback. The spec.Default values should match so the
// system behaves identically whether the operator explicitly seeds
// settings_kv, sets an env var, or relies on the code constant.
func TestAutoSummarySpecs_DefaultsMatchCodeConstants(t *testing.T) {
	byKey := make(map[string]any)
	for _, sp := range AutoSummarySpecs() {
		byKey[sp.Key] = sp.Default
	}
	cases := []struct {
		key       string
		wantFloat float64 // JSON-decoded int defaults surface as float64
	}{
		{"auto_summary.rolling_turn_gate", 3},
		{"auto_summary.map_reduce_threshold", 12000},
		{"auto_summary.chunk_approx_chars", 3000},
		{"auto_summary.default_rate_per_min", 6},
		{"auto_summary.default_worker_slots", 4},
	}
	for _, tc := range cases {
		got, ok := byKey[tc.key]
		if !ok {
			t.Errorf("missing key %s", tc.key)
			continue
		}
		// The Spec.Default type is any; numeric defaults come back as
		// the underlying Go int we put in, which == the float64 target.
		if gotInt, isInt := got.(int); isInt {
			if gotInt != int(tc.wantFloat) {
				t.Errorf("%s.Default = %d, want %d", tc.key, gotInt, int(tc.wantFloat))
			}
			continue
		}
		if gotFloat, isFloat := got.(float64); isFloat {
			if gotFloat != tc.wantFloat {
				t.Errorf("%s.Default = %v, want %v", tc.key, gotFloat, tc.wantFloat)
			}
			continue
		}
		t.Errorf("%s.Default = %T (%v), want int(%v)", tc.key, got, got, tc.wantFloat)
	}
}

// TestAutoSummarySpecs_RangesAreOperational (2026-08-06) — sanity check
// that the Min/Max ranges don't accidentally allow values that would
// break the system. For example, worker_slots = 0 would deadlock the
// semaphore; map_reduce_threshold = 0 would force every summary
// through map-reduce.
func TestAutoSummarySpecs_RangesAreOperational(t *testing.T) {
	cases := []struct {
		key           string
		wantMin, wantMax float64
	}{
		{"auto_summary.rolling_turn_gate", 1, 100},
		{"auto_summary.map_reduce_threshold", 1000, 1_000_000},
		{"auto_summary.chunk_approx_chars", 500, 100_000},
		{"auto_summary.default_rate_per_min", 1, 600},
		{"auto_summary.default_worker_slots", 1, 128},
	}
	byKey := make(map[string]*Spec)
	for _, sp := range AutoSummarySpecs() {
		byKey[sp.Key] = sp
	}
	for _, tc := range cases {
		sp := byKey[tc.key]
		if sp == nil {
			t.Errorf("missing key %s", tc.key)
			continue
		}
		if *sp.Min != tc.wantMin {
			t.Errorf("%s.Min = %v, want %v", tc.key, *sp.Min, tc.wantMin)
		}
		if *sp.Max != tc.wantMax {
			t.Errorf("%s.Max = %v, want %v", tc.key, *sp.Max, tc.wantMax)
		}
		// Default is any; numeric defaults surface as int.
		d, ok := sp.Default.(int)
		if !ok {
			t.Errorf("%s.Default = %v (%T), want int", tc.key, sp.Default, sp.Default)
			continue
		}
		if d < int(*sp.Min) || d > int(*sp.Max) {
			t.Errorf("%s.Default = %d outside [%v, %v]",
				tc.key, d, *sp.Min, *sp.Max)
		}
	}
}

// TestAutoSummarySpecs_RegisteredInPlatformSpecs (2026-08-06) — the
// PlatformSpecs() aggregator must include our auto_summary set or the
// settings registry will silently skip registration. Without
// registration, settings.GetPlatformInt returns the fallback constant
// every time and the hot-reload chain is dead.
func TestAutoSummarySpecs_RegisteredInPlatformSpecs(t *testing.T) {
	platform := PlatformSpecs()
	want := map[string]bool{
		"auto_summary.rolling_turn_gate":     false,
		"auto_summary.map_reduce_threshold":   false,
		"auto_summary.chunk_approx_chars":     false,
		"auto_summary.default_rate_per_min":   false,
		"auto_summary.default_worker_slots":    false,
	}
	for _, sp := range platform {
		if _, ok := want[sp.Key]; ok {
			want[sp.Key] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("PlatformSpecs() missing %s — registration in specs.go is stale", k)
		}
	}
}