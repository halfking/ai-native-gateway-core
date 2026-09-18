# D14 安全场景横切 子代理报告（窗口：48h = 643735a28^..HEAD；重点增量 = b75c91900..HEAD）

依据：docs/audit/playbook/conventions.md、docs/audit/playbook/domains/D14-security.md §3 清单 1–8。只读未改。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 触发路径 | 建议处置 |
|---|---|---|---|---|---|
| F1 | **P0 候选**（CI 必挂 + 迁移交付通道断裂） | 迁移 726（credential_model_index_hot 唯一索引修复）五点同步仅完成 2/5：canonical 与 embeddata 文件副本在，但 (a) `apply-db-revision-sequence.sh` 文件列表止于 725，726 未登记；(b) installer `runner.go` StartupFiles 无 726；(c) installer `main.go` 的 go:embed var + embeddedSQLFiles map 无 726（embeddata 里是死副本）。既有门禁 `TestStartupFilesAreAllEmbedded`（embeddata ⊆ StartupFiles 方向）与 `TestCanonicalStartupMigrationsAtOrAbove704AreRegistered`（canonical ≥704 ⊆ StartupFiles）按当前树**应双红**——与 R40 f5328e13c 同款事故（720 单点交付）三犯 | scripts/apply-db-revision-sequence.sh:440-471（列表末条=725）；installer/internal/dbinit/runner.go:170-184；installer/cmd/llm-gw-installer/main.go:451-470、613-618；sql/migrations/startup/726_restore_credential_model_index_hot_unique.sql；installer/cmd/llm-gw-installer/embeddata/startup/726_*.sql（死副本）；installer/cmd/llm-gw-installer/stats_migrations_test.go:308-341、355-385 | 除 154（手工带外应用）外，任何新环境 fresh install 或 sequence 增量重放都不会建 idx_credential_model_index_hot_unique → bg/auto_index_refresher rollup 每周期 42P10、hot 表持续被 promote+DELETE 清空、auto 路由指标冻结（726 头注记载的 154 生产事故原样复发）；同时 installer 包 `go test` 应挂 | 主代理先跑 `go test ./installer/...` 复核双红；补 runner.go + main.go embed/map + revision-sequence 三点登记（embeddata 副本已在，比照 724/725 的登记形态），走五点同步收口 |
| F2 | **P1 候选**（句柄泄漏，存量缺陷、位于本轮触碰文件内） | `UpsertSummary` 执行 INSERT 用 `e.store.Query` 并把返回的 RowIterator 丢弃（`_, err := ...`），从不 Next/Close。pgx v5 无 SetFinalizer，pgxpool 的 Query 在 rows.Close 前一直占用 puddle 连接资源 → 每次调用永久泄漏 1 个池连接。函数头注释自证漂移："不需要 RETURNING；用 Exec 接口"，实际却是 Query。auto 摘要持久化（service.go:181 → 此处）生产常态执行，池上限 32（db.Open 默认）迟早耗尽 | domains/sessionforensics/export.go:455-467（Query 丢弃）；对照同文件 :257、:377 两条读路径均正确 `defer rows.Close()`；调用链 domains/sessionforensics/service.go:181、cmd/sessionforensics/main.go:155 | auto_summary/CLI 每次落 summary → pgxpool 连接被未 Close 的 rows 占住不归还 → 累计至 MaxConns 后全库 Acquire 阻塞/超时（网关级池饥饿） | 改为 `Exec` 语义（e.store 无 Exec，则 rows 走 `rows.Next()` 一次后 Close，或给 Store 接口补 Exec）；`go test ./domains/sessionforensics/ -count=1` + 泄漏计数钉桩 |
| F3 | P3 | `ensureApiKeyAutoProfileIdentity`：`SET lock_timeout='2s'`（会话级）+ `RESET` 用同一 ctx；若 ctx 在 CREATE INDEX 执行期间被取消，`RESET` 必然同败（仅 Warn），连接带着 lock_timeout=2s 回池，后续复用该连接的启动 DDL 可能 55P03 | db/db.go:1615-1690（RESET 段） | 启动迁移 ctx（3min）超时/取消窗口内恰逢该 ensure → 污染单条池连接 | RESET 改 `context.WithoutCancel(ctx)`；或重试循环失败终态时 `conn.Destroy()` |
| F4 | P3 | taskprofile CSV 导出无公式注入中和：request_id/annotator/reason 为人工可输入字段，`=`/`+`/`-`/`@` 开头值原样写入（csv.Writer 不转义首字符），Excel/WPS 打开即公式注入 | taskprofile/csv.go:71-84 | admin 导出 corrections CSV → 离线用表格软件打开 | 导出侧对 `=+-@\t\r` 开头字段加 `'` 前缀或 `\t` 前缀（admin-gated，低危） |
| F5 | P3（记录既知接受面，非新放大） | 725 两条 super_admin_bypass policy：无 `TO` 子句（对 PUBLIC 生效，谓词即唯一门）；`app.current_role`/`app.bypass_rls` 是自定义 GUC，任何能对池连接执行 SQL 的面都可自设从而满足旁路——即 R38 立案的"GUC 旁路 = 应用自身可信"架构面，725 与既有系列（analysis_events_322、session_bodies_430、output_compliance_316）同形，未引入新放大。租户前台路径（withTenantTx 族）只设 app.current_tenant，无法满足旁路谓词；USING-only 在 ALL policy 下 USING 同时充当 WITH CHECK（PG 语义），未额外放开写权限 | sql/migrations/startup/725_r41_request_logs_and_tmp_super_admin_bypass.sql:27-37；sql/objects/policies/request_logs_super_admin_bypass_request_logs.sql:5；tenant_model_policies_tenant_model_policies_super_admin_bypass.sql:5 | Phase 2 降权后若新增任何把租户输入拼进 SQL 的路径，即可借池连接 SET GUC 越权读全租户 request_logs | 无需本轮动手；Phase 2 降权前 GUC 逐路径审计（R38 硬门槛）覆盖 |

## 二、核实为健康的面

- **725 迁移本体**：`DROP POLICY IF EXISTS + CREATE POLICY` 幂等；.down 完整（对称 DROP 两条，恢复 pre-R41 形态，注释如实记录回滚代价）；与 720 统一词汇后 request_logs/tenant_model_policies 各自已有 tenant_isolation 主 policy（sql/objects/policies/request_logs_tenant_isolation_request_logs.sql、tenant_model_policies_tenant_isolation_tmp.sql），725 是追加的第二条 permissive policy，不修改既有形态——与头注声明一致。
- **durable/rls.go**：旁路/租户 GUC 全部 `set_config(..., true)` 事务级；execWithBypassTx/queryWithBypassTx 均显式 Begin + defer Rollback(WithoutCancel) + Commit 配对；头注把"autocommit 下 is_local GUC 即设即回收"钉成规范，与 R38 教训闭环。
- **sessionaudit MarkTimeout（R41 P1-6）**：裸 pool.Exec 已改显式事务 + `SET LOCAL app.current_role='super_admin'`，BeginTx/Rollback/Commit 配对正确；decide 路径 `FOR UPDATE` + 状态条件 UPDATE 双防并发重复审批；setTenantGUC 手工转义（SET LOCAL 不能参数化的正确替代）。
- **sessionforensics 导出面**：admin/session_export.go 经 resolveExportTenant（super_admin/admin_key 才可 `?tenant=` 跨租户）+ withTenantTx RLS 双门控；导出含原始 request/response body 属 forensics 设计（租户域内、admin 中间件后）；UpsertSummary 增加 tenantID 参数修掉硬编码 'default' 的正确性缺陷，SQL 全参数化。
- **internal/outbox/dispatcher.go**：claim+mark 单事件单事务 + `FOR UPDATE SKIP LOCKED` 锁覆盖整个投递窗；HTTP client 10s 超时（不用 DefaultClient）；backoff 移位钳 6 位防 maxAttempts≥33 溢出负 backoff（重试风暴面已封）；gauge 双 COUNT(*) 30s 节流（gaugeMu 正确）；ctx 取消 → Commit 失败 → Rollback → at-least-once 语义保持。
- **db/db.go**：`poolMaxConnsFromEnv` int32 解析、非正值/溢出 Warn 保持默认 32（坏值不阻断启动，上限旋钮为运维显式行为）；ensureTaskTypeCorrections 纯 IF NOT EXISTS 幂等；手动余额保护谓词收敛为单一常量 `ManualBalanceProtectionPredicate` 且 R42 已做"写时也守卫"的 TOCTOU 收口（floor guard 候选 SELECT + 两条 UPDATE 同谓词，TestBalanceManualProtectionIsWired 钉桩）。
- **admin/provider_credential.go**：updateCredential 的 balance CASE WHEN `IS DISTINCT FROM $n` 全参数绑定无注入，且修复了无关编辑翻转 balance_source 的静默探针暂停；getProviderErrorStats 包 withAllTenantReadOnlyTx（RLS Phase 2 适配）且 rows.Err 升级 500 不静默。
- **RLS Phase 2 适配一批（事务+GUC 配对全对）**：modelpolicy ReloadAll（super_admin 旁路）/reloadTenant（改 tenant GUC，弃 `row_security=off` 这个 NOSUPERUSER 下会抛错的形态）；bg/partition_manager runWithBypass；session/v2 reaper execWithBypassTx；candidate_failure_logger execWithRLSBypass——均为 BeginTx → set_config(true) → Commit、defer Rollback，无裸 SET。
- **共享状态污染面**：bg/node_probe ursmFailureWritable 使 gateway-side 错误不再写共享 Redis URSM v2 键（R39 P1-1 第二面收口），R40 decrypt 豁免细分保留；deescalate sweep 的 R42 权衡有注释存档。
- **资源/锁面**：vacuum_worker 移除对分区父表的 VACUUM FULL（消除 ACCESS EXCLUSIVE 与集群 VacuumFullMutex 空耗）；attachments DeleteOlderThan 改 1 万行 ctid 分块；feature_stats DATE() 谓词改半开区间（分区剪裁）；telemetry lookupTurnNumber 加 30d 窗口恢复剪裁（有行为变化论证）；runTableMaintenanceJob 的 lock_timeout 改会话级 SET + defer RESET（修复 SET LOCAL 事务外 no-op 的 5s 帽失效）。
- **ttl_cache**：LoadOrStore + 每条目 mutex + registry 指针守卫防测试换 Global 的交叉污染；失败不缓存；InvalidatePlatformValue 接入 Set/Rollback/Delete 三写路径；5s TTL 有文档钉死使用条件。
- **.db-audit 三脚本**：无硬编码 DSN/密码（committed 的 sql_catalog.jsonl 复核仅 schema 形态 SQL：password_hash 列定义等，无凭据）；路径全部钉死在脚本相对目录（`.db-audit/tmp`、docs/database/...），无穿越面。
- **errorsx/anthropic_bridge**：KindClientBug 并案 + `bad_request_error` SSE 分支，切断"400→KindTransient→UpstreamDown→冷却/探针乒乓"的凭据状态抖动链（资源竞争面正向修复）。
- **taskprofile 接线**：六条路由全部套 admin 中间件（taskprofile/handler.go:49-57）；since_days/limit/import 行数边界钳制；apply-tier-config 仅写 DB（202609_02 表），非文件落盘面；overlay 文件路径来自运维 env（TASKPROFILE_OVERLAY），失败不半应用。

## 三、未覆盖项与原因

- **三门验证实跑**（`go build/vet/test -race`）：只读审计不执行测试；F1 的双红、F2 的泄漏计数、outbox 的 -race 留给主代理复核。
- **720 .down 912 行改动逐句核对**：720 属 R40 已审迁移，本轮随 merge 带入窗口，未逐行复核 down 链（R42 已记录 .down 豁免策略）。
- **internal/ir/serialize_openai.go（+165）方言序列化深度/数值溢出**：属 D08 协议域；本域仅确认三分支 TargetProvider 接线无并发原语新增。
- **auto_summary_hook.go:142 TenantID 硬编码 "default"**：不在窗口 diff 内且该函数注释自述为未接线 stub；未沿 SummarizeArgs 全链路追到 auth 上下文（时序预算），列此为线索。
- **deploy 脚本族（154/245/seamless）网络与 TCP 会话可靠性**：bash 部署面超出 D14 代码横切取样，且 73c8a6c51/468a1ce82 已有部署锁与超时专项。
- **725 在真库的实跑验证**（迁移三纪律 #1：新迁移必须在存量真库实跑后定稿）：需要真库凭据，只读审计无法执行；154 是否已带外应用 725 亦未验证。
- **bg/balance_manual_protection 与 721 列在 252 共享库的实存性**：需真库查询（三纪律 #3），未做。

**主代理复核结论（R43）**：F1 实锤（亲跑双红）→ 已修复（见 D06 复核）；F2 实锤 → 已修（drain+Close+err 传播）+ 双钉桩测试；F3 成立 → RESET 已改 `context.WithoutCancel`；F4 成立 → 导出已加 `'` 前缀中和；F5 维持既知接受面登记（Phase 2 前置门槛已在 R38 设计）。taskprofile 路由/注入/权限健康面采信。
