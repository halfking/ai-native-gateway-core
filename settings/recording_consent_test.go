package settings

import "testing"

func TestRecordingConsentSpecs_DefaultOff(t *testing.T) {
	var consent *Spec
	for _, sp := range RecordingConsentSpecs() {
		if sp.Key == KeySessionRecordingConsent {
			consent = sp
			break
		}
	}
	if consent == nil {
		t.Fatal("session_recording_consent missing from RecordingConsentSpecs")
	}
	if consent.Scope != ScopeTenant {
		t.Fatalf("scope=%s want tenant", consent.Scope)
	}
	if consent.Default != false {
		t.Fatalf("default=%v want false", consent.Default)
	}
}

func TestRecordingConsentEnabled_FailClosedWithoutRegistry(t *testing.T) {
	prev := Global
	Global = nil
	t.Cleanup(func() { Global = prev })
	if RecordingConsentEnabled("any-tenant") {
		t.Fatal("nil Global must fail closed (no bodies)")
	}
}

func TestTenantSpecsIncludesRecordingConsent(t *testing.T) {
	found := false
	for _, sp := range TenantSpecs() {
		if sp.Key == KeySessionRecordingConsent {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("TenantSpecs must register session_recording_consent")
	}
}
