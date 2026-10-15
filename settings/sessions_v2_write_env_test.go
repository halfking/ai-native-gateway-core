package settings

import "testing"

// Local deployments need an explicit write opt-in without enabling V2 reads.
// Exercise the actual registry/env path, not just the EnvName metadata.
func TestSessionsV2ExplicitWriteEnvironmentPreservesReadOptIn(t *testing.T) {
	for _, key := range []string{"sessions_v2.enabled", "sessions_v2.shadow_write", "sessions_v2.l3_read", "sessions_v2.primary_read", "sessions_v2.dual_read"} {
		t.Setenv(EnvNameAuto(key), "")
	}
	previous := Global
	Global = NewRegistry()
	t.Cleanup(func() { Global = previous })
	Init(nil)
	for _, spec := range SessionsV2Specs() {
		Global.MustRegisterSpec(spec)
	}
	for _, key := range []string{"sessions_v2.enabled", "sessions_v2.shadow_write", "sessions_v2.l3_read", "sessions_v2.primary_read", "sessions_v2.dual_read"} {
		if GetPlatformBool(key, true) {
			t.Fatalf("%s must remain explicit opt-in", key)
		}
	}
	t.Setenv("LLM_GATEWAY_SESSIONS_V2_ENABLED", "true")
	t.Setenv("LLM_GATEWAY_SESSIONS_V2_SHADOW_WRITE", "true")
	if !GetPlatformBool("sessions_v2.enabled", false) || !GetPlatformBool("sessions_v2.shadow_write", false) {
		t.Fatal("explicit deployment write configuration must reach the effective settings registry")
	}
	for _, key := range []string{"sessions_v2.l3_read", "sessions_v2.primary_read", "sessions_v2.dual_read"} {
		if GetPlatformBool(key, true) {
			t.Fatalf("write opt-in must not enable %s", key)
		}
	}
	t.Setenv("LLM_GATEWAY_SESSIONS_V2_SHADOW_WRITE", "false")
	if GetPlatformBool("sessions_v2.shadow_write", true) {
		t.Fatal("explicit write opt-out must remain effective")
	}
}
