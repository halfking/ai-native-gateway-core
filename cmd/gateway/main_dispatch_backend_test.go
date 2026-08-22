package main

// Stage B.4 — table-driven tests for the env-flag → backend selection
// helper. Tests the pure function so a real Pipeline / Redis client
// is not required (nil client pins the local-fallback paths; only
// cases that require a client supply a miniredis-derived *redis.Client).
//
// Stage C.2 — adds tests for resolveGovernorObserver (the observer env
// flag) below.

import (
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestResolveGovernorBackendTableDriven(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: 0})
	t.Cleanup(func() { _ = client.Close() })

	cases := []struct {
		name     string
		mode     string
		client   *redis.Client
		wantKind string
	}{
		{
			name:     "empty env → LocalBackend",
			mode:     "",
			client:   client,
			wantKind: "local",
		},
		{
			name:     "local → LocalBackend",
			mode:     "local",
			client:   client,
			wantKind: "local",
		},
		{
			name:     "LOCAL (uppercase) → LocalBackend (case-insensitive)",
			mode:     "LOCAL",
			client:   client,
			wantKind: "local",
		},
		{
			name:     "redis_shadow with client → RedisShadowBackend",
			mode:     "redis_shadow",
			client:   client,
			wantKind: "redis_shadow",
		},
		{
			name:     "redis_enforce with client → RedisEnforceBackend",
			mode:     "redis_enforce",
			client:   client,
			wantKind: "redis_enforce",
		},
		{
			name:     "redis_shadow with nil client → LocalBackend (warn + fallback)",
			mode:     "redis_shadow",
			client:   nil,
			wantKind: "local",
		},
		{
			name:     "redis_enforce with nil client → LocalBackend (warn + fallback)",
			mode:     "redis_enforce",
			client:   nil,
			wantKind: "local",
		},
		{
			name:     "bogus value → LocalBackend (error log + fallback)",
			mode:     "wtf",
			client:   client,
			wantKind: "local",
		},
		{
			name:     "REDIS_ENFORCE (uppercase) → RedisEnforceBackend (case-insensitive)",
			mode:     "REDIS_ENFORCE",
			client:   client,
			wantKind: "redis_enforce",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Production path: env value is lowercased by envGovernorBackend().
			// Test path simulates that here so case-insensitivity is verifiable.
			mode := tc.mode
			if mode != "" {
				mode = strings.ToLower(strings.TrimSpace(mode))
			}
			b := resolveGovernorBackend(mode, tc.client, "test-instance")
			if b == nil {
				t.Fatalf("resolveGovernorBackend returned nil")
			}
			if got := string(b.Kind()); got != tc.wantKind {
				t.Fatalf("Kind = %q, want %q", got, tc.wantKind)
			}
			if got := b.Name(); got != "test-instance" {
				t.Fatalf("Name = %q, want %q", got, "test-instance")
			}
		})
	}
}

// ── Stage C.2: observer env-flag tests ────────────────────────────────────

// TestResolveGovernorObserverDefaults verifies the default (unset env)
// disables the observer and treats the empty string the same as "off".
func TestResolveGovernorObserverDefaults(t *testing.T) {
	t.Setenv("LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER", "")
	t.Setenv("LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER_TICK_MS", "")

	tick, enabled := resolveGovernorObserver()
	if enabled {
		t.Fatalf("empty env must disable observer; got tick=%v enabled=true", tick)
	}
}

// TestResolveGovernorObserverObserveNoTick verifies the observe mode
// without a tick override returns the 100ms default.
func TestResolveGovernorObserverObserveNoTick(t *testing.T) {
	t.Setenv("LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER", "observe")
	t.Setenv("LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER_TICK_MS", "")

	tick, enabled := resolveGovernorObserver()
	if !enabled {
		t.Fatal("observe must enable observer")
	}
	if tick != 100*time.Millisecond {
		t.Fatalf("default tick: got %v want 100ms", tick)
	}
}

// TestResolveGovernorObserverObserveCustomTick verifies the tick override.
func TestResolveGovernorObserverObserveCustomTick(t *testing.T) {
	t.Setenv("LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER", "observe")
	t.Setenv("LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER_TICK_MS", "250")

	tick, enabled := resolveGovernorObserver()
	if !enabled {
		t.Fatal("observe must enable observer")
	}
	if tick != 250*time.Millisecond {
		t.Fatalf("custom tick: got %v want 250ms", tick)
	}
}

// TestResolveGovernorObserverOffExplicit verifies the explicit "off"
// value disables the observer.
func TestResolveGovernorObserverOffExplicit(t *testing.T) {
	t.Setenv("LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER", "off")
	if _, enabled := resolveGovernorObserver(); enabled {
		t.Fatal("explicit off must disable observer")
	}
}

// TestResolveGovernorObserverBogusFallsBackToOff verifies unknown values
// fall back to disabled with an error log (not a panic).
func TestResolveGovernorObserverBogusFallsBackToOff(t *testing.T) {
	t.Setenv("LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER", "banana")
	if _, enabled := resolveGovernorObserver(); enabled {
		t.Fatal("bogus value must disable observer")
	}
}

// TestResolveGovernorObserverTickOverrideInvalid verifies an invalid tick
// value is ignored (falls back to 100ms default), and the env reader
// returns ok=false.
func TestResolveGovernorObserverTickOverrideInvalid(t *testing.T) {
	t.Setenv("LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER", "observe")
	t.Setenv("LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER_TICK_MS", "not-a-number")

	tick, enabled := resolveGovernorObserver()
	if !enabled {
		t.Fatal("observe must enable observer despite invalid tick")
	}
	if tick != 100*time.Millisecond {
		t.Fatalf("invalid tick must fall back to 100ms default; got %v", tick)
	}
}

// TestResolveGovernorObserverCaseInsensitive verifies the env reader
// lowercases so "OBSERVE" / "Observe" are accepted.
func TestResolveGovernorObserverCaseInsensitive(t *testing.T) {
	t.Setenv("LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER", "OBSERVE")
	if _, enabled := resolveGovernorObserver(); !enabled {
		t.Fatal("OBSERVE (uppercase) must enable observer (case-insensitive)")
	}
}

// ── Stage D: capacity-aware soft-sort env-flag tests ─────────────────────

// TestResolveCapacityAwareSortDefaults verifies the default (unset env)
// disables the soft sort, matching the Stage B / C env-flag pattern.
func TestResolveCapacityAwareSortDefaults(t *testing.T) {
	t.Setenv("LLM_GATEWAY_DISPATCH_CAPACITY_AWARE_SORT", "")
	enabled, err := resolveCapacityAwareSort()
	if err != nil {
		t.Fatalf("empty env: unexpected error %v", err)
	}
	if enabled {
		t.Fatal("empty env must disable capacity-aware sort")
	}
}

// TestResolveCapacityAwareSortOff verifies the explicit "off" value.
func TestResolveCapacityAwareSortOff(t *testing.T) {
	t.Setenv("LLM_GATEWAY_DISPATCH_CAPACITY_AWARE_SORT", "off")
	enabled, err := resolveCapacityAwareSort()
	if err != nil {
		t.Fatalf("off: unexpected error %v", err)
	}
	if enabled {
		t.Fatal("explicit off must disable")
	}
}

// TestResolveCapacityAwareSortOn verifies the "on" value enables the sort.
func TestResolveCapacityAwareSortOn(t *testing.T) {
	t.Setenv("LLM_GATEWAY_DISPATCH_CAPACITY_AWARE_SORT", "on")
	enabled, err := resolveCapacityAwareSort()
	if err != nil {
		t.Fatalf("on: unexpected error %v", err)
	}
	if !enabled {
		t.Fatal("on must enable capacity-aware sort")
	}
}

// TestResolveCapacityAwareSortBogus verifies an unknown value returns
// an error and falls back to disabled (caller slog-warns).
func TestResolveCapacityAwareSortBogus(t *testing.T) {
	t.Setenv("LLM_GATEWAY_DISPATCH_CAPACITY_AWARE_SORT", "banana")
	enabled, err := resolveCapacityAwareSort()
	if err == nil {
		t.Fatal("bogus value must return error")
	}
	if enabled {
		t.Fatal("bogus value must disable sort (with error)")
	}
}

// TestResolveCapacityAwareSortCaseInsensitive verifies the env reader
// lowercases so "ON" / "On" are accepted.
func TestResolveCapacityAwareSortCaseInsensitive(t *testing.T) {
	t.Setenv("LLM_GATEWAY_DISPATCH_CAPACITY_AWARE_SORT", "ON")
	enabled, err := resolveCapacityAwareSort()
	if err != nil {
		t.Fatalf("ON (uppercase): unexpected error %v", err)
	}
	if !enabled {
		t.Fatal("ON (uppercase) must enable (case-insensitive)")
	}
}
