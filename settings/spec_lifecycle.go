package settings

const CategoryLifecycle Category = "lifecycle"

func LifecycleSpecs() []*Spec {
	return []*Spec{
		{Key: "lifecycle.hot_retention_hours", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(720), Default: 24, DangerLevel: Warning, HotReload: true, Description: "热表保留小时数", DescriptionLong: "所有 *_hot 表的默认保留窗口（除 model_probe_runs_hot）。超过此时长的行会在下次 promote 周期被自动迁移到对应的月度分区表。默认 24 小时（1 天）。", Unit: "小时"},
		{Key: "lifecycle.promote_interval_hours", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(168), Default: 1, DangerLevel: Safe, HotReload: true, Description: "Promote 轮询间隔", DescriptionLong: "bg.PartitionManager 每 N 小时执行一次 promote（hot → 月度分区）。默认 1 小时。", Unit: "小时"},
		{Key: "lifecycle.promote_batch_size", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(100), Max: floatPtr(50000), Default: 5000, DangerLevel: Safe, HotReload: true, Description: "Promote 批大小", DescriptionLong: "每次 promote_*_hot_to_partition 调用迁移的最大行数。", Unit: "行"},
	}
}
