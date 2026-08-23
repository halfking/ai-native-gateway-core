package admin

import (
	"testing"
	"time"
)

func TestConfigureLiveStreamInflightProtectFromEnv(t *testing.T) {
	t.Cleanup(func() { liveStreamInflightProtectDuration = 2 * time.Hour })

	t.Run("default unchanged when unset", func(t *testing.T) {
		t.Setenv("LLM_GATEWAY_LIVE_STREAM_INFLIGHT_PROTECT_SECONDS", "")
		ConfigureLiveStreamInflightProtectFromEnv()
		if got := liveStreamInflightProtectDuration; got != 2*time.Hour {
			t.Fatalf("duration = %s, want 2h", got)
		}
	})

	t.Run("valid override", func(t *testing.T) {
		t.Setenv("LLM_GATEWAY_LIVE_STREAM_INFLIGHT_PROTECT_SECONDS", "3600")
		ConfigureLiveStreamInflightProtectFromEnv()
		if got := liveStreamInflightProtectDuration; got != time.Hour {
			t.Fatalf("duration = %s, want 1h", got)
		}
	})

	t.Run("invalid ignored", func(t *testing.T) {
		liveStreamInflightProtectDuration = 2 * time.Hour
		t.Setenv("LLM_GATEWAY_LIVE_STREAM_INFLIGHT_PROTECT_SECONDS", "bad")
		ConfigureLiveStreamInflightProtectFromEnv()
		if got := liveStreamInflightProtectDuration; got != 2*time.Hour {
			t.Fatalf("duration = %s, want unchanged 2h", got)
		}
	})
}
