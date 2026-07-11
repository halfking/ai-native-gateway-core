package transformation

import (
	"os"
	"testing"
)

// TestIRTransportDefaultEnabled verifies that IR transport is enabled by default
// when LLM_GATEWAY_TRANSPORT_IR is unset (P0-1, 2026-07-11).
func TestIRTransportDefaultEnabled(t *testing.T) {
	tests := []struct {
		name   string
		envVal string
		want   bool
	}{
		{
			name:   "unset_defaults_to_true",
			envVal: "",
			want:   true,
		},
		{
			name:   "explicit_true",
			envVal: "true",
			want:   true,
		},
		{
			name:   "explicit_1",
			envVal: "1",
			want:   true,
		},
		{
			name:   "explicit_false",
			envVal: "false",
			want:   false,
		},
		{
			name:   "explicit_0",
			envVal: "0",
			want:   false,
		},
		{
			name:   "other_value_defaults_to_true",
			envVal: "anything",
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore original env
			old := os.Getenv("LLM_GATEWAY_TRANSPORT_IR")
			defer func() {
				if old == "" {
					os.Unsetenv("LLM_GATEWAY_TRANSPORT_IR")
				} else {
					os.Setenv("LLM_GATEWAY_TRANSPORT_IR", old)
				}
			}()

			if tt.envVal == "" {
				os.Unsetenv("LLM_GATEWAY_TRANSPORT_IR")
			} else {
				os.Setenv("LLM_GATEWAY_TRANSPORT_IR", tt.envVal)
			}

			got := ShouldEnableTransportIR()
			if got != tt.want {
				t.Errorf("ShouldEnableTransportIR() = %v, want %v (env=%q)", got, tt.want, tt.envVal)
			}
		})
	}
}
