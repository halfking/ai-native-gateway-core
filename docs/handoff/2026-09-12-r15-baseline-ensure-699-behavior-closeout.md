# Handoff: R15 收口 — 三方 baseline ensure 收敛 + 699 钉扎 + 真库行为夹具（2026-09-12）

## 1. 任务概要（Mission Summary）

承接上轮 handoff（`2026-09-12-migration-694-rollback-selfheal-closeout.md`）的
5 条遗留风险，本轮以四个逻辑单元闭合 #1–#4 并完成 #5 评估：

| 单元 | 提交 | 内容 |
|---|---|---|
| #1/#2 baseline 再生 | `f08e0a95b` | 三份 01-schema.sql 的 ensure 函数面收敛 + 三方契约测试 |
| #3 时区覆盖补全 | `531ea1a86` | 迁移 699（supplier_errors 钉扎）+ Go 调用侧显式上海日历日 |
| #4 真库行为测试 | `0db9d9b7e` | testcontainers 夹具（integration 标签），UTC 月界行为断言 |
| #5 并发竞态评估 | 本文 §5 | 结论：值得做，六面同步成本高，单列一轮（未实现） |

## 2. 结论 / 根因（Findings & Root Cause）

| 级别 | 发现 | 处置 |
|---|---|---|
| P1（新发现） | **本机 llm-gateway-pg 从未应用 687**：ledger 有 686/689/694 却无 687，25 个分区仍带 `08:00:00+08` 边界（credential_model_index / credit_ledger / request_wal / routing_decision_log / tool_usage_stats / usage_ledger 的 2026_07..2026_10 + routing_decision_log_archive_2026_08）。2026-10-01 起当日 ensure 按 +08 零点建 10 月分区会与 08:00 上界重叠 → 42P17 连锁。行为夹具首轮 TEST_PG_URL 运行实证（警告清单见测试输出） | **未擅自修复**（共享实例、分区重建含数据回灌，须人工窗口执行）。行动项：对本机应用 687（幂等，干净分区自动跳过），2026-10-01 前 |
| P1（本轮修复） | 三份 baseline 的 `ensure_request_logs_bodies_partition` / `ensure_next_month_request_wal_partition` 仍是 columnar 体——59c5dda12 给 baseline 手工钉扎时用了 pre-562 旧基底，权威库（694）是 heap | 两函数按 694 文替换（三份同步），三方契约测试锁定 heap/columnar 存储策略 |
| P1（本轮修复） | `ensure_tool_usage_stats_partition` / `ensure_credit_ledger_partition` 三份 baseline 全缺，而 promote 函数 PERFORM 它们；installer 重放集从 478 起，334/335 永不重放 → 全新安装 promote 链运行时 42883 | 从权威库 pg_dump 原文补齐（含 COMMENT 块），按字典序插入 |
| P2（本轮修复） | installer embeddata 副本整体陈旧：11 个 ensure 函数是 pre-689/694 体（candidate void 空壳、0 处钉扎） | 从 canonical 逐函数移植；三份归一化对比 15/15 SAME |
| P2（本轮修复） | `ensure_supplier_errors_partition`（timestamptz）无钉扎——694 清单漏了 deploy 轨（V371）出身的它；UTC 会话月界窗口建出 8h 偏移分区 | 迁移 699（694 定式：SET LOCAL 首语句 + DECLARE 初始化器移入体内；保持 V359 columnar 语义），down 按 42710 教训 replace-safe |
| P2（本轮修复） | Go `ensureNextMonthPartitions` 用服务器时钟 + `$1::date` 会话时区转换推导"当前/次月"——与函数体钉扎无关的独立偏移面 | `time.Now().In(partitionTZ)`（FixedZone +08，跟随 survival_coordinator 先例）；date 签名 spec（sessions_v2 / session_module_executions / dashboard_events / cache_metrics / auto_route_selections **5 个**，上轮口径写 4 个）传上海日历日字面量字符串 |

并行会话（同一工作树，已吸收/共存）：
- `2ad8d64ad`：迁移 698 = 9 个 promote_hot_to_partition 函数钉扎（占用 698，本轮迁移顺延 699）。
- `b2639182b`：anthropic IR unknown-only 归因修复（其半成品曾短暂挂住 pre-commit go vet，等待其完成后重试提交解决）。
- 进行中：三份 baseline 的 promote 函数面收敛 + `baseline_promote_functions_contract_test.go`（未提交）——与本轮 ensure 面守卫互补，实测共存绿。

## 3. 改动文件与关键行为（Changes）

### f08e0a95b（4 文件 +627/−48）
- `sql/schema/01-schema.sql`、`deploy/sql/schemas/baseline/01-schema.sql`：补 2 函数（权威 dump 原文）+ 2 函数 columnar→heap（694 文）
- `installer/cmd/llm-gw-installer/embeddata/01-schema.sql`：同上 + 11 个陈旧函数体移植
- `sql/migrations/startup/baseline_ensure_functions_contract_test.go`（新增）：三方同集合、归一化同体、钉扎为首条体内语句、heap-vs-columnar 策略

### 531ea1a86（10 文件 +356/−2）
- `sql/migrations/startup/699_supplier_errors_ensure_timezone_pin.sql` + `.down.sql`（新增）
- `sql/migrations/startup/migration_699_test.go`（新增）：镜像字节一致 / 合同 / down 可执行三组
- installer 五点接线：`embeddata/startup/699_*.sql` ×2、`main.go`（embed var + map）、`runner.go`（StartupFiles）、`stats_migrations_test.go`（字节等价 map）
- `bg/partition_manager.go`：`partitionTZ` + `ensureNextMonthPartitions` 上海日历日推导

### 0db9d9b7e（1 文件 +381）
- `sql/migrations/startup/migration_694_behavior_integration_test.go`（新增）：见 §4

## 4. 测试命令与结果（Verification）

```bash
# 静态门禁（每单元提交前均绿）
go test ./sql/migrations/startup -count=1        # ok（含三方契约 + 694/699 合同）
go test ./db -count=1                            # ok
go test ./bg -count=1                            # ok
cd installer && go test ./...                    # ok（含 TestStartupFilesAreAllEmbedded 双向对账）

# 699 真库实证（本机 llm-gateway-pg，UTC 会话 + 回滚事务，零残留）
#   up: 2026-12-01 02:00+08 → supplier_errors_2026_12（pre-699 体在 UTC 下会产出 2026_11）
#   down: 恢复 pre-699 体（无钉扎、DECLARE 初始化器），ledger 行删除
TEST_PG_URL='postgres://…@127.0.0.1:5432/llm_gateway?sslmode=disable' \
  go test -tags integration ./sql/migrations/startup -run TestMigration694UTCBoundary -count=1 -v
#   citus 路径 PASS：694 全 13 函数 + 699 + next-month columnar 三件套；
#   2027_04 边界时刻（任何实例都无此分区，强制走 create 路径）全部产出
#   '2027-04-01 00:00:00+08' 上海午夜边界；pre-699 形态会得到 2026_09。
#   附带 25 个 pre-existing 08:00 边界警告（见 §2 P1 行动项）。
go test -tags integration ./sql/migrations/startup -run TestMigration694UTCBoundary -count=1
#   vanilla testcontainers（postgres:16-alpine）路径 PASS：columnar 定义过滤，
#   10 个 heap 函数全验证，容器正常终止。
git push origin main  # f08e0a95b / 531ea1a86 / 0db9d9b7e 均已推送
```

## 5. #5 评估结论：ensure 函数 advisory lock 化（656 先例）

**结论：值得做，但六面同步成本高，建议单列一轮，本轮未实现。**

- 竞态真实：`IF NOT EXISTS` → CREATE 非原子。触发面 = 蓝绿双侧同时活跃窗口
  （154 翻转制）× 交叠调度（日级 ensure pass vs 1h promote 调度——698 起每个
  promote 体都会调 ensure）。败者报 duplicate relation/42P17，该表本周期
  ensure 失败（有日志、下周期自愈，无数据损坏）。
- 修法零风险且先例充分：656 的 `PERFORM pg_advisory_xact_lock(hashtext('ensure_<fn>'))`
  置于存在性检查之前；同表 promote 批次串行化反而语义更正确。xact 域锁无泄漏。
- 成本在同步面而非 SQL：694 已是"已发布"迁移，不能改历史 → 新迁移 700 全量
  重定义 14 个函数（694 的 13 + 699 的 supplier_errors）→ installer 五点接线 +
  `db.go` 自愈镜像同步（689/694 教训：镜像必须收敛最新迁移体，否则每次开机
  覆盖回旧体）+ 三份 baseline 再收敛 + 契约测试更新。这不是大改动，但每一面
  都有独立先例会忘。
- 顺带项（可并入 700）：`ensure_cache_metrics_partition` / `ensure_dashboard_events_partition`
  等的 `DEFAULT CURRENT_DATE` / `DEFAULT NULL` 参数默认值在 UTC 会话同样偏移
  （Go 侧已改显式传参兜住现行路径，但函数默认值仍是陷阱）；`ensure_handoff_logs_partition`
  是 noop、`ensure_model_probe_runs_partition` 已退役，无需处理。

## 6. 遗留风险（Open Risks，更新后）

1. **本机 687 未应用**（P1 行动项，2026-10-01 死线）：见 §2。幂等，可随时执行。
2. **三份 baseline 的 promote 面分歧**：canonical 缺 695 P2 self-heal 体；
   installer/deploy 缺 566 `bump_credentials_governor_revision`、credentials
   新列（concurrency_mode/tpm_limit）、tenant_model_policies 约束等。并行会话
   正在处理（工作树已见其改动），本轮未触碰。
3. **#5 advisory lock**（见 §5）：单列一轮，从 700 起核对编号。
4. **非活跃路径的钉扎缺口**（低优）：`ensure_model_probe_runs_partition`（退役，
   仍 columnar + 无钉扎）、`ensure_handoff_logs_partitions`（复数版，columnar +
   无钉扎，活跃调用方未接）。若未来重新接线须先钉扎。
5. **baseline 再生长效机制**：本轮靠手工移植收敛，`scripts/` 无权威 dump→三份
   派生的自动化；长期应从 252 权威库一键再生成（含 ensure/promote 全函数面 +
   三方对比工具化）。

## 7. 下一轮提示词（Next-Round Prompt）

```
继续会话：llm-gateway-go 迁移审计轮（R16）。HEAD=0db9d9b7e 起 fetch 核对（并行
会话活跃：2ad8d64ad=698 promote 钉扎、b2639182b=anthropic IR、另有 baseline
promote 面收敛在途），勿覆盖他人工作树改动、精确路径提交。

优先级：
1. 【P1 环境行动项】本机 llm-gateway-pg 应用 687（ledger 无 687 行，25 个
   08:00+08 边界分区，2026-10-01 起 42P17 风险；687 幂等）。应用后用
   integration 夹具复跑确认警告清零：
   TEST_PG_URL=… go test -tags integration ./sql/migrations/startup \
     -run TestMigration694UTCBoundary -count=1 -v
2. 【#5 落地】迁移 700（fetch 核对编号，699 已占用）：14 个 ensure 函数
   （694×13 + 699 supplier_errors）加 pg_advisory_xact_lock(hashtext(…))，
   656 先例；六面同步 = 迁移 up/down + installer 五点 + db.go 自愈镜像 +
   三份 baseline + 契约测试。评估细节见
   docs/handoff/2026-09-12-r15-baseline-ensure-699-behavior-closeout.md §5。
3. 【视并行会话进度】若 baseline promote 面收敛未完成，接手收尾（canonical
   缺 695 self-heal 体、installer/deploy 缺 566 governor_revision 等）。
4. ensure 函数 DEFAULT CURRENT_DATE / DEFAULT NULL 参数默认值的 UTC 偏移
   陷阱（Go 侧已显式传参兜住现行路径），可并入 700 或单列。

门禁：触碰 sql/migrations → go test ./sql/migrations/startup -count=1；触碰
db → go test ./db -count=1；触碰 bg → go test ./bg -count=1；触碰 installer →
cd installer && go test ./...；integration 夹具双路径（TEST_PG_URL citus +
vanilla 容器）；提交走 pre-commit（6 项）；逻辑单元精确路径提交并 push。
```

## 8. 引用（References）

- 本轮提交：f08e0a95b、531ea1a86、0db9d9b7e
- 前序：055cecc78（694 收口）、59c5dda12（694 主体）、2ad8d64ad（698 promote 钉扎，并行）
- 先例：656（advisory lock）、562/687/689（存储策略与边界修复）、V359/V371（deploy 轨 supplier_errors）
- 行为夹具运行方式见 `sql/migrations/startup/migration_694_behavior_integration_test.go` 头注释
