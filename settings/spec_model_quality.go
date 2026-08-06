package settings

// CategoryModelQuality groups all model-quality-related settings.
const CategoryModelQuality Category = "model_quality"

// ModelQualitySpecs returns all platform-scoped model quality monitoring specs.
//
// The model quality worker periodically runs MMLU benchmark tests against
// featured models to detect quality degradation (model "water leakage").
func ModelQualitySpecs() []*Spec {
	return []*Spec{
		{
			Key:         "model_quality.enabled",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     false, // 默认关闭，避免影响现有系统
			Description: "是否启用模型质量监控",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "model_quality.interval_hours",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     24,
			Min:         floatPtr(1),
			Max:         floatPtr(168), // 最多一周
			Description: "质量检测周期（小时）",
			Unit:        "小时",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "model_quality.use_lite_benchmark",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     true, // 默认使用快速测试（50题）
			Description: "是否使用精简测试（50题快速测试 vs 完整测试）",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "model_quality.alert_threshold",
			Type:        TypeFloat,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     5.0,
			Min:         floatPtr(1.0),
			Max:         floatPtr(20.0),
			Description: "质量下降告警阈值（准确率下降百分比）",
			Unit:        "%",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "model_quality.data_dir",
			Type:        TypeString,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     "./data",
			Description: "质量数据存储目录",
			DangerLevel: Safe,
			HotReload:   false, // 数据目录不支持热更新
		},
		{
			Key:         "model_quality.api_key",
			Type:        TypeString,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     "",
			Description: "质量测试专用API Key（为空则使用系统API Key）",
			DescriptionLong: "用于调用网关进行质量测试的API Key。" +
				"留空则使用系统默认的selfCheckAPIKey。" +
				"建议为质量测试创建专用的API Key以便单独追踪token消耗。",
			DangerLevel: Warning, // API Key属于敏感配置
			HotReload:   true,
		},
		{
			Key:         "model_quality.base_url",
			Type:        TypeString,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     "http://localhost:8787",
			Description: "网关基础URL（用于质量测试调用）",
			DescriptionLong: "模型质量测试时调用的网关地址。" +
				"本地开发: http://localhost:8787\n" +
				"测试环境245: http://10.177.48.245:8787\n" +
				"生产环境154: http://10.177.48.154:8787",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "model_quality.test_timeout_seconds",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryModelQuality,
			Default:     30,
			Min:         floatPtr(10),
			Max:         floatPtr(120),
			Description: "单个测试请求超时时间（秒）",
			Unit:        "秒",
			DangerLevel: Safe,
			HotReload:   true,
		},
	}
}
