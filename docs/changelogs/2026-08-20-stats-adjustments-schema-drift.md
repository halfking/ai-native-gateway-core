# 2026-08-20 — stats reconciliation approval schema drift (P0-1)

## 做了什么

修复 `admin/stats.go handleStatsReconciliationApprove` 与
`stats_adjustments` 表的 schema 漂移。Runbook §6 已显式声明此为
release-blocker：handler INSERT 引用 `adjustment_type`/`metric_name`
（schema 实际列是 `adjustment_id`/`metric`），导致每次审批事务
必然回滚，`llm_gateway_stats_adjustments_total{result="committed"}`
恒为 0。

同时修复 `month_start` 字段的潜在跨月落桶问题：handler 原来用
`date_trunc('month', now())::date`，跨月审批会把 adjustment 落到
审批动作发生的月份，而非 reconciliation run 实际覆盖的月份。改
为 JOIN `stats_reconciliation_runs` 取 `date_trunc('month', period_start)::date`。

## 改动清单

| 文件 | 说明 |
|---|---|
| `admin/stats.go` | 重写 SELECT（增加 JOIN runs 与 month_start 列）与 INSERT（改用 `adjustment_id`/`metric` schema 列）；INSERT 失败时直接调用 `RecordStatsAdjustment("approve","failed",1)` 而非依赖 closure（修复 closure 看不到 approved++ 之前失败的语义 bug） |
| `admin/stats.go`（底部） | 抽出 `beginApprovalTx` 包级 seam + `beginApprovalTxOverride` 变量，供 pgxmock 测试注入事务 |
| `sql/migrations/startup/544_stats_adjustments_alignment.sql` + `.down.sql` | follow-up migration：新增 `adjustment_type text NOT NULL DEFAULT 'reconciliation'` + `metric_name text`（NULL backfill from `metric`）+ `idx_stats_adjustments_type` 索引 |
| `installer/cmd/llm-gw-installer/embeddata/startup/544_stats_adjustments_alignment.sql` + `.down.sql` | 镜像到 installer 嵌入式升级目录（沿用 539/540 做法） |
| `admin/stats_approval_test.go` | 新建 10 个 pgxmock 单元测试覆盖 happy / column set / period-aware / reject / already-resolved / insert failure / commit failure / auth forbidden / diff not found / tenant isolation |
| `docs/runbooks/stats-reconciliation-rollout.md` | §2 schema readiness 表更新（544 新增列）；§6 重写为「handler + 544 已上线」状态，附 post-deploy 验证步骤与回滚策略 |

## 为什么这样做

- **修复 + migration 组合** 而非单一路径：handler INSERT 必须立刻对齐
  schema 才能让 approval 链路真正闭环；migration 544 把 handler 想要的
  `adjustment_type`/`metric_name` 列加到 schema，未来 handler 可以脱离
  reason-prefix 把数据写到正确的列。两个 PR 并行解耦 deploy 顺序。

- **reason prefix 是临时妥协**：在 544 上线前 handler 把
  `reason = "[reconciliation] " + req.Reason`，audit trail 仍可读；
  544 上线后下一次 PR 可删除 prefix，改回直写 `adjustment_type`/`metric_name`。

- **month_start = period_start 的月份**：handler 现 JOIN runs，避免
  跨月审批把 adjustment 写到错误月份（这是 2026-08-19 影子对比抽样
  时发现的统计型缺陷，调用方未明确文档化）。

- **`approved` 计数语义修复**：之前 `recordFailedAccounting` 仅在
  `approved++` 之后才生效，所以 INSERT 失败路径不会记录
  `approve/failed`。改为 INSERT 失败时直接 emit metric，
  `committed`/`failed` 比值不再偏向乐观。

## 验证结果

| 项 | 结果 |
|---|---|
| `go build ./...` | ✅ |
| `go test -race -count=1 -run TestHandleStatsReconciliationApprove ./admin/` | ✅ 10/10 通过 |
| handler INSERT column set 与 migration 536 + 544 schema 一致 | ✅ `adjustment_id` (via `gen_random_uuid()`), `tenant_id`, `month_start`, `dimension_type`, `dimension_key`, `metric`, `delta`, `currency`, `reason`, `source_event_id`, `approved_by`, `approved_at`, `created_by`, `created_at` |
| handler INSERT 不再引用 `adjustment_type`/`metric_name`（除非 544 上线） | ✅ happy path 测试断言 |
| migration 544 应用后 pg_dump 显示 `adjustment_type` 默认值 + `metric_name` backfill | ✅ schema 改动幂等（`ADD COLUMN IF NOT EXISTS`） |
| `month_start` 取自 `r.period_start` 而非 `now()` | ✅ 通过 SELECT 列显式 JOIN |

部署后（154）观察：

| 指标 | 预期 |
|---|---|
| `llm_gateway_stats_adjustments_total{action="approve",result="committed"}` | 第一次审批后分钟内 > 0 |
| `llm_gateway_stats_adjustments_total{action="approve",result="failed"}` | 仅在真实 DB 错误时 > 0 |

## 部署流程

1. **252 灰度**：先发 migration 544（schema-only，可独立 apply）；再发
   handler 修复（向后兼容，因为 544 默认值填充了 handler 原本想写但写不
   进去的两列）。
2. **245 验证**：在 245 上跑一遍 `go test -race ./admin ./domains/stats ./metrics`，
   集成测试 `go test -tags=integration -run TestReconciliation_Metrics_CompletedAndAutoRepaired ./domains/stats` 必须保持绿。
3. **154 生产**：先 apply migration 544，再部署 gateway 镜像；观察 §6 表格
   两个指标至少 1 小时。

## 提交链

```
feat(stats): adjust approvals schema alignment + period-aware month_start
  ├── admin/stats.go (SELECT JOIN runs + INSERT schema-aligned)
  ├── admin/stats_approval_test.go (10 pgxmock tests)
  ├── sql/migrations/startup/544_stats_adjustments_alignment.{sql,down.sql}
  ├── installer/.../embeddata/startup/544_stats_adjustments_alignment.{sql,down.sql}
  ├── docs/runbooks/stats-reconciliation-rollout.md (rewrite §6)
  └── docs/changelogs/2026-08-20-stats-adjustments-schema-drift.md
```

## 遗留与风险

- ⚠️ **migration 544 与 handler 的 deploy 顺序**：handler 已不再写
  `adjustment_type`/`metric_name`；如果 154 部署顺序错误（先发代码再发
  migration 544），handler 在迁移应用前仍能跑（因为 INSERT 改用了 schema
  已有列）。所以**安全方向是先发 544 再发代码**，但反向也安全。
- ⚠️ **`recordFailedAccounting` closure 仍可能误用**：handler 其他位置
  仍依赖 `approved++`/`rejected++` 之后再走 closure 路径。本次只在
  INSERT 失败路径加显式 metric；下一次 PR 应统一改用「每次操作立即记
  metric」，避免类似 bug 再现。
- ⚠️ **line 390 的 tenant check 是 dead code**：当前 handler 入口仅放行
  `super_admin`/`admin_key`，两者都被 line 390 的 tenant bypass 条件覆
  盖。后续若放开入口给 `tenant_admin`，需要重新启用 line 390 并补对应
  集成测试。本次未改此路径，留待独立 PR。
- ✅ **后续清理 task**：
  1. handler 移除 reason prefix，迁移到直写 `adjustment_type`/`metric_name`。
  2. line 390 dead code 启用 + tenant_admin 集成测试。
  3. handler 整体抽象 `dbBeginner` 接口，消除 `beginApprovalTxOverride` 包级 seam（更 OO 的方案）。