# 对账报表页白屏修复 · 批判式审计轮（2026-09-28/29）

## 结论 / 根因

`/admin/reconciliation`（含 `?view=internal`）白屏的直接根因：

**本仓 `web/src/main.ts` 不做 ElementPlus 全局注册（无 `app.use(ElementPlus)`，vite 也无 unplugin 自动导入），所有 `el-*` 组件必须在 `<script setup>` 显式 import。`ReconciliationReport.vue` 模板用了 7 个 `el-*` 组件（ElAlert/ElButton/ElDatePicker/ElRadioButton/ElRadioGroup/ElTable/ElTableColumn）却只 import 了 ElMessage。**

失败链（逐环已实证，非推断）：
1. 编译产物退化为运行时 `resolveComponent("el-table")`；生产构建裁剪 dev 警告 → **静默失败**，返回字符串标签名。
2. 组件成为 ELEMENT vnode；Vue `normalizeChildren` 对「ELEMENT + slot 对象」走 ELEMENT|TELEPORT 分支（minified `s&65`），**以无参调用 `default()`**。
3. 列插槽 `({ row }) => …` 解构 undefined → `TypeError: Cannot destructure property 'row' of 'undefined'`，渲染中断 → 白屏。
4. 该错误与构建无关——连续三版 chunk（Npm2bEhn → CDQl711I → …）偏移 1:6831/6917 分毫不变，因为源码没改。

旁证：`scripts/element-import-audit.mjs`（`pnpm element:check`）全仓 250 个 .vue 中**唯一标红即本文件 7 项**；正常页面（如 ClientAnalyticsView）均显式 import。

修复提交：`9946d75b5`（补 7 个 import + `groupRows` computed 收窄 providers|tenants 联合类型，import 生效后 vue-tsc 才开始真正检查该组件，顺带消除了暴露的 TS2322）。

## 本轮（审计轮）发现并修正的问题

对上轮工作的批判式复核，发现三个"声明完成但证据不足/缺失"的缺口：

| # | 缺口 | 本轮处置 |
|---|------|---------|
| 1 | **无浏览器级运行时验证**——上轮全部证据是静态的（审计脚本/typecheck/chunk 字节比对），部署冒烟只测登录+凭据，未测对账页本身 | 已用浏览器实测两个 URL：登录后 provider 视角 3 表 71 行、internal 深链 4 表 48 行、radio 选中态正确、汇总卡片有真实数据（截图留证） |
| 2 | **无回归测试**——删掉 import 也不会红 | 新增 `web/src/views/admin/ReconciliationReport.test.ts`（真实挂载组件+ElTable，断言行渲染）。**红-绿验证**：回退修复前组件 → 2 failed（渲染抛 TypeError）；修复后 → 2 passed。非假绿 |
| 3 | **`element:check` 未接入构建**——门禁形同虚设（脚本本身 exit 1 语义正确，但没人跑） | `web/package.json` build 链中插入 `node scripts/element-import-audit.mjs`，此后每次 `pnpm build`（含 deploy 前端构建）硬门禁 |

测试编写过程本身也踩了假绿陷阱并被纠正：mock 返回结构错包了一层 `{report:…}`（`getReportSummary` 在 api 层已解包），首轮 2 failed；修正后过。

## 改动文件与关键行为

- `web/src/views/admin/ReconciliationReport.vue`（9946d75b5，前轮）：7 个 el-* 显式 import；`groupRows` computed。
- `web/src/views/admin/ReconciliationReport.test.ts`（本轮新增）：2 用例（provider 行渲染 / internal 深链含 persons 表）。
- `web/package.json`（本轮）：build 脚本插入 element-import-audit 硬门禁。

## 测试命令与结果

- `pnpm element:check` → OK（250 files）
- `npx vitest run src/views/admin/ReconciliationReport.test.ts` → 2 passed
- 红-绿验证：回退旧组件 → 2 failed + TypeError；恢复 → 2 passed
- `pnpm test -- --run`（全套）→ **138 files / 1003 tests 全绿**
- `pnpm build`（含新门禁）→ 成功
- 浏览器实测 8782（登录 admin）→ 两 URL 渲染出表格数据行，无白屏

## 503 说明（非缺陷）

`/maintain-api/healthz`、`/maintain-api/menu/ops` 503：本地容器未配 `MAINTAIN_SERVICE_URL`，`maintain_proxy.go` 按设计返回 `maintain.not_configured`；前端 `edition.ts` 已 catch 并回落本地兜底菜单。浏览器网络面板红条不可避免，与白屏无因果（崩溃栈触发源是 `refresh()` 赋值 `report.value`）。

## 遗留风险

1. `element:check` 是启发式（只扫 `<template>` 内 `<el-*>` 标签 vs import）；`component :is`、JSX 等写法不覆盖。
2. 登录态浏览器验证依赖本地 admin 口令（kaixuan env SSOT）；口令轮换后脚本化复验需换源。
3. `GroupRow = ReportProviderRow & Partial<ReportTenantRow>` 交叉类型是局部妥协；若后续两视角列进一步分化，应拆成两张表或后端统一行形。
4. deploy-local 的部署后验证不含本页 UI 冒烟（本次靠手工浏览器验证补位）；若要常态化，需扩 deploy-lib（跨仓共享库，另行立项）。
5. 并行会话频繁（本轮期间 8782 又被升到 2.5.6.2322，所幸均含本修复）；共享网关上的回归验证需先核对 `git log` 与 slot 版本。

## 下一轮提示词（建议）

> 对账页白屏修复（9946d75b5 + 审计轮）已收口。下一轮可选方向：(a) 把「登录→打开关键 admin 页→断言 el-table 渲染」做成 Playwright 冒烟脚本挂进 deploy 后验证；(b) 审计 element-import-audit 对 v-loading 指令外的其他指令/动态组件的盲区；(c) GroupRow 类型收敛重构。开始前先 `git fetch` 并确认 8782 slot 版本含 9946d75b5 祖先。
