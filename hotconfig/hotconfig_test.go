package hotconfig

import "testing"

func TestGetIntAcceptsNumericString(t *testing.T) {
	cfg := &Config{values: map[string]interface{}{
		"llmgw_node_timeout_seconds": "240",
	}}

	if got := cfg.GetInt("llmgw_node_timeout_seconds", 180); got != 240 {
		t.Fatalf("GetInt() = %d, want 240", got)
	}
}

func TestGetIntFallsBackForInvalidString(t *testing.T) {
	cfg := &Config{values: map[string]interface{}{
		"llmgw_node_timeout_seconds": "not-a-number",
	}}

	if got := cfg.GetInt("llmgw_node_timeout_seconds", 180); got != 180 {
		t.Fatalf("GetInt() = %d, want default 180", got)
	}
}
