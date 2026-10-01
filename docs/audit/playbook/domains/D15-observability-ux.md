# D15 — 可观测性与前端一致性

> 领域编号: D15 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：流程可观测性（日志/指标/追踪覆盖与统一性）；数据展示统一性（web 端复用同组控件）；菜单组织易用性；交互操作规范一致性（确认弹窗/表格/表单/暗色模式）。
**不管**：统计口径正确性（D10）；灰藏页的路由注册本身（D16 闭环），但"该不该灰藏"归本域。

## 2. 参考基线

设计文档：
- `docs/metrics-catalog.md` — 指标目录（命名/标签规范）
- `docs/03-design/02-feature-design/design/DASHBOARD_V2_TECHNICAL_DESIGN.md` 等三份
- web 端 ui 套件（DataTable/AppModal/confirmDialog——以 web/src 实际布局为准）

代码入口：
- `web/`（Vue 管理台）、`web/public/menu-config.json`（生成物，冲突取远程）
- `telemetry/`、`metrics/`、`monitoring/`、`internal/observability/`、`internal/logging/`

## 3. 检查清单

1. **新增必有观测**：窗口内新增流程/worker/端点有结构化日志与指标（复用 metrics-catalog 命名），不引入私有 printf 风格日志通道。
2. **控件复用**：新增页面/区块复用 ui 套件（DataTable/AppModal/confirmDialog），不手写 table/window.confirm（FreeDiscoveryView 教训）；列表/详情/筛选三形态风格一致。
3. **暗色模式**：新增页面暗色下无白底/白字（用户协议页教训 7c9e3b042）。
4. **菜单组织**：新增路由入 menu-config（或明确登记灰藏+理由）；无"无菜单无页内链接"的孤儿页（R30 遗留 #2 清单为基线，逐项核对是否仍孤儿）。
5. **交互一致**：破坏性操作有确认；加载态/空态/错误态三态齐备；分页与时间窗选择控件与既有页面一致。
6. **生成物纪律**：menu-config.json 变更走生成流程；冲突取远程不手并。

## 4. 历史回归点（轮末回注区）

- [R30 遗留#2] 七个孤儿路由（/routing-decisions、/admin/approvals、/admin/output-compliance、/admin/usage、/correlations、/quality-correlations、/routing/overrides）——开放债，逐轮核对
- [R30 遗留#3] FreeDiscoveryView 手写 table/window.confirm 未复用套件；freediscovery 页未展示 auto_disabled_at 等健康字段——开放债
- [09-16] 用户协议页暗色白底 — 修复 7c9e3b042

- **R44 | "缺省不冒充"语义覆盖分子分母两侧**：命中率类派生指标在任一输入字段缺省时显示 —（R44 L-2：`prompt_tokens ?? 0` 使只报 cache_read 的上游冒充 100%）；LiveRequest 的 hub 路径与 hub==nil 兜底分支字段面必须对齐（R44 L-3：兜底分支补 cache 字段时漏 GwSessionID 实例）。

## 5. 子代理派发提示词

```text
你是 D15（可观测性与前端一致性）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D15-observability-ux.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：窗口内 web/ 与观测面的 diff；孤儿路由与手写控件存量债是否恶化。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```

### R42 回注（2026-09-18，i18n 全语言纪律 + 错误态渲染）
- **新增 i18n key 必须 8 语言全落**：parity 门禁（每 locale ⊇ zh-CN leaf keys）在 main 上红过一次（721 五 key 只落 zh/en）——verify.sh --web 全红。交接/验证清单必须含 vitest run，vue-tsc+vite build 不覆盖 parity。
- **catch 只写"从未渲染的状态"= 用户静默失败**：⟳ 刷新失败曾只写死状态 balanceRefreshError；修复后统一写已渲染的 c.balance_error，400（无余额 API）映射 balanceUnsupported。新增交互的验收标准：每条失败路径都能在 UI 上被用户看见。
- manual 戳条件化（值不变不盖章）+ 后台失败落 balance_error 属 D08 R42 回注的 UX 侧。

### R43 回注（2026-09-18，前端闭环三断点批）
- **前端闭环端到端验证要看后端词表**：工作台 L1 种子（api-work-types.ts）与 taskprofile registry 是两套词表——注册表缺类时提交 400 且 `.catch(() => undefined)` 静默吞掉（R43 已修：registry +6 类 + correctionWarning 警告条 + 8 语言 i18n）。新增标注/反馈前端时，词表 SSOT 对齐是验收项。
- **生产接线 ≠ 测试接线**：FeedbackRecorder 挂在测试共享实例上 e2e 全绿、生产 admin store recorder=nil 计数恒零——e2e 注释自认"production wiring: admin handlers carry none"时，交付清单必须含生产装配点 grep。
- vue-tsc 必跑：taskProfile.ts 泛型强转 TS2352 在 main 存在数日（a8d3a5bbe 交付漏 vue-tsc），vitest 925 绿不覆盖类型门（R42 i18n 教训的同款姊妹案例）。

### R45 回注（2026-09-19）
- RequestTile 命中率边界定式：`prompt_tokens` 显式 0 且 cache_read>0 时 100% 是合法值（非"未上报"）；缺省（null）才显示 —。判据 = `prompt_tokens != null && r + prompt_tokens > 0`。
- 实时流 hub==nil 兜底分支的字段面**有意**保持最小集（不可达防御代码不追求与 hub 路径逐字段对齐，避免双份漂移面）——字段对齐判据只适用于可达路径。
- R69 回注（2026-09-27）：**新 admin 端点三件套缺一即 P2**——①生产构造点+路由挂载（9f62818c5 的 list_v2 因无构造点被子树路由吞成 sessionID="list"，CLI 契约破裂）；②GetAuthContext nil→404 双保险（自定义 mux 直挂不裸奔）；③tenantFromQueryOrContext 租户钉扎（?tenant= 仅 super 生效）。反向臂类查询的候选集加 LIMIT N+1（歧义判定只需">1"）；数据完整性类 500（歧义拒绝）必须落服务端日志作告警锚点；列表/详情响应的空数组序列化为 [] 而非 null。

### R71 回注（2026-09-27，新端点错误体卫生与 D11 文档一致性）
- 新端点上线轮必须同步做错误体卫生（固定文案+slog 锚点）：list_v2 接线轮（2a8ad6b74）只补了鉴权防线，500 体裸回显 err.Error() 由 R71 补收；契约测试双泄漏断言（query 臂+list_v2）入册。
- D11 文档教训：收口摘要的数字/行数/口径必须与权威报告逐项对账（plan.md 四处矛盾 + file:///Users 绝对死链），文档勘误=代码事实优先。

### R72 回注（2026-09-27，L4 admin 500 回显债批量收口）
- **台账数字必须与代码事实对账再立项**：R71 记载"85 处/22 文件"，R72 实测主口径 395 行/77 文件（+Sprintf 55 + http.Error 26）——批量收口前先多口径 grep 实勘（同线 err.Error()×SIC / Sprintf 变体 / http.Error / 502-503）。
- 收口机制定型：`admin/internal_error.go` 三 helper（writeInternalErr JSON-detail 形状 / writeInternalErrStr 平面 error 字符串形状 / writeInternalTextErr text/plain 形状）——客户端只见到固定 op 文案，真实错误带 runtime.Caller file:line 锚点进 slog；**响应线形状（JSON 对象/字符串/text）必须与原 helper 对齐**，否则破坏前端消费。旧 writeInternalErr 105 调用点改名为薄包装统一 slog 位点。
- 回归钉桩：`internal_error_guard_test.go` 静态扫描包内源码禁 500 行含 `*.Error()`（LEGIT 白名单同 sqlreadguard 机制，刻意留空）；dashboardapi writeErrorJSON 单点闸门（5xx details 一律不下发、落 slog）——带 details 参数的共享 helper 优先做单点闸门，比逐点改写便宜且防未来新增漏网。
- 变换实操坑：GNU sed ERE 不支持 `(?:...)` 非捕获组（"Invalid preceding regular expression"），多表达式脚本须逐条单 sed 执行。

### R75 回注（2026-09-28，守卫跨平台 + 契约注释三面一致）
- **源码扫描型守卫的路径必须归一化**：零回显守卫白名单键为斜杠路径，Windows 上 WalkDir 产出反斜杠 → 24 处假泄漏全红（十七轮路径化只在 macOS 验证过）。**凡 filepath.WalkDir + 白名单/断言路径匹配的守卫，回调入口统一 filepath.ToSlash**；跨平台守卫必须在两个 OS 各跑一次才算绿。教训一般化：**恒查门若只在单一 OS 验证，等于半个门**。
- wire 契约注释必须与实现+测试三面一致：types.go 把 JSON null 写在 available 侧（实现判 unavailable）——JSON null 是写路径"载荷为空"的常态编码（jsonTextOrNull），注释错向会误导下一个读契约的消费者；9 locale "tri-state" 措辞随实际两态键集同步。
- autoroute fail-open 新增 `autoroute_task_vocabulary_absent_total{task}`（词表缺失静默降级的唯一指标面信号）；apihub.HealthStorage 登记为零消费预留枚举。

### R85 回注（2026-10-01，菜单可达性门已建 + 控件复用实测）
- **新增永久门 `web/src/config/navCoverage.test.ts`**（6 断言，4/4 变异全红）。本域此前的 `appNav.test.ts` **21/21 全绿却零业务覆盖**（全测可见性，无 labelKey×语种、无菜单↔路由断言）。**新建或改写任何扫描型守卫前必读 `conventions.md` §9**——该门判据改了四版才收敛，且 §9.3 记的「判据自证漏洞」正是它的第一版失败形态。
- **R56「menu-config 无漏注册」结论漂移**：那只覆盖了已注册项的字段对齐，**漏了反向覆盖率**。实测 7 条硬孤儿路由（`/routing-decisions`、`/routing/overrides/audit`、`/quality-correlations`、`/admin/session-analytics/users`、`/admin/session-analytics/users/:owner`、`/admin/output-compliance`、`/admin/usage`）。`/admin/session-analytics/users` 已于 R85 挂载入口（API `main.go:6901-6906` 与真库 `session_dim` 121 万行早已就绪，而 8 语种 `nav.item.sessionAnalytics` 文案**早已存在却零引用**＝可交付未交付），其余 5 条待产品确认归属，已登记在 navCoverage 白名单。
- **控件复用实测（分母 71 个顶层视图，objective「尽可能复用同组控件」未落地）**：`PageHeader` 6/71 vs **53 处自写 h1/h2**；`DataTable` 4/71 vs **42 处裸 `<table>`**；`FilterBar` 1/71 vs 22 处自建；`PaginationBar` 3/71 vs 7 处；`StatCard/StatsRow` 6/71 vs 14 处；~~**`EmptyState` 组件根本不存在而 16 处视图各写空态**~~（**R88-f 订正，见本节末 R88-f 回注：组件存在，两个数字都不对**）；`el-skeleton` 使用 0 处。9 类标准件中 **8 类复用率 <20%**，仅 `KxDateRangePicker`(13/71) 达标。
- **`i18n/parity.test.ts` 以 zh-CN 为基准 ⇒ 对「加菜单忘配文案」结构性失守**；实测 21 个陈旧 nav 键（含 9 个会话域键，如 `sessionClusters` 引用数 0）。
- **objective 的三条要求（菜单可达性 / 控件复用率 / 会话聚合入口）在 17 个域 plan 中均无立项条目** —— 这才是 7 条孤儿路由能存活至今的根因：**没有立项就没有门**。

### R88-f 回注（2026-10-01，EmptyState 精确清单 + 机械统一不可行的理由）

**背景**：本节 R85 写「`EmptyState` 组件根本不存在而 16 处视图各写空态」，
62 号已在审计报告里更正为「组件存在」，**但未回灌到本 playbook** ⇒ 文档一致性缺陷。
62 号当时标注「**精确数字仍未定**」，本轮把它定死。

**实测（`web/`，263 个 `.vue`）**：

| 量 | 数值 | 数法 |
|---|---|---|
| 引用 `EmptyState` 的文件 | 8（**含组件自身 ⇒ 7 个消费者**） | `grep -rl EmptyState src --include=*.vue` |
| 自建 `.empty-state` 容器 | **13 处 / 8 个文件** | 逐行读模板确认，排除 `EmptyState.vue` 自身与 `-icon`/`-title`/`-desc` 子元素 |
| 全站 `.vue` 总数 | 263 | `find src -name '*.vue'` |

**13 处逐条归类**（这是**可复现的分类**，不是「16 处视图各写空态」那种印象式计数）：

| 类别 | 位置 | 为什么**不能**机械替换 |
|---|---|---|
| **B·loading 态**（3） | `UsageCost:371/440/488`、`ProbeHealthPanel:487` | 渲染的是 `...loading` 文案，**语义不是空态**；且两处带 `card empty-state` 复合类。**换过去会改变客户端观感** |
| **C·表格单元格**（2） | `ProxyView:644`(`colspan=8`)、`:737`(`colspan=11`) | 组件渲染 `<div>`，**放不进 `<td>`**；结构上无法承载 |
| **D·`!important` 覆盖**（1） | `ProxyView:491`（`padding: 2rem !important`） | 覆盖优先级会与组件的行内 `padding` 打架 |
| **A·真空态·div**（7） | `ProbeHealthPanel:488`、`CredentialHeatmapView:677`、`RoutingLogView:264`、`RoutingDashboardView:1006`、`ModulesView:1103`、`ClientConfigDialog:314`、`ProxyView:491` | 见下 |

**A 类里也只有部分能低成本统一**——组件的**能力缺口是实锤**：

- `ProbeHealthPanel.vue:487-488` 的本地样式是 `padding:40px; text-align:center; color:var(--muted)`，**与组件默认逐项相同** ⇒ 这一处是**纯重复**，统一零成本；
- `ClientConfigDialog.vue:314-317` 需要 **icon + title + desc 三层**，`ModulesView.vue:1103` 配有 **`.empty-icon`（40px）**，而 `EmptyState.vue` **只有一个默认 slot** ⇒ **结构上无法承载**；
- ⇒ **62 号提的「缺 `action` 能力」方向正确但不够**：真正卡住的是**连 icon/title/desc 都没有**，不止 action（OmniRoute 的 `actionLabel`/`onAction` 是第四项）。

**⇒ 结论：本轮不做统一重构。** 理由不是「工作量大」，而是三条硬约束：
① 13 处里 6 处**语义或结构上就不该换**（B/C/D 类）；
② A 类中需要富结构的那些**组件当前承载不了** ⇒ 要么先扩组件（`icon`/`title`/`desc`/`action` 四槽），要么逐处重排；
③ 任何替换都要逐处比对 padding/color token，否则违反 objective「**不要影响客户端的观感**」。

**已登记为待裁决第 29 条**（是否给 `EmptyState` 补 icon/title/desc/action 四槽并逐类收敛），
本轮**未改任何渲染行为**、未改组件 props。

**教训**：本轮分类时我先用「行内是否含 `loading` 子串」打标签，被
`v-else-if="!loading && !filteredCredentials.length"` 骗了——那是**真空态**。
**子串命中是代理量**（与 `conventions.md` §9.2 同源），最终按**逐行读渲染内容**归类。
