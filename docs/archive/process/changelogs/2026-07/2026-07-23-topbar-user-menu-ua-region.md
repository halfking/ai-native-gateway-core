# 2026-07-23 — 顶部导航用户菜单精简 + 更新与激活浅色块隔离

## 变更摘要

1. **顶部导航用户菜单精简**：trigger 只显示用户名（角色/角色徽章/角色徽标挪到下拉里的 header）。这样 navbar 占用宽度收窄（用户角色与 chevron 不再并排占用一格）。
2. **去掉导航栏上菜单中的 chevron ▾**：组触发按钮（如「我的服务 / 模型与路由 / 数据运维 / 接入指南 / 对话 / 运维中心」）不再显示右侧向下三角箭头。`UserMenuDropdown` 同步移除 chevron（用户头像/角色下拉入口已通过按钮 `aria-haspopup` 表达）。
3. **`/customer/update-activate` 浅色块隔离**：5 个区域分别包一层 `.ua-region--{info|success|warning|primary|neutral}` 容器，使用 `color-mix` 与 `--kx-*` 语义色 token 混合出浅色背景 + 边框。暗色主题沿用同一套 token 自动适配。
4. **`/customer/update-activate` 离线激活按钮条件渲染**：已激活场景下 (`isActivated === true`) 隐藏 "离线激活" 链接，避免给已激活用户展示冗余入口。

## 关键文件

- `web/src/components/shell/UserMenuDropdown.vue`
  - trigger: 删除 `roleLabel` / `chevron` 显示，保留 `displayName`，新增 `:title` 把角色名放到 hover tooltip
  - 下拉: 新增 `.user-menu__header` 区段（含 `user-menu__header-name` / `user-menu__header-role`），下方再列「个人信息 / 修改密码 / 退出登录」
  - CSS: `.user-menu__trigger` 简化为单行；`.user-menu__role` 标记 `display: none`（保留向后兼容的 class）
- `web/src/components/shell/AppTopbar.vue`
  - 删除组触发按钮内的 `<span class="app-topbar__chevron" aria-hidden="true">▾</span>` 元素
  - CSS: `.app-topbar__chevron { display: none }` 兜底（防止下游子组件或回退渲染再次出现）
- `web/src/views/lifecycle/UpdateActivateView.vue`
  - 5 个 el-card 外层各包一个 `.ua-region--<variant>` 容器
  - "离线激活" 链接加 `v-if="!isActivated"`
  - 新增 CSS: 5 个变体的浅色背景 + 边框，使用 `color-mix(in srgb, var(--kx-*) N%, var(--kx-surface))` 公式

## 验证

- `pnpm vue-tsc --noEmit`：✅ 0 errors
- `pnpm build`：✅ 8.45s 成功
- 浏览器实测（vite preview + 模拟 localStorage 用户态）：
  - 导航栏用户信息「超级管理员」在最右；下拉点击后 header 显示「超级管理员」+ 角色，下方有「个人信息 / 修改密码 / 退出登录」
  - 导航栏菜单项（我的服务 / 模型与路由 / ...）均无 chevron
  - `/customer/update-activate` 5 块卡已激活态下显示不同浅色块（info/primary 用蓝色、success 用绿色、warning 用黄色、neutral 用底色）；"离线激活"按钮在未激活场景下显示，已激活场景下自动隐藏

## 全局 CSS 排查

近期全局变更 `ecd3846c4` 引入 `.btn::after { content: '→' }` 为激活流程按钮统一加尾箭头。本排查确认：
- 该规则**不**影响 navbar（navbar 按钮均使用 `app-topbar__link` / `user-menu__trigger` 而非 `.btn`）
- 入口按钮（如「登录」/「同意协议并激活」/「打开公网激活站点」）有箭头属于设计意图
- 不希望带箭头的按钮统一加 `.btn-no-arrow` 即可关闭（已在 `OfflineActivationView` / `UpdateActivateView` / `OperationAgreementDialog` 等位置落实）

navbar 内的"右箭头"实际是 chevron `▾`（BLACK DOWN-POINTING SMALL TRIANGLE, U+25BE），已被本次提交移除。
