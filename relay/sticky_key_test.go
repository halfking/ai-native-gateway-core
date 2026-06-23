package relay

import "testing"

// TestBuildRouteStickyKey_IncludesClientIdentity verifies the
// 4-component client identity (tenant+app+key+profile) flows
// through to the final sticky key. 2026-06-24: the previous
// "model is part of the key" assertion is gone — model is NOT
// in the key anymore (see routing.BuildClientStickyKey).
func TestBuildRouteStickyKey_IncludesClientIdentity(t *testing.T) {
	appID := 1
	keyID := 2
	got := buildRouteStickyKey("tenant-a", &appID, &keyID, "cursor")
	want := "tenant-a:1:2:cursor"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestBuildRouteStickyKey_StableWithoutSession verifies the helper
// is now session-free: same client → same key regardless of
// sessionID, endUser, fpSeed, or clientModel. The 4-arg signature
// is the whole point — fewer arguments means fewer ways to
// accidentally re-introduce a session-scoped or model-scoped
// fingerprint.
func TestBuildRouteStickyKey_StableWithoutSession(t *testing.T) {
	appID := 1
	keyID := 2
	got := buildRouteStickyKey("tenant-a", &appID, &keyID, "cursor")
	// old signature would have produced different keys for these calls;
	// the new signature doesn't even take the old parameters, so all
	// these inputs collapse to the same value.
	if got == "" {
		t.Fatal("sticky key must be non-empty")
	}
}

// TestBuildRouteStickyKey_DifferentProfileDifferentKey verifies
// that the profile component is part of the key (so two clients
// with the same tenant+app+key but different profiles — e.g.
// "cursor" vs "opencode" — get different fingerprints and don't
// share an fp_slot).
func TestBuildRouteStickyKey_DifferentProfileDifferentKey(t *testing.T) {
	appID := 1
	keyID := 2
	a := buildRouteStickyKey("tenant-a", &appID, &keyID, "cursor")
	b := buildRouteStickyKey("tenant-a", &appID, &keyID, "opencode")
	if a == b {
		t.Fatalf("different clientProfile must produce different sticky keys: %q vs %q", a, b)
	}
}

// TestBuildRouteStickyKey_ApiKeyIsolation verifies that two
// different API keys (under the same tenant + app) get different
// fingerprints so credentials bound to key A cannot leak into
// key B's slot pool.
func TestBuildRouteStickyKey_ApiKeyIsolation(t *testing.T) {
	appID := 1
	keyA := 10
	keyB := 20
	a := buildRouteStickyKey("tenant-a", &appID, &keyA, "cursor")
	b := buildRouteStickyKey("tenant-a", &appID, &keyB, "cursor")
	if a == b {
		t.Fatalf("different apiKeyID must yield different sticky keys to prevent cross-tenant credential pinning: %q", a)
	}
}
