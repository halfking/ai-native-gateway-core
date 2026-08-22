package settings

// AutoSummarySpecs — 5 平台级 — 即时会话总结 pipeline 的运行时常量
// 对应 LLM_GATEWAY_AUTO_SUMMARY_* 环境变量（向后兼容保留）。
//
// 2026-08-06：把 admin/auto_summary_generator.go 的硬编码常量
// (autoSummaryRollingTurnGate=3, autoSummaryMapReduceThreshold=12000,
// autoSummaryChunkApproxChars=3000, autoSummaryDefaultRatePerMin=6,
// autoSummaryDefaultWorkerSlots=4) 接入 settings_kv 热更新机制。
//
// 热更新约束（详见 settings/spec.go 与 settings/helpers.go）：
//   - rolling_turn_gate / map_reduce_threshold / chunk_approx_chars:
//     在 use-site 调用 settings.GetPlatformInt(key, fallback)，每次重新读取
//     settings_kv → env → default。安全无锁，热更新生效。
//   - default_rate_per_min: 在 NewAutoSummaryGenerator 中读取并保存到 g.ratePerMin，
//     构造后即冻结。需要重启进程或在 AutoSummaryGenerator 上加 Reload() 方法
//     才能热更新（下一轮）。本轮保持"env 启动 + 立即生效，settings_kv 启动
//     后首次构造生效"的语义。
//   - default_worker_slots: 同上，chan struct{} 容量构造后不可变。本轮保持。
func AutoSummarySpecs() []*Spec {
	return []*Spec{
		{
			Key:             "auto_summary.rolling_turn_gate",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryCompression,
			Min:             floatPtr(1),
			Max:             floatPtr(100),
			Default:         3,
			Description:     "即时总结滚动闸门",
			DescriptionLong: "自上次总结以来需要累积的新 turn 数才会重做总结。默认 3（避免每 turn 都跑总结）。最小 1，最大 100。热更新生效（每次 checkSessionHasTitle 决策时读最新值）。",
			Unit:            "turn 数",
			DangerLevel:     Warning,
			HotReload:       true,
			Observability:   "/api/admin/auto_title/stats",
		},
		{
			Key:             "auto_summary.map_reduce_threshold",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryCompression,
			Min:             floatPtr(1000),
			Max:             floatPtr(1_000_000),
			Default:         12000,
			Description:     "即时总结 map-reduce 阈值",
			DescriptionLong: "语料字符数超过此值时走 map-reduce 分段；否则单次 LLM 调用。默认 12000 字符（与廉价模型的上下文窗口留余量）。最小 1000，最大 1000000。热更新生效（每次 generateSummary 决策时读最新值）。",
			Unit:            "字符数",
			DangerLevel:     Warning,
			HotReload:       true,
			Observability:   "/api/admin/auto_title/stats",
		},
		{
			Key:             "auto_summary.chunk_approx_chars",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryCompression,
			Min:             floatPtr(500),
			Max:             floatPtr(100_000),
			Default:         3000,
			Description:     "即时总结 map-reduce 单 chunk 字符数",
			DescriptionLong: "splitCorpusIntoChunks 的目标单 chunk 字符数；优先 newline 边界。默认 3000。最小 500，最大 100000。热更新生效（每次 splitCorpusIntoChunks 调用时读最新值）。",
			Unit:            "字符数",
			DangerLevel:     Warning,
			HotReload:       true,
			Observability:   "/api/admin/auto_title/stats",
		},
		{
			Key:             "auto_summary.default_rate_per_min",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryCompression,
			Min:             floatPtr(1),
			Max:             floatPtr(600),
			Default:         6,
			Description:     "即时总结每租户速率（次/分钟）",
			DescriptionLong: "每租户的 rate.Limiter 令牌桶大小（次/分钟）。默认 6。最小 1，最大 600。生成器构造时读取一次（启动生效），运行时改 settings_kv 需要重启或后续 Reload 接口。",
			Unit:            "次/分钟",
			DangerLevel:     Dangerous,
			HotReload:       true,
			Observability:   "/api/admin/auto_title/stats",
		},
		{
			Key:             "auto_summary.default_worker_slots",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryCompression,
			Min:             floatPtr(1),
			Max:             floatPtr(128),
			Default:         4,
			Description:     "即时总结全局 worker slot 信号量容量",
			DescriptionLong: "全局并发上限（chan struct{} 容量）。默认 4。最小 1，最大 128。生成器构造时一次性写入 chan；运行时改 settings_kv 需要重启或后续 Reload 接口（chan 容量构造后不可变）。",
			Unit:            "并发数",
			DangerLevel:     Dangerous,
			HotReload:       true,
			Observability:   "/api/admin/auto_title/stats",
		},
	}
}
