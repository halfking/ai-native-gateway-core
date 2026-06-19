package admin

import "testing"

// 2026-06-19 audit: ensure /api/routing/available-models cache hit/miss
// and invalidation behave correctly so the previous "一会就被刷新" can
// not regress through a stale cache (or worse, stay sticky after a
// PATCH).  The cache lives in routing.go; this test exercises the
// exported helper hooks in the same package.
func TestAvailableModelsCacheLifecycle(t *testing.T) {
	InvalidateAvailableModelsCache()

	if _, ok := getAvailableModelsCacheForTest(); ok {
		t.Fatal("expected miss on empty cache")
	}

	payload := map[string]any{
		"families":  []any{},
		"popular":   []any{},
		"unmapped":  []any{},
		"total_raw": 0,
	}
	setAvailableModelsCacheForTest(payload)

	got, ok := getAvailableModelsCacheForTest()
	if !ok {
		t.Fatal("expected hit after set")
	}
	if got["total_raw"] != 0 {
		t.Fatalf("unexpected cached payload: %#v", got)
	}

	InvalidateAvailableModelsCache()
	if _, ok := getAvailableModelsCacheForTest(); ok {
		t.Fatal("expected miss after invalidate")
	}
}
