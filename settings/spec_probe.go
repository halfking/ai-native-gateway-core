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
	}
}
