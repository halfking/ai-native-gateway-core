package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// PreloadScripts SCRIPT LOADs every embedded URSM Lua script into Redis so
// the first EVALSHA hit succeeds instead of falling back to EVAL (which
// ships the full source — record_request.lua is 11,469 bytes per fallback).
//
// 2026-08-27 P1 (Redis audit sess_01JGHDCWSZQZS8D9D5H41A7VWN): Redis
// commandstats showed a 3.4% EVALSHA failure rate driven by script-cache
// misses after SCRIPT FLUSH / restarts. Call this once at gateway startup.
//
// Failure is non-fatal: go-redis Script.Run transparently re-uploads on
// NOSCRIPT, so preloading is a pure optimization — callers log-and-continue
// on error (cmd/gateway/main.go sets llmgw_redis_lua_script_preloaded{module="ursm}=0).
//
// The SHA1 of a script is a pure function of its source, so callers never
// need the returned SHAs: redis.NewScript computes the same SHA internally.
// The map return exists for logging/diagnostics.
func PreloadScripts(ctx context.Context, rdb *redis.Client) (map[string]string, error) {
	if rdb == nil {
		return nil, fmt.Errorf("redis client is nil")
	}

	scripts := map[string]string{
		"record_request.lua":      recordRequestSrc,
		"record_request_dual.lua": recordRequestDualSrc,
		"apply_decision.lua":      applyDecisionSrc,
		"apply_admin.lua":         applyAdminSrc,
		"apply_admin_dual.lua":    applyAdminDualSrc,
		"apply_probe.lua":         applyProbeSrc,
		"apply_probe_dual.lua":    applyProbeDualSrc,
		"clear_state.lua":         clearStateSrc,
		"clear_state_dual.lua":    clearStateDualSrc,
	}

	shas := make(map[string]string, len(scripts))
	start := time.Now()
	for name, src := range scripts {
		sha, err := rdb.ScriptLoad(ctx, src).Result()
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", name, err)
		}
		shas[name] = sha
	}

	slog.Info("ursm: lua scripts preloaded",
		"count", len(shas),
		"elapsed_ms", time.Since(start).Milliseconds())
	return shas, nil
}

// ScriptSizes returns the byte sizes of all embedded Lua scripts, for the
// llmgw_redis_lua_script_size_bytes gauge (fallback cost = size × rate).
func ScriptSizes() map[string]int {
	return map[string]int{
		"record_request.lua":      len(recordRequestSrc),
		"record_request_dual.lua": len(recordRequestDualSrc),
		"apply_decision.lua":      len(applyDecisionSrc),
		"apply_admin.lua":         len(applyAdminSrc),
		"apply_admin_dual.lua":    len(applyAdminDualSrc),
		"apply_probe.lua":         len(applyProbeSrc),
		"apply_probe_dual.lua":    len(applyProbeDualSrc),
		"clear_state.lua":         len(clearStateSrc),
		"clear_state_dual.lua":    len(clearStateDualSrc),
	}
}
