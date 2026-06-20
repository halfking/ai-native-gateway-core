package settings

// RateLimitPlatformSpecs — 平台级 rate limit 默认值（Q3 + Q4:C 迁移）
// 替代 app_settings.rate_limit_rpm / _concurrent / _tpm
func RateLimitPlatformSpecs() []*Spec {
	return []*Spec{
		{
			Key:             "default.rate_limit_rpm",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryRateLimit,
			Min:             floatPtr(0),
			Max:             floatPtr(1000000),
			Default:         60,
			Description:     "默认 RPM 限流",
			DescriptionLong: "当 api_key 未设置自己的 rate_limit_rpm 时使用此默认值。",
			Unit:            "req/min",
			DangerLevel:     Dangerous,
			HotReload:       true,
			Observability:   "/api/usage",
		},
		{
			Key:             "default.rate_limit_concurrent",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryRateLimit,
			Min:             floatPtr(1),
			Max:             floatPtr(10000),
			Default:         20,
			Description:     "默认并发限流",
			DescriptionLong: "当 api_key 未设置自己的 rate_limit_concurrent 时使用此默认值。",
			Unit:            "并发",
			DangerLevel:     Dangerous,
			HotReload:       true,
			Observability:   "/api/usage",
		},
		{
			Key:             "default.rate_limit_tpm",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryRateLimit,
			Min:             floatPtr(0),
			Max:             floatPtr(100000000),
			Default:         0,
			Description:     "默认 TPM 限流",
			DescriptionLong: "当 api_key 未设置自己的 rate_limit_tpm 时使用此默认值。0 表示不限制。",
			Unit:            "tokens/min",
			DangerLevel:     Warning,
			HotReload:       true,
			Observability:   "/api/usage",
		},
	}
}

// RateLimitTenantSpecs — 租户级 rate limit（Q3）
// 真正的 per-tenant 设置；管理员可单独调整每个租户的限流。
// 实际生效时仍优先于 api_keys 表（per-key）的值。
func RateLimitTenantSpecs() []*Spec {
	return []*Spec{
		{
			Key:             "rate_limit_rpm",
			Type:            TypeInt,
			Scope:           ScopeTenant,
			Category:        CategoryRateLimit,
			Min:             floatPtr(0),
			Max:             floatPtr(1000000),
			Default:         0,
			Description:     "租户 RPM 限流",
			DescriptionLong: "覆盖平台级默认值的每租户 RPM 限制。0 = 不覆盖（使用平台默认或 api_key 自己的设置）。",
			Unit:            "req/min",
			DangerLevel:     Dangerous,
			HotReload:       true,
			Observability:   "/api/usage",
		},
		{
			Key:             "rate_limit_concurrent",
			Type:            TypeInt,
			Scope:           ScopeTenant,
			Category:        CategoryRateLimit,
			Min:             floatPtr(0),
			Max:             floatPtr(10000),
			Default:         0,
			Description:     "租户并发限流",
			DescriptionLong: "覆盖平台级默认值的每租户并发限制。0 = 不覆盖。",
			Unit:            "并发",
			DangerLevel:     Dangerous,
			HotReload:       true,
			Observability:   "/api/usage",
		},
	}
}
