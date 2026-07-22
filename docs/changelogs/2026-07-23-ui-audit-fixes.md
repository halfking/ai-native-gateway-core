# 2026-07-23 — UI 审计修复

## 修复内容

- 将页面级 `isActivated` 传入 `UpdateActivateLicenseCard`。
- 已激活节点不再在 License 异常状态提示中显示“离线激活”链接。
- 删除 AppTopbar 中已无对应 DOM 的 chevron CSS。
- 将用户名称触发器宽度上限恢复为 `min(28vw, 200px)`，避免窄屏下过早截断。

## 验证

- `pnpm vue-tsc --noEmit`：通过。
- `pnpm build`：通过。
- `go test ./domains/streaming -run 'TestResponses|Test.*Response' -count=1`：通过。
- Playwright 激活态模拟：`offlineLinkCount=0`，激活表单和 warning 区域均隐藏。
