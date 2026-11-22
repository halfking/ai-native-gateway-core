package main

// history_guard_memo_init.go —— 历史守卫判决缓存 + 归约并行的装配（2026-11-23）。
//
// 背景：agent 会话每一轮都重发整段历史，归约时对**每一条历史消息**都要过一遍
// 守卫（211 实测 7.7MB / 3953 条的会话：归约 5 秒，其中守卫约 88%）。旧消息
// 的判决每轮被原样重算——而它是 (规则策略, 消息字节) 的纯函数。
//
// 装配两件事：
//   1. 判决缓存（sanitize.TieredVerdictMemo）：进程内有界缓存 + 可选的共享层；
//   2. 声明守卫可并发 ⇒ 归约器把逐条处理分给工作协程（冷路径的兜底）。
//
// 开关（全部是 env，重启生效；与其它 LLM_GATEWAY_* 流式开关同一口径）：
//
//	LLM_GATEWAY_HISTORY_GUARD_MEMO=off                  关闭缓存（归约回到逐条重算）
//	LLM_GATEWAY_HISTORY_MEMO_LOCAL_ENTRIES=131072       进程内缓存条目上限
//	LLM_GATEWAY_REDUCE_WORKERS=8                        单次归约的最大工作协程数
//
// 共享层（Redis）默认**不启用**；只有多实例同时服务、同一会话相邻两轮可能
// 落到不同进程时才值得开（单实例与蓝绿都只有本地层就够——重启后第一轮走
// 并行冷路径，实测不到 1 秒）：
//
//	LLM_GATEWAY_HISTORY_MEMO_SHARED_TTL=6h              设了才启用；键的存活时间
//	LLM_GATEWAY_HISTORY_MEMO_SHARED_MAX_USED_MB=512     共享后端内存水位线（超过即停写）
//	LLM_GATEWAY_HISTORY_MEMO_SHARED_MAX_WRITES_PER_HOUR=500000   本进程每小时写入上限
//
// ★ 共享层为什么自带硬边界：211 的 Redis 没有内存上限且不淘汰
// （maxmemory=0 / noeviction），与限流、会话、队列共用。一份「加速用的缓存」
// 绝不能成为那台 Redis 的 OOM 来源 ⇒ TTL + 写入预算 + 内存水位线，任一触线
// 即停写（读照常，结论不受影响）；后端连续失败或变慢 ⇒ 熔断绕过（见
// sanitize.TieredVerdictMemo）。

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/internal/verdictkv"
	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

const (
	// exampleHistoryMemoSharedTTL 是共享层的建议存活时间；共享层默认关。
	exampleHistoryMemoSharedTTL       = 6 * time.Hour
	defaultHistoryMemoSharedMaxUsedMB = 512
	defaultHistoryMemoSharedMaxWrites = 500_000
	defaultHistoryMemoLocalEntries    = 1 << 17
)

// historyMemoEnv 是解析后的缓存配置（解析与装配分开，便于测试）。
type historyMemoEnv struct {
	Disabled           bool
	LocalEntries       int
	SharedTTL          time.Duration // 0 ⇒ 不使用共享层（默认）
	SharedMaxUsedBytes int64
	SharedMaxWrites    int64
}

// parseHistoryMemoEnv 解析 LLM_GATEWAY_HISTORY_* ；非法值回落默认并记日志
// （一个拼错的 env 不该让网关起不来，但也不能悄悄当没发生）。
func parseHistoryMemoEnv(getenv func(string) string) historyMemoEnv {
	cfg := historyMemoEnv{
		LocalEntries:       defaultHistoryMemoLocalEntries,
		SharedMaxUsedBytes: defaultHistoryMemoSharedMaxUsedMB << 20,
		SharedMaxWrites:    defaultHistoryMemoSharedMaxWrites,
	}
	// ★ strconv.ParseBool 不认 "off"（只认 true/false/1/0/t/f…）——首版直接用它，
	// 结果文档里写的 `=off` 根本关不掉缓存。这里把「关」的拼法显式列全。
	switch strings.ToLower(strings.TrimSpace(getenv("LLM_GATEWAY_HISTORY_GUARD_MEMO"))) {
	case "off", "0", "false", "no", "disabled":
		cfg.Disabled = true
		return cfg
	case "", "on", "1", "true", "yes", "enabled":
	default:
		slog.Warn("history guard memo switch unparsable; keeping default (on)", "value", getenv("LLM_GATEWAY_HISTORY_GUARD_MEMO"))
	}
	if n, ok := envPositiveInt(getenv, "LLM_GATEWAY_HISTORY_MEMO_LOCAL_ENTRIES"); ok {
		cfg.LocalEntries = n
	}
	if raw := strings.TrimSpace(getenv("LLM_GATEWAY_HISTORY_MEMO_SHARED_TTL")); raw != "" {
		if raw == "off" || raw == "0" {
			cfg.SharedTTL = 0
		} else if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			cfg.SharedTTL = d
		} else if strings.EqualFold(raw, "on") {
			cfg.SharedTTL = exampleHistoryMemoSharedTTL
		} else {
			slog.Warn("history guard memo shared TTL unparsable; shared cache stays off", "value", raw)
		}
	}
	if n, ok := envPositiveInt(getenv, "LLM_GATEWAY_HISTORY_MEMO_SHARED_MAX_USED_MB"); ok {
		cfg.SharedMaxUsedBytes = int64(n) << 20
	}
	if n, ok := envPositiveInt(getenv, "LLM_GATEWAY_HISTORY_MEMO_SHARED_MAX_WRITES_PER_HOUR"); ok {
		cfg.SharedMaxWrites = int64(n)
	}
	return cfg
}

func envPositiveInt(getenv func(string) string, key string) (int, bool) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		slog.Warn("history guard memo setting unparsable; keeping default", "key", key, "value", raw)
		return 0, false
	}
	return n, true
}

// newHistoryVerdictMemo 按 env 构造缓存；关闭 ⇒ nil。
//
// ★ 类型化 nil：verdictkv.NewRedis(nil, …) 返回 (*RedisKV)(nil)，直接赋给
// 接口会得到一个「不等于 nil」的接口 ⇒ 先判返回值再赋值。
func newHistoryVerdictMemo(cfg historyMemoEnv, rc redis.UniversalClient) *sanitize.TieredVerdictMemo {
	if cfg.Disabled {
		return nil
	}
	hc := sanitize.HistoryMemoConfig{
		LocalEntries:           cfg.LocalEntries,
		SharedTTL:              cfg.SharedTTL,
		SharedMaxUsedBytes:     cfg.SharedMaxUsedBytes,
		SharedMaxWritesPerHour: cfg.SharedMaxWrites,
	}
	if cfg.SharedTTL > 0 && rc != nil {
		if kv := verdictkv.NewRedis(rc, verdictkv.KeyPrefix); kv != nil {
			hc.Shared = kv
		}
	}
	return sanitize.NewHistoryVerdictMemo(hc)
}

// installHistoryGuardMemo 把缓存挂到检测器、把计数注册成指标，并声明守卫
// 可并发（归约器据此逐条并行）。
func installHistoryGuardMemo(h *streaming.ChatHandler, d *sanitize.PatternDetector, rc redis.UniversalClient) {
	installHistoryGuardMemoWithEnv(h, d, rc, os.Getenv)
}

func installHistoryGuardMemoWithEnv(h *streaming.ChatHandler, d *sanitize.PatternDetector, rc redis.UniversalClient, getenv func(string) string) {
	cfg := parseHistoryMemoEnv(getenv)
	memo := newHistoryVerdictMemo(cfg, rc)
	if memo != nil && d != nil {
		d.SetHistoryVerdictMemo(memo)
		metrics.RegisterHistoryGuardMemoStats(func() metrics.HistoryGuardMemoCounters {
			st := memo.Stats()
			return metrics.HistoryGuardMemoCounters{
				LocalHits: st.LocalHits, SharedHits: st.SharedHits, Misses: st.Misses,
				SharedErrors: st.SharedErrors, SharedWrites: st.SharedWrites, SharedSkipped: st.SharedSkipped,
				BreakerOpens: st.BreakerOpens,
			}
		})
	}
	// sanitize 的策略守卫（连同缓存包装）自身没有共享可变状态（正则可并发、
	// 每次调用各开各的 reader、缓存分片加锁）⇒ 可并发 ⇒ 归约器逐条并行。
	// ★ 自定义守卫（测试里的闭包）不走这里 ⇒ 保持串行。
	if h != nil {
		h.SetHistoryGuardConcurrencySafe(d != nil)
	}
	shared := "off"
	if memo != nil && cfg.SharedTTL > 0 && rc != nil {
		shared = "on ttl=" + cfg.SharedTTL.String()
	}
	slog.Info("history guard verdict memo configured",
		"memo", memo != nil,
		"shared_cache", shared,
		"local_entries", cfg.LocalEntries,
		"reduce_workers", streaming.ReduceWorkersConfigured(getenv),
	)
}
