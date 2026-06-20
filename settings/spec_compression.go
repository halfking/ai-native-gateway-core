package settings

// CompressionSpecs — 5 平台级 + 0 租户级 — 压缩与会话相关
// 对应 LLM_GATEWAY_COMPRESSION_MODE / _WINDOW_FRACTION /
// LLM_GATEWAY_SESSION_TTL_HOURS / LLM_GATEWAY_ENABLE_DISGUISE /
// LLM_GATEWAY_STREAM_RETRY_THRESHOLD
func CompressionSpecs() []*Spec {
	minFraction := 0.0
	maxFraction := 1.0
	return []*Spec{
		{
			Key:             "compression.mode",
			Type:            TypeEnum,
			Scope:           ScopePlatform,
			Category:        CategoryCompression,
			Options:         []string{"off", "auto_threshold", "on_4xx"},
			Default:         "on_4xx",
			Description:     "压缩模式",
			DescriptionLong: "off=从不压缩；auto_threshold=请求前按阈值压缩；on_4xx=仅在 upstream 返回 context_length_exceeded 后压缩（推荐）",
			Unit:            "",
			DangerLevel:     Warning,
			HotReload:       true,
			Observability:   "/api/admin/compression/stats",
		},
		{
			Key:             "compression.window_fraction",
			Type:            TypeFloat,
			Scope:           ScopePlatform,
			Category:        CategoryCompression,
			Min:             &minFraction,
			Max:             &maxFraction,
			Default:         0.8,
			Description:     "压缩窗口阈值比例",
			DescriptionLong: "触发压缩的 token 比例阈值（占模型上下文窗口的比例）。建议 0.7-0.85。",
			Unit:            "比例",
			DangerLevel:     Warning,
			HotReload:       true,
			Observability:   "/api/admin/compression/stats",
		},
		{
			Key:             "session.ttl_hours",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Min:             floatPtr(1),
			Max:             floatPtr(8760),
			Default:         168,
			Description:     "会话 TTL",
			DescriptionLong: "会话在 Redis 中保留的小时数。影响 sessions.Manager 的 TTL。",
			Unit:            "小时",
			DangerLevel:     Dangerous,
			HotReload:       true,
			Observability:   "/api/admin/session-context",
		},
		{
			Key:             "enable_disguise",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategorySecurity,
			Default:         false,
			Description:     "启用 UA/TLS 伪装",
			DescriptionLong: "启用 User-Agent 和 TLS 指纹轮换（参考 docs/legal/disguise-compliance.md）。",
			Unit:            "",
			DangerLevel:     Breaking,
			HotReload:       true,
			Observability:   "/healthz?full=true",
		},
		{
			Key:             "stream_retry_threshold",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryTimeout,
			Min:             floatPtr(0),
			Max:             floatPtr(100),
			Default:         5,
			Description:     "流式重试阈值",
			DescriptionLong: "上游流式 chunk 数低于此值时触发 failover 到下一个 credential。",
			Unit:            "chunks",
			DangerLevel:     Warning,
			HotReload:       true,
			Observability:   "/api/routing/decisions",
		},
	}
}

func floatPtr(f float64) *float64 { return &f }
