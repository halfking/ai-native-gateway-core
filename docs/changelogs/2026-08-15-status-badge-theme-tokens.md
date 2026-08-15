# Status Badge Theme Tokens

## Summary

将凭据监控的模型可用性五态状态徽章迁移至现有 daylight/night 语义 token，保持
状态文案、提示、CSS 状态类和故障优先级不变。

## Changes

- 可用、手动禁用和绑定缺失状态分别复用 `--success-soft`、`--danger-soft` 和
  `--warning-soft`。
- 探测失败继续比手动禁用拥有更强的危险色背景和边框；未声明继续保持中性 muted
  语义。
- 将 `StatusBadge.vue` 加入颜色合规测试，禁止重新引入硬编码 `#hex` 或 `rgb/rgba`
  色值。

## Verification

- `pnpm exec vitest run src/components/color-token-compliance.test.ts`
- `pnpm exec vue-tsc --noEmit`
- `pnpm build`
- staged `scripts/pre-commit-check.sh`
- 待补：具备认证后端会话的 Chromium daylight/night、desktop/mobile 浏览器验证。
  本地 `browser-use` session RPC 超时，Playwright Chromium 安装不可用，未将其视为通过。

## Rollback

代码回滚可直接 revert 本次提交。
