package streaming

import (
	"os"
	"testing"
	"time"
)

// TestRecoveryHoldbackForModel_UnstableModels verifies that unstable models
// (glm-5.2, minimax-m3, minimax-text-01) receive extended holdback windows.
func TestRecoveryHoldbackForModel_UnstableModels(t *testing.T) {
	tests := []struct {
		model           string
		wantWindow      time.Duration
		wantMaxChunks   int
		description     string
	}{
		{"glm-5.2", 10 * time.Second, 50, "glm-5.2 should get extended window"},
		{"minimax-m3", 10 * time.Second, 50, "minimax-m3 should get extended window"},
		{"minimax-text-01", 10 * time.Second, 50, "minimax-text-01 should get extended window"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			window, chunks := RecoveryHoldbackForModel(tt.model)
			if window != tt.wantWindow {
				t.Errorf("%s: window = %v, want %v", tt.description, window, tt.wantWindow)
			}
			if chunks != tt.wantMaxChunks {
				t.Errorf("%s: maxChunks = %d, want %d", tt.description, chunks, tt.wantMaxChunks)
			}
		})
	}
}

// TestRecoveryHoldbackForModel_StableModels verifies that stable models use
// the global env or default values (0 when env unset).
func TestRecoveryHoldbackForModel_StableModels(t *testing.T) {
	// Clear env to ensure we test the default path
	os.Unsetenv("LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS")
	os.Unsetenv("LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS")

	tests := []struct {
		model       string
		description string
	}{
		{"gpt-4o", "gpt-4o is stable"},
		{"claude-3-5-sonnet-20241022", "claude is stable"},
		{"gpt-3.5-turbo", "gpt-3.5 is stable"},
		{"unknown-model", "unknown models are treated as stable"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			window, chunks := RecoveryHoldbackForModel(tt.model)
			// With env unset, stable models should get 0 (disabled)
			if window != 0 {
				t.Errorf("%s: window = %v, want 0 (disabled when env unset)", tt.description, window)
			}
			if chunks != 0 {
				t.Errorf("%s: maxChunks = %d, want 0 (disabled when env unset)", tt.description, chunks)
			}
		})
	}
}

// TestRecoveryHoldbackForModel_EnvOverride verifies that model-specific env
// vars take precedence over hardcoded defaults.
func TestRecoveryHoldbackForModel_EnvOverride(t *testing.T) {
	// Set model-specific env for glm-5.2
	os.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS_GLM_5_2", "15000")
	os.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS_GLM_5_2", "80")
	defer func() {
		os.Unsetenv("LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS_GLM_5_2")
		os.Unsetenv("LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS_GLM_5_2")
	}()

	window, chunks := RecoveryHoldbackForModel("glm-5.2")
	if window != 15*time.Second {
		t.Errorf("glm-5.2 with env override: window = %v, want 15s", window)
	}
	if chunks != 80 {
		t.Errorf("glm-5.2 with env override: maxChunks = %d, want 80", chunks)
	}
}

// TestRecoveryHoldbackForModel_GlobalEnvFallback verifies that stable models
// use the global LLM_GATEWAY_RECOVERY_HOLDBACK_* env when set.
func TestRecoveryHoldbackForModel_GlobalEnvFallback(t *testing.T) {
	os.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS", "8000")
	os.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS", "30")
	defer func() {
		os.Unsetenv("LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS")
		os.Unsetenv("LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS")
	}()

	window, chunks := RecoveryHoldbackForModel("gpt-4o")
	if window != 8*time.Second {
		t.Errorf("gpt-4o with global env: window = %v, want 8s", window)
	}
	if chunks != 30 {
		t.Errorf("gpt-4o with global env: maxChunks = %d, want 30", chunks)
	}
}

// TestIsUnstableModel verifies the unstable model detection logic.
func TestIsUnstableModel(t *testing.T) {
	tests := []struct {
		model    string
		unstable bool
	}{
		{"glm-5.2", true},
		{"minimax-m3", true},
		{"minimax-text-01", true},
		{"gpt-4o", false},
		{"claude-3-5-sonnet-20241022", false},
		{"", false},
		{"unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := isUnstableModel(tt.model)
			if got != tt.unstable {
				t.Errorf("isUnstableModel(%q) = %v, want %v", tt.model, got, tt.unstable)
			}
		})
	}
}

// TestToEnvKey verifies the model name to env key conversion.
func TestToEnvKey(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"glm-5.2", "GLM_5_2"},
		{"minimax-m3", "MINIMAX_M3"},
		{"gpt-4o", "GPT_4O"},
		{"claude-3-5-sonnet", "CLAUDE_3_5_SONNET"},
		{"model.with.dots", "MODEL_WITH_DOTS"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := toEnvKey(tt.model)
			if got != tt.want {
				t.Errorf("toEnvKey(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

// TestRecoveryHoldbackForModel_UnstableModelWithGlobalEnv verifies that
// unstable models use their hardcoded defaults even when global env is set,
// unless a model-specific env override exists.
func TestRecoveryHoldbackForModel_UnstableModelWithGlobalEnv(t *testing.T) {
	os.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS", "3000")
	defer os.Unsetenv("LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS")

	// Unstable model should use its hardcoded 10s/50, not the global 3s
	window, chunks := RecoveryHoldbackForModel("glm-5.2")
	if window != 10*time.Second {
		t.Errorf("glm-5.2 ignores global env: window = %v, want 10s", window)
	}
	if chunks != 50 {
		t.Errorf("glm-5.2 ignores global env: maxChunks = %d, want 50", chunks)
	}
}
