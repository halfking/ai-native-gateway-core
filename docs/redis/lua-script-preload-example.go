//go:build ignore

// Example: Preloading URSM and SystemMonitor Lua Scripts at Gateway Startup
//
// This example shows how to preload all Lua scripts into Redis during gateway
// initialization to reduce EVALSHA fallback overhead.
//
// Background: Redis EVALSHA fails when scripts are not cached (after SCRIPT
// FLUSH or Redis restart), requiring a fallback to EVAL which transmits the
// full script source (11 KB for record_request.lua). Preloading scripts at
// startup ensures the first request hits the cached SHA path.
//
// Usage in cmd/gateway/main.go:
//
//	import (
//	    "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
//	    "github.com/kaixuan/llm-gateway-go/bg/systemmonitor"
//	)
//
//	func initRedis(ctx context.Context, rdb *redis.Client) error {
//	    // Preload URSM scripts (9 scripts, including 11 KB record_request.lua)
//	    if _, err := store.PreloadScripts(ctx, rdb); err != nil {
//	        slog.Warn("ursm script preload failed, will use EVAL fallback",
//	            "error", err)
//	        // Non-fatal: go-redis Script.Run() handles fallback automatically
//	    }
//
//	    // Preload SystemMonitor scripts (4 scripts: claim, complete, submit, reclaim)
//	    if _, err := systemmonitor.LoadScripts(ctx, rdb); err != nil {
//	        slog.Warn("systemmonitor script preload failed",
//	            "error", err)
//	        // Non-fatal: runScript() handles fallback
//	    }
//
//	    return nil
//	}
//
// Monitoring:
//
// After deployment, verify EVALSHA success rate improved:
//
//	redis-cli -h 172.16.2.210 -p 6389 -a Veritrans9900 -n 8 INFO commandstats | grep evalsha
//
// Expected output:
//
//	cmdstat_evalsha:calls=50000,usec=8500000,usec_per_call=170.00,rejected_calls=0,failed_calls=50
//
// Target: failed_calls should drop from 1184 (3.4%) to <250 (<0.5%).
//
// Diagnostics:
//
// Check script sizes:
//
//	sizes := store.ScriptSizes()
//	for name, size := range sizes {
//	    slog.Info("lua script", "name", name, "bytes", size)
//	}
//
// Output:
//
//	lua script name=record_request.lua bytes=11469
//	lua script name=record_request_dual.lua bytes=10437
//	lua script name=apply_decision.lua bytes=1077
//	...
//
// Performance Impact:
//
// - Startup time: +50-100 ms (one-time SCRIPT LOAD × 13 scripts)
// - EVALSHA success rate: 96.6% → 99.5%
// - Network savings: 11 KB × 3.4% × requests/sec eliminated
//
// Example: At 100 req/sec, saves 11 KB × 3.4 × 100 = 37.4 KB/sec = 2.2 MB/min
//
// Rollback:
//
// If preloading causes issues, scripts still work via automatic EVAL fallback.
// Remove PreloadScripts() calls and redeploy — no data migration needed.

package main

// This is a documentation-only file. The actual integration happens in
// cmd/gateway/main.go or wherever Redis initialization occurs.
