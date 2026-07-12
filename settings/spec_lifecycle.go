package settings

const CategoryLifecycle Category = "lifecycle"

func LifecycleSpecs() []*Spec {
	return []*Spec{
		{
			Key:         "lifecycle.hot_retention_hours",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryLifecycle,
			Default:     24,
			Min:         floatPtr(0),
			Max:         floatPtr(720),
			Description: "Hot 表数据保留时长（小时）：超过此时间的数据将被迁移到月分区。0 = 全部迁移。",
			Unit:        "小时",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "lifecycle.promote_interval_hours",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryLifecycle,
			Default:     1,
			Min:         floatPtr(1),
			Max:         floatPtr(24),
			Description: "Hot 表迁移检查间隔（小时）：后台 PartitionManager 每隔 N 小时自动检查并迁移符合条件的行。",
			Unit:        "小时",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "lifecycle.promote_batch_size",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryLifecycle,
			Default:     5000,
			Min:         floatPtr(100),
			Max:         floatPtr(50000),
			Description: "Hot 表迁移单批处理行数：每批最多迁移 N 行。调大提高吞吐，调小降低锁粒度。",
			Unit:        "行",
			DangerLevel: Safe,
			HotReload:   true,
		},
	}
}
