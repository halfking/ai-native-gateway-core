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
- 2026-08-16：245 认证 Chromium 在 build `#1559` 中实测 Credential Monitor 模型详情。
  desktop daylight/night 均显示 `available` 和 `offer_missing`，状态文案、tooltip、
  token 计算色和页面横向溢出检查正常；相关 API 请求完成且无浏览器错误。
- 证据：`ui-verify-status-badge-daylight-desktop-detail-20260816-015200.png` 与
  `ui-verify-status-badge-night-desktop-detail-20260816-015500.png`，保存在系统临时目录。
- 未将五态标为全部通过：245 当前数据未暴露 `manual_disabled`、`probe_broken` 或
  `binding_missing`；认证浏览器的 CDP 端点不支持移动 viewport 覆盖，故 mobile 验证待
  可用测试会话补充。

## Audit Follow-up

- 245 无感部署在干净 worktree 中因缺少 `web/node_modules` 而在 Vite 配置加载前失败。
  部署脚本现在仅在依赖目录缺失时运行 `npm ci`，使用现有 `web/package-lock.json` 后再构建。
- 修复后 245 release `1559-a2822717` 完成 bundle 校验、原子切换、`/healthz` 和数据库
  就绪检查；回滚入口保持 `bash scripts/deploy-seamless.sh rollback 245`。

## Rollback

代码回滚可直接 revert 本次提交。
