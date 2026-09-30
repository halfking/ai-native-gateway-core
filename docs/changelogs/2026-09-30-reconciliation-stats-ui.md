# 2026-09-30 对账结算页方案 A

方案来源：`docs/proposals/2026-09-30-stats-ui/plan.md`。只做方案 A（`/admin/reconciliation`）。方案 B（租户统计）和方案 C（用户用量）没有做。

## 行为

- 页面拆成工具条、KPI、chart.js 双趋势、失败原因和分布表。`ReconciliationReport.vue` 172 行，查询在 `useReconciliationPage.ts`。
- 占比条分母是区间 `report.totals`，不是行内最大值。
- 下钻用 `router.push` 同步 `view`、`provider_id`、`model`、`tenant_id`、`person`。日期快捷区间不进 URL。
- 空的 `snapshot_dates` 不产生趋势点，并且卸掉 canvas。耗时卡的值是 P50，副文案是 P95。`chart.destroy()` 在下一拍恢复行内样式，`v-show` 藏不住空白画布，所以空态用 `v-if`。
- 更慢的 summary 响应会被丢弃。按天明细展开后才挂载表格。
- 导出仍是 summary。旧的 6 维主表和 summary/daily 切换不再是主交互。
- 日期预设结束在 UTC 昨天，与 `reportRange` 一致。不提供「今天」。跨度上限 367 天（含首尾）。
- 不在 `snapshot_dates` 里的按天补零行标「未聚合」。租户「占比」列跟随当前指标。Token 面积图 `fill` 为 `stack`。

## 验证

对账相关 vitest 覆盖空 canvas、未聚合日、租户占比和 `fill: 'stack'`。2026-09-30 22:30 在 Vite `127.0.0.1:5781` 看到默认区间 09/23–09/29，预设没有「今天」。该进程把 `/api` 代理到 `localhost:8781`，summary 为 500，按天表和趋势图没有画出来。8782 镜像里的前端没有换成这次源码。方案 B/C 未做。
