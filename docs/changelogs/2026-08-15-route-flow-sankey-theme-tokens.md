# Route Flow Sankey Theme Tokens

## Summary

将路由流向图的任务分类色迁移到现有 daylight/night 主题 token。保持 8 类任务的独立
视觉编码，不改变数据契约、图例、SVG 布局、连线宽度或交互。

## Changes

- `RouteFlowSankey` 的 8 类任务色改用 `--accent`、`--success`、`--warning`、
  `--danger` 及其 `color-mix()` 组合。
- 指定模型与未知任务继续使用中性色，但改为 `--muted` 和 `--border` 组合。
- 将该组件加入颜色合规测试，禁止重新引入硬编码 hex 或 rgb/rgba 色。

## Verification

- `pnpm exec vitest run src/components/color-token-compliance.test.ts`
- `pnpm exec vue-tsc --noEmit`
- `pnpm build`
- kx-design analytics 目录扫描：目标组件无硬编码色，紫色专项 0 违规。
- Playwright Chromium：daylight/night × 1440px/430px 四场景；8 类计算色保持独立，
  页面无横向溢出，console/page error 为 0。
- 截图：`/var/folders/q9/_5p60_p90ts99ybv605s8h9r0000gn/T/opencode/ui-verify-route-flow-sankey-*-20260815.png`。

## Rollback

代码回滚可直接 revert 本次提交。
