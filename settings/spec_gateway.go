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
			Max:             floatPtr(10485760),
			Default:         1048576,
			Description:     "入口 prompt 预算（估算 tokens）",
			DescriptionLong: "三个协议入口在 JSON 解析前按估算 tokens 拒绝超预算 prompt，返回 413 prompt_too_large。默认 1048576（1M tokens）；0 表示关闭。通过系统配置修改后约 5 秒内热生效。",
			Unit:            "tokens",
			DangerLevel:     Warning,
			HotReload:       true,
		},
	}
}
