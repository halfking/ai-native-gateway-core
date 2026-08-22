package settings

// ProviderProfileSpecs returns all provider profile system specs (Phase 1, 2026-07-26).
func ProviderProfileSpecs() []*Spec {
	return []*Spec{
		{
			Key:         "provider_profile.enabled",
			Type:        TypeBool,
			EnvName:     "LLM_GATEWAY_PROVIDER_PROFILE_ENABLED",
			Default:     false,
			Category:    CategoryGeneral,
			HotReload:   true,
			Description: "启用供应商画像系统（7维度供应商质量评分）",
		},
		{
			Key:         "provider_profile.collection_interval",
			Type:        TypeInt,
			EnvName:     "LLM_GATEWAY_PROVIDER_PROFILE_COLLECTION_INTERVAL",
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
			Default:     604800,
			Min:         float64Ptr(86400),
			Max:         float64Ptr(2592000),
			Category:    CategoryGeneral,
			HotReload:   true,
			Description: "历史数据清理间隔（秒），默认7天",
			Unit:        "秒",
		},
	}
}

// float64Ptr is declared in spec_modules.go
