package settings

// ProviderProfileSpecs returns all provider profile system specs (Phase 1, 2026-07-26).
//
// ★ 2026-10-21 R72f（属主决策项 #10 的前半）修了两处**「配置了也不生效」**：
//
//	① **Scope 缺失**（本文件原有 4 个 spec 全都漏了 `Scope`）
//	   ⇒ `Registry.RegisterSpec`（settings/spec.go:239）**不归一化 Scope**
//	   ⇒ 零值 `""` 既不是 `ScopePlatform`（"platform"）也不是 `ScopeTenant`
//	   ⇒ `EffectiveValue`（:284）第一步 `r.backends[scope]` 用 `""` 去查
//	   ⇒ **查不到 ⇒ DB（settings_kv）里的值永远读不到**
//	   ⇒ 且第二步 env 分支要求 `scope == ScopePlatform` ⇒ **env 那条路也走不到**
//	   ⇒ ⇒ **⇒ 这 4 个键是「彻底不可配」**：写 settings_kv 无效、设 env 也无效，
//	     代码只能一直走调用方给的 fallback。
//	   ⇒ ⇒ ★ 2026-10-21 实测：211/252 的 settings_kv 里**早就写着**
//	     `provider_profile.enabled = true`，而生产上它**一直是被忽略的**
//	     （代码实际取 fallback=false）⇒ ⇒ ★★ 补上 Scope 之后，**这个值会
//	     第一次真正生效** ⇒ ⇒ ⇒ **这是一处「生产行为变化」，上线前必须知情**。
//
//	② **键从未注册**（新增 5 个 spec）
//	   ⇒ `provider_profile.cost_reconciliation.*` 与 `provider_profile.alert_interval`
//	     只出现在读方（cmd/gateway/provider_profile_init.go）⇒ 从未 `Register`
//	     ⇒ `Global.Spec(key)` 返回 nil ⇒ `GetPlatformBool` 直接回退 fallback
//	     ⇒ ⇒ **写 settings_kv 完全无效**（连 spec 都没有）。
//
// ★ 零语义风险承诺：★ 注册本身**不改生效值** —— `getPlatformBool`
//
//	（settings/helpers.go:83）在「spec 存在但 kv 无行」时**仍然返回调用方的
//	fallback**（不是 spec 的 Default）⇒ ⇒ ⇒ 所以只要 kv 里没行，注册前后
//	行为逐字一致；★ 而 `Default` 字段必须与读方的 fallback **对齐**，这样
//	即使将来有别的读法（如 `Global.EffectiveValue` 直接取值）也不会漂移。
func ProviderProfileSpecs() []*Spec {
	return []*Spec{
		{
			Key:         "provider_profile.enabled",
			Type:        TypeBool,
			EnvName:     "LLM_GATEWAY_PROVIDER_PROFILE_ENABLED",
			Scope:       ScopePlatform,
			Default:     false,
			Category:    CategoryGeneral,
			HotReload:   true,
			Description: "启用供应商画像系统（7维度供应商质量评分）",
		},
		{
			Key:         "provider_profile.collection_interval",
			Type:        TypeInt,
			EnvName:     "LLM_GATEWAY_PROVIDER_PROFILE_COLLECTION_INTERVAL",
			Scope:       ScopePlatform,
			Default:     7200,
			Min:         float64Ptr(300),
			Max:         float64Ptr(86400),
			Category:    CategoryGeneral,
			HotReload:   true,
			Description: "指标采集间隔（秒），默认2小时",
			Unit:        "秒",
		},
		{
			Key:         "provider_profile.aggregation_interval",
			Type:        TypeInt,
			EnvName:     "LLM_GATEWAY_PROVIDER_PROFILE_AGGREGATION_INTERVAL",
			Scope:       ScopePlatform,
			Default:     86400,
			Min:         float64Ptr(3600),
			Max:         float64Ptr(259200),
			Category:    CategoryGeneral,
			HotReload:   true,
			Description: "每日聚合间隔（秒），默认24小时",
			Unit:        "秒",
		},
		{
			Key:         "provider_profile.cleanup_interval",
			Type:        TypeInt,
			EnvName:     "LLM_GATEWAY_PROVIDER_PROFILE_CLEANUP_INTERVAL",
			Scope:       ScopePlatform,
			Default:     604800,
			Min:         float64Ptr(86400),
			Max:         float64Ptr(2592000),
			Category:    CategoryGeneral,
			HotReload:   true,
			Description: "历史数据清理间隔（秒），默认7天",
			Unit:        "秒",
		},

		// ---- 2026-10-21 R72f 新注册（读方：cmd/gateway/provider_profile_init.go）----

		{
			// 读方：settings.GetPlatformDuration("provider_profile.alert_interval", 86400)
			Key:         "provider_profile.alert_interval",
			Type:        TypeInt,
			EnvName:     EnvNameAuto("provider_profile.alert_interval"),
			Scope:       ScopePlatform,
			Default:     86400,
			Min:         float64Ptr(300),
			Max:         float64Ptr(259200),
			Category:    CategoryGeneral,
			HotReload:   true,
			Description: "画像告警扫描间隔（秒），默认24小时",
			Unit:        "秒",
		},
		{
			// 读方：settings.GetPlatformBool("provider_profile.cost_reconciliation.enabled", false)
			// ★ 该 env（LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_ENABLED）是
			//   `costReconciliationEnabled()` 的**第一优先**读法；本键是它
			//   的**后备路**（cmd/gateway/provider_profile_init.go:264）。
			//   ★ 后备路此前是**死的**（键从未注册 ⇒ Spec()==nil ⇒ 回退 false），
			//   本 spec 注册后它才真正可用。
			Key:         "provider_profile.cost_reconciliation.enabled",
			Type:        TypeBool,
			EnvName:     EnvNameAuto("provider_profile.cost_reconciliation.enabled"),
			Scope:       ScopePlatform,
			Default:     false,
			Category:    CategoryGeneral,
			HotReload:   true,
			Description: "启用费用对账（网关用量 vs 上游账单差异）",
			DescriptionLong: "开启后由独立的对账 worker 周期性比对网关侧用量与上游账单，" +
				"差异超阈值时落库并产生事件。★ 能力自包含（PG 聚合/存储/事件 sink），" +
				"不依赖供应商画像开关。★ 与 env 变量 LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_ENABLED " +
				"并存时**以 env 为准**（env 是第一优先读法）。",
		},
		{
			// 读方：settings.GetPlatformDuration("provider_profile.cost_reconciliation.interval", 86400)
			Key:         "provider_profile.cost_reconciliation.interval",
			Type:        TypeInt,
			EnvName:     EnvNameAuto("provider_profile.cost_reconciliation.interval"),
			Scope:       ScopePlatform,
			Default:     86400,
			Min:         float64Ptr(300),
			Max:         float64Ptr(259200),
			Category:    CategoryGeneral,
			HotReload:   true,
			Description: "费用对账周期（秒），默认24小时",
			Unit:        "秒",
		},
		{
			// 读方：settings.GetPlatformFloat("provider_profile.cost_reconciliation.cost_diff_threshold", 0.05)
			Key:         "provider_profile.cost_reconciliation.cost_diff_threshold",
			Type:        TypeFloat,
			EnvName:     EnvNameAuto("provider_profile.cost_reconciliation.cost_diff_threshold"),
			Scope:       ScopePlatform,
			Default:     0.05,
			Min:         float64Ptr(0),
			Max:         float64Ptr(1),
			Category:    CategoryGeneral,
			HotReload:   true,
			Description: "费用差异告警阈值（相对比例）",
			Unit:        "比例",
		},
		{
			// 读方：settings.GetPlatformFloat("provider_profile.cost_reconciliation.token_diff_threshold", 0.05)
			Key:         "provider_profile.cost_reconciliation.token_diff_threshold",
			Type:        TypeFloat,
			EnvName:     EnvNameAuto("provider_profile.cost_reconciliation.token_diff_threshold"),
			Scope:       ScopePlatform,
			Default:     0.05,
			Min:         float64Ptr(0),
			Max:         float64Ptr(1),
			Category:    CategoryGeneral,
			HotReload:   true,
			Description: "token 差异告警阈值（相对比例）",
			Unit:        "比例",
		},
	}
}

// float64Ptr is declared in spec_modules.go
