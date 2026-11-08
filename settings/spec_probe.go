package settings

// CategoryProbe groups all model-probe-related settings together.
const CategoryProbe Category = "probe"

// ProbeSpecs returns all platform-scoped probe / lifecycle management specs.
//
// These settings control model_probe_runs hot table + monthly partition
// retention. The hot table holds the most recent N hours of probe records
// (fast INSERT, fast DELETE); rows older than that get promoted into
// monthly columnar partitions by bg.PartitionManager.
func ProbeSpecs() []*Spec {
	return []*Spec{
		// ── Deprecated model_probe_runs partition settings ─────────────
		{
			Key:             "probe.hot_retention_hours",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(1),
			Max:             floatPtr(720), // 30 days
			Default:         24,
			Description:     "已废弃: model_probe_runs 热表小时数",
			DescriptionLong: "已废弃。2026-07-14 起 model_probe_runs_hot 改为纯 hot 表 + DELETE TTL 策略，不再读取 probe.hot_retention_hours，也不再 promote 到月度 columnar 分区。请改用 lifecycle.model_probe_runs_ttl_days。",
			Unit:            "小时",
			DangerLevel:     Warning,
			HotReload:       true,
		},

		// ── Deprecated model_probe_runs partition retention ────────────
		{
			Key:             "probe.partition_retention_days",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(7),
			Max:             floatPtr(3650), // 10 years
			Default:         90,
			Description:     "已废弃: model_probe_runs 分区保留天数",
			DescriptionLong: "已废弃。2026-07-14 起 model_probe_runs 不再写入月度分区，也不再执行 drop_old_model_probe_runs_partitions()。请改用 lifecycle.model_probe_runs_ttl_days 控制 hot 表 DELETE TTL。",
			Unit:            "天",
			DangerLevel:     Warning,
			HotReload:       true,
		},

		// ── Deprecated model_probe_runs promote settings ───────────────
		{
			Key:             "probe.promote_batch_size",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(100),
			Max:             floatPtr(50000),
			Default:         5000,
			Description:     "已废弃: model_probe_runs promote 批大小",
			DescriptionLong: "已废弃。2026-07-14 起 model_probe_runs_hot 不再 promote；该设置已不再生效。",
			Unit:            "行",
			DangerLevel:     Safe,
			HotReload:       true,
		},

		// ── Deprecated model_probe_runs cleanup switch ────────────────
		{
			Key:             "probe.partition_cleanup_enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Default:         true,
			Description:     "已废弃: 启用 model_probe_runs 分区清理",
			DescriptionLong: "已废弃。2026-07-14 起 model_probe_runs 不再使用月度分区，该开关不再影响实际行为。",
			Unit:            "",
			DangerLevel:     Dangerous,
			HotReload:       true,
		},

		// ── Backoff parameters ───────────────────────────────────────────
		{
			Key:             "probe.backoff_base_seconds",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(30),
			Max:             floatPtr(3600),
			Default:         300,
			Description:     "探测退避基础秒数",
			DescriptionLong: "指数退避的基准间隔（秒）。第 1 次重试 = base，第 2 次 = base × multiplier，以此类推。",
			Unit:            "秒",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "probe.backoff_max_seconds",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(300),
			Max:             floatPtr(86400),
			Default:         7200,
			Description:     "探测退避最大秒数",
			DescriptionLong: "指数退避的上限（秒），重试间隔不会超过此值。",
			Unit:            "秒",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "probe.backoff_multiplier",
			Type:            TypeFloat,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(1.0),
			Max:             floatPtr(10.0),
			Default:         2.0,
			Description:     "探测退避倍数",
			DescriptionLong: "指数退避的乘法因子。默认为 2（每次失败翻倍）。",
			Unit:            "",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "probe.backoff_jitter_seconds",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(0),
			Max:             floatPtr(300),
			Default:         30,
			Description:     "探测退避抖动秒数",
			DescriptionLong: "在退避间隔上附加的随机抖动（秒），防止惊群效应。设 0 关闭抖动。",
			Unit:            "秒",
			DangerLevel:     Safe,
			HotReload:       true,
		},

		// ── Watchdog intervals ───────────────────────────────────────────
		{
			Key:             "probe.broken_watchdog_hours",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(1),
			Max:             floatPtr(720),
			Default:         168,
			Description:     "Broken 状态看门狗小时数",
			DescriptionLong: "模型进入 broken_confirmed 状态后，等待多少小时才再次探测。默认 168（7 天）。",
			Unit:            "小时",
			DangerLevel:     Safe,
			HotReload:       true,
		},

		// ── 常用模型分级自检（2026-08-13）───────────────────────────────
		// 常用模型 = routing_policy.featured_models ∪ 用量 Top-N。常用模型更高频/
		// 更深探测、更高队列优先级、失败后更快恢复；非常用模型降频。详见
		// docs/自检优化/02-常用模型分级自检.md。
		{
			Key:             "probe.featured_cycle_seconds",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(60),
			Max:             floatPtr(86400),
			Default:         900,
			Description:     "常用模型深探测周期（秒）",
			DescriptionLong: "ModelProbeRunner 为常用模型运行 chat-ping 深探测的周期。原硬编码 1800（30 分钟）；调小则常用模型自检更频繁。",
			Unit:            "秒",
			DangerLevel:     Safe,
			HotReload:       false, // ticker 启动时读取；修改需重启
		},
		{
			Key:             "probe.featured_tenant",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Default:         "default",
			Description:     "常用模型判定所用的租户 ID",
			DescriptionLong: "ModelTier 从 routing_policy 读取 featured_models 静态精选列表时所用的 tenant_id（默认 'default'）。多租户部署中若需以特定租户的精选列表驱动自检分级，修改此项。nonfeaturedWatchdogTick 也读取同一设置以保持一致性。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "probe.featured_usage_top_n",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(0),
			Max:             floatPtr(500),
			Default:         20,
			Description:     "按用量纳入「常用」的模型数（Top-N）",
			DescriptionLong: "在静态精选列表之外，按会话族热表（session_turns_hot）近窗口成功调用数取 Top-N 一并视为常用模型。设 0 则仅按静态精选列表判定（kill-switch）。★ 2026-10-24：原读源是 v1 `_request_logs_hot`，该族已随迁移 890 退役。",
			Unit:            "个",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "probe.featured_usage_window_hours",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(1),
			Max:             floatPtr(2160), // 90d (hot table retention)
			Default:         24,
			Description:     "用量统计窗口（小时）",
			DescriptionLong: "计算用量 Top-N 时回看的窗口长度（默认 24=1 天，2026-11-05：与自动探测 INV-3「近 24h 真实用量」对齐；统计已排除探测自身流量）。★ 2026-11-06 审计口径补充：本键只影响 ModelTier.usage Top-N 统计；自动探测 model-scope 门的「真实用量」臂走会话族热腿，实际可见窗口受热腿保留期（默认 8h）约束，比本键更窄（见 bg/probe_model_scope.go P-3 注释）。",
			Unit:            "小时",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:      "probe.recovery_max_backoff_seconds",
			Type:     TypeInt,
			Scope:    ScopePlatform,
			Category: CategoryProbe,
			Min:      floatPtr(60),
			// ★ 上界取 21600（6h）＝ 2026-09-25 P0-2 长尾的最大一级
			//   ⇒ ⇒ 把本旋钮调到 21600 就能**纯配置**回到旧口径（见下 DescriptionLong）。
			Max:         floatPtr(21600),
			Default:     300,
			Description: "可恢复探测退避上限（秒）",
			DescriptionLong: "瞬时/网络/5xx 等非永久失败的自动探测重试间隔**上限**（默认 300=5 分钟）。长尾仍是渐进的 5m→1h→2h→6h，但每一级都被削到本上限 ⇒ 默认下恒为 5 分钟，以便供应商恢复后约 5 分钟内被重新探测到。永久类（404/协议/配额固定节奏）不受此上限约束。" +
				" ★ ★ 回退路径（纯配置、不用改代码）：★ 本旋钮调到 21600（6h）⇒ 长尾恢复成 2026-09-25 P0-2 的原梯 5m→1h→2h→6h；★ 调到 60 ⇒ 退化成短梯封顶。★ 所以这次口径变更是**可逆的**。" +
				" ★ 代价（必须一起看）：★ 2026-09-25 P0-2 实测 60s 档滞留 45 对（connection_error/503/timeout/500），长尾化落地后 node_probe_runs ~5,000/h→~1,610/h。" +
				" ★ 本旋钮把长尾封顶从 6h 改到默认 5m ⇒ ★ 对【持续故障的对】稳态频率由 1 次/6h 变 1 次/5m（★ 72×）⇒ 以那 45 对计 ≈ +530 次/h（★ 约合 1,610/h 基线的 +33%）。" +
				" ★ 另一项同轮成本（2026-11-06 审计 P-8 补记）：recoveringSweeper 扫描周期由 30m 收紧到 5m ⇒ 持续故障对的最坏验证探测量上限由 480/day 升至 2,880/day（6×）；两项叠加请以 dashboard 的 node_probe_runs 频率实测为准。" +
				" ★ 换来的好处：供应商恢复后 ≤5 分钟内被复检（原先最长 6h）。★ 成本敏感的部署请把本旋钮调到 3600（1h）——恢复发现慢 6 倍，探测量回到 P0-2 之后的水平。",
			Unit:        "秒",
			DangerLevel: Safe,
			HotReload:   true,
		},
		// ── Sustained-failure escalation (2026-11-07 探测浪费根修轮) ──────
		{
			Key:      "probe.sustained_failure_attempt_threshold",
			Type:     TypeInt,
			Scope:    ScopePlatform,
			Category: CategoryProbe,
			Min:      floatPtr(7),  // 一代队列任务的最深梯级（nodeProbeMaxAttempts）
			Max:      floatPtr(1000),
			Default:  21, // 3 代完整梯走完仍零成功 ⇒ 判持续失败
			Description: "持续失败升级的尝试阈值",
			DescriptionLong: "node-probe 失败的 attempt 是跨代持续的（=node_probe_state.consecutive_failures+1，2026-09-26 P0-2）。attempt ≥ 本阈值且梯级本会被 probe.recovery_max_backoff_seconds 削平时，复检间隔升级到 probe.sustained_failure_recheck_seconds（默认 1h）而不是恒 5 分钟——「新失败 5 分钟内复检」的恢复 SLA 保留，只在**持续失败**（默认 21 次 ≈ 一小时级零成功）时放慢盲探。恢复感知不降级：配额/可用性恢复事件仍即时回拉（RearmCredentialFailures），业务成功仍立即停放。7=最早生效（第一代任务末段）。",
			Unit:        "次",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:      "probe.sustained_failure_recheck_seconds",
			Type:     TypeInt,
			Scope:    ScopePlatform,
			Category: CategoryProbe,
			Min:      floatPtr(300), // 不得低于恢复 SLA 下限
			Max:      floatPtr(86400),
			Default:  3600,
			Description: "持续失败对的复检间隔（秒）",
			DescriptionLong: "attempt 达到 probe.sustained_failure_attempt_threshold 后，可恢复类失败的下次自动复检间隔（默认 3600=1h；绑定冷却 unavailableBindingHorizon 同步升级）。实测背景（2026-11-07，211）：SLA 封顶把持续故障对钉在 ~5 分钟一探 ⇒ 单对 ~480 次/天、consecutive_failures 累积至 ~1000 仍不停（spec 里 probe.recovery_max_backoff_seconds 已登记该 72× 成本）。调大=更省但恢复发现更慢；调到 300=回到恒 5 分钟口径（等效关闭升级）——★ 注意该等价只在 probe.recovery_max_backoff_seconds=300（默认）时成立：若把恢复 SLA 调大，本键的有效值会被 SLA 下限托起（bg/probe_recovery_policy.go 的 sustainedFailureRecheck 以 SLA 为下限）⇒ 「300=关闭升级」的说法不适用。",
			Unit:        "秒",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:             "probe.nonfeatured_watchdog_multiplier",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(1),
			Max:             floatPtr(24),
			Default:         4,
			Description:     "非常用模型健康态看门狗倍率",
			DescriptionLong: "非常用模型 healthy_confirmed 后下次再探的看门狗间隔倍率（×基准 2h）。默认 4 → ~8h 才再探，降低非常用模型自检频度。仅作用于健康态，不影响失败检测与熔断。",
			Unit:            "倍",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "probe.featured_backoff_multiplier",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(20), // floor to keep 5s rung non-zero (audit #6)
			Max:             floatPtr(100),
			Default:         50,
			Description:     "常用模型失败回退倍率（百分比）",
			DescriptionLong: "常用模型 node-probe 失败时的回退间隔按此百分比折算（默认 50%=缩短一半），使其更快重试恢复。100=与非常用一致。下限 20 保证首阶梯 (5s) 缩放后仍 ≥1s。",
			Unit:            "%",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "probe.featured_queue_priority",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(0),
			Max:             floatPtr(100),
			Default:         80,
			Description:     "常用模型探测队列优先级",
			DescriptionLong: "统一自检队列中常用模型任务的 Priority（高于 passive 50 / integrity 70），抢占式先执行。",
			Unit:            "",
			DangerLevel:     Safe,
			HotReload:       true,
		},

		// ── Network short-ladder long tail (P0-2, 2026-09-26) ─────────
		{
			Key:             "probe.network_chain_long_tail",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Default:         true,
			Description:     "网络类短梯长尾化",
			DescriptionLong: "网络/超时/5xx 等瞬时类失败退避在走完短梯 4 步（5s→15s→30s→60s）后进入长尾；2026-11-05 起长尾封顶为 probe.recovery_max_backoff_seconds（默认 5 分钟），不再沉到 1h/2h/6h，以便供应商恢复后及时复检。false=短梯封顶 60s 永续。见 docs/03-design/perf-2026-09-25-probe-cost-optimization.md §5 P0-2。",
			Unit:            "",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "probe.request_failure_min_gap_seconds",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(0),
			Max:             floatPtr(3600),
			Default:         60,
			Description:     "request_failure 触发 pair 级频控最小间隔（秒）",
			DescriptionLong: "业务失败触发（request_failure）入队前的 pair 级频控：该对已有排程中的探测（node_probe_state.next_retry_at 未到且行带错误证据）或距上次探测不足该秒数时，跳过本次触发——一次业务失败已足够触发验证，同分钟内第 N 条失败不应引发第 N 次探测。健康停放行（无错误证据）不受影响：新的真实失败仍立即重武装（INV-2）。0=现行行为（不频控）。",
			Unit:            "秒",
			DangerLevel:     Safe,
			HotReload:       true,
		},

		// ── Credential self-check rate-limit abort (P0-3) ──────────────
		{
			Key:             "probe.selfcheck.ratelimit_abort",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Default:         true,
			Description:     "自检 429 周期熔断",
			DescriptionLong: "凭据自检（credential-selfcheck-worker）收到网关 429 / gw_rpm_exceeded / key_throttled 时终止本凭据剩余候选模型，且下一周期只试主模型 1 次（周期间记忆）。语义：网关层拒绝=本凭据本周期不可用，逐个再试只是放大（docs/03-design/perf-2026-09-25-probe-cost-optimization.md §5 P0-3）。false=现行行为（fallback 逐个试完）。",
			Unit:            "",
			DangerLevel:     Safe,
			HotReload:       true,
		},
	}
}

// ── P0-2 accessors ───────────────────────────────────────────────────────

// ProbeNetworkChainLongTail returns whether the network-class short backoff
// ladder long-tails into the generic ladder (5m→1h→2h→6h) after the 4 short
// steps (5s/15s/30s/60s) instead of capping at 60s forever. ProbeBackoffForKind
// reads this on every call so the kill-switch is hot-reloadable.
func ProbeNetworkChainLongTail() bool {
	return GetPlatformBool("probe.network_chain_long_tail", true)
}

// ProbeRequestFailureMinGapSeconds returns the minimum gap between successive
// request_failure-triggered enqueues for the same (credential, model) pair.
// 0 disables the pair-level skip (legacy behavior, only the in-memory 5min
// in_flight dedup applies). Background lifecycle callers should keep this on
// the hot-reload path so operators can dial it during an upstream rate-limit
// storm without restarting.
func ProbeRequestFailureMinGapSeconds() int {
	return GetPlatformInt("probe.request_failure_min_gap_seconds", 60)
}
