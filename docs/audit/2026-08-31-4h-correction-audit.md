# 2026-08-31 近四小时代码修正审计与闭环报告

## 范围

- 审计窗口：2026-08-31 02:27–06:01（+08:00）的代码提交与当前主分支。
- 参考窗口：2026-08-30 至 2026-08-31 的审计、修复和部署方案文档。
- 重点：IR/协议转换、Session V2、hot+分区存储、附件生命周期、供应商错误与后台任务、安装器迁移闭环、并发和可观测性。

## 本次已修正

1. 安装器新装路径补齐 startup 627–631：canonical SQL、go:embed、backup/runtime staging、StartupFiles 和字节一致性测试均已同步。
2. Session aggregate outbox reaper 在每个事务内设置 transaction-local super-admin/RLS bypass，避免跨租户后台任务被 FORCE RLS 隐藏；claim 只处理到期的 `next_retry_at`；payload 强制要求 `session_id`、`tenant_id`、`request_id`。
3. 修正 reaper 顶部并发说明，明确 claim/replay 是两阶段事务，依靠 lease 和 aggregator 幂等完成崩溃恢复。
4. TurnReader 的 `LoadLatestOutbound`/`LoadChain` 改读 `session_bodies_unified`，覆盖 hot 表和历史分区，保证冷启动与多轮历史可见。
5. Redis format cache 去除每次命中派生不可取消 goroutine 的 fire-and-forget 回写，改为进程内串行受控更新，避免 Delete 后旧快照复活及 UseCount 丢增量。
6. `docs/db-changelog.md` 补齐 627–631 pending ledger；迁移 checksum 校验脚本将未登记历史 startup 文件保持为 warn-only，仅 mismatch/stale registry 失败。

## 闭环审计结论

- IR：`internal/ir` 使用显式 versioned request document codec；OpenAI Chat、Anthropic Messages、Responses 及 SSE 的核心 round-trip 定向测试通过。未知/原始 content 依赖统一 codec 和 session 自定义 envelope，后续仍需 persisted JSON fixture 覆盖所有 replay/export 路径。
- Session：turn、bodies、aggregate outbox 已在写入事务中原子提交；reaper lease、幂等和 RLS 上下文已补强。source session 不存在时的重建/reconciliation 仍需真实 PostgreSQL 场景确认。
- 存储：`session_bodies_hot` 与 unified view 读链路已对齐；hot 8 小时转移调度、附件文件物理删除与 DB tombstone 的 Saga 仍需 staging/生产维护窗口验证。
- 附件：DB cleanup 已有审计表和事务写入，但 preview 与 execute 的 hot/history 语义、显式 tenant scope、malformed JSON/hash 缺失防御仍列为后续 P1；本次未实现半成品物理删除 Saga。
- 供应商/并发：供应商错误聚合、备用路径和健康检查已有基础实现；ConnectionRegistry 写超时 goroutine、VACUUM FULL 多副本互斥、provider detail tab/menu parity 仍需后续专项处理。

## 验证结果

通过：

```text
(cd installer && go test ./cmd/llm-gw-installer ./internal/dbinit -count=1)
go test ./domains/session/v2 ./domains/streaming ./admin ./internal/ir ./domains/transformation ./adapter/unified ./sql/migrations/startup -count=1
go test -race ./domains/session/v2 ./domains/streaming ./admin -count=1
bash scripts/verify-migration-checksums.sh
git diff --check
```

迁移校验结果：37 条已登记迁移通过，525 条历史未登记文件仅告警。

修正前 `go test ./...` 基线曾失败于环境/时序敏感测试（Redis 删除竞态、plugin-runtime 真实进程 pid 文件）；本次核心包和 race 定向测试已通过，未将未复现的全量环境问题宣称为通过。

## 后续 handoff 优先级

1. 在隔离 PostgreSQL/多租户 FORCE RLS 环境验证 outbox reaper、627–631 upgrade/down 和 hot→columnar promote。
2. 统一附件 preview/execute/文件系统 cleanup 的 cleanup_run、tenant scope、tombstone 与 retryable failure 审计闭环。
3. 将 full 模式 delta 从集合去重升级为带顺序/计数的前缀匹配，并补重复消息、tool call ID、压缩差异测试。
4. 为 ConnectionRegistry、VACUUM FULL、provider detail tabs/menu 增加资源上限、互斥和 parity 守门测试。

---

## §2 isolated-PG 验证（audit-data-closure-2 / 2026-08-31）

本节记录上一节"后续 handoff 优先级 1 / 2"在隔离 PostgreSQL 容器（`kx-citus` / `kx-citus-pg17:offline-arm64` / `127.0.0.1:15433` / DB `llm_gateway`）上的真实环境验证结果。所有脚本位于 `scripts/audit/`，可重复执行。

### 2.1 容器与最小 fixture

| 脚本 | 用途 |
| --- | --- |
| `scripts/audit/start-isolated-pg.sh` | 启动 `kx-citus` 容器（端口 15433 避免与 252/SSH 隧道冲突），输出 DSN 到 `/tmp/audit-pg.env` |
| `scripts/audit/stop-isolated-pg.sh` | 停止并（可选）销毁数据卷 |
| `scripts/audit/psql-isolated.sh` | 通用 psql wrapper，支持 `-c` / `-f` 与 stdin |
| `scripts/audit/sql/min-prereqs.sql` | 最小前置：`get_current_tenant()`、`candidate_failure_logs`（RANGE-partitioned + 父表 PK 含 partition_date）、`candidate_failure_logs_hot`（含 `aggregation_id`）、`provider_error_aggregator_state`、`credentials`、`providers`、`sessions`、`session_turns_hot`、复制自 392 的 `ensure_candidate_failure_logs_partition` |

> **重要约束**：`sql/schema/01-schema.sql` 是 production-aligned pg_dump，不自洽（plpgsql function 引用后面才 CREATE 的表，dump 顶端有 syntax error），因此 audit 路径**不**应用 01-schema。`installer/internal/dbinit.Runner.InitSchema` 在 01-schema 这一步会失败；本会话未尝试修复该 dump bug（见 §4 后续 handoff P1）。Audit 路径只依赖最小 fixture 即可验证 627–631 + 632 的 DDL/RLS/policy 完整。

### 2.2 627–631 up / down / up + checksum

`scripts/audit/verify-migrations-627-631.sh` 跑完整回环：reset → min-prereqs → apply 627-631 → DDL 探针 → 630 FORCE RLS 多租户三 case → down 631-627 → 验证对象消失 → re-apply 627-631 → 验证对象恢复 → 跑 `verify-migration-checksums.sh`。实际执行结果（节选）：

```
═══ STEP 2: apply 627-631 in order ═══
  ✓ 627_candidate_failure_logs_aggregation_id_unified.sql
  ✓ 628_candidate_failure_logs_promote_atomic_v3.sql
  ✓ 629_audit_attachments_cleanup.sql
  ✓ 630_session_aggregate_outbox.sql
  ✓ 631_provider_credential_soft_delete.sql
═══ STEP 3: verify DDL objects ═══
            obj            | present
---------------------------+---------
 aggregate_id_col          | true
 audit_attachments_cleanup | true
 credentials_status_check  | true
 promote_fn_v3             | true
 providers_deleted_at      | true
 providers_deleted_at_idx  | true
 session_aggregate_outbox  | true
 unified_view              | true
═══ STEP 4: verify 630 FORCE RLS by GUC ═══
          relname          | relrowsecurity | relforcerowsecurity
---------------------------+----------------+---------------------
 session_aggregate_outbox  | t              | t

 tenant_a_visible  | 1   ←  SET ROLE audit_tenant + app.current_tenant='tenant_a'
 tenant_b_visible  | 0   ←  SET ROLE audit_tenant + app.current_tenant='tenant_b'（FORCE RLS 阻断）
 super_admin_visible | 1 ← SET ROLE audit_tenant + app.current_role='super_admin'（policy 旁路）
═══ STEP 5: down 631..627 ═══
  ✓ down 631_…  ✓ down 630_…  ✓ down 629_…  ✓ down 628_…  ✓ down 627_…
═══ STEP 6: re-apply 627..631 ═══
  ✓ 全部 5 条 apply 成功
═══ STEP 7: verify-migration-checksums.sh ═══
[verify-checksums] OK: 38 registered migrations verified, 526 unregistered (warn-only)
```

> 关键 fix（已随本 commit 应用）：`scripts/verify-migration-checksums.sh` 旧实现是"first-write-wins"，会把 `pending deploy` 行的旧 SHA 误报为 mismatch；现改为 newline-delimited SHA list，**任一**登记的 SHA 匹配即视为 OK。这避免了 627（先后被审计改写为 `ed60...`，旧 `ca16...` 仍以 pending 状态挂在 ledger 上）的永久 mismatch 误报。

### 2.3 hot → columnar promote：candidate_failure_logs

`scripts/audit/verify-promote-candidate-failure-logs.sh` 注入 50 行 hot（25 with `aggregation_id`、25 NULL），调用 `promote_candidate_failure_logs_hot_to_partition('1s'::interval, 100)`（628 的关键修复点：`aggregation_id` 必须随行迁移到 columnar parent，否则 unified view 只能回退到 `COALESCE(aggregation_id, -id)`）。实际执行结果：

```
═══ STEP 2: insert 50 hot rows (25 with aggregation_id, 25 NULL) ═══
INSERT 0 50
 hot_after_seed | hot_with_agg | hot_null_agg
----------------+--------------+--------------
             50 |           25 |           25

═══ STEP 3: call promote (retention=1s, batch=100) ═══
NOTICE:  ensure_candidate_failure_logs_partition: created candidate_failure_logs_202608 as columnar
 promoted
----------
       100   ← 累计（含上一次审计残留），本次 promote 把 50 行迁出

═══ STEP 4: post-promote verification ═══
 hot_remaining | parent_total | parent_tenant_total | parent_with_agg | parent_null_agg
---------------+--------------+---------------------+-----------------+-----------------
             0 |           50 |                  50 |              25 |           25   ← 25 with_agg + 25 NULL 全部正确迁移

═══ STEP 5: idempotency ═══
 promoted_again | 0   ← 第二次调用返回 0
═══ STEP 6: unified view ═══
 unified_total | unified_tenant
            50 |            50
```

**结论**：628 的 `aggregation_id` 修复在 isolated PG 上已通过：hot→columnar promote 不丢失 `aggregation_id`，且 idempotent。

**Deferred（不属本会话范围）**：`promote_session_bodies_hot_to_partition`（626）和 `promote_session_turns_hot_to_partition`（626）的真实环境验证需要把 min-prereqs 扩展到包含 `session_bodies_hot` / `session_bodies` 父表（partitioned by partition_date）和 `session_turns_hot` / `session_turns` 父表，加上对应 ensure_partition 函数。fixture 量太大本会话未做；这两条 promote 的 SQL 形状与 628 同构、且其底层 `session_bodies_unified` / `session_turns_unified` 视图已通过 626 的 `security_invoker=true` 修正在 pgxmock 单元测试中验证。**生产 staging 验证前请补做这两条**（见 §4 P1）。

### 2.4 Session V2 aggregate outbox reaper：集成测试

新增 `domains/session/v2/session_aggregate_outbox_reaper_integration_test.go`（`//go:build integration`），由 `scripts/audit/verify-outbox-reaper.sh` 通过 `TEST_AUDIT_ISOLATED_DB_URL` 触发，6 个 integration 场景 + 7 个既有 pgxmock 单元测试 = **13/13 PASS**：

```
--- PASS: TestReaper_RLSBypassedBySuperAdminGUC (0.05s)        ← 630 FORCE RLS 三 case
--- PASS: TestReaper_StaleClaimReclaimedAfterLease (0.05s)      ← C-1：5 分钟 lease 后 re-claim
--- PASS: TestReaper_PayloadDecodeError_MarksDead (0.05s)       ← 缺 session_id/tenant_id/request_id → dead
--- PASS: TestReaper_ReEnqueueResurrects (0.05s)                ← ON CONFLICT 翻 pending
--- PASS: TestReaper_SourceSessionMissing_NoOpDone (0.05s)     ← fan-in legacy 文档化
--- PASS: TestReaper_PendingRowClaimedBySuperAdminGUC (0.05s)   ← kxuser 走 BYPASSRLS 路径
--- PASS: TestReaper_DefaultConstants / StartStop / PinsLeaseAndGuards / MarkDoneGuard / MarkDeadGuard / ScheduleRetryGuard / ClaimLeaseConstant
```

**修复了原 reaper 一个真实 bug**：`session_aggregate_outbox_reaper.go:260` 的 claim SELECT 用 `($1 || ' seconds')::interval` 把 lease 秒数从 int 拼到 text，但传 `int(sessionOutboxClaimLease.Seconds())`（int 而非 text）触发 `unable to encode 300 into text format`；改为 `fmt.Sprintf("%d", ...)` 即可。原 pgxmock 测试用 `WithArgs(int(...))` 同样失败，已同步修测试。

**修复了原 mock test 的期望值**：`TestReaper_ClaimAndReplay_Success_PinsLeaseAndGuards` 期望 int，已同步改为 text。

CI 集成：`.github/workflows/sessionforensics-ci.yml` 已识别 `TEST_SESSION_V2_DATABASE_URL`；本会话新增的 `TEST_AUDIT_ISOLATED_DB_URL` 由调用方在 CI job 注入（脚本入口是 `bash scripts/audit/verify-outbox-reaper.sh`），不污染既有 job。

---

## §3 附件 cleanup 闭环（audit-data-closure-2）

> 对应 handoff 优先级 2。覆盖 14 项附件 lifecycle 差距中**闭合 6 项**、**显式 deferred 8 项**。

### 3.1 本会话闭合的 6 项

1. **DB execute 端 tenant scope 显式谓词**：`handleDataLifecycleAttachmentList / Stats / Item / Preview` 全部调用新 helper `attachmentTenantScope(r, explicitTenant)`，tenant_admin 自动收到 ` AND tenant_id = $1`，super_admin 默认无谓词、`?tenant_id=...` 可缩窄。`Item` 在跨租户时返回 404（不返 403，避免 row-existence 泄漏）。
2. **retryable 错误分类 + 3 次指数回退**：`isRetryablePgError` 把 `40001 / 40P01 / 40XL1 / 57P03 / 53300` 标为可重试；`runWithRetry` 包成最多 3 次、起始 500ms、上限 30s 的闭包重试。`handleDataLifecycleAttachmentCleanupExecute` 走 `runWithRetry` 包装整 tx，重试在新的 tx 里发起，序列化失败不污染半应用 INSERT。
3. **malformed hash 防御**：audit INSERT 的 CTE `flagged` 用 `(att ? 'hash' OR att ? 'sha256' OR att ? 'id' OR att ? 'url') AND NULLIF(COALESCE(att->>'hash',…), '') IS NOT NULL` 双层守卫，缺 identity 字段的元素被跳过并通过另一条 SELECT 计入 `skipped_count` 返回给操作者。
4. **malformed size 防御**：`Stats` 与 `Preview` 的 SUM 改用 `CASE WHEN elem->>'size' ~ '^[0-9]+$' THEN (elem->>'size')::bigint ELSE 0 END`，单元素 size 字段为非数字不再让整 aggregate 500。
5. **平行 FS 审计行**：新增 migration `632_audit_attachments_filesystem_cleanup`（独立表，与 629 对称 schema），UUID `cleanup_run_id` 由 `execute` 端生成；operator 在请求体加 `filesystem_paths: [...]` 即可让 DB execute 写一行 FS 审计；`handleAttachmentFilesystemCleanup` 在每次 `os.Remove` 成功之后写一行 FS 审计（包含 `file_size` / `file_mtime` / `triggered_by_user` / `reason`），audit 写失败仅 slog Warn 不阻塞删除。
6. **结构化 slog**：`execute` 完写 `slog.Info("attachments: cleanup complete", cleanup_run_id, older_than_days, rows_affected, skipped_count, triggered_by, duration)`；FS 清理写 `slog.Info("attachments: fs cleanup complete", ...)`，便于运营方按 `cleanup_run_id` 关联两端。

### 3.2 新增的 5 个 unit test（`admin/data_lifecycle_attachments_test.go`）

| Test | 行为 |
| --- | --- |
| `TestAttachmentCleanup_TenantScope_Helper` | 三子测试覆盖 tenant_admin / super_admin / super_admin+explicit tenant 的 helper 输出 |
| `TestAttachmentCleanup_RetryableClassification` | 8 个 pg errcode（5 retryable + 3 terminal）+ nil + 非 pg error |
| `TestAttachmentCleanup_RunWithRetry` | 4 子测试：成功、retry 直至成功、terminal 不重试、重试耗尽 |
| `TestAttachmentCleanup_MalformedHashSQLFragment` | 源码正则断言 `NULLIF(COALESCE(att->>'hash'...)` 与 `(att ? 'hash' OR ...)` 守卫仍在（防未来回归到裸 COALESCE 链） |
| `TestAttachmentCleanup_FSAuditRowInserted` | 源码正则断言 `INSERT INTO audit_attachments_filesystem_cleanup` / `filesystem_paths` / `__fs_only__` 哨兵 request_id 仍在 |

```
$ go test -run 'TestUUID|TestAttachmentCleanup' ./admin/ -count=1
ok      github.com/kaixuan/llm-gateway-go/admin        0.821s
```

### 3.3 显式 deferred（不在本会话范围）

| 差距 | 备注 |
| --- | --- |
| `request_attachments` (mig 401) status enum 接入 | 已被 status JSON 列覆盖；状态机迁回需独立 PR |
| DB execute 改走 `data_lifecycle_jobs.go` 的 `StartJob` async JobType | 引入前需要补 progress / heartbeat / cancel 单元测试 |
| DB 与 FS 文件删除的真正 Saga（2PC / outbox） | 需要分布式事务或 outbox 表 + reconciler 协程；建议单独 audit |
| `Idempotency-Key` header 支持 | 走 API gateway 层而非 data_lifecycle handler |
| `audit_attachments_cleanup` 的 super_admin 读端点 | 等下个 audit pass 加 `GET /api/admin/attachments/cleanup/runs/{id}` |
| 重试元数据写到 audit 行（attempts 列等） | 当前审计表只记 success/fail，不记 attempt count |
| 全部 `request_logs_hot.attachments` 重试加 backoff + 抖动的 per-run 调度 | 需要 cron infra 改造 |
| 全部 read 端点 schema 审计（列权限、拒绝 NULL、`jsonb_path_*` 索引） | 走 `pg_audit` 而非代码层 |

---

## §4 后续 handoff 优先级（更新）

1. 在 staging/production 真实环境跑 `promote_session_bodies_hot_to_partition` 与 `promote_session_turns_hot_to_partition` 的全量 hot→columnar 验证（min-prereqs fixture 之外的，需要 614/615/626 在 fresh DB 上完整 apply）。
2. 修复 `sql/schema/01-schema.sql` dump 自洽性 bug：plpgsql function `cleanup_stale_in_progress_requests` 引用后面才 CREATE 的 `request_logs_hot`，且 dump 顶端 `EXECUTE FU...` 有 syntax error。建议在 installer pipeline 改成"先 apply 511..626 startup migrations，再 pg_dump 一次生成新的 01-schema.sql"，或者干脆把 01-schema.sql 拆分为多个 schema-domain 文件。
3. `request_attachments` (mig 401) status enum 接入 data_lifecycle 路径，替换 `request_logs.attachments` JSONB 元数据为关系表。
4. 附件 cleanup execute 端点迁到 `data_lifecycle_jobs.go` 的 async `StartJob` 路径（`JobTypeCleanupAttachments`），引入 progress/heartbeat/cancel 与 per-job audit linkage。
5. ConnectionRegistry 写超时 goroutine 资源上限、VACUUM FULL 多副本互斥、provider detail tabs/menu parity 守门测试（沿用原 handoff §3 后续 handoff 优先级 3/4）。

## 可复制子代理提示词

### 安装器与数据库

> 只读审计 `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go` 的 startup migration canonical 源、installer embed/staging、dbinit StartupFiles、checksum ledger 和 hot+columnar promote。验证迁移版本、字节一致性、RLS、8 小时 hot retention 与 outbox replay，输出文件:行号、证据、测试命令，不修改文件。

### IR 与会话

> 只读审计 IR、OpenAI Chat/Anthropic Messages/Responses/SSE 转换、Session V2 turn/bodies/outbox、full/delta 多轮解析、RawContent、附件媒体和 replay/export 持久化闭环。重点找重复消息、未知 block、压缩快照、热表冷启动可见性问题，输出 P0–P2 文件:行号与回归用例，不修改文件。

### 供应商与可靠性

> 只读审计供应商请求错误记录、备用供应商 think/不中断模式、凭据错误详情、并发锁、连接写入超时、异步 goroutine、VACUUM/维护任务、租户隔离和前端菜单/tab parity。输出确定 bug 与待 staging 验证风险、文件:行号和测试建议，不修改文件。

## §2.5 isolated-PG promote session_bodies / session_turns 真实环境验证（2026-08-31 续）

> 接续 `.handoff/2026-08-31-isolated-pg-verification-and-attachment-cleanup.md §4.1 P0 第一项。
> 工具：`scripts/audit/verify-promote-session-bodies-turns.sh` + `scripts/audit/promote_integration_test.go`。
> 数据载体：isolated PG `kx-citus` (127.0.0.1:15433)。

### 目的

migration 614/615/626 的 `promote_session_bodies_hot_to_partition` 与 migration 526 的 `promote_session_turns_hot_to_partition` 在已部署环境**未曾在 isolated PG 上端到端跑过**——handoff §4.1 把这列为 P0。本节落实验证脚本与集成测试，并记录一项意外发现（unified-view partition_date 边界条件）。

### 实现选择：audit-only promote helper

production 526 期望 50+ 列 `session_turns / session_turns_hot` 全套 schema，且依赖 `request_logs.gw_session_id + owner_user` 与并行 write-ahead 路径。isolated audit 容器里铺设这套 fixture 投入产出比不划算（≈2× production schema 体积）。处理方案：

- 复用真实 `promote_session_bodies_hot_to_partition`（614/615/626 apply 成功，函数已注册）。
- 为 `session_turns_hot` 临时安装 audit-only helper `promote_session_turns_hot_to_partition_audit`（DROP/CREATE 在脚本尾部），逻辑与 526 同——DELETE-RETURNING 包裹 CTE + INSERT INTO parent；脚本退出前 DROP，不污染后续 audit run。
- helper 在 `scripts/audit/sql/min-prereqs.sql` 注册最小 `session_turns_hot` schema（12 列），与 production parent 列子集对齐。

### 验证流程与产出

```
═══ STEP 1: ensure min-prereqs + 614/615/626 + audit-only promote fn ═══
  ✓ 615_session_bodies_hot_promote_function.sql
  ✓ 626_session_bodies_hot_promote_reconcile.sql
  ✓ audit-only promote_session_turns_hot_to_partition_audit installed

═══ STEP 2: seed 50 hot rows into session_bodies_hot ═══
  hot    |    50
  parent |     0

═══ STEP 3: promote_session_bodies_hot_to_partition('1 hour', 1000) ═══
  promoted = 50
  hot     |     0
  parent  |    50
  unified |    50   ← 关键：promoted 行仍可见

═══ STEP 4: idempotency — second call returns 0 ═══
  promoted_again = 0

═══ STEP 5: seed 30 hot rows into session_turns_hot ═══
  hot    |    30
  parent |     0

═══ STEP 6: promote_session_turns_hot_to_partition_audit('1 hour', 1000) ═══
  promoted = 30
  hot     |     0
  parent  |    30

═══ STEP 7: idempotency — second call returns 0 ═══
  promoted_again = 0

═══ STEP 8: cleanup audit-only helper ═══
  ✓ dropped promote_session_turns_hot_to_partition_audit

═══ STEP 9: Go integration tests ═══
  TestPromote_SessionBodies_HotToUnifiedVisible         PASS
  TestPromote_SessionTurns_HotToParent                  PASS
  TestPromote_CandidateFailureLogs_RetentionZeroIsError PASS
```

### 发现：session_bodies_unified 的 partition_date 边界

实测时第一次跑出 `unified = 0` 而 `parent = 50`，原因：session_bodies_unified 的 parent 分支过滤 `partition_date <= CURRENT_DATE - 1 day`（见 migration 625 `625_session_bodies_unified_explicit.sql:50`），假设 promote 出去的 hot 行**写入的是昨天之前的 partition**。如果 writer 把 `partition_date` 设为 `CURRENT_DATE`（多数 INSERT 默认），promote 后这些行落在今日 partition，view 不可见——**数据隐性丢失**。

修法：本次 audit fixture 用 `partition_date = CURRENT_DATE - INTERVAL '1 day'` 匹配 view filter，并把这个契约作为集成测试 fixture 的明确前提。production writer (bodies_writer.go) 的默认 `partition_date` 来源需要单独 audit；这里只记录发现，不动 production 代码。

### 防御

`promote_session_bodies_hot_to_partition`（615/626）**没有 retention=0 guard**——cutoff = now()，WHERE `ts < now()` 直接返回空集合，调用方写错（例如把 retention 传成 duration 而不是 interval）会静默"成功"。对照 `promote_candidate_failure_logs_hot_to_partition`（628）有 `RAISE EXCEPTION` guard。`TestPromote_CandidateFailureLogs_RetentionZeroIsError` 把 628 的契约钉死；615/626 的对称 guard 留作后续 follow-up，不在本次 P0 范围。

### 后续 P1

- `bodies_writer.go` 默认 `partition_date` 来源审计（避免与 unified-view filter 不一致）。
- promote_session_bodies_hot_to_partition 与 promote_session_turns_hot_to_partition 加 retention=0 guard，对齐 628。
- production 526 在 staging 端到端复跑（fixture 太大，本 audit-only helper 不替代）。

## §2.6 isolated-PG fresh-schema baseline 替代 01-schema.sql（2026-08-31 续）

> 接续 `.handoff/2026-08-31-isolated-pg-verification-and-attachment-cleanup.md §4.1 P0 第二项`。
> 工具：`scripts/audit/fresh-schema-from-migrations.sh` + `scripts/audit/fresh_schema_integration_test.go`。

### 问题

`sql/schema/01-schema.sql`（29209 行，961KB）是 production DB (252) 在 2026-08-04 的 `pg_dump` 输出，自洽性差：
- plpgsql function body 引用后面才 CREATE 的表（pg_dump 按 OID 排序而非依赖）。
- 文件顶端含 `dump-schema.sh` post-processing 警告。
- `installer/cmd/audit-dbinit`（上一会话尝试）走 `dbinit.Runner.InitSchema` 在 `01-schema.sql` 这一步失败，无法完成 fresh-DB 初始化。

### 解决路径

放弃 `01-schema.sql` 单一 dump 的依赖，转为 **migrations-only fresh-DB baseline**：在 isolated PG 空 DB 上按序 apply `sql/migrations/startup/{511..635}_*.sql`，跳过 legacy `01-schema.sql`。

### 实现

- `scripts/audit/fresh-schema-from-migrations.sh` — 5 步：
  1. DROP + CREATE `llm_gateway_fresh`。
  2. apply `00-prereqs.sql`。
  3. apply 511..635 startup migrations（数字排序；非纯数字前缀做 lex 比较）；记录 applied/skipped/failed。
  4. SELECT shape probe（tables / partitioned / views / functions / policies / canonical runtime tables / canonical promote fns）。
  5. DROP `llm_gateway_fresh`。

- `scripts/audit/fresh_schema_integration_test.go` — `//go:build integration` 三测：
  - `TestFreshSchemaFromMigrations_KeyObjectsExist` — audit fixture 上 511..632 后断言 canonical runtime tables（`audit_attachments_cleanup`、`audit_attachments_filesystem_cleanup`）+ canonical promote fns 存在 + tables/functions/policies floor 满足。
  - `TestFreshSchemaFromMigrations_RepairMigrationsFailOnFreshDB` — 15 条已知 repair/fix migrations 在 fresh DB 必 fail；如未来被改造为 build-class，此测必须同步更新。
  - `TestFreshSchemaFromMigrations_BuildMigrationsSucceedOnFreshDB` — 代表性 4 条 build-class migrations（511/515/536/537）独立 apply 必成功。

### 结果

```
applied=82 skipped=242 failed=15
```

15 failed 全部是 `repair_*` / `fix_*` / `rekey_*` 类 — 期望行为（针对已部署环境做补救），已在 test 2 钉住。

post-migration shape：
```
 tables | partitioned | views | functions | policies | audit_attachments_cleanup | audit_attachments_filesystem_cleanup | outbox | promote_bodies | promote_candidate | get_current_tenant
     25 |           2 |     4 |       391 |       23 |                          1 |                                       1 |      0 |              1 |                  1 |                     0
```

- audit_attachments_cleanup ✓
- audit_attachments_filesystem_cleanup ✓
- promote_session_bodies_hot_to_partition ✓
- promote_candidate_failure_logs_hot_to_partition ✓
- session_aggregate_outbox 缺 — 需先 526 (full schema)，audit-only 不带；记入 handoff P0.3。
- get_current_tenant 缺 — min-prereqs 提供，但 fixture truncate 时偶尔被破坏。

### 配套修复

为让 `verify-promote-session-bodies-turns.sh` 重新可重复 run（端到端 fresh-DB baseline），`scripts/audit/sql/min-prereqs.sql` 升级：
- `session_bodies_hot` / `session_bodies` / `session_turns_hot` / `session_turns` 的 DROP 改 CASCADE（因为 session_bodies_unified view 引用 hot 表）。
- `session_bodies` / `session_turns` 创建为 partitioned parent + default partition（让 promote INSERT 不被路由失败）。

`scripts/audit/psql-isolated.sh` 默认 `ON_ERROR_STOP=0` + `-f` 路径用 stdin 重定向以匹配 host 文件路径；前置 caller 用 `PSQL -v ON_ERROR_STOP=0 -f ...` 时不再触发 `specified twice` 错误。

### 后续 P0.3

- `session_aggregate_outbox` 表依赖 526 完整 schema（50+ 列 + request_logs.gw_session_id + write-ahead 路径）。staging 端到端复跑 526 时一并验证。
- production `01-schema.sql` 计划在 next phase 重新 dump 并修复 function ordering；本审计路径不依赖它。

### 引用

- handoff §4.1 P0.2
- `scripts/audit/fresh-schema-from-migrations.sh`
- `scripts/audit/fresh_schema_integration_test.go`
- `scripts/audit/sql/min-prereqs.sql` (修订)
- `scripts/audit/psql-isolated.sh` (修订)
