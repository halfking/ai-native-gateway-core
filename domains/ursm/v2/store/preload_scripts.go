package store

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"
)

// LoadedScripts holds the SHA1 hashes of all preloaded URSM Lua scripts.
// Preloading scripts at startup reduces EVALSHA fallback overhead when Redis
// script cache is cold (e.g., after SCRIPT FLUSH or Redis restart).
//
// Design: go-redis Script.Run() internally uses EVALSHA with automatic EVAL
// fallback on NOSCRIPT errors. Explicit preloading ensures the first call
// hits the SHA path, avoiding the 11 KB transmission of record_request*.lua
// on cold start.
type LoadedScripts struct {
	recordRequestSHA     string
	recordRequestDualSHA string
	applyDecisionSHA     string
	applyAdminSHA        string
	applyAdminDualSHA    string
	applyProbeSHA        string
	applyProbeDualSHA    string
	clearStateSHA        string
	clearStateDualSHA    string
}

// PreloadScripts loads all URSM Lua scripts into Redis and returns their SHA1
// hashes. This should be called once at gateway startup before serving traffic.
//
// If preloading fails (e.g., Redis unavailable), it logs a warning and returns
// an error, but the gateway can still operate — go-redis Script.Run() will
// automatically upload scripts on first use via the EVAL fallback path.
//
// Usage:
//
//	if loaded, err := store.PreloadScripts(ctx, redisClient); err != nil {
//	    slog.Warn("ursm script preload failed, will use EVAL fallback", "error", err)
//	} else {
//	    slog.Info("ursm scripts preloaded", "count", 9)
//	}
func PreloadScripts(ctx context.Context, rdb *redis.Client) (*LoadedScripts, error) {
	if rdb == nil {
		return nil, fmt.Errorf("redis client is nil")
	}

	scripts := []struct {
		name   string
		source string
		dest   *string
	}{
		{"record_request", recordRequestSrc, new(string)},
		{"record_request_dual", recordRequestDualSrc, new(string)},
		{"apply_decision", applyDecisionSrc, new(string)},
		{"apply_admin", applyAdminSrc, new(string)},
		{"apply_admin_dual", applyAdminDualSrc, new(string)},
		{"apply_probe", applyProbeSrc, new(string)},
		{"apply_probe_dual", applyProbeDualSrc, new(string)},
		{"clear_state", clearStateSrc, new(string)},
		{"clear_state_dual", clearStateDualSrc, new(string)},
	}

	loaded := &LoadedScripts{}
	for i, script := range scripts {
		sha, err := rdb.ScriptLoad(ctx, script.source).Result()
		if err != nil {
			return nil, fmt.Errorf("failed to load script %q: %w", script.name, err)
		}
		*script.dest = sha

		// Assign to struct fields
		switch i {
		case 0:
			loaded.recordRequestSHA = sha
		case 1:
			loaded.recordRequestDualSHA = sha
		case 2:
			loaded.applyDecisionSHA = sha
		case 3:
			loaded.applyAdminSHA = sha
		case 4:
			loaded.applyAdminDualSHA = sha
		case 5:
			loaded.applyProbeSHA = sha
		case 6:
			loaded.applyProbeDualSHA = sha
		case 7:
			loaded.clearStateSHA = sha
		case 8:
			loaded.clearStateDualSHA = sha
		}
	}

	slog.Info("ursm lua scripts preloaded",
		"count", len(scripts),
		"record_request_sha", loaded.recordRequestSHA[:8],
		"record_request_dual_sha", loaded.recordRequestDualSHA[:8])

	return loaded, nil
}

// ScriptSizes returns the byte sizes of all embedded Lua scripts, useful for
// diagnostics and monitoring.
func ScriptSizes() map[string]int {
	return map[string]int{
		"record_request.lua":       len(recordRequestSrc),
		"record_request_dual.lua":  len(recordRequestDualSrc),
		"apply_decision.lua":       len(applyDecisionSrc),
		"apply_admin.lua":          len(applyAdminSrc),
		"apply_admin_dual.lua":     len(applyAdminDualSrc),
		"apply_probe.lua":          len(applyProbeSrc),
		"apply_probe_dual.lua":     len(applyProbeDualSrc),
		"clear_state.lua":          len(clearStateSrc),
		"clear_state_dual.lua":     len(clearStateDualSrc),
	}
}
