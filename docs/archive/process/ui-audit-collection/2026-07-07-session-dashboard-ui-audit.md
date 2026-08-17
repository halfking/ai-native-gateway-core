# UI 审计报告：会话管理 + 首页 Dashboard（2026-07-07）

> **范围**：UI/UX 层（视觉、交互、布局、状态、响应式、a11y）
> **不重复**：P0/P2 已做的代码逻辑/i18n 键/类型契约审计（commit `7c18c68e`/`04b59d1c`/`0c690b5f`）
> **项目**：`llm-gateway-go-4`，分支 `main`，HEAD `0c690b5f`

---

## 0. 首页现状厘清（首要疑点）

**结论**：✅ 三套首页 **不是死代码**，而是**版本切换系统**。

```
HomeView.vue (登录态分流 → DashboardView)
  └── DashboardView.vue (统一入口，按 version 切换)
        ├── V2 (默认) → DashboardViewV2.vue    [新版紧凑统计 + 泳道]
        ├── V1 (旧)   → DashboardViewLegacy.vue [遗留版]
        └── 非默认租户 → TenantDashboardView.vue
```

`DashboardView.vue` 第 33 行 `version = ref<'v1' | 'v2'>('v2')`，localStorage 持久化，V1/V2 互切。

**未提交改动核对**：交接文档说"4 个 web 文件未提交改动"，实际**没有**。
当前工作区未提交的是后端 Go + SQL（`admin/session_analytics_handler.go` / `session_tenant.go` / `357_*.sql` / `358_*.sql`），
**与本次 UI 审计无关**。

---

## 1. P0 — Critical（阻塞用户、信息丢失、可访问性）

### 1.1 SessionManagementView.vue — 全文件无 i18n（用户用其他语言看全是 key 或硬编码中文）

**位置**：`web/src/views/SessionManagementView.vue`（572 行）
**严重程度**：🔴 **P0**

| 行 | 问题 |
|---|---|
| 第 5 行 | `import { useI18n }` 但**完全没用到**，死代码 |
| 第 56-61 行 | 6 个状态标签 "运行中 / 已停止 / 已恢复 / 等待中 / 异常 / 已过期" **全是硬编码中文** |
| 第 66-70 行 | 健康等级 A-F 中文 label **硬编码** |
| 第 116 / 234 / 249 行 | 错误提示 `'Failed to load sessions'` / `alert('停止会话失败...')` / `alert('恢复会话失败...')` **英文/中文硬编码** |
| 第 222 行 | `confirm('确定要停止此会话吗？')` — **原生 confirm，阻塞、丑、不可 i18n、不可测试** |
| 第 264-267 行 | `'刚刚' / '${minutes}分钟前' / '${hours}小时前' / '${days}天前'` **硬编码中文** |
| 第 295 / 297 行 | 标题"会话管理"和"刷新"按钮 **硬编码中文** |
| 第 313-323 行 | 11 列表头（状态/标题/Session ID/租户/轮次/费用/Tokens/当前模型/健康等级/最后活跃/操作）**全部硬编码** |
| 第 351 行 | `'(未命名)'` 硬编码 |
| 第 380 / 387 / 394 行 | 操作按钮"详情/停止/恢复" **硬编码** |
| 第 403 行 | "暂无会话" 硬编码 |
| 第 411 / 418-468 行 | 详情模态框标题/标签 **全部硬编码** |
| 第 491 / 519 行 | "凭据轮换历史" / "进行中" 硬编码 |
| 第 543 行 | "关闭" 硬编码 |

**结果**：当用户切换到德语/日语/阿拉伯语等 6 种语言，这个页面**完全无法翻译**——这是 v2.1 重要模块的核心页面，体验断裂。

### 1.2 SessionManagementView.vue — 19 处 `catch { console.error(...) }` 静默吞错

| 行 | 函数 | 风险 |
|---|---|---|
| 140-142 | `refreshSessionRow` | 单行刷新失败，用户无感知 |
| 184-186 | SSE `onmessage` | SSE 事件解析失败 |
| 189-191 | SSE `onerror` | **SSE 断流无提示**，用户以为系统在更新 |
| 216-218 | `viewDetail` 凭据轮换加载 | 详情页某部分空白，无解释 |
| 233-235 | `stopSession` 用 `alert()` | 见 1.1 第 234 行 |

**严重性**：在 v2.1 super-admin 页面上，"静默吞错 + alert/confirm" 是最差的错误处理 UX 模式。

### 1.3 SessionStatsPanel.vue — 错误吞错

**位置**：`web/src/components/SessionStatsPanel.vue` 第 26-30 行

```ts
} catch (e) {
  console.error('Failed to load session overview:', e)
}
```

首页核心组件，**会话统计加载失败时用户什么都看不到**——卡片显示 0、空表格、用户以为是数据问题。

### 1.4 SessionListView.vue — 无 i18n、硬编码中文

**位置**：`web/src/views/SessionListView.vue`（138 行）

| 行 | 硬编码中文 |
|---|---|
| 第 5 行 | 标题"会话列表" |
| 第 6 行 | 副标题 `按 {{ hours }}h 时间窗口展示` |
| 第 16 行 | "刷新" 按钮 |
| 第 20 行 | "加载中..." |
| 第 25-27 行 | "会话总数 / 已压缩 / 当前页" |
| 第 35-42 行 | 8 列表头 |
| 第 49 行 | `s.model_used \|\| '—'` |
| 第 56 行 | "已压缩 / 未压缩" |
| 第 60 行 | 成功率阈值 `>= 90` 颜色硬编码 |
| 第 65 / 66 行 | "对比 / 日志" 链接 |
| 第 73 行 | "暂无会话数据" |
| 第 74 行 | "会话在请求日志产生 `gw_session_id` 后自动聚合显示" |
| 第 79 行 | "共 X 条，Y 页" |
| 第 82 行 | "上一页 / 下一页" |

---

## 2. P1 — Major（体验问题、信息架构、视觉不一致）

### 2.1 三套首页并存 → 维护混乱（虽非死代码但有重叠）

`DashboardViewV2.vue` 已实现：
- 单行紧凑统计（9 个指标）
- 后台任务横幅
- SessionStatsPanel
- LiveRequestStreamV2 泳道
- StatsDrawer / RequestLogDrawer

`DashboardViewLegacy.vue` 仍维护的：
- 多区域仪表盘（含历史图表）
- TenantDashboardView 入口

**问题**：
- 版本切换器让用户在 V1/V2 之间切，造成视觉/行为不一致
- 两个页面都展示相似的统计指标，UX 不确定走哪个版本

**建议**：
- 短期：保留切换器（已在用），但 V2 应明显标记"推荐"
- 中期：评估 Legacy 流量，半年后移除（但本次不删）

### 2.2 DashboardViewV2.vue — SessionStatsPanel 错误态黑洞

`DashboardViewV2.vue` 第 263 行 `<SessionStatsPanel style="margin-bottom: 20px;" />`
- SessionStatsPanel 的 `catch` 静默吞错 → 用户看到的是空数据或 0
- 无重试按钮
- 无 loading skeleton（v-loading 是 Element Plus 的默认遮罩，非骨架屏）

### 2.3 视觉一致性 — 各页面用不同风格 token

| 文件 | 风格 token | 备注 |
|---|---|---|
| `DashboardViewV2.vue` | `var(--bg-subtle)`、`--bg`、`--border`、`--accent`、`--success`、`--warning` 等深色 token | ✅ 完整 |
| `DashboardViewLegacy.vue` | 待读（推测是 Element Plus 默认） | ⚠️ |
| `SessionAuditView.vue` | 硬编码 `#111`, `#666`, `#e5e7eb`, `#3b82f6` 等 | ❌ 完全脱离 token |
| `SessionListView.vue` | 用 `var(--card)`, `var(--accent)`, `var(--muted)` | ⚠️ 部分 token |
| `SessionManagementView.vue` | 用 Tailwind/DaisyUI 类 `bg-error`, `badge-success` 等 | ⚠️ 风格不同 |
| `SessionAnalyticsDashboardView.vue` | `padding: 20px; background: #f5f7fa` 硬编码 | ⚠️ 浅色背景 |
| `SessionPanoramaView.vue` | 用 Element Plus 卡片 + 硬编码颜色 | ⚠️ |
| `SessionConfigView.vue` | 硬编码 `#303133`, `#909399`, `#e4e7ed`, `#409eff` | ❌ |

**结论**：8 个会话模块页面 + 3 套首页，**至少 3 种风格系统并存**（深色 token / 浅色硬编码 / DaisyUI）。

### 2.4 加载态：5 种风格

| 页面 | 加载态 |
|---|---|
| `DashboardViewV2.vue` | `.stat-mini--skeleton` 骨架屏（9 个） |
| `DashboardViewLegacy.vue` | 待确认 |
| `SessionStatsPanel.vue` | `v-loading` Element Plus 遮罩 |
| `SessionListView.vue` | `<div>加载中...</div>` 纯文字 |
| `SessionAuditView.vue` | `<td class="loading-cell">加载中</td>` 表格行 |
| `SessionManagementView.vue` | `<span class="loading loading-spinner loading-lg">` DaisyUI spinner |
| `SessionPanoramaView.vue` | `<el-icon class="is-loading">` + 文字 |
| `SessionAnalyticsDashboardView.vue` | 9 个独立 loading flag 传给子图表组件 |

**结论**：**没有统一的加载态规范**。骨架屏只在 V2 首页用。

### 2.5 错误态：4 种风格

| 页面 | 错误态 |
|---|---|
| `DashboardViewV2.vue` | `<div class="alert alert-danger">{{ error }}</div>` |
| `SessionStatsPanel.vue` | **无 UI 错误提示**（catch 静默吞） |
| `SessionListView.vue` | `<div v-if="error" class="alert alert-danger">` |
| `SessionAuditView.vue` | `<div v-if="error" class="error-banner">` |
| `SessionManagementView.vue` | `alert('停止会话失败...')` / `console.error` |
| `SessionPanoramaView.vue` | `<el-result icon="error" title="加载失败" sub-title="...">`  + 重试按钮 ✅ |
| `SessionAnalyticsDashboardView.vue` | `ElMessage.error(...)` 顶部 Toast ✅ |

**结论**：4 种风格（alert banner / 原生 alert / Toast / 静默吞）。**SessionPanorama 是最佳实践**。

### 2.6 空状态：3 种

| 页面 | 空状态 |
|---|---|
| `DashboardViewV2.vue` | 统计行 `v-if="summary && overview"` 直接隐藏 |
| `SessionListView.vue` | `暂无会话数据` + 解释文案 ✅ |
| `SessionAuditView.vue` | `<td colspan="13" class="empty-cell">{{ t('sessions.audit.empty') }}</td>` |
| `SessionManagementView.vue` | `暂无会话` + 图标 |

**结论**：空状态文字均硬编码中文（除 SessionAuditView 已 i18n）。

### 2.7 模态框缺失无障碍

| 页面 | 模态框 | 问题 |
|---|---|---|
| `SessionAuditView.vue` | 自制 modal-overlay | ✕ 关闭按钮无 `aria-label`、无 focus trap、ESC 不能关闭 |
| `SessionManagementView.vue` | `<dialog>` 元素 | ✕ 用原生 `<dialog>` 但配合 `class="modal"`（DaisyUI）— Element Plus 项目里不一致 |
| `SessionPanoramaView.vue` | `<el-dialog>` | ✅ 自动 focus trap，但模态按钮 `<el-button text @click="tagDialogVisible = true">+ 添加</el-button>` 中文硬编码 |

---

## 3. P2 — Minor（响应式、可维护性）

### 3.1 响应式断点不统一

| 文件 | 断点 |
|---|---|
| `DashboardViewV2.vue` | `@media (max-width: 1024px)` + `(max-width: 768px)` ✅ 完整 |
| `DashboardViewLegacy.vue` | 待确认 |
| `SessionAuditView.vue` | **无**（表格 13 列在窄屏必然溢出） |
| `SessionListView.vue` | **无** |
| `SessionManagementView.vue` | 用 Tailwind `overflow-x-auto`，**无自定义断点** |
| `SessionPanoramaView.vue` | `<el-row :gutter="16">` 6 个 el-col :span="4" — 窄屏全挤一行 |
| `SessionAnalyticsDashboardView.vue` | `@media (max-width: 768px)` 改 padding ✅ |

**关键 bug**：SessionPanoramaView 第 18-35 行 6 个 `<el-col :span="4">` 在 <768px 不会自动堆叠（`el-col` 默认 `xs` 不设置 = 24 不生效？需确认）。建议显式加 `:xs="24" :sm="12" :md="8"`。

### 3.2 i18n 长文本溢出风险

- 德语（如 "Sitzungsmanagement - Aktive Sitzungen"） 比中文长 ~40%
- 阿拉伯语 RTL：SessionAuditView 13 列表格用 `<th>` 无 `dir` 属性
- DashboardViewV2 第 219 行 `总请求数 / 总Token / 总费用` 等 label — 德语可能撑破 100px `.stat-mini { min-width: 100px }`

### 3.3 健康度评分混用 A-F 和 0-10

| 页面 | 显示方式 |
|---|---|
| `SessionStatsPanel.vue` | A/B/C/D/F 徽章（与新架构 0703 "分数 0-10" 文档不一致） |
| `SessionAuditView.vue` | 0-10 整数 + 颜色阈值 (>=8 绿, 6-7 黄, <6 红) ✅ 与文档一致 |
| `SessionManagementView.vue` | A-F 徽章 + 括号里 `({{ selectedSession.health_score }}/100)`（**注意是 /100！**） |
| `SessionPanoramaView.vue` | `health_score` |
| `SessionAnalyticsDashboardView.vue` | `avg_health_score` + `gradeDistribution` |

**结论**：**健康度分数制度不一致**——A-F 还是 0-10 还是 0-100？**这是一个数据契约 + UI 不一致问题**，但用户感知是"为啥同一个东西显示不同"。

> 注：交接文档说"后端分数统一为 0-10"，但 SessionStatsPanel 和 SessionManagementView 仍显示 A-F。
> 这是 **P0 级数据契约不一致**，建议老板确认后端到底是哪个分数。

---

## 4. P3 — Nit（代码质量、可维护性）

### 4.1 直接 `fetch` 而非走 `api` 工具

| 文件 | 行 | 用法 |
|---|---|---|
| `SessionManagementView.vue` | 107, 125, 161, 209, 225, 240 | 全部用 `fetch()` + `credentials: 'include'`，绕开统一 axios 客户端 |
| `SessionListView.vue` | 91 | ✅ 走 `getSessionList()` |

**影响**：拦截器、错误处理、JWT 注入都不一致。

### 4.2 `<dialog>` 元素 + DaisyUI class + Vue 项目

`SessionManagementView.vue` 第 408 行用 `<dialog :open="showDetail">` + DaisyUI `class="modal modal-box"` — 但其他页面用 Element Plus。这造成视觉/交互/可访问性分裂。

### 4.3 硬编码 `0c690b5f` 注释提到 "0-10" 但实际还是 A-F

`SessionAuditView.vue` 第 10 行注释 `// 后端分数均为 0-10` ✅
但同一文件第 142 行 `severity` 是数字 0-10，**其他 4 个页面用 A-F**——注释与代码/UI 不一致。

---

## 5. 修复优先级清单（按"小步提交 + ROI"排序）

| # | 优先级 | 文件 | 修改 | 预计行数 |
|---|---|---|---|---|
| 1 | **P0** | `SessionManagementView.vue` | 用 `t()` 替换 30+ 处硬编码中文，移除 `alert()`/`confirm()` 改用 `ElMessage`/`ElMessageBox`，加 i18n key | ~80 行 |
| 2 | **P0** | `SessionStatsPanel.vue` | 错误态改为 `ElMessage.error()` + UI 显示空状态 | ~10 行 |
| 3 | **P0** | `SessionListView.vue` | 用 `t()` 替换硬编码中文 | ~30 行 |
| 4 | **P1** | `SessionStatsPanel.vue` | 加 i18n key 到 6 语言 locale 文件 | ~120 行（每语 ~20） |
| 5 | **P1** | `SessionManagementView.vue` | 把 9 处 `catch console.error` 改成 UI 提示 + ElMessage | ~20 行 |
| 6 | **P1** | 抽 `PageHeader.vue` 组件 | 统一标题/副标题/操作区结构 | ~50 行新文件 |
| 7 | **P1** | 抽 `FilterBar.vue` 组件 | 统一筛选区（参考 `DashboardFilterBar`） | ~80 行新文件 |
| 8 | **P1** | `SessionAuditView.vue` | `bg: white` → `var(--bg)` 等颜色 token 化 | ~30 行 |
| 9 | **P2** | `SessionPanoramaView.vue` | 加 `el-col :xs="24" :sm="12" :md="8"` 显式响应式 | ~6 行 |
| 10 | **P2** | 8 个会话页面 | 加 `@media (max-width: 768px)` 移动端适配 | 每页 ~10 行 |
| 11 | **P2** | SessionAuditView 模态框 | 加 ESC 关闭 + focus trap + aria-label | ~15 行 |
| 12 | **P3** | `SessionManagementView.vue` | 改用 `api/admin/sessions` 工具替代直接 fetch | ~20 行 |

**总计**：~470 行改动，分布在 13 个文件，可拆为 **5 个 commit**：

| Commit | 内容 |
|---|---|
| `fix(i18n): SessionManagement + SessionList 加 i18n` | #1, #3 |
| `fix(ui): SessionStatsPanel 错误态提示` | #2 |
| `feat(ui): 抽 PageHeader 通用组件` | #6 |
| `feat(ui): 抽 FilterBar 通用组件` | #7 |
| `fix(ui): 会话模块颜色 token 化 + 响应式 + a11y` | #5, #8, #9, #10, #11 |

---

## 6. 不在本次范围（明确排除）

- ❌ DashboardViewLegacy.vue 删除评估（需业务确认流量）
- ❌ 健康度分数制度统一（**需老板确认后端契约是 A-F / 0-10 / 0-100**）
- ❌ DashboardViewV2 的 9 卡片骨架屏优化（已很好）
- ❌ `LiveRequestStreamV2` 内部组件（不在 8 个会话页面清单）
- ❌ 真正的浏览器实测（rule 11 §6 强制要求，但本次为本地代码审计，需老板部署到 test 环境后再做）

---

## 7. 风险与阻断

- ⚠️ **健康度分数制度**未确定前，#1/#3 的 i18n 修复可能涉及数据契约变更
- ⚠️ **DaisyUI + Element Plus** 混用（`SessionManagementView.vue`） — 抽组件时是否统一到 Element Plus？
- ⚠️ **生产环境 UI 实测** — 按 rule 11 §6，需要老板在测试环境验证后再合并
- ⚠️ **当前工作区有未提交 Go/SQL 改动**（与本次 UI 任务**无关**），不会冲突，但提交时需注意

---

**审计完成时间**：2026-07-07 02:30 UTC+8
**审计人**：老板 + AI 助手
**下一步**：等老板确认健康度分数制度（D-F vs 0-10 vs 0-100）+ 是否允许拆 5 个 commit