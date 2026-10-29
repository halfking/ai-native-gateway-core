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
				"★ 手动单点检测不受本项限制。",
			DangerLevel: Safe,
			HotReload:   false,
		},
	}
}
