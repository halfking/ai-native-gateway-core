# Migration 993: report_snapshots.estimated_cost_cents 精度升级（F4 落实）

## 目的

对账快照的费用列从 bigint（逐分组取整）升级为 numeric(24,6)（精确分值）：

- 旧口径：每个 grain/provider/tenant 桶各自 `ROUND(SUM(cost_amount)*100)`，
  区间总计 = Σ 取整分组 ⇒ 与一次求和每天差 ≤3¢（211 实测定量）。
- 新口径：存精确分值，读面总计 = Σ 精确 = 精确，展示一次取整（toFixed(2)）
  ⇒ 对账页总费用与看板英雄区（usage_facts 一次求和）逐字相等。

## 配套

- 写面（domains/reportrollup）六段聚合 SQL 去掉逐分组 ROUND，Go 类型 int64→float64。
- 历史快照回填：对 usage_facts 仍覆盖的日期重跑 RollupDay（POST /run 幂等）；
  更早日期保留旧取整值（真源已不在）。

来源：2026-11-20 看板对账口径对齐轮（docs/audit/2026-11-20-看板对账口径对齐-主档.md §五 L-cost）。
