// Package bg/systemmonitor — lua_scripts.go
//
// Redis Lua 脚本加载与执行。
// 设计依据: docs/会话优化v2/32-系统监测模块设计.md §3.2
package systemmonitor

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/metrics"
)

//go:embed lua/claim.lua
var claimLuaSrc string

//go:embed lua/complete.lua
var completeLuaSrc string

//go:embed lua/submit.lua
var submitLuaSrc string

//go:embed lua/reclaim.lua
var reclaimLuaSrc string

// LoadedScripts holds the SHA1-loaded Redis scripts ready for EVALSHA.
type LoadedScripts struct {
	claimSHA    string
	completeSHA string
	submitSHA   string
	reclaimSHA  string
}

// LoadScripts precomputes SHA1 of all Lua scripts and verifies the embedded
// source compiles cleanly via SCRIPT LOAD.
//
// Must be called once at startup. Subsequent EVALSHA calls reuse the SHA1
// to avoid shipping the entire Lua source over the wire per task.
func LoadScripts(ctx context.Context, rdb *redis.Client) (*LoadedScripts, error) {
	if rdb == nil {
		return nil, errors.New("redis client is nil")
	}
	claimSHA, err := rdb.ScriptLoad(ctx, claimLuaSrc).Result()
	if err != nil {
		return nil, fmt.Errorf("script load claim.lua: %w", err)
	}
	completeSHA, err := rdb.ScriptLoad(ctx, completeLuaSrc).Result()
	if err != nil {
		return nil, fmt.Errorf("script load complete.lua: %w", err)
	}
	submitSHA, err := rdb.ScriptLoad(ctx, submitLuaSrc).Result()
	if err != nil {
		return nil, fmt.Errorf("script load submit.lua: %w", err)
	}
	reclaimSHA, err := rdb.ScriptLoad(ctx, reclaimLuaSrc).Result()
	if err != nil {
		return nil, fmt.Errorf("script load reclaim.lua: %w", err)
	}
	return &LoadedScripts{
		claimSHA:    claimSHA,
		completeSHA: completeSHA,
		submitSHA:   submitSHA,
		reclaimSHA:  reclaimSHA,
	}, nil
}

// runScript is a thin wrapper around EVALSHA that falls back to EVAL if the
// script was evicted from Redis (NOSCRIPT reply). Redis evicts scripts
// under memory pressure, so this fallback keeps the system working after
// a FLUSH or a restart.
func runScript(ctx context.Context, rdb *redis.Client, sha, src string, keys []string, args ...any) (any, error) {
	res, err := rdb.EvalSha(ctx, sha, keys, args...).Result()
	if err != nil && isNoScript(err) {
		metrics.RedisLuaScriptFallbacksTotal.WithLabelValues(luaScriptName(src)).Inc()
		return rdb.Eval(ctx, src, keys, args...).Result()
	}
	return res, err
}

// luaScriptName maps an embedded Lua source to its stable metric label.
// Unknown sources (test stubs) fall back to "unknown" — bounded cardinality.
func luaScriptName(src string) string {
	if name, ok := luaSrcNames[src]; ok {
		return name
	}
	return "unknown"
}

var luaSrcNames = map[string]string{
	claimLuaSrc:    "claim.lua",
	completeLuaSrc: "complete.lua",
	submitLuaSrc:   "submit.lua",
	reclaimLuaSrc:  "reclaim.lua",
}

func isNoScript(err error) bool {
	if err == nil {
		return false
	}
	// redis.Nil is "key not found" — keep as a normal result, not a script error.
	if errors.Is(err, redis.Nil) {
		return false
	}
	// go-redis returns the raw redis error string for script-related failures.
	// We check both the wrapped form and the lower-level prefix.
	msg := err.Error()
	return msg == "NOSCRIPT" ||
		msg == "NOSCRIPT No matching script. Please use EVAL." ||
		len(msg) >= 8 && msg[:8] == "NOSCRIPT"
}
