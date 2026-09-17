package probemode

import "testing"

func TestEnabled_Parse(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"", true},        // unset → default true (2026-07-14 spec rewrite)
		{"true", true},    // explicit truthy set
		{"1", true},       // numeric truthy
		{"yes", true},     // word truthy
		{"on", true},      // word truthy variant
		{"TRUE", true},    // case-insensitive
		{" false", false}, // explicit falsey (with padding)
		{"0", false},      // numeric falsey
		{"no", false},     // word falsey
		{"garbage", false},
	}
	for _, tc := range cases {
		t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", tc.env)
		if got := Enabled(); got != tc.want {
			t.Errorf("Enabled() with env %q = %v, want %v", tc.env, got, tc.want)
		}
	}
}

func TestGuardStateTable_FollowsProbeMode(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "true")
	if got := GuardStateTable(); got != "v_node_probe_state_compat" {
		t.Fatalf("GuardStateTable() under new mode = %q, want the node_probe_state compat projection", got)
	}

	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "false")
	if got := GuardStateTable(); got != "model_probe_state" {
		t.Fatalf("GuardStateTable() under legacy mode = %q, want model_probe_state", got)
	}
}
