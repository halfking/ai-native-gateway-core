package compression

import (
	"testing"

	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// withPlatformFlag swaps settings.Global for a registry whose platform
// backend returns the provided value for flagKey, and restores Global on
// cleanup. Reuses the package-local fakeDBBackend (defined in
// compaction_models_test.go) which implements the full settings.Backend
// interface.
func withPlatformFlag(t *testing.T, flagKey string, val bool) {
	t.Helper()
	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })

	store := map[string][]byte{}
	if val {
		store[flagKey] = []byte("true")
	}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeDBBackend{store: store})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	registry.MustRegisterSpec(&settings.Spec{
		Key:   flagKey,
		Type:  settings.TypeBool,
		Scope: settings.ScopePlatform,
	})
	settings.Global = registry
}

// TestSessionCompressor_ShouldUseV2_FlagDisabled covers the default path:
// flag off (or unset) → V2 never activates even when components are wired.
func TestSessionCompressor_ShouldUseV2_FlagDisabled(t *testing.T) {
	// No flag injected → GetPlatformBool falls back to false.
	cacheV2 := v2.NewSessionCacheV2(nil, "")
	builder := v2.NewOutboundBuilder(v2.NewTurnReader(nil))

	sc := &SessionCompressor{deps: SessionCompressorDeps{
		CacheV2: cacheV2,
		Builder: builder,
	}}

	if sc.shouldUseV2("t1") {
		t.Fatal("expected shouldUseV2=false when flag is unset")
	}
}

// TestSessionCompressor_ShouldUseV2_FlagEnabled covers the previously
// untested branch: flag=true → shouldUseV2 returns true as long as both
// V2 components are non-nil. Before the audit fix this branch could never
// fire because the reader referenced a non-existent API.
func TestSessionCompressor_ShouldUseV2_FlagEnabled(t *testing.T) {
	withPlatformFlag(t, "sessions_v2_compression_read", true)

	cacheV2 := v2.NewSessionCacheV2(nil, "")
	builder := v2.NewOutboundBuilder(v2.NewTurnReader(nil))

	sc := &SessionCompressor{deps: SessionCompressorDeps{
		CacheV2: cacheV2,
		Builder: builder,
	}}

	if !sc.shouldUseV2("t1") {
		t.Fatal("expected shouldUseV2=true when flag is on and both V2 deps are wired")
	}
}

// TestSessionCompressor_ShouldUseV2_MissingComponents verifies that a
// missing component short-circuits even when the flag is on — the
// fail-safe ordering that prevents nil-deref in tryLoadV2State.
func TestSessionCompressor_ShouldUseV2_MissingComponents(t *testing.T) {
	withPlatformFlag(t, "sessions_v2_compression_read", true)

	t.Run("cacheV2 nil", func(t *testing.T) {
		sc := &SessionCompressor{deps: SessionCompressorDeps{
			CacheV2: nil,
			Builder: v2.NewOutboundBuilder(v2.NewTurnReader(nil)),
		}}
		if sc.shouldUseV2("t1") {
			t.Fatal("expected false when CacheV2 is nil")
		}
	})

	t.Run("builder nil", func(t *testing.T) {
		sc := &SessionCompressor{deps: SessionCompressorDeps{
			CacheV2: v2.NewSessionCacheV2(nil, ""),
			Builder: nil,
		}}
		if sc.shouldUseV2("t1") {
			t.Fatal("expected false when Builder is nil")
		}
	})
}

// TestSessionCompressor_ShouldUseV2_NilReceiver guards against a panic
// on a nil compressor (defensive; matches the check at the top of the fn).
func TestSessionCompressor_ShouldUseV2_NilReceiver(t *testing.T) {
	var sc *SessionCompressor
	if sc.shouldUseV2("t1") {
		t.Fatal("expected false for nil receiver")
	}
}
