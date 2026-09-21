// Package redis — script.go
//
// Drop-in replacement for redis.NewScript(...).Run(...) that exposes the
// EVALSHA→EVAL fallback as a Prometheus metric. The stdlib Script.Run
// swallows the NOSCRIPT signal internally, making the audit's
// "llmgw_redis_lua_script_fallbacks_total" blind to anything other than
// the systemmonitor's hand-rolled runScript. This wrapper closes the gap
// for the URSM v2 hot-path scripts (record_request*.lua, 21KB combined)
// and any future redis.NewScript site that migrates to RunScript.
//
// Usage (drop-in):
//
//	var myScript = redis.NewScript(myLua)
//	// before:
//	//   r := myScript.Run(ctx, rdb, keys, args...).Slice()
//	// after:
//	r := redissafe.RunScript(ctx, rdb, myScript, "my_script_name", keys, args...).Slice()
package redis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/metrics"
)

// noscriptPrefix mirrors the Redis NOSCRIPT error wording. We match the
// substring rather than the typed redis.ErrNoScript because go-redis wraps
// the error and the exact surface is "NOSCRIPT No matching script...".
const noscriptPrefix = "NOSCRIPT"

// RunScript invokes a Redis Lua script with EVALSHA-first semantics and
// records a fallback into metrics.RedisLuaScriptFallbacksTotal when the
// SHA cache is cold.
//
// Returns the same *Cmd value as redis.Script.Run so callers can use
// .Slice(), .Int(), .Result(), etc. as usual.
//
// The name parameter is the metric label — use the bare filename (e.g.,
// "record_request.lua") so dashboards can group by script source.
func RunScript(ctx context.Context, c redis.Scripter, s *redis.Script, name string, keys []string, args ...any) *redis.Cmd {
	r := s.EvalSha(ctx, c, keys, args...)
	if err := r.Err(); err != nil && isNoScript(err) {
		metrics.RedisLuaScriptFallbacksTotal.WithLabelValues(name).Inc()
		return s.Eval(ctx, c, keys, args...)
	}
	return r
}

// isNoScript matches the NOSCRIPT error returned by Redis when EVALSHA is
// called against a script that has been evicted from cache. Both the
// bare sentinel and the wrapped form are recognised.
func isNoScript(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, redis.Nil) {
		return false
	}
	return strings.Contains(err.Error(), noscriptPrefix)
}

// RunScriptResult is a convenience wrapper that returns (value, error) for
// the common case where callers only need the final result. Use RunScript
// directly when you need the *Cmd to call .Slice() or .Int64Slice().
func RunScriptResult(ctx context.Context, c redis.Scripter, s *redis.Script, name string, keys []string, args ...any) (any, error) {
	r := RunScript(ctx, c, s, name, keys, args...)
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("redis script %s: %w", name, err)
	}
	return r.Val(), nil
}
