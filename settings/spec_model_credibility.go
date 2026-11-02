package settings

// CategoryModelCredibility groups all model-credibility (anti-downgrade)
// settings.
//
// The model credibility worker periodically probes each (credential, model)
// node with one tiny request to check whether the upstream is actually
// serving the model it claims to serve, then writes the verdict into the
// provider's model-quality / credibility parameters.
const CategoryModelCredibility Category = "model_credibility"

// ModelCredibilitySpecs returns all platform-scoped model credibility specs.
//
// The probe costs real tokens (one small request per node per cycle), so the
// interval and the master switch are both operator-visible.
func ModelCredibilitySpecs() []*Spec {
	return []*Spec{
		{
			Key:         "model_credibility.enabled",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategoryModelCredibility,
			Default:     false,
			Description: "是否启用模型可信度检测（防掺水/防偷换模型）",
			DescriptionLong: "默认关闭。打开后按周期对每个 (凭据,模型) 节点直连上游发一次标定请求，" +
				"判定上游给的是不是它声称的那个模型。" +
				"★ 会消耗真实 token，并打到供应商限流。单节点单次约 35 token，默认 6 小时一轮。" +
				"★ 只读探测：不改变上游状态，也不改路由。",
			DangerLevel: Warning,
			HotReload:   false,
		},
		{
			Key:         "model_credibility.interval_hours",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryModelCredibility,
			Default:     6,
			Min:         floatPtr(1),
			Max:         floatPtr(168), // 最多一周
			Description: "模型可信度检测周期（小时）",
			DescriptionLong: "扫描周期。掺水是持续行为不是瞬时事件 ⇒ 频率太高只是烧钱；" +
				"但也不能太慢（掺水方常在客户投诉后才切回真货）⇒ 默认 6 小时。",
			DangerLevel: Safe,
			HotReload:   false,
		},
		{
			Key:         "model_credibility.probe_timeout_seconds",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryModelCredibility,
			Default:     60,
			Min:         floatPtr(10),
			Max:         floatPtr(180),
			Description: "单次可信度探测超时（秒）",
			DescriptionLong: "单次身份探测的上游超时。默认 60 秒：推理模型在 max_tokens=1024 " +
				"预算下可能思考较久，超时太短会把「慢」误判成「探测失败」。",
			DangerLevel: Safe,
			HotReload:   false,
		},
		{
			// 扫描面：featured = routing_policy.featured_models 命中的绑定；
			// all = 全部活跃绑定。
			//
			// ★ 默认 featured 的理由：探测消耗真实 token 并打到上游限流，
			// 全量目录里有大量未投流的长尾绑定，扫它们花钱又不出结论。
			// featured 与 model_probe 的 featuredCycle / shared_pick 的
			// Priority 2 同一张清单 ⇒ 三个探测层对「哪些模型值得看」口径一致。
			Key:         "model_credibility.scope",
			Type:        TypeString,
			Scope:       ScopePlatform,
			Category:    CategoryModelCredibility,
			Default:     "featured",
			Options:     []string{"featured", "all"},
			Description: "可信度扫描面：featured（只扫特色模型）| all（全部活跃绑定）",
			DescriptionLong: "featured = 只扫 routing_policy.featured_models 命中的 (凭据,模型) 绑定" +
				"（与模型探测 featuredCycle、探针模型选择 shared_pick 同一张特色清单）；" +
				"all = 扫全部活跃绑定（长尾模型也会被打真实请求，token 成本显著上升）。" +
				"★ 手动单点检测不受本项限制。" +
				"★ 探活成功触发（trigger_on_probe_success）在 featured 下同样只打特色模型。",
			DangerLevel: Safe,
			HotReload:   false,
		},
		{
			// B 方案：探活成功后异步触发独立可信度扫描（另发标定请求）。
			// 与 enabled（定时全量）独立 —— 可只开触发、关定时，以探活成功
			// （30 天停泊后偶发）作为低成本入口。
			Key:         "model_credibility.trigger_on_probe_success",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategoryModelCredibility,
			Default:     true,
			Description: "探活成功后是否异步触发独立的模型可信度扫描",
			DescriptionLong: "默认开。探活成功（node_probe）后另发一次标定请求做身份/指纹判定，" +
				"**不复用**探活请求体。受 cooldown / featured scope / inflight 去重约束。" +
				"★ 不挂在业务成功（MarkNodeProbeHealthy）上，避免真实流量放大标定成本。" +
				"★ 与 model_credibility.enabled（定时扫描）独立：可关定时、只靠探活触发。",
			DangerLevel: Warning,
			HotReload:   true,
		},
		{
			Key:         "model_credibility.probe_success_cooldown_hours",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryModelCredibility,
			Default:     6,
			Min:         floatPtr(1),
			Max:         floatPtr(168),
			Description: "探活成功触发可信度扫描的冷却（小时，verified 结论）",
			DescriptionLong: "上次判定为 verified 且距今不足本值 ⇒ 跳过本次触发。" +
				"默认 6 小时，与定时扫描周期对齐，避免探活成功把扫描频率抬高。",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "model_credibility.probe_success_recheck_hours",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryModelCredibility,
			Default:     1,
			Min:         floatPtr(1),
			Max:         floatPtr(168),
			Description: "探活成功触发可信度扫描的短冷却（小时，suspected/diluted/unknown）",
			DescriptionLong: "上次判定异常或 unknown 时用更短冷却，便于复测；" +
				"不得超过 probe_success_cooldown_hours。",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			// C 方案：真实会话 integrity 异常 → 异步可信度扫描。
			Key:         "model_credibility.trigger_on_integrity_anomaly",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategoryModelCredibility,
			Default:     true,
			Description: "检出 model_mismatch / fingerprint_drift（high/critical）后是否异步触发可信度扫描",
			DescriptionLong: "默认开。真实会话（或指纹漂移 worker）打出掺水信号后另发标定请求。" +
				"受 integrity_anomaly_cooldown / featured scope / trusted_skip / inflight 约束。" +
				"★ 与 IntegrityProbePlanner 的 integrity_verify（探活核实）正交。" +
				"★ 挂钩在采样前，不会被 integrity sampleRatio 悄悄掏空。",
			DangerLevel: Warning,
			HotReload:   true,
		},
		{
			Key:         "model_credibility.integrity_anomaly_cooldown_hours",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryModelCredibility,
			Default:     1,
			Min:         floatPtr(1),
			Max:         floatPtr(168),
			Description: "integrity 异常触发可信度扫描的冷却（小时）",
			DescriptionLong: "距上次可信度判定不足本值 ⇒ 跳过。默认 1 小时；" +
				"不受 verified 长冷却压制（异常本身就是复测理由）。",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "model_credibility.trusted_skip",
			Type:        TypeString,
			Scope:       ScopePlatform,
			Category:    CategoryModelCredibility,
			Default:     "[]",
			Description: "信任跳过名单（JSON 数组）：自动扫描跳过这些模型",
			DescriptionLong: "条目形态：`model` 或 `provider/model`（也认 `provider:model`）。" +
				"命中后定时扫描 / 探活成功触发 / integrity 异常触发一律跳过。" +
				"★ 手动「立即检测」不受本名单限制。" +
				"例：[\"gpt-4o\",\"openai/gpt-4o\",\"anthropic:claude-sonnet-4-5\"]。",
			DangerLevel: Safe,
			HotReload:   true,
		},
	}
}
