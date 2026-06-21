package settings

// PassthroughSpecs returns platform-level settings for passthrough mode configuration.
// These settings control compression, caching, and format conversion behavior.
// Provider-level overrides in provider_settings table take precedence.
func PassthroughSpecs() []*Spec {
	return []*Spec{
		{
			Key:             "cache.enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         true,
			Description:     "会话缓存",
			DescriptionLong: "启用3层会话缓存(L1内存+L2 Redis+L3数据库)用于智能压缩。Provider级可覆盖此全局默认值。",
			DangerLevel:     Warning,
			HotReload:       true,
			Observability:   "/api/admin/compression/stats",
		},
		{
			Key:             "format_conversion.enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryGeneral,
			Default:         true,
			Description:     "格式转换",
			DescriptionLong: "启用Anthropic↔OpenAI协议自动转换(Q2/Q3路径)。Provider级可覆盖此全局默认值。",
			DangerLevel:     Safe,
			HotReload:       true,
			Observability:   "/healthz?full=true",
		},
	}
}
