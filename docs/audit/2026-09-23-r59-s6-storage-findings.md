# R59 S6 组审计发现——双存储 D06 + hot/分区存储 D07

- 审计员：S6（存储组）
- 日期：2026-09-23
- 基点：main @ 9f7b0ea3f
- 范围：48h DB 域（boot 烧点家族八修 / statement_timeout 钉住 / FlushToPG 延迟两修 / 迁移 742 / D06 / D07 / t0 债）
- 性质：只读审计（可跑测试）；必跑项已执行（见 §七）

---

## 〇、发现总表

| # | 严重度 | 发现 | 状态 |
|---|--------|------|------|
| S6-1 | P2 | boot 链 POLICY 家族无守卫：每 boot 无条件 DROP POLICY+CREATE POLICY（ACCESS EXCLUSIVE），含自注释"hot path"的表 | 开放 |
| S6-2 | P2 | t0_arrived_at 99.5% NULL 根因定位：NewRequestLogContext 不落 T0，仅 dispatch 队列路径传播 | 开放（修法已给） |
| S6-3 | P3 | `lifecycle.promote_interval_hours` 设置零消费者（死旋钮）；SetPromoteInterval 生产接线缺失 | 开放 |
| S6-4 | P3 | hosted_task_events（742 扩的表）无 TTL/轮转，无本轮新裁决记录（R29 已记 P3，债务延续） | 开放（已知债） |
| S6-5 | P3 | ensureRouteIncidentPendingState 每 boot 无条件 DROP CONSTRAINT+ADD + DROP INDEX+CREATE UNIQUE INDEX（非 CONCURRENTLY）；"小表"注记会随事件累积腐烂 | 开放（有设计注记） |
| S6-6 | P3 | ensurePartitionAutovacuumSchema 每 boot 对全部 *_hot + 11 家族分区执行 ALTER TABLE SET（SHARE UPDATE EXCLUSIVE，不阻 DML，但每 boot 锁队列参与者） | 开放（低危） |
| S6-7 | Info | statement_timeout 钉住为 best-effort（SET 失败静默）；session_summaries 归档段 SET ... = DEFAULT 会把钉住的 5min 回落为角色 30s（下一次 acquire 自愈） | 记录 |
| S6-8 | Info | applyMigrationsOnce 3min 预算与钉住 5min 的组合语义未对齐文档（5min 单条超时 + 3min 总预算 = 长锁等待仍可烧穿总预算，只是从 30s/条 放宽） | 记录 |

必审项通过项（✅）：八修模式一致（§一）；钉住时机/影响面正确（§二）；FlushToPG 两修机制与重试链验证（§三）；70e47c4f4 守卫与 764a95330 恢复脚本（§四）；742 四件套齐全（§五）；D07 轮转 20/20 覆盖 + 8h 保留 + hot-only 写路径（§六）；D06 sqlite 路径近 48h 无破坏（§六.4）。

---

## 一、boot 烧点家族八修一致性（最高优先级项）

### 1.1 八修逐一核对 ✅

全部落在 `db/db.go`，模式两族：

**A 族——Go 侧目录短路（探针返回 0 = 零 DDL 直接 return；任一缺失/探针出错回落原 ensure）：**

| 修 | commit | 函数（现行号） | 守卫清单 |
|---|--------|----------------|----------|
| 1 | 7b5627e6e | ensureRequestLogSchema (db.go:1305, 守卫 1317-1363) | 31 父列 + 11 hot 列 + 13 索引；清单与 ensure 体逐项对齐（本审计抽取 body 索引名 = 13 个，与守卫名单逐一相符） |
| 2 | 5e1ae4a3d | columnsAllPresent 通用 helper (db.go:188) + ensureCredentialBalanceFloor (1662) + ensureCredentialPlanQuotaProbeBackoff (1726) | 8 列 / 1 列；幂等 701/704 stamp 照常执行 |
| 3 | 1bc6d7e12 | ensureAutoRouteSelectionsHotSchema (db.go:6869, arsGuardSQL 6891) | 44 列 + 4 索引 + 3 约束 + 视图；索引臂兼当新环境安全网（表不存在时索引必缺 → 必回落） |
| 4 | 9ad0a7ce1 | ensureRoutingAnalyticsColumns (db.go:2405) | request_logs_hot + request_logs 各 3 列 |
| 5 | 71eac22d8 | ensureCredentialColumns (4532) / ensureFpSlotLimit (4625) / ensureConcurrencyMode (4715) | 列+表+索引联合探针；backfill IS NULL 谓词空集论证成立 |
| 7 | 4e7175f4d | ensureCredentialGovernorRevision (4779, governorGuardSQL) | revision 列 + 序列 + 2 函数 + 4 触发器计数 |
| 8 | 0a5ad6b20 | ensureTuningSignalsViews (3352, matview×2+4 索引) + ensureUnavailableRecoverAtSchema (5018, 列+索引) | 两处独立守卫 |

**B 族——SQL 侧 conname/definition 守卫（DO 块内 IF NOT EXISTS(pg_constraint)，存在即整段跳过）：**

| 修 | commit | 位置 |
|---|--------|------|
| 6 | 79eee4582 | ensureNodeProbeTriggerKindSchema (db.go:4112)，node_probe_runs_trigger_kind_check + credential_probe_queue_source_check 两约束补 conname 守卫；route_incidents/ursm/approval_queue 同型 DROP+ADD 判小表不动（源码有注记，见 S6-5） |

**一致性结论**：八修实现模式一致（A 族七修 + B 族一修），语义统一为"全在位 → 零 DDL 零锁；任一缺失/探针出错 → 回落原幂等 ensure"；守卫清单与 DDL 清单抽核对齐；均有审计轮注记与实证序号。与更早的 beda1cd89（session_summaries，lock_timeout 2s+分片快失败+CONCURRENTLY 的缓解式修法，非短路式）及 1560/631 先例（pg_constraint 定义守卫）构成三代同族，模式演进有注释链可循，无互相矛盾。

**与 ef2b43156 钉住的关系**：八修时间上穿插钉住提交（08:19），属"守卫管能不能不跑、钉管跑时别被杀"的双保险，无重复或冲突。

### 1.2 反查缺口：未守卫 boot 链 DDL（下次烧点候选清单）

方法：脚本化解析 db/db.go（及 db/*.go 辅助文件）全部 boot 链 ensure 函数（applyMigrationsOnce 调用序 70 余个），按 Go 侧守卫关键词 + SQL 侧 pg_constraint 守卫双通道分类；42 个含 DDL 无守卫函数逐一抽取目标表与高危模式（无条件 DROP+ADD / DROP VIEW+CREATE / DROP TRIGGER+CREATE / DROP POLICY+CREATE POLICY / 无条件 UPDATE）后分级。已人工排除伪缺口：ensureOmniFreeSchema（credentials ALTER 有 information_schema 列守卫）、ensureRequestLogsCurrentMonthView（视图健康即零 DDL 早退）、ensureApiKeyAutoProfileIdentity（pg_index 探针 + lock_timeout 2s）、ensureSessionSummariesArchivalSchema（beda1cd89 缓解式）、constraint DO 块族（均有 conname+definition 守卫）。

**Tier 1（热表 / 每boot无条件 ACCESS EXCLUSIVE——下一批烧点候选）：**

1. **DROP POLICY + CREATE POLICY 家族（26 处，全部无守卫，每 boot 重跑）**。DROP/CREATE POLICY 取表上 ACCESS EXCLUSIVE：
   - `request_journey_observation_outbox`（db.go:1198-1206，每请求 outbox 写入表）×2 policy
   - `journal_snapshot_receipts`（db.go:1284-1290，**同文件上方注释自述 "receipts are written on the hot path of the shared DB"**）×2
   - `users`（db.go:2866，ensureWorkTypeRouteCoverage 内）
   - `analysis_events` + `intent_aggregates`（ensureAnalysisEventsRLS db.go:4412）
   - `response_format_anomalies`(3607) / `model_integrity_events`(3692) / `tenant_model_policies(_audit)`(4249) / ensureSupplementalRLS 六表 `tenant_settings_kv, settings_audit, tenant_tool_policies, tool_call_events, tool_registry, tool_usage_stats`(4355+) / `credential_keys`(ensureCredentialKeysSchema 内)
   - 同函数内的 TRIGGER DROP+CREATE 同病：credential_keys ×2、routing_overrides_audit、webcookie_sessions、orchestration_runtime_instances、tenant_model_policies、route_incident_events
   - 建议：比照 fix 6 的 conname 守卫——policy 存在且 pg_get_policydef 一致则跳过；或函数级探针早退。
2. **ensureProxyManagementCanonicalSchema**（db.go:6572，88 行 DDL 无守卫）：proxy_subscriptions 每 boot 无条件 UPDATE 全行回填 + 5×ALTER COLUMN SET NOT NULL（ACCESS EXCLUSIVE + 全表校验扫描）+ providers/proxy_nodes 的 FK DROP/ADD DO 块。表尚小，但属每 boot 固定锁队列参与者。
3. **ensureCredentialKeysSchema 的 2×DROP TRIGGER+CREATE TRIGGER**（credential_keys 高频 UPDATE 触发器重建，每次排 credentials 子表锁队列）。

**Tier 2（中低热表 ADD COLUMN IF NOT EXISTS 族，成熟库逐列 no-op 仍取锁，与八修同病但表小）：**
ensureSessionTitleStates(3289)、ensureTenantModelPoliciesSchema、ensureLicenseModulesSchema、ensureVibeCodingSchema、ensureFaultManagementSchema、ensureAutoUpdateSchema、ensureCenterOpsSchema(30 条)、ensureRouteIncidentPhase2Schema(26 条)、ensureDistributionSchema、ensureUrsmKeyMigrationLedgerSchema、ensureApprovalResumeClaimSchema、ensureWebCookieSessionsSchema、ensureCredentialKeysSchema、ensureQualityFixModeSchema、ensureOrchestrationRuntimeInstancesSchema、ensureResponseFormatAnomaliesSchema、ensureModelIntegrityEventsSchema、ensureModelIQSchema、ensurePassiveProbeStateSchema、ensureFreediscoveryTemplateHealth、ensureRoutingOverridesTable/Audit、ensureSessionTitles、ensureSessionMemoraExtractionLog、ensureTuningSignalsStrategyColumn、ensureTaskTypeCorrections、ensureRoleTaskLLMMapping、ensureProviderModelsCanonicalClearedAt、ensureApplicationsTable、ensureProbeWatchdogIndex（model_probe_state CREATE INDEX IF NOT EXISTS）、ensureAnalysisEventsRLS。四段式 Exec 拆分钉住后单条锁等待上限已放宽到 5min，这类表在晨峰下的累计排队仍可能吃 retry_budget，建议按"表写入速率 × DDL 条数"排序分批补守卫。

**Tier 3（有设计注记/低危，列册备查）：**
- ensureRouteIncidentPendingState（见 S6-5）
- ensurePartitionAutovacuumSchema（见 S6-6）
- ensureApprovalResumeClaimSchema / ensureUrsmKeyMigrationLedgerSchema 的 DROP+ADD：fix 6 提交注记明示"小表毫秒级不动"。

---

## 二、ef2b43156 ApplyMigrations 迁移期 statement_timeout 钉住 ✅（附两条记录项）

机制核对（db/db.go:25-31, 75-95, 149-159）：
- `migrationsPinned` atomic.Bool 挂在 DB；`cfg.BeforeAcquire` 闭包在 pinned 时对每条取出的连接 `SET statement_timeout='5min'`（session 级）。
- `ApplyMigrations` 入口置位、defer 清零 + `pool.Reset()`；`maxAttempts=2` 的两次 attempt 全程在 pin 窗口内（含 ensure 链——applyMigrationsOnce 是 ApplyMigrations 的被调方）。
- 唯一调用方是 `Open`（db.go:111）；`pool.Reset()` 全仓仅此一处；`cfg.BeforeAcquire` 无既有钩子被覆盖。

时机与影响面判定：
- ✅ Reset 发生在 ApplyMigrations 返回前、Open 返回前——serving 未启动、无在途业务连接，被抬连接（含 checkout 中者归还即弃）零回流；角色默认 30s 即时恢复。提交注记的真库验证（迁移期 SHOW=5min / Reset 后回默认）与代码语义一致。
- ✅ 只影响迁移窗口，正常运行期每条连接来自 Reset 后的新拨或 BeforeAcquire pinned=false 分支（零 Exec）。
- ✅ statement_timeout 从语句开始计时、含锁等待，与 57014 实证一致；比 lock_timeout 粗但够用。
- S6-7（Info）：`_, _ = conn.Exec` SET 失败静默放行（连接仍以 30s 交付）——best-effort 可接受，建议至少 slog.Warn。
- S6-7（Info）：session_summaries 归档段（db.go:599 函数内）`SET statement_timeout='5s'` 与 defer `SET ... = DEFAULT` 在 pin 窗口内会把该连接回落到角色 30s 而非 5min；下一次 acquire 时 BeforeAcquire 重钉，自愈，无永久残留。
- S6-8（Info）：单条 5min × applyMigrationsOnce 总预算 3min——重锁等待下仍可能烧穿总预算进入 attempt 2（70e47c4f4 工单实录即此形态），钉住放宽的是"烧点不可推进"而非总预算；两者关系建议写进部署手册。

---

## 三、be192a2ba + 778423af6 FlushToPG 首试延迟两修 ✅

现行重试链（domains/streaming/handler.go:1947-1962）：`[1.2s, 5s, 5s, 5s]` 4 次尝试，每次 flush 独立 2s 超时；最坏 ≈ 16.2s 睡眠 + 8s 超时窗。

- ✅ 两修演进链完整可追溯：delay=0 →（be192a2ba，本地 7601:144 失败比实证）→ 250ms →（778423af6，2238 部署 attempt1 仍失败 66 条/5min 实证）→ 1.2s。理由链两段各自成立：①telemetry 批处理窗 200ms（telemetry/client.go:901 `time.NewTimer(200 * time.Millisecond)` 实锤）——FlushToPG 的 UPDATE `WHERE request_id=$2` 早于 INSERT 批落地即 miss；②api_keys 行锁串行化——rate_limited 探针 INSERT 事务（usage_ledger+request_logs+api_keys 同事务）提交晚于 now() 数百 ms 到秒级，250ms 覆盖不了。
- ✅ 时效影响评估：trace 读路径 Redis-first（internal/trace/trace.go Load），1.2s 只延迟 PG 落底与 Redis key 删除，用户可见时效无损；PG 侧 trace_events 最坏 ~24s（4 试全超时）。
- ✅ 兜底语义：parent 未落库 → ErrTraceParentNotFound + Redis key 保留（TTL 10min，trace.go:205）；终局失败的 key 靠 TTL 兜底，无额外 sweeper——4 试全败（≈24s 后仍无父行）+10min TTL 内父行到位的窗口无人补 flush，属可接受损耗（与既往设计一致，非本轮回归）。
- 注：FlushToPG 的 UPDATE 目标是 `request_logs_hot`（hot-only 写路径，与 D07 契约一致）。

---

## 四、70e47c4f4 URSM 守卫 + 764a95330 pg 崩溃恢复 ✅

- 70e47c4f4（db.go:4905-4990）：列守卫（columnsAllPresent request_logs_hot task_type/origin_stage）+ 函数签名探针（pronargtypes '20 25 23 23'，探错走保守含 DROP 路径）+ CREATE OR REPLACE 始终执行保持函数体同步 + backfill UPDATE 刻意不短路（broken_confirmed 实行时必须回填）+ 单体 Exec 拆四段。四项设计决策均有注释论证，与八修模式同族且更细。deploy-seamless.sh 探针失败分类诊断（listen failed / 迁移烧点 / listening / ensure 推进中四分支）合理。
- 764a95330：`scripts/local-dev/recover-pg-citus-image.sh`（368 行，--dry-run/--tarball、Mounts 自解析、不动 user/password、不 initdb）+ 文档 §5 Q6 + changelog v1.15。根因（错镜像 catalog 残留 citus + sql_drop 事件触发器 dlopen 失败）与修复步骤（load tarball→swap→patch shared_preload_libraries→验证）自洽；ALTER SYSTEM 逗号列表引号坑的记录有复用价值。

---

## 五、742_hosted_task_recalled_event 四件套 ✅

| 件 | 位置 | 状态 |
|----|------|------|
| SQL 主文件 | sql/migrations/startup/742_hosted_task_recalled_event.sql | ✅ |
| down 文件 | 同名 .down.sql（还原 711 白名单 + 清 recalled 行 + ledger 反注册） | ✅ |
| embed map | installer/cmd/llm-gw-installer/main.go:530-531（go:embed var）+ :689（embeddedSQLFiles 条目） | ✅ |
| runner 列表 | installer/internal/dbinit/runner.go:245（含注释） | ✅ |

- ✅ sql/ 与 embeddata/ 两份逐字节一致（diff 验证）；R51 对账门禁（installer/cmd/llm-gw-installer/stats_migrations_test.go 三方同步检查）在本轮 go test ./... 全绿，742 过门禁。
- ✅ SQL 内容与 Go 侧消费一致：白名单新增 'recalled' ↔ domains/hostedtask/types.go:115 `EventRecalled EventType = "recalled"`；up/down 均带 pg_get_constraintdef 验证 DO 块；ledger self-registration 带 schema_migrations 存在性守卫（裸测试库兼容）。
- 注：742 的 DROP+ADD CONSTRAINT 是 ledger 跟踪的一次性迁移（非 boot 链），无每 boot 烧点风险；ADD CONSTRAINT 校验扫描在 hosted_task_events 大表上是一次性成本，可接受。
- 扩展发现见 S6-4：hosted_task_events 本身无轮转（下节）。

---

## 六、D07 hot+分区清单核查 + D06 双存储

### 6.1 轮转覆盖 20/20 ✅

CREATE TABLE 源全量枚举得 20 张 `*_hot` 表；bg/partition_manager.go promoteSpecs（:970-998）20 项一一对应（request_logs/usage_ledger/request_wal/routing_decision_log/credential_model_index/request_logs_bodies/credit_ledger/tool_usage_stats/candidate_failure_logs/session_turns/session_turn_details/session_bodies/handoff_logs/session_module_executions/dashboard_access_events/auto_route_selections/supplier_errors/session_memora/session_censors/session_tools）。唯一例外 model_probe_runs_hot 为**明示裁决**的纯 hot 策略（2026-07-14，promote 退出 + 14 天 TTL DELETE，spec_lifecycle.go:23）。

### 6.2 保留 8h 配置点 ✅

`lifecycle.hot_retention_hours`（settings/spec_lifecycle.go:7，默认 8，1-720，HotReload）+ 三张表独立覆盖（handoff/session_module_executions/dashboard_access_events 各自 8h 默认）+ partition_manager.go:1304-1376 消费链。`lifecycle.promote_interval_hours` 声明默认 1h——与实际相符（DefaultPromoteInterval=1h），但该设置本身零消费者（S6-3）。main.go:4693 的 24h 参数是 partition-create/archive 周期，非 promote 周期，无冲突。

### 6.3 更新/删除只在 hot 抽查 ✅（3 表）

- request_logs 家族：telemetry INSERT→hot；FlushToPG UPDATE→request_logs_hot（trace.go:499）；Go 侧无任何 UPDATE/DELETE 打到 request_logs 父表（唯一命中 bg/lite_retention_worker.go:99 是 sqlite 侧行，属 D06，见 6.4）；hot→partition 迁移由 promote_*_to_partition SQL 函数原子完成。
- auto_route_selections 家族：settle worker UPDATE 仅 auto_route_selections_hot（bg/auto_route_settle_worker.go:557,578）；父表零 UPDATE/DELETE。
- session_turns 家族：PG 侧无 UPDATE/DELETE（bg/lite_retention_worker.go:120 的 DELETE 为 sqlite 侧）。

### 6.4 742 新表轮转归属 → S6-4（P3）

hosted_task_events（711 建表，742 扩约束）是普通 heap append-only 表（task_id 外键 CASCADE），不在 _hot 族、不具分区、不在 promoteSpecs，也无任何 TTL/清理 worker。检索到唯一裁决痕迹是 R29（docs/audit/2026-09-15-r29-24h-audit-round.md#P3-10）"hosted_task_events/dead 行无保留期 TTL"——**已知债延续，本轮未新增裁决**。增长上界 = 任务数 × 每任务事件数，任务本体无清理路径时为慢性无界。建议：给 hosted_tasks 终态行 + 事件定 TTL，或在 D07 清单中正式豁免并留档。

### 6.5 D06 双存储 ✅

- 架构：`LLM_GATEWAY_STORAGE_MODE=lite` → sqlite 三 store + FileBodies/Memory + LiteRetentionWorker（cmd/gateway/storage_mode_init.go；未设置/full 零行为变化）。full = pg+redis+memory+files。
- 近 48h sqlite 路径改动两笔均带测试闭环：R51 P3（03b439798，storage/sqlite/turns_store.go 幂等键改 (tenant_id,request_id) DO UPDATE + turns_store_test.go +72 行）、R52（89a4a9f88，lite 停写门控装配缺口 settings.Init(nil)+storage 族 specs）。未发现破坏 sqlite 路径的改动。
- go build ./... 全绿含 sqlite 驱动（driver_cgo/nocgo 双轨）。

---

## 七、t0_arrived_at NULL 数据质量债——根因定位（S6-2，P2）

154 复审 §四.4 记录了现象（request_logs_hot 99.5% NULL）；本轮定位写入侧缺口：

- **赋值链现状**：T0 唯一源头是 `dispatch.NewQueuedRequest`（queued_request.go:268，构造即 setStage(ReqStageArrived)）→ `StageTimestamps()` → 仅两条上屏通道：`executor_dispatch.go:341/345/350`（dispatch 结果/错误）→ handler.go:4731（失败）/5241（成功）`ApplyQueueTimestamps*`。
- **缺口**：`NewRequestLogContext`（request_log_pipeline.go:373-378）只存 `StartTime` **不落 T0ArrivedAt**。因此：
  1. 所有 dispatch 提交前的拒绝路径（invalid_key / auth_unavailable / budget_exhausted / insufficient_credits / rate_limited / attachment_store_failed…captureAndEmitFailure 族）→ T0 NULL，虽然"到达时刻"就是 StartTime，信息其实一直在手；
  2. 合成行全部 NULL：emitClientDisconnectProbe（handler.go:6644）、bg/active_probe_emitter.go:203、embeddings、405 兜底等；
  3. 探针/拒绝类行在生产占比高（778423af6 工单自证 rate_limited 探针量级），与 99.5% NULL 的观测吻合。
- **修复建议（最小改动）**：`NewRequestLogContext` 里 `T0ArrivedAt: &start`（或 Build() 时以 StartTime 兜底）；dispatch 路径现有 ApplyQueueTimestamps* 覆盖语义不变（队列精确值覆盖到达近似值）；telemetry 侧 upsert 已是 `COALESCE(EXCLUDED.t0_arrived_at, request_logs_hot.t0_arrived_at)`（client.go:1463）非空保留，兼容存量。合成行构造器（buildClientDisconnectProbeEntry / active_probe_emitter）同步补 entry.T0ArrivedAt = 合成时刻。附带收益：154 类审计的按时间统计不再被迫走 bodies ts join。

---

## 八、必跑项记录

| 项 | 结果 |
|----|------|
| `go build ./...` | ✅ exit 0 |
| `go test ./db/...` | ✅ ok（0.731s）——**注意**：依赖真库的契约测试因无 TEST_PG_URL/BOOTSTRAP_TEST_DSN 跳过（-v 可见 ≥6 SKIP：omnifree parity、view round-trip 等），记为跳过而非通过 |
| installer 模块 `go test ./...`（含 dbinit 对账门禁 + stats_migrations_test 三方同步门禁） | ✅ 全 ok（主仓 internal/installer 路径不存在，实际模块根为 installer/go.mod，审计项要求已按实际布局执行） |

---

## 九、建议处置序

1. S6-1 POLICY/TRIGGER 守卫族（P2）——下批 boot 烧点最可能爆点，建议比照 fix 6 conname 守卫模式批量收口，优先 request_journey_observation_outbox / journal_snapshot_receipts / users 三处。
2. S6-2 t0 一行修（P2）——NewRequestLogContext 补 `T0ArrivedAt: &start`，低风险高收益。
3. S6-3 promote_interval_hours 死旋钮（P3）——接线或撤 spec，二选一。
4. S6-4/S6-5/S6-6 随下次触碰对应文件时顺手收口。
