# 2026-07-20 — Gateway 运维导航对齐 /maintain/*

## 背景

`ai-native-maintain` 已接管运维 SPA（`/maintain/ops/*`）。Gateway 侧栏若仍用 Vue `RouterLink` + History `redirect` 指向 `/maintain/*`，不会加载另一套 SPA，会落入 Gateway catch-all。

## 变更

- `NavItem.external`：运维/租户 License 等项改为 `<a href="/maintain/...">` 全页跳转
- legacy `/ops/*`、`/download`、`/activate` 等用 `window.location.replace` 跳到 `/maintain/*`
- `/ops/vibecoding` 仍留在 Gateway SPA
- 分发文档公开 URL 改为 `/maintain/download`、`/maintain/offline-activation`

## 验证

- `npx vitest run src/config/appNav.test.ts` — 4 passed
- `npm run build` — OK
