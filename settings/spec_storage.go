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
			// 存储优化方案 v2 S3 波1 读端灰度（plan §4-S3，纯代码无 DDL）。
			// 开启后 admin 日志列表/详情（/api/logs、/api/logs/{id}）的 FROM
			// 直读 session_turns 家族原生投影（复用 710 视图 session 分支的
			// 113 列契约），不再经过视图 v1 分支与反连接；API 响应形状不变。
			// 默认关闭走视图。镜像链启用前的 request_logs 历史行在原生模式
			// 下不可见，停写 + TTL 退役后边界消解（样板注释已登记）。
			Key:             "storage.admin_logs_native_turns_read",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryStorage,
			Default:         false,
			Description:     "admin 日志读端原生 session_turns（S3 波1）",
			DescriptionLong: "开启后 /api/logs 列表与详情直接读 session_turns(_hot) 的 710 会话分支投影（113 列与视图逐列同形），跳过视图的 v1 冻结分支 UNION ALL 与反连接开销。响应契约不变，可随时关闭回切视图。注意：镜像链启用前的 request_logs 历史窗口在原生模式下不可见。",
			DangerLevel:     Warning,
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
		// 2026-09-24: 全模式热区层方案（docs/storage/2026-09-24-hotzone-dual-mode-plan.md）。
		// 三个平台级开关控制 hotzone（data/hotzone/）装配与生命周期；热重载为 H4
		// 验收项（运行时改 storage.hotzone_max_size_gb=1 后 ≤1 轮询周期内 trimmer
		// 生效），settings > env > YAML > default 与现有 settings 语义对齐。
		{
			Key:             "storage.hotzone_enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryStorage,
			Default:         true,
			Description:     "启用全模式热区层（data/hotzone/）",
			DescriptionLong: "开启后两模式（lite / full）的本地热区层装载 SessionStateV2 + 会话 body + 请求 body 三件套镜像；关闭即回退历史装配（lite 仍保留 L1.5；full 仅 L1→L2→L3）。默认开启；可通过 LLM_GATEWAY_HOTZONE_ENABLED=false 一键关闭。",
			DangerLevel:     Dangerous,
			HotReload:       true,
		},
		{
			Key:             "storage.hotzone_max_size_gb",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryStorage,
			Min:             floatPtr(1),
			Max:             floatPtr(100),
			Default:         1,
			Description:     "热区磁盘硬上限",
			DescriptionLong: "data/hotzone/ 目录总体积上限（cache + session_bodies + requests 三子树共享同一预算），超过上限时按 mtime 从旧到新淘汰。默认 1GB。扩容立即生效（FileCache.ResizeMax）；缩容交由下一轮 trimmer 执行。",
			Unit:            "GB",
			DangerLevel:     Dangerous,
			HotReload:       true,
		},
		{
			Key:             "storage.hotzone_retention_hours",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryStorage,
			Min:             floatPtr(1),
			Max:             floatPtr(168),
			Default:         7,
			Description:     "热区保留时长",
			DescriptionLong: "data/hotzone/ 三子树（cache / session_bodies / requests）超过此时长即视为过期，由 HotZoneTrimmer 按 mtime 删除。默认 7h；范围 1–168h（=7 天）。",
			Unit:            "小时",
			DangerLevel:     Dangerous,
			HotReload:       true,
		},
	}
}
