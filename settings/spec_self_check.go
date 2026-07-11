package settings

// CategorySelfCheck groups all self-check-related settings.
const CategorySelfCheck Category = "self_check"

// SelfCheckSpecs returns all platform-scoped self-check specs.
//
// The self-check worker periodically runs ping + tool-call smoke tests
// against key models to verify gateway availability and routing correctness.
func SelfCheckSpecs() []*Spec {
	return []*Spec{
		{
			Key:         "self_check.enabled",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategorySelfCheck,
			Default:     true,
			Description: "是否启用系统自检",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "self_check.normal_interval_seconds",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategorySelfCheck,
			Default:     60,
			Min:         floatPtr(10),
			Max:         floatPtr(600),
			Description: "正常测试周期（秒）",
			Unit:        "秒",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "self_check.fault_interval_seconds",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategorySelfCheck,
			Default:     30,
			Min:         floatPtr(10),
			Max:         floatPtr(300),
			Description: "故障加密测试周期（秒）",
			Unit:        "秒",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "self_check.max_models",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategorySelfCheck,
			Default:     10,
			Min:         floatPtr(1),
			Max:         floatPtr(50),
			Description: "单轮最多测试模型数",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "self_check.max_tokens_per_run",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategorySelfCheck,
			Default:     100000,
			Min:         floatPtr(1000),
			Max:         floatPtr(1000000),
			Description: "每次会话最大 token 数",
			DangerLevel: Safe,
			HotReload:   true,
		},
	}
}
