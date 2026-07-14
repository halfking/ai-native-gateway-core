package settings

const CategoryLifecycle Category = "lifecycle"

func LifecycleSpecs() []*Spec {
	return []*Spec{
		{Key: "lifecycle.hot_retention_hours", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(720), Default: 24, DangerLevel: Warning, HotReload: true, Description: "热表保留小时数", DescriptionLong: "所有 *_hot 表的默认保留窗口（除 model_probe_runs_hot）。超过此时长的行会在下次 promote 周期被自动迁移到对应的月度分区表。默认 24 小时（1 天）。", Unit: "小时"},
		{Key: "lifecycle.promote_interval_hours", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(168), Default: 1, DangerLevel: Safe, HotReload: true, Description: "Promote 轮询间隔", DescriptionLong: "bg.PartitionManager 每 N 小时执行一次 promote（hot → 月度分区）。默认 1 小时。", Unit: "小时"},
		{Key: "lifecycle.promote_batch_size", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(100), Max: floatPtr(50000), Default: 5000, DangerLevel: Safe, HotReload: true, Description: "Promote 批大小", DescriptionLong: "每次 promote_*_hot_to_partition 调用迁移的最大行数。", Unit: "行"},

		// 2026-07-13: 状态表精简 - per-table retention 设置。
		// 设计原则：状态/路由类表默认 30 天 DROP PARTITION；
		// 请求记录类表保持 hot 1d（依赖月度分区长期保留）。
		// 所有这些设置通过 DROP PARTITION 或 TimescaleDB retention policy 实施。
		{Key: "lifecycle.routing_decision_log_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(365), Default: 30, DangerLevel: Warning, HotReload: true, Description: "routing_decision_log 保留天数", DescriptionLong: "routing_decision_log 月度分区保留天数。超过此时长的分区会被 archive_routing_decision_log 自动 DROP。默认 30 天。", Unit: "天"},
		{Key: "lifecycle.candidate_failure_logs_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(365), Default: 30, DangerLevel: Warning, HotReload: true, Description: "candidate_failure_logs 保留天数", DescriptionLong: "candidate_failure_logs 月度分区保留天数。默认 30 天。", Unit: "天"},
		{Key: "lifecycle.handoff_logs_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(365), Default: 30, DangerLevel: Warning, HotReload: true, Description: "handoff_logs 保留天数", DescriptionLong: "handoff_logs 月度分区保留天数。默认 30 天。", Unit: "天"},
		{Key: "lifecycle.credential_model_call_history_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(365), Default: 30, DangerLevel: Warning, HotReload: true, Description: "credential_model_call_history 保留天数", DescriptionLong: "credential_model_call_history TimescaleDB chunk 保留天数。通过 TimescaleDB retention policy 实施。默认 30 天（原 7 天）。", Unit: "天"},
		{Key: "lifecycle.model_probe_runs_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(365), Default: 14, DangerLevel: Warning, HotReload: true, Description: "model_probe_runs hot 表保留天数", DescriptionLong: "model_probe_runs_hot 的 DELETE 保留天数（纯 hot 表策略，2026-07-14 起不再 promote 到 columnar 分区）。超过此时长的行会被 cleanupOldModelProbeRuns() 直接 DELETE。默认 14 天。", Unit: "天"},
		{Key: "lifecycle.credential_probe_model_log_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(3650), Default: 90, DangerLevel: Warning, HotReload: true, Description: "credential_probe_model_log 保留天数", DescriptionLong: "credential_probe_model_log 保留天数（列存储堆表，需通过 batch cleanup 任务）。默认 90 天。", Unit: "天"},

		// 2026-07-14: request_logs_bodies 月度分区保留天数。
		// 请求/响应 body 仅用于调试/导出，常规运营很少看 >1 天的 body。
		// 超过此天数的月度分区会被 drop_old_request_logs_bodies_partitions() 自动 DROP。
		{Key: "lifecycle.request_logs_bodies_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(365), Default: 7, DangerLevel: Warning, HotReload: true, Description: "request_logs_bodies 保留天数", DescriptionLong: "request_logs_bodies 月度分区保留天数。body 仅用于调试，超过此天数的分区会被自动 DROP。默认 7 天。", Unit: "天"},

		// 2026-07-13: 请求记录类表保留期 - 默认 1 天（hot 表）。
		// 业务方可通过 setting 调整。注意：月度分区仍由 partition_manager 自动创建，
		// 但不会自动 DROP（保留期由各业务方按需设置 archive.*_days）。
		{Key: "lifecycle.usage_ledger_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(30), Default: 1, DangerLevel: Warning, HotReload: true, Description: "usage_ledger 保留天数", DescriptionLong: "usage_ledger 月度分区保留天数。默认 1 天（仅 hot 表）。", Unit: "天"},
		{Key: "lifecycle.request_wal_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(30), Default: 1, DangerLevel: Warning, HotReload: true, Description: "request_wal 保留天数", DescriptionLong: "request_wal 月度分区保留天数。默认 1 天。", Unit: "天"},
		{Key: "lifecycle.credit_ledger_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(30), Default: 1, DangerLevel: Warning, HotReload: true, Description: "credit_ledger 保留天数", DescriptionLong: "credit_ledger 月度分区保留天数。默认 1 天。", Unit: "天"},
		{Key: "lifecycle.tool_usage_stats_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(30), Default: 1, DangerLevel: Warning, HotReload: true, Description: "tool_usage_stats 保留天数", DescriptionLong: "tool_usage_stats 月度分区保留天数。默认 1 天。", Unit: "天"},
	}
}
