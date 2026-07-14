package settings

// CategoryProbe groups all model-probe-related settings together.
const CategoryProbe Category = "probe"

// ProbeSpecs returns all platform-scoped probe / lifecycle management specs.
//
// These settings control model_probe_runs hot table + monthly partition
// retention. The hot table holds the most recent N hours of probe records
// (fast INSERT, fast DELETE); rows older than that get promoted into
// monthly columnar partitions by bg.PartitionManager.
func ProbeSpecs() []*Spec {
	return []*Spec{
		// ── Hot table retention ────────────────────────────────────────
		{
			Key:             "probe.hot_retention_hours",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(1),
			Max:             floatPtr(720), // 30 days
			Default:         24,
			Description:     "热表保留小时数",
			DescriptionLong: "model_probe_runs_hot 表中保留的最近探测记录小时数。超过此时间的记录会被 bg.PartitionManager 自动 promote 到月度 columnar 分区。设为 1 表示只保留当天数据。修改后下次 promote 周期（约 1 小时）生效。",
			Unit:            "小时",
			DangerLevel:     Warning,
			HotReload:       true,
		},

		// ── Monthly partition retention ────────────────────────────────
		{
			Key:             "probe.partition_retention_days",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(7),
			Max:             floatPtr(3650), // 10 years
			Default:         90,
			Description:     "分区保留天数",
			DescriptionLong: "model_probe_runs 历史分区的保留天数。超过此天数的月度分区会被 drop_old_model_probe_runs_partitions() 自动删除（释放磁盘空间）。DROP PARTITION 是 O(1) 操作，对在线查询无影响。",
			Unit:            "天",
			DangerLevel:     Warning,
			HotReload:       true,
		},

		// ── Auto-promote cycle ─────────────────────────────────────────
		{
			Key:             "probe.promote_batch_size",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(100),
			Max:             floatPtr(50000),
			Default:         5000,
			Description:     "Promote 批大小",
			DescriptionLong: "每次 promote_model_probe_runs_hot_to_partition 调用迁移的最大行数。控制单次事务的内存占用和锁等待时间。增大可提升吞吐，减小可降低长事务风险。",
			Unit:            "行",
			DangerLevel:     Safe,
			HotReload:       true,
		},

		// ── Auto-cleanup cycle ─────────────────────────────────────────
		{
			Key:             "probe.partition_cleanup_enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Default:         true,
			Description:     "启用自动分区清理",
			DescriptionLong: "控制 bg.PartitionManager 是否自动 drop 过期分区。关闭后需要手动调用 drop_old_model_probe_runs_partitions()。建议保持开启。",
			Unit:            "",
			DangerLevel:     Dangerous,
			HotReload:       true,
		},

		// ── Backoff parameters ───────────────────────────────────────────
		{
			Key:             "probe.backoff_base_seconds",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(30),
			Max:             floatPtr(3600),
			Default:         300,
			Description:     "探测退避基础秒数",
			DescriptionLong: "指数退避的基准间隔（秒）。第 1 次重试 = base，第 2 次 = base × multiplier，以此类推。",
			Unit:            "秒",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "probe.backoff_max_seconds",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(300),
			Max:             floatPtr(86400),
			Default:         7200,
			Description:     "探测退避最大秒数",
			DescriptionLong: "指数退避的上限（秒），重试间隔不会超过此值。",
			Unit:            "秒",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "probe.backoff_multiplier",
			Type:            TypeFloat,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(1.0),
			Max:             floatPtr(10.0),
			Default:         2.0,
			Description:     "探测退避倍数",
			DescriptionLong: "指数退避的乘法因子。默认为 2（每次失败翻倍）。",
			Unit:            "",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "probe.backoff_jitter_seconds",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(0),
			Max:             floatPtr(300),
			Default:         30,
			Description:     "探测退避抖动秒数",
			DescriptionLong: "在退避间隔上附加的随机抖动（秒），防止惊群效应。设 0 关闭抖动。",
			Unit:            "秒",
			DangerLevel:     Safe,
			HotReload:       true,
		},

		// ── Watchdog intervals ───────────────────────────────────────────
		{
			Key:             "probe.broken_watchdog_hours",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryProbe,
			Min:             floatPtr(1),
			Max:             floatPtr(720),
			Default:         168,
			Description:     "Broken 状态看门狗小时数",
			DescriptionLong: "模型进入 broken_confirmed 状态后，等待多少小时才再次探测。默认 168（7 天）。",
			Unit:            "小时",
			DangerLevel:     Safe,
			HotReload:       true,
		},
	}
}
