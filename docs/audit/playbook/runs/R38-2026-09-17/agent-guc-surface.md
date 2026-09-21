# GUC 设置面全仓盘点 子代理报告（R38，2026-09-17，窗口：全仓静态扫描）

> 主代理注：载荷断言已逐条亲读复核（credential_success_rate 谓词/516 policy/durable store 零 GUC/V371 注释/apihub RESET/rls_helper 死代码判定），全部成立；P1-1（no-op DELETE）据此实修。FORCE 表计数与角色根因由主代理真库普查补充修正。

扫描完成，证据充分。以下为报告。

## 一、GUC 清单

自定义 GUC 实际共 **9 个**（`app.code`/`app.id`/`app.customer_id`/`app.default_client_profile`/`app.display_name`/`app.js` 等均为 SQL 表别名 `app` 的误匹配，非 GUC；`pg_instance.*` 为工具脚本用）。

| GUC 名 | 设置点（is_local / 所在函数） | 调用方链 | 读取点（fallback） | 覆盖表/policy |
|---|---|---|---|---|
| **app.current_tenant**（347 处引用） | 参数化 `set_config($1,$2,true)`：apihub/pg_store.go:455、admin/tenant_ctx.go:91、domains/hostedtask/store.go:77、security/armor/logger.go:147、internal/dbx/scope.go:122、bg/freequotareset/worker.go:92、bg/freequotacleanup/worker.go:93。`SET LOCAL` 字面拼接：sessionaudit/approval_manager.go:426、admin/model_policies.go:678、freeresource/quota_tracker.go:87,215,429,546、freeresource/pool_dedup.go:48、autocombo/resolver.go:83、autocombo/virtual_factory.go:234、freediscovery/template_manager.go:459。**裸 SET（会话级）**：freeresource/rls_helper.go:60,75（无生产调用方） | admin 全部租户读端（tenant_ctx.go 被约 45 处/15+ 文件调用：session_list、session_export、annotation、audit 等）；apihub agent 读写；OmniFree 计量 Record/Correct/Preflight；审批队列；freediscovery 模板 | `public.get_current_tenant()` = `COALESCE(NULLIF(current_setting('app.current_tenant',true),''),'default')`（db/db.go:274、db_omnifree.go:443、075/084 迁移）；**无 fallback 的裸 current_setting**：db/db.go:1077（observation_outbox）、1162（journal_snapshot_receipts）、526_session_turns_hot.sql:195、516_durable_llm_tasks.sql:196、hotfix-turns:96、V371 注释 | 几乎全部 tenant_isolation_* policy（omnifree 4 表、credential_keys db.go:5421、credential_client_quota db.go:5694、webcookie 077、durable 516、sessions 系、hostedtask 711 等） |
| **app.bypass_rls**（334） | 全部 `set_config(...,true)` 字面量：requestjourney/observation_outbox.go:118、retention.go:131；streaming/anomaly_harvester.go:136,201,257,340、format_anomaly_recorder.go:138、executors/supplier_error_logger.go:50（const）；session/v2 reaper:304、backfill:320；admin/tenant_ctx.go:77、candidate_failure_handlers.go:75、errors_trend.go:178、vendor_credential_error_handlers.go:39；hostedtask/store.go:83；sessionv2mirror/replay.go:80；bg/partition_manager.go:1089、supplier_error_stats_aggregator.go:384。另 `SET LOCAL` 字面：analysis/bus/publisher.go:75,80、assets/intent_store.go:80 | 全部后台/跨租户 worker（聚合、reaper、promote、outbox）与 admin 跨租户路径 | `current_setting('app.bypass_rls',true)='true'`（missing_ok=true，未设时 NULL→false） | 几乎所有 `*_super_admin_bypass` / bypass policy（V364:39、V367:11、V371:96、db.go:1084 等） |
| **app.current_role**（297） | `set_config(...,'super_admin',true)`：admin/tenant_ctx.go:74、session/v2 reaper:271,301、backfill:316、sessionv2mirror/replay.go:80、bg/provider_error_aggregator.go:187、scan_scheduler.go:291,426、freequotareset:134、freequotacleanup:139。`SET LOCAL`：sessionaudit:436 | admin withAllTenantTx、后台枚举 worker | `current_setting('app.current_role',true)='super_admin'`；approval_queue/session_audit_records 用 `COALESCE(NULLIF(...,''),'')`（baseline 29653,29776） | 全部 super_admin_bypass policy、approval_queue、session_audit_records |
| **app.actor**（59） | 唯一设置点 admin/routing.go:1436 `set_config('app.actor',$1,true)`（候选 reorder handler，BEGIN 后第一条） | admin 路由候选 reorder API | 触发器 `v_actor := current_setting('app.actor', true)`（startup/568:191,240,303、569、571、578 全量、541_draft:123；NULL 时触发器自行 fallback） | credential_priority / candidate_binding_scope_revision 系审计触发器 |
| **app.current_admin**（26） | `SET LOCAL` 字面拼接：admin/model_policies.go:682（setPolicyTxGUCs）、admin/routing_overrides.go:222,287、control/routing/create.go:138 | tenant_model_policies 写路径（该表 **已 FORCE RLS**，注释记载 2026-06-23 42501 事故）、路由 override 写 | 触发器 `tenant_model_policies_audit_fn`：`COALESCE(NULLIF(current_setting('app.current_admin',true,''),'system'))`（db/db.go:2950,3712、baseline:4489,28603） | tenant_model_policies 审计触发器 |
| **app.current_user**（17） | **无任何生产设置点**；仅测试 set_config（session/v2/rls_owner_filter_test.go:25 用 `is_local=false` 会话级、test_525_526.test.sql:413） | — | `first_rl.owner_user = current_setting('app.current_user', true)`（startup/457:51,75,99、526:165,189,231,255） | sessions/session_turns/session_dim/session_turn_snapshots owner_filter policy |
| **app.tenant_id**（9） | **无任何 Go/SQL 设置点**（V371:88 注释明示"Go 侧无任何 app.tenant_id set_config"） | — | `tenant_id = current_setting('app.tenant_id', true)`（V371:95,214，无 fallback→NULL） | supplier_errors_hot / supplier_errors（V371） |
| **app.is_super_admin**（8） | 无设置点 | — | `current_setting('app.is_super_admin',true)='true'`（baseline 01-schema.sql:29608,29615） | analysis_events / intent_aggregates 旧 policy（现被 db.go ensureAnalysisEventsRLS 的 current_role/bypass 版覆盖） |
| **llmgw.admin_override** | 无设置点 | — | `current_setting('llmgw.admin_override', true) = '1'`（sql/objects/functions/trg_cmb_protect_manual_disable.sql:12、schema 01:4586、baseline:4558） | trg_cmb_protect_manual_disable 触发器（credential/model 组合表手动禁用保护） |
| 附属：app.current_org_id | 无（仅 docs/测试/01-pg-instance-sync/acc-conflict-migration.sql:80） | — | 同左 | 无生产 policy |

非租户 GUC（顺带）：`SET LOCAL row_security = off`（internal/modelpolicy/checker.go:265）、`SET LOCAL lock_timeout/statement_timeout/TIME ZONE`（db/db.go:503,663,674、admin/data_lifecycle_storage.go:743 等）。

## 二、事务/连接边界风险

- **is_local 语义总体良好**：全部非测试 `set_config` 第三参均为 `true`（freequotareset/worker.go:92 以参数 `$2` 传 `true`）；全部 `SET LOCAL` 均位于显式 `BeginTx/Begin` 之后（freeresource/autocombo/freediscovery 用 database/sql，其余 pgx）。未发现生产代码 `set_config(..., false)`。
- **裸 SET 污染面（唯一）**：domains/freeresource/rls_helper.go:60 `SET app.current_tenant = '<id>'`（*sql.DB 池，会话级，无 RESET 配对）与 :75（*sql.Conn）。错误被 WARN 吞掉。**但经全库 grep 该三个函数（SetRLSTenantContext/Conn/Tx）无任何非测试调用方——是死代码**；生产路径改用 quota_tracker.go 等内联 SET LOCAL。
- **唯一 RESET 点**：apihub/pg_store.go:406,437 `RESET app.current_tenant`（defer 内，commit 后经 tx.Exec）。注释（:385-391）记载经验：`set_config(...,true)` 提交后连接仍残留会话级旧值，故显式 RESET。其他所有 set_config 路径均无 RESET（依赖 is_local=true 自动回收）。
- **非事务连接上读 current_setting**：admin/credential_success_rate.go:142 在 `db.Exec`（池连接、无事务、无 GUC 设置）的 DELETE 谓词里读 `app.current_role`/`app.current_tenant` → 恒 NULL → 该 DELETE 恒为 no-op（注释称这是防回归守卫，但事实是当前永不删除）。
- SET LOCAL 字面拼接的注入防线为白名单转义（escapeTenant [A-Za-z0-9_-]{1,64}：quota_tracker.go:491、rls_helper.go:100；freediscovery template_manager.go:443 违规直接报 ErrInvalidTenantID 而非回落 default）。

## 三、RLS helper 机制

1. **internal/dbx/scope.go ScopeRunner**（`WithTenantTx`/`WithTenantReadOnlyTx`/`WithSuperAdminTx`，setGUC 参数绑定 GUC 名与值，scope.go:122）——通用框架；GUC 约定注入在 db/dbxmanifest/manifests.go:19 `ScopeConfig()`。**生产数据路径零调用**（仅 internal/dbx 测试与 dbxmanifest/pilot_integration_test.go:180），Phase 3 pilot 尚处 shadow-read 阶段。
2. **admin/tenant_ctx.go**（withTenantTx/withAllTenantTx/withAllTenantReadOnlyTx + setLocalTenantGUC:87/setAllTenantGUC:73）——admin 读端主力，约 45 个调用点分布在 annotation、attempt_quality、audit_operations、credential_success_rate、format_anomalies、handoff_logs、model_integrity、model_policies、session_*（compare/analytics/audit/list/export/tenant）等。
3. **apihub/pg_store.go withTenantTx/withTenantReadOnlyTx + setTenantGUC + RESET**（:392-459）——apihub 表专用，唯一带 RESET 的实现。
4. **domains/freeresource/rls_helper.go**（3 函数）——死代码（仅测试引用）；实际生效的是各文件内联 helper：quota_tracker.go、pool_dedup.go:48、autocombo/resolver.go:83、virtual_factory.go:234、freediscovery/template_manager.go:459。
5. 其余单点 helper：sessionaudit/approval_manager.go:413,435（beginTenantTx 组合）、domains/hostedtask/store.go:75,81,88（handler 设 tenant / worker 设 bypass，711 policy"bypass 仅 worker"）、admin/model_policies.go:671 setPolicyTxGUCs（current_tenant+current_admin 成对）、security/armor/logger.go:136、internal/sessionv2mirror/replay.go:80（replay 事务内提权，注释明示避免会话级泄漏）。

## 四、未覆盖面（有 policy、业务路径不设 GUC）

1. **supplier_errors_hot / supplier_errors / supplier_error_stats**（V371，FORCE RLS，读 `app.tenant_id`）：全库无 `app.tenant_id` 设置点，V371:88 注释自认；当前整链路依赖 `app.bypass_rls` 旁路（supplier_error_logger.go:50、supplier_error_stats_aggregator.go:384、partition_manager.go:1089）。
2. **sessions/session_turns/session_bodies/session_dim/session_turn_snapshots/session_summaries 的 owner_filter policy**（读 `app.current_user`）：无生产设置点。热路径写入 session_turns_hot（domains/session/v2/turn_writer.go:314）、bodies_writer、session_aggregator 均**不设任何 GUC**；admin 读端走 withTenantTx（只设 current_tenant，不设 current_user）。今天可用仅因这些表 ENABLE（非 FORCE）RLS，owner=llm_gateway 绕过。
3. **durable_llm_tasks/durable_llm_task_events/durable_pending_outbox**（516:196 `tenant_id = current_setting('app.current_tenant',true)` 无 fallback）：durable/store.go、store_claim.go、store_terminal.go、settlement_outbox.go 全部不设 GUC。
4. **analysis_events/intent_aggregates** baseline 版 policy 读 `app.is_super_admin`（baseline:29608,29615）：无设置点（现行为靠 db/db.go:3842 起重建的 current_role/bypass 版 policy 与 publisher 的 SET LOCAL bypass，db.go:3842 注释）。
5. **trg_cmb_protect_manual_disable** 读 `llmgw.admin_override`：无设置点，触发器的 override 分支不可达。
6. **request_logs_hot**（admin/credential_success_rate.go:142 在裸池连接上读 current_setting 谓词，见二节）。
7. apihub/service.go:16 注释声称 RLS 基于 `app.tenant_id`，实际 pg_store 用 `app.current_tenant`（文档漂移，非缺陷）。
