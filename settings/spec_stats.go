package settings

// StatsShadowSpecs controls the read-only statistics shadow comparison used
// during migration from legacy usage/dashboard projections.
func StatsShadowSpecs() []*Spec {
	return []*Spec{
		{
			Key:             "stats.shadow_read.enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryGeneral,
			Default:         false,
			Description:     "启用统计影子读取",
			DescriptionLong: "异步读取 canonical stats projection 并与旧统计结果比较；不改变主请求响应。",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             "stats.shadow_read.sample_percent",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryGeneral,
			Default:         0,
			Description:     "统计影子读取采样比例",
			DescriptionLong: "影子比较采样比例（0-100）；采样任务在后台有界执行。",
			Unit:            "%",
			DangerLevel:     Safe,
			HotReload:       true,
			Min:             floatPtr(0),
			Max:             floatPtr(100),
		},
		{
			Key:             "stats.shadow_read.timeout_ms",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryGeneral,
			Default:         750,
			Description:     "统计影子读取超时",
			DescriptionLong: "影子查询的最大后台执行时间；超时只记录比较失败，不影响主请求。",
			Unit:            "毫秒",
			DangerLevel:     Safe,
			HotReload:       true,
			Min:             floatPtr(100),
			Max:             floatPtr(5000),
		},
	}
}
