# 2026-09-05 晚间 PG 日志审计：SQL 错误根因分析与修复报告

> 数据源：`llm-gateway-pg`（本地 kx-citus-pg17）之上运行的网关
> `~/kaixuan/llm-gateway-go/logs/gateway.log` 全天日志（32.5 万行，含轮转），
> 交叉验证 PG 目录（catalog）、触发器定义、fallback 备份文件与真实流量回放。
> 关联：`docs/audit-2026-09-05-ir-storage-provider.md`（遗留提示第 99 行指出
> V371 未重放）、`scripts/apply-db-revision-sequence.sh` 头注（656 被整序列
> 标记掩盖的前科）。

## 结论摘要

全天 828 条 SQL 错误，归为 6 类、5 个独立根因，全部修复并在运行态验证归零。
其中第 6 类（22003）的初始"日志观测增强"并非终局——部署后深挖定位到真正的
根因（563 触发器覆盖 572 修复），属本轮最有价值的发现。

| # | 错误（SQLSTATE） | 当日次数 | 根因 | 修复 | 方向 |
|---|------------------|---------|------|------|------|
| 1 | relation "supplier_errors_hot"/"supplier_error_stats" does not exist（42P01） | 310 | V371 迁移从未应用到升级库（供应商错误唯一事实源 + 预聚合缺失） | V371 纳入 `apply-db-revision-sequence.sh`（17:48 已应用，标记入表） | 表结构 |
| 2 | function promote/ensure_supplier_errors_* does not exist（42883） | 70 | 同上（V371 内函数） | 同上 | 表结构 |
| 3 | weekly peak rollup ON CONFLICT 无匹配唯一约束（42P10） | 1 | `credential_model_weekly_peak` 建表即无 `(week_start, credential_id, raw_model)` 唯一索引，Go 的 UPSERT 目标悬空 | 660 号迁移补唯一索引（幂等，已应用） | 表结构 |
| 4 | reconcile daily column "occurred_at" is ambiguous（42702） | 1 | `domains/stats/reconciliation.go` SELECT 列未加表别名，与 `stats_event_dedup.occurred_at` 歧义 | 限定 `f.occurred_at`（核对 dedup 表仅 3 列，无其他歧义点） | Go |
| 5 | cache lookup failed for relation/function（XX000） | 49 | 653/654 在网关运行期间 DROP/重建函数与分区，连接级缓存计划失效 | 无需修码：部署重启自愈，重启后未复发 | 无 |
| 6 | telemetry persist numeric field overflow（22003） | 397 | **563 的 `update_session_summary()` 触发器**：`v_prompt_tokens::DECIMAL(10,6) / v_total_tokens::DECIMAL(10,6)` 只有 4 位整数，`total_tokens ≥ 10000` 必然溢出并回滚整条 request_logs 事务。572 已修此行，但序列顺序 572→563 且同为 CREATE OR REPLACE——**563 的旧体在 1 秒后把修复整体覆盖回去**，572 的 per-file 标记已记录永不重跑 | 661 号迁移在 563 之后重申 572 修复体（含函数源码校验）；563 源文件同步修正（新装环境不再引入） | 表结构 |

## 第 6 类的证据链（22003，当日最大量）

1. 时间分布：397 次集中在 14:00–16:18 CST——563 于当日 06:33 应用后第一批
   长上下文 agent 流量到来之时；此前的 09-03/09-04 历史 `request_logs` 中
   有 4851 行 `total_tokens ≥ 10000`（触发器尚为 310 旧体时写入正常）。
2. 完美截断：当日 hot 表 18376 行 `MAX(total_tokens) = 9988`，≥10000 者为零——
   全部被触发器溢出杀死（非抽样，是硬边界）。
3. 双路失败机制：op=insert 直接触发 AFTER INSERT 触发器；op=update 走
   `client.go:2170` 的 `RowsAffected()==0 → insertRequestLog` 回退，再次撞
   同一触发器（当日 54 update / 2 insert 的分布与此吻合）。
4. 实锤复现：回滚事务内探针
   `INSERT ... prompt_tokens=66662` →
   `ERROR: numeric field overflow / DETAIL: A field with precision 10, scale 6
   must round to an absolute value less than 10^4 / CONTEXT: PL/pgSQL function
   update_session_summary() line 34`——与 PG 文档语义逐字吻合。
5. 排除项：request_logs_hot 的 5 个 numeric 列（cost_usd/cost_display/
   auto_confidence/confidence_num/quality_score）取值均 ≤ 1，api_keys 累计列
   最大 9466（numeric(14,8) 上限 100 万），credits_charged 为 bigint——均无
   溢出路径；4 个 float 列全历史 NULL 是"带值条目全被 22003 拒写"的果而非因。

> 为什么首轮部署后"看似归零"：17:51–19:13 窗口恰好没有 ≥10K tokens 的请求。
> 19:13 起大流量恢复，失败立刻重现（11:13–11:22 UTC 共 56 条）——这正是把
> 观测增强升级为根因修复的契机。661 应用后（19:49 CST）至最后观测
> （20:05+）零复发，且 66K/133K tokens 探针通过。

## 修复清单（全部已应用/已合入工作区）

| 文件 | 动作 |
|------|------|
| `scripts/apply-db-revision-sequence.sh` | 序列补入 660/661/V371；注释记录 572→563 覆盖事故与 657/658 重号原因 |
| `sql/migrations/startup/660_credential_model_weekly_peak_unique.sql` | 新增：weekly peak 周桶唯一索引（ON CONFLICT 推断只要唯一索引即可） |
| `sql/migrations/startup/661_session_summary_token_ratio_reassert.sql` | 新增：563 之后重申无界 numeric 比值函数体 + 源码校验 DO 块 |
| `sql/migrations/startup/563_session_summary_trigger_on_hot.sql` | 源修正：`::DECIMAL(10,6)` → `::numeric`（新装环境不再带 bug） |
| `domains/stats/reconciliation.go` | `occurred_at` → `f.occurred_at`（42702） |
| `domains/hooks/observability/telemetry/client.go` | 新增 `pgErrorDiagnostics()`：持久化失败时输出 PgError 的 Detail/Hint/Table/Column/Constraint——首轮部署时 22003 尚未定位，该诊断日志是定位触发器根因的保险索 |
| `deploy/sql/migrations/V371__supplier_errors_hot_and_stats.sql` | 纳入升级库部署轨道（文件本身无改动） |

编号说明：首轮的 657/658 与 merge 进来的 origin/main
（657_durable_llm_tasks_decision_history / 658_auto_route_structured_features）
撞号，已让位重编为 660/661；旧编号的 DB 标记（`gateway_db_revision_sequences`
按 basename 记录）保留为历史，重跑序列按新名重新应用（两迁移幂等）。

## 运行态验证证据

- 部署：`deploy-local.sh` 2.5.0.1950，`VERIFY_PASS=1`；V371/657（现 660）/658
  （现 661）标记全部入表；`supplier_errors_hot`、3 个月度 columnar 分区、
  `supplier_errors_unified`、`supplier_error_stats`、promote/ensure 函数、
  weekly peak 唯一索引全部就位。
- 供应商错误链路：真实 nvidia/kimi-k3 canceled 错误落 hot 表，分钟/小时/天
  三级聚合出现在 `supplier_error_stats`（修复前 267 次写入全败、表不存在）。
- reconcile SQL：修复后语句实跑返回 520 行（修复前 42702）。
- weekly peak：`ON CONFLICT (week_start, credential_id, raw_model)` 推断通过。
- 22003：66K/133K tokens 回滚探针通过；线上修复后零复发。

## 遗留与建议

1. **序列内同函数双迁移的覆盖风险是模式级问题**：572→563 的覆盖不是孤例
   （整序列标记掩盖 656 是上一例）。建议后续给 `apply-db-revision-sequence.sh`
   加一条规则：同一 `CREATE OR REPLACE FUNCTION` 只允许出现在序列的一个文件
   里，否则部署时报错，避免再靠事后审计发现。
2. 5 条 XX000（cache lookup failed）为运行期 DDL 的连接缓存失效，重启自愈；
   若高频复现可考虑 Go 侧把 XX000 归类为"重连后重试"，本轮未做。
3. PG 容器 stderr 在 09-04 16:04 至 09-05 17:26 之间有一段日志黑洞（服务正常、
   仅日志缺失），不影响本审计（以网关日志为准），供环境运维排查。
4. fallback 文件 `sessions-2026-09-05.jsonl.gz` 中段损坏（gzip 成员断裂），
   397 条失败条目仅抢救回 154 条；DB 恢复后的自动重放未覆盖这些行，如需找回
   历史明细可走 admin 侧 file reader 重放，本轮未操作。
