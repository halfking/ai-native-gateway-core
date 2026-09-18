# D03 三层缓存 provenance 子代理报告（窗口：48h = 643735a28^..HEAD；重点增量 = b75c91900..HEAD）

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 触发路径 / 建议处置 |
|---|---|---|---|---|
| 1 | P3 | `lookupTurnNumber` 新增 `ts >= NOW() - INTERVAL '30 days'` 后，存活超过 30 天的会话在 request_logs 遥测侧 turn_no 重新从 1 计数，与 session v2 聚合器权威 turn_no 发散；任何取证/导出/前端跨存储按 turn 号关联 provenance 的消费方（request_logs.metadata.compression_meta ↔ session_turns.compression_meta）在 >30d 会话上会拿到两套不一致的轮号 | domains/hooks/observability/telemetry/client.go:3436（注释 3426-3434 已自认此偏差，称"session/v2 aggregator owns the authoritative turn_no"） | 触发路径：一个 >30d 的长会话再发一轮 → 遥测 COUNT 只统计 30d 窗口 → request_logs.turn_no 回绕。处置：P3 登记，建议在两条遥测/取证读侧文档标注"request_logs.turn_no 仅 30d 窗口内可信"；若 sessionforensics 导出有按 turn_no join 的 SQL 需亲查（本轮未逐条核查该 join 面） |
| 2 | P3 | `attachments.Repository.DeleteOlderThan` 的 ctid 分块加固落在一个**当前零生产调用方**的方法上（仅 repository_test.go 调用）；且中途出错返回已删部分计数即退出（best-effort 契约内可接受，但保留期清理实际未接线） | domains/attachments/repository.go:281-309；全仓 `\.DeleteOlderThan\(` 仅命中 repository_test.go:103 | 触发路径：无——该方法现无调度方（注释称"admin tooling calls this on a schedule"，实际接线缺失）。处置：P3 顺带清账——要么在 data_lifecycle 调度里接线，要么注释标注"暂无调用方"，避免误以为保留期在生效 |

无 P0/P1/P2 候选。重点增量四处关注的结论：**R35 既定健康面未被本窗口改动破坏**（详见 §二）。

## 二、核实为健康的面

**1. session_writer_v2 写路径变更后三层 provenance 仍完整落库（重点问题 1）——健康**
- 变更仅为共享 turn 事务开头追加 `set_config('app.current_tenant', $1, true)`（domains/session/v2/session_writer_v2.go:353，R41 a64f16480），不改动任何序列化内容。
- provenance 载荷 `TurnRecord.CompressionMeta = req.CompressionMeta`（session_writer_v2.go:427）仍在同一事务内经 `json.Marshal` 写入 turn 行（domains/session/v2/turn_writer.go:298）；bodies/turn/final_full/memora/outbox enqueue 全部携带 `TenantID=req.TenantID`，GUC 与行租户严格一致。
- GUC 名与策略函数闭环核对：`get_current_tenant()` = `COALESCE(NULLIF(current_setting('app.current_tenant', true),''),'default')`（sql/migrations/startup/001_users_table.sql:36-42）；outbox 策略 tenant 分支命中（sql/migrations/startup/630_session_aggregate_outbox.sql:73-75）。
- `EnqueueSessionAggregateOutbox` 生产侧唯一调用点即该路径（session_writer_v2.go:650），无其它未设 GUC 的 enqueue 入口。
- pgxmock 期望顺序（set_config → session lock → 读 → 写）与实现一致（domains/session/v2/session_writer_tx_test.go:190-193 等五处）。

**2. outbox reaper 新增逻辑不会误删/跳过携带 AlignmentMap 的未投递事件（重点问题 2）——健康（结构性不可能）**
- outbox 载荷 `SessionUpdate` 只含聚合计数/摘要字段，**根本不携带 AlignmentMap/SanitizedMessageRefs**（domains/session/v2/session_aggregator.go:62-96）；provenance 在 turn/bodies 行内，不经 outbox。
- 清理仅删 `status='done' AND completed_at < NOW()-7d LIMIT 5000`（session_aggregate_outbox_reaper.go:274-282），pending/claimed/dead 永不被 trim。
- markDone/markDead/scheduleRetry 三者 `AND status='claimed'` 守卫原样保留（reaper.go:391-460）；新增 `execWithBypassTx`（reaper.go:639-663）与既有 reference 实现 durable/rls.go:57-78 模式一致（begin → 双 GUC → exec → commit）；GUC 名与 630 策略的 `current_setting('app.current_role'/'app.bypass_rls')` 分支逐字匹配（630:74-75,79-80）。claim 事务本就设双 GUC（reaper.go:301-305），claim→replay→markDone 链无新增跳过窗口。FORCE RLS 行为有真库集成测试覆盖（session_aggregate_outbox_reaper_integration_test.go:121,439）。
- 附带小差异（非缺陷）：reaper 的 deferred rollback 用 `ctx`，durable 用 `context.WithoutCancel(ctx)`——纯风格差。

**3. attachments 仓库变更保持引用闭环与租户隔离（重点问题 3）——健康**
- 变更只有 DeleteOlderThan 分块化（3a9dca46d）；`request_attachments` 是非分区普通表（sql/migrations/startup/401_request_attachments_relational.sql:29），ctid 自选安全（无跨分区 ctid 撞号），WHERE 语义与改前完全一致，循环以 `RowsAffected < chunk` 终止。
- `request_attachments` 无 RLS（723 仅对 `public.attachments` 与 candidate_failure_logs_columnar_old ENABLE，sql/migrations/startup/723_rls_enable_attachments_and_cfl_old.sql:28-40），分块 DELETE 不受 Phase 2 影响。
- sanitized→original 引用闭环：本仓库只存元数据（hash/storage_path/original_url，repository.go:19-33），无 tenant_id 列；租户隔离由 admin 下载路由的归属校验承担（R35/R36 修复：admin/attachments_routes.go:31-96，request_id→request_logs.tenant_id 联查 + fail-closed 404），本轮亲读确认在 HEAD 完好。

**4. provider-window source telemetry 写读两侧未受影响（重点问题 4）——健康**
- 写侧 `buildOutboundProvenance`（domains/streaming/request_log_pipeline.go:1398-1452：window_source:1414、alignment_map_truncated:1421、sanitize_refs_truncated:1431、128KB 计数器兜底：1446）为 R35 定案代码，重点窗口零 diff。
- 镜像白名单三键齐全 + `safeWindowSource` 有界过滤（internal/sessionv2mirror/hook.go:439-451、458-476）——R36 回归点未复发。
- 读侧三键在 `applyCompressionMeta`（domains/session/v2/cache_v2.go:777-779）。写/镜像/读三层对齐。

**5. 零改动健康面（git log 已核）**
- `domains/hooks/compression/`、`domains/secretmask/`、`domains/hooks/security/` 在 **48h 全窗口（643735a28^..HEAD）与重点窗口均零改动**——alignment.go/session_cache.go/session_compressor.go/quality_score.go 及 sanitizer 跨进程 offset 原子锁（R35 定案）原封未动；e2e_alignment_test / session_cache_concurrency_test 基线未被触碰。
- 域清单其余条目：§3.1（映射完整性——映射代码零改动，截断有 flag 非静默）、§3.2（offset 原子预占——未触碰）、§3.5（四类 TTL——无新增缓存对象；bg/vacuum_worker.go 变更仅移除对分区父表的 VACUUM FULL，改热表 `VACUUM (ANALYZE)`，不属四类）、§3.6（quality_score——零改动）均健康。

## 三、未覆盖项与原因

- **FORCE RLS / 降权真库实跑**：reaper RLS 集成测试与 630 策略命中需真库凭据与真库状态，只读审计环境未运行；建议主代理验证门跑 `go test ./domains/session/v2/... -count=1`（涉并发包加 -race）。
- **build/vet/test 三门**：本子代理只读不执行构建，留给主代理收尾门。
- **sessionforensics 导出侧按 turn_no 的 join 面**：3a9dca46d 对 UpsertSummary 补了 tenantID（export.go:431、service.go:181，属修复），但导出 SQL 是否存在 turn_no 关联未逐条亲查——与发现 #1 的处置联动，交主代理定夺是否追查。
- **D05 压缩触发/准入、D04 队列缓存、D07 分区存储**：按域边界不查。
- **request_log_pipeline.go 的 R35/R36 hunks**：属窗口内"已审计增量"（45054170c/ad68c91ac），本轮只做 HEAD 现行亲读确认在位，未逐行重审已定案 diff。

**主代理复核结论（R43）**：两条 P3 均成立，登记遗留（turn_no 权威面另有归属、DeleteOlderThan 无调用方）；四个健康面采信并入轮文档。
