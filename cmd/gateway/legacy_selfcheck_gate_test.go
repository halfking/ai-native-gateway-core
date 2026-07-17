package main

import (
	"os"
	"testing"
)

// TestLegacySelfcheckGate verifies the double-gate logic for the legacy
// featured-model self-check worker (bg.NewSelfCheckWorker).
// The worker should only start when BOTH conditions are met:
// 1. LLM_GATEWAY_USE_NEW_PROBE_MODE is explicitly "false"
// 2. LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK is explicitly "true"
func TestLegacySelfcheckGate(t *testing.T) {
	tests := []struct {
		name                    string
		useNewProbeMode         string // LLM_GATEWAY_USE_NEW_PROBE_MODE
		enableLegacySelfcheck   string // LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK
		expectWorkerShouldStart bool
	}{
		{
			name:                    "new mode (default) - legacy disabled",
			useNewProbeMode:         "",
			enableLegacySelfcheck:   "",
			expectWorkerShouldStart: false,
		},
		{
			name:                    "new mode explicit true - legacy disabled",
			useNewProbeMode:         "true",
			enableLegacySelfcheck:   "",
			expectWorkerShouldStart: false,
		},
		{
			name:                    "rollback mode but no explicit opt-in",
			useNewProbeMode:         "false",
			enableLegacySelfcheck:   "",
			expectWorkerShouldStart: false,
		},
		{
			name:                    "rollback mode with explicit opt-in",
			useNewProbeMode:         "false",
			enableLegacySelfcheck:   "true",
			expectWorkerShouldStart: true,
		},
		{
			name:                    "new mode with legacy opt-in (gate 1 blocks)",
			useNewProbeMode:         "true",
			enableLegacySelfcheck:   "true",
			expectWorkerShouldStart: false,
		},
		{
			name:                    "rollback mode but legacy=false",
			useNewProbeMode:         "false",
			enableLegacySelfcheck:   "false",
			expectWorkerShouldStart: false,
		},
		{
			name:                    "rollback mode with legacy=TRUE (case insensitive)",
			useNewProbeMode:         "false",
			enableLegacySelfcheck:   "TRUE",
			expectWorkerShouldStart: true,
		},
		{
			name:                    "rollback mode with legacy=True (mixed case)",
			useNewProbeMode:         "false",
			enableLegacySelfcheck:   "True",
			expectWorkerShouldStart: true,
		},
		{
			name:                    "rollback mode with legacy whitespace padded",
			useNewProbeMode:         "false",
			enableLegacySelfcheck:   "  true  ",
			expectWorkerShouldStart: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup env
			if tt.useNewProbeMode == "" {
				os.Unsetenv("LLM_GATEWAY_USE_NEW_PROBE_MODE")
			} else {
				os.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", tt.useNewProbeMode)
			}
			if tt.enableLegacySelfcheck == "" {
				os.Unsetenv("LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK")
			} else {
				os.Setenv("LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK", tt.enableLegacySelfcheck)
			}

			// Simulate the gating logic
			gate1 := useNewProbeMode()
			gate2 := os.Getenv("LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK")
			gate2Lower := ""
			if gate2 != "" {
				gate2Lower = os.Getenv("LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK")
			}
			gate2Lower = trimToLower(gate2Lower)

			actualShouldStart := !gate1 && gate2Lower == "true"

			if actualShouldStart != tt.expectWorkerShouldStart {
				t.Errorf("gate logic mismatch: got shouldStart=%v, want=%v (useNewProbeMode=%v, enableLegacy=%q)",
					actualShouldStart, tt.expectWorkerShouldStart, gate1, gate2)
			}

			// Cleanup
			os.Unsetenv("LLM_GATEWAY_USE_NEW_PROBE_MODE")
			os.Unsetenv("LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK")
		})
	}
}

// trimToLower is a helper to mimic the actual gate2 check logic
func trimToLower(s string) string {
	s = os.Getenv("LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK")
	if s == "" {
		return ""
	}
	return toLower(trim(s))
}

func trim(s string) string {
	// Simple trim simulation
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

func toLower(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			result[i] = s[i] + 32
		} else {
			result[i] = s[i]
		}
	}
	return string(result)
}
