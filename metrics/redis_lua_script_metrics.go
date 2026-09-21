// Package metrics - Redis Lua 脚本可观测性指标.
//
// 2026-08-27 P1 优化 (Redis 审计 sess_01JGHDCWSZQZS8D9D5H41A7VWN):
// Redis commandstats 显示 EVALSHA 失败率 3.4% (1184/34469)，根因是
// SCRIPT FLUSH 后脚本缓存失效，回退 EVAL 需要每次传输完整脚本源码
// (record_request.lua 达 11,469 字节)。
//
// 本文件提供启动预加载状态与脚本体积指标；gateway 启动时调用
// store.PreloadScripts() 预热脚本缓存后设置 gauge，运维可通过:
//
//	llmgw_redis_lua_script_preloaded == 0
//
// 发现预加载失败的实例；通过 fallback 计数监控缓存失效复发:
//
//	rate(llmgw_redis_lua_script_fallbacks_total[5m])
//
// 命名遵循 llmgw_ 前缀（项目惯例）和 GW-00 低基数规范：
// script 标签有界（URSM 9 个 + systemmonitor 4 个），method 两个值。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// RedisLuaScriptPreloaded 表示某模块的 Lua 脚本在启动时是否成功
	// SCRIPT LOAD 预加载 (1=成功, 0=失败)。失败是非致命的 —— go-redis
	// Script.Run 会在首次 NOSCRIPT 时自动重新上传 —— 但失败意味着
	// 预热优化未生效，值得告警排查 (常见原因: 启动时 Redis 短暂不可达)。
	//
	// module ∈ {ursm, systemmonitor}
	RedisLuaScriptPreloaded = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llmgw_redis_lua_script_preloaded",
		Help: "Whether a module's Redis Lua scripts were preloaded at startup (1=ok, 0=failed).",
	}, []string{"module"})

	// RedisLuaScriptSizeBytes 暴露内嵌 Lua 脚本的字节体积 (gauge, 启动时
	// 设置一次)。用于评估 EVAL fallback 的单次网络开销 —— 体积 × fallback
	// 率即带宽损失 (11 KB 脚本 × 3.4% ≈ 每请求 390 字节)。
	RedisLuaScriptSizeBytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llmgw_redis_lua_script_size_bytes",
		Help: "Size in bytes of embedded Redis Lua scripts (set once at startup).",
	}, []string{"script"})

	// RedisLuaScriptFallbacksTotal 统计 EVALSHA → EVAL 的回退次数。
	// 由 go-redis Script.Run 的 NOSCRIPT 处理路径触发记录；持续增长
	// 表示脚本缓存被反复清除 (SCRIPT FLUSH / Redis 重启 / 内存驱逐)。
	//
	// 注意: 当前仅预加载路径设置 preload gauge；fallback 计数需要调用
	// 方在 NOSCRIPT 回退分支显式 Inc，见 store.RecordRequest 集成点。
	RedisLuaScriptFallbacksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_redis_lua_script_fallbacks_total",
		Help: "EVALSHA to EVAL fallbacks (NOSCRIPT), by script.",
	}, []string{"script"})
)

// Sentinel values for RedisLuaScriptPreloaded gauge. Use these constants
// instead of raw numbers so Prometheus queries (gauge == -1 vs == 0)
// remain self-documenting across call sites.
//
//	== -1: Redis is not configured (LLM_GATEWAY_REDIS_ADDR unset)
//	==  0: Redis configured but preload failed (transient or persistent)
//	==  1: Redis configured and preload succeeded
const (
	RedisLuaScriptPreloadedDisabled = -1.0
	RedisLuaScriptPreloadedFailed   = 0.0
	RedisLuaScriptPreloadedOK       = 1.0
)
