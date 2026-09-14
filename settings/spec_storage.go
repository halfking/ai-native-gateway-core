package settings

// CategoryStorage groups all attachment/storage management settings.
// 2026-07-02: 本地文件存储（附件）的运维配置。
const CategoryStorage Category = "storage"

// StorageSpecs returns all platform-scoped storage/attachment management specs.
// 这些设置控制附件文件系统的保留策略、配额水位、自动清理行为。
// 持久化到 settings_kv（category='storage'），通过数据生命周期页面管理。
func StorageSpecs() []*Spec {
	return []*Spec{
		{
			Key:             "storage.attachment_dir_override",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryStorage,
			Default:         "",
			Description:     "附件存储目录覆盖",
			DescriptionLong: "覆盖默认附件存储目录（环境变量 LLM_GATEWAY_ATTACHMENT_DIR）。留空则使用环境变量或默认 ./data/attachments。修改后需要重启服务进程生效（不会自动迁移已有文件）。",
			DangerLevel:     Dangerous,
			HotReload:       false,
		},
		{
			Key:             "storage.attachment_ttl_days",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryStorage,
			Min:             floatPtr(1),
			Max:             floatPtr(3650),
			Default:         30,
			Description:     "附件保留天数",
			DescriptionLong: "超过此天数的附件文件视为过期，可被清理或归档。默认 30 天。",
			Unit:            "天",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             "storage.attachment_max_size_mb",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryStorage,
			Min:             floatPtr(1),
			Max:             floatPtr(200),
			Default:         20,
			Description:     "单个附件大小上限",
			DescriptionLong: "单个附件文件（解码后）的最大字节数。超过此大小的附件会被拒绝存储（但请求仍正常转发）。默认 20MB。",
			Unit:            "MB",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "storage.disk_quota_percent",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryStorage,
			Min:             floatPtr(50),
			Max:             floatPtr(99),
			Default:         80,
			Description:     "磁盘告警水位",
			DescriptionLong: "磁盘使用率达到此百分比时触发告警。默认 80%。",
			Unit:            "%",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "storage.auto_cleanup_enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryStorage,
			Default:         false,
			Description:     "启用自动清理",
			DescriptionLong: "启用后，后台 worker 会定期检查磁盘水位，超过阈值时自动按 LRU 清理最老的附件和归档日志。默认关闭，避免误删。",
			DangerLevel:     Dangerous,
			HotReload:       true,
		},
		{
			Key:             "storage.auto_cleanup_threshold",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryStorage,
			Min:             floatPtr(60),
			Max:             floatPtr(99),
			Default:         85,
			Description:     "自动清理触发水位",
			DescriptionLong: "磁盘使用率达到此百分比时触发自动清理（需启用 auto_cleanup_enabled）。默认 85%。",
			Unit:            "%",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			// 存储优化方案 v2 S1b 灰度开关（migration 707）。
			Key:             "storage.session_turns_bodies_enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryStorage,
			Default:         false,
			Description:     "会话轮次正文写入 session_turns",
			DescriptionLong: "开启后 SessionWriterV2 把每轮 request_delta/response_delta 同步写入 session_turns 正文列（宽表，turn 级唯一事实源的第一步）。关闭时正文仍只落 session_bodies 逐轮行。默认关闭；回切即关闭开关（列保留不删，方案 §7）。",
			DangerLevel:     Dangerous,
			HotReload:       true,
		},
		{
			// 存储优化方案 v2 S1b 灰度开关（migration 708）。
			Key:             "storage.session_final_full_enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryStorage,
			Default:         false,
			Description:     "会话最后完整快照（final_full）+ 停写 outbound_body",
			DescriptionLong: "开启后 SessionWriterV2 每轮把完整 outbound upsert 进 session_bodies(kind='final_full', turn_no=0) 一行，并停止逐轮写 outbound_body（差集读端优先读 final_full、未命中回退旧行）。关闭时完全回到旧行为。默认关闭；方案 §6 估算停写 outbound_body 月省约 1.8GB。",
			DangerLevel:     Dangerous,
			HotReload:       true,
		},
		{
			// 存储优化方案 v2 S2（migration 710 轮）：request_logs 停写 gate。
			// S2 仅登记（默认 true = 继续双写）；S4 落 telemetry client 分支化
			// 后由本开关一键停写/回切（plan §4 S2/S4 行、§7）。
			Key:             "storage.request_logs_write_enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryStorage,
			Default:         true,
			Description:     "request_logs 主账本写入（S4 停写 gate）",
			DescriptionLong: "存储优化方案 v2：开启时 telemetry/admin ingest 维持 request_logs(_hot) 与 bodies 双写（现状）；S4 落地写分支后关闭即停写，session 六表族成为唯一事实源。关闭前提：dual_read_validator 对账 7 天零漂移（plan §4 S2 退出条件）。回切即重新开启，热表结构不动、无数据丢失窗口。",
			DangerLevel:     Dangerous,
			HotReload:       true,
		},
		{
			Key:             "log.delete_days",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryLogs,
			Min:             floatPtr(7),
			Max:             floatPtr(3650),
			Default:         30,
			Description:     "日志删除天数",
			DescriptionLong: "超过此天数的归档日志自动删除。默认 30 天。删除不可恢复。",
			Unit:            "天",
			DangerLevel:     Warning,
			HotReload:       true,
		},
	}
}
