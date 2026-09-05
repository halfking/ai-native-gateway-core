# 轴D（round2）：hot+columnar 分区大数据存储

范围：`bg/partition_manager.go`、`bg/auto_route_settle_worker.go`、`bg/supplier_error_stats_aggregator.go`、`admin/data_lifecycle_hot_partition.go`、V371/655/656/657 迁移、`deploy/sql/verify/supplier_errors_pg_test.go`、`sql/tests/`、installer embeddata。基线：`docs/audit-2026-09-05-eight-closures.md` 闭环5、`docs/audit-2026-09-05-24h-comprehensive.md` §四 D 组遗留（已修复项不重复报告）。

## 发现列表

### D-2#1 [P1] supplier_errors_hot 未注册后台 promote 调度，hot 无界增长（不变式断裂）
- 证据：`bg/partition_manager.go:865-887` `promoteSpecs()` 共 15 项，**无** `promote_supplier_errors_hot_to_partition`；对照 `admin/data_lifecycle_hot_partition.go:41` hotPromoteTableMap 有注册（16 项）。`resolvePromoteConfig`（partition_manager.go:1071）也无该 label 分支。V371 迁移自身声明「默认 8h 保留，由 promote…批量迁移」（`deploy/sql/migrations/V371__supplier_errors_hot_and_stats.sql:16`），且 supplier_errors_hot 无任何 TTL cleanup（partition_manager 全部 cleanup 函数均未覆盖；全仓 grep 无 `DELETE FROM supplier_errors_hot` 除 promote/down）。
- 影响：后台每小时 promote 周期永不迁移该表，只有管理员手动点按钮才会迁移 → 违反「hot 只保留 8 小时」不变式，错误明细表随失败量无界增长（错误行是高频事件）；同时 admin 手动注册与后台调度两份注册表出现漂移。
- 最小修复：`promoteSpecs()` 增 `{fnName: "promote_supplier_errors_hot_to_partition", label: "supplier_errors_hot"}`；`resolvePromoteConfig` 走 default 8h 分支即可；加一个 bg 侧测试断言 `hotPromoteTableMap` 值集合 ⊆ `promoteSpecs()` fnName 集合（bg 不被 admin 依赖，可直接 import admin 的 map，无环）。
- 工作量：S

### D-2#2 [P1] V371 promote 复辟已被 602 证伪的「copy→DELETE→INSERT+EXCEPTION 吞错」非原子模式，INSERT 失败即批次静默丢失
- 证据：`deploy/sql/migrations/V371__supplier_errors_hot_and_stats.sql:249-278`——temp table 复制 → `DELETE FROM supplier_errors_hot`（在异常块**外**）→ `INSERT INTO supplier_errors ... EXCEPTION WHEN OTHERS THEN RAISE WARNING '...rows preserved in hot table'; n:=0`。602 的修复注释（`sql/migrations/startup/602_request_logs_promote_atomic.sql`）明确记录了同一模式的真实事故：「DELETE had already executed outside the exception sub-block, each failed batch was silently dropped (the RAISE WARNING even claimed 'rows preserved in hot table')」。plpgsql EXCEPTION 子块只回滚子事务（INSERT），先前 DELETE 照样提交。
- 加重因素：① `INSERT ... SELECT *`（:272）按位置对齐，正是 602 因 schema drift 翻车的写法；② 父表 supplier_errors 无 PK/唯一约束（V371:121-142），重复 promote 无冲突防护；③ 手动 promote 路径（admin/runHotPromoteJob）无 advisory lock（见 D-2#11），双实例竞态下 temp table 快照可重复插入父表。
- 影响：任何 INSERT 失败（列漂移、分区缺失、columnar 序列化超限等）= 该批错误明细**数据丢失**且日志宣称安全。真实 PG 验证（闭环5）只修了 GENERATED ALWAYS 这一个触发器，未修失败模式本身。
- 最小修复：按 656 模板（`sql/migrations/startup/656_auto_route_selections_hot.sql:121-147`）改写为单条 data-modifying CTE（`WITH batch ... FOR UPDATE SKIP LOCKED, moved_rows AS (DELETE...RETURNING 显式列), inserted AS (INSERT ... RETURNING)`），错误自然上抛由 Go 侧 `recordPromoteFailure` 记录；新迁移重装函数体。V371 尚在验证期，也可直接改 V371 文件。
- 工作量：S

### D-2#3 [P2] 迁移 657 未嵌入 installer embeddata，fresh install 缺 decision_history 列且同步测试拦不住
- 证据：`installer/cmd/llm-gw-installer/main.go:250-251/992/1131` 有 656 的 go:embed 与两个 map 条目；657 无任何 embed（grep 0 命中）。同步门禁 `installer/cmd/llm-gw-installer/stats_migrations_test.go` 的 `expected` map 为手工维护清单，657 不在其中（656 在），测试只校验 map 内条目 → 缺失静默通过。
- 影响：installer 全新装机不执行 `sql/migrations/startup/657`，`durable_llm_tasks.decision_history` 缺列；`durable/store_decision_history.go:46-59` SaveDecisionHistory 报 42703，按 best-effort 契约被吞（attempt_outcome 侧仅日志）→ 闭环3 的「伪迁移变真迁移」在 installer 装机上退化为零值历史，循环检测失效且无告警。
- 最小修复：embeddata/startup/ 加 `657_durable_llm_tasks_decision_history.sql`（从 `sql/migrations/startup/` 拷贝），main.go 三处登记，stats_migrations_test.go expected map 增条目。
- 工作量：S

### D-2#4 [P2] D-#3 维持确认：656 无网关侧 ensure，selection 写入在「只升二进制」存量库上整批静默丢弃
- 证据：`db/db.go` 的 ApplyMigrations 有 655（:142）、631、632 等迁移的 Go 侧 ensure，无 656 等价物（grep 无 ensureAutoRouteSelections）；`domains/hooks/observability/telemetry/selection_writer.go:186-195, 269-277` INSERT `auto_route_selections_hot` 失败 → `dropped.Add(len(batch))` + 一条 Warn，队列继续消费。settle worker（bg/auto_route_settle_worker.go:321）同库查询将每 5min 报错、promote 每小时报错——有日志但 selection 数据持续丢。
- 影响：升级二进制未跑 656 的存量库上，AUTO 路由选择数据全丢，亲和学习/settle 链路静默空转。
- 最小修复：在 db.go ApplyMigrations 增 `ensureAutoRouteSelectionsHotSchema`（656 的 hot 表 DDL 幂等版，或至少 `CREATE TABLE IF NOT EXISTS auto_route_selections_hot(...)` + 三索引），与 655 ensure 同位置。
- 工作量：S

### D-2#5 [P2] supplier_error_stats 无 TTL 出口，分钟桶预聚合无界增长
- 证据：V371 建表（V371:291-313）无分区无 retention；`bg/partition_manager.go` 的 10 项 cleanup（:342-395 及 cleanupOldProviderErrorDetails）均未覆盖；全仓 grep 无 `DELETE FROM supplier_error_stats`。聚合器每 5min UPSERT minute 桶（`bg/supplier_error_stats_aggregator.go:30-57`），行数 = 分钟数 × (supplier × credential × error_type × model) 组合数。
- 影响：趋势 API 唯一读源表线性无界增长（provider_error_details 同类问题本轮刚加过 TTL 清理）。
- 最小修复：仿 `cleanupOldProviderErrorDetails`（partition_manager.go:1155）加 `cleanupOldSupplierErrorStats`，`lifecycle.supplier_error_stats_ttl_days` 默认 30，day/hour 桶可另设更长档或只清 granularity='minute'。
- 工作量：S

### D-2#6 [P2] 7 张遗留 hot 表 promote 仍是 602 之前的非原子模式（批次丢失潜在面）
- 证据：399 重装了 341/344/346/347/353 的函数体并保留 temp-table+EXCEPTION 模式（`sql/migrations/startup/399_fix_columnar_promote_on_conflict.sql` 注释还断言错误的「INSERT 失败 EXCEPTION 保留 hot 数据」）；此后仅 341→602、359→624/628、526、534、579/580、614/615/626、656 完成原子化改写。仍遗留：usage_ledger(344)、request_wal(345)、routing_decision_log(346)、credential_model_index(347)、tool_usage_stats(348)、credit_ledger(349)、request_logs_bodies(353)（objects 快照 `sql/objects/functions/promote_*_hot_to_partition_*.sql` 与 345/348/349 迁移体均为该模式）。
- 影响：与 D-2#2 同款失败模式；request_logs 的 2026-08-25 事故证明 schema drift 真实发生，一旦命中即每批静默丢数据。
- 最小修复：以 656 函数体为模板逐表改写为单 CTE（可一个迁移批量重装 7 个函数）；顺带把各表 SELECT * 换显式列。
- 工作量：M

### D-2#7 [P2] supplier_error_stats 聚合器窗口过窄：迟到行 >10min 永久漏聚，聚合停摆 >8h 数据被 promote 后永久缺口，且 rollup 失败无指标
- 证据：`bg/supplier_error_stats_aggregator.go:114-131` 每次 tick 仅重算 `[now-2×interval, now)`（10min）；无 watermark/追赶逻辑；失败仅 `slog.Warn`（:124），无 Prometheus 计数。promote（修复 D-2#1 后）按 8h 搬走 hot 行，`supplier_error_stats` 读源只看预聚合表，`admin/errors_trend.go` fallback 仅在「窗口内 stats 全空」时触发（:10-12, 200）——部分缺口不会触发 fallback。
- 影响：聚合器停摆超过 8h（长事务、DB 抖动、发版窗口叠加）后，那段错误趋势永久缺失且页面无任何异常信号；>10min 迟到写入（promote/网络重试回填场景）同理。
- 最小修复：rollup 窗口改为「上次成功水位 → now」（进程内存水位或 settings_kv 存 last_success_to），加 `llmgw_supplier_stats_rollup_total{outcome}` 计数器；至少把窗口拉到 promote 保留窗（8h）内滚动重算。
- 工作量：S

### D-2#8 [P3] D-#7 维持确认：promote_request_logs SQL 默认 7d 与 8h 口径漂移
- 证据：`sql/migrations/startup/602_request_logs_promote_atomic.sql:1` `DEFAULT '7 days'`；Go 调度（resolvePromoteConfig default `lifecycle.hot_retention_hours`=8h）与 admin（defaultHotRetentionHours=8）均显式传参。仅影响手工 psql 直调。最小修复：新迁移重装函数 DEFAULT '8 hours'。S

### D-2#9 [P3] D-#12 维持确认：auto_route_selections 月分区无 TTL 出口
- 证据：`sql/objects/functions/drop_old_state_partitions_integer.sql:12-18` 目标表列表无 auto_route_selections；archiveSpecs（partition_manager.go:843）无；Go cleanup 无。父表只进不出。最小修复：把 `auto_route_selections` 加入 drop_old_state_partitions 目标数组（或独立 lifecycle TTL 设置）。S

### D-2#10 [P3] D-#4 维持确认：auto_route_selections_hot_tests.sql 未接入自动执行
- 证据：`sql/tests/auto_route_selections_hot_tests.sql` 无任何 runner 引用；`scripts/apply-hot-table-migrations.sh:105` 只跑 `partition_hot_table_tests.sql`，且测试失败仅 `log_warning` 不 fail（:108-115）。最小修复：脚本 run_integration_tests 增跑该文件并让失败置非零退出；或改写为 `deploy/sql/verify/` 下 DSN 门控 Go 测试（同 supplier_errors_pg_test.go 模式，可复用其 bootstrap）。S

### D-2#11 [P3] admin 手动 promote 路径无 advisory lock，双实例竞态无防护
- 证据：`admin/data_lifecycle_hot_partition.go:384-388, 641-645` 直接 `SELECT fn(...)`，无 `pg_try_advisory_xact_lock`（对照 bg 调度 partition_manager.go:954-974）；findRunningJobForTable 仅单实例去重。原子 CTE 表（656/602/526…）自愈；legacy temp-table 表（D-2#6 清单 + V371）在竞态下 temp 快照重复 → 父表重复行（V371 父表无 PK 无约束）。最小修复：runHotPromoteJob 每批前取同款 advisory lock，取不到即复用/skip。S

### D-2#12 [P3] data-lifecycle metrics 端点读法过时：扫父表全历史、hot 口径 7d、父表 size 恒 0
- 证据：`admin/data_lifecycle_metrics.go:51-63` `FROM request_logs`（父表 = 全历史 COUNT(*) FILTER，Prometheus 抓取即全表扫 columnar），`pg_total_relation_size('request_logs')` 对分区父表返回≈0，hot 桶按 7d 划分——与 8h hot 架构完全脱节。最小修复：hot 桶改查 `request_logs_hot`（count + pg_total_relation_size），warm/cold 按月分区元数据估算。S

### D-2#13 [P3] ensureSpecs 缺 ensure_supplier_errors_partition
- 证据：`bg/partition_manager.go:786-828` 已接 656 的 ensure，无 supplier_errors 对应项。因只有 promote 写父表且 promote 自 ensure（V371:241-247），无写入失败风险；但下月分区不预建、ensure 日志/可观测链路缺一张表。最小修复：加一行 `{fnName: "ensure_supplier_errors_partition", label: "supplier_errors"}`（timestamptz 签名，默认 argExpr）。S

### D-2#14 [P3] 656/657 无 down 迁移
- 证据：`sql/migrations/startup/` 目录 656、657 均无 `.down.sql`（651-655 均有；V371 有 down 且对称）。657 down 至少应 `ALTER TABLE durable_llm_tasks DROP COLUMN IF EXISTS decision_history`。S

### D-2#15 [P3] ColumnarInvariantCheck 启动结果被丢弃
- 证据：`cmd/gateway/main.go:512` `_, _ = bg.ColumnarInvariantCheck(...)`；函数注释明说「Returns the count of non-compliant partitions for callers that want to surface it on a /healthz endpoint」但无人接。最小修复：noncompliant>0 时 slog.Error + 暴露 `llmgw_columnar_noncompliant_partitions` gauge。S

### D-2#16 [P3] sql/objects/functions 的 promote 快照与现网函数体漂移（sync 重放会回退修复）
- 证据：`sql/objects/functions/promote_request_logs_hot_to_partition_interval_integer.sql` 仍是 7d+非原子旧体（现网已被 602 替换）；candidate_failure 等同理。`deploy/sql/README.md` 称 objects「按需生成，不入库」，但 sync-objects.sh 会把它同步进部署资产，若有人按快照重放将覆盖 602/624/628 原子修复。最小修复：从现库 pg_get_functiondef 重新生成快照，或在文件头加「生成物，禁止手工重放」横幅。S

## 已确认闭环（本轮不再报）

**settle worker 直写父表违规修复（D-#1）确认**：`bg/auto_route_settle_worker.go` 全部读写仅 `auto_route_selections_hot`/`request_logs_hot`（writeReward :484-497、abandon :505-509 均 UPDATE _hot 且带 `settled_at IS NULL` 守卫）；`settleAbandonAfter=4h < 8h` hot 保留（:52-59 有不变式论证）；文件头 :202-229 完整记录「禁 UNION ALL 父表」的 columnar 计划器限制。全仓 grep 确认 Go 侧无任何对 request_logs/auto_route_selections/supplier_errors/usage_ledger 父表的 INSERT/UPDATE/DELETE（storage/sqlite/request_log_store.go 为独立 SQLite 存储，不相关）。

**全表清单核对表**（promote 注册 / 后台调度 / 函数体原子性 / 窗口语义）：

| hot 表 | hotPromoteTableMap | promoteSpecs | promote 函数体 | 默认窗口 |
|---|---|---|---|---|
| request_logs_hot | ✓ | ✓ | 原子 CTE (602) | 8h（SQL DEFAULT 7d 漂移，D-2#8） |
| usage_ledger_hot | ✓ | ✓ | 旧模式 (D-2#6) | 8h |
| request_wal_hot | ✓ | ✓ | 旧模式 (D-2#6) | 8h |
| routing_decision_log_hot | ✓ | ✓ | 旧模式 (D-2#6) | 8h |
| credential_model_index_hot | ✓ | ✓ | 旧模式 (D-2#6) | 8h |
| tool_usage_stats_hot | ✓ | ✓ | 旧模式 (D-2#6) | 8h |
| credit_ledger_hot | ✓ | ✓ | 旧模式 (D-2#6) | 8h |
| request_logs_bodies_hot | ✓ | ✓ | 旧模式 (D-2#6) | 24h（文档化例外） |
| candidate_failure_logs_hot | ✓ | ✓ | 原子 CTE (624/628) | 8h |
| **supplier_errors_hot** | ✓ | **✗ D-2#1** | **非原子 (D-2#2)** | 8h（仅 SQL DEFAULT） |
| session_turns_hot | ✓ | ✓ | 原子 CTE (526) | 8h |
| session_bodies_hot | ✓ | ✓ | 原子 CTE (615/626) | 8h |
| handoff_logs_hot | ✓ | ✓ | 原子 CTE (534) | 8h（独立设置，floor 1h） |
| session_module_executions_hot | ✓ | ✓ | 原子 CTE (580) | 8h |
| dashboard_access_events_hot | ✓ | ✓ | 原子 CTE (579+607) | 8h |
| auto_route_selections_hot | ✓ | ✓ | 原子 CTE (656) | 8h |
| model_probe_runs_hot（特例） | —（有意退出） | — | — | TTL DELETE 14d ✓ |

**V371/656/657 迁移质量**：V371 幂等（IF NOT EXISTS/DO 块守卫/迁移内建验证块 + verify 测试 TestSupplierErrorsMigrationIdempotent 重放验证）；V371 down 对称完整。656 promote 含 retention/batch 参数守卫（:116-117）、DEFAULT 分区 rescue（detach→搬移→attach，:61-107）、ensure 带 advisory xact lock（:56）；656 的 ON CONFLICT DO NOTHING 跳行设计有注释论证。657 为幂等 ADD COLUMN + 后置条件断言；durable 侧 fencing UPSERT（store_decision_history.go:46-59）+ 128 条有界历史（attempt_outcome.go:271-272）+ fail-open 读取，语义正确。**已修两缺陷无同类残留**：全仓迁移 grep `compression=zstd` 0 命中（V371/656 均裸 `USING columnar` 或经 ensure 模板）；GENERATED ALWAYS AS IDENTITY 仅存在于 hot/stats 表自身（写入侧 DB 生成，合法），父表均普通 bigint。installer 656 已嵌入（main.go:250/992/1131）。

**真实 PG 验证矩阵**：`deploy/sql/verify/supplier_errors_pg_test.go` A-G 七组用例齐全（heap 语义/columnar 不可变/8h+批次/unified+RLS/UPSERT 幂等/迁移重放），DSN 门控离线 skip 设计正确。

**可观测性（部分闭环）**：promote 有完整指标族（`llm_gateway_hot_table_promote_duration_seconds/-failures/-batches/-rows/-skipped_total/-zombie_lock_streak`，bg/metrics.go:150-208，含 2026-08-31 P2-8 zombie-lock 语义修正）；settle 有 settled_total/lag/reward 直方图。分区缺失由 ensure 30s 超时日志 + 写入 23503 兜底；columnar drift 有启动检查（但结果被丢弃，D-2#15）。

## 冗余/待清理代码
- `bg/partition_manager.go:107` `archiveSpec.enabled bool //nolint:unused` — 从未读取，删除。
- `admin/data_lifecycle_hot_partition.go:568-690` 旧同步 promote 接口 + `handleDataLifecyclePromoteHot` 与异步路径逻辑重复且更弱（无去重、无 advisory lock、10min 硬超时），前端迁移后可删。
- `sql/objects/functions/promote_*_default_batch_*.sql` 系列（request_logs/routing_decision_log 等 default 分区旧批次函数快照）+ 341-359 时代 `promote_*_hot_to_partition_*` 快照 — 与现网漂移（见 D-2#16），建议一次性从 pg_get_functiondef 再生成。
- `bg/partition_manager.go:86` supplierStats 与 `promoteSpecs` 的注册割裂本身就是 D-2#1 的结构性症状——建议注册表收敛为单一来源。

## 统计
P0=0，P1=2，P2=5，P3=9，共 16 项。
