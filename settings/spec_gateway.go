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
	}
}
