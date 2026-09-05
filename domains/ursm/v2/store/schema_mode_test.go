package store

import "testing"

func TestParseKeySchemaMode(t *testing.T) {
	for s, want := range map[string]KeySchemaMode{
		"legacy":    KeySchemaModeLegacy,
		"dual":      KeySchemaModeDual,
		"canonical": KeySchemaModeCanonical,
	} {
		m, err := ParseKeySchemaMode(s)
		if err != nil || m != want {
			t.Fatalf("ParseKeySchemaMode(%q) = %v, %v; want %v, nil", s, m, err, want)
		}
		if m.String() != s {
			t.Fatalf("String() = %q, want %q", m.String(), s)
		}
	}
	for _, s := range []string{"", "LEGACY", "Legacy", "off", "authoritative", "canonica", "dual ", "k2"} {
		if _, err := ParseKeySchemaMode(s); err == nil {
			t.Fatalf("ParseKeySchemaMode(%q) must be rejected: modes are frozen to legacy/dual/canonical", s)
		}
	}
	// The zero value must be legacy: the migration never flips a default.
	var zero KeySchemaMode
	if zero != KeySchemaModeLegacy {
		t.Fatalf("zero value = %v, want legacy", zero)
	}
}
