package streaming

import (
	"testing"
	"time"
)

func TestRecoveryHoldbackFromEnvDisabledByDefault(t *testing.T) {
	t.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS", "")
	t.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS", "")

	window, chunks := RecoveryHoldbackFromEnv()
	if window != 0 || chunks != 0 {
		t.Fatalf("unset holdback env = (%s, %d), want disabled", window, chunks)
	}
}

func TestRecoveryHoldbackFromEnvRejectsInvalidWindow(t *testing.T) {
	for _, value := range []string{"0", "-1", "not-a-number", "9223372036854775807"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS", value)
			t.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS", "5")

			window, chunks := RecoveryHoldbackFromEnv()
			if window != 0 || chunks != 0 {
				t.Fatalf("window=%q parsed as (%s, %d), want disabled", value, window, chunks)
			}
		})
	}
}

func TestRecoveryHoldbackFromEnvUsesConfiguredValues(t *testing.T) {
	t.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS", "5000")
	t.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS", "7")

	window, chunks := RecoveryHoldbackFromEnv()
	if window != 5*time.Second || chunks != 7 {
		t.Fatalf("configured holdback = (%s, %d), want (5s, 7)", window, chunks)
	}
}

func TestRecoveryHoldbackFromEnvUsesSafeChunkDefault(t *testing.T) {
	t.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS", "5000")
	t.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS", "-3")

	window, chunks := RecoveryHoldbackFromEnv()
	if window != 5*time.Second || chunks != DefaultHoldbackMaxChunks {
		t.Fatalf("invalid chunk cap = (%s, %d), want (5s, %d)", window, chunks, DefaultHoldbackMaxChunks)
	}
}
