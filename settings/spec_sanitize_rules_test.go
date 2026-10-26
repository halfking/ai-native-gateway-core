package settings

import "testing"

func TestSanitizeRulesSpecsRegisteredAndBounded(t *testing.T) {
	expected := map[string]any{"sanitize.rules.refresh_interval_seconds": 5, "sanitize.rules.audit_enabled": true, "sanitize.rules.event_limit": 10000}
	found := 0
	for _, sp := range PlatformSpecs() {
		want, ok := expected[sp.Key]
		if !ok {
			continue
		}
		found++
		if sp.Default != want || sp.Scope != ScopePlatform || sp.Category != CategoryModules || !sp.HotReload || sp.Observability != "/admin/sanitize-rules" {
			t.Errorf("invalid spec %+v", sp)
		}
		if err := sp.Validate(want); err != nil {
			t.Fatal(err)
		}
		if sp.Key == "sanitize.rules.refresh_interval_seconds" {
			if sp.Validate(0) == nil || sp.Validate(301) == nil || sp.Validate(1) != nil || sp.Validate(300) != nil {
				t.Fatal("refresh bounds")
			}
		}
		if sp.Key == "sanitize.rules.event_limit" {
			if sp.Validate(99) == nil || sp.Validate(100001) == nil || sp.Validate(100) != nil || sp.Validate(100000) != nil {
				t.Fatal("event bounds")
			}
		}
	}
	if found != 3 {
		t.Fatal("unregistered rule settings", found)
	}
}
func TestSanitizeRulesSettingsReadHotValues(t *testing.T) {
	previous := Global
	t.Cleanup(func() { Global = previous })
	rows := map[string][]byte{}
	r := NewRegistry()
	r.RegisterBackend(ScopePlatform, &fakeBackend{store: rows})
	for _, sp := range SanitizeRulesSpecs() {
		if err := r.RegisterSpec(sp); err != nil {
			t.Fatal(err)
		}
	}
	Global = r
	if SanitizeRulesRefreshIntervalSeconds() != 5 || !SanitizeRulesAuditEnabled() || SanitizeRulesEventLimit() != 10000 {
		t.Fatal("defaults")
	}
	rows[KeySanitizeRulesRefreshIntervalSeconds] = []byte(`17`)
	rows[KeySanitizeRulesAuditEnabled] = []byte(`false`)
	rows[KeySanitizeRulesEventLimit] = []byte(`321`)
	if SanitizeRulesRefreshIntervalSeconds() != 17 || SanitizeRulesAuditEnabled() || SanitizeRulesEventLimit() != 321 {
		t.Fatal("DB changes not hot read")
	}
	rows[KeySanitizeRulesRefreshIntervalSeconds] = []byte(`999`)
	rows[KeySanitizeRulesEventLimit] = []byte(`1`)
	if SanitizeRulesRefreshIntervalSeconds() != 300 || SanitizeRulesEventLimit() != 100 {
		t.Fatal("read clamp")
	}
}
