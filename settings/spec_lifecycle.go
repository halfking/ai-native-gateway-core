package settings

const CategoryLifecycle Category = "lifecycle"

func LifecycleSpecs() []*Spec {
	return []*Spec{
		{Key: "lifecycle.hot_retention_hours", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(720), Default: 8, DangerLevel: Warning, HotReload: true, Description: "热表保留小时数", DescriptionLong: "所有 *_hot 表的默认保留窗口（除 model_probe_runs_hot）。超过此时长的行会在下次 promote 周期被自动迁移到对应的月度分区表。默认 8 小时。", Unit: "小时"},
		{Key: "lifecycle.promote_interval_hours", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(168), Default: 1, DangerLevel: Safe, HotReload: true, Description: "Promote 轮询间隔", DescriptionLong: "bg.PartitionManager 每 N 小时执行一次 promote（hot → 月度分区）。默认 1 小时。", Unit: "小时"},
		{Key: "lifecycle.promote_batch_size", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(100), Max: floatPtr(50000), Default: 5000, DangerLevel: Safe, HotReload: true, Description: "Promote 批大小", DescriptionLong: "每次 promote_*_hot_to_partition 调用迁移的最大行数。", Unit: "行"},
		{Key: "lifecycle.handoff_logs_hot_retention_hours", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(720), Default: 8, DangerLevel: Warning, HotReload: true, Description: "handoff_logs 热层保留小时数", DescriptionLong: "handoff_logs_hot 超过此窗口后由 PartitionManager 原子迁移到月度 columnar 分区。默认 8 小时。", Unit: "小时"},
		{Key: "lifecycle.session_module_executions_hot_retention_hours", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(720), Default: 8, DangerLevel: Warning, HotReload: true, Description: "session_module_executions 热层保留小时数", DescriptionLong: "session_module_executions_hot 超过此窗口后由 PartitionManager 原子迁移到月度分区（Migration 580）。默认 8 小时。", Unit: "小时"},
		{Key: "lifecycle.dashboard_access_events_hot_retention_hours", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(720), Default: 8, DangerLevel: Warning, HotReload: true, Description: "dashboard_access_events 热层保留小时数", DescriptionLong: "dashboard_access_events_hot 超过此窗口后由 PartitionManager 原子迁移到月度分区（Migration 579，函数体由 607 修复）。默认 8 小时。", Unit: "小时"},

		// 2026-07-13: 状态表精简 - per-table retention 设置。
		// 设计原则：状态/路由类表默认 30 天 DROP PARTITION；
		// 请求记录类表保持 hot 1d（依赖月度分区长期保留）。
		// 所有这些设置通过 DROP PARTITION 或 TimescaleDB retention policy 实施。
		{Key: "lifecycle.routing_decision_log_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(365), Default: 30, DangerLevel: Warning, HotReload: true, Description: "routing_decision_log 保留天数", DescriptionLong: "routing_decision_log 月度分区保留天数。超过此时长的分区会被 archive_routing_decision_log 自动 DROP。默认 30 天。", Unit: "天"},
		{Key: "lifecycle.candidate_failure_logs_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(365), Default: 7, DangerLevel: Warning, HotReload: true, Description: "candidate_failure_logs 保留天数", DescriptionLong: "candidate_failure_logs 月度分区的行级 DELETE 保留天数(bg/opslog_trimmer.go,689 起分区为 heap 可删)+ 按表 TTL 的分区 DROP(dropOldStatePartitions)。所有查询窗口仅 5 分钟,7 天已远超 RCA 需求;此前 spec 默认 30 与 Go/trimmer 默认 7 漂移,2026-09-09 审计 R3 统一为 7。默认 7 天。", Unit: "天"},
		{Key: "lifecycle.handoff_logs_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(365), Default: 30, DangerLevel: Warning, HotReload: true, Description: "handoff_logs 保留天数", DescriptionLong: "handoff_logs 月度分区保留天数。默认 30 天。", Unit: "天"},
		{Key: "lifecycle.supplier_errors_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(365), Default: 90, DangerLevel: Warning, HotReload: true, Description: "supplier_errors 保留天数", DescriptionLong: "supplier_errors 月度 columnar 分区保留天数（R30 审计 2026-09-16 补齐：此前历史分区不在任何 drop 清单，无限增长）。hot 侧 8h promote 不变；超过此时长的历史分区由 dropOldStatePartitions 逐分区 DROP（689 helper）。settings_kv 行在管理员首次显式设置时落库，此前按默认 90 天生效。", Unit: "天"},
		{Key: "lifecycle.credential_model_call_history_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(365), Default: 30, DangerLevel: Warning, HotReload: true, Description: "credential_model_call_history 保留天数", DescriptionLong: "credential_model_call_history TimescaleDB chunk 保留天数。通过 TimescaleDB retention policy 实施。默认 30 天（原 7 天）。", Unit: "天"},
		{Key: "lifecycle.model_probe_runs_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(365), Default: 14, DangerLevel: Warning, HotReload: true, Description: "model_probe_runs hot 表保留天数", DescriptionLong: "model_probe_runs_hot 的 DELETE 保留天数（纯 hot 表策略，2026-07-14 起不再 promote 到 columnar 分区）。超过此时长的行会被 cleanupOldModelProbeRuns() 直接 DELETE。默认 14 天。", Unit: "天"},
		{Key: "lifecycle.node_probe_runs_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(365), Default: 14, DangerLevel: Warning, HotReload: true, Description: "node_probe_runs 保留天数", DescriptionLong: "node_probe_runs（探测审计追加表）的 DELETE 保留天数。每次 node-probe attempt 写一行，无分区，靠此 cleanup 清理。超过此时长的行会被 cleanupOldNodeProbeRuns() 直接 DELETE。默认 14 天。注意：node_probe_state（状态机表）不受此设置影响。", Unit: "天"},
		{Key: "lifecycle.request_context_attrs_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(90), Default: 7, DangerLevel: Warning, HotReload: true, Description: "request_context_attrs 保留天数", DescriptionLong: "request_context_attrs 侧表（每业务请求一行）的 DELETE 保留天数。该表无分区，与 _request_logs 同速增长；超过此时长的行会被 cleanupOldRequestContextAttrs() 直接 DELETE。默认 7 天。", Unit: "天"},
		{Key: "lifecycle.credential_probe_model_log_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(3650), Default: 90, DangerLevel: Warning, HotReload: true, Description: "credential_probe_model_log 保留天数", DescriptionLong: "credential_probe_model_log 保留天数（列存储堆表，需通过 batch cleanup 任务）。默认 90 天。", Unit: "天"},
		{Key: "lifecycle.request_stats_minute_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(90), Default: 14, DangerLevel: Warning, HotReload: true, Description: "request_stats_minute 系列表保留天数", DescriptionLong: "request_stats_minute / _dim_minute / _error_drill_minute 三张分钟级 rollup 表的 DELETE 保留天数。每分钟 upsert，行数随 分钟×维度基数 增长。超过此时长的行会被 cleanupOldRequestStatsMinute() 直接 DELETE。默认 14 天（request_stats_rollup_cursor 单行状态表不受影响）。", Unit: "天"},
		{Key: "lifecycle.runtime_metrics_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(365), Default: 30, DangerLevel: Warning, HotReload: true, Description: "runtime_metrics 保留天数", DescriptionLong: "runtime_metrics（网关实例周期推送追加表）的 DELETE 保留天数。无分区，超过此时长的行会被 cleanupOldRuntimeMetrics() 直接 DELETE。默认 30 天。", Unit: "天"},
		{Key: "lifecycle.runtime_alert_events_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(365), Default: 30, DangerLevel: Warning, HotReload: true, Description: "runtime_alert_events 保留天数", DescriptionLong: "runtime_alert_events（告警事件追加表）的 DELETE 保留天数。状态有 triggered/resolved 但行不删，超过此时长的行会被 cleanupOldRuntimeAlertEvents() 直接 DELETE。默认 30 天。", Unit: "天"},

		// 2026-07-14: _request_logs_bodies 月度分区保留天数。
		// 请求/响应 body 仅用于调试/导出，常规运营很少看 >1 天的 body。
		// 超过此天数的月度分区会被 drop_old_request_logs_bodies_partitions() 自动 DROP。

		// 2026-09-27 (R67 session-storage 审计子任务 7, handoff §9): _request_logs
		// 主表 archive 流水线。331 把 archive_request_logs 整族移除后，主表的
		// 月分区一直只被保留、从不归档；本键驱动迁移 754 的
		// archive_request_logs_default(p_retention_days) 把超出窗口的月分区
		// 摘要字段落进 request_logs_archive_YYYY_MM（丢弃 18 个大 JSONB 列，
		// 归档体积约为主表 5% 级）。源分区不 DROP（R68 纪律：750 同款
		// move-then-attach，且 654/337 事故复盘禁止 DROP 父表月分区）。
		// 范围 7-365：下限对齐业务对账最低窗口，上限是一年（合规最长期）。
		// 消费点：bg.PartitionManager.runCleanup 周期调用；SQL 侧另有 [7,365]
		// 硬守卫，越界 RAISE EXCEPTION（失败即停，不静默）。
		// ★ 2026-10-24（v1 → session_turns 替换轮）：v1 族已随 890 物理删除 ⇒
		//   archive_request_logs_default 与 request_logs_archive_* 都不存在 ⇒
		//   本键的归档腿**不可达**（bg/partition_manager.go 的归档体被
		//   v1LifecycleRetired() 恒 true 挡住）。键与默认值保留（既有配置不丢），
		//   但描述必须说实话 —— 否则运维以为它在回收 v1 的空间。
		{Key: "lifecycle.request_logs_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(7), Max: floatPtr(365), Default: 30, DangerLevel: Warning, HotReload: true, Description: "请求日志归档 TTL（天）", DescriptionLong: "请求日志归档阈值（天）。★ 2026-10-24：v1 `_request_logs` 族已随迁移 890 物理删除，其归档函数与归档表一并消失 ⇒ **本键的归档腿当前不可达**（bg/partition_manager.go 的归档体被 v1LifecycleRetired() 恒 true 挡住）。键与默认值保留，避免既有配置失效；会话族（session_turns / session_bodies）的历史窗口由各自的生命周期键管理。默认 30 天（7-365）。", Unit: "天"},

		// 2026-09-27 (R67 session-storage 审计子任务 2, handoff §4;
		// 语义按 2026-09-27 批判式审计修正, R72 审计轮补分批):
		// session_turn_logs 保留时长。真正的 24h 硬编码在写入方
		// (turn_logs_writer.go 的 expires_at 烘焙);430 定义的清理函数
		// 全仓无调用方,该表在生产从未被清理过。本键是保留期的单一
		// 真相来源:改档只影响**新写入**行的 expires_at,存量行按写入
		// 时刻烘焙的到期时间清扫。
		//
		// 范围 1-168:下限 1h 防呆(误配 0 会瞬时清空整表,spec 风险约束
		// 明令"不要把 retention 改成 < 1h");上限 168h = 7 天,与
		// docs/storage/2026-09-20-session-storage-decoupling-plan.md
		// §5 S5 的 TTL 燃尽曲线对齐。默认 24 与旧写入行为一致,但清扫
		// 本身是首次接上(753),不是参数化既有行为。
		//
		// 消费点:写入侧 turn_logs_writer.go 烘焙 expires_at;清扫侧
		// bg.PartitionManager.cleanupSessionTurnLogsByTTL(跑在 24h 的
		// archiveOldPartitionsIfNeeded tick 上,非 1h runCleanup)分批
		// 调 cleanup_session_turn_logs_by_ttl(ttl, batch)(迁移 753)。
		// Go 侧 clamp 到 [1,168] 后传参,SQL 侧越界 RAISE EXCEPTION
		// (fail-closed 联锁,函数内无 GREATEST/NULL 兜底)。HotReload
		// 让运维调档无需重启网关。
		{Key: "lifecycle.session_turn_logs_ttl_hours", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(168), Default: 24, DangerLevel: Warning, HotReload: true, Description: "session_turn_logs 保留小时数", DescriptionLong: "session_turn_logs 保留时长(小时)。写入方(turn_logs_writer)按本值把 expires_at 烘焙进每一行;清扫函数 cleanup_session_turn_logs_by_ttl(迁移 753)按「到期即删」(expires_at < NOW())分批删除,不重复叠加本值。改档只影响新写入的行,存量行按写入时烘焙的到期时间清扫。默认 24 小时,范围 1-168(上限 7 天)。越界配置被 Go 侧 clamp 到 [1,168],SQL 侧对越界入参直接 RAISE(失败即停),不会静默钳制。", Unit: "小时"},

		// 2026-09-09 (审计 R3#4): session_summaries 归档行 TTL。
		// 471 起归档(domains/sessionarchive 把 30d 不活跃的行
		// SET archived_at = NOW())但归档行从不删除,表无界增长。
		// bg.SessionSummariesTrimmer 分批 DELETE「已归档且归档时间
		// 早于此刻」的行;索引自迁移 690
		// (idx_session_summaries_archived,partial on archived_at)。
		{Key: "lifecycle.session_summaries_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(3650), Default: 90, DangerLevel: Warning, HotReload: true, Description: "session_summaries 归档保留天数", DescriptionLong: "session_summaries 已归档行(archived_at IS NOT NULL)的 DELETE 保留天数。归档后保留 90 天供合规/审计回溯,超过后被 SessionSummariesTrimmer 分批删除(单批 ≤5000 行)。默认 90 天。活跃行(archived_at IS NULL)不受影响。", Unit: "天"},

		// 2026-07-13: 请求记录类表保留期 - 默认 1 天（hot 表）。
		// 业务方可通过 setting 调整。注意：月度分区仍由 partition_manager 自动创建，
		// 但不会自动 DROP（保留期由各业务方按需设置 archive.*_days）。
		{Key: "lifecycle.usage_ledger_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(30), Default: 1, DangerLevel: Warning, HotReload: true, Description: "usage_ledger 保留天数", DescriptionLong: "usage_ledger 月度分区保留天数。默认 1 天（仅 hot 表）。", Unit: "天"},
		{Key: "lifecycle.request_wal_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(30), Default: 1, DangerLevel: Warning, HotReload: true, Description: "request_wal 保留天数", DescriptionLong: "request_wal 月度分区保留天数。默认 1 天。", Unit: "天"},
		{Key: "lifecycle.credit_ledger_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(30), Default: 1, DangerLevel: Warning, HotReload: true, Description: "credit_ledger 保留天数", DescriptionLong: "credit_ledger 月度分区保留天数。默认 1 天。", Unit: "天"},
		{Key: "lifecycle.tool_usage_stats_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(30), Default: 1, DangerLevel: Warning, HotReload: true, Description: "tool_usage_stats 保留天数", DescriptionLong: "tool_usage_stats 月度分区保留天数。默认 1 天。", Unit: "天"},

		// 2026-10-21（§143.44）：usage_facts —— **两实例上唯一没有任何留存的族**。
		//
		// 证据（值无关判据）：211 与 252 的 settings_kv 里 11 个
		// `lifecycle.*_ttl_days` 键**完全一致**，其中**没有** usage_facts
		// ⇒ 该族逐日分区 + DEFAULT 分区**无界增长**（252 ≈1.5 MB/天）。
		//
		// ★ 保留期 = **365 天**（属主 2026-10-21 定）：它是**计费事实**族，
		//   保留期属于账期/审计语义（年审窗口），不是技术参数。
		//   回收方式 = 按日分区 DROP（bg/partition_manager.go 的
		//   dropOldDailyStatePartitions；既有月度 SQL helper 只认
		//   ^<parent>_\d{4}_\d{2}$，对日分区名一个都不匹配）。
		{Key: "lifecycle.usage_facts_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(3650), Default: 365, DangerLevel: Warning, HotReload: true, Description: "usage_facts 保留天数", DescriptionLong: "usage_facts 按日分区（usage_facts_YYYYMMDD）的保留天数。默认 365 天（计费事实族，对齐年度审计/账期窗口）。超过保留期的**整个日分区**被 DROP 回收空间（不是逐行 DELETE）。", Unit: "天"},

		// ====================================================================
		// 2026-10-21 R72f（属主决策项 #10 的前半）：补注册 10 个**只出现在读方**
		// 的键。★ 它们此前从未 Register ⇒ `Global.Spec(key)` 返回 nil ⇒
		// `GetPlatformInt` 直接回退 fallback ⇒ ⇒ **写 settings_kv 完全无效**。
		//
		// ★ 每个键的 Default 都与读方的 fallback **逐字对齐**
		//   （见各行注释里的读方位置）⇒ ⇒ ⇒ 「kv 无行时」注册前后行为一致。
		// ★ ★★ 2026-10-22 12h 审计轮（B1）：**本块原先登记的三个
		//   `_request_logs_bodies` 键已全部删除**：
		//     · lifecycle.request_logs_bodies_retention_hours
		//     · lifecycle.request_logs_bodies_promote_batch_size
		//     · lifecycle.request_logs_bodies_ttl_days（前两者之外，本轮顺带查出）
		//   理由（**代码级可证**）：
		//     · 读方在 bg/partition_manager.go 的 `case "_request_logs_bodies":`
		//       分支里，而那条 promoteSpec 于 8531596a6 被删 ⇒ 该分支**不可达**；
		//       `cleanupOldRequestLogsBodies` 同样已被删（读方 grep 为空）。
		//     · 迁移 890 真删了 `request_logs_bodies{,_hot}` / `_request_logs_bodies{,_hot}`
		//       ⇒ 被治理的表**根本不存在**。
		//   ⇒ ⇒ 这三个键注册后是**「写了没人读」**的旋钮：管理员在 UI 上改它们，
		//     后台**一行代码都不看** ⇒ 属于「看起来可控、实际不可控」的最坏形态。
		//   ⇒ ⇒ 附带订正：本块原先断言「注册后保留期从 24h 变 8h ⇒ 上线前必须知情」，
		//     而这句话**在 890 之后就不成立了**（值落进一个死分支，没有任何保留期变化）
		//     ⇒ 它曾作为「属主知情接受」的材料 ⇒ 一条**假的**行为变化说明被签过字。
		//   ⇒ ⇒ 真要保留这组旋钮，正确做法是**改指向会话族 bodies 链**
		//     （promote_session_bodies_hot_to_partition / session_bodies），而不是留着死键。
		//
		// ★★ R107 轮（12h 审计）补记：**B1 那次只删了 1 个（ttl_days）**，
		//   另 2 个（retention_hours / promote_batch_size）**仍留在本块里**，
		//   且其中一条**仍带着那句已被自己证伪的「保留期从 24h 变 8h」说明**
		//   ⇒ 「注释说三个都删了、代码只删了一个」⇒ 注释与实现口径不一致。
		//   ⇒ 本轮把剩下 2 个一并删除 ⇒ 注释与实现这才对齐。
		//   ★ 删除的**安全性**已逐条核实（不是「看着像死键就删」）：
		//     · 读侧不可达：`_request_logs_bodies` 在 promoteSpecs()/ensureSpecs()
		//       里**一个条目都没有**（全文件仅 partition_manager.go:2331 的 case 标签
		//       出现过该名字）⇒ resolvePromoteConfig 的该分支**永远不会被调用**。
		//     · 表已不存在：迁移 890 的步骤 5 DROP 清单里确有
		//       'request_logs_bodies' / '_request_logs_bodies' /
		//       'request_logs_bodies_hot' / '_request_logs_bodies_hot'。
		//     · 读侧 nil-安全：Registry.Spec(key) 对未注册键返回 nil
		//       （不是 error）⇒ GetPlatformInt 直接落 fallback。
		//     · 写侧会**如实报错**：settings/store_db.go 的 Set/SetTenant 对未注册键
		//       返回 "unknown setting: <key>" ⇒ 211/252 里那条已存在的 kv 行（值 8）
		//       在 UI 上再写会**显式失败**，而不是继续被静默吞掉
		//       ⇒ 这正是「去掉假旋钮」应有的可见后果。
		//   ⏳ 残留（已登记，未在本轮动）：bg/partition_manager.go:2331 的
		//     `case "_request_logs_bodies":` 分支本体还在（连同它读的两个键名）。
		//     它现在**既无注册键也无表**，是一段纯死代码；删它属纯清理，
		//     但与「旋钮改指向会话族 bodies 链」这个属主决策耦合 ⇒ 留给那一轮一起做。
		// ====================================================================
		{Key: "lifecycle.analysis_events_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(3650), Default: 7, DangerLevel: Warning, HotReload: true, Description: "analysis_events 保留天数", DescriptionLong: "analysis_events 的清理保留天数。读方：bg/partition_manager.go（fallback 7）。", Unit: "天"},
		{Key: "lifecycle.credential_model_index_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(3650), Default: 7, DangerLevel: Warning, HotReload: true, Description: "credential_model_index 保留天数", DescriptionLong: "credential_model_index 的冷分区裁剪保留天数。读方：bg/partition_manager.go:1288（fallback 7）。★ 与 §143.21 的「冷族 TTL 空文」问题同源：冷月分区已转 heap ⇒ 7 天 TTL 在冷族上是空文。", Unit: "天"},
		{Key: "lifecycle.provider_error_details_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(3650), Default: 30, DangerLevel: Warning, HotReload: true, Description: "provider_error_details 保留天数", DescriptionLong: "provider_error_details 的保留天数。读方：bg/partition_manager.go（fallback 30）。", Unit: "天"},
		{Key: "lifecycle.routing_feedback_log_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(3650), Default: 14, DangerLevel: Warning, HotReload: true, Description: "routing_feedback_log 保留天数", DescriptionLong: "routing_feedback_log 的保留天数。读方：bg/partition_manager.go（fallback 14）。", Unit: "天"},
		{Key: "lifecycle.routing_optimization_metrics_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(3650), Default: 30, DangerLevel: Warning, HotReload: true, Description: "routing_optimization_metrics 保留天数", DescriptionLong: "routing_optimization_metrics 的保留天数。读方：bg/partition_manager.go（fallback 30）。", Unit: "天"},
		{Key: "lifecycle.session_project_backfill_batches", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(10000), Default: 50, DangerLevel: Warning, HotReload: true, Description: "project_dim 回填单轮最大批数", DescriptionLong: "project_dim 回填 worker 单轮最多跑多少批。读方：bg/project_backfill_worker.go:167（fallback 50）。★ 代码侧另有 ≥1 的安全下限。", Unit: "批"},
		{Key: "lifecycle.stats_event_inbox_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(3650), Default: 7, DangerLevel: Warning, HotReload: true, Description: "stats_event_inbox 保留天数", DescriptionLong: "stats_event_inbox 的保留天数。读方：bg/partition_manager.go（fallback 7）。", Unit: "天"},
		{Key: "lifecycle.supplier_error_stats_ttl_days", Type: TypeInt, Scope: ScopePlatform, Category: CategoryLifecycle, Min: floatPtr(1), Max: floatPtr(3650), Default: 30, DangerLevel: Warning, HotReload: true, Description: "supplier_error_stats 保留天数", DescriptionLong: "supplier_error_stats 的保留天数。读方：bg/partition_manager.go（fallback 30）。", Unit: "天"},
	}
}
