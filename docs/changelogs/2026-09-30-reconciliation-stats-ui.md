# 2026-09-30 对账结算页方案 A

方案来源：`docs/proposals/2026-09-30-stats-ui/plan.md`。只做方案 A（`/admin/reconciliation`）。方案 B（租户统计）和方案 C（用户用量）没有做。

## 行为

- 页面拆成工具条、KPI、chart.js 双趋势、失败原因和分布表。`ReconciliationReport.vue` 172 行，查询在 `useReconciliationPage.ts`。
- 占比条分母是区间 `report.totals`，不是行内最大值。
- 下钻用 `router.push` 同步 `view`、`provider_id`、`model`、`tenant_id`、`person`。日期快捷区间不进 URL。
- 空的 `snapshot_dates` 不产生趋势点，并且卸掉 canvas。耗时卡的值是 P50，副文案是 P95。`chart.destroy()` 在下一拍恢复行内样式，`v-show` 藏不住空白画布，所以空态用 `v-if`。
- 更慢的 summary 响应会被丢弃。按天明细展开后才挂载表格。
- 导出仍是 summary。旧的 6 维主表和 summary/daily 切换不再是主交互。

## 验证

`web/` 下 5 个 vitest 文件 29 项通过（含空覆盖后 canvas 必须从 DOM 消失）。`vue-tsc` 全量仍失败在既有 live-stream / session-jump 测试，本轮未重跑全量。浏览器复测走 Vite 开发服务代理本机 8782：下钻后退、按天 7 行、空区间卡片内「暂无数据」。8782 镜像里的前端没有换成这次源码。方案 B/C 未做。
