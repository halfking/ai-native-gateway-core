package store

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newPreloadTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func TestPreloadScripts_Success(t *testing.T) {
	rdb := newPreloadTestRedis(t)

	shas, err := PreloadScripts(context.Background(), rdb)
	if err != nil {
		t.Fatalf("PreloadScripts failed: %v", err)
	}

	if len(shas) != 9 {
		t.Fatalf("expected 9 script SHAs, got %d", len(shas))
	}
	for name, sha := range shas {
		if sha == "" {
			t.Errorf("script %s returned empty SHA", name)
		}
	}

	// Verify the scripts are actually cached in Redis
	keys := make([]string, 0, len(shas))
	for _, sha := range shas {
		keys = append(keys, sha)
	}
	exists, err := rdb.ScriptExists(context.Background(), keys...).Result()
	if err != nil {
		t.Fatalf("ScriptExists check failed: %v", err)
	}
	for i, ok := range exists {
		if !ok {
			t.Errorf("script %s (SHA %s) not cached in Redis", keys[i], keys[i][:8])
		}
	}
}

func TestPreloadScripts_NilRedis(t *testing.T) {
	if _, err := PreloadScripts(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil redis client")
	}
}

func TestPreloadScripts_AfterScriptFlush(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close() //nolint:errcheck // test cleanup
	ctx := context.Background()

	// Preload once, flush, preload again — the reload path must succeed
	// (this is the exact scenario production hits after a SCRIPT FLUSH).
	if _, err := PreloadScripts(ctx, rdb); err != nil {
		t.Fatalf("initial preload: %v", err)
	}
	// miniredis has no ScriptFlush helper; drop the script cache via the
	// client command. If unsupported, the preload below still validates the
	// idempotent re-load path.
	_ = rdb.ScriptFlush(ctx).Err()
	shas, err := PreloadScripts(ctx, rdb)
	if err != nil {
		t.Fatalf("re-preload after flush: %v", err)
	}
	if len(shas) != 9 {
		t.Fatalf("expected 9 SHAs after re-preload, got %d", len(shas))
	}
}

func TestScriptSizes(t *testing.T) {
	sizes := ScriptSizes()

	expectedScripts := []string{
		"record_request.lua",
		"record_request_dual.lua",
		"apply_decision.lua",
		"apply_admin.lua",
		"apply_admin_dual.lua",
		"apply_probe.lua",
		"apply_probe_dual.lua",
		"clear_state.lua",
		"clear_state_dual.lua",
	}

	if len(sizes) != len(expectedScripts) {
		t.Fatalf("expected %d script sizes, got %d", len(expectedScripts), len(sizes))
	}

	for _, name := range expectedScripts {
		size, ok := sizes[name]
		if !ok {
			t.Errorf("missing size for script %q", name)
			continue
		}
		if size <= 0 {
			t.Errorf("script %q has invalid size %d", name, size)
		}
	}

	// The two hot-path monsters that motivated preloading (audit data):
	// any regression here changes the fallback network cost.
	if s := sizes["record_request.lua"]; s != 11469 {
		t.Errorf("record_request.lua size = %d, want 11469 (update if script changed intentionally)", s)
	}
	if s := sizes["record_request_dual.lua"]; s != 10437 {
		t.Errorf("record_request_dual.lua size = %d, want 10437 (update if script changed intentionally)", s)
	}
}
