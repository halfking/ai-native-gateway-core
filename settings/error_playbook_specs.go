package settings

// ErrorPlaybookSpecs 错误处理手册（Error Handling Playbook）的平台级开关族。
//
// 手册语义：错误命中手册条目 ⇒ 按预定方案标准执行；未知错误 ⇒ PI 智能体
// log+上下文分析 → 根因 → 方案 → 门控执行 → 落一条新条目供后续标准模式。
//
// ★ 默认全部关闭（与 PI 错误采集/分析同款默认）：
//
//	① 自动化会读错误日志并可能执行白名单内的变更动作 —— 是否让系统
//	  「自己动生产状态」是 owner 的决策面，不是出厂行为；
//	② 未知错误分析会调外部分析端点（LLM 花费）—— 默认关避免意外开销。
//	管理面（手册 CRUD/手动执行/手动分析）不受这些开关影响，始终可用。
func ErrorPlaybookSpecs() []*Spec {
	return []*Spec{
		{
			Key:         "error_playbook.enabled",
			EnvName:     "LLM_GATEWAY_ERROR_PLAYBOOK_ENABLED",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategorySecurity,
			Default:     false,
			Description: "启用错误处理手册自动化（摄取错误窗口→匹配→标准执行→未知错误 PI 分析→自动完善）",
			DescriptionLong: "启用后后台 worker 每 5 分钟（可调）摄取 candidate_failure_logs_hot 的错误窗口，按 error_kind 匹配错误处理手册：" +
				"命中且条目 auto_execute 开启 ⇒ 按预定方案标准执行（白名单动作 + 旧值快照 + 冷却期）；" +
				"未知错误累计达阈值 ⇒ PI 智能体做 log+上下文分析，产出根因与执行方案，过置信度门后执行一次修正并落一条新手册条目；" +
				"标准执行失败的条目带失败上下文自动重新提炼。关闭后仅保留管理面的手册 CRUD 与手动执行/分析。",
			HotReload:   true,
			DangerLevel: Safe,
		},
		{
			Key:         "error_playbook.auto_analyze",
			EnvName:     "LLM_GATEWAY_ERROR_PLAYBOOK_AUTO_ANALYZE",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategorySecurity,
			Default:     true,
			Description: "未知错误自动触发 PI 智能体分析（总开关开启时生效）",
			DescriptionLong: "未知错误类型（手册无条目）累计出现 ≥ error_playbook.kind_threshold 次后，自动调用外部分析端点做 log+上下文分析。" +
				"关闭后未知错误只做计数（管理面 pending_unknown 可见），由管理员手动触发 /analyze。",
			HotReload:   true,
			DangerLevel: Safe,
		},
		{
			Key:         "error_playbook.auto_execute",
			EnvName:     "LLM_GATEWAY_ERROR_PLAYBOOK_AUTO_EXECUTE",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategorySecurity,
			Default:     true,
			Description: "标准模式允许自动执行（仍需条目自身 auto_execute=true 与冷却期）",
			DescriptionLong: "全局执行闸。关闭后命中条目只记 awaiting_approval（管理面可见、可手动执行）。" +
				"注意：即使本闸开启，变更类动作也受条目级 auto_execute 与（PI 产出方案的）0.80 置信度门约束。",
			HotReload:   true,
			DangerLevel: Safe,
		},
		{
			Key:         "error_playbook.kind_threshold",
			EnvName:     "LLM_GATEWAY_ERROR_PLAYBOOK_KIND_THRESHOLD",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategorySecurity,
			Default:     3,
			Description: "未知错误累计出现多少次才触发 PI 分析（防抖 + 省 LLM 花费）",
			HotReload:   true,
			DangerLevel: Safe,
		},
		{
			Key:         "error_playbook.execution_cooldown_minutes",
			EnvName:     "LLM_GATEWAY_ERROR_PLAYBOOK_COOLDOWN_MINUTES",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategorySecurity,
			Default:     30,
			Description: "同一条目两次自动执行的最小间隔（分钟）",
			DescriptionLong: "错误风暴下防止同一条目被反复执行（每次执行都有留痕与副作用）。" +
				"冷却期内的命中会记 skipped 执行（管理面可见），不会丢失可观测性。",
			HotReload:   true,
			DangerLevel: Safe,
		},
		{
			Key:         "error_playbook.analyze_interval_seconds",
			EnvName:     "LLM_GATEWAY_ERROR_PLAYBOOK_INTERVAL_SECONDS",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategorySecurity,
			Default:     300,
			Description: "错误摄取窗口的轮询间隔（秒）",
			HotReload:   false,
			DangerLevel: Safe,
		},
		{
			Key:             "error_playbook.llm_timeout_seconds",
			EnvName:         "LLM_GATEWAY_ERROR_PLAYBOOK_TIMEOUT_SEC",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryTimeout,
			Default:         150,
			Description:     "单次 PI 分析 LLM 调用的超时（秒，5..600）",
			DescriptionLong: "与 PI 错误分析同款默认 150s：推理模型对分析级大提示词实测贴线（245 生产实证 60s 会灭）。",
			HotReload:       false,
			DangerLevel:     Safe,
		},
	}
}
