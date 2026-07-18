package settings

import "testing"

func TestSessionServiceAuthSpec(t *testing.T) {
	specs := SessionServiceAuthSpecs()
	if len(specs) != 1 {
		t.Fatalf("expected one service auth spec, got %d", len(specs))
	}
	sp := specs[0]
	if sp.Key != "session_service_auth.enabled" {
		t.Fatalf("key = %q, want session_service_auth.enabled", sp.Key)
	}
	if sp.EnvName != "LLM_GATEWAY_SESSION_SERVICE_JWT_ENABLED" {
		t.Fatalf("env name = %q, want LLM_GATEWAY_SESSION_SERVICE_JWT_ENABLED", sp.EnvName)
	}
	if sp.Default != false {
		t.Fatalf("default = %v, want false", sp.Default)
	}
	if !sp.HotReload {
		t.Fatal("service auth gate must support hot reload")
	}
	if err := sp.Validate(false); err != nil {
		t.Fatalf("false default does not validate: %v", err)
	}
	if err := sp.Validate(true); err != nil {
		t.Fatalf("true value does not validate: %v", err)
	}
}

func TestSessionServiceAuthSpecRegisteredInPlatformSpecs(t *testing.T) {
	var found bool
	for _, sp := range PlatformSpecs() {
		if sp.Key == "session_service_auth.enabled" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("session_service_auth.enabled is not registered in PlatformSpecs")
	}
}
