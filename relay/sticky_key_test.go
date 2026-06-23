package relay

import "testing"

func TestBuildRouteStickyKeyIncludesClientModel(t *testing.T) {
	appID := 1
	keyID := 2
	base := buildRouteStickyKey("tenant-a", &appID, &keyID, "cursor", "sess-1", "user-1", "fp-seed", "glm-4-flash")
	other := buildRouteStickyKey("tenant-a", &appID, &keyID, "cursor", "sess-1", "user-1", "fp-seed", "gpt-4o")
	if base == other {
		t.Fatalf("expected different sticky keys for different client models, got %q", base)
	}
	if base == "" || other == "" {
		t.Fatal("sticky keys must be non-empty")
	}
}

// TestBuildRouteStickyKey_StableWithoutSession verifies that
// without a session ID, the sticky key is still client-scoped
// (tenant+app+key+profile+model) and identical across calls —
// endUser and fpSeed no longer influence the key. This is the
// post-refactor behavior; the previous fallback to endUser/fpHash
// was removed to prevent slot fragmentation.
func TestBuildRouteStickyKey_StableWithoutSession(t *testing.T) {
	appID := 1
	keyID := 2
	k1 := buildRouteStickyKey("tenant-a", &appID, &keyID, "cursor", "", "user-1", "fp-seed-1", "glm-4-flash")
	k2 := buildRouteStickyKey("tenant-a", &appID, &keyID, "cursor", "", "user-2", "fp-seed-2", "glm-4-flash")
	if k1 != k2 {
		t.Fatalf("without session id, sticky key must be stable across endUser/fpSeed changes: %q vs %q", k1, k2)
	}
}

// TestBuildRouteStickyKey_ModelVariesWithoutSession verifies that
// even without a session ID, different client models still produce
// different sticky keys (so each model gets its own fingerprint slot).
func TestBuildRouteStickyKey_ModelVariesWithoutSession(t *testing.T) {
	appID := 1
	keyID := 2
	k1 := buildRouteStickyKey("tenant-a", &appID, &keyID, "cursor", "", "user-1", "fp-seed", "glm-4-flash")
	k2 := buildRouteStickyKey("tenant-a", &appID, &keyID, "cursor", "", "user-1", "fp-seed", "gpt-4o")
	if k1 == k2 {
		t.Fatalf("different client models must produce different sticky keys even without session: %q", k1)
	}
}

func TestBuildRouteStickyKey_EmptyModelMatchesDefault(t *testing.T) {
	appID := 1
	keyID := 2
	empty := buildRouteStickyKey("tenant-a", &appID, &keyID, "cursor", "sess-1", "user-1", "fp-seed", "")
	defaultKey := buildRouteStickyKey("tenant-a", &appID, &keyID, "cursor", "sess-1", "user-1", "fp-seed", "default")
	if empty != defaultKey {
		t.Fatalf("empty client model must fall back to 'default' bucket: %q vs %q", empty, defaultKey)
	}
}

func TestBuildRouteStickyKey_TrimAndLowercase(t *testing.T) {
	appID := 1
	keyID := 2
	a := buildRouteStickyKey("tenant-a", &appID, &keyID, "cursor", "sess-1", "user-1", "fp-seed", " GLM-4-Flash ")
	b := buildRouteStickyKey("tenant-a", &appID, &keyID, "cursor", "sess-1", "user-1", "fp-seed", "glm-4-flash")
	if a != b {
		t.Fatalf("client model must be normalized (trim+lowercase): %q vs %q", a, b)
	}
}

func TestBuildRouteStickyKey_ApiKeyIsolation(t *testing.T) {
	appID := 1
	keyA := 10
	keyB := 20
	a := buildRouteStickyKey("tenant-a", &appID, &keyA, "cursor", "sess-1", "user-1", "fp-seed", "glm-4-flash")
	b := buildRouteStickyKey("tenant-a", &appID, &keyB, "cursor", "sess-1", "user-1", "fp-seed", "glm-4-flash")
	if a == b {
		t.Fatalf("different apiKeyID must yield different sticky keys to prevent cross-tenant credential pinning: %q", a)
	}
}
