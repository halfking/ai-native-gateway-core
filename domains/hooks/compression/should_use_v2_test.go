package compression

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// newCompressorWithV2Deps builds a SessionCompressor whose V2 components are
// non-nil (so shouldUseV2's component guard passes), isolating the flag logic.
// NewSessionCacheV2(nil,"") and NewOutboundBuilder(nil) are nil-pool-safe per
// their own constructors; we only need them non-nil here.
func newCompressorWithV2Deps() *SessionCompressor {
	return NewSessionCompressor(SessionCompressorDeps{
		CacheV2: v2.NewSessionCacheV2(nil, ""),
		Builder: v2.NewOutboundBuilder(nil),
	})
}

// withGlobal swaps settings.Global for the test and restores it on cleanup.
func withGlobal(t *testing.T, r *settings.Registry) {
	t.Helper()
	old := settings.Global
	t.Cleanup(func() { settings.Global = old })
	settings.Global = r
}

// TestShouldUseV2_DefaultOn is the A1 decision: with no setting registered,
// the flag falls back to TRUE, so V2 reads are on by default. The kill-switch
// is exercised by TestShouldUseV2_KillSwitchFalse.
func TestShouldUseV2_DefaultOn(t *testing.T) {
	// Global == nil ⇒ GetPlatformBool returns the fallback (true).
	withGlobal(t, nil)
	sc := newCompressorWithV2Deps()
	if !sc.shouldUseV2("any-tenant") {
		t.Fatal("shouldUseV2 = false with components present and default-on; want true (A1 decision)")
	}
}

// TestShouldUseV2_KillSwitchFalse verifies the kill-switch: when the platform
// flag is explicitly false, reads revert to V1 immediately (no redeploy).
func TestShouldUseV2_KillSwitchFalse(t *testing.T) {
	r := settings.NewRegistry()
	// Register the real spec so Global.Spec(key) resolves and the backend value
	// is consulted (otherwise getPlatformBool short-circuits to its fallback).
	for _, s := range settings.SessionsV2CompressionPlatformSpecs() {
		r.MustRegisterSpec(s)
	}
	// JSON bool false — what getPlatformBool unmarshals.
	r.RegisterBackend(settings.ScopePlatform, flagBackend{"sessions_v2_compression_read": []byte("false")})
	withGlobal(t, r)

	sc := newCompressorWithV2Deps()
	if sc.shouldUseV2("any-tenant") {
		t.Fatal("shouldUseV2 = true with kill-switch false; want false")
	}
}

// TestShouldUseV2_FlagTrue confirms an explicit true is honoured (not just the
// default), i.e. the DB/backend path round-trips through the spec correctly.
func TestShouldUseV2_FlagTrue(t *testing.T) {
	r := settings.NewRegistry()
	for _, s := range settings.SessionsV2CompressionPlatformSpecs() {
		r.MustRegisterSpec(s)
	}
	r.RegisterBackend(settings.ScopePlatform, flagBackend{"sessions_v2_compression_read": []byte("true")})
	withGlobal(t, r)

	sc := newCompressorWithV2Deps()
	if !sc.shouldUseV2("any-tenant") {
		t.Fatal("shouldUseV2 = false with explicit true; want true")
	}
}

// TestShouldUseV2_NilComponentsGuards asserts that even with the flag default-on,
// missing V2 components force V1 — the buildV2PipelineHooks wiring must be
// present for V2 reads to activate.
func TestShouldUseV2_NilComponentsGuards(t *testing.T) {
	withGlobal(t, nil)                                  // default-on
	sc := NewSessionCompressor(SessionCompressorDeps{}) // CacheV2 == nil, Builder == nil
	if sc.shouldUseV2("any-tenant") {
		t.Fatal("shouldUseV2 = true with nil V2 components; want false (component guard failed)")
	}
}

// TestShouldUseV2_NilReceiver guards the nil-safety contract.
func TestShouldUseV2_NilReceiver(t *testing.T) {
	var sc *SessionCompressor
	if sc.shouldUseV2("any-tenant") {
		t.Fatal("nil receiver shouldUseV2 = true; want false")
	}
}

// flagBackend is a minimal settings backend that returns canned JSON bytes for
// the keys it knows about and errors otherwise (matching how getPlatformBool
// treats unknowns).
type flagBackend map[string][]byte

func (f flagBackend) Get(_ settings.Scope, key string) ([]byte, error) {
	if v, ok := f[key]; ok {
		return v, nil
	}
	return nil, errMissing
}
func (f flagBackend) Set(_ settings.Scope, _ string, _ any) ([]byte, error) { return nil, errMissing }
func (f flagBackend) GetTenant(_, _ string) ([]byte, error)                 { return nil, errMissing }
func (f flagBackend) SetTenant(_, _ string, _ any) ([]byte, error)          { return nil, errMissing }

var errMissing = &missingErr{}

type missingErr struct{}

func (e *missingErr) Error() string { return "flag not set" }
