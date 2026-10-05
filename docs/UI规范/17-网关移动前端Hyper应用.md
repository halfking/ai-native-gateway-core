# 17 — 网关移动前端 Hyper 应用（llm-gateway `web-mobile/`）

> 状态标记：**[部分落地]** —— 工程主体与 Go 接线已并入 `origin/main`；2026-10-05 深夜复验修正：桌面侧 entry-switch 与部署管线接线**已核验在 origin/main**（`5f65feace`/`2ca4df36b`，本标记初版「origin 任何分支均不存在」的断言被复验推翻，见 §10 本仓补记二）；**移动侧 entry-switch（large→/ 腿）仍未落地**。

> **本仓收录说明（2026-10-05）**：本篇自参考仓 `~/workspace/yatao/nbjl3/docs/UI规范/17-网关移动前端Hyper应用.md`
> 移植（参考仓提交 `f5fac05f` 新增、`9672f86e` 验收基线、`007496e3` 二轮复核），
> 正文保持参考仓语义，仅头部加本仓定位。
>
> **本仓定位（读前必看，见 10 §4.6.4 的架构裁决）**：
> - 本仓**主形态**仍是「改造既有 `web/` SPA（compact 档）+ Capacitor Android 壳」，
>   18 条 H6 切片落在 `web/src/views/*.vue`，R1–R10 已按 10 §4.6.5 逐条评估回写。
> - 本篇描述的 `web-mobile/` 独立 SPA（`/m/*` 同源挂载、首轮不装壳）是**并行会话**
>   实现的第二载体，**已并入 `origin/main`**（本分支工作区未检出该目录，
>   读工程细节用 `git show origin/main:web-mobile/README.md`）。
>   两条架构的取舍点与迁移面见 10 §4.6.4 对照表；**本篇在本仓的角色是
>   「已存在的第二载体的契约 + 对全集生效的跨文档裁决（§4 R1–R11）」**，
>   不是对主形态的推翻。
>
> 适用对象：`llm-gateway-go` 仓库 `web-mobile/` 项目（Hyper 类型移动前端）
> 版本：1.0（2026-10-04）
> 上游协议：[06](./06-Hyper导航上下文.md)（导航上下文）、[07](./07-Hyper滚动与专注模式.md)（滚动/专注）、[08](./08-Hyper框架与原生AI体系.md)（分层与 capabilities）、[11](./11-平台交互与导航状态.md)–[15](./15-真机验收与落地路线.md)（平台交互/验收）
> 工程侧 SSOT：`web-mobile/README.md`（本篇管设计契约，那边管构建与接线）

本篇把 Hyper 协议落到 **LLM 网关移动运维前端** 这个第一个真实载体上：定义页面清单、席位、API 映射、Hyper 子集的实现矩阵，并集中裁决 06–16 遗留的跨文档矛盾（§4）。**本文描述的是首个落地轮的目标状态**，与 01–05 的“现行约束”互补：网关移动端从第一天就按 Hyper 协议构建，不经历“桌面 SPA 后补适配”阶段。

## 1. 定位与范围

- **产品定位**：移动端**快查与轻操作**（01 §1.3 同源原则）——看网关状态、看板、节点健康、告警，查模型目录，管理 API Key。密集对账、路由调试、凭据编辑仍是桌面 `web/` 的工作，移动端不藏入口但标记 `desktopOnly`。
- **形态**：独立 Vue 3 SPA（`web-mobile/`），同源挂载在网关 `/m/*`（仿 maintain 双 SPA 先例），不装原生壳（Capacitor 留后续轮，见 §9）。
- **一套代码、按窗口形态**：复用 01 §2 四档 window class。compact 为主战场；medium+ 自动获得更宽的内容栅格与侧栏抽屉常驻形态，**不写第二套桌面代码**。
- **风格**：视觉 token、BEM 命名、中文表格语言全部沿用本专题 01 §5 与 `theme.css` token 体系（`--app-*` / `--kx-*`，浅色主色 `#1e4fd6` / 暗色 `#5b8cff`）。不引入 Element Plus——移动端组件全部手写轻实现，避免桌面组件库的体积与触控债。

> ⚠️ 本仓注：`web-mobile/` 用 **600/960/1280** 自有断点（见 §4-R11），
> 与本仓 `web/` 的 **768/1024/1440**（`config/breakpoints.ts` SSOT）是两套并存事实，
> 见本仓 README「与参考仓的四处实质差异」——不要拿一套去改另一套。

## 2. 页面清单与席位映射

底栏 ≤5 席（02 §4 约束）：**总览 / 节点 / 模型 / 密钥 / 更多**。「更多」唤起抽屉（告警、用量、外观、语言、登出）；账户入口固定在顶栏头像 → AccountSheet（02 §5）。

| 页面 | 路由 | 数据源（网关 API） | 形态 |
| --- | --- | --- | --- |
| 登录 | `/login` | `POST /api/auth/token` | 全屏表单，16px 输入 |
| 总览 | `/` | `/healthz` + `/api/system/version` + `/api/admin/dashboard/board?days=7&include_operational=1` | 状态条 + 汇总卡 + 趋势 sparkline + 后台任务 chips，下拉刷新 |
| 节点（凭据健康） | `/nodes` | `/api/credentials/monitor-summary` | 卡片列表（状态点、effective_state、并发、模型可用数），点开 Sheet 看每模型探测明细（宽表 → 专注模式入口） |
| 模型目录 | `/models` | `/api/routing/available-models` | 家族分组连续加载 + 搜索（250ms debounce） |
| 密钥 | `/keys` | `/api/keys` 全套 | 卡片列表 + 创建 Sheet + 禁用/揭示（揭示走确认框，不缓存） |
| 告警 | `/alerts` | `/api/candidate-failures/alerts` | 时间线卡片 |
| 用量 | `/usage` | `/api/usage/summary` + `/api/usage/by-model` | 汇总卡 + 模型分布，Tab 停靠 |
| 我的 | AccountSheet | `/api/auth/me` | 全屏 Sheet（用户/外观/语言/登出），02 §5 结构 |

`desktopOnly` 页面：无（移动端只收快查面）；桌面专属功能（路由调试、对账、审计）不进移动端导航，也不做"建议桌面端"横幅——它们本来就不在移动端信息架构里。

## 3. Hyper 子集实现矩阵

| 协议能力 | 出处 | 本轮 | 说明 |
| --- | --- | --- | --- |
| TitleResolver 优先级链 | 06 §2 | ✅ | 注册标题 > 弹层标题 > 继承快照 > 路由 titleKey > 应用名；≤200 字符 |
| NavigationContext v2 | 06 §3 | ✅ | entries ≤80 / operations ≤100；sessionStorage 去敏持久化（scope 隔离、query 白名单、凭据/Key 不落盘） |
| 路由所有权三真源 | 06 §4 | ✅ | Vue Router 真源；afterEach + history.state.position 判 push/replace/pop；同页筛选 replace |
| BackDispatcher 单一仲裁 | 06 §5 / 11 §3 | ✅ | 覆盖层 → beforeClose 拒绝即消费 → pop 前驱 → fallback；顶栏/Esc/系统返回同入口 |
| 覆盖层 history 标记 | 06 §5.1 | ✅ | 同 URL pushState 标记 + popstate 拦截 + 拒绝后受控恢复 |
| 滚动恢复 | 06 §6 | ✅ | ScrollHost 注册表 + 离开快照 + 挂载恢复（锚行优先、clamp、2s 预算） |
| 菜单/返回图标 | 06 §7 | ✅ | 矢量 SVG 图标，禁字形 `⋯/☰`；触控目标按 §4-R1 |
| forward() 前进 | 06 §5 | ⚠️ 简化 | 仅无覆盖层且 history 前进分支存在时启用；tombstone 合并留白 |
| Android 预测性返回四阶段 | 11 §4 | ❌ | Web 容器拿不到 start/progress/cancel；capability 报 `back:'commit-only'`，原生壳轮补 |
| ScrollHost / 下拉刷新状态机 | 07 §1–§2 | ✅ | `idle→pulling→armed→refreshing→settling`；64px 触发 / 12px 方向锁 / 96px 最大位移 / single-flight / 与追加互斥（queryRevision） |
| ContinuousListController | 07 §3 | ✅ | sentinel 240px 预载、首屏补页 ≤3 页、rowId 去重、失败保旧 + 手动重试、已加载 X/总计 Y |
| DockCoordinator | 07 §4 | ⚠️ 简化 | CSS sticky 优先；effectiveTop 由可见顶栏下沿实测（ResizeObserver），无 Tab 区域 tabHeight=0（§4-R7） |
| Tab 左右滑切换 | 07 §5 | ❌ | 首轮不做内容区横滑，Tab 用点按；横滑仲裁留给原生壳轮（§4-R4 同源裁决） |
| 手势仲裁表 | 07 §7 / 12 §1–2 | ✅（子集） | 单 owner 指针序列、方向锁 12px、表格横滚归表格、`touch-action` 分区设置 |
| 专注工作区 | 07 §8–§9 | ✅ | 单实例 Teleport portal、`inert` 背景（降级焦点门）、滚动锁引用计数、进入/退出状态机、history 标记 |
| capabilities 协商 | 08 §3 / 14 §2 | ✅ | 合并版 v2 schema（§4-R3）；Web 端恒报原生能力 false，不以 `window.Shell` 存在与否推能力 |
| 后台任务 / 录音 / OCR / Agent | 08 §4–§10 | ❌ | 无原生壳，全部不实现也不占位假装；SSE 实时流留后续轮 |
| LLM 直连（llmgateway.internal.example.com） | 08 §11 | ❌ | 移动运维端不接 LLM 数据面；`HYPER_LLM_API_KEY` 约定不变，留给后续聊天壳 |

> 本仓注：此矩阵描述 `web-mobile/` 侧。`web/` 侧对应实现状态见
> 本仓 10 §4.2 十八条切片与 10 §4.6.5 吸收表，两边的「已实现」集合**不相等**。

## 4. 跨文档裁决记录（R1–R11）

以下裁决对本专题全集生效；与旧篇冲突处以本节为准，旧篇将在后续修订轮回写。
**本仓注：R1–R10 对 `web/` 侧的逐条吸收状态见 10 §4.6.5**（R1 已修 48px + 门禁、
R3 已补齐六字段；R2/R6/R7/R8/R9 登记「未实现/不适用」）。R11 是 2026-10-04
首个落地轮（`web-mobile/`）新增，仅作用于双 SPA 入口层。

- **R11 统一入口自动切换（2026-10-04 首个落地轮新增）**：移动端与桌面端是**同源同端口的两个 SPA**，用户只记一个地址，切换由 window class 自动裁决、不由 UA 猜测：
  - 桌面 `web/` 首帧脚本 `/entry-switch.js`：compact（<600px）访问**根入口**（`/`）→ `location.replace('/m')`；
  - 移动端 `/m/entry-switch.js`：large（≥1280px）访问 `/m` **根入口** → `location.replace('/')`；
  - medium/expanded（600–1279）双端皆宜，不强制弹跳（移动端自有宽栅格形态）；
  - `?ui=mobile|desktop` 与 `sessionStorage['llmgw_ui_mode']` 覆盖优先于自动判定；深链（非根入口）永不弹跳；
  - 首帧脚本必须是**外部文件**（CSP `script-src 'self'` 禁 inline，桌面 theme-init.js 同款先例）；`replace()` 不留历史，防返回键在两侧之间成环。

- **R1 触控目标 44 vs 48**（06 §7 × 11 §1）：**新增触控控件一律 ≥48 CSS px**；44px 是存量控件下限，不是新标准。06 §7 已同步修订。（本仓 `web/` 侧已按此修 4 文件 5 处并加门禁，见 10 §4.6.5。）
- **R2 代次原语统一**（07 queryRevision × 13 contextKey/generation × 06 entryId+renderEpoch）：定义三层正交代次——
  1. `sessionEpoch`：会话代次（登录/登出/换号递增），所有缓存与在途请求的根键；
  2. `queryRevision`：查询代次（下拉刷新/筛选变更递增），同 sessionEpoch 内隔离新旧响应，旧响应不得回写；
  3. `renderEpoch`：渲染代次（NavigationEntry 级，异步标题解析携带，不匹配即丢弃）。
  校验关系：请求提交前检查 `sessionEpoch`；响应回写前检查 `queryRevision`；标题/快照回写前检查 `entryId + renderEpoch`。三者缺省值均为 0，只增不减。
- **R3 capabilities v2 合并 schema**（08 §3 × 14 §2）：合并后的单一接口——
  ```ts
  interface HyperCapabilities {
    protocolVersion: 2
    platform: 'web' | 'ios' | 'android'
    navigation: boolean          // BackDispatcher 可用
    back: 'none' | 'commit-only' | 'interactive'  // 预测返回三档
    focusWorkspace: boolean
    insets: 'css-only' | 'native-css-px'
    keyboard: 'viewport-only' | 'native'
    haptics: boolean
    appLifecycle: boolean        // 原生前后台事件
    tasks: { durableLocal: boolean; cloudDetached: boolean; continuation: 'foregroundOnly' | 'bestEffort' | 'osScheduled' | 'activeAudio' | 'serverDurable' }
    recording: { available: boolean; background: boolean }
    recognition: { pdfText: boolean; ocr: 'none' | 'fast' | 'accurate'; asr: 'none' | 'fast' | 'accurate' }
    agent: { available: boolean; skillFormat: string }
  }
  ```
  Web 端基线：`back:'commit-only'`、`insets:'css-only'`、`keyboard:'viewport-only'`、其余原生项 false。未知字段忽略，未知能力默认不可用（14 §2 不变）。（本仓 `web/` 侧 `capabilities.ts` 已对齐此 schema，含六字段补齐，见 10 §4.6.6。）
- **R4 内容区滑动返回方向**（07 §5 × 11 §1）：**Web 端不实现内容区滑动返回**——系统/浏览器返回 + 顶栏返回钮已覆盖，自建横滑与 iOS 系统边缘手势、表格横滚、Tab 横滑三重冲突。原生壳轮再按 11 §1 裁决 iOS 边缘（系统右滑）与 Android（预测返回）。
- **R5 缓存联动**（13 §6 × 06 §3）：页面快照 LRU（5）驱逐时，**保留 NavigationEntry 记录、清除其 `view` 快照**——导航时间轴仍完整，回到被驱逐页时重新走 initialLoading（有骨架），不静默复用过期滚动位置。entries 80 上限淘汰仍按 06 §3（截断 forward 分支）。
- **R6 useHyperPage / useHyperOverlay 返回值**（补 06 §8 留白）：
  ```ts
  useHyperPage(opts): {
    setTitle(title: string): void      // 更新注册标题并触发重解析
    snapshotView(): void               // 立即采集滚动/Tab/筛选进当前 entry
    release(): void                    // onUnmounted 自动调用，可显式提前
  }
  useHyperOverlay(opts): {
    setTitle(title: string): void
    close(result?: unknown): void      // 走 beforeClose 的受控关闭
    release(): void
  }
  ```
  `operations[].type` 枚举：`push | replace | pop | deepLink | restore | present | dismiss | refresh`。
- **R7 无 Tab 区域的 tableTop**：`tableTop = effectiveTop + 0 + 区域工具栏高度`（Tab 项按 0 计）；"目标 pane 至少一个剩余可见视口高度" = `viewport 高度 − tabTop − 工具栏高度 > 0` 时才渲染切换目标。
- **R8 时间预算交叠**（06 §6 × 13 §2）：滚动恢复等待期内维持 `initialLoading` 骨架态（150ms 阈值照常生效）；2s 恢复预算到期仍无数据 → 骨架切换为"正在加载，可返回/可取消"文案；3s 阈值（13 §2）仍为最终兜底。恢复成功后骨架直接让位，不经过空态。
- **R9 feature flag**（补 08 §12 留白）：读取 API `Hyper.flags.get(name)` / `set(name, value)`；存储 `localStorage['hyper.flags']`（JSON，非凭据）；缺 flag 时默认值 = 该能力的实现矩阵（§3）状态——已实现的 true、未实现的 false；旧宿主读不到 flag 不报错，按默认值降级。
- **R10 弹层深链判据**（补 06 §3 留白）：仅"需要分享/收藏/通知直达的详情弹窗"用 route query 承载；判据 = 产品明确要求外链直达。网关移动端首轮全部弹层为内存弹层（Sheet/确认框），不深链——运维端无分享场景。

## 5. API 映射与鉴权

| 端点 | 鉴权 | 移动端用途 |
| --- | --- | --- |
| `POST /api/auth/token` | 公开 | 登录；HttpOnly `llmgw_session` cookie + body `access_token`（内存持有，不落 localStorage） |
| `GET /api/auth/me` | session | 冷启动水合（401 = 未登录，正常路径非错误） |
| `GET /healthz`、`/version`、`/api/system/version` | 公开 | 状态条（版本/构建） |
| `GET /api/admin/dashboard/board?days=&include_operational=` | session | 总览汇总/趋势/后台任务 |
| `GET /api/credentials/monitor-summary` | session | 节点健康列表 + 每模型明细 |
| `GET /api/candidate-failures/alerts` | session | 告警时间线 |
| `GET /api/routing/available-models` | session | 模型目录（families/versions） |
| `GET/POST /api/keys`（+`/reveal` `/disable`） | session | Key 管理；**reveal 结果不缓存不预取**（13 §4） |
| `GET /api/usage/summary`、`/api/usage/by-model` | session | 用量页 |

请求层契约（复刻网关 `web/src/api/_core.ts` 语义）：cookie 优先（`credentials:'same-origin'`），无 userInfo 才补 `Authorization: Bearer`；401 → replace 到 `/login?redirect=`（06 §4）；`AbortSignal` 全链路透传；`ApiError` 携带 status + detail。

## 6. i18n 与主题

- 首轮 **zh-CN + en-US** 两语言，点路径词典（无 i18n 运行时依赖），`localStorage['llmgw_mobile_locale']`，`Accept-Language` 随请求携带。桌面端 8 语言体系不因此迁移。
- 主题：亮/暗 = 用户偏好，`html.dark` 切换 + `<meta name="theme-color">` 联动 + 首帧内联脚本防 FOUC（14 §3 不变）。

## 7. 工程门禁与部署

- `vite target/cssTarget` 含 `safari15`；仓库门禁 `verify-css-media-syntax`（范围语法计数 = 0）——01 §3 硬约束原样继承。
- 构建门禁：`vue-tsc --noEmit`（strict + noUncheckedIndexedAccess + verbatimModuleSyntax）→ `vite build`；核心运行时（导航/滚动/专注/capabilities）必须有同名 `.spec.ts`。
- 挂载：Go 侧 `MobileStaticHandler`（仿 `maintain_static.go`），`MOBILE_WEB_DIST` env 指向 dist，缺省探测 `web-mobile/dist`；`/m/*` SPA fallback，`/m/api|/m/v1` 前缀 404 防 SPA 遮蔽。deploy 脚本集成为后续轮（§9）。
- 壳层缓存与部署序号（上线后启用本地框架缓存）按 [18](./18-壳层缓存与版本自更新.md) 执行：SW scope 按网关路径前缀（`/m`）限定。

## 8. 验收用例（移动端子集）

从 15 §4 与 06 §8 提炼，首轮必须全绿：

1. **标题序列**：总览 → 节点 → 节点详情 Sheet（显式标题）→ 无标题确认框：顶栏标题依次为 `总览 / 节点 / <凭据名> / <凭据名>`（第 4 层继承第 3 层快照）。
2. **返回仲裁**：确认框打开时系统返回 → 只关确认框；Sheet 打开时 → 只关 Sheet；节点页 → 回总览；总览（根页）→ Web 停留首页（不跳外域）。
3. **下拉刷新**：总览页下拉 ≥64px 松手 → 单发一次 board 请求；刷新期间上滑触发加载不并发（互斥）；刷新失败保留旧数字 + "内容为上次更新"提示。
4. **连续加载**：模型目录滚动至距底 240px 自动追加；断网追加失败 → 列表保留 + 底部重试按钮；恢复后从失败页续载，无重复行（rowId 去重）。
5. **滚动恢复**：节点列表滚到第 N 行 → 进详情 → 返回：列表回到第 N 行（锚行优先），不跳顶。
6. **专注模式**：节点每模型明细宽表 → 专注查看：背景 `inert` + 滚动锁定；退出后原位、原 Tab、原滚动恢复；Esc/返回/退出钮三入口等效。
7. **鉴权代次**：登录态下拉刷新途中登出（另一标签页）→ 旧响应不回写，跳登录页（R2 sessionEpoch 校验）。
8. **触控目标**：抽查底栏/菜单/关闭钮 ≥48px；输入框字号 16px（iOS 不缩放）。
9. **降级**：`prefers-reduced-motion` 下刷新位移取消、文案保留；暗色下所有状态点/soft 色仍可辨（非仅色相区分）。
10. **行点击详情（03 §3.1，2026-10-04 新增；2026-10-05 销案）**：点节点行进节点详情 Sheet；模型版本行曾在首轮分支带 chevron 却无 @click（见 §10 末行）。落地线已按「二选一」的**选项①（接上详情）**销案：家族卡片行 `@click` 打开版本明细 Sheet（origin/main `ModelsView.vue:102`/`:119`），`ver-row` 死按钮不复存在，Sheet 内版本行为静态卡片（无 button 语义、无 chevron）。
11. **可点性诚实（UI-L02/L03）**：有 chevron 的行必须点了有反应；没有 chevron 的行不是按钮、不进 Tab 序；读屏遍历每行只播报一次。
12. **窄屏 320px（UI-L04）**：节点/模型/密钥列表在 320 CSS px 下无横向滚动，长模型名换行不断破；390px 为常见机型但 **320px 才是地板**（03 §3.2）。

性能目标沿用 15 §4（点击反馈 p95 ≤100ms、INP p75 ≤200ms、首交互 p75 ≤2.5s），真机测量留 §9。

## 9. 留白与后续轮

- Capacitor 原生壳（引导页、safe-area 桥、`appLifecycle`、Android 预测返回、haptics）。
- SSE 实时请求流（`/api/admin/live-stream?token=`）接入总览。
- 8 语言对齐桌面端（当前 zh-CN + en-US）。
- forward() tombstone 合并、Tab 横滑、`dvh` 之下的 iOS 15.0–15.3 支持决策（15 §2 未决项在本应用同样未决，当前按 15.4+ 目标实现、传统语法兜底）。
- 真机（iPhone Safari / Android Chrome）验收轮：15 §3 设备清单与测量方法。

## 10. 首个落地轮实现状态（2026-10-04，llm-gateway-go `feat/web-mobile-hyper`）

> ⚠️ 该首轮分支**本地未推送、已被超越**：可核验落地线是 origin/main（`5f65feace` 起）与活跃分支
> `feat/hyper-mobile-ui-2026-10-04`，下表「已交付」以两处为准核对；行点击契约销案与 R11 落地偏差见本节末两条记录。

本篇从目标协议落为真实工程。已交付与偏差登记：

| 项 | 状态 | 说明 |
| --- | --- | --- |
| `web-mobile/` SPA | ✅ 已交付 | Vue 3 + vue-router，运行时零 UI 库依赖（主包 gzip ~47KB）；8 页面 + AccountSheet + MoreSheet 全部落地 |
| Hyper 运行时子集 | ✅ 已交付 | epochs(R2)/capabilities(R3)/titleResolver/navigationContext/backDispatcher/scrollHost/pullToRefresh/continuousList/focusWorkspace 各带同名 `.spec.ts`，36 用例全绿 |
| Go 接线 | ✅ 已交付 | `cmd/gateway/mobile_static.go`（MobileStaticHandler，逐字沿用 maintain 双 SPA 先例：扩展白名单 + SPA fallback + `/m/api\|/m/v1` 404 防遮蔽）；`MOBILE_WEB_DIST` env + `web-mobile/dist`、`web-mobile` 双探测（镜像 `defaultStaticDir`） |
| R11 统一入口 | ✅ 已交付 | 两侧外部 entry-switch.js（见 §4-R11）；桌面 `web/index.html` 仅新增一行 script 引用，桌面零回归 |
| 部署 | ✅ 已交付 | `deploy-local.sh` 蓝绿链构建并平铺 `web-mobile/dist` 进 bundle（lib `dl_stage_release` 可选第 7 参，空目录兜底防旧调用方破）；根 `deploy-252.sh` 统一入口（薄封装 `scripts/deploy-252-gateway.sh`，构建+上传 web-mobile + systemd `MOBILE_WEB_DIST`） |
| 工程门禁 | ✅ 已交付 | `vue-tsc`（strict+noUncheckedIndexedAccess+verbatimModuleSyntax）、`scripts/verify-css-media-syntax.mjs` 范围语法门（含 `--self-test` 正/负对照）、vitest |
| 视觉 token | ✅ 已交付 | `--app-*`/`--kx-*` 命名与取值沿用本专题 01 §5（浅 `#1e4fd6`/暗 `#5b8cff`），但直接落值不依赖 Element Plus 变量 |
| 实现偏差 | ⚠️ 备案 | ①专注工作区以独立全屏覆盖层实现（`inert` 背景 + Esc/返回/退出三入口），工具浮层为简化版；②连续加载用于目录类长列表，总览/节点为整页拉取+下拉刷新；③「表格全页查看与编辑」覆盖节点每模型明细、密钥清单（可禁用/启用）、用量分布三处宽表 |
| **行点击详情契约（03 §3.1）** | ✅ **已销案（2026-10-05）** | 2026-10-04 复核记的不合规（`ver-row` 按钮 + chevron 无 @click、无详情目的地）经分支取证**只存在于从未推送的本地分支 `feat/web-mobile-hyper`**（`8347a388d`，不在 origin）。可核验落地线 origin/main 自 `5f65feace`（2026-10-04 16:24，早于二轮复核落档 7 小时）即按**选项①**交付：家族卡片行 `<button class="data-card family-card" @click="detail = f">`（`ModelsView.vue:102`）打开 AppSheet 版本明细（`:119`），Sheet 内版本行是静态卡片（无 button 语义、无 chevron）。全视图静态复核（2026-10-05）：NodesView `:123` 节点卡 → Sheet；AlertsView 无详情卡片退回静态行（`:41` 修复注释在案）；Keys/Login/Usage/NotFound 与壳层全部 button 均有 @click；AccountSheet 三处 chevron 行均有真实处理器（主题/语言/检查更新），信息行走 `--static` 无 chevron——**§8-11 可点性诚实同轮通过**。门禁（origin/main 临时检出实测）：vue-tsc 0 错、vitest 9 文件 44 用例全绿、`pnpm build`（含范围语法门）全绿；工作区 `ModelsView.vue` 与 origin/main 逐字节一致。 |

验收基线（本地，2026-10-04 实测 11/11 通过）：`pnpm build`+`pnpm test`（36 用例）全绿；`go test ./cmd/gateway/ -run TestMobileStatic` 全绿（含真实文件内容与 MIME 断言）；部署后 `/m` 服务 SPA、`/m/api/*` 404、390px 视口 `/`→`/m`、1280px+ `/m` 根→`/`、`?ui=` 覆盖生效、登录→总览/密钥真实数据渲染、专注宽表全页查看+Esc 退出恢复。首部署实测抓出两真缺陷已修并留锚：①静态 handler 未剥挂载前缀致真实文件被 SPA fallback 遮蔽（200 假绿教训——文件类断言必须查内容与 MIME）；②专注层 Esc/系统返回关闭路径不清视图 ref 致覆盖层滞留（关闭通知必须经 runtime 订阅同步到视图）。工程全记录：llm-gateway-go `docs/plan/2026-10-04-web-mobile-hyper-unified-entry.md`。

⚠️ 上表「行点击详情契约」一行是 **2026-10-04 第二轮复核追加的**，**不在上述 11/11 验收基线内**——
11/11 验的是静态 handler、SPA 接线、刷新/加载/专注恢复，不含行可点性。

✅ **销案记录（2026-10-05）**：上行 🔴 项以**选项①（接上详情）**在可核验落地线销案，上表行已改写并附证据。分支取证补记：`feat/web-mobile-hyper`（本节表头所指首轮分支）止于 10-04 17:08 且**不在 origin**；origin/main 自 `5f65feace`（10-04 16:24）起即携带合规版 ModelsView，早于二轮复核落档（参考仓 `007496e3`，10-04 23:27）7 小时——二轮复核审到的是被超越分支的快照，🔴 结论对落地线不成立。2026-10-05 深夜复验：上述门禁与静态复核结论对 origin/main 最新 tip 仍成立。

⚠️ **R11 统一入口落地偏差备案（2026-10-05 复验）**：§10 表「R11 ✅ 两侧外部 entry-switch.js」对落地线**只成立一半**——桌面侧 `web/public/entry-switch.js` 在 origin/main（`5f65feace`）✅，部署接线（`deploy-local.sh` 12 处 web-mobile 引用、`dl_stage_release` 第 7 参）在 origin/main ✅；但**移动侧 `/m/entry-switch.js`（large→/ 腿）未随落地线继承**（web-mobile 无 `public/entry-switch.js`，index.html 无引用）。落地实现改走「克制语义」：服务端 UA 302 只切裸入口（`/`、`/index.html`）+ 桌面客户端腿只补 iPadOS 缺口（`pointer:coarse` + 短边 ≤930）+ `?desktop` 逃生口 + `MOBILE_WEB_ENTRY_REDIRECT` kill-switch；**深链不切、不写 sessionStorage 模式键**——与 §4-R11 的「`?ui=`/`llmgw_ui_mode` 覆盖 + 双向 window class 弹跳」明确不同（2026-10-04 验收基线里的「1280px+ `/m` 根→`/`」依赖的正是未继承的移动侧腿）。是否补齐移动侧腿或修订 §4-R11 本身，留待产品拍板；在此之前 §4-R11 维持原裁决文本，落地状态以本备案为准。

> **本仓补记（2026-10-05，同日审计修正）**：`web-mobile/` **主体工程与 Go 接线
> （`cmd/gateway/mobile_static.go`）已并入 `origin/main`**（本专题主形态分支
> `feat/hyper-mobile-ui-2026-10-04` 未检出该目录）。⚠️ **本表 §10 有两行「已交付」
> 在 origin 不可核验**：R11 entry-switch.js 与 deploy 管线接线（`dl_stage_release` /
> `MOBILE_WEB_DIST`）在 origin 全部分支零命中，且所指的 `feat/web-mobile-hyper`
> 分支不在 origin——这部分工作只存在于构建会话的本地工作区/部署机克隆。
> 显示修复延续在 `feat/hyper-display-fixes`（未并入 main）。详见 18 号 §10.2。
> 两架构并存期的互操作边界：
> ①`/m` 与 `/` 共用同一后端与鉴权，互不写对方 localStorage 键
> （`llmgw_ui_mode` 是唯一共享键，按 R11 属有意共享）；②`web/` 的 compact 档
> 与 `web-mobile/` 是**两条独立交付物**，规范条目按各自载体核对实现状态，
> 不得把一侧的「已交付」记到另一侧头上。
