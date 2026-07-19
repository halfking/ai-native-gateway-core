# 2026-07-20 — 路由面板暗色主题 + WorkType 意图识别 UI

## 变更

### 路由抽屉与面板暗色主题（修复 `#fff` 透传）

路由抽屉 / 面板 / 任务类型栏此前使用 `var(--bg-card, #fff)`、`var(--text-muted, #6b7280)`、
`var(--border, #e5e7eb)` 等占位符，这些 token 在本项目 `src/style.css :root` 中**根本未定义**，
夜态全部 fallback 到 `#fff` / 浅灰 → 抽屉在深色背景下亮成白块。

**实际项目 token（`src/style.css:3-17`）：**`--bg`、`--card`、`--bg-subtle`、`--border`、`--text`、`--muted`、`--accent`、`--accent-h`、`--success`、`--warning`、`--danger`、`--radius`。

**kx-design token（`--kx-bg-card`、`--kx-text-muted` 等）** **未在本项目加载**
（`@kaixuan/design` 不是 `package.json` 依赖，`src/` 内零引用），不能用。

替换映射：

| 旧 | 新 |
|---|---|
| `var(--bg-card, #fff)` | `var(--card, #1c2128)` |
| `var(--text-muted, #6b7280)` / `#9ca3af` | `var(--muted, #8b949e)` |
| `var(--border, #e5e7eb)` / `#d1d5db` | `var(--border, #30363d)` |
| `var(--bg-muted, #f3f4f6)` / `#f9fafb` | `var(--bg-subtle, #161b22)` |
| `var(--text, #1f2937)` / `#111` | `var(--text, #e6edf3)` |
| `var(--bg-accent-soft, #eef2ff)` + `var(--border-accent, #c7d2fe)` | `rgba(99,102,241,.18~.2)` + `var(--accent, #6366f1)` |
| `rgba(15, 23, 42, 0.4)` overlay | `rgba(0, 0, 0, 0.45)` |

涉及文件：

- `web/src/components/routing/SmartRoutingConfigDrawer.vue` — 抽屉背景、head 分隔、hint/close 文本色、overlay 透明度
- `web/src/components/routing/SmartRoutingConfigPanel.vue` — `.panel-body` 容器、subtitle、empty 文本
- `web/src/components/routing/DefaultRoutingDetailDrawer.vue` — 抽屉背景、head/foot 分隔、label/input 文本与边框
- `web/src/components/routing/TierGroupList.vue` — `.tier-group` / `.row-item` / `.profile-seg` / `.add-panel` / `.scope-chip` / `.seg.active`
- `web/src/components/routing/TaskTypeRail.vue` — 主色 / hover / active / badge / 移动端分隔

### TaskTypeRail L1 任务类型迁移到 api-work-types

`TASK_TYPES`（位于 `api-autoroute.ts`，硬编码 8 个 L1 类型 + emoji）与
`L1_TASK_TYPES`（位于 `api-work-types.ts`，仅 label，无 icon）存在重复，
且前者语义已经过时（WorkType 体系接管）。

**做法：**

1. `web/src/api-work-types.ts` `L1_TASK_TYPES` 升级为 canonical source：每项增加 `icon` 字段。
2. `web/src/components/routing/TaskTypeRail.vue` 从 `TASK_TYPES` 切到 `L1_TASK_TYPES`，包一层
   `const tasks = computed(() => L1_TASK_TYPES)` 便于未来动态加载。
3. `api-autoroute.ts` 的 `TASK_TYPES` 暂留（`RoutingDashboardView.vue` 还在用），未删除避免破坏性变更。

### WorkTypesView 意图识别（Intent Recognition）UI 强化

`WorkTypeConfig.prompt_keywords`（string[]）此前是 1 个逗号分隔 `<input>`，
既不直观也不能表达"意图识别"的语义。`acc_task_type`（ACC 端 task_type 映射）虽在
后端 schema 与 API 里支持，但前端从未暴露。

**新增 `detail-section--intent` 卡片（IR 标签 / 琥珀色），独立于"基本配置"：**

- **关键词 chip 编辑器**：每个 keyword 是一个 `kw-chip`（带 × 按钮移除），
  输入框按 `Enter` / `,` / 中文逗号 `，` 触发添加；空 chip 自动 trim + 去重；
  `Backspace` 在空输入框时删除最后一个 chip；`blur` 自动 flush 待提交输入。
  顶部显示 `{n} 个关键词` 计数 + intent-hint 提示文案。
- **`acc_task_type` 文本框**：可选，保存时空字符串 → `null`，非空 → 字符串。
  带 placeholder 与 hint 解释 ACC 集成映射的语义。
- **保存按钮独立放卡片底部**（basic 与 intent 各有独立 save，符合"卡片内原子保存"原则）。

**create modal 同步增强：**

- 新增 `acc_task_type` 字段（与 detail 字段语义一致）。
- `prompt_keywords` 维持逗号输入（modal 里空间有限，不引入 chip UI 复杂度）。

**脚本侧：**

- `syncDetailForm` 复制 `prompt_keywords` 为 `[...arr]`（避免引用共享）；
- `saveDetailMeta` 在 `keywordInput` 还有内容时先 `addKeyword()` 再保存，确保不留 dirty 输入；
- `saveCreate` / `saveDetailMeta` 把 `acc_task_type` 当 nullable 发送。

新增 8 个 i18n key（`zh-CN/workTypes.ts`，命名空间 `workTypes.detail`）：

- `intentRecogTitle`、`intentRecogHint`、`promptKeywordsPlaceholder`、`keywordCountLabel`
- `accTaskTypeLabel`、`accTaskTypePlaceholder`、`accTaskTypeHint`、`removeKeyword`

## 验证

- `pnpm build` 通过（9.56s，dist 体积与改动前持平）
- `pnpm run i18n:check` 仅剩 6 个 **pre-existing** 缺失 key（在 `RoutingDashboardView.vue`/`RoutingDefaultsView.vue` 内，
  与本次改动无关）
- 新增 i18n key 全部命中 `zh-CN/workTypes.ts`，未触发 missing 警告

## 风险

- `L1_TASK_TYPES` 从 2 列扩到 3 列（多 icon）。`WorkTypesView.vue:466/634` 的 `<select>` 用法
  只读 `t.key` 与 `t.label`，不受影响。
- `detail-section--intent` 引入第三张 detail 卡片，`.detail-grid` 是 2 列自适应；
  视觉上 basic + intent 在左列，routes 在右列。无空白行问题（`align-items: start`）。
- 并发冲突：本次会话期间有另一位 ACC Agent 在同 workspace 操作（`stash@{1}` "WIP: web UI and dbdegradation work - before rebase"），
  多次触发 stash → 改回滚循环。最终从 stash 恢复全部 8 文件改动并继续。