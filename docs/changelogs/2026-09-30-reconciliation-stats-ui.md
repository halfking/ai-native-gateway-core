# 2026-09-30 对账结算页方案 A

方案来源：`docs/proposals/2026-09-30-stats-ui/plan.md`。只做方案 A（`/admin/reconciliation`）。方案 B（租户统计）和方案 C（用户用量）没有做。

## 行为

- 页面拆成工具条、KPI、chart.js 双趋势、失败原因和分布表。`ReconciliationReport.vue` 172 行，查询在 `useReconciliationPage.ts`。
- 占比条分母是区间 `report.totals`，不是行内最大值。
- 下钻用 `router.push` 同步 `view`、`provider_id`、`model`、`tenant_id`、`person`。日期快捷区间不进 URL。
- 空的 `snapshot_dates` 不产生趋势点。耗时卡的值是 P50，副文案是 P95。
- 更慢的 summary 响应会被丢弃。按天明细展开后才挂载表格。
- 导出仍是 summary。旧的 6 维主表和 summary/daily 切换不再是主交互。

## 验证

`web/` 下 5 个 vitest 文件 28 项通过。`element-import-audit` 通过。`vue-tsc` 对账路径无报错，全量仍失败在既有 live-stream / session-jump 测试。没有浏览器实测，8782 上的已构建页面不是这次源码。
