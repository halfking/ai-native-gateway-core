package settings

// GatewaySpecs returns hot-reloadable gateway entry-guard settings.
func GatewaySpecs() []*Spec {
	return []*Spec{
		{
			Key:             "gateway.max_prompt_tokens",
			EnvName:         "LLM_GATEWAY_MAX_PROMPT_TOKENS",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategorySecurity,
			Min:             floatPtr(0),
			Max:             floatPtr(2097152),
			Default:         2097152,
			Description:     "入口 prompt 接收上限（估算 tokens）",
			DescriptionLong: "三个协议入口最多接收 2097152（2M）估算 tokens；超过后在 JSON 解析和上游转发前返回 413 prompt_too_large。该值是网关接收上限，不是供应商模型上下文窗口；候选解析后按实际供应商上下文窗口的 80% 触发压缩。0 表示关闭网关上限。通过系统配置修改后约 5 秒内热生效。",
			Unit:            "tokens",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             "gateway.mode",
			EnvName:         "LLM_GATEWAY_MODE",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryGeneral,
			Options:         []string{"simple", "expert"},
			Default:         "expert",
			Description:     "管理端工作模式（界面投影层）",
			DescriptionLong: "控制管理端界面风格：simple（简易模式，首页是接入清单，专家面收在「高级」组）｜expert（专家模式，完整导航与全部页面，当前默认）。只决定前端界面投影，不影响后端子系统装配、路由逻辑、数据结构。升级默认保持 expert，不改变既有用户界面。通过系统配置修改后约 5 秒内热生效。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
	}
}
