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
	fraction := byKey["compression.window_fraction"]
	if fraction == nil || fraction.Default != 0.85 {
		t.Fatalf("compression.window_fraction default = %#v, want 0.85", fraction)
	}
}
