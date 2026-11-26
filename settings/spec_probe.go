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
			// ★2026-11-26（探测回退周期上限轮）：上界压到 3600（1h）。
			//  指数退避是**探测重试梯**之一 ⇒ 「探测回退最多不超过 1 小时」
			//   要求它在配置面也无法被调到 1h 以上（否则上界只是默认值口径，
			//   旋钮一转就破口）。
			Max:         floatPtr(3600),
			Default:     3600,
			Description: "探测退避最大秒数",
			DescriptionLong: "指数退避的上限（秒），重试间隔不会超过此值。★ 2026-11-26 起上限与默认值均为 3600（1h）：探测回退周期的总口径是「逐层回退、最多不超过 1 小时」，本键是其中一条指数梯，与通用梯（bg/probe_backoff.go 的 NodeProbeBackoffChain，末档 1h）互相独立、同时受同一上限约束。",
			Unit:        "秒",
			DangerLevel: Safe,
			HotReload:   true,
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
			DescriptionLong: "ModelProbeRunner 为常用模型运行 chat-ping 深探测的周期。原硬编码 1800（30 分钟）；调小则常用模型自检更频繁。★ 2026-11-17：featuredCycleLoop 每轮结束后按当前值重武装 timer ⇒ HotReload=true（此前启动读一次，改设置需重启）。",
			Unit:            "秒",
			DangerLevel:     Safe,
			HotReload:       true,
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
			// ★2026-11-26（探测回退周期上限轮）：上界从 21600（6h）压到 3600（1h）。
			//  本键是网络/瞬时类长尾的**上限旋钮**；通用回退梯的末档已压到 1h
			//  ⇒ ⇒ 本键调到 1h 以上**不再有任何效果**（梯子先封顶）
			//  ⇒ ⇒ 继续允许 21600 会让「旋钮可调区间」与「实际效果」脱节
			//  ⇒ ⇒ 上界同步压到 3600。
			Max:         floatPtr(3600),
			Default:     300,
			Description: "可恢复探测退避上限（秒）",
			DescriptionLong: "瞬时/网络/5xx 等非永久失败的自动探测重试间隔**上限**（默认 300=5 分钟）。长尾仍是渐进的，但每一级都被削到本上限 ⇒ 默认下恒为 5 分钟，以便供应商恢复后约 5 分钟内被重新探测到。永久类（404/协议/配额固定节奏）不受此上限约束。" +
				" ★ ★ 2026-11-26 口径变更：★通用回退梯（bg/probe_backoff.go 的 NodeProbeBackoffChain）末档已由 6h 压到 **1h** ⇒ ⇒ 本键调到 3600 以上**不再产生任何效果**（通用梯先封顶）⇒ ⇒ 上界随之从 21600 压到 3600，避免「旋钮可调但无效」的脱节口径。★ 2026-09-25 P0-2 的 5m→1h→2h→6h 原长尾**不再能纯配置回退**（要恢复需改通用梯本身）。★ 调到 60仍是退化成短梯封顶。" +
				" ★ 代价（必须一起看）：★ 2026-09-25 P0-2 实测 60s 档滞留 45 对（connection_error/503/timeout/500），长尾化落地后 node_probe_runs ~5,000/h→~1,610/h。" +
				" ★ 本旋钮把长尾封顶从 6h 改到默认 5m ⇒ ★ 对【持续故障的对】稳态频率由 1 次/6h 变 1 次/5m（★ 72×）⇒ 以那 45 对计 ≈ +530 次/h（★ 约合 1,610/h 基线的 +33%）。" +
				" ★ 另一项同轮成本（2026-11-06 审计 P-8 补记）：recoveringSweeper 扫描周期由 30m 收紧到 5m ⇒ 持续故障对的最坏验证探测量上限由 480/day 升至 2,880/day（6×）；两项叠加请以 dashboard 的 node_probe_runs 频率实测为准。" +
				" ★ 换来的好处：供应商恢复后 ≤5 分钟内被复检。",
			Unit:        "秒",
			DangerLevel: Safe,
			HotReload:   true,
		},
		// ── Sustained-failure escalation (2026-11-07 探测浪费根修轮) ──────
		{
			Key:             "probe.sustained_failure_attempt_threshold",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(5), // 一代队列任务的最深梯级（nodeProbeMaxAttempts，2026-11-26 起为 5）
			Max:             floatPtr(1000),
			Default:         15, // 3 代完整梯走完仍零成功 ⇒ 判持续失败（3 × 5 档）
			Description:     "持续失败升级的尝试阈值",
			DescriptionLong: "node-probe 失败的 attempt 是跨代持续的（=node_probe_state.consecutive_failures+1，2026-09-26 P0-2）。attempt ≥ 本阈值且梯级本会被 probe.recovery_max_backoff_seconds 削平时，复检间隔升级到 probe.sustained_failure_recheck_seconds（默认 1h）而不是恒 5 分钟——「新失败 5 分钟内复检」的恢复 SLA 保留，只在**持续失败**时放慢盲探。恢复感知不降级：配额/可用性恢复事件仍即时回拉（RearmCredentialFailures），业务成功仍立即停放。★ 2026-11-26：通用回退梯末两级（2h/6h）被移除 ⇒ 一代任务的最深梯级由 7 变 5 ⇒ 本键下限随之 7→5、默认值由 21（3×7）改为 15（3×5）⇒ 「三代完整梯」这条口径保持不变。5=最早生效（第一代任务末段）。",
			Unit:            "次",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:         "probe.quota_exhausted_recheck_seconds",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryProbe,
			Min:         floatPtr(300),
			Max:         floatPtr(86400),
			Default:     21600,
			Description: "配额耗尽对的停放复检间隔（秒）",
			DescriptionLong: "配额族裁决（余额/配额耗尽，errorsx KindQuotaPermanent/KindQuotaBalance/KindQuota）**不再**走通用失败梯子，而是停放到本间隔（默认 21600=6h）。" +
				"★ 实测背景（2026-11-20，211）：apiclaude 凭据 56 对 claude-sonnet-5-5 连回 403 INSUFFICIENT_BALANCE，通用梯子对着一个余额已耗尽的节点每轮注定失败 ⇒ 20 个模型对各连败 175~489 次、每 3 小时一轮空打。" +
				"★ 绑定面的冷却时长与探针自己的下一跳**取同一个数**（bg/probe_recovery_policy.go quotaVerdict.horizon）⇒ 绑定面不再写「5 分钟后复检」而探针 3 小时后才动的假账。" +
				"★ 恢复感知不降级：凭据级余额复检探针（bg/balance_quota_probe.go）发现充值 ⇒ RearmCredentialFailures 立即回拉本对的失败行 ⇒ 泵 30s 内接管验证。" +
				"★ 调小=更频繁复检（更贵、余额耗尽时是注定失败的请求）；调大=更省但充值后回拉更依赖事件。周期族（KindQuotaPeriodic）不受本键影响——它钉在上游给出的重置点+60s。" +
				"★ ★ 2026-11-26 登记（诚实边界）：本键是**配额族的固定重查节奏**，**不是**逐层回退梯 ⇒ ⇒ 「探测回退周期最多不超过 1 小时」的口径覆盖的是通用梯与指数梯（末档均 1h），**不覆盖**本键（默认 6h）。★ 理由：调小本键会让「余额已耗尽」的绑定以更密频率去打注定失败的请求（2026-11-20 实测：56 对连败 175~489 次、每3 小时一轮空打）⇒ 与本轮「压探测成本」的目标相反。★ 若 owner 要求全口径 ≤1h，把本键调到 3600 即可（纯配置，无需改代码）。",
			Unit:        "秒",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:             "probe.sustained_failure_recheck_seconds",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(300), // 不得低于恢复 SLA 下限
			Max:             floatPtr(3600),
			Default:         3600,
			Description:     "持续失败对的复检间隔（秒）",
			DescriptionLong: "attempt 达到 probe.sustained_failure_attempt_threshold 后，可恢复类失败的下次自动复检间隔（默认 3600=1h；绑定冷却 unavailableBindingHorizon 同步升级）。实测背景（2026-11-07，211）：SLA 封顶把持续故障对钉在 ~5 分钟一探 ⇒ 单对 ~480 次/天、consecutive_failures 累积至 ~1000 仍不停（spec 里 probe.recovery_max_backoff_seconds 已登记该 72× 成本）。调大=更省但恢复发现更慢；调到 300=回到恒 5 分钟口径（等效关闭升级）——★ 注意该等价只在 probe.recovery_max_backoff_seconds=300（默认）时成立：若把恢复 SLA 调大，本键的有效值会被 SLA 下限托起（bg/probe_recovery_policy.go 的 sustainedFailureRecheck 以 SLA 为下限）⇒ 「300=关闭升级」的说法不适用。★ 2026-11-26：本键同时是回退梯的**持续失败档封顶**（bg/probe_recovery_policy.go 的 capRecoverBackoffForAttempt）⇒ 为兑现「探测回退周期最多不超过 1 小时」，上界由 86400 压到 3600 ⇒ 本键**调不到** 1h 以上（默认 3600 本身即 1h）。",
			Unit:            "秒",
			DangerLevel:     Safe,
			HotReload:       true,
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

		// ── 自动探测作用域（2026-11-26 探测工作模式修正轮） ────────────────
		{
			Key:             "probe.full_model_probe",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Default:         false,
			Description:     "模型全量探测",
			DescriptionLong: "自动探测的**模型全集**开关。false（默认）⇒ 只探测「近 7 天被客户端正常调用过」的模型 + 运维静态精选清单（routing_policy.featured_models 等）里的模型；「不知状态」（既没被精选、近期也没有真实业务用量）的模型一律不自动探测，需要时手工探。true ⇒ 连「不知状态」的模型也纳入自动探测（回到 2026-11-26 之前的行为）。" +
				"★ 「被客户端正常调用」的判定口径（bg/probe_model_scope.go）：统计会话族（session_turns ∪ session_turns_hot）近 7 天的**业务**流量，并**排除探测自身流量**（origin_stage='business' AND task_type<>'probe_triggered' AND origin_actor 不在探针 actor 词表）⇒ ⇒ 探测不会把自己的流量算成「使用」，模型不会因为被反复探测而永久留在范围内。" +
				"★ ★ 诚实登记：本键为 true 时**不能**突破「图形类/视频类默认不探测」那道门（见 probe.multimodal_auto_probe）—— 两个开关是**正交**的：full_model_probe 管「模型全集的宽窄」，multimodal_auto_probe 管「媒体生成这一类是否参与」。★ 本键为 false 时静态精选清单仍被豁免（运维显式策展的白名单），这是对「只探测 7 天内被正常调用的模型」的一处**有意偏离**，登记在此供 owner 复议。",
			Unit:            "",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "probe.multimodal_auto_probe",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Default:         false,
			Description:     "多模态（图形/视频类）模型自动探测",
			DescriptionLong: "false（默认）⇒ **图形类、视频类模型不参与自动探测**（识别器：bg/probe_multimodal_model.go，按生产库真实模型清单整词匹配，并豁免 nvidia/ai-synthetic-video-detector 这类「名字带 video 但实为分类器」的模型）。true ⇒ 这些模型照常按作用域门参与自动探测。★ 理由：媒体生成模型的单次探测成本远高于文本探针（一次真实出图/出片），而自动探测是**周期性**发生的 ⇒ 成本不可忽略（2026-11-26 实测：gpt-image 家族的请求100% 是网关自己的探针流量，业务流量 0 条）。" +
				"★ ★ 诚实登记：本键为 false 时，图形/视频类模型的**凭据恢复状态不再被自动更正**（上游恢复后，绑定仍可能停在 unavailable）⇒ ⇒ 需要时置本键为 true，或由运维手工触发探测。★ 本键**不影响**手工探测（admin 手工探测从不过作用域门）。",
			Unit:            "",
			DangerLevel:     Safe,
			HotReload:       true,
		},
	}
}

// ── P0-2 accessors ───────────────────────────────────────────────────────

// ProbeNetworkChainLongTail returns whether the network-class short backoff
// ladder long-tails into the generic ladder after the 4 short steps
// (5s/15s/30s/60s) instead of capping at 60s forever. ProbeBackoffForKind
// reads this on every call so the kill-switch is hot-reloadable.
//
// ★ 2026-11-26（探测回退周期上限轮）：长尾档位随通用梯末档由 6h 压到 **1h**
// 而变化 —— 通用梯现为 5s→30s→60s→5m→1h ⇒ 网络长尾取通用梯的 [5m,1h] 尾段。
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

// ── 探测作用域访问器（2026-11-26 探测工作模式修正轮） ─────────────────────

// ProbeFullModelProbe returns whether automatic probing covers models with
// unknown status (no static featured entry AND no recent business usage).
//
// false (default) ⇒ the automatic-probe scope is limited to models that real
// business traffic called within the last 7 days, plus the operator-curated
// featured list. true ⇒ every model is a candidate again.
//
// Read on every scope decision so the switch is hot-reloadable.
func ProbeFullModelProbe() bool {
	return GetPlatformBool("probe.full_model_probe", false)
}

// ProbeMultimodalAutoProbe returns whether graphics-generation and
// video-generation models take part in automatic probing.
//
// false (default) ⇒ they are skipped (their per-probe cost is orders of
// magnitude above a text probe). true ⇒ they follow the normal scope gate.
//
// Read on every scope decision so the switch is hot-reloadable.
func ProbeMultimodalAutoProbe() bool {
	return GetPlatformBool("probe.multimodal_auto_probe", false)
}

// ProbeSustainedFailureAttemptThreshold returns the cross-generation attempt
// count at which the recovery SLA stops compressing the probe ladder.
//
// ★ 单源存在这里的理由（2026-11-26）：这个阈值有两个读者 ——
// bg/probe_recovery_policy.go 的 capRecoverBackoffForAttempt，以及
//   domains/streaming/executors/executor.go 的 recordModelNotFound ——
//   而**后者不能 import bg**（bg → domains/streaming → bg 会成环，
//   见 bg/active_probe_emitter.go 的注释）⇒ ⇒ 把读取点提到 settings，
//   两边共用同一个 getter ⇒ ⇒ 两边的默认值不会分叉。
//
// ★ 2026-11-26：默认值 21 → **15**（通用回退梯末两级移除 ⇒ 一代任务
//   最深梯级 7 → 5 ⇒ 「三代完整梯」= 3 × 5 = 15，与原先 3 × 7 = 21 同义）。
func ProbeSustainedFailureAttemptThreshold() int {
	return GetPlatformInt("probe.sustained_failure_attempt_threshold", 15)
}
