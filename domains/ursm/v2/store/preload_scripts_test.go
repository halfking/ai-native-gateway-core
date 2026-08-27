package store

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestPreloadScripts_Success(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	ctx := context.Background()
	loaded, err := PreloadScripts(ctx, rdb)
	if err != nil {
		t.Fatalf("PreloadScripts failed: %v", err)
	}

	if loaded == nil {
		t.Fatal("expected non-nil LoadedScripts")
	}

	// Verify all SHAs are non-empty
	if loaded.recordRequestSHA == "" {
		t.Error("recordRequestSHA is empty")
	}
	if loaded.recordRequestDualSHA == "" {
		t.Error("recordRequestDualSHA is empty")
	}
	if loaded.applyDecisionSHA == "" {
		t.Error("applyDecisionSHA is empty")
	}

	// Verify scripts are actually loaded in Redis (miniredis supports SCRIPT EXISTS)
	exists, err := rdb.ScriptExists(ctx, loaded.recordRequestSHA).Result()
	if err != nil {
		t.Fatalf("ScriptExists check failed: %v", err)
	}
	if len(exists) == 0 || !exists[0] {
		t.Error("recordRequestSHA not found in Redis after preload")
	}
}

func TestPreloadScripts_NilRedis(t *testing.T) {
	ctx := context.Background()
	_, err := PreloadScripts(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil redis client")
	}
}

func TestScriptSizes(t *testing.T) {
	sizes := ScriptSizes()

	// Verify we have sizes for all 9 scripts
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

	// Verify the two largest scripts are record_request*.lua
	recordSize := sizes["record_request.lua"]
	recordDualSize := sizes["record_request_dual.lua"]

	if recordSize < 10000 {
		t.Errorf("record_request.lua size %d seems too small (expected >10KB)", recordSize)
	}
	if recordDualSize < 10000 {
		t.Errorf("record_request_dual.lua size %d seems too small (expected >10KB)", recordDualSize)
	}

	t.Logf("Script sizes: record_request=%d bytes, record_request_dual=%d bytes",
		recordSize, recordDualSize)
}

func TestPreloadScripts_Integration(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	ctx := context.Background()

	// Preload scripts
	loaded, err := PreloadScripts(ctx, rdb)
	if err != nil {
		t.Fatalf("PreloadScripts failed: %v", err)
	}

	// Verify EVALSHA works without fallback
	// (miniredis doesn't fully emulate EVALSHA, but we can verify the SHA was stored)
	shaList := []string{
		loaded.recordRequestSHA,
		loaded.recordRequestDualSHA,
		loaded.applyDecisionSHA,
	}

	exists, err := rdb.ScriptExists(ctx, shaList...).Result()
	if err != nil {
		t.Fatalf("ScriptExists check failed: %v", err)
	}

	for i, sha := range shaList {
		if !exists[i] {
			t.Errorf("SHA %s not found in Redis", sha[:8])
		}
	}
}
