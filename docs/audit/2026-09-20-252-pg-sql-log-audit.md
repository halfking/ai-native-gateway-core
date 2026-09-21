# 252 PG SQL 日志审计轮（2026-09-20）

> 审计对象：pg-252-pg17（115.29.212.252 podman，PG 17.10 + Citus 13.3）实例级 SQL 日志与 pg_stat_statements（stats_reset 2026-09-11，覆盖 9 天）。
> 范围含同集群其他库（26 个库共享实例），跨库发现登记不越界修复。
> 本轮接续 2026-09-19 R46 离线静态扫描（docs/audit/2026-09-19-r46-offline-static-scan/，前会话遗留）与空目录 docs/audit/2026-09-19-252-pg-audit/。

## §〇、审计面的事实底座（先于一切发现）

1. **SQL 日志是易失的，历史不存在**：`logging_collector=off`，全部日志走 stderr 被 podman 捕获；容器 LogConfig **max-size=100MB 且轮转即丢弃**（无 .1 存档）。实测写入速率 ~300-500KB/s（`log_statement=all` + 大 payload 全量语句日志），**任何时刻只保留最近 ~6 分钟** SQL 历史。09-19 11:21 快照（15MB）实际只覆盖 38 秒；本轮已再快照 100 秒窗口（logs/ctr_snapshot_0920.log，51MB，不入库）。
2. **慢查询时长日志原本不存在**：`log_min_duration_statement=-1`，pg_stat_statements 是唯一慢 SQL 证据源。
3. pg_stat_statements 是实例级视图，**Top 榜必须按 dbid 过滤**（未过滤时 Top1 是 smm 库的 `SELECT MAX(date) FROM daily_kline`，20.5ks 均值 8.2s——与本产品无关但值得一提）。

## §一、发现与处置

| # | 级别 | 发现 | 处置 | 证据 |
|---|------|------|------|------|
| F1 | **P1** | **RLS super_admin bypass 全路径运行时失效**：`approval_manager.go setSuperAdminGUC` 写 `SET LOCAL app.current_role = 'super_admin'`，而 `current_role` 是 PG 保留字、不能作 customized-option 名 → 每次调用 syntax error（252 生产日志今日窗口 2 次，频率=ApprovalTimeoutWorker 60s 扫描节奏，**R41 起从未成功过**）。受害面：审批超时清扫（过期审批永久滞留 pending，slog.Warn 每分钟吞错）、跨租户审批读取（notification approval_notifier.go:350、admin session_approval.go:190 super_admin 详情） | 改 `SELECT set_config('app.current_role','super_admin',true)`（与 session_digest_backfill/reaper 既有正确写法对齐）；setTenantGUC 同步参数化（去手工单引号转义）；注释全部对齐 | 生产日志 STMT 原文；`character 15` 精确落在 `current_role` 词首；apihub/pg_store 等其他模块已用 set_config 佐证 |
| F2 | **P2** | 会话存在性 EXISTS 查询（request_logs_with_current_month）**P50 687ms / max 29.8s 贴 statement_timeout=30s 线，生产日志中被反复取消**（昨日快照取到被取消的 STMT 原文）。根因：视图 session_turns 分支（R37 冻结不可改）以 `CASE WHEN session_id LIKE 'sys:%' THEN NULL ELSE session_id END = $gw` 过滤，无表达式索引 → session_turns_hot（16k 行 cost 4476）与当月分区（cost 78304）**两侧全表扫**。hot 表是独立表非分区（dual-write），父表索引不级联 | 迁移 **727**：session_turns 父表 + session_turns_hot 分别建 `(tenant_id, (CASE...))` 表达式索引。planner 借"CASE=非空常量 ⇒ session_id=常量"恒等推导复用既有前缀索引。**真库 A/B：687ms → 1.2ms**（566×） | pg_stat_statements 9 天 6297 调用均值 1.21s；EXPLAIN A/B；252 实跑后 EXPLAIN 全 Index Scan |
| F3 | **P2** | request_stage_events 保留清理 DELETE **批均值 10.5s / max 29.7s 撞 30s 批超时**（9 天 532 批）。根因：子查询按 `created_at` 过滤无索引，全表扫 1.27GB（151k 行、行均 8.4KB trace payload）。retention 文件头注释（2026-09-05）**预登记过**"生产实测成本高再补 created_at 索引"——条件现已达成 | 迁移 727 一并补 `idx_stage_events_created_at`；子查询实测 **0.035ms**（原 Seq Scan） | pg_stat_statements DELETE FROM request_stage_events 条目；真库 EXPLAIN |
| F4 | **P1(ops)** | 日志策略双缺陷：`log_statement=all`（300-500KB/s 写风暴 + 6 分钟取证窗口）+ `log_min_duration_statement=-1`（慢 SQL 不可审计） | **ALTER SYSTEM 已于 252 实改**（source=postgresql.auto.conf，reload 即刻生效，无重启）：`log_statement=none`、`log_min_duration_statement=1s`。实测日志量 **7KB/s（↓约 50 倍）**，100MB 窗口覆盖从 6 分钟延至 ~4 小时；慢查询（含语句全文）与全部 ERROR 仍完整记录 | 修复后 `SHOW` 双确认 + pg_sleep(1.2) 慢日志落地 + 顺带捕获 2 条生产 1.2s/2.0s 慢语句 |
| F5 | P2（**跨库**，登记不修） | `pocket` 库（opencode_pocket.scheduled_tasks）：`UPDATE scheduled_tasks SET next_run_at=$4 ... CASE WHEN $4=0` — $4 被同时推导 timestamp/integer → **每次必失败**（inconsistent types deduced for parameter $4）。该库属 opencode-pocket 系统 | 报告移交，不在本仓库修 | 昨日快照 ERROR+STATEMENT 原文；llm_gateway 库无此表（26 库枚举确认归属） |
| F6 | P3（跨库，登记不修） | 实例级：`SELECT name FROM pg_proc WHERE proname='pg_stat_statements_reset'` — PG17 pg_proc 无 `name` 列，必炸（来源未知库的巡检脚本） | 同上 | 昨日快照 ERROR 原文 |
| F7 | P3 登记 | REFRESH MATERIALIZED VIEW CONCURRENTLY routing_analytics_7d：每 ~5min，均值 5.8s、**max 29.9s 贴超时线**，单次物化 16.96M 行 | 维持现状登记；若生产再报 canceled 需评估增量物化或窗口错峰 | pg_stat_statements 2631 调用 |
| F8 | P3 登记 | session_turns advisory lock 争抢：`pg_advisory_xact_lock(session_turns_advisory_lock_key)` 1.3M 调用、**max 21.5s**（锁排队）| 维持现状登记（claim 串行化设计代价，S4 停写前 claim 路径整改时一并看） | pg_stat_statements |
| F9 | P3 登记 | credential_model_bindings UPDATE max 12.8s（行锁等待，探活/绑定批量更新争抢）；request_logs_hot trace_events UPDATE max 19.5s；request_logs COALESCE 统计读 148k 调用 × 均值 135ms = 20ks 总量最大项 | 登记不动（无 cancel/timeout 实害证据，且涉及读面口径属 R45 §五#5 遗留） | pg_stat_statements |
| F10 | P3 登记 | request_stage_events 四个 9 天零扫描索引（tenant_ts/stage_status/upstream_failure/redis_miss，合计 443MB 死重）+ idx_stage_events_request_id 亦 0 扫描 | 登记 R37 纪律：drop 须先代码侧证实零读面，留待专项；本轮不删 | pg_stat_user_indexes idx_scan（stats_reset 09-11 起） |

**cancel 风暴量化**：今日 100 秒窗口 8 次 "canceling statement due to user request"（≈7k/天）+ 6 次 FATAL "connection to client lost"（pgx 取消后弃连，连接池churn）。来源三类：F2 视图查询、F3 保留清理批、路由候选 WITH 查询（昨日快照各取到原文）。F2/F3 修复后预计大幅回落，**下轮用新日志策略（慢日志+错误日志留存 4 小时窗口）复核回落幅度**。

## §二、改动清单

1. `domains/sessionaudit/approval_manager.go`：setSuperAdminGUC→set_config（保留字根修）；setTenantGUC→参数化 set_config；四处注释对齐。
2. `domains/sessionaudit/approval_manager_test.go`：10 处 pgxmock 断言随 SQL 形态更新（tenant GUC 期望补 WithArgs）。
3. `domains/sessionaudit/approval_guc_integration_test.go`（新）：真库 GUC 回归两用例（super_admin 读回 super_admin；tenant 含单引号原样保留），无 TEST_DATABASE_URL 时 skip——**此类 PG 语义缺陷只有真库测试能防复发**。
4. `sql/migrations/startup/727_sql_audit_slow_query_indexes.sql`（+down）：三索引。关键实现点：**PG17 不支持分区父表 CREATE INDEX CONCURRENTLY（252 实测 42809）**，采用"逐分区 \gexec CONCURRENTLY（只对已存在分区）+ 父表 ONLY 壳 + 幂等 DO 守卫 ATTACH"三段式，全新库（无分区）自然退化为空操作；未来月分区经 PARTITION OF 自动继承。
5. `scripts/apply-db-revision-sequence.sh`：727 登记（沿用 724-726 注释格式）。
6. 252 生产已实改（幂等，二进制后续部署 no-op，同 726 先例）：迁移 727 三索引全 valid（8/8 indisvalid=t）；日志策略 F4。

## §三、测试

- `go build ./...` 过；`go test ./domains/sessionaudit/... ./bg/... ./domains/notification/...` 全绿。
- 契约 `apply-db-revision-sequence_test.sh` PASS。
- 真库：本机 llm_gateway PG 跑 GUC 两用例 PASS；252 真库实跑 727（首次 CONCURRENTLY 父表 42809 → 改三段式后 8 索引全建成功且 valid）+ EXPLAIN A/B（687ms→1.2ms；子查询 0.035ms）+ pg_sleep 慢日志验证。
- 明确未覆盖：cancel 回落幅度需数天新日志观察（下轮取证）；R45 §五 顺延项原样继承。

## §四、风险

1. **F1 行为翻转面**：修复后原本 100% 失败的路径首次开始工作——审批超时清扫将真正把过期 pending 批量置 timeout/auto-approve（timeout_action=settings_kv `session_audit.timeout_action`，252 当前=reject 默认）；notification 会开始对审批发通知；admin super_admin 审批详情页首次可用。**上线后首日应观察 approval_queue 状态迁移量**（若存量积压大，首个 sweep 会一次性清算——worker 单 tx，量大时受 30s statement_timeout 约束，必要时手动分批）。
2. 日志策略改动：ALTER SYSTEM 写在容器内 postgresql.auto.conf——容器**重建**（非重启）会丢失，需随部署镜像的 postgresql.conf 固化（登记运维项）。`log_statement=none` 后不再有全量语句留痕，深取证依赖慢日志+错误日志+pg_stat_statements。
3. 727 依赖分区命名 `<partition>_effective_session`（\gexec 自动生成名）；若未来手工建非标名分区，ATTACH 守卫会跳过、父索引欠挂——仅影响未来分区索引继承，PARTITION OF 路径不受影响。
4. 表达式索引加速依赖"CASE=非空常量 ⇒ session_id=常量"推导，**查询侧必须沿用视图现有投影写法**（改写 CASE 结构会脱扣）。

## §五、handoff 更新

- 记忆库 llm-gateway-go-audit-cycle-progress 追加本轮；新增 pg-252-sql-log-audit-facts（日志易失性/策略新基线/跨库归属）。
- 原始物证：docs/audit/252-pg-log-audit-2026-09-19/（38s 窗口）与 docs/audit/2026-09-20-pg-sql-log-audit/logs/（100s 窗口）均**不入库**（体积，沿用 09-19 先例保持 untracked）；R46 offline-static-scan 目录为本轮上游输入，保留。

## §六、下一轮提示词（建议）

> 以本文 §四 + R45 §五 + R44 §五 顺延为起点，审计入口 docs/audit/playbook/orchestrator-prompt.md（R47）。优先级：
> 1) **F1 修复上线观察**：approval_queue 状态迁移量、approval timeout sweep 成功率（ gateway 侧 "approval timeout sweep failed" 应绝迹）、notification 通知量；存量 pending 清算是否触发 30s 批超时；
> 2) **cancel/慢查询回落复核**（本文 §一末）：用新日志策略（>1s 慢日志 + 错误日志）对比 727 前后 pg_stat_statements 增量，确认 EXISTS 视图查询与 stage_events DELETE 回落；
> 3) **matview 29.9s 边缘**（F7）专项评估：routing_analytics_7d 增量化或错峰；
> 4) F5/F6 跨库缺陷移交 pocket/opencode-pocket 巡检脚本 owner（可从 252 网关日志反查 client addr 定位服务）；
> 5) F10 死索引专项（先代码侧证实零读面再 drop，443MB）。
> 纪律沿用 R45 §六 ①-⑤；另加：⑥ 实例级日志分析必须按 dbid 过滤 pg_stat_statements；⑦ PG 语义类修复必须真库测试（pgxmock 测不出）。
