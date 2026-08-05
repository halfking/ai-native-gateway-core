// Package compression - session_cache_ttl_test.go
//
// Tests for the hot-reloadable Redis TTL and L1 capacity settings
// (cache.session_redis_ttl_minutes and cache.session_l1_capacity).

package compression

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// fakeIntBackend is a minimal settings backend that serves integers from a map.
type fakeIntBackend struct {
	store map[string][]byte
}

func (f *fakeIntBackend) Get(scope settings.Scope, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *fakeIntBackend) Set(scope settings.Scope, key string, value any) ([]byte, error) {
	return nil, nil
}
func (f *fakeIntBackend) GetTenant(tenantID, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *fakeIntBackend) SetTenant(tenantID, key string, value any) ([]byte, error) {
	return nil, nil
}

// registerCacheTTLSpecs registers only the two cache specs needed for these tests.
func registerCacheTTLSpecs(t *testing.T, registry *settings.Registry) {
	t.Helper()
	for _, sp := range settings.CompressionSpecs() {
		if sp.Key == "cache.session_redis_ttl_minutes" || sp.Key == "cache.session_l1_capacity" {
			registry.MustRegisterSpec(sp)
		}
	}
}

func TestSessionCacheRedisTTLHotReload(t *testing.T) {
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	store := map[string][]byte{
		"cache.session_redis_ttl_minutes": []byte("45"),
	}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeIntBackend{store: store})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	registerCacheTTLSpecs(t, registry)
	settings.Global = registry

	// First call should see 45 minutes.
	got := sessionCacheRedisTTL()
	if got != 45*time.Minute {
		t.Fatalf("sessionCacheRedisTTL() = %v, want 45m", got)
	}

	// Hot-reload: change value in the store and call again.
	store["cache.session_redis_ttl_minutes"] = []byte("60")
	got = sessionCacheRedisTTL()
	if got != 60*time.Minute {
		t.Fatalf("after hot-reload, sessionCacheRedisTTL() = %v, want 60m", got)
	}
}

func TestSessionCacheL1CapacityHotReload(t *testing.T) {
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	store := map[string][]byte{
		"cache.session_l1_capacity": []byte("512"),
	}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeIntBackend{store: store})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	registerCacheTTLSpecs(t, registry)
	settings.Global = registry

	if got := sessionCacheL1Capacity(); got != 512 {
		t.Fatalf("sessionCacheL1Capacity() = %d, want 512", got)
	}

	// Hot-reload.
	store["cache.session_l1_capacity"] = []byte("2048")
	if got := sessionCacheL1Capacity(); got != 2048 {
		t.Fatalf("after hot-reload, sessionCacheL1Capacity() = %d, want 2048", got)
	}
}

func TestSessionCacheRedisTTLClamp(t *testing.T) {
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	// TTL set to 2 minutes, below the 5-minute floor.
	store := map[string][]byte{
		"cache.session_redis_ttl_minutes": []byte("2"),
	}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeIntBackend{store: store})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	registerCacheTTLSpecs(t, registry)
	settings.Global = registry

	got := sessionCacheRedisTTL()
	if got != 5*time.Minute {
		t.Fatalf("sessionCacheRedisTTL() with value=2 = %v, want 5m (clamped)", got)
	}
}

func TestSessionCacheRedisTTLFallback(t *testing.T) {
	// When Global is nil, sessionCacheRedisTTL should return the built-in default.
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })
	settings.Global = nil

	got := sessionCacheRedisTTL()
	if got != redisKeyTTL {
		t.Fatalf("sessionCacheRedisTTL() with nil Global = %v, want %v (built-in default)", got, redisKeyTTL)
	}
}

func TestSessionCacheL1CapacityFallback(t *testing.T) {
	// When Global is nil, sessionCacheL1Capacity should return the built-in default.
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })
	settings.Global = nil

	got := sessionCacheL1Capacity()
	if got != l1MaxSessions {
		t.Fatalf("sessionCacheL1Capacity() with nil Global = %d, want %d (built-in default)", got, l1MaxSessions)
	}
}
