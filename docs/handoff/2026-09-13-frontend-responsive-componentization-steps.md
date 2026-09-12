# handoff：前端响应式与组件化改造 · 分步执行提示词（Batch 2 起）

日期：2026-09-13
依据方案：[docs/03-design/frontend-responsive-and-componentization-design.md](../03-design/frontend-responsive-and-componentization-design.md)（§4 技术方案 / §5 实施计划）
工作目录：仓库根 `llm-gateway-go-5`，前端子目录 `web/`（所有命令在 `web/` 下执行）

## 一、当前状态（接手前核查）

### Batch 1 已交付（2026-09-12，**工作区未提交**）

- `web/src/components/ui/` 新增 `PaginationBar.vue` / `PageHeader.vue` / `StatCard.vue`，配套 3 个测试文件；`web/src/composables/usePagination.ts` + 测试。合计 34 个用例全绿（含响应式源码断言，断点 768/480px）。
- `RequestLogsView.vue`：两份手写分页栏 → `PaginationBar`；`changePage/resetPageAndLoad` → `usePagination`（`resetPageAndLoad` 保留为 12 处调用点的别名）；5 个内联统计卡 → `StatCard compact`；删除分页 scoped CSS（含 720px 碎片段点）。
- `KeysView.vue`：手写页头 → `PageHeader`。
- `App.vue`：删除 topbar 迁移遗留的 sidebar 死样式约 386 行（821 → 435 行）；`appNav.ts`：删除无调用方的 `readSidebarCollapsed/writeSidebarCollapsed`。
- 8 语言 `common.pagination` 补 `pageOf`/`perPage` 词条。
- 验证记录：`vue-tsc` 0 错误；vitest 全量 469 通过 / 40 个失败文件为**存量环境问题**（见下）；i18n 严格审计 0 missing，硬编码 CJK 较基线降 838。
- git 状态：13 个修改 + 6 个新增/目录（`git status` 可见），**尚未提交**。Step 0 处理。

### 环境注意事项（每轮开工前必读，踩过的坑）

1. **pnpm 未安装**：本机只有 npm/npx（node v26）。运行脚本用 `npm run <script>` 或 `npx <bin>`。**不要** `npm install`（会重写 pnpm 布局的 node_modules）。
2. **node_modules 是 darwin 平台装的**：本机是 arm64 Linux，rollup/esbuild 原生二进制缺失已手工补装（tarball 解压进 `node_modules/@rollup/rollup-linux-arm64-gnu`、`node_modules/@esbuild/linux-arm64`，未动 manifest/lockfile）。若 vitest 报 `Cannot find module '@rollup/rollup-linux-arm64-gnu'` 或 esbuild platform 报错，按同样方法补：`cd /tmp && npm pack @rollup/rollup-linux-arm64-gnu@4.60.4`（esbuild 用 `@esbuild/linux-arm64@0.21.5`）→ 解压 `package/` 到对应目录。
3. **存量测试失败基线 = 40 个文件**（jsdom `localStorage` 未定义等环境问题，经 git stash 基线对照确认，与代码无关）。判断标准：**失败集合不得超出该基线**；新写测试必须全绿。不要去修这 40 个存量失败（非本项目范围）。
4. **i18n 扫描器会扫描注释**：组件/代码注释里**不要写 `t('xxx.yyy')` 字样**（会被当作真实引用报 missing key）。用法示例写 `someLabel` 之类的占位变量。
5. **测试读源码用** `readFileSync(resolve(process.cwd(), 'src/...'), 'utf8')`（项目惯例，`new URL(..., import.meta.url)` 在 vitest 下报 `ERR_INVALID_URL_SCHEME`）。
6. **死代码删除先验证边界**：用行内容断言或 grep 确认无调用方后再删，保留仍在用的相邻规则（Batch 1 删 App.vue 时 `.header-alert` 夹在死区中间被保留）。
7. 需要查响应式/断点现状时：`grep -rn "@media" src --include="*.vue" --include="*.css" | grep -oP 'max-width:\s*\d+px' | sort | uniq -c | sort -rn`。

### 全局门禁（每步完成必须全过，四件套）

```bash
cd web
npx vue-tsc --noEmit                                            # ① 0 错误
npx vitest run 2>&1 | tail -3                                   # ② 失败集合不超基线 40 文件
node scripts/i18n-audit.mjs --strict --missing-only             # ③ STRICT PASS
node scripts/responsive-audit.mjs                               # ④ Step 1 落地后加入
```

完成后在本文件"状态登记表"打勾并写一行交付摘要；**每步一个独立 commit**（格式 `feat(web): <step编号>-<主题>`），未经用户同意不 push。

---

## 二、分步执行提示词（按序复制执行，每步一个新会话）

> 每条提示词自含目标、先读文档、任务清单、验收标准。执行者无需本会话之外的上下文。

### Step 0 — 提交 Batch 1（清理工作区）

```text
工作目录：llm-gateway-go-5 仓库根。前端 Batch 1 改动当前未提交（git status
可见 13 修改 + web/src/components/ui/ 等新增）。请：
1. cd web && npx vue-tsc --noEmit 确认 0 错误后回到仓库根。
2. 将全部未提交改动作为一个 commit 提交，message：
   feat(web): 前端组件化 Batch1 — ui/PaginationBar+PageHeader+StatCard+usePagination，
   RequestLogsView/KeysView 迁移，App.vue sidebar 死代码删除
   （提交前先阅读 docs/handoff/2026-09-13-frontend-responsive-componentization-steps.md
   第一节核对交付清单是否与实际 diff 一致；不一致则在 commit message 中如实说明差异）。
3. 不 push。完成后在 handoff 文档状态登记表给 Step 0 打勾。
```

### Step 1 — P0 基础设施：断点单一事实源 + useBreakpoint + 断点审计脚本

```text
你是前端工程师，在 llm-gateway-go-5/web 实施"响应式与组件化改造"Step 1。
先阅读：
- docs/03-design/frontend-responsive-and-componentization-design.md 的 §4.2/§4.3/§5.1（P0）
- docs/handoff/2026-09-13-frontend-responsive-componentization-steps.md 的
  "环境注意事项"与"全局门禁"两节（含坑位：pnpm/npm、rollup 二进制、
  i18n 注释扫描、测试源码读取路径）。

任务：
1. 新建 src/config/breakpoints.ts：
   export const BREAKPOINTS = { mobile: 0, tablet: 768, desktop: 1024, wide: 1440, small: 480 }
   以及 MEDIA_QUERY_WHITELIST = [480, 640, 768, 1024, 1440]（640 为存量过渡值），
   带注释说明各档位用途与"@media 条件无法使用 var()，故 CSS 侧靠审计脚本治理"。
2. 新建 src/composables/useBreakpoint.ts（零依赖，接口对齐 VueUse 便于未来替换）：
   - 模块级 matchMedia 全局单例绑定三个查询（<1024 / >=768 / <480），
     组件共用一份监听；导出 readonly ref：isMobile(<1024，布局决策口径)、
     isTablet(>=768)、isSmall(<480)、isDesktop(computed)。
   - SSR/jsdom 安全：模块加载时若 window 未定义则惰性初始化。
3. 新建 web/scripts/responsive-audit.mjs（仿 scripts/i18n-audit.mjs 的结构与
   CLI 参数风格）：扫描 src/**/*.{vue,css} 的 @media 断点值，
   - 白名单内的通过；白名单外的（存量 17+ 种碎片值）默认 WARN 并计数，
     --strict 时非白名单值退出码 1，但允许 --allow-legacy 降级为 WARN；
   - 输出人类可读报告 + --json 模式；package.json 增加
     "responsive:check": "node scripts/responsive-audit.mjs --strict --allow-legacy"。
   本步只接入脚本不收紧（存量值太多，后续 Step 8 收敛）。
4. vitest 环境补 matchMedia mock：在 vite.config.ts 的 test 配置加
   setupFiles（若无）并新建 setup 文件实现 window.matchMedia stub
   （默认 matches=false、addEventListener/removeEventListener no-op），
   保证 useBreakpoint 在 jsdom 下可测。
5. 新建 src/composables/useBreakpoint.test.ts：mock matchMedia.matches
   验证三档判定与响应式变化（改变 matches 后 ref 更新）。
6. style.css 令牌补齐：--kx-space-1..6（4/8/12/16/24/32px）与
   --kx-font-sm/md/lg，注释标注"新组件一律消费令牌"。
7. 新建 src/styles/responsive-base.css 并在 main.ts 引入：
   - html { -webkit-text-size-adjust: 100%; }
   - 全局 .btn/按钮/输入在 <768px 的最小触摸高度 36→44px 兜底（只加 media 内
     最小高度，不改桌面样式）；
   - body 使用 env(safe-area-inset-*) padding 的工具类 .safe-area。
   注意：不要全局改既有元素样式，只做增量工具类与媒体内兜底，避免视觉回归。

验收（全部满足才算完成）：
- npx vue-tsc --noEmit 0 错误；新增测试全绿；全量 vitest 失败集合不超基线 40；
- node scripts/i18n-audit.mjs --strict --missing-only 通过（注意注释别写 t('...')）；
- npm run responsive:check 退出码 0（allow-legacy 模式）；
- 断点白名单与 useBreakpoint 数值严格来自 breakpoints.ts 单一事实源。
完成后：一个 commit；在 handoff 文档状态登记表打勾并写交付摘要
（含 responsive-audit 报告的存量断点分布统计，供 Step 8 收敛用）。
```

### Step 2 — P1a 通用弹层：AppModal + AppDrawer

```text
你是前端工程师，在 llm-gateway-go-5/web 实施 Step 2（方案 §4.5.7/§4.5.8）。
先阅读 handoff 文档（环境注意事项+全局门禁）与设计文档 §4.5.7/§4.5.8，
再看现有实现参考：
- 手写 modal 模式：src/style.css 的 .modal-overlay/.modal 全局类 +
  src/components/LoginModal.vue（v-model、ESC、z-index 约定）
- 手写 drawer 模式：grep "drawer-mask\|drawer-panel" 的代表实现 +
  src/styles/node-detail-drawer.css
- 测试范式：src/components/ui/PaginationBar.test.ts（i18n 内联 messages +
  响应式源码断言）

任务：
1. 新建 src/components/ui/AppModal.vue：
   - props：modelValue、title、size(sm|md|lg，宽度映射 max-width 480/640/860px)、
     closeOnMask=true、disabledConfirm 等最小集；emits：update:modelValue、close；
   - 复用全局 .modal-overlay/.modal 类保持视觉零回归（scoped 只补增量）；
   - 新增能力：ESC 关闭、打开时 body 滚动锁定（引用计数，多个弹层嵌套不误解锁）、
     初焦点圈闭（Tab 循环）与关闭后焦点返还；
   - 响应式：useBreakpoint().isSmall 时自动全屏（顶部关闭栏 + 底部 #footer
     吸底），断点 768px 以下宽度 min(92vw, size)。
2. 新建 src/components/ui/AppDrawer.vue：
   - props：modelValue、title、width(desktop 右侧面板 min(33vw,520px))、
     direction(auto|right|bottom)；auto=按 useBreakpoint：>=768 右侧、
     <768 底部 bottom sheet（max-height 90dvh + 顶部拖拽把手视觉）；
   - 遮罩点击关闭、ESC、滚动锁定、焦点圈闭；RTL 下自动换边
     （项目已有 postcss-rtlcss，逻辑属性 inset-inline-end）。
3. 各配测试：行为（开合、ESC、mask、滚动锁定的 body class）+ 响应式源码断言
   （768px 断点存在、bottom-sheet/media 全屏规则存在）。
4. 迁移示范（每处迁移前 git diff 自查视觉结构不变）：
   - src/components/LoginModal.vue 改为 AppModal 承载（保留其表单逻辑与既有
     LoginModal.test.ts 断言全绿，必要时小调测试）；
   - 任选 1 个视图内的手写 modal-overlay（如 KeysView 或 ProvidersView 的
     新增/编辑弹窗）迁移为 AppModal。
5. 不迁移其余 30 处存量（后续页面轮逐步做）。

验收：四件套全过；LoginModal 既有测试不回归；AppModal/AppDrawer 新测试全绿；
桌面 1440px 与移动 375px（DevTools 模拟自查即可，描述进 commit message）
两档截图或文字走查记录弹层行为。完成后 commit + 状态登记表打勾。
```

### Step 3 — P1b 壳层移动导航：useAppNav + AppNavDrawer + AppTopbar 分支

```text
你是前端工程师，在 llm-gateway-go-5/web 实施 Step 3（方案 §4.4，本改造核心）。
先阅读 handoff 文档（环境注意事项+全局门禁）与设计文档 §4.4，再读：
- src/components/shell/AppTopbar.vue（菜单合并 mergeRemoteOps/mergeRemotePluginNav、
  角色过滤 canShowNavItem、现有 768/480px 断点与其 responsive 测试）
- src/config/appNav.ts（NAV_GROUPS 单一事实源、TopbarNavGroup）
- src/composables/usePluginNav.ts、src/config/edition.ts（动态菜单两路注入）

任务：
1. 抽 src/composables/useAppNav.ts：把 AppTopbar 内的菜单构建（静态分组 +
   插件菜单合并 + 运维中心远端菜单合并 + 角色过滤）整为一个 composable，
   AppTopbar 与新 AppNavDrawer 共用，逻辑不改行为（AppTopbar 桌面态渲染
   结果必须与现在完全一致）。
2. 新建 src/components/ui/AppNavDrawer.vue（基于 Step 2 的 AppDrawer）：
   - 分组手风琴（当前路由对应分组默认展开）、激活项高亮复用
     isNavItemActive、点击菜单项路由跳转后自动收起；
   - 宽度 min(80vw, 320px)；RTL 换边；ESC/遮罩关闭。
3. AppTopbar 接 useBreakpoint().isMobile（<1024）：菜单区替换为汉堡按钮，
   顶栏保留 品牌 + SystemStatusIndicator + ThemeToggle + LanguageSelector +
   UserMenuDropdown；>=1024 渲染保持现状 DOM 结构不变（桌面零回归红线）。
4. App.vue 的 main-content 区给 AppNavDrawer 挂载点（遮罩 z-index 与既有
   弹层约定对齐，见 styles/confirm-dialog.css）。
5. 测试：
   - useAppNav 单测（静态+角色过滤；远端菜单可 mock）；
   - 扩展 src/components/shell/AppTopbar.responsive.test.ts：断言
     isMobile 分支渲染汉堡而非 nav 列表（源码断言或 mount+mock matchMedia）；
   - AppNavDrawer 行为测试（分组展开、点击 emit/收起）。
6. 走查：DevTools 375px / 768px / 1024px 三档下确认 50 个菜单项可达
   （含 ops 运行时注入项的降级 LOCAL_OPS_MENU 场景）。

验收：四件套全过；AppTopbar 既有 responsive 测试与新增断言全绿；
桌面 1024px+ DOM 结构与改造前一致（对比 git diff 模板段）。
完成后 commit + 状态登记表打勾，并在交付摘要里记录三档走查结论。
```

### Step 4 — P1c 壳层收尾：访客壳 / 公共门户 / dvh 清理

```text
你是前端工程师，在 llm-gateway-go-5/web 实施 Step 4（方案 §4.4 尾段）。
先阅读 handoff 文档与设计文档 §4.4"访客壳/公共门户壳/移动端全局规则"。
前置：Step 3 已完成（AppNavDrawer 可复用）。

任务：
1. App.vue 访客壳：guest-header <768px 收纳为汉堡（复用 AppNavDrawer，
   数据源为现有 6 个静态链接数组化），guest-nav 桌面态不动。
2. PublicPageShell.vue：--pub-max 960px 在 <768px 降为全宽 + padding 12px
   （scoped media，不改桌面）。
3. 100vh → 100dvh 存量清理：grep "100vh" src/，对 App.vue auth-loading、
   弹层/抽屉类逐处替换为 100dvh（保留 fallback：height:100vh; height:100dvh;
   两行写法）。表格容器类 overflow 高度一并核查。列出改动清单进 commit message。
4. ServiceLandingPage.vue 补 768px 档（已有 960px，检查 hero 双列在 768-959
   的表现，必要时 768px 即单列化）。

验收：四件套全过；访客页/登录页在 375px 无横向溢出（DevTools 走查记录）。
完成后 commit + 状态登记表打勾。P1 阶段至此收口：在 handoff 交付摘要里
给 P1 整体写一段验收结论（全站移动端导航可用性）。
```

### Step 5 — P2 剩余组件：FilterBar + StatsRow + DataTable（包裹模式）

```text
你是前端工程师，在 llm-gateway-go-5/web 实施 Step 5（方案 §4.5.1/§4.5.4/§4.5.5）。
先阅读 handoff 文档与设计文档 §4.5 相关小节；已完成的组件范式参考
src/components/ui/（PaginationBar/PageHeader/StatCard 及其测试）。
现有素材：FilterInput.vue、ActiveFilterChips.vue、useFilterChips.ts、EmptyState.vue、AppSpinner.vue。

任务：
1. src/components/ui/FilterBar.vue：声明式 definitions（key/type:
   select|search|daterange + options）+ v-model:filters；内部整合 FilterInput
   与 ActiveFilterChips；<1024 控件纵向堆叠 + 搜索按钮全宽；
   <768 默认折叠为"筛选（N）"展开面板（展开态文案走 common.button.filter 词条，
   缺词条则补 8 语言）。配测试（含折叠行为源码断言）。
2. src/components/ui/StatsRow.vue：响应式栅格容器（grid auto-fit + 容器查询，
   mobile 2 列→desktop 4 列可配），子项即 StatCard。配测试。
3. src/components/ui/DataTable.vue（先只做"包裹模式"，方案 §4.5.5 的 1）：
   - props：loading、empty-text、scrollable=true、min-width；
   - 结构：横向滚动容器 + 表格插槽（thead/tbody 具名插槽，视图原表格结构
     原样搬入）+ loading 时 AppSpinner 替换 tbody + 空数据 EmptyState；
   - 配测试（loading/空态/插槽透传）。
4. 迁移示范各 1 处：AuditLogView（filter-bar+pagination 一起换 PaginationBar
   +FilterBar）、RequestLogsView 表格外层换 DataTable 包裹模式、
   CredentialMonitorView 的 summary-card 行换 StatsRow+StatCard。
5. 列配置模式（行转卡片）本轮不做，留 Step 6 按页评估。

验收：四件套全过；3 个示范视图桌面 1440px 截图/走查对比无回归；
375px 无页面级横向滚动（表格容器内滚动除外）。
完成后 commit + 状态登记表打勾（含各组件采用计数基线，供 Step 6 目标设定）。
```

### Step 6 — P3 高频页面迁移循环（模板化，每页一个 commit）

```text
你是前端工程师，在 llm-gateway-go-5/web 实施 Step 6（方案 §5.2 高频页面迁移）。
先阅读 handoff 文档与设计文档 §4.5/§5.2；组件库现状见状态登记表。

本轮页面清单（按序，每个独立 commit，做完一个再下一个；若上下文不够，
完成当前页后在 handoff 登记进度即可结束本轮）：
  1) DashboardViewV2  2) ChatView  3) ProvidersView  4) ModelsView
  5) CredentialMonitorView（大文件拆分：filter/table/子表 → 子组件目录
     components/credential-monitor/）  6) RoutingDashboardView  7) TenantsView
  8) UsersView  9) FreePoolView  10) ModulesView

每页执行同一模板：
a. 读视图，列出该页的 page-header / 手写 filter-bar / 手写 pagination-bar /
   手写 modal-overlay / 手写 stat|summary-card / 手写 table 清单；
b. 逐项替换为 ui/ 组件（PageHeader/FilterBar/PaginationBar+usePagination/
   AppModal/StatCard+StatsRow/DataTable 包裹模式），删除对应 scoped 重复样式
   与死断点（720/760/900 等收敛到 768/1024，改完跑 responsive-audit 该文件
   无 WARN）；
c. el-table 视图（如遇到）：外层加 DataTable 包裹获得横向滚动兜底；
d. 1500+ 行的大视图按 §4.7-4 拆子组件（装配留在 View，块拆到
   components/<domain>/）；
e. 验收：四件套 + 该页 375px/1440px 两档走查（记录进 commit message）+
   npm run responsive:check 该文件断点无新增 WARN；
f. commit：feat(web): P3-<页面名> 组件化迁移。

页面全清单完成度在 handoff 状态登记表逐页打勾。
```

### Step 7 — P4 折叠屏增强层（渐进增强，零风险降级）

```text
你是前端工程师，在 llm-gateway-go-5/web 实施 Step 7（方案 §4.6）。
先阅读 handoff 文档与设计文档 §4.6（含 MDN Viewport Segments / Device Posture
资料要点）。前置：Step 3/5 完成。

任务：
1. src/composables/useViewportSegments.ts：特性检测 window.viewport.segments，
   resize + navigator.devicePosture?.change 时更新；不支持则恒空数组。
   导出 supported/segments/isSpanning。配单测（jsdom 下 supported=false 路径）。
2. src/styles/foldable.css（独立文件，纯增量）：
   - @media (horizontal-viewport-segments: 2) 的 .app-shell--spanning 三列
     grid（左屏/铰链留白/右屏，用 env(viewport-segment-width 0 0) 等）；
   - @media (device-posture: folded) 的安全降级（按窄视口处理，内容避开铰链）；
   - main.ts 引入。不支持的浏览器这两段自动忽略，无 JS 兜底需求。
3. 应用到 RequestLogsView：isSpanning 时列表固定左栏、RequestLogDrawer
   详情改渲染进右栏面板（详情组件复用，宿主从抽屉换 split-pane 容器；
   非 spanning 行为完全不变）。
4. 调试基建：devDependencies 加 viewportsegments-polyfill（仅 dev，
   不进产物；若网络不可用则记录并跳过，改用真机）。
5. 验收：四件套；polyfill 或 DevTools 双屏模拟下截图/走查记录
   （左列表右详情、铰链区无内容）；普通浏览器（无支持）无任何回归。

完成后 commit + 状态登记表打勾，交付摘要注明验证方式（polyfill/真机/受限）。
```

### Step 8 — P5 治理收尾

```text
你是前端工程师，在 llm-gateway-go-5/web 实施 Step 8（方案 §4.7 / §5.1 P5）。
先阅读 handoff 文档（含 Step 1 登记的存量断点分布统计）与设计文档 §4.7。

任务：
1. 断点白名单收缩：按 Step 6 进度，把仍存在的存量碎片断点逐文件收敛到
   480/768/1024/1440；随后 responsive-audit 去掉 --allow-legacy 并把
   responsive:check 升级为严格模式（package.json 同步）。剩余无法收敛的
   逐个列出理由登记（如第三方约束）。
2. 修复 14 个"模板使用未 import 的 el-* 组件"文件（Batch 1 分析发现，
  grep -rn "<el-" src/views src/components | 筛出无 import 的）：
  补 import 或替换为自研 ui 组件，逐文件截图确认渲染正常。
3. el-drawer/el-dialog 弃用清理评估：确认 AppModal/AppDrawer 采用率后，
  若仍有 3 处 el-dialog 留存则登记不强制迁移（风险低）。
4. 文档：docs/03-design/ 下新增或扩写《前端组件使用指南》（ui/ 组件 props
   清单 + 每个的迁移前后对照示例 + 断点规范），设计文档实施状态节更新为
   全量完成度。
5. 验收：四件套 + responsive:check 严格模式 0 告警 + 96 路由抽查无
   "resolve component" 运行时告警（DevTools console 抽查 5 页）。

完成后 commit + 状态登记表打勾，并在 handoff 写整体收口结论。
```

---

## 三、状态登记表

| Step | 内容 | 状态 | 交付摘要（完成时填） |
|---|---|---|---|
| 0 | 提交 Batch 1 | ✅ 完成 2026-09-13 | commit f797779d4，23 文件（13 修改 + 设计文档/handoff + ui 组件 6 文件 + usePagination 2 文件）；提交前核验 vue-tsc 0 错误、vitest 469 通过 / 40 存量失败文件（= 基线）、diff 与交付清单一致；未 push |
| 1 | P0 基础设施（breakpoints/useBreakpoint/审计脚本/令牌/responsive-base） | ✅ 完成 2026-09-13 | commit 64b6983d3。新增 breakpoints.ts（BREAKPOINTS+MEDIA_QUERY_WHITELIST=[480,640,768,1024,1440]）、useBreakpoint.ts（单例 matchMedia，isMobile<1024/isTablet>=768/isSmall<480/isDesktop computed，含 \_resetForTests）、scripts/responsive-audit.mjs（白名单直接 import breakpoints.ts，--strict/--allow-legacy/--json）、vite.config setupFiles=src/test/setup.ts（matchMedia stub）、useBreakpoint.test.ts 8 用例全绿、style.css 补 --kx-space-1..6 与 --kx-font-sm/md/lg（12/14/16px，对齐存量组件实际字号）、styles/responsive-base.css（text-size-adjust+<768 触摸高度 44px 兜底+.safe-area）+main.ts 引入、package.json 加 responsive:check。四件套全过（vitest 477 通过/40 存量失败文件=基线）。**存量断点分布（Step 8 收敛用）**：共 90 处宽度断点/244 文件——白名单内 768×18、640×8、1024×5、480×4；白名单外 55 处：900×12、800×9、720×8、960×6、700×5、760×4、1200×3、520×2、1100×2、1600/680/600/1000 各×1，明细可跑 `node scripts/responsive-audit.mjs` 查看 |
| 2 | P1a AppModal + AppDrawer | ✅ 完成 2026-09-13 | commit dbfcad3e9。ui/AppModal（复用全局 .modal-overlay/.modal 零视觉回归；props: modelValue/title/size sm=480,md=640,lg=860/closeOnMask/fullscreen/closable/disabledConfirm/panelClass；ESC、引用计数滚动锁定、焦点圈闭+返还；isSmall<480 自动全屏=顶部关闭栏+footer 吸底，<768 宽 min(92vw,size)）；ui/AppDrawer（direction auto=按 isTablet>=768 右侧 min(33vw,520px) / <768 bottom-sheet 90dvh+把手；遮罩/ESC/滚动锁/焦点圈闭；inset-inline-end RTL 自动换边——注：项目实际未装 postcss-rtlcss，改用 CSS 逻辑属性原生 RTL）；新增 useScrollLock/useFocusTrap；迁移示范：LoginModal（AppModal 承载+panelClass 皮肤保留，测试补 i18n 插件）与 ProvidersView 新增弹窗（#footer 槽）。新增测试 16 用例全绿；四件套全过（vitest 493 通过/40 存量失败文件=基线） |
| 3 | P1b 壳层移动导航 | ✅ 完成 2026-09-13 | commit 9a3815fcd。useAppNav.ts（菜单构建单例化：静态分组/mergeRemoteOps 运维中心远端合并/mergeRemotePluginNav 插件合并/canShowNavItem 角色过滤/激活状态解析 + navDrawerOpen 共享开关）；AppNavDrawer.vue（AppDrawer direction=right + min(80vw,320px)，手风琴默认展开当前路由组，isNavItemActive 高亮，路由跳转自动收起）；AppTopbar isMobile(<1024) 汉堡分支（v-else 保桌面 DOM 零回归），App.vue main-content 区挂载 AppNavDrawer。测试：useAppNav 4 用例（角色过滤/远端合并 mock）+ AppNavDrawer 5 用例 + AppTopbar.responsive 扩展 4 断言。四件套全过（vitest 506 通过/40 存量失败文件=基线）。**三档走查结论**：375px/768px 汉堡+抽屉导航，50 菜单项经分组手风琴全可达（含 LOCAL_OPS_MENU 降级与插件注入项）；1024px+ 桌面 DOM 与改造前一致（v-else 模板未动） |
| 4 | P1c 访客壳/门户/dvh 收尾（P1 收口） | ✅ 完成 2026-09-13 | commit d7726ceaa。访客壳 guest-header <768 汉堡（isTablet 断点）+ AppNavDrawer 新增 guestLinks 扁平列表模式（6 静态链接数组化），>=768 guest-nav 不动；PublicPageShell <768 --pub-max:100%+padding 12px；ServiceLandingPage 补 768px 档（间距微调，hero ≤960 已单列）；100vh→100dvh 清理 19 处（App.vue×3、style.css drawer-panel、弹层抽屉类×5、表格/滚动容器类×8、两个 landing shell），全部保留两行 fallback，0 孤立 100vh。四件套全过。**P1 整体验收结论**：全站移动端导航可用——已登录态 <1024 汉堡+AppNavDrawer 手风琴（50 菜单项可达，含运维中心降级与插件注入），访客态 <768 汉堡+静态链接抽屉；弹窗/抽屉（AppModal/AppDrawer）在 375px 自动全屏/bottom-sheet；RTL 经逻辑属性自动换边；桌面态 DOM 与样式零回归（所有响应式改动均为媒体内增量或 JS 分支） |
| 5 | P2 剩余组件 FilterBar/StatsRow/DataTable | ☐ 未开始 | |
| 6 | P3 高频页面迁移（10 页清单） | ☐ 未开始 | |
| 7 | P4 折叠屏增强层 | ☐ 未开始 | |
| 8 | P5 治理收尾 | ☐ 未开始 | |

（状态取值：☐ 未开始 / ◐ 进行中 / ✅ 完成+日期；Step 6 按 10 页逐页在交付摘要里列完成度）

## 四、给并行线/评审的说明

- 本线只动 `web/`（前端）与 `docs/`，不触碰 Go/迁移/部署面；与数据库迁移线、部署线无在途冲突面。
- 桌面零回归是每步红线：所有响应式改动必须媒体查询内增量，禁止改动桌面态 DOM 与样式。
- 组件接口如有调整（新增 props/改名），须同步更新《组件使用指南》与既有采用处，避免接口漂移。
