# 前端组件使用指南（components/ui/）

日期：2026-09-13 ｜ 依据：[frontend-responsive-and-componentization-design.md](./frontend-responsive-and-componentization-design.md)
适用：llm-gateway-go-5/web 前端。新页面/新组件一律消费本目录与令牌，禁止再手写分页栏、弹层壳、统计卡、筛选栏。

## 0. 断点与令牌规范（必读）

- **单一事实源**：`src/config/breakpoints.ts`
  - `BREAKPOINTS = { mobile: 0, tablet: 768, desktop: 1024, wide: 1440, small: 480 }`
  - `MEDIA_QUERY_WHITELIST = [480, 640, 768, 1024, 1440]`（640 为存量过渡档）
- JS 侧：`useBreakpoint()` → `isMobile(<1024，布局决策口径)/isTablet(>=768)/isSmall(<480)/isDesktop`
- CSS 侧：`@media` 只允许白名单值；`@media` 条件无法用 `var()`，由
  `scripts/responsive-audit.mjs` 审计（`npm run responsive:check`）。
- 移动端开关统一用 `max-width: 768px`（存量约定）；平板档用 `max-width: 1023px`（FilterBar 范式）。
- 间距/字号令牌：`--kx-space-1..6`（4/8/12/16/24/32px）、`--kx-font-sm/md/lg`（12/14/16px）。新组件禁止魔法数。
- 增量红线：响应式改动只允许「媒体查询内增量」或「JS 断点分支」，禁止改写桌面态 DOM/样式。

## 1. PageHeader

| prop | 类型 | 说明 |
|---|---|---|
| title | string | 页面标题（渲染 h2） |
| subtitle | string? | 标题下副文案 |

插槽：`#leading`（标题左侧）、`#actions`（右侧操作区）、默认插槽（标题下整行）。
迁移前：`<div class="page-header"><h2>…</h2><div style="…">按钮</div></div>` + 各页重复样式。
迁移后：`<PageHeader :title="t('x.title')"><template #actions>…</template></PageHeader>`，删除本页 .page-header 样式。
注意：紧凑变体页头（DashboardViewV2 的 Tab 内嵌型）不强迁，保持自定义。

## 2. PaginationBar + usePagination

props：`page/pageSize/total/pageSizes[]`；events：`prev/next/change-size(number)`。
数据层用 `usePagination(loader)`；`resetPageAndLoad` 保留为调用点别名。
迁移前：手写 `.pagination-bar`（很多页上下两份）→ 迁移后每份一行 `<PaginationBar … @prev="changePage(-1)" @next="changePage(1)" @change-size="onPageSizeChange"/>`。

## 3. StatCard / StatsRow

StatCard props：`label/value/sub/tone('neutral'|'success'|'warning'|'danger')/compact`；`#value`、`#sub` 插槽可放富内容。
StatsRow props：`cols?: { mobile?: 2, tablet?: 2, desktop?: 4 }`——容器查询（768/1024 切换点）而非视口，抽屉/面板内也正确降列。
迁移前：`.summary-row/.summary-card/.stat-row/.stat-inline` 及 `summary-good/warn/bad` 变体。
迁移后：tone 左色条取代边框变体（§4.5.3 口径）：

```vue
<StatsRow>
  <StatCard label="可用" :value="n" tone="success" />
  <StatCard label="异常" :value="m" :tone="m > 0 ? 'warning' : 'neutral'" sub="unreachable/cooling" />
</StatsRow>
```

## 4. FilterBar

类型：`FilterDefinition`（`ui/filter-types.ts`）：`key/type('select'|'search'|'daterange')/label/placeholder/options[]/suggestions[]/fromKey/toKey/fromLabel/toLabel`。
用法：`v-model="filters"`（Record<string,string>）+ `:definitions` + `@search` + `@clear`。
- search 提供 `suggestions` 时内部用 FilterInput 自动补全；否则普通输入框（Enter 触发 search）。
- daterange 渲染两个 datetime-local，绑定 `fromKey/toKey`（默认 `${key}From/To`）。
- 已生效条件渲染 ActiveFilterChips（点 chip 移除该键）；「清除」按钮清空全部并 emit clear。
- 响应式：<1023 纵向堆叠 + 搜索全宽；<768 折叠为「筛选（N）」入口（文案走 `common.button.filter`，8 语言已有）。
迁移对照（AuditLogView）：4 个 ref（filterActor 等）→ 一个 `filters` 对象；`@keyup.enter` → `@search`；clearFilters → `@clear`。

## 5. DataTable（包裹模式）

props：`loading/empty/emptyText/scrollable(true)/minWidth('720px')`。
视图原 `<table>` 原样放默认插槽；loading→AppSpinner、empty→EmptyState 整区替换。
迁移前：`<div class="card" style="overflow-x:auto"><table>…`。
迁移后：`<div class="card"><DataTable min-width="960px"><table>…</DataTable></div>`。
列配置模式（行转卡片）为后续步骤，本轮不做。

## 6. AppModal

props：`modelValue/title/size('sm'480|'md'640|'lg'860)/closeOnMask(true)/fullscreen/closable(true)/disabledConfirm/panelClass/stacked`。
emits：`update:modelValue/close`；插槽：默认、`#footer`（作用域 `{ disabledConfirm, close }`）。
内置能力：ESC、引用计数滚动锁定（嵌套弹层安全）、焦点圈闭+返还、isSmall(<480) 自动全屏（顶栏关闭+footer 吸底）、<768 宽 `min(92vw, size)`、stacked→`.modal-overlay-stacked`(z-110)。
迁移对照（ProvidersView showAdd）：外层两层 div + h3 → `<AppModal v-model="show" :title="…" size="sm">`，按钮行进 `#footer`。
保留皮肤：`panel-class="login-modal"` + 全局 `.modal.login-modal` 样式（LoginModal 范式）。
不适配场景：顶部对齐的超长表单（ModelsView create-modal 顶部对齐+页面滚动）保留手写。

## 7. AppDrawer

props：`modelValue/title/width('min(33vw,520px)')/direction('auto'|'right'|'bottom')/closeOnMask(true)/closable(true)`。
auto：`isTablet(>=768)` 右侧、<768 bottom-sheet（90dvh+把手）。右侧用 `inset-inline-end`，dir=rtl 自动换边（项目未装 postcss-rtlcss，逻辑属性原生 RTL 生效）。
迁移对照（ModelsView 特色抽屉）：`.drawer-backdrop > .drawer-panel(.drawer-header+.drawer-body)` → `<AppDrawer v-model="show" title="…" width="min(900px,95vw)">`，内层 header/body 删除。
z-index：遮罩 z-100；需要盖在另一弹层上的抽屉（如 diagnose z-110）暂保留手写。

## 8. AppNavDrawer（移动导航）

- 登录态：App.vue 已登录分支挂载；AppTopbar 汉堡（isMobile<1024）打开 `navDrawerOpen`（composable 共享开关）。
- 访客态：`<AppNavDrawer :guest-links="[{ labelKey, href }…]" />`。
- 数据源：登录态 useAppNav()（与桌面下拉完全同源）；手风琴默认展开当前路由组，跳转自动收起。

## 9. useBreakpoint / useViewportSegments / useScrollLock / useFocusTrap

- `useBreakpoint()`：全局单例 matchMedia，见 §0。
- `useViewportSegments()`：折叠屏（§4.6）。isSpanning 时宿主挂 `.app-shell--spanning`（foldable.css 三列 grid），左栏 `.span-left`/右栏 `.span-right`；不支持的浏览器恒 false，CSS 特性查询原生忽略，零降级成本。polyfill 因环境禁 npm install 未装，真机验证待补。
- `useScrollLock`（lockBodyScroll/unlockBodyScroll）：引用计数，`data-scroll-locked` 属性供测试/样式钩子。
- `useFocusTrap(container)`：弹层焦点圈闭三件套。

## 10. 治理与登记（2026-09-13 收口时点）

- el-* 注册：24 个文件补齐 EP 导入（此前运行时 resolve 失败）。el-dialog（OperationAgreementDialog/PromptInjectionSettingsView/VibeCodingView）与 el-drawer（TurnDigestDrawer）共 4 处：已可用、风险低，**登记不强制迁移**。
- 断点白名单：`responsive:check` = `--strict --allow-legacy`（存量 43 文件 49 处放行）。这批文件属设计 §5.1 P5「剩余 ~30 个列表页滚动迁移（可按团队节奏分摊）」范围，不在本轮 10 页清单内；待其页面迁移时逐文件收敛（P3 已示范：900/960/1000/700 等→768/1024/640），清零后从 package.json 去掉 `--allow-legacy` 即自动升级为全严格门禁。
