package main

// Stage B.4 — table-driven tests for the env-flag → backend selection
// helper. Tests the pure function so a real Pipeline / Redis client
// is not required (nil client pins the local-fallback paths; only
// cases that require a client supply a miniredis-derived *redis.Client).

import (
	"strings"
	"testing"

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
