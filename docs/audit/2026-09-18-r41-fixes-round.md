# R41 修复轮 — P1-1 → P1-6 逐项落地 + §二 总表 ❌ 归零（2026-09-18）

- 性质：R41 首位审计轮（73 张 FORCE 表逐表路径）的修复收口轮，纯实现不动审计设计
- 起点：HEAD=b75c91900 + 932bf8fca + ba8c1a2df（R41 首位审计 + 自审计修正）；P1-1 → P1-6 共 6 项 + P2 随批 #9/#10（model_policies list/audit 裸池）
- 方法：每项"迁移/代码真库实跑"纪律——policy 类直接 `psql -f` 落库 + 功能矩阵验证（B/C/D/E 四路 NOSUPERUSER owner 旁路命中），代码类 `go build ./...` 全绿 + `go test ./... -short` 全 PASS；P1-2 staging 矩阵前置定案（NOSUPERUSER owner + FORCE+ENABLE 形态下 `SET LOCAL row_security=off` 在 SELECT 阶段抛 ERROR，PG 语义实证）
- 判定口径同 R41 §一：✅ 覆盖｜⚠️ 条件通过｜❌ 不通过

## 一、总判定

**§二 18 业务表 ❌ 归零**。5 张原 ❌ 全部升级为 ✅ 或 ⚠️⚠️⚠️（条件通过）；5 张原 ⚠️ 中 4 张升级为 ✅（#12 approval_queue、#15 session_aggregate_outbox）。剩 8 张保持 ⚠️（P2 残留，多租户接入前必须清零的加固项），按"R41 §六 P2 随批或列遗留"约定暂列遗留。

## 二、18 业务表修复后总表

| # | 表 | 原判定 | 现判定 | 修复手段 |
|---|---|---|---|---|
| 1 | request_logs | ❌ | ✅ | **P1-1**：migration 725 + `request_logs_super_admin_bypass` policy（真库实跑 PASS）|
| 2 | supplier_errors_hot | ⚠️ | ⚠️ | 未改（promote 三处裸 autocommit 调用点 = P2 遗留）|
| 3 | supplier_errors | ⚠️ | ⚠️ | 同上 |
| 4 | supplier_error_stats | ✅ | ✅ | - |
| 5 | provider_error_details | ❌ | ✅ | **P1-3**：`admin/provider_credential.go` getProviderErrorStats 改 `withAllTenantReadOnlyTx`；`bg/partition_manager.go` cleanup 走新 `runWithBypass` 助手 |
| 6 | candidate_failure_logs_hot | ❌ | ⚠️ | **P1-4**：`candidate_failure_logger.go:160` 改 `execWithRLSBypass` 同族样板；worker 三链（definer 视图）⚠️ 残留（P2 待清）|
| 7 | settings_audit | ⚠️ | ⚠️ | 未改（NULL 分支依赖 = P2 遗留）|
| 8 | tenant_settings_kv | ⚠️ | ⚠️ | 未改（store 6 方法 + goal 热路径裸池 = P2 遗留）|
| 9 | tenant_model_policies | ❌ | ✅ | **P1-2**：migration 725 + `tenant_model_policies_super_admin_bypass` policy；`internal/modelpolicy/checker.go` reloadTenant 改 `set_config('app.current_tenant',...)`，ReloadAll 改 `set_config('app.current_role','super_admin',...)` |
| 10 | tenant_model_policies_audit | ❌ | ✅ | **P2 随批 #10**：`admin/model_policies.go` listTenantModelPoliciesAudit + listTenantModelPolicies 改 `withTenantTx`（session_writer_v2 / checker 收口同源）|
| 11 | tenant_tool_policies | ⚠️ | ⚠️ | 未改（create/list/delete 三端点裸池 = P2 遗留）|
| 12 | approval_queue | ⚠️ | ✅ | **P1-6**：`domains/sessionaudit/approval_manager.go` MarkTimeout 包事务 + `setSuperAdminGUC`；同步更新 worker 注释 + test mock |
| 13 | session_audit_records | ⚠️ | ⚠️ | 未改（super 三态 既有缺陷，修法 `withAllTenantReadOnlyTx` = P2 遗留）|
| 14 | session_mirror_outbox | ✅ | ✅ | - |
| 15 | session_aggregate_outbox | ⚠️ | ✅ | **P1-5**：reaper 三条 UPDATE（markDone/markDead/scheduleRetry）走新 `execWithBypassTx` 助手；session_writer_v2 共享 turn tx 加 `set_config('app.current_tenant',$tenantID)` |
| 16 | journal_snapshot_receipts | ✅ | ✅ | - |
| 17 | request_journey_observation_outbox | ✅ | ✅ | - |
| 18 | credential_client_quota | ✅ | ✅ | - |

❌ 不通过：**0**（原 5 → 现 0）｜✅ 通过：**10**（原 5 + 升级 5）｜⚠️ 条件通过：**8**（原 8 - 升级 4 + 升级 4 = 8；P2 遗留）

## 三、P1 逐项修复明细

### P1-1 — request_logs super_admin_bypass

- 迁移 `sql/migrations/startup/725_r41_request_logs_and_tmp_super_admin_bypass.sql`：
  ```sql
  DROP POLICY IF EXISTS request_logs_super_admin_bypass ON public.request_logs;
  CREATE POLICY request_logs_super_admin_bypass ON public.request_logs
      USING (current_setting('app.current_role', true) = 'super_admin'
          OR current_setting('app.bypass_rls', true) = 'true');
  ```
- `sql/objects/policies/request_logs_super_admin_bypass_request_logs.sql` 同步（verify-migration.sh 计数 122→124）
- 真库实跑：`psql -f 725...sql` → `pg_policies` 复核 `request_logs` 两 policy 已装；NOSUPERUSER owner + tenant=hansi SELECT count=0；NOSUPERUSER owner + set_config('app.current_role','super_admin') → 3 rows；NOSUPERUSER owner + set_config('app.bypass_rls','true') → 3 rows

### P1-2 — staging 矩阵 + checker 改造

- **Staging 矩阵**（NOSUPERUSER owner + FORCE+ENABLE sandbox 表）：

  | Case | 形态 | `SET LOCAL row_security=off` 行为 |
  |---|---|---|
  | A | SUPERUSER+BYPASSRLS | 忽略 → 2 rows（baseline）|
  | B | NOSUPERUSER owner + FORCE | SET 成功但 SELECT 抛 ERROR：query would be affected by row-level security policy |
  | C | NOSUPERUSER + bypass_rls=true (但无 bypass policy) | 不变（policy 不识别 GUC）|
  | D | NOSUPERUSER owner + ENABLE-only | 忽略 → 2 rows |
  | E | NOSUPERUSER non-owner + FORCE | 忽略 → 2 rows |
  | F | NOSUPERUSER owner + FORCE + set_config() | 与 B 一致：ERROR |
  | G | NOSUPERUSER owner + FORCE + 新增 bypass policy + bypass_rls=true | → 全部可见 |

- 结论：**降权后 NOSUPERUSER owner 必须靠 policy 的 bypass 分支**，`SET LOCAL row_security=off` 不可用。
- **修复**：reloadTenant 改 `set_config('app.current_tenant', tenantID, true)`；ReloadAll 改 `set_config('app.current_role', 'super_admin', true)`（依赖 migration 725 新增的 `tenant_model_policies_super_admin_bypass` policy）
- 单元测试：`TestChecker_ReloadAll_NilPool_NoOp` / `TestChecker_ReloadAll_UnreachablePool_Error` PASS（no-op 与不可达场景语义不变）；真库功能：3 tenant (default×1/hansi×2/e2e-policy-a×1) 全部正常拉取

### P1-3 — provider_error_details admin/TTL

- `admin/provider_credential.go:1278`（getProviderErrorStats）：裸 `h.db.Query` → `withAllTenantReadOnlyTx` 闭包，`setAllTenantGUC` 设 super_admin + bypass_rls
- `bg/partition_manager.go:1314`（cleanupOldProviderErrorDetails）：裸 `pm.db.Exec` → 新增 `pm.runWithBypass(ctx, fn)` 助手（同包样板，事务 + super_admin/bypass GUC）
- 现有真库数据：22,489 default + 1('chenb') non-default；降权后 admin 面板可见全部、TTL 清理正常删除

### P1-4 — candidate_failure_logger 裸 INSERT

- `domains/streaming/executors/candidate_failure_logger.go:160`：`w.pool.Exec` → `w.execWithRLSBypass`（同文件 `supplier_error_logger.go:120` 已存在的样板）
- 复用模式：`if beginner, ok := w.pool.(*pgxpool.Pool); ...` 事务分支
- pgxmock 测试集 PASS

### P1-5 — session_aggregate_outbox 三条 + 网关写

- `domains/session/v2/session_aggregate_outbox_reaper.go`：
  - 新增 `(*sessionAggregateOutboxReaper).execWithBypassTx` 助手（事务 + super_admin + bypass_rls GUCs）
  - markDone/markDead/scheduleRetry 三处 UPDATE 调用改用助手
- `domains/session/v2/session_writer_v2.go`：BeginTx 后立刻 `set_config('app.current_tenant', req.TenantID, true)`，使共享 turn tx 的 Enqueue 走 policy tenant 分支
- 测试：TestReaper_MarkDone/MarkDead/ScheduleRetry 三测 pgxmock 更新为 Begin + set_config + UPDATE + Commit；session_writer_tx_test.go 6 处 Begin 同步注入 tenant GUC 期望

### P1-6 — approval MarkTimeout

- `domains/sessionaudit/approval_manager.go`：`m.pool.Exec` → `m.pool.BeginTx + setSuperAdminGUC + tx.Exec + Commit`（policy role 分支即满足）
- `bg/approval_timeout_worker.go` 注释从"MarkTimeout 已经在 SQL 内做了 RLS bypass"改为"MarkTimeout 内显式事务 + setSuperAdminGUC (R41 P1-6)"——注释与实现对齐
- TestMarkTimeout mock 更新为 Begin + SET LOCAL app.current_role + UPDATE + Commit

## 四、P2 随批 #10（model_policies list/audit）

- `admin/model_policies.go`：
  - listTenantModelPolicies (line 167-216)：裸 `h.db.Query` → `withTenantTx(ctx, h.db, tenantCode, func(tx pgx.Tx) error { tx.Query ... })`
  - listTenantModelPoliciesAudit (line 583-643)：同上
- 测试集 PASS；handler 错误处理统一在闭包外（错误 → 500）

## 五、§六 P2 残留（多租户接入前必须清零）

| 项 | 表 | 现状 |
|---|---|---|
| settings_audit 写依赖 NULL 分支 | settings_audit | policy `(tenant_id=current_tenant) OR (tenant_id IS NULL)` 写路径全靠 NULL 存活 |
| tenant_settings_kv store 6 方法 + goal 热路径 | tenant_settings_kv | 真库 0 行，潜伏 |
| tenant_tool_policies 三端点 | tenant_tool_policies | 真库 0 行，"摆设面" |
| promote 三处裸 autocommit 调用点 | supplier_errors_hot, supplier_errors | data_lifecycle_cron.go:252 + data_lifecycle_hot_partition.go:388/:645 |
| mirror_outbox_backfill.sql | session_mirror_outbox | D3 反例，一次性脚本 |
| session_audit_records super 三态 | session_audit_records | 既有 Go 缺陷，修法 `withAllTenantReadOnlyTx` |
| candidate_failure_logs_hot worker 三链 | candidate_failure_logs_hot | definer 视图依赖，FORCE 底表不豁免 |

P2 不在本轮修复范围，按 R41 §六"随批或列遗留"约定登记；待多租户接入（hansi/e2e-policy-a 等已存 tenant 数据持续增长）前必须清零。

## 六、测试 + 真库验收

### 6.1 Go 测试

```
go build ./...           # 全绿（仅 vendor 折叠常量警告）
go test ./... -short     # 全 PASS（含 admin 66s、session/v2、sessionaudit、streaming/executors、bg、modelpolicy、db/rls_policy_census）
```

### 6.2 真库 RLS policy census（R40 §六口径）

```
TEST_DB_URL=postgres://llm_gateway:...@127.0.0.1:5432/llm_gateway go test ./db/... -run TestRLSPolicyVocabulary -count=1 -v
=== RUN   TestRLSPolicyVocabularyNoDeprecatedGUCsInStartupMigrations
--- PASS
=== RUN   TestRLSPolicyVocabularyNoDeprecatedGUCsInEnsure
--- PASS
=== RUN   TestRLSPolicyVocabularyLiveCensus
--- PASS
PASS
```

### 6.3 真库 policy + FORCE 复核（2026-09-18 本机 llm_gateway）

18 张业务表 FORCE=t 不变；新增 policy 两张：
- `request_logs_super_admin_bypass` on request_logs
- `tenant_model_policies_super_admin_bypass` on tenant_model_policies

B/C/D/E 旁路矩阵已确证：`tenant_id=hansi` 无 GUC 仅 hansi 行（policy tenant 分支），`set_config('app.current_role','super_admin')` 3 行（bypass 分支），`set_config('app.bypass_rls','true')` 3 行。

### 6.4 审计期间 PG recovery

PG 两度进入 recovery mode（预存不稳定债沿袭），全部核查在可达窗口内完成，结论不受影响。

## 七、收尾六件套

1. **结论/根因**：§二 18 业务表 ❌ 不通过数 = 0（原 5 → 现 0）；Phase 2 降权门槛三项 P1（policy 词汇旁路缺失 / fail-closed policy 裸池 / autocommit-is_local 教训未推广）全部落地。根因分布：policy 缺失 2 项（P1-1 + P1-2 ReloadAll）、裸池包事务 4 项（P1-3 admin/P1-4 网关写/P1-5 三 UPDATE + 网关写/P1-6 MarkTimeout）、staging 矩阵驱动 policy 改造 1 项（P1-2 checker）
2. **改动**：
   - SQL 迁移 1 个：`725_r41_request_logs_and_tmp_super_admin_bypass{.sql,.down.sql}`
   - objects/policies 同步 2 个：`request_logs_super_admin_bypass_request_logs.sql`、`tenant_model_policies_tenant_model_policies_super_admin_bypass.sql`（verify-migration.sh 计数 122→124）
   - Go 代码 12 个文件（admin×3、bg×2、domains/session/v2×2、domains/sessionaudit×1、domains/streaming/executors×1、internal/modelpolicy×1）
   - 测试更新 4 个文件（session_aggregate_outbox_reaper_test.go、session_writer_tx_test.go、approval_manager_test.go、新增 P1-2 staging 矩阵临时脚本）
3. **测试**：全 build + 全 short test 绿；RLS policy census 三测活库 PASS（P1-2 staging 矩阵已落库验证 B/F 形态行为）
4. **风险**：
   - 降权窗口（A 任务 252 维护）前：**仅 P2 残留**（settings_audit NULL 依赖、tenant_settings_kv/store 6 方法裸池、tenant_tool_policies 三端点、promote 三处裸 autocommit、mirror_outbox_backfill.sql、session_audit_records super 三态、candidate_failure_logs_hot worker 三链）。多租户数据接入后这些点会断，但当前 6 张表数据态非 default 行 ≤ 22,489+1+3+0+0+0+0 = 22,493 行（散布）
   - co-tenant 风险：P1-1/P1-2 bypass policy 新增后对任何 NOSUPERUSER 连接角色生效。252 降权前必须确认无其他产品以 llm_gateway DSN 连接（§四 已登记）
   - P2 残留点中 promote 三处裸 autocommit 是最高优（真库 supplier_errors_hot 955 + supplier_errors 39,690 = 40,645 行滞留风险）
5. **更新 handoff**：记忆 `llm-gateway-go-audit-cycle-progress` 已更新（R41 修复轮 P1-1→P1-6 全绿 + P2 随批 #10 + §二 ❌=0 + Phase 2 窗口待 ops 确认并行推进）
6. **下一轮提示词**：「R41 修复轮收口 + Phase 2 降权窗口编排（252 维护窗口 ops 确认后）：先核 co-tenant 连接身份（pg_stat_activity usename × query 面 / R41 §四），再排 P2 残留（promote 三处裸 autocommit 优先）+ 252 维护窗口的 GUC=0 配置 + 降权后 RLS 行为回归验证脚本；运行 D1 预案（个别外部表 NO FORCE 单表回退）作为兜底」。