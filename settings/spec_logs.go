package settings

// CategoryLogs groups all log-related settings together.
const CategoryLogs Category = "logs"

// LogSpecs returns all platform-scoped log management specs.
// These settings control the gateway's file-based log rotation
// and request log retention policies.
func LogSpecs() []*Spec {
	return []*Spec{
		// ── File rotation (internal/logging) ─────────────────────────
		{
			Key:             "log.max_size_mb",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryLogs,
			Min:             floatPtr(1),
			Max:             floatPtr(1000),
			Default:         100,
			Description:     "单个日志文件大小上限",
			DescriptionLong: "日志文件达到此大小（MB）后自动轮转。默认 100MB，轮转后生成 .gz 压缩备份。需重启生效。",
			Unit:            "MB",
			DangerLevel:     Warning,
			HotReload:       false,
		},
		{
			Key:             "log.max_backups",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryLogs,
			Min:             floatPtr(0),
			Max:             floatPtr(100),
			Default:         10,
			Description:     "日志备份保留数量",
			DescriptionLong: "保留的历史日志文件数量上限。设为 0 表示不限制数量（仅受 max_age_days 约束）。默认 10 个，与 100MB 配合约 1GB 上限。",
			Unit:            "个",
			DangerLevel:     Safe,
			HotReload:       false,
		},
		{
			Key:             "log.max_age_days",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryLogs,
			Min:             floatPtr(0),
			Max:             floatPtr(365),
			Default:         7,
			Description:     "日志备份最大保留天数",
			DescriptionLong: "超过此天数的日志备份将被自动删除。设为 0 表示不按时间清理（仅受 max_backups 约束）。默认 7 天滚动保留。",
			Unit:            "天",
			DangerLevel:     Safe,
			HotReload:       false,
		},
		{
			Key:             "log.compress",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryLogs,
			Default:         true,
			Description:     "日志轮转时压缩",
			DescriptionLong: "轮转时是否对旧日志文件进行 gzip 压缩。压缩后文件扩展名为 .log.gz，可节省约 70% 存储空间。默认开启。",
			Unit:            "",
			DangerLevel:     Safe,
			HotReload:       false,
		},

		// ── Request log retention (scripts/cleanup-request-logs.sh) ──
		{
			Key:             "log.request_retention_days",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryLogs,
			Min:             floatPtr(7),
			Max:             floatPtr(3650),
			Default:         90,
			Description:     "请求日志保留天数",
			DescriptionLong: "request_logs 表中的请求记录超过此天数后将被归档并删除。影响审计日志和请求分析的可用历史深度。默认 90 天。",
			Unit:            "天",
			DangerLevel:     Warning,
			HotReload:       false,
		},
		{
			Key:             "log.archive_days",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryLogs,
			Default:         "30-90",
			Description:     "冷数据归档时间范围",
			DescriptionLong: "指定需要归档的请求日志天数范围（格式：起始天数-结束天数）。范围内的日志将被导出到归档存储后从数据库删除。默认 30-90 天。",
			Unit:            "天",
			DangerLevel:     Warning,
			HotReload:       false,
		},
		{
			Key:             "log.trim_days",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryLogs,
			Default:         "7-30",
			Description:     "温数据裁剪时间范围",
			DescriptionLong: "指定需要裁剪大字段（request_body/response_body）的请求日志天数范围（格式：起始天数-结束天数）。裁剪后保留关键元数据，节省存储。",
			Unit:            "天",
			DangerLevel:     Safe,
			HotReload:       false,
		},
		{
			Key:             "log.cleanup_enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryLogs,
			Default:         true,
			Description:     "启用自动清理",
			DescriptionLong: "是否启用请求日志的自动清理任务（cron 每天凌晨 2:00 执行）。关闭后日志将无限积累，磁盘空间可能耗尽。",
			Unit:            "",
			DangerLevel:     Warning,
			HotReload:       false,
		},
	}
}
