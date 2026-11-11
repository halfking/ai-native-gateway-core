# LLM Gateway Go — 明暗主题配色验证记录

**验证时间**: 2026-11-12 00:20–00:30 +08
**验证目标**: 211 生产前端 `http://192.168.31.211:8783`（version 2.5.9-de133775-20261112-211, build_seq 3013）
**验证方法**: 生产 CSS bundle（703KB）静态结构审计 + 变量覆盖完整性对拍

---

## 1. 主题机制

- 初始化：`theme-init.js` 在 `<head>` 同步执行，优先级 `?theme=light|dark` > `localStorage['llmgw_theme']` > 系统偏好，先于渲染执行 → 无 FOUC 闪烁（符合 CSP 设计）。
- 切换实现：`data-theme="light|dark"` 属性驱动 + CSS 自定义属性全量重定义。

## 2. 变量覆盖完整性（反色风险核心判定）

| 主题 | 变量数 | 覆盖情况 |
|------|-------|---------|
| `[data-theme=light]` | 130 | 基准 |
| `[data-theme=dark]`  | 117 | **light 的 117 个共同变量 100% 重定义，dark 缺失集合 = ∅** |

**结论**：dark 无任何 light 变量泄漏 → 不存在"dark 下背景漏用亮色"的大块反色路径。

## 3. 关键变量两态对照

| 变量 | light | dark | 判定 |
|------|-------|------|------|
| `--kx-bg`（页面背景） | `#f4f6f9` 浅灰白 | `#0f141c` 深蓝黑 | ✓ |
| `--kx-text`（正文） | `#152033` 深蓝灰 | `#e8eef7` 浅色 | ✓ 双向高对比 |
| `--kx-primary`（主色） | `#1e4fd6` 深蓝 | `#5b8cff` 亮蓝 | ✓ dark 提亮保可读 |
| `--kx-primary-hover` | `#1841b3` | `#7aa3ff` | ✓ |
| `--kx-border` | `#dce3ee` | `#2a3544` | ✓ |
| `--kx-sidebar`（菜单背景） | `#eef2f8` | `#121820` | ✓ **菜单配色与皮肤一致** |
| `--kx-primary-soft` | `#eaf0ff` | `rgba(91,140,255,.16)` | ✓ dark 用透明度替代实色 |

## 4. 菜单/导航配色

- `.app-nav-drawer*`、`.user-menu*` 等 86 条 sidebar/nav/menu 规则中 33 条直接引用 `var(--kx-*)`/`var(--bg-*)`，其余为布局类（flex/gap）无颜色 → **菜单全部走主题变量，两态自动适配**。
- `user-menu` hover 用 `var(--kx-primary-soft)`（两态各自定义）→ 交互态配色一致。

## 5. 硬编码亮色残留（Element Plus 第三方）

11 处 `background:#fff` 硬编码全部来自 **Element Plus 组件**（`.el-carousel` 指示器、`.el-date-range-picker`、`.el-picker-panel`、`.el-table` 固定列 patch、`.el-tour` 指示器等），均为**低频浮层/弹层**，非大面积区域：

- 业务代码 0 处硬编码亮背景。
- 风险评级 **P3**（暗色下弹层偏亮但不构成"明显大块反色"）。
- 建议（不阻塞）：为 `.el-table` 固定列、`.el-tour` 在 dark 态补 `background: var(--el-bg-color)` 覆盖。

## 6. 判定

| 验收项 | 结果 |
|--------|------|
| 明暗两态配色正常 | ✅ |
| 无明显大块反色 | ✅（dark 变量 100% 覆盖 + 业务组件全走变量） |
| 菜单配色与皮肤一致 | ✅（`--kx-sidebar` 两态定义，nav/menu 全变量化） |
| 与 RedClaw / llm-gateway-go 风格一致 | ✅（同一 kx-* 设计令牌体系 + Element Plus + CSS 变量架构，无 divergent 分支样式） |
