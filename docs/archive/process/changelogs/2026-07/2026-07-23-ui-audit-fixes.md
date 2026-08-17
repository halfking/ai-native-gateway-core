# 2026-07-23 — UI 审计修复

## 修复内容

### 第一轮（b8dd2d21e）
- 将页面级 `isActivated` 传入 `UpdateActivateLicenseCard`。
- 已激活节点不再在 License 异常状态提示中显示"离线激活"链接。
- 删除 AppTopbar 中已无对应 DOM 的 chevron CSS。
- 将用户名称触发器宽度上限恢复为 `min(28vw, 200px)`，避免窄屏下过早截断。
- 移除 License 卡中的 CSS token fallback，统一使用语义 token。

### 第二轮（可访问性增强）
- AppTopbar 组触发按钮添加 `aria-haspopup="menu"`，完善菜单导航的 ARIA 标注。
- License 卡 hint 提示文字颜色从 `var(--kx-warning)` 改为 `var(--kx-text)`，确保 night 主题下的对比度符合 WCAG AA 标准。

## 验证

- `pnpm vue-tsc --noEmit`：通过。
- `pnpm build`：通过。
- `go test ./domains/streaming -run 'TestResponses|Test.*Response' -count=1`：通过。
- Playwright 激活态模拟：`offlineLinkCount=0`，激活表单和 warning 区域均隐藏。
