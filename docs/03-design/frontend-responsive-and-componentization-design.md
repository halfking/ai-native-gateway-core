# 前端响应式适配（桌面端 / 移动端 / 折叠屏）与组件化架构方案

> 状态：草案（待评审） · 日期：2026-09-12 · 范围：`web/`（Vue 3 + Element Plus + Vite 管理控制台）
>
> **实施状态（2026-09-12）**：第一批组件已落地 — `components/ui/` 新增 `PaginationBar` / `PageHeader` / `StatCard`（均带 vitest 测试与响应式源码断言，断点统一 768/480px）+ `composables/usePagination`；`RequestLogsView` 双份手写分页栏与 5 个内联统计卡已替换，`KeysView` 页头已替换；`App.vue` 废弃 sidebar 死样式（约 386 行）与 `appNav.ts` 无调用方的 `readSidebarCollapsed/writeSidebarCollapsed` 已删除；`common.pagination` 词条补齐 8 语言（新增 `pageOf`/`perPage`）。验证：vue-tsc 0 错误、新增测试 34/34 通过、i18n 严格审计 0 missing（硬编码 CJK 较基线下降 838）。
>
> 结论先行：**技术上完全可行，推荐"渐进增强"路线**——先建立统一断点体系与响应式基础设施，再把管理壳层（顶栏导航）改造为"桌面顶栏 + 移动抽屉导航"，同时抽取 8 个通用组件收敛 60+ 视图的重复 UI；折叠屏适配基于 W3C 折叠屏标准（Viewport Segments / Device Posture API）做纯增量增强，不支持的浏览器自动降级为普通响应式，零风险。全量落地预估 31~48 人日，可按阶段交付、随时暂停。

---

## 目录

1. [现状分析](#1-现状分析)
2. [需求整理](#2-需求整理)
3. [业界调研](#3-业界调研)
4. [技术方案](#4-技术方案)
5. [实施计划](#5-实施计划)
6. [可行性评估](#6-可行性评估)
7. [风险与缓解](#7-风险与缓解)
8. [附录：参考资料](#8-附录参考资料)

---

## 1. 现状分析

> 数据基于 2026-09-12 对 `web/src` 的全量盘点（234 个 `.vue`、855 个 `.ts`、96 条路由）。

### 1.1 前端概况

| 维度 | 现状 |
|---|---|
| 技术栈 | Vue 3.5 + Element Plus 2.8（**实际使用率极低**）+ Vite 6 + TS 5.8 + vitest(jsdom) + vue-i18n（8 语言） |
| 应用壳层 | 三套并行：① 管理后台顶栏壳（`App.vue` + `src/components/shell/AppTopbar.vue`）② 访客落地页（`ServiceLandingPage.vue`）③ 公共门户页（`PublicPageShell.vue`，960px 居中） |
| 导航 | 顶栏两级菜单（2026-07-21 由 sidebar 迁移而来）：8 个分组 + primary，共 **50 个菜单项**，单一事实源在 `src/config/appNav.ts`；支持插件菜单、运维中心远端菜单两路运行时注入 |
| 布局形态 | 桌面优先的全宽流式布局，无固定宽度容器；`main-body` padding 24px（≤640px 降为 12px） |
| 设计令牌 | `src/style.css`（1016 行）有完善的 `--kx-*` 双主题颜色令牌 + 旧别名桥接层；**无 spacing / typography / 断点令牌** |
| 样式写法 | 221 个组件用原生 `<style scoped>`，仅 1 个用 `lang="scss"`（影响断点令牌策略选型，见 4.2） |
| Element Plus 引入 | CSS 全量引入（`main.ts`）；组件手动 named import，无全局注册、无 unplugin 自动导入；vite 已拆 `element-vendor` chunk |

### 1.2 响应式现状（量化）

| 指标 | 数据 | 评价 |
|---|---|---|
| `@media` 覆盖 | 71 个文件 / 100 处（views 覆盖率 36%，components 24%） | 补丁式局部适配 |
| 断点值 | **17+ 种不同值并存**（768/900/800/720/760/640/700/960/1024/480/1200/520/1100/680/600/1000/1600…） | 严重碎片化；768px 最高频但仅占 ~17% |
| 移动端导航 | 无汉堡菜单、无抽屉导航，仅有"菜单换行 + 横向滚动"（`AppTopbar.vue:603` ≤768px、`:629` ≤480px） | 50 个菜单项横向滚动，移动端可用性差 |
| JS 层响应式 | `matchMedia` 仅 1 处（主题）、`window.innerWidth` 4 处、`resize` 监听 5 处，全部局部一次性实现 | 无 `useViewport`/`useBreakpoint` composable，无 VueUse |
| 表格 | 60 个视图手写原生 `<table>`；6 个视图用 `el-table` 且 **0 个有移动端适配** | 最大盲区 |
| 弹层 | `el-dialog` 仅 3 文件 / 12 处；**32 个文件手写 `modal-overlay`**；`el-drawer` 全项目 0 处、18 个文件手写 drawer | 弹层体系完全自研但无统一封装 |
| 分页 | 10 个视图手写 `pagination-bar`，11 处自写分页状态；无 `usePagination` | 高度重复 |
| 响应式测试 | 仅 2 个（`AppTopbar.responsive.test.ts`、`LiveRequestStreamV2.responsive.test.ts`），均为"源码文本断言"模式（jsdom 无真实布局） | 有回归保护，无视觉验证 |
| viewport meta | `index.html:5` 标准配置 `width=device-width, initial-scale=1.0` | 合格 |
| 可访问性基础 | `prefers-reduced-motion` 13 处；RTL（postcss-rtlcss）完善 | 良好 |
| 方案文档 | docs/ 目录无任何响应式/移动端设计文档 | 空白 |

### 1.3 重复 UI 模式盘点（组件化机会）

项目存在一条清晰的**列表页骨架**在 40~60 个视图中重复：

```
page-header（标题 + 刷新/自动刷新按钮）
  → stat/summary 卡片行
  → filter-bar（select + input + 时间范围）
  → data-table（loading 行内联"加载中…"）
  → pagination-bar
  → modal-overlay / drawer × N（新增/编辑/详情内联）
```

| 模式 | 重复度 | 已有组件（采用率） |
|---|---|---|
| 页面头部 | **42 个视图**（126 处 `page-header`），31 个视图重复定义同名样式 | `PageBackLink`（仅 6 处） |
| 统计卡片 | 17 个文件 `stat-card` + 4 个文件 `summary-card` 两套平行实现，15 个视图重复定义样式 | 无 |
| 筛选栏 | 16 个视图手写 `filter-bar` | `FilterInput`（1 处）、`ActiveFilterChips`（3 处）、`useFilterChips` |
| 表格 | 60 个视图手写原生 table，loading/空态行内联重复 | `EmptyState`（仅 5 视图用，另有 12 视图手写空态） |
| 分页 | 10 个视图手写 `pagination-bar`（`RequestLogsView.vue` 在同一文件内复制两份） | 无 |
| 弹窗 | 32 个文件手写 `modal-overlay`（9 个视图重复定义样式、重复处理 z-index 堆叠） | `useConfirmDialog`（**26 个视图采用——已验证的收敛成功范例**） |
| 抽屉 | 18 个文件手写 drawer-mask/drawer-panel | 无 |

**最大的 10 个 `.vue` 文件**（组件化拆分的直接受益者）：

| 文件 | 行数 |
|---|---|
| `views/CredentialMonitorView.vue` | 2403 |
| `views/RoutingDashboardView.vue` | 2191 |
| `views/ModulesView.vue` | 2081 |
| `views/RequestLogsView.vue` | 2033 |
| `components/RouteIncidentDrawer.vue` | 1985 |
| `views/FreePoolView.vue` | 1958 |
| `components/QueuePerspectivePanel.vue` | 1835 |
| `views/ModelsView.vue` | 1813 |
| `views/ProvidersView.vue` | 1771 |
| `views/data-lifecycle/HotPartitionManager.vue` | 1612 |

### 1.4 已发现的技术债与隐患

1. **死代码**：`App.vue:294-639` 残留整套已不渲染的 sidebar CSS（~350 行）；`appNav.ts:270-292` 的 `readSidebarCollapsed/writeSidebarCollapsed`（localStorage `llmgw_sidebar_collapsed`）模板已无调用方。
2. **Element Plus 解析风险（已验证）**：`main.ts` 无全局注册、无 auto-import 插件，但 `views/ForbiddenView.vue:28-34`、`components/SessionSummaryBar.vue:115` 等 **14 个文件在模板中使用 `<el-card>/<el-button>` 等标签却未 import**——运行时 Vue 无法解析组件（退化为未知自定义元素 + 控制台告警，丢失 EP 渲染结构）。"全量 CSS + 按需组件"策略执行不一致。
3. **断点无治理**：17+ 种断点值无白名单约束，新增代码可继续随意硬编码。
4. **composables 缺口**：38 个 composable 均为业务向，缺 `usePagination` / `useBreakpoint` / `useDrawer` 类列表页与视口通用件。

---

## 2. 需求整理

### 2.1 功能性需求

| 编号 | 需求 | 说明 | 优先级 |
|---|---|---|---|
| R1 | 桌面端体验保持 | ≥1024px 维持现有顶栏布局与信息密度，**不引入视觉回归** | P0 |
| R2 | 移动端可用性 | <768px：汉堡菜单 + 抽屉式导航（承载 50 项菜单的分组折叠）；表格横向滚动或卡片化；弹窗全屏化；触摸目标 ≥44px；`safe-area-inset` 适配刘海/圆角 | P0 |
| R3 | 平板与折叠屏展开态 | 768~1023px（含折叠屏展开内屏竖态，≈平板宽度）：内容栅格 2 列化、间距收敛 | P1 |
| R4 | 折叠屏增强 | ① 折叠态外屏（~360px）等同小手机；② **展开态横跨双屏**：利用 Viewport Segments 做"左列表 + 右详情"双栏布局，内容避开铰链；③ 半开笔记本姿态（`device-posture: folded`）提供合理降级布局 | P2（渐进增强） |
| R5 | 组件化收敛 | 将 1.3 节 7 类重复模式抽取为通用组件 + composables，新页面"搭积木"完成；存量页面渐进迁移 | P0 |
| R6 | 架构优化 | 清理死代码；统一 Element Plus 引入策略并修复 14 个风险文件；大文件拆分；补齐 spacing 令牌；建立断点治理脚本 | P1 |

### 2.2 设备与场景矩阵

| 设备形态 | 视口特征（CSS px） | 目标体验 |
|---|---|---|
| 桌面显示器 | ≥1280 | 现状顶栏 + 全宽内容，信息密度最高 |
| 笔记本 | 1024~1279 | 同桌面，间距略收敛 |
| 平板横/竖 | 768~1023 | 顶栏保留（菜单可收纳为"更多"），内容 2 列 |
| 折叠屏展开竖态（如 Galaxy Fold 内屏） | ≈768~1010 | 等同平板档 |
| **折叠屏展开横跨双屏** | 双 segment（各 ~450+） | 双栏：左列表右详情，铰链区留白 |
| 折叠屏半开（笔记本姿态） | 上屏内容 + 下屏操作区（或整体窄视口） | 降级为平板/手机档，可选增强 |
| 折叠屏外屏 / 手机 | 344~430 | 抽屉导航 + 单列 + 卡片化表格 |
| 小手机 | <380 | 最小支持 320px 不出横向滚动（适配页内） |

### 2.3 非功能性需求

- **性能**：不新增运行时依赖（不引 VueUse，自研 ~100 行 composable）；移动端首屏不变差；保持现有 manualChunks 策略。
- **兼容性**：折叠屏 API 仅 Chromium 支持 → 必须**渐进增强**（CSS 未知媒体查询自动忽略 + JS 特性检测），Safari/Firefox 下降级为普通响应式，不白屏不报错。
- **i18n/RTL**：抽屉导航、新组件全部走 vue-i18n 词条，沿用 postcss-rtlcss，不硬编码方向。
- **可访问性**：抽屉导航焦点圈闭 + ESC 关闭；触摸目标 ≥44×44px；移动端字号 ≥14px；沿用 `prefers-reduced-motion`。
- **可测试性**：延续并推广"源码文本断言"响应式测试模式；`useBreakpoint` 提供 matchMedia mock 的单测；建立手动设备验证清单（Chrome DevTools + viewportsegments-polyfill）。
- **回归控制**：分阶段灰度，每阶段有明确验收标准；96 条路由分级改造，非目标页面保持现状不劣化。

---

## 3. 业界调研

### 3.1 折叠屏 Web 标准（W3C / Chromium）

折叠屏适配有两大标准 API，均源自 W3C Devices and Sensors Working Group / Microsoft dual-screen 项目：

**① Viewport Segments API（视口分段）**——回答"屏幕被铰链分成了几块、每块多大"：

```css
/* 横跨双屏（左右两段，书本姿态） */
@media (horizontal-viewport-segments: 2) {
  .wrapper { flex-direction: row; }
  .list-view  { width: env(viewport-segment-width 0 0); }              /* 左屏 */
  .fold-gap   { width: calc(env(viewport-segment-left 1 0)
                   - env(viewport-segment-right 0 0)); }               /* 铰链宽度 */
  .detail-view { width: env(viewport-segment-width 1 0); }             /* 右屏 */
}
/* 竖跨双屏（上下两段，笔记本姿态） */
@media (vertical-viewport-segments: 2) { /* ... */ }
```

- 六个 `env()` 变量：`viewport-segment-width/height/top/right/bottom/left x y`（索引：水平布局左段 `0 0` 右段 `1 0`；垂直布局上段 `0 0` 下段 `0 1`）。
- JS 侧 `window.viewport.segments` 返回每段 `DOMRect`；配合 `resize` + `devicePosture.change` 事件响应变化。

**② Device Posture API（设备姿态）**——回答"设备现在折到什么程度"：

- 姿态枚举：`continuous`（完全展开/普通设备）、`folded`（半开，如笔记本姿态）；早期草案的 `foldedFlat` 已移除。
- CSS `@media (device-posture: folded)` 与 JS `navigator.devicePosture.type` + `change` 事件。
- 指纹熵极低（默认 continuous，至多 1 bit），隐私风险可忽略。

**兼容性现状（2026-09）**：两 API 在 Chromium（Chrome/Edge，含 Android/Windows 双屏设备）可用；**非 Baseline**，Safari/Firefox 未实现 → 方案定位为**渐进增强层**。桌面开发调试可用微软官方 [`viewportsegments-polyfill`](https://github.com/foldable-devices/viewportsegments-polyfill)（注意 DevTools 设备模拟通常不含物理分段模拟，需 polyfill 或真机）。

**折叠屏典型布局模式**（MDN / Smashing Magazine 总结）：列表-详情双栏（每栏精确对齐一个 segment，中间留铰链空隙）、跨屏地图、避开铰链的单一内容流。

### 3.2 响应式管理后台通用模式

研究了 Vuetify、Quasar、MUI、Ant Design Pro、vue-pure-admin / vue-element-plus-admin / naive-ui-admin 等主流实现，共识如下：

1. **导航两态模式（业界标准）**：宽屏 permanent（常驻）+ 窄屏 temporary（遮罩抽屉，backdrop + ESC/点击关闭）。Vuetify `v-navigation-drawer`、Quasar `QDrawer`（内置 `breakpoint` 属性，低于阈值自动切 overlay 模式）、MUI Drawer 均为此模式。本项目桌面是顶栏形态，对应做法是：**≥1024 保留顶栏，<1024 顶栏只留"汉堡 + 品牌 + 关键操作"，菜单整体移入左侧抽屉**（分组折叠面板，复用 `appNav.ts` 数据源与角色过滤逻辑）。
2. **断点单一事实源**：断点令牌同时供 CSS 与 JS 消费（VueUse `useBreakpoints` 是社区事实标准；CSS-Tricks/Penpot 的 design token 实践）。**注意 `@media` 条件中不能使用 `var()`**，CSS 侧无法真正变量化——业界通行解法是 *stylelint 白名单约束* 或 *审计脚本*，本项目选择后者（见 4.2，契合项目已有的 `i18n:check` 审计脚本文化）。
3. **常见阈值**：640（手机/平板界）、768、1024、1280；主流 admin 模板普遍以 768/1024 为主断点——与本项目最高频现值 768px 吻合，迁移成本最低。
4. **移动端表格三板斧**：优先列 + 横向滚动（工程成本最低）；行转卡片（label:value 列表，体验最佳）；"展开行"披露次要列（折中）。Ant Design Pro / TDesign 移动端后台均以卡片化为主。
5. **弹层移动端模式**：Dialog 全屏化（`fullscreen`）或收敛宽度至 90%+；Drawer 改为 bottom sheet（底部上滑面板，桌面端仍为右侧抽屉）。

### 3.3 Element Plus 的移动端能力与局限

- **官方定位桌面端优先**（PC 中后台组件库）：Dialog/Drawer 无断点式响应式宽度属性（社区请求 [element-plus#13068](https://github.com/element-plus/element-plus/discussions/13068) 至今未实现）；无触摸滑动关闭、无 bottom sheet 手势。
- 可用能力：`el-dialog` 的 `fullscreen`、Drawer 的 `direction="btt"`（近似 bottom sheet）、width 支持 `%`/`fit-content`。
- **对本方案的含义**：不能指望 Element Plus 自带移动端适配，但项目实际对 EP 的依赖极浅（dialog 3 文件 / pagination 2 / table 9 / drawer 0），自研统一弹层封装（AppModal/AppDrawer）反而与项目现状（32 处手写 modal、全局 `.modal-overlay` 样式、`useConfirmDialog` 成功先例）更匹配，视觉零回归（决策见 4.5.7）。

### 3.4 值得借鉴的开源项目清单

| 项目 | 借鉴点 |
|---|---|
| [vue-pure-admin](https://github.com/pure-admin/vue-pure-admin) / vue-element-plus-admin | Vue3+EP 系响应式 admin 的抽屉导航实现、断点常量组织 |
| Vuetify `v-navigation-drawer` / Quasar `QDrawer` | 两态导航（permanent/temporary）与 breakpoint 属性的交互细节（遮罩、宽度、ESC、路由跳转自动收起） |
| MUI Drawer 文档 | 临时抽屉的可访问性规范（焦点圈闭、aria-modal、恢复焦点） |
| [VueUse useBreakpoints](https://vueuse.org/core/usebreakpoints/) | composable API 形状（`greater/smaller/current` 返回 reactive refs）——自研实现的接口参考 |
| Ant Design Pro | 列表页骨架（PageContainer + ProTable）的组件抽象方式 |
| [foldable-devices 组织示例库](https://github.com/foldable-devices) + MDN/Smashing 教程 | 折叠屏双栏布局代码模式、polyfill 调试方法 |

---

## 4. 技术方案

### 4.1 总体思路

1. **单代码库、移动优先的渐进增强**，不做独立移动站（96 条路由 × 独立站成本不可接受）。分层：
   - **基础层**（所有设备）：统一断点体系 → 手机/平板/桌面三档；
   - **增强层**（仅支持设备生效）：折叠屏双栏、posture 姿态布局，用 CSS 未知媒体查询自动忽略 + JS 特性检测实现零风险降级。
2. **壳层先行、组件铺路、页面渐进**：先改 AppTopbar（一处改动全局受益），再建通用组件库，最后按页面优先级滚动迁移，未迁移页面不劣化。
3. **组件化与响应式一次做完**：每个新通用组件内置响应式行为（如 AppModal 移动端自动全屏），迁移页面"顺手获得"移动端能力，避免二次返工。

### 4.2 断点体系与令牌

**标准断点（全项目唯一合法值）**：

| 档位 | 范围 | 用途 |
|---|---|---|
| `mobile` | `< 768` | 手机 / 折叠屏外屏：抽屉导航、单列、卡片化表格、弹窗全屏 |
| `tablet` | `768 ~ 1023` | 平板 / 折叠屏展开竖态：2 列栅格、抽屉导航（或收纳式顶栏） |
| `desktop` | `≥ 1024` | 桌面：现状顶栏布局 |
| `wide` | `≥ 1440` | 大屏：内容最大宽度约束（可选） |
| 细分 `small` | `< 480` | 小屏微调（字号/按钮尺寸），不改变布局结构 |

选 768/1024 的理由：与业界主流一致；768 已是项目最高频值（15 处）；EP 栅格 `sm=768/md=992` 接近，认知负担小。

**JS 单一事实源**（新增 `src/config/breakpoints.ts`）：

```ts
export const BREAKPOINTS = {
  mobile: 0,     // < 768
  tablet: 768,   // >= 768
  desktop: 1024, // >= 1024
  wide: 1440,    // >= 1440（可选档）
  small: 480,    // < 480 细分微调
} as const

/** 允许出现在 CSS @media 中的断点白名单（responsive-audit.mjs 消费） */
export const MEDIA_QUERY_WHITELIST = [480, 640, 768, 1024, 1440] // 640 为存量过渡保留值
```

**CSS 侧治理**：因 `@media` 条件不能用 `var()`、且 221/222 个组件是原生 CSS（不切 SCSS），采用**审计脚本白名单**而非 mixin：

- 新增 `web/scripts/responsive-audit.mjs`（仿 `scripts/i18n-audit.mjs`）：扫描 `src/**/*.{vue,css}` 的 `@media`，断点值不在白名单即失败；`pnpm responsive:check` 接入 CI。
- 迁移策略：白名单初期带上存量值并打 `deprecated` 警告，随页面改造逐步收缩，最终只留标准值。
- 新组件提供 `--kx-bp-*` 文档化注释 + 常用间距令牌（顺带补齐 `--kx-space-1..6`，见 4.7）。

### 4.3 视口 composables（自研，零依赖）

新增 `src/composables/useBreakpoint.ts`（接口对齐 VueUse 便于未来替换）：

```ts
import { ref, computed, readonly, onMounted, onUnmounted } from 'vue'
import { BREAKPOINTS } from '../config/breakpoints'

const mqlMobile  = window.matchMedia(`(max-width: ${BREAKPOINTS.desktop - 1}px)`) // <1024
const mqlTablet  = window.matchMedia(`(min-width: ${BREAKPOINTS.tablet}px)`)      // >=768
const mqlSmall   = window.matchMedia(`(max-width: ${BREAKPOINTS.small - 1}px)`)   // <480

const isMobile = ref(mqlMobile.matches)   // < 1024：手机+平板（需要抽屉导航的档位）
const isTablet = ref(mqlTablet.matches)   // >= 768
const isSmall  = ref(mqlSmall.matches)

function bind() { /* addEventListener('change') 同步三个 ref，全局单例，SSR 安全 */ }

export function useBreakpoint() {
  return {
    isMobile: readonly(isMobile),   // 决策口径：<1024 即"非桌面"
    isTablet: readonly(isTablet),
    isSmall: readonly(isSmall),
    isDesktop: computed(() => !isMobile.value),
  }
}
```

另增 `useViewportSegments.ts`（折叠屏增强层，全部特性检测）：

```ts
export function useViewportSegments() {
  const supported = typeof window !== 'undefined' && 'viewport' in window && 'segments' in (window as any).viewport
  const segments = ref<DOMRect[]>(supported ? (window as any).viewport.segments : [])
  // resize + navigator.devicePosture?.addEventListener('change') 时更新；不支持则恒为 []
  return { supported, segments, isSpanning: computed(() => segments.value.length > 1) }
}
```

命名澄清：`isMobile = <1024` 是**布局决策口径**（是否需要抽屉导航/全屏弹窗），而非设备分类；768~1023 的平板在交互上归入"触屏壳层"，文档中统一称"移动壳层"。现有 4 处 `window.innerWidth`（`liveStreamStore`、`RequestTile`、`UserMenuDropdown`）与 5 处 `resize` 监听逐步迁移到此 composable，消除重复监听。

**测试**：vitest setup 增加 `matchMedia` mock（jsdom 未实现），为 `useBreakpoint` 写单测；沿用 `AppTopbar.responsive.test.ts` 的源码断言模式为新组件加响应式回归测试。

### 4.4 应用壳层方案

**管理后台壳（核心改造）**：

```
≥1024（desktop）                <1024（mobile 壳层）
┌──────────────────────┐        ┌──────────────────────┐
│ logo 一级菜单… 操作区 │        │ ☰ logo   状态 主题 语言 用户│ ← 顶栏精简
├──────────────────────┤        ├──────────────────────┤
│                      │        │▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓│
│   main-body (现状)   │        │▓▓ 抽屉导航(遮罩) ▓▓▓▓│ ← 8 分组手风琴
│                      │        │▓▓ + main-body 单列 ▓▓│    复用 appNav.ts
│                      │        │▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓│    + canShowNavItem
└──────────────────────┘        └──────────────────────┘
```

实现要点：

1. `AppTopbar.vue` 内 `useBreakpoint().isMobile` 分支渲染：菜单区替换为汉堡按钮；`NAV_GROUPS` 渲染逻辑抽成 `AppNavDrawer.vue`（手风琴分组 + 角色过滤 + 插件/运维中心动态菜单合并逻辑复用 `AppTopbar` 现有 `mergeRemoteOps`，抽 composable `useAppNav()` 共享）。
2. 抽屉实现走 **4.5.8 的 `AppDrawer`** 组件（不引 EP）：遮罩点击关闭、ESC 关闭、路由跳转自动收起、焦点圈闭、宽度 `min(80vw, 320px)`、RTL 自动反向。
3. `main-body` padding 响应式化：24px → 16px（tablet）→ 12px（mobile）；全局补 `env(safe-area-inset-*)`。
4. 操作区在 <480 只保留用户头像与汉堡（现状已隐藏品牌文字，规则保留并固化进测试）。

**访客壳 / 公共门户壳**：`guest-header` <768 收纳为汉堡（同款 AppNavDrawer 复用，数据源为静态 6 链接）；`ServiceLandingPage` 已有 960px 断点，补 768px 档即可；`PublicPageShell` 的 960px 居中在移动端降为全宽 + 12px padding。

**移动端全局规则**（`src/style.css` 新增 `responsive-base.css` 段）：

- `100vh` → `100dvh`（移动端浏览器地址栏问题，存量逐处替换）；
- 触摸目标：按钮/输入最小高度 40px（<768 时 44px）；
- `--kx-space-*` 间距令牌在 <768 收敛一档；
- `text-size-adjust: 100%` 防 iOS 横屏字号膨胀。

### 4.5 通用组件体系（组件化方案）

#### 4.5.1 组件清单与优先级

| 组件 / composable | 替代的重复模式 | 优先级 | 预计采用规模 |
|---|---|---|---|
| `AppDrawer` | 18 个手写 drawer + 移动端 bottom sheet | **P0**（壳层依赖） | 壳层 + 18 存量 |
| `AppModal` | 32 个手写 modal-overlay | **P0** | 32 存量 + 新页 |
| `PaginationBar` + `usePagination` | 10 个手写分页栏 / 11 处手写状态 | **P0** | 10+ 视图 |
| `PageHeader` | 42 个视图手写页头 / 31 处重复样式 | P1 | 42 视图 |
| `DataTable`（含响应式行为） | 60 个手写 table + loading/空态重复 | P1 | 60 视图（渐进） |
| `StatCard` / `StatsRow` | stat-card ×17 + summary-card ×4 两套实现 | P1 | 21 文件 |
| `FilterBar` | 16 个手写 filter-bar（整合 FilterInput + ActiveFilterChips + useFilterChips） | P1 | 16+ 视图 |
| `useBreakpoint` / `useViewportSegments` | 4 处 innerWidth + 5 处 resize | **P0** | 全局 |

新组件统一放 `src/components/ui/`（与领域组件区分），配套 `.test.ts`（含响应式源码断言）。

#### 4.5.2 PageHeader

```vue
<PageHeader :title="t('keys.title')" :subtitle="t('keys.subtitle')"
  :loading="loading" :refresh="load" v-model:auto-refresh="autoRefresh"
  :auto-refresh-interval="10" back-to="/keys">
  <template #actions>…按钮区…</template>
</PageHeader>
```

收敛点：标题层级统一（h1）、返回链接（内嵌 `PageBackLink`）、刷新按钮（28 处 `@click=load` 重复）、自动刷新开关（10 处重复）、#actions 移动端自动换行为全宽按钮行。全局 `.page-header` 样式成为唯一实现，31 处局部重复样式删除。

#### 4.5.3 StatCard / StatsRow

```vue
<StatsRow :cols="{ mobile: 2, tablet: 2, desktop: 4 }">
  <StatCard :label="t('stats.total')" :value="total" tone="neutral" />
  <StatCard :label="t('stats.errors')" :value="errCount" tone="danger" />
  <StatCard :label="…" :value="…" tone="good|warn|loading" :trend="+12%" />
</StatsRow>
```

统一 `stat-card` 与 `summary-card` 两套平行实现：`tone` 取代 `summary-good/warn/bad`；`StatsRow` 内置响应式栅格（mobile 2 列 → desktop 4 列，grid `auto-fit` + 容器查询），移动端可选紧凑模式（label 上 value 下）。

#### 4.5.4 FilterBar

```vue
<FilterBar v-model:filters="filters" :definitions="[
  { key: 'status', type: 'select', options: STATUS_OPTS },
  { key: 'keyword', type: 'search' },
  { key: 'range', type: 'daterange' },
]" @search="load" />
```

- 声明式定义驱动，内置整合现有 `FilterInput` / `ActiveFilterChips` / `useFilterChips`（三者已存在但采用率 1/3/3 处，整合后归一入口）。
- **响应式行为**：<1024 控件纵向堆叠 + 搜索按钮全宽；<768 默认折叠为"筛选（2）"入口 + 展开面板（业界标准做法），避免一屏被筛选项占满。

#### 4.5.5 DataTable（响应式表格，难点攻坚）

60 个手写 table 不可能一次性重写，组件提供**两种采用姿势**：

1. **包裹模式（低成本，先铺开）**：`<DataTable scrollable>` 仅包一层横向滚动容器 + 统一 loading 行（`AppSpinner`）+ 空态（`EmptyState`）+ `min-width` 保护，**表格内部结构不动**，视图零重构获得基础可用性。
2. **列配置模式（彻底，逐页做）**：声明列 `{ key, label, priority: 'primary' | 'secondary' | 'detail', minWidth }`，组件按断点输出：
   - desktop：完整表格；
   - tablet：次要列隐藏（priority=secondary 隐藏）+ 横向滚动兜底；
   - mobile：**行转卡片**（primary 列渲染 label:value 卡片，secondary 列折叠进"详情"展开区），点击行为对齐现有 row-click。

```vue
<DataTable :columns="columns" :rows="rows" :loading="loading"
  mobile-mode="card" row-key="id" @row-click="openDetail">
  <template #cell-status="{ row }"><StatusBadge … /></template>
</DataTable>
```

6 个 `el-table` 视图：短期外层加滚动容器（el-table 自身移动端无适配），中期随页面改造切到 DataTable。移动端列宽体系（优先列 / 次要列 / 详情列）写入组件文档，作为后续所有列表页的规范。

#### 4.5.6 PaginationBar + usePagination

```ts
const pager = usePagination((p) => fetchLogs({ page: p.page, pageSize: p.size }))
// { page, pageSize, total, pages, loading, prev(), next(), setTotal() } + watch 联动
```

```vue
<PaginationBar v-bind="pager" :total-text="t('pager.total')" />
```

消灭 10 份手写 pagination-bar（含 `RequestLogsView` 同文件双份复制）与 11 处手写状态；移动端形态收敛为"‹ 3/12 ›"紧凑式（页码省略），桌面保留完整页码。

#### 4.5.7 AppModal（弹窗统一，含移动端全屏）

**决策**：封装现有手写 `modal-overlay` 模式（不切换 el-dialog）。理由：① 32 处存量是手写模式，wrapper 化零视觉回归；② EP dialog 移动端本就需 CSS 覆盖（官方无响应式宽度）；③ 与 `useConfirmDialog` 的收敛路线一致。

```vue
<AppModal v-model="showEdit" :title="t('keys.edit')" size="md"
  :fullscreen="isMobile" /* 或 responsive 默认 <768 自动全屏 */>
  <template #footer>…</template>
</AppModal>
```

统一解决存量重复踩坑点：z-index 堆叠（`modal-overlay-stacked`/10000 修复）、ESC 关闭、滚动锁定、焦点圈闭、移动端全屏 + 顶部关闭栏 + 底部操作栏吸底。存量 32 处渐进迁移，迁移同时修复 9 处重复样式定义。

#### 4.5.8 AppDrawer（抽屉统一，含 bottom sheet）

同样封装现有 drawer-mask/drawer-panel 模式：

- desktop：右侧面板（宽度 `min(33vw, 520px)`，可配置）；
- mobile/tablet：**bottom sheet**（`direction` 自动切换，高度 `max-content` 上限 90dvh，顶部拖拽把手视觉 + 下滑关闭区）；
- `NodeDetailDrawer` / `RouteIncidentDrawer` / `SessionSummaryDrawer` / `RequestLogDrawer` 等大组件改为"内容部分复用 + 壳换 AppDrawer"。

壳层 `AppNavDrawer`（4.4）基于它实现，是第一个消费者。

### 4.6 折叠屏适配方案（增强层）

**策略：纯 CSS 优先、JS 兜底、全程特性检测**——不支持的浏览器里这些代码是死分支或被忽略的媒体查询，零副作用。

**① 折叠态外屏（~360px）**：落入 `mobile` 断点的 `small` 档（<480），无需专属代码，靠 4.4/4.5 的移动端方案天然覆盖。

**② 展开态横跨双屏（主要价值场景）**——"左列表 + 右详情"双栏：

```css
/* src/styles/foldable.css —— 独立文件，只在支持时生效 */
@media (horizontal-viewport-segments: 2) {
  .app-shell--spanning {           /* App.vue 根节点，由 useViewportSegments() 打标 */
    grid-template-columns:
      env(viewport-segment-width 0 0)          /* 左屏：列表/主内容 */
      calc(env(viewport-segment-left 1 0)
           - env(viewport-segment-right 0 0))  /* 铰链：留白（非交互区） */
      env(viewport-segment-width 1 0);         /* 右屏：详情/辅助面板 */
  }
}
```

应用点（按价值排序）：请求日志列表 + 详情抽屉（详情从"抽屉"升级为"右屏面板"）、会话列表 + 会话详情、Chat 对话 + 参数/轨迹面板、凭证列表 + 监控详情。实现方式：`useViewportSegments().isSpanning` 为真时，页面用 `<div class="split-pane">` 双栏容器替代抽屉调用（详情组件复用，只是宿主不同）。

**③ 半开笔记本姿态（`device-posture: folded`）**：

```css
@media (device-posture: folded) and (vertical-viewport-segments: 2) {
  /* 上屏主内容、铰链留白；下屏可选承载操作栏（筛选/分页吸底） */
}
```

首版只做"安全降级"：folded 时按窄视口处理（天然落入现有断点），保证内容绝不出现在铰链遮蔽区；下屏操作区作为后续可选增强。

**④ 调试与验收**：DevTools 不模拟分段 → 用 [`viewportsegments-polyfill`](https://github.com/foldable-devices/viewportsegments-polyfill)（仅 devDependencies，不进产物）+ 真机（如有 Galaxy Fold / Surface Duo）抽检；验收标准写明"Chrome Android on Fold 真机或 polyfill 截图"。

### 4.7 架构优化清单（与组件化并行的非响应式收益）

1. **死代码清理**：删除 `App.vue:294-639` sidebar CSS（~350 行）与 `appNav.ts` 的 sidebar 折叠持久化 API；顺带清理 `--kx-sidebar` 令牌的引用面。
2. **Element Plus 策略统一**：决策为"**按需手动 import 为唯一合法方式**"——修复 14 个未注册即使用的文件（补 import 或替换为自研组件）；新增审计规则（可并入 responsive-audit 或 eslint 自定义检查）防止再犯；EP CSS 全量引入问题（产物体积）单列后续优化项（按需 CSS 需引 unplugin，本轮不做，避免扩大改动面）。
3. **间距/排版令牌补齐**：`src/style.css` 增加 `--kx-space-{1..6}`（4/8/12/16/24/32）与 `--kx-font-{sm,md,lg}`，新组件一律消费令牌，终结 31 处 `.page-header`、15 处 `.stat-card` 类局部重复样式。
4. **大文件拆分**：Top 10 大视图（1600+ 行）在组件化迁移时同步拆分——`*View.vue` 只留装配，filter/table/dialog/drawer 各成子组件（如 `CredentialMonitorView` → `components/credential-monitor/` 目录）；`RouteIncidentDrawer`（1985 行）拆面板。
5. **composables 归位**：`useAppNav()`（AppTopbar 与 AppNavDrawer 共享菜单合并/角色过滤）、`usePagination`、`useBreakpoint` 入 `src/composables/`；views 下的通用弹窗组件（`TenantCreateDialog` 等）规范到领域目录。
6. **测试基建**：vitest setup 补 `matchMedia` mock；推广源码断言响应式测试（每个新组件必须带）；建立手动设备矩阵清单（见 5.6）。

### 4.8 目录结构规划（增量）

```
src/
├── config/breakpoints.ts            # 断点单一事实源（新）
├── composables/
│   ├── useBreakpoint.ts             # 视口断点（新）
│   ├── useViewportSegments.ts       # 折叠屏分段（新，增强层）
│   ├── usePagination.ts             # 分页状态（新）
│   └── useAppNav.ts                 # 菜单合并/过滤共享逻辑（新，自 AppTopbar 抽出）
├── components/
│   ├── ui/                          # 通用 UI（新目录，全部带响应式行为与测试）
│   │   ├── AppModal.vue  AppDrawer.vue  PageHeader.vue
│   │   ├── StatCard.vue  StatsRow.vue  FilterBar.vue
│   │   ├── DataTable.vue  PaginationBar.vue
│   │   └── AppNavDrawer.vue         # 壳层导航抽屉
│   └── …(领域组件维持现状)
└── styles/
    ├── responsive-base.css          # 移动端全局规则：dvh/触摸目标/safe-area（新）
    └── foldable.css                 # 折叠屏增强层（新，纯增量）
```

---

## 5. 实施计划

### 5.1 阶段划分（每阶段独立可交付、可暂停）

| 阶段 | 内容 | 产出 | 工作量 | 验收标准 |
|---|---|---|---|---|
| **P0 基础设施** | `breakpoints.ts`、`useBreakpoint`、`responsive-audit.mjs`（白名单含存量值）、`matchMedia` mock、`--kx-space-*` 令牌、`responsive-base.css`、死代码清理（App.vue sidebar CSS） | 断点体系 + 治理脚本 + 审计全绿 | 2~3 人日 | `pnpm responsive:check` 通过并入 CI；`useBreakpoint` 单测通过；现有 2 个响应式测试不回归 |
| **P1 壳层改造** | `AppDrawer`/`AppModal` 组件、`useAppNav()` 抽取、`AppNavDrawer`、AppTopbar 分支渲染、guest 壳收纳、PublicPageShell 移动全宽、LoginModal 迁移 AppModal | 移动端可完整导航全站 | 4~6 人日 | <768 与 768~1023 下 50 菜单项可达（含插件/运维中心动态项）；ESC/遮罩关闭；RTL 正常；`AppTopbar.responsive.test.ts` 扩展通过 |
| **P2 通用组件库** | `PageHeader`、`StatCard/StatsRow`、`FilterBar`、`DataTable`（先包裹模式）、`PaginationBar + usePagination`，各带单测 + 响应式断言测试 | `components/ui/` 组件齐备 + story 式示例页 | 8~12 人日 | 组件测试全绿；示例页在 360/768/1024/1440 四档无横向溢出；审计脚本白名单收紧一轮 |
| **P3 高频页面迁移** | Top 10 大视图 + 移动高频页（Dashboard、RequestLogs、Chat、Providers、Keys、CredentialMonitor、Models、Tenants…约 15 个）切组件 + DataTable 列配置模式 + 大文件拆分 | 核心页面双端可用 | 10~15 人日 | 每页在 360px 完成核心操作路径（列表→筛选→详情→基础操作）；页头/分页/弹窗重复代码删除量 ≥60% |
| **P4 折叠屏增强** | `useViewportSegments`、`foldable.css`、请求日志/会话/Chat 双栏（isSpanning 分支）、folded 姿态安全降级、polyfill 调试基建 | 折叠屏增强层 | 4~6 人日 | Chrome Android（Fold 真机或 polyfill）横跨双屏时列表-详情双栏且内容不压铰链；Safari/Firefox 无任何回归 |
| **P5 收尾治理** | 剩余 ~30 个列表页滚动迁移（可按团队节奏分摊）、白名单收敛至标准断点、EP 未注册文件修复完成、文档（组件使用指南 + 断点规范） | 全量收敛 | 5~6 人日（不含页面分摊） | 审计脚本白名单仅剩 480/768/1024/1440；96 路由抽查无未注册组件告警 |

**总计：31~48 人日**（P0+P1+P2 = 14~21 人日即达到"移动端全站可用 + 组件库成型"的关键里程碑）。

### 5.2 迁移优先级依据

- **页面流量/使用频率**：Dashboard、请求日志、Chat 对话是移动场景（值班响应、on-call 排查）最常用；
- **重复度**：Top 10 大文件全部命中列表页骨架，组件化收益最大；
- **风险隔离**：每页独立 PR，`git` 可回滚粒度小；非迁移页面因只依赖全局壳层变化，回归面可控。

### 5.3 协作方式

- 组件库（P2）与壳层（P1）可由两人并行（P1 只依赖 AppDrawer/AppModal 先行，可提前 2 日交叉）；
- P3 起页面迁移可拆给多人，按"组件使用指南"自助进行；
- 每阶段结束跑 `typecheck + vitest + responsive:check + i18n:check` 四件套。

### 5.4 质量门禁

1. 静态：`vue-tsc`、`responsive-audit`（断点白名单）、i18n 审计、EP 注册审计；
2. 单测：新组件/组合式函数全覆盖（含响应式源码断言测试）;
3. 手动设备矩阵（每阶段末）：Chrome DevTools 360/375/390/768/1024/1440 + iPad + polyfill 双屏（P4）+ 真机抽检（如有）；
4. 验收红线：任何已适配页面在 320px 宽度下不允许出现页面级横向滚动条（页内表格滚动容器除外）。

---

## 6. 可行性评估

### 6.1 技术可行性：高

| 事项 | 评估 |
|---|---|
| 移动端响应式 | ✅ 成熟技术（媒体查询 + composable + 组件行为分支），无框架迁移、无新依赖；`viewport` meta 已就绪，`--kx-*` 令牌体系可承接 |
| 抽屉导航 | ✅ 50 项菜单的数据源（`appNav.ts`）与角色过滤、动态菜单合并逻辑已经收敛为函数，抽壳复用即可；手写 drawer 模式项目已有 18 处经验 |
| 组件化 | ✅ 有 `useConfirmDialog`（26 视图）与 `EmptyState`/`PageBackLink` 的成功先例，且列表页骨架高度一致，wrapper 化技术风险低 |
| 折叠屏双栏 | ✅（有条件）标准 API 已在 Chromium 稳定；因设计为纯增强层，最坏情况（API 缺失/行为差异）= 降级为普通响应式，**不存在功能不可用风险** |
| 折叠屏 API 兼容面 | ⚠️ 非 Baseline（Safari/Firefox 未实现）——已通过渐进增强化解；真机覆盖依赖有限（需至少一台 Fold 类设备或以 polyfill 替代） |
| jsdom 测试局限 | ⚠️ 无法验证真实布局——沿用项目已验证的源码断言模式 + 手动设备矩阵补位；如需升级可后续引 Playwright（本轮不做） |

### 6.2 成本与收益

- **成本**：31~48 人日（单人全职约 6~10 周；关键里程碑 P0~P2 约 3~4 周）；
- **直接收益**：移动端/平板从"基本不可用"到"核心路径可用"；折叠屏双栏属行业内少有的体验亮点；
- **工程收益**：消灭 ≥200 处重复实现（42 页头 + 21 卡片 + 16 筛选 + 10 分页 + 32 弹窗 + 18 抽屉…），Top 10 大文件预计减重 30~50%；31+15+9 处重复样式定义删除；新增页面开发从"复制旧页改"变为"组 8 个积木"；
- **风险收益**：修复 14 个 EP 未注册文件的运行时隐患；删除 ~350 行死代码；断点治理防止碎片化复发。

### 6.3 执行可能性结论

**可行，建议按 P0→P1→P2 启动（14~21 人日），在 P1 末用真机走查决定是否继续 P3 全量节奏。** 理由：前两阶段不依赖任何实验性 API、不动业务逻辑（只动壳层与新增件）、随时可停且已交付物（断点体系、审计脚本、AppModal/AppDrawer）独立有价值；唯一的外部不确定性（折叠屏 API 兼容面）被隔离在 P4 增强层，不做也不影响其余目标达成。

---

## 7. 风险与缓解

| # | 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|---|
| 1 | 60 个表格视图迁移量大、周期长 | 高 | 中 | 包裹模式先行铺开（分钟级/页），列配置模式只对高频页做；审计脚本只约束新代码 |
| 2 | 折叠屏 API 行为差异/真机不足 | 中 | 低 | 纯增强层 + 特性检测；polyfill 调试；验收允许"polyfill 截图"替代真机 |
| 3 | 视觉回归（桌面端） | 低 | 高 | 壳层 ≥1024 分支保持现状 DOM；每页 PR 附 1440px 截图比对；现有源码断言测试扩围 |
| 4 | 弹层焦点管理/滚动锁定引入新 bug | 中 | 中 | AppModal/AppDrawer 集中实现 + 单测，迁移存量时逐处走查；保持与 `confirm-dialog.css` z-index 约定兼容 |
| 5 | 96 路由回归面大 | 中 | 中 | 分阶段、页面级独立 PR；非目标页面仅受壳层影响（P1 验收专门覆盖） |
| 6 | 团队绕过规范继续硬编码断点 | 中 | 中 | `responsive:check` 接 CI 强制；白名单收紧节奏公开；组件示例页降低"手写"动机 |
| 7 | Element Plus 修复引出隐藏视觉问题 | 低 | 低 | 14 个文件多为低频页（Forbidden 等），逐处修复 + 截图确认；替换为自研组件也是选项 |
| 8 | 移动端性能（EP CSS 全量 + 图表库） | 低 | 中 | 本轮不加重依赖；EP 按需 CSS 与图表懒加载列作后续优化专项 |

---

## 8. 附录：参考资料

**折叠屏 / 双屏 Web 标准**

- [MDN — Using the Viewport Segments API](https://developer.mozilla.org/en-US/docs/Web/API/Viewport_segments_API/Using)
- [W3C — Device Posture API 规范](https://www.w3.org/TR/device-posture/) ｜ [MDN — navigator.devicePosture](https://developer.mozilla.org/en-US/docs/Web/API/Navigator/devicePosture) ｜ [MDN — @media device-posture](https://developer.mozilla.org/en-US/docs/Web/CSS/Reference/At-rules/@media/device-posture)
- [Chrome for Developers — Support Foldable Devices with the Viewport Segments API](https://developer.chrome.com/blog/viewport-segments-api-shipped) ｜ [Origin trial for Foldable APIs](https://developer.chrome.com/blog/foldable-apis-ot)
- [Smashing Magazine — Building Web Layouts For Dual-Screen And Foldable Devices](https://www.smashingmagazine.com/2022/03/building-web-layouts-dual-screen-foldable-devices/)
- [foldable-devices/viewportsegments-polyfill](https://github.com/foldable-devices/viewportsegments-polyfill)（调试用）
- [caniuse — Viewport Segments](https://caniuse.com/wf-viewport-segments) ｜ [caniuse — Device Posture API](https://caniuse.com/device-posture-api)

**响应式管理后台 / 断点体系**

- [VueUse — useBreakpoints](https://vueuse.org/core/usebreakpoints/) ｜ [自研 media query composable 参考](https://dev.to/unorthodev/build-a-custom-media-query-composable-for-vue-apps-1o2c)
- [MUI Drawer（temporary vs permanent 模式）](https://mui.com/material-ui/react-drawer/) ｜ [Vuetify Navigation Drawer](https://vuetifyjs.com/en/components/navigation-drawers/) ｜ [Quasar Layout Drawer（breakpoint 属性）](https://quasar.dev/layout/drawer/)
- [CSS-Tricks — Responsive Designs and CSS Custom Properties](https://css-tricks.com/responsive-designs-and-css-custom-properties-defining-variables-and-breakpoints/)
- [vue-pure-admin](https://github.com/pure-admin/vue-pure-admin) ｜ [LogRocket — Top Vue Admin Dashboards](https://blog.logrocket.com/top-vue-admin-dashboards/)

**Element Plus 移动端局限**

- [Element Plus Dialog 文档（fullscreen）](https://element-plus.org/zh-CN/component/dialog) ｜ [Drawer 文档（direction=btt）](https://element-plus.org/zh-CN/component/drawer)
- [element-plus#13068 — Drawer/Dialog 响应式宽度请求（未实现）](https://github.com/element-plus/element-plus/discussions/13068)
- [知乎 — element-plus 对移动端的适配如何](https://www.zhihu.com/question/641304216)

**项目内关联文档/代码**

- 现状依据：`web/src/style.css`（令牌）、`web/src/App.vue`（壳层/死代码）、`web/src/components/shell/AppTopbar.vue`（导航与现有断点）、`web/src/config/appNav.ts`（菜单单一事实源）、`web/src/components/shell/AppTopbar.responsive.test.ts`（响应式测试模式）
- 收敛先例：`web/src/composables/useConfirmDialog.ts`（26 视图采用）、`web/src/components/EmptyState.vue`
