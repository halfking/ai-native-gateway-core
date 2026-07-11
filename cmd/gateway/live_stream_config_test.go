package main

import (
	"testing"
	"time"
)

func TestPositiveDurationEnv(t *testing.T) {
	const key = "LLM_GATEWAY_TEST_POSITIVE_DURATION"
	for _, testCase := range []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "missing", want: 15 * time.Second},
		{name: "valid", value: "45s", want: 45 * time.Second},
		{name: "zero", value: "0s", want: 15 * time.Second},
		{name: "negative", value: "-1s", want: 15 * time.Second},
		{name: "invalid", value: "bad", want: 15 * time.Second},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(key, testCase.value)
			if got := positiveDurationEnv(key, 15*time.Second); got != testCase.want {
				t.Fatalf("positiveDurationEnv(%q) = %s, want %s", testCase.value, got, testCase.want)
			}
		})
	}
}

func TestLiveStreamCachedDurationsFromEnv(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("LLM_GATEWAY_LIVE_STREAM_CACHED_TTL", "")
		t.Setenv("LLM_GATEWAY_LIVE_STREAM_CACHED_CLEANUP_INTERVAL", "")
		ttl, cleanup := liveStreamCachedDurationsFromEnv()
		if ttl != 10*time.Minute || cleanup != ttl {
			t.Fatalf("expected defaults ttl=cleanup=10m, got ttl=%s cleanup=%s", ttl, cleanup)
		}
	})

	t.Run("cleanup_follows_ttl_when_not_overridden", func(t *testing.T) {
		t.Setenv("LLM_GATEWAY_LIVE_STREAM_CACHED_TTL", "45s")
		t.Setenv("LLM_GATEWAY_LIVE_STREAM_CACHED_CLEANUP_INTERVAL", "")
		ttl, cleanup := liveStreamCachedDurationsFromEnv()
		if ttl != 45*time.Second || cleanup != ttl {
			t.Fatalf("expected ttl=cleanup=45s, got ttl=%s cleanup=%s", ttl, cleanup)
		}
	})

	t.Run("cleanup_explicitly_overrides_ttl", func(t *testing.T) {
		t.Setenv("LLM_GATEWAY_LIVE_STREAM_CACHED_TTL", "45s")
		t.Setenv("LLM_GATEWAY_LIVE_STREAM_CACHED_CLEANUP_INTERVAL", "5s")
		ttl, cleanup := liveStreamCachedDurationsFromEnv()
		if ttl != 45*time.Second || cleanup != 5*time.Second {
			t.Fatalf("expected ttl=45s cleanup=5s, got ttl=%s cleanup=%s", ttl, cleanup)
		}
	})
}
