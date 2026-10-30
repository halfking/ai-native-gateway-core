package settings

// CategoryModelQuality groups all model-quality-related settings.
const CategoryModelQuality Category = "model_quality"

// ModelQualitySpecs returns all platform-scoped model quality monitoring specs.
//
// The model quality worker periodically runs MMLU benchmark tests against
// featured models to detect quality degradation (model "water leakage").
func ModelQualitySpecs() []*Spec {
	return []*Spec{
		{
			Key:         "model_quality.enabled",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     true, // 2026-08-29: 默认开启，管理后台"模型智商"检测依赖此开关
			Description: "是否启用模型质量监控",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "model_quality.interval_hours",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     24,
			Min:         floatPtr(1),
			Max:         floatPtr(168), // 最多一周
			Description: "质量检测周期（小时）",
			Unit:        "小时",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "model_quality.use_lite_benchmark",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     true, // 默认使用快速测试（50题）
			Description: "是否使用精简测试（50题快速测试 vs 完整测试）",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "model_quality.alert_threshold",
			Type:        TypeFloat,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     5.0,
			Min:         floatPtr(1.0),
			Max:         floatPtr(20.0),
			Description: "质量下降告警阈值（准确率下降百分比）",
			Unit:        "%",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "model_quality.data_dir",
			Type:        TypeString,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     "./data",
			Description: "质量数据存储目录",
			DangerLevel: Safe,
			HotReload:   false, // 数据目录不支持热更新
		},
		{
			Key:         "model_quality.api_key",
			Type:        TypeString,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     "",
			Description: "质量测试专用API Key（为空则使用系统API Key）",
			DescriptionLong: "用于调用网关进行质量测试的API Key。" +
				"留空则使用系统默认的selfCheckAPIKey。" +
				"建议为质量测试创建专用的API Key以便单独追踪token消耗。",
			DangerLevel: Warning, // API Key属于敏感配置
			HotReload:   true,
		},
		{
			Key:         "model_quality.base_url",
			Type:        TypeString,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			// 2026-10-29 (24h 审计第 7 轮): 默认改为**空** ⇒ 调用方
			// (bg.NewModelQualityWorker / NewGatewayModelInvoker) 落
			// loopback.GatewayBase()（读 LLM_GATEWAY_LISTEN，蓝绿感知）。
			// 原默认 http://localhost:8787 是一个从未存在的监听端口——
			// 211 生产实测每次 24h 定时检查 12 模型全部 dial refused
			// （error_count=50/50, grade=F），且经 EffectiveValue 的
			// spec-default 链路把旧值顶回 main.go 的空判 ⇒ 修 worker 默认
			// 是死代码，必须改这里（单一事实源）。
			Default:     "",
			Description: "网关基础URL（用于质量测试调用；留空 = 跟随本实例监听端口）",
			DescriptionLong: "模型质量测试时调用的网关地址。" +
				"留空（推荐）= 自动跟随本实例监听端口（loopback.GatewayBase）；\n" +
				"跨机测试时可显式指定，如 http://10.177.48.245:8781。",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "model_quality.test_timeout_seconds",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     30,
			Min:         floatPtr(10),
			Max:         floatPtr(120),
			Description: "单个测试请求超时时间（秒）",
			Unit:        "秒",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			// 2026-08-11: 启用按凭据节点直连测试。开启后 worker 会用
			// DirectNodeInvoker 绕过网关、直连每个 (凭据, 模型) 节点测量智商，
			// 写入 model_iq_runs / node_iq_latest。需要 DB + 解密 key 可用，
			// 否则降级为仅经网关聚合测试。前端「立即测试」按钮与可疑动作触发
			// 均依赖此开关。
			Key:         "model_quality.enable_per_node",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     false, // 默认关闭，避免产生真实 token 费用
			Description: "是否启用按凭据节点直连智商测试（产生真实 token 费用）",
			DescriptionLong: "开启后对每个活跃 (凭据, 模型) 节点直连上游跑精简智商测试，" +
				"结果写入 model_iq_runs / node_iq_latest，供供应商模型列表与品质计算读取。" +
				"需要 DB + 凭据解密 key 可用。关闭时仅做经网关的聚合测试（不计节点维度）。",
			DangerLevel: Warning, // 会产生真实 token 费用
			HotReload:   true,
		},
		{
			// 2026-10-30 (24h 审计第 7 轮)：per-node IQ 的范围收窄。
			// featured = routing_policy.featured_models 命中的绑定（与
			// model_credibility.scope 的 featured 同一口径）。无收窄时
			// 每次 boot 对全量活跃绑定打真实请求（211 ~3500 绑定）。
			Key:         "model_quality.per_node_scope",
			Type:        TypeString,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     "featured",
			Description: "per-node 智商测试的节点范围（featured=仅特色模型，all=全量）",
			DescriptionLong: "featured（默认）只测 routing_policy.featured_models 命中的绑定，" +
				"与 model_credibility.scope 的 featured 同口径；all 不收窄（token 成本高，慎用）。",
			DangerLevel: Safe,
			HotReload:   false, // 每次 per-node 周期开始时读取，无需热更
		},
		{
			// 2026-10-30: 成功 IQ 实测在此窗口内的节点跳过——防止每次部署
			// 重启的「启动立即跑一次」反复打同一批节点（商汤 429 教训）。
			Key:         "model_quality.per_node_min_interval_hours",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     24,
			Min:         floatPtr(0),
			Max:         floatPtr(168),
			Description: "per-node 智商测试的最小重测间隔（小时；窗口内已成功实测的节点跳过）",
			DescriptionLong: "窗口内已有成功 IQ 实测的节点跳过本轮；0 = 不跳过（每次 boot 全量重测，" +
				"token 成本高，慎用）。默认 24 与定时周期一致。",
			DangerLevel: Safe,
			HotReload:   false,
		},
		{
			// 2026-10-30 (24h 审计第 8 轮)：失败实测的去重窗。211 实测：
			// dedup 只认 success ⇒ EOL/坏节点每个 scheduled 周期都全量重烧
			// （24h 1568 跑里 scheduled failed 763 次、同模型最多 92 次），
			// 每跑 50 题都是真实 token。失败节点按独立（更短）的窗口冷却：
			// 失败可能是瞬时的（上游抖动），窗太长会让故障恢复后迟迟不被重测。
			Key:         "model_quality.per_node_failed_interval_hours",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     4,
			Min:         floatPtr(0),
			Max:         floatPtr(72),
			Description: "per-node 失败实测的冷却间隔（小时；窗口内刚失败的节点跳过）",
			DescriptionLong: "窗口内已有失败 IQ 实测的节点跳过本轮（与成功窗独立判断，取更近的一扇）；" +
				"0 = 失败不冷却（每个周期都重试，token 成本高，慎用）。默认 4 小时：" +
				"既拦住每周期的重试风暴，又保证上游恢复后当日内能被重测。",
			DangerLevel: Safe,
			HotReload:   false,
		},
	}
}
