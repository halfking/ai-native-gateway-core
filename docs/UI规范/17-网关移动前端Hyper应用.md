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
| 节点（凭据健康） | `/nodes` | `GET /api/credentials/monitor-summary?mode=core` | 卡片列表（状态点、effective_state、并发、模型可用数），点开 Sheet 看每模型探测明细（宽表 → 专注模式入口）+ **运维操作区**（2026-10-06，见 §11） |
| 模型目录 | `/models` | `/api/routing/available-models` | 家族分组连续加载 + 搜索（250ms debounce） |
| 密钥 | `/keys` | `/api/keys` 全套 | 卡片列表 + 创建 Sheet + 禁用/揭示（揭示走确认框，不缓存） |
| 告警 | `/alerts` | `/api/candidate-failures/alerts` | 时间线卡片 |
| 路由检查 | `/routing` | `GET /api/routing/resolve?model=` | 抽屉席位；输入模型名 → 可路由/被阻塞候选分组（2026-10-06，见 §11） |
| 供应商 | `/providers` | `GET /api/providers` | 抽屉席位；卡片 + 搜索（客户端）+ 可用性筛选（**服务端** routability）（2026-10-06，见 §11.6）；卡片可点开该供应商的**节点操作审计** Sheet |
| 请求日志 | `/logs` | `GET /api/logs` | 抽屉席位，admin 档（tenant_admin 可用）；**时间窗按租户收窄**（2026-10-06，见 §11.15） |
| 模型完整性 | `/integrity` | `GET /api/admin/model-integrity/{events,summary}` + `POST …/events/{id}/resolve` | 抽屉席位，**superAdmin 档**；**唯一的服务端分页列表** + 异常处置闭环（2026-10-06，见 §11.12） |
| 请求日志 | `/logs` | `GET /api/logs` | 抽屉席位，admin 档（tenant_admin 可用）；**时间窗按租户收窄**（2026-10-06，见 §11.15） |
| 模型完整性 | `/integrity` | `GET /api/admin/model-integrity/{events,summary}` + `POST …/events/{id}/resolve` | 抽屉席位，**superAdmin 档**；**唯一的服务端分页列表** + 异常处置闭环（2026-10-06，见 §11.12） |
| 用量 | `/usage` | `/api/usage/summary` + `/api/usage/by-model` | 汇总卡 + 模型分布，Tab 停靠 |
| 我的 | AccountSheet | `/api/auth/me` | 全屏 Sheet（用户/外观/语言/登出），02 §5 结构 |

`desktopOnly` 页面：**对账、审计、候选重排/策略编辑**（后两者是 superAdmin + 乐观并发
`expected_revision` 的写操作，见 §11.4）。它们不进移动端导航，也不做"建议桌面端"横幅。
其余原列为桌面专属的**节点运维**与**路由 explain**已于 2026-10-06 收进移动端，见 §11。

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

> **本仓补记二（2026-10-05 深夜复验，修正上方补记的部分断言）**：以最新 origin/main
> （含 `5f65feace` 2026-10-04 16:24 / `2ca4df36b` 2026-10-04 17:06）重新核验——
> ①桌面侧 `web/public/entry-switch.js` **在 origin/main** ✓（内容为克制语义：只切裸入口、
> 只补 iPadOS 缺口、`?desktop` 逃生口、不写 sessionStorage，见 §10 R11 偏差备案）；
> ②deploy 接线**在 origin/main** ✓（`deploy-local.sh` 12 处 web-mobile 引用、
> `dl_stage_release` 第 7 参、`MOBILE_WEB_DIST` 双候选探测）——上方补记「origin 全部分支
> 零命中 / deploy-local.sh 无 web-mobile 字样」对当前 origin 不再成立（审计时点与推送时点
> 的先后不再追认，以可复验的当前事实为准）；③**移动侧 `/m/entry-switch.js` 确实不存在**，
> [部分落地] 判定维持；④行点击契约（上表末行）已按选项①销案——ModelsView 在落地线为
> 家族卡片行 → 版本明细 Sheet，全视图静态复核通过，门禁（typecheck/vitest 44 用例/build）
> 在 origin/main 临时检出实测全绿。

## 11. 运维能力上移轮（2026-10-06，`feat/hyper-mobile-ui-2026-10-04`）

把 §2 原列 `desktopOnly` 的**凭据节点运维**与**路由 explain**收进移动端。契约层
`web-mobile/src/api/credentialsOps.ts`，视图 `NodesView.vue`（详情 Sheet 内操作区）
+ `RoutingCheckView.vue`（抽屉席位 `/routing`）。

### 11.1 权限矩阵 —— 按后端中间件实测，不是 UI 偏好

| 能力 | 端点 | 中间件 | tenant_admin | 证据（后端注册处） |
| --- | --- | --- | --- | --- |
| 停用 | `POST /api/credentials/set-manual-disabled` | `h.admin` | ✅（限本 tenant） | `admin/credential_monitor.go:167` |
| 恢复 | `POST /api/credentials/clear-manual-disabled` | `h.admin` | ✅ | `admin/credential_monitor.go:166` |
| 凭据检查 | `POST /api/credentials/{id}/test` | `h.superAdmin` | ❌ 403 | `admin/credential_state_handlers.go:176` |
| 批量检查 | `POST /api/credentials/test-batch` | `h.superAdmin` | ❌ 403 | `admin/credential_state_handlers.go:177` |
| 强制恢复 | `POST /api/admin/diagnostics/credential/force-recover?id=` | `h.superAdmin` | ❌ 403 | `admin/handler.go:1461` |
| 记账式复位 | `POST /api/routing/credentials/{id}/reset-state` | `h.superAdmin` | ❌ 403 | `admin/handler.go:946` |
| 路由 explain | `GET /api/routing/resolve?model=` | `h.admin` | ✅ | `admin/handler.go:929` |

`admin/handler.go:880-888` 注释明写 tenant_admin 对 `/api/admin/**` 直接 403。
⇒ **操作区按 `authStore.role` 分档渲染**，非 super_admin 不显示检查/强制恢复按钮。
统一显示再吃后端 403 会给 tenant_admin 一堆必然失败的入口，而移动端没有桌面端
「打开抽屉才发现没权限」的过程。

### 11.2 三个必须在前端兜住、后端不兜的契约

1. **`reason` 必填**：`set/clear-manual-disabled` 对空串直接 400
   （`admin/credential_monitor.go:1785-1792`、`:1689-1691`）⇒ 确认框内嵌 reason
   输入框（`AppConfirm` 为此新增默认 slot）；留空时补带凭据 id 与操作人的默认理由。
2. **force-recover 无二次确认门禁**：该端点**无请求体、只有 query `id`**，且后端
   **没有** `X-Confirm` 头校验（对比 `PATCH /api/admin/providers/{id}/enable` 有该门禁，
   `admin/node_operations.go:398`）⇒ 误触即执行 5 步状态重置，二次确认是唯一防线。
   移动端**走 `req()` 而非裸 fetch**：桌面 `EmergencyDiagnosticModal.vue:122` 绕开
   `req()` 且只发 Bearer 不发 cookie，移动端不照抄（要 cookie 鉴权 + 401 bounce +
   `sessionEpoch` 代次保护）。
3. **检查是 202 异步、不是探测结果**：`POST /api/credentials/{id}/test` 提交队列后
   立即 202（`admin/credential_state_handlers.go:30-51`）⇒ 反馈文案是「探测已提交，
   后台执行中」，**不得**显示成「探测通过」。回归判据见
   `NodesView.spec.ts`「立即探测的反馈文案」。

### 11.3 路由检查的语义

- 后端**始终返回全部候选**并带不可用原因（`admin/routing.go:273-275` 注释）⇒ 视图分
  「可路由 / 被阻塞（含 `block_reason`、`availability_recover_at`）」两组，被阻塞那组才是
  explain 的价值所在。
- **无变体时返回空 `candidates` 的合法对象**（`admin/routing.go:288-296`），**不是错误**
  ⇒ 显示「无候选凭据」而非错误态。否则「模型名打错」会被说成「加载失败」。

### 11.4 仍留桌面的部分

候选重排（`POST /api/routing/candidate-bindings/reorder`，superAdmin + 乐观并发
`expected_revision`，陈旧返回 409）、路由策略/精选/人工优先级/打分权重编辑、对账与审计。
这些是 superAdmin 写操作且带并发版本语义，移动端先不收。

### 11.5 本轮门禁

`web-mobile/` 实测：`vue-tsc -b` 通过；`vitest run` **99 用例 / 15 文件全绿**
（新增 NodesView 运维操作区 5 条 + RoutingCheckView 4 条）；`npm run build` 通过
（`RoutingCheckView` 4.91 kB / gzip 1.84 kB，`NodesView` 10.99 kB / gzip 3.89 kB）。

### 11.6 供应商视图（2026-10-06 同轮补齐）

`/providers` 进移动端抽屉席位。选它是因为桌面菜单里 `/providers` 是运维高频入口，
而移动端此前**只能从节点卡反推供应商** —— 供应商维度的「谁挂了 / 谁没绑模型 /
谁被手动停用」在移动端完全不可见。纯只读（`h.providerConsole`，`admin/handler.go:1224`），
改供应商属重操作，留桌面。

**筛选分工（易写反，已用测试钉住）**：
- 可用性筛选（`routability`）走**服务端** query ⇒ 换筛选必须**清缓存重取**。
  若只做本地过滤，用户点「不可用」看到的仍是全量筛出来的结果，与后端口径不一致，
  看着像「过滤没生效」。
- 搜索走**客户端**（250ms debounce，13 §5）⇒ 不触发重取。

### 11.7 三个端点三种响应形态（移植时最容易踩的一类）

同一批移植里，三个看起来都是「列表」的端点，返回形态**互相相反**：

| 端点 | 形态 | 证据 |
| --- | --- | --- |
| `GET /api/providers` | **裸数组** | 桌面 `web/src/api/providers.ts:130` `req<Provider[]>` |
| `GET /api/candidate-failures/alerts` | `{data, count}` 信封 | `web-mobile/src/api/alerts.ts:18` |
| `GET /api/credentials/monitor-summary` | `{credentials, count, meta}` 信封 | `admin/credential_monitor.go:686-694` |
| `GET /api/credentials/decisions` | `{credential_id, decisions, total}` 信封 | `admin/credential_monitor.go:1658-1662` |

⇒ **不要抽一个「通用解包器」**。那会掩盖真正的契约差异，让下一个维护者以为全站同形。
每个端点各自显式解包，且形状不符一律**抛错而非返 []** —— 返 [] 会让「后端改了返回
结构」显示成「数据被清空」，是一起静默故障。

### 11.8 节点详情的「近期路由决策」

`GET /api/credentials/decisions?credential_id=&limit=`，回答「这个凭据最近在承载什么
流量」。**独立于详情主数据**加载：拉不到只让决策区报错，不牵连状态字段与操作区。

★ 一条具体的**假绿**成因（已写入 `NodesView.spec.ts` 注释）：该 spec 对整个
`@/api/credentialsOps` 做 `vi.mock`。第一版 mock 漏了新导出的
`fetchCredentialDecisions` ⇒ 它是 `undefined` ⇒ 调用即抛 ⇒ 决策区永远走错误态、
从不渲染内容，而**所有旧测试照样全绿**。⇒ 模块被整体 mock 时，新增导出必须显式
登记，否则新增区域零覆盖。

### 11.9 本轮门禁（第二轮，含供应商视图）

`web-mobile/` 实测：`vue-tsc -b` 通过；`vitest run` **125 用例 / 18 文件全绿**
（新增 ProvidersView 4 条 + providers 解包 8 条 + credentialsOps 解包 6 条 +
NodesView 决策区 3 条）；`npm run css:check` 通过（29 文件）；`npm run build` 通过。

### 11.10 节点操作审计（第三轮补齐，2026-10-06）

`GET /api/admin/audit/node-operations`（admin 档，**tenant_admin 可用**；租户隔离靠 RLS，
`admin/audit_operations.go:219-223`）→ 供应商卡点开 `NodeAuditSheet`。

**★ 覆盖面实测结论（这条很容易想当然，务必先读）**：该审计面**只记供应商级**
两类操作，**不记凭据级**：

| 落库值 | 含义 | 写入处 |
| --- | --- | --- |
| `test-now` | 供应商级触发探测 | `admin/node_operations_audit.go:35` |
| `enable_toggle` | 供应商级启停 | `admin/node_operations_audit.go:63` |

两处都是 `go func()` 异步落库、不阻塞主流程（:30、:59）。
⇒ 后果：**§11 上移的四个凭据级写操作（停用/恢复/检查/强制恢复）不会出现在这里。**
所以本视图**只**回答「谁动过这个供应商」，**不得**当凭据操作审计用，
也**不**挂到凭据详情页 —— 那会让用户以为「我刚才那次停用有记录」而实际查不到，
误判成系统故障。UI 上用 `audit.coverageHint` 显式说明这个缺口。

**另一处注释与实现不一致**：`audit_operations.go:14` 注释写 operation 取值是
`test_now / enable_toggle`，但真正落库的是 **"test-now"（连字符）**，:35。
过滤器若照抄注释会**永远查不到行**。`operationLabel` 按连字符版匹配并保留下划线版兼容。

**limit 硬校验**（:95-103）：`<1` 或非数字 → 400 `invalid limit`；`>200` → 静默 clamp
到 `maxAuditOperationLimit`（:57）。移动端前端先夹：`Math.trunc` → `Number.isFinite`
→ `min(·, 200)`，且 `<1` **不发** limit（让后端用默认 50，而不是收一个非法值）。
判据是**观察实际 URL**，不是「函数被调用过」。

**第四种响应形态**（延续 §11.7）：本端点返回 `{entries, count, limit}`
（:229-233）。至此四形态并存，仍不抽通用解包器，形状不符一律抛错。

### 11.11 本轮门禁（第三轮，含节点操作审计）

`web-mobile/` 实测：`vue-tsc -b` 通过；`vitest run` **150 用例 / 20 文件全绿**
（新增 NodeAuditSheet 9 条 + nodeAudit API 15 条 + ProvidersView 审计入口 1 条）；
`npm run css:check` 通过（30 文件）；`npm run build` 通过。

**一处测试预期写错、实现是对的**（留作判据样例）：我第一版断言
「`limit: Infinity` 应被夹到 200」，实测实现是**不发 limit** ——
因为 `Number.isFinite(Infinity)` 为 false，根本进不了夹取分支。
是我把 `Math.trunc(Infinity) === Infinity` 想成了「能过 isFinite」。
已改成断言「不发」，并把这段推理写进测试注释。


### 11.12 模型完整性异常（第四轮补齐，2026-10-06）

`/integrity` 抽屉席位。**整段 superAdmin 档**（`admin/handler.go:924-925` 两处注册都包
`h.superAdmin`），tenant_admin 一律 403 ⇒ 视图按 role 分档，非 super_admin 只显示权限说明、
不挂列表。

选它的理由：这类问题（模型名对不上 / 参数漂移 / 指纹变化）表现为「请求失败或结果诡异」，
但**不落在节点健康上**（凭据探测是好的）。移动端此前完全没这个面，排查只能开电脑。

**★ 本页是移动端唯一的服务端分页列表**，与节点/模型/供应商/密钥页（「一次拉全量 +
客户端切片」）根本不同，别照抄它们：

| 差异点 | 本页 | 其余列表页 |
| --- | --- | --- |
| 数据源 | `limit`+`offset` 取一页 | 单端点返回全量 |
| `total` | `resp.count` = **COUNT(\*) 命中总数**（`model_integrity.go:140`/`:202`） | `items.length` |
| `fetchPage` | `(page-1) * PAGE_SIZE` | `if (page > 1) return []` |

`total` 若误用 `resp.events.length`，「已加载 X / 总计 Y」会两数永远相同、看起来像「已全部加载」。

**处置端点** `POST /api/admin/model-integrity/events/{id}/resolve`（:271-310）有个与同批
`set-manual-disabled` **不同**的宽松点：body 解析用 `readJSON` 且**显式忽略返回错误**
（`_ = readJSON(r, &body)`，:284）⇒ `resolution_notes` **不是必填、解析失败也不 400**。
移动端因此不强制填备注，但**仍**给输入框——「为什么处置」是复盘时唯一有用的信息。
id 非法 → 400 `invalid integrity id`（:276-279）。

**字段易混**：JSON tag 是 `detected_at`（:19），SQL 那一列叫 `ts`（:149）。同一字段，勿混。

### 11.13 ★★ 本轮抓到一个 flaky 判据（判据自身有 bug，不是产品缺陷）

`IntegrityView` 的「page→offset 映射」用例**同一份代码 6 次里 3 次失败**。两处叠加：

1. **`loadNext()` 不是无条件的**：`continuousList.ts:111-119` 在 `_state` 为
   `initialLoading / refreshing / loadingNext / exhausted / loadFailed` 时**静默 return**。
   测试 `await flushPromises()` 不等状态机就调它 ⇒ 请求被随机丢弃。
   ⇒ 必须轮询等 `controller.state` 离开 loading 再调。
2. **`vi.clearAllMocks()` / `m.mockClear()` 不清 once 队列**（只清调用记录）：
   - 前一用例若设了 `mockResolvedValueOnce` 而因 `v-if` 分支没被消费，该实现**串到下一用例**；
   - 首屏 `autoFill`（07 §3，首屏补页 ≤3 页）排的第 2、3 页请求可能在 `mockClear`
     **之后**才落地 ⇒ `mock.calls.at(-1)` 取到的是补页的 `offset=20` 而非首屏的 `0`。
   ⇒ 需要重置**实现队列**时用 `vi.resetAllMocks()` / `m.mockReset()`。

★ **教训分三层，别只记结论**：
- 「单跑绿、整跑红」先怀疑**跨用例污染**，而不是「随机」；
- 定位手段是 `-t` 二分（哪个前序用例组合会污染），不是加 `sleep`；
- 症状「收到的数据是**另一个用例造的**」是污染的强信号 —— 本次收到 `id=1` 而本用例造的是
  `id=5`，而 `id=1` 正是前一个用例的数据。**看到这种症状要直接去查前序用例的 mock 队列**。
- 加了 `mockReset` 后仍偶发，才暴露出**第二层**原因（autoFill 补页竞态）。一次修复不绿
  不等于根因找全了。

**验证标准改为「连跑 N 次」**：修完连跑 8 次全绿才算稳。本页判据已按此验收。

### 11.14 本轮门禁（第四轮，含模型完整性）

`web-mobile/` 实测：`vue-tsc -b` 通过；`vitest run` **168 用例 / 22 文件全绿**
（新增 IntegrityView 5 条 + modelIntegrity API 13 条）；`npm run css:check` 通过（31 文件）；
`npm run build` 通过。**并按 §11.13 的标准连跑 8 次全绿**（flaky 已消除）。


### 11.15 请求日志（第五轮补齐，2026-10-06）

`/logs` 抽屉席位，`GET /api/logs`（admin 档，**tenant_admin 可用**）。选它的理由：节点/
供应商页回答「现在谁不健康」，请求日志回答「刚才那次到底发生了什么」——后者才是排障起点。

**★ 时间窗会被后端静默收窄（`admin/logs.go:476` → `clampQueryWindowForTenant` :1336-1341）**：

| 租户 | 最大跨度 | 常量 |
| --- | --- | --- |
| 非 default | **72h（3 天）** | `tenantLogQueryWindow` :1322 |
| default / 空 tenant | **366 天** | `maxLogQueryWindow` |

收窄方式是「**以 end 为锚、回推上限**」，且**响应里没有任何字段说明窗口被改过**，`count`
也只是被改过之后的窗口内的计数。⇒ 用户选「最近 7 天」在非 default 租户上只拿到 3 天，
看起来像「最近 3 天真的没请求」。

⇒ 移动端的修法是 **UI 层就不提供超限选项**（`availableRanges` 按 `maxWindowHours()` 过滤），
并额外显示 `logs.windowCapped` 提示。**不是**「先让用户选、事后告诉他被改了」。

**分页参数与 model-integrity 不同**：`/api/logs` 用 `page` / `page_size`
（`logs.go:478-488`，上限 500），**不是** `limit` / `offset`。两个分页端点参数名不同，
照抄会静默停在第 1 页（多传的 `limit`/`offset` 被后端忽略，`page` 保持默认 1）。

**第五种响应形态**：`{items, count, aggregate}`（`logs.go:831-833`）。本仓至此五形态并存，
仍不抽通用解包器。

**成本字段双形态**：`cost_usd` / `cost_display` 可能是 number 也可能是 string（列存路径按
文本返回）。`costNumber()` 先挡空串 —— `Number('')` 是 **0** 不是 NaN，只判
`Number.isFinite` 会把「无成本数据」渲染成「$0.0000」，那是在断言一个没有依据的数字。

### 11.16 ★★★ flaky 收敛：这道题修了三轮才收敛（判据写法本身就是答案）

`§11.13` 记录的 flaky 在新增 `/logs` 后**再次以同型复发**，且暴露出更深一层。
三轮尝试的收敛过程（每一轮的失败都指向下一层）：

| 尝试 | 做法 | 结果 | 它暴露的下一层 |
| --- | --- | --- | --- |
| 1 | `loadNext()` 前轮询 `controller.state` | 6 次里 3 次红 | ② state 会在 autoFill 轮间回 idle |
| 2 | 轮询「不忙 **且** `mock.calls.length` 不变」 | 10 次里 8 次红 | ③ `mock.calls` 是**非响应式**的，定时器里读它同样有竞态 |
| 3 | **固定时长真实等待**（macrotask 定时器） | **10 次全绿** | — |

根因是 `autoFill`（`continuousList.ts:134-152`，07 §3 首屏补页 ≤3 页）本身是
`for` 循环里 `await waitForIdleish()` **逐页**推进的：每轮之间 state 短暂回 idle ⇒
任何「只看状态」的判据都会在间隙误判完成并提前返回。

★ **这一轮的真正教训不是「用 sleep」**，而是：
- **判据不该断言调用顺序**。`at(-1)` 等于把「补页正常发生」当成失败 ——
  改成「请求集合**出现过** offset=0 / offset=20」后，判据与补页行为解耦，
  这才同时消掉了 ② 和 ③。
- 判据脆弱时，先问「**这个断言是否依赖被测系统没做的事**」。autoFill 不补页时
  `at(-1)` 恰好成立，补页时就不成立 ⇒ 它验的不是映射规则，是**时序巧合**。
- `vi.resetAllMocks()` 也不能替代等待：它清队列，不清**在途**。
- 最终验收仍是**连跑 10 次全绿**（本轮达成）。

### 11.17 本轮门禁（第五轮，含请求日志）

`web-mobile/` 实测：`vue-tsc -b` 通过；`vitest run` **182 用例 / 24 文件全绿**
（新增 RequestLogsView 5 条 + requestLogs API 9 条）；`npm run css:check` 通过（32 文件）；
`npm run build` 通过。**并按 §11.16 的标准连跑 10 次全绿**。


### 11.18 自查发现：导航层缺权限门控（2026-10-06，与 §11.16 同批）

完成度审计时自查发现的 —— **不是用户报的**。`AppDrawer` 的
`v-for="item in DRAWER_NAV"` **无条件渲染全量**，而 `/integrity` 整段是
`h.superAdmin`（`admin/handler.go:924-925`）⇒ tenant_admin 抽屉里躺着一个
点进去必然 403 的入口。

这与 §11.1 对凭据操作区定的规矩是**同一个问题的上下游两端**：
「按 role 分档渲染，不是一律显示再吃后端 403」。导航是最上游那一端，
在抽屉层就该挡住，而不是等用户点进去看报错再回头。

修法（`config/appNav.ts`）：
- `NavItem.requiresRole` 字段（当前只有 `super_admin` 一档在用）；
- `navItemsFor(items, role)` 过滤器，`AppDrawer` 改用它。

★ **一处刻意的「不过滤」**：admin 档页面（`/logs`、`/providers`、`/routing`）
对 tenant_admin **照常显示**。它们走的都是 `h.admin`，后端允许 tenant_admin
（只是限定在自己 tenant 内）⇒ 在这一层挡掉反而是误伤，会让本该能看的功能消失。
**「按权限分档」不等于「把非超管能看的都藏了」。**

判据（`AppDrawer.spec.ts`，10 条）除了三条门控行为，还钉了
「role 为空（未 hydrate 完成）时也不要把 admin 档页面全藏起来」——
登录前 `role` 是 `''`，若过滤逻辑写成「读不到角色就保守隐藏」，
用户会在登录前看到一个空抽屉。AppDrawer 测试必须挂**真 router**
（`createMemoryHistory`）而不是 mock `useRoute` —— 否则
`isNavItemActive` 与真实路由表脱节，测出来的东西不成立。

### 11.19 本轮门禁（第六轮，导航权限门控）

`web-mobile/` 实测：`vue-tsc -b` 通过；`vitest run` **192 用例 / 25 文件全绿**；
`npm run css:check` 通过（32 文件）；`npm run build` 通过，且 **4 个新页面各自
产出独立 chunk**（`IntegrityView-*.js`、`RequestLogsView-*.js`、
`ProvidersView-*.js`、`RoutingCheckView-*.js`）⇒ 证明它们真被路由表 lazy 引用、
不是「文件写了但没接上」。

**flaky 观测记录（如实）**：本轮 10 连跑中出现过 1 次失败，但**连续 15 次单跑
与 8 路并发压测均未复现**。捕获失败的那次是**我同时起了两个 vitest 进程**
（争 stdout）。按 §11.16 的纪律不放过，但也不虚报：目前证据指向
「并发跑测试工具本身」而非判据或产品缺陷；AppDrawer 这 10 条判据**不含**
§11.16 那类时序断言（无 autoFill 参与），与已定位并收敛的两处 flaky 形态不同。


### 11.20 ★★ 修正一个**已存在**的误报缺陷：降级被显示成「真的一分钱没花」

这一轮不是加页面，是**修**。查 `cost-trend` 时读到后端原注释
（`admin/usage_enhanced.go:48-52`）：

> 「这里的降级形状是『可选视图未迁移』⇒ 200 + 空 entries + total_cost 0，
> 页面上表现为一张空饼图。**若不标记，它与『这段时间真的一分钱没花』同形。**」

**移动端此前正落在这个坑里**：`UsageView` 的 summary 降级只在
「请求数」一张卡的 `hint` 上提示，其余 5 张卡（tokens / cost / credits /
成功率 / 延迟）**照常显示 0**。⇒ 一次后端可选视图缺失，被用户读成
「这段时间零调用、一分钱没花」。

修法分两层：
1. **summary 降级 → 整块声明**，降级时**不再渲染那 6 张含 0 的卡**。
   判据反向锁定：若改回「照常显示 0 + 一句小提示」，`UsageView.spec.ts` 会红。
2. 新增 `/api/usage/cost-trend` 维度切换（model / provider / key / application /
   tenant，后端 `planCostTrend` 的维度集），并把三态显式抬成
   `readCostTrend() → 'ok' | 'degraded' | 'empty'`：
   - `degraded` 时 `costIsMeaningful: false` —— 数字照样透传（UI 可能要显示），
     但调用方无法把它当零花费读；
   - `degraded` 与 `empty` **强制互斥**，判据里有专门一条反向用例守住。

★ 后端那行注释还记了一个真实教训，可直接迁移：**同文件的 PeriodCompare /
CacheEconomics 先后加了 `degraded` 标记，CostTrend 自己漏了** ⇒
「本轮已修降级载荷」这句话对自己文件都不成立。移动端读同一族降级载荷时
要**逐端点核对它带不带标记**，不能因为「这个端点我加了降级处理」就假定
同族其它端点也有。

### 11.21 本轮门禁（第七轮，成本趋势 + 降级修正）

`web-mobile/` 实测：`vue-tsc -b` 通过；`vitest run` **205 用例 / 27 文件全绿**
（新增 usageCostTrend 9 条 + UsageView 4 条）；`npm run css:check` 通过（32 文件）；
`npm run build` 通过；**连跑 10 次全绿**。
另附 i18n 键集核对：`zh-CN` / `en-US` 各 **345 键，零差异**（新增的
`usage.dim.*` 五个维度键也在其中）。

**一处自己踩的坑（已修）**：新增的 `usage.costTrend` 等 9 个键最初被插到
`models:` 词典里（脚本按「第一个 `keys: {`」定位，而 `models` 在 `keys` 之前），
运行时表现为**页面直接显示字面量 `usage.costTrend`**（i18n 缺键回退键名）。
是那 4 条视图测试先红才暴露的 —— 若只跑 API 层测试就漏过去了。


### 11.22 ★ 同一缺陷的第二处副本：HomeView 首屏汇总（比 §11.20 更严重）

上一轮修完 UsageView 后，本轮把「降级处理」扩大到全部页面，**HomeView 是同一个
缺陷的另一份拷贝，且更严重**。后端原话（`admin/dashboard_board_queries.go:159-164`）：

> 「42P01：主聚合查询缺表/缺视图，上面所有数字都是 0。这个载荷仍然是 200
> （保持既有降级形状），但必须自报家门。
> 数据源 X 不可用，**本页所有汇总数字（请求/Token/费用）均为 0，不可作为结论**」

移动端原先只在「请求数」「Token」两张卡挂 `summaryMissing` 提示，其余 6 张
（费用 / 积分 / 成功率 / 延迟 / 活跃 Key / 活跃模型）**照常显示 0**。

⇒ 主聚合表缺一次（42P01），首屏会显示「今天没人用、花了 0 块、成功率 0%、
活跃 Key 0 个」。这在事故现场是**主动误导**。现已与 UsageView 同一修法：
降级时整块声明、不再渲染那 8 张含无依据 0 的卡，并透出后端自带的 `summary_hint`
原话（那是后端对「不可作为结论」的权威表述，比前端自己改写更可信）。

**★ 同时辨析了一处「看着像漏、其实对」**：`degraded` / `degraded_reason` 这两个键
**不能**声明到 `BoardSummary` 上。后端 `dashboard_board_queries.go:163-169` 有明确注释：
不在 summary 层写它们，因为那会让「积分降级」与「整屏降级」共用一对键 →
积分卡显示「不可信」而整屏明明有真实数字。**顶层 `board.degraded` 与
`summary` 内部的降级是两种作用域，键名相同但含义不同** —— 这是后端已经踩过的坑，
移动端照抄会踩同一个。

**★ 顺带补上一个「类型比实现少字段」**：`summary_missing_view` 后端一直会写
（:160），但本仓 `BoardSummary` 漏了它。是新写的视图测试用这个键构造夹具时
被 `vue-tsc` 抓到的 —— 类型缺字段会逼着人用 `any` 绕开，而绕开之后真实载荷
里的信息就再也没人看了。**后端返回了、前端类型没声明 ⇒ 等于没有这条信息。**

### 11.23 关于「为什么这轮没加新页面」

§11.20/§11.22 连续两轮都在**修**同一个族的缺陷，而不是往前堆功能。理由：
目标里的「重点检查」四字指的正是这类检查 —— 移动端已上线的 11 个页面里，
凡涉及汇总数字的都可能把「后端降级」显示成「真的没数据」。堆新页面不会减少
这类误报，只会在旁边再添几个同样的坑。

**判据要求**（`HomeView.spec.ts` 三条 + `UsageView.spec.ts` 四条）：
- 整屏降级 ⇒ 统计卡**不渲染**（反向判据：改回「照常显示 0 + 小提示」就红）；
- 未降级 ⇒ 统计卡**照常**渲染（防「修法写成永远不显示」）；
- ★ **积分单独降级（`credits_missing_view`）时整屏统计卡仍要显示** ——
  积分走的是另一个数据源（`queryTotalCreditsCharged`，:136-137），
  只有积分不可信。把它也整块降级，等于把「只有积分没数据」说成「整页都没数据」。

### 11.24 本轮门禁（第八轮，HomeView 降级修正）

`web-mobile/` 实测：`vue-tsc -b` 通过；`vitest run` **208 用例 / 28 文件全绿**
（新增 HomeView 3 条）；`npm run css:check` 通过（32 文件）；`npm run build` 通过；
**连跑 10 次全绿**。


### 11.25 生命周期与写操作反馈：两处「功能看着有、边界没处理」

本轮把降级面扫完（`alerts` / `keys` / `models` 三端点核对下来**不返回**降级形状，
降级面到此扫完），转向另一族：**看着能跑、边界不成立**。

#### ① 在途请求不随卸载取消（六页共用路径）

`HyperList` 的 `onBeforeUnmount` 此前只 `observer?.disconnect()`，**没管在途请求**：
用户在列表加载途中切走（compact 档底栏/抽屉跳转非常常见）⇒ `fetchPage` 的 await
续体照样回来改 `_items` / `emit()`，而组件已经不在了。

Vue 3 对「卸载后 setState」只产生一次警告、不影响别的页面 ⇒ **这个洞能一直躺着，
单测也测不出来**（测试里卸载组件照样全绿）。

修在 **controller 层**而非各视图：`ContinuousListController.dispose()`（递增
revision + abort + 清 listeners），`HyperList.onBeforeUnmount` 调用它。
理由与 §11.24 同款：六页走同一条路径，逐个视图补 abort 是「给每个用例加隔离助手」
式的错解 —— 落盘/卸载路径会随实现增长，测试作者未必知道（**包括我自己**）。

`HomeView` 另有一处同样的问题（不走 HyperList，自己 `Promise.allSettled` 且不传
signal），单独补了 `AbortController` + `disposed` 双保险。

#### ② ★★ `doDisable` 既无 catch 也无错误位 —— 停用失败**完全静默**

`KeysView.doDisable` 原实现：

```ts
try { … await disableKey(id) … }
finally { disableTarget.value = null }
```

**没有 catch**。停用/启用失败时（409 已被别处停用 / 403 权限 / 500），用户点完
确认框，界面毫无反馈，**误以为成功了**。而同文件的 `submitCreate` 有完整
catch + `createError` 提示位 ⇒ **同页两套标准，是漏写不是有意设计**。

已修：加 catch + 页面级错误位，403/409 单独说人话。错误位放在**页面级**而不是
确认框内 —— `AppConfirm.onConfirm` 立即把 `modelValue` 置 false，框一关，框内的
提示没人看得到。

#### ③ 判据又踩了「依赖被测系统的浮动行为」

修完跑 15 连跑，run 11 抓到 IntegrityView 的 flaky：
`expected [40] to include 20` —— 判据假定 `loadNext()` 落在第 2 页，但首屏
`autoFill` 会补到**第 3 页**（offset=40），所以「多等一会儿」也救不回来。

⇒ 这是 §11.16 那条教训的**第三次复发**，形态又变了一点：
前两次是「断言调用**顺序**」（`at(-1)`），这次是「断言**具体页号**」。
共同点仍是「断言依赖了被测系统一个会浮动的量」。改法一致：验**关系**
（offset 恒为 `PAGE_SIZE` 的正整数倍、且存在非 0）而不是验**取值**。

★ 三次复发的判据清单，直接可复用：
- ✗ `at(-1)`（依赖调用顺序）
- ✗ `mockClear()`（不清 once 队列 / 在途）
- ✗ 轮询 `state`（autoFill 轮间会回 idle）
- ✗ 轮询 `mock.calls.length`（非响应式，同有竞态）
- ✗ 断言「第 N 页」（补页停在哪页会浮动）
- ✓ 验**关系/集合**，不验**顺序与具体取值**

### 11.26 本轮门禁（第九轮，生命周期 + 写操作反馈）

`web-mobile/` 实测：`vue-tsc -b` 通过；`vitest run` **213 用例 / 29 文件全绿**
（新增 KeysView 5 条）；`npm run css:check` 通过（32 文件）；`npm run build` 通过；
**连跑 15 次全绿**。


### 11.27 触控热区 R1：`web-mobile/` 此前**无门**，而本专题自己就违反了 5 处

R1 原文（§4-R1 × 06 §7）：「**新增**触控控件一律 ≥48 CSS px；44px 是存量控件下限，
不是新标准。」桌面 `web/` 侧有门（10 §4.6.5 记「已加门禁」），**`web-mobile/` 没有**。

实测后果：本专题 2026-10-06 新增的 4 个视图里，**5 处 chip 全部低于 48px**：

| 位置 | 原值 | 所属 |
| --- | --- | --- |
| `.logs__chip`（窗口筛选） | 36px | §11.15 |
| `.logs__chip--sm`（状态筛选） | 32px | §11.15 |
| `.providers__chip` | 36px | §11.6 |
| `.usage__dim` | 36px | §11.20 |
| `.routing__go` | 40px | §11.2 |

根因与 §11.25 的 dispose 缺口同族：**规范写了、没人验 ⇒ 等于没有**。而且这些 chip
是**自定义选择器**，绕开了 `shared.css` 的 `.btn`（44px 存量基线），
所以连「复用 .btn 就自动合规」这条兜底也没有。

已修 5 处 + 新增两道门：

#### ① `scripts/verify-touch-targets.mjs`（接进 `npm run build` 前置）

扫 `src/views` + `src/components` 下 `.vue` 的 `<style scoped>`，报出
`文件:行号  选择器  min-height: Npx`。

**不扫 `shared.css` 的 `.btn` / `.btn--sm`**：它们是 44px 的**存量**基线，R1 明确
「44px 是存量下限，不是新标准」。把存量也扫进来，这道门第一天就红 ⇒ 大家学会忽略它
——**门一旦变成背景噪音就等于没有**。存量豁免用显式 `/* R1-legacy */` 标注，
留下可审计痕迹而不是悄悄放过（当前 `UsageView.usage__tab` 即此例）。

#### ② `scripts/verify-touch-targets.selftest.mjs` —— 给门自己搭前提

照 `settle-selftest.mjs` 的先例：「只测该过的」是判据自证的陷阱。一道**只会放行**的
门和一个坏掉的门，在绿灯时完全无法区分。样本覆盖 5 组正反两侧。

★ **这道自测立刻抓出了门自己的三个 bug**，且三个都产出过**漂亮的绿灯**：

| # | 门里的错法 | 表现 | 抓到它的自测组 |
| --- | --- | --- | --- |
| 1 | `new URL('..').pathname` | macOS 带 `/private` 前缀，扫 0 个文件 | 组 1 |
| 2 | 改 `fileURLToPath(new URL('..'))` | 仍指脚本**父**目录 ≠ web-mobile/ | 组 1 |
| 3 | `dirname(脚本)` | 少退一层，ROOT 指到 `scripts/` | 组 1 / 组 5 |
| 4 | 选择器正则用字符类白名单 | 样本 4 漏匹配，报成 `(file scope)` 定位不到人 | 组 4 |

第 1/2/3 条尤其值得记：**它们当时都报 OK**。是「扫到 0 个文件必须红」这条自查
（组 5）把它们全照出来的 —— 没有那条，一个扫不到任何文件的门会一直绿下去，
而所有人都会以为 R1 有门禁。

★ 门跑通了还只是「能跑」。**变异证明它有牙**：把 `.providers__chip` 改回 36px，
门立刻转红并精确报出 `src/views/ProvidersView.vue:234 .providers__chip min-height: 36px`。

### 11.28 本轮门禁（第十轮，触控热区 R1）

`web-mobile/` 实测**六道**：
1. 触控门自测 **11/11**
2. `vue-tsc -b` 通过
3. 触控热区门 通过（29 个 .vue）
4. `vitest run` **213 用例 / 29 文件全绿**，**连跑 10 次全绿**
5. `css:check` 通过（32 文件）
6. `npm run build` 通过（**已含触控门 + css 门前置** —— 门不进 build 就会被绕开）

### 11.29 ★★★ i18n 键集门 + 一个恒绿的门：`@media` 范围语法（第十一轮）

本轮加 i18n 门的过程中，撞上一件必须单独记的事：**一道已接进 `build` 的门，
它宣称的保证和它实际检查的不是一回事。**

#### (1) i18n 键集门（新增，第三道前置门）

`verify-i18n-parity.mjs`：zh-CN / en-US 两份词典键集必须完全一致，且不得有
「值 === 键名」的未翻译项。

**这不是假想需求——本轮进门禁前就抓到了真的错**：
`keys.errForbidden` / `keys.errConflict` 在 en 侧落进了 `home:` 词典。
按中英两份词典的行序手工定位插入位置时极易插错（同一个键名两边出现位置不同）。

`verify-i18n-parity.selftest.mjs` **9/9**，含反例：仅 zh 多键 / 仅 en 多键 /
值 === 键名 / en 文件缺失 / 两侧都 0 键。

#### (2) ★★ `@media` 范围语法门：声称判 MQ4，实际只判 `<=` / `>=`

`verify-css-media-syntax.mjs` 的契约是「MQ4 范围语法计数必须为 0」（01 §3，
iPhone 6s / iOS 15.8.3 实证：Safari 15 在**解析期**整块丢弃该 `@media`）。

旧实现：

```js
const RANGE_RE = /\(\s*(?:min-|max-)?[a-z-]+\s*(?:<=|>=)/
```

**只匹配 `<=` / `>=`，且要求「特性在左」。** 而 MQ4 范围语法有四种书写，
其中三种含**裸 `<` / `>`**，一条都抓不到：

| 写法 | 旧门 | 说明 |
|---|---|---|
| `(width <= 600px)` | ✅ 拦下 | 旧门唯一覆盖的形态 |
| `(width < 600px)` | ❌ **放行** | 单侧裸 `<` |
| `(600px < width)` | ❌ **放行** | 值在左 |
| `(400px < width <= 600px)` | ❌ **放行** | **双向范围，真实最常见** |
| 跨行写法 | ❌ **放行** | 旧门逐行判，`@media (` 与条件不同行必漏 |

⇒ 门恒绿，而恒绿和「仓库没有违规」在输出上**完全一样**。

新判据改成两个正交约束：

1. **括注内出现裸 `<` 或 `>` 即违规** —— 传统媒体特性
   （`min-width:` / `hover` / `aspect-ratio: 16/9` / `orientation: landscape`）
   一个都没有这两个字符，四种范围语法形态全都有。
   限定在**括注内**是为了放过同行嵌套选择器：`@media print { div > span { … } }`。
2. **按「`@media` → 深度 0 的第一个 `{`」取前奏**，不再逐行判 ⇒ 跨行写法也能覆盖。
   另先剥掉 `/* */` 注释，避免文档里的反例留档把门带偏。

`verify-css-media-syntax.selftest.mjs` **11/11**，六组：四种范围形态逐一必须红 /
跨行必须红 / 8 种传统写法必须绿 / 嵌套选择器不误报 / 注释不误报 / `src` 缺失报错
（防「扫了 0 个也报 OK」）。

★ **变异证明它有牙**：把旧正则塞回门里，自测立刻从 **11/11 变 6 passed / 5 failed**，
红的正是上表那 5 个洞。这 5 条是本轮唯一「修了就真的修对了」的证据。

> **纪律（可外推到所有门）**：门宣称的判据（"MQ4 范围语法"）与门实际匹配的正则
> （"含 `<=`/`>=` 的行"）**是两个东西**。写门时若没有反例样本，
> 「门跑通了」只证明它能在自己的好样本上跑通。
> §11.27 的触控门、§11.29 的 i18n 门都按同法补了自测。

#### (3) ★ 顺带修掉一条「断言测错了对象」的用例

`KeysView.spec.ts` 原注释写着「本文件的 i18n 解析出的是**中文**（与 NodesView.spec
相反）⇒ 断言按中文写」。**那是误判**：

```
jsdom 的 navigator.language = en-US  ⇒ 本文件一直跑的是 en-US
之所以看到中文：en 侧 keys.errForbidden 当时放错在 home 词典下
                ⇒ t() 缺键回退 zh-CN（i18n/index.ts:55）⇒ 吐出中文
```

⇒ 那两条断言测到的是「**zh 回退路径**」，不是被测词典本身。
en 词典按 §11.29(1) 补正后，它们当场变红 —— 变红的是**用例的前提**，不是产品。

修法两处：

1. `clickDisable()` 的按钮文案从写死的 `'Disable'` / `'Confirm'` 改为查词典
   （`t('keys.disable')` / `t('common.confirm')`）。写死英文会让这个助手**只在
   en-US 下可用**：一旦某条用例 `setLocale('zh-CN')`，查找失败抛「停用按钮不存在」，
   **报错会把 locale 问题伪装成渲染问题**。
2. 403 / 409 两条用例显式钉 locale，并对 **zh-CN + en-US 两侧**都断言。
   要守的不变量本就与语言无关：**403 必须映射成人话，永不退化成后端原文
   `'Forbidden'`**。`afterEach` 复位 `locale` —— 它是模块级 `ref`，不复位会跨用例残留。

★ **变异**：移除 `KeysView.vue:136-145` 的 403/409 映射（退回 `err.message`），
两条用例转红，报 `zh-CN: expected 'Forbidden' to contain '权限'` ——
`zh-CN:` 前缀同时证明断言确实跑在中文下，不是只跑了一半。

### 11.30 本轮门禁（第十一轮，i18n 键集 + @media 范围语法）

`web-mobile/` 实测**九道**：
1. css 门自测 **11/11**（含变异：旧正则 6 passed / 5 failed）
2. 触控门自测 **11/11**
3. i18n 门自测 **9/9**
4. `npm run gate:selftest` 串起三道，**31 条断言全绿**
5. `vue-tsc -b` 通过
6. i18n 键集门 通过（zh-CN / en-US 各 **348 键**，键集一致，无未翻译项）
7. 触控热区门 通过（29 个 .vue）
8. `vitest run` **213 用例 / 29 文件全绿**，**连跑 10 次全绿**
9. `npm run build` 通过（**已含 css 门 + 触控门 + i18n 门前置**）

新增 npm 脚本：`i18n:check`、`gate:selftest`。

### 11.31 运维排障线上移：链路 / 详情 / 路由流水 / 调度瀑布（第十二轮）

前面几轮加的是「谁不健康」（节点/供应商/完整性），这轮加的是「**这一条请求到底
怎么了**」——排障真正卡住的地方。四个页面构成闭环：

```
请求链路 /journey            → 详情 /journey/:id        → 路由流水 /routing-log
  在途请求 + 降级横幅            逐跳事件时间线                凭据侧事件佐证
                                                      → 调度瀑布 /waterfall
                                                        时间侧分段佐证
```

与已有的 `/logs` 分工：logs 是**已结束请求的结果面**（谁/什么模型/多少 token），
本线是**过程面**（走了几跳、为什么改道、时间花在哪）。两者都不回答「为什么失败」。

#### 端点与角色档（均已**实读源码**复核，非照抄桌面）

| 页面 | 端点 | 门禁 | tenant_admin |
|---|---|---|---|
| 请求链路 | `GET /api/admin/request-journeys/queues` | `AdminMiddleware` | ✅ |
| 链路详情 | `GET /api/admin/request-journeys/{id}` | `AdminMiddleware` | ✅ |
| 路由流水 | `GET /api/credentials/routing-log` | `h.admin` | ✅ |
| 调度瀑布 | `GET /api/admin/dispatch/waterfall` + `/queues` | `wrapAdmin` | ✅ |

★ **四条全是 admin 档**，所以 `DRAWER_NAV` 里新三项的 `requiresRole` **一律不设**
（按 appNav.ts 末尾口径：只有 super_admin 档才需要在导航层挡，对比 `/integrity`）。
抽屉 6 → 9 席，**底栏仍 4 席 + 固定「更多」= 5**，未破 02 §4 的 ≤5 约束。

#### ★ 本轮真正要记的：三个「写错了不报错、只是骗人」的坑

**(1) 降级字段叫 `observation_status`，字面值是 `observation_degraded`——不是 `degraded`**

本仓库其它面（usage / board / 降级成本）一律用裸 `degraded`。照抄过来，
`if (r.degraded)` **恒为 false**，降级被显示成「真的没有请求」。
这条已用反例锁死：`裸 degraded 标记不触发横幅`（RequestJourneyView.spec）。

**(2) 详情端点不是靠 404 表示「没有」**

`request_journey.go:265-268` 的条件是
`journey == nil && observation_status != observation_degraded` 才 404
⇒ **降级且无数据时返回 200 + 没有 `journey` 字段**。
所以必须三态：`ok` / `observation_degraded`（看不到）/ `not_found`（真没有）。
合成两态就是在对用户断言一个没有依据的结论。
判据：`classifyJourneyDetail` 三态用例 + 视图级「降级时不得出现『查不到』」。

**(3) 调度瀑布这一面根本没有 `degraded` 字段**

不可观测只由 `wired === false`（投影未接上）或 `source === 'none'`（无数据源）
表达。照抄 `degraded` 会让**整个观测面不可用被渲染成「当前没有请求」**——
恰恰把排障最需要的信号抹掉。
反向锁定两条：`wired:false ⇒ 有警告`、`wired:true 且空 ⇒ 不得有警告`
（后者方向相反，是真结论，不能被误标）。

同类的一条同族教训：桌面端拉队列深度时用 `.catch(() => null)` **静默吞掉失败**，
于是「队列指标挂了」与「队列是空的」在界面上完全一样。移动端不照抄——
`queuesError` 单独渲染（判据：队列端点 500 ⇒ 必须显示错误）。

#### 其它后端约束（都在**发出请求前**处理，而不是让用户吃 400）

- 路由流水时间窗 **> 7d 直接 400**（`credential_routing_log.go:235-237`），
  不是 clamp ⇒ 时间选项按 7 天封顶 + 界面说明原因。
- 路由流水是 **`limit`/`offset`** 分页，不是 cursor，也不是 `page/page_size`
  ⇒ `offset = (page-1)*PAGE_SIZE`。照抄 cursor 端点会永远停在第 1 页。
- 调度瀑布后端注释宣称 `max 200`，**实现根本没有 clamp**（`main_dispatch.go:124-134`）
  ⇒ 前端自己封顶到 200，不依赖后端兜底。
- 请求链路 queues **无分页**（有界 FIFO ring）⇒ `fetchPage` 恒返回第 1 页并把
  `total` 设成 `items.length`，让 controller 直接 `exhausted`；否则会反复请求同一全量响应。
- 瀑布时间戳是 **RFC3339 字符串**，`b - a` 对字符串是 NaN；且缺一端时
  `spanMs` 返回 **null 而不是 0** ——「没测到」与「耗时 0ms」在排障里是**相反**的指示。

#### 本轮我自己犯的两个错（都靠门与断言逮住，值得留档）

1. **12 个 CSS 变量全部是我编的**（`--line` / `--accent` / `--text-1` …）。
   真实设计系统一律 `--app-*` 前缀。`var(--未定义)` 不报错，
   整页会**静默无样式**。已加一条全量扫描核对（扫所有 .vue 的 `var()` 引用
   对照 theme.css 已定义集），并顺手确认 `AppAccountSheet.vue` 的
   `--app-font-mono` 带 fallback（`var(--app-font-mono, monospace)`），不是缺陷。
2. **详情页标题写成 `t('journey.degraded').split('——')[0]`** —— 那个分隔符只存在于
   中文，英文下整句会原样变成标题。已拆成独立键 `journey.degradedTitle`。

另：写视图测试时又踩了 §11.29(3) 的同一个 locale 坑（jsdom 的 `navigator.language`
是 en-US，断言按中文写会全跑英文）。本轮两个 spec 都**显式 `setLocale('zh-CN')`**
并在 `afterEach` 复位模块级 locale——这已是第三次，因此该约定写进测试文件头。

#### 触控门拦下我一处 `.wf-stage` 32px —— 选择抬值而不是放松门

R1 门扫的是**所有** `min-height < 48px` 的选择器，不区分是否可点，`.wf-stage`
（只读进度行）严格说是误报。但那是 7 行用户真要读的标签/条/数值，
32px 在手机上确实挤 ⇒ 抬到 48px。**不为过门而放松门禁，也不谎标 `R1-legacy`**
（那是新代码，不是存量）。

### 11.32 本轮门禁（第十二轮，运维排障线）

`web-mobile/` 实测**十道**：
1. css 门自测 **11/11**
2. 触控门自测 **11/11**
3. i18n 门自测 **9/9**
4. `gate:selftest` 三道串联 **31 条断言全绿**
5. `vue-tsc -b` 通过
6. i18n 键集门 通过（zh-CN / en-US 各 **451 键**）
7. css 媒体查询门 通过（36 文件）
8. 触控热区门 通过（**33 个 .vue**，本轮新增 4 个）
9. `vitest run` **261 用例 / 33 文件全绿**，**连跑 10 次全绿**
   （新增 48 条：api 契约 32 + 视图 16）
10. `npm run build` 通过（已含三道门前置），4 个新视图各自产出独立 chunk

★ **变异证据**（两处，均实测转红）：
- 视图层：把 queues 的降级判据换成裸 `'degraded'`、把瀑布的
  `isWaterfallUnavailable` 换成 `false` ⇒ 4 条用例转红。
- 门本身（§11.29）：旧正则塞回 css 门 ⇒ 自测 11/11 变 6 passed / 5 failed。

### 11.33 运维排障线收尾：会话轮次 + 路由覆盖审计（第十三轮）

补完 §11.31 提到的两个遗漏项。至此排障线 6 个端点全部上移。

| 页面 | 端点 | 门禁 | tenant_admin |
|---|---|---|---|
| 会话轮次 `/turns` | `/api/admin/turns/sessions` + `/api/admin/sessions/{id}/turns` | `admin(...)` | ✅ |
| 路由覆盖审计 `/routing-audit` | `/api/admin/routing/overrides/audit` | **`h.superAdmin`** | ❌ 403 |

★ `/routing-audit` 是**整条排障线唯一的 superAdmin 档**（handler.go:1381
`RegisterAutoRouteRoutes(mux, h.superAdmin)`，auth.go:353-357），所以它的
`requiresRole: 'super_admin'`。这符合直觉：审计「谁改了配置」本身就是管理动作。

#### ★★ 本轮最有价值的一条：一个真实缺陷，是被**判据自己走不通**逼出来的

游标分页要套进 `ContinuousListController`（它传的是页号，本端点要的是不透明游标），
我第一版写的是：

```ts
const cursor = page <= 1 ? undefined : cursors.get(page)   // ✗
if (resp.next_cursor) cursors.set(page, resp.next_cursor) // 写的是 page
```

**读的是本页的游标、而本页的游标此刻还不存在** ⇒ 永远 `undefined`
⇒ **从不发送游标** ⇒ 后端每页都返回第 1 页的 20 条
⇒ 按 `stableKey` 去重后列表再也不增长 ⇒ 用户无限滚动而内容不动。

它没被任何门拦住，原因是一条**恒真判据**：

> 我先写的是「改搜索后第 1 页不带 cursor」。
> 但 `loadFirst` 会把页号重置为 1，而 `fetchPage` 对 `page<=1` **按设计就不取游标**
> —— 删掉 `cursors.clear()` 那个变异，这条**照样全绿**。
> ⇒ 它量的不是「清没清游标」，而是「第 1 页本来就不带游标」。

拆成三步才真正照出来：

1. **先量「发出去的那一枪」**：写探针打印每次 `fetchPage` 实际发出的参数，
   看到第 2 页的 args 是 `{"limit":20}` —— 没有 cursor。缺陷暴露。
2. **承认测试走不到第 2 页**：jsdom 的 `IntersectionObserver` 不触发，
   HyperList 的 sentinel 永远不预载。⇒ 用 `defineExpose({ controller })`
   让测试能主动 `loadNext()`。**这不是为测试开的后门，而是让「第二页」第一次
   真的被走到过。**
3. **修完再变异验一次**：`get(page)` 变体 ⇒ 转红（钉住了真缺陷）。

⇒ 修正后 `get(page - 1)`，探针复测：第 2 页 args = `{"limit":20,"cursor":"CUR"}`。

★ 顺带留档**判据自己走不通**的三种失败形态（都是本轮实际发生的）：
- **恒真**：断言了一个本来就成立的性质（第一版游标断言）
- **恒假**：断言了一个根本不成立的性质（第二版断言「改搜索后第 2 页没有 cursor」——
  正常态也红。重置后第 1 页会刷成新游标，第 2 页本就该带它）
- **走不到**：断言的目标路径在测试环境压根不会执行（第 2 页，需要 loadNext）

#### ★ 诚实边界：`cursors.clear()` 并没有被测试证明是必要的

删掉 `cursors.clear()` 这个变异**不会**让用例转红（实测）。原因是重置后的
第 1 页请求总会覆写 `cursors[1]`，而第 2 页只读 `cursors[1]`
⇒ 更高位的陈旧条目在被读到之前必被覆写。
所以它是**防御性、当前非承重**的（为分页中途失败等边界留的），测试与代码注释都照此写明，
不假装「这条锁住了它」。

#### 其它后端约束（同样在发出前处理）

- **同一族数据两个端点延迟字段名不同**：
  sessions 列表 `TurnGroupItem.latency_ms`（omitempty）；
  turns 树 `SessionTurnTreeItem.latency` ——
  后者源码是 `LatencyMs *int \`json:"latency"\``（session_turns_tree.go:49），
  **Go 字段名与 JSON 键不同**。照抄任一边到另一边都取不到，
  而取不到的表现是「延迟永远 undefined」而不是报错。`latencyOf` 统一收口。
- `limit` 越界是**静默回落 20**（turns_sessions.go:244-249），不是 400 ⇒ 前端夹 1..50。
- 路由审计的 `days`(1..90) / `limit`(1..1000) 越界是 **400**，
  ★ 与 `admin/audit_operations.go:95-103` 的**静默 clamp 语义相反** —— 两处不能互抄。
- `filter` 回显是 `map[string]string`，**连 `days` 也是字符串**（routing_overrides.go:424-429）。
- `override_id` 后端解析失败是**静默忽略该过滤条件**（:377-381）而不是报错
  ⇒ 发非数字会让用户以为在按 ID 过滤、实际没过滤 ⇒ 前端只发合法数字。
- `/api/admin/turns`（v2 扁平列表）**不用**：该端点 SQL 不扫两列，
  恒返回 `"request_id": ""` 和 `"child_requests": null`。

#### 延迟未知不许显示成 0ms

`latency` 是 `*int`，NULL = 未知。渲染成 `0ms` 会让「这一轮慢」被读成
「这一轮很快」，方向正好反。判据：`latency 为 null ⇒ 显示「延迟未知」`，
且**反向锁定**不得出现字符串 `0ms`。变异（把占位符换成 `0ms`）实测转红。

#### 一个被门逮住的「假绿」

`AppDrawer.spec` 的「tenant_admin 少掉的只有超Admin 档」是**白名单式**断言
（逐个列出 key 并排序后比对）。加 `/routing-audit` 后它转红——这正是它该做的，
没有选择放宽。同时补了不变量本身：「被放行的项**必须**都不是 `super_admin`」，
否则只列白名单会漏掉「某项被误标成 super_admin 导致对所有 admin 档用户消失」。

### 11.34 本轮门禁（第十三轮，会话轮次 + 路由覆盖审计）

`web-mobile/` 实测**十道**：
1. css 门自测 **11/11**
2. 触控门自测 **11/11**
3. i18n 门自测 **9/9**
4. `gate:selftest` **31 条断言全绿**
5. `vue-tsc -b` 通过
6. i18n 键集门 通过（zh-CN / en-US 各 **487 键**）
7. css 媒体查询门 通过（38 文件）
8. 触控热区门 通过（**35 个 .vue**）
9. `vitest run` **290 用例 / 35 文件全绿**，**连跑 10 次全绿**（新增 29 条）
10. `npm run build` 通过，2 个新视图各自产出独立 chunk

★ 变异证据：本轮 3 处变异，其中 2 处**如期转红**（延迟未知→0ms；
游标 `get(page)`），1 处**不转红并已如实标注为「防御性非承重」**
（`cursors.clear()`）。

### 11.35 凭据监控线上移：热力图 + 单凭据健康时间线（第十四轮）

补上「哪个**模型 × 凭据**的组合在坏」——NodesView 答「哪条路挂了」、
ProvidersView 答「哪个供应商挂了」，两者都答不了这一问，而排障时三者经常指向
不同结论（某模型整体 40% 失败，但分散在 3 条凭据上而非集中在 1 条 ⇒
问题不是某条路坏了，而是这个模型的路由面太窄）。

| 页面 | 端点 | 门禁 | tenant_admin |
|---|---|---|---|
| 凭据热力图 `/heatmap` | `GET /api/credentials/heatmap` | `h.admin` | ✅ |
| 健康时间线 `/node-health/:id` | `GET /api/admin/node-health/{id}/timeline` | `admin(...)` | ✅ |

`/node-health/:id` 是热力图的下钻，不占导航席。

#### ★★ 相邻两个端点对「超窗」的语义**正好相反** —— 照抄必错

| | 超 7 天的行为 | 出处 |
|---|---|---|
| 热力图 | **400 硬失败** | `credential_monitor_heatmap.go:136-139` |
| 健康时间线 | **静默 clamp 到 7d** | `node_health.go:64-66` |

⇒ 健康时间线若照抄热力图的「超了就报错」预期，会以为后端在拦；实际上
`since=30d` 不会报错，只会**悄悄少给 23 天**。界面若无其事地显示「30 天」，
用户会以为看全了 —— 这正是 §11.20/§11.22 反复修的「静默降级被当成全量」。
⇒ `buildSince` 在前端夹住，UI 只给 ≤7 天的选项，并显式说明。

#### 热力图的四个 400 约束，四个都得在**发出前**处理

1. `time_start` / `time_end` **必填**（:113-116）—— 缺一个是 400，不是「用默认窗口」。
2. 窗口 > 7d ⇒ 400。
3. `granularity` ∈ `{1m,5m,15m,1h,1d}`（:669-684）。
4. ★ **桶数 > 5000 ⇒ 400**（:153-157）。这一条最容易漏：
   7d × 1m = **10080 桶** ⇒「选 7 天 + 1 分钟粒度」是**一个必然失败的组合**。
   ⇒ 粒度选项按**当前窗口的桶数**过滤（`allowedGranularities`），
   而不是按窗口长短短给固定粒度。切窗口时若当前粒度变非法，自动落到最细合法值。

判据把桶数上限做成**边界**而不是抄一个数：83h × 1m = 4980 ✓ / 84h × 1m = 5040 ✗。

#### 三个「会静默说谎」的地方

- **`exclude_self_test` 缺省是 `true`**（:106-111）。后端特意改过——此前
  `queryBool` 缺省返回 false，裸 API 调用方会把自检流量算进服务质量。
  ⇒ 移动端不传这个参数跟默认走；只有用户显式要看自检才发 `false`。
- **`bucketRate` 在 `total_requests === 0` 时返回 `null` 而非 0**。
  「没有样本」与「成功率 0%」是两件事：后者意味着**全部失败**。
  直接 `success/total` 得 NaN，用 `|| 0` 兜底则把两者混为一谈。
- **`nodeStateTone` 对词表外的状态一律 muted**，默认给 success 会把
  「看不懂的状态」显示成健康。词表：`healthy_confirmed | broken_confirmed |
  suspicious | probing | unknown | unprobed`。

#### 响应里的 `credential_id` 是**字符串**

`node_health.go:171` 是 `strconv.FormatInt(credID, 10)` 再序列化 ⇒ JSON 里是 `"7"`。
按 number 读会得到 `undefined`；`credentialIdOf` 把「空 / 非数字 / 负数」都归成
**null 而不是 0** —— 0 拼进 URL 会得到 400 `invalid credential_id`。

#### ★ 顺带补上一个门的真实缺口：i18n 门漏检重复键

我给 `nav.heatmap` 插了两次，**i18n 门全程绿** —— `parseDict` 用 `Map.set`，
后者被静默覆盖；键集一致、值也正常，两项判据都察觉不到。
只有 `vue-tsc` 的 TS1117 抓到。而 build 前置里 **i18n 门跑在 vue-tsc 之前**，
既然它跑得更早，就应该由它先报。

⇒ 新增第 3 条判据：**同一段内重复键**（带行号），并按**完整键路径**区分，
   所以 `nav.title` 与 `logs.title` 不算重复（反向锁定，防误报刷屏）。
   自测 **9/9 → 14/14**（新增 4 条 + 1 条反向锁定）。
   变异实测：往真实 zh-CN.ts 注入重复键 ⇒ 报 `zh-CN nav.heatmap（第 24 行与第 25 行）`，退出 1。

#### 本轮自己犯的两个错（都被判据逮住）

1. **测试里算错桶数**：我写「7d 下 5m 是 2016 桶 > 5000」—— 2016 < 5000，
   5m 其实是**可用**的，最细可用粒度是 5m 不是 15m。判据报错才照出来。
2. **`buildSince(-5)` 返回 `'1h'` 而不是默认 `'1d'`**：`Math.max(1, …)` 把
   非法输入夹成 1 小时。界面标签写「1 天」而实际只取 1 小时 ——
   标签与实取不一致，正是排障最不能有的那种错。已改为非法输入回落到默认 24h。

### 11.36 本轮门禁（第十四轮，凭据监控线）

`web-mobile/` 实测**十道**：
1. css 门自测 **11/11**
2. 触控门自测 **11/11**
3. i18n 门自测 **14/14**（本轮 +5：重复键 4 + 反向锁定 1）
4. `gate:selftest` **36 条断言全绿**
5. `vue-tsc -b` 通过
6. i18n 键集门 通过（各 **528 键**，含**无重复键**）
7. css 媒体查询门 通过（40 文件）
8. 触控热区门 通过（**37 个 .vue**）
9. `vitest run` **314 用例 / 36 文件全绿**，**连跑 10 次全绿**（新增 24 条）
10. `npm run build` 通过，2 个新视图各自产出独立 chunk

★ 变异证据：往真实词典注入重复键 ⇒ i18n 门转红并报出行号。

### 11.37 探测面上移：探测队列 + 供应商探测延时（第十五轮）

探测面共 11 个端点（`/api/admin/probe/*`）。本轮**只取其中 2 个**，理由：
`system-health` 返回的是 `unified` / `legacy` **双轨混合结构**（`legacy_health`
marshal 成 map 后再塞 `unified`），`queue-snapshot` 同样是
`{unified, queues, total, legacy:{…}}` —— 这类双轨结构一旦在移动端只实现一半，
就会稳定地产出一批「取不到值但不报错」的字段。先把形状规整的取走。

| 页面 | 端点 | 形状 |
|---|---|---|
| 探测 `/probe` | `GET /api/admin/probe/queue-tasks` | `{tasks, total}` |
| 探测 `/probe` | `GET /api/admin/probe/provider-latency` | `{entries, total}` |

两条都 `adminWrap` = AdminMiddleware ⇒ tenant_admin 可用，不设 `requiresRole`。

它补的是 NodesView 的**结论**页缺的那一半：NodesView 展示 `node_probe_state`
的判定（健康/故障/可疑），本页展示**得出该判定的过程**——排到第几次、
下次何时重试、上次结果、供应商直连一次要多久。
「这一条为什么被判成可疑」经常只能从这里看出来：结论页不告诉你它是第 3 次
重试还是第 1 次就成功了。

#### ★ 越界语义：本仓库已经攒到**第四种**了

`queue-tasks` 的 `limit` 越界是**静默保持默认 100**（`if n > 0 && n <= 200` 不成立
时就不赋值）—— 不是 400、也不是 clamp 到边界。加上前几轮的：

| 语义 | 端点 |
|---|---|
| **400** | `routing/overrides/audit`、`credentials/heatmap` |
| **静默 clamp 到边界** | `audit_operations`、`credentials/routing-log` |
| **静默回落默认值** | `turns/sessions`（20）、`probe/queue-tasks`（100） |
| **静默 clamp 到上限** | `admin/node-health/.../timeline`（7d） |

⇒ **每接一个新端点都要重读那三行条件，不能沿用上一个的记忆。**
这一条在 api 模块头注释里逐个记了「越界行为」行。

#### 两个「没出现 ≠ 不存在」

1. **`provider-latency` 不接受任何参数**，且写死三个口径：
   `direct_ok = TRUE AND direct_latency_ms > 0`、`now() - 1 hour`、`LIMIT 500`。
   ⇒ 某供应商不在列表里 = **最近一小时没有成功的直连探测**，不是供应商没了。
   UI 必须在空态旁说明这一句（判据：口径说明**无条件**渲染，
   不是「出错才提示」——它防的是「看空列表的人以为供应商没了」）。

   ★ 客户端把它写成**无参函数**：`fetchProviderLatency()` 不接受任何参数，
   让「想传参」的冲动在类型层面就被挡掉，而不是发一个被后端忽略、
   让用户以为过滤生效了的查询串。

2. **`taskStatusTone` 对词表外的状态一律 muted，不给 success**。
   这个结构体后端**没有**定义状态词表（对比 heatmap 的 `node_status` 是有的），
   所以值可能是没见过的。
   ★ 且刻意**不用** `Record<string, tone>` 查表 —— 查表天然是
   `TONE[status] ?? 'success'`（缺省成功），而这里要的缺省是 muted。
   写成 switch 后「缺省是什么」是显式的一行，不靠默认值隐含。

#### 两个端点**分别**记错误

桌面端这里是 `Promise.all` + `.catch(() => null)` **静默吞掉**（与
`fetchDispatchWaterfall` 旁的写法同款）。移动端不照抄：合并成一个 error 会把
「队列段挂了」显示成「整页都挂了」，而 `provider-latency` 失败并不影响队列。
判据两条：`仅 queue-tasks 失败 ⇒ 延时段照常渲染`、`两个都失败 ⇒ 显示整页错误`。

### 11.38 本轮门禁（第十五轮，探测面）

`web-mobile/` 实测**十道**：
1. css 门自测 **11/11**
2. 触控门自测 **11/11**
3. i18n 门自测 **14/14**
4. `gate:selftest` **36 条断言全绿**
5. `vue-tsc -b` 通过
6. i18n 键集门 通过（各 **542 键**，含无重复键）
7. css 媒体查询门 通过（41 文件）
8. 触控热区门 通过（**38 个 .vue**）
9. `vitest run` **332 用例 / 37 文件全绿**，**连跑 10 次全绿**（新增 18 条）
10. `npm run build` 通过，`/probe` 产出独立 chunk

★ 变异证据：把两个端点合并成一个 error + 把口径说明改成「出错才渲染」
⇒ 4 条视图用例转红（合并错误那条正好命中「仅一段失败」的反向锁定）。

### 11.39 凭据**写入面**上移：模型绑定开关 + 凭据调参（第十六轮）

移动端此前只有 5 个写操作（凭据探测提交 / 手动停用 / 手动恢复 / 强恢复 /
密钥启停）。本轮补齐「改配置」类，让凭据运维闭环完整。

| 端点 | 作用 | 门禁 |
|---|---|---|
| `POST /api/credentials/model-toggle` | 单个 (凭据, 模型) 绑定上/下线 | `h.admin` |
| `POST /api/credentials/promote` | 手动提升为 `ready`，清 `recover_at` | `h.admin` |
| `POST /api/credentials/demote` | 手动降级为 cooling + 定时自愈 | `h.admin` |
| `POST /api/credentials/set-concurrency-auto` | 设自动并发上限 | `h.admin` |

入口挂在凭据热力图的每张卡上（`CredentialOpsSheet`，抽成独立组件，
因为 heatmap / nodes / node-health 三个入口都可能要用它）。

#### ★★ 本组最反直觉的一处：reason 的强制性**在各端点之间不一致**

| 端点 | reason |
|---|---|
| `model-toggle` | **必填**且 ≤500 字，空串 400（`validateModelToggleRequest`） |
| `promote` | **不校验** |
| `demote` | **不校验** |
| `set-concurrency-auto` | **不校验** |

四个都写 `auditLog`。所以「后端不要求」并不等于「可以不填」：
空 reason 会让审计日志失去意义，且 `promote` 那条还会把
`state_reason_detail = "manual_promote: " + reason` 落成悬空的 `"manual_promote: "`。

⇒ **移动端一律强制 reason**，理由不是「对齐后端」而是：这几个操作会改变
生产路由面，事后没人能说清「谁在什么时候因为什么把它降了级」。
与 §11.1 对凭据操作区定的规矩同源。

另一处不对称：`demote` 的 `recover_after_hours` 传 0 会被**默默改成 2**
（不是报错）⇒ 前端显式发值，界面上写「几小时后恢复」就必须真的发几小时。

#### ★ 「上线」不是随手可点的按钮

`model-toggle` 的 `action=online` 在当前 reason 不是**恰好** `manual_offline` 时
返回 409（后端原话：*only manual_offline can be toggled back to online*）。
像 `model_probe_broken` 这类由**探测共识**持有的状态，操作员点了必然 409。

⇒ UI 按 `unavailable_reason` 决定是否渲染「上线」，其余情况给一句说明。
判据反向锁定：夹具里给三个模型（在线 / `manual_offline` / `model_probe_broken`），
断言全文**只有一处**可点的「上线」。

`raw_model_name` **原样透传不规范化** —— 规范化后命中不了，是 404
`binding not found`；而 404 与「确实没绑这个模型」在移动端无法区分，
所以错误文案必须写成「可能模型名已被规范化」而不是「凭据不存在」。

#### 失败必须可见且区分档位

403 = 权限档位、404 = 模型没绑上、409 = 被别处改动。三者各给一句人话，
不混成一句「操作失败」—— 否则用户会去查网络而问题在权限/并发。
（原 `KeysView.doDisable` 无 catch 导致失败完全静默，是本专题修过的真实缺陷。）

### 11.40 本轮门禁（第十六轮，凭据写入面）

`web-mobile/` 实测**十道**：
1. css 门自测 **11/11**
2. 触控门自测 **11/11**
3. i18n 门自测 **14/14**
4. `gate:selftest` **36 条断言全绿**
5. `vue-tsc -b` 通过
6. i18n 键集门 通过（各 **567 键**，含无重复键）
7. css 媒体查询门 通过（42 文件）
8. 触控热区门 通过（**39 个 .vue**）
9. `vitest run` **353 用例 / 38 文件全绿**，**连跑 10 次全绿**（新增 21 条）
10. `npm run build` 通过

★ 变异证据（两处分别验，叠加时会因类型标注触发解析错，故分开跑）：
- 去掉 `validateReason` 调用 ⇒ 2 条转红（空 reason 不发请求）
- 把「上线」按钮的 `canToggleOnline` 换成 `true` ⇒ 1 条转红

★ 本轮踩到的两个**与真实原因无关的报错**（都值得记）：
- `AppSheet` 是 teleport 的，`w.findAll('button')` / `w.text()` 拿到空
  ⇒ 报「按钮不存在：下线」/ `expected '' to contain ...`。
  同款坑 KeysView.spec 已踩过一次（见 §11.29(3)），本次补了注释说明。
- `vi.mock` 的工厂 `})` 后紧跟 `return` 缺分号，ASI 拼成 `})(return …)`。

★ 还纠正了一个**我凭推断写下的错误注释**：曾写「model-toggle 找不到绑定会
返回 200 当无操作」，回源码核对是 **404 `binding not found`**（`pgx.ErrNoRows`
分支）。⇒ 注释里凡是「我以为后端会怎样」的断言，都要回源码核。

### 11.41 路由覆盖规则上移：规则本体（superAdmin 档，第十七轮）

与 §11.33 的 `/routing-audit` 配成一对：
**本页答「现在生效的规则是什么」，审计页答「这些规则是谁在什么时候改的」。**
只看审计不知道当前状态；只看规则不知道是不是刚被人动过。

四个端点全在 `RegisterAutoRouteRoutes(mux, **h.superAdmin**)` 下
（handler.go:1381）⇒ tenant_admin 必 403，导航已按 `requiresRole` 挡住。
**这是第三条超管线**（`/integrity`、`/routing-audit`、`/overrides`）。

| 方法 | 路径 | 作用 |
|---|---|---|
| GET | `/api/admin/routing/overrides` | 列出规则（`active` / `task_type` / `profile` 过滤） |
| POST | 同上 | 新建 → **201** |
| DELETE | `/{id}` | 停用（**软删**） |
| PATCH | `/{id}/extend` | 延长有效期 |

#### ★★ 三个后端语义直接决定了 UI 怎么做

**(1) DELETE 是软删** —— SQL 是 `SET expires_at = NOW() - INTERVAL '1 second'`，
行**仍留在表里**。⇒ 停用后必须切到 `active=true` 刷新，否则刚删的规则还在列表里，
用户会以为删除失败而重复操作。

**(2) 创建后约 1 分钟才生效** —— 后端 201 的 message 明说 OverrideStore
在下一个 1-min reload 生效。⇒ 成功文案必须带这个延迟，否则用户会立刻去查
路由解析、看不到新规则 ⇒ 重复提交 ⇒ 而重复提交必然撞 **409**（同一
`(task_type, profile, model_chosen, mode)` 已存在），**越急越错**。

**(3) `profile` / `mode` 后端无枚举校验** —— `control/routing/create.go:167-185`
只校验 `task_type` 与 `reason`，`profile`/`mode`/`model_chosen` 是自由文本，
也没有「合法值列表」端点 ⇒ **候选值只能从现有规则里取**（`knownProfiles` /
`knownModes`）。自由输入仍允许（后端接受），但 UI 优先给候选。

#### 两处「回显类型」坑

- `filter` 回显的三个值**全是字符串**（`map[string]string`），
  包括 `active` —— 后端是 `strconv.FormatBool`。当 boolean 用会得到 `undefined`。
- `active` 参数必须发字符串 `"true"`（后端判 `== "true"`）。
  发 `active=false` **不是「只看过期的」，而是根本不过滤** ——
  与 `active=1` / `active=yes` 等价。客户端索性**不发**这个参数。

#### `model_chosen` 留空必须发 `null` 而不是空串

`""` 会解成「指向空串的非 nil 指针」，即**指定了一个名字为空的模型**，
规则永远不会匹配。`JSON.stringify` 会把 `undefined` 键直接丢掉
（Go 解出来同样是 nil），所以真正要锁的是「**绝不能是空串**」——
判据用「显式传空串会原样发出去」作反向锁定，证明前一条不是碰巧。

### 11.42 本轮门禁（第十七轮，路由覆盖规则）

`web-mobile/` 实测**十道**：
1. css 门自测 **11/11**
2. 触控门自测 **11/11**
3. i18n 门自测 **14/14**
4. `gate:selftest` **36 条断言全绿**
5. `vue-tsc -b` 通过
6. i18n 键集门 通过（各 **596 键**）
7. css 媒体查询门 通过（43 文件）
8. 触控热区门 通过（**40 个 .vue**）
9. `vitest run` **380 用例 / 40 文件全绿**，**连跑 10 次全绿**（新增 27 条）
10. `npm run build` 通过

★ 变异证据（4 处，**其中 1 处第一版没抓到，判据本身是恒真的**）：
- 停用后不切 `active=true` ⇒ 转红（**需先让判据的前置是「筛选关着」**）
- 成功文案去掉「1 分钟生效」⇒ 转红
- `model_chosen` 空串照发 ⇒ 转红

★ 又一次**判据恒真**，形态与 §11.33 相同：
   我写「停用后下一次拉取带 active=true」，而 `activeOnly` 初值本来就是 true
   ⇒ 删掉那行重置，变异**照样全绿**。真实场景是用户先**关掉**筛选看全部，
   此时停用才必须切回。⇒ 判据前置改成「筛选处于关闭态」后才转红。
   ★ 规律：**判据的前置状态必须显式构造**，否则它量的是默认值而不是被测行为。

★ 另有一个与真实原因无关的失败：顶栏的「新建规则」按钮与面板提交钮**同名**，
  按文案点击会点到顶栏那个（只是把面板又打开一次），现象是
  「表单填了但请求没发出去」。⇒ 改按 `.ov__submit` 类选择，并写进注释。

---

### 11.43 自动调优面上移：请求漏斗 + 调优建议（第十八轮，superAdmin 档）

本轮上移两个页面，补齐 auto-route 线的第三段。三页凑成一个闭环：

| 页面 | 端点 | 回答什么 |
|---|---|---|
| `/overrides`（上轮） | `GET/POST/DELETE/PATCH /api/admin/routing/overrides` | 现在生效的**规则**是什么 |
| `/funnel`（本轮） | `GET /api/admin/auto-route/analytics/funnel` | 请求进来后**被筛掉多少**、这数**可不可信** |
| `/proposals`（本轮） | `GET /api/admin/auto-route/tuning/proposals` | 系统认为**规则该怎么调**、哪些已落地 |

只看规则不知道规则对不对；只看漏斗不知道该怎么改；只看建议不知道哪些已生效。

**鉴权**：两条都是 superAdmin。
- funnel 走 `analytics.go:58` 的 `adminWrap`，而 `RegisterAnalyticsRoutes`
  在 `admin/handler.go:1430` 是用 `h.superAdmin` 调的；
- proposals 走 `admin/auto_route.go:116` 的 `adminWrap`，而
  `RegisterAutoRouteRoutes` 本身在 `handler.go:1381` 就是 `h.superAdmin`。

⇒ 抽屉导航两条都 `requiresRole: 'super_admin'`。这是**第四条超管线**
（前三条：`/integrity`、`/routing-audit`、`/overrides`）。

#### ★★ `meta.blocked` 的 0 有两种完全不同的含义

`analytics.go` 的三档口径：

| `data_source` | 触发条件 | blocked 可靠吗 |
|---|---|---|
| `exact` | `routing_decision_log` 的 `decision_trace` 聚合 | ✅ 真聚合过 `SUM(blocked_candidates)` |
| `approximate` | RDL 一行都没有 | ❌ **没算** |
| `mixed` | RDL 有行但 trace 全空 | ❌ **没算** |

原因在 `analytics.go` 的分支里：`approximate` / `mixed` 走的是
`routing_analytics_source` 补数，那条 SQL 的 SELECT 列表里
**根本没有 blocked 这一列** ⇒ `fr.totalBlocked` 保持零值。

⇒ 所以「近似 + blocked=0」= **没算过**，不是「一个都没被拦」。
渲染成 `0` 就是在编造一个我们并不知道的结论。
判据用 `blockedIsInconclusive(meta)`，返回 `true` 时 UI 显示「未统计」。

★ 这与 §11.20 修的「降级被显示成真的一分钱没花」、§11.22 的
   `observation_degraded` 是**同一族错误**的第三个面。

#### ★ `approximate` 是权威位，不能自己猜

判据依据是后端给的 `approximate` 位（= `dataSource != "exact"`），
**不是**拿 `sample_n` 自行判断。因为后端的 `confidence` 规则是：

```
exact && requests >= 30 && trace_ratio >= 0.8  ⇒ high
exact && requests >= 10                        ⇒ medium
mixed                                          ⇒ medium
其余                                           ⇒ low
```

样本量小会被降级；而 `data_source != exact` 一律 `approximate=true`，
即使 `sample_n` 很大。只看样本量会判反。

#### ★ funnel 有 2 分钟服务端缓存，必须自曝

`admin/funnel_cache.go:21-24`：`ttl: 2 * time.Minute`，key = `scope|model|window`。

用户刚在 `/overrides` 改完规则就过来刷新，会看到**没变化的旧数**。
⇒ 页首必须挂「服务端缓存 2 分钟」提示，否则用户会认定「我刚写的规则没生效」
并去重复提交 —— 而重复提交必然撞 409，**越急越错**。
（与 §11.41 的「创建后约 1 分钟生效」是同一个坑的两个面。）

#### ★ 「查不到」与「没流量」在响应里完全同形

模型名拼错时后端**不 404**，返回 `200 + requests=0`。
所以空态必须挂在 `requests === 0` 上，**不能**挂在 `data === null` 上：

```vue
<!-- ✗ 第一版就是这么写的，被自己的判据当场抓出来 -->
<p v-if="!loading && searched && !data && !error">{{ t('funnel.empty') }}</p>
```

挂 `data === null` 时，拼错模型名会得到一张
「请求数 0 / 已选凭据 0 / trace 覆盖率 0%」的置信度面板 ——
**把「没查到」显示成「统计结果就是零」**。

★ 同时拆出**第三个**互斥态：`requests > 0` 但 `stages` 为空 = **形状不符**
（该去查后端），不能复用「没流量」文案。三者文案必须都不同：

| 判据 | 文案 |
|---|---|
| `requests === 0` | 这个模型在所选窗口内没有请求记录 |
| `requests > 0 && stages.length === 0` | 响应形状不符：有请求数但没有任何阶段 |
| `data === null`（出错） | 报错 |

且 `requests === 0` 时**不渲染阶段表** —— 后端此时仍会吐 3 个 `value=0` 的阶段，
渲染出来是三行 0，看着像「量过了，结果是零」。

#### 第五种越界语义：400 for unknown enum

`auto_route_tuning.go:188/193` 对 `status` / `category` 做 allowlist 校验
（`:50-51` 定义词表），非法值 **400**。空串 = 不过滤，是合法值。

★ **后端对这两个参数既不 TrimSpace 也不 ToLower** —— 对比
  `analytics.go:62` 的 `parseAnalyticsWindow` 用了
  `strings.ToLower(strings.TrimSpace(raw))`。
  ⇒ 用户手输 `Pending ` 或 `PENDING` 必然 400。

⇒ 筛选器只能是**固定 chip**，绝不能是自由输入框。
判据里有一条专门量「页面上没有自由文本输入」。

`limit` 越界 400（`limit must be 1-500`，`:200`），**后端默认 50**（`:197`）。

#### ★ `proposal` / `evidence` 是 `json.RawMessage`，没有任何 schema

三种类别（`keyword_add` / `weight_adjust` / `threshold_change`）各有自己的结构。
写死 `p.model` 访问在 `keyword_add` 上就是 `undefined`，
渲染出去是「该建议没有模型」—— 一个我们并不知道的结论
（真相是「这类建议本来就不带模型」）。

⇒ `pickFields(obj, keys)`：只在键存在且非空串/null 时取出，取不到**整段不出现**。
判据用两份结构完全不同的样本互证，并加一条「`proposal` 为空对象 ⇒ 不渲染任何 kv 行」。

#### 状态词配色必须封闭

词表外一律 muted —— 给一个看不懂的状态打 `success`，
等于把「不知道」显示成「已生效」。同 `probeTasks` 的纪律。

★ 判据里有一条**反向锁定**：`StatusDot` 上不许出现 `status-dot--success`。
同时另有一条「已生效确实是绿的」证明它不是恒真。

#### 本轮不做的部分（明确划在范围外）

- **审批动作**（`POST proposals/:id/{approve,reject}`）未上移。
  它写 `tuning_proposals` 状态并落审核人，属于**有副作用的人工决策**，
  移动端上审批会绕过桌面端的复核环节。这不是「做不了」，是明确不做。
- `analytics/matrix` / `analytics/flow` / `analytics/model-task-index` 未上移。
- `system-health` 与 `queue-snapshot` 仍是 `unified`/`legacy` **双轨混合结构**，
  移动端只实现一半会稳定产出「取不到值但不报错」的字段 ⇒ 9 个 probe 端点未动。

### 11.44 本轮门禁（第十八轮，自动调优面）

`web-mobile/` 实测**十道**：

| # | 门 | 结果 |
|---|---|---|
| 1 | css 门自测 | **11/11** |
| 2 | 触控门自测 | **11/11** |
| 3 | i18n 门自测 | **14/14** |
| 4 | `gate:selftest` | **36 条断言全绿** |
| 5 | `vue-tsc -b` | 通过 |
| 6 | i18n 键集门 | 通过（各 **641 键**，+45） |
| 7 | css 媒体查询门 | 通过（**45 文件**，+2） |
| 8 | 触控热区门 | 通过（**42 个 .vue**，+2） |
| 9 | `vitest run` | **442 用例 / 45 文件全绿**，**连跑 10 次全绿**（新增 62 条） |
| 10 | `npm run build` | 通过 |

★ 新增 i18n 键里 `window24h` / `window7d` **故意不用点路径** `window.24h`：
  i18n 门按 `/^(\s*)([A-Za-z_$][\w$]*):\s*'(...)'/` 抽叶子键，
  `24h` 抽不出来 ⇒ 键集门会**静默少认一个键**，两侧同时漏、门照样全绿。
  ⇒ 与 §11.20 的「键被插错词典段」是同一族：**键集门的判据只认它能解析的形状**。

★ 变异证据（**5 处，全部转红**）：

| 变异 | 结果 |
|---|---|
| `blockedIsInconclusive` 漏判 `data_source` | 1 failed |
| `blockedUnknown` 恒假（永远显示数字） | 2 failed |
| 空态挂回 `data === null`（第一版的错法） | 2 failed |
| `statusKeyOf` 词表外不设防 | 1 failed |
| `proposalFieldsOf` 无条件渲染键 | 2 failed |

★ **本轮被抓到的一个真缺陷**（不是判据的问题，是代码的问题）：
  空态原写作 `v-if="... && !data"`，而 `requests=0` 时后端**返回 200 + 合法 body**
  ⇒ `data` 非 null ⇒ 空态永远不命中 ⇒ 用户看到一张
  「请求数 0 / 已选凭据 0 / trace 覆盖率 0%」的置信度面板。
  把「**没查到**」显示成「**统计结果就是零**」。
  ⇒ 已改为挂在 `requests === 0` 上，并拆出第三态「形状不符」。

★ 又一次**判据量的不是它声称要量的那件事**：
  「词表外状态不得显示成已生效」原写作 `expect(w.text()).not.toContain('已生效')`，
  而整页文本里必然含**筛选 chip**「已生效」（它是筛选项，不是这一条的状态）
  ⇒ 判据恒红，与被测行为无关。已收窄到 `w.find('.tp__item-status').text()`
  并补一条「`status-dot--success` 不存在」的反向锁定 +
  一条「已生效确实是绿的」证明它不是恒真。
  ★ 规律：**断言页面全文前先问「页面上还有哪些别的东西也会命中这段文字」**。

★ 清理死变量：判据从 `data === null` 换成 `requests === 0` 后，
  `searched` ref 不再参与任何渲染 ⇒ 删掉，不留「看起来在用」的残骸。

---

### 11.45 全局横向对比面上移：路由矩阵 + 流量分布（第十九轮，superAdmin 档）

auto-route 分析面的最后一块。两个页面把「单点纵深」补成「全局鸟瞰」：

| 页面 | 端点 | 视角 |
|---|---|---|
| `/funnel`（上轮） | `analytics/funnel` | **单个模型**纵深：这个模型被筛掉多少 |
| `/matrix`（本轮） | `analytics/matrix` | **全体模型**横向对比：谁擅长什么 |
| `/flow`（本轮） | `analytics/flow` | **链路去向**：请求落到哪个供应商 |

**鉴权**：两条都在 `RegisterAnalyticsRoutes` 里（`analytics.go:55-58`），
而该注册由 `handler.go:1430` 用 `h.superAdmin` 挂载 ⇒ 导航
`requiresRole: 'super_admin'`。这是**第五条超管线**。

#### ★★ 矩阵单元的 0 有两种完全不同的含义，且后端给不出判据

`handleMatrix` 的 `cellMap` 只装「DB 里真有行」的 `(模型,任务)` 对；
渲染成矩形时缺的格子由 **Go 侧零值**填充（`cells[i][j]` 初始即 0）。

⇒ 0 至少可能意味着：
- 该模型 × 该任务组合**根本没有记录**（Go 填的）
- **真的**是 0（`success_rate` 0% = 全部失败；`cost_usd` 0 = 没花钱）

两者运营含义**完全相反**：前者不用管，后者是故障。

**唯一能证明「这格有数据」的办法是切到 `count` 指标** ——
`COUNT(*)` 对任何产出组 ≥ 1，所以 `count = 0 ⟺ 无记录`。
这个推理是可证的，所以 `cellMayBeAbsent()` 只对 `count` 返回 `false`。

UI 做法（`zeroIsUncertain`）：
- `count` 下 0 → `mx__cell--empty`（弱化 + 铺底色，「确定没量到」）
- 其它指标下 0 → `mx__cell--faint`（更弱、**不铺底色**）
  —— 不铺底色是刻意的：铺了就像「这一格确确实实量到了 0」
- 两种 class 下都挂常驻说明，且说明**给可执行指引**（「切到请求数可确认」）
- 鼠标 `title` 用**同样的保留措辞** —— 悬停时看到的不能比移动端更笃定

判据：同一份 `cells`，两种指标下 0 的 class **必须不同**（反向锁定，
防止「弱化」被改回一致）。

#### ★★ p95 在 7d 下必是近似值，且两种窗口口径不同

`useMaterializedView`（`analytics_materialized.go:53-58`）**只在**
`windowLabel == "7d"` 且物化视图新鲜时返回 `true`：

| 窗口 | SQL 表达式 | 口径 |
|---|---|---|
| 7d + MV | `SUM(p95_latency_ms * request_count) / SUM(request_count)` | **各小时 p95 的请求数加权平均** |
| 24h（MV 恒不启用） | `percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms)` | 对原始行的**真 p95** |

⇒ 同一个模型，**24h 的 p95 必然 ≥ 7d 的 p95**（平均 of p95s < p95 of all）。

★ **响应里没有任何字段告诉客户端本次走的是哪条路。**
  ⇒ p95 指标下**无条件**标注口径。若只在「7d」时提示，24h 那次会显示成
  一个精确值 —— 而那条虽然是真 p95，也不该和别处的 p95 直接比较。
  判据专门有一条「切到 24h 窗口后提示**仍在**」。

★ 同一份常量 `P95_IS_ALWAYS_APPROXIMATE` **接进视图判据**，
  不是只测不用的探针（采到了不用 = 指标采了白采）。
  将来后端加了能说明路径的字段，把它改成 `false`，提示会自动消失。

#### ★ `__specified__` 是合成键，不是真实任务类型

`analytics.go:36` `const SpecifiedModelTaskKey = "__specified__"`：
显式指定模型、网关没做任务分类的请求，任务类型为空时用这个占位。
直接渲染会让人以为有个叫这名字的真实任务类型 ⇒ 映射成「指定模型」，
`unknown` 映射成「未分类」。

#### ★ 行 = 模型、列 = 任务（2026-06-22 换过轴）

`analytics.go:271-275` 明确写了，但「matrix」这个词会让人默认行是任务。
⇒ 变量名（`rows`/`cols` 的用法）与 UI 文案（表头写「行 = 模型 / 列 = 任务」）
都要钉死这个方向。

#### 第五种越界语义在 analytics 线上的具体形态

`window` / `row` / `metric` 三个参数都是 allowlist，非法值 400，
且 `parseAnalyticsWindow` 只对 `window` 做了 `ToLower`+`TrimSpace`，
`row`/`metric` 是精确匹配。

★ **`as const` 编译后不存在**，`as any` 就能塞进 `metric: 'latency'`。
  ⇒ API 层必须做**运行时** allowlist 守卫，词表外的值不发、退回后端默认。
  判据三条：大写错、词表外、非法 window，全部锁「不发」。

#### `/flow` 为什么不做桑基图

后端给的是三层桑基数据。移动端改成分层列表 + 链路明细，理由写进了文件头：
- 三层桑基在 390px 宽的屏上，节点标签要压到 8px 以下才排得下；
- 带宽编码在小屏上分辨率不足，分不出 3% 和 7%；
- 而「哪个供应商承接了最多流量」是运维真正要答的问题，
  用「按流量排序 + 占比」表达比图形更直接、更好点。

⇒ **列表是主动选择（带宽 → 排序 + 占比数字），不是「做不了图」的降级。**
   写进注释，免得下一个人「优化」回桑基图。

★ `links[].task_type` 必须回显：L2→L3 的链路按 `(任务, 模型)` 聚合，
  不带任务的话「代码生成 → gpt-4o → openai」和「翻译 → gpt-4o → openai」
  会合成一条，看起来一样。

★ 占比分母用**全图总量**，不能用某层的和 —— 用后者会让每层都是 100%。
  分母为 0 显示「未知」而不是 0%。

#### ★★ i18n 门新增第 4 条判据：视图引用的键必须存在

**为什么加**：原有 3 条判据全都只看**两侧词典互相**对齐，于是漏掉整整一类
—— **两侧一致地缺同一个键**。i18n 缺键回退成键名本身（`i18n/index.ts`），
运行时用户看到字面量 `usage.successRate`。

**实况**：这道判据上线后**当场抓出一个一直存在的潜在缺陷** ——
`RoutingCheckView.vue:128` 写的是 `t('usage.successRate')`，
而两侧词典都没有这个键（而且它借用了 `usage.` 命名空间）。
已改到 `routing.successRate` 并补上两侧键。

**实现要点（三条都是踩出来的）**：
1. **扫描范围是整个 `src/`（`.vue` + `.ts`），不是只有 views。**
   初版只扫 views，漏掉了 `src/api/autoRouteMatrix.ts` 里
   `taskLabel()` 写死的 `t('matrix.specified')` —— 那个键被误删时门全程绿。
   ⇒ 「判据的绿只覆盖它量到的那件事」：**扫描范围也是判据的一部分**。
2. 抽键正则的末尾必须有 `.?`。真实代码是 `t('matrix.row.' + r)`，
   那个**尾点**会让「要求 `'` 紧跟标识符」的老正则**整条失配**
   ⇒ 既没进字面量桶、也没进动态桶，**两桶都漏**。
3. 「扫到过文件」这个守卫是错的：词典本身也是 `src/` 下的 `.ts`。
   守卫必须是「扫到过**可能引用 `t()` 键的**源文件」。

**排除 `*.spec.ts` / `*.test.ts`**：那些文件里的 `t()` 键是**被测样本**，
不是产品引用，拿它们当契约会把「故意写错的样本」报成缺陷。

#### ★ 「测不到」单列出来，再补一个测试把它变成「测得到」

门抽不出 `t('prefix.' + x)`，所以把 7 处动态前缀**单列**输出
（「本门未覆盖，由 `src/i18n/dynamicKeys.spec.ts` 覆盖，需人工确认」），
而不是静默跳过 —— 跳过会让「已确认合格」覆盖到没量过的地方。

`src/i18n/dynamicKeys.spec.ts` 把每个前缀与它在代码里**真实的后缀来源**
（`ANALYTICS_WINDOWS` / `MATRIX_ROWS` / `MATRIX_METRICS` /
`TUNING_STATUSES` / `TUNING_CATEGORIES` 这些 allowlist 常量）拼起来，
跑遍**两侧**词典 ⇒ 少一个键立刻红（32 用例）。
后缀从代码常量取而不是手抄 —— 手抄的那份会漂。

★ 有一条「至少覆盖 5 处」的断言：清单少于这个数说明它没跟上代码。

#### 本轮不做的部分

- `analytics/model-task-index` 未上移（`top` 参数是**静默回落 20**，
  与 funnel/proposals 的 400 语义不同，且空表时后端返回 `warning` 字段
  而不是空 items —— 需要单独一套「尚未首刷」的状态）。
- 审批动作仍不上移（见 §11.43）。

### 11.46 本轮门禁（第十九轮，路由矩阵 + 流量分布）

`web-mobile/` 实测**十道**：

| # | 门 | 结果 |
|---|---|---|
| 1 | css 门自测 | **11/11** |
| 2 | 触控门自测 | **11/11** |
| 3 | i18n 门自测 | **23/23**（原 14，+9：第 4 条判据自己的 4 组样本 + 修复连带暴露的 5 条） |
| 4 | `gate:selftest` | **45 条断言全绿** |
| 5 | `vue-tsc -b` | 通过 |
| 6 | i18n 键集门 | 通过（各 **678 键**，+37；**508 个源码字面量键全部存在**） |
| 7 | css 媒体查询门 | 通过（**47 文件**，+2） |
| 8 | 触控热区门 | 通过（**44 个 .vue**，+2） |
| 9 | `vitest run` | **536 用例 / 49 文件全绿**，**连跑 10 次全绿** |
| 10 | `npm run build` | 通过 |

★ 触控门当场抓到本轮新写的两处：
  `.mx__cell min-height: 44px` 与 `.fl__edge min-height: 24px`。
  门给的唯一豁免是 `/* R1-legacy */`（**已知存量，故意不改**）——
  拿它盖住**新写的**代码就是撒谎。
  ⇒ 两处都按 48px 重做，并把 `.fl__item` 的 padding 收紧补偿列表高度。

★ 变异证据（**5 处，全部转红**）：

| 变异 | 结果 |
|---|---|
| 矩阵 0 一律当「无记录」（去掉 `cellMayBeAbsent`） | 4 failed |
| p95 提示改成「只在 7d 显示」 | 1 failed |
| `taskLabel` 不映射 `__specified__` | 3 failed |
| API 层去掉运行时 allowlist 守卫 | 2 failed |
| 边不回显来源任务 | 1 failed |
| 边不按流量排序 | 4 failed |
| 占比分母误用本节点出边数 | 2 failed |

★ **两次「变异没转红」，两次都不是判据无牙**（分属不同成因）：

1. **第 ③ 类（期望值算错）**：`/flow` 的样本 links 原本**就是有序的**，
   于是「去掉排序」是空操作。⇒ 先把样本改成乱序。
2. **第 ② 类（前提失效）**：改乱序后**仍然绿**。真正的成因是
   样本里**每个任务节点只有 1 条出边**，而排序是对**单个节点的出边数组**
   做的 —— 1 元素数组排序恒为空操作。
   ⇒ 必须补「一个任务分流到多个模型」这条真实情形，变异才转红（4 failed）。
   ★ 这两轮合起来说明：**「变异没转红」必须先分类再动手**，
     第 ② 和第 ③ 类都会伪装成「判据无牙」。

★ 又一次**判据量的不是它声称量的东西**：
  `/flow` 两条判据都用 `item.text().includes('gpt-4o')` 找条目，
  而 `task:translation` 那条边渲染出的目标**就写着** `→gpt-4o`
  ⇒ `find()` 命中的是**任务项**而不是模型项，读到 41.7% 而不是 50.0%。
  ⇒ 改成按**自身名字元素**（`.fl__item-name`）定位。
  ★ 规律（与 §11.43 的「整页文本命中筛选 chip」同族）：
    **用 `text().includes(x)` 定位元素时，要问「还有哪些别的元素也会命中 x」**。
    子树文本里包含**后代的标签**，比整页文本更容易误命中。

★ i18n 门第 4 条判据**当场抓出一个一直存在的潜在缺陷**：
  `RoutingCheckView.vue:128` 写 `t('usage.successRate')`，
  两侧词典都没这个键（且借用了 `usage.` 命名空间）⇒ 英文用户看到中文、
  该键在任何一侧都显示裸键。已改到 `routing.successRate` 并补两侧键。
  ⇒ 这类缺陷**前三条判据永远看不见**（它们只比两侧是否一致）。

★ 第 4 条判据本身的三条实现要点，都是踩出来的：
  ① 扫描范围必须覆盖**整个 `src/`（含 `.ts`）** —— 只扫 views 会漏掉
     `src/api/autoRouteMatrix.ts` 里 `taskLabel()` 写死的键名；
  ② 抽键正则末尾必须有 `.?` —— 真实代码是 `t('matrix.row.' + r)`，
     那个**尾点**会让老正则整条失配 ⇒ 字面量桶和动态桶**同时漏**；
  ③ 「扫到过文件」这个守卫是错的（词典本身也是 `.ts`），
     必须是「扫到过**可能引用 `t()` 键的**源文件」。
  排除 `*.spec.ts` / `*.test.ts`：那些文件里的 `t()` 键是**被测样本**，
  拿它们当契约会把「故意写错的样本」报成缺陷。

★ 「测不到」的处理范式（可复用）：
  门抽不出的 7 处动态键**单列**输出，并注明由哪个测试覆盖；
  再用 `src/i18n/dynamicKeys.spec.ts`（32 用例）把前缀与代码里的
  **allowlist 常量**拼起来跑遍两侧词典 ⇒ 「测不到」变成「测得到」。
  后缀从代码常量取而不是手抄 —— 手抄的那份会漂。

---

### 11.47 探测双轨端点上移：系统健康 + 队列快照（第二十轮，admin 档）

这一轮解掉的是**从第十七轮起就主动搁置**的那个缺口：
`system-health` 与 `queue-snapshot` 的 `unified`/`legacy` **双轨混合结构**。
搁置的理由是「移动端只实现一半会稳定产出『取不到值但不报错』的字段」——
现在把两个轨道都实现，并且**只把 unified 当权威**。

**鉴权**：两条都在 `RegisterProbeDashboardRoutes(mux, wrapAdmin)`
（`admin/probe_dashboard.go:1900-1909`，注册点 `cmd/gateway/main.go:7298`）
⇒ **admin 档**，tenant_admin 可用，导航**不设** `requiresRole`
（与 `/probe` 同级，区别于那五条 superAdmin 管线）。

#### ★★★ 陷阱一：`system-health` 的顶层字段**全是 legacy 的**

handler 把 `ProbeSystemHealth` marshal 之后**摊平到顶层**
（`probe_dashboard.go:958-972` 的 `legacyPayload`），再挂
`unified` / `legacy` / `legacy_mode_safe`。于是：

| 顶层（legacy） | `unified`（新） | 是一个东西吗 |
|---|---|---|
| `total_nodes` | `total_credentials` | **不是** |
| `ready_probes` | `queue_ready` | **不是** |
| `current_probing` | `queue_in_flight` | **不是** |

写 `resp.total_nodes` 读出来的是 `model_probe_state` 的遗留数字。
⇒ **本模块的类型里刻意不声明任何顶层具名字段**，只暴露
  `unified` / `legacy` / `legacy_mode_safe` 三个显式键
  （保留 `[k: string]: unknown` 索引签名只用于判别形状）。
  「读错源」这件事在**类型层面**就做不到。

#### ★★★ 陷阱二：`queue-snapshot` 顶层 `queues` / `total` **也是 legacy 的**

handler 先查 `v_probe_queue_snapshot`（= `model_probe_state`）得到 `queues`，
再把它**同时**放进顶层和 `legacy` 键下（`:775-830`）。

★ 而 handler 自己的注释写着（`:764-767`）：

> the 572-row historical backlog (AGENT C handoff 2026-08-18)
> lives in the legacy view and is unaffected by the active queue

⇒ 拿 `resp.total` 当「当前探测队列积压」会显示 **572**，
而实际活动队列可能只有 **25**（12+3+6+4）。**差 23 倍。**
这是本轮存在的最大理由。

⇒ 页面的「活动积压」只由 `activeBacklogOf(unified)` 算；
  legacy 明细单独放在**默认收起、明确标注**的区块里。

#### ★★ 陷阱三：两个端点的 legacy 查询**软/硬失败不对称**

| 端点 | legacy 查询失败时 | 代码位置 |
|---|---|---|
| `queue-snapshot` | **直接 500** | `:780-786` |
| `system-health` | 只 `slog.Warn`，`legacyHealth` 保持**零值**，响应仍 200 | `:888-895` |

★ 而且 `handleProbeSystemHealth` 的注释写着
  「Surface the error in the response so the dashboard can warn the operator」
  —— **代码没有这么做**（`grep legacyErr` 只有 `slog.Warn` 一处使用）。

⇒ 所以 `legacy.total_nodes === 0` 有两种含义：
  「真的一个节点都没有」/「这份历史视图没加载上来」。
  **客户端无法区分，也不能替它编。**
  ⇒ `legacySectionMayBeUnloaded()` 判 true 时页面显示
  「可能未加载，无法判断」，**不显示那四个 0**。
  这不是精确判据（全 0 且加载成功也会命中），
  方向刻意偏向「宁可说可能没加载，也不要把未知说成零」。

#### ★ `legacy_mode_safe: false` 是后端给的显式警告

两处都有（`TotalLegacySystemHealth.LegacyModeSafe` / `LegacyQueueBlock.legacy_mode_safe`），
后端注释写明「Toggle to true only after the legacy table is fully drained
(planned for migration 536+)」。
⇒ 必须透出，并据此给 legacy 区块打「已不权威」badge +
  一条说明「它是历史遗留统计，不代表当前状况」。
**绝不**把 legacy 数字和 unified 数字并排放进同一个统计条。

#### ★★ 两个 `unified` 同名，但**字段集不同**

这是本轮第二个大坑，**不是我看出来的，是 `vue-tsc` 抓的**（TS2339）：

| 字段 | 在哪个 unified 上 |
|---|---|
| `node_unclaimable` / `stale_leases` | **只**在 `queue-snapshot` 的 `UnifiedProbeQueueStats`（`:202-231`） |
| `queue_expired` | **只**在 `system-health` 的 `UnifiedProbeSystemHealth`（`:250-257`） |

我第一次写 `leaseAnomaliesOf(unified.queue_expired)` 时编译器报
「Property 'queue_expired' does not exist」—— 它逼我回去读 Go 结构体，
才发现两个都叫 `unified` 的对象根本不是一套字段。

⇒ `leaseAnomaliesOf()` 收**两个**源，并返回 `sourcesAvailable`
（读到几个源），让视图能区分「确实全是 0」与「一个源都没读到」。

#### ★ `success_rate_last_1h` 三态不可二元化

Go 侧是 `SuccessRateLast1h *float64 \`json:"success_rate_last_1h,omitempty"\``：
nil ⇒ **JSON 里根本没有这个键**。
⇒ 缺失 = 近 1h **没有运行记录**，不是 0% 成功率；
真正的 `0` = 跑了且**全部失败**。写成 `?? 0` 就会说「这一小时全挂了」。

#### 租约异常三项分开返回

`node_unclaimable`（租约过期没 worker 认领 ⇒ 队列会卡）、
`stale_leases`（心跳超时 ⇒ worker 可能死了）、
`queue_expired`（近 2h 过期）——
三者的**处置动作完全不同**，求和会丢掉处置信息。

#### 其它已核实的口径

- `queue_completed` / `queue_failed` / `queue_expired` 是**近 2 小时**窗口，
  不是全量（Go 结构体注释写明）⇒ 页面挂常驻说明。
- `pseudo_success_count`：探测记录 `direct_ok=true` 但凭据的 URSM tenant key
  已消失（handoff §6 P0）⇒ 非 0 时单独告警。
- **两个端点分别记错误**（`Promise.allSettled` + 逐段拼错误）：
  合并成一个 error 会把「一段挂了」显示成「整页都挂了」（同 §11.30 纪律）。

### 11.48 本轮门禁（第二十轮，探测双轨）

| # | 门 | 结果 |
|---|---|---|
| 1 | css 门自测 | **11/11** |
| 2 | 触控门自测 | **11/11** |
| 3 | i18n 门自测 | **23/23** |
| 4 | `gate:selftest` | **45 条断言全绿** |
| 5 | `vue-tsc -b` | 通过 |
| 6 | i18n 键集门 | 通过（各 **719 键**，+41；**548 个源码字面量键全部存在**） |
| 7 | css 媒体查询门 | 通过（**48 文件**，+1） |
| 8 | 触控热区门 | 通过（**45 个 .vue**，+1） |
| 9 | `vitest run` | **583 用例 / 51 文件全绿**，**连跑 10 次全绿**（见下） |
| 10 | `npm run build` | 通过 |

★ 变异证据（**6 处，全部转红**）：

| 变异 | 结果 |
|---|---|
| 活动积压改用 legacy `total` | 2 failed |
| `success_rate_last_1h` 缺失当 0% | 3 failed |
| legacy 全 0 判为已加载（API 层） | 1 failed |
| 租约异常判定只看第一项 | 3 failed（补样本后） |
| `queue_expired` 误从 queue 源取 | 1 failed |
| `sourcesAvailable` 恒为 2 | 2 failed |
| 活动积压写死 572 | 2 failed |
| 无运行记录当 0%（视图） | 1 failed |
| legacy 默认展开 | 4 failed |
| 两段错误合并成一个 | 3 failed |
| legacy 全 0 判为已加载（视图） | 1 failed（补判据后） |

★ **两处「变异没转红」，都不是判据无牙**：

1. `hasLeaseAnomaly` 只判第一项仍全绿 —— 样本里 `node_unclaimable=2`
   **恒非 0**，「任一 > 0」在它身上永远成立 ⇒ **第 ② 类前提失效**：
   后两项从没被单独量到。⇒ 补「每项各自当主角」的样本后转红（3 failed）。
2. 视图层的 `legacyMayBeUnloaded` 改成恒假仍全绿 —— **视图根本没测
   「legacy 全 0」这条路径**。API 层测了 `legacySectionMayBeUnloaded` 本身，
   但**没人量过视图消费它时显示什么**。
   ★ 规律：**helper 有测试 ≠ 消费它的分支有测试**。
     「值算对了」和「拿这个值渲染出了什么」是两个独立的东西。

★ 第三次**「用 `expect` 断言一个我没实现的字符串」**：
  403 的文案实际是「当前账号没有查看探测系统的权限」，
  我断言 `toContain('没有权限')`；覆盖率 115/120 渲染成 `95.8%`，
  我断言 `toContain('115')`。两条都是我断言里的字符串写错，不是产品问题。
  ★ 规律：断言文案前先 `grep` 一次实际字符串，别凭印象写。

★ zh 文案里写了 `**近 2 小时**`（markdown 粗体）——
  这是纯文本 UI，`**` 会**原样显示**。已改成「」。
  ⇒ 规律：i18n 值不是 markdown，写强调要么用 UI 组件，要么不用。

★ **十连跑要重新跑一遍才算数**：
  第一次连跑的第 10 轮报了 `1 failed | 583 passed (584)` ——
  **不是产品 flaky**，是我自己为了核键而在 `src/views/` 临时放了一个
  `__probe_keys.spec.ts`，它被这一轮的 glob 收了进去。
  ⇒ 判据本身出问题的时候，先看**这一轮的用例数对不对**：
    584 ≠ 583 说明集合被污染了，而不是代码变了。
  重跑（清掉临时文件后）10 轮全为 583/583。

★ 顺带记一次**自己写的核验脚本出错**：
  我写了个一次性脚本想确认 `ProbeHealthView` 的 41 个键都能解析，
  报「41 个全部未解析」（含确定存在的 `probeHealth.title`）。
  真因是脚本的正则只捕获了**后缀**（`title`）却按**全路径**去查。
  ⇒ **41/41 全挂**本身就是「脚本错了」的信号：真缺键不会一个都不中。
    修正后 41/41 全过。
  ★ 规律：一次性核验脚本报错时，先问「**报错面是不是符合缺陷的形状**」。
    真实缺陷通常只命中一部分；命中 100% 几乎总是量具本身坏了。

---

### 11.49 探测模型级端点上移：模型健康 + 节点探测队列（第二十一轮，admin 档）

本轮把 probe 线的粒度补齐。四个页面构成**从粗到细**的完整链路：

| 页面 | 粒度 | 端点 |
|---|---|---|
| 本页 `/probe-model` | **模型**级 | `probe/dashboard` |
| `/heatmap` | 模型 × 凭据 | `credentials/heatmap` |
| `/probe`（本轮扩容） | **任务**级 + **节点队列** | `probe/queue-tasks` + `probe/node-tasks` + `probe/provider-latency` |
| `/probe-health`（上轮） | **系统**级 | `probe/system-health` + `probe/queue-snapshot` |

**鉴权**：都在 `RegisterProbeDashboardRoutes(mux, wrapAdmin)` ⇒ **admin 档**，
不设 `requiresRole`。

#### ★★★ 后端把 SQL NULL 压成了 0，客户端**拿不到区分依据**

`admin/probe_dashboard.go:2336-2348`：

```go
func nullFloat64(v sql.NullFloat64) float64 {
	if !v.Valid { return 0 }   // ★ NULL → 0
	return v.Float64
}
func nullInt(v sql.NullInt64) int { if !v.Valid { return 0 }; return int(v.Int64) }
```

而 `ModelHealthSummary` 的这些字段是**普通 `float64` / `int`**
（无指针、无 `omitempty`）：`healthy_percentage`、`failing_percentage`、
`avg_success_rate_7d`、`avg_verification_hours`、`total_real_success_24h`、
`total_real_failure_24h`。

⇒ `healthy_percentage: 0` 有两种完全不同的含义：
  「0% 的凭据健康」（真值）/「SQL 算出来是 NULL」（没数据）。
**信息在后端就丢了，客户端无法恢复。**

★ **唯一可推导的判据**：`total_credentials === 0`。
  「0 个里的 0%」在数学上不成立 ⇒ 该 0 必然是 NULL 被压平的产物。
  这不是猜测，是可证的，所以 `derivedStatsAbsent()` 只在这一点上判 true。
  ⚠️ 它**不是**完备判据：`total_credentials > 0` 时若 SQL 仍返回 NULL，
     客户端**仍然识别不了** —— 那只能靠后端把字段改成指针。
     ⇒ 文案措辞是「无数据」而不是「0%」，方向刻意偏向保守。

★ 另加一条**自相矛盾检测**：状态明细之和（healthy+suspicious+failing+probing）
  大于 `total_credentials` 时报「这行的数据自相矛盾」——
  后端数据自己打架时，把它当正常行展示等于替它背书。

#### ★ `real_success_rate_24h` 三态（与 §11.47 的 `success_rate_last_1h` 同款）

`ModelHealthSummary:64` 是 `*float64` + `omitempty`
⇒ **字段缺失 = 近 24h 没有真实请求**，不是 0% 成功率。
而 `total_real_success_24h` / `total_real_failure_24h` 是普通 `int`（NULL→0）
⇒ 两者之和为 0 时**分不清**「没有请求」与「NULL 被压平」
⇒ `realRequests24hOf()` 返回 `null`，UI 显示「无数据」而不是 0。

★ ★★ **本轮我自己写错并被测试当场抓住的一个 bug**：
  `real_success_rate_24h` 是 0..1 的**比例**，我在模板里写成
  `value.toFixed(1) + '%'`（漏了 `* 100`）
  ⇒ **98.4% 会被显示成「1.0%」**。
  被 `ProbeModelHealthView.spec` 的 `有值 ⇒ 显示百分比` 抓住。
  ★ 同页的 `healthy_percentage` 后端**已经**是百分数（83.3），
    `avg_success_rate_7d` 才是 0..1 —— 同一个卡片里两种量纲，
    漏乘一次就是一个差 100 倍的显示错误。

#### ★ 第六种越界语义：静默回落，且姊妹端点默认值不同

`node-tasks`（`:1444-1453`）与 `queue-tasks`（`:1313-1323`）的 `limit`
都是 `if n > 0 && n <= 200` 才采纳，否则**保持默认、不报错**：

| 端点 | 默认 | 上限 |
|---|---|---|
| `queue-tasks` | **100**（`:1318`） | 200 |
| `node-tasks` | **120**（`:1445`） | 200 |

⇒ 抄错默认值会让人以为「后端只返回 100 条」而实际是 120 条。
⇒ 判据专门锁住「两个常量**不相等**」。
⇒ 前端**必须自己夹**，且夹到 200；不发 `limit` 时才用后端各自的默认。

#### ★ `queue-tasks` 与 `node-tasks` 是**两个不同的队列**

- `queue-tasks` ← `credential_probe_queue`（完整性探测规划器），`source="integrity"`
- `node-tasks` ← `node_probe_state`（错误触发的 `NodeProbeWorker`），`source="node_probe"`

后端 `NodeProbeTaskRow.Source` 的注释明说是为了让「前端能一致地 badge/合并两队列的行」。
⇒ 页面上分成**两段**独立渲染，各带自己的空态与错误。
★ 渲染成一团就分不清「这是哪条队列在积压」。

#### ★ 「等一等就好」与「等再久也不会好」必须分得开

`node_probe_state` 的状态机（与 `bg/node_probe.go` 一致）：
`running`（被租约持有）/ `paused`（达重试上限）/ `pending`（等下一个 backoff tick）。

**`paused` 不会自愈** —— 它已达重试上限，等下去也不会自己好；
`pending` / `running` 会自己往前走。
⇒ `paused` 行加左边框 + 「需要人工介入」提示。
判据覆盖三种状态各自的前置（避免「只看 `status==='paused'`」在
`paused` 字段为 false 时误判 —— 那条也被显式锁住）。

★ `last_latency_ms` 是 `*int` + omitempty ⇒ 缺失 = 没有延时记录，
  `0` 是**真实值**（探到了但 0ms）。两者必须分开。

#### ★ `model` 过滤是 **ILIKE 子串匹配**

`probe_dashboard.go:675-679`：`WHERE raw_model_name ILIKE $1 OR outbound_model_name ILIKE $1`
且 `$1 = "%" + modelFilter + "%"`。
⇒ 搜 `gpt-4` 会同时命中 `gpt-4o` / `gpt-4o-mini` / `gpt-4-turbo`。
⇒ 搜索框旁必须挂「按子串匹配」说明，否则用户以为筛的是精确名。

#### ★ 「整页错误」的判据从「两个都失败」改成「**一个都没成功**」

`/probe` 加入第三个端点后，`error`（整页横幅）只在**三个全挂**时显示。
挂 2/3 时只逐段报错 —— 用户能看出哪一段挂了、哪一段还活着，
这比一条全局横幅**信息量更大**；而「整页都挂了」这个横幅
只在真的全挂时才有意义。

★ 这条**改的是语义不是数字**，所以同步改了既存判据并补了三条：
  挂 2/3 不显示整页错误 / 三个全挂显示 / 只挂一个也不显示（反向锁定）。
  ⇒ 判据变红时先问「**是产品错了还是语义本来就该变**」，
    不要为了让旧判据变绿就改回去。

### 11.50 本轮门禁（第二十一轮，探测模型级）

| # | 门 | 结果 |
|---|---|---|
| 1 | css 门自测 | **11/11** |
| 2 | 触控门自测 | **11/11** |
| 3 | i18n 门自测 | **23/23** |
| 4 | `gate:selftest` | **45 条断言全绿** |
| 5 | `vue-tsc -b` | 通过 |
| 6 | i18n 键集门 | 通过（各 **751 键**，+32；**574 个源码字面量键全部存在**） |
| 7 | css 媒体查询门 | 通过（**49 文件**，+1） |
| 8 | 触控热区门 | 通过（**46 个 .vue**，+1） |
| 9 | `vitest run` | **647 用例 / 53 文件全绿**，**连跑 10 次全绿** |
| 10 | `npm run build` | 通过 |

★ 变异证据（**6 处，全部转红**）：

| 变异 | 结果 |
|---|---|
| 分母 0 不判「无数据」 | 1 failed |
| ★ 真实成功率漏乘 100（本轮真犯的错） | 2 failed |
| 不查明细/分母自相矛盾 | 2 failed |
| `node-tasks` 错误被静默吞掉 | 1 failed |
| 缺失延时当 0ms | 1 failed |
| 不再标记「需要人工」 | 1 failed |

★ 触控门抓到 `.pm__kv-row min-height: 20px`。门唯一豁免是
  `/* R1-legacy */`（**已知存量**），拿它盖新代码就是撒谎
  ⇒ 抬到 48px 并把该容器改成**两列网格**补偿高度
  （4 行 48px 会把卡片撑得过长）。

★ 顺手修一个**既存缺陷**：`zh-CN.ts` 的 `probe.latencyWindowHint` 值里
  写了 markdown 粗体 `**直连成功**` —— 纯文本 UI 会**原样显示**。
  ⇒ 已改成「」。
  ★ `grep '\*\*'` 在两个词典里共 10 处，逐条看过：**9 处在注释里**
  （开发备注，正常），**只有 1 处是真 UI 值**。
  ⇒ 规律：批量替换前**先看清命中的是注释还是值**，
    否则会把 9 条正常注释一起改坏。

★ 一次**变异脚本自身写错**：`(x).value * 100` 换成
  `(x).value /* 注释 */` 时，**行内 `//` 注释吞掉了模板的闭合 `)` 与 `}}`**
  ⇒ 模板编译失败，那个 spec 根本没加载（vitest 只报了另一个文件的 21 条）。
  ★ 现象是「变异后用例数变少」，不是「变异后红」——
    **先看用例数对不对**，再判断判据有没有牙。

---

### 11.51 探测缓存面上移：可用性时间线 + 可用性缓存快照（第二十二轮，admin 档）

| 端点 | 移动端 | 档位 | 抽屉席 |
|---|---|---|---|
| `GET /api/admin/probe/availability-timeline` | `/timeline` | `admin` | 可用性时间线 |
| `GET /api/admin/probe/cache-state` | `/cache-state` | `admin` | 可用性缓存 |

累计（`git ls-tree ea1b1a1a1` 实测）：**30 视图 / 29 API 模块 / 26 抽屉席**（7 席 superAdmin 档）。
（口径：`views/*.vue` 去 spec、`api/*.ts` 去 `*.spec.ts` / `*.test.ts`。
★ 这里曾写成「28 API 模块」，比 `git ls-tree` 实测少 1 —— 已按实测更正。
★ 「API 模块」是**目录现状**，会被同一分支上的并发会话改动（例如此后
  并发会话新增了 `transport.ts`），引用这个数时必须**当场实测**，不要沿用上轮。）

缓存页回答的是一个**具体且高频**的运维问题：
「探测说这个凭据是健康的，路由为什么没选它？」

#### ★★★ 陷阱一：**两个静默截断，响应里都没有标记**

| 端点 | 上限 | 位置 | 后果 |
|---|---|---|---|
| `availability-timeline` | `LIMIT 500` | `admin/probe_dashboard.go:1154` | ≈20 模型 × 24 小时；再多就丢，且**无任何标记** |
| `cache-state` | `ScanKeys` 4096 | `bg/model_availability_reader.go:148-153`（注释：`Cap admin enumeration to keep the endpoint cheap`） | key 枚举被截断，**无标记** |

⇒ 两页撞上限时只能说「**可能被截断**」，**不能说**「共 N 条，全部如下」。
⇒ 判据必须成对写：撞上限说截断 / 上限−1 说正常计数（否则边界那条是恒真的）。

#### ★★★ 陷阱二：**量纲陷阱第 3 处**，同一个仓库里三种量纲并存

`v_model_availability_timeline`（`deploy/sql/schemas/baseline/01-schema.sql:18765-18780`）：

```sql
round(((count(*) FILTER (WHERE status='ok')::numeric * 100.0) / count(*)::numeric), 2) AS success_rate
```

**视图里已经乘过 100** ⇒ 客户端再乘一次就是 **9000%**。

累计三处，必须各走各的显式函数（不自己写 `.toFixed(1) + '%'`）：

| 字段 | 原始量纲 | 处理 |
|---|---|---|
| `v_model_availability_timeline.success_rate` | 0..100（已乘） | 直接用 |
| `v_model_health_dashboard.avg_success_rate_7d` | 0..1 | **要 ×100** |
| `healthy_percentage` | 已是百分数 | 直接用 |

★ 判据写成对拍两条：`success_rate=90` ⇒ 显示 `90.0%` 且**不含** `9000`；
`success_rate=0.9` ⇒ 显示 `0.9%` 且**不含** `90.0%`（后者证明没有多乘）。

#### ★★ 陷阱三：同族端点的 `model` 语义**相反**

- dashboard 侧：`WHERE raw_model_name ILIKE '%'||$1||'%'`（`:675-679`）——**子串**
- timeline 侧：`WHERE raw_model_name = $1`（`:1149-1152`）——**精确**

⇒ 搜 `gpt-4` 在「模型健康」页能命中 `gpt-4o`，在时间线页**不能**。
⇒ 时间线页**无条件**显示「精确匹配」提示（不限有没有输入筛选词）。

#### ★ 陷阱四：两端点的空态形状**不同**

| 端点 | 空时 | 依据 |
|---|---|---|
| `availability-timeline` | `timeline: null`（nil slice） | `var timeline []TimelinePoint`，**无** `make`、**无**初始化 |
| `cache-state` | `entries: []`（显式空数组） | `entries = []CacheStateEntry{}`（`:2040`） |

⇒ 两个 spec 各测一条 null 与 `[]` 都走空态。

★ 顺带一条：`avg_latency_ms` 的 Go 类型是 `*float64` + `json:",omitempty"`
⇒ SQL NULL 时**键整个不存在**（客户端读到 `undefined`，不是 `0`）。
⇒ 「该小时无成功探测」与「真的 0ms」必须分开显示，两者判据成对。

★ 视图里 `outbound_model_name` 恒等于 `raw_model_name`
（视图定义第二列就是 `raw_model_name AS outbound_model_name`）⇒ **零信息量**，不渲染。

#### ★★ 陷阱五：`format=prom` 会把 `cache-state` 变成 `text/plain`

`writeCacheStateProm` 走的是纯文本输出 ⇒ **同一个 URL 换一种 format，JSON 解包就炸**。
⇒ 客户端**永远不发** `format`（写进文件头注释，防止下一个人「顺手加个导出按钮」）。

#### ★★★ 陷阱六：**503 ≠ 空**，两个来源都表示「读不到」

| 503 message | 位置 | 真实含义 |
|---|---|---|
| `availability reader not wired` | `:1971-1973` | 这个部署**没接** Redis 读取器 |
| `redis client unavailable` | `:2008-2011` | 接了，但 `h.redisClient` 不是 `*redis.Client` |

⇒ 两者都**绝不能**显示成「缓存里没有匹配的条目」——
那会让运维去查「凭据是不是没被探测」，而问题在**部署配置**。
⇒ 两套独立文案，且都明说「不是『缓存里什么都没有』」。

★ 姊妹端点的越界语义继续不一致（累计第 6 种）：本端点**没有**任何
`format`/`limit` 校验，也没有 400 分支 —— 与 `/api/credentials/heatmap`（400）
和 `/api/admin/node-health/{id}/timeline`（静默 clamp）都不同。

#### ★★ 陷阱七：`state` 与 `available` 是**两个独立字段**

`toCacheStateEntry`（`:2126-2140`）分别取 `snap.State` 与 `snap.Available`
—— 后端**不做**任何一致性推导。

⇒ `state ∈ {healthy, healthy_confirmed, available}` 且 `available === false`
就是「**模型明明健康却没被选中**」的根因信号，必须单独标出
（左侧描边 + 独立提示条），否则这页就只是个列表。

⇒ 判据覆盖：`healthy` 标 / `healthy+available` 不标（防恒真）/
`failing+available=false` **不**标（本来就是故障，不是矛盾）/
`healthy_confirmed` 标 / 大写 `HEALTHY` 标（大小写不该决定结论）/
多条混排时**逐条**判断（只标该标的那一条）。

#### ★★★ 陷阱八：非法 `credential_id` 被后端**静默忽略** = 不过滤返全量

⇒ 客户端本地拦。但**守卫必须是 `^[1-9]\d*$`，不能是 `^\d+$`**：

- `^\d+$` 放行 `"0"`；
- `fetchCacheState` 的运行时守卫是 `credentialId > 0` ⇒ **`0` 被丢掉不发**；
- ⇒ 用户输入 0，看到的是**全量**，却以为「筛了凭据 0」。

★ 这是**本轮真犯的错**，且是「静默忽略」那一类缺陷的**同构复发**：
修掉 `abc` 却漏了 `0`。文案写的是「必须是正整数」，代码却接受 0 —— **文案与判据不一致**。

#### ★ Go 字段名与 JSON 键不同名

`CacheStateEntry` 的 Go 字段是 **`RawModel`**（`:1937`），JSON 键是 `raw_model_name`。
按 Go 名访问得到 `undefined` 且**不报错** ⇒ 类型里加注释标注
（同类的 `TurnGroupItem.latency_ms` vs `SessionTurnTreeItem.latency` 已在
`web` 侧出现过一次）。

#### ★ 本轮修的两处既存缺陷

1. **新写的 i18n 值里带了 markdown `**`**（5 处：`timeline.truncated`、
   `cache.truncated`、`notWiredHint`、`redisUnavailableHint`、`contradiction`）。
   本仓**没有** markdown 渲染器 ⇒ 星号原样显示。
   ★ 上一轮刚修过 `probe.latencyWindowHint` 的**同款**问题，
     **修完一轮又自己犯** ⇒ 教训：新增字典段时，值里不许出现 `**`。
     （`grep '\*\*'` 现在 zh 里剩 7 处，逐条确认**全是注释**。）
2. **`common.cancel` 被当成「清空筛选」按钮标签**（3 个视图：
   `AvailabilityTimelineView` / `CacheStateView` / `ProbeModelHealthView`）。
   「取消」读起来像关掉页面 ⇒ 新增 `common.clearFilters`（「清空筛选」/ Clear filters）。
   ★ 判据锁死「标签是**清空筛选**且**不是取消**」，并锁「无筛选时按钮不出现」。

### 11.52 本轮门禁（第二十二轮，探测缓存面）

| # | 门 | 结果 |
|---|---|---|
| 1 | css 门自测 | **11/11** |
| 2 | 触控门自测 | **11/11** |
| 3 | i18n 门自测 | **23/23** |
| 4 | `gate:selftest` | **45 条断言全绿** |
| 5 | `vue-tsc -b` | 通过 |
| 6 | i18n 键集门 | 通过（各 **797 键**，+46；**609 个源码字面量键全部存在**，扫 107 个 `.vue`/`.ts`） |
| 7 | css 媒体查询门 | 通过（**51 文件**，+2） |
| 8 | 触控热区门 | 通过（**48 个 .vue**，+2） |
| 9 | `vitest run` | **733 用例 / 56 文件全绿**，**连跑 10 次全绿**（+86：32 API + 18 时间线 + 36 缓存） |
| 10 | `npm run build` | 通过 |

★ 缓存页变异证据（**6 处，全部转红**）：

| 变异 | 结果 |
|---|---|
| 503 不再走「读不到」分支（退化成空态） | 3 failed |
| 撞 4096 不再说「可能被截断」 | 1 failed |
| 非法 `credential_id` 不本地拦（`if (false)`） | 3 failed |
| 矛盾项永远描边（恒真） | 3 failed |
| 矛盾提示条永远不显示 | 1 failed |
| 503 两类合并成同一句 | 1 failed |

★ **变异脚本第一次 6 处全部「没施上」**（第 ① 类成因：锚点不存在）。
  原因是 `perl -0pi -e` 的多层 `bash`/正则转义把字面量改写了
  ⇒ 改成 **Node 字面量串替换**（不做正则）后 6 处全中。
  ★ 归因纪律的价值：没有直接归「判据无牙」，而是先打印锚点真实字节。

★ 一次**断言写错**（第 ③ 类：期望值算错）：
  断言「不把后端英文抛给用户」写成 `not.toContain('availability reader')`，
  结果红了 —— 但那串是我**故意**留在文案里的组件名（运维要看）。
  ⇒ 改成精确断言**原始 message 整句**（`'availability reader not wired'`），
  并补了一条 redis 分支。★ 「不许泄漏原文」要断言的是**原文本身**，
  不是原文里出现的**词**。

★ 还原纪律：变异后用 `cp` 备份**无条件还原**（脚本里写 `restore()` 不带 `||` 兜底），
  结束核对 `md5` 与残留标记 —— 本轮 md5 一致、残留 0。

---

### 11.53 自动路由索引上移：模型 × 任务表现（第二十三轮，superAdmin 档）

| 端点 | 移动端 | 档位 | 抽屉席 |
|---|---|---|---|
| `GET /api/admin/auto-route/analytics/model-task-index` | `/task-index` | **`super_admin`** | 模型任务索引 |

累计（当场实测）：**31 视图 / 31 API 模块 / 27 抽屉席**（**8 席 superAdmin** 档）。
（口径同 §11.52。★ 31 个 API 模块里有 `transport.ts` 是并发会话新增的，
不属于本专题 —— 这个数是**目录现状**，不是「本专题搬了几个」。）

同族三份的分工：`/funnel` 答「**单个模型**在 7d/24h 窗口里的漏斗与可信度」，
`/matrix` 答「模型 × 任务的热力矩阵」，本页答「**按 5 分钟桶滚动**的表现排行，
带主用凭据」。

#### ★★★ 陷阱一：**只看自动路由的请求**

生产者 `bg/auto_index_refresher.go` 的 rollup SQL 里：

```sql
WHERE rl.ts >= NOW() - INTERVAL '5 minutes' AND rl.ts < $1
  AND rl.is_auto_request = TRUE
  AND rl.canonical_id IS NOT NULL
```

⇒ 人工/直连流量**完全不进这张表**，排行榜天然偏小众模型。
⇒ 页首必须常驻口径，否则「这个模型请求量这么低」会被读成全站事实。

#### ★★ 陷阱二：**只返回最新一个 5 分钟桶**

handler 先 `SELECT MAX(bucket) FROM model_task_index`，
再 `WHERE mti.bucket = $1`（`admin/analytics.go:621-660`）。

⇒ 这**不是**窗口聚合，也不是趋势图。桶窗口是滚动的 5 分钟，
而刷新器每 5 分钟跑一次 ⇒ 数据可能**滞后约 5~10 分钟**。
⇒ 页首常驻「数据截至 <bucket>」+ 滞后提示。
★ 有 bucket 与没 bucket 是两个不同状态（见陷阱五）。

#### ★★★ 陷阱三：**量纲第 4 处，而且这个 `0.9` 是陷阱中的陷阱**

列类型 `success_rate numeric(5,4)`，生产者是
`COALESCE(AVG(CASE WHEN rl.success THEN 1.0 ELSE 0.0 END), 0.9)`
⇒ **0..1 比率**，显示成百分比要 ×100。

⚠️ 同一份刷新器里**另一个表**用 `0.9::numeric(5,4)` 作冷启动默认
（`auto_index_refresher.go:625`，注释写 `success_rate=0.9` 是默认分）
⇒ **同一个家族里 `0.9` 既是「90% 成功率」又是「默认值」**。
⇒ 下一个人很容易在这里加一条「0.9 视为无数据」特判 —— 那是错的。

★ 因此函数**刻意不同名**：`formatModelTaskRatePct`（0..1）
vs `probeTimelineCache` 的 `formatSuccessRatePct`（0..100）。
两个**同名**函数处理**相反量纲**，是最容易埋「9000%」的地方。

累计四处量纲：

| 字段 | 原始量纲 | 处理 |
|---|---|---|
| `v_model_availability_timeline.success_rate` | 0..100（视图已乘） | 直接用 |
| `v_model_health_dashboard.avg_success_rate_7d` | 0..1 | ×100 |
| `model_task_index.success_rate` | **0..1** | **×100** |
| `healthy_percentage` | 已是百分数 | 直接用 |

#### ★★★ 陷阱四：三个数值列的「0」/「1000」是**生产者兜底值**

```sql
COALESCE(AVG(rl.latency_ms), 0)::int                                  -- 0 = 没量到延时
COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY rl.latency_ms), 1000)::int  -- 1000 = 没量到
CASE WHEN SUM(rl.total_tokens) > 0 THEN (...) ELSE 0 END              -- 0 = 没有成本数据
```

⇒ 三者含义**完全不同**：`avg=0` 是「没量到」，`p95=1000` 是「没量到」，
`cost=0` 是「没有 token/成本数据」——**都不是**「0ms」「免费」。

★ 后端**给不出判据**（列可空，但被 `COALESCE` 吃掉了）
⇒ 真实测量也可能是 `0ms` / `1000ms` / `$0`（免费模型），
  客户端**无法区分**，只能弱化：

- **数值照显**（不改数 —— 没有判据就不该改数）；
- 加 `.ti__kv-item--maybe` 弱化样式（灰 + 斜体）；
- 页脚常驻图例，逐条说明三个兜底值。

★ 这与 §11.44「helper 有测试 ≠ 消费它的分支有测试」同源：
判据表达的是「**可能**是兜底」，不是「一定是」。

#### ★★★ 陷阱五：`items` 是**稀疏对象**，`bucket === null` 是独立状态

handler 用 `map[string]interface{}` **条件写入**每个字段
（`admin/analytics.go:684-722`）。对照表定义：

| 字段 | 表列 | 响应里 |
|---|---|---|
| `canonical_id` | `integer NOT NULL` | **恒存在** |
| `sample_count` | `integer NOT NULL`（`COUNT(*)` ⇒ ≥1） | **恒存在** |
| `canonical_name` | 来自 `LEFT JOIN models_canonical` | **未命中时整个键不存在** |
| `success_rate` / `avg_latency_ms` / `p95_latency_ms` / `avg_cost_per_1k_usd` / `primary_credential_id` | 列可空 | **键可能整个不存在** |

⇒ 类型里这些字段一律可选；`undefined` 渲染成 `—`，**不是 0**。
⇒ `canonical_name` 缺失回落成 `#<id>`（`||` 兜底），不渲染成空白。

空表是**第四种**状态（`admin/analytics.go:643-649`）：

```json
{ "bucket": null, "items": [], "warning": "model_task_index is empty; awaiting first bg worker refresh" }
```

⇒ 这是「**后台刷新器还没首刷**」，不是「这段时间没有自动路由流量」
⇒ 独立渲染，且必须明说后者**不成立**。

#### ★ 陷阱六：`top` 越界**静默回落 20**（第 7 种越界语义）

```go
top := 20
if v, err := strconv.Atoi(r.URL.Query().Get("top")); err == nil && v > 0 && v <= 500 { top = v }
```

⇒ 不传 / 非法 / `0` / `501` 四种情况都得到 20，**不 400**。
★ 与同族 `window` / `metric`（**400**）语义相反，**别照抄**。

⇒ 但这一条和前两轮的「写死上限」有本质区别：
`top` 是**客户端自己发的**，所以 `items.length === top` 是**精确**的截断信号
（时间线的 `LIMIT 500` / 缓存的 `ScanKeys` 4096 是后端写死且无标记，
只能说「可能被截断」）。
⇒ 判据必须随 chip 变化：切到 50 后 20 行**不**算截断。

#### ★ 其它已核实的口径

- `task_type` 过滤：`strings.TrimSpace` 但**不** `ToLower`，
  SQL 是 `mti.task_type = $1`（**大小写敏感**）
  ⇒ 客户端只 trim，**擅自改小写反而查不到**。
  （与 `parseAnalyticsWindow` 的 ToLower+TrimSpace 形成对照，
  又是同族内两种行为。）
- `primary_credential_id` = `MODE() WITHIN GROUP (ORDER BY rl.credential_id)`
  ⇒ 是「这一组最常用的那个凭据」，不是「唯一/必需的凭据」。
- `__specified__` 是合成键（`analytics.go:36` `SpecifiedModelTaskKey`），
  表示「请求显式指定了模型，任务类型未知」
  ⇒ **复用** `autoRouteMatrix` 的 `taskLabel`（同族收口，不重写），
    并在该行单独挂提示条。

#### ★ 本轮修的两处（含一个**我自己刚犯完又犯**的）

1. **又一次在新写的 zh 值里带了 markdown `**`**（3 处：`scopeNote`、
   `awaitingFirstHint`、`truncated`）。
   ★ 这是**同一个坑在同一个会话里修完 40 分钟后再犯一次**：
     §11.50 刚把 `probe.latencyWindowHint` 的 `**` 修掉，
     §11.52 又在自己新增的 `timeline`/`cache` 段里带进 5 处并当场修掉，
     §11.53 的 `taskIndex` 段再带进 3 处。
   ⇒ **规律**：新增/编辑字典值后，提交前固定跑一次
     `grep -n '\*\*' src/i18n/zh-CN.ts`，
     **逐条**确认命中的是注释还是值（注释里的 `**` 是正常开发备注）。
2. **`formatMs` 加了千分位，与全站延时口径不一致。**
   时间线页渲染 `avgLatency: '{ms}ms'` ⇒ `1200ms`；
   本轮初版写成 `1,200ms`（还顺手复制了一个 `fmtIntSafe`，等于造第二份格式化）。
   ⇒ 改为不加千分位，并加判据锁死 `formatMs(1000) === '1000ms'`。
   ★ **判据自己先红了一次**（`toContain('1000ms')` 实得 `1,000ms`），
     才暴露出这个不一致 —— 断言写错有时正是缺陷的信号，
     不总是「我的测试有问题」。

### 11.54 本轮门禁（第二十三轮，自动路由索引）

| # | 门 | 结果 |
|---|---|---|
| 1 | css 门自测 | **11/11** |
| 2 | 触控门自测 | **11/11** |
| 3 | i18n 门自测 | **23/23** |
| 4 | `gate:selftest` | **45 条断言全绿** |
| 5 | `vue-tsc -b` | 通过 |
| 6 | i18n 键集门 | 通过（各 **825 键**，+28；**636 个源码字面量键全部存在**，扫 110 个 `.vue`/`.ts`） |
| 7 | css 媒体查询门 | 通过（**52 文件**，+1） |
| 8 | 触控热区门 | 通过（**49 个 .vue**，+1） |
| 9 | `vitest run` | **810 用例 / 58 文件全绿**，**连跑 10 次全绿**（+77：40 API + 36 视图 + 1 抽屉白名单） |
| 10 | `npm run build` | 通过 |

★ 抽屉白名单是**白名单式**断言（`AppDrawer.spec.ts:115-124`）：
把被 tenant_admin 挡掉的 superAdmin 席 key 逐个列出，加新 superAdmin 席**必须**同步，
不得放宽。

★ 变异证据（**9 处，全部转红**）：

| 变异 | 结果 |
|---|---|
| `formatModelTaskRatePct` 漏乘 100（0..1 当百分数） | 5 failed |
| 「尚未首刷」永不触发 | 2 failed |
| 撞 top 不说截断 | 2 failed |
| 兜底值弱化标记恒假 | 1 failed |
| 删掉页首口径说明 | 3 failed |
| `canonical_name` 缺失不回落（渲染空白） | 1 failed |
| ★ **截断判据不跟 `top` 走**（写死后端默认 20） | 1 failed |
| `top` 上界守卫去掉（501 也发） | 1 failed |
| `isAwaitingFirstRefresh` 改判 `items.length === 0` | 3 failed |

★ 第 7 条是本轮**最值得记的一条**：它只在一个用例上转红
（「切到 50 后 items=20 不该判截断」），其它全绿 ——
即「截断提示」这个功能**看起来是好的**，只在 top≠20 时才出错。
⇒ 判据若只测默认 top，**整条截断逻辑可以带着 bug 长期存活**。

★ 变异脚本自身又出一次错：9 处里有 2 处「写入后没找到标记」——
  替换文本**没有把标记 ID 带进去**（`v-if="false"` 和一个模板插值无处插标记）。
  ★ 这不是判据无牙，是**脚手架自检**在报警；
    补上 `data-mut="MUT5"` / `<!--MUT6-->` 后两条都转红。
    ⇒ 变异脚本的「施上确认」必须覆盖**每一条**，否则会误报成「判据无牙」。

---

### 11.55 会话运维面上移：审计清单 + 在线会话 + 轮次树（第二十四轮，admin 档）

| 端点 | 移动端 | 档位 | 抽屉席 |
|---|---|---|---|
| `GET /api/admin/sessions/list` | `/session-audit` | `admin` | 会话审计 |
| `GET /api/admin/sessions/online` | `/sessions-online` | `admin` | 在线会话 |
| `GET /api/admin/sessions/{id}/timeline` | 同上，行内展开 | `admin` | （不单独占席） |

三条都挂 `wrapAdmin` / `admin(...)`（`cmd/gateway/main.go:7268-7275`、
`admin/handler.go:1182-1183`）⇒ **admin 档，tenant_admin 可用**，导航不设 `requiresRole`。

★ 但同族的 **`/api/admin/sessions/summary` 是 POST-only**
（`admin/session_summary_v2.go:89`）⇒ 同族端点方法不一致，本专题不碰它。

#### ★★★ 陷阱一：同族三端「没登录」是**两种**状态码

| 端点 | `GetAuthContext(r) == nil` 时 | 依据 |
|---|---|---|
| `sessions/list` | **404** `not found` | `session_list_v2.go:128-131`，注释明说「不能让 legacy default 掩盖缺失身份」 |
| `sessions/online` | **401** `authentication required` | `handleSessionsOnline` |
| `sessions/{id}/timeline` | 走 `auth.TenantID` 为空分支 | 同上 |

⇒ 客户端**不能**用「404 ⇒ 没权限」做跨端点推断。
本专题把 401/403/404 **都**映射成「没有权限」文案，但这是**逐端点各判各的**，
不是抽一个通用规则（三个端点的错误信封本身也不一致，见陷阱八）。

#### ★★★ 陷阱二：`list` 有**四个恒定字段**，本页面**故意不渲染**

追到结构体构造处（`admin/session_list_v2.go:175-180` 包 `audit` ⇒
`domains/sessionforensics/export.go:459` 的 `SessionAudit` 字面量）：

| 字段 | 实际取值 | 原因 |
|---|---|---|
| `has_session_id` | **恒 `true`** | `export.go:456` 的 `if sid == nil \|\| *sid == "" { continue }` 已把空/NULL 行全部跳过 |
| `missing_session_ids` | **恒 `0`** | 同上：NULL 行自成一组，又被整组跳过 |
| `has_title` | **恒 `false`** | SQL 的 SELECT 列表里**根本没有** `session_titles` 表 |
| `has_summary` | **恒 `false`** | 同上，`session_summaries` 也没 join |

⇒ 渲染成「无标题」「会话 ID 缺失」= **编造一个后端根本没查的结论**。
⇒ 页面改为：这三项一律不显示，页脚常驻图例**逐条解释为什么没有**。

★ 这与 §11.53「三个数值列的 0 是 COALESCE 兜底」是同一族，
但更极端 —— 那三个至少还是**测量过的值**，这四个是**压根没测**。

#### ★★ 陷阱三：`limit` 与 `tenant` 都被**回显** ⇒ 截断是**精确**信号

```go
limit := 50
if s := r.URL.Query().Get("limit"); s != "" {
    if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 500 { limit = n }
}
```

⇒ 越界**静默回落 50**（不是 400，第 8 种越界语义）。
⇒ 但响应里同时回显 `"limit": limit` 和 `"tenant": tenantID`
⇒ `sessions.length >= limit` 就是「**确实**被后端填满」，不需要说「可能」。

★ 判据必须**用回显值**而不是客户端请求值：
一条专门锁「请求 500 / 回显 2 / 返回 2 ⇒ 仍判截断」——
若错用请求值，`2 < 500` 会漏报，而界面上看不出任何异常。

★ 同理，页面显示的是**后端认下来的那个租户**，不是客户端猜的
（`?tenant=` 只对 super/admin-key 生效，其余角色钉 auth 租户）。

#### ★★★ 陷阱四：`online` 的 **superAdmin 不加租户过滤**

```go
if !IsSuperAdminOrLegacy(r) { query += ` AND rl.tenant_id = $1`; ... }
```

⇒ superAdmin / legacy 角色看到的是**全平台**活跃会话。
⇒ 页首作用域提示必须**按角色**分支，不能只写一句中性说明 ——
同一屏数据在两种角色下含义完全不同。

#### ★★ 陷阱五：`online` 的 `limit` 是**静默 clamp**，与 `list` 的「回落」不同

`NormalizePaginationParams`（`admin/session_online_pagination.go:122-130`）：
`≤0 → 20`；**`>100 → 100`**（不发错、不提示）。

⇒ 客户端只发 1..100；发 500 会被悄悄改成 100。
⇒ 分页用 `LIMIT limit+1` 多取一条判 `has_more`
⇒ **没有**「静默截断」问题，`has_more` 是精确信号（与前两轮不同）。
⇒ `cursor` **不透明**（后端自己 base64 编解码）⇒ 客户端只用回传的
  `next_cursor`，**绝不自己拼**；非法游标 ⇒ 400 `session.pagination_invalid_cursor`。

#### ★★★ 陷阱六：`timeline` 的 `{id}` 有**三种身份**，且后端会告诉你用了哪种

仓库有 5 类 ID 的互查表；这个端点接受其中两种入口：

| 入口 | `session_id_source` |
|---|---|
| `{id}` 按**文本** `gw_session_id` 解释 | `path_gw_session_id` |
| `?session_pk=<sessions.id 数值>` | `session_pk_resolved` |
| **兜底**：`{id}` 是纯数字且按文本查不到行 ⇒ 再按 `sessions.id` 解析 | `numeric_fallback_resolved` |

★ `numeric_fallback_resolved` 意味着「**你传的 id 我是猜的**」
⇒ 页面必须标出来，否则用户会以为在看自己指定的那个会话。
⇒ 未知来源也不能当正常路径，要如实把来源字符串打出来。

★ `session_pk` 非正整数 ⇒ **400**（客户端先拦，不发）。
★ ★★ 这个端点**自带 `truncated` 字段**（= `has_more`）——
  与 `availability-timeline`（写死 500 无标记）、`cache-state`（4096 无标记）
  **不同**，有标记就别再自己猜。

#### ★ `models_used` 可能是 `null`，而 `sessions` 永远不是

- `models_used` 来自 `array_agg(DISTINCT client_model) FILTER (WHERE client_model IS NOT NULL)`
  ⇒ 组内**全部** `client_model` 为 NULL 时聚合结果是 NULL ⇒ 序列化成 **`null`**；
- `sessions` 是 `make([]map[string]any, 0, len(audits))` ⇒ 永远 **`[]`**。

⇒ 前者必须兜底成空数组并显示「未记录模型名」；后者不用管。

#### ★ `total_cost_usd` 的 0 是 COALESCE 兜底（第 4 处同类）

`COALESCE(SUM(cost_usd), 0)` ⇒ **0 = 没有成本数据**，不是免费。
⇒ 复用 `modelTaskIndex.costMayBeNoData`（**同族收口，不重写一份**）。

#### ★ 两种错误信封并存（唯一该共享解包的地方）

- `writeExportJSONError`（`session_list_v2`）⇒ `{"error": "query sessions failed"}`（**扁串**）
- `writeError` / `writeErrorWithCode`（`admin/handler.go`）⇒
  `{"error": {"detail": ..., "code": ...}}`（**嵌套**）

⇒ 响应体**各端点独立解包**（照旧），但**错误文案**由 `api/client.ts` 的
`errorMessage()` 统一处理，它同时认这两种形状 —— 这是唯一合理的共享点
（错误是给人看的字符串，响应体是要按字段解的契约）。

#### ★ 本轮修的两处

1. ★★ **重试不清理上一次错误 ⇒ 重试成功了也看不到内容。**
   `SessionAuditView` 展开失败时写 `timelineError[id]`，
   但重试**没有清**它，而模板是 `v-else-if="timelineError[id]"`
   ⇒ 第二次拉成功时错误分支仍命中，轮次永远渲染不出来。
   ⇒ 重试前先清。★ 这条是**判据抓到的产品缺陷**，不是测试写错：
   「失败 → 收起 → 再展开能拿到数据」这条用例第一次就红了。
2. ★★ **`.on__kv-v` 的 `—` 被整页子串断言误伤。**
   判据写 `expect(text).not.toContain('0ms')`（延时缺失不该显示 0ms），
   结果 freshness 里的 `1200ms` 命中了 `0ms`。
   ⇒ 改成**按「最后延时」那一格的结构断言**（`cell.find('.on__kv-v').text() === '—'`）。
   ★ 子串断言在有数字的页面上要格外小心：子串会被更大的数字包含。

★ i18n 门抓到一条：`en-US` 的 `online.stale` 值写成 `'stale'`，
  与键名同名 ⇒ 被判「未翻译」（运行时显示裸键）。
  ⇒ 改成 `'outdated'`。★ 英文里「标签恰好等于键名」时仍要换词。

★ **markdown 星号第 4 次复发**：本轮 `online.scopeSuper` 又写了
  `后端**不加租户过滤**`。⇒ 提交前的固定动作应是：
  `grep -n '\*\*' src/i18n/zh-CN.ts | grep -vE ':\s*(//|/\*)'`
  （**排除注释行**再判，否则会把 20 多条正常开发备注一起报出来）。

### 11.56 本轮门禁（第二十四轮，会话面）

| # | 门 | 结果 |
|---|---|---|
| 1 | css 门自测 | **11/11** |
| 2 | 触控门自测 | **11/11** |
| 3 | i18n 门自测 | **23/23** |
| 4 | `gate:selftest` | **45 条断言全绿** |
| 5 | `vue-tsc -b` | 通过（先抓到一处未用 import：`ONLINE_LIMIT_DEFAULT`） |
| 6 | i18n 键集门 | 通过（各 **867 键**，+42；**676 个源码字面量键全部存在**，扫 113 个 `.vue`/`.ts`） |
| 7 | css 媒体查询门 | 通过（**54 文件**，+2） |
| 8 | 触控热区门 | 通过（**51 个 .vue**，+2） |
| 9 | `vitest run` | **910 用例 / 62 文件全绿**，**连跑 10 次全绿** |
| 10 | `npm run build` | 通过 |

★ 用例增量的**诚实拆分**：910 − 810 = 100，其中**本专题新增 93**
（`sessions.test.ts` 39 + `SessionAuditView.spec.ts` 32 + `OnlineSessionsView.spec.ts` 22），
**另 7 条属并发会话新增的 `src/styles/safeArea.spec.ts`**。
⇒ 报增量必须逐文件数，不能拿总数差当自己的产出。

★ 提交纪律：并发会话已把 `client.ts` / `transport.ts` / `AppSheet.vue` /
  `HyperApp.vue` / `safeArea.spec.ts` / `theme.css` **加入暂存区**，
  此时 `git commit`（不带路径）会把**他们的工作一起提交**。
  ⇒ 必须用 `git commit --only <本专题路径>`，
    `git add` 只用于让新文件变成「已跟踪」，不影响 `--only` 的边界。

★ 变异证据（**12 处，全部转红**）：

| 变异 | 结果 |
|---|---|
| 截断判定 `>=` 改 `>`（差一） | 4 failed |
| `models_used` 的 null 不兜底 | 3 failed |
| `online` 的 limit 上界守卫去掉（500 会发出去被 clamp） | 1 failed |
| 「猜的 id」判定换成另一个来源 | 3 failed |
| `countTurns` 只数顶层不递归 | 2 failed |
| 删掉图例（恒定字段的解释） | 3 failed |
| 「猜的 id」警告恒真 | 2 failed |
| 截断提示恒假 | 2 failed |
| 成本兜底弱化恒假 | 1 failed |
| ★ 作用域文案恒用 superAdmin 版 | 2 failed |
| ★ 「加载更多」不带 cursor（拿第一页冒充下一页） | 1 failed |
| ★ 延时缺失显示成 `0ms` | 1 failed |

★ 第 11 条值得记：不带 cursor 时**接口不报错、不空**，只是把第一页又返回了一遍 ——
  界面上表现为「加载更多之后列表没变」，很容易被当成后端没数据。
  ⇒ 判据必须断言**实际带上的 cursor 值**，不是只断言「有没有再请求」。

---

### 11.57 系统监控上移：队列状态 + 最近结束的探针运行（第二十五轮，admin 档）

| 端点 | 移动端 | 档位 | 抽屉席 |
|---|---|---|---|
| `GET /api/admin/system-monitor/stats` | `/system-monitor`（面板） | `admin` | 系统监控 |
| `GET /api/admin/system-monitor/recent-runs` | 同上（明细） | `admin` | （不单独占席） |

★ **同族端点档位是混的**（`admin/systemmonitor_handlers.go:629-645`）：

| 端点 | 档位 / 方法 |
|---|---|
| `stats` / `recent-runs` / `migration-metrics` / `recovery` / `stream` | **admin**，GET |
| `by-credential/{id}` / `by-provider/{id}` / `by-model/{model}` / `concurrency` | **superAdmin** |
| `submit` / `start-all` / `stop-all` | superAdmin，写操作 |

⇒ `concurrency` 还是 **PATCH**（改 `self_check_settings.monitor_concurrency`）
⇒ 本轮**不做**（写操作，与 §11.45 起的同纪律一致）。

本页面答的是「**监控器这层**自己在不在干活」，
与 `/probe`（任务级队列）、`/probe-model`（模型健康）、`/probe-health`（凭据探针）
分处不同层，不重复。

#### ★★★ 陷阱一：`stats` 里**每个 0 都可能是「不知道」**

三条独立来源，全都不会报错：

1. **监控器未接线**：`if h.systemMonitor != nil` 整块被跳过 ⇒
   `queue_size` / `running_size` 留 **0**、`in_fallback` 留 **false**
   ⇒ 「队列 0 条、没在降级」是**没接线**时的假象。
2. **读不到配置**：`monitor_concurrency` 用
   `if err == nil && monitorConcurrency > 0 { … }` ⇒ **恒回落 5**。
3. ★★ **最严重的一处**：近 1 小时四个计数用的是

   ```go
   _ = h.db.QueryRow(r.Context(), `SELECT … FROM system_probe_runs
        WHERE created_at >= NOW() - INTERVAL '1 hour'`).Scan(&completed, &failed, &skipped, &tokens)
   ```

   —— **错误被 `_ =` 显式丢弃**。查询失败时四项全是 0，
   响应里与「一小时零次运行」**完全无法区分**。

⇒ 前端方向统一为「宁可说可能没取到」：四项同时为 0 时挂显式说明；
队列与运行同时为 0 时挂「可能未接线」；并发恰等于 5 时挂「可能读配置失败」。

#### ★★★ 陷阱二：**三项之和不是总运行数**（本轮最有价值的一条）

`status` 的 CHECK 约束（`deploy/sql/schemas/baseline/01-schema.sql:16969`）允许 **6 个**值：

```sql
CHECK ((status = ANY (ARRAY['success','failed','expired','skipped','timeout','network_error'])))
```

而 `stats` 的三个计数口径逐字是：

| 字段 | 口径 |
|---|---|
| `completed_total_1h` | `status = 'success'` |
| `failed_total_1h` | `status IN ('failed','timeout','network_error')` |
| `skipped_total_1h` | `status = 'skipped'` |

★ **`expired` 既不在成功、也不在失败、更不在跳过** ⇒ 三者相加**漏掉它**。

⇒ 页面**不显示**「一小时共 N 次」，只显示三个分项 + 一条常驻说明。
⇒ 每一条 `status = expired` 的运行行单独挂标注。
⇒ 判据：`statusCountsAsFailed('expired') === false`
且 `statusIsUnaccounted` 只对 `expired` 为真（六个枚举逐个验过）。

#### ★★ 陷阱三：`total` 是**本页返回行数**，不是数据库计数

扫描出错走 `warnRowSkip(...)` + `continue`（**不是**上抛）。
handler 自己的注释就写着：

> 探针运行记录少一截 = 失败/跳过的探针被静默抹掉，"系统监控全绿"是假象。
> total 取自 len(out)，静默截断不会体现为数字异常。

⇒ UI 里这个数只能叫「记录数」，图例里必须写明它不是数据库总数。

#### ★★ 陷阱四：清单**只含已结束的运行**

两处写入方（`bg/credential_selfcheck.go:908`、`bg/systemmonitor/audit.go:122`）
都在**运行结束后**才 INSERT，`finished_at` 列是 `NOT NULL`，
两处都传 `time.Now()`。

★ 这里我**先做了个推断又自己推翻了**：看到 `FinishedAt time.Time`（非指针）
且无 `omitempty`，第一反应是「未完成的运行会序列化成 `0001-01-01`」，
差点把这条当陷阱写进文档。追到两处写入方才发现**根本不存在这种行**。
⇒ **推出来的陷阱必须追到写入方才能写进契约文档**，
否则文档里会多一条并不存在的坑，而后来的人会照着它加无用的防御代码。

⇒ 真正的语义是：**进行中的探测不会出现在这张清单里** ⇒ 页面说「最近**已结束**的运行」。

#### ★ 陷阱五：`limit` 默认 50 / 上限 200，与同族都不同

| 端点 | 默认 | 上限 | 越界行为 |
|---|---|---|---|
| `system-monitor/recent-runs` | 50 | 200 | 静默回落 50 |
| `sessions/list` | 50 | 500 | 静默回落 50 |
| `sessions/online` | 20 | **静默 clamp 100** | clamp |
| `analytics/model-task-index` | 20 | 500 | 静默回落 20 |

★ 四个端点四套数字 ⇒ **不能照抄任何一处**。
★ 但 `limit` 同样被回显 ⇒ `total >= limit` 是**精确**信号。

#### ★ 陷阱六：同一 URL 前缀下**错误信封不同**

`stats` / `recent-runs` 走 `writeError`（嵌套 `{"error":{"detail":…}}`），
而同文件的 `migration-metrics` 走 `http.Error`（**text/plain**）。
⇒ 各端点独立解包；页面**两个端点独立取**（`Promise.allSettled`），
一个失败不清空另一个。

#### ★ 本轮修的两处

1. **把「陷阱常量」写成注释却没接进判据** ——
   `STATS_MAY_NOT_BE_WIRED` / `STATS_1H_COUNTS_MAY_BE_UNAVAILABLE` /
   `PROBE_RUNS_ONLY_SETTLED` 三个常量 import 进来却没用，
   `vue-tsc` 的 TS6133 直接报出来。
   ⇒ **接进判据**（`&& STATS_MAY_NOT_BE_WIRED && …`、
   `v-if="PROBE_RUNS_ONLY_SETTLED"`），而不是删掉 import：
   后端哪天改了语义，常量为假时界面提示会**自动消失**。
2. i18n 门抓到 `zh-CN sysmon.tokens = 'tokens'`（值 === 键名，判为未翻译）
   ⇒ 改「消耗 token」。
   ★ 这是**同一类问题第二次出现**（上次是 `en-US online.stale = 'stale'`）：
   标签词恰好等于键名时，中英文两侧都要换词。

### 11.58 本轮门禁（第二十五轮，系统监控）

| # | 门 | 结果 |
|---|---|---|
| 1 | css 门自测 | **11/11** |
| 2 | 触控门自测 | **11/11** |
| 3 | i18n 门自测 | **23/23** |
| 4 | `gate:selftest` | **45 条断言全绿** |
| 5 | `vue-tsc -b` | 通过（先抓到 3 处 TS6133 未用 import） |
| 6 | i18n 键集门 | 通过（各 **902 键**，+35；**710 个源码字面量键全部存在**，扫 115 个 `.vue`/`.ts`） |
| 7 | css 媒体查询门 | 通过（**55 文件**，+1） |
| 8 | 触控热区门 | 通过（**52 个 .vue**，+1） |
| 9 | `vitest run` | **962 用例 / 64 文件全绿**，**连跑 10 次全绿**（+52：25 API + 27 视图） |
| 10 | `npm run build` | 通过 |

★ 变异证据（**12 处，全部转红**）：

| 变异 | 结果 |
|---|---|
| failed 口径改成「非 success 即失败」（把 expired 算进去） | 4 failed |
| `statusIsUnaccounted` 改成永不命中 | 2 failed |
| `expired` 不再单独一档色 | 1 failed |
| 截断判定 `>=` 改 `>` | 2 failed |
| `limit` 上界守卫去掉（201 会发出去） | 1 failed |
| `runDurationMs` 不再挡负时长 | 1 failed |
| ★ 三项合计数写死成 999 | 1 failed |
| 「可能未接线」提示恒假 | 1 failed |
| expired 行标注恒假 | 1 failed |
| 「只含已结束运行」说明恒假 | 1 failed |
| 图例恒假 | 1 failed |
| 「四项全 0」提示恒假 | 1 failed |

★ 这次 12 处**每处只红 1~2 条**（除口径那条红 4 条），说明判据是**贴着各自那条
不变量**写的，而不是靠一个大而全的断言兜着。
★ 「四项全 0」与「队列与运行全 0」是**两条独立判据**：
它们都是「全 0」，若合并成一条判断，变异时无法定位是哪条语义被破坏。

---

### 11.59 数据生命周期上移：分区清单 + 体积榜（第二十六轮，admin 档）

| 端点 | 移动端 | 档位 | 抽屉席 |
|---|---|---|---|
| `GET /api/admin/data-lifecycle/partitions` | `/data-lifecycle`（上半） | `admin` | 数据生命周期 |
| `GET /api/admin/data-lifecycle/storage/tables` | 同上（下半） | `admin` | （不单独占席） |

★ 同族其余端点**几乎全是 superAdmin 且多数是写操作**
（`archive` / `archive-batch` / `drop` / `vacuum` / `vacuum-full` / `reindex` /
`promote` / `hot/*` / `blobs/cleanup/execute` / `degradation/control` /
`degradation/recover`）⇒ 本轮**一条都不碰**。
唯一例外是 superAdmin 的只读 `hot/cron/stats`，留待后续。

#### ★★★★ 陷阱一：`total_bytes` **不是整库大小**，是「返回的前 N 张之和」

`queryTableSizes` 只在 **LIMIT 之后的循环里**累加：

```go
for rows.Next() {          // ← 已经 ORDER BY ... LIMIT $1 截断过了
    …
    totalBytes += t.TotalBytes
    out = append(out, t)
}
```

响应里的 `total_bytes` / `total_human` 就是这个 `totalBytes`。
handler 自己的注释写明：

> 占比对 Top-N 求和，截断会让 Top-N 大表榜 + DB 占用同时偏小。

⇒ 显示成「本库共 12.3 GB」是**错的**。页面只能说「上榜 N 张表合计 X」，
并把「不是整库大小」这句挂在榜的**上方**（不能塞进底部小字）。

#### ★★★★ 陷阱二：`percent_of_db` 的分母是**榜内之和**

```go
out[i].PercentOfDB = int(out[i].TotalBytes * 100 / totalBytes)
```

⇒ 这一列恒以「榜内占比」为语义，加起来约 100%，
与「占全库百分比」**毫无关系**。字段名 `percent_of_db` 在骗人。

#### ★★★ 陷阱三：`rows` 是 planner 估计值

SQL 取的是 `COALESCE(s.n_live_tup, 0) AS row_estimate`
（`pg_stat_user_tables`，**需要 analyze 才有意义**，写入后未统计时会明显偏低），
而 JSON 键叫 `rows`。⇒ 页面标「估计行数」。

#### ★★★ 陷阱四：分区父表与分区**可能同时在榜**，体积**重复计入**

`WHERE c.relkind IN ('r','p')` 同时包含普通表与分区父表，
而 `pg_total_relation_size(父表)` **本身已经包含所有子分区**。
⇒ 榜上同时出现 `request_logs`（'p'）与 `request_logs_2026_10`（'r'）时，
两行体积**重叠**，累加会偏大。
⇒ 榜里逐行标出「分区父表」，并提示不可与分区行相加。

#### ★★★ 陷阱五：`row_count = -1` 是后端**明确的「未知」哨兵**

每个分区单独 `SELECT COUNT(*)`；失败时 handler 显式写：

```go
pinfo.RowCount = -1 // Indicate unknown
```

⇒ 渲染成「未知」，**不是**「-1 行」，也不是「0 行」。
★ 这是本专题遇到的**唯一一个后端主动提供的哨兵值** ——
比那些「0 到底是真 0 还是没取到」的端点友好得多，应当优先利用。

#### ★★ 陷阱六：`partitions` 里某张表**整个消失**是查不出来的

handler 遍历固定表清单，某张表状态查询出错就 `slog.Warn` + `continue`：

```go
status, err := h.getPartitionTableStatus(ctx, tableConfig)
if err != nil { slog.Warn(…); continue }
```

⇒ 响应少一张表，客户端**无法区分**「这张查不到」与「配置里就没有它」。
⇒ 页面显示「本次返回 N 张分区表」+ 一句说明缺失不可分辨。

#### ★★ 陷阱七：`archived_count` 把 **columnar** 的也算成「已归档」

```go
if pinfo.IsArchived || pinfo.IsColumnar { status.ArchivedCount++ }
```

⇒ 「已归档 N」≠「归档表里真有 N 个分区」。
且 `IsArchived` 本身是**按分区名是否含 `_archive_` 推断**的
（`containsSubstring(pinfo.PartitionName, "_archive_")`），不是查目录。

★ 另有一条**静默丢行**：分区边界解析失败
（`parsePartitionBounds`，例如 DEFAULT 分区）会 `continue`，
**整个分区从清单里消失**（不只是缺日期）。
handler 注释自陈：「少列一个分区就等于让管理员以为该分区不存在」。

★ 关于日期：这里 `start_date` / `end_date` 是**非指针 `time.Time`**，
  看起来会有「零值时间 `0001-01-01`」的风险 —— 但**实测不存在**：
  解析失败的那一行会先 `continue`，所以凡是出现在 `partitions[]` 里的
  分区都带真实日期。⇒ 客户端不必写零值兜底。
  ★ 与 §11.57 的 `finished_at` 同款：**推断出来的坑必须追到代码才能写进契约**，
    否则文档里会多一条并不存在的坑，后来的人会照着它加无用的防御。

#### ★★ 一个**又犯了**的错：常量 import 却不接进判据

§11.57 刚因为这个被 `vue-tsc` TS6133 抓到并定下「接进判据而不是删 import」的修法，
§11.59 **又犯了一次**（5 个常量）。

⇒ 说明「知道修法」不等于「下次会照做」。
  把规则固化成一个**可执行的门**更可靠：给「导入了但未在模板/脚本里出现」
  的标识符加一条检查。本轮已全部接进 `v-if` 条件
  （`v-if="STORAGE_TOTAL_IS_TOPN_SUM"` 等），后端改语义时界面提示会自动消失。

### 11.60 本轮门禁（第二十六轮，数据生命周期）

| # | 门 | 结果 |
|---|---|---|
| 1 | css 门自测 | **11/11** |
| 2 | 触控门自测 | **11/11** |
| 3 | i18n 门自测 | **23/23** |
| 4 | `gate:selftest` | **45 条断言全绿** |
| 5 | `vue-tsc -b` | 通过（先抓到 5 处 TS6133 未用 import） |
| 6 | i18n 键集门 | 通过（各 **928 键**，+26；**735 个源码字面量键全部存在**，扫 117 个 `.vue`/`.ts`） |
| 7 | css 媒体查询门 | 通过（**56 文件**，+1） |
| 8 | 触控热区门 | 通过（**53 个 .vue**，+1） |
| 9 | `vitest run` | **1005 用例 / 66 文件**（+43：20 API + 23 视图）——★ 见下方 flaky 记录，**不能**写成「10 次全绿」 |
| 10 | `npm run build` | 通过 |

★ i18n 门又抓到一次「值 === 键名」：`en-US lifecycle.unknown = 'unknown'` ⇒ 改 `not measured`。
  ★ 这已是**第三次**（`online.stale` / `sysmon.tokens` / `lifecycle.unknown`）。
  ⇒ 规律固化为提交前检查项：英文里「标签词恰好等于键名」是最容易踩的一类，
    因为直觉上 `unknown` / `stale` / `tokens` 看着就是对的译文。

★ 变异证据（**10 处，全部转红**）：

| 变异 | 结果 |
|---|---|
| 截断判定 `>=` 改 `>` | 3 failed |
| ★ 「未知」哨兵改成判 `=== 0`（把真空值当成未知） | 3 failed |
| archived 口径去掉 columnar 那一支 | 1 failed |
| `limit` 上界守卫去掉 | 1 failed |
| 「不是整库大小」提示恒假 | 1 failed |
| 「分区父表」徽标恒假 | 1 failed |
| 去掉「估计行数」标注 | 8 failed |
| archived 口径说明恒假 | 1 failed |
| ★ `-1` 不再当未知（直接打印 -1） | 1 failed |
| 「上榜 N 张合计」写死 0 | 1 failed |

★ 第 2 条与第 9 条是一对：哨兵方向搞反（把 0 当未知）和哨兵被忽略（把 -1 打出来）
  都会让「未知」这层信息彻底消失，而两者的界面表现完全不同 ——
  ⇒ 判据必须**双向**写：`0 ⇒ 显示 0`、`-1 ⇒ 显示 未知`。

★ ★★ **本轮观测到一次未捕获的 flaky（必须如实记，不得抹掉）**

十连跑的第 4 次出现 `Tests 2 failed | 1003 passed (1005)`。
**失败用例名没有捕获到** —— 那一轮的后台循环只保留了汇总行
（`grep -E '^ +Tests +'`），没有把完整输出落盘。这本身是个流程缺陷。

随后为定位做了两组补充跑：

| 跑法 | 次数 | 失败 |
|---|---|---|
| 全量 `vitest run`（第一组） | 4 | **1**（run#4，2 条） |
| 全量 `vitest run`（第二组，带失败落盘） | 6 | 0 |
| 全量 `vitest run`（第三组，带失败落盘） | 12 | 0 |
| 本轮新增的 4 个视图 spec **隔离**跑 | 20 | 0 |
| **合计全量** | **22** | **1（约 4.5%）** |

⇒ **已排除**：本轮新增的四个视图 spec（隔离 20 次零失败）、
  本轮新增的两个 API spec（随全量跑，零失败）。
⇒ **未定位**：失败发生在哪两个用例、哪一层。可能是既有 spec，
  也可能是并发会话新增的 `src/styles/safeArea.spec.ts`（归他们），
  也可能是 vitest 多 worker 下的调度敏感。

⇒ **提交纪律**：这一批**不写**「连跑 10 次全绿」。
  门禁表里保留失败记录，并在此处列出复现所需的正确跑法：

```bash
# 只看汇总行会丢失败名 ⇒ 失败时必须落盘
for i in $(seq 1 12); do
  OUT=$(npx vitest run 2>&1)
  echo "$OUT" | grep -qE '^ +Tests +.*failed' && echo "$OUT" > "/tmp/vitest-fail/run$i.txt"
done
```

★ **教训**：十连跑的价值不在「跑了几次」，而在**失败时能不能说出是哪几条**。
  只留汇总行的循环，等于在最需要证据的那一刻把证据丢了 ——
  这与「报告里写「已验证」但没量过」是同一族问题。
  ⇒ **门禁脚本必须默认落盘完整输出**，而不是在出问题时才想起来加。

---

### 11.61 flaky 定位（第二十七轮，订正 §11.60 的记录）

§11.60 记了那次未捕获的 flaky。本轮专门花一轮去定位它。**结论：仍未定位**，
但把排除范围收窄了很多，并把「下次必然留证」这件事做成了可执行的门。

#### 复现尝试与真实账目

| 跑法 | 次数 | 失败 |
|---|---|---|
| 原始十连跑（只留汇总行） | 4 | **1**（run#4，2 条，无用例名） |
| 全量（补跑，带失败落盘） | 6 | 0 |
| 全量（补跑，带失败落盘） | 12 | 0 |
| 全量压测（带失败落盘，40 次） | 34 + 6 | 0 / **6 次失败是我自己的临时探针造成的污染**（见下） |
| 本轮新增 4 个视图 spec **隔离**跑 | 30 | 0 |
| `continuousList.spec.ts` **隔离**跑（与全量压测并发） | 30 | 0 |
| **全量无污染合计** | **约 56** | **1（约 1.8%）** |

★ 压测的 run#35/36 记到的 2 条失败是 `故意失败 A / B` ——
  那是我为了验证新写的连跑器而临时放的探针 spec（见 §11.62），
  **不是真 flaky**。⇒ 「抓到失败」与「抓到真失败」要分开，
  压测跑到一半改了被测集合，结果就得这么标注。

#### ★ 两个主要嫌疑，**实测排除**

**嫌疑一：`continuousList.spec.ts` 的墙钟截止时间轮询。**
它是全仓**唯一**用 `Date.now()` + 截止时间轮询的 spec：

```ts
function waitFor(predicate: () => boolean, timeoutMs = 2000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  const tick = () => { …; if (Date.now() > deadline) return reject(new Error('waitFor timeout'))
                       setTimeout(tick, 10) }
```

11 处调用全用默认 2000ms。★ 但实测每条用例只花 **13~39ms**
（总 157ms），截止时间余量 **13~150 倍**；
隔离跑 30 次（含与全量压测并发的重负载）**零失败**。
⇒ 要触发它需要某个 worker 被饿过 2s，这在当前负载下不成立。
★ **不据此改那个文件** —— 拿一个自己都量出「余量 50 倍」的假设去动既有代码，
  是用推测代替证据。

**嫌疑二：`pullToRefresh.spec.ts`（第二慢，476ms）。**
实测该文件**没有任何** `Date.now` / `setTimeout` / `setInterval` /
`useFakeTimers` / `advanceTimers` / `requestAnimationFrame`；
205ms 与 218ms 来自被测状态机自身的等待。
⇒ 同样排除。

#### ★ 已排除的范围（这部分是有力的）

- **本轮新增的 4 个视图 spec**：隔离 30 次零失败。
- **本轮/上轮新增的 API spec**：随每一次全量跑（≥56 次）零失败。
- ⇒ flaky **不在本专题新增的代码里**。可能性按概率排序：
  ①既有 spec（66 个文件里其余的）；②并发会话新增的
  `src/styles/safeArea.spec.ts`（归他们，我不该动）；③vitest 多 worker 调度敏感。

★ 顺带一个诚实的观察：本专题把用例数从 733 推到 1005、spec 文件从 53 推到 66，
  worker 争用随之上升 ⇒ **可能放大**了某个既有的计时敏感用例的触发概率。
  但这一条**同样是推断**，没有证据，不写成结论。

### 11.62 门禁：连跑器（失败必落盘）

新增 `web-mobile/scripts/run-vitest-stability.mjs`（`npm run test:stability`）。

★ **要解决的就是本次的流程缺陷**：手工循环只 `grep '^ +Tests +'`，
失败用例名当场丢失。⇒ 门禁脚本**默认落盘完整输出**。

行为（**已用故意失败的探针 spec 双向验证过**，不是「应该能跑」）：

| 场景 | 实测结果 |
|---|---|
| 2 次全绿 | 每次打印 `Tests` 汇总行；末尾「总次数 2 · 失败次数 0 · 快照 0 份」；**exit 0** |
| 故意失败 2 条 | 打印汇总 `2 failed \| 1006 passed (1008)` + **快照路径**；**逐条打印 `× 用例名` 与 `FAIL 所在文件`**；**exit 1** |
| `--runs` 非法 | exit 2 + 明确报错 |
| `--run=<file>` | 只跑该文件 |

★ 故意不做「重跑到绿为止」：重跑会把**第一次**的失败掩盖掉，
  而这里要的正是第一次的结果。
★ 快照目录 `test-stability-snapshots/` 已进 `.gitignore`
  （每次失败内容都不同，不该进版本库）。
★ 全部跑绿时，末尾会明确写「本次未复现 —— 这不是『已修复』，只是没抓到」，
  避免把「没复现」读成「已解决」。

---

### 11.63 路由优化器上移：准确率 + 激活参数 + 5 分钟明细（第二十八轮，admin 档）

新增 `src/api/routingOpt.ts`（4 端点）+ `src/views/RoutingOptView.vue` +
路由 `/routing-opt` + 抽屉席「路由优化器」。
这一族答的是「**优化器自己调得准不准、现在用的是哪套参数**」，
与已上移的 `/overrides`（规则是什么）、`/routing-audit`（谁改的）、
`/funnel`（请求漏斗）、`/matrix`（热力矩阵）、`/model-task-index`（5 分钟桶表现）
**互不重叠** —— 这一族是**效果与参数**面。

| 端点 | 移动端 | 档位 | 抽屉席 |
|---|---|---|---|
| `GET /api/admin/routing-opt/stats` | `/routing-opt`（总体准确率） | `admin` | 路由优化器 |
| `GET /api/admin/routing-opt/accuracy` | 同上（小时 × 任务桶） | `admin` | 同上 |
| `GET /api/admin/routing-opt/parameters` | 同上（激活参数） | `admin` | 同上 |
| `GET /api/admin/routing-opt/metrics` | 同上（5 分钟明细） | `admin` | 同上 |

累计（**当场实测**：`views/*.vue` 去 spec、`api/*.ts` 去 `*.spec.ts`/`*.test.ts`、
抽屉席按 `appNav.ts` 实际条目）：
**36 视图 / 35 API 模块 / 32 抽屉席**（**8 席 superAdmin** 档）。
★ 沿用 §11.52 的告警：这个数是**目录现状**，并发会话仍在加文件
（例如此前新增的 `transport.ts`），引用前必须当场实测。

#### 七个坑（逐条实读源码，不是推断）

1. ★★★★ **准确率是「1 条人工标注算 2 条」的加权平均，不是合并准确率。**
   `routingOptWeightedAccuracy`：`denom = autoTotal + 2*humanTotal`、
   `num = autoCorrect + 2*humanCorrect`、`accuracy = num/denom`
   （`num` 另有 `if num > denom { num = denom }` 防御性夹取）。
   ⇒ 用户按 `(correct+correct)/(total+total)` 算是**另一个数**。
   ★ 更要紧：响应**只给样本量、不给命中数**（`auto_samples`/`human_samples` 是 COUNT，
   没有 correct 字段）⇒ **客户端无法验证**这个加权值，只能照抄并说明口径。
   页面三条说明常驻：加权口径 / 两种「正确」定义 / 无法验证。

2. ★★★★ **auto 与 human 的「正确」是两种定义。**
   `auto` = `SUM(CASE WHEN success THEN 1 ELSE 0 END)`（**请求成功**）；
   `human` = `predicted_provider = correct_provider`（**命中人工真值**）。
   加权平均把「成功」和「命中人工真值」揉成一个数，两者不能互相替代解读。

3. ★★★★ **量纲第 5 处：`overall_accuracy` / `accuracy` 是 0..1 比率**，不是百分数
   ⇒ 走 `formatRoutingOptAccuracy`（×100），刻意与 `formatSuccessRatePct`（0..100）、
   `formatModelTaskRatePct`（0..1，task-index）**不同名**。

4. ★★★ `accuracy` 每个桶 **恒有样本** ⇒ 桶里 `accuracy: 0` 是**真的 0%**。
   ★★ 这是本轮**特意追到 SQL 才排除**的一处「疑似缺陷」：
   handler 写 `b.Accuracy, _ = routingOptWeightedAccuracy(...)`，把 `hasData` 丢掉了 ——
   按 §11.20 的「三态必须互斥」直觉（`degraded`/`empty` 那样），这看着像缺陷。
   **但** SQL 是 `… GROUP BY date_trunc('hour',created_at), task_type`，**没有**
   `generate_series`，而 `GROUP BY` 只产出非空组 ⇒ `COUNT(*) ≥ 1` ⇒ `denom ≥ 1 > 0`
   ⇒ `hasData` **恒为 true** ⇒ 丢弃它**无害**。
   ⇒ 既不该去「修」，也**不该在文档里写成坑**。
   ★ 对照：`stats` 整窗无样本时 `hasData=false` 是**真会发生的**，后端也**正确处理**了
   （回落 `persisted_state` 或 `none`）。两处不可混谈。

5. ★★★ `accuracy_source` 有**三个语义完全不同的来源**，共用 `overall_accuracy` 一个字段，
   不看它就分不出来：
   `weighted_feedback`（窗口内实时算出）/ `persisted_state`（**旧值回落**，必须标出）/
   `none`（既无反馈样本也无持久化状态）。未知来源要**如实显示来源名**，不得当实时。

6. ★★ `hours` 是**静默回落 + 静默 clamp，永不报错**（`parseRoutingOptHours`）：
   非数字 / <1 → 24；>720 → 720。
   ★ 而 `stats` 的窗口是**写死的常量 24，根本没有参数**
   ⇒ 两个端点的窗口**不能共用一个控件**；页面因此分成两组 chip。
   ★★ 视图里 `ROUTING_OPT_STATS_WINDOW_HOURS` 不当摆设：它当**基准**用 ——
     响应 `window_hours` 与它不等时**告警**（后端改了常量而文档没跟上），
     而不是默默照抄响应。（这一处是被 `vue-tsc` TS6133 逼出来的，
     但**接进判据**而不是删 import，比删掉多一层保护。）

7. ★★★ `metrics` 的 `LIMIT 2000` 在长窗口下**必然命中**，且 `ORDER BY time_bucket DESC`
   ⇒ **丢的是最旧的数据**。
   ★ SQL 注释自陈「30 天 × 288 桶/天 × 维度组合，正常远小于此」—— 这条推理的**算术与自己的
   LIMIT 矛盾**：5 分钟桶 ⇒ 一天 288 个；30 天（`hours=720`）⇒ **8640 个桶，仅时间桶就超 2000**。
   好在**后端自带** `resp.Truncated = len(rows) >= 2000`
   ⇒ 不要自己猜。这与 `availability-timeline`（写死 500 无标记）、
   `cache-state`（ScanKeys 4096 无标记）**不同**。
   ⇒ 页面两条：`truncated=true` 时说「已达 2000 行上限、丢最旧的」；
     未命中但窗口够长（`hours × 12 ≥ 2000`，即 ≥168h）时给**预估**提示。

★ 另两条：`parameters` 没有激活版本时返回 **404** `No active optimization state`
  —— 那是「还没配置」，**不是错误**，独立渲染。
★ `human_annotations_used` 与 `human_samples` **恒等**（都取 `humanTotal`）——
  不是另一项统计，页面上只展示后者。
★ 错误信封全族走 `http.Error`（**text/plain**），与 `writeError`（嵌套 JSON）不同族；
  `routingOptPool() == nil` ⇒ **503** `Database not available`。

8. ★★★★★ **四个端点里只有 `metrics` 读的是「物化聚合表」，另外三个都是实时读。**
   ★★★ 这条**不是从 handler 看出来的** —— handler 只说「`FROM routing_optimization_metrics`」，
   追到写入方 `bg/routing_metrics_aggregator.go` 才知道那张表是后台 sweep 从
   `routing_feedback_log` 滚动写出来的（先 `DELETE … WHERE time_bucket >= $1`
   再整窗 `INSERT`，事务级 advisory lock 串行化，避免同桶重复行让下游 `SUM` 双倍计数）。
   ⇒ 后果：**sweep 没跑或落后时，`metrics` 会空/过期，而 `stats`/`accuracy` 却有数**
     ⇒ 四个面板之间「对不上」**不是 bug**，是物化延迟。页面据此标注了 metrics 面板的来源。
   ⇒ 同一处追出另外三件事：
   - `accuracy_rate = successful::float / NULLIF(total,0)` ⇒ **0..1 确认**，
     但它量的是**成功率**，名字却叫 accuracy ⇒ 这是本族**第三个** accuracy 口径
     （另两个见坑 2）；
   - `human_accuracy_rate` = `(successful + 2*human_agree)/(total + 2*human_count)`，
     且 `human_count = 0` 时写的是 **NULL**（列 CHECK 允许 NULL），**不是 0、不是 1.0**；
   - p50/p95/p99 与 `avg_latency_ms` 都是 `::int` ⇒ **整数毫秒**。

#### ⚠️ 由坑 8 引出的一个**本轮自造的真缺陷**（已实修）

聚合 SQL 用的是 `GROUPING SETS ((), (task_type), (predicted_provider))`
⇒ **同一批数据会同时产出四类行**，而不是「按 task × provider 交叉」：

| 类 | `task_type` | `predicted_provider` | 含义 |
|---|---|---|---|
| `task_provider` | 有 | 有 | 单个 (任务,供应商) 组合的分组行 |
| `task` | 有 | **缺** | 该任务**跨供应商**的汇总行 |
| `provider` | **缺** | 有 | 该供应商**跨任务**的汇总行 |
| `global` | 缺 | 缺 | 整窗汇总行 |

★ 我第一版写的 `metricsRowDimLabel` 只分「有维度 / 没维度」两类
⇒ 把**三类汇总行中的两类当成明细行**，且供应商汇总行会被渲染成
「全部任务 × openai」—— 用户会把它读成一条预测。
现已改为 `metricsRowDim → 'task_provider'|'task'|'provider'|'global'`
＋ `metricsRowIsAggregate`，三类汇总行统一标「汇总行」并用 `muted` 色。
API spec 里那条 `('chat', undefined) → 'global'` 的**旧断言本身就是这个 bug**
——它是先红的。

★★ 一个容易跟着写错的细节：`''` **不是**维度缺失。
`*string` + `omitempty` 只在 NULL 时丢键；存在但为空的维度会带着 `""` 出来
⇒ 仍算「这一维有值」。（写断言时我一度把它当缺失，被自己的实现打回。）

#### 范围外（主动划线，与前几批同口径）

同族写操作一条不碰：`proposals/{approve,reject}`、`probe/cache-rebuild`；
superAdmin 兄弟端点 `hot/cron/stats` 也不碰。

#### 量纲累计（五处，函数一律不同名）

| 函数 | 量纲 | 所属 |
|---|---|---|
| `formatSuccessRatePct` | 0..**100** | probeTimelineCache |
| `formatModelTaskRatePct` | 0..**1** | modelTaskIndex |
| `formatRoutingOptAccuracy` | 0..**1** | routingOpt（本轮） |

#### 本轮门禁

- **变异验证 9/9 + 2/2 有牙**（共 11 条）：
  量纲漏乘 100（红 6 条）/ 来源判别错写 / p95 空指针回落成 0ms /
  显示封顶 50→1000 / 删 `truncated` 告警 / 404 不再当「未配置」/
  accuracy 失败连带清空 stats / 窗口说成可调 / 删窗口失配告警 /
  四类行退回二分类 / 汇总行标签恒不显示。
  全部红在**具名断言**上（非收集失败），还原后 **md5 与基线一致**、复跑全绿。
- 全量 **10 连跑全绿，1087 用例**（连跑器落盘，**0 份失败快照**）。
  其中本轮新增：`routingOpt.test.ts` + `RoutingOptView.spec.ts`；
  并给 `dynamicKeys.spec.ts` 加了第 6 处动态前缀（`routingOpt.dim_`）。
- `npm run build` **rc=0**（`BUILD_RC` 直接从命令取，未过管道）；
  三门全过（css-media 57 文件 / touch-target 54 个 `.vue` / i18n parity **978 键**、
  源码字面量键 780 个）；`vue-tsc -b` 无 TS6133。
- 累计：**36 视图 / 35 API 模块 / 32 抽屉席（8 席 superAdmin）**。

★★ **本轮两次「量具本身有问题」的现场记录**（都属于「结论长得像通过」那一类）：

- **「`vue-tsc --noEmit` rc=0」是假读数。** 写成
  `npx vue-tsc --noEmit … | tail -20; echo rc=$?`，`$?` 取的是 **`tail` 的退出码**。
  紧接着 `npm run build`（内含 `vue-tsc -b`）当场报出 2 条 TS6133。
  ⇒ 结论：**rc 必须从被测命令直接取**（`cmd > log 2>&1; echo $?`，
  或用 `PIPESTATUS`），管道后的 `$?` 一律不作数。
  ★ 另外 `--noEmit` 与构建用的 `-b`（project references）**不是同一模式**，
  两者结论不可互相代替。
- **一处变异转红了，但转红的原因不对。** 原 M7 把 `Promise.allSettled` 改成裸数组，
  vitest 报的是 `no tests`（**收集失败**），不是断言红 —— 按四类归因这是「②前提失效」，
  不能算「判据抓到了」。改成合法且可观测的注入
  （在 accuracy 的 reject 分支里清空 `stats`）后，红在**具名断言**
  `★★ accuracy 失败 ⇒ 不清空 metrics` 上，才算数。
  ★ 中途还遇到一次**等价变异**：把清理放进 stats 的 reject 分支**居然没转红** ——
    因为 `p/a/m` 三个分支在其后执行，会把值又写回去。
    等价变异**不能**用来给判据背书，也不能用来指控判据无牙。
- ★ 模板属性里写 `v-if="false" /* MARKER */"` 会让 SFC **收集失败**；
  变异标记要放进**表达式内部**（`&& 'MARKER' === ''`）才安全。
- ★ 还有一次「标记没落盘」：变异文本里**压根没写标记**，自检正确地拦下了
  （G2 报「标记没落盘」而不是假装验证过）。
  ⇒ 自检脚本必须真的检查**替换后的文件内容**，不能只检查替换字符串本身。

### 11.64 flaky 复查（第二十八轮，未复现）

改完代码后（GROUPING SETS 那处缺陷修完后）重跑 10 次全量：**10/10 全绿，1087 用例**。
★ 这**不是**「已修复」，只是**没抓到**（连跑器全绿时的结尾语就是这个意思）。
累计无污染全量跑 ≥76 次、失败仍为 1 次（≈1.3%），失败用例名**仍未捕获**。

---

### 11.65 待处理响应上移：有没有卡住的请求（第二十九轮，admin 档）

新增 `src/api/pendingResponses.ts` + `src/views/PendingResponsesView.vue`
+ 路由 `/pending-responses` + 抽屉席「待处理响应」。
答的是一个很具体的运维问题：**网关现在有没有卡住的请求**。
与已上移的 `/sessions-online`、`/routing-opt`、`/data-lifecycle` 互不重叠。

| 端点 | 移动端 | 档位 | 抽屉席 |
|---|---|---|---|
| `GET /api/admin/pending-responses` | `/pending-responses` | `admin` | 待处理响应 |
| `GET /api/admin/pending-responses/stats` | 同上 | `admin` | 同上 |
| `GET /api/admin/pending-responses/{sessionID}` | 同上（详情） | `admin` | 同上 |

★ `/stats` 注册在 `.../pending-responses/` 子路由**之前**
（`admin/handler.go:1364` vs `:1366`）——顺序错的话 `/stats` 会被子路由
当成 sessionID 吞掉。**这个顺序是有意为之，不是巧合。**

累计（**当场实测**）：**37 视图 / 36 API 模块 / 33 抽屉席**（**8 席 superAdmin** 档）。

#### 九个坑（逐条实读源码）

1. ★★★★ **`status` 过滤是假的。** 底层 `pending.Store.ListStaleInProgress`
   在 `pending/pending.go:355` 有一句
   `if r == nil || r.Status != StatusInProgress { continue }`
   ⇒ 它**只**返回 `in_progress`；而 adapter 里 `Status: "in_progress"`
   （`admin/pending_handlers.go:95`）是**写死的字面量**，不是从条目读的。
   ⇒ `?status=completed` / `failed` / **任意垃圾值** 都**永远返回空数组且不报错**。
   ⇒ 页面因此**不提供**这些筛选项 —— 提供了用户就会得出
     「没有已完成的挂起响应」这种完全错误的结论。
   ★ 判据特意**不**写成「文本里不能出现『已完成』」：说明文案**必须**引用这两个词
     才能解释为什么不给筛选项，那样写会把好文案判红。
     判据落在**控件**上：无 `select`/`radio`/`checkbox`，且 chip 只有 limit 三档。

2. ★★★★ **列表的 `provider_id` / `is_stream` / `bytes_buffered` 恒为 `0`/`false`/`0`。**
   `StaleEntry`（`pending/pending.go:283-288`）**只有四个字段**
   （`SessionID/RequestID/CreatedAt/TenantID`）—— 根本没有这三个可填；
   而 `listEntry`（`:52-63`）给它们的 JSON tag **没有 `omitempty`**
   ⇒ 响应里**一定会出现** `"provider_id":0, "is_stream":false, "bytes_buffered":0`。
   ★★ **`provider_id: 0` 不是「供应商 0」，是「列表端点不返回这个」。**
   只有 `GET /{sessionID}` 详情（`:236-248`）才真的填。
   ⇒ 页面**不显示**这三列，改为一句说明 + 进详情看真值。
   ★ 顺带一个容易跟着写错的区别：`completed_at` 有 `omitempty`
     ⇒ 它是「**键缺失**」；那三个是「**恒 0**」。两种都叫「没值」，但成因不同，
     所以判据里把 `completed_at` 排除在恒假清单之外。

3. ★★★ **时间是 Unix 秒（int64），不是 ISO。**
   `created_at` / `completed_at` / `oldest_created_at` 全是秒。
   ★ 与本仓库其它端点（ISO 字符串）不同；直接丢给 `relativeTime()` 会
     `Date.parse("1791…")` 失败后**原样回显那个数字**。
   ⇒ 一律先过 `pendingUnixToIso`。

4. ★★★ **`stats.by_status` 永远只有一个键** `in_progress`，且恒等于 `total`
   （`:331-336` 写死 `byStatus["in_progress"]++`）⇒ 没有信息量，页面不展示它。

5. ★★★ **`stats.oldest_created_at` 在没有条目时是 `0`**（循环不进，初值原样输出）
   ⇒ 0 是「没有条目」，**不是 1970 年**。且口径是「in_progress 里最老的那条」。

6. ★★ `limit` clamp [1,500]、**非数字/≤0 回落 50、永不报错**（`pageBounds:124-141`）；
   `offset` 越界**静默返空**（`offset>len ⇒ offset=len`，`end` 也被 clamp 到 len
   ⇒ 切出空片，不会 panic）。

   ★★ **顺带订正一个「很容易推断错」的点**：`ListStaleInProgress(…, 1000)` 里的
   `1000` **不是条数上限**，它是 Redis `SCAN` 的 `COUNT` **提示**；
   该函数循环到 `cursor==0` 为止（`pending/pending.go:325-369`），
   函数自己的注释也写着「COUNT is a hint, not a guarantee」。
   ⇒ 我第一版把它写成「列表只含 1000 条」——**追到循环体才发现是错的**，
     幸好没写进文档。
     ⇒ 这条是 §11.63「推断必须追到代码确认」的又一次实例。

7. ★★ `TenantID` 是 `json:"-"` ⇒ **响应不返回租户**，但过滤确实做了
   （`:184`：superAdmin 全量、tenant_admin 只看本租户）
   ⇒ 对 tenant_admin「列表变少」是**正常的**，页面照实说明。

8. ★★ 详情 404 与「跨租户不可见」**共用** `PENDING_NOT_FOUND`
   （`:228-232`：`!found || 租户不符` 一起返 404）
   ⇒ 这是**刻意不泄漏存在性**，页面渲染成「查不到」，**不是**红色错误。

9. ★ 错误信封走 `writeErrorJSON`（`:391-399`）⇒ **嵌套 JSON**
   `{"error":{"message":…,"code":…}}`，与 routing-opt 那一族的
   `http.Error`（text/plain）**不同族**。
   503 `PENDING_STORE_UNAVAILABLE` / 503 `PENDING_STORE_ERROR` /
   404 `PENDING_NOT_FOUND` / 405 `METHOD_NOT_ALLOWED`。

★ **范围外**：写操作 `DELETE /api/admin/pending-responses/{sessionID}`
  （手动清理挂起条目）本页一条不碰。
★ **注释与实现矛盾一处（只记录，不改后端）**：`handlePendingDelete` 的注释写
  「Idempotent — deleting a missing entry is not an error」，
  但代码里 `!found` 直接 **404** ⇒ **并非幂等**。
  不写这一页是因为本页不碰 DELETE，写了会变成无出处的断言。

#### 本轮门禁

- **变异验证 8/8 有牙**：status 不再拦 / 恒假字段清单清空 / Unix 秒漏乘 1000 /
  0 哨兵不再当「没有」/ 年龄阈值抬高 / 详情 404 当普通错误 / limit 上界不拦 /
  形状不符静默返空。**全部红在具名断言上**（非收集失败），还原后逐字节一致、复跑全绿。
- 全量 **10 连跑全绿，1157 用例**（连跑器落盘，**0 份失败快照**）。
- `npm run build` **rc=0**（`BUILD_RC` 直接从命令取）；三门全过
  （css-media 58 文件 / touch-target 55 个 `.vue` / i18n parity **1017 键**、
  源码字面量键 816 个）；`vue-tsc -b` 无 TS6133。
- `dynamicKeys.spec.ts` 动态前缀清单增至 **7 处**（新增 `pending.band_`，
  取值从 `PENDING_AGE_BANDS` 常量取，不手抄）。

---

### 11.66 请求侧异常上移：上游在拒绝我们的什么请求（第三十轮，superAdmin 档）

新增 `src/api/requestAnomalies.ts` + `src/views/RequestAnomaliesView.vue`
+ 路由 `/request-anomalies` + 抽屉席「请求侧异常」。
它答的是「某个供应商开始拒绝我们的某个请求参数」这类问题，
与 `/node-audit`（节点健康）、`/pending-responses`（卡住的请求）、
`response_format_anomalies`（另一族，PG 表）互不重叠。

| 端点 | 移动端 | 档位 | 抽屉席 |
|---|---|---|---|
| `GET /api/admin/request-anomalies` | `/request-anomalies` | **`super_admin`** | 请求侧异常 |
| `GET /api/admin/request-anomalies/count` | 同上（徽标计数） | **`super_admin`** | 同上 |

★ 本批是**第一条 superAdmin 档的抽屉席**（前面几批都是 admin 档）
⇒ 路由与 `appNav` 都带 `requiresRole`，且 **`AppDrawer.spec.ts` 的白名单
必须同步**（白名单式断言会在漏改时主动变红）。

累计（**当场实测**）：**38 视图 / 37 API 模块 / 34 抽屉席（9 席 superAdmin）**。

#### 九个坑（逐条实读源码）

1. ★★★★ **同一个筛选面板里，四个字段的大小写敏感度不一致。**
   `internal/reqprobe/types.go:117-134` 的 `Filter.matches`：

   | 字段 | 判定 | 敏感度 |
   |---|---|---|
   | `day` | `r.Day != f.Day` | **敏感**精确 |
   | `provider` | `!strings.EqualFold(...)` | 不敏感 |
   | `model` | `!EqualFold(ClientModel) && !EqualFold(OutboundModel)` | 不敏感 |
   | `trigger` | `string(r.Trigger) != f.Trigger` | **敏感**精确 |

   ⇒ `?trigger=PARAM_REJECTED` **静默返回空数组且不报错**，
     而 `?provider=OpenAI` 却能命中 `openai`。
   ⇒ 前端**不对 trigger 做大小写归一**：`toLowerCase()` 会把「用户填了大写」
     悄悄改成「按小写筛」，界面上看起来像后端认了大写。
   ⇒ 页面只给三个**后端常量**的小写按钮，不给 trigger 文本框。

2. ★★★★ **`model` 筛的是「客户端模型 **或** 出站模型」的 OR。**
   用户按「我请求的模型」筛，出来的行 `outbound_model` 可能完全不同
   （网关做了模型重写）⇒ 两个模型**必须都显示**，且不同时要明说，
   否则用户会以为筛错了。

3. ★★★★ **同一类错误每天一行。** `Fingerprint`（`types.go:160-176`）把 `Day`
   算进 sha1 ⇒ 同一问题**每天**产生一条新记录。
   ⇒ `occurrences` 只是**今天**这一行的次数（注释自陈），
     「这个错误总共出现过几次」必须自己跨天累加。页面照实说明。

4. ★★★ `day` 是 `FirstSeen` 所在**本地日期**（`YYYY-MM-DD`），
   而 `Today()` 用 `t.Local()` ⇒ **网关进程的时区**，不是浏览器时区。
   ⇒ 跨时区时「今日新增」可能与用户以为的今天不是同一天。

5. ★★★ **`Counts` 只统计未解决的**（`redis.go:167-170`：`if rec.Resolved { continue }`）
   ⇒ `unresolved` 不含已解决，`new_today` 是「今天首次出现**且未解决**」。
   ★ 而列表**默认返回全部**（含已解决），除非带 `unresolved_only=true`
   ⇒ 徽标数字与列表条数**天然对不上**。这不是 bug，但**必须说明**，
     否则用户会以为「计数错了」。

6. ★★★ `trigger` **只有三个**取值（`types.go:48-61`）：
   `param_rejected` / `mode_mismatch` / `upstream_error`。
   其中 `mode_mismatch` 不参与参数学习（协议切换是每请求的廉价回退）。

7. ★★ `limit` clamp [1,500]、非数字回落 50（`queryInt(r,"limit",50)`），
   `offset<0` → 0，**永不报错**。
   ★ 数字与 `pending-responses` 的 `pageBounds` **完全一样**（50/500），
     但那是**两个不同实现**（分别在 `admin/request_anomalies.go:41-51`
     与 `admin/pending_handlers.go:124-141`）⇒ **不可当成同一份契约**。
   ★ `loadAll` 是 `HGETALL` 全量读回内存过滤（`redis.go:119`），
     **没有条数上限**（retention 30 天）⇒ 列表**不会被静默截断**。

8. ★★ 多数可选字段**键可能整个不存在**（`omitempty`）：
   `client_model` / `outbound_model` / `param` / `suggest_mode` /
   `error_kind` / `error_sample` / `last_request_id` / `resolution_notes`，
   以及 `resolved_at`（`*time.Time` + omitempty）。
   ★ `param` 是**逗号连接的多个**参数名（一个请求可能被拒多个参数），
     不是单值 ⇒ 页面按逗号拆成逐个 tag。

9. ★ 错误信封走 `writeError`（`admin/handler.go:1494-1498`）⇒
   `{"error":{"detail":…}}`，键是 **`detail`** 不是 `message`。
   ★ 至此本仓库已见**三个信封族**：

   | 端点族 | 信封 |
   |---|---|
   | `pending-responses` | `{"error":{"message":…,"code":…}}`（`writeErrorJSON`） |
   | `routing-opt` | **text/plain**（`http.Error`） |
   | `request-anomalies` | `{"error":{"detail":…}}`（`writeError`） |

   `api/client.ts` 的 `errorMessage`（`:100-117`）三种都兜得住 ——
   **错误文案是唯一允许共用解包器的例外**，响应本体仍各端点独立解包。

★ **范围外**：写操作 `POST /{id}/resolve` 与 `POST /batch-resolve` 不碰。

#### 本轮门禁

- **变异验证 8/8 有牙**：trigger 被 `toLowerCase` 归一 / `unresolved_only=false`
  也发 / `param` 不按逗号拆 / 模型重写提示恒显 / 抽屉席丢掉 `requiresRole` /
  形状不符静默返空 / limit 上界不拦 / 删掉「只统计未解决」说明。
  **全部红在具名断言上**，还原后逐字节一致、复跑全绿。
- ★ 其中 A5（抽屉席丢掉 `requiresRole`）红在 `AppDrawer.spec.ts` 的
  「tenant_admin 少掉的**只有** superAdmin 档那些」——
  白名单式断言**真的在守**，不是装饰。
- 全量 **10 连跑全绿，1219 用例**（连跑器落盘，**0 份失败快照**）。
- `npm run build` **rc=0**（`BUILD_RC` 直接从命令取）；三门全过
  （css-media 59 文件 / touch-target 56 个 `.vue`）；`vue-tsc -b` 无 TS6133。
- i18n parity **1056 键**、源码字面量键 851 个；`dynamicKeys.spec.ts`
  动态前缀增至 **8 处**（新增 `anomalies.trigger_`，取值取自 `ANOMALY_TRIGGERS`）。

★ **本轮又踩了 i18n 的两个老坑，都当场修掉**：
1. 值里出现 markdown `**`（本会话已第 5 次复发）—— 固定提交前检查：
   `grep -n '\*\*' src/i18n/zh-CN.ts src/i18n/en-US.ts | grep -vE ':\s*(//|/\*)'`。
2. 值 === 键名：`en-US anomalies.resolved = 'resolved'` 被门判红 ⇒ 换成 `handled`。
★ 还有一个**新坑**：用 Python 批量改 en-US 时，字符串里的 `\'`
   在 Python 侧被当成转义写成了裸 `'`，**提前闭合了字符串**，
   结果那一行之后的整个对象语法坏掉、键 `oneRowPerDayNote` 从 en 侧消失。
   ⇒ i18n 门立刻报「仅 zh-CN 有」——**这道门又一次起了作用**。
   ⇒ 教训：批量改 i18n 优先用 `edit` 工具逐条改，Python 只用于**不含引号**的批量插入。

---

### 11.67 输出合规上移：命中复核面 + 策略词库面（第三十一轮，admin 档）

新增 `src/api/outputCompliance.ts`（**6 个只读端点**）+ 两个视图
（`/compliance-hits` 现象面、`/compliance-policy` 配置面）
+ 两个抽屉席「输出合规」「合规策略」。

| 端点 | 移动端 | 档位 |
|---|---|---|
| `GET /api/admin/output-compliance/stats` | `/compliance-hits` | `admin` |
| `GET /api/admin/output-compliance/records` | `/compliance-hits` | `admin` |
| `GET /api/admin/output-compliance/review-queue` | `/compliance-hits` | `admin` |
| `GET /api/admin/output-compliance/feedback` | （下一批） | `admin` |
| `GET /api/admin/output-compliance/policy` | `/compliance-policy` | `admin` |
| `GET /api/admin/output-compliance/keywords` | `/compliance-policy` | `admin` |

★ 鉴权是 `RegisterRoutes`（`admin/output_compliance_handler.go:38-56`）统一套
`AdminMiddleware` ⇒ **admin 档**，两个抽屉席都不设 `requiresRole`，
`AppDrawer.spec.ts` 白名单不变。
★ 该文件注释记了一笔 2026-10-03 的**安全根修**：此前 8 个端点**裸挂 mux**，
而全局 auth 中间件对 `/api/` 前缀显式旁路（`middleware/auth_mw.go` 自证的不变式）
⇒ 等于未认证即可读 default 租户的合规记录、甚至未认证写 policy。现在全部套上了。

累计（**当场实测**）：**40 视图 / 38 API 模块 / 36 抽屉席（9 席 superAdmin）**。

#### ★★★★★★ 三个「不是真值」的字段（本批的头号理由）

1. ★★★★★ `jailbreak_hits` **恒为 0，且永远会**是 0：handler 写死，
   注释自陈「本仓无 jailbreak检测器，诚实报 0，而不是编一个数出来」
   ⇒ 0 的含义是「**没有这个检测器**」，**不是**「没有越狱问题」。
2. ★★★★★ `avg_latency_ms` **恒为 0**（审计表无此列），同理由。
3. ★★★★★ `total_checks` **不是检查次数**，它**镜像 `total_issues`**：
   注释写「暂无真实计数源（检查通过不落审计行，`policies.total_checks` 列存在
   但全仓无递增点），先镜像 `total_issues` 保持字段可用」。
   ⇒ 把它说成「检查次数」会让人以为「查了 100 次只命中 3 次」，实际是同一个数写两遍。

配套两条：
4. ★★★★ **`stats` 的单路失败会静默回 0**（不是 500）：
   `pendingReviews` 查询失败 ⇒ `slog.Warn` 后置 0；分类查询失败 ⇒ 四个变量保持零值。
   注释自陈「stats 是聚合展示，单路失败不应 500 整个面板，但也不能静默吞掉——
   **面板上该字段回 0 可见异常**」。
   ⇒ ★★ 于是页面上的 `0` **可能是查询失败**。所以这些 0 **不能**无条件渲染成「没有」。
5. ★★★★ `stats` 只统计 `issue_type` 的**三个**（`pii`/`secret`/`toxic`），
   而写库处 `domains/outputcompliance/checker.go` 的 Type 字面量有**五个**
   （多出 `internal_ip`/`bias`）⇒ 那两类的命中**在 stats 里没有字段**。
   ⇒ `total_issues ≥ 三类之和`，差额就是它们。页面照实说明，不假装五类齐全。

#### ★★★★★★ `records` 的 `content_preview` 是安全约束

6. ★★★★★ SQL 是
   `CASE WHEN COALESCE(redacted,false) THEN left(COALESCE(redacted_output,''),120)
    ELSE '' END`
   ⇒ **只有引擎已经脱敏过的行才有预览**，未脱敏一律返回**空字符串**
   （不是 NULL、不是原文）。handler 注释写明：回显原文等于把这个列表变成
   「把 PII/密钥原文摊平给人看」的通道，与合规模块存在的目的相反。

   ★★ 由此定下一条**客户端判据写法**：这条不变量**不能**只依赖
   「后端保证未脱敏时 preview 必为空串」这个前提。
   变异验证抓到这一点：把 `hasPreview` 改成「有内容就显示」时**仍然全绿**，
   因为我的夹具里未脱敏行的 `content_preview` 本来就是 `''`（**等价变异**）。
   补了一条判据：故意喂「`redacted=false` 但带内容」的行，页面必须照样不显示。
   ⇒ 修改后才转红。**安全判据要独立于被依赖方的善意。**

#### ★★★★★ `keywords` 与 `review-queue` 可能**整个端点 500**

7. ★★★★★ 这两个列表把**可空列直接扫进裸 Go `string`**，且扫失败就
   `writeInternalErrStr` + `return`（**放弃整份列表**）：

   | 端点 | 扫描函数 | 可空列 |
   |---|---|---|
   | `/keywords` | `scanKeyword`（:448-452） | `description`（另有 `action`/`enabled`/`severity` 也可空） |
   | `/review-queue` | `scanReviewQueueItem`（:604-611） | `session_key` / `issue_subtype` / `reviewer` / `review_comment`（四列全可空） |

   ⇒ pgx 扫 NULL 进 `*string` 报 `cannot scan NULL into *string`。
   ★★ 本仓**已有三处先例**记录同一失败模式：
   `domains/reportrollup/grainreport.go:305`（「真库 E2E 实测整个端点 500」）、
   `domains/providerprofile/pg_profile_store.go:104/184/264`、
   `cmd/gateway/turn_logs_aggregator_crossmonth_realdb_test.go:67`。
   ★★ **同族内不一致**：`records` 那一侧全部用 `COALESCE(...)` 规避了（5 处），
     `policy` 也用 `sql.NullString` 兜了 `last_detection_at`（:223）
     —— **只有 `keywords` 与 `review-queue` 没兜**。
   ⇒ 客户端义务：**500 时不许渲染成「清单为空」**，必须显示「后端扫描失败」
     并说明最可能的成因。
   ★ 这条**只记录不修后端**（不属本专题范围），但它已经是移动端的硬约束。

#### 其余四条

8. ★★★ `review-queue` 的响应**没有 `total`**（只有 `items/status/limit/offset`）
   ⇒ 无法做「共 N 条」分页，只能按 `items.length === limit` 近似说「可能还有更多」。
   而 `records` **有** `total`。⇒ 两个分页控件的行为不同，是设计如此。
9. ★★★ **同族默认条数不同**：`review-queue`/`feedback` 默认 **20**，
   `records` 默认 **50**；上限统一 200（`complianceMaxLimit`，:935）。
   ★ `parsePagination`（:937-955）是本仓**第四个**同族分页实现
     （pending `pageBounds` 50/500、request-anomalies `queryInt` 50/500、更早的 sessions/list）
     —— **数字可能一样，实现各不相同，不可互相引用**。
     非数字/≤0 ⇒ 回落默认；>200 ⇒ 静默 clamp；`offset<0` ⇒ 0。**永不报错。**
10. ★★★ `review-queue` 的 `status` 查询参数**没有 allowlist**：
    空 ⇒ 默认 `pending`；传什么按什么查 ⇒ 传 `status=xxx` **静默返回空**。
    ★ DB 侧 `status` 列有 `CHECK IN ('pending','approved','rejected')`（migration 365）
      —— 但那只约束**写入**，不约束**查询**。⇒ 前端只发这三个字面值。
11. ★★ 时间格式**不统一**：`records.created_at` 是**显式
    `.UTC().Format(time.RFC3339)`**（:917），时区确定；
    而 `review-queue`/`keywords` 的 `CreatedAt` 是 `string`，由 pgx 从 `TIMESTAMPTZ`
    **直接扫进字符串、未经 `.UTC()`**，具体字面格式取决于编解码路径与库的 session timezone。
    ★★ **本轮没有真库可验，因此不下结论**（§11.63 的「推断必须追到代码确认」
    在这里只能追到「没有 `.UTC()`」这一步）。
    ⇒ 客户端义务：一律走自己的格式化，**解析不了就原样回显**，
      绝不假定这一族全是 UTC RFC3339。
12. ★★★ **没配策略时，后端返回的是一份「合成的默认策略」，不是 404。**
    `fetchPolicy`（:210-219）在 `pgx.ErrNoRows` 时
    `return defaultOutputCompliancePolicy(tenantID), nil`
    ⇒ 「库里没配」与「配了但看着像默认值」在响应里**长得一模一样**：
    `defaultOutputCompliancePolicy` 的 `id` 是 **0**、`created_at`/`updated_at` 是**空串**、
    `policy_name` 是字面量 `"default"`。
    ⇒ 判据：`id === 0 || created_at === ''` ⇒ 标「这是内置默认策略，不是你们的配置」。
    ★ 我为此把 `unwrapCompliancePolicy` **收紧到只接受对象**：handler 的
      `writeJSON(w, 200, policy)` 传的是 `*OutputCompliancePolicy`，源码实测**永不返回数组**，
      「宽容接受单元素数组」会掩盖真实的后端形状变更。
13. ★★ `exception_rules` / `notification_channels` 是 `json.RawMessage`
    ⇒ **形状不定**（数组/对象/null），原样透传不解析。
14. ★ `llm_engine_id` / `last_detection_at` 是**指针但 JSON tag 没有 `omitempty`**
    ⇒ **键一定存在**，值可能为 `null`（不是「键缺失」）。

#### 一处**我自己推断错、追到列类型才发现**的修正

我第一版写「`COALESCE(whitelist_keywords,'{}')` 可能把数组兜成**对象**」。
追到 `deploy/sql/schemas/baseline/01-schema.sql:10894` 才发现列是
`whitelist_keywords text[] DEFAULT '{}'`，而 Postgres 会把无类型字面量 `'{}'`
**推断成 `text[]`** ⇒ **永远是数组**，不会变成对象。已按实测更正。
（这是本会话第 3 次「推断必须追到 schema/写入方才成立」。）

#### 本轮门禁

- **变异验证 9/9 有牙**：阈值不再 ×100 / 合成默认策略识别被去掉 /
  形状不符静默返空 / preview 规则改成「有内容就显示」/ 队列 500 退化成空态 /
  删「只统计三类」差额说明 / 词库 500 退化成空态 / 关闭开关不再灰化 /
  queue status 不再拦非法值。**全部红在具名断言上**，还原后逐字节一致、复跑全绿。
- ★★ 变异脚本本身也暴露两处**读数问题**，都当场修正：
  1. **C4 首次没转红 = 等价变异**（见坑 6 的判据改写），不是判据无牙。
  2. **C8 的读数误报**：合并跑三个 spec 时我的正则 `Tests\s+(.*)` 匹配到了
     **另一个文件的汇总行**，打印出「67 passed (67)」却报了 rc=1。
     单独跑该文件复核 ⇒ `rc=1`、红在
     `★★★ 关闭的检查项也要出现（灰的），不是只列开着的`。
     ⇒ **合并跑的失败输出里，正则很容易抓到别人的汇总行**；
       判「红在哪」要能报出**具名用例**。
- 全量 **10 连跑全绿，1339 用例**（连跑器落盘，**0 份失败快照**）。
- `npm run build` **rc=0**（`BUILD_RC` 直接从命令取）；三门全过
  （css-media 61 文件 / touch-target 58 个 `.vue` / i18n parity **1154 键**）；
  `vue-tsc -b` 无 TS6133。
- i18n parity **1154 键**、源码字面量键 918 个；`dynamicKeys.spec.ts` 动态前缀增至
  **11 处**（新增 `compliance.issue_` / `compliance.qstatus_` / `compliancePolicy.th_`，
  阈值后缀用 `f.replace('_threshold','')` **现算**，不手抄）。
- ★ 本轮又踩到两个既有门，各被拦下一次：
  1. 值 === 键名：`en-US compliance.none='none'`、`compliancePolicy.disabled='disabled'`
     ⇒ 换成 `not recorded` / `switched off`。
  2. `@ts-expect-error` 放在**调用行**上方是无效的（错误落在**实参那一行**）
     ⇒ 报 TS2578「未使用的指令」。改用 `as never`。

### 11.68 提示词注入上移：检测现象面 + 规则配置面（第三十二轮，admin 档）

新增 `src/api/promptInjection.ts`（**8 个只读端点**）+ 两个视图
（`/injection` 现象面、`/injection-config` 配置面）
+ 两个抽屉席「提示词注入」「注入规则」。

| 端点 | 移动端 | 档位 |
|---|---|---|
| `GET /api/admin/prompt-injection/stats` | `/injection` | `admin` |
| `GET /api/admin/prompt-injection/detections` | `/injection` | `admin` |
| `GET /api/admin/prompt-injection/attack-vectors` | `/injection` | `admin` |
| `GET /api/admin/prompt-injection/rules` | `/injection-config` | `admin` |
| `GET /api/admin/prompt-injection/engines` | `/injection-config` | `admin` |
| `GET /api/admin/prompt-injection/severity-matrix` | `/injection-config` | `admin` |
| `GET /api/admin/prompt-injection/canary-tokens` | `/injection-config` | `admin` |

★ 鉴权：`admin/prompt_injection_handler.go:33-72` **8 条路由全部挂 `AdminMiddleware`**
⇒ **admin 档**，两个抽屉席都不设 `requiresRole`，`AppDrawer.spec.ts` 白名单不变。

累计（**当场实测**）：**42 视图 / 39 API 模块 / 38 导航席（底栏 4 + 抽屉 34，其中 9 席 superAdmin）**。
★★ 「导航席」= `BOTTOM_NAV`(4) + `DRAWER_NAV`(34)，不是抽屉单独计数；
`client.spec.ts` 与并发会话新增的 `nativeTransport.ts` 均**不计入** API 模块数
（39 = 41 个 `src/api/*.ts` 减去 `client.spec.ts` 再减去 `nativeTransport.ts`）。

#### 坑位编号**以 `promptInjection.ts` 模块头为准**

★★★ 本节编号**不另起炉灶**：`promptInjection.ts` 顶部注释里有 **(1)..(14) 的完整清单**，
而代码里散落着 **20+ 处 `见坑 N`** 引用它（行内注释 + 两个视图）。
⇒ **那份清单是 SSOT**，本文只做**补充说明**并沿用同一编号；
文档自造编号会让所有 `见坑 N` 指空（本轮第一版就是这么写错的，当场返工）。

1. ★★★★ `rules` / `engines` / `canary-tokens` **完全没有分页参数**——
   SQL 里没有 `LIMIT`/`OFFSET`，响应是 `{rules:[…], count: len(rules)}`。
   ⇒ `count` 是**本次返回的条数**，不是「总共有多少」。
   ⇒ 页面上**不提供**翻页控件，也不把 `count` 说成总数。
2. ★★★★ `detections` 用 `page` + `page_size`，**不是** `limit`/`offset`：
   `page<1 → 1`；`pageSize<1 || >100 → 20`。
   ★★ `page_size` 越界是**回落默认 20**，**不是** clamp 到 100
   —— 与 output-compliance / pending / request-anomalies 的 clamp 语义**相反**。
   ⇒ ★ **同一个仓里四个同族分页实现，越界行为一半 clamp 一半回落，不可互相引用。**
   响应 `{detections, page, page_size, total}`，`total` 是 `COUNT(*)` 的真总数。
3. ★★★★ `attack-vectors` 的 `page`/`page_size` 规则与 detections 相同，
   但响应 `{vectors, page, page_size}` —— **没有 `total`**。
   ⇒ 同一个页面里两种分页控件行为不同，只能按「这页排满」近似。
4. ★★★★ `listRules`（:322-325）与 `handleDetections`（:551-554）都是
   `if enabled != "" { … enabled == "true" }`
   ⇒ `?enabled=1` / `?enabled=yes` / 带空格的值都判为 **false**，
     筛出**恰好相反**的结果，而且**不报错**。
   ⇒ 客户端**只发字面量 `true` / `false`**（变异 P3 打的就是这条）。
5. ★★★★ `handleStats`（:637-670）读**预聚合表** `prompt_injection_stats_enhanced`，
   且 `if err == pgx.ErrNoRows { stats = &DetectionStats{} }` ⇒ **返回 200 与全 0**。
   ⇒ 「表里没有本租户的行」与「统计值全是 0」在响应里**长得一模一样**。
   ★ 与 output-compliance 的「合成默认策略」是**同一形态**的问题。
   ★ 而且它是**后台刷新**的 ⇒ 与 detections 的实时读法**存在延迟**，两个面板天然对不上。
   ⇒ 形状校验只能认 `total_detections` 是 `number`，**不能**把「全是 0」当没数据抛错（P4）。
6. ★★★ `rules` 的 `category` 过滤是**两列 OR**
   （`AND (category = $n OR category_new::text = $n)`），
   而 `detections` 的 `category` 是 `$n = ANY(categories)` 的**数组包含**
   ⇒ ★★ **同名不同义**：同一个参数名打到两族端点上语义完全不同，
     所以规则列表必须**同时显示新旧两套分类**。
7. ★★★ `rules` 的 `search` 是 `ILIKE '%q%'` ⇒ **子串 + 不分大小写**
   （匹配 `rule_name` 或 `description`），客户端**不**自己加 `%`；
   `rules.type` 则是**精确匹配且大小写敏感**（与 `search` 又是一种口径）。
   `detections` **没有** search。
8. ★★★ `severity-matrix` 的 `notify_channels` 由 `jsoncol.Decode` 解析而
   **返回值被丢弃**（:985）⇒ 非法 JSON 时**静默变空数组**。
   ⇒ 「没有通知渠道」有两种可能，**接口分不出来**，页面照实说，不替后端编原因。
9. ★★ `severity-matrix` 的排序是
   `CASE severity_level WHEN 'low' THEN 1 … WHEN 'critical' THEN 4`
   ⇒ **只认这四个字面值**；其它值排序键为 NULL，会被排到**最后**。
10. ★★ `detections` 的 `total` 是用
    `strings.Replace(query, "<SELECT 段原文>", "SELECT COUNT(*)", 1)` **拼**出来的
    —— 极脆（依赖 SELECT 子句逐字匹配），改 SQL 就会错。
    ★ 对客户端不可见，只作为记录；**不**据此怀疑 `total` 是错的。
11. ★ `avg_score` / `avg_llm_confidence` 是 `COALESCE(…,0)`
    ⇒ 无数据是 **0 而不是 null**，页脚必须说明，否则「平均分 0」会被读成「平均分极低」。
12. ★ 指针字段**键一定存在、值可能为 `null`**（不是「键缺失」）：
    `detections.llm_confidence`、`engines.model_canonical_id`/`credential_id`/`last_called_at`、
    `attack-vectors.detected_at`、`canary-tokens.expires_at`/`last_leaked_at`。
    ★★ 例外：`engines.model_name` 来自 `LEFT JOIN models_canonical`
    ⇒ `model_canonical_id` 为 null 时是**空串**（不是 null），显示成「未关联模型」。
13. ★ 错误信封是 `{"error":"…"}`（**字符串**，与 output-compliance 同族），
    或 `writeInternalErrStr` 的 `{"error": op}` ⇒ 两种都由 `client.ts` 的
    `errorMessage` 统一兜住（本仓第 **5** 个错误信封族）。
14. ★★★★★ `detections.risk_level` 在库里是 **`integer`（CHECK 1..10）**
    （`deploy/sql/schemas/baseline/01-schema.sql:11379`），
    而 Go 结构体声明成 `string` ⇒ 响应里是 **`"7"` 这种数字字符串，不是等级名**。
    ★★ 而 `severity_action_matrix.severity_level`（同文件 `:16673-16688`）
    **才是** `VARCHAR` + CHECK 四个值（`low`/`medium`/`high`/`critical`）
    ⇒ **同一个概念在两张表里一个是数字、一个是单词**。
    页面上若不分开讲，看的人会把「风险 8」拿去和矩阵里的 `high` 比，
    比完对不上还以为数据坏了。
    ★ 这正是「推断差一格就会错」：字段名叫 `risk_level`、同模块另一张表就是档位名
    ⇒ **不查 schema 几乎必然写成 `low/medium/high`**。
    客户端因此有 `parseInjectionRiskLevel`：非字符串 / 非整数 / 越界一律返 `null`，
    页面显示「无法识别」，**绝不**硬套一个档位（变异 P1/P7 各打一次）。
    ★ 同表里 `detection_score` 也是整数，是**另一把刻度**（检测分 vs 风险级别）。

#### 视图层另外查到的四条（**不在**上表 1..14 内，故不占编号）

- ★★★ `rules.is_system` 是 `COALESCE(is_system, true)`
  ⇒ 库里为空按「**是**」算。缺值**不是**「未知」，页面要说清。
- ★★★ `action_override` **空串 = 沿用处置矩阵**，不是「无动作」
  ⇒ 空串渲染成「无动作」等于谎报处置策略（变异 P9）。
- ★★ 蜜罐 token 的值是**诱饵凭据**（故意放进内容里看会不会被回显/外传），
  **不是**用户凭据。页面必须警告「不要拿去当密钥用」。
- ★★ `rules`/`engines`/`canary-tokens` 的 500 **绝不能**渲染成「没有配置」
  —— 安全规则的缺失等于**检测被静默关闭**，与「清单为空」语义完全相反
#### schema 照抄（不靠推断）

- `public.injection_category` **15 个值**（`01-schema.sql:66-81`）
- `public.injection_action` **11 个值**（`:40-52`）
- `severity_level` CHECK **4 个值**（`:16673-16688`）
⇒ 三个常量数组 + 三条判据（数量与顺序都对）。



#### 本轮**没有**发生「推断订正」，但有两个**差一格就会错**的点

本批 14 条坑全部是**先追到 schema / handler 再落笔**，没有出现「先断言、后订正」。
但有两处按直觉写就会错，且都属于**同一个类别——量纲与形状**：

- ★ `risk_level`（上表第 14 条）：直觉会写成 `low/medium/high` 档位名。
- ★★★ 「说明文案里必然含某个词」**不能**写成文本负向断言。
  本会话已踩 **3 次**：pending 的「已完成/已失败」、injection 的「没有统计记录」、
  compliance 的 `**`。第三次在 `InjectionView.spec.ts:211` 又撞上，
  判据最终写成「全 0 的 stats 照样渲染、不出现空态节点」—— **结构断言**，
  而**不是** `expect(wrapper.text()).not.toContain('没有统计记录')`
  （后者会把一段**正确的说明文案**判红）。
  ⇒ 定死一条规矩：**这类判据落在控件/结构上**（无 select / radio / checkbox、
    面板内无空态节点、请求确实发出），**不落在文案字面上**。

#### ★ 门禁数字必须**当场实测**，不能靠上一批的增量推算

本批第一版写的「API 模块 40 个」是**错的**：直接按上一批的 38 + 1 加出来，
而工作区里同时有**并发会话**新增的 `web-mobile/src/api/nativeTransport.ts`
（`git status` 里的 `??`），被裸扫一起数了进去。
当场重数才是 **39**（41 个 `src/api/*.ts` 减 `client.spec.ts` 再减 `nativeTransport.ts`）。
同理「抽屉席 38」实为 **底栏 4 + `DRAWER_NAV` 34**。
⇒ ★★ 同一工作区有并发会话时，**任何「累计 X 个」都必须重数**，不能在上批数字上加。

#### 本轮门禁（第五批接入后当场实测）

- **变异验证 9/9 有牙**：P1 risk 解析改成「有值就当数字」/
  P2 `page_size` 越界改成 clamp 100 / P3 布尔筛选不再只发字面量 /
  P4 stats 形状校验改成「有对象就接受」/ P5 「这页排满」改成「有条就 true」/
  P6 detections 500 退化成空态 / P7 risk 解析失败硬套成 0 /
  P8 rules 500 退化成空态 / P9 `action_override` 空串渲染成「无动作」。
  全部红在**具名断言**上；还原后逐字节一致、复跑全绿。
- ★★ 变异脚本**又一次**暴露读数问题：**P9 在合并跑时正则报「抓不到具名用例」**，
  单独跑 `InjectionConfigView.spec.ts` 复核 ⇒ `rc=1`、红在
  `★★★ action_override 空串 ⇒ 说「沿用处置矩阵」，不是「无动作」`。
  ⇒ 与 §11.67 的 C8 **同根因、本会话第 2 次**：**合并跑**时抓到了**别的文件的汇总行**。
    **合并跑的失败输出不可信**；判「红在哪」必须能报出**具名用例名**，
    只有 `rc≠0` 而抓不到名字 ⇒ 降级为「可疑」，单独跑该文件复核。
- 本批三个 spec：**98 用例全绿**（API 37 + 现象面 31 + 配置面 30，`RC=0`）。
- 全量 **10 连跑全绿，1467 用例，0 份失败快照**（连跑器落盘，`STAB_RC=0`）。
  ★ 1467 里含并发会话同期新增的用例，**不是**「我加了 128 个」。
- `npm run build` **rc=0**（`BUILD_RC` 直接从被测命令取，未经管道）。
- 三门全过：css-media **63 文件** / touch-target **60 个 `.vue`** /
  i18n parity **各 1283 键**（源码字面量键 **1026** 个，扫了 **130** 个 `.vue/.ts`）；
  `vue-tsc -b` 无 TS6133。
- `vue-tsc` 本轮修三处：`unwrapInjectionDetections` 的未用 import
  **接进一条断言**而不是删（顺带把它单测了）；`InjectionConfigView` 删未用的
  `computed` / `fmtInt`；`InjectionView` 的 `riskOf(d) as number` cast。
- `dynamicKeys.spec.ts` 动态前缀增至 **12 处**
  （新增 `injection.cat_`，取值取自 `INJECTION_CATEGORIES`），阈值 7→12。

### 11.69 输出合规的复核结论面：接上最后一个只读端点，并修掉一个**已上生产的真 bug**（第三十三轮，admin 档）

本轮补齐 `output-compliance` 族最后一个未接视图的只读端点
`GET /api/admin/output-compliance/feedback`，作为 `/compliance-hits` 的**第 4 个面板**
（与「复核队列」相邻——两者是同一复核闭环的两半：待复核 / 复核结论）。

★ **不单开一页**：底栏 5 席已满、抽屉席已 34 个，为一个「三个筛选项、无总数」的
列表再占一个抽屉席不划算；且它与队列同源同族，放一起语义更连贯。

#### ★★★★★★ 本轮头号发现：**已推到 origin 的真 bug**

1. ★★★★★★ `unwrapComplianceFeedback` 把响应键写成 **`items`**，
    而后端 `listFeedback`（`admin/output_compliance_handler.go:711`）写的是：

    ```go
    writeJSON(w, http.StatusOK, map[string]interface{}{
        "feedback": items, "limit": limit, "offset": offset})
    ```

    ⇒ 该函数对**每一个真实响应**都抛 `形状不符`。
    ★★ 而**夹具也是照着 `items` 写的**（当时的 `outputCompliance.test.ts:143`）：
    `jsonResponse({ items: [], limit: 20, offset: 0 })`
    ⇒ 三条用例全绿，其中一条就叫「六个只读端点各自独立」。

    **传播路径已核实**：`git log -- web-mobile/src/api/outputCompliance.ts` 只有一条
    （`b0d90bb8f`），且 `git show b0d90bb8f:…` 里键**当时就是 `items`**
    ⇒ 该 bug **随创建它的那个提交一起推到了 origin**，
    并一路存活穿过整个注入批次（`a67e05c45`）才被抓到。

##### ⇒ 由此定一条硬规矩（本轮最值钱的产出）

**夹具证明的是「代码符合我对契约的理解」，不是「代码符合真实契约」。**
夹具和被测代码出自同一个人/同一次理解 ⇒ 两者**同时错**时，用例必然全绿。

所以：**每个端点至少要有一条判据，其夹具里的响应体是从后端 `writeJSON`
的 map 字面量逐字抄下来的，并在用例名里带上该字面量的行号。**
这样后端改键名/改行号时，那条判据会先响，而不是跟着夹具一起错。

新增的 `outputCompliance.test.ts` 分组「★★★★★★ 响应键逐字对得上 writeJSON（后端行号钉死）」
就是这道门，六个端点逐条列出后端行号与键名，并**互喂对方的形状**验证必抛错。

#### 同一 handler 六个 200 响应的键名（实测，逐字）

| 端点 | 行 | 响应键 | 有 `total` |
|---|---|---|---|
| `stats` | `:799` | **扁平对象**，无包装键 | 无 |
| `records` | `:924` | `records` | **有** |
| `review-queue` | `:602` | `items` | 无 |
| `feedback` | `:711` | **`feedback`** | 无 |
| `keywords` | `:445` | `keywords` | 无 |
| `policy` | `:208` | **裸对象** `policy` | — |

⇒ ★ **同一族里 `review-queue` 用 `items`、`feedback` 用 `feedback`**，
键名不同；而 `stats` / `policy` 干脆没有包装层。
**「同族就是同一个形状」这个假设在本族三次都是错的。**

#### feedback 端点其余契约

2. ★★★★ 响应**没有 `total`**（只有 `feedback`/`limit`/`offset`）
   ⇒ 与队列一样只能用 `feedback.length >= 生效 limit` 近似说「后面可能还有」。
3. ★★★ 默认条数 **20**（`parsePagination(r, 20, 0)`，:675），上限 200
   —— 与 `records` 的 50 不同，是本族第三种默认条数。
4. ★★★ `reporter` / `comment` 的 JSON tag 带 **`omitempty`**（:159-160）
   ⇒ **键可能整个不存在**（不是「值为空」）。
   同表 `reporter VARCHAR(255)` / `comment text` 在 schema 里**可空**
   （`01-schema.sql:10811-10812`），而 handler 又是裸扫进 `string`
   ⇒ 与坑 7 同族的 NULL 扫描 500 风险。
   ★ **但这一条只能算「潜在」不能算「必然」**：`authEmail`（:1251-1258）永不返空
   （兜底字面量 `"system"`），且 `createFeedback` 的 `req.Comment` 是 Go `string`
   零值 ⇒ **本 handler 自己的写入路径造不出 NULL**，只有仓外写入才会触发。
   与 `keywords`/`review-queue` 的确定性不同，此处不下「必然 500」的结论。
5. ★★★ `created_at` 由 pgx 把 `TIMESTAMPTZ` **直接扫进字符串、没有 `.UTC()`**
   ⇒ 与 queue/keywords 同族；**格式本轮仍无真库可验，不下结论**，
   客户端一律走自己的格式化、解析不了就原样回显。
6. ★★★ `type` 查询参数**没有 allowlist**（DB CHECK 只约束写入）⇒ 只发三个字面值
   `false_positive` / `false_negative` / `correct`（migration 365 / `01-schema.sql:10814`）。
7. ★★ `createFeedback`（:723）只校验 `feedback_type != ""`、**不**校验是否在 CHECK 内
   ⇒ 传非法值会撞 DB CHECK 而返回 **500 而不是 400**。
   本页不碰写操作，仅记录。

#### ★★ 本轮**第四次**踩到「文本负向断言」，而且是**我自己新写的判据**

新加的一条断言本想写「feedback 面板不许出现『共 N 条』」：
`expect(w.text()).not.toContain('共 ')` —— **红了**。
原因是 `records` 面板**有** `total`、**合法地**渲染了「第 1-1 条，共 1 条」
⇒ 一条**跨面板**的全页文本否定断言，被另一个面板的**正确文案**判死。

修法：**断言必须按面板作用域**——
`w.findAll('.ch__panel').find(p => p.text().includes('复核结论'))!.text()`，
并配一条**正向锚点**（`toContain('推测后面还有')`）防止「因为面板没渲染所以没匹配」。

⇒ 这是本会话第 4 次（pending 的「已完成/已失败」、injection 的「没有统计记录」、
compliance 的 `**`、本轮的「共 」）。**规律不变：凡是否定断言，先问
「这句文案在本页是不是本来就该出现」，还要问「我断言的是不是整个页面」。**

#### ★★ i18n 门禁的解析器只认**单行** `key: 'value'`

本轮 en-US 侧把两个长文案写成换行形式：

```js
feedbackNoTotalNote:
  'Like the review queue above, …',
```

⇒ `verify-i18n-parity.mjs:68` 的解析器是
`/^(\s*)([A-Za-z_$][\w$]*):\s*'((?:[^'\\]|\\.)*)'\s*,?\s*$/`（**逐行匹配**）
⇒ 换行条目**不被识别**，门禁报「仅 zh-CN 有」。
追到解析器确认后改为单行（未改门禁脚本——门禁是对的，是写法不合规）。
★ 全文件**只有我这两条**是换行形式，说明单行就是本仓惯例。

#### 差集测绘（本轮新做）

`grep` 全仓路由字面量得 **657 条路由 / 267 个域**；
与移动端 `src/api` + `src/views` 引用的路径归一化（去 `/api/`、把 `${…}` 折成 `{x}`）后
⇒ **移动端已覆 37 个域**。

★ 差集计算本身踩过一次坑：`comm` 要求**字典序**输入，
而我喂的是按端点数降序的列表 ⇒ 一开始把 `prompt-injection`/`output-compliance`
误报成「未覆盖」。**先排好序再 `comm`。**

未覆盖域（端点数降序，前列）：

| 域 | 端点 | 档位 | 备注 |
|---|---|---|---|
| `admin/tenants` | 25 | 多为 superAdmin | 租户管理 |
| `admin/session-analytics` | 6 注册 | **admin** | ★ 下一批候选 |
| `admin/maas` | 11 | `h.superAdmin` | MaaS 平台 |
| `admin/request-detail` | 12 | **admin** | 统一请求详情 |
| `admin/attachments` | 8 | admin | 数据生命周期·文件 |
| `admin/approvals` | 8 | 混合 | 审批 |
| `admin/tenant-approval-config` | 7 | 混合 | |
| `admin/modules` | 7 | | |
| `admin/logs` | 7 | 混合（`admin` + `h.superAdmin`） | 日志配置 |
| `system/session-context` | 6 | | |

#### 本轮门禁（当场实测）

- **变异验证 7/7 有牙**：FB1 反馈解包键改回 `items` / FB2 面板读 `items` /
  FB3 500 退化成空态 / FB4 排满判断改成「有条就 true」/ FB5 删掉「键不同名」说明 /
  FB6 空串也发 `type` / FB7 缺值渲染空白。
  **全部红在具名断言上**；还原后**逐字节一致**，基线复跑全绿。
- 本批三个 spec：**212 用例全绿**
  （`outputCompliance.test.ts` 49 + `ComplianceHitsView.spec.ts` 46 + `dynamicKeys.spec.ts` 118）。
  ★ `ComplianceHitsView.spec.ts` 31 → 46。
- ★★ 既有 spec 里**原本没有** `fetchComplianceFeedback` 的 mock
  ⇒ 新面板第一版在打**真实 fetch**，异常被 catch 吞成错误态，
  而「31 全绿」照样成立。补 mock 后才暴露出那条真红的断言。
  ⇒ **新增一个端点时，视图 spec 的 mock 清单必须同步**，
    否则「全绿」只证明「异常被吞得干净」。
- `npm run build` **rc=0**（`BUILD_RC` 直接从命令取）。
- 三门：css-media **63 文件** / touch-target **60 个 `.vue`** /
  i18n parity **各 1295 键**（源码字面量键 **1035**，扫了 **130** 个 `.vue/.ts`）。
- `dynamicKeys.spec.ts` 动态前缀增至 **13 处**
  （新增 `compliance.ftype_`，取值来自 `COMPLIANCE_FEEDBACK_TYPES`）。

### 11.70 会话分析面测绘：数据源是**从未被刷新过的物化视图**（第三十四轮，测绘，未实现）

本轮先做差集测绘再决定做哪一族。测绘对象：
`GET /api/admin/session-analytics/{clients,tasks,users}` 及各自的 `/{id}` 详情
（`admin/handler.go:1091-1098`，6 条注册**全部** `admin(...)` ⇒ **admin 档**）。

**本节只记录已查实的契约与一个后端缺陷，本轮未实现视图。**

#### ★★★★★★ 头号发现：`session_client_stats` 等四个物化视图**没有任何东西在刷新**

`admin/session_analytics_*.go` 读的是**物化视图**（不是表）：
`session_client_stats` / `session_task_stats` / `session_client_task_matrix` / `session_owner_stats`
（创建于 `sql/migrations/startup/357_session_analytics_aggregation_views.sql:13`
与 `358_session_ownership.sql`）。

刷新函数是 `refresh_session_analytics_views()`（357:128 定义、358:382 重定义），
它自己的注释写着「**建议每小时或每日执行**」（357:137 / 358:392）。

★ **而它的调用点只有两处，都在 migration 文件的末尾**：

    357:143   SELECT refresh_session_analytics_views();   -- 「初始刷新（首次创建后立即填充数据）」
    358:396   SELECT refresh_session_analytics_views();   -- 同上

已穷尽核实的排除项（**不是「我没找到」，是逐条查过**）：

| 可能来源 | 核实结果 |
|---|---|
| `bg.MaterializedViewRefresher`（本仓唯一的周期性 MV 刷新器，`RefreshInterval = 10 * time.Minute`） | 它硬编码管理的**只有** `routing_analytics_7d` 与 `routing_audit_summary_7d`（`cmd/gateway/main.go:3854` 构造） |
| `pg_cron` / cron 注册 | 全仓无针对这四个视图的注册（`382_session_module_executions.sql` 的 pg_cron 是别的事） |
| 任何 `.go` 里的调度器 | 全仓 `.go` 提到 `session_task_stats`/`session_owner_stats`/`session_client_task_matrix` 的**只有 `admin/` 的读取处** |
| 安装脚本 / shell | 只有 `scripts/_lib/db-init-lib.sh` 提到过该函数名，且是在讲**另一个**问题（见下） |

⇒ 迁移按版本在 `public.schema_migrations` 记账（`db/db.go:770/1043/1216` 的 `INSERT`）、
**每个版本只跑一次** ⇒ 这四个视图自建表那次的「初始刷新」之后，
**再没有任何自动刷新路径**。

⚠️ **未验证的部分（不能下结论的部分）**：本轮**没有真库**，
所以「线上现在是不是冻结的」**未实测**；运维手工执行过该函数也可能。
**能确定的是「代码与迁移里没有周期刷新路径」**，不能确定「线上数据一定没变过」。

★ 附带查到的一条**同类历史事故**：`scripts/_lib/db-init-lib.sh:283-296` 的
Round 43 注释记载，`COMMENT ON FUNCTION refresh_session_analytics_views()`
曾因被 pg_dump 排到 `CREATE FUNCTION` **之前**而导致 baseline 生成时整文件回滚
（`function public.refresh_session_analytics_views() does not exist`，
另见 `docs/handoff/20261001-baseline-generator-round.md:65`）
⇒ 这个函数**连「存在」都曾经不稳定过**。

**客户端义务（下一批实现时必须落进去）**：
列表响应里**有 `refreshed_at`**，页面**必须显示「数据截至 …」**，
否则等于把一份可能冻结的数字当实时指标展示。

#### ★★★★★ `refreshed_at` 在**空列表**时是 Go 零值

`var refreshedAt time.Time` 在循环**外**声明，每行 `rows.Scan(..., &refreshedAt)` **覆盖**，
循环不执行就保持零值 ⇒ 空列表时序列化成
`0001-01-01T00:00:00Z`。
且它是**最后一行**的值——同一页内各行 `NOW()` 相同所以看不出差异，
但**它不是「查询时刻」也不是「视图创建时刻」**，只是这一页最后扫到的那一行的刷新戳。

#### ★★★★ 扫描失败是 `continue`（**静默丢行**），与 output-compliance 相反

```go
err := rows.Scan(...)
if err != nil {
    warnRowSkip("session analytics clients", err)
    continue          // ← 跳过这一行，整份列表照常 200 返回
}
```

⇒ 与 `output_compliance_handler.go` 的「一行为空 ⇒ **整个端点 500**」
（§11.67 坑 7）**方向完全相反**。

★ 由此产生一条**跨端点不可套用**的结论：
`total` 来自独立的 `COUNT(*)`，而列表可能因为坏行而**少于** `total`
⇒ **「共 N 条」与实际渲染条数天然可能对不上**。
客户端必须**允许**「本页显示数 < total」并说明原因，**不能**把它当分页 bug。

#### 可空列：哪些 NULL 会让整行消失

物化视图定义（357:13-36）决定了可空性：

| 列 | 视图里的表达式 | NULL 时 |
|---|---|---|
| `avg_health_score` | `AVG(health_score)::INT` | ✅ 已用 `sql.NullInt64` 兜住 |
| `avg_latency_ms` | `AVG(avg_latency_ms)::INT` | ✅ 同上 |
| `models_used` | `array_agg(...) FILTER (...)` | ✅ Go 侧 nil → `[]` |
| **`first_seen_at`** | `MIN(first_request_at)` | ★★ **裸扫进 `time.Time` ⇒ NULL ⇒ 该行被 `continue` 丢掉** |
| **`last_seen_at`** | `MAX(last_request_at)` | ★★ 同上 |
| `total_cost_usd` / `avg_cost_per_session` | `SUM/AVG(...)` | ★ 可为 NULL ⇒ 裸扫 `float64` ⇒ 同样丢行 |

★ `session_summaries.first_request_at` / `last_request_at` 是
`timestamp with time zone` 且**无 NOT NULL**（`655_session_summaries_schema_reconcile.sql:48-49`）
⇒ NULL 在库层面是允许的。

#### 分页与排序（本仓第五种越界语义）

- ★★★★ `limit := queryInt(r, "limit", 50)`；`if limit < 1 || limit > 200 { limit = 50 }`
  ⇒ 越界是**回落 50**，**既不是 clamp 也不是回落 20**
  （对比：pending 50/500 clamp、request-anomalies 50/500 clamp、
  output-compliance 20→200 clamp、prompt-injection 20 **回落**）。
- `offset := queryInt(r, "offset", 0)`。
- ★★★ 三个列表的**默认排序各不相同**：
  | 端点 | 默认 `order_by` | switch 认的值 | 未命中时 |
  |---|---|---|---|
  | clients | **`cost`** | `sessions` / `health` | 落回 `total_cost_usd DESC` |
  | tasks | **`sessions`** | `cost` / `health` | 落回 `session_count DESC` |
  ⇒ `switch` **没有 default 分支** ⇒ 传 `order_by=xxx` **静默落回默认排序，不报错**。
- 响应 `{clients|tasks, total, limit, offset, refreshed_at}` —— **这族有 `total`**。

#### 权限：比 `admin(...)` 更严

- ★★★ 三处列表都显式挡普通用户：
  `if IsRegularUser(r) { writeError(w, 403, "client analytics requires admin access") }`
  ⇒ **注册档位是 admin，但普通用户仍被单独拒绝**。
- ★★★ 租户隔离：非 superAdmin 且显式传了别的 `tenant_id` ⇒ **403 cross-tenant**；
  非 superAdmin 且**不传** ⇒ **自动填成 `callerTenant`**（不是报错、也不是全库）。
- 三个列表的 403 文案各不相同（`client analytics…` / `task analytics…`）。

#### 错误分类：42P01 单列一档（**503**，不是 404）

`writeAnalyticsDetailErr`（`admin/session_analytics_mv_guard_test.go:20-45` 固化的契约）：

| 错误 | 状态 | 载荷 |
|---|---|---|
| `pgconn.PgError{Code:"42P01"}`（物化视图不存在） | **503** | 含 `analytics_view_missing` 与视图名 |
| 其它（含 `ErrNoRows`） | **404** | 原文案 |

★ 测试注释自陈这是 R35 复审的修复：「detail 端点此前把一切查询错误吞成 404——
缺 357 视图时误导排查」。
⇒ 与本仓其它族的 `{"error":"…"}` 字符串信封并存，**这一族多一个结构化引导码**。

#### 下一批待办（留档）

- 实现 `clients` / `tasks` 两个列表（同一 MV 族、形状对称，各带 `total`），
  页面**必须显示 `refreshed_at`「数据截至」**；
- `tasks` 比 `clients` 多一个 `clients_used` 数组；
- `users` 列表的 handler 在 `admin/user_profile.go`，**读的是 `session_ownership` 活表**，
  不是物化视图 ⇒ **数据新鲜度与前两者不同**，不能套用同一条说明；
- 三个 `/{id}` 详情形状各不相同（`ClientAnalyticsDetailResponse` 含
  `related_tasks`/`daily_cost_trend`/`recent_sessions`）。

#### 本轮门禁

- 本节**只做测绘**，未改任何 `web-mobile/` 代码；
- 测绘本身的方法论：路由字面量 `grep` 得 **657 条 / 267 个域**，
  移动端已覆 **37 个域**（`grep` 两份清单 → 去 `/api/` 前缀 →
  `${…}` 折成 `{x}` → 排序后 `comm`）。
  ★ **踩坑**：`comm` 要求字典序，我第一次喂的是按端点数降序的列表，
  于是把 `prompt-injection` / `output-compliance` **误报成未覆盖**。
  ⇒ **先排序再 `comm`**，否则差集结论整体不可信。

### 11.71 会话分析上移：客户端维度 + 任务维度（第三十五轮，admin 档）

新增 `src/api/sessionAnalytics.ts`（**2 个只读端点**）+ `src/views/SessionAnalyticsView.vue`
（一页两维度：客户端 / 任务）+ 抽屉席「会话分析」。

| 端点 | 移动端 | 档位 |
|---|---|---|
| `GET /api/admin/session-analytics/clients` | `/session-analytics`（默认维度） | `admin` |
| `GET /api/admin/session-analytics/tasks` | `/session-analytics`（切维度） | `admin` |

★ 鉴权：`admin/handler.go:1091-1094` 四条注册全是 `admin(...)` ⇒ **admin 档**，
抽屉席不设 `requiresRole`，`AppDrawer.spec.ts` 白名单不变。
★ 但**两个列表都显式挡普通用户**（`IsRegularUser` → 403），比注册档位更严。

§11.70 已把 §11.70 里那条头号发现查实；本节把它**落成页面契约**。

#### ★★★★★★ 本页唯一不可省的东西：「数据截至」

1. ★★★★★★ 响应里的 **`refreshed_at` 必须显眼地显示**。
   这是读者判断「这份花费数字有多旧」的**唯一**线索
   （§11.70 已证：物化视图无周期刷新路径）。
   ⇒ 页面顶部第一块就是「数据新鲜度」面板，含：
   - 一句来源说明（物化视图 + 刷新只在建表时跑过一次 + 仓里没有定时调度器）；
   - 「数据截至」的相对时间；
   - **原始时间戳原样透出**（只给相对时间不够，读者要能核对）。
2. ★★★★★ **空列表时 `refreshed_at` 是 Go 零值** `0001-01-01T00:00:00Z`
   ⇒ 那一块换成专门文案「这一页是空的，拿不到真实的刷新时刻」，
   **并且完全不渲染「数据截至」那一格**。
   ⇒ ★ 把零值当成「1970 年之前刷新过」显示出来是最容易犯、也最难发现的错。

#### ★★★★★ 坏行被静默丢弃 ⇒ 「共 N 条」与显示条数可以不一致

3. ★★★★★ 后端扫描失败是 `continue`（**丢行但照常 200**），
   而 `total` 来自**独立的** `COUNT(*)`
   ⇒ **`total=10` 但页面只显示 1 行是后端行为，不是分页 bug。**
   ⇒ `analyticsRowsWereDropped(shown, total)` + 页面显式说明
   「有行的字段为空时被跳过了，不代表数据对不上」。
4. ★★★★ 追到 schema 后把丢行来源**收窄到一列对**：
   物化视图里 `first_seen_at`/`last_seen_at` ← `MIN/MAX(first_request_at)`，
   而 `session_summaries.first_request_at` 是 `timestamptz` 且**无 NOT NULL**
   （`655_session_summaries_schema_reconcile.sql:48-49`）
   ⇒ 全空 ⇒ `MIN()` 为 NULL ⇒ 裸扫进 `time.Time` 失败 ⇒ 整行被丢。
   ★★ **我原本怀疑的 `request_count`/`total_cost_usd` 等被 schema 否掉了**：
   它们全是 `NOT NULL DEFAULT 0`（655:52-55、677:40-43）⇒ `SUM`/`AVG` 不可能为 NULL。
   ⇒ **页面说明只写真正的那一对列**，把已排除的写进去会误导排查。

#### 分页：第五种越界语义

5. ★★★★ `if limit < 1 || limit > 200 { limit = 50 }` ⇒ 越界是**回落 50**。
   至此本仓五种：pending 50/500 **clamp**、request-anomalies 50/500 **clamp**、
   output-compliance 20→200 **clamp**、prompt-injection 20 **回落**、本族 50 **回落**。
   ⇒ 客户端侧也照此回落（不发会被后端改写的值）。
6. ★★★ **这一族有 `total`** ⇒ 分页是**精确**的
   （与 `review-queue`/`feedback`/`attack-vectors` 的近似分页相反）。
   页面文案是「第 1-50 条，共 N」而不是「后面可能还有」。

#### ★★★ 两个维度的默认值不同，客户端不许替后端决定

7. ★★★ `clients` 默认 `order_by=cost`、`tasks` 默认 `order_by=sessions`，
   且 `switch` **无 default 分支** ⇒ 非法值静默落回、不报错。
   ⇒ **不传 `order_by` 时客户端不发这个参数**（让后端用它自己的默认）；
   只发 `cost`/`sessions`/`health` 三个合法值。
   （变异 SV7：若客户端图省事替后端填 `'cost'`，两个维度就都被改成按花费排。）

#### ★★★ `health_distribution` 有两个**同名不同包**的类型

8. ★★★ `admin` 包（`dashboard_session_stats.go:25`）只有 **5 个计数键** `a/b/c/d/f`；
   `admin/dashboardapi` 包（`session_overview.go:63`）**同名**但还有
   `total` 与五个 `*_percent`（在 `session_overview.go:369` 计算）。
   **本族用的是前一个。**
   ⇒ 页面只渲染五档计数，并明说「不返回占比，也不返回总数」；
   判据锁死 `.sa__grade` **恰好 5 个**（两行 ⇒ 10 个，证明不是恒有）。

#### ★★ omitempty 指针与成功率分母

9. ★★ `avg_health_score` / `avg_latency_ms` 是 `*int` + `omitempty`
   ⇒ **键可能整个不存在**，缺失显示 `—` 而不是 0
   （与 output-compliance 的「键一定存在、值为 null」正好相反）。
10. ★★ 成功率分母用 `total_requests`（`SUM(request_count)`，NOT NULL），
    **不用** `success + errors` —— 那两个是独立计数器，不保证相等。
    分母为 0 ⇒ 显示 `—` 而不是 `0.0%`。

#### ★★★★ 变异验证逼出的一条**恒真判据**（本轮最值钱的教训）

11. ★★★★ 我先写的两条判据是**恒真**的，变异后仍绿：
    - `expect(w.text()).toContain('数据截至')`
    - `expect(w.text()).toContain('—')`

    逐条实测才看清原因：
    - 「数据截至」**本来就出现在说明文案里**（「下面显示的『数据截至』就是这份数据真正的年龄」）
      ⇒ 把标签清空（SV1）后断言照样绿。
    - **本页散文里有 4 个破折号**，其中 `orderImplicit` 的
      「两个默认值**——**不一样」就贡献了两个
      ⇒ `toContain('—')` 被**中文标点**喂饱，**永远不会红**。

    ⇒ 修法：断言**取到那个格子自己**再判它的文本
    （`cells.find(c => c.text().includes('平均健康分'))!.text().endsWith('—')`
    且 `.not.toMatch(/\d/)`）。
    ⇒ 这与前几次踩的「说明文案必然含该词」是**同一族的第三种形态**：
      ①负向断言被好文案判红 ②跨面板全页断言被另一面板判红
      **③正向 `toContain` 被散文标点喂饱 ⇒ 恒真，永不失败。**
      前两种是「红着时判据写错了」，**第三种是「绿着时判据根本没在工作」**。

#### 本轮门禁（当场实测）

- **变异验证 19/19 有牙**：
  - API 层 9/9（SA1 键串成 `items` / SA2 键串成 `clients` / SA3 clamp 改回落 /
    SA4 去掉 order_by allowlist / SA5 形状校验放宽 / SA6 宽容解包 /
    SA7 零值判断恒 false / SA8 丢行检测恒 false / SA9 成功率分母改错）。
    ★ 另有 **SA10 判为等价变异**：给 TS interface 加可选字段，
      **运行期被擦除、vitest 观测不到** ⇒ 那处的守门是 `vue-tsc`/build，不是 vitest。
  - 视图层 10/10（SV1 不显示数据截至 / SV2 零值当时间 /
    SV3 删差额说明 / SV4 500 退化空态 / SV5 omitempty 显示 0 /
    SV6 成功率显示 0.0% / SV7 替后端填默认排序 / SV8 分页忽略 total /
    SV9 渲染六档 / SV10 抹掉 tasks 独有的 clients_used）。
- 本批两个 spec：**69 用例全绿**（API 35 + 视图 34）。
- `npm run build` **rc=0**；三门全过
  （css-media **64 文件** / touch-target **61 个 `.vue`** / i18n parity **各 1336 键**，
  源码字面量键 1069，扫了 132 个 `.vue/.ts`）；`vue-tsc -b` **rc=0**。
- `dynamicKeys.spec.ts` 动态前缀增至 **14 处**（新增 `sa.order_`）。
- ★ 途中还修了两个**测试自己**的错误：
  1. `vi.stubGlobal('fetch', …)` 放在模块顶层 + `afterEach` 里的
     `unstubAllGlobals` ⇒ **只有第一条用例能跑到假 fetch**，后面 19 条全走真 fetch
     （报 `Failed to parse URL`）。⇒ stub 必须在 `beforeEach` 里。
  2. `noUncheckedIndexedAccess` 开着 ⇒ `r.clients[0]` 一律补显式非空 helper。
- 累计（**当场实测**）：**43 视图 / 40 API 模块 / 39 导航席（底栏 4 + 抽屉 35，其中 9 席 superAdmin）**。
- 全量 **10 连跑全绿，1605 用例，0 份失败快照**（`STAB_RC=0`）。
  ★ 1605 里含并发会话同期新增的用例，不是「我加了 N 个」。
- 抽屉席 35 是**重数**的（底栏 4 + `DRAWER_NAV` 35），
  与 §11.67 的「36 抽屉席」同一口径（当时底栏 4 + 抽屉 32）。

### 11.72 统一请求详情上移：一处五层取数、五个状态码（第三十六轮，admin 档）

新增 `src/api/requestDetail.ts` + `src/views/RequestDetailView.vue`
+ 路由 `/request-detail/:id?`（无抽屉席 —— 它是**详情页**，靠 request_id 进入，
另配一个输入框以便独立使用）。

★ 鉴权：`admin/handler.go:1271` 是 `admin(...)` ⇒ **admin 档**。

★ 这是本仓最厚的只读端点：**一个 request_id 背后有五层取数**，而响应里的
`source` 决定这份数据有多新、丢了会不会再没有 ⇒ 页面**先渲染来源，再渲染正文**。

| 状态码 | 含义 | 客户端义务 |
|---|---|---|
| **400** | request_id 不合法 | 本地先按同一规则校验，别白换一个 400 |
| **404** | **身兼两职**（见下） | **不许**只说「不存在」 |
| **413** | body > 10MB（**不是 500**） | 说明「请求级限制，不是服务端故障」 |
| **503** | request detail store 未接线 | 说明「部署没接线」，不是「查不到」 |
| 500 | 其余 | 原样透出 |

#### ★★★★★★ 404 身兼两职，其中一职是 fail-closed

1. ★★★★★★ `ErrNotFound` ⇒ 404 `request detail not found`；
    而「**跨租户**」**或**「`meta.tenant_id` 为空」⇒ **也是** 404
    （`unified_detail.go:455-470`）。
    注释自陈这是 2026-08-26 的 P1-29 修复：此前**只对 `PersistencePersisted`**
    做租户校验，**在途/落盘路径**对知道别人 request_id 的 tenant_admin 是敞开的。
    ★ 空 TenantID 被当作「来源不明」而**拒绝** —— 理由是「早于该提交写入的
      遗留在途 meta 没有租户记录，不能跨租户泄漏」。
    ⇒ 后端**无法**区分这两种情况 ⇒ 页面必须写「也可能是跨租户 / 无租户归属」。

2. ★★★★★ **413 不是 500**（`ErrBodyTooLarge`，`MaxBodyFileSize` = 10MB）。
    注释自陈 2026-08-29 审计跟进：映射成 500 会误导运维和客户端。
3. ★★★★ **503 = 部署未接线**（`h.requestDetailLocator == nil`），
    **不是**「查不到」。

#### ★★★★★ `body_status` 是三态，且口径反直觉

4. ★★★★★ `available` = request/response/outbound **至少一个**带真实载荷，
    **空容器（`[]` / `{}`）也算载荷**；
    `unavailable` = 三者**全是** SQL NULL / **JSON null 字面量** / 纯空白。
5. ★★★★ `body_status` 带 `omitempty` ⇒ **键可能整个缺失**，
    注释明写「缺失一律当作 unknown / not computed」
    ⇒ 客户端有第三态 `unknown`，**既不是 available 也不是 unavailable**。
6. ★★★ `dropped` **有意不在**线上契约里（保留期与 `bodies_trimmer` 作业未落地，
    分不出「被清理」与「从没写过」）⇒ 判据锁死取值集**不含** `dropped`。
7. ★★★★ ★★ **不能**用「`available` ⇒ 一定有内容」来渲染：
    JSON null 是写路径对「载荷为空」的**常态编码**（`bodies_writer jsonTextOrNull`），
    而实现（`admin/body_status.go` 的 `columnHasPayload`）与迁移后的 SQL 探针
    `<> 'null'::jsonb` **都把它判为无载荷**；
    注释还记着一次 **R73 订正**（初稿把「含 JSON null」写在 available 侧，与实现相反）。
    ⇒ `bodiesPresent()` 只看**键是否存在**，不做内容断言（变异 V6 守这条）。

#### ★★★★ `request_id` 有三种合法形态，且旧正则被判定**过宽**

8. ★★★★ `ValidateRequestID`（`store.go:440-451`）= 三形态 + 长度 8..128：
    ① 32 位纯 hex（大小写皆可）② 带连字符的 uuid ③ **必须以字母开头**的前缀形态。
    ★ 旧正则 `^[A-Za-z0-9._-]{8,128}$` 被明确**废弃**：
      任何 8 字符的 `abc..def` 都会被接受，而 `..` 在下游工具漏调
      `filepath.Base` 时是**路径穿越向量**。
      新正则用「一个或多个安全字符 / 一个分隔符 + 恰好一个安全字符」这个 atom
      来禁掉连用分隔符 —— 注释自陈这是 **RE2 没有负向前瞻时的写法**。
9. ★★★ 客户端**本地先校验**，非法时**不发请求**（不拿它去换一个 400）。
    ★★ 而这道闸门有**两条路径**：UI 上的「查询」按钮（禁用）与
    **`load()` 内部**那条。路由参数 `/request-detail/:id` 走的是 `watch(..., {immediate:true})`
    ⇒ **直接调 `load()`、绕过按钮禁用**。
    ★ 变异 V7 逼出这条：原先只有测按钮禁用的用例，
    `load()` 里的 if **从没被走到** ⇒ 补了「路由参数带非法 id」的用例才让它有牙。

#### ★★ `omit_body` 只认两个字面值

10. ★★ 后端判定是 `== "1" || == "true"`
    ⇒ `omit_body=0` **不是**「要 body」，它只是「不过滤」。
    ⇒ 客户端 `omitBody: false` 时**完全不带这个参数**（变异 RD2 守这条）。

#### ★★ 五种 `source` 决定新鲜度

11. ★★ `memory`（本进程在途内存）/ `file`（本地落盘）/ `live_stream`（Redis 实时流）
    / `request_logs`（DB 审计表）/ `session_turns`（跨会话轮次表）。
    ★ `memory` 与 `live_stream` 归为「在途」：**进程一重启就没了**，
    页面必须给出这个警告；其余两个是「可复查的记录」。

#### 其它两条

12. ★ `Meta` 的指针字段**全部**带 `omitempty` ⇒ **键可能整个不存在**
    （与 output-compliance 的「键一定存在、值为 null」相反）；
    `Bodies` 的三块是 `json.RawMessage` ⇒ **形状不定**，原样字符串化不解析。
13. ★ 错误信封是 `{"error":{"detail":"…"}}`（`admin/handler.go:1494-1498` 的 `writeError`）
    ⇒ 本仓的 `error.detail` 族。

#### ★★★★ 本轮踩到两处**变异读数不可信**（都不是判据的问题，是脚本的问题）

14. ★★★★ **注入污染**：RD8 原本把 `/* MUT-RD8 */` 写进了**模板串内部**，
    注入后 URL 里多出一段字面量 ⇒ 用例红了 4 条，看着像「有牙」。
    但同轮实测 `encodeURIComponent` 对**全部三种合法 id 形态**都是**恒等函数**
    （字符集 `[A-Za-z0-9._-]` 全是 unreserved）
    ⇒ **真结论是「等价变异」**，那次红是假读数。
    ⇒ 标记必须放在**被测表达式之外**的注释位。
15. ★★★★ **注入留了不配对花括号**：V1/V2/V3 注入 `"if (false) { /* MUT */"`
    导致文件解析失败 ⇒ **具名失败列表为空**。
    只有 rc≠0 而用例名为空时，要先看 log 里有没有 `Failed to load/collect`。

#### 本轮门禁（当场实测）

- **变异验证 19 条**：API 9 条有牙 + **RD8 判为等价变异**（见上）；
  视图 **10/10 有牙**（V1 404 退化成「不存在」/ V2 413 / V3 503 /
  V4 不渲染 source / V5 在途源不警告 / V6 body_status 塌成两态 /
  V7 本地 ID 闸门失效 / V8 发出第三态参数 / V9 omitempty 显示 0 /
  V10 bodies 缺失仍渲染正文）。
- 本批两个 spec：**65 用例全绿**（API 34 + 视图 31）。
- `npm run build` **rc=0**（含 `vue-tsc -b`）；三门全过
  （css-media **65 文件** / touch-target **62 个 `.vue`** / i18n parity **各 1381 键**）。
- ★ i18n 门禁在本轮**抓到一次真缺失**：`rd.badId` 我在视图里引用了却没加进词典
  ⇒ 门禁报「视图引用了不存在的键」才补上。**门禁是活的。**
- ★ 途中修了三个**测试/脚本自己**的问题：
  1. 切换按钮文案是「当前：省略正文 / 当前：含正文」，**默认态不含「省略」**
     ⇒ 用 `includes('省略正文')` 找不到按钮；
  2. `fmtInt` 加千分位（1200 → `1,200`）⇒ 硬找 `'1200'` 判红；
  3. `noUncheckedIndexedAccess` ⇒ `findAll(...)[0]` 补显式非空判断。
- 累计（**当场实测**）：**44 视图 / 41 API 模块 / 39 导航席（9 席 superAdmin）**。
- 全量 **10 连跑全绿，1670 用例，0 份失败快照**（`STAB_RC=0`）。

### 11.73 MaaS 积分价上移：响应里 7 个价都是**算出来的**（第三十七轮，superAdmin 档）

新增 `src/api/maas.ts` + `src/views/MaasRatesView.vue` + 路由 `/maas-rates`
+ 抽屉席「MaaS 积分价」（**本仓第 10 条 superAdmin 抽屉席**）。

★ 鉴权：`admin/maas_handlers.go:14-24` **全部 11 条**注册都是 `h.superAdmin(...)`
⇒ **superAdmin 档**，抽屉席设 `requiresRole: 'super_admin'`，
并同步 `AppDrawer.spec.ts` 的白名单（那条测试**正确地抓到了**本轮新增的席）。

#### ★★★★★★ 本族头号问题：字段名会骗人

1. ★★★★★★ 响应里 7 个 `credits_per_1m_*` **不是「库里存的值」**，
    而是 `globalEffective` + `effectiveModelRates` **算出来的生效价**
    （`maas/model_rates.go:143-149`）。不复刻那两个函数，
    就没有任何办法回答「这个数字从哪来」。

2. ★★★★★ **七维各判各的**，`pick()` 是三条件与运算（`maas/rates.go:96-102`）：

    ```go
    pick := func(manual bool, val *int64, fallback int64) int64 {
        if manual && val != nil && *val > 0 { return *val }
        return fallback        // ← global
    }
    ```

    ⇒ ★★★ **`manual_X = true` 不保证用自定义值**：
    没存值（`nil`）或存的是 0 / 负数 ⇒ **照样回落到全局**。
    ⇒ 同一行完全可能是「输入走自定义、输出走全局」。

3. ★★★★★ 折扣**只作用于全局价**，自定义价是**原值不打折**：
    `globalEffective` 里七维全部 `applyDiscount(base, disc)`，
    而 `pick()` 命中手动分支时直接 `return *val`。
    ⇒ 同一个 300 的自定义价在打八折时生效价仍是 300；
      而全局 400 打八折后是 **320**。
4. ★★★★ `applyDiscount` 是 **`math.Ceil`（向上取整）**：
    `401 × 0.8 = 320.8 ⇒ 321`，不是 320。
5. ★★★★★ `normalizeDiscount`（rates.go:28-33）是 `d <= 0 || d > 1 ⇒ 1`
    ⇒ ★★ **`global_discount = 0` 的含义是「不打折」，不是「全免」**。
    配 0 的人以为免费，实际按原价计费；配 `1.5` 也等价于不打折，
    而**响应里不会告诉你**。

#### ★★★★ 七维回落链各不相同（且末位是硬编码字面量）

6. ★★★★ `globalEffective`（rates.go:42-70）：

    | 维 | 回落链 | 来源标记 |
    |---|---|---|
    | `in` | `base_credits_per_1m_in` ?? `base_credits_per_1m` ?? **10000** | configured / configured_legacy / **hardcoded** |
    | `out` | `base_credits_per_1m_out` ?? baseIn | 两级 |
    | `cache_in` | `base_credits_per_1m_cache_in` ?? baseIn | 两级 |
    | `cache_out` | `base_credits_per_1m_cache_out` ?? baseIn | 两级 |
    | ★ `image`/`audio`/`video` | **恒等于 baseIn** | `Settings` 里**根本没有**这三个字段 |

    ★ `BaseRateSet` 的注释自陈多模态是「model-wide 默认值，取输入价，
    简单文本模型不受影响」。
    ⇒ 客户端 `maasGlobalBaseIn` / `maasGlobalRate` **逐维复算**，
      页面据此说清「这个 10000 是**硬编码兜底**还是你们配的」。

#### ★★★★ 「改了但没启用」——最容易被当成 bug 的那一种

7. ★★★★ `custom_credits_per_1m_*` **不是**「自定义值 vs 生效值」的对，
    它是 **stored 的原样拷贝**（`model_rates.go:130-136`：`r.CustomIn = stored.In` …）。
    ⇒ 完全可能出现「**生效价 = 全局 10000**、**自定义值 = 300**」：
    因为 `manual_in = false` 时生效走 global，而 stored 里的 300 照样被吐出来。
    ⇒ `maasDimHasDormantCustom` + 页面显式说明，否则看起来像数据自相矛盾。
8. ★★★ `is_custom` 是七个 `manual_*` 的**或**（`storedIsManual`，rates.go:119-122）
    ⇒ 含义是「**有没有任何一维**开过手动」，**不是**「整行被定制」。
    页面明说「要逐维看上面那张表」。

#### 其余五条

9. ★★ `vendor` 是**三级回落**
    （`COALESCE(NULLIF(TRIM(mf.vendor),''), NULLIF(TRIM(mc.family),''), '其他')`）
    ⇒ 字面量 **`'其他'` 是兜底值**，不表示真有个叫「其他」的厂商；
    页面打标签说明。
    ★ 而 `mf` 的 LEFT JOIN 条件里带 `AND COALESCE(mf.status,'active')='active'`
    ⇒ **family 停用时这层回落整层失效**。
    `display_name` 回落 `canonical_name`；`modality` 回落字面量 `'text'`。
10. ★★ **只列 active 模型**：`WHERE COALESCE(mc.status,'active')='active'`
    ⇒ inactive / 已下架的模型**完全不可见**，清单看着不完整不代表库里没有。
11. ★★ **没有分页**（SQL 无 `LIMIT`，只有 `ORDER BY mc.canonical_name`）
    ⇒ 「共 N 个」只能数本次返回条数，**没有后端总数**可比对。
12. ★★ 指针字段**没有** `omitempty`（`family`/`updated_at`）⇒ **键一定存在、值可能 null**
    ⇒ 与 request-detail 那一族的 omitempty **正好相反**。
    ★ `updated_at` 为 `null` = `model_credit_rates` 里**没有这一行**（LEFT JOIN 未命中），
    该模型全部价来自全局基价 —— 页面明说。
13. ★ `svc == nil || !svc.Enabled()` ⇒ **503 `database not configured`**
    ⇒ 是「MaaS 没开 / 没接库」，**不是**「没配过价」。

#### ★★★★ 一条**差点写成假结论**的推断（追到代码才改回来）

我看到 SQL 里 `LEFT JOIN model_credit_rates` 时立刻怀疑：
「价格列会被 NULL 扫描打爆，和 output-compliance 同族」。
**追到 `maas/rates.go:86-94` 才发现 `storedModelRates` 的数值字段是 `*int64`**
⇒ NULL 被正确兜住，**这一族没有那个缺陷**。
⇒ 与 §11.70 的 `session_task_stats` 相反（那边**确实**是裸扫）。
★ **同一条 SQL 形态（LEFT JOIN）在两族里的结论完全相反** ——
这正是「推断必须追到扫描类型」又一条实证。

#### ★★★★ 变异逼出两处判据问题

14. ★★★★ **断言自引用**：M3 把硬编码常量 10000 改成 0，**仍然全绿** ——
    因为我写的是 `expect(b.value).toBe(MAAS_HARDCODED_BASE_IN)`，
    拿**被测常量**比自己。
    ⇒ 改成断言**后端源码里的字面量 10000**，并额外反向钉住常量本身。
    ★ 这是「自引用判据」的又一例：夹具/期望值不能来自被测对象。
15. ★★ **真覆盖缺口**：M6 去掉 `pick()` 里的 `manual` 条件后仍全绿 ——
    因为 `maasDimHasDormantCustom` 并不经过 `maasDimUsesCustom`，
    而 `maasEffectiveSource` 在 `manual=false` 那一侧**一条判据都没有**。
    ⇒ 补了「生效来源三态」那组判据后才红。

#### 本轮门禁（当场实测）

- **变异验证 20/20 有牙**：API 10 条（M1 折扣 0 不归一 / M2 ceil 改 floor /
  M3 硬编码 10000 / M4 image 改回落 out / M5 pick 去掉「值 > 0」/
  M6 pick 去掉 manual / M7 休眠检测恒 false / M8 null 判定恒 false /
  M9 解包不看 settings / M10 自定义价也打折）；
  视图 10 条（V1 不渲染来源 / V2 「没生效」退化成「在用自定义」/
  V3 删休眠提示 / V4 删折扣陷阱 / V5 删硬编码提示 / V6 503 退化 /
  V7 503 退化成空清单 / V8 删无 rate 行提示 / V9 删「只含 active」/
  V10 删兜底标签）。
- 本批两个 spec：**62 用例全绿**（API 38 + 视图 24）。
  ★ `AppDrawer.spec.ts` 11 条仍全绿（白名单已加 `maas-rates`）。
- `npm run build` **rc=0**（含 `vue-tsc -b`）；三门全过
  （css-media **66 文件** / touch-target **63 个 `.vue`** / i18n parity **各 1426 键**）。
- ★ 途中修了三个我自己的问题：
  1. 测试名里嵌套单引号（`是 'text'`）把字符串截断 ⇒ 整个文件解析失败；
  2. 跨 describe 引用了别的块里的局部常量（`allZero`）⇒ ReferenceError；
  3. `noUncheckedIndexedAccess` 打在 `as const` 键表上 ⇒ 补 `?? 0` / `=== true`；
     并删掉一个模板里已改用 CSS 类、因而没被引用的 `sourceTone`。
- 累计（**当场实测**）：**45 视图 / 42 API 模块 / 40 导航席（10 席 superAdmin）**。
- 全量 **10 连跑全绿，1732 用例，0 份失败快照**（`STAB_RC=0`）。

#### 本族未实现的端点（留档）

`maas_handlers.go` 里还有 10 条 superAdmin 只读面未上移：
`settings` / `plans` / `topup-packages` / `tenants/{id}` / `orders` / `orders/{id}` /
`model-rates/{id}`。写操作（batch、batch-reset、batch-fill-global、
settings PUT、model-rates 的 POST/PUT/DELETE）按前几批同口径**一律不碰**。

### 11.74 MaaS 订单上移：这条端点**没有分页**，且列表恒缺支付信息（第三十八轮，superAdmin 档）

新增 `src/api/maas.ts` 的 **orders 段**（追加，不改 rates 段）
+ `src/views/MaasOrdersView.vue`（列表）+ `src/views/MaasOrderDetailView.vue`（详情）
+ 路由 `/maas-orders` 与 `/maas-orders/:id`
+ 抽屉席「MaaS 订单」（**本仓第 11 条 superAdmin 抽屉席**；详情页**不占席**）。

★ 鉴权同上：`admin/maas_handlers.go:14-24` 全部 11 条都是 `h.superAdmin(...)`
⇒ 抽屉席设 `requiresRole: 'super_admin'`，并同步 `AppDrawer.spec.ts` 白名单
（那条白名单式断言**再次正确地抓到了**本轮新增的席）。

#### ★★★★★★ 头号问题：这条端点**根本没有分页**

1. ★★★★★★ `ListOrders(ctx, tenantID, limit)`（`maas/orders.go:221-252`）
    只有**三个参数**，SQL 是 `ORDER BY bo.created_at DESC LIMIT $1`
    —— **没有 OFFSET、没有游标、没有页码**。
    ⇒ 想看更老的订单，唯一办法是**把 limit 调大**。
    ⇒ **页面上放「下一页 / 加载更多」是造一个不存在的功能**：
      列表页据此**刻意不放**任何翻页控件，并有判据钉住这一点
      （V1 注入一个「下一页」按钮 ⇒ 必须红）。

2. ★★★★ 还有**第二层限流**：`maas/orders.go:222-224`

    ```go
    if limit <= 0 || limit > 100 { limit = 20 }
    ```

    ⇒ 有效区间 **1..100**，越界**回落 20**（本仓第六种分页语义）。
    ★ 注意 handler 那一层的 `strconv.Atoi` 是**裸解析不校验**的
      （`maas_handlers.go:617-639`），所以真正把关的是上面这两行。
    ⇒ 客户端**主动**按同规则回落 20，而不是 clamp 到 100 ——
      因为 clamp 出去的值会被后端**悄悄改写**，页面显示的 limit 与实际取数就会对不上。
    ⇒ 列表页的 limit 选择器只给 **20 / 50 / 100** 三个值，全部落在有效区间内。

3. ★★★★★ 响应**只有 `items`**（`writeJSON(w, 200, map[string]any{"items": items})`，
    `maas_handlers.go:638`）：**没有 `total`、没有 `limit` 回显**。
    ⇒ 「还有没有更多」只能靠「**这页排满了**」近似
      （`maasOrdersMaybeMore(items, limit)` = `length >= limit`）。
    ★ `>=` 与 `>` 在 `length === limit` 那一格上给出相反答案 ——
      A9 变异把 `>=` 改成 `>` 最初**仍全绿**，补了「条数正好等于 limit」那条判据才红。

4. ★★★ 列表**跨全部租户**：handler 传 `svc.ListOrders(ctx, "", limit)`
    ⇒ SQL 走 else 分支，**没有 `WHERE bo.tenant_id = …`**。
    ⇒ 看到别的租户的单子**不是越权，是这个端点的设计**，页面明说。

#### ★★★★★ 列表恒缺 `payment_hint` / `stub_mode` —— 端点差异，不是数据缺失

5. ★★★★★ `enrichOrderPaymentHint(ctx, &o)`（`orders.go:216`）**只在 `GetOrder` 里调**；
    `ListOrders` 那一圈**没有**这一行。
    ⇒ 列表里的每一行**恒定**没有 `payment_hint`；
      `stub_mode` 因为是 `bool` + `omitempty`（`orders.go:33-55`），
      **值为 false 时连键都没有**。
    ⇒ 页面若写「这单没有支付信息」就是**在说谎**：
      它必须说「**列表接口不返回这个字段**，去详情页看」。
    ★ 这也是拆成两页的直接理由：**详情页才是唯一的真值来源**。

6. ★★★★ 详情页由此多出两条说明：
    - `stub_mode` 键**缺失**时要说破：「显示『关闭』是按『非 true』推断的，
      **不是**读到了显式的 false」（因为 omitempty 让两者不可区分）；
    - `payment_hint` 缺失显示 `—`（复用 `maas.noValue`），不编。

#### ★★★★ 详情端点：形状不同 + 404 身兼两职 + 一个真契约错配

7. ★★★★ 详情响应是**裸对象**（`writeJSON(w, 200, order)`，`maas_handlers.go:683`），
    **没有** `items`、没有任何包装键 ⇒ **与列表形状不同**。
    ⇒ 两条 `unwrap` 各自独立，并互相加「互喂必须抛错」判据
      （V/A5 两条变异分别钉住两个方向）。

8. ★★★★ 404 **身继两职**：`GetOrder` 出错一律
    `writeError(w, 404, "order not found")`（`maas_handlers.go:679-682`），
    **不区分「订单不存在」与「查询失败」**
    ⇒ 页面 404 时不许只说「订单不存在」，必须并列「也可能是查询本身失败了」。

9. ★★★★★ **本轮查实并修掉的真契约错配**（客户端自己的 bug）：
    后端是 `id, err := strconv.ParseInt(parts[0], 10, 64)`（`maas_handlers.go:653`）
    ⇒ **只吃纯十进制数字**。而详情页原本用 `Number(trimmed)` 解析，
    `Number()` 会把 `'1e3'` / `'7.0'` / `'0x10'` / `'+7'` **解析成整数**
    ⇒ 这四种串**本地守卫放行、发出去后端必回 400 `invalid order id`**。
    ⇒ 守卫改成先卡 `/^\d+$/` 再谈数值，并补四种形态的判据
      （V11 变异把正则退回 `Number()` ⇒ 必须红）。
    ★ 这类错配**只有把前端的解析规则和后端的解析规则并排读**才会暴露 ——
      两边都「看起来对」。

10. ★★ `svc == nil || !Enabled()` ⇒ **503 `database not configured`**
    ⇒ 是「MaaS 没开 / 没接库」，**不是**「没有订单」。页面不许退化成空清单。

#### ★★★ 其余三条

11. ★★★ `amount_cents` 单位是**分** ⇒ 显示换算成元。
    ★ **不是**乘 `cents_per_credit`——那是**积分**的单价（settings 里另有的字段）。
12. ★★★ `plan_name` / `package_name` 是 `COALESCE(sp.name,'')` 出来的
    （`orders.go:233` / `:247`）⇒ **空字符串表示 LEFT JOIN 未命中**，
    即关联的套餐/包**已被删**。判据「有 id 但名字是空串 = 孤儿」；
    且 `plan_id`/`package_id`/`paid_at` 都是**指针 + omitempty**，键可能整个不存在
    ⇒ 没 id 的不算孤儿。
13. ★★ 枚举全部实读后端：`OrderType` = `subscribe`/`topup`（orders.go:18-20）、
    `OrderStatus` = `pending`/`paid`/`cancelled`/`expired`（:26-30）、
    `PaymentChannel` = `alipay`/`wechat`/`manual`（payment.go:12-14）。
    页面遇到**枚举外**的值原样显示并打 info 色调，**不猜也不吞**。

#### 本轮门禁（当场实测）

- **变异验证 23/23 有牙**：API 10 条（A1 limit clamp / A2 limit≤0 不回落 /
  A3 列表解包不看 `items` 键 / A4 详情解包只看一个键 / A5 列表形状喂详情 /
  A6 金额不换算 / A7 缺 `payment_hint` 判据反向 / A8 孤儿判定丢掉 id 那一半 /
  A9 排满判定 `>=` 改 `>` / A10 id 守卫放宽）；
  视图 13 条（V1 **注入一个假「下一页」按钮** / V2 排满提示恒不出现 /
  V3 删「没有分页」说明 / V4 把端点差异说成数据缺失 / V5 金额不换算 /
  V6 孤儿标签不出现 / V7 删跨租户说明 / V8 503 退化 /
  V9 删 404 的「查询失败」那半句 / V10 删 stub 键缺失说明 /
  V11 **id 守卫退回 `Number()`** / V12 删「只有详情返回支付信息」/
  V13 未支付时留空）。
- 本批三个 spec：**99 用例全绿**（API 70 + 列表视图 23 + 详情视图 16）。
  ★ `AppDrawer.spec.ts` 11 条仍全绿（白名单已加 `maas-orders`）。
- `npm run build` **rc=0**（含 `vue-tsc -b`）；三门全过
  （css-media **68 文件** / touch-target **65 个 `.vue`** / i18n parity **各 1476 键**）。
- ★ 途中修了四个我自己的问题：
  1. orders 段测试忘了 `import { fetchMaasOrders }` ⇒ 4 条 ReferenceError；
     补 import 时又误加了已存在的 `maasOrdersMaybeMore` ⇒
     **整个文件 `ParseError: already been declared`、具名失败列表为空** ——
     典型「rc≠0 却抓不到用例名」，先查收集失败再谈判据；
  2. 详情模板把 `<div class="mo__grid">` 闭合成了 `</p>` ⇒ `Invalid end tag`；
     ★ 而 `vue-tsc -b` 那次**是绿的**（增量缓存）—— 只有真跑 vitest 才炸出来；
  3. 「不显示空名」那条判据写成全页 `not.toContain('订阅套餐: ')`
     ⇒ 被**标签前缀**喂饱 ⇒ 改成按节点取值 `.mo__name-v`
     （这正是「按节点作用域断言」那条纪律的第三次应用）；
  4. `order() as Record<string, unknown>` 报 TS2352 ⇒ 需经 `unknown` 中转。
- 累计（**当场实测**）：**47 视图 / 42 API 模块 / 41 导航席（11 席 superAdmin）**。
- 全量 **10 连跑全绿，1803 用例，0 份失败快照**（`STAB_RC=0`，10/10 `passed (1803)`）。
  ★ 累计未定位的 flaky 仍**未捕获**：全量无污染跑 ≥116 次、失败 1 次（≈0.9%），
  本批十连跑未复现 —— 这**不等于「已修复」**；`test:stability` 会在下次失败时落盘留证。

#### 本族仍未上移的端点（留档）

`maas_handlers.go` 里还有 7 条 superAdmin 只读面未上移：
`settings` / `plans` / `topup-packages` / `tenants/{id}` / `model-rates/{id}`。
写操作（`settings PUT`、`model-rates` 的 POST/PUT/DELETE、batch 系列、
`POST /orders/{id}/confirm`）按前几批同口径**一律不碰**。

### 11.75 MaaS **租户/客户面**上移：同域**不同档**，且有一条 GET **会写库**（第三十九轮，admin 档）

新增 `src/api/maas.ts` 的**第三段**（坑 19~24，**不改动**前两段）
+ `src/views/MaasCatalogView.vue` + `src/views/MaasWalletView.vue`
+ 路由 `/maas-catalog`、`/maas-wallet`
+ **两条 admin 档抽屉席**（「MaaS 目录」「MaaS 钱包」，**不设** `requiresRole`）。

#### ★★★★★★ 先纠正两节自己的错：这里本来是**另一档**的端点

1. ★★★★★★ §11.73 / §11.74 的「本族未实现的端点」把
    `model-rates/{id}` 列成 **superAdmin 只读面** —— **这是错的**：
    `handleMaasModelRateByID`（`maas_handlers.go`）只有
    `PUT` / `PATCH` / `DELETE` **三个 case，没有 GET**
    ⇒ 它是**纯写端点**，按前几批同口径**根本不该上移**。
    ★ **教训**：留档里的端点清单必须**逐条核过 method**，不能按 URL 形状猜。
2. ★★★★★ 清单漏掉了**整整一族**：`maas_handlers.go:26-30` 还注册了
    **5 条 `h.admin(...)` 的端点**，它们**不在** `/api/admin/maas/` 下：

    ```go
    mux.HandleFunc("/api/maas/settings",         h.admin(h.handleMaasPublicSettings))
    mux.HandleFunc("/api/maas/models",           h.admin(h.handleMaasPublicModels))
    mux.HandleFunc("/api/maas/plans",            h.admin(h.handleMaasPublicPlans))
    mux.HandleFunc("/api/maas/topup-packages",   h.admin(h.handleMaasPublicTopup))
    mux.HandleFunc("/api/maas/wallet",           h.admin(h.handleMaasWallet))
    ```

    ⇒ 同一个 `maas_handlers.go` 里**两档并存**：`/api/admin/maas/**` 全 superAdmin，
      `/api/maas/**` 全 admin。
    ★★ 接线后果直接相反：superAdmin 席必须设 `requiresRole: 'super_admin'`，
      这两条**设了就会把 tenant_admin 挡在门外**（点进来 403）。
      判据 B15 就是把这条席误设成 super_admin ⇒ 必须红。

#### ★★★★★★ public settings 只有 3 个键，且**互喂抛错这条判据不成立**

3. ★★★★★★ `/api/maas/settings` 只吐三个键
    （后端自陈 `// Tenants see conversion knobs only, not internal cost data`）：

    ```go
    writeJSON(w, 200, map[string]any{
        "cents_per_credit":    st.CentsPerCredit,
        "base_credits_per_1m": st.BaseCreditsPer1M,
        "currency_display":    st.CurrencyDisplay,
    })
    ```

    ⇒ 租户**看不到** `global_discount`、也看不到 `base_credits_per_1m_in`。
    ★★ 而 `base_credits_per_1m` 是**旧字段**（§11.73 的 `maasGlobalBaseIn`
    是先看 `_in` 再回落它）⇒ **租户面显示的基价可能不是生效基价**。
4. ★★★★★ **「把 admin 全量 Settings 喂给租户解包器应当抛错」这条判据写不出来**：
    admin 的 `Settings`（12 键）是租户面这 3 键的**超集**
    ⇒ 任何「这 3 键都在？」的检查都**必然**接受它。
    ⇒ 真正该守的是**另一头**：`unwrapMaasPublicSettings` 改成
    **显式投影**这 3 个键，多出来的**一个都不带出去**，
    于是页面读不到 `global_discount`（B1 变异取消投影 ⇒ 必须红）。
    ★ 这是本轮又一条「自造纪律」：**「互喂必须抛错」不是万能的**，
      它要求两个形状**互不包含**；一旦是子集关系，就得换成投影式判据。

#### ★★★★★ `/api/maas/models` 是**另一个结构**，只有 4 维

5. ★★★★★★ 行类型是 **`ModelRateRow`（maas/service.go:536，12 键）**，
    与 admin 档的 **`AdminModelRateRow`（model_rates.go:11-36，30 键）**
    是**两个不同的 Go struct**：

    | | admin 档 | 租户面 |
    |---|---|---|
    | 维度 | **7**（含 image/audio/video） | **4**（in/out/cache_in/cache_out） |
    | manual_* / custom_* / is_custom | 有 | **无** |
    | updated_at | 有 | **无** |
    | canonical_id | 有 | **无** |
    | 排序 | `mc.canonical_name` | **vendor 再 canonical_name** |

    ⇒ ★★★ 拿 admin 那套七维判读去读它，五个维度**读到 undefined**，
      再被 `?? 0` 渲染成 0 ⇒ 看起来像「这几维免费」。
    页面只画 4 维，并有判据钉住「表格恰好 4 行、且不出现图像/音频/视频」
    （B9 把维表扩回 7 个 ⇒ 必须红）。
6. ★★★ `billing_mode` 是后端**硬编码字面量** `"token"`，永远不会变。
7. ★★ `family` / `family_display_name` / `context_window` 是**指针 + omitempty**
    ⇒ 键可能整个不存在（与 admin 档「无 omitempty、值可能 null」**正好相反**）。

#### ★★★★ 模态：**盖章 vs 猜**这件事在响应里被抹掉了

8. ★★★★ 租户面走 `catalog.EffectiveModality(name, stored, modality_source)`
    （catalog/display.go:220）—— 与 admin 档那条 `COALESCE(…,'text')`
    **完全是两套逻辑**：
    - `modality_source` 是 semantic / manual ⇒ **原样返回 stored**
      （按名字猜的结果**没资格推翻**盖章值）；
    - 否则先放行 `{multimodal, vision, audio, embedding, video}`，
      **再**按名字猜，最后才回 stored / `'text'`。
    ★★ 后果：**`modality='text'` 且未盖章的 `gemini-*` 会被报成 `multimodal`**
      （`text` 不在放行名单里 ⇒ 掉进按名推断分支）。
      这正是该函数注释警告的「把『核实判负降级成 text』与
      『运维手工设成 text』双双翻回 multimodal」。
9. ★★★★ `vendor` 也**不同源**：`catalog.ResolveVendor`（display.go:118）是
    `dbVendor → familyVendor 映射 → **按名字推断** → HumanizeFamilyID → '其他'`
    ⇒ **比 admin 档多一层「按名字推断」**。
10. ★★★★ 而 `ModelRateRow` **没有** `modality_source` 字段
    ⇒ **租户无从分辨**这个模态是盖过章的库值还是猜出来的。
    ⇒ 页面明说「这一栏不要当成配置好的模态来解读」（B11 把这句换成声称配置值 ⇒ 必须红）。

#### ★★★★★★ 钱包：一条 **GET 会写库** 的端点

11. ★★★★★★ `GetWallet`（maas/service.go:516）**第一行就是写**：

    ```go
    _ = s.ensureWalletDirect(ctx, tenantID)   // INSERT … ON CONFLICT DO NOTHING
    ```

    ⇒ 这是一个「**看着只读、实际会建行**」的端点；
      第一次打开钱包页就会给该租户插一行 `tenant_credit_wallets`。
    ⇒ 页面明说「它不是『刷新绝不改数据』的接口」（B16 删掉这句 ⇒ 必须红）。
12. ★★★★ `tenantID = GetTenantID(r)` ⇒ **只看本租户**，
    与 superAdmin 侧 `ListOrders(ctx, "", …)` 的**跨租户语义正好相反**。
13. ★★★★ `balance_credits` **不是原始列**：
    `if w.BalanceCredits == 0 { w.BalanceCredits = Granted + Purchased }`
    ⇒ 列值是 0 时会被两个余额之和**顶替**。
14. ★★★★ `total_available = quota_remaining + granted + purchased`
    ⇒ 把**订阅额度**（请求次数）与**积分余额**（两种不同单位）**加在一起**。
15. ★★★ `subscription` 是 `*SubscriptionView` + omitempty
    ⇒ 没有生效订阅时**键整个不存在**，
    与「有订阅但 `status` 不是 `active`」是**两回事**，页面分开说
    （B18 把两句文案换成同一句 ⇒ 必须红）。

#### ★★★ plans / topup-packages

16. ★★★ 走 `ListPlans(ctx, enabledOnly=true)` / `ListTopupPackages(ctx, true)`
    ⇒ **只列 `enabled = TRUE`**（admin 档那两个传 `false` ⇒ 含停用行）。
17. ★★ 返回 `jsonSlice(out)`（maas/json_slice.go:4-9），nil 切片换成 `[]T{}`
    ⇒ **`items` 永远是数组，永不为 `null`**；客户端可以直接用 `.length`。
18. ★ `Plan` / `TopupPackage` 是**无 omitempty** 的普通 struct
    ⇒ 8 个键**一定都在**（连 `enabled: false` 都有键）。

#### 本轮门禁（当场实测）

- **变异验证 19/19 有牙**：B1 取消 settings 投影 / B2 少一键也放过 /
  B3 「三维不存在」判据反向 / B4 wallet 解包不看 `tenant_id` /
  B5 兜底顶替判据恒 false / B6 总额混合判据恒 false / B7 单价 0 积分不判 null /
  B8 「无订阅」判据恒 true / **B9 把维表扩回 7 维** / B10 删「只有 4 维」提示 /
  B11 把模态说成配置值 / B12 删「只返回 3 个键」 / B13 积分为 0 仍显单价 /
  B14 503 不收敛 / **B15 抽屉席误设 super_admin** / B16 删「GET 会写库」/
  B17 删「总额混合两种单位」/ B18 两句文案混用 / B19 抛错退化成余额 0。
  全部红在**具名**断言上，还原后逐字节一致。
- 本批三个 spec：**126 用例全绿**（API 96 + 目录视图 17 + 钱包视图 16），
  另有 `AppDrawer.spec.ts` 11 条（两条 admin 席**不该**动白名单，验过确实没红）。
- ★ **顺带补上一处前几轮留下的真缺口**：`verify-i18n-parity` 报告
  **29 处**动态前缀「本门未覆盖」，而 `dynamicKeys.spec.ts` 只登记了 **14** 条
  ⇒ §11.73 的 `maas.dim_` / `maas.src_` 一直没被动态键判据覆盖。
  本轮把 MaaS 一族 6 条全部登记（`maas.dim_` / `maas.src_` / `mo.type_` /
  `mo.status_` / `mo.channel_` / `mp.dim_`），
  阈值 14 → 20，用例 **124 → 170**。
  ★ 维表与后缀**从 API 模块的常量 import**，不手抄（手抄的那份会漂）。
- `npm run build` **rc=0**（含 `vue-tsc -b`）；三门全过
  （css-media **70 文件** / touch-target **67 个 `.vue`** / i18n parity **各 1526 键**）。
- ★ 途中修了五个我自己的问题：
  1. `MaasWalletView.vue` 漏写 `</script>`，把 `</style>` 接在了 `<template>` 前面
     ⇒ 445 条解析错误；`vue-tsc -b` 是绿的，**只有真跑 vitest 才炸出来**；
  2. 视图里混用了 Vue 2 Options API 的第二个 `<script>` 块 ⇒ 改回 setup 内 const；
  3. `m[d]` 直接按维度名索引（响应键其实是 `credits_per_1m_in`）
     ⇒ 改成字段映射表 + 显式兜底；
  4. 一条判据用全页 `.mp__unit` 取值，被**充值包那一块的合法行**喂饱
     ⇒ 改成按面板作用域取值（这已是本轮第三次应用「按节点作用域断言」）；
  5. 变异脚本里 B15 的 `file` 写成了钱包视图，实际目标在 `appNav.ts`
     ⇒ 注入没施上却只显示「没施上」；同时修掉一个 python 批改脚本
     **中途抛错导致整份不落盘**（B13/B4 的改动一起丢）的坑。
- 累计（**当场实测**）：**49 视图 / 42 API 模块 / 43 导航席（11 席 superAdmin）**。
- 全量 **10 连跑全绿，1905 用例，0 份失败快照**（`STAB_RC=0`，10/10 `passed (1905)`）。
  ★ 累计未定位的 flaky 仍**未捕获**（无污染跑 ≥126 次、失败 1 次，≈0.8%），
  本批十连跑未复现 —— 这**不等于「已修复」**。

#### 仍未上移的 MaaS 端点（留档，**已逐条核过 method**）

- **admin 档**（5 条读面，已全部上移）：settings / models / plans /
  topup-packages / wallet。
- **superAdmin 档只读面（剩 5 条）**：
  `GET /api/admin/maas/settings`（裸全量 `Settings`）、
  `GET /api/admin/maas/plans`（**含停用**行）、
  `GET /api/admin/maas/topup-packages`（同上）、
  `GET /api/admin/maas/tenants/{code}/wallet|account|usage/summary|usage/detail|ledger`
  （**5 条读动作**，注意 `/tenants/{code}/…` 至少要两段路径，否则 404）。
- ★ **`/api/admin/maas/model-rates/{id}` 不是只读面**：只有 PUT / PATCH / DELETE。
- 写操作一律不碰：settings PUT、model-rates 的 POST/PUT/DELETE/PATCH、batch 三条、
  `POST /orders/{id}/confirm`、`tenants/{code}/adjust|grant`。

### 11.76 MaaS **superAdmin 租户运维面**上移：days **决定读哪张表**（第四十轮，superAdmin 档）

新增 `src/api/maas.ts` 的**第四段**（坑 25~36）
+ `src/views/MaasTenantOpsView.vue` + `src/views/MaasAdminCatalogView.vue`
+ 路由 `/maas-tenant-ops`、`/maas-admin-catalog`
+ **两条 superAdmin 抽屉席**（⇒ 第 12、13 条 superAdmin 席）。

本批 8 条只读端点（全部 `h.superAdmin(...)`）：
`settings` / `plans` / `topup-packages` /
`tenants/{code}/wallet|account|usage/summary|usage/detail|ledger`。

#### ★★★★★★ 头号问题：days **决定读哪一张物理表**

1. ★★★★★★ `requestLogsSource(days)`（maas/usage.go）：

    ```go
    func requestLogsSource(days int) (string, string) {
        if days <= 7 { return "request_logs_hot AS r", "r" }
        return "request_logs_with_current_month AS r", "r"
    }
    ```

    ⇒ `days=7` 与 `days=8` 读的是**两张不同的表**。
    ★ 这**不是**「窗口更长」，是**换了数据源** —— 两张表的保留期与新鲜度都可能不同，
      跨过 7 这个数时**数据口径会变**（同一时刻两个窗口的数字可能对不上账）。
    ⇒ 页面**必须**把「本次读的是哪张表」显式渲染出来，跨过边界时额外警告。
2. ★★★ `days` 的选值刻意给 **1 / 7 / 30 / 90**，`7` 用虚线边框标出是**换表边界**。

#### ★★★★★ 三套限幅各不相同，且 usage 的**两端不对称**

3. ★★★★★ `ClampUsageDays`：`days < 1 ⇒ 1`、`days > 90 ⇒ 90`（**两端都 clamp**）。
4. ★★★★★ `ClampUsageLimit`：`limit < 1 ⇒ **回落 10**`、`limit > 50 ⇒ 50`
    ⇒ ★★ **两端不对称**：下界是**回落**到默认值 10，不是 clamp 到 1。
5. ★★★ `ListLedger`：`limit <= 0 || limit > 200 { limit = 50 }` ⇒ **回落 50**。

    | | 下界 | 上界 |
    |---|---|---|
    | `ClampUsageDays` | clamp 到 1 | clamp 到 90 |
    | `ClampUsageLimit` | **回落 10** | clamp 到 50 |
    | `ListLedger` | **回落 50** | **回落 50** |

    ⇒ 这是本仓**第七种**分页语义。三处放在同一页上时，页面把三套生效值一起打出来
      （变异 C3 / C4 各自钉住一条）。
6. ★★★ handler 侧三个 query 参数都是
    `if n, err := strconv.Atoi(v); err == nil { x = n }`
    ⇒ **解析失败静默沿用默认值**（days=7 / limit=10 / ledger limit=50），**不是 400**。
7. ★★ 与订单段正相反：`UsageSummary` **回显 clamp 后的 `days`**
    ⇒ 客户端**能**核对生效值，页面把回显值渲染出来（变异对照）。

#### ★★★★ 收入与毛利是**算出来的**，客户端要能核对

8. ★★★★ `QueryConsumptionDetail` 逐行算：

    ```go
    row.TenantRevenueUSD = float64(row.CreditsCharged) * centsPerCredit / 100
    row.GrossMarginUSD   = row.TenantRevenueUSD - row.UpstreamCostUSD
    if row.TenantRevenueUSD != 0 { row.GrossMarginRate = row.GrossMarginUSD / row.TenantRevenueUSD }
    ```

    而 `centsPerCredit` 是 `SELECT … FROM maas_settings WHERE id = 1` 直读并**回显**
    ⇒ ★★ 响应里带着复算所需的全部输入，客户端**独立复算**，
      对不上就标出来（变异 C7 把 `/100` 去掉 ⇒ 红）。
9. ★★★★ ★★★ `gross_margin_rate` 在**零收入**时**保持零值 0**（上面那个 `if` 不进）
    ⇒ 响应里「rate = 0」有**两种**含义：真的是零毛利，**或者**根本没收入
      （此时 rate **无定义**），两者**不可区分**。
    ⇒ 页面在这行显示「（无收入 · 无定义）」而不是 `0.0%`。
10. ★★★ `cost_usd` / `total_cost_usd` 是 **float64 + omitempty**
    ⇒ **成本恰好为 0 时键整个不存在**。
    ⇒ 页面必须显示 **0**，而不是「—」—— 否则「真·零成本」被误报成「数据缺失」。

#### ★★★ 一个新的收入侧漏点被测了出来

11. ★★★ `cancelled_billed_requests` 是 SQL `FILTER` 出来的计数：

    ```sql
    COUNT(*) FILTER (WHERE COALESCE(credits_charged,0) > 0
                       AND COALESCE(stream_interrupted,false)
                       AND lower(COALESCE(failure_detail_code, error_kind,''))
                           IN ('client_cancel','client_disconnected'))
    ```

    ⇒ 「**已扣积分但被客户端取消**」的请求数。
    非零时页面明确点出这是收入侧的已知漏点（变异 C16）。

#### ★★★ 三种「键缺失」语义并存

12. ★★★ `ConsumptionDetailRow` **同一个结构里 omitempty 混用**：
    `owner_user` / `provider_id` / `credential_id` / `canonical_id` 带 omitempty
    （指针 ⇒ 仅 `nil` 时省略）；其余 **15 个键无** omitempty ⇒ 一定存在。
13. ★★★ 而 `LedgerEntry` 的 `pool` / `ref_type` / `ref_id` 是**指针但无** omitempty
    ⇒ **键一定在、值为 `null`**。

    | 结构 | 指针字段 | nil 时 |
    |---|---|---|
    | `ConsumptionDetailRow` | 带 omitempty | **缺键** |
    | `LedgerEntry` | **无** omitempty | 键在，值 `null` |
    | `Settings` | 全无 omitempty | 12 键一定都在 |

    ⇒ **三套判读，不能一套通吃**。页面据此分别处理。

#### ★★ 其余三条

14. ★★ `tenant_id` 为空 ⇒ service 直接 `fmt.Errorf("tenant_id required")`
    ⇒ handler 走 `writeInternalErr` ⇒ **500**（不是 400）。
    而 `/tenants/` 后面**少于两段路径**时是 **404 `not found`**。
15. ★★ 租户码里的 `/` 必须 `encodeURIComponent`
    ⇒ 否则后端 `strings.Split(rest, "/")` 会把 `a/b` 劈成两段 ⇒ 404（变异 C8）。
16. ★ admin 档的 plans / topup 传 `enabledOnly=false` ⇒ **含停用行**，
    与 §11.75 的租户面（`true`）**响应形状相同、内容不同**；
    配置面页面把停用行打标签并压暗，并说明「**租户那边根本列不出来**」。

#### 本轮门禁（当场实测）

- **变异验证 22/22 有牙**：C1 换表边界改成 30 / C2 换表判据恒 true /
  C3 usage 下界改成 clamp 1 / C4 ledger 改成 clamp 200 /
  C5 cost 缺失不再读 0 / C6 「零收入无定义」恒 false / C7 revenue 漏 /100 /
  **C8 租户码不 encode** / C9 summary 不校验 trend / C10 account 不校验 wallet 形状 /
  C11 admin settings 不校验 `global_discount` / C12 换表提示不渲染 /
  **C13 抽屉席误改成 admin 档** / **C14 毛利率退化成正常百分比** /
  C15 收入核对告警不渲染 / C16 取消计费提示不渲染 / **C17 空租户码不再本地拦截** /
  **C18 catch 里不清空旧数据** / C19 折扣陷阱提示不渲染 /
  C20 停用行不打标签 / C21 硬编码基价提示不渲染 / C22 租户看不到成本数据的说明不渲染。
- 本批三个 spec：**166 用例全绿**（API 131 + 租户运维视图 21 + 配置面视图 16）；
  `AppDrawer.spec.ts` 11 条（白名单加了 `maas-tenant-ops` / `maas-admin-catalog`）、
  `dynamicKeys.spec.ts` 181 条仍全绿。
- `npm run build` **rc=0**（含 `vue-tsc -b`）；三门全过
  （css-media **72 文件** / touch-target **69 个 `.vue`** / i18n parity **各 1607 键**）。
- ★ 途中修了四个我自己的问题：
  1. 一条判据写成整页 `toContain('无定义')` ⇒ 被**上方提示文案**（「毛利率是**无定义**」）
     喂饱 ⇒ **恒真，永不红**。改成按 `.mt__cell` 取值后才在 C14 变异下变红
     （这是「正向 `toContain` 被散文喂饱」那条纪律的又一次实证）；
  2. 一条「抛错时面板必须清空」的判据只在**首屏就失败**时验证 ⇒
     首屏本来就是 null，**删掉 catch 里的清空语句照样全绿** ⇒ 补了
     「先查成功 → 再查失败」的序列判据；
  3. 三条判据期望值写错（`undefined` 第三参、`$` 前缀、「2 条」实际 1 条）；
  4. 一条「nil ⇒ 缺键」判据在**自己写的 JS 字面量**上用 `in` 断言
     ⇒ Go 的 omitempty 根本没参与，必然为 true。改成造 Go 实际吐出的形状。
- 累计（**当场实测**）：**51 视图 / 42 API 模块 / 45 导航席（13 席 superAdmin）**。
- 全量 **10 连跑全绿，1976 用例，0 份失败快照**（`STAB_RC=0`，10/10 `passed (1976)`）。
  ★ 累计未定位的 flaky 仍**未捕获**（无污染跑 ≥136 次、失败 1 次，≈0.7%），
  本批十连跑未复现 —— 这**不等于「已修复」**。

#### MaaS 一族上移进度（截至本节）

- **admin 档**（`/api/maas/**`，5 条读面）：**已全部上移**（§11.75）。
- **superAdmin 档**（`/api/admin/maas/**`）：**只读面已全部上移** ——
  model-rates（§11.73）、orders/orders/{id}（§11.74）、
  admin 租户面（§11.75）、settings + plans + topup-packages +
  tenants/{code}/五条（**本节**）。
- ★ `/api/admin/maas/model-rates/{id}` **不是只读面**（只有 PUT/PATCH/DELETE）。
- 写操作全部未上移（按前几批同口径）：settings PUT、model-rates 增删改、
  batch 三条、`POST /orders/{id}/confirm`、`tenants/{code}/adjust|grant`。

#### 下一批候选（已逐条核过 method）

1. `GET /api/admin/maas/tenants/{code}/usage/**` 已做完 ⇒ 剩下的 superAdmin 面
   就是 `admin/attachments`(8, admin)、`admin/modules`(7)、
   `admin/logs`(7, 混合读写)、`system/session-context`(6)、
   `admin/tenants`(25, 多为 superAdmin)。
2. ★ `admin/tenants` 那 25 条里多为 superAdmin，且很可能带**租户筛选语义**
   ⇒ 与本批的「跨租户 vs 本租户」那组对照值得单独一节。

### 11.77 租户名录上移：一条「**看着有数据、其实可能没算完**」的列表（第四十一轮，superAdmin 档）

新增 `src/api/tenants.ts`（**新模块**）+ `src/views/TenantsView.vue`
+ `src/views/TenantDetailView.vue`
+ 路由 `/tenants`、`/tenant-detail/:code`（详情页**不占**抽屉席）
+ **第 14 条 superAdmin 抽屉席**。

5 条只读端点：`/tenants`（列表，可带 `?status=`）、`/{code}`、`/{code}/users`、
`/{code}/keys`、`/{code}/stats?days=`。

#### ★★★★★★ 头号问题：五条端点**全部返回裸结构**

1. ★★★★★★ `admin/handler.go:926-927` 两条注册都是 `h.superAdmin(...)`
    ⇒ **superAdmin 档**。
    ★★ 而且 handler **内部还有第二道**校验：

    ```go
    if auth := GetAuthContext(r); auth != nil &&
       auth.Role != "super_admin" && auth.Role != "admin_key" { 403 }
    ```

    ⇒ **`admin_key` 这个角色也放行**，它是中间件那道 `requiresRole`
    **没有建模**的角色 ⇒ 移动端**无法**只用角色字符串判权限，只能靠后端 403。
2. ★★★★★★ 响应形状与本仓多数 admin 端点**都不同**：

    | 端点 | 形状 |
    |---|---|
    | `GET /tenants` | **裸数组** |
    | `GET /tenants/{code}` | **裸对象** |
    | `GET /tenants/{code}/users` | **裸数组** |
    | `GET /tenants/{code}/keys` | **裸数组** |
    | `GET /tenants/{code}/stats` | **裸对象** |

    ⇒ 误按 `{items: […]}` 解包，这四条端点对真后端 **100% 抛错**。
3. ★★ `keys` 是 **按 id DESC**（最新在前），`users` 是 **按 id 正序** ——
    两个列表**排序方向相反**，不要照抄同一个 sort 逻辑。

#### ★★★★★★ 「近 7 天用量」这一列**可能不可信**

4. ★★★★★★ `attachTenantUsage7d`（`admin/tenants.go`）给富化单独留了
    **1.5 秒**预算（`usageCtx` 派生自 `r.Context()`，好让列表主查询的
    `cancel()` 不会连带掐掉它），查询失败/超时时：

    ```go
    slog.Warn("tenants 7d usage enrichment failed; fields left at zero", …)
    return          // ← 四个字段留在 0，客户端拿不到任何信号
    ```

    ⇒ ★★★ 客户端**分辨不出**「这个租户真没用量」与「富化没在 1.5s 内跑完」。
    源码注释记录了根因：这条聚合在真实数据量下要 **20s+**
    （`request_logs_with_current_month` 7 天 32 万行），而原先与主查询共用 5s，
    于是每次打开列表先白等满 5 秒、用量列永远为空（实测 `duration_ms=5001`，HTTP 仍 200）。
    ⇒ 页面文案必须是「**读数不可信**」，**不能**是「这段时间没有用量」——
      后者是一个**看起来很确定、其实可能错**的结论。
5. ★★★★ `tenantInfo` 的 **7 个聚合字段全是 `omitempty`**：

    ```go
    UserCount     int     `json:"user_count,omitempty"`
    APIKeyCount   int     `json:"api_key_count,omitempty"`
    Requests7d    int64   `json:"requests_7d,omitempty"`
    Tokens7d      int64   `json:"tokens_7d,omitempty"`
    Credits7d     int64   `json:"credits_7d,omitempty"`
    Cost7d        float64 `json:"cost_7d_usd,omitempty"`
    TotalRequests int64   `json:"total_requests,omitempty"`
    ```

    ⇒ 「键不存在」至少有三种成因：真的为 0 / 被 omitempty 省掉 / 富化降级留下的 0。
6. ★★★ **详情页的聚合比列表页更不可信**：`getTenant` 的五个计数全是
    `_ = h.db.QueryRow(...)` ⇒ **连一行日志都不留**地吞掉错误。
    ⇒ 列表与详情的「0」**可靠性不同**，页面不能一视同仁（两处文案因此不同）。

#### ★★★★ `stats` 会返回 **504** —— 本仓第一次

7. ★★★★ `writeTenantStatsError`：`context.DeadlineExceeded` ⇒
    **504 Gateway Timeout**，报文
    `tenant stats query timed out; retry with a smaller days window`
    ⇒ ★★ 这是「查询**超时**、调小窗口重试」，**不是**「查不到」，
    也不是「这个租户没用量」。页面必须**单列**这一档。
8. ★★★ `days`：`< 1 ⇒ 7`、`> 365 ⇒ 365`；且 `strconv.Atoi` 失败时 `days=0`
    ⇒ 落进 `< 1` ⇒ 仍是 7 ⇒ **又一套限幅**（本仓第八种，语义是「回落 7」）。
9. ★★ `UsageSummary` 同族那个「回显 clamp 后 days」的好习惯在这条上**没有** ——
    `stats` 的 `days` 是**直接赋值**的（`s.Days = days`，在 clamp 之后），
    所以客户端能核对（页面把「前端选 N / 后端回显 M」并排打出来）。

#### ★★★★ 成本与积分**来自两张不同的表**

10. ★★★★ 同一个响应里：

    | 字段 | 来源 |
    |---|---|
    | `total_requests` / `total_tokens` / `total_cost_usd` / `unique_keys` / `unique_models` / `unique_apps` | `usage_ledger_with_current_month` |
    | `total_credits` / `input_tokens` / `output_tokens` / `cache_read_tokens` / `cache_write_tokens` / `avg_latency_ms` | `logsTable`（`request_logs_hot` ∪ `request_logs` 的 UNION ALL 子查询） |

    ⇒ 「收入侧」与「上游成本侧」口径不同，**两者对不上是可能的，不是 bug**。
11. ★★★ R36-B4 记着这条的来历：原先两个 totals 查询都是 `_ =`，
    于是「上下文已死 / statement_timeout」会**静默发布 0**，
    与后续查询的结果**自相矛盾**，读起来就是「这个租户什么都没烧」。
    现在改成 `writeTenantStatsError` **响亮失败**。
    ⇒ ★ 这是本仓「`_ =` 吞错 ⇒ 内部自相矛盾的 200」的又一例，
      与 §11.70 的**静默丢行**同族、方向相反（那个是少行，这个是假 0）。

#### ★★★★ 同一个产品里**两套日切**，且是有意分叉

12. ★★★★ `stats.daily` 的日切是
    `date_trunc('day', ts AT TIME ZONE 'Asia/Shanghai')`（R36-A3 **显式钉死**），
    而对账 / 结算页保持**显式 UTC 日**。
    源码自陈「对账页保持显式 UTC 日（结算口径，**有意分叉**）」。
    ⇒ 跨零点的差异**不是数据错**，页面必须写出来，否则运营会去查「假 bug」。
13. ★★★ `daily` 用 `generate_series` **补零** ⇒ 恒有 `days` 条连续日期。
    ⇒ 页面据此判「完整，可直接画」；条数少于 `days` ⇒ 服务端降级过，
      **不能画**（画了会骗人）。

#### ★★★ 其余三条

14. ★★★ 三种「键缺失」语义并存：
    `userInfo.last_login_at` 是 `*time.Time` 但**无** omitempty ⇒ 键在值为 `null`
    = **从未登录**；而 `tenantKeyInfo` 的 `key_alias` / `owner_user` /
    `application_code` / `expires_at` 都**带** omitempty ⇒ 键可整个不存在，
    其中 `expires_at` 缺失 = **永不过期**（不是「没查到到期时间」）。
15. ★★ 子资源路由有历史坑：`sub` 是 `SplitN(path,"/",2)` 的**尾部**，
    所以 `/model-policies/audit` 的 `sub` 是 `"model-policies/audit"`；
    2026-06-23 曾因此让 `/check` 与 `/audit` 都报
    `unknown sub-resource`，现已由 `isModelPoliciesSubResource` 按前缀匹配修掉。
    未知子资源 ⇒ **404 `unknown sub-resource: <sub>`**；`h.db == nil` ⇒ **503**。
16. ★ 六处聚合都用 `warnRowSkip` + `continue` ⇒ **静默丢行**
    （`tenants.list` / `listUsers` / `listKeys` / `stats.byModel` / `byApplication` / `daily`）
    ⇒ 返回条数**可能**少于库里真实行数，页面不能把条数当「全量」。

#### 本轮门禁（当场实测）

- **变异验证 22/22 有牙**：D1/D2 裸数组解包改成只认 `{items}` /
  D3 「读数不可信」判据恒 false / D4 缺失键清单恒空 / **D5 「从未登录」改查键存在**
  （Go 那边 key 一定在 ⇒ 永远 false）/ **D6 「永不过期」判据反向** /
  D7 daily 完整性恒 true / D8 stats 下界改成 clamp 1 / D9 租户码不 encode /
  D10 stats 不校验 daily / **D11 抽屉席误改成 admin 档** / D12 行级降级标记不渲染 /
  D13 「1.5 秒预算」说明不渲染 / **D14 状态筛选退回本地过滤（控件变死）** /
  **D15 504 被归成「其他」** / D16 双源说明不渲染 / D17 时区分叉说明不渲染 /
  D18 daily 不完整不警告 / D19 详情聚合警告不渲染 / D20 从未登录提示退化 /
  D21 永不过期文案退化 / **D22 catch 里不清空旧数据**。
- 本批三个 spec：**60 用例全绿**（API 28 + 名录视图 13 + 详情视图 19）；
  `AppDrawer.spec.ts` 11 条（白名单加 `tenants`）、`dynamicKeys.spec.ts` 181 条仍全绿。
- `npm run build` **rc=0**（含 `vue-tsc -b`）；三门全过
  （css-media **74 文件** / touch-target **71 个 `.vue`** / i18n parity **各 1669 键**）。
- ★★ **本轮修出一个真产品缺陷**（不是测试问题）：
  名录页的状态筛选分段控件**是死的** —— 状态是 `?status=` 的**服务端**筛选，
  而我只改了 ref、没重新取数 ⇒ 点了按钮不发请求、列表也不变。
  是「切状态筛选会重新请求」那条判据把它抓出来的（D14 变异又钉了一遍）。
- ★ 途中修了四个我自己的问题：
  1. i18n parity 门禁抓到 `tnd.credits7d` / `tnd.requests7d` 两个键**只加在了 `tn` 下**，
     详情视图引用的是 `tnd.*` ⇒ **门禁当场报出**（这条门禁确实在干活）；
  2. 一条 `not.toContain('没用量')` 又一次被**自己的降级说明文案**喂到
     （文案里就写着「真没用量」）⇒ 改成按**那一行**取 warn 节点；
  3. 测试 helper 无条件 `mockResolvedValue`，把「先设 reject / 先设降级数据」**吃掉**了
     ⇒ helper 改成接受覆盖参数，且按 `instanceof Error` 分派
     reject / resolve（`mockResolvedValue(new Error())` 会真的 resolve，判据会变恒真）；
  4. 变异脚本里 D11 的 `file` 又写成了视图（实际在 `appNav.ts`），
     D21 直接删 `v-if` 那行让后面的 `v-else` **悬空** ⇒ 模板解析失败。
- 累计（**当场实测**）：**53 视图 / 43 API 模块 / 46 导航席（14 席 superAdmin）**。
- 全量 **10 连跑全绿，2036 用例，0 份失败快照**（`STAB_RC=0`，10/10 `passed (2036)`）。
  ★ 累计未定位的 flaky 仍**未捕获**（无污染跑 ≥146 次、失败 1 次，≈0.7%），
  本批十连跑未复现 —— 这**不等于「已修复」**。

#### 仍未上移（`admin/tenants` 剩下的）

写操作一律不碰：`POST /tenants`（创建，含 community 模式租户数上限 403）、
`PATCH /tenants/{code}`、`model-policies` 的 POST/PATCH/DELETE/`{id}/undelete`、
`approval-config` / `approvers` / `approval-rules` 的增删。

`model-policies` 子树（`/tenants/{code}/model-policies`、`/check`、`/audit`、`/{id}`）
是**另一个 handler**（`admin/model_policies.go`），带**软删除**（`undelete`），
值得单独一节；`approval-config` 那一族（config/approvers/rules/stats）
在 `admin/approval_config_handler.go`，是另一族配置面。

### 11.78 租户模型策略子树上移（第四十二轮，`model-policies`）

本批把 `/api/admin/tenants/{code}/model-policies` 子树的**三条只读端点**上移：
列表、模型名校验（check）、审计（audit）。写操作（创建 / 改理由 / 软删 / 恢复）**一律不碰**。

- 新 API 模块 `web-mobile/src/api/modelPolicies.ts`（三条端点**各自独立解包**，形状不符抛错）
- 新视图 `ModelPoliciesView.vue`（列表 + check 面板）、`ModelPolicyAuditView.vue`（审计）
- 新路由 `/model-policies`、`/model-policy-audit`
- 新增**两条 superAdmin 抽屉席** `model-policies`、`model-policy-audit`
- i18n 新命名空间 **`mpol` / `mpolAudit`**（**不是** `mp` / `mpa` —— 见下「我犯的错」）
- 门禁：变异 **27/27 有牙**，用例 **102 条**（58 API + 24 策略面 + 20 审计面）

#### ★★★★★★ 头号发现：档位注释是过时的，**租户管理员管不了自己租户的策略**

`admin/model_policies.go:10-11` 的文件头注释写着

```go
//   POST   /api/admin/tenants/{code}/model-policies/check          autocomplete (admin)
//   GET    /api/admin/tenants/{code}/model-policies/audit          audit log (admin)
```

**这是错的。** 真相是 `admin/handler.go:926-927`：

```go
mux.HandleFunc("/api/admin/tenants",  h.superAdmin(h.handleTenants))
mux.HandleFunc("/api/admin/tenants/", h.superAdmin(h.handleTenants))
```

而 `handleTenantModelPolicies` **只有这一个调用点**（`admin/tenants.go:188-190`）
⇒ **整棵子树（含 check 与 audit）都是 superAdmin 硬门槛**。

★ 顺带查出 `SuperAdminMiddleware` **只认 JWT**：`claims.Role != "super_admin"` 即 403，
`sk-...` 那类 legacy Bearer 走不到 handler 直接 401。
⇒ `canAdministerTenant`（`model_policies.go:659-672`）里的
`case "tenant_admin"` 与 `case "admin_key"` 两个分支
**在这棵子树上都是死代码** —— tenant_admin 在中间件就被挡掉了。

★★★ 也就是说：**租户自己的 model-policy，租户管理员也管不了**，必须 super_admin。
页面文案不许按「本租户管理员可自助」写。两条抽屉席因此必须设 `requiresRole: 'super_admin'`。

#### ★★★★★★ 第二头号发现：check 端点**完全不做租户隔离**

`checkTenantModelPolicy(w, r, tenantCode)` 收了 `tenantCode string`，
但它的 SQL（`model_policies.go:564-570`）里是：

```sql
SELECT mc.family, COALESCE(NULLIF(TRIM(mc.modality), ''), 'text')
FROM models_canonical mc
WHERE lower(mc.canonical_name) = lower($1)
  AND COALESCE(mc.status, 'active') = 'active'
LIMIT 1
```

**tenantCode 一个字都没用上。** 查的是**全局** `models_canonical`。

⇒ 在 A 租户下校验和在 B 租户下校验，结果**完全相同**。
⇒ 页面**不能**把这个面板描述成「本租户可用的模型」，文案里明写「查的是全局名录、与租户无关」。

★ check 的匹配条件还有两条都不是「存在即真」：
`lower(...) = lower($1)` 是**大小写不敏感**的；`COALESCE(status,'active')='active'`
意味着**已下线的规范模型报 `exists:false`**。
⇒ `exists:false` 至少有三种成因（真没登记 / 已下线 / 大小写写法不同），
页面不许把它渲染成「拼错了」。

#### ★★★★ `vendor` 字段后端**从不赋值**

`TenantModelPolicyCheckResp.Vendor`（`model_policies.go:79`）全文件**只有声明**，
`omitempty` + 从不写 ⇒ **响应里永远没有这个键**。源码注释自陈：

> Returning empty string is acceptable for the UI which falls back to "Unknown vendor"

⇒ 本 TS 接口**故意不声明 `vendor`**（声明了会诱导视图去读它）。
页面厂商格必须显式显示「后端不提供（字段从不赋值）」，不能留空白格。

★ `modality` 相反 —— 它**恒有值**：`resp := {CanonicalName: canonical, Modality: "text"}`
是初值，命中才被 `COALESCE(NULLIF(TRIM(mc.modality),''),'text')` 覆盖。
⇒ `exists:false` 时 `modality` 是字面量 `"text"`，**不是「没有模态」**。

#### ★★★★★ 三条只读端点对「租户不存在」**三样响应**

| 端点 | 租户码拼错时 | 依据 |
|---|---|---|
| `GET .../model-policies` | **404 `tenant not found`** | `model_policies.go:173-178` 先 `SELECT EXISTS(SELECT 1 FROM tenants WHERE code=$1)` |
| `GET .../model-policies/audit` | **200 + 空数组** | `withTenantTx`（`admin/tenant_ctx.go`）只 `SET LOCAL app.current_tenant` 就查，**不校验租户存在** |
| `POST .../model-policies/check` | **200 + `exists:false`** | 见上，tenantCode 压根没进 SQL |

★★★ 第一个的 404 **还不能当成「租户不存在」的证据** ——
条件写的是 `if err != nil || !exists`，**数据库查询出错也被报成同一个 404**
⇒ 客户端分辨不出「拼错了」与「tenants 表这一行查失败」。
页面文案必须说「不一定是不存在」。

⇒ 审计页拿到空列表时，**必须**出警告说「空 ≠ 没有变更过」。

#### ★★★★ `count` 是**派生值**，证明不了扫描没丢行

`model_policies.go:218-222`：

```go
writeJSON(w, http.StatusOK, map[string]any{
    "policies": out,
    "count":    len(out),      // ← 就是同一个切片的长度
    "tenant":   tenantCode,
})
```

⇒ ★★★ `count` **恒等于** `policies.length`，**既不是**全库条数，**也证明不了**没丢行
（`for rows.Next() { if serr := rows.Scan(...); serr != nil { continue } }`
在 `model_policies.go:202-211`，audit 侧 `631-641` 同）。
页面不许把 `count` 当「全库策略数」显示。

`policies` / `audit` 都是 `make([]T, 0)` ⇒ **永不为 null**，空就是 `[]`。

#### ★★★★ audit：`limit` 越界是**回落默认值 100**，不是 clamp

`model_policies.go:596-601`：

```go
limit := 100
if s := r.URL.Query().Get("limit"); s != "" {
    if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 500 {
        limit = n
    }
}
```

⇒ `limit=0` ⇒ 100、`limit=-1` ⇒ 100、`limit=501` ⇒ **100**（不是 500！）、
`limit=abc` ⇒ 100。★ **本仓第九种限幅语义**，与 MaaS 的 `ClampUsageLimit`（>50⇒50）**方向相反**。

★ 两个层次的整数处理要分清：客户端 `fetchTenantModelPolicyAudit` 先 `Math.trunc`
再进 URL，所以后端 `Atoi` 拿到的一定是合法整数字符串。
若哪天把 URL 里的 `Math.trunc` 去掉，后端 `Atoi("500.9")` 会**失败** ⇒ 静默回落 100
⇒ 页面显示的条数与实际不符。判据钉住了「URL 里的小数确实被截断成整数」。

★ audit 是 `ORDER BY ts DESC LIMIT $2`，**没有 OFFSET、没有游标**
⇒ **没有「更早一页」**，页面刻意**不放任何翻页控件**（有判据钉住）。

#### ★★★ audit 表由**数据库触发器**写，不是 `h.writeAuditLog`

函数体 `sql/objects/functions/tenant_model_policies_audit_fn.sql`，四条由此而来的事实：

- **(a)** `actor` 的兜底是
  `COALESCE(NULLIF(current_setting('app.current_admin', true), ''), 'system')`
  ⇒ **`actor === 'system'` 表示当时没设 `app.current_admin` GUC**，
  **不是「某个叫 system 的账号」**。页面单列成「无署名」。
- **(b)** `delete` 那行写的是 **`OLD.reason`**（删除**前**的理由），
  而 `insert`/`update`/`undelete` 写 `NEW.reason`
  ⇒ 同一条策略的「理由」在审计里会**前后不一致**，**这是设计不是数据错**。
- **(c)** `UPDATE` 只在 `deleted_at` 变了、**或** `reason`/`canonical_name` 变了时才落审计行
  ⇒ 一次「改成同样的值」的 PATCH **不留任何审计痕迹**。
- **(d)** 动作集合是**闭合的**：表上有
  `CHECK (action = ANY (ARRAY['insert','update','delete','undelete']))`
  ⇒ 库里不可能出现别的动作。★ 注意第一个是 **`insert`**，不是 `create`（我一开始猜错了，
  追到 `sql/objects/tables/tenant_model_policies_audit.sql` 才改对）。

`auditRow.policy_id` 是**指针 + omitempty** ⇒ 键不存在 = 这行没挂到具体策略。
触发器写它时**总**填 `NEW.id`/`OLD.id`，所以走触发器产生的行该键恒在。

#### ★★★ `include_deleted` 只认**字面** `"true"`

`model_policies.go:180`：`r.URL.Query().Get("include_deleted") == "true"`
⇒ `1` / `TRUE` / `yes` / 空 一律当 **false**，**静默少返回软删行，不报错**。

`deleted_at` / `deleted_by` 都是**指针 + omitempty** ⇒ 未软删时键**整个不存在**（不是 `null`），
两者**各自独立** omitempty，理论上可只缺一个。

#### ★★ 三个 handler 的 ctx 预算各不相同

list 5s / audit 5s / **check 3s** —— check 更容易踩超时。

#### ★★ 租户码含 `/` 时**到不了**这些端点

`admin/tenants.go:145` 用 `strings.SplitN(r.URL.Path, "/", 2)`，而 `r.URL.Path` 是
**已解码**路径 ⇒ `%2F` 会被解回真斜杠再切坏。前端按既有约定 encode，
但那只防 URL 层面歧义，**防不住 Go 的解码**；含 `/` 的租户码是后端限制。

#### 子树分派（写操作那侧，本批只记录）

`handleTenantModelPolicies`（`model_policies.go:87-163`）：
`rest == ""` ⇒ GET list / POST create / 其他 405；
`head == "check"` **必须 POST**，否则 405 `check requires POST`；
`head == "audit"` **必须 GET**，否则 405 `audit requires GET`；
`head` 走 `strconv.ParseInt` 失败 ⇒ **400 `expected numeric policy id`**；
`tail == "undelete"` **必须 POST**，否则 405 `undelete requires POST`；
其他 tail ⇒ **404 `unknown sub-path: <tail>`**。
`h.db == nil` ⇒ **503 `database not configured`**。

#### 我在这一批犯的错

1. ★★★ **i18n 前缀撞车**：先用了 `mp` / `mpa`，被 `verify-i18n-parity` 抓到
   「`zh-CN mp.title`（第 1408 行与第 1978 行）」重复键 ——
   **`mp` 早被 MaaS 目录与价目占用**（`MaasCatalogView.vue` 的 `mp.dim_`）。
   门禁在这里是真有牙的。改用 `mpol` / `mpolAudit`。
2. ★★★ **抄 TenantsView 时把 MaaS 文案一起抄了过来**：错误分支用了
   `t('maas.unconfigured')`，那句文案写着「服务端没有启用 **MaaS**」——
   出现在模型策略页是错的。补了本页自己的 `mpol.unconfigured` / `mpolAudit.unconfigured`。
3. ★★ **python 批改脚本锚点只匹配到键名没匹配整行**，把 `unconfigured:` 插进了
   `includeDeletedNote:` 的**键与值之间**，两个 locale 文件同时语法破坏。
   自造锚点必须含值或换行。
4. ★★ **断言里 `expect.anything()` 不匹配 `undefined`**：视图调用 `fetch(code, params)`
   只传两个参数，`options` 是 `undefined` ⇒ 5 条断言全红。是我断言写错，不是实现错。
5. ★★ **把「小数先截断」的期望写反**：我以为 `500.9` 会被服务端回落 100，
   实际客户端 `Math.trunc(500.9)=500` **先截断再发** ⇒ 后端看到 `"500"` ⇒ 500。
   实现是对的，断言是错的。改成反映真实的两层行为。
6. ★★★ **全页 `not.toContain` 被自己写的免责文案判红**：断言
   `expect(w.text()).not.toContain('没有变更过')`，而那条免责警告本身就是
   `空列表**不能**当成「这个租户没有变更过」` ⇒ **引述了那几个字**。
   这是既有纪律（否定断言要按节点作用域）的一个**新变体**：
   被判红的是**同一面板、同一目的、我自己刚写的免责说明**。
   改成「凡提到该措辞的节点必须全部是 `.mpa__note--warn`，且 `.mpa__msg` 里一个都不许有」。
7. ★★★★ **补出一条真判据缺口**（变异 V3 抓出来的）：我原来只写
   `expect(w.findAll('.mp__item')).toHaveLength(0)` 来守「抛错不许退化成空名单」——
   把 catch 改成 `list.value = {policies: [], count: 0, tenant}` 时它**照样是 0** ⇒ **无牙**。
   ★ 「列表项为 0」**证明不了**「列表面板没渲染」：后者才是错误态与空态的区别。
   补 `expect(w.findAll('.mp__badge')).toHaveLength(0)`（计数徽标只存在于
   `v-if="list"` 之内）后，V3 与 V11 都有牙了。审计面同理补 `.mpa__badge`。
8. ★★ **变异注入把注释插在模板属性值里**（`v-for="..." /* MUT-V10 */`）破坏了模板解析
   ⇒ 收集失败、具名红为空。标记要放在标签内容区，不能插进属性值中间。
9. ★★ **变异脚本删掉 `requiresRole` 那一行后产物里没有可 grep 标记** ⇒ 脚本正确拦下（N1 缺标记）。
   注入必须自带标记，哪怕改的是「删一行」。
10. ★★ **测试标题与断言不符**：`503 ⇒ 明说「没启用」` 在断言改成「没有配置数据库」
    之后没跟着改。标题是给人读的契约，也得跟。

#### 门禁与实测

- 变异 **27/27 有牙**（M1–M15 API 段、V1–V11 视图段、N1 导航段），还原逐字节一致
- 全量 **2138 用例全绿**（97 个测试文件）
- `npm run build` rc=0（含 `vue-tsc -b`）
- 三门 rc=0：css-media / touch-target（**73** 个 `.vue`）/ i18n parity
  （零重复键；本批**零新增**动态前缀，仍 29 处，与改动前一致）
- 全量 **10 连跑**：见下

#### 仍未上移

写操作一律不碰：`POST /model-policies`、`PATCH /{id}`、`DELETE /{id}`、
`POST /{id}/undelete`。

**下一批候选**：`approval-config` 一族（config / approvers / rules / stats）——
★ 注意它实际注册在 **`/api/admin/tenant-approval-config/`（单数 tenant）**
且走 `wrapAdmin`（admin 档），且**只在 `dbConn.Enabled() && redisClientForCache != nil` 时注册**；
打 `/api/admin/tenants/{code}/approval-config` 会落进 `handleTenants` 的
`unknown sub-resource` 404 分支。另：`admin/attachments`(8)、`admin/modules`(7)、
`admin/logs`(7)、`system/session-context`(6)。


### 11.79 租户审批配置面上移（第四十三轮，`approval-config`）

本批把审批配置一族的**四条只读端点**上移。写操作一律不碰。

- 新 API 模块 `web-mobile/src/api/approvalConfig.ts`（四端点各自独立解包）
- 新视图 `ApprovalConfigView.vue`（config + stats + 通知渠道）、`ApprovalRulesView.vue`（审批人 + 规则）
- 新路由 `/approval-config`、`/approval-rules`
- 新增**两条 admin 档抽屉席** `approval-config`、`approval-rules`（**故意不设** `requiresRole`）
- i18n 新命名空间 **`acfg` / `acfgRules`**
- 门禁：变异 **25/25 有牙**，用例 **68 条**（36 API + 20 配置面 + 12 规则面）

#### ★★★★★★ 路径是 `tenant-approval-config`（**单数** tenant），档位是 **admin**

handler 文件头的注释写的是 `/api/admin/tenants/{tenant_id}/…`（`approval_config_handler.go:41`
起，每条都这么标），**那是 2026-07-03 之前的旧前缀**。现在注册的是（`cmd/gateway/main.go:7367`）：

```go
mux.HandleFunc("/api/admin/tenant-approval-config/", func(w http.ResponseWriter, r *http.Request) { … })
```

`main.go:7364-7366` 的注释记录了原因：两条前缀曾同时注册，`net/http.ServeMux` 直接在启动时 panic。

★ 而 `/api/admin/tenants/` 归 `admin/handler.go:926-927` 的 `h.superAdmin(h.handleTenants)` 所有
⇒ ★★ 打**复数** `/api/admin/tenants/{code}/approval-config` 会落进
`handleTenants` 的 `unknown sub-resource: approval-config` **404** 分支。

★ 档位与上一批**相反**：这一族走 `wrapAdmin`，而
`cmd/gateway/main_admin_wrappers.go:26-30` 里 `newWrapAdmin = admin.AdminMiddleware`
⇒ `h.admin` 语义，**tenant_admin 可用** ⇒ 抽屉席**不设** `requiresRole`，
并配了判据：设成 `super_admin` 必须让测试红（变异 N1）。

★ 但 handler 内部**还有一道** `canAccessTenant` / `canModifyTenant`
（`approval_config_handler.go:452-490`），两者都额外放行 `admin_key` 角色，
中间件那道 `requiresRole` 没有建模。

#### ★★★★★★ 整族挂载**有条件**（`cmd/gateway/main.go:7358`）

```go
if dbConn != nil && dbConn.Enabled() && redisClientForCache != nil { …注册… }
```

★★ **Redis 没起 ⇒ 这整个前缀根本没有注册** ⇒ 请求走 Go mux 的默认行为，
拿到的是**裸文本** 404（`default: http.NotFound(w, r)`），**不是** `{"error":{"detail":…}}` 信封。

⇒ 页面必须把「这个租户没有审批配置」与「这一族压根没开」分成两种文案
（判据：`isnotregistered` 分支要求出现「没注册」四个字，且**不许**出现合成默认面板）。

★ 派发是 `strings.Contains` **逐条 case**（`main.go:7369-7396`），不是路径解析：

| case | 条件 |
|---|---|
| 1 | `Contains(path, "/approval-config/stats")` |
| 2 | `Contains(path, "/approval-config")` && GET |
| 3 | `Contains(path, "/approval-config")` && PUT |
| 4 | `Contains(path, "/approvers/")` && PUT |
| 5 | `Contains(path, "/approvers/")` && DELETE |
| 6 | `Contains(path, "/approvers")` && GET |
| 7 | `Contains(path, "/approvers")` && POST |
| 8 | `Contains(path, "/approval-rules/")` && DELETE |
| 9 | `Contains(path, "/approval-rules")` && GET |
| 10 | `Contains(path, "/approval-rules")` && POST |
| default | `http.NotFound(w, r)` ← **裸文本** |

★★★ **后端缺陷（只记录不修）**：`Contains` 不看段边界 ⇒ 若租户码让路径里出现
`/approval-config` / `/approvers` / `/approval-rules` 子串（例如租户码就叫 `approval-config`），
GET 会被**派发到错误的 handler**。
`extractTenantID`（`:413-424`）反而是对的，它按**段**匹配 `tenant-approval-config` 或 `tenants`。

#### ★★★★★★ 「没有配置」**不是错误**：后端返回**合成默认配置**

`domains/approval/store.go:401-414`：

```go
if errors.Is(err, pgx.ErrNoRows) {
    return &ApprovalConfig{
        TenantID: tenantID, Enabled: false, Mode: ModeDisabled,
        TimeoutSeconds: 3600, AutoRejectOnTimeout: true,
        Approvers: []Approver{}, Channels: []NotificationChannel{}, Rules: []ApprovalRule{},
    }, nil
}
```

⇒ ★★★ **没有「租户不存在」的 404**：任何过了鉴权的租户码都拿到 200。
⇒ ★★★ 而且 `timeout_seconds: 3600` 与 `auto_reject_on_timeout: true` **是凭空造的**，
库里根本没有这行 ⇒ 页面把它们显示成真实配置就是**假读数**。
⇒ ★ 唯一能分辨「从未配置过」的标志是 `created_at` / `updated_at` 为**零值时间**
（Go 把零值 `time.Time` 序列化成 `"0001-01-01T00:00:00Z"`，且这两个键**无** `omitempty`）。

#### ★★★★★ `/approvers` 与 `/approval-rules` 只回**启用中**的行，而且是**另一张表**

`store.go:389` / `store.go:431`：

```sql
SELECT … FROM approval_approvers WHERE tenant_id = $1 AND enabled = true ORDER BY priority ASC
SELECT … FROM approval_rules     WHERE tenant_id = $1 AND enabled = true ORDER BY priority DESC
```

⇒ ★★★ **被停用的审批人 / 规则根本不在这两个列表里。**
⇒ ★★ 它们读的是 **`approval_approvers` / `approval_rules` 表**；
`config.approvers` / `stats.*_count` 读的是 **`approval_configs.config` 那个 JSONB 列**（含停用的）
⇒ **两个数据源**，条数可以不一致，**这不是数据错**。页面并排显示时必须标口径。

★ 排序方向**相反**，各按自己结构体注释的语义（`types.go:133` 与 `types.go:159`）：
approvers `priority ASC` ← 「Lower number = higher priority」；
rules `priority DESC` ← 「Higher number = higher priority」。

#### ★★★★ 空列表序列化成 **`null`**，不是 `[]`

`store.go:390` 是 `var approvers []Approver`（**nil 切片**，不是 `make([]T,0)`），
`GetRules` 同理 ⇒ 无行时 `writeJSON` 把 nil 切片写成 **`null`**，
而 `count` 是 `len(nil)` = **0**。

⇒ `{"approvers": null, "count": 0}` 是**合法成功响应**，
解包器**不许**因为 `approvers === null` 就抛错。
★ 与上一批 model-policies 那族**正好相反**（那边是 `make([]T,0)` ⇒ 永不为 null）。

#### ★★★★★ `ConfigStats` 的键是 `ApprovalConfig` 的**真子集** ⇒ 判别键必须多于三个

`ConfigStats`（`config_manager.go:475-487`）与 `ApprovalConfig`（`types.go:104-115`）
**共享四个键**：`tenant_id` / `enabled` / `mode` / `timeout_seconds`（两个都在！）。
只有 `auto_reject_on_timeout`（config 独有）与 `approver_count`（stats 独有）能区分。

★★ 我最初只按 `tenant_id`+`enabled`+`mode` 三个键判，**结果 stats 会被当 config 放行**，
而 stats 缺 `approvers` / `channels` / `rules` ⇒ 页面会拿着 stats 渲染出三个 `undefined`。
⇒ 补上 `auto_reject_on_timeout`（Go 侧**无** `omitempty` ⇒ 真实响应里必然在）。
⇒ ★ 这正是既有纪律「『互喂必须抛错』要求两形状互不包含；是子集关系就得换成投影式判据」
   的一次现场复现 —— **由我自己写的互喂用例抓出来的**。

#### ★★★★★ stats 是 config 的**纯函数**，客户端独立复算

`config_manager.go:435-472`：`approver_count` / `rule_count` / `channel_count` 来自
`len(config.X)`，`enabled_*` 来自「数 config.X 里 enabled 的个数」，
`last_updated` 就是 `config.updated_at`，`mode` / `enabled` / `timeout_seconds` 直接搬。

⇒ 11 个字段**全部可由 config 独立复算** ⇒ 页面并排显示并**自己算一遍**核对
（这是本族最强的判据：11 个字段逐个改一个都必须判不一致）。
⇒ 「不一致」按**异常**上报，不是「口径差异」。

#### ★★ `omitempty` 与 nil map 造成的键缺失

- `COALESCE(email,'')` / `COALESCE(phone,'')` + `omitempty`
  ⇒ 空邮箱/手机**整个键不存在**（不是空串、不是 null）
- `NotificationChannel.Config` 是 `map[string]string` 且**无** `omitempty`
  ⇒ nil map 序列化成 **`null`**（不是 `{}`）
- `RuleCondition` 三字段、`RuleAction` 三字段全部无 `omitempty`

#### ★ 枚举都来自**注释**，不是数据库 CHECK

`Mode` = disabled/automatic/manual；`ChannelType` = feishu/wechat/dingtalk/email/webhook；
`RiskLevel` = LOW/MEDIUM/HIGH/CRITICAL（`types.go:65-68`）；
`RuleAction.Type` = require_approval/auto_approve/auto_reject；
`RuleCondition.Operator` = contains/gt/lt/eq/regex。
⇒ 库里出现别的值是可能的，页面按未知渲染、不猜。

#### 子树分派（写操作那侧，本批只记录）

`AddRule` 成功是 **201**，其余写端点是 200。
`canModifyTenant` 与 `canAccessTenant` 当前**逻辑完全相同**（都放行 super_admin /
admin_key / 本租户的 tenant_admin）⇒ 读写权限没有区别。
`GetConfigStats` 失败 ⇒ **500** `failed to get stats`；
`GetApprovers` / `GetRules` 失败 ⇒ **500**；`GetConfig` 失败 ⇒ **500** `failed to get config`。
`extractTenantID` 返回空 ⇒ **400** `missing tenant_id`；
`extractRuleName` 返回空 ⇒ **400** `missing rule_name`；
鉴权不过 ⇒ **403**（读是 `access denied`，写各有各的文案）。

#### 我在这一批犯的错

1. ★★★ **`ApprovalRulesView` 里留了一个从未被赋值的 `channels` ref** ⇒ 那个渠道面板是
   **死代码**，`v-if="channels"` 恒假。是 `vue-tsc -b` 顺带把同文件里另一个未用导入
   `approvalModeTone` 报出来，我才顺藤摸到它。⇒ 渠道挪到配置页（那边已经取了 config），
   并补了 5 条判据钉住它现在是活的。
2. ★★★ **python 批改的锚点假设错了**：我以为 `refreshArmed` 那块在 locale 文件末尾，
   其实上一批的 `mpol`/`mpolAudit` 已经追加在它后面 ⇒ `assert count==1` 直接失败，
   没写坏文件。**这次锚点失配救了场** —— 上一批同样的操作把两个 locale 文件写坏了。
   ⇒ 教训不变：**锚点必须验证唯一**，别凭结构印象。
3. ★★★ **变异 V2/V8 两条注入各自坏了**：
   · V2 把标记写成 `v-if="…" /* MUT-V2 */` —— 注释插在**属性值**里，模板解析失败 ⇒ 收集失败、具名红为空。
     **这与上一批 V10 是同一个错，我记了却再犯**。⇒ 给变异脚本加了 `after` 钩子，
     标记改放**标签内容区**。
   · V8 用了 `s.replace(...)` 而该串在文件里**出现两次**（审批人/规则各一处）
     ⇒ 只改了第一个，第二个还在 ⇒ 仍全绿。⇒ 改用 `split().join()` 全替。
4. ★★★ **变异 A10 是等价变异**：`'email' in a` 改成 `a.email !== undefined`，
   而夹具里压根没有 `email` 键 ⇒ 两种写法同解。⇒ 换成 `return true` 才是有牙的变异。
   ⇒ 归因纪律：**仍全绿先证明该变异可观测**，证不出才叫「判据无牙」。
5. ★★★ **两条真判据缺口**（都是变异抓出来的）：
   · **V4 = 「首屏就失败」恒真**：我只测了首屏取数失败，就把 catch 里的
     `config.value = null` 删掉 —— 首屏本来就没有上一轮结果，**照样全绿**。
     ⇒ 必须造「先成功 → 再失败」序列。已补（6 个断言）。
   · **V6 = 被说明文案喂饱**：判据写 `w.text()).toContain('通知渠道')`，
     而那条 `channelsNote` 说明里就带着「通知渠道」四个字 ⇒ 面板标题删了也不红。
     ⇒ 改按**节点**判（`.acfg__panel-title` 的文本）。这与上一批
     「正向 `toContain` 被同一页说明文案喂饱」是同一条。
6. ★★ **expect 串又抄错 2 处**（A4、V11）：A4 实际红在「四个形状两两互不包含」那条
   （因为新加的「子集」测试当时只比键集、护不住行为 ⇒ 已补上
   `expect(() => unwrapApprovalConfig(stats())).toThrow()` 的行为断言）；
   V11 的标题是 `且**不**显示`，我写成了「不许显示」。
7. ★★ **那条「子集」测试本身是半自造的**：它只比我自写夹具的键集，
   任何正确实现都不会让它红。⇒ 加了行为断言之后它才真的护住东西。
   ⇒ 判别信号：**一条断言如果只比较我自己构造的两个字面量，它护不住产品**。
8. ★★ **`channels` 被我先放在错误的那一页**：渠道只存在于 config 响应的 `channels` 里，
   规则页那两个端点根本不返回渠道。第一版我把面板放进规则页并留了个永不赋值的 ref。
9. ★ `ConfigStats` 也带 `timeout_seconds`（`config_manager.go:485`）——
   我第一版写「共享三个键」，被自己的测试打回。⇒ 实测**共享四个**。

#### 门禁与实测

- 变异 **25/25 有牙**（A1–A13 API 段、V1–V11 视图段、N1 导航段），还原逐字节一致
- 全量 **2212 用例 / 100 文件全绿**
- `npm run build` rc=0（含 `vue-tsc -b`）
- 三门 rc=0：css-media / touch-target（**75** 个 `.vue`）/ i18n parity
  （零重复键；本批**零新增**动态前缀）
- 全量 **10 连跑**：见下

#### 仍未上移

写操作一律不碰：`PUT /approval-config`、`POST` / `PUT` / `DELETE /approvers[/{user_id}]`、
`POST` / `DELETE /approval-rules[/{rule_name}]`。

**下一批候选**：`admin/attachments`（8，admin 档）、`admin/modules`（7）、
`admin/logs`（7，混合读写）、`system/session-context`（6）。


### 11.80 附件留存读面上移（第四十四轮，`admin/attachments`）

本批把附件留存一族的**六条只读端点**上移。写操作（`cleanup/execute`、`filesystem/cleanup`）一律不碰。

- 新 API 模块 `web-mobile/src/api/attachments.ts`（六端点各自独立解包）
- 新视图 `AttachmentsView.vue`（清单 + 统计 + 策略 + 文件系统 + 清理预览，五块合一页）
- 新路由 `/attachments`
- 新增**一条 admin 档抽屉席** `attachments`（**故意不设** `requiresRole`）
- i18n 新命名空间 **`att`**
- 门禁：变异 **27/27 有牙**，用例 **71 条**（42 API + 29 视图）

#### ★★★★★★ 档位：六条全是 admin 档，但**同一前缀下混着 superAdmin**

`admin/handler.go:998-1008`：

```go
mux.HandleFunc("/api/admin/attachments/filesystem/stats",    admin(h.handleAttachmentFilesystemStats))
mux.HandleFunc("/api/admin/attachments/filesystem/cleanup", h.superAdmin(h.handleAttachmentFilesystemCleanup))
mux.HandleFunc("/api/admin/attachments",                    admin(h.handleDataLifecycleAttachments))
mux.HandleFunc("/api/admin/attachments/stats",              admin(h.handleDataLifecycleAttachmentStats))
mux.HandleFunc("/api/admin/attachments/policy",             admin(h.handleDataLifecycleAttachmentPolicy))
mux.HandleFunc("/api/admin/attachments/cleanup/preview",    admin(h.handleDataLifecycleAttachmentCleanupPreview))
mux.HandleFunc("/api/admin/attachments/cleanup/execute",    h.superAdmin(h.handleDataLifecycleAttachmentCleanupExecute))
mux.HandleFunc("/api/admin/attachments/",                   admin(h.handleDataLifecycleAttachmentItem))
```

★ 上移的六条**全是 `admin(...)`** ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`，
并配判据：设成 `super_admin` 必须红（变异 N1）。
★★ 但 `filesystem/cleanup` 与 `cleanup/execute` 是 **superAdmin** ⇒
**前缀相同、档位不同，移动端不能按前缀判权限**。

#### ★★★★★★ 头号陷阱：同一个 `attachments` 字段，**两条端点的 nullability 不同**

- **list**（`data_lifecycle_attachments.go:182`）：
  `COALESCE(client_model,''), success, attachments::text`
  ⇒ `attachments` 是 `json.RawMessage` **原样透传**，**可以是 JSON 标量 `null`**。
  源码注释（`:238-241`）：2026-09-14 起 `request_logs.attachments` 里
  **18k+ 行**存的是 JSON `null` 标量而不是数组。
- **item**（`:629`）：`COALESCE(attachments::text, '[]')`
  ⇒ ★ 这里**保证是数组**。

⇒ ★★ 「列表里那条没附件」与「详情里 attachments 是空数组」是**同一件事的两种表现**。
客户端**不许**把 list 侧的 `null` 当 `[]` 渲染成「无附件」，**也不许**因此抛错
（`attachments` 是 `unknown`，后端还可能写进对象或字符串）。

#### ★★★★ `stats` 的行集合是 `list` 的**真子集**

- list  ：`WHERE attachments IS NOT NULL`（`:170`）
- stats ：`WHERE attachments IS NOT NULL AND jsonb_typeof(attachments) = 'array'`（`:242`）

后者是因为 18k+ 行的 `null` 标量会让 `jsonb_array_elements` 报
`cannot extract elements from a scalar`。

⇒ ★★★ 「列表 N 条」与「统计 M」**对不上是预期的**，页面必须标口径，不许报成数据不一致。

★ `cleanup/preview`（`:363-364`）用的是 **stats 那套**条件（多 `jsonb_typeof`），
所以 preview 的 `affected_records` 也**只数数组行**。

#### ★★★★ 时间参数**解析失败被静默丢弃**

```go
if s := r.URL.Query().Get("since"); s != "" {
    if t, err := time.Parse(time.RFC3339, s); err == nil { since = t }
}
```

⇒ ★★★ `?since=garbage` / `?since=2026-13-45T00:00:00Z` 一律**当作没传**，
**不报错、不告警**，返回**全时间范围**的数据。
⇒ ★ 区间是**半开**的：`ts >= since` 且 `ts < until`（含下不含上）。

★ 我为此写的客户端自查 helper 第一版有个**真缺陷**：
只用 `/^\d{4}-\d{2}-\d{2}T...$/` 这种**形状正则**会把 `2026-13-45T00:00:00Z` 判成有效
（`\d{2}` 照单全收），而 Go 的 `time.Parse` 会因月份 13 越界而**失败** ⇒
页面被告知「窗口会生效」，后端却静默丢弃 ⇒ **提示反过来误导人**。
⇒ 改成带**取值范围**校验（月 1–12、日 1–31、时 ≤23、分 ≤59、秒 ≤60 闰秒合法）。

#### ★★★ `limit` / `offset` 是**两端 clamp**（不是回落）

```go
limit  := clampInt(r.URL.Query().Get("limit"), 50, 1, 200)
offset := clampInt(r.URL.Query().Get("offset"), 0, 0, 100000)
```

`clampInt`（`:650-662`）：空 ⇒ def；`Atoi` 失败 ⇒ def；`< min` ⇒ **min**；`> max` ⇒ **max**。

⇒ ★★ `?limit=99999` ⇒ **200**（不是回落 50）、`?limit=0` ⇒ **1**、`?offset=-5` ⇒ **0**。
★ 与 approval-config 那个 audit limit（「越界**回落** 100」）**方向相反**。
★ 而 `preview` 的 `older_than_days` 走 `parseOlderThanDays`：**没有上界**，
`≤0` 或非法 ⇒ **回落 30** ⇒ **同一族里两套限幅语义**。

#### ★★★ `tenant_id` 对 tenant_admin 被**静默忽略**

```go
func attachmentTenantScope(r, explicitTenantID string, alreadyAppended int) (string, []any) {
    if IsTenantAdmin(r) { return fmt.Sprintf(" AND tenant_id = $%d", …), []any{GetTenantID(r)} }
    if explicitTenantID != "" { return …, []any{explicitTenantID} }
    return "", nil
}
```

⇒ tenant_admin 传了 `?tenant_id=别的租户` 也**不报错**，只是不生效，仍只看自己租户。
super_admin + 显式 `tenant_id` ⇒ 收窄；super_admin 不传 ⇒ **看全部**。

#### ★★ 两种行丢失的失败方式**处理相反**

- 每行 `rows.Scan` 失败 ⇒ `warnRowSkip` + `continue` ⇒ **静默丢行**
- `rows.Err()`（传输层截断）⇒ `writeAggRowsErr` ⇒ **整个 500**
  （缺关系时是 **503 `analytics_view_missing`**，是本仓第 N 次见到这个码）

源码注释：「截断的清单会被当成"就这么多附件"，静默 200 比失败更有害。」

⇒ 200 **不保证**条数完整，但也不是「悄悄少了就当全量」。页面文案要说清这两层。

#### ★★★ `policy` 是**硬编码常量**，且是本族唯一**不需要数据库**的

```go
writeJSON(w, http.StatusOK, map[string]any{
    "policy": map[string]any{
        "retention_days": 30, "max_size_bytes": 20 * 1024 * 1024,
        "auto_cleanup": false, "delete_filesystem": false, "description": "…",
    },
    "note": "策略为内置默认值，暂不支持动态配置。可通过环境变量 LLM_GATEWAY_ATTACHMENT_DISABLED=1 完全关闭附件捕获。",
})
```

★ 它**没有** `h.db == nil` 检查（其余五条没库都 503）
⇒ 数据库挂了，这一页**还能出数**。这一点页面要单独说，否则「别的面板空了这块有数」
会被当成数据不一致。

`max_size_bytes` 就是 **20971520**（20 MiB），客户端**不许**另算一套。

#### ★★ `cleanup/preview` **没有方法门**

注册是 `admin(...)` 且 handler 里**没有** `if r.Method != …` ⇒ **GET 也能调**
（用 `?older_than_days=`）。源码注释写的是 POST —— **文档与实现不一致**。
`dry_run` 恒为 `true`（这个端点里没有执行分支）。

#### ★★ `{request_id}` 详情端点

`ORDER BY ts DESC LIMIT 1` ⇒ 同一 request_id 有多行时取**最新**那行。
跨租户返 **404 而不是 403** —— 源码注释明说是**故意**的：
`// (not 403, to avoid leaking the existence of cross-tenant rows)`。

`request_id == ""` ⇒ **400 `missing request_id`**。

#### ★ `filesystem/stats`（裸对象，10 键）

`oldest_file_time` 是 `*string` 且**无** `omitempty` ⇒ **键一定在**，值为 `null` =
目录里**一个文件都没有**（不是「没查到」）。
`disk_warning_level` 由 `disk_usage_percent` 分档：**>=90 danger、>=75 warning、否则 safe**
（客户端独立复算，不信任后端给的值）。
它**有**方法门（`GET` only）⇒ 与 preview 形成对照。

★ `filepath.WalkDir` 里 `if err != nil { return nil }` ⇒ **静默忽略**无权访问的目录
⇒ `total_files` / `total_size_bytes` 可能偏小。

#### 后端缺陷（只记录不修）

1. ★ `handleDataLifecycleAttachmentStats`（`:243-258`）把 `since`/`until`
   **追加了两遍**：`:243-250` 加一次，`:251-258` 又原样加一次
   ⇒ WHERE 里出现 `ts >= $N AND ts < $N+1 AND ts >= $N+2 AND ts < $N+3`，
   args 也多两个占位符。**结果正确**（重复的谓词相同），但纯属冗余、易误导。
2. ★ `handleDataLifecycleAttachmentItem`（`:618-621`）里
   `tenantIdx := 2` 紧接着 `_ = tenantIdx` ⇒ 死变量，租户下标实际由
   `attachmentTenantScope(r, "", 1)` 的 `alreadyAppended=1` 决定。
3. ★ 六条 handler 全部带 `//nolint:unused` 注释，但都真实注册在 mux 上
   ⇒ 说明它们曾经真的没被调用过（路由是 2026-07-02 补的，见 `handler.go:1001` 注释）。

#### 我在这一批犯的错

1. ★★★★ **我自己的 helper 有真缺陷**：RFC3339 自查只用形状正则，
   把 `2026-13-45T00:00:00Z` 判成有效 ⇒ 页面会提示「窗口生效」而后端静默丢弃
   ⇒ **提示反过来误导人**。补了取值范围校验，并加了 8 条越界断言
   （含「秒 60 闰秒 Go 允许 ⇒ 判有效」）。
2. ★★★ **测试标题里写了撇号**：`it('…（`COALESCE(…,\'[]\')`）…')`
   ⇒ 单引号字符串被截断 ⇒ 整个文件 `ParseError` ⇒ **0 个用例收集**。
3. ★★★★ **变异 T1 抓出一条真判据缺口**：我只靠「五种形状互喂」把关，
   而别的形状**都没有 `items`** ⇒ 把 `offset` / `count` / `limit`
   从 list 的解包条件里删掉，测试**照样全绿**。
   ⇒ 补「缺 `offset` / 缺 `count` / 缺 `limit` 各自必须抛错」。
4. ★★ **变异 T12 的注入把语法写坏了**：我把多行 `return ( … )` 的条件整体换成
   `return true`，留下悬空的 `)` ⇒ 收集失败。⇒ 改成只替换第一个条件项。
5. ★★★ **expect 串第 5 次被 markdown 星号切断**（累计）：这次是
   `不许显示` vs 实际标题 `且**不**显示`；还有 `既不等于` vs `**不等于**`。
   ⇒ 已把「取标题里那段没有 markdown 强调的纯文字」写进纪律，但仍会犯。
6. ★★ **视图里留了 3 个未用导入**（`attachmentCountIsDerived`、
   `attachmentPreviewDaysEffective`、`route`）⇒ `vue-tsc` 报出来才删。
   `route` 未用是因为本页没有 query 预填（与 model-policies 那两页不同）。
7. ★★ **`--noproxy` 那条只对单条命令有效**：`git push` 第一次因
   `HTTP(S)_PROXY=127.0.0.1:7897` 指向已关掉的代理而 rc=128。
   ⇒ 判 rc 之后用 `env -u HTTP_PROXY -u HTTPS_PROXY git push …` 重推才 rc=0。

#### 门禁与实测

- 变异 **27/27 有牙**（T1–T14 API 段、W1–W12 视图段、N1 导航段），还原逐字节一致
- 全量 **2291 用例 / 103 文件全绿**
- `npm run build` rc=0（含 `vue-tsc -b`）
- 三门 rc=0：css-media / touch-target（**76** 个 `.vue`）/ i18n parity
  （零重复键；本批**零新增**动态前缀）
- 全量 **10 连跑**：见下

#### 并发会话提醒

本批中途全量一度出现「1 failed | 102 passed（103）」而**2283 个用例全绿**、
`Sparkline.spec.ts` 报 `Cannot find name 'fileURLToPath'` + 未用的 `resolve`：
那是**并发会话**刚建的未跟踪文件（`git status` 里 `?? Sparkline.spec.ts`
与 `M Sparkline.vue`），随后被对方自己修好。按既定口径**不碰并发会话的文件**。

#### 仍未上移

写操作一律不碰：`POST /api/admin/attachments/cleanup/execute`、
`POST /api/admin/attachments/filesystem/cleanup`（两条都是 **superAdmin** 档）。

**下一批候选**：`admin/modules`（7）、`admin/logs`（7，混合读写）、
`system/session-context`（6）。

### 11.81 功能模块面上移（第四十五轮，`admin/modules`）

本批把功能模块一族的**三条只读端点**上移。两个不碰的端点里有一个陷阱特别值得记：`POST /{key}/test` 虽然名字叫 test，但它**有外部副作用**。

- 新 API 模块 `web-mobile/src/api/modules.ts`（三端点各自独立解包）
- 新视图 `ModulesView.vue`（清单）+ `ModuleDetailView.vue`（详情 + 运行配置摘要）
- 新路由 `/modules` 与 `/modules/:key`（**详情页不占抽屉席**）
- 新增**一条 admin 档抽屉席** `modules`（**故意不设** `requiresRole`）
- i18n 新命名空间 **`mods`** / **`modsDetail`**
- 门禁：变异 **47/47 有牙**，用例 **101 条**（48 API + 27 列表视图 + 26 详情视图）

#### ★★★★★★ 注册**不在** `admin/handler.go`，grep 路由注册必须限到全仓

`admin/modules.go:1129-1134`：

```go
func (h *Handler) registerModuleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/modules",  h.admin(h.handleModulesList))
	mux.HandleFunc("/api/admin/modules/", h.admin(h.handleModulesRouter))
}
```

★ 我第一轮测绘**只 grep 了 `admin/handler.go`**，得出「这族根本没注册」的结论。
⇒ 这是「推断出的陷阱必须追到代码确认」的又一例：路由注册分散在**多个文件**里，
搜一个文件得到的是**否证**而不是证明。

档位：两条都是 `h.admin(...)` ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
`AppDrawer.spec.ts` 的 superAdmin 白名单**本批无需改动**（白名单是「被过滤掉的 key」逐个列出的断言，
新增 admin 档席不会让它红）。

#### ★★★★★★ `enabled: true` 可能是「没读到配置」的兜底，而且有**六种**成因

`resolveModuleEnabled`（`admin/modules.go:628-648`）**五条**失败路径全部返回 `(true, "default")`：

```go
if m.SettingKey == ""   { return true, "default" }   // 1 压根没有开关
sp := settings.Global.Spec(m.SettingKey)
if sp == nil            { return true, "default" }   // 2 规格不存在
raw, src, err := settings.Global.EffectiveValue(sp.Scope, m.SettingKey, "")
if err != nil           { return true, "default" }   // 3 取值出错
if raw == nil           { return true, "default" }   // 4 值为 NULL
if err := json.Unmarshal(raw, &v); err != nil { return true, "default" }  // 5 值不是布尔
```

★ 复核 `settings/spec.go:284-317` 又查出**第六条**同形来源：优先级链是 **DB > env > default**，
第 3 步 `return b, "default", nil` 把 spec 自己的 `Default` marshal 出来。

⇒ ★★★ `source === "default"` 有**六种**成因，客户端**一种都分辨不出**。
⇒ 页面**不说**「已启用」，只说「**没能读到配置**」或「读自 db/env」。

#### ★★★ `source` 的取值集合被后端 doc 注释写死：只有三个

`settings/spec.go:281-283`：

```go
// Returns (rawValue, source, error) where source ∈ {"db","env","default"}.
```

★★★ **我第一版测试夹具里编了 `'platform'` / `'tenant'` 两个不存在的值。**
「夹具必须照抄后端」这条纪律又救了一次场 —— 但真正救我的是去读了那个函数的 doc 注释，
而不是凭「source 听起来像作用域名」去猜。

更糟的是我第一版把判读写成 `source !== 'default'`（「不是兜底就是真读到」），
这在实现上恰好也成立，却**没有钉住取值集合**：任何新出现的 source 都会被判成「真读到」。
⇒ 变异 T2 把实现退化成那一行，**全绿**。⇒ 补了「集合外的 source 不算真读到」这条判据。

#### ★★★★★★ 配置键是**三**态，不是两态（第一轮测绘漏掉的第三态）

`handleModulesGet` 的 config 循环（`admin/modules.go:816-833`）：

```go
raw, src2, err := settings.Global.EffectiveValue(sp.Scope, ck, "")
if err != nil { continue }                       // ← 静默跳过（spec 不存在 或 取值出错）
var v any
jsoncol.Decode("admin.modulesGet/config_value", raw, &v)   // ← **没有 raw == nil 判断**
config[ck] = map[string]any{"value": v, "source": src2, "spec": sp}
```

★ 对比 `resolveModuleEnabled` 是**有** `if raw == nil` 的。所以这里 `raw` 为 NULL 时
**不会**被 `continue` 跳过，而是 `Decode` 失败、`v` 保持 nil、**键照样进 `config`**。

⇒ 每个声明的配置键有**三**态：

| 态 | 现象 | 成因 |
|---|---|---|
| 1 | **键整个不在** `config` 里 | spec 不存在 **或** EffectiveValue 报错（两处 `continue`，静默、无日志） |
| 2 | **键在但 `value` 是 `null`** | 值确实是 NULL（读到了，只是没值） |
| 3 | 键在且有值 | 正常 |

⇒ 只报「缺了哪些键」会把第 2 态**误并进第 1 态**。页面三栏分开列。
★ 顺带：旧的 `moduleConfigCoverage` 把第 2 态算作「已取到」（键确实在），
而 `moduleMissingConfigKeys` 又看不到它 —— **同一个键，两条判据给出相反的结论**。

#### ★★★★★★ 跨端点自相矛盾：同一个开关键，两处能对不上

模块面和配置摘要读的是**同一个** `feishu_bot.enabled`，但判定函数不同：

- `resolveModuleEnabled` 的失败路径 fallback 到 **`true`**
- `readBool`（配置摘要里的）失败路径 fallback 到 **`false`**（Go 零值）

⇒ ★★★ 读失败时，`GET /{key}` 报 `enabled: true`、`GET /{key}/config` 报 `enabled: false`。
⇒ 页面**原样并列呈现两处**，不挑一个信、也不悄悄 reconcile。

#### ★★★★★★ 配置摘要的两个**零值产物**

`feishuBotConfigSummary`（`admin/modules.go:1289-1357`）的 20 个键是**逐个无条件赋值**的
⇒ 响应里这 20 键**永远都在**，少一个都算形状不符（⇒ 20 条「缺它必抛错」判据）。
但所有 `read*` helper 在 spec 不存在 / 取值出错 / raw 为 NULL / 解码失败四条路径上
**全部返回零值**，于是有两个假信号：

```go
summary["allowed_user_count"] = len(strings.Split(readString("feishu_bot.allowed_users"), ","))
// ★ readString 失败时返回 "" ⇒ strings.Split("", ",") == [""] ⇒ 长度是 1
summary["quiet_hours_window"] = readString(start) + "–" + readString(end)
// ★ 两端空时是字面量 "–"（U+2013 EN DASH），一个**看着像有值**的非空串
```

⇒ `allowed_user_count: 1` 不可分辨「一个都没配」与「恰好允许 1 个人」。
⇒ 「静默时段非空」**不等于**配了。

#### ★★ `/config` 只有 feishu_bot 实现 ⇒ 本仓**第一次**出现 501

```go
switch key {
case "feishu_bot":
	h.feishuBotConfigSummary(w, r)
default:
	writeError(w, http.StatusNotImplemented, "config endpoint not implemented for module: "+key)
}
```

（此前本仓出现过 400/403/404/405/500/503/504，501 是新增。）
⇒ 页面**先按 key 判断**再决定要不要给按钮，不去打注定 501 的请求；
真收到 501 时**单列**渲染，不混进「其他错误」。

#### ★★ `POST /{key}/test` 名字叫 test，但**真给飞书机器人发一条消息**

`admin/modules.go:1169-1180` 的 handler 注释明写「对 feishu_bot：发送一条测试消息到
`webhook_url`」⇒ **有外部副作用**，不是只读端点。本页一律不提供入口。
（`PUT /{key}/toggle` 是写操作，同样不碰。）

#### ★★★ `blocked_reason` 带 omitempty ⇒ 「没有阻塞」= 键**整个不存在**

`moduleStatusMap`（`:731-738`）里 `CanToggleEnabled = (blocked == "")`，两者**联动**。
⇒ 「没有阻塞」不是空串，是**键不存在**。⇒ `can_toggle_enabled: false` 时一定有非空
`blocked_reason`，形如 `需先启用依赖模块: A、B`（只统计 `dep.Required` 的）。

#### 读面三条的其余契约

- `GET /api/admin/modules` ⇒ `{items: [...]}`，`items` 是 `make(..., 0, len(defs))` ⇒ **永不为 null**
- `GET /{key}` ⇒ `{module, config}`，`config` 是 `make(map[string]any)` ⇒ 无键时是 `{}` 不是 `null`
- `GET /{key}/config` ⇒ **没有信封**，响应本身就是那 20 个字段
- 子树分派（`:1136-1161`）：`GET /{key}` / `PUT /{key}/toggle` / `POST /{key}/test` / `GET /{key}/config`，
  其余（含空 key、`/{key}/unknown`）⇒ **404 `unknown modules endpoint`**
- 503 有**两种文案**：`settings registry not initialised`（list/get）与
  `settings not initialised`（feishubot 摘要）—— 都是 settings 注册表没起，**不是角色问题**

#### ★★★ 本批的变异验证：47 条，挖出 **3 条真判据缺口**

变异结果与分诊（`RESTORED=OK`，逐字节比对还原）：

- **44/47 一轮就有牙**；余下 3 条经分诊**全是真缺口**，已补判据后二次跑满
- `T4`（`moduleIsAlwaysOn` 退化成「只看 `setting_key`」）→ 仍全绿
  ⇒ **缺口**：`setting_key === ''` 与 `source !== 'default'` 这两个条件**从没被拆开测过**
- `T12`（`unwrapModules` 把 `Array.isArray(m.items)` 换成 `m.items !== undefined`）→ 仍全绿
  ⇒ **缺口**：「键在但类型不对」这种形状**从没喂过**（裸数组被外层 `Array.isArray(resp)` 先挡掉）
- `V9`（尾行的 `can_toggle_enabled ? '可' : '被依赖挡住'` 换成恒显示「可」）→ 仍全绿
  ⇒ **缺口**：「不可切换」那一侧**从没被渲染过**
- 另 5 条是**我 expect 串猜错**（写了 i18n 文案、或指向了另一条用例的名字），
  红确实出现了、只是没命中我猜的名字 ⇒ 已按**实际 `it` 标题**改正
- 2 条是**注入本身没施上**：D6 缩进写错（6 空格 vs 实际 8）、D9 替换串忘带标记

★ 顺带修掉**产品真缺陷**：两个视图原本**一个刷新按钮都没有**（只读页没有任何重取手段），
是写「先成功 → 再失败」序列用例时才发现的 ⇒ 两页各补一个 `common.refresh`。

#### ★★★ 判据侧的自造错误（第 6 次 markdown 星号切断子串）

`modsDetail.configThreeStateNote` 的文案是「每个声明的配置键有**三**种状态」，
我断言 `toContain('三种状态')` —— `**` 把它切开了，**不是**子串。
⇒ 判据串必须从**实际文案**里挑一段**不含 markdown 星号**的（本次改用「种状态」+「不是两种」）。

★ 另一次是**否定断言被自己写的免责文案判红**：头号免责 `mods.fallbackNote` 把五条失败路径
**逐条列出来**，其中一条就叫「没有开关键」⇒ 全页 `not.toContain('没有开关键')` 恒被自己的说明命中。
⇒ 否定断言一律**按节点作用域**缩到条目内（`.mods__item .mods__note`）。

#### 顺带修掉的仓库级缺陷：4 处 U+FFFD 乱码

全仓扫 `U+FFFD`（非法 UTF-8 替换字符）发现 4 处**用户可见文案**里的汉字被吃掉，
均由前几批引入：

| 位置 | 原文 | 修成 |
|---|---|---|
| `src/i18n/zh-CN.ts:1363` | `这里◻◻◻的聚合计数` | `这里的聚合计数` |
| `src/i18n/zh-CN.ts:2128` | `理论◻◻◻不该发生` | `理论上不该发生` |
| `src/views/MaasOrdersView.spec.ts:16` | `跨租户要◻◻◻破` | `跨租户要突破` |
| `src/views/InjectionConfigView.vue:5` | `（有没有被命中）◻◻◻ /injection` | `（有没有被命中）在 /injection` |

⇒ ★ 建议在门禁里加一条「`src/**` 不得含 U+FFFD」的扫描：这类损坏**编译期完全合法**、
测试**照样全绿**，只有肉眼读文案才会发现。

#### 仍未上移

`PUT /api/admin/modules/{key}/toggle`（写）、`POST /api/admin/modules/{key}/test`（**有外部副作用**）。

**下一批候选**：`admin/logs` 的四条只读（`body-cache-stats` / `files` / `stats` / `archive/list`；
**混合档** —— `config`/`archive`/`cleanup` 三条是 superAdmin）、`system/session-context`（6）。

### 11.82 日志管理读面上移（第四十六轮，`admin/logs`）

本批把日志管理的**四条只读端点**上移，三条 superAdmin 写操作一律不碰。

- 新 API 模块 `web-mobile/src/api/logsAdmin.ts`（四端点各自独立解包）
- 新视图 `LogAdminView.vue`（缓存命中 / 目录统计 / 文件清单 / 归档列表，四面板合一页）
- 新路由 `/log-admin`
- 新增**一条 admin 档抽屉席** `log-admin`（**故意不设** `requiresRole`）
- i18n 新命名空间 **`logsAdmin`**
- 门禁：变异 **34/34 有牙**，用例 **76 条**（48 API + 28 视图）

#### ★★ 档位：四条只读全是 admin，但**同一前缀下混着三条 superAdmin**

`admin/handler.go`：

```go
mux.HandleFunc("/api/admin/logs/body-cache-stats", admin(h.handleBodyFetchCacheStats))   // :959
mux.HandleFunc("/api/admin/logs/config",  h.superAdmin(h.handleLogConfig))              // :1114 写 + 热加载
mux.HandleFunc("/api/admin/logs/files",   admin(h.handleLogFiles))                      // :1115
mux.HandleFunc("/api/admin/logs/stats",   admin(h.handleLogStats))                      // :1116
mux.HandleFunc("/api/admin/logs/archive", h.superAdmin(h.handleLogArchive))             // :1117 归档
mux.HandleFunc("/api/admin/logs/cleanup", h.superAdmin(h.handleLogCleanup))             // :1118 删除
mux.HandleFunc("/api/admin/logs/archive/list", admin(h.handleLogArchiveList))            // :1119
```

⇒ 上移的四条**全是 `admin(...)`** ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
★ 与第四十四轮 attachments 同样的坑：同前缀混档 ⇒ 移动端**不能按前缀判权限**。

#### ★★★★★★ 同一个「文件日志没启用」在三个端点上是**三个不同判据**

后端只有一个开关（`logging.ActiveConfig().File == ""`），但三处各自表达：

| 端点 | 判据 | 代码依据 |
|---|---|---|
| `files` | `dir === ''` | 结构体字段 `Dir string`，恒存在、可能为空串 |
| `stats` | `log_dir === ''` | 同上（字段名不同） |
| `archive/list` | **`'dir' in resp === false`** | map 字面量在那一支**根本没写**这个键 |

⇒ ★★★ 「看起来都是目录字段」，但**判据形态不一样**（空串 vs 键不存在）
⇒ 抽一个通用 helper 就会把其中一处说错 ⇒ 页面三处**各按各的**措辞，
并有一条判据专门断言**三段文案两两不同**。

#### ★★★★★★ `archive/list` 是异形端点：`dir` / `exists` 条件存在

`admin/log_management.go`：

```go
if cur.File == "" {
    writeJSON(w, 200, map[string]any{"archives": []any{}, "total": 0})                                  // ① :574
    return
}
entries, err := os.ReadDir(archiveDir)
if err != nil {
    writeJSON(w, 200, map[string]any{"archives": []any{}, "total": 0, "dir": archiveDir, "exists": false}) // ② :580
    return
}
writeJSON(w, 200, map[string]any{"archives": items, "total": len(items), "dir": archiveDir, "exists": true}) // ③ :609
```

⇒ ★★ TS 接口里 `dir` / `exists` 必须是**可选**字段；写成必填就是错的。
⇒ ★★ ① 与 ② 的 `archives` / `total` **完全一样**，只有「键在不在」能分开。
⇒ ★ 解包判据只能钉「`archives` 是数组且 `total` 是数字」——**不能**要求 `dir` 在场
   （要求了就永远收不到状态①，变异 T17 专门打这一条）。
⇒ `archives` 三态都非 nil ⇒ 永不为 null。

#### ★★★★★★ `files` 的 `is_archived` **永远是 false**

`internal/logging/logging.go:449`：

```go
out = append(out, LogFileInfo{
    Name: name, SizeBytes: info.Size(), ModTime: info.ModTime(),
    IsCurrent:    name == currentName,
    IsCompressed: strings.HasSuffix(name, ".gz"),
    IsArchived:   false,          // ★ 硬编码字面量，从来没算过
})
```

★ 而 `ListFiles` 的 doc 注释说「含轮转备份和**归档**」—— **注释是错的**：
`scanLogDir` 收的是**顶层目录**且 `if e.IsDir() { continue }`（不递归）
⇒ ★★ `files` **永远不包含** `archive/` 里的东西，归档只有 `/archive/list` 能看。
⇒ 客户端**不许**用 `is_archived === false` 推出「没有归档」。

★ 对照：`is_compressed` 是**真算的**（`.gz` 后缀）⇒ 同一个结构体里
**一个字段真算、一个字段写死**，页面必须分别对待。

★ 顺带：过滤条件 `!HasSuffix(".log") && !HasSuffix(".log.gz") && !HasSuffix(".gz")`
里的 `.log.gz` 分支是**冗余**的（`.gz` 已经覆盖）⇒ 实际过滤是 `.log` 或 `.gz`。

#### ★★★★ 三个「0 是二义的」

| 字段 | 二义的两种成因 | 代码依据 |
|---|---|---|
| `hit_rate` | 0% 命中率 **或** 一次流量都没有 | `logs_body_cache.go:138-141` `if total > 0 {…}`，否则留 0 |
| `disk_usage_pct` | 真的 0% **或** `diskUsageAt` 探测失败 | `log_management.go:364-366` `if err == nil {…}`，失败静默留 0 |
| `exists:false` | 未启用 **或** 目录不存在 | 见下面的三态 |

⇒ 三处都**不报错、不留标记** ⇒ 客户端分辨不出 ⇒ 页面必须并排展示原始计数。

★ `body-cache-stats` 的 doc 注释还写着「前端仅在 super admin 视图展示」，
与实际的 `admin(...)` **矛盾**（注释过时；与第四十二轮 model-policies 头注释同类）。

#### ★★★ `stats` 的三种状态共用同一个形状

```go
if cur.File == "" { writeJSON(w, 200, resp); return }   // ① 整个零值对象
resp.LogDir = dir; resp.Exists = dirExists(dir)
if !resp.Exists { writeJSON(w, 200, resp); return }      // ② 只有 log_dir 与 exists 非零值
```

① 与 ② 的**十个字段完全一样**（`exists:false`、全部数字 0、mtime 为 null），
**只有 `log_dir` 空不空能分开** ⇒ 判「未启用」**必须**看 `log_dir`，看 `exists` 没用。

★ `oldest_mtime` / `newest_mtime` 是 `*time.Time` **且没有 omitempty**
⇒ 这两个键**永远在**，无文件时是 **`null`**（不是缺键、也不是零值时间串）。

★ `total_files` 与 `archive_files` 是**分开的两栏**（命中 archive 路径就 `return`），
且判定是 `strings.Contains(p, sep+"archive"+sep)` **或**
`filepath.Base(filepath.Dir(p)) == "archive"` ⇒ **任意层级**里名叫 `archive`
的目录都算，不只是顶层那个。页面自己加的合计数要说明「后端并没有这个字段」。

#### ★★ 同族两种错误风格

- `files` 走 `os.ReadDir`，失败会 **500**（`writeInternalErr`）
- `stats` 的 `filepath.Walk` 错误**静默跳过**（`if err != nil || fi.IsDir() { return nil }`）
  ⇒ 读不了的条目在统计里**直接消失**，不报错、不留日志
- `archive/list` 的 `e.Info()` 失败 ⇒ `continue`，同样静默

#### ★★ 四条都有方法门 ⇒ 非 GET 一律 405 `method not allowed`

`body-cache-stats` 是唯一有 503 的（`body cache not initialized`）；
⚠️ 它的 doc 注释把文案写成 "not initialised"，**代码里是 "not initialized"** ⇒
客户端两种拼写都认。

#### 变异验证：34 条，一次跑满

- 变异 **34/34 有牙**，`RESTORED=OK`（逐字节比对）
- 重点几条：把三个「未启用」判据**统一成同一个**（T2）、把 archive 的解包
  **反过来要求** `dir` 在场（T17）、把 `Promise.allSettled` 改回 `Promise.all`（V4）、
  把 `statsDisabled` 接到错误的端点上（V1）—— 这四条正是本批想防的退化

#### ★★★ 本批的自造错误（四条，全是我的判据错，不是产品错）

1. **跨面板全页选择器**：`.la__item` 同时存在于 files 与 archives 两个面板，
   全页计数被默认夹具里那个日志文件算进去 ⇒ 补 `panelItems(w, 标题)` 按面板取。
2. **判据与夹具自相矛盾**：夹具写了 `is_current: false`，我却断言文案含「当前活动」。
3. **expect 串凭印象写**（第 7 次同类）：i18n 文案是「没有一个非归档日志文件」，
   我写的是「一个非归档日志文件都没有」—— 不是子串 ⇒ 改用实际文案片段。
4. **否定断言被自己写的免责文案判红**（记忆里记过的老坑，第 N 次）：全页
   `not.toContain('删除')` 被 `readOnlyNote` 里的「归档、删除都是 superAdmin
   档的写操作」命中 ⇒ 判据只能落在**可点元素**上（按钮数 + 按钮文案）。

#### 仍未上移

`PUT /api/admin/logs/config`（改轮转配置 + 热加载）、`POST /api/admin/logs/archive`、
`POST /api/admin/logs/cleanup`（三条都是 **superAdmin**，且后两条是删日志）。

**下一批候选**：`system/session-context`（6）。

### 11.83 会话上下文读面上移（第四十七轮，`system/session-context`）

本批把会话上下文的**两条只读端点**上移。四个写端点一律不碰。

- 新 API 模块 `web-mobile/src/api/sessionContext.ts`
- 新视图 `SessionContextView.vue`（抽取状态 + 批量标题，两面板）
- 新路由 `/session-context`
- 新增**一条 admin 档抽屉席** `session-context`（**故意不设** `requiresRole`）
- i18n 新命名空间 **`sctx`**
- 门禁：变异 **32/32 有牙**，用例 **64 条**（37 API + 27 视图）

#### ★★★★ 变异验证：首轮 23/32，分诊出 3 条真缺口 + 1 条**等价变异**

**真判据缺口（补判据后二次跑满）**：

1. `extractionStatusLacksDetailFields` 只断言过 B 形返回 `false`，
   **A 形返回 `true` 那一侧从没喂过** ⇒ 把它恒置 `false` 全绿。
2. 「出错时必须清空旧结果」只在**首屏就失败**时验证 ⇒ 而视图**首屏不自动加载**，
   首屏失败根本没有「旧结果」可留 ⇒ 必须造「先成功 → 再失败」序列。
3. id 列表的**分隔**只喂过换行 ⇒ 把 `.split(/[\n,;\s]+/)` 换成 `.split(/\n/)` 全绿
   ⇒ 补「逗号/分号/空格分隔」那条。
4. 同上，批量侧也缺「先成功 → 再失败」序列。

**★ 等价变异（已证实，不是判据无牙）**：

删掉 `loadStatus` 里 **catch 分支**的 `status.value = null` ⇒ 读数仍全绿。
实测确认：`loadStatus` 在 **`try` 之前**就已经 `status.value = null`
⇒ catch 里那一行是**死代码** ⇒ 行为不变 ⇒ **等价变异**。
⇒ 变异改成「**两处都删**」后才真的有牙（失败后旧结果真的留在屏幕上）。
★ 这就是「变异后仍绿先证明该变异可观测」的又一例：先查可观测性，
再判是等价变异还是判据无牙 —— 本例是**产品里的冗余代码**，不是判据的问题。

**注入自身的畸形（4 条）**：

- V8 把 `:disabled="…"` 换成 `:disabled="false" /* MUT-V8 */` ⇒
  **Vue 模板属性位不允许 JS 注释** ⇒ 编译错、`rc≠0` 却抓不到具名用例
  ⇒ 改用 `data-mut="MUT-V8"` 带标记
- V10 删 `v-if` 首行让 `v-else` 变孤儿 ⇒ 同上 ⇒ 改置 `v-if="false"`
- V1 / V12 的替换串忘带 `MUT-<id>` 标记
- V8 那条 rc≠0 却被我的 `collectionFailed` 正则**漏判**成「转红但未命中」
  ⇒ 正则里补了 `Error compiling template` / `Extraneous (closing|opening) tag` 等形态


#### ⚠️★ 前缀是 `/api/system/…`，不是 `/api/admin/…`

`admin/handler.go:1296`：

```go
mux.HandleFunc("/api/system/session-context/", h.admin(h.handleSessionContextRoutes))
```

★ handler 文件在 `admin/` 包里、路径也走 `h.admin`，但**对外前缀挂在 `/api/system` 下**
⇒ 照抄 `/api/admin/` 会 404。抽屉路径 `/session-context` 与 API 前缀不一致，别混淆。

档位 `h.admin(...)` ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。

#### ★★★★★★★★ 头号陷阱一：`extraction-status` 是**异形端点**

`admin/session_extract.go:231-277` 只有两种响应形状：

```go
// A 形（未抽取）—— 只有 2 个键
writeJSON(w, 200, map[string]any{"task_id": taskID, "extracted": false})
// B 形（已抽取）—— 8 个键
writeJSON(w, 200, map[string]any{
    "task_id": taskID, "extracted": true, "extracted_at": ..., "written": written,
    "skipped_noise": ..., "skipped_duplicate": ..., "status": status, "detail": detailObj})
```

⇒ A 形**没有** `extracted_at` / `written` / `status` / `detail` 这些键，
客户端**不许**把它们当空值渲染（变异 V1 专打「A 形也渲染 B 形字段」）。
⇒ 解包判据只能钉「`task_id` 是字符串且 `extracted` 是布尔」（变异 T5）。

#### ★★★★★★★★ 头号陷阱二：`extracted:false` 有**三种**成因，且**数据库故障也长这样**

三条路径返回**逐字节相同**的响应：

| 成因 | 代码位置 |
|---|---|
| 任务不属于你的租户（`assertTaskInTenant` 为假） | `:240-246` |
| 表里压根没这行（`sql.ErrNoRows`） | `:258` |
| ★★ **数据库查询出错**（任何 `err`） | `:258-263` |

```go
err := h.db.QueryRow(ctx, `SELECT … WHERE task_id = $1`, taskID).Scan(…)
if err != nil {
    writeJSON(w, http.StatusOK, map[string]any{"task_id": taskID, "extracted": false})
    return
}
```

⇒ ★★★ **数据库故障被报告成「没抽取过」，而且从不返回 500**。
⇒ ★★ **任务不存在也不是 404**，是 200 + `extracted:false`。
⇒ 页面只能说「**未能确定**」，不许说「没抽过」。

★ 租户隔离**只在** `IsTenantAdmin(r) && GetTenantID(r) != "" && GetTenantID(r) != "default"`
时才生效（`:239`）⇒ super_admin、或租户 id 为 `default` 的请求**完全不做归属检查**。
★ `titles/batch` **根本不做**租户隔离。

#### ★★★★★★★ 头号陷阱三：批量标题的 map **键里含一个字面 NUL**

`admin/session_title.go:381-383`：

```go
func sessionTitleMapKey(taskID, scopedSessionID string) string {
	return taskID + "\x00" + scopedSessionIDKey(scopedSessionID)   // ← 字面 NUL
}
```

★ `scopedSessionIDKey` 只是 `strings.TrimSpace`（`:274-276`）
⇒ 键形如 `"task-123\u0000scoped-456"`，`scoped_session_id` 为空时是 `"task-123\u0000"`（**带尾随 NUL**）
⇒ ★★ 客户端按 `titles[taskId]` 查**永远 miss** ⇒ 只能靠 `splitSessionTitleMapKey` 拆开显示
⇒ ★★★ **源码里必须写转义序列 `\u0000`，不能写字面 NUL 字节**：
本批第一版写了字面量，结果 `grep` 直接报 `Binary file matches`，
整份 .ts 变成「二进制」，任何按文本检索的工具都看不见内容
⇒ 这是第四十五轮修掉 4 处 U+FFFD 时那条教训的**同族变种**：
**不可见/控制字符会让工具静默降级，而编译期完全合法。**

#### ★★★★ `titles/batch` 是「只包成 POST 的只读查询」，且有四种分不开的「空」

- **GET ⇒ 405**：源码注释明说是为了给 request-logs 列表**一次往返**批量富化标题（`:611-613`）
- **限幅 `len(keys) > 500` ⇒ 400**（`:627-630`）★ 是 `>` 不是 `>=`
  ⇒ **正好 500 个合法**。这是本仓**第 10 种**限幅语义。
- 四种「空」全部是 `{"titles": {}}` + **200**：
  1. `keys: []`（早返回分支 `:623-626`）
  2. 每个键的 `task_id` 都是空串 ⇒ 静默 `continue`（`:640-642`）
  3. ★★ **数据库查询出错** ⇒ 直接返回空 map（`:362-364`）
  4. 真的没存过任何标题
- ★★★ **没有存过标题的键，整个键从 map 里消失** ⇒ **键缺失 ≠ 标题是空串**
- 重复的 `(task_id, scoped_session_id)` **静默去重**（`:643-647`）
- ★ `titles` 是 `make(map[string]string, …)` ⇒ 永不为 null
- ★ `rows.Err()` 在迭代中断时只 `slog.Warn`，仍按已取到的映射返回 **200**（部分结果）

#### ★★ 分派器的两条「同名不同码」错误

```go
rest = strings.Trim(rest, "/")
if rest == "" { writeError(w, 404, "task_id required"); return }        // :34-37
…
taskID := strings.TrimSpace(parts[0])
if taskID == "" { writeError(w, 400, "task_id required"); return }      // :54-57
```

⇒ ★ 同一句话 `task_id required`，**一个是 404、一个是 400** ⇒ 只能看状态码才能分。
⇒ `len(parts) == 1`（只有 taskId）与未知子动作 ⇒ 404 `unknown session-context route`。
⇒ 方法不匹配 ⇒ 405 `method not allowed`。

#### ★★ `titles/batch` 在 `{taskId}` 分派**之前**被特判

`:44-52`，注释明说是为了不让字面量 `"titles"` 被当成 task_id
⇒ ★★ **task_id 恰好叫 `titles` 的会话永远走不到自己的分支**。

#### ★★ `detail` 列的 nullability

```sql
COALESCE(detail, '{}'::jsonb)
```

★ `COALESCE` 只挡 **SQL NULL**，**挡不住 JSON 标量 null**
⇒ 列里存的是 JSON `null` 时，`detail` 就是 **`null`**。

#### ★★★ 本批顺带修掉一个**别人的类型缺陷**

`vue-tsc` 报 `src/api/authMeShape.test.ts` 的夹具比 `UserInfo` 多了
`last_login_at` / `created_at`。查后端 `admin/users.go:20-32`：
`userInfo` 是 **10 字段**的完整结构体，**这两个字段一直在发**
⇒ 是 **`src/api/client.ts` 的 TS 接口漏了两个字段**，夹具是对的。

★★ 但我第一次把它们设成**必填**，结果**打破另外 9 个 spec 文件**
（它们只构造前 8 个字段）⇒ 改回**可选**。
⇒ 教训：**改共享类型的必填性，影响面远大于触发它的那一处** ——
必须跑真的类型门（而不是只跑自己那批用例）才能发现。

★ `LastLoginAt *time.Time` **没有 omitempty** ⇒ 键恒存在，
从没登录过时是 **`null`**（本仓第三次见到这个形态）。

#### ★★★★ 本批的自造错误（六条）

1. **跨面板/跨作用域断言**：面板标题「记忆抽取状态」本身就含子串「抽取状态」，
   面板级 `not.toContain('抽取状态')` **恒不可满足**
2. **否定断言被自己写的免责文案判红**（记忆里那条老坑，本批又踩）：
   `not.toContain('没抽过')` 被免责里的「压根没抽过」命中；
   `not.toContain('未能确定')` 被免责末句「所以下面只能说「未能确定」」命中
   ⇒ 修法：断言一律落到 `.sc__cell-l` / `.sc__cell-v` 这个**数据格作用域**
3. **expect 串凭印象写**（累计第 7、8 次）：i18n 实际是「不会有空字符串这种值」，
   我连写两次「不会出现空字符串」「不存在空字符串」
   ⇒ 修法：**从 i18n 源文件里正则提取实际文案片段**，不再手打
4. **真产品缺陷**：`noStatusFieldNote`（「响应里没有状态/计数这些字段」）被我
   **无条件渲染**了 —— 已抽取时那些字段明明在，页面却在说「不存在」
5. **判别联合的对象字面量**：`ExtractionStatus` 是 `extracted: false | true`
   的判别联合，普通字面量会把 `extracted` 拓宽成 `boolean` ⇒ 需 `as const`
6. **i18n 门禁抓到我**：`en-US` 的 `undetermined: 'undetermined'`
   **值与键名相同** ⇒ 运行时显示裸键 ⇒ 改成 `'not determinable'`

#### 仍未上移

`POST /{taskId}/extract-to-memora`（抽取写入 + 调 memora）、
`POST /{taskId}/summarize-title`（**调模型**生成标题，有外部副作用）、
`PUT /{taskId}/title`（人工改标题）、`DELETE /{taskId}/title`。

**下一批候选**：MaaS settings（PUT）/ model-rates 写、routing-opt proposals 决策、
pending-responses DELETE、request-anomalies resolve、tenants 创建/更新
（以上按既有口径都属于写操作，本仓继续不碰；需要另找只读面）。

### 11.84 免费资源自动发现读面上移（第四十八轮，`admin/free-discovery`）

本批把 `free-discovery` **整族 7 条只读 GET** 上移 —— 这是全仓路由差集扫描（472 注册 / 449 已覆盖 / 23 未覆盖）
定位到的**唯一一整族未上移的只读面**。六个写端点（建模板 / PUT / PATCH / 删模板 / scan / import /
import-orbi）一律不碰。

- 新 API 模块 `web-mobile/src/api/freeDiscovery.ts`（七条**各自独立**解包，不抽通用解包器）
- 新视图 `FreeDiscoveryView.vue`（五个面板：模板 / 预设 / 任务列表 / 任务详情与结果 / 调度器）
- 新路由 `/free-discovery`
- 新增**一条 admin 档抽屉席** `free-discovery`（**故意不设** `requiresRole`）
- i18n 新命名空间 **`fd`**（101 个 `fd.*` + 1 个 `nav.freeDiscovery`）
- 门禁：变异 **52/52 有牙**，用例 **160 条**（96 API + 64 视图）

#### ★★★★★★ 头号缺陷一：任务列表的 `updated_at` **恒为 Go 零值**

`domains/freediscovery/discovery_engine.go:460-464`（`ListTasks`）：

```go
SELECT id, tenant_id, template_id, provider_code, status, trigger_type,
       COALESCE(triggered_by,''), started_at, completed_at,
       COALESCE(error_message,''), models_found, models_imported, created_at   ← ★ 没有 updated_at
FROM discovery_tasks ORDER BY created_at DESC LIMIT $1
```

而同一个结构体、同一个表的 `GetTask`（`:408-412`）**有**这一列：

```go
       COALESCE(error_message,''), models_found, models_imported, created_at, updated_at
```

⇒ ★★★★★★★★★★ **`GET /tasks` 里每个任务的 `updated_at` 恒为 `"0001-01-01T00:00:00Z"`**，
而 **`GET /tasks/{id}` 给的是真值**。
⇒ **同字段、同名、同结构体、两个端点、两个值、都 200。**

`scanTemplate` 走的是**同一份列清单**（`template_manager.go:401-438`，`Get` 与 `List` 共用），
所以**模板面没有这个病** —— 只有任务面有。这正是「不能从一个端点外推到另一个端点」的一例。

处置：页面在列表里**不显示** `updated_at`，改为常驻一条免责说明 + 每行一个
`data-fd="updated-at-unreadable"` 标记节点；详情端点才显示真值。
判据同时钉住两侧（列表里出现零值时间 ⇒ 红；详情里被说成「读不出来」⇒ 红）。

#### ★★★★★★ 头号缺陷二：「空列表」在本族有**两种表示**

| 端点 | 代码 | 空的时候 |
|---|---|---|
| `templates` | `free_discovery.go:121-123` `if tpls == nil { … }` | `[]` |
| `tasks` | `:383-385` `if tasks == nil { … }` | `[]` |
| `results` | `:439-441` `if results == nil { … }` | `[]` |
| **`presets`** | **`:218` `var out []presetView`** | **`null`** |

⇒ ★★★ 同一个 handler 文件里四条列表端点，**三条做了 nil-guard、一条没做**。
⇒ 客户端**不能统一按数组处理**：`presets` 为 `null` 是合法响应，必须**原样保留那个 null**
（降级成 `[]` 就丢掉了「后端没兜底」这条信息）。页面分别渲染 `data-fd="presets-null"` 与
`data-fd="presets-empty"` 两个**不同节点、不同文案**。

#### ★★★★★★ 头号缺陷三：**按方法分档**（又一条「不能按路径判权限」）

`admin/handler.go:1302-1309`：

```go
mux.HandleFunc("GET  /api/free-discovery/templates",      h.admin(h.handleFreeDiscoveryTemplates))
mux.HandleFunc("POST /api/free-discovery/templates",      h.admin(h.handleFreeDiscoveryTemplates))
mux.HandleFunc("GET  /api/free-discovery/templates/{id}", h.admin(h.handleFreeDiscoveryTemplateByID))
mux.HandleFunc("PUT  /api/free-discovery/templates/{id}", h.admin(h.handleFreeDiscoveryTemplateByID))
…
```

★ 同一个 handler、同一条路径，GET 注册为 `h.admin`，写方法也注册为 `h.admin` ——
但 handler 内每个写分支开头都有 `RequireSuperAdminForWrite(w, r)`（`free_discovery.go:127 / 170 / 186`）
⇒ **GET 是 admin 档，POST/PUT/PATCH/DELETE 是 superAdmin 档。**
⇒ 抽屉席（读面 admin）**不设** `requiresRole`；但同路径的写操作 tenant_admin 拿到 **403**
`tenant_admin has read-only access; write operations require super_admin`。

#### ★★★★★★★ 头号缺陷四：super_admin 看到的**不是全部租户**

`free_discovery.go:100-103` → `admin/context.go:60-65`：

```go
func EffectiveTenantID(r *http.Request) string {
    if IsTenantAdmin(r) { return GetTenantID(r) }
    return "default"
}
```

⇒ ★★★★★★★ **super_admin / legacy admin_key ⇒ `"default"`，只有这一个租户。**
本仓存在 `EffectiveTenantIDAll`（`admin/context.go:69-74`，返回 `""` = 查全部），
但**这条线没有用它** ⇒ 又一条「按角色判可见范围会判反」的陷阱。

#### ★★★★★★ 头号缺陷五：两种「scheduler 不存在」是**两种表示**

- `h.scanSchedulerStatus == nil` ⇒ **503** `scan-scheduler is not available`（`free_discovery.go:84-87`）
- 接口里装着 typed-nil `*ScanScheduler` ⇒ `Status()` 走 `bg/scan_scheduler.go:509-511`
  返回 `ScanSchedulerStatus{Enabled: false}` ⇒ **200** + `interval:""` + 全 0 计数 + **无 `last_error`**

⇒ ★★ `interval === ""` 是 typed-nil 分支的**唯一指纹**（真 scheduler 至少有 `"0s"`）。
⇒ ★★★ 而且它是全族**唯一不查 `fdDeps`** 的端点 ⇒ **六条都在 503 的同时它可以 200**。

#### ★★★★ 方法检查与依赖检查的顺序不一致

`presets` 是**唯一 405 在 503 之前**的（`:202-208`），其余五条都是先 `fdDeps(w)` 再判 method。

#### ★★★★ `limit` 是**回落 50 而不是 clamp 到 200**（本仓第 11 种限幅语义）

`discovery_engine.go:445-449`：

```go
// ListTasks lists the tenant's tasks (newest first; limit capped at 200).   ← ★ 注释说 capped
func (e *DiscoveryEngine) ListTasks(ctx context.Context, tenantID string, limit int) ([]*DiscoveryTask, error) {
    if limit <= 0 || limit > 200 { limit = 50 }                             ← ★ 实现是回落 50
```

- `200` **合法**（就是 200）；**`201` ⇒ 50**（不是 200）
- `limit, _ := strconv.Atoi(...)`（`free_discovery.go:375`）**丢弃 error** ⇒ `?limit=abc` ⇒ 0 ⇒ 50

⇒ **注释与实现不符**。页面在用户填的值被静默改写时显式提示，并复刻生效值。

#### ★★★★ `?status=` 未知取值**不被拒**

`:429-432` 空 ⇒ 默认 `"pending"`；`discovery_engine.go:519` `status != "" && status != "all"` ⇒ 加 `AND import_status=$2`
⇒ `?status=bogus` ⇒ **200 + 空数组**，与「没有 pending 结果」**分不开**。

#### ★★★ 其余六条已查实的契约

1. **`enabled` 只认字面 `"true"`**（`:115` `== "true"`）⇒ `"1"`/`"TRUE"`/`"yes"` ⇒ **过滤关闭，返回全部**
2. **`DiscoveryTask.template_id` 是 `*int64` 且没有 `omitempty`**（`types.go:152`）⇒ 键**恒存在**，
   模板被删后（`ON DELETE SET NULL`）是 **JSON `null`** ⇒ 按 `typeof === 'number'` 校验会**拒掉合法形状**
3. **`DiscoveryResult.tenant_id` 不来自 DB**：`ListResults` 的 SELECT（`:512-517`）**没有这一列**，
   Go 侧 `r.TenantID = tenantID`（`:544`）用**请求者的租户**回填
4. **四个 0 全是二义**（`COALESCE(…,0)`）：`context_window` / `max_tokens` / `monthly_tokens` / `daily_tokens`
   ⇒ **对照**：`free_type` 的 `''` **不是**二义 —— `CHECK (free_type IN (…7 值…))`（084 迁移 `:107-110`）
   **不允许 `''`** ⇒ `''` **唯一**对应 SQL NULL ⇒ 反而是确定的「推断不出」
5. **`ProviderTemplate.APIKeyEncrypted` 是 `json:"-"`**（`types.go:93`）⇒ 密文**从不下发**；
   而 `HasCredential()`（`:111-113`）还看 `len(APIKeyEncrypted) > 0`
   ⇒ ★★★ **客户端算不出这个谓词**：`api_key_env === ''` 同时意味着「无认证」和「有密文但没 env 引用」
6. **两条 404 的 detail 形状不同**：模板 = sentinel 原文（**不带 id**，`template_manager.go:148`）；
   任务 = `fmt.Errorf("%w (id %d)")`（`discovery_engine.go:424`）⇒ **带 `(id 42)`**
7. `presetView`（`:209-217`）**丢了 `tos_url`** ⇒ `ProviderPreset` 有 `TosURL`，但**客户端永远拿不到预设的条款链接**
8. **两个 `omitempty` 键**：`last_scan_failure_at` / `auto_disabled_at` ⇒ **条件存在，缺失 ≠ null**
9. **同族两个 500 文案**：`templates` GET 直接 `writeInternalErr`（`:118`）⇒ `internal error (see server logs)`；
   另外五条走 `writeFDErr` ⇒ `free discovery request failed`

#### ★★★★ 变异验证：首轮 44/52，分诊出 **6 条真等价变异 + 1 条 expect 指错 + 1 条判据缺口**

**★ 真等价变异（6 条，全部是产品里的死代码）**

V14–V19 都是「抛错时不清空」。第一版只删 `catch` 分支那一行 ⇒ **仍全绿**。
实测确认：六个 loader 的 `ref.value = null` 都写在 **`try` 之前**，
所以 `catch` 分支里那行是**死代码** ⇒ 行为不变 ⇒ **等价变异，不是判据无牙**。
⇒ 改成「**两处都删**」后 6 条全部有牙（失败后旧数据真的留在屏幕上）。

★ 这是第四十七轮 §82.10 那条结论的**独立复现**（不同文件、不同视图）。
⇒ 「变异后仍绿先证明该变异可观测」这条纪律，第二次拦下了一个会被误判成「判据无牙」的结论。

**★ 判据缺口（1 条，已补）**

A9 把「任务详情解包」换成「先套列表信封再取第一项」⇒ **仍全绿**。
因为抛错时机一样、只是**错误文案里的端点名**从 `tasks/{id}` 变成了 `tasks`。
⇒ 补一条钉住端点名的断言后转红。
⇒ ★ 这类「把 A 端点实现换成 B 端点实现」的变异，只有**断言错误文案的来源**才抓得到。

**★ expect 串指错（1 条，已改）**

A31 让 `scanSchedulerMissingMessage` 也认 `free-discovery is not available (...)`
⇒ 确实转红了，但命中的是「六条 deps 端点的 503 文案」那条（它断言 scheduler 判据对 deps 文案为 false），
而我写的 expect 指向了另一条只断言反方向的用例。
⇒ ★★ **转红但未命中预期用例名 ≠ 缺陷**，要读具名红再判归属，不能直接当「判据缺口」处理。

**注入自身**

- Vue 模板里一律用 `data-mut="MUT-<id>"` 带标记（属性位不允许 JS 注释，见第四十七轮 §11.83）
- `--dry` 模式先验 52 条注入全部匹配上（`注入匹配 52/52`）**才**跑用例 —— 纪律要求「当场验证匹配数」

#### ★★ 视图用例的三个断言纪律（本轮新踩）

1. **★ 含 `{占位符}` 的 i18n 值不能整串匹配渲染文本** ——
   渲染后占位符已被替换成实参，字面量对不上。首轮 **10 条失败里 8 条**栽在这。
   ⇒ 修法：取第一个 `{` 之前的**字面量部分**做前缀断言（`head()` helper），仍然不手打文案。
2. **★ 自造免责文案会把否定断言喂饱** —— 只读说明那句话里就列了「触发扫描 / 批量导入 / 删模板」，
   于是「页面不该出现写操作词」的全页 `not.toContain` **恒被自己的文案判红**；
   同理免责文案里写了「（0001-01-01）」，于是「零值时间不该出现」的**全面板**否定断言也恒红。
   ⇒ 修法：分别落到**按钮文本集合**与 **`.fd__cell-v` 数据格作用域**上。
3. **★ 面板标题/说明会喂饱面板级 `not.toContain`**（第四十七轮已记，本轮在「零值时间」那条再次确认）

#### 仍未上移

`POST /api/free-discovery/templates`（建模板）、`PUT|PATCH|DELETE /templates/{id}`（改/删模板）、
`POST /api/free-discovery/scan`（触发扫描）、`POST /api/free-discovery/import`（批量导入）、
`POST /api/free-discovery/templates/import-orbi`（导入 Orbi 模板，1 MiB 上限）。
以上按既有口径都属于**写操作**，本仓继续不碰。

**差集里剩下的「候选」已几乎全是写操作。** 真正还没上移的只读面，
全仓扫描下来只剩 `GET /api/credentials/{id}/models/{model}/state` 一条（档位待查）。
⇒ **下一批候选**：那一条单端点，或按需扩到 credentials 族的其余只读面。

### 11.85 凭据×模型状态读面上移（第四十九轮，`credentials/.../state`，**superAdmin 档**）

全仓路由差集扫描（472 注册 / 449 已覆盖 / 23 未覆盖）里**最后一条还没上移的只读面**：
`GET /api/credentials/{id}/models/{model}/state`。同族另外三条是 **superAdmin 档且真的会触发一次探测**
（有外部副作用）⇒ 一律不碰。

- 新 API 模块 `web-mobile/src/api/credentialState.ts`
- 新视图 `CredentialStateView.vue`（单面板：查询 + 十格 + 分层说明）
- 新路由 `/credential-model-state`
- 新增**一条 superAdmin 档抽屉席** `credential-model-state`（**要**设 `requiresRole`，并同步
  `AppDrawer.spec.ts` 的白名单）
- i18n 新命名空间 **`cs`**（47 个 `cs.*` + 1 个 `nav.credModelState`）
- 门禁：变异 **33/33 有牙**，用例 **79 条**（38 API + 41 视图）

#### ★★ 方向与前几批相反：这一条**要**设 `requiresRole`

`admin/credential_state_handlers.go:174-180`：

```go
func (h *Handler) registerStateRoutes(mux *http.ServeMux) {
	wrap := h.superAdmin                                                     // ← ★ 不是 h.admin
	mux.HandleFunc("POST /api/credentials/{id}/test", wrap(h.handleTestCredential))
	mux.HandleFunc("POST /api/credentials/test-batch", wrap(h.handleBatchTestCredentials))
	mux.HandleFunc("POST /api/credentials/{id}/models/{model}/test", wrap(h.handleTestCredentialModel))
	mux.HandleFunc("GET  /api/credentials/{id}/models/{model}/state", wrap(h.handleCredentialStateQuery))
}
```

⇒ 前几批（`free-discovery` / `logs` / `modules` / `session-context`）的抽屉席都**故意不设** `requiresRole`，
**照抄那条会把这页暴露给租户管理员**。视图用例专门加了一条反向判据：
前三批的 key 仍**不设** `requiresRole`，本批的 key **必须**设。

#### ★★★★★★★★★★ 头号陷阱一：这一族系列的错误是 **text/plain**，而且**带尾换行**

四个 handler 全部用 `http.Error(w, msg, code)`（`:143 / :158 / :185 / :198`），
而本仓其它端点用 `writeError(w, code, msg)` ⇒ `{"error":{"detail":msg}}`。

⇒ ★★★★ 响应体是纯文本，**`error.detail` 根本不存在**。
  移动端 `client.ts` 的 `errorMessage()` 走 `JSON.parse` 失败分支 ⇒ **原样返回整段文本**。
⇒ ★★★★★★ 而且 `http.Error` 走 Go 的 `fmt.Fprintln(w, error)` ⇒ **追加一个换行符**
  ⇒ 移动端拿到的是 `"state service not available\n"`
  ⇒ **任何带 `$` 锚点的正则都匹配不上**，任何 `toBe(原文)` 都失败。
⇒ ★★★ `admin` 包里 `http.Error` 共 **165 处**、横跨 20+ 文件 ⇒ **不是孤例**，
  已上移的页面里凡是用 `http.Error` 的端点，其报错文案都带这个不可见字符。
⇒ 本批所有错误判据一律**不做 `$` 锚点**，并另给 `stripHttpErrorNewline` / `HTTP_ERROR_TRAILING_NEWLINE`。

★ **变异 A17 的分诊（真等价变异 + 「双重保护」）**：
第一版给 `stateServiceMissingMessage` 加 `$` 锚点 ⇒ **仍全绿**。
实测发现判据里已经「**先 `stripHttpErrorNewline` 再 `.trim()`**」⇒ 尾换行根本到不了正则面前
⇒ **等价变异**。改成**两处都拆**（strip 变恒等 + 判据不再 `trim`）后才有牙。
⇒ ★★ 与第四十八轮的「两处都删」同一形态：**单侧改动被另一侧的防御抵消，就是等价变异**。

#### ★★★★★★★★★★ 头号陷阱二：`state` **可以是 `null`** —— 三层缓存全 miss

`domains/credentialstate/manager.go:738-765`：

```go
func (m *Manager) GetState(ctx context.Context, credID int, model string) (*State, error) {
	if state, ok := m.getFromMemCache(key); ok { return state, nil }        // L1 内存
	if state, err := m.getFromRedis(ctx, key); err == nil && state != nil { … }  // L2 Redis（★err 被吞）
	state, err := m.getFromDB(ctx, credID, model)                            // L3 DB
	if err != nil { return nil, err }
	if state != nil { … }
	return state, nil          // ★★ state 可能是 nil，且**不报错**
}
```

handler（`:152-167`）只判 `err != nil` ⇒ `state == nil` 时照样序列化：

```go
_ = json.NewEncoder(w).Encode(map[string]any{
	"credential_id": credID, "model": model, "state": state,   // ← state 是 nil ⇒ JSON null
})
```

⇒ ★★★★ 「从没探测过」返回 **200 + `{"credential_id":N,"model":"M","state":null}`**，**不是** 404。
⇒ 按 `state.available === boolean` 之类校验会**拒掉合法形状**。
⇒ 视图单列 `data-cs="state-null"` 节点，并且**null 态下不渲染任何数据格**
  （「没量过」绝不能显示成「可用」）。

#### ★★★★★★★★★★ 头号陷阱三：五条指标在**两条 DB 分支里根本没被赋值**

`cache.go:118-134`（`node_probe_state` 支）只填 7 个字段：
`CredentialID / Model / Available / ConsecutiveFails / LastUpdatedAt / RecoverAt / Source`。
`cache.go:144-202`（`model_probe_state` 旧支）只填 5 个。

⇒ ★★★★ `success_rate` / `avg_latency_ms` / `p95_latency_ms` / `active_sessions` / `concurrency_limit`
  在**两条 DB 分支里都是 Go 零值** ⇒ 序列化成 `0`。
⇒ ★★★ 而缓存命中时（探测写入的完整 State）它们**有真值**
  ⇒ **同一个 (凭据, 模型) 第一次查是 0、缓存后再查可能是 0.87**，两次都是 200，
  **没有任何字段说明差异来自哪一层**。
⇒ `success_rate === 0` 有**三种**成因（DB 没实现 / 真的是 0% / 从没成功过），分不开。

★ 顺带：`getFromDB` **先查新表 `node_probe_state`，miss 才回退旧表 `model_probe_state`**
⇒ 同一端点在**新探测模式**与**老部署**下返回的形状可能不同。

#### ★★★★★★★★ 头号陷阱四：两处枚举的**注释是不全的**

| 字段 | 注释（`state.go:15,27`） | 实际还会出现 |
|---|---|---|
| `source` | `request, probe_v2, model_probe, passive, manual` | `node_probe_db`（`cache.go:133`）、`db`（`cache.go:199`） |
| `health_status` | `healthy, warning, degraded, unreachable` | `healthy_confirmed` / `probing` / `available`（`cache.go:184-187`）、**空串**（`cache.go:131-133` 未赋值时） |

⇒ ★★ 按注释建枚举会把**真实取值判成异常**。
⇒ ★★ `health_status === ''` **不等于「健康」** —— 它表示「没被判为不可达」。
⇒ ★★ `source` 记的是「**谁写的**」，**不是**「从哪一层读的」⇒ 命中层**不可判**。

#### ★★★★ 静默降级两处

- `manager.go:748` `if state, err := m.getFromRedis(...); err == nil && state != nil`
  ⇒ ★★★ **Redis 挂掉 / 超时 / 反序列化失败全部被吞掉**，直接落到 DB，无任何信号
  ⇒ ★★ 因此「500 `failed to get state`」**不会**由 Redis 挂掉引起 —— 客户端看到的只是一次成功的 DB 查询
- `cache.go:138` `isUndefinedTable(nodeErr)`（PG 错误码 42P01，表不存在）
  ⇒ ★★★ **表不存在被当作「这一支没数据」**，静默落到旧表

#### ★★★ 时间字段的两处陷阱

- `cache.go:128` `LastUpdatedAt: time.Now()` ⇒ `node_probe` 支的「最后更新」是**查询时刻**，不是探测时刻
- `cache.go:190-192` `lastAttemptAt` 为 `*time.Time`，为 NULL 时**保持 Go 零值**
  ⇒ ★★★ `last_updated_at` 可能是 `"0001-01-01T00:00:00Z"`
  （本仓**第 2 处** Go 零值时间；第 1 处是第四十八轮 free-discovery `ListTasks` 的 `updated_at`）
- `cache.go:197` `state.RecoverAt = nextRetryAt` 是**值类型** `time.Time`
  ⇒ 即使是零值也会赋 ⇒ 该 `omitempty` 键在这两条分支里**恒存在**，且可能是 Go 零值

#### ★★ 其余已查实的契约

1. `parseCredentialID`（`:182-189`）：`Atoi` 失败**或** `<= 0` ⇒ 同一句 **400** `invalid credential ID`
2. `model == ""` ⇒ 400 `model is required`（`:142-145`）——★ **实际不可达**：Go 1.22 的 `{model}` 不匹配空段，
   而 ServeMux 会先把 `//` 清理掉 ⇒ `/models//state` 被重定向成 `/models/state`
3. 503 `state service not available`（`:197-199`）；`Enabled() = m != nil && m.db != nil`（`manager.go:899`）
4. 500 `failed to get state`（`:158`）
5. 成功响应用 `json.NewEncoder(w).Encode(...)` ⇒ **末尾带换行符**
6. `State` 是 12 个恒存在键 + **4 个 `omitempty` 键**（`last_success_at` / `last_failure_at` / `recover_at` / `last_error`）
7. 两条自相矛盾的组合**后端不可能产生**，页面单列成异常上报：
   `health_status === 'unreachable'` 却 `available === true`（`cache.go:131-133` 两者同时设）；
   `available === false` 配 `healthy_confirmed/probing/available/healthy`（旧支从不把 `available` 置否）

#### ★★★ 变异验证：首轮 23/33，分诊出 8 条 expect 指错 + 2 条**变异自身写错**

**★ 变异自身写错（2 条，都不是判据问题）**

1. **V1 第一版是个空变异**：只往抽屉席插了一行注释、**没有真删** `requiresRole` ⇒ 行为没变 ⇒ 必然全绿。
   ⇒ 改成**真删**那一行后转红。★ 这正是「变异注入失败被读成判据无牙」的形态，
   而 `--dry` 只能验「匹配上了」，**验不出「改了行为」** ⇒ 仍全绿时必须先查变异本体。
2. **A17 第一版被双重保护**（见上文「头号陷阱一」）。

**★ 8 条 expect 串指错**：转红了，但命中的不是我写的那条用例名 —— 逐条读具名红后改正。
⇒ ★★ **「转红但未命中预期用例名」≠ 缺陷**，这条纪律本轮又用上 8 次。

**★★ 本批最有价值的一条：`vue-test-utils` 的 `.text()` 会 `trim()`**

V11 把视图里的 `stripHttpErrorNewline(errMsg)` 换成 `errMsg` ⇒ **仍全绿**。
原因不是等价变异，而是 **DOM 断言根本看不见尾换行**：
`wrapper.text()` 返回的是 `element.textContent?.trim()`。
⇒ 修法：断言改读 **`element.textContent`** 原始值（本批已改成 `w.find(...).element.textContent`），
  并同时保留 `.text()` 断言给可读部分。
⇒ ★★★ **凡是断言「不可见字符」是否存在，必须绕过工具的规范化，直接读原始 DOM 文本。**
  同族：不可见字符、零宽字符、大小写折叠、全角半角 —— 都会被工具悄悄规范化掉。

#### 仍未上移

`POST /api/credentials/{id}/test`（单凭据快速探测）、
`POST /api/credentials/test-batch`（批量快速探测，上限 100）、
`POST /api/credentials/{id}/models/{model}/test`（按模型手动探测）。
三条都是 **superAdmin 档且真的会触发一次探测**（有外部副作用）⇒ 本仓继续不碰。

**至此，全仓路由差集里「只读且未上移」的部分已经扫空** ——
剩下的全部是写操作或有外部副作用的端点。若要继续上移，需要先定「哪些写操作允许在移动端暴露」的策略，
这属于范围决策，不在「只读面复制」的既定口径内。

---

## 11.86 第五十批：凭据写操作面补测 + `reset-state` 端点首次接入（2026-10-08）

### 起因：一次自查推翻了上一条结论

上一条回复把「强制恢复」列成待决策项，**这是错的**。
`forceRecoverCredential` 早已实现，`NodesView.vue:244` 有入口、有二次确认、测试里有 mock。
⇒ **在把某项能力写进「待办/待决策」之前，先 grep 一遍确认它是否真的缺。**
  凭印象记账会凭空造出一个不存在的缺口，并让用户以为要替他做决定。

本批真正的缺口是另一件事：**`credentialsOps.test.ts` 整个文件只测了一个只读解包函数，
七个写操作函数一个都没测**，`resetCredentialState` 与 `submitBatchProbe` 更是**零引用**。

### 后端实读：`routing_reset.go` 逐字段

`POST /api/routing/credentials/{id}/reset-state`，注册 `admin/handler.go:946` = **`h.superAdmin`**。

| 位置 | 事实 |
|---|---|
| `:56-60` | 405 `method not allowed` |
| `:62-65` | 400 `id path param must be a positive integer` |
| `:69-72` | 400 `reason is required for audit trail`（reason 是**审计留痕**，不是备注） |
| `:37-39` | `raw_model` 空串 = **整凭据**复位 |
| `:93-96` | ★ `actor` 回退是 `r.RemoteAddr`（**裸 IP**）；对比 `fdActor` 回退 `"legacy-admin-key"` ⇒ **同仓两套回退** |
| `:98-104` | `beforeAfter` 恒含 5 键：`credential_id` / `raw_model` / `reason` / `endpoint`(恒 `"reset-state"`) / `actor` |
| `:106-119` | ★★★ 部分失败支 |
| `:125` | `trigger_probe` 守卫 `req.TriggerProbe && h.probeSubmitter != nil` |
| `:136-140` | `autoHealOneShot` **独立于** `trigger_probe`，命中才加 `auto_heal_pairs_submitted` |
| `:146-153` | 真实响应**只有 6 个键**：`message` / `credential_id` / `raw_model` / `actor` / `probe_triggered` / `details` |

★ 移动端原先的 TS 接口是**凭空造的**：写了 `success` / `reset_fields`（后端一个都不发），
又漏了 `raw_model` / `actor` / `probe_triggered` / `details` 四个。本批按后端逐字段重写，
并把原先的 `req<ResetStateResult>` **零校验**直传改成逐键校验的 `unwrapResetState`。

### ★★★★★★★★ 缺陷一：`partial_failed` 分支的错误响应里**没有** `details`

```go
if err := h.applyForceEnable(...); err != nil {
    if committed { beforeAfter["audit_outcome"] = "partial_failed"; h.logAudit(...) }
    writeInternalErr(w, "internal error (see server logs)", err)   // ← 5xx
}
```

`writeInternalErr` → `writeError` → `{"error":{"detail":"internal error (see server logs)"}}`。
⇒ **`db_committed` / `audit_outcome` / `details` 一个都不在响应里**，
客户端拿到的 5xx 与「DB 完全没动」在报文上**逐字节相同**。

推论一：`audit_outcome` 只对**审计消费方**可见，HTTP 客户端永远读不到。
推论二：**失败必须按 status 分档**，`resetStateOutcomeAmbiguous(status)`：
- 4xx（400/401/403/404/405）**全部**在 `applyForceEnable` 之前 return ⇒ 能证明「什么都没发生」；
- 5xx ⇒ 假定「可能已改」；
- ★ `status === 0`（`client.ts:172` 把传输层失败归一成 `network_error`）与
  `status === undefined`（`EpochError` / 非 `ApiError`）**同样二义** ——
  请求可能已到达服务端并落库，只是回程断了。
  ⇒ **「失败 = 没生效」这个直觉在两个方向都错。**

⚠️ 本批第一版写的判据是 `resetStatePartiallyFailed(msg, details?)`，
读的是 `details.audit_outcome` —— **那个值在 HTTP 路径上永远拿不到**，是条不可达的判据。
它「看起来对」是因为读的是后端源码里的字段名，但没追问**该字段是否会出现在响应体里**。
⇒ 写完判据要追问一句：**这个值在这个调用点上真的可得吗？**

### ★★★★★★ 缺陷二：`probe_triggered` 的 `false` 分不开「没请求」与「请求了但没生效」

后端写的是 `req.TriggerProbe && h.probeSubmitter != nil`。提交器没接线时**静默**降级成 `false`，
而客户端无从知道后端接没接 ⇒ `resetStateProbeIndeterminate(r, requested)` 必须把
「请求了却拿到 false」单列为**未能确定**，UI 不能承诺「已触发探测」。
反向也要守：`probe_triggered: true` 只代表 fire-and-forget 的**已提交**，不代表探测通过
（提交是 `h.probeSubmitter(credID, m, ...)`，错误不传播）。

三档文案因此必须互斥：`resetProbeSubmitted`（已提交 + 免责句）/
`resetProbeIndeterminate`（未能确定）/ 空（压根没请求探测）。

### ★★★★★★★ 本轮实抓的**前端**缺陷：await 期间 ref 被清空

现象：`reset-state` 的「未能确定」提示**永远不出现**，尽管请求体里明明带了 `trigger_probe=true`。

链条：
1. `runConfirmedOp()` 开头 `confirmOpen.value = false`；
2. `AppConfirm` 随之 emit `update:model-value(false)`；
3. 视图绑的是 `@update:model-value="(v) => { if (!v) resetOpState() }"`，
   而 `resetOpState()` 会把 `resetTriggerProbe` 清成 `false`；
4. ★ **Vue 的响应式 flush 发生在 `await` 期间** ⇒
   `const r = await resetCredentialState(..., resetTriggerProbe.value)` 的**实参**求值在前（拿到 `true`），
   回调里 `resetStateProbeIndeterminate(r, resetTriggerProbe.value)` 的**读值**在后（拿到 `false`）。

修法：**在 `await` 之前把值快照成局部变量**，`await` 之后只用快照。

⇒ ★★★ 通则：**`await` 之后再读任何会被「关闭对话框 / 重置表单」清空的 ref，拿到的都是过期值。**
  同一次 `runConfirmedOp` 里 `reasonText` 有同样的暴露，只是它作为**实参**在 `await` 前求值才幸免。
  这类缺陷**不会让任何用例变红**（功能看起来「正常地什么都不显示」），
  只有当判据明确要求那条提示存在时才会暴露 —— 这也是把它写成独立用例的价值。

### 变异验证：37 条，36 有牙 + 1 真等价

| 组 | 条数 | 覆盖 |
|---|---|---|
| A 解包与守卫 | 9 | 6 个必填键、details 5 键、类型校验、两个可选键清单 |
| B 失败分档 | 4 | 5xx / `status===0` / `undefined` / `>=500` vs `===500` |
| C `probe_triggered` 三义 | 3 | 丢掉 `requested`、恒 false、`!==false` |
| D 小判据 | 3 | 裸 IP（含 IPv6）、整凭据、空 reason |
| E 批量上限 | 2 | 不截断、上限挪到 101 |
| F 其余写操作 URL/body | 4 | `manual_disabled` 键名、clear 专用端点、两处 id 守卫、`raw_model` 缺省 |
| G 视图 | 9 | **快照回退**、失败分档、reason 必填、角色分档、默认理由、`probe_triggered=true` 分支、`raw_model` 传值、danger 档 |
| H 文案 | 3 | 二义失败提示、未能确定提示、免责句 |

**分诊（三处真缺口 + 一类变异自身问题）：**

1. **A8 判据缺口（已补）**：`RESET_STATE_DETAIL_REQUIRED_KEYS` 的内容**从未被逐字断言**。
   而「缺任一个都抛错」那条用例遍历的**就是这个常量本身** ⇒
   把清单缩短一个键，循环永远不会去试那个键，校验随之变弱而**全绿**。
   ⇒ 已补 `toEqual([...])`。★ **遍历一个常量的循环，必须另有一条断言钉住那个常量的内容。**
2. **G9 判据缺口（已补）**：没有任何断言钉住确认框的 `danger` 标志
   （`AppConfirm.vue:50` 用 `danger ? 'btn--danger' : 'btn--primary'`）
   ⇒ 不可撤销的审计写入可以静默降级成普通档。已补。
3. **G8 / A6 / A7 是 expect 串指错**（变异确实转红，只是命中了相邻用例名）⇒ 已改指。
4. **H1/H2/H3 打在没人读的语种上**：第一版变异改的是 `zh-CN.ts`，而用例断言的是**英文**渲染文本
   ⇒ 三条全绿，但这既不是等价变异也不是判据无牙，是**变异本体选错了文件**。
   已切到 `en-US.ts`，三条立刻有牙。
   ⇒ 判读「仍全绿」时，**先问这个变异改的东西有没有人读**。
5. **C3 是真等价变异**：`probe_triggered === true` → `!== false`。
   实跑对照表证明两版只在值不是布尔量时不等价，而 `unwrapResetState` 的
   `typeof ... !== 'boolean'` 把这批值全部拒掉（该守卫由 **A5 证明有牙**）
   ⇒ 在 API 层可达域内**恒等**。

### 顺带修掉的自身问题：i18n 门 rc=1

门报 `nodes.confirmResetStateBody`「两侧一致地缺」。真因不是漏写，是我把长文案写成了
**跨行值**（`key:` 换行再写字符串），而门按单行 `key: 'value'` 解析 ⇒ 判成缺失。
邻居那些长文案（如 `confirmRecoverBody`）全是单行。⇒ 已改回单行，门 rc=0。
⇒ ★ i18n 门的 rc=1 要先分清是**「键真的缺」**还是**「值的写法没被解析到」**，
  两者的修法完全不同；本次是后者。

### 产物

- API：`src/api/credentialsOps.ts` —— `ResetStateResult` 按后端逐字段重写；
  新增 `RESET_STATE_REQUIRED_KEYS`(6) / `RESET_STATE_DETAIL_REQUIRED_KEYS`(5) /
  `RESET_STATE_DETAIL_OPTIONAL_KEYS`(3) / `unwrapResetState` /
  `resetStateOutcomeAmbiguous` / `resetStateProbeIndeterminate` /
  `resetStateProbeOnlySubmitted` / `resetStateActorLooksLikeIp` /
  `resetStateIsWholeCredential` / `resetStateReasonMissing`
- 视图：`src/views/NodesView.vue` —— 新增 `resetState` 动作（superAdmin 档、危险档）、
  reason 审计输入、`trigger_probe` 勾选位（R1 ≥48px 命中区）、三档探测提示、二义失败提示；
  `effectiveReason` 的默认理由由三元链改成**全函数映射**
  （三元链在新增动作时会静默落到别的动作的理由上，审计因此记错）
- 用例：API 34 条 + 视图 19 条；全量 2820 条（114 文件）
- 门禁：build / 三门 / `vue-tsc` 全 rc=0；十连跑 10/10

### 仍未上移

`POST /api/credentials/{id}/test`、`POST /api/credentials/test-batch`、
`POST /api/credentials/{id}/models/{model}/test` 三条快速探测
—— superAdmin 档且**真的会触发一次探测**（有外部副作用）⇒ 本仓继续不碰。
`submitBatchProbe` 的**契约层与上限截断**已补测，但**不接 UI 入口**。

---

## 11.87 第五十一批：★ 重建路由差集基线（推翻前几轮结论）+ 路由阻塞诊断

### ★★★★★★★ 本批第一件事：发现前几轮的「只读面已扫空」建立在**坏的量具**上

上一轮结尾写下的基线是「后端注册 472 条 / 移动端已覆盖 449 条 / 未覆盖 23 条」，
并据此宣布「全仓路由差集里只读且未上移的部分已经扫空」。

本轮按惯例重建清单时，**读数与该基线矛盾了一个数量级**（已覆盖 113 vs 449）。
按纪律先怀疑量具、不急着采信「又缺了 300 个」，逐层查下去，**是量具坏了**：

| 层 | 缺陷 | 后果 |
|---|---|---|
| 1 | 归一函数没剥 Go 1.22 的**方法前缀** | `"POST /api/x"` 永远匹配不上前端路径 |
| 2 | 后端正则只认 `mux.HandleFunc` | 漏掉 `h.mux.` / `adminMux.` / `r.` 等全部接收者；且只扫了 3 个顶层目录，漏掉 `api/`、`taskprofile/` |
| 3 | 前端路径正则被 **`${`** 截断 | 模板字面量产出 `/api/admin/maas/orders${s` 这类垃圾条目 |
| 4 | 分母用错 | 拿「后端注册数」当分母 —— 很多端点**桌面 web 也没用**，根本不是「功能」 |

修好后用**地面实况**校准（6 条逐条 grep 验证）：`audit-logs` / `cache-metrics` /
`backups` / `auto-route/index` 在移动端**零引用**，而 `monitor-summary` 有 2 处 ——
读数与事实吻合。

**修正后的对照：**

| 口径 | 数量 |
|---|---|
| 后端注册端点 | 450 |
| **桌面 web 实际调用**（真正的「功能面」） | 394 |
| 移动端实际调用 | 136 |
| **桌面有、移动端没有** | **258**（其中 GET 只读 **145**） |

⇒ **「只读面已扫空」是错的。** 只读缺口还有 145 条，工作远未收口。

★★★ 可迁移的教训：
1. **读数与已知基线矛盾时，先查量具。**「又缺了 300 个」和「基线错了」，
   前者会让人重新扫一遍，后者才是真相。
2. **分母要用「谁真的在用」**，不是「注册了多少」。注册了没人调用的端点不是功能面。
3. **提取器要对样例逐条人工验证**再采信它的汇总数。
4. ★ **别把上一轮自己的结论当既成事实**。本批第一件事就是重测基线，
   而它确实是错的。

### 后端实读：`admin/diagnostics_routing.go`（`h.superAdmin`，handler.go:1464）

`GET /api/admin/diagnostics/routing-blocked?provider_id=X`
后端注释原话：*"credentials look healthy but routing can't find them"*
—— 判据来自视图 `v_routable_credential_models`，**不是**从 `credentials` 表反推。
这正是「凭据节点检查」与「路由检查」两条诉求的交汇点。

| 位置 | 事实 |
|---|---|
| `:61-64` | 405 `method not allowed` |
| `:65-70` | 400 `missing or invalid provider_id query parameter` —— 缺失 / Atoi 失败 / ≤0 **三种同一个文案** |
| `:72` | 10s 超时 |
| `:90` | `maxBindings = 500`，但 SQL 取 **501** 行用于判定截断 |
| `:144-147` | ★★★ `truncated = total > 500` ⇒ **`total` 被钳到 500** |
| `:140-142` | ★ `rows.Err()` 有查（否则静默返回截断列表，方向完全错） |
| `:119-122` | ★★ `warnRowSkip`：**Scan 失败就跳行**，只写服务端日志 ⇒ 客户端**完全看不到**少了几条 |
| `:130-133` | ★★ 原因取 `UnavailableReason`，**只在非 nil 时**才换成 `"unknown"` ⇒ 空串会原样落进 breakdown |
| `:27` | `UnavailableReason *string` + `omitempty` ⇒ NULL 时**整个键消失** |
| `:168` | ★ `credential_label` 是**拼接值** `name \|\| ':' \|\| COALESCE(provider_name,'unknown')` |
| `:173-196` | ★★★★★ 凭据状态查询**失败也不报错**，五个字段全落回零值 |
| `:238-247` | ★★★ `BindingsBlocked = total - routable`，用的是**钳后**的 total |

#### ★★★★★★★ 缺陷：钳位只钳了一半，计数可以自相矛盾到 `blocked` 为负

`total` 被钳到 500，但 `routable` 是**遍历 LIMIT 501 命中的全部行**累加的、**没钳**，
而 `blocked = 钳后total - routable`。当 501 行里几乎全可路由时：

```
bindings_total=500  bindings_routable=501  bindings_blocked=-1
```

★ **子集大于全集，且「被阻塞数」为负。** 截断时逐凭据 `bindings_total` 之和
（未钳，=501）也必然与顶层（500）对不上。

⚠️ 踩过的坑：我第一版把判据写成 `total !== routable + blocked`。
**这个等式恒成立**（`blocked` 就是用钳后的 `total` 减出来的），检测不出任何异常 ——
第一版 22 条里它一条红都没报。真正的判据是**子集 > 全集** `routable > total`，
或直接看 `blocked < 0`。
⇒ ★ **写「三个数对不上」的判据前，先问这个等式是不是被构造出来恒真的。**

#### ★★★★★★ 缺陷：凭据状态整段缺失时 `manual_disabled: false` 是**危险错值**

`:173-196` 的 best-effort 设计：状态查询失败只 `slog.Warn`，响应照发。
后果是 `status` / `availability_state` / `health_status` / `lifecycle_status`
全为 `""`，且 ★★ `manual_disabled` 变成 **`false`** ——
**一个真被手动停用的凭据会显示成「未停用」**。
`credential_label` 至少有回退路径（`:214-217` 用 binding 侧标签），
但**五个状态字段没有任何回退，也没有任何「未知」标记**。

代码注释自己写明了「否则『凭据状态全空』会被误读成『所有凭据都没有状态』」——
**但它并没有做任何标记让客户端能识别这件事**。
⇒ 移动端因此加了两道判据：`routingBlockedStateUnavailable()`（整段）与
`routingBlockedManualDisabledUnreliable(c)`（逐条），
UI 显示「状态未知」而不是「正常 / 未停用」。

#### ★★ `unavailable_reason` 的两种「拿不到原因」语义不同

| 形态 | 含义 | 后端行为 |
|---|---|---|
| **键不存在** | SQL 里是 NULL | breakdown 记为 `"unknown"` |
| `"unavailable_reason": ""` | 后端确实存了空串 | breakdown 记为 **`""`（空字符串键）** |

`:130-133` 判的是 `!= nil` 而不是 `!= ""` ⇒ **breakdown 里可能存在一个空字符串键**。
UI 必须把两者画成不同的文案，合并就丢掉了「原因字段本身是空的」这条线索。

### 移动端实现

- API：`src/api/routingBlocked.ts` —— 三层结构**各自**校验必填键（7 / 11 / 4），
  **不抽通用解包器**（这个端点的层级各有清单，混进通用函数会丢层）；
  `unavailable_reason` 与 `truncated` 因 `omitempty` 不进必填清单
- 视图：`src/views/NodesView.vue` 详情内新增诊断区。★ **按需加载**，
  不随详情自动拉 —— 最坏 500 条绑定，而详情是随手点开的
- 权限：`h.superAdmin` ⇒ 入口按角色分档，tenant_admin 看不到

### 变异：36 条 —— 32 有牙 + 3 真等价 + 1 变异本体写错（已修）

| 组 | 条数 | 覆盖 |
|---|---|---|
| A 三层解包 | 9 | 顶层 7 键 / 凭据 11 键 / 绑定 4 键、类型校验、三份必填清单 |
| B 钳位判据 | 6 | 子集>全集、恒真等式、blocked<0、truncated、求和不一致、逐凭据矛盾 |
| C 状态与原因 | 8 | 整段状态缺失、逐条不可信、空 credentials、原因两义、breakdown 空键 |
| D 守卫与路径 | 2 | provider_id 守卫、URL 缺 query |
| E 视图 | 9 | 角色分档、按需加载、截断/矛盾/状态/求和提示、原因两渲染、provider_id 取值 |
| F 文案 | 2 | 状态不可信提示、空串原因文案 |

**首轮 29/36，分诊后收口到 32 有牙。三类问题分开归因：**

1. **判据缺口 2 处（已补）**
   - **A5**：此前只钉了「缺键」，**整段顶层类型校验可以被整段删掉而全绿**
     （`block_reason_breakdown` 传数组、`bindings_total` 传字符串都不报）。
     ⇒ 已补「键在但类型错」七条断言。★ **缺键与类型错是两种不同的漂移，两条都要钉。**
   - **C7**：`routingBlockedReasonEmpty` 原有样本用
     `is_routable: true, unavailable_reason: undefined` ⇒ `=== ''` 本来就是 false，
     于是「判据不看 `is_routable`」这种变异照样全绿。
     ⇒ 已补「可路由 **且** 原因为空串」的样本。
     ★ **样本选得「恰好不触发」时，判据等于没测。**

2. **夹具巧合 1 处（已补负控）**
   - **E8**：视图把 `loadRoutingBlocked(pid)` 硬编码成 `loadRoutingBlocked(3)`
     时**全绿** —— 因为夹具的 `provider_id` 恰好也是 3（`WITH_COUNTS`）。
     ⇒ 已加负控：换成 `provider_id: 7` 的卡片，并显式断言**没有**用 3
     （3 是全仓最常见的默认供应商 id，硬编码它会让绝大多数用例照常通过）。
     ★ **变异选的值必须与夹具无关，否则测的是巧合。**

3. **变异本体写错 1 处（已修）**
   - **E1**：把 `v-if="isSuperAdmin && !routingBlockedOpen"` 改成
     `v-if="!routingBlockedOpen" /* MUT-E1 */` —— 标记放在**标签属性区**，
     Vue 编译报 `SyntaxError: Illegal '/' in tags` ⇒ **收集失败**，
     不是判据问题。⇒ 改放进属性值的 JS 表达式注释里（Vue 3 表达式支持注释）。
     ★ rc≠0 且抓不到具名用例名时，**先查是不是收集失败**，别急着判判据无牙。

4. **真等价变异 3 条（用穷举证明，不是推理）**
   | 变异 | 等价性证明 |
   |---|---|
   | **B3** 只保留 `routable > total` | 穷举 56 组真实可达输入（`blocked` 由后端算成 `total - routable`）**零不一致**：`blocked < 0 ⇔ routable > total`。构造的反例组合后端算不出来 ⇒ 等价**仅限可达域**。 |
   | **C4** 去掉 `manual_disabled === false` | 差异只出现在「状态全空 **且** `manual_disabled: true`」，而状态查询失败时五个字段**全部取零值**（`:173-196`）⇒ 该组合后端不可达。（且 C4 的更宽松版本其实更稳健。） |
   | **E2** `await load...` 改 `void load...` | 该函数 `await` 之后无语句、不返回值、不被别处 await ⇒ 改 fire-and-forget 不改变任何可观测行为。 |

★ 这三条的共性：**等价性只在「后端真实可达域」内成立**，域外两者确有差异。
  所以「等价」这个结论必须附上域，否则就等于没验证。

### B2：一条用来证明「恒真等式检测不出异常」的变异

把判据改回 `total !== routable + blocked` 那个**恒成立**的等式 —— 它有牙，
因为新增的那条判据直接断言「这个等式在矛盾数据上**依然成立**，而矛盾判据为真」。
⇒ 这条变异的作用不是证明原判据对，而是**把「我第一版写错了」这件事钉成可执行事实**。

---

## 11.88 第五十二批：路由决策面三条只读端点（overview / decisions / audit）

### 差集清单必须**每轮重算**

上一轮做完 routing-blocked 后，我直接复用了那份「未覆盖」清单来选题 ——
清单里 `/api/admin/diagnostics/routing-blocked` 还在，因为它是**实现前**生成的。
⇒ 刚做完的东西还在待办里，是差集清单不新鲜最直观的症状。
重新提取后：桌面 394 / 移动端 **137**（+1）/ 缺口 257（GET 只读 144）。
**差集清单是易腐的产物，不是可以跨轮复用的结论。**

### 三条端点：同一族，三种形状

| 端点 | 权限档（实读） | 形状 | handler |
|---|---|---|---|
| `GET /api/routing/overview` | `admin`（handler.go:930） | 信封 `{featured, rows}` | routing.go:2075 |
| `GET /api/routing/decisions` | `admin`（:1204） | 信封 `{total, offset, limit, decisions}` | routing.go:3215 |
| `GET /api/routing/audit` | **`h.superAdmin`**（:1206） | **裸数组** `[]` | routing.go:3443 |

⚠️ 同一族里 admin 档与 superAdmin 档混排，**不能按路径前缀判权限**。
⚠️ `audit` 是裸数组而 `decisions` 是信封 —— 同一族两种形状，
   所以本批**没有**抽通用解包器，各端点独立解包 + 各自的必填键清单。

### ★★★★★★★★ overview 的两个**编造默认值**

`routing.go:2138-2141` 的 SQL：

```sql
COALESCE(mo.success_rate, 0.9)::float8  AS success_rate,
COALESCE(mo.p95_latency_ms, 9999)::int   AS p95_latency_ms
```

⇒ 一个**从没探测过**的凭据，会以「成功率 90%、p95 延迟 9999ms」出现在表里。
后端**不给任何「这是默认值」的标记** ⇒ 客户端无法把它们与真值区分
（真值恰好是 0.9/9999 时更无从分辨）。

⇒ 移动端只能靠形状给出**免责**（`overviewMetricsMayBePlaceholder()`），
  并在 UI 上明说「可能是默认值（未探测过）」，**不许当成实测指标展示**。

另两处同类：`tier` 兜 2、`weight` 兜 100。

### ★★★ overview 的其它事实

- ★★ `routable` 与 `runtime_routable` 是**同一个变量**赋给两个键
  （`:2199-2200`）⇒ 永远不可能不相等；不等即**契约漂移信号**（已加判据）。
- ★★ `featured` 查询的 error 被**丢弃**（`_ = h.db.QueryRow(...)`，`:2084`）
  ⇒ DB 出错时就是空数组、无任何信号；而 `featured_only=true` 且 featured 为空时
  **过滤条件整个不下发**（`:2133` 的 `if featuredOnly && len(featured) > 0`）
  ⇒ 「只看精选」静默退化成「看全部」。已显式提示。
- ★ `runtime_block_reason` **条件存在**（只在不可路由时写入）
  ⇒ 不能拿它当「可路由」的判据。
- ★ `warnRowSkip`（`:119-122`）：Scan 失败跳行，只写服务端日志 ⇒ 客户端看不到少了几条。
- `routingBlockReason` 的 `"unknown"` 分支**不可达**（只在 `!runtimeRoutable` 时调用，
  而那六个条件至少有一个必假）—— 一条死分支。
- `if outRows == nil`（`:2212`）同理不可达：`make([]map[string]any, 0)` 永不为 nil。
- tenant 在 SQL 里硬编码 `'default'`。

### ★★★★★★★★ audit 查询失败回的是 **200 + 空数组**

```go
rows, err := h.db.Query(ctx, `...`)
if err != nil { writeJSON(w, http.StatusOK, []any{}); return }   // routing.go:3460-3463
```

⇒ 客户端拿到的「没有审计记录」与「数据库查询失败」**完全一致**。
对一个审计端点，这意味着故障期间运维会看到「最近没人动过配置」——**方向完全错**。
已加 `routingAuditEmptyIsUnreliable(rows, status)` 显式标记这一档。

### ★★ decisions 的两处「静默失真」

- `total` 的计数查询 error 被吞成 0（`:3259-3262`）⇒ **有数据但 total=0**。
  已加 `routingDecisionsTotalUnreliable()`。
- `limit > 500` 是 **clamp 到 500**（`:3222-3224`），不是回落
  —— 与 free-discovery 那条「超上界回落 50」的写法不同，**不能跨端点类推**。

### ★★ jsonb 损坏与 NULL 不可区分

`:3480-3481` 调 `jsoncol.Decode(...)` 但**忽略返回值**
⇒ 损坏的 `before_json` / `after_json` 与真的 SQL NULL **都**渲染成 `null`。
（`internal/jsoncol` 的文档明确说这个包就是为区分二者而写的，返回值即那个区分；
调用方丢掉返回值，问题就回到了原点。）

### 两个防撞/防呆

1. ★★ **命名防撞**：仓里已有 `@/api/routingAudit` 导出同名的 `fetchRoutingAudit`，
   但它打的是**另一个端点** `/api/admin/routing/overrides/audit`。
   本批的改叫 `fetchRoutingAuditLog`。
   ⇒ **同名不同端点时给其中一个加限定词**，别让调用方靠记忆区分。
2. ★ `!(k in obj)` 的括号：`!(k) in obj` 会被解析成 `(!(k)) in obj`，
   返回值类型变成 boolean、整段「键存在性」判据**静默失效**。
   本批第一版就踩了，`vue-tsc` 报了 `TS2322` 才暴露出来。
   ⇒ **类型门不只是「能不能编译」，也是表达式解析歧义的探测器。**

### 视图

`/api/routing/overview` 接进已有的 `/routing` 页（`RoutingCheckView.vue`），
与 explain 并存且**按需加载** —— overview 是全网 `model_offers × credentials` 的笛卡尔积，
自动拉会让每次进页面都付这个代价。
被阻塞的组合按 `runtime_routable` 分栏列出，并带上编造默认值的免责。

### 变异：29 条 —— **29 有牙，零可疑**

| 组 | 条数 | 覆盖 |
|---|---|---|
| A overview 解包 | 5 | 信封 2 键、行内 31 键、数组类型、清单增删（含把条件键算成必填） |
| B 编造默认值 / 可路由性 | 8 | 默认值判据、可路由键、阻塞原因、契约漂移、featured 失效 |
| C decisions | 4 | 信封 4 键、类型校验、还有下一页、total 不可信 |
| D audit 裸数组 | 5 | 裸数组 vs 信封、行内 8 键、空列表不可信、actor 空串 |
| E 视图 | 5 | 按需加载、featured 提示、默认值免责、漂移提示、阻塞栏条目 |
| F 文案 | 2 | 免责句、featured 失效提示 |

**首轮 21/29，分诊后 29/29。四类问题：**

1. **判据缺口 3 处（已补）—— 都是「样本恰好不触发」**
   - **B2**：`overviewMetricsMayBePlaceholder` 原有样本只覆盖「两个都命中」与「两个都不命中」
     ⇒ 「判据只看其中一个」照样全绿。已补「只命中一半」的两条。
   - **B3**：所有样本都让 `routable` 与 `runtime_routable` **同值**
     ⇒ 「判据改看另一个键」测的是巧合。已补两键漂移的正反两个样本。
   - **B4**：可路由的行**本来就没有**原因键，所以「判据不先看可路由性」永远测不出来。
     已补「可路由的行**带了**原因键」这一漂移形态。
   ★ **三条同源：样本里被测的那个差异被消掉了。变异比断言更容易发现这一点。**

2. **变异本体没改到行为 1 处（已修）**
   - **E1** 第一版把自动加载插进 `loadOverview()` —— 但那个函数**只有按钮会调**，
     于是「变异注入成功、行为没变、全绿」。已改为在 `onMounted` 上注入，
     并且**由变异自己补上 import**：生产代码不该为了「让变异能跑」而留一个没用到的导入。

3. **变异本体写错 1 处（已修）**
   - **E2/E3/E4** 把标记放在 `<p v-if="..." class=...>` 的**标签属性区**，
     Vue 编译报 `SyntaxError: Illegal '/' in tags` ⇒ **收集失败**而非判据失效。
     已改放进属性**值**的 JS 表达式注释。
     ★ 这已是**连续两批**踩同一个坑（上一批的 E1 同因）⇒ 写成固定规矩：
     **模板里的标记一律放进属性值，不要放进标签区。**

4. **夹具巧合 1 处（已修）**
   - **E5** 第一版让阻塞栏过滤 `credential_id !== 99` —— 而夹具的阻塞行是 10，
     过滤后结果不变 ⇒ 变异是空转。已改成「阻塞栏返回全部行」，
     并**补上条目数断言**（夹具 2 行 ⇒ 阻塞栏必须恰好 1 条），
     这样判据不再依赖任何具体 id。
     ★ 只断言「阻塞原因出现在页面上」会被这种变异蒙混过去 —— 因为可路由那行里
     也写着同一个值。**「某段文本出现了」不等于「它在正确的分段里」。**

### ⚠️ 本轮自己踩的一条操作纪律

变异套件在跑的同时我并行跑了全量测试，`routingRead.test.ts` 报了一条红。
**归因：变异脚本此刻正把 B7 注入源文件**，全量撞进了注入窗口。
⇒ 与「投测试前先扫一眼别的重测试进程」同族：**变异脚本会改源文件，
  跑全量/十连跑之前必须确认它已结束**，否则会把环境干扰读成回归。
（确认变异收口后重跑：2883 条全绿 rc=0。）

---

## 11.89 第五十三批：模型路由树 / 可用模型原始名单 / 熔断健康

三条 admin 档只读端点：`model-tree`（handler.go:1199）、`available-models/raw`（:1203）、`health`（:1205）。

### ★★★★★★★★ 同一端点，按调用者角色返回**两种形状**

`admin/routing.go:2261`：`hideCredentialDetails := IsTenantAdmin(r)`

| | super_admin | tenant_admin |
|---|---|---|
| 顶层键 | `featured, series, unmapped` | `featured, series, unmapped, **`readonly: true`**` |
| variant 级 | 只有 `credentials[]`（逐凭据详情） | `available` + `credential_count`，**无 credentials** |
| `available` 含义 | `credentials[].available` = **该单个凭据** | `variants[].available` = **全部**凭据都可用（`:2482` 遇任一 false 即 break） |

★★ `readonly: true` 是**两侧唯一的区分标记**。
★★★ `available` 这个名字在两侧**层级不同、语义也不同** ——
一个是单凭据的可用性，一个是全称判断。把它当同一个指标读会得到**相反**的结论。

移动端因此把两种形状建成**两套类型**、给一个显式分流判据
（`modelTreeIsRedacted` / `isSimpleVariant`），并让
`modelTreeVariantAvailable()` 在完整形状下**返回 `null` 而不是猜一个**。
⇒ 「拿不到答案」就明说拿不到，别返回一个看着像答案的值。

### ★★★★★★ `availability_state` 的 NULL 被写成 **`"ready"`**

SQL（`:2276`）：`COALESCE(c.availability_state, 'ready')`
相邻的 `credential_status` 兜的是 `'unknown'`（`:2275`）——
**两个兜底值不一致，恰恰说明 `'ready'` 是失误而非设计**。

后果比 §11.87 那两个编造默认值（`0.9` / `9999`）更严重：
那两个编造的是**展示指标**，这个直接改变「这条能不能路由」的判断
⇒ 一个状态未知的凭据在树里显示为「就绪」。
已加 `modelTreeAvailabilityFabricated()`（判据要求 `status === 'unknown'` 才算命中，
因为单独看 `availability_state === 'ready'` 不足以判定）。

### ★★★ `available-models/raw` 零行返回 **`null`** 而不是 `[]`

```go
var names []string          // routing.go:3203 —— nil 切片
for rows.Next() { names = append(names, name) }
writeJSON(w, http.StatusOK, names)
```

`json.Marshal` 把 **nil slice** 编码成 `null`；只有 `make([]string, 0)` 才是 `[]`。
⇒ 「一个可用模型都没有」与「契约漂移」在客户端必须分开处理。

★ 同族对照：§11.88 的 `audit` 走的是 `make([]map[string]any, 0)` ⇒ 返 `[]`。
**同一族两种表示**，不能跨端点类推。
⇒ 已让 `unwrapAvailableModelsRaw` 显式接受 `null` 并归一成 `[]`，
  但**非数组且非 null 仍然抛错**（不静默返回空）。

### ★★ `featured_only` 在 model-tree 里同样会**整个失效**

过滤写在 Go 里（`:2387` `if featuredOnly && len(featuredModels) > 0`），
按 `rawName` 或 `canonicalName` 匹配 ⇒ featured 为空时过滤条件整个不下发，
返回全量且无提示。与 §11.88 的 `overview` 是同一个坑的两个实例。

### ★★ `routing/health` 的 summary 三个数是**客户端可复算**的

`total = len(credentials)`、`open = circuit_state=='open'` 的条数、`closed = total - open`
（`:3415-3440`）⇒ `routingHealthSummaryDisagrees()` 现场复算一遍，
对不上即契约漂移或中间层加工过。

另外加了 `routingHealthCoolingDesynced()`：`cooling_until` 非空但
`circuit_state` 不是 `open` ⇒ 冷却窗口与熔断状态不同步。

### 类型上的一个坑

`modelTreeVariants()` 要在**两种结构不同的 series 联合类型**上做 `flatMap`，
TS 推不出共同元素类型（两个重载都不匹配）⇒ 那里有一处**显式 cast**，
并在注释里写明「安全的前提是调用方按 `isSimpleVariant` 分流」。
⇒ **类型门不只是「能不能编译」，也是联合类型设计的探测器**：
它逼着人把「两种形状」这件事在类型层面说清楚，而不是用 `any` 混过去。

### 变异：26 条 —— **26 有牙，零可疑**

| 组 | 条数 | 覆盖 |
|---|---|---|
| A model-tree 形状 | 9 | 信封 3 键、三处数组类型、`readonly` 分流（恒 false / 只看真值 / 看 credentials 在不在）、全称 fold（漏 single / 空列表）、变体跨层展开、两个伪造判据、featured 失效 |
| B available-models-raw | 4 | null 放行、非数组抛错、重复检出、非字符串元素滤除 |
| C routing/health | 8 | 信封、summary 类型、凭据 9 键、summary 可复算（恒 false / 恒 true / 只看 total）、冷却不同步两支 |

**首轮 21/26，分诊出的 5 条里 3 条是同一个病根：「样本恰好不触发」。**

| 变异 | 为什么测不出来 | 补法 |
|---|---|---|
| **A4** 判据只看真值（`!!`） | 后端只发 `readonly: true` 或**键不存在** —— 这两种输入下 `=== true` 与 `!!` **恒等** | 加一条真值非布尔（字符串 `"true"`）的契约漂移样本 |
| **A9** 只取第一层 generation | 夹具里每个 series **只有一个** generation | 夹具加第二个 generation，并断言变体总数与 `gpt-35-turbo` 在列 |
| **B4** 原样返回不过滤 | 夹具里**全是字符串** | 加一条混入 `[1, null, {}]` 的样本 |

★★★ 这已经是**连续第三批**同一病根（§11.87 的「样本只命中一半」「两键同值」「可路由行本就没原因键」，
§11.88 的三条同源缺口，本批又三条）。
⇒ **规律**：变异比断言更容易发现「样本选歪」。断言断言的是**一个具体样本**的行为，
变异扰动的是**规则**；样本恰好没落在规则的敏感区，规则改了也测不出来。
⇒ 已定做法：**写判据时主动构造「两个实现只在边缘输入上分叉」的样本**，
   而不是只写一个「正常样本 + 一个明显错的样本」。

另 2 条（A5/A6）是 expect 串指错 —— 变异确实转红，只是命中了相邻用例名。

### 类型门也是「联合类型设计」的探测器

`modelTreeVariants()` 要在两种**结构不同**的 series 上做 `flatMap`，
TS 推不出共同元素类型（两个重载都不匹配），逼出一处显式 cast。
那处 cast 不是偷懒 —— 它迫使「两种形状」这件事在类型层面被说清楚，
而不是用 `any` 糊过去。

---

## 11.90 第五十四批：模型路由树 / 熔断健康 / 可用模型名单接入 `/routing` 页

§11.89 把三条端点的**契约层**做完了，本批接上 UI。三段都**按需加载**——
树是全网 `model_offers × credentials` 的笛卡尔积，健康是全量凭据，名单是全网模型名；
自动拉会让每次进 `/routing` 都付这三份代价，而这一页的主动作是 explain（有个输入框）。

### ★★★★★★★ 两种形状在 UI 上必须**分流渲染**

后端同一个 URL 按角色返回两种结构（`routing.go:2261`）。UI 上的坑不是「字段读不到」，
而是**同名不同义**：

| | super_admin（完整树） | tenant_admin（裁剪树，`readonly: true`） |
|---|---|---|
| variant 级 | `credentials[]` 明细 | `available` + `credential_count` |
| 「全部可用」怎么来 | 客户端**自己 fold** `credentials` | **后端已经算好**在 `available` 上 |
| 明细区 | 逐凭据渲染 | **不渲染**，只显示「N 个凭据（明细不可见）」 |

⇒ 不分流的两种典型坏结果：
1. 裁剪树去渲染 `credentials` ⇒ 那一整段空掉，用户以为「这个模型没有凭据」；
2. 完整树去读 variant 级的 `available` ⇒ 恒 `undefined`，被 `!== null` 判成「有值」
   而显示成**恰好相反**的结论。

第三种坏结果最隐蔽：`readonly` 形状的变体**万一带了** `credentials`（契约漂移），
仍然必须按形状判据走、不逐条渲染。已为此单独立了一条用例 ——
**形状判据优先于字段是否恰好存在**。

### ★★★★ 两条「编造值」的免责在 UI 上是硬要求

- `availability_state` 的 NULL 被写成 `"ready"` ⇒ UI 必须显示
  「状态未知（服务端把空值填成了就绪）」，不能显示「就绪」。
- `success_rate` / `p95` 的 COALESCE 兜底 ⇒ 显示时必须带免责句。

这两条在 UI 上都是 `v-if` 级别的开关，任何一条被去掉都不会让页面崩，
只会让用户**相信一个假数字** —— 所以它们必须各有独立的变异。

### ★★★ 「故障」与「真结论」在 UI 上也要分开

`available-models/raw` 拉取失败时，视图**保持不显示**，
**不**渲染成「当前没有任何可用模型」——
端点挂了不等于没有可用模型。这与「零行显示空态」是两条独立判据：
零行 ⇒ 显示「没有可用模型」；失败 ⇒ 什么都不显示。

### 本轮变异：18 条

首轮 **3/18**，分诊出的不是等价变异，而是两类系统性问题：

1. **expect 串指错（10 条）**：我把 `expect` 指向了**英文渲染文本**
   （如 `'status unknown'`），而脚本的具名匹配读的是**中文用例名**。
   ⇒ 变异其实已经转红，只是没匹配上。
   ★ 写变异脚本时 `expect` 必须对准**用例名**，不是断言里检查的字符串。
2. **判据缺口（8 条，全部是「样本恰好不触发」）**：
   | 变异 | 为什么测不出来 | 补法 |
   |---|---|---|
   | A3 凭据列表不按形状分流 | 裁剪形状的夹具本来就没有 `credentials` | 加「裁剪形状却带了 credentials」的漂移形态 |
   | A5/A7/A8 两处全称判断 | 完整形状夹具只有一个**可用**凭据 | 加第二个**不可用**凭据，并断言显示的是「部分不可用」且不出现「全部可用」 |
   | B4 空树空态 | 没有专门断言空态 | 加空树用例 |
   | D2 熔断列表不过滤 | 夹具里只有一条**熔断中**的凭据 | 加一条 `closed` 的，断言它**不出现** |
   | D3 健康空态 | 没断言 | 加「无熔断」用例 |
   | D5 失败被当成零 | 只测了成功路径 | 加失败路径用例 |
   
★★★ **这是同一病根的第四批**（§11.87 三条、§11.88 三条、§11.53 三条、本批八条）。
   连续四批的共同形态：**夹具里缺一个「反向」或「漂移」的样本**。
   ⇒ 已把它写成固定检查项：每写一个判据，问「有没有一个样本能让**两种实现分叉**，
     而不只是「看起来正常」或「明显错误」」。

## 11.91 自动路由读面六条端点（第五十五批，API 层）

### 基线刷新

差集清单是易腐产物，每轮必须重算（第五十二批复用旧清单，导致已完成的
`routing-blocked` 还挂在待办里）。本批重算：

| | 第五十二批 | 本批 |
|---|---|---|
| 移动端实际调用 | 137 | **144** |
| 桌面有、移动端无 | 257 | **252** |
| 其中只读 GET | 144 | **139** |

第五十三批新增的三条（`model-tree` / `available-models/raw` / `health`）已计入。
量具自检：那三条已不在缺口清单里，地面实况 `api/routingTree.ts:122/217/263` 对得上。

### ★★★★★★★ 权限档位是 superAdmin，**形参名 `adminWrap` 是假名**

三个注册函数的形参都叫 `adminWrap`，看起来像 admin 档：

```go
// admin/auto_route.go:99
func (h *AutoRouteHandlers) RegisterAutoRouteRoutes(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc)
```

但实际绑定的值是：

- `admin/handler.go:1381` `autoH.RegisterAutoRouteRoutes(mux, h.superAdmin)`
- `admin/handler.go:1430` `analyticsH.RegisterAnalyticsRoutes(mux, h.superAdmin)`
- `admin/auto_route.go:116` `tuning.RegisterTuningRoutes(mux, adminWrap)` —— 转手，
  而 `adminWrap` 就是上一行那个形参，**值仍是 `h.superAdmin`**

`h.superAdmin` = `SuperAdminMiddleware`（`handler.go:886`）⇒ **tenant_admin 直接 403**。

⚠️ 本族六条端点全部是 superAdmin 档。挂抽屉席时必须 `requiresRole: 'super_admin'`，
否则 tenant_admin 用户点进去只看到 403。这是「不能按名字判权限」的一个实例：
**名字骗人，要读到实际绑定的那个值。**

### ★★★★★★ 一条**完全死掉的垂直切片**——照抄清单就会给 404 做 UI

差集清单里列着 `/api/admin/auto-route/tuning/strategies`。按「清单即待办」的惯例
直接搬到移动端，就会给一个 404 做出一整套 UI。正面枚举全仓 21 条 auto-route 注册：

```
admin/auto_route.go:100-125   decisions / index / profile / audit / refresh /
                             cost/customer / cost/model / quality-correlations /
                             defaults / defaults/ / affinity / affinity/selections
admin/auto_route_tuning.go:83-89  tuning/proposals / proposals/generate /
                             proposals/ / tuning/accuracy / POST tuning/analyze
admin/analytics.go:55-59     analytics/matrix / flow / model-task-index / funnel /
                             analytics/decision/
admin/auto_route_correlations.go:332  correlations
```

**`tuning/strategies` 不在其中。** 全仓唯一的字面量出现在 handler 自己的注释上
（`auto_route_tuning.go:826`），而函数本体带 `//nolint:unused`（`:838`）
—— golangci-lint 的 `unused` 检查器只对**无引用**的函数标这个，
所以它是货真价实的死代码。

前端侧同样死：`web/src/api/tuning.ts:42` 的 `getTuningStrategies` 全仓**只有定义处**，
没有任何调用方（`web/src/api.ts:33` 的 `export *` 只是把名字再导出一次）。

⇒ 后端 handler 没注册、前端封装没人调，**两端都是死的**。当前没有线上影响
（没有 UI 会触发它），但这是差集扫描给出的一个**假缺口**。
**教训：清单里的每一项，在写代码之前都要先确认它真的注册了。**

### ★★★★★ 三条数组端点全是稀疏键

`index` / `cost/customer` / `cost/model` 逐行用 `*float64` / `*int` 指针扫，
再按 `if xxx != nil` 决定写不写这个键：

| 端点 | 无条件键 | 条件键 |
|---|---|---|
| `index` | **4 个**：`bucket` `credential_id` `raw_model` `updated_at` | 13 个 |
| `cost/customer` | **1 个**：`api_key_id` | 13 个 |
| `cost/model` | **1 个**：`raw_model` | 7 个 |

⇒ **「键缺失」= 该指标无数据，不是 0。** UI 把两者都渲染成 0% 会让运维
以为「这个模型成功率 0%」，实际是「这一列没数据」。

### ★★★★★ 空索引返回的是**异构哨兵**，不是 `[]`

`auto_route.go:294-297`：索引表为空时后端返回

```go
writeJSONOk(w, []map[string]interface{}{
    {"warning": "credential_model_index is empty; awaiting first bg worker refresh (…)"},
})
```

一个**只有 `warning`、没有 `credential_id`** 的元素，与正常行结构不同。
照「数组里每行都有 `credential_id`」去解包，网关启动 5 分钟内每次进这一页都会抛错。
⇒ 解包器必须放行它，并且单独提供 `autoRouteIndexAwaitingFirstRefresh()` 把它
与「真·空索引」区分开。

### ★★★★ `audit` 的三个块**查询失败时键直接缺失，HTTP 仍 200**

`task_distribution` / `profile_distribution` / `top_chosen_models`
三处赋值**全部包在 `if err == nil` 里**（`auto_route.go:627 / :665 / :724 / :758`）。

⇒ 「这一块没有数据」与「这一块的查询挂了」在响应里**长得一模一样**。
UI 若把「键缺失」渲染成空分布，等于把一次数据库故障报成「没有流量」。
`autoRouteAuditMissingBlocks()` 专门返回缺哪几块，供 UI 渲染成「查询失败」而非 0。

同一端点的 `outcome_source`（`auto_route_outcome_freshness.go:70-76`）是刻意加的
证据源：这一屏的成功率/奖励/路由数字由后台 settle worker 回填，不由被统计的请求测出来。
`stale === true` ⇒ **数字不再产生**，与「数字低」必须分开显示。

**可复算不变量**：`total_requests === total_auto_requests + specified_model_requests`
在基表与物化视图**两条路径上都成立**——两者的 WHERE 准入条件逐字相同
（`auto_route.go:559-563` vs migration `649:112`），且对布尔列 `= TRUE` 与
`IS NOT TRUE` 是互斥且穷尽的划分（NULL 归后者）。⇒ 可做客户端交叉校验。

### ★★★★ `analytics/decision` 的 `l1` 会被 blob **逐键覆盖**，且**不可判定**

`analytics.go:830-840` 的赋值顺序是：

1. `l1 := {task_type, profile}`（DB 列，经 `nullStringOrEmpty`）
2. `if confidence != nil { l1["confidence"] = … }`
3. `json.Unmarshal(auto_decision)` 后 **`for k, v := range parsed { l1[k] = v }`**

第 3 步在最后 ⇒ blob 里的同名键**覆盖**前两步。客户端不能假设 `l1.task_type`
来自数据库。

⚠️ 更麻烦的是**不可判定**：合并之后响应里已经分不清「这个键来自 DB 列」还是
「来自 blob」。所以**空结果也不能当免责**——blob 完全可能只带一个同名的
`task_type` 而不带任何新键，此时一切看起来都正常，值已经被顶掉了。
本批只提供可判定的那一半（`autoRouteDecisionL1SplatKeys` = l1 里出现三个
DB 键之外的任何键 ⇒ splat 跑过），并在注释里写明它**不构成免责**。

`l2` 是条件键，缺失有三种原因而响应里区分不了：后端根本没查
（`l2Lookup` 只在 id 能生成 dashed 变体时为真，`:854-859`）／查了但无决策日志／
L2 查询真出错（那条是 500，走错误分支）。⇒ UI 只能说「没有 L2 记录」。

### ★★★ `tuning/accuracy` 的五个 `avg_*` 全是 **COALESCE 编造的 0**

`auto_route_tuning.go:774-778` 五列全部 `COALESCE(…, 0)`。一行 `total > 0`
但源列全 NULL 时，`avg_success = 0` 与「真的 0% 成功率」**逐字节相同**。

同端点还有一处口径切换：后端**按窗口长度换物化视图**（`:757-763`），
`days <= 7` 走 5 分钟桶、8..90 走天桶 ⇒ 同一组 task_type 的 `avg_*`
在两个窗口下**不可直接比大小**。

★ 与同族的 `top` 处理**完全相反**：`days` 越界是 400 报错，
而 `index`/`cost` 的 `top` 越界是**静默回落**（且三端默认值/上限各不相同：
index 100/1000，cost 50/500）⇒ **不能跨端点类推**。

### 变异 44 条 → 44 有牙，零可疑

首轮 40/44。四条可疑，**分诊后是四种不同的原因**：

| id | 现象 | 真实归因 | 处置 |
|---|---|---|---|
| A9 | 仍全绿 | **判据无牙（样本缺失）**：index 的 top 用例只有 `0/-1/1001`，全是整数，去掉 `Number.isInteger` 测不到 | 补非整数样本 `0.5` / `10.5` / `NaN`（并顺手给同族的 cost top 也补上） |
| B2 | 转红未命中 | **expect 串指错**：真正被它打红的是「三个条件键全缺失」那条 | 改 expect |
| E8 | 疑似收集失败 | **变异写错**：只替换了括号里的表达式，留下 `return ( return false )` ⇒ 语法错误 | 连 `return (` 一起替换 |
| F9 | 转红未命中 | **expect 串指错**：真正被它打红的是「blob 带来新键」那条 | 改 expect |

★★★ A9 又是那个病根：**同族的两个判据，只有一个带了边缘样本**
（`accuracyDaysAccepted` 有 `7.5`，`indexTopAccepted` 没有）。
连续五批同一形态（§11.87/§11.88/§11.89/§11.90/本批）。

★ 另有一条**自造缺陷**在写测试时暴露：本批第一版把遮蔽判据写成
「`l1` 里有没有 `task_type`/`profile`」，而这两个是**恒在的必填键** ⇒
**恒真判据**，永远返回它俩、完全不区分。是类型门 + 用例自己把它顶出来的
（首轮 60 条里 2 条红）。改成可判定的 `autoRouteDecisionL1SplatKeys`，
并补了一条「真正的盲区样本」：blob 只带同名键、不带新键时谓词返回空
——**空结果不构成免责**，这一点写进了注释。

### 门禁

build / 三门 / vue-tsc 全 rc=0；`autoRoute.test.ts` 60 条；全量 2976 条（118 文件）；
十连跑 10/10。

文档 §11.91 纯追加。

## 11.92 自动路由读面接 UI（第五十六批）

### 落点

新页 `/auto-route`（`AutoRouteView.vue`），抽屉席，`requiresRole: 'super_admin'`，
与 `/proposals` 配对：**那一页答「系统认为规则该怎么调」，这一页答「现在跑得怎么样、
花了多少钱」** —— 建议与效果之间缺的就是这一页。

五段全部**按需加载**（index 是全网 credential × model 的笛卡尔积，cost/model 是全网
模型名，audit 要扫 7 天窗口 ⇒ 自动拉会让每次进这一页都付这三份代价）。

同时把第五十五批的 `api/autoRoute.ts` 改名 **`autoRouteRead.ts`**：仓里已有
`autoRouteInsights.ts`（funnel + proposals），两个 `autoRoute*.ts` 覆盖**零重叠**的
端点集却只差一个后缀，是个必然踩的坑。改名同时对齐既有 `routingRead.ts` 约定。

### ★ 五处「不能都渲染成同一个东西」

| # | 场景 | 错误渲染 | 本页渲染 |
|---|---|---|---|
| 1 | **稀疏键缺失**（每行只有 1~4 个键无条件） | `0.00` | `无数据` + `.ar__cell--nodata` |
| 2 | **audit 块缺键**（查询失败，HTTP 仍 200） | `没有数据` | `查询失败` + `.ar__block-failed` |
| 3 | **outcome_source.stale** | 照常显示成功率 | 挂免责句「数字是停止产生后的残留」 |
| 4 | **空索引哨兵** `[{warning}]` | `没有数据` | `等待后台首轮刷新` |
| 5 | **success_rate=0** | `0.00%` | `没有流量`（带独立 class 与斜体） |

外加：模型成本的「每请求成本」在**分母缺失或为 0** 时显示 `无数据` ——
否则会算出 Infinity/NaN，渲染成**空白而不是错误**，用户只会以为这一列没数据。

### 变异 28 条 → 28 有牙，零可疑

首轮 23/28。五条可疑是**五种不同原因**：

| id | 现象 | 真实归因 | 处置 |
|---|---|---|---|
| A1 | 转红未命中，**具名红 0 条** | **TDZ**：注入点排在 `useHyperPage`（第 64 行），而 `load` 依赖的 `loading`/`error` 还是未初始化的 const ⇒ setup 抛 ReferenceError。**rc≠0 但没有具名红就是收集失败，不是判据失效** | 注入点挪到 `onBeforeUnmount` 之前 |
| B8 | 疑似收集失败 | **变异写错**：删掉那个 `v-if` 后，紧邻的 `v-else` 失去前驱 ⇒ Vue 模板编译失败 | 只换文案，模板保持合法 |
| D3 | 转红未命中 | **expect 串指错**：真正被它打红的是 index 失败那条 | 改 expect |
| F2 | 转红未命中 | **expect 串指错**：被新增的「整表 + 逐行各一次」计数断言抓到 | 改 expect |
| G2 | 仍全绿 | **判据无牙（样本没触发）**：原用例只在**首次**就失败，`audit.value` 从未被赋值 ⇒ 「清不清旧数据」无从观测 | 补「先成功、再失败」的用例 |

复验时又暴露两处**判据/断言的粗细问题**：

- **B8 第二次仍全绿**：三处空块共用一个文案，我改的只是其中一处，而用例只断言
  「出现过『没有数据』」⇒ 改成 `.ar__cell--nodata:not(.ar__block-failed)` 定位后
  **逐个断言文案**。
- ★★ **同一个用例里挂两个 wrapper 会互相干扰**：用 `attachTo: document.body` 时，
  第二个 wrapper 的文本计数把 3 数成了 6。**每个用例只挂一次。**

★ 顺带一个 class 设计上的教训：`.ar__cell--nodata` 被**空数据格与失败格共用**
（失败格额外挂 `.ar__block-failed`），所以这两个 class **不互斥**，
按 class 计数时必须 `:not()` 排除，否则数出来的不是想数的东西。

### 门禁

build / 三门 / vue-tsc 全 rc=0；`AutoRouteView.spec.ts` 34 条；
全量 3010 条（119 文件）；十连跑 10/10。

文档 §11.92 纯追加。

## 11.93 决策回放接入口（第五十七批）

### 落点

新详情页 `/auto-route-decision/:id?`（`AutoRouteDecisionView.vue`），
**不占抽屉席**（口径同 `/maas-orders/:id`）。id 可从 `/request-detail` 带进来，
也可直接粘。

★ 入口按**角色**隐藏：`/request-detail` 是 admin 档而决策回放是 superAdmin 档
⇒ 在 `RequestDetailView` 里用 `auth.role === 'super_admin'` 挡掉，
否则 tenant_admin 点进去只会撞 403（口径同 `/integrity`）。

### ★ 补了一条 API 判据：id 形态上有没有可能带出 l2

`uuidVariants`（`analytics.go:1147-1166`）只有当 id **去连字符后长度恰为 32 且全为 hex**
时才产出 `{id, dashed}` 两个变体 ⇒ `l2Lookup = true`。
任何别的形态（探测 id、带前缀 id）**只返 1 个变体 ⇒ 后端连查询都不发 ⇒ l2 必然缺失**。

`autoRouteDecisionCanHaveL2()` 逐字照抄这条规则，于是页面能在**发请求之前**
就告诉用户「这个 id 不可能有 L2 段」，而不是让他加载完看一个空白区块。

⚠️ 它是**必要条件**不是充分条件 —— 形态对了也可能表里没这行。用例里钉住了这一点。

### ★ 四处「不能只说一半」

| # | 场景 | 错误渲染 | 本页渲染 |
|---|---|---|---|
| 1 | **404 身兼两职**（不存在 / 跨租户被拒） | 「查不到」 | 「可能不属于你可见的租户，两种情况无法区分」 |
| 2 | **l2 缺失** | 统一说「没有记录」 | 形态不可能 / 查了但没这行，**两种说法** |
| 3 | **l1 被 blob 覆盖** | 无标记 | 有 splat 证据⇒免责+`shadowed` 标记；**无证据⇒另挂「不能证明」免责** |
| 4 | **模型双空** | 合并成一句 | 单侧空不合并；两个都空才说「没有模型信息」 |

### ★★ 用例当场顶出一个**死分支**

第一版 404 同时写进了 `notFound` 和 `error`，而模板里
`v-else-if="error"` 排在 `v-else-if="notFound"` **前面**
⇒ 双职提示那条分支**永远不渲染**。用例直接顶出来（渲染出来的是短版「查不到」）。
修法：404 时 `error` 置 null，只走 `notFound` 分支。

★ 顺带一条：这条分支的失败**只有**在「error 不置位」时才会显形 ——
它是一条**只靠渲染文本就能发现**的死分支，不需要变异。

### 变异 23 条 → 23 有牙，零可疑

覆盖两个面：`api/autoRouteRead.ts` 的 `canHaveL2` 判据 + 视图本身。

首轮 20/23，三条可疑：

| id | 现象 | 真实归因 | 处置 |
|---|---|---|---|
| A2 | 仍全绿 | **判据无牙（路径没被走到）**：原用例只设了非法 id，但**按钮是禁用的、根本没点**，`load()` 里的本地守卫从未执行 | 补「回车触发」用例 —— 按钮禁用时只有 `@keyup.enter` 会真的调 `load()` |
| B2 | 仍全绿 | **判据无牙（样本没触发）**：原用例只在**首次**就 404，`detail` 从未被赋值，「L1 不出现」是恒真的 | 补「先成功回放、再 404」用例（与第五十六批 G2 同一个坑） |
| D1 | 转红未命中 | **自己的工具错了**：用**无范围的 `sed`** 改 expect 串，而 D1 与 D3 本来就是同一句 ⇒ 两条一起被改 | 改用 Python 按块定位精修 |

★ 附带一条：变异脚本**自己**把 D1/D3 两条 expect 改串这件事，
是靠运行器打印的「实际具名红列表」当场看出来的 —— 所以**分诊时先读那条列表**，
不要凭印象判断「是不是变异没施上」。

### 门禁

build / 三门 / vue-tsc 全 rc=0；`AutoRouteDecisionView.spec.ts` 21 条；
`autoRouteRead.test.ts` 67 条；全量 3038 条（120 文件）；十连跑 10/10。

文档 §11.93 纯追加。

## 11.94 Dashboard API v2 七条只读端点（第五十八批，API 层）

### ★ 缺口清单先取证，再开写

第五十五批的教训（`tuning/strategies` 两端都死）在这一族没有重演：
正面枚举全仓 dashboard 注册（`grep -rn 'HandleFunc("[^"]*dashboard'`），
清单里的九条**全部真的注册**。

⚠️ 但**权限档位与 auto-route 族相反**：这九条是 `admin(...)`（`handler.go:1053-1069`）
⇒ **tenant_admin 可用**，抽屉席**不设** `requiresRole`。

### ★★★★★★ 降级响应的 data 与「真的全是零」**逐字段相同**

`admin/dashboardapi/types.go:186-192` 的 `writeSuccessJSON` 产出：

```json
{ "success": true, "data": {…}, "metadata": {…}, "timestamp": "…" }
```

而 `writeDegraded`（`errors.go:319-334` 等）**也是 HTTP 200 + `success:true`**，
只是把 `data` 填成**零值/空数组**，并在 metadata 上打三个键：

```json
"metadata": { "degraded": true, "missing_view": "request_logs_7d",
              "hint": "数据视图尚未初始化，请先执行数据聚合迁移" }
```

⇒ 客户端若只看 data，会在聚合迁移没跑时给出一张**全部正常的看板**。
这是本族最危险的一处，也是本批所有判据的重心。

★ `degraded` 带 omitempty ⇒ **正常时是键缺失，不是 `false`**。
   判据必须用 `=== true`；写成 `!== false` 会把「键缺失」判成降级
   （D3 变异专测这一条）。

### ★★★ tenant_admin 填 `tenant_id` 会被**静默改写**

`normalizeDashboardScope`（`auth.go:39-45`）：非 `super_admin`/`admin_key`
一律 `params.TenantID = auth.TenantID` ⇒ 用户填别的租户也会拿到自己租户的数据，
而**响应里没有任何标记告诉他「你填的被忽略了」**。
⇒ UI **不得**给 tenant_admin 提供这个筛选框。

### ★ 三处容易踩的参数口径（与同批其它端点又不一样）

| 参数 | 口径 |
|---|---|
| `days` | 越界或非整数 ⇒ **静默回落 7**（不是 400） |
| `size` | 越界 ⇒ 静默回落 20，上限 100 |
| `refresh` | 后端判 `== "true"`，**严格相等** ⇒ 必须发字面量 `true` |

⚠️ auto-route 的 `tuning/accuracy` 里 `days` 越界是 **400 报错**，
`index`/`cost` 的 `top` 是**静默回落**，audit 的 `limit>500` 是 **clamp** ——
**同一个参数名在这一个仓里至少有四种口径，不能跨端点类推。**

### ★ 一处字段名与 JSON 键不一致

`ErrorStatsResponse.Trend` 的 JSON tag 是 **`recent_errors`**（`errors.go:31`）。
按 Go 字段名去读 JSON 会读到 `undefined`。

### 变异 27 条 → 27 有牙，零可疑

首轮 25/27，两条可疑：

- **D4 仍全绿** ⇒ **判据无牙（样本没触发）**：`missingView` 去掉 `isDegraded`
  守卫后看不出来，因为**正常夹具里本来就没有 `missing_view`**。
  补「判别样本」：metadata 里有 `missing_view` 但**没有** `degraded`
  ⇒ 降级标记缺失时就不许声称「缺表」。
  （后端两个键总是一起写，这个组合理论上不会发生；判据的价值正在这里。）
- **F2 转红未命中** ⇒ expect 串指错，变异实际打红的是
  「session-overview 打 GET 且带查询参数」。

★ 又一次印证：**去掉某个守卫的变异，要先构造一个「该守卫唯一在生效」的样本**，
否则两条实现输出相同，判据测不出。

### 门禁

build / 三门 / vue-tsc 全 rc=0；`dashboard.test.ts` 35 条；
全量 3073 条（121 文件）；十连跑 10/10。

文档 §11.94 纯追加。

## 11.95 看板的两条裸 JSON 端点（第五十九批，API 层）

`operational`（handler.go:1068）+ `board/error-drill`（:1069），同为 `admin(...)` 档。

⚠️ 这两条**不在** `admin/dashboardapi` 包里，走 `writeJSON` ⇒ **无信封**。
与第五十八批那七条（`{success,data,metadata}` 信封 + `metadata.degraded` 降级三联）
**不是同一种形状** ⇒ **同一个 `/api/admin/dashboard/*` 前缀下有两种响应契约，
不能跨端点类推。** 解包器里专门加了一道反向检测：拿到
`{success,timestamp}` 信封形状就报错，避免把 `success`/`timestamp` 当业务数据。

### ★★★★★ 后端缺陷：「从未运行过」被算成 `degraded`

`queryBoardBackgroundTasks`（`dashboard_board_aux.go:29-37`）只对**非**
`pgx.ErrNoRows` 记 slog，但第 66 行算 degraded 用的是**原始 err**：

```go
out["degraded"] = discErr != nil || checksErr != nil
if discErr != nil { out["degraded_reason"] = "discovery status unavailable" }
```

而那条查询是 `... ORDER BY started_at DESC LIMIT 1`
⇒ **一行都没跑过时返回 ErrNoRows ⇒ degraded=true**。
且 `strPtrVal(nil)` = `null`（不是空串），所以真错误与「没记录」在 `status` 上
**完全一样** ⇒ 客户端分不开。

**后果**：一套**从未跑过 discovery** 的新网关会一直挂着一个红的降级提示，
直到第一次真正跑起来 —— 而它什么故障都没有。

★ 那句注释（aux.go:65「恒发：前端要区分『真的 0 次』与『没查出来』」）说明
**意图是对的**，只是把第三种情况（没有记录）也塞进了 degraded。
`selfcheck` 那条查询是聚合、恒返一行，不会有 ErrNoRows ⇒ 该缺陷只在
`background_tasks.discovery` 上。

⇒ 客户端只能把它说成「**状态未知 / 从未运行过**」，**不能直接说「降级」**。

### ★ `source` 是条件键，真正的取数来源**根本没下发**

- 命中看板缓存 → 多写一个 `"source": "redis"`（dashboard_board.go:187）
- 未命中 → 那个 map 里**只有 `error_kind`/`dimension`/`items`**（:198-202）

而 `queryErrorDrill`（aux.go:115-136）在分钟视图失败/为空时会**回落到 hot-log 兜底**；
能说清来源的 `boardSource()`（`request_stats_minute` vs
`request_logs_with_current_month`）**只在 `/dashboard/board` 用**（:121），
本端点根本调不到它。

⇒ **「兜底来的」与「权威视图来的」在响应里无法区分。**
同理「零行」既可能是真没有，也可能是兜底查完仍然空。

### ★ `days` 在本仓已是第四种口径

`boardDays`（dashboard_board.go:204-213）是 **clamp 到 [1,90]，默认 1**。

| 端点 | `days` 口径 |
|---|---|
| dashboardapi 七条 | 越界/非整数 ⇒ **静默回落 7** |
| auto-route `tuning/accuracy` | 越界 ⇒ **400 报错** |
| `board/error-drill` | **clamp 到 [1,90]**，默认 1 |
| auto-route `audit` `limit>500` | clamp（另一个参数名） |

### 变异 24 条 → 24 有牙，零可疑

首轮 22/24，两条**都是 expect 串指错**（变异确实转红，只是命中我没想到的用例）：

- **B3**（去掉 `b.degraded` 守卫）实际打红的是「degraded=false 时即便 status=null
  也不算」—— 那正是「去掉守卫后多认的那一类」。
- **C5**（drill 信封检测恒真）打红 10 条，其中包含被判为「按预期通过」的那条
  （因为它本来就期待抛错）。

★ 这两处的教训是同一条：**「打红了几条」不等于「我指的那条红了」**，
分诊时必须看**实际具名列表**里有没有自己的 expect 串。

### 门禁

build / 三门 / vue-tsc 全 rc=0；`dashboardBoard.test.ts` 27 条；
全量 3100 条（122 文件）；十连跑 10/10。

文档 §11.95 纯追加。

### 11.96 第六十批：dashboard 九条接 UI（`DashboardOpsView`，admin 档）

**背景**：第五十八批做了 dashboardapi 七条的 API 层，第五十九批做了
`operational` + `board/error-drill` 两条裸 JSON 端点。两条 API 模块
（`api/dashboard.ts` 60 个导出、`api/dashboardBoard.ts` 28 个导出）
在第五十九批收口时**全无 UI 消费方**（`grep "from '@/api/dashboard'"` 与
`from '@/api/dashboardBoard'` 均只命中各自的 `.test.ts`）
—— 「写完 API 层就算接完了」是最常见的一种自我交付：
声明了不等于消费了。本批把九条端点接进一个页面。

#### 11.96.1 交付物

| 文件 | 性质 | 说明 |
|---|---|---|
| `web-mobile/src/views/DashboardOpsView.vue` | 新建 | 九段，全部按需加载 |
| `web-mobile/src/views/DashboardOpsView.spec.ts` | 新建 | 40 条 |
| `web-mobile/src/router/index.ts` | 改 | `/dashboard-ops` |
| `web-mobile/src/config/appNav.ts` | 改 | 抽屉席 `dashboard-ops` |
| `web-mobile/src/i18n/zh-CN.ts` / `en-US.ts` | 改 | `nav.dashboardOps` + `dashboardOps.*` 段 |

**权限档位**：`admin/handler.go:1052-1069` 九条注册**全部**是 `admin(...)`
⇒ tenant_admin 可用 ⇒ **抽屉席不设 `requiresRole`**。

★ 这与 `/auto-route` **相反**（那条整族是 `h.superAdmin`，`handler.go:1381/:1430`）。
两条都叫「路由/看板面」，权限档却完全相反，是本仓最容易照抄错的一处。
变异 #23 专门钉这条：把抽屉席改成 `requiresRole: 'super_admin'`
⇒ `AppDrawer.spec.ts` 转红。

**与既有页面的边界**：
- `/session-analytics` 走 `/api/admin/session-analytics/*`（另一族前缀），
  字段与降级语义与本页**完全不同**，不可互相顶替；
- `HomeView` 已消费 `/dashboard/board`（饼图那套，含 `include_operational=1`），
  本页**不重复**它，聚焦另外九条。

#### 11.96.2 六处「不能都渲染成同一个东西」

| # | 语义 | 后端依据 | UI 处置 |
|---|---|---|---|
| 1 | **降级 = HTTP 200 + `success:true` + data 全零** | `dashboardapi/errors.go:319-334` | 数字渲染成 `—` + `do__nodata` class，并挂免责句 |
| 2 | **「从未运行过」被算成 `degraded`** | `dashboard_board_aux.go:66` | 措辞降级成「状态未知 / 从未运行过」 |
| 3 | **tenant_admin 的 `tenant_id` 被静默改写** | `dashboardapi/auth.go:39-45` | 该角色**不给**筛选框 |
| 4 | **drill 的 `source` 是条件键** | `dashboard_board.go:187` vs `:198-202` | 键缺失 = 现算，不是「来源未知」 |
| 5 | **`days` 两种口径** | `types.go:119-148` vs `dashboard_board.go:204-213` | 顶部回显「静默回落 7」，drill 回显「钳位 [1,90]」 |
| 6 | **分页双份** | `session_active.go:44-45` + `:62-63` | 不一致单独报警 |

**★ 关于 (1) 的 `degraded` 键**：`Metadata.Degraded` 带 `omitempty`
⇒ **正常时是键缺失而非 `false`**。判据必须写 `=== true`；
写 `'degraded' in metadata` 会把全部正常响应误判成降级。

**★ 关于 (2)**：`queryBoardBackgroundTasks` 第 66 行
`out["degraded"] = discErr != nil || checksErr != nil` 用的是**原始 err**，
而那条查询是 `ORDER BY started_at DESC LIMIT 1` ⇒ 一张都没跑过时
返回 `pgx.ErrNoRows` ⇒ degraded=true；`strPtrVal(nil)` = `null`
⇒ 真错误与「没记录」在 status 上一样是 `null`，客户端**分不开**。
⇒ UI 只能说「状态未知 / 从未运行过」，说「降级」等于告诉运维「你这里有故障」，
而它其实什么都没跑过。

**★ 关于 (5)**：本仓 `days` 到本批为止共**四种口径**：

| 端点 | 越界行为 | 默认 |
|---|---|---|
| dashboardapi 七条 | 静默回落 **7** | 7 |
| auto-route `tuning/accuracy` | **400 报错** | — |
| `board/error-drill` | **clamp 到 [1,90]** | **1** |
| auto-route `audit`（参数名 `limit`） | clamp | — |

⇒ 顶部的窗口选择器**不能**同时驱动信封族与 drill（口径不同、默认值不同），
本批给 drill 单独一个输入框与单独的回显。

#### 11.96.3 类型门当场抓出的两个「凭印象写字段」

写视图时我凭 `api/board.ts` 里 `BoardBackgroundTasks` 的形状写了
`probe_loop.running` 与 `probe_loop.checks_last_10m === null`，
`vue-tsc` 立刻报 `TS2339: Property 'running' does not exist`。回查后端：

- `probe_loop` **恒是单键 map**（`aux.go:61`
  `map[string]any{"checks_last_10m": checksLast10m}`）⇒ 没有 `running`；
  `running` 只存在于 `discovery`（`aux.go:41-44`）。
  照着 `board.ts` 写会**凭空造出一个后端从没说过的状态**。
- `checks_last_10m` / `total_runs_24h` / `success_rate` 分别是
  `int` / `int` / `float64` 的 Go 零值，查询失败时 `Scan` 不写
  （`aux.go:53-58` / `:86-89` 只 `slog.Warn`）⇒ **恒非 null**。
  判 `=== null` 是**恒真判据**。真正可用的信号是兄弟键
  `probe_degraded`（仅 `checksErr != nil` 时出现）与 `selfcheck.degraded`。

★ 也就是说：这三条不是「类型不匹配」这么轻——照原样上线会让
「查不出来」被渲染成「真的 0 次检查」。

#### 11.96.4 变异验证：23 条，**23/23 有牙**

脚本 `/tmp/mut-co60.mjs`（含 `--dry` / `--only=N`），被测面三个文件
（`DashboardOpsView.vue` / `appNav.ts` / `dashboard.ts` / `dashboardBoard.ts`），
还原用 `writeFileSync` 原始内容 + 逐字节比对（`RESTORED=OK`，
md5 与备份一致）。

**首轮 20/23，三条异常分诊后补了两条判别样本 + 改了两个 expect 锚点，
终轮 23/23、零可疑。**

★ **三条异常的形态各不相同，值得单记**：

| # | 症状 | 真实原因 | 修法 |
|---|---|---|---|
| 4 | `rc=1` 但没抓到期望用例名 | **锚点错了**，不是判据无牙 | `expect` 改指判别样本 B |
| 9 | **仍全绿** | 真无牙：`tenant_admin` 看不到输入框 ⇒ `tenantId` 恒为 `''`，去掉 `tenantFilterVisible.value &&` 也照样不发 | 构造「残留值 + 角色降级」样本 |
| 19 | **仍全绿** | 真无牙：只喂了 `running=true`，把模板改成恒「运行中」看不出差别 | 补 `running=false` 反例 |

**★ #4 是本批最容易误判的一条**：`rc≠0` 说明判据**有牙**，
但红在另一条用例上。若按「没抓到期望名 = 变异没施上」去分诊，
就会白白去查脚本、查源码改动 —— 实际只需改 `expect` 指向。
判读顺序：**先读脚本打印的实际具名红列表**，再决定动不动手。

**★ #9 的判别样本是真实的可达路径**，不是硬凑的：
先以 `super_admin` 登录填了租户号，token 过期后服务端把角色降级，
页面上的 `tenantId` ref 仍留着旧值 —— 这时若还照发，
后端会静默改写成调用者自己的租户（`auth.go:39-45`），
用户看到的是**别的租户**的数据却以为自己在筛选。

**★ #19 是「只测一种取值等于没测这个分支」的教科书形态**：
`OPERATIONAL_NORMAL` 的 `running` 就是 `true`，
所以「把三元改成恒运行中」这条变异打上去是全绿的。
一个 `v-if` 写成恒真、恒假、或只喂一种取值，都属同一类。


### 11.97 第六十一批：人工标注工作台 API 层 + 差集基线刷新

#### 11.97.1 功能面基线刷新（第六十批前一直是陈旧的）

差集扫描重算（`/tmp/gapscan.py`，显式传绝对路径，工作区根是另一个仓）：

| | 第五十五批基线 | 本批刷新 |
|---|---|---|
| 桌面 web 实际调用 | 394 | **394**（不变） |
| 移动端实际调用 | 144 | **160** |
| 缺口 | 252 | **237** |
| 其中 GET 只读 | 139 | **124** |

移动端 +16 正是第五十六~六十批新增的调用点（`/auto-route` 1、
`/auto-route-decision` 1、`/dashboard-ops` 9、加上既有页面的连带调用）。

★ **差集清单是易腐产物**：连续五批都拿 144/252 当分母，
实际分母早就不是那个数了。分母变了，「还剩多少」这个判断本身就会错。

#### 11.97.2 ★★★ 差集清单里**有一整族是不该做的**

`GET /api/admin/center/*`（4 条）与 `GET /api/admin/faults/*`（2 条）
在差集里排在最前面，看起来是「最该补的缺口」。全仓一搜才发现：

```
cmd/gateway/maintain_proxy.go:34  maintainCompatPrefixes = []string{
    "/api/admin/licenses", "/api/admin/downloads", "/api/admin/faults",
    "/api/admin/releases", "/api/admin/autoupdate", "/api/admin/center", ...
```

这两个前缀**在网关进程里根本没有 handler**——它们被
`maintainReverseProxy` 反向代理到**独立的 maintain 服务**
（`/maintain-api`），并打上 `deprecated` 标签。

⇒ 照差集清单做，等于给一个**由另一个进程提供、且已被标记弃用**的端点做移动端 UI。
同族还有 `licenses` / `releases` / `downloads` / `autoupdate` / `vibecoding` /
`donations` / `offline-activation` 等 —— **占差集清单相当大一块**。

★ 这是第五十五批教训（`auto-route/tuning/strategies` 两端都死）的**更大规模版本**：
那次是单条，这次是**一整族**。差集清单必须先过「谁在提供这个端点」，
再谈移动端要不要做。

#### 11.97.3 交付物：annotations 三条（`api/annotations.ts`）

| 端点 | 权限 | 形状 |
|---|---|---|
| `GET /api/admin/annotations/stats` | `admin(...)` | 裸 JSON |
| `GET /api/admin/annotations/samples` | `admin(...)` | 裸 JSON |
| `GET /api/admin/annotations/first-turn-samples` | `admin(...)` | 裸 JSON |

后端注册 `admin/handler.go:1386-1393`。三条走 `json.NewEncoder(w).Encode(resp)`
⇒ **无信封**，与 `api/dashboard.ts` 不是一个家族
（解包器里加了反向检测：拿到 `success`+`timestamp` 就报错）。

**不碰写操作**：`POST /annotations`、`POST /annotations/batch`、
`DELETE /annotations/{request_id}` 会改标注事实，且互不可逆
（删一条标注会改变该样本 accuracy 的统计口径）。

#### 11.97.4 ★★★★ 本族最刺眼的一处：零标注时 stats 整条 500

`GetOverallStats`（`annotation/stats.go:31-59`）是一条**没有聚合子句**的

```sql
SELECT total_annotations, correct_count, incorrect_count,
       accuracy_percent, num_annotators, first_annotation_at, last_annotation_at
FROM annotation_stats
```

`annotation_stats` 是**单行汇总表**，不是明细表。
⇒ **表里没有行时返回 `pgx.ErrNoRows`**
⇒ `handler.go:1136-1139` 直接 `writeInternalTextErr` ⇒ **HTTP 500**。

更糟的是四个块是**串联早退**：

```go
overall, err := querier.GetOverallStats(ctx);      if err != nil { …500; return }
byProvider, err := querier.GetProviderAccuracy(ctx);  if err != nil { …500; return }
byAnnotator, err := querier.GetAnnotatorStats(ctx);   if err != nil { …500; return }
byReason, err := querier.GetReasonDistribution(ctx);  if err != nil { …500; return }
```

任一块出错**整条端点**就 500，客户端拿不到「部分可用」。

⇒ **客户端故意不写 try/catch 降级**：500 与「统计为零」在响应上不可分，
降级等于把一次数据库故障讲成「大家一条标注都没打」。
让错误以错误形态冒出来，是本批唯一诚实的选择。
（对照 `writeDegraded` 那种「200 + 全零 + `metadata.degraded`」的设计，
本族**连那个标志都没有**。）

**★ `overall` 是指针字段**（`handler.go:131` `*annotation.AnnotationStats`），
是本族唯一可能为 `null` 的块 ⇒ 解包时 `null` **不得**要求它的 7 个子键，
而「缺键」必须抛错。两者在响应上长得一样（都是读不到子键），语义完全相反。

#### 11.97.5 本族第 5、6 种 `days` 类参数口径

到本批为止，本仓**同名/同类参数**的越界行为已确认六种：

| 端点 | 参数 | 越界行为 | 默认 |
|---|---|---|---|
| dashboardapi 七条 | `days` | 静默回落 **7** | 7 |
| auto-route `tuning/accuracy` | `days` | **400 报错** | — |
| `board/error-drill` | `days` | **clamp [1,90]** | **1** |
| auto-route `audit` | `limit` | clamp | — |
| `annotations/samples` | `page` | 静默回落 **1** | 1 |
| `annotations/samples` | `size` | **双向钳位 [1,200]** | 50 |
| `annotations/samples` | `per_strata` | 钳位 [1,20] | 5 |
| `annotations/samples` | `strategy` | **400**（唯一会报错的） | recent |
| `first-turn-samples` | `start_date` | **格式错 400**（`YYYY-MM-DD`） | **今天(UTC)** |

★ **`first-turn` 的缺省日期是「今天」而不是「不限」**
（`resolveFirstTurnDateRange`，`handler.go:628-633`），
而 `samples` 的缺省是**不限窗口** —— 同一个族里两个端点的缺省语义**相反**。

★ **`strategy` 非法值会 400**，是本族唯一会报错的参数 ⇒ 前端在
`fetchSamples` 里**前置拦截**（`Promise.reject`），不发那个必失败的往返。
同理 `first-turn` 的日期格式也在前端拦。

#### 11.97.6 ★ 条件键与字段名陷阱

- **`strategy` 是条件键**：`resp.Strategy` 带 omitempty，且只在
  `strategy != "recent"` 时赋值（`handler.go:225-227`）
  ⇒ **键缺失 = recent**，不是「策略未知」。
  `unwrapFirstTurnSamples` 里有**反向检测**：本端点没有 `strategy` 键，
  拿到就报「串了端点」—— 形状对但语义错的情形。
- **`ReasonDistribution.Percent` 的 JSON 键是 `percentage`**（`types.go:97`），
  不是 `percent` ⇒ 按 Go 字段名去写前端类型会读到 undefined。
  变异 #6 专钉这条。
- `AnnotatorStats.FirstAnnotationAt` 是 `time.Time`（**非指针**）⇒ 恒有值；
  而 `AnnotationStats.FirstAnnotationAt` 是 `*time.Time` ⇒ 可能 `null`。
  **同名字段在同一族里一个指针一个值**。

#### 11.97.7 验证

- 用例 **43 条**（`api/annotations.test.ts`）
- 变异 `/tmp/mut-co61.mjs` **22 条，22/22 有牙、零可疑**，
  `RESTORED=OK`（逐字节一致）
- 三门全 rc=0；`vue-tsc --noEmit` rc=0；`npm run build` rc=0
- 全量 3183 条（124 文件）rc=0；十连跑 10/10（3183 × 10）
- ★ 本批 22 条变异**首轮即 22/22**，未出现第六十批那种「判别样本缺失」——
  差别在于**先写夹具时就把每个分支的两侧取值都准备了**
  （零标注 vs 有标注、自相矛盾 vs 正常、键缺失 vs null、
  键存在 vs 键缺失），而不是只写「正常」那一种。

文档 §11.97 纯追加。

### 11.98 第六十二批：日志运维面（API 层 + UI）

#### 11.98.1 ★★ 同一前缀下混着两种权限档

`/api/admin/logs/*` 的七条注册（`admin/handler.go:959` / `:1114-1119`）：

| 端点 | 档位 | 本批 |
|---|---|---|
| `body-cache-stats` | `admin(...)` | ✔ |
| `files` | `admin(...)` | ✔ |
| `stats` | `admin(...)` | ✔ |
| `archive/list` | `admin(...)` | — |
| `config` | **`h.superAdmin(...)`** | ✘ 另有 `PUT` 写操作 |
| `archive` | **`h.superAdmin(...)`** | ✘ 归档（动文件） |
| `cleanup` | **`h.superAdmin(...)`** | ✘ 删除（动文件） |

⇒ 本页三条都是 admin 档，**抽屉席不设 `requiresRole`**。
但这是**前缀级的巧合**，不是整族的档位 ——
★ **按前缀判权限会判错**。往后往本页加 `config`/`archive`/`cleanup`
任何一条，**整页档位必须跟着升**，不能只加端点不改抽屉席。

不碰写操作的三条理由各不相同：`config` 的 `PUT` 改的是日志级别（改行为），
`archive` 移动文件，`cleanup` **删文件**。

#### 11.98.2 ★★★★★ 五处「不能都渲染成同一个东西」

**(1) 三种「什么都没有」长得不一样**（`handleLogStats` 的三条早退路径）：

```go
resp := LogStatsResponse{}
if cur.File == "" { writeJSON(w, 200, resp); return }   // :326-329 文件日志未启用
resp.LogDir = dir
resp.Exists = dirExists(dir)
if !resp.Exists { writeJSON(w, 200, resp); return }     // :332-335 目录不存在
```

| 信号 | 语义 | UI 文案 |
|---|---|---|
| `log_dir === ''` | **文件日志根本没启用** | 「文件日志未启用」 |
| `log_dir !== '' && exists === false` | 配了路径但**目录不存在** | 「日志目录不存在：{dir}」 |
| `exists === true && total_files === 0` | 目录在，**真的没文件** | 「目录存在但一个文件都没有」 |

三者都写「没有数据」的话，运维会去查一个**根本不存在的目录问题**。

**(2) `disk_usage_pct` 的 0 是 Go 零值**：

```go
if pct, _, _, _, err := diskUsageAt(dir); err == nil { resp.DiskUsagePct = pct }   // :367-369
```

查失败时只是**不赋值** ⇒ 下发 0，与「真的 0%」不可分。
⇒ 页面**不**把 0% 讲成「磁盘没被日志占」，而是照回显并附口径说明
（「这个 0 可能是查询失败，不是真的没占」）。

**(3) `hit_rate` 同构**：`hits/(hits+misses)`，分母为 0 时后端留 `0.0`
（`logs_body_cache.go:139-142`）⇒ 「无样本」与「0% 命中率」必须分开。

**(4) `files` 的空列表也有两种成因**：
`logging.ListFiles()` 在文件日志未启用时返回**空切片 + nil 错误**
（`internal/logging/logging.go:408-410` 注释原文），
而 handler 对此**没有早退**（`log_management.go:305-315` 一路构造到底）
⇒ 唯一区分信号是 `dir` 是否为空串。

**(5) 时间指针可为 null**：`OldestMtime`/`NewestMtime` 是 `*time.Time`
（`:363-364` 直接赋可能为 nil 的指针），目录里没文件时序列化成 `null`
⇒ 时间跨度**算不出来**，不能拿「现在」或默认值顶上去。

#### 11.98.3 ★ Go 内嵌结构体会被 JSON 扁平化

`LogFileInfoExt`（`log_management.go:85-88`）内嵌 `logging.LogFileInfo`：

```go
type LogFileInfoExt struct {
    logging.LogFileInfo          // 内嵌 ⇒ json 编码**扁平化**
    SizeHuman string `json:"size_human"`
}
```

⇒ 响应里**没有** `log_file_info` 这样的嵌套层，
内层六个字段（`name`/`size_bytes`/`mod_time`/`is_current`/`is_compressed`/`is_archived`，
`internal/logging/logging.go:399-406`）与 `size_human` **平级**。
按「外层只有一个内嵌字段」的直觉写前端类型 ⇒ 七个键一个都读不到。
变异 #8（把必填键改成 `['log_file_info', 'size_human']`）专钉这条。

#### 11.98.4 变异验证：18 + 15 条，全部有牙

- API 层 `/tmp/mut-co62.mjs`：**18/18**（首轮 17/18，#6 是 `expect` 锚点错）
- 视图 + 抽屉席 `/tmp/mut-co62b.mjs`：**15/15**（其中 1 条可证等价变异）
- 两轮 `RESTORED=OK`（逐字节一致，md5 与备份相符）

**★ 一条可证等价变异（#2）**，值得单记 —— 它不是判据无牙：

```ts
const statsState = computed(() => {
  if (!stats.value) return 'ok'
  if (logStatsNotEnabled(stats.value)) return 'notEnabled'   // log_dir === ''
  if (logStatsDirMissing(stats.value)) return 'dirMissing'   // !notEnabled && exists === false
  if (logStatsEmpty(stats.value)) return 'empty'             // exists === true && total_files === 0
  return 'ok'
})
```

把 `logStatsEmpty` 里的 `exists === true` 去掉，**全绿**。
但这不是判据无牙，而是**守卫顺序使其可证冗余**：
到达 `empty` 分支时，上一行 `logStatsDirMissing` 刚返回 false，
即 `!notEnabled && exists === false` 为假 ⇒ `exists !== false`；
`exists` 是 boolean ⇒ 必为 true ⇒ 被删的检查是**恒真项**。

⇒ 这与「恒真判据」的处置相反：**恒真判据要删，恒真**守卫**要留**，
因为前者是无用的复杂度，后者是可读的显式条件。
本仓 API 层那条同名检查的判据由 `mut-co62.mjs` #3 覆盖（已验证转红）。

**★ 变异 #1/#9 首轮是「rc=1 但零具名红」—— 又是收集失败而非判据失效。**
原因是我的变异把 `/*MUTCO62B_01*/` 注释放进了**标签属性区**
（`v-else-if="false"` 后面跟注释），Vue 模板编译直接失败：

```
Error: Codegen node is missing for element/if/for node.
  Plugin: vite:vue
```

⇒ **`rc≠0` 且具名红为 0 ⇒ 先看是不是编译失败**，
模板里的变异标记必须放进属性「值」的 JS 表达式里
（`v-if="cond /*MUT*/"`），不能放在标签属性区。

#### 11.98.5 ★★ 一条被十连跑本身抓出来的流程问题

第一次十连跑报「10/10 全绿」，但日志里：

```
run#1 … run#8   Tests  3214 passed (3214)
run#9 … run#10  Tests  3238 passed (3238)
```

⇒ **中途测试条数变了**：我在这轮十连跑运行期间新增了 `LogOpsView.spec.ts`
的 24 条用例。也就是说**那轮十连跑覆盖的不是最终代码**，
前 8 轮跑的是旧代码、后 2 轮跑的是新代码。

⇒ 纪律补充：**十连跑必须在代码完全静止后启动**。
「10/10」这个数字若不附条数，是不足以证明它跑的是哪份代码的。
本批已按最终代码重跑一遍（3239 × 10）。

#### 11.98.6 交付物

| 文件 | 性质 | 说明 |
|---|---|---|
| `web-mobile/src/api/logOps.ts` | 新建 | 三条只读端点 |
| `web-mobile/src/api/logOps.test.ts` | 新建 | 31 条 |
| `web-mobile/src/views/LogOpsView.vue` | 新建 | 三段，全部按需加载 |
| `web-mobile/src/views/LogOpsView.spec.ts` | 新建 | 25 条 |
| `web-mobile/src/router/index.ts` | 改 | `/log-ops` |
| `web-mobile/src/config/appNav.ts` | 改 | 抽屉席 `log-ops`（admin 档） |
| `web-mobile/src/i18n/zh-CN.ts` / `en-US.ts` | 改 | `nav.logOps` + `logOps.*` 段 |

验证：全量 3239 条（126 文件）rc=0；三门 / `vue-tsc` / `build` 全 rc=0；
十连跑 10/10（3239 × 10）。

文档 §11.98 纯追加。

### 11.99 第六十三批：标注工作台接 UI（`AnnotationsView`）

第六十一批做好的 `api/annotations.ts`（43 条用例）**全无 UI 消费方**——
写完 API 层不等于接完了。本批补上这一段。

#### 11.99.1 交付物与权限

| 文件 | 性质 |
|---|---|
| `web-mobile/src/views/AnnotationsView.vue` | 新建 |
| `web-mobile/src/views/AnnotationsView.spec.ts` | 新建（27 条） |
| `web-mobile/src/router/index.ts` | 改（`/annotations`） |
| `web-mobile/src/config/appNav.ts` | 改（抽屉席 `annotations`） |
| `web-mobile/src/i18n/zh-CN.ts` / `en-US.ts` | 改 |

权限：`handler.go:1386/1389/1390` 三条注册全是 `admin(...)`
⇒ tenant_admin 可用 ⇒ **抽屉席不设 `requiresRole`**。

**本页只有只读面**。同前缀的三条写操作全部不接：
`POST /annotations`、`POST /annotations/batch`、`DELETE /annotations/{id}`。
后两者互不可逆 —— 删一条标注会改变该样本 accuracy 的统计口径，
而 stats 端是历史累计值，删完就对不上了。

#### 11.99.2 ★★★★★ 稀疏键是本族最刺眼的一处

`FirstTurnSample`（`admin/annotation_handler.go:100-121`）有 **9 个指针字段**，
其中 **5 个标注字段带 `omitempty`**：

```go
Title       *string    `json:"title"`                             // 无 omitempty ⇒ 可能是 null
Confidence  *float64   `json:"confidence"`
StatusCode  *int       `json:"status_code"`
Success     *bool      `json:"success"`
LatencyMs   *int       `json:"latency_ms"`
TotalTurns  *int       `json:"total_turns"`
HumanTaskType *string  `json:"human_task_type,omitempty"`           // ★ 有 omitempty
HumanModel    *string  `json:"human_model,omitempty"`
HumanProvider *string  `json:"human_provider,omitempty"`
IsCorrect     *bool    `json:"is_correct,omitempty"`
Reason        *string  `json:"reason,omitempty"`
Annotator     *string  `json:"annotator,omitempty"`
AnnotatedAt   *time.Time `json:"annotated_at,omitempty"`
```

⇒ **未标注的行，那些键根本不存在**（不是 `null`）。
⇒ 缺键必须渲染成「未标注」，**绝不能**渲染成 0、空串或「正确」——
「没标过」与「标了但判错」是两件完全不同的事，混起来会让工作台的产出不可信。

同理 `AnnotationStats.FirstAnnotationAt` 是 `*time.Time` ⇒ 可能 `null`，
而 `AnnotatorStats.FirstAnnotationAt` 是 `time.Time` ⇒ **恒有值**。

#### 11.99.3 ★★ 零标注时 stats 整条 500 —— 页面不许把它讲成「没人标注」

第六十一批已挖到：`GetOverallStats` 查的是无聚合子句的单行汇总表
（`FROM annotation_stats`），空表返 `pgx.ErrNoRows` ⇒ handler 500；
且四个块串联早退、任一失败整条挂。

本批在 UI 侧的处置：

- **不写任何 try/catch 降级**（与 dashboard 的 `writeDegraded` 相反）；
- 500 时显示的文案**明说这是查询失败**，
  并直接写出「一条标注都没有时这个端点也会返回 500，两者无法区分」；
- 失败时**不渲染任何 KPI** —— 画一个「0 条标注」的 KPI 等于把 500 讲成业务事实。

#### 11.99.4 三个分布块都要渲染

stats 的响应是 `overall` + `by_provider` + `by_annotator` + `by_reason` 四块。
我在第一版**只渲染了 `by_provider` 与 `by_reason`，漏了 `by_annotator`** ——
是 `vue-tsc` 报 `TS6133: 'annotatorRows' is declared but its value is never read`
把它顶出来的。

★ 漏一块比不渲染更糟：三个分布块里少一个，
看的人会以为「没有人标注」，而实际上是「这一块的代码没写」。
变异 #10 专钉这条（把 `v-if="annotatorRows.length"` 改成恒 false）。

#### 11.99.5 两种「日期缺省」语义相反

| 端点 | 日期留空时 |
|---|---|
| `/annotations/samples` | **不限窗口** |
| `/annotations/first-turn-samples` | **只看今天（UTC）** |

（`resolveFirstTurnDateRange`，`handler.go:628-633`：`startStr == ""` ⇒ 取今天）

⇒ 本页给 first-turn 段一个**常驻提示**说明这一点，
并配一条判据：日期留空时请求里**不得**带 `start_date`
（否则等于把窗口锁死在 2000-01-01，变异 #17 覆盖）。

#### 11.99.6 ★ 用例里的一次样本选歪（子串匹配）

写「人工判定」那两行的断言时用了
`r.text().includes('人工判定')` 找行 —— 而页面上还有一行标签叫
**「人工判定模型」**，它是「人工判定」的前缀，`find` 先撞上前者，
于是取到了 `human_model` 的值（`claude`），断言失败。

修法是加一个 `rowByLabel(w, i, label)` 工具，按 `<dt>` **精确**文本找行。

★ 这是 [[变异比断言更容易发现样本选歪]] 的又一次同族表现：
**互为前缀的标签 + 子串匹配 = 断言打在错误的行上**，
症状还很像「实现写错了」，容易去改实现。

#### 11.99.7 变异验证：20 条，20/20 有牙

脚本 `/tmp/mut-co63.mjs`（含 `--dry` / `--only=N`），
被测面 `AnnotationsView.vue` + `appNav.ts`，
`RESTORED=OK`（逐字节一致，md5 与备份相符）。

**★ 本轮踩了一次自己的坑**：前六条模板类变异我写成
`v-if="cond"` → `v-if="cond /*MUT*/"` —— **条件根本没变**，只是多了个注释，
六条全部「仍全绿」。按 [[量具先自证]] 的判读顺序，
第一步「变异本体是否真改到行为」就已经否定了它们；
改成 `v-if="false /*MUT*/"` 后六条全部转红。

⇒ **注入标记不等于变异**。带标记是为了事后 grep 与还原，
但**判据有没有牙取决于行为是否真的改变** —— 这两件事要分开确认。

验证：全量 3266 条（127 文件）rc=0；
三门 / `vue-tsc` / `build` 全 rc=0；十连跑 10/10（3266 × 10）。

文档 §11.99 纯追加。

### 11.100 第六十四批：审批查询面 API 层

#### 11.100.1 ★★★★ 本族最刺眼的一处：`total` 不是真实总数

`ListApprovals`（`api/approval_handler.go:336-372`）：

```go
filter := &sessionaudit.ApprovalFilter{
    Limit:  req.PageSize,
    Offset: (req.Page - 1) * req.PageSize,
}
records, err := h.manager.List(r.Context(), filter)
// Get total count (simplified - return length for now)
total := len(records)                                   // :348-350
...
totalPages := (total + req.PageSize - 1) / req.PageSize  // :357
if totalPages < 1 { totalPages = 1 }
```

⇒ `records` 已带 `Limit`/`Offset`，**就是当前这一页**，
所以 `total` = 本页返回了几条，**不是库里一共几条**
（源码注释自己写着 "simplified - return length for now"）。

连带地 `total_pages` 也失真：`total ≤ page_size` ⇒ `totalPages` **恒为 1**。

⇒ **UI 绝不能**：
- 显示「共 N 条」而不说明那是本页数；
- 用 `total > items.length` 判断「还有更多」（永远为 false ⇒ 永远没有下一页）。

翻页只能靠「本页取满」（`items.length >= page_size`）。

#### 11.100.2 ★★★★ `risk_level` / `trigger_type` 可以是空串

`buildListItem`（:530-549）只在 `record.DetectResult != nil` 时才填这两列：

```go
if record.DetectResult != nil {
    item.RiskLevel   = string(record.DetectResult.Decision)
    item.TriggerType = record.DetectResult.Reason
}
```

⇒ 没有检测结果的行，这两列是**空字符串**，
既不是「低风险」也不是「无风险」，是**「不知道」**。

★ 与 [[累计量 ≠ 现状]] 同源：空串在这里是「未检测」，
若渲染成「低」会让人以为系统判定过。

#### 11.100.3 ★★★ `time_left` 与三个 omitempty 键

| 键 | omitempty | 何时出现 |
|---|---|---|
| `time_left` | 是 | **仅** `status == pending` 且 `time.Until(ExpiresAt) > 0`（:551-556） |
| `approved_by` | 是 | 已审批 |
| `approved_at` | 是 | 已审批 |
| `reason` | 是 | 有理由 |

⇒ **`status == "pending"` 但**没有 `time_left` = **已过期却还标着待审批**。
UI 必须显示「已逾期」，不能显示成「待审批中」。

⚠ 判别样本的必要性在这里体现得很具体：
`approvalCountingDown` 里的 `status === 'pending' &&` 去掉后，
**全部 pending 夹具都测不出来**（它们的 `time_left` 要么有要么本就 pending）——
需要构造「**已审批但仍带 `time_left`**」这个取值才能让两种实现分叉。
（后端只在构造时按当时状态填一次，不回填清理，所以这个组合真实可达。）

#### 11.100.4 ★★ 三种静默行为与一个「混口径」的统计端

- `page` 非整数或 ≤0 ⇒ 静默回落 **1**（`err == nil && val > 0`）；
- `page_size` 非整数或 >200 ⇒ 静默回落 **50**（多条件 `&&` 全过才用）；
- `status` **完全不校验** ⇒ 非法值直接进 SQL filter，结果是**空列表**而非 400；
- `start_time` / `end_time` 是 **RFC3339**，格式错 **`if err == nil` 不成立 ⇒ 静默忽略**（:398-408）。

★ `ApprovalStats` 里还**混着两套口径**：
`today_total` / `today_pending` 在 `calculateStats` 里按「今天」单独算，
而 `start_time`/`end_time` 控制的是**其余八个字段**。
⇒ 同一份响应里，六个数字是「按你给的时间范围」，两个是「今天」。

另：`avg_approval_time_seconds` 是 `float64` 零值，
分母为 0 时留 `0.0` ⇒ 与「真的是 0 秒审批」不可分，
判据要看 `approved + rejected`（变异 #14 的判别样本：
「`approved=0` 但 `rejected>0`」——这一种取值两种实现才分叉）。

#### 11.100.5 权限与不碰的写操作

两条注册都是 `wrapAdmin(...)`（`cmd/gateway/main.go:7384-7385`）
⇒ tenant_admin 可用 ⇒ 抽屉席不设 `requiresRole`。

不碰 `/api/v1/approvals/*` 下的 **approve / reject / resume** 三条
（:7375-7381）：它们**真的改变审批状态**（会导致超时、影响会话是否放行）。

#### 11.100.6 验证

- 用例 **39 条**（`api/approvals.test.ts`）
- 变异 `/tmp/mut-co64.mjs` **19 条，19/19 有牙、零可疑**，`RESTORED=OK`（逐字节一致）
- 三门 / `vue-tsc` / `build` 全 rc=0；全量 3305 条（128 文件）rc=0；十连跑 10/10
- ★ 首轮 16/19，三条异常里有**两条是判据无牙**（#5/#14，缺判别样本），
  一条是**锚点错**（#19）。
  补两条判别样本后，#5/#14 从「仍全绿」变成「红在**新加的那条**」——
  这正好印证 [[量具先自证]] 的判读顺序：
  **「仍全绿」先问判据够不够严（这里是不够），「rc≠0 没抓到名」先问锚点对不对。**

文档 §11.100 纯追加。

### 11.101 第六十五批：审批队列接 UI（`ApprovalQueueView`）

第六十四批的 `api/approvals.ts`（39 条用例）**全无 UI 消费方**。本批补上。

#### 11.101.1 与既有审批页的边界（三页不重叠）

| 页面 | 答什么 | API 模块 |
|---|---|---|
| `/approval-config` | 租户审批**配置** | `api/approvalConfig.ts` |
| `/approval-rules` | 审批人与**规则** | 同上 |
| `/approval-queue`（本批） | 运行中的**审批实例** | `api/approvals.ts` |

配置说「该问谁」，规则说「什么条件下拦」，本页说「现在有几条在等」。

权限：`main.go:7384-7385` 两条都是 `wrapAdmin(...)` ⇒ tenant_admin 可用
⇒ 抽屉席**不设** `requiresRole`（与同族的 `approval-config`/`approval-rules` 一致）。

不碰 `/api/v1/approvals/*` 的 **approve / reject / resume**（:7375-7381）——
它们真的改变审批状态，会导致超时、影响会话是否放行。

#### 11.101.2 ★★★★★ 五处「不能都渲染成同一个东西」

**(1) `total` 是本页条数。** UI 因此显示「**本页返回 N 条**（后端只给本页数，
不给库里总数）」，并且**明确排除「共 N 条」这种措辞**。
配套钉一条 `approvalsTotalIsPageSize`：`total !== items.length` ⇒ 报契约异常。

**(2) 翻页判据只能靠「本页取满」。**
`total_pages` 恒为 1（`total ≤ page_size`），
用它会让「下一页」按钮**永远禁用**。变异 #3 专门把判据换成
`page < total_pages`，实测转红。

**(3) `risk_level`/`trigger_type` 空串 = 未检测。**
`buildListItem` 只在 `DetectResult != nil` 时才填（:546-549）。
⇒ 变异 #4 把空串渲染成「低」，实测转红 ——
「低风险」会让运维以为系统判定过，而实际上**根本没检测**。

**(4) 三态互斥：倒计时 / 已逾期 / 已决定。**

| 条件 | 显示 |
|---|---|
| `status == pending` 且有 `time_left` | 倒计时 |
| `status == pending` 且**无** `time_left` | **已逾期**（带 `aq__overdue` class） |
| 已审批（`approved_at` 键存在） | 处理时间 |
| 其它 | 无数据占位 |

★ 「已逾期」是本族**唯一会挡住会话**的状态，所以给了独立底色，
并且页面顶部额外提醒「本页有 N 条已逾期的待审批 —— 它们还在挡着会话」。

**(5) 统计端混两套口径。**
`today_total`/`today_pending` 按「今天」算，与 `start_time`/`end_time`
控制的其余八个字段**无关** ⇒ 页面上分区呈现，并明写
「今天」那两个不受时间范围影响。

另：`avg_approval_time_seconds` 分母为 0 时是 Go 零值，
⇒ 显示「无样本（还没有已通过/已拒绝的记录）」而不是「0 秒」。

#### 11.101.3 ★ 一条判别样本**自己漏了**的真实案例

本批变异 #8（把 `approvalCountingDown` 的首个分支改成恒 false）
报「rc≠0 但没抓到期望名」。追下去发现两件事：

**一、锚点命名误导了我。** 我把 `expect` 指向「已审批但仍带 `time_left`」
那条判别样本，但它红在「pending + 有 `time_left` ⇒ 显示倒计时」——
因为 `LIST_NORMAL` 里有三条 item，`rowByLabel` 返回**第一条**，
它的 `58m` 先消失。两条都是真缺陷，`namedFails` 只报首个。

**二、更值得记的是那条判别样本自己太弱。**
它原本只断言：

```ts
expect(row.find('dd').classes()).not.toContain('aq__overdue')
```

而变异让 `itemState` 落到 `'other'`（渲染成「—」）时**照样通过** ——
**判别样本自己也漏**。已改成正面断言：

```ts
expect(row.find('dd').element.textContent).toBe('2026-10-08T01:30:00Z')
expect(row.find('dd').element.textContent).not.toContain('58m')
expect(row.find('dd').classes()).not.toContain('aq__overdue')
expect(row.find('dd').classes()).not.toContain('aq__nodata')
```

★ **「判别样本」也需要被变异检验。** 它的作用是让两种实现在该取值上分叉，
但**断言强度不够时，它会「通过」而什么也没证明** ——
与 [[量具先自证]] 同源，只是这次漏在**用例**而不是**工具**上。

#### 11.101.4 验证

- 用例 **30 条**（`ApprovalQueueView.spec.ts`）
- 变异 `/tmp/mut-co65.mjs` **20 条，20/20 有牙、零可疑**，`RESTORED=OK`（逐字节一致）
- 三门 / `vue-tsc` / `build` 全 rc=0；全量 3335 条（129 文件）rc=0；十连跑 10/10
- ★ 本轮写脚本时又踩了一次嵌套模板字符串：变异串里写
  `` t(\`approvalQueue.status.${statusEcho}\`) `` 会被 **JS 求值**（`statusEcho is not defined`
  直接崩在脚本加载阶段）⇒ 变异串里的 `${}` 必须写成 `\${}`。

文档 §11.101 纯追加。

---

### 11.102 data-lifecycle 只读四条接 API 层（第六十六批）

**范围**：`web-mobile/src/api/dataLifecycleStats.ts`（新建）+ `.test.ts`（新建）
**四条的注册（全部 `admin` 档，tenant_admin 可用）**

| 端点 | 注册 | handler |
|---|---|---|
| `GET /api/admin/data-lifecycle/stats` | `admin(...)` | `handler.go:960` |
| `GET /api/admin/data-lifecycle/metrics` | `admin(...)` | `handler.go:962` |
| `GET /api/admin/data-lifecycle/jobs` | `admin(...)` | `handler.go:979` |
| `GET /api/admin/data-lifecycle/blobs/top` | `admin(...)` | `handler.go:994` |

**与既有 `api/dataLifecycle.ts` 不重叠**：那个模块只覆盖 `storage/tables` 与 `partitions` 两条。

**排除的写操作**：`POST /data-lifecycle/cleanup/preview`（虽名为 preview，body 带
`action ∈ {trim, archive, delete}` 且走 POST）、`POST /blobs/cleanup/*`、`/partitions/*`、`/hot/promote`。

**同前缀混两档**：`partitions/archive` 起、`hot/*`、`storage/tables/vacuum*|reindex` 是
`h.superAdmin`（`handler.go:963-978`），本批这四条是 `admin` ⇒ **不能按前缀判权限**。

#### 11.102.1 ★★★★★ `metrics` 恒不提供清理/归档时间——是死字段，不是「从未清理过」

`data_lifecycle_metrics.go:33-34` 声明了

```go
LastCleanupAt *string `json:"last_cleanup_at,omitempty"`
LastArchiveAt *string `json:"last_archive_at,omitempty"`
```

而 `handleDataLifecycleMetrics`（`:39-86`）**从头到尾没有给它们赋值**——
一次 `QueryRow(...).Scan(10 个目标)` 只覆盖十个数字字段。
全仓 grep 证据：这两个标识符**只出现在那两行声明里，零个赋值点**。

⇒ 这两个键**永远不存在**，无论清理/归档是否真的发生过。
⇒ 键缺失**不能**说成「从未清理过」。清理可能早就跑过了，只是这个端点不报。
⇒ 客户端只能保留可选字段以求前向兼容，措辞必须是「本端点不提供这个时间」。

> ★ 第一版我把这里写成了「从没清理/归档过时键不存在」，并配了
> `metricsNeverCleaned()` / `metricsNeverArchived()`。
> 写用例时才发现**这两个函数是恒真的**（真实响应恒无键），
> 而恒真会让 UI 永远显示「从未清理」——一句**假话**。
> 这是本批唯一一个「注释写错 ⇒ 判据恒真 ⇒ UI 撒谎」的完整链条。

#### 11.102.2 ★★★★ `jobs.running` 空时是 `null`，`history` 空时是 `[]`

```go
// data_lifecycle_jobs.go:237  listJobs
var running []*JobRun            // ← nil 切片
// :247
hs := make([]*JobRun, 0, ...)    // ← 非 nil
```

无任务时 `running` 的 `append` 一次都不执行 ⇒ `json.Encode(nil 切片)` ⇒ **`null`**；
而 `history` 由 `make(..., 0, ...)` 起步 ⇒ 恒为 `[]`。
**「刚重启、一个任务都没起过」是常态** ⇒ `running: null` 是高频合法响应。

第一版 `unwrapLifecycleJobs` 对两个键一律 `requireArray` ⇒ **一上线就抛错**。
改为 `nullableArray`：`null` 归一为 `[]`，非 null 非数组仍抛错。

> ★ 对照：`blobs/top` 的 `rows` 是 `make([]blobRow, 0, limit)`（`:120`）⇒ **恒 `[]`**。
> **同一个 Go 家族里两个切片的空态编码不同**，只能逐个读源码，不能类推。

#### 11.102.3 ★★★★ `metrics` 不做租户隔离，但注册是 `admin` 档

`data_lifecycle_metrics.go:3-11` 的文件头注释写着：

> Currently the endpoint is super-admin only and the SQL is left unscoped.

而注册处是 `admin(...)` ⇒ **tenant_admin 实际能调**，
而 SQL 是 `FROM request_logs`（`:63`）**无 WHERE** ⇒ 它讲的是**整表**。
同时 `stats`（`:57-63`）与 `blobs/top`（`:91-97`）都做了 `IsTenantAdmin(r)` 判别
⇒ **同族三条端点的隔离口径不一致**。

> **注释与注册矛盾时以注册为准。** 这条与 `/auto-route` 的 `h.superAdmin` 正好相反，
> 照抄任一边都会错。

#### 11.102.4 ★★★★ `stats` 里混了两种口径，且一个端点有三种「查不出来」编码

**混口径**：`total_rows` 走 `COUNT(*) … WHERE 1=1` + `tenantFilter`（`:75-79`），
而 `total_size_bytes` 是 `pg_total_relation_size('request_logs')`（`:76`）——
**整张表的物理大小，不带任何过滤**。同一个对象里既有租户口径又有全表口径
⇒ 不能并排写成「本租户 X 行 / Y 字节」。

同理每段 / 每租户的 `size_bytes` 是
`pg_total_relation_size('request_logs') * rows / total_count`（`:105-106`）——
**按行数摊派出来的估算值（含索引），不是实测大小**。

**三种失败编码并存**：

| 环节 | 后端行为 | 客户端看到 |
|---|---|---|
| 总量查询失败 | `:81-85` **500** | 整条挂 |
| 分段查询失败 | `:137-141` **500** | 整条挂 |
| 分段行 `Scan` 失败 | `:146-149` `warnRowSkip` + `continue` | **该段 `null`** |
| `by_tenant` 查询失败 | `:197-201` 非致命（注释 "non-fatal, continue"） | **`[]`** |
| `growth_trend` 查询失败 | `:262-266` 非致命 | **`[]`** |

⇒ `[]` 与「真的没有数据」**不可分** ⇒ UI 不能说「无数据」，
只能说「没有可展示的记录」。导出 `listEmptyIsAmbiguous()` 钉住这一点。

#### 11.102.5 ★★★ 30 天边界是双侧闭区间 ⇒ 段行数之和可能超过 `total_rows`

```sql
-- warm (:116)  ts BETWEEN NOW() - INTERVAL '30 days' AND NOW() - INTERVAL '7 days'
-- cold (:124)  ts BETWEEN NOW() - INTERVAL '90 days' AND NOW() - INTERVAL '30 days'
```

Postgres 的 `NOW()` 在一个语句内是同一个事务时间，两侧都含端点
⇒ 落在 `NOW()-30d` 那一瞬间的行**会被数两次**。

⇒ 「四段之和 > `total_rows`」不是数据错了，而是**边界重复计数**。
解读时不能报成缺陷。`metrics` 侧（`:57-60`）同一成因。

#### 11.102.6 ★★★ 其余已确认的契约

- `days` 是**后端写死的标注值** `:165/168/171/174` = `7 / 23 / 60 / 999`
  （不是区间上界；`warm` 是 23 不是 30，`expired` 是 999 不是 91）。
- `percent_of_total` 是 **0-100**（`:153` `rows/total*100`），不是 0-1；
  `total_rows === 0` 时四个都留 `0.0`，与「真的是 0%」不可分。
- `compression_rate` **被后端夹到 100**（`:280-282`），且 `requests === 0` 时留 `0.0`。
- `by_tenant` `LIMIT 10`、`growth_trend` `LIMIT 7` 且 **`ORDER BY day DESC`（新的一天在前）**
  ⇒ 折线图若按返回序直接连线会**倒着走**。
- `jobs` 的 `limit` **硬编码 50**（`:322` `h.listJobs(50)`），
  而 `listJobs` 内部又钳到 `registry.maxKeep`（`:244-246`）⇒ 前端传什么都没用。
- `JobRun` 除 `run_id`/`op`/`status`/`duration_ms` 外**十个字段都带 omitempty**。
  `finalizeJob`（`:200-219`）入历史前必设 `finished_at` ⇒
  `running` 列表无 `finished_at`、`history` 列表必有。
- `blobs/top` 的 `total_bytes` 是**这 N 行的合计**（`:135` 逐行累加），**不是全表总量**。
- `blobs/top` 的 `limit` 口径：`Atoi` 成功 **且** `0 < n <= 200` 才生效，
  否则**静默回落 20**（`:82-86`）——注意**不是** approvals 的「回落 50」。
- `blobs/top` 的逐行 `total_bytes` 是后端在 Go 里现算的（`:133`）⇒
  客户端的一致性校验**结构上恒真**，抓的是形状不符/传输损坏，不是后端逻辑错。
- ★ 同一响应里两种时间精度：`occurred_at` 是 `ts.UTC().Format(time.RFC3339)`（`:132`）⇒ **秒级 + Z**；
  `collected_at` 是 Go `time.Time` 直编 ⇒ **纳秒级**。
- `blobs/top` 的 `session_key`/`tenant_id` 是 `COALESCE(..., '')` ⇒ 无会话/无租户时是**空串**不是 null；
  `model` 是 `COALESCE(outbound_model, '')` + omitempty ⇒ 无模型时**键不存在**。

#### 11.102.7 ★ 变异暴露的判据缺陷：夹具用常量造 = 自指恒真

`/tmp/mut-co66.mjs` 第一轮 **35/43**，8 条异常。判读结果：

- **5 条是锚点指错**（变异确实转红了，只是红在别的判别样本上）——
  与 §11.101 的老问题同源：`namedFails` 只报首个红名，
  锚点必须指向「该变异第一个破坏的取值」。
  例：把 `>= GROWTH_TREND_DAYS` 放宽成 `> 0` 后，
  「满 7 条」那条仍为 true，**分叉点是「只有 3 天却判成截断」**。
- **3 条是真缺陷**（判据无牙 / 测试自指）：
  1. `by_tenant 满 10 条` 用 `Array.from({length: BY_TENANT_LIMIT})` 造夹具
     ⇒ **改常量两边一起变**（变异 #22 把 10 改成 20 仍全绿）。
  2. `growth_trend 满 7 条` 同样自指（变异 #23）。
  3. 缺「未取满 ⇒ 未截断」的**负控**（变异 #17 放宽后仍全绿）。

修法：夹具条数**硬写后端 SQL 的字面量**（10 / 7），并补负控；
再把常量值本身也断言（`BY_TENANT_LIMIT === 10`）。

> ★ **同族**：「`--dry` 只验匹配验不出行为」——这里更隐蔽：
> **自指夹具能让「匹配上 + 改了常量 + 用例照过」三者同时成立。**
> 判据的输入里若出现被测常量本身，那条判据对它就是恒真。
> 判据的输入必须来自**外部真相**（后端源码的字面量），不能来自被测代码。

#### 11.102.8 验证

- 用例 **99 条**（`dataLifecycleStats.test.ts`）
- 变异 `/tmp/mut-co66.mjs` **43 条，43/43 有牙、零可疑**，`RESTORED=OK`（逐字节一致）
- 三门 rc=0（css-media 91 文件 / touch-target 88 个 .vue / i18n 2457 键一致）
- `vue-tsc --noEmit -p tsconfig.app.json` rc=0；`npm run build` rc=0
- 全量 **3434 条（130 文件）** rc=0

> ★ 类型门又抓出两处：`noUncheckedIndexedAccess` 下
> `s.growth_trend[i].date` 报 TS2532（要显式收窄）；
> 以及给夹具加了返回类型标注后，`as Record<string, unknown>` 全部撞 TS2352
> （接口无索引签名）⇒ 改用 `raw()` 辅助走 `unknown` 中转。

#### 11.102.9 留给第六十七批（UI）的硬约束

1. `metrics` 必须标「**全表口径**」，不能与 `stats`（做了租户过滤）并排成同口径的两个数。
2. `stats.total_rows` 是租户口径、`total_size_bytes` 是全表口径 ⇒ 不能并排写「本租户 X 行 / Y 字节」。
3. 四段 `null` 要显示「**查不出来**」，不是「0 行」。
4. `by_tenant` / `growth_trend` 空数组措辞只能是「没有可展示的记录」。
5. `growth_trend` 折线要**反转**成时间正序再画。
6. `jobs` 的 `limit` 是 50 的后端硬编码，UI 不该给「条数」选择器。
7. `blobs/top` 的 `total_bytes` 要标「本次 N 行合计」。
8. `last_cleanup_at` / `last_archive_at` 标「本端点不提供」，**绝不能**说「从未清理」。
9. 抽屉席：admin 档，**不设** `requiresRole`。

文档 §11.102 纯追加。

---

### 11.103 data-lifecycle 只读四条接 UI（第六十七批）

**范围**：`web-mobile/src/views/DataFlowView.vue` + `.spec.ts`（新建）、
`router/index.ts`、`config/appNav.ts`、`i18n/{zh-CN,en-US}.ts`
**路由**：`/data-flow`，抽屉席 key `data-flow`，**admin 档不设 `requiresRole`**
（四条注册 `admin/handler.go:960/962/979/994` 全部是 `admin(...)`）。

#### 11.103.1 ★ 新建独立页而不是并进 `/data-lifecycle` —— 按「答什么」划界

| 页面 | 答什么 | 粒度 |
|---|---|---|
| `/data-lifecycle` | 数据库这一层什么状态（分区清单 + 表体积榜） | **表级** |
| `/data-flow`（本页） | 记录怎么分布 / 有没有在清理 / 大字段占多少 | **记录级** |

理由不只是粒度不同：本页的 `metrics` 是**全表口径**
（`data_lifecycle_metrics.go:63` 的 `FROM request_logs` 无 WHERE，注册却是 `admin`），
而 `/data-lifecycle` 的数字是租户/榜内口径 ⇒
**并排会给出错误对比**。合并两页就是本仓反复踩的「按名字划界」。

#### 11.103.2 ★★★★★ UI 必须钉住的九件事（全部来自 §11.102 取证）

| # | 约束 | UI 处置 |
|---|---|---|
| ① | `metrics` 全表口径 | 独立区顶部挂橙色横幅「整张表」，且**不得**在 stats 区出现同一块横幅 |
| ② | `total_rows` 租户口径 / `total_size_bytes` 全表口径 | 两行**各挂口径标签**，并明写「口径不同」 |
| ③ | 四段可为 `null` | 显示「这一段查不出来」，**不是「0 行」**（独立 class `df__rowMain--unknown`） |
| ④ | 空数组是二义的 | 措辞「没有可展示的记录 —— 也可能是这一段查询被跳过了」 |
| ⑤ | 趋势新的一天在前 | 列表 `.reverse()` 成时间正序，并明写「已改成时间正序」 |
| ⑥ | `jobs` limit 硬编码 50 | **刻意不给条数选择器**，只写「传了也不会生效」 |
| ⑦ | `total_bytes` 是 N 行合计 | 横幅带 N：「本次返回的 2 行合计 249 KB（不是全表总量）」 |
| ⑧ | 清理/归档时间恒不存在 | 显示「本端点不提供」，并解释「不能说从未清理」 |
| ⑨ | `size_bytes` 是摊派估算 | 每处带「摊派估算 {size}」 |

#### 11.103.3 ★ 变异暴露的三处判据缺陷（本批真缺陷）

第一轮 **18/28**，10 条异常。判读结果：

1. **7 条锚点指错**（变异确实转红，只是红在别的用例上）——
   与 §11.101/§11.102 同源。**锚点必须指向用例标题里的片段**，
   我第一版把断言**正文**里的字样（如「值单元格断言」「摊派估算 512 MB」）
   当成了 `expect` ⇒ 一条都没匹配上。
2. **`segment()` 里的三元是恒真死代码**：
   ```ts
   return segmentUnavailable(s, key) ? null : s[key]
   //                    为真时 s[key] 本来就是 null ⇒ 两个分支同值
   ```
   变异把它改成 `return s[key]` 仍全绿。⇒ 已删（`DataLifecycleStats[key]`
   的类型本身就是 `DataSegment | null`）。
   替代变异改成「让 `segmentSizeText` 不走 UNAVAILABLE 兜底」——
   它**仍是等价变异**，因为模板的 `v-else` 已先行分流，null 段根本走不到那个 helper。
   按「恒真守卫要留」处置，标记为**可证等价变异**并单列统计。
3. **两条缺失用例**（都是变异实测漏出来的，不是想出来的）：
   - **`statsRowsDisagree` 在视图层完全没覆盖**。补用例时才发现：
     该判据在**有段缺失时直接返回 false**（缺段不代表多算了），
     而默认夹具里 `cold_data` 恰是 `null` ⇒
     **不先把四段补齐就永远测不到这条判据**。
     ⇒ 夹具必须**显式**造一个「四段齐全但和 > 总量」的样本。
   - **「报错时清空数据」在首次失败时是空操作**。原用例只测首次失败 ⇒
     清空前本来就是 null，改不改编排一样。
     ⇒ 改成「先成功加载一次，再让第二次失败」，断言旧数据消失。

> ★ 后者是「**判据的输入必须能区分被测的两个分支**」的又一例：
> 若判据的两条路径在夹具上等价，它就对改动无感。

#### 11.103.4 其它

- 抽屉席**不设** `requiresRole`；变异 #28 把它误设成 `super_admin`
  ⇒ `AppDrawer.spec.ts` 的白名单式断言转红（该门已能抓到这类误标）。
- i18n：新增 69 键（`dataFlow.*` 66 个 + `nav.dataFlow` + 2 个），
  zh-CN / en-US 各 2526 键，键集一致。
  ★ 首次跑 i18n 门报了 **13 个「视图引用了词典里不存在的键」** ——
  我把键嵌在 `stats`/`jobs`/`blobs` 子对象下，视图却按**顶层**路径取，
  另有 `rateSaturated`/`rateNoSamples` **压根没定义**。
  这正是那道门存在的意义。
- 类型门（`vue-tsc --noEmit`）抓出三处：夹具返回类型未标注导致
  `cold_data` 被推成 `null` 字面量、`f[k]` 在 `noUncheckedIndexedAccess` 下
  可能为 null、`f.hot_data.rows` 漏收窄。
  ★ **`build` 里内含类型门** ⇒ 只跑 `vue-tsc` 通过还不够，`npm run build` 必须单独 rc=0。

#### 11.103.5 验证

- 用例 **47 条**（`DataFlowView.spec.ts`）
- 变异 `/tmp/mut-co67.mjs` **28 条：27 条有牙 + 1 条可证等价，零异常**，
  `RESTORED=OK`（视图与 `appNav.ts` 均逐字节一致）
- 三门 rc=0（css-media 91 文件 / touch-target **89 个 .vue** / i18n 2526 键一致）
- `vue-tsc` rc=0；`npm run build` rc=0
- 全量 **3481 条（131 文件）** rc=0；十连跑 10/10

文档 §11.103 纯追加。

---

### 11.104 差集基线刷新 + compression 只读两条接 API 层（第六十八批）

#### 11.104.1 ★ 差集基线（第二次经量具校准）

| 指标 | 上一基线（第六十一批） | 本次 | 变化 |
|---|---|---|---|
| 桌面 web 实际调用 | 394 | **394** | — |
| 移动端实际调用 | 160 | **175** | **+15** |
| 桌面有 / 移动端无 | 237 | **225** | −12 |
| 其中 GET 只读 | 124 | **112** | −12 |

已排除（属 `cmd/gateway/maintain_proxy.go:34` 的 `maintainCompatPrefixes`，
由独立 maintain 服务代管且已 deprecated）：`center/*`、`faults/*`、`licenses*`、
`releases*`、`downloads/*`。

#### 11.104.2 本批范围

| 端点 | 注册 | handler |
|---|---|---|
| `GET /api/admin/compression/stats` | `admin(...)` | `handler.go:954` |
| `GET /api/admin/compression/sessions` | `admin(...)` | `handler.go:955` |

桌面调用方：`web/src/api/compression.ts:40,77`。移动端此前**无**本族模块
（`dataLifecycleStats` 里的 `compression_rate` 是另一个字段，无关）。

#### 11.104.3 ★★★★★ 本族最要紧的五件事

**(1) ★★★★★ `count` 查询失败返的是 200 + `{items:[], count:0}`，不是 500**

```go
// compression_sessions.go:108-112
if err := h.db.QueryRow(ctx, countSQL, args...).Scan(&totalCount); err != nil {
    slog.Warn("compression_sessions count query failed", "error", err)
    writeJSON(w, http.StatusOK, compressionSessionsResponse{Items: make([]compressionSessionItem, 0), Count: 0})
    return   // ← 主查询根本没跑
}
```

⇒ `count: 0` **无法区分**「真的没有会话」与「count 查询失败」，且后者连主查询都没跑。
⇒ 同时注意主查询失败走的是 **500**（:155-160）——**两条失败路径不一致**。

**(2) ★★★★ `hours` 只在 `from` 与 `to` 都缺省时才参与时间窗计算**

```go
// :89-109（stats）与 :64-85（sessions）逐字相同
if fromStr != "" { from = parse(fromStr) } else { to = now; from = to - hours }
if toStr   != "" { to   = parse(toStr)   } else if fromStr != "" { to = now }
```

⇒ **只要传了 `from` 或 `to` 任一，`hours` 就被完全忽略。**
前端「顺手」带上 `hours` + `from` 会出现「界面上显示 hours，实际没生效」。
⇒ 导出 `timeWindowMode()` / `timeWindowQuery()`，后者**只在 hours 口径下才发 `hours`**。

另：`hours` 是 **clamp 到 [1,720]**（:75-80），而 sessions 的 `page_size`
是**静默回落 50**（:56-58）—— 同一族里两种越界口径。
`from`/`to` 格式错 ⇒ **400**（不是静默忽略）。

**(3) ★★★★ `compressed_total` 是组级口径，不是行级口径**

```go
// compression_stats.go:173-176
result.TotalRequests += cnt
if withOutbound > 0 { result.CompressedTotal += cnt }   // ← 整组都算
```

按 `strategy` 分组，`with_outbound = COUNT(rb.outbound_body)` 是**组内**计数
⇒ 只要组内有任意一行有 outbound_body，**整组 cnt 都进 `CompressedTotal`**。

⇒ `compression_rate = CompressedTotal / TotalRequests`（:187，**0-1 比例**）
的分子**不是**「被压缩的行数」，而是「至少有一行被压缩的策略组的行数之和」。

★ 与 `data-lifecycle` 的 `percent_of_total`（0-100）**单位相反**，两族不可直接比较。

**(4) ★★★★ 本族两个端点的 nil 指针编码相反**

| 位置 | 字段 | 编码 |
|---|---|---|
| `compressionStats` | 七个 token 字段 | `*int64` + **omitempty** ⇒ 值不大于阈值时**键不存在** |
| `compressionSessionItem` | 四个 `*int` | **无 omitempty** ⇒ 键**恒存在**，值为 **`null`** |

⇒ 「键缺失」与「值为 null」在本族是**两种不同的失败语义**，不能互相套用。
`sessions` 的解包器因此**逐个校类型但不校非空**。

**`estimated_tokens_saved` 尤其危险**：只在
`estimated_original_tokens > total_outbound_tokens` 时才设指针（:204-206）
⇒ 「节省为 0」「节省为负」「估算查询静默失败」**三种情况都是键缺失**
⇒ 客户端**不能**把缺失说成「没节省」。

`estimated_original_tokens` 的缺失同理：估算查询失败只有 `slog.Warn`
（:196-200，**静默**），与「值为 0」（:202）**分不出来**。
后端注释自己记了这个历史坑：pre-P2-C1 的 `::text` 缺失导致该字段
**一辈子没被填过**，被 `err==nil` 静默吞掉。

**(5) ★★★ 租户隔离之外还有第二重口径差异：`($3 OR rl.success)`**

`$3 = !tenantFilter`（:135 / :90）⇒ **只有非 tenant_admin 才忽略 `success`**
⇒ **tenant_admin 只看成功请求**，super_admin 的数字含失败请求。

另：租户过滤走 `tenantLogsClause`（`admin/session_tenant.go:16-26`），
它**只对非 default 租户的 tenant_admin 注入** `AND tenant_id = $N`
⇒ **default 租户的 tenant_admin 不被隔离**（看得到全部租户的行）。
这与 `data-lifecycle` 的 `IsTenantAdmin` + 字符串拼 `tenant_id`
（`data_lifecycle.go:57-63`）**口径不同** —— 那条连 default 租户也过滤。
⇒ **两族的租户隔离不可类推。**

#### 11.104.4 ★ 与 approvals 的关键对比：`count` 是真实总数

| | `approvals.total` | `compression.sessions.count` |
|---|---|---|
| 来源 | `total := len(records)`（records 已带 Limit/Offset） | `COUNT(DISTINCT gw_session_id)`（全量） |
| 含义 | **本页条数** | **真实总数** |
| 可否据此翻页 | ❌ 只能「本页取满」 | ✅ 可以 |

★ 但 `count` 与 `items` 仍有**两处**对不上，且都是**可预期**的：

1. 空 `gw_session_id` 的行被 `if item.GwSessionID != ""`（:190）**静默丢弃**，
   而 `COUNT(DISTINCT)` **把空串也算进去**
   （where 里只有 `gw_session_id IS NOT NULL`，**空串**仍会通过）。
2. 逐行 `Scan` 失败走 `continue`（:177-180），count 不受扫描失败影响。

#### 11.104.5 ★ 其余已确认的契约

- `hourly_series` 的粒度**随时间窗变化**（:257-266），字段名骗人：
  `≤48h` ⇒ 每小时；`≤168h` ⇒ **每 6 小时**（`date_trunc('day') + 6h*(hour/6)`）；
  更长 ⇒ 每天。导出 `seriesGranularityOf()`。
- 桶查询失败是**静默**的（:279-280）⇒ `hourly_series` 空数组**不能**断言「没有流量」。
- `strategy_distribution` 与 `hourly_series` 都预置为空（:131-132）
  ⇒ 空值恒为 `{}` / `[]`，**不是 null**。
- `strategy` 过滤是精确 `=` 且**不校验合法性** ⇒ 非法值返回空列表而非 400。
- sessions 的 where 含 `rb.outbound_body IS NOT NULL AND rl.gw_session_id IS NOT NULL`
  ⇒ **列表天然只含「确实有 outbound_body 的会话」**。
- `compression_strategy` 用 `MAX(rl.compression_strategy)`（:129）
  ⇒ **字典序最大**的那一条，不是首个也不是最新（会话中途换策略时读到的是巧合值）。
- `sample_request_id` 是 `MAX(rl.request_id)` ⇒ **字典序最大**，不是最新那次。
- `first_ts`/`last_ts` 是 Go `time.Time` 直编 ⇒ RFC3339 **纳秒**级。
- `estimated_original_msgs` 由 SQL 侧 `COALESCE(latest.orig_msg_count, 0)` 兜底（:125）
  ⇒ **恒非 null**；而 `outbound_msg_count` 直接 `MAX()` 扫进指针 ⇒ **可为 null**。
  ⇒ 同名字段族里一个恒有值一个可能为空。
- `msg_reduction` 只在两个指针都非 nil 时才计算（:183），
  且 **`red = orig - outbound`，负值被夹到 0**（:185-187）⇒ **永不出现负数**。
  ⚠️ 判「被夹住」的条件是 **`outbound > orig`**（本批写反过一次，被变异 #29 抓到）。
- `compression_sessions.go:199-201` 的 `if items == nil` 是**死代码**：
  `:163` 已经 `make([]compressionSessionItem, 0)`，永不为 nil。

#### 11.104.6 验证

- 用例 **58 条**（`compression.test.ts`）
- 变异 `/tmp/mut-co68.mjs` **30 条，30/30 有牙、零可疑**，
  `RESTORED=OK`（逐字节一致）。#29 直接抓出「夹值判断方向写反」这个真缺陷。
- 三门 rc=0；`vue-tsc` rc=0；`npm run build` rc=0
- 全量 **3539 条（132 文件）** rc=0；十连跑 10/10

#### 11.104.7 留给第六十九批（UI）的硬约束

1. `count` 可以当总数用，但 `count ≠ items.length` 时**不要报成 bug**。
2. `count: 0` 的空态措辞只能说「没有可展示的记录」。
3. `compression_rate` 是 **0-1 比例**，UI 要乘 100 再显示，且**不能**与
   data-flow 的 `percent_of_total`（0-100）并列。
4. 传了 `from`/`to` 时**不要**再显示 `hours` 控件。
5. `hourly_series` 要按 `seriesGranularityOf` 标注真实粒度，不能写「按小时」。
6. 三个 token 估算字段缺键时统一措辞「未给出估算」，**不能**说「为 0」/「没节省」。
7. `msg_reduction` 为 null 显示「未知」；`= 0` 且 outbound > orig 时
   要提示「压缩后消息数未减少（已按 0 记）」。
8. `compression_strategy` / `sample_request_id` 都要标「取字典序最大值」，不是最新。
9. tenant_admin 看到的是**仅成功请求**的数字，要标明。

文档 §11.104 纯追加。

---

### 11.105 compression 只读两条接 UI（第六十九批）

**范围**：`web-mobile/src/views/CompressionView.vue` + `.spec.ts`（新建）、
`router/index.ts`、`config/appNav.ts`、`i18n/{zh-CN,en-US}.ts`
**路由**：`/compression`，抽屉席 key `compression`，**admin 档不设 `requiresRole`**
（`handler.go:954/955` 两条都是 `admin(...)`）。

#### 11.105.1 与既有页面的分工（按「答什么」划界）

| 页面 | 答什么 | 压缩率单位 |
|---|---|---|
| `/data-flow` | 记录怎么分布（逐日冷热、增长趋势） | **0-100 百分数** |
| `/compression`（本页） | 压缩策略实际压了多少、被压成什么样 | **0-1 比例** |

⚠️ **单位相反，不可并列成同一组对比** —— 页面上以明示文案挡住。

#### 11.105.2 ★★★★★ UI 必须钉住的九件事（§11.104.7）

| # | 约束 | UI 处置 |
|---|---|---|
| ① | `count` 是真实总数，可据此翻页；但 `count > items.length` 是**可预期**的 | 页码按 `sessionsHasNextPage(resp, pageSize)` 判；缺口用「是可预期的」说明，**不报成 bug** |
| ② | `count: 0` 是二义的 | 空态文案「总数查询被跳过了……和『真的没有』分不出来」 |
| ③ | `compression_rate` 是 0-1 比例 | 展示 ×100 并标「比例」，另附「与数据流页不是同一单位」 |
| ④ | `hours` 只在 from/to 都缺省时生效 | **二选一切换器**；切到自定义区间时跨度控件**整块消失** |
| ⑤ | 序列粒度随窗口变 | 标题带真实粒度（每小时 / 每 6 小时 / 每天） |
| ⑥ | 三个估算字段缺键 | 统一「未给出估算」；节省字段单独说「未给出节省估算」 |
| ⑦ | `msg_reduction` null / 被夹 | null ⇒「未知」；`=0` 且 outbound > orig ⇒ 额外挂「后端按 0 记」 |
| ⑧ | 两个 `MAX()` 字段 | 策略与样本请求都标「取字典序最大值」 |
| ⑨ | tenant_admin 只看成功请求 | 时间窗区顶部挂常驻横幅 |

★ ④ 的实现要点：切口径时**必须重新拉取**，否则界面上留着上一套时间窗的数字。
切换器因此调用 `reloadAll()` 而不是只切 UI。

#### 11.105.3 ★ 变异暴露的判据缺陷（本批真缺陷）

第一轮 **20/29**，10 条异常。判读结果：

1. **6 条锚点指错**（变异确实转红，只是红在别的用例上）——
   锚点必须取用例**标题**里的片段（§11.101 / §11.102 / §11.103 连续三批同款）。
2. **2 条「注入标记 ≠ 变异」** —— 我自己犯的：
   - #1 删掉了一行**注释**（`<!-- explicit 口径：hours 控件整块消失 -->`），
     渲染完全不变 ⇒ 根本不是变异。
   - #18 把 HTML 注释塞在插值表达式**后面**，渲染同样不变。
   ⇒ 改成真变异：#1 改为 `v-if="windowMode === 'hours'"` → `v-if="true"`
   （explicit 模式下选择器仍在）；#18 改为删掉整个 `<span>`。
   ⇒ **判据**：注入后先问「渲染结果变了吗」，只看「代码变了」不算。
3. **3 条是真缺用例**：
   - 策略分布对不上总数时的提示（#23）：夹具 `none:400 + p2c:600 = 1000 = total`，
     永远一致 ⇒ 判据从来没被触发过。
   - 每页条数「越界静默回落 50」的说明（#26）：选择器值有断言，说明文案没有。
   - **stats 报错时清空数据**（#28）：又是「首次失败是空操作」——
     必须**先成功一次再让第二次失败**（§11.103.3 同款）。

#### 11.105.4 ★ i18n 门抓到的三件事（与 §11.103 同款，但多了一个新的）

1. **键路径不匹配**：`reductionUnknown` / `reductionClamped` 嵌在 `sessions` 子对象下，
   视图却按顶层取；`ratioNote` 反过来（词典在顶层、视图按 `stats.` 取）⇒ 3 个裸键。
2. **en-US「值 === 键名」**：`band.preliminary = 'preliminary'`、
   `band.forced = 'forced'` 被判未翻译 ⇒ 改成 `'preliminary band'` / `'forced band'`。
3. **★ 新增：中文文案被 shell heredoc 损坏**。
   `estimateNote` 里出现 **2 个 U+FFFD**（「没给␦␦」）。
   ⇒ 我此前的 U+FFFD 扫描**只查了文档文件**，没查 i18n。
   ⇒ 从本批起，U+FFFD 扫描的**范围**扩到 `src/i18n/*.ts`。

#### 11.105.5 验证

- 用例 **45 条**（`CompressionView.spec.ts`）
- 变异 `/tmp/mut-co69.mjs` **29 条，29/29 有牙、零可疑**，
  `RESTORED=OK`（视图与 `appNav.ts` 逐字节一致）
- 三门 rc=0（css-media 91 文件 / touch-target **90 个 .vue** / i18n 2592 键一致）
- `vue-tsc` rc=0；`npm run build` rc=0
- 全量 **3584 条（133 文件）** rc=0；十连跑 10/10

文档 §11.105 纯追加。

---

### 11.106 usage 增强三条接 API 层（第七十批）

**范围**：`web-mobile/src/api/usageEnhanced.ts` + `.test.ts`（新建）
**三条端点都挂在 `h.admin(h.HandleUsageAdmin)`（`admin/handler.go:1267`）这个前缀子路由上**
（分派见 `admin/usage.go:49-68`）⇒ tenant_admin 可用。

| 端点 | handler | 缺省时间窗 |
|---|---|---|
| `GET /api/admin/usage/cost-trend` | `usage_enhanced.go:128` | **7 天** |
| `GET /api/admin/usage/period-compare` | `usage_enhanced.go:347` | **不接受时间窗** |
| `GET /api/admin/usage/cache-economics` | `usage_enhanced.go:616` | **30 天** |

同族 `trend-series` / `trend-models` 也是 admin 档（`admin/usage_trend_series.go`），
**留下一批**（该文件 615 行，另起一批更划算）。

**权限档位取证**：`format-anomaly*`（`handler.go:913-915`）、`report-rollup/`（`:1067`）、
`storage/config`（`:1108`）都是 **`h.superAdmin`** ⇒ 将来接时抽屉席必须设
`requiresRole: 'super_admin'`；`storage/migration-state`（`:1111`）、
`ops/overview`（`:1070`）、`connection-registry`（`:1024-1025`）是 `admin(...)`。

#### 11.106.1 ★★★★★ 本族最要紧的五件事

**(1) ★★★★★ `degraded` 是恒发字段（不带 omitempty），`degraded_reason` 才是条件键**

```go
// usage_enhanced.go:51-52 / :334-336 / :612-613（三处逐字相同）
Degraded       bool   `json:"degraded"`                 // 恒发
DegradedReason string `json:"degraded_reason,omitempty"` // 仅降级时下发
```

注释写明理由：字段缺失与 `false` 在 API 语义上无法区分，
**那正是这个字段要消灭的歧义** ⇒ 客户端可以断言「服务端确认过它是好的」。

> ★ **本仓少见的「故意不省略」写法**，与 compression / data-lifecycle 族的
> omitempty 条件键**正好相反** ⇒ **不能照抄别族的「键缺失即异常」判据**。

降级时返回的是 **200 + 全 0**（`:215-228` / `:387-393` / `:703-709`）。
注释记了实测：2026-09 实际花费 1139.62 美元，period-compare 却显示 0 ——
**用户看到的是「本月没花钱」**。⇒ UI 必须先读 `degraded`。

**(2) ★★★★★ `cache-economics` 的四个「节省」数字全是按硬编码假设推算的**

```go
avgPricePerToken = dollarsSpent / (cacheReadTokens + promptTokens) // :729 把「已花的钱」当单价
dollarsSaved     = cacheReadTokens * avgPricePerToken * 0.9         // :733 ← 假设缓存价是 10%
compressionSaved = compressedRequests * 8000 * avgPricePerToken     // :739 ← 假设每次省 8000 token
totalSaved       = dollarsSaved + compressionSaved                  // :743
```

⇒ `dollars_saved` / `compression_saved` / `total_saved` / `savings_rate`
**不是实测账单**，是「三条写死的假设」的推论 ⇒ UI 必须标「估算」。

**(3) ★★★★ `compressed_requests === 0` 分不清「没压缩」与「查询静默失败」**

压缩计数是**附加信息**，主聚合成功后才取，失败只记日志（`:694-697`）
⇒ 它是 0 时**分不清**两种情况 ⇒ 连锁着 `compression_saved` / `total_saved` /
`savings_rate` 一起不可信。导出 `compressedCountMayBeFailed()` /
`compressionSavedUnreliable()` 把这条连锁显式化。

**(4) ★★★★ `group_by` 决定读哪张表**（`planCostTrend`，`:91-126`）

| 维度 | 基表 |
|---|---|
| `model` / `provider` / `api_key` | `usage_ledger_with_current_month ul` |
| `work_type` / `intent` | `request_logs_with_current_month rl` |

⇒ 换 `group_by` 就**换基表**。两表 cost 口径经注释核对一致（`:660`），
但那是某一天的一次实测，**不是契约保证** ⇒ 导出 `REQUEST_SIDE_GROUP_BYS` /
`costTrendSwitchesBaseTable()` 供 UI 标注。
另：`group_by` 非法 ⇒ **400**（`:142`），**不是**静默回落；而缺省是 `model`。

**(5) ★★★ 三条端点的时间窗缺省各不相同，参数名也与别族不同**

- 参数是 **`start` / `end`**（不是 `from`/`to`），**必须同时给**，
  只给一个 ⇒ **400**（`usage.go:1448-1450`）；格式 `YYYY-MM-DD`，错 ⇒ 400；
  `end < start` ⇒ 400。
- `days` 是 **clamp [1,366]**（`:1439-1444`），且走 days 口径时起点被
  `.Truncate(24*time.Hour)` **对齐到 UTC 零点**（`:1445`）⇒ 不是「此刻往前 N 天」。
- `period-compare` **不接受时间窗**，只收 `current` / `previous` 两个
  `YYYY-MM`（`time.Parse("2006-01", …)`），**两个都必填**、缺任一 ⇒ 400（`:357-360`）。

#### 11.106.2 ★ 其余已确认的契约

- `entries` **不是全集**：占比 <2% 且已有 10 条的条目被合并进 `other`（`:262-267`）
  ⇒ `other_cost` / `other_count` 给出被合并的量。
- `total_cost` 是**合并前**所有分组之和（`:259`）⇒ 应等于 `Σ entries + other_cost`。
- `dimension_value` 来自 `COALESCE(…, 'unknown')`（`:165`）⇒ 分组值缺失归到 `'unknown'`。
- **`percentage` 是 0-100**（`:205`），而 **`error_rate` 是 0-1**（`:187`）⇒ 同一响应两种单位。
- `trend` 阈值 **±5%**（`:426-430`），`significant` 阈值 **±20%**（`:433-435`）
  ⇒ **可能「up 但不 significant」** ⇒ 导出 `trendWithoutSignificance()`。
- 上期成本为 0 ⇒ `change_pct` 留 **0**（`:421-423`），不是无穷大也不是 null。
- `by_dimension` 只在 `len(modelChanges) > 0` 时才放 `"model"` 键（`:447-449`）
  ⇒ 查询失败时是 `{}`，与「查了但无变化」**同形**（R68 注释自陈，只留 `slog.Warn`）。
- 维度明细 SQL 硬编码 **`LIMIT 10`**（`:563`）。
- `effective_cost_ratio` 分母为 0 时留 **1.0**（`:746` 初始化），不是 0
  ⇒ 「成本占比 100%」是假的 ⇒ 导出 `effectiveCostRatioIsFakeFull()`。
- `cache_hit_ratio` 分母为 0 时留 0（`:719-722`）。
- `PeriodStats.unique_sessions` 已于 2026-10-03 **删除**，注释记了三条理由，
  核心是：「一个无消费者的指标算不出来时，返回 0 与『真的是 0』在报告上无法区分」
  ⇒ 客户端**不要**去读这个键。
- 租户口径用的是 `EffectiveTenantIDAll`（`context.go:69-74`）——
  本仓**第三种**租户过滤（见 §11.108 的三口径对照）。

#### 11.106.3 ★ 本批最大的一处：自己写出了**冗余判据**

第一轮变异 **28/32**，4 条 `STILL_GREEN` 里最要紧的一条是：

```ts
// 两条检查同时存在：
requireKeys(d, [..., 'degraded'], '成本趋势')     // 存在性
if (typeof d.degraded !== 'boolean') throw ...   // 类型
```

**后一条完全覆盖前一条**（`typeof undefined !== 'boolean'` 也会抛）
⇒ 把 `'degraded'` 从必检键里删掉，用例照样全绿
⇒ 这是我自己写的**恒真判据**（无用复杂度）。

修法：把 `degraded` 从三个必检键数组里**移出**，只由类型校验单独把关，
并把这条冗余写进代码注释（免得下一个维护的人又把它加回去）。

> ★ **判据**：两条检查若**一条蕴含另一条**，那条被蕴含的就是恒真。
> 与 §11.103 的「判据两条路径在夹具上等价」同源 ——
> 都在问「**两个版本在所有输入上等价吗**」。

另有 3 条真缺用例（补上）：
`by_dimension` 不是对象、period-compare 缺参不静默填默认值、
period-compare 的 `degraded` 键缺失/类型错。

#### 11.106.4 验证

- 用例 **59 条**（`usageEnhanced.test.ts`）
- 变异 `/tmp/mut-co70.mjs` **32 条，32/32 有牙、零可疑**，`RESTORED=OK`（逐字节一致）
- 三门 rc=0；`vue-tsc` rc=0；`npm run build` rc=0
- 全量 **3643 条（134 文件）** rc=0；十连跑 10/10
- U+FFFD 自查：源与用例均 0（按 §11.105 新增的规则，扫描范围含本批新写文件）

文档 §11.106 纯追加。


## §11.107 usage 趋势线两条接 API 层（第七十一批）

**产物**：`web-mobile/src/api/usageTrendSeries.ts`（新建）+ `.test.ts`（新建，158 用例）

### 三路取证（写代码前）

| 问 | 答 | 证据 |
|---|---|---|
| 后端注册存在吗 | 是 | `admin/handler.go:1267` `h.admin(h.HandleUsageAdmin)`；分派 `admin/usage.go:60-64` 两个 case |
| 谁在提供 | **本进程**，不经 maintain 反代 | 不在 `cmd/gateway/maintain_proxy.go:37` 的 `maintainCompatPrefixes` 里 |
| 前端有调用方吗 | 桌面有、移动端无 | `web/src/api/usage.ts:508/512`；`BoardUsageTrendSection.vue`、`UsageTrendExplorer.vue`；移动端只有 `usageEnhanced.ts:12` 一句「留待下一批」 |

**权限档位**：`h.admin(...)` ⇒ **tenant_admin 可用** ⇒ 将来接抽屉席**不设** `requiresRole`。

### 本族挖到的后端缺陷 / 契约（★ 越高越要紧）

1. **★★★★★ `degraded` 又是恒发字段（不带 `omitempty`）——第二次遇到。**
   `usage_trend_series.go:75-80` / `:96-101` 两个响应都是：
   ```go
   // 恒发（无 omitempty）：空序列与「真的没有用量」在图上同形。
   Degraded       bool   `json:"degraded"`                 // 恒发
   DegradedReason string `json:"degraded_reason,omitempty"` // 仅降级
   ```
   `dashboard_degrade.go:105` 的注释写明「与 `PeriodCompareResponse.Degraded` 同理」
   ⇒ **这是本仓既定风格，不是偶然**（第七十批 usageEnhanced 同款）。
   压缩统计（第六十八批）的 `*int64`+omitempty 条件键恰好相反
   ⇒ **不能照抄别族的「键缺失即异常」判据**。

2. **★★★★★ 降级时 `top` 与 `bucket_minutes` 仍是解析后的值，不是零值**（`:204-205`）
   ⇒ 「降级 + 空序列」与「成功 + 零用量」的区分**只能靠 `degraded` 那一个键**。
   判据 `trendSeriesIsGenuinelyEmpty()` 两个分支都写了。

3. **★★★★★ `days` 的 clamp 是 90，不是 366。**
   本族走 `boardTimeRangeFromRequest`（`board_time_range.go:17-38`）→ `boardDays`
   （`dashboard_board.go:204-213`，clamp **[1,90]**），**不是** `resolveUsageTimeRange` 的 366。
   起点 `boardPresetTimeRange`（`board_time_range.go:120`）是
   `todayStart.Add(-(days-1)*24h)` ⇒ **减 days-1 天**，days=7 起点是**六天前**零点。
   ⚠️ `resolveUsageTimeRange` 的 days 分支减 `days` 天（`usage.go:1447`）——**两套语义差一天**，
   但那条分支在本族**不可达**（只在 `start`/`end` 至少给一个时被调用，而它内部 days 分支
   的前提正是两者都缺省）⇒ 死代码，仅作对照。

4. **★★★★★ 自定义区间左闭右开，响应 `end` 是「次日零点」。**
   `usage.go:1467-1469`：`return startDay.UTC(), endDay.Add(24*time.Hour).UTC()`
   ⇒ 用户选 2026-01-01 ~ 2026-01-07，响应 `end` 是 `2026-01-08T00:00:00Z`。
   对照 days 预设路径的 `End = now`（带时分秒）⇒ **两条路径 `end` 形态不同**，
   `trendEndIsNextMidnight()` 可用来判断后端走了哪条。

5. **★★★★ 分桶两套规则，同一个实际跨度可能是两个档位。**
   `trendBucketMinutes`（`board_time_range.go:47-67`）：
   - 预设档按 `Days`：`<=1` → 5；`<=7` → 15；否则 60
   - 自定义档按实际 `span`：`<=48h` → 5；`<=14d` → 15；否则 60
   ⇒ `start`/`end` 恰好 24 小时跨度 ⇒ **5 分钟**；days=2 预设 ⇒ **15 分钟**。

6. **★★★★ 非法 / 负数 ID 静默换数据档，不报 400。**
   `queryInt`（`handler.go:1539-1542`）解析失败**回落默认值**；`usageTrendSource`
   （`:155-164`）判的是 `f.providerID > 0` / `f.apiKeyID > 0`
   ⇒ `provider_id=abc` 与 `provider_id=-5` 都变成 0 ⇒ 落到 **default 档（dim）**。
   用户以为加了过滤，实际拿到的是**另一张表**的数据。
   ⇒ 客户端只发正整数 ID（`trendQuery` 里 `Math.trunc(x) > 0` 才发），
   并提供 `trendFilterIdInvalid()` 在发请求前拦住。

7. **★★★★ `source` 少 `_without_customer_id` 后缀。**
   `:158` 对外返回 `"request_logs_with_current_month"`，真实读的是
   `request_logs_with_current_month_without_customer_id`（`:407`/`:588`）
   ⇒ **不能把 `source` 当真实表名用**，`trendSourceRealTable()` 做映射。

8. **★★★ 三个不同的上限，后两个都不回显。**
   - `top`：默认 8、clamp **[1,20]**（`:129`/`:144-149`）⇒ **响应回显 clamp 后的值**，
     降级时也回显 ⇒ UI 可以照着显示「已按 N 展示」
   - `model` 多选：空值/重复剔除，超过 **20** 截断（`:133-143`）
   - `trend-models` 的 SQL **硬编码 `LIMIT 100`**（`:524`/`:554`/`:592`）
     ⇒ 超 100 个模型**静默截断，响应里没有任何字段说明被截断了**

9. **★★★ 折叠只在「没指定 model」且「模型数 > top」时发生。**
   `foldUsageTrendRows:439` 的 `modelFiltered || len(rows)==0` 直接透传 ⇒ **指定 model 一律不折叠**；
   `:455-457` 的 `len(ranked) <= top` 也透传 ⇒ **恰好 top 条不折叠**
   ⇒ 判据必须用 **`>`** 而不是 `>=`。

10. **★★★ `__others__` 固定排最后**（`:221-229`），其余按 `TotalRequests` 降序。
    ★★ **`sort.SliceStable` 的「同请求数保持原序」客户端验证不了** ——
    后端原序来自 SQL `ORDER BY 2, 1`（`:334`），响应里没有可比对的参照物。
    ⇒ 我第一版写了 `trendTiesKeepOriginalOrder()` 去验这件事，**变异 #47 实测它恒真**
    （在已排好序的序列上「相邻不递减」本来就是排序的定义）
    ⇒ 重做成可验证的那一半：`trendSeriesNonIncreasing()`，并配了乱序负控。

11. **★★ `degraded_reason` 与 `missing_view` 是同一个值**（`:209-210`、`:283-284`）⇒ 两键恒等。
12. **★★ `hint` 可本地推导**（`dashboard_degrade.go:83-88`）⇒ 客户端自己拼（可本地化），
    后端那份只当契约校验对象（`trendHintDisagrees()`）。
13. **★★ detail 档的 `request_status` 是三态白名单**
    `IN ('success','failure','rate_limited')`（`:383`/`:565`）⇒ 不是只看成功。
    对照压缩统计的 `($3 OR rl.success)` 又是另一种口径。
14. **★ 租户：`statsTenantScope`（`stats.go:87-97`）** —— 角色不是 `super_admin`/`admin_key`
    就用 `auth.TenantID` 并**忽略 `tenant_id` 参数** ⇒ tenant_admin 天然隔离。
    ★ 与 data-lifecycle 的 `metrics` 端点（**不隔离但注册是 admin 档**）**恰好相反**
    ⇒ 本族是本仓**第四种**租户口径。
15. **★ 超时 45 秒**（`:182`/`:260`），detail 档长窗可能超时
    （文件头注释：前端对超时给出「缩短时间范围」提示）。

### 桌面对照：两处**不要抄**

- `web/src/api/usage.ts:456`/`:474` 把 `degraded` 声明成 `degraded?: boolean`
  ⇒ 可选，暗示可能缺键，但后端恒发 ⇒ 桌面「缺键即降级」恒假
- `BoardUsageTrendSection.vue:71` `resp.bucket_minutes || …` /
  `UsageTrendExplorer.vue:167` `resp.bucket_minutes || 60`
  ⇒ 后端恒发 int ⇒ **兜底恒不生效**，且 60 不是唯一合法档位（还有 5/15）

### 顺带记录：后端文件头注释的格式损坏

`usage_trend_series.go:21-23`：
```
// 唯一含 api_key_id 的读面；无 api_key 索引的旧分区上长窗会慢，前端对超时
//
//	给出「缩短时间范围」提示。
```
中间断了一行、`给出` 那行被 tab 缩进 ⇒ Go doc 把它当代码块渲染。不影响行为，但会误导读者。

### 验证

- 用例 **158 条全绿**
- 变异 `/tmp/mut-co71.mjs` **54 条，54/54 有牙、零可疑**，`RESTORED=OK`（逐字节一致）
- 三门 rc=0；`vue-tsc --noEmit -p tsconfig.app.json` rc=0；`npm run build` rc=0
- 全量 **3801 条（135 文件）** rc=0；十连跑 10/10
- U+FFFD 自查：源、用例、两侧 i18n 均 0

### 变异验证暴露的判据缺陷（真缺陷，已修）

1. **锚点指错 2 条**（#43 / #54）：变异实际打红的是**相邻**用例，
   而 `expect` 写在了「看起来最相关」的那条标题上 ⇒ 报成 STILL_GREEN。
   连续第五批踩这个坑（第六十六~六十八、六十九、七十、本批）。
2. **自己写出了恒真守卫 1 条**（#47 `trendTiesKeepOriginalOrder`）：
   它承诺了一件客户端**无法验证**的事（后端 SliceStable 的原序），
   在任何已排好序的载荷上都返回 true ⇒ 重做成可验证的 `trendSeriesNonIncreasing` + 乱序负控。
3. **反向检测顺序写反**（首次跑就红 2 条）：`requireKeys` 先于「拿到对方载荷形状」检查，
   报的是「缺 3 个键」而掩盖了真正的原因 ⇒ 改成**反向检测先于缺键检查**。
4. **夹具数错 1 条**：写「25 个位置 21 个不同值」实际写了 23 个位置 13 个不同值。

### 本批新增的类型门教训

`noUncheckedIndexedAccess` 下 **`arr[i]` 一律是 `T | undefined`**：
- 源文件里 `r.series[i].model` 直接编译不过 ⇒ 改用 `findIndex` / `slice(1).every` 这类
  **不产生索引访问**的写法，而不是靠 `!` 糊过去
- 测试文件里 4 处 `reqMock.mock.calls[0][1]` 与 6 处 `(d.series as …)[0]` 必须显式 `!`
- 一轮 `vue-tsc` 只清掉当轮暴露的那批 ⇒ **要跑到 rc=0 为止**，不能看到少了就停


### 顺带修掉的长期 flaky：十连跑第一次 run#8 失败（**首次具名**）

第七十批之前就有「历史无污染跑 ≥180 次里失败 1 次（≈0.5%）」的记录，一直没具名。
第七十一批十连跑第一次就撞上并落盘了快照 —— **具名两条，同一形态**：

```
FAIL src/views/ComplianceHitsView.spec.ts > 筛选与分页 > ★★★ records 有 total ⇒ 分页信息是精确的
FAIL src/views/RoutingOptView.spec.ts   > 入口与窗口控件 > ★ 填精确匹配条件后提交 ⇒ 带 taskType/provider 重查
TypeError: Cannot read properties of undefined (reading '0')
  ❯ ComplianceHitsView.spec.ts:365  expect(recMock.mock.calls[1]![0]).toMatchObject({ offset: 50 })
  ❯ RoutingOptView.spec.ts:216     expect(metricsMock.mock.calls[1]![0]).toEqual({...})
```

即：**单次 `await flushPromises()` 之后 `mock.calls[1]` 仍是 undefined**。

**处置**：两个 spec 里**所有**索引 ≥1 的 `mock.calls[N]` 断言（共 8 处）前面
插入显式等待，把「靠时序巧合」换成「等到调用发生」：

```ts
await vi.waitFor(() => {
  expect(recMock.mock.calls.length).toBeGreaterThanOrEqual(2)
})
expect(recMock.mock.calls[1]![0]).toMatchObject({ offset: 50 })
```

`vi.waitFor` 默认超时 1000ms ⇒ **第二次调用真的不来时仍然失败**，
且报错带最后一次断言的详情（比裸 `TypeError` 可读）⇒ **不会把真 bug 藏起来**。

**★ 根因未完全定位，如实记录**：

1. 我先猜「第二次请求的 promise resolve 得慢」。**受控复现失败**，而且顺带证伪了这个假设：
   **`mock.calls` 记录的是调用时刻，不是 resolve 时刻** ⇒ 慢 resolve 根本不会让
   `calls[1]` 变 undefined（第一次注入 `setTimeout(0)` 时全绿，正是因为
   `flushPromises` 内部用 `setImmediate`/宏任务，一轮就把单层 timer 抓到了；
   改成三层嵌套 timer 仍全绿；最后加到 20ms 也全绿）。
2. 视图侧排除项：`ComplianceHitsView.vue` 与 `RoutingOptView.vue` 里
   **都没有** `debounce` / `setTimeout` / `watch` ⇒ 「防抖晚到」这条假设也没有代码依据。
3. 修前版本在 4 并发 × 6 轮（共 24 次）下也**没复现** ⇒ 触发条件更接近
   run#8 那种整体高负载（该轮 `environment` 累计 120.51s，明显高于其他轮次）。

⇒ 所以准确的说法是：**脆弱的时序断言已改成显式等待**，而不是「已定位并修复根因」。
若将来再次偶发，快照会比原来更容易读（`vi.waitFor` 的超时信息带完整调用轨迹）。

**本轮实验沉淀的判据纪律**：
设计「复现 flaky」的注入实验前，先问**判据的输入变量是不是你以为的那个**。
我连续两次把变量选错（选 resolve 时机、选防抖），两次都被「全绿」证伪；
真正该选的变量（**发起时刻**）当时没找到可注入点。
**「全绿」在复现实验里同样是结果，要读，不要当成实验没跑。**


## §11.108 流式连接注册台两条接 API 层（第七十二批）

**产物**：`web-mobile/src/api/connectionRegistry.ts`（新建）+ `.test.ts`（新建，76 用例）

### 三路取证（写代码前）

| 问 | 答 | 证据 |
|---|---|---|
| 后端注册存在吗 | 是 | `admin/handler.go:1024-1025`，两个都是 `admin(...)` |
| 谁在提供 | **本进程** | 不在 `cmd/gateway/maintain_proxy.go:37` 的 `maintainCompatPrefixes` 里 |
| 前端有调用方吗 | 桌面有、移动端无 | `web/src/api/connection-registry.ts:30/36` |

**权限档位**：后端 `admin(...)` ⇒ **tenant_admin 可用** ⇒ 将来接抽屉席**不设** `requiresRole`。
⚠️ 但桌面 `web/src/router.ts:284` 与 `web/src/config/appNav.ts:195` 把这条路由
标成了 `requiresSuper: true` / `super: true` ⇒ **前端比后端严**。
按纪律以可执行的注册为准，**不按前端标记判权限**。

### ★ 同批排除掉的两个候选

- **`ops/overview` 不做**：后端 `handler.go:1070` 确实注册了，但桌面
  `web/src/router.ts:309` 是 `externalMaintainRedirect('/ops', '/maintain/ops/overview')`，
  `web/src/config/edition.ts:112` 也标了 `external: true`
  ⇒ **桌面的真实入口在 maintain 服务**，本进程这个注册是死路径。
  这正是「要确认**是谁在提供**」那条纪律拦下来的。
- **`storage/migration-state` 暂不做**：`getStorageMigrationState`
  （`admin/storage_migration.go:403-421`）只有 18 行，载荷恒为
  `{running: nil, latest: nil}` 二选一 ⇒ 信息量太薄，不值当单独一批。
  （它本身有个可测契约：两个键**恒在**、值恒为 `null` 或同一个 run 对象、**互斥**。）

### 本族挖到的后端缺陷 / 契约（★ 越高越要紧）

1. **★★★★★ `live` 恒数组，`closed` 恒「`null` 或非空数组」，绝不会是 `[]`。**
   ```go
   // List()：domains/streaming/connection_registry.go:473
   out := make([]ConnectionSnapshot, 0, len(entries))   // ⇒ 恒非 nil ⇒ JSON 恒 []

   // ClosedHistory()：:483-493
   if n <= 0 || len(r.closed) == 0 {
       return nil                                       // ⇒ JSON 是 null，不是 []
   }
   if n > len(r.closed) { n = len(r.closed) }
   out := make([]ConnectionSnapshot, n)                 // n ≥ 1 ⇒ 恒非空数组
   ```
   ⇒ **同一份载荷里两个数组键的 nil 编码相反**，且 `closed: []` **后端永不产生**
   ⇒ 客户端收到空数组就说明契约漂了。
   这是本仓**第五种** nil 编码（已见：恒数组 / 键缺失 / 裸 null / omitempty 条件键 /
   恒发布尔），且**同载荷内两个数组键编码相反**是首次。

2. **★★★★★ 4 个 omitempty 条件键 + 1 个恒发布尔 —— 与前两批恰好相反。**
   `domains/streaming/connection_registry.go:151-163`：
   ```go
   RequestID     string    `json:"request_id"`              // 恒在
   Protocol      string    `json:"protocol,omitempty"`      // ★ 条件键
   ClientType    string    `json:"client_type,omitempty"`   // ★ 条件键
   TenantID      string    `json:"tenant_id,omitempty"`     // ★ 条件键
   RegisteredAt  time.Time `json:"registered_at"`           // 恒在
   LastFrameAt   time.Time `json:"last_frame_at"`           // 恒在
   FramesWritten uint64    `json:"frames_written"`          // 恒在
   BytesWritten  uint64    `json:"bytes_written"`           // 恒在
   CloseReason   string    `json:"close_reason,omitempty"`  // ★ 条件键
   Closed        bool      `json:"closed"`                  // ★ 恒发（无 omitempty）
   ```
   ⇒ 第七十/七十一批那两条端点的 `degraded` 是**恒发**，本族这 4 个是**条件键**
   ⇒ **判据不能跨族照抄**：前者缺键才异常，本族缺键才正常。
   ⇒ `closed` 恒发布尔 ⇒ 「是否已注销」这一条**不是恒真判据**（真能区分 live/closed）。

3. **★★★★★ 注释与实现矛盾：`SetConnectionRegistry(nil)` 不能解绑。**
   `admin/connection_registry.go:32-37`：
   ```go
   // SetConnectionRegistry wires ... Pass nil to disable (endpoints return 503).
   func SetConnectionRegistry(reg *streaming.ConnectionRegistry) {
       if reg == nil {
           return          // ← 注释说「传 nil 可禁用」，代码是「传 nil 什么也不做」
       }
       connectionRegistry.Store(reg)
   }
   ```
   ⇒ 装配后**没有任何 API 能把端点退回 503** ⇒ 注释是错的，以实现为准。
   （同族参照：第六十六批 `data_lifecycle_metrics.go` 的 `LastCleanupAt` 也是注释有、实现无。）

4. **★★★★ `Lookup` 只查活跃表，已注销的取不到。**
   `domains/streaming/connection_registry.go:454-465` 只看 `r.entries`，不看 `r.closed`
   ⇒ **`closed` 列表里的条目用详情端点必然 404**
   ⇒ 「列表里看得到、点进去 404」是**契约行为**，不是 bug。

5. **★★★★ Go 零值时间会真的出现。**
   `LastFrameAt` 是 `time.Time` 且无 omitempty ⇒ 从未写过帧的连接
   `last_frame_at` 是 **`"0001-01-01T00:00:00Z"`**（`time.Time{}.Format(RFC3339)`）
   ⇒ 不能把「键存在」当「有值」，也不能渲染成「1970 年」或异常。
   ⇒ 自查判据 `snapshotFrameFieldsAgree()`：`frames_written === 0` **应当**配零值时间。

6. **★★★ `live` 的顺序没有保证。**
   `List()` 的注释自陈「map walk is unordered, so callers sort as needed」（:467-471）
   ⇒ 不能靠顺序判稳定，也不能靠它做 diff ⇒ 客户端要自己按 `registered_at` 排。

7. **★★★ 上限 50 不回显。** `connection_registry.go:58` 写死 `reg.ClosedHistory(50)`
   ⇒ 注销历史最多 50 条，响应里**没有任何字段**说明被截断了。

8. **★★ 503 与「没数据」是两种不同的失败**：未装配返 **503 `connection registry not wired`**，
   不是空列表 ⇒ 「空列表」永远只表示「装配了但当前没有连接」。

9. **★ `live_count` 是可自查的冗余字段**：`:56` 的 `len(live)` 与 `live.length` 恒等
   ⇒ 不等就说明载荷被换过/被代理改过，不是「后端口径变了」。

### 桌面对照：三处**不要抄**

- `web/src/api/connection-registry.ts:12-23` 把 `registered_at` / `last_frame_at` /
  `frames_written` / `bytes_written` / `closed` 五个**恒在**键标成了可选（`?:`）
- 同文件 `:28` 把 `closed: ConnectionSnapshot[]` 标成**必定是数组**
  ⇒ 而后端无历史时给的是 **`null`** ⇒ 按那个类型直接 `.map()`/`.length`
  会在「从无注销记录」时抛 `Cannot read properties of null`
- `web/src/router.ts:284` 的 `requiresSuper: true` 比后端 `admin(...)` 严

### 验证

- 用例 **76 条全绿**
- 变异 `/tmp/mut-co72.mjs` **42 条，42/42 有牙、零可疑**，`RESTORED=OK`（逐字节一致）
- 三门 rc=0；`vue-tsc --noEmit -p tsconfig.app.json` rc=0；`npm run build` rc=0
- 全量 **3877 条（136 文件）** rc=0；十连跑 10/10
- U+FFFD 自查：源、用例、两侧 i18n 均 0

### 变异验证暴露的判据缺陷（真缺陷，已修）

1. **★★ `.toThrow(/live/)` 这类过宽正则会匹配上 TypeError。**
   第一轮 42 条里 8 条 STILL_GREEN，其中 2 条的根因是**判据本身无牙**：
   ```ts
   expect(() => unwrap(listWith({ live: {} }))).toThrow(/live/)   // ✗
   ```
   变异把 `!Array.isArray(d.live)` 换成 `d.live === null` 后，解包器不再拦 `live: {}`，
   于是执行到 `d.live.forEach(...)` 抛 **`d.live.forEach is not a function`** ——
   **这条 TypeError 消息里含 "live" ⇒ 宽松正则照样匹配 ⇒ 用例绿。**
   ⇒ 判据无牙的形态不是「断言太弱」，而是「**断言太宽，恰好被下游的意外异常兜住了**」。
   ⇒ 修法：收紧到**自己写的错误文案**（`/live 不是数组/`、`/live\[0\] 不是对象/`），
     而不是「载荷里那个字段名」。
   ★ 同族教训：`toThrow(/字段名/)` 这类写法在本仓已被证伪两次
     （本批 2 次 + `d.closed.forEach` 1 次）。

2. **★★ 自己写出了**被蕴含的冗余判据**：解包器里
   `if (d.live === null || !Array.isArray(d.live))` 里的 `=== null` 分支是**恒被蕴含**的
   —— `Array.isArray(null)` 本身就是 `false` ⇒ 删掉它用例照样全绿。
   ⇒ 已删（并把理由写进代码注释），记为**可证等价变异**。

3. **锚点指错 2 条**（#12 / #20）：变异实际打红的是**相邻**用例
   （「closed 为 null 时通过」/「六个恒在键一起缺失时报缺六」），而 `expect` 写在了
   「看起来最相关」的那条标题上。连续第六批踩这个坑。

4. **`from` 片段不唯一 1 条**（#22）：`registryRowIsNotFetchableById` 的函数体与
   `snapshotIsClosed` **逐字相同**（都是 `return s.closed === true`）
   ⇒ `String.replace` 只替换第一处 ⇒ 变异打在了**错误的函数**上。
   ⇒ 变异脚本里凡是函数体只有一两行的，`from` 必须带**函数签名**做唯一锚点。

5. **两条我选错了等价变异**（#16 / #35）：`length === 0` 改成 `length < 1`（对非负整数等价）、
   `status >= 500`（对夹具里的 200/404 仍为假）⇒ 变异本身无效，不是判据问题。
   ⇒ 与第六十九批「注入标记 ≠ 变异」同族：**注入必须真的改变被观察行为**，
     且要拿夹具里的**实际取值**去验「变了吗」。


## §11.109 节点恢复时间线接 API 层（第七十三批）

**产物**：`web-mobile/src/api/nodeHealthTimeline.ts`（新建）+ `.test.ts`（新建，84 用例）

### 三路取证

| 问 | 答 | 证据 |
|---|---|---|
| 后端注册存在吗 | 是 | `admin/handler.go:1028`，`admin(...)` |
| 谁在提供 | **本进程** | 不在 `maintainCompatPrefixes` 里 |
| 前端有调用方吗 | 桌面有、移动端无 | `web/src/api/node-health.ts:38`、`NodeHealthTimelineView.vue` |

**权限档位**：后端 `admin(...)` ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
⚠️ 桌面 `web/src/router.ts:285` 又标了 `requiresSuper: true` ——
**连续第二批**遇到「前端比后端严」。

### ★★★ 本族最大的问题：这条端点没有租户过滤

`admin/node_health.go:183-184`：
```sql
WHERE credential_id = $1 AND started_at >= $2
```
**只有两个条件，没有 `tenant_id`** ⇒ 而注册是 `admin(...)` 档
⇒ ★★ **tenant_admin 能查任意 credential_id 的完整探测时间线**，
包括 `reason_code`（错误码）与 `note`（模型名 · 触发来源）。
⇒ 与第六十六批 data-lifecycle 的 `metrics` 同型（「不隔离 + admin 档」），
   本仓已出现**两次**。
⇒ 移动端接入时不要把它放在 tenant_admin 可见的位置而不加说明。

### 其它挖到的契约

1. **★★★★★ `observation_status` 是硬编码 `"complete"`，没有任何分支能产生别的值。**
   `admin/node_health.go:173`，而查询失败走的是 **503**（`:165-167`），
   不是「status: partial + 空 events」⇒ **「部分观测」在本端点上不可表达**。
   ⇒ 解包器**只校验它是字符串，不校验取值** —— 校验 `"complete"` 是**恒真判据**。

2. **★★★★★ `event_type` 是三态，且「恢复」有两个值。**
   `admin/node_health.go:82-90`：
   ```go
   eventType := "failed"
   if row.Success {
       eventType = "recovered"
       if row.TriggerKind == "credential_recovery" {
           eventType = "reconnected"      // ← 强制恢复触发的探测
       }
   }
   ```
   ★★ 把「恢复」当单一状态会**漏掉 `reconnected`**
   —— 而 `credential_recovery` 正是**强制恢复**那条路径的触发来源。

3. **★★★★ 三个条件键全是「指针 + omitempty」，填充条件各不相同。**
   | 键 | 什么时候**有**键 |
   |---|---|
   | `duration_ms` | **仅 `> 0`**（`:100-102`）⇒ 0 毫秒 ⇒ **键缺失**，不是 `0` |
   | `reason_code` | 非 nil **且** trim 后非空 **且 ≠ `"none"`**（`:105-107`） |
   | `note` | 模型名 / 触发来源至少一个非空（`:108-109` + `formatProbeNote :126-140`） |
   ★★ `"none"` 是**哨兵**：`firstNonEmptyPtr` 选中它、紧接着的过滤又丢掉它
     ⇒ **客户端永远看不到 `"none"`**，键缺失就是「无原因码」。
   ★ 这是本仓**第六种**缺键编码（已见：恒数组 / 键缺失 / 裸 null /
     omitempty 条件键 / 恒发布尔 / **指针+omitempty**）。

4. **★★★★ 查询失败是 503 不是 500**（`:165-167`），
   且与「数据库没配」（`:158`）**同为 503**，只能靠 message 区分。

5. **★★★★ `since` 按后缀分派两套语法，超上限静默 clamp 到 7 天。**
   `parseTimelineSince`（`:52-79`）：`Nd` 走 `Atoi`，其余走 `time.ParseDuration`
   （Go 的 ParseDuration **不认 `d`** ⇒ 两套语法不打架）；
   两者**都** clamp 到 7 天、**都**不报错；缺省是 **24h**。
   ⇒ 客户端只对 `^[+-]?\d+d$` 做 clamp，**不重新实现** `ParseDuration`
     （那套语法可小数可多段，复刻容易把合法值变成 400）。

6. **★★★ 排序键与展示键不是同一个字段。**
   SQL `ORDER BY started_at DESC LIMIT 200`（`:186-187`），
   展示用的 `occurred_at` 优先取 `CompletedAt`、回落 `StartedAt`（`:91-94`）
   ⇒ **跨完成的探测会与「开始时间」的排序不一致**；
   并且过滤也按 `started_at >= $2` ⇒ **`since` 过滤的是开始时间**。

7. **★★★ 上限 200 不回显，被丢的是最早的**（`:21` + `:187` + `:211-213` 的反转）。

8. **★★ 响应里的 `credential_id` 是字符串**（`strconv.FormatInt`，`:172`），
   且 `credential_id <= 0` 判 400（`:146-151`）；
   `events` 恒为数组（`make(...,0,32)` + `:169-171` 兜底）；
   `occurred_at` 是 RFC3339**Nano**。

### 验证

- 用例 **84 条全绿**
- 变异 `/tmp/mut-co73.mjs` **46 条，46/46 有牙、零可疑**，`RESTORED=OK`
- 三门 rc=0；`vue-tsc` rc=0；`npm run build` rc=0
- 全量 **3961 条（137 文件）** rc=0；十连跑 10/10
- U+FFFD 自查：源、用例、两侧 i18n 均 0

### U+FFFD 扫描当场抓到一处写坏的字

写完 `nodeHealthTimeline.ts` 第一次扫描就抓到 **2 个 U+FFFD**，在第 100 行：
```
 * 而展示用的 `occurred_at` 优先取 `CompletedAt`、回<U+FFFD><U+FFFD> `StartedAt`
                                                     ↑ 「落」字被写坏
```
⇒ 由「回落」修复为「回落」后归零。
★★ 这正是 §11.105 立的那条规则的直接收益：
**扫描范围必须覆盖本批新写的每个文件**（i18n 门**不检查替换字符**，
文档扫描也不会扫到 `.ts`）。中文字符在批量写入时可能被截断成 U+FFFD，
而**肉眼看代码是发现不了的** —— 第 100 行在注释里，不影响任何行为。

### 变异验证暴露的判据缺陷（真缺陷，已修）

1. **★★ 判据正则要区分「同一条端点的不同失败分支」。**
   「缺 `observation_status` 时抛错」原来写 `.toThrow(/observation_status/)`
   ⇒ 变异把 `requireKeys` 删掉后，改由下面的**类型校验**抛
   `observation_status 不是字符串`，**同一个词照样匹配** ⇒ 判据被自己的另一条分支兜住。
   ⇒ 收紧成 `.toThrow(/缺 1 个键/)`。
   ★ 这是第七十二批「正则太宽」那条教训的**同族第二形态**：
     上次是被**下游 TypeError** 兜住，这次是被**自己的另一条错误分支**兜住。

2. **★★ 锚点指错 3 条**（#22 / #39 / #45）：实际转红的是**相邻**用例。
   连续第七批踩这个坑。其中两条是我**新加了夹具却没同步锚点** ——
   加完用例必须回头核对「这条判据对应哪个标题」。

3. **★★ 夹具缺键数与变异的敏感度要匹配。**
   变异 #22 把 `filter(缺键).slice(0, 1)` 注入后**全部用例仍绿**，
   手工实测才确认：**每条「缺某个键」的用例都只缺 1 个键**，
   `slice(0, 1)` 对单元素数组**无影响** ⇒ 对现有夹具是**等价变异**。
   ⇒ 补了一条「**两个**恒在键一起缺失」的夹具才有区分力；
   且特意选了**不做取值校验**的两个键（`event_type` 有取值校验会兜住）。

4. **两条我选错了等价变异**：
   - #11 `days < 1` 守卫删掉后，后续 `days > 7` 判断对 0/负数仍为假 ⇒ 仍返回原值。
   - #44 `credential_id` 是 `number` ⇒ `String(id)` 只有数字
     ⇒ `encodeURIComponent` **恒等于恒等** ⇒ 改成「路径写错」才是真变异。
   ⇒ 与第六十九/七十二批同族：**注入必须真的改变被观察行为**。

5. **一处防御被记为可证等价变异（保留）**：`reason_code` 取值里的 `!== ''` 判断。
   后端 `:105-107` 已经过滤掉空串 ⇒ 客户端这条分支**不可达**
   ⇒ 它是**防御性守卫**（防后端异常下发），按纪律**保留**并记为可证等价。


## §11.110 请求动作时间线接 API 层（第七十四批）

**产物**：`web-mobile/src/api/requestActions.ts`（新建）+ `.test.ts`（新建，71 用例）

### 三路取证

| 问 | 答 | 证据 |
|---|---|---|
| 后端注册存在吗 | 是 | `admin/handler.go:1026`，`admin(...)` |
| 谁在提供 | **本进程** | 不在 `maintainCompatPrefixes` 里 |
| 前端有调用方吗 | 桌面有、移动端无 | `ActionTimeline` 走的是 SSE，本端点是刷新后的 REST 回退 |

**权限档位**：后端 `admin(...)` ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
★ 名字里有 "actions"，但**它本身是只读端点**（只有 GET、只 `LRange` 读 Redis），
与本仓一律不碰的写操作端点不是一回事。

### ★★★★★ 本族最要紧的七件事

1. **★★★★★ `actions[i]` 是一个「键集不可穷举」的开放对象。**
   `admin/live_stream_lifecycle.go:96-125` `flattenActionEvent` 的做法是
   「整体序列化 → 反序列化成 `map` → 把 `detail` 的每个键**摊平到顶层**」：
   ```go
   if detail, ok := m["detail"].(map[string]any); ok {
       delete(m, "detail")
       for k, v := range detail {
           if _, taken := m[k]; !taken {   // ★ 不覆盖已存在的顶层键
               m[k] = v
           }
       }
   }
   ```
   ⇒ 顶层键 = `ActionEvent` 的固定键 ∪ **`detail` 里内容决定的任意键**。
   ⇒ ★★ 这是本仓**第一次**出现「载荷形状开放」的端点
     ⇒ 解包器**只能校验必有的那几个键**，绝不能对全部键做 `requireKeys`。
   ⇒ ★★ 提升时**不覆盖**已有键 ⇒ 若 `detail` 里带了 `action` / `seq` 这类名字，
     **会被静默丢弃**。
   ⇒ 类型声明必须带索引签名 `readonly [key: string]: unknown`，
     否则 TS 会把这些键当不存在的数据丢掉。

2. **★★★★★ `detail` 这个键永远不出现**（`:108` `delete(m, "detail")`）
   ⇒ 客户端按 `{action, detail: {...}}` 去读会拿到 `undefined`。

3. **★★★★★ `action` 是**开放字符串**，不是封闭枚举。**
   `admin/request_actions.go:90-92` 的 `decodeStoredAction` **只**判 `ev.Action == ""`：
   ```go
   if ev.Action == "" { return ev, false }
   ```
   **不校验是否在 `liveactions` 的 Action 常量列表内** ⇒ 后端放行任意非空字符串。
   ⇒ 解包器**只校验「非空字符串」，绝不能校验枚举** ——
     校验枚举会在后端新增动作时把正常响应判成异常。
   ★★ 对照第七十三批的 `event_type`：那个是 `if/else` 决定的**三态**（**封闭**），
     这个是 Redis 内容决定的（**开放**）⇒ 判据不能跨族照抄。

4. **★★★★ `count` 是**去重后**的条数，不是分页元信息。**
   `:65-68` 按 `seq` 去重；`seq` 跨进程重启会碰撞（`:55-57` 注释自陈），
   碰撞时**保留先出现的那条**（LIST 头 = 最新优先扫描顺序）
   ⇒ `count` 可能小于「实际匹配数」。

5. **★★★★ 这是短期回放，不是历史。**
   `:56` 的 `LRange(ctx, RedisKey, 0, RedisMaxLen-1)` 扫**整个**队列，
   而队列是 `LPUSH` + `LTRIM 5000`（`internal/liveactions/liveactions.go:115-117`）
   ⇒ **更老的事件已被 LTRIM 掉**。
   `:23-24` 的注释自陈：「Long-term history stays in request journey
   ⇒ **this endpoint must not scan Redis as the long-term solution**」。

6. **★★★★ 失败编码有三种，其中一种是 502。**
   | 情形 | 状态码 | message |
   |---|---|---|
   | `id` 为空（`:36-38`） | **400** | `missing request id` |
   | Redis 未装配（`:41-44`） | **503** | `live actions store not wired` |
   | Redis 读失败（`:52-56`） | **502** | `live actions store unavailable` |
   ★★ **502 Bad Gateway** 在本仓基本不用 ⇒ 客户端的「网关错误」分类必须显式容纳它。
   ★ 读 Redis 的超时只有 **2 秒**（`:29`）。

7. **★★★ `credential_label` 在本端点永远不出现。**
   `flattenActionEvent` 支持注入 `labels`（`:115-123`），但本端点调用时传的是
   **`nil`**（`request_actions.go:69`）⇒ 那是 SSE 那条路才有的字段
   ⇒ 按 SSE 契约去等它会永远等不到。

### 另注

- 排序：`actionEntryLess`（`:98-107`）先比 `ts`（两边都是 string 且不等时）、
  否则比 `seq`（`.(float64)` 断言失败静默取 0）。
  但 `ActionEvent` 的 `ts`/`seq` **都不带 omitempty** ⇒ 恒在 ⇒ 断言不会失败
  ⇒ 排序实际可信。★ 真正让顺序看着反常的是**零值 `ts`**
  （`time.Time{}` ⇒ `"0001-01-01T00:00:00Z"`，字符串比较下**最小** ⇒ 排到最前）。
- `request_id` 是**原样回显**（`:78` 用 `r.PathValue("id")`，不 trim 不规范化），
  且过滤是**精确比较**（`:62`）⇒ 大小写敏感、前后空格也不匹配。
- `actions` 恒为数组（`make([]map[string]any, 0, 16)`，`:58`）。
- 坏行被静默丢弃：JSON 解析失败（`:87-89`）与 `action` 为空（`:90-92`）都 `continue`
  ⇒ 响应里看不出丢过东西。

### 验证

- 用例 **71 条全绿**
- 变异 `/tmp/mut-co74.mjs` **40 条，40/40 有牙、零可疑**，`RESTORED=OK`
- 三门 rc=0；`vue-tsc` rc=0；`npm run build` rc=0
- 全量 **4032 条（138 文件）** rc=0；十连跑 10/10
- U+FFFD 自查：源与用例均 0

### 变异验证暴露的判据缺陷（真缺陷，已修）

1. **★★ 锚点自检在 `--dry` 阶段就抓到了 1 条**（`ts 不是字符串时抛错` 不在任何标题里）
   —— 本批把这个自检保留下来是对的：它比实跑快一个数量级。
   ★ 而且它顺带暴露了**夹具的缺口**（见第 2 条）。

2. **★★ 夹具缺口：「键缺」与「类型错」是两条不同的检查分支。**
   我最初只有「删掉 `ts` 键」的夹具，而变异删的是 `typeof d.ts !== 'string'` 那行
   ⇒ 删键仍由 `requireKeys` 拦下 ⇒ **判据无牙**。
   ⇒ 补了「键在但类型错」的夹具（`ts: 7` / 容器 `request_id: 7`）。
   ★★ 推论：`requireKeys`（缺键）与类型校验（键在但错）**永远是两条分支**，
     覆盖其中一条时**必须同时覆盖另一条**。

3. **★★ 夹具自身先抛，测的就不是被测代码。**
   「actions 不是数组时抛错」原本用 `actionsOk(null as never)`，
   而 `actionsOk` 内部要算 `actions.length` ⇒ **`TypeError` 在夹具里就抛了**，
   根本没走到解包器（`rc=1` 但失败消息是
   `Cannot read properties of null` 而不是我们的断言消息）。
   ⇒ 改为直接构造对象。★ 这是「rc≠0 但失败消息不是被测代码的消息」的一个实例。

4. **★★ 想造「类型错误的载荷」时，夹具签名不能太严。**
   `entry(seq: number, action: string, ts: string)` 严格签名下
   `entry(1, 7, '…')` 会在 **`vue-tsc` 里报错**，而 **`vitest` 不做类型检查**
   ⇒ 两道门结论不一致（vitest 绿、type gate 红）。
   ⇒ 新增 `entryRaw(o: Record<string, unknown>)` 专用于构造非法载荷。
   ★★ 这是「`vitest` 通过 ≠ `vue-tsc` 通过」的又一个具体实例（前面已有 `noUncheckedIndexedAccess` 那次）。

5. **★★ 判据恒真 + 夹具无区分力（各 1 条）。**
   - 「ts 与 seq 都在时排序键齐全」断言 `toBe(true)` ⇒ **它本身是恒真的**
     ⇒ 补了「ts 不是字符串时排序键不齐全」负控（需绕过解包器直接构造）。
   - 「ts 倒退时判定不成立」的夹具 `[12:00, 08:00]` 对「只比 ts 不比 seq」的变异
     **无区分力** ⇒ 补了「ts 相同但 seq 倒退」的夹具。

6. **锚点指错 2 条**（#18 / #30 / #33 中有两条）—— 连续第八批。
   ⇒ 每次**新加夹具或新加负控**之后，必须回头核对「对应变异的 `expect`」指向哪个标题。

## 11.111 看板运维芯片（dashboard/operational，第七十五批）

- **端点**：`GET /api/admin/dashboard/operational`
- **注册**：`admin/handler.go:1069` 的 `admin(...)` ⇒ **admin 档**，tenant_admin 可用 ⇒ 抽屉席不设 `requiresRole`
- **不在** `maintainCompatPrefixes` ⇒ 本进程提供
- **后端**：`admin/dashboard_operational.go`（83 行）+ `admin/dashboard_board_aux.go:23-111`
- **落点**：`web-mobile/src/api/boardOperational.ts`（448 行）+ `.test.ts`（81 用例）

### 挖到的八条契约

1. **★★★★★ 三个子查询有三种租户口径，没有一种是「按调用方租户过滤」的。**

   | 子查询 | 过滤条件 | 出处 |
   |---|---|---|
   | `model_discovery_runs` | **硬编码 `tenant_id = 'default'`** | `dashboard_board_aux.go:32` |
   | `credential_health_checks` | **完全不过滤**（全租户合计） | `:53-56` |
   | `self_check_runs` | **完全不过滤**（全租户合计） | `:84-89` |

   ⇒ 本仓**第三次**「不按调用方隔离 + admin 档」（第六十六批 data-lifecycle `metrics`、
   第七十三批 node-health、本条）。
   ⇒ 非 default 租户的 tenant_admin 看到的是**别人**的 discovery 状态，而两个计数是**所有租户**的合计。

2. **★★★★★ `degraded` 是显式 map 赋值 ⇒ 恒发，且由两个来源驱动。**

   ```go
   out["degraded"] = discErr != nil || checksErr != nil
   if discErr != nil   { out["degraded_reason"] = "discovery status unavailable" }
   if checksErr != nil { out["probe_degraded"] = true }
   ```

   ⇒ 两个条件键**各自对应一个查询** ⇒ `degraded: true` 时必须看条件键才知道是谁挂了。
   ⇒ 判读要互斥：`degraded_reason` 在 ⇒ discovery 挂；`probe_degraded` 在 ⇒ 计数查询挂。

3. **★★★★★ `degraded: true` 可能是「表里从来没有记录」，不是「查询失败」。**
   `:35-37` 打日志时**排除**了 `pgx.ErrNoRows`，`:66` 的降级判定**没有排除**
   ⇒ 「没跑过 discovery」也标成降级（`status: null` + 同一个 `degraded_reason`）。
   而 `selfcheck` 用 `COUNT(*)` 聚合**恒返回一行** ⇒ 空表时不降级
   ⇒ ★ **两个 `degraded` 的触发原因不同构**，客户端不能复用同一套文案。

4. **★★★★ `checks_last_10m === 0` 是二义的。** 计数失败只 `slog.Warn`，值留 0（`:52`）
   ⇒ 与第六十八批 `compressed_requests === 0` 同型 ⇒ 只有 `probe_degraded` 缺失时这个 0 才可信。

5. **★★★★ `success_rate` 是 0-1 比例**（`:96-99`），且 `total == 0` 时**留 0.0**
   ⇒ 「没跑过」与「全失败」同值 ⇒ 必须同时看 `total_runs_24h` 与 `degraded`。
   ★ 单位是 0-1（对照 `percentage` 是 0-100）。

6. **★★★ `discovery` 里恒发键与条件键混排**：`running`/`status`/`trigger` 恒发
   （`strPtrVal(nil)` ⇒ JSON `null`），`started_at`/`heartbeat_at` 是条件键。

7. **★★★ 30 秒缓存，两层**：进程内 `boardOperationalCache`（TTL 30s）+ 响应头
   `Cache-Control: private, max-age=30` ⇒ 两次采样看不到变化**可能只是缓存**；缓存是进程内的，多副本各不同。

8. **★★ `include_operational` 参数被本端点完全忽略** —— `includeBoardOperational`（`:44-52`）
   是看板汇总那条路用的，handler 从头到尾没读这个 query 参数。

### 验证

- 用例 **81 条全绿**
- 变异 `/tmp/mut-co75.mjs` **44 条 = 41 有牙 + 3 条可证等价 + 0 STILL_GREEN**，`RESTORED=OK`
- 三门 rc=0；`vue-tsc` rc=0；`npm run build` rc=0
- 全量 **4113 条（139 文件）** rc=0；十连跑 10/10
- U+FFFD 自查：源与用例均 0

### 变异验证暴露的判据缺陷

1. **★★★★★ 量具缺陷：解包器在 `describe` 体顶层调用 ⇒ 整份 spec 收集期就挂。**
   缓存那个 `describe` 里写的是

   ```ts
   const a = unwrapBoardOperational(payload())   // ← 顶层
   ```

   注入「把条件键误加进必检」后，抛错发生在**收集期** ⇒ vitest 报
   `FAIL <file> [ <file> ]`（**没有 `>` 分隔符**）+ `Tests  no tests`。
   我的 harness 只 grep `file > 用例名` 这一种形态 ⇒ **两条明明有牙的变异被报成 STILL_GREEN**。
   ⇒ 两处都改：① spec 侧改成惰性 `healthy()` 构造；② harness 加 `parseFailures()`，
     把「`Tests no tests` / `FAIL … [`」也算红。
   ★★★ 归因顺序里第 ① 步（变异是否真改到行为）本来能抓到这个，
   **是量具把「整份文件挂掉」呈现成了「一条都没红」** —— 零结果先怀疑量具。

2. **★★★★ 判据缺口 3 处（`板` 判定只有正向、阈值相关判定被阈值掩盖、状态码只测了 200）。**
   - `boardChecksCountMayBeFailed` 只有「0 + probe 降级 ⇒ true」的正向断言，
     缺「0 + probe 未降级 ⇒ false」的负控 ⇒ 删掉 `&& boardBgProbeDegraded` 打不出差异。
   - `boardSelfCheckHealthy(r, 0.8)` 在 `total=0` 时，`0.0 >= 0.8` 本来就是假
     ⇒ 删掉 `if (boardSuccessRateIsMeaningless(r)) return false` **打不出差异**。
     ⇒ 补「**阈值放宽到 0**」的用例：这时 `0.0 >= 0` 为真，
     「没跑过 ≠ 健康」才真正与阈值解耦。
   - `boardNotConfigured` 只测了 200 ⇒ 放宽成「所有 5xx」打不出差异
     ⇒ 补「**500 + 同一个 message**」⇒ 挡住「一律说成数据库未配置」的错误处置指引。

3. **★★★ 三条可证等价变异（保留守卫，不删）。**
   `boardBgDiscoveryReason` / `boardDiscoveryStartedAtOrNull` / `boardSelfCheckReason`
   里的 `v !== ''` 守卫：后端只写非空字面量（`degraded_reason`）或
   `time.RFC3339` 格式化值（`started_at`）⇒ **该分支对本族契约不可达**。
   保留的理由：这几个键是**条件键，解包器不校它们的类型** ⇒ 这是唯一兜底。
   ⇒ 记为可证等价变异，已在源码注释里写明理由。

4. **`--dry` 阶段抓到 3 处锚点错误**（1 处用例名不在标题里、2 处 `from` 前缀多两个空格）。
   ★ 其中 #8 的用例名我写的是「**全**健康载荷原样通过」，实际标题是「健康载荷原样通过」
   —— 又一次「`expect` 必须从 `it('…')` 标题里抄」。

## 11.112 系统自检族（self-check，第七十六批）

- **端点**：`/api/self-check/{runs, runs/{id}, settings, stats, models, trigger/availability}`
- **注册**：`admin/self_check_handlers.go:60-68` 的 `RegisterRoutes`，六个 GET **全部 `admin(...)`**
  ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`
  （同族 `settings/update`（`:64`）与 `trigger`（`:66`）是 `superAdmin(...)`，写操作，不碰）
- **表**：`deploy/sql/schemas/baseline/01-schema.sql`
- **落点**：`web-mobile/src/api/selfCheck.ts` + `.test.ts`（151 用例）

### 挖到的九条契约

1. **★★★★★ `self_check_runs` 有 `tenant_id` 列，但六个 handler 一个都不用。**

   ```sql
   tenant_id text DEFAULT 'default'::text NOT NULL,
   ```

   而 `handleListRuns` 是 `WHERE 1=1`、`handleGetRun` 是 `WHERE id=$1`、
   `handleStats` 三处是 `WHERE started_at >= $1`、`handleModels` 是 `GROUP BY model_name`
   ⇒ **全部不带租户条件**；注册是 `admin(...)`
   ⇒ **tenant_admin 能读所有租户的自检记录**，含 `error_detail`、`request_body`、
   `response_preview` 等正文级内容。
   ⇒ 本仓**第四次**「不隔离 + admin 档」。与前三次不同的是：
   **列就在表里，只是查询从不引用它** —— 不是「表没有租户概念」。

2. **★★★★★ `status` 是五值枚举，统计却只数三个。**
   建表 CHECK：`('running','success','partial','failed','retrying')`；
   而 summary / by_model 只 FILTER 三个 ⇒ `total_runs` 走 `COUNT(*)`，**含 running 与 retrying**
   ⇒ `success_runs + partial_runs + failed_runs` **可能小于** `total_runs`。
   ⇒ 拿三项相加当分母会算错。
   ⇒ 桌面 `api-selfcheck.ts:55` 只声明四值，**漏 `retrying`**。

3. **★★★★★ `range` 回显的是请求值，不是生效窗口。**
   `:777-794` 用 `rangeParam` 原值算 `since`（未知值静默落 24h），
   `:963` 又把**同一个原值**回显 ⇒ `range=xyz` 得到的是 **24h 的数据、标着 `xyz` 的 range**。

4. **★★★★★ `stats` 四个区块有四种失败策略，其中两种会骗人。**

   | 区块 | 查询失败时 | 客户端看到 |
   |---|---|---|
   | `summary` | **错误被丢弃**（`:805` 的 `Scan` 返回值没接） | 零值 + `success_rate: 0.0`，**HTTP 200** |
   | `by_model` | 500 | 报错 |
   | `error_breakdown` | 只 `slog.Warn`（`:872-874`） | `[]` —— **与「没有失败记录」同形** |
   | `trend` | 只 `slog.Warn`（`:904-906`） | `[]` —— **与「该窗口没跑过」同形** |
   | `probe_system` | 两个查询的错都 `_ =` 丢弃（`:941`/`:949`） | 全零 ⇒ **`healthy` 算成 `true`** |

   ⇒ 最严重的是最后一行。这个区块的注释（`:926-931`）自陈存在的理由就是
   「页面绿灯但探测管线已死」（glm-5.2 事故），而**它的查询失败恰好产出绿灯**：
   `queue_ready_unclaimable == 0` 且 `last_activity_at == nil` ⇒ `healthy = true`。
   ⇒ 与第六十八批 `compressed_requests === 0`、第七十五批 `checks_last_10m === 0`
     同族，但这次**直接落在健康判据上**。

5. **★★★★ `credential_id` 不是数据库列，是从 `model_name` 推导的。**
   `credentialIDFromSelfCheckLabel`（`:75-85`）：前缀 `cred-` + `ParseInt` + `id > 0`，
   否则 nil，`omitempty` ⇒ 键缺失。⇒ **客户端可以自己验算**。
   ⇒ ★ `ParseInt` 不跳前导空白，`Number(" 7") === 7`；`ParseInt("0x10", 10, 64)` 报错，
     `Number("0x10") === 16` ⇒ 用 JS 的 `Number()` 直译会推出错误的 id。

6. **★★★★ `omitempty` 打在 `int` 上 ⇒ 0 毫秒是「键缺失」不是 `0`。**
   `upstream_latency_ms`（`:106`）。同族 `error_type`/`error_detail`/`upstream_result`/
   `upstream_error`/`selection_strategy` 是 SQL `COALESCE(...,'')` 成空串**再被 omitempty 吃掉**
   ⇒ 「键在」等价于「非空」。scRun 一共 **9 个条件键 / 11 个恒在键**。

7. **★★★★ run 详情把「数据库挂了」说成「记录不存在」。**
   `:230-233`：`QueryRow(...).Scan(&err)` 的**任何**错误都走 404 `run not found`
   ⇒ 客户端无法区分 404 的两种成因。

8. **★★★ `settings` 这个 GET 有写副作用。**
   `:327-345`：读不到行就 `INSERT ... ON CONFLICT (id) DO NOTHING` 播种默认值再重查一次。
   ⇒ 它不是纯只读端点，预取会真的落库。播种常量见 `SELF_CHECK_DEFAULT_FEATURED_MODELS`（`:290`）。

9. **★★★ `/models` 没有 `partial` 计数，而 `stats` 的 `by_model` 有。**
   ⇒ 两个端点 `total` 口径相同，但 `success + failed` 在 models 里**不等于** `total`；
   且 models 是**全时段**，stats 按窗口。

### 另注

- `error_type`（31 值）与 `selection_strategy`（7 值）都是**建表 CHECK 约束** ⇒ 真的封闭枚举，
  与第七十四批 `action` 那种「只判 `!= ""`」的开放字符串不同。
- `limit` 静默回落：`1..500` 才生效，其余（含 `0`/负数/`abc`/空）一律回 50，**不回显**。
- `items` / `rounds` / `by_model` / `trend` / `models` 全是 `make(...,0)` ⇒ **恒数组，不是 null**。
- `probe_system` 的两个时间键是 map 里塞 `*time.Time` ⇒ **恒在键但可为 null**；
  而 `models[].last_run` 是 struct 字段 + omitempty ⇒ **可为键缺失**。两种编码出现在同一个族里。
- 桌面 `SelfCheckStats` 类型**没有 `probe_system`** ⇒ 桌面把这个区块整个丢了。

### 验证

- 用例 **151 条全绿**
- 变异 `/tmp/mut-co76.mjs` **96 条 = 92 有牙 + 4 条可证等价 + 0 STILL_GREEN**，`RESTORED=OK`
- 三门 rc=0；`vue-tsc` rc=0；`npm run build` rc=0
- 全量 **4264 条（140 文件）** rc=0；十连跑 10/10
- U+FFFD 自查：源与用例均 0

### 变异验证暴露的判据缺陷

1. **★★★ 锚点未同步（第 N 次）：8 条 STILL_GREEN 全是这个。**
   我补了有区分力的新用例（`gpt-0042`、前导空格、`cred-9` 不一致、含斜杠的 id…），
   却**没回头改对应变异的 `expect` 指向** ⇒ 用例有牙、变异打偏。
   ⇒ 「新加夹具/新加负控之后必须回头核对每个相关变异的锚点」——这条纪律的价值再次被证明。

2. **★★★ 夹具无区分力（6 条），其中三条是同一个函数的两个分支各缺一半。**
   - `selfCheckCredentialIdMatches` 的两个分支：`derived === null` 的用例一大把，
     但**「有推导值且对不上」的用例一条都没有** ⇒ 把那一半恒真化打不出差异。
   - 前缀检查：`'glm-5.2'` 的后缀本来就不是纯数字 ⇒ 删掉前缀检查照样返回 null
     ⇒ 补 `'gpt-0042'`（无 `cred-` 前缀但后缀是纯数字）。
   - 纯数字检查：`'cred-7a'` 交给 `Number()` 得 NaN ⇒ 仍返回 null
     ⇒ 补 `'cred- 7'`（Go 的 `ParseInt` 报错）与 `'cred-0x10'`。

3. **★★ 短路链让后面的分支测不到：`queue_last_activity_at === null` 那支。**
   夹具 `probeSystem()` 默认 `queue_running: 1` ⇒ 判据在**下一行**就 `return true` 了
   ⇒ 把 null 那支改成 `return false` 打不出差异。
   ⇒ 测短路链的某一支，必须把它**前面所有分支的触发条件都关掉**。

4. **★★ 数字键与字符串键合并校验 ⇒ 放行了 `error_type: 500`。**
   我最初写的是 `typeof d[k] === 'string' || typeof d[k] === 'number'`，
   用例「error_type 是数字时抛错」首跑就红 ⇒ 这是**实现缺陷**，不是判据缺陷。
   ⇒ 改成 `SELF_CHECK_RUN_NUMERIC_OPTIONAL_KEYS` / `..._STRING_OPTIONAL_KEYS` 两组分别校。
   ★ 与已记录的「`toThrow(/字段名/)` 被下游兜住」同族：**类型校验写宽等于没写**。

5. **★★ 两条恒等式用错方向（我自己的断言写错，实现是对的）。**
   - `error_type` 枚举我断言 32，**实际建表 CHECK 是 31 值** ⇒ 改断言。
   - Go 是 `time.Since(...) < 15*time.Minute`（严格小于）⇒ 恰好 15 分钟判 stale。
     我原本断言 true ⇒ 改断言，并补「差一毫秒仍在阈值内」。

6. **★★★ 四条可证等价变异（保留守卫，不删）。**
   `#54` 允许「键在而值为 undefined」——`requireKeys` 已保证键存在，JSON 解析也不会产出这种值；
   `#70` `<= 0` 与 `=== 0`——`total_runs` 来自 `COUNT(*)` 恒 ≥ 0；
   `#73` `> 0` 与 `!== 0`——`queue_ready_unclaimable` 同理；
   `#84` `|| null` 与 `?? null`——`upstream_latency_ms` 带 omitempty，0 根本不落键。
   ⇒ 全部记为可证等价变异，已在脚本与源码注释里写明理由。

7. **顺手纠一处上一批的追溯错误。** 第七十五批把 `dashboard/operational` 写成
   `handler.go:1069`，实际是 **`:1068`**（`:1069` 是 `board/error-drill`）。
   ⇒ 行号是「逐字照抄」的产物，**跨批次也会漂**，每批开写前都要重新确认。

## 11.113 路由策略配置面（routing policy，第七十七批）

- **端点**：`GET /api/routing/{policy, featured, scoring-weights, featured-models}`
- **注册**：`admin/handler.go:1200`（superAdmin）/ `:1201`（superAdmin）/ `:1215`（superAdmin）/ `:1216`（**admin**）
- **后端**：`admin/routing.go`（4200+ 行）与 `deploy/sql/schemas/baseline/01-schema.sql`
- **落点**：`web-mobile/src/api/routingPolicy.ts` + `.test.ts`（81 用例）

### 挖到的七条契约

1. **★★★★★ 一个族里三档一档：`featured-models` 是 admin 档，其余三个是 superAdmin 档。**
   ⇒ ★★ **绝不能按「同前缀都是一类」定档** —— 必须逐条看注册。
   ⇒ 抽屉席放后三个**必须**设 `requiresRole: 'super_admin'` 并同步
     `src/components/shell/AppDrawer.spec.ts` 白名单；`featured-models` 不设。

2. **★★★★★ `policy` 的响应形状是 `row_to_json(rp)` ⇒ 形状由表决定，不由代码决定。**
   `routing.go:2527`：
   ```sql
   SELECT row_to_json(rp)::text FROM routing_policy rp WHERE tenant_id = 'default' ORDER BY id LIMIT 1
   ```
   ⇒ **21 个列全是响应键**（7 个 NOT NULL + 14 个可空），加一个 DDL 就要加一个客户端键。
   ⇒ ★ `row_to_json` 对可空列输出 **`null` 而不是省略键**
   ⇒ 这是本仓第**八**种 nil 编码，与第七十二批「同一载荷里两个数组键编码相反」同族。

3. **★★★★★ 空对象 `{}` 是三合一语义。**
   `routing.go:2533`：
   ```go
   if err := row.Scan(&raw); err != nil || raw == "" { writeJSON(w, http.StatusOK, map[string]any{}); return }
   ```
   ⇒ 「没有这一行」「查询失败」「文本为空」**三种都回 HTTP 200 + `{}`**
   ⇒ ★ 解包器把 `{}` 解成 `null`，客户端**只能说「拿不到」**，不能说「未配置」。

4. **★★★★ `scoring-weights` 的降级完全不可辨。**
   `getScoringWeights`（`:4026-4055`）：
   ```go
   if err != nil || len(weightsJSON) == 0 { return defaultWeights }
   if err := json.Unmarshal(...); err != nil { return defaultWeights }
   for k, v := range defaultWeights { if _, ok := weights[k]; !ok { weights[k] = v } }
   ```
   ⇒ **查询失败、解析失败、缺键**三种都产出同一份默认值，响应里**没有任何标记**。
   ⇒ ★★ 但**额外键能定案**：兜底分支 `return defaultWeights`（`:4041`/`:4046`）
     那张 map 只有五个键 ⇒ 响应里只要有一个额外键，就一定来自 DB。
     这条是本批唯一一个「不可辨 ⇒ 用别处证据定案」的正面例子。

5. **★★★★ `scoring-weights` 的数字键是开放形状。**
   `scoringWeightsDisplayOnlyPayload`（`:3919-3927`）把 jsonb 里**任意**键摊平，
   再加两个披露键：
   ```go
   out["display_only"] = true
   out["note"] = "these weights only affect /api/routing/resolve and /api/routing/score-details previews, not live routing"
   ```
   ⇒ 只有五个键**保证存在**（默认值回填）。
   ⇒ ★ `display_only` 是**硬编码常量** ⇒ 校验它的取值是**恒真判据**，只校类型。

6. **★★★★ 三个端点硬编码 `tenant_id = 'default'`，第四个是对的。**
   `routing.go:2529` / `:2592` / `:4038` 写死；而 `featured-models`（`:4068`）走
   `EffectiveTenantIDAll(r)`（`context.go:69`）—— tenant_admin 拿自己的租户、
   super_admin 拿全租户合计。
   ⇒ ★ **同族两个隔离口径**：与第七十三/七十五/七十六批「全族都不隔离」不同，
     这里是「族里三个错、一个对」。

7. **★★★ `featured-models` 的 `standardized_name` 恒等于 `name`。**
   `routing.go:4081-4082` 两个字段都取 `p.CanonicalName`
   ⇒ 客户端不该把它们渲染成两种东西，也不该拿它做「标准化前后」对照；
   但**类型仍要校**（后端两处同源不等于两处同型）。

### 另注

- `featured_models` 恒为数组：`COALESCE(..., ARRAY[]::TEXT[])`（`:2591`）+ nil 兜底（`:2598`）
  ⇒ **不会是 null**；但查询失败只 `slog.Warn` 后回 `[]` ⇒ 与「没配」**同形**。
- `models` 恒数组（`make([]featuredModel, 0, len(popular))`），四键无 omitempty。
- `source` 是**两个字面量**决定的封闭枚举（`:2862` 的 `"policy"`、`:2903` 的 `"usage"`）
  ⇒ 与第七十四批「只判 `!= ""`」的开放字符串**不同**，这里**可以**校验取值。
- `count` 在内层是 `*int`，写出时 nil→0 ⇒ 对外**恒为数字**。
- usage limit 20 **只**作用于 usage 来源；`policy` 来源不受限（`:2865-2867` 的注释明说）。
- 四个端点都是 5 秒超时；`policy` 的 PATCH 只读六个已知列、其余忽略（COALESCE 静态 UPDATE）。

### 验证

- 用例 **81 条全绿**
- 变异 `/tmp/mut-co77.mjs` **55 条 55/55 有牙、零 STILL_GREEN**，`RESTORED=OK`
- 三门 rc=0；`vue-tsc` rc=0；`npm run build` rc=0
- 全量 **4345 条（141 文件）** rc=0；十连跑 10/10
- U+FFFD 自查：源与用例均 0

### 变异验证暴露的判据缺陷

1. **★★★ 锚点未同步（两批内第三次，又是它）。**
   补了三条负控（`undefined` 形状、`name` 类型错、只改最后一个权重键），
   却先跑了一轮才发现 STILL_GREEN ⇒ 用例有牙、锚点没跟上。
   ⇒ ★ **补负控与改锚点必须是同一个原子步骤**，不能只做前一半。

2. **★★ 变异本身写错：`to` 只追加不替换。**
   #25 我原本写的是「在 `requireObject` 里加一条 `resp === undefined` 的分支」，
   但**没有删掉**原来的 `isPlainObject` 检查 ⇒ 两条路径吐同一句错误消息
   ⇒ **行为完全没变**。
   ⇒ ★ 这正是「注入标记 ≠ 变异」的另一种形态：标记也在、行为也没改。
   ⇒ 自查方法：把 `to` 写完后问一句「原来那段代码还有没有一个字节留在这条路径上」。

3. **★★★ 夹具被上游检查截胡（3 条）。**
   - `★ 元素缺键抛错并点名下标` 用 `{ name: 'x' }`，它在 `requireKeys` 就抛了
     ⇒ `name` 的类型分支**从没执行** ⇒ 删掉那个校验打不出差异。
     ⇒ 补「四键齐全、只错类型」的夹具。
   - 「响应形状不是裸对象时抛错」只测了 `null`/`[]`/`'x'`，漏 `undefined`
     ⇒ 补 `undefined` 用例。
   - 「改过任一键后不再判为可能是兜底」改的是 `price`（**第一个**键）
     ⇒ 把 `default_price_usd`（最后一个）从判据里去掉打不出差异
     ⇒ 补「只改最后一个键」的用例。**逐键都要有专属用例。**

4. **★ 变异自身无区分力：同一个集合里重复一个键。**
   #7 我把 `transient_fail_threshold` 在**必填集合里**写了两次
   ⇒ 与可空集合仍然没有交集 ⇒ 断言照过。
   ⇒ 改成「把必填键塞进可空集合」才真的制造了交集。
   ⇒ 断言是 `A ∩ B = ∅` 时，变异必须动**跨集合**的关系，在集合内部打转没用。

5. **★★ 首跑即红的实现缺陷（1 条）：判定写弱了。**
   `routingScoringWeightsMayBeDefaults` 原本只看五个键的值，
   于是「五键全是默认值 + 带一个额外键」也会被判成「可能是兜底」——
   而额外键恰好是**唯一能定案**的证据。
   ⇒ 补 `extraKeyCount(w) > 0 ⇒ false`，并把这条推理写进函数注释。

## 11.114 三态探测队列（probe/tasks，第七十八批）

- **端点**：`GET /api/admin/probe/tasks?status=pending|in_flight|completed&limit=`
- **注册**：`admin/probe_dashboard.go:1907` 的 `adminWrap(h.handleProbeTaskRoute)` ⇒ **admin 档**
  ⇒ tenant_admin 可用 ⇒ 抽屉席不设 `requiresRole`
- **路由是方法多路复用**：GET 走 `handleProbeTaskList`（`:1841`，只读）；
  POST 走 `handleProbeTaskCreate`、DELETE 走 `handleProbeTaskCancel`
  ⇒ 本批**只碰 GET**，写操作不碰
- **表**：`credential_probe_queue`（`sql/migrations/domain/343_credential_probe_queue.sql`）
- **桌面调用方**：`web/src/api-selfcheck.ts:313`、`components/probe/ProbeTriStateQueue.vue`
- **落点**：`web-mobile/src/api/probeTriStateTasks.ts` + `.test.ts`（72 用例）

### 挖到的八条契约

1. **★★★★★ 响应里的 `status` 不是数据库里的 status。**
   `queryProbeTriStateTasks`（`:1813-1821`）把六个 DB 状态压成三个：
   ```go
   switch t.Status {
   case "ready":   t.Status = "pending"
   case "running": t.Status = "in_flight"
   default:        t.Outcome = t.Status; t.Status = "completed"
   }
   ```
   DB 的 CHECK 是 **6 值**：`('ready','running','success','failed','expired','cancelled')`
   ⇒ ★ **原始状态只在 completed 行以 `outcome` 保留**，
     pending / in_flight 行的原值（`ready` / `running`）**被丢掉且不可恢复**。
   ⇒ 客户端看到的 `status: 'pending'` **不能**反推「这一行是 ready 而不是别的」。

2. **★★★★★ `outcome` 与 `status` 互斥且可验。**
   ⇒ `status === 'completed'` ⇒ `outcome` 必在（四值之一）；
     否则 `outcome` 必不在（omitempty）。
   ⇒ ★ 因为 `:1818-1820` 的 default 分支是**唯一**给 `Outcome` 赋值的地方，
     可达载荷恒满足 `outcome 存在 ⇔ status === 'completed'`。

3. **★★★★★ `next_retry_at_ms` 的存在性 ⇔ `status === 'pending'`。**
   `:1822-1824` 的条件是 `t.Status == "pending" && !nextRunAt.IsZero()`，
   而 `next_run_at TIMESTAMPTZ NOT NULL DEFAULT now()` ⇒ pending 行必然非零
   ⇒ ★ **pending 行必有这个键，另两条腿必没有**。退避信息只在 pending 腿有意义。

4. **★★★★★ `*int` + omitempty 与 `int` + omitempty 的行为相反。**
   `HTTPStatus *int` / `LatencyMs *int`（`:1733-1734`）是**指针**：
   omitempty 只在 nil 时省略 ⇒ **数据库里的 0 会原样出现**（`http_status: 0`）。
   对照第七十六批 `upstream_latency_ms int` ⇒ 0 被 omitempty **吃掉**变成键缺失。
   ⇒ ★★ 用 `if (t.http_status)` 判「有没有测出状态码」会把 0 误判成「没有」。

5. **★★★★ `provider_id` / `provider_name` / `provider_code` 三个都带 omitempty。**
   provider_id 是 `*int64`（LEFT JOIN 可能 NULL）；
   name/code 走 `COALESCE(NULLIF(...), NULLIF(...), NULLIF(...), '')`（`:1781-1782`）
   ⇒ 三者皆空时 SQL 给 **`''`**，再被 omitempty 吃掉
   ⇒ ★「供应商未知」表现为**键缺失**，不是空串也不是 null。

6. **★★★★ `origin` 是从 `source` 推出来的三值枚举，且客户端可自验。**
   `probeTriStateOrigin`（`:1743-1751`）：
   ```go
   case "request_failure", "no_candidates": return "error"
   case "admin", "external_async":          return "manual"
   default:                                  return "scheduled"
   ```
   ⇒ ★★ **`no_candidates` 不在 `source` 的 CHECK 约束里**
   （CHECK 只有 `'request_failure','periodic','external_async','admin'`）
   ⇒ 那个分支项**不可达**（恒真守卫，按既有纪律保留并在注释里写明）。
   ⇒ 但 `origin` 与 `source` 的对应关系**客户端可自验**，不必信后端。

7. **★★★ 参数是**严格校验**，不是静默回落。**
   - `status` 不在白名单 ⇒ **400** `status must be pending|in_flight|completed`；
   - `limit` 不在 `1..200` ⇒ **400** `limit must be 1..200`；
   - 缺省 `status=pending`、`limit=50`。
   ⇒ ★ 与第七十六批 `/self-check/runs` 的 `limit` **静默回落成 50** 形成直接对照：
     **同一个仓里两种参数校验风格并存**，写客户端时不能凭直觉假设。

8. **★★★ `count` 是本页长度，不是总数。**
   `:1879` 的 `"count": len(tasks)` ⇒ `count === tasks.length` **恒成立**
   ⇒ ★「还有没有下一页」只能靠 `tasks.length >= limit` 判断，`count` 不提供额外信息。

### 另注

- `tasks := []ProbeTriStateTask{}`（`:1797`）⇒ **恒数组，不是 null**。
- **三条腿的排序键各不相同**：pending = `priority DESC, next_run_at ASC, id ASC`；
  in_flight = `started_at DESC NULLS LAST, id DESC`；
  completed = `COALESCE(finished_at, updated_at) DESC, id DESC`
  ⇒ 客户端**不能**假设跨腿的统一排序。
- 扫描失败是 `return nil, err` ⇒ **整条 500**（不像别处 `continue` 跳行）。
- 503 `database not configured`；500 `internal server error`。
- 结构体 13 个恒在键 + 9 个条件键 = 22 键；`outcome`/`next_retry_at_ms`/`http_status`/
  `latency_ms`/`reason_code`/`finished_at` 都是条件键。
- 注释自陈「Metadata only — no `result_body_preview`」（`:1709-1710`）⇒
  **请求/响应正文刻意不进 API 与 SSE**（可观测安全红线）。

### 验证

- 用例 **72 条全绿**
- 变异 `/tmp/mut-co78.mjs` **47 条 = 45 有牙 + 2 条可证等价 + 0 STILL_GREEN**，`RESTORED=OK`
- 三门 rc=0；`vue-tsc` rc=0；`npm run build` rc=0
- 全量 **4417 条（142 文件）** rc=0；十连跑 10/10
- U+FFFD 自查：源与用例均 0

### 变异验证暴露的判据缺陷

1. **★★ 锚点指错（1 条）。**
   `退避一致性只判 pending 腿` 那条变异打破的是「in_flight 居然带退避」那一支
   （`true || …` 恒真），而我的锚点指着「pending 缺退避」——
   那一支两条实现都返回 `false` ⇒ 打不出差异。
   ⇒ 判据是**双向不变量**时，两个方向各要一条用例，锚点也要指向对的那一条。

2. **★★ 两条可证等价变异。**
   `probeTaskSucceeded` / `probeTaskFailedOrAbandoned` 里去掉 `status === 'completed'`，
   对可达载荷行为不变（`:1818-1820` 保证 `outcome` 存在 ⇔ completed）。
   ⇒ 记为可证等价，保留守卫。

3. **★★ 实现缺陷（首跑即红）：字段名写错。**
   `probeTaskLatencyMsOrNull` 里写的是 `t.latencyMs`，而字段名是 **`latency_ms`**
   ⇒ 永远取到 `undefined` ⇒ 恒返回 `null`。
   用例「`latency_ms` 为 0 时取得到 0 而不是 null」当场抓到。
   ⇒ ★ **snake_case 的后端字段最容易在这里出错**；语义函数必须有一条
     「键在时能取到值」的用例，否则 `?? null` 会把打错的字段名吞掉。

4. **★★ 类型门比 vitest 更严（又一次）。**
   `requireTask` 里 `(… as readonly string[]).includes(d.origin)` —— `d.origin` 是
   `unknown`，`vitest` 不做类型检查所以全绿，`vue-tsc` 报 TS2345
   ⇒ 补 `String(...)`。
   ⇒ 与第七十四批那次同源：**「vitest 全绿」不等于「类型门绿」**。

## 11.115 供应商错误趋势（errors/trend，第七十九批）

- **端点**：`GET /api/errors/trend`
- **注册**：`admin/handler.go:1249` 的 `admin(h.errorsTrendHandlers.getErrorsTrend)` ⇒ **admin 档**
  ⇒ tenant_admin 可用 ⇒ 抽屉席不设 `requiresRole`
- **实现**：`admin/errors_trend.go`（350 行）
- **落点**：`web-mobile/src/api/errorsTrend.ts` + `.test.ts`（74 用例）

### 挖到的八条契约

1. **★★★★★ 两个数据源，`source` 告诉你是哪个。**

   | source | 读的是 | 何时被选中 |
   |---|---|---|
   | `stats` | `supplier_error_stats`（预聚合，分钟桶由后台聚合器每 5 分钟 UPSERT） | 该窗口该粒度**有行** |
   | `fallback` | `supplier_errors_unified`（明细） | stats 返回**零行**（`:122-133`） |

   ⇒ ★ `fallback` 只说明「**预聚合表在这个粒度上没有行**」，
     **不一定是「没有错误」** —— 粒度不匹配（聚合器只写了别的粒度）也会走到这里。
   ⇒ ★★ 但 `fallback` **且** `time_series` 为空是**确定的**：
     fallback 分支总是被真的执行一遍，明细表也为空 ⇒ 窗口内确实没有错误。
   ⇒ 对照第七十八批 `summary` 四区块四种失败策略：这里是**显式标出来源**的写法。

2. **★★★★★ 读路径刻意绕过 RLS。**
   `withTrendReadTx`（`:171-196`）在只读事务里
   `set_config('app.bypass_rls','true',true)`（`is_local=true`，
   保证旁路随事务提交即失效、pooled 连接不保留提权）。
   注释自陈原因（`:163-170`）：`supplier_errors_hot` / `supplier_errors`
   是 **FORCE RLS + 租户隔离**，而网关应用角色**不是 superuser**，
   直连读会被**静默过滤到 0 行** ⇒「趋势数据闭环断裂」。
   ⇒ ★★ **「0 行」本身是一个被代码注释文档化的失败模式**，
     客户端看到的数字是 **bypass 之后**的；租户隔离靠 `EffectiveTenantIDAll(r)` 传参
     而不是靠 RLS ⇒ super_admin 拿到的是全租户合计。

3. **★★★★★ `summary.unique_requests` 是各桶相加，不是去重计数。**
   `loadFromStats:221-222` / `loadFromDetail:263-264` 只做
   ```go
   resp.Summary.TotalErrors    += p.ErrorCount
   resp.Summary.UniqueRequests += p.UniqueRequests
   ```
   ⇒ ★★ 一个跨桶的 `request_id` 会被数两次 ⇒ 汇总值**是上界**。
   ⇒ 与第七十六批 `summary` 三项之和（含 running/retrying）是同一族陷阱：
     **「桶求和」不等于「全局去重」**。

4. **★★★★ `by_supplier` / `by_error_type` 是 `map[string]int` + `omitempty`。**
   `:42-43` 两个键都带 omitempty，SQL 侧是
   `jsonb_object_agg(...) FILTER (WHERE supplier <> '')`
   ⇒ 全部被 FILTER 掉时聚合返回 NULL ⇒ 扫到空字节 ⇒ map 保持 nil
   ⇒ ★ **空 map 被整个键省略**（不是 `{}`，不是 `null`）——
     本仓第**九**种 nil 编码，也是**第一次出现 map 类型**。

5. **★★★★ `top_error_types` / `top_suppliers` 显式初始化成空数组。**
   `loadBreakdowns:308-309`：
   ```go
   resp.Summary.TopErrorTypes = []errorsTrendBreakdownRow{}
   resp.Summary.TopSuppliers = []errorsTrendBreakdownRow{}
   ```
   ⇒ 恒数组，**不会是 null**（与第 (4) 条的 map 恰好相反，**同一个响应里并存**）
   ⇒ ★★ 两者都被**截到 10 条**（`:319`/`:323`），**不回显被丢掉的数量**。

6. **★★★★ `hours` 是严格三值枚举，`granularity` 的缺省由它推导。**
   `parseVendorErrorHours`（`vendor_credential_error_handlers.go:182-191`）
   只接受 `1 / 24 / 168`，其余（含 `0`、负数、`abc`）⇒ **400** `invalid_hours`。
   `granularity` 缺省（`:81-90`）：
   ```go
   case hours <= 1:  granularity = "minute"
   case hours <= 24: granularity = "hour"
   default:          granularity = "day"
   ```
   显式传值则严格校验（`:91-94`）⇒ **400** `invalid_granularity`。
   ⇒ ★ 判据是 `<=1` / `<=24` 而非 `===1` / `===24`，客户端复刻时要照抄。

7. **★★★ 错误信封是嵌套的，与 `writeError` 的扁平形不同。**
   `writeErrorWithCode`（`handler.go:1501-1508`）：
   ```go
   writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "detail": msg}})
   ```
   ⇒ 形状是 `{"error":{"code":"…","detail":"…"}}`。
   六个 code：`db_not_configured`（503，detail 是 `database is not configured`
   —— ★ 与别处的 `database not configured` **措辞不同**）、
   `invalid_hours` / `invalid_granularity` / `invalid_credential_id`（三个 400）、
   `trend_stats_query_failed` / `trend_fallback_query_failed`（两个 500，
   ★ **detail 原文完全相同**，只有 code 能区分）。

8. **★★★ `supplier` / `error_type` 的 `'all'` 是魔法值。**
   三处 SQL 一致：`AND ($3 = '' OR $3 = 'all' OR supplier = $3)` 等。
   ⇒ ★ 空串与字面量 `'all'` 都表示「不过滤」；
     而 `credential_id` 只能用 `0` 表示不过滤，
     但入口校验又要求它 `> 0`（`:97-103`）⇒ **一旦传了 credential_id 就一定是过滤**。

### 另注

- `time_series` 两条 load 函数都显式初始化 ⇒ 恒数组。
- 8 秒超时；`since`/`until` 是服务端算的时间边界（`until = time.Now()`）并回显。
- 扫描失败是 `return err` ⇒ **整条 500**（不跳行）。
- `loadBreakdowns` 的第三个分支（`kind='creds'`）专门喂
  `summary.affected_credentials`；注释自陈该 KPI「曾声明但从未计算（恒 0）」，
  是 2026-09-12 审计 P2 补上的。

### 验证

- 用例 **74 条全绿**
- 变异 `/tmp/mut-co79.mjs` **53 条 53/53 有牙、零 STILL_GREEN**，`RESTORED=OK`
- 三门 rc=0；`vue-tsc` rc=0；`npm run build` rc=0
- 全量 **4491 条（143 文件）** rc=0；十连跑 10/10
- U+FFFD 自查：源与用例均 0

### 变异验证暴露的判据缺陷

1. **★★★ 「判据有两个必要条件」时，两个条件各要一条能单独打掉它的用例。**
   - `errorsTrendGranularityIsDefault` 的变异去掉 `!granularityWasSent`：
     我只测了「显式传了**且与缺省不同**」⇒ 两种实现都返回 false。
     ⇒ 必须补「**显式传了、但值恰好等于推导值**」那条。
   - `errorsTrendBreakdownIsTruncated` 的变异把阈值 10 改成 5：
     我只测了「1 条」与「满 10 条」⇒ **中间那段 5..9 没有专属用例**。
     ⇒ 阈值类判据要**逐段**覆盖。
   ★ 这两条都是「**夹具要挑能打破某一个必要条件的输入**」，与第七十八批
     「双向不变量两个方向各一条」同源。

2. **★★★ 锚点未同步（又一次，已是第四次同一形态）。**
   补完上面两条用例后先跑了一轮，仍 STILL_GREEN ⇒ 锚点还指着旧标题。
   ⇒ **补负控与改锚点必须是同一个原子步骤**；这次是连着两批踩同一个坑，
     说明它必须写成流程里的一步而不是靠记性。

3. **★★ `it.each` 的标题抓不到：`/it\('([^']*)'/` 只认字面量。**
   我用 `it.each(cases)('hours=%i ⇒ %s', …)` 生成三个粒度用例，
   dry 阶段立刻报两条 `NO_EXPECT_NAME`。
   ⇒ ★ 变异 harness 靠标题取锚点 ⇒ **用例标题必须字面量写出来**，
     参数化测试（`it.each` / `test.each`）的标题对 harness 不可见。
   ⇒ 已改成五条显式 `it('hours=… ⇒ …')`，顺带补了 `hours=0` 与 `hours=25`
     两条边界用例（判据是 `<=1` / `>24` 而不是 `===1` / `===24`）。

---

## 11.116 存储总览 + 表级大小（第八十批，2026-10-08）

- 新增 `web-mobile/src/api/dataLifecycleStorage.ts`（`fetchStorageOverview` /
  `fetchStorageTableSizesChecked` / 22 个语义判据 / `humanBytesGo` 复刻）
- 新增 `web-mobile/src/api/dataLifecycleStorage.test.ts`（**165 条**）
- 覆盖 `GET /api/admin/data-lifecycle/storage` 与 `GET /api/admin/data-lifecycle/storage/tables`
- 鉴权：两个都是 `admin(...)`（`admin/handler.go:982` / `:983`）⇒ **admin 档**（tenant_admin 可用）
- ★ 同族 `:986-992` 的 vacuum / vacuum-full / reindex（六个）**全是 `h.superAdmin` 且全是写操作** ⇒ 一条不碰
- 实现：`admin/data_lifecycle_storage.go`（两个 handler `:131-229` + helpers `:232-561`）

### 本族最要紧的十三件事

1. ★★★★ **`database` 是值类型字段，查询失败留「全零整块」而不是缺键。**
   `storageOverview.Database` 无 omitempty ⇒ 键恒在。`:150-152` 的 else 分支才赋值。
   ⇒ 判据 `database_human === ''` ⇔ 查询失败（成功路径必然来自 `pg_size_pretty`）。
   ⇒ 与 `columnar` 的失败表达**不同构**：columnar 用 `note` 说原因，database 只留零值 + `warnings`。
2. ★★★★ **`database.free_bytes` 恒 0、`free_human` 恒 `""` —— 不是「空闲 0 字节」，是「测不到」。**
   `queryDatabaseStorage`（`:242-290`）**从未给这两个字段赋值**，作者在 `:54` 自陈
   「当前 connection 看不到 PG server 端 fs 剩余」。⇒ **第十种 nil 编码：显式声明的「测不到」位**。
   ⇒ 客户端**禁止**把它渲染成「DB 剩余 0 B」；本模块只留常量 `STORAGE_DB_FREE_IS_UNMEASURED`，
     **不提供**读它的判据函数（那是恒真判据，按纪律删）。
3. ★★★★★ **同一个响应里有两套 humanize，单位串不重叠 ⇒ 可自验哪个是哪个。**
   `database_human` 来自 **PostgreSQL 的 `pg_size_pretty`**（`"1 kB"` 小写 k、字节级是 `"512 bytes"`）；
   其余全部 `*_human` 来自本仓 `humanBytes`（`:502-518`，`"1 KB"` **大写 K**）。
   ⇒ `storageDatabaseHumanIsGoStyle` 能把「这个响应不是本端点的」挑出来。
   ⇒ `humanBytesGo` 逐字复刻，含两级 `TrimRight` 的**顺序**（先去尾 `0` 再去尾 `.`）
     ⇒ `1.0 KB` 渲染成 `"1 KB"`（无小数点），`1.25 KB` 渲染成 `"1.2 KB"`（**截断**不是四舍五入）。
4. ★★★★ **`queryColumnarStorageSafe` 恒返回 `nil` error ⇒ `warnings` 里那条「列存统计查询失败」不可达。**
   `:296-303` 两条出口都 `return …, nil` ⇒ handler `:155-161` 的 `if colErr != nil` **永假**。
   ⇒ 列存失败的真实表达是 `columnar.note` 带前缀，**永远不会**出现在 `warnings` 里。
   ⇒ 与第七十六批 `probe_system`（`err` 被 `_ =` 丢弃 ⇒ 失败被算成 `healthy: true`）同型：
     那次是**静默成好**，这次是**静默留 note**。
5. ★★★★ `columnar.total_human` 是**恒发键但可能为空串**，且它非空 ⇔ 统计查询成功。
   `:343` 的赋值在扫描成功之后 ⇒ `available === true` **不**保证 `total_human` 非空
   （扫描失败时 `Available` 已被 `:323` 置 true，`:341` 直接 return）。
   ⇒ `note` 的四个出口（连接未就绪 / 扩展未安装 / 查询失败前缀 / 尚无表使用）是封闭的。
6. ★★★★ `warnings` 六个取值，三条查询失败、三条阈值；5 倍那条有**三个必要条件**
   （`db>0` ∧ `fs>0` ∧ **严格** `> 5.0`），5GB 那条是 `5<<30` = 5368709120 **严格大于**。
   ⇒ `warnings` 在 `:141` 显式 `[]string{}` ⇒ 恒数组。
7. ★★★★ `filesystem.free_bytes` 与 `used_bytes` **取自 statfs 的不同字段** ⇒ 两者不互补。
   `:356-362` 用 `Bavail` 当 free、用 `Bfree` 算 used ⇒ `used + free ≠ total`，
   差额是「预留给 root 的块」，由 `storageFilesystemReservedBytes()` 读出。
   ⇒ 与 (2) 的 database 块是**两个不同性质**：这一个是**口径不齐**，那一个是**根本测不到**。
8. ★★★ `local_logs` 是**指针 + omitempty**（`resolveLogDir` 失败时键被省略，实际恒在）。
   `directoryInfo` 九键恒在，但 `size_human === ''` ⇔ `exists === false`（`:407` 的赋值在提前 return 之后），
   `oldest_mtime === 0` ⇔ `files === 0`。
9. ★★★ `database.total_bytes` 有静默兜底（`:268` 赋值 `= database_bytes`），
   而 `:46` 注释里的不等式方向**其实是错的**：`pg_database_size` 算全库，
   SUM 的 WHERE（`:263`）却把 `pg_catalog` / `information_schema` **排除**了
   ⇒ **`database_bytes` 可以大于 `total_bytes`**，由 `storageDatabaseExceedsRelationSum()` 读出。
10. ★★ `collected_at` 是 `time.Now().UTC()`（两个端点都是）⇒ 必以 `Z` 结尾，可自验。
11. ★★ `limit` 用 `strconv.Atoi` ⇒ **静默回落 20，不是 400**。
    三个必要条件：`Atoi` 不报错 ∧ `n > 0` ∧ `n <= 200`。
    `Atoi` ≡ `ParseInt(s,10,0)` ⇒ `" 20"` / `"0x14"` / `"20.0"` 都报错 ⇒ 回落。
    ⇒ 与第七十八批 `/api/admin/probe/tasks`（非法 limit ⇒ **400**）是同仓两种风格并存的又一例。
12. ★★★ `/storage/tables` 只有一条错误路径，两条子路径（`Query` 失败 / `rows.Err()`）**文案逐字相同**
    ⇒ 不可区分。500 是嵌套信封 `{"error":{"detail":"查询表大小失败"}}`（**无 `code`**），
    且 `internal_error.go` 头注释明确「客户端只见到 op，绝不携带 `err.Error()`」。
13. ★★★★ `tables` 是**恒数组**（`make(…,0,limit)`），行扫描失败**静默跳过**（`warnRowSkip` + `continue`）
    ⇒ **`total_bytes === Σ tables[].total_bytes` 恒成立**（跳过多少行都不影响自洽），
    `percent_of_db` 同样只对留存行算 ⇒ 逐行重算恒吻合。
    ★ `percent_of_db` 凑不满 100（`3 × 33 = 99`）—— 整数除法截断。
    ★ 与 (7)/(9) 同族：这里的 schema 排除列表只有两个，而 `queryColumnarStorage`（`:335-337`）
      **还额外排除** `citus` / `citus_internal` / `columnar` / `columnar_internal` ⇒ **同族两个查询排除列表不同**。

### 桌面侧缺陷（本批顺带记录，不在本模块修）

★ `web/src/api/tuning.ts:456-468` 的 `TableSizeInfo` **漏了后端的 `toast_human`**（`:107`）——
本模块按后端 struct 逐字带上，spec 里有一条专属用例钉住（`行缺 toast_human`）。

### 验证

- 用例 **165 条全绿**
- 变异 `/tmp/mut-co80.mjs` **80 条**，见下节
- 三门 rc=0；`vue-tsc` rc=0；`npm run build` rc=0
- U+FFFD 自查：源与用例均 0

### 变异验证暴露的判据缺陷（80 条 → 首跑 56 有牙，逐条修到 77）

1. **★★★ 「下游的类型检查会兜住上一步的缺键检查」⇒ `toThrow` 的正则必须收紧到**自己那一步**的措辞。**
   5 条键集变异（#7-#11：从 `STORAGE_*_KEYS` 里删掉一个键）首跑全绿。
   归因：删掉 `requireKeys` 里的 `free_human` 后，**紧跟着的类型检查**
   `typeof d['free_human'] !== 'string'` 看到 `undefined` 照样抛错，
   而错误消息里也含 `free_human` ⇒ `/free_human/` 匹配上了 ⇒ 用例照绿。
   ⇒ ★ 与已记录的「夹具被上游检查截胡」是**同一个陷阱的镜像**：
     那次是更早的检查抛错、后面的分支从没执行；
     这次是**更晚的检查兜住**、正则宽到两边都匹配。
   ⇒ 修法：`toThrow(/缺 1 个键（free_human）/)` —— 只匹配 `requireKeys` 自己的措辞。

2. **★★★ 夹具不得用被测常量造：文案类判据自指恒真。** 4 条（#14-#17）。
   `storageColumnarNoteKind(col({ note: STORAGE_COLUMNAR_NOTE_EXT_MISSING }))`
   里的夹具与被测常量是**同一个符号** ⇒ 改常量的值，两边一起变 ⇒ 永远绿。
   ⇒ 改成逐字照抄后端源码的字面量：`note: 'citus_columnar 扩展未安装'`（`:320`）、
     `'数据库连接未就绪'`（`:299`）、`'尚无表使用列存（citus_columnar 已加载）'`（`:345`）、
     三个告警文案（`:183-184` / `:188` / `:192-193`，含全角 `—` `≥` `%` 与两个空格）。

3. **★★★ 样本要挑「两种实现输出不同」的那一格，不是「语义上最典型」的那一格。** 8 条。
   | 变异 | 我原来选的夹具 | 为什么不区分 | 换成的夹具 |
   |---|---|---|---|
   | #21 基数 1024→1000 | `humanBytesGo(1024)` | 两种进制都恰好进位到 1.0 ⇒ 都是 `"1 KB"` | `1500` ⇒ 1024 进制 `"1.4 KB"` / 1000 进制 `"1.5 KB"` |
   | #31 漏 `free_bytes` | 整块全零 | 那个字段本来就是 0 ⇒ 漏不漏都一样 | 只有 `free_bytes` 非零、其余全零 |
   | #32 漏 `server_version` | `db({database_human:''})` | 其余字段都非零 ⇒ 本来就 false | 其它全零但带着 `server_version` |
   | #53 换成 `=== '0 B'` | 正常目录（`1 MB`） | 两种实现都 false | **空目录**（`size_human: '0 B'` 且 `exists: true`）|
   | #54 换成 `files === 0` | 有文件的目录 | files=12 ⇒ 两种都 false | `files: 12` 但 `oldest_mtime: 0` |
   | #55 改成严格 `<` | oldest<newest | 两种都 true | **单文件目录**（两者相等）|
   | #61 放宽成 `>=` | total 比行和小（999）| 999 >= 和 仍是 false | total 比行和**大** |
   | #65 trunc→round | 3 行各 1 字节 | 33.33 两种都得 33 | **6 行各 1 字节** ⇒ 16.67 ⇒ trunc 16 / round 17 |
   ⇒ ★ 规律：**边界值相等的那一格**（1024 进位边界、单文件、并列行、total 相等）
     往往正是两种实现分不出来的格子；阈值判据要挑**小数部分 ≥ 0.5** 的那一格。

4. **★★ `String.prototype.replace` 只替换**第一个**匹配 ⇒ `from` 片段必须唯一。** 1 条（#47）。
   `return 'unknown'\n}` 在 `storageColumnarNoteKind` 与 `storageWarningKind` 里各有一处
   ⇒ 变异改的是**前者**，而锚点指向后者的用例 ⇒ 永远绿。
   ⇒ 修法：`from` 带上紧邻的前置分支，让片段唯一。

5. **★★ 锚点未同步（第五次，已连续两批）。**
   补完 3 里的 8 条新用例后忘了把变异指向它们 ⇒ #21 仍绿。
   ⇒ ★ 这已是同一形态的**第五次**（批 76/77/78/79 各踩过），
     必须在流程里固化：**「加夹具」与「改锚点」是同一个原子步骤**，
     或者跑完后逐条核对「每条变异指向的用例，是不是这条变异唯一能打红的用例」。

6. **★ 三条可证等价变异（保留，理由写进源文件注释）。**
   | # | 变异 | 为什么等价 |
   |---|---|---|
   | 27 | 删掉 `formatIntGo` 的 `if (n === 0) return '0'` | Go 侧**必须**有（buf 是定长数组，循环一次不进 ⇒ 切出空串）；JS 侧 `String(Math.trunc(0))` 天然是 `'0'` ⇒ 输出不变。保留是为了与 Go 逐字对齐。 |
   | 48 | 去掉 `storageDbOverDiskRatio` 的 `db > 0` 那一项 | db=0 且 fs>0 时 `0/fs` 数学上恒为 0；db=0 且 fs=0 时又被 `fs > 0` 那一项挡在前面（返回 0，漏不出 `0/0=NaN`）⇒ **任何夹具下输出都不变**。后端 `:180` 把两项写成对称的 `&&`，客户端照抄。 |
   | 60 | 去掉 `storageTablesLimitIsSendable` 的 `Number.isFinite` | `NaN` 被 `n >= 1` 挡住（NaN 参与比较恒假），`±Infinity` 被 `n <= 200` 挡住 ⇒ 完全冗余。保留是因为它可读，且 Go 的 `strconv.Atoi` 确有 `ErrRange` 语义，将来若改成透传原始字符串就会变得必要。 |
   ⇒ ★ 与已记录的「恒真守卫 vs 恒真判据处置相反」一致：
     这三条都是**可读的显式条件**，删了看不出意图 ⇒ 留，记等价，注释写明。

7. **★ 类型门比 vitest 严（又一次，且是同一形态）。**
   165 条 vitest 全绿，但 `vue-tsc` 报 4 条：3 条 `TS6133`（用例改成字面量后
   `STORAGE_COLUMNAR_NOTE_DB_NOT_READY` 等三个 import 变成未使用）+
   1 条 `TS2352`（`StorageOverview` 无索引签名，不能直接断言成 `Record<string, unknown>`）。
   ⇒ ★ 提醒自己：**批量改用例写法之后必须重跑类型门**，
     删 import 这类「vitest 完全看不见」的后果就是漏在这里。

---

## 11.117 指纹漂移事件（第八十一批，2026-10-08）

- 新增 `web-mobile/src/api/modelIntegrityDrift.ts`
- 新增 `web-mobile/src/api/modelIntegrityDrift.test.ts`（**125 条**）
- 覆盖 `GET /api/admin/model-integrity/fingerprint-drift?days=7&limit=200`
- **鉴权：`admin/handler.go:924-925` 的整个 `/api/admin/model-integrity/` 前缀都是 `h.superAdmin`**
  ⇒ ⇒ 接抽屉席**必须**设 `requiresRole: 'super_admin'`，并同步 `AppDrawer.spec.ts` 白名单
- 分发：`admin/model_integrity.go:62-84` 的 switch；实现 `:314-388`
- 桌面调用方：`web/src/api/integrity.ts:96-102`；视图 `ModelIntegrityView.vue` 的 fingerprint-drift 标签页

### ★ 差集扫描排掉的一个假阳性（候选集不等于待办集）

`/api/admin/auto-route/tuning/strategies` 在候选清单里，桌面 `web/src/api/tuning.ts:44`
也在调它 —— 但后端**从不注册**：`admin/auto_route_tuning.go:838` 的
`func (h *TuningHandlers) handleStrategies(...) //nolint:unused`
标着 **`nolint:unused`**，而 `RegisterTuningRoutes`（`:82-91`）的六个 `mux.HandleFunc`
里**没有** `strategies`。
⇒ ★★ 桌面调的是一个**必然 404 的死端点**；handler 代码还在、注释也还在
（`// handleStrategies: GET /tuning/strategies?days=7`），只有注册表里没有。
⇒ **「handler 存在」不等于「端点可用」** —— 必须查注册表，不能只查 handler。

### 本族最要紧的九件事

1. ★★★★★ **19 键里 14 个是指针 + omitempty ⇒ 零值是「键缺失」，不是 `null`、不是 `0`。**
   `ModelIntegrityRecord`（`model_integrity.go:19-40`）的**所有**可选字段都是
   `*string` / `*int` / `*time.Time` / `any`，**没有一个是可空值类型**。
   ⇒ **五键恒在**（`id` `detected_at` `anomaly_type` `severity` `resolved`）
   / **十四键条件**（`request_id` `provider_id` `provider_code` `credential_id`
   `client_model` `outbound_model` `raw_model_name` `expected_value` `actual_value`
   `sample` `context` `resolved_at` `resolution_notes` `tenant_id`）。
2. ★★★★★ **★ 指针 + omitempty 与值类型 + omitempty 的行为完全相反：零值指针会原样出现。**
   `ProviderID *int`：DB 里 `provider_id = 0` ⇒ 扫成**非 nil 指针指向 0** ⇒ JSON 是
   `provider_id: 0`（**键在**）；DB 里 `NULL` ⇒ 键被**省略**。
   ⇒ ⇒ `if (row.provider_id)` 会把**真实的 0** 误判成「没有值」；
     判据必须写 `'provider_id' in row`。
   ⇒ ★ 与第八十批 `database` 块（值类型 + 无 omitempty ⇒ 恒 0）正好相反，
     两个方向在本仓都出现过 ⇒ **先看 struct tag，再看声明类型**。
3. ★★★★★ **读路径跨全部租户：SELECT 出 `tenant_id` 却从不过滤。**
   `:328` 用 `withAllTenantReadOnlyTx`，WHERE 只有 `anomaly_type` 与时间窗，
   **没有任何租户条件** ⇒ **本仓第五次「不隔离」**（批 75/76/77 共五次）。
   ★ 这次档位是 **superAdmin**，tenant_admin 够不着这条路由，危害面比前几次小；
     但平台超管看到的是**所有租户**的指纹漂移（含 `sample` 与 `context`）。
4. ★★★★ `anomaly_type` 在这个端点是**硬编码常量** ⇒ 可严格校验取值。
   `:336` 的 WHERE 写死 `anomaly_type = 'fingerprint_drift'`，而 SELECT 又把它扫进
   非指针无 omitempty 的字段 ⇒ **每行必然等于该字面量**。
   ⇒ 这是本族**唯一**能严格校验取值的字符串键。
5. ★★★★ `severity` 有注释声明的四值域，但**表上没有任何 CHECK 约束**。
   `462:52` 写的是 `severity TEXT NOT NULL DEFAULT 'low'  -- low | medium | high | critical`，
   `baseline/01-schema.sql:9654` 同样没有 CHECK。
   ⇒ 取值域只存在于**两处注释**：`integrity/signals.go:80` 的 `Severity` 常量族，
   与 `bg/integrity_probe_sink.go:49-51` 的硬编码 `"high"` / `"low"`。
   两条写入路径都在四值域内 ⇒ 可校验，但**依据是代码不是约束**。
   ⇒ ★ 与批 78 的 `no_candidates`（不在 CHECK 内 ⇒ 那个分支项不可达）同族：
     **注释里的取值域不等于数据库约束。**
   ⇒ ★ 反例提醒：`domains/analysis/optimizer.go` 用的是**另一套** severity
     （`warn` / `info` / `action_required`）⇒ 同名不同域，按字段名归类会出错。
6. ★★★★ `days` 与 `limit` 都是**静默回落**，不是 400。
   ```go
   days := queryInt(r, "days", 7);   if days <= 0 || days > 30  { days = 7 }
   limit := queryInt(r, "limit", 200); if limit <= 0 || limit > 500 { limit = 200 }
   ```
   `queryInt`（`handler.go:1534-1544`）用 `strconv.Atoi`，解析失败**直接返回缺省**
   ⇒ `days=31` / `0` / `abc` / `" 7"` **四种写法都拿到 `days=7` 的数据**。
7. ★★★ `days` 回显的是**生效窗口**，不是请求值 ⇒ 请求 31 拿到 7 并标着 7。
   ★★ 与批 76 `errors/trend` 的 `range` 回显**请求值**正好相反
   （那个是回显 `xyz` 却给 24h 数据）⇒ **同一仓两种回显口径，必须逐端点读。**
   ⇒ 移动端可直接拿 `resp.days` 当窗口长度，**不要**另存请求值。
8. ★★★ `count` 恒等于 `events.length`，`events` 恒数组（`make([]…, 0)`）。
   ★★ 响应**只回显 `days`、不回显 `limit`** ⇒ 截断只能靠「拿满请求上限」反推。
9. ★★ 错误信封嵌套 `{"error":{"detail":"…"}}`，三条固定文案：
   非 GET ⇒ 405 `method not allowed`；`h.db == nil` ⇒ **503 `database not configured`**
   （**dispatcher 层**，在子路径分发之前 ⇒ 与 `summary` / `events` 共享）；
   查询失败 ⇒ 500 `drift query failed`。
   ⇒ ★ 与批 79 `errors/trend` 的 `db_not_configured` 那条
     `database **is** not configured`（多一个 is）**措辞不同**，别抄错。

### 验证

- 用例 **125 条全绿**
- 变异 `/tmp/mut-co81.mjs` **57 条**，见下节
- 三门 rc=0；`vue-tsc` rc=0（一次通过）；`npm run build` rc=0
- U+FFFD 自查：源与用例均 0

### 变异验证暴露的判据缺陷（57 条 → 首跑 45 有牙，修到 51）

1. **★★★ 六条「判据函数里的守卫/分支」是可证冗余 ⇒ 等价变异，不是判据无牙。**
   本批 8 条 STILL_GREEN 里有 **6 条**属于这一类，形态各不相同：
   | # | 删掉的东西 | 为什么等价 |
   |---|---|---|
   | 16 | `Number.isFinite(days)` | `Infinity` 被 `<= 30` 挡住、`NaN` 被 `>= 1` 挡住 |
   | 20 | `Number.isFinite(limit)` | 同上（上下界各挡一个） |
   | 17 | `typeof days !== 'number'` | JS 里 `Number.isFinite(undefined)` 与 `(null)` **本来就返回 false** |
   | 33 | `key in row &&`（`=== 0` 前半） | `k in obj && obj[k] === 0` ≡ `obj[k] === 0` —— `in` 只在值为 `undefined` 时才有区别，而 `undefined === 0` 恒假 |
   | 36 | `Array.isArray(c)` 分支 | JSON 不产生稀疏数组，而 `Object.keys(A).length === 0` ⇔ `A.length === 0` |
   | 37 | `if (!('context' in row)) return false` | 键缺时 `c` 是 `undefined`，过完下面三条分支仍落到 `return false` |
   ⇒ ★★★ **规律**：判据函数（纯布尔、无副作用）里的前置守卫若**不改变任何一条分支的结果**，
     就是可证冗余。这类函数尤其容易出现，因为没有「提前返回以避免类型错误」的刚需。
   ⇒ ★★ 注意 #37 是「**守卫被下游兜住**」，而第七十九/八十批记的是「**下游兜住上游**」——
     两者都是等价变异，方向相反、结论相同：**兜住 = 等价**。
   ⇒ 处置一致：全部**保留**（都是可读的显式条件），在源文件注释里写明等价理由。

2. **★★★ 样本要挑「两种实现输出不同」的那一格（又一次，4 条）。**
   | 变异 | 我原来选的夹具 | 为什么不区分 | 换成的 |
   |---|---|---|---|
   | #35 `in` → `!!` | `context: {}` | `!!{}` 也是 true | `context: null`（键在但假值）|
   | #39 `!== undefined` → `!!` | `tenant_id: 'default'` | 非空串真值判定也 true | `tenant_id: ''`（空串被真值吞掉）|
   | #54 漏掉 `count` 条件 | 有行但 `count` 不对 | `events.length !== 0` 两边都 false | **无行但 `count=3`**（两边才分岔）|
   | #57 换成 `count===0 && days===0` | 有行但 `count=5` | 两边都 false | **零行窗口（`days=7`）**（变异体 `days===0` 不成立）|
   ⇒ ★ 与第八十批的教训同源：**「语义上最典型」的格子往往正是分不出来的格子**。

3. **★★ 锚点未同步（第六次）。**
   #54 / #57 补完夹具后忘了把变异指过去 ⇒ 又一次同一形态。
   ⇒ 与批 76/77/78/79/80 连续六批同坑，已固化为流程的原子步骤：
     **「加夹具」与「改锚点」必须一起做**。

4. **★ 判据的反面形态也要单独一条用例。**
   `driftIsUnresolved` 写成 `resolved===false && !resolved_at && !resolution_notes`
   ⇒ 漏掉 `resolution_notes` 那一项时，我只有「有 `resolved_at`」的用例
   （两种实现都 false）⇒ 指不到牙；
   补「**没有 `resolved_at` 但有 `resolution_notes`**」后立刻有牙。
   ⇒ ★ 与「判据有两个必要条件各要一条」同族：
     **每个合取项都要有一条「只让那个项不同」的用例。**

---

## 11.118 响应格式异常：明细 + 汇总（第八十二批，2026-10-08）

- 新增 `web-mobile/src/api/formatAnomalies.ts`
- 新增 `web-mobile/src/api/formatAnomalies.test.ts`（**137 条**）
- 覆盖 `GET /api/admin/format-anomalies` 与 `GET /api/admin/format-anomaly-summary`
- **鉴权：`admin/handler.go:913` / `:914` 两个都是 `h.superAdmin`**
  ⇒ 抽屉席须设 `requiresRole: 'super_admin'`，并同步 `AppDrawer.spec.ts` 白名单
- ★ `:915` 的 `format-anomalies/{id}/resolve` 是 **POST 写操作** ⇒ 本模块**不碰**
- 实现：`admin/format_anomalies.go`（list `:51-192`、summary `:194-267`）
- 数据表：`response_format_anomalies`（`454_response_format_anomalies.sql`）

### ★★ 这两张表不是同一张（批 81 的注释容易让人误会）

`admin/model_integrity.go:48-49` 说 `ModelIntegritySummary` "Mirrors the SQL used by
/api/admin/format-anomaly-summary" —— 指的是**聚合口径相似**，
底表是 `response_format_anomalies` 而**不是** `model_integrity_events`。
两表连 `severity` 的建表缺省都不同（`medium` vs `low`）⇒ 跨表比较前必须逐列对。

### 本族最要紧的十五件事

1. ★★★★★ **`count` 是「全表命中数」，不是本页长度 —— 与批 78/81 语义相反。**
   `:100-111` 先跑**独立的** `SELECT COUNT(*)`，再跑带 `LIMIT/OFFSET` 的列表查询。
   ⇒ 恒成立的不变式是 **`count >= anomalies.length`**。
   ⇒ ★★ 批 78 的 `count == tasks.length`、批 81 的 `count == events.length` **在这里不成立**
     ⇒ **判据不能跨族照抄**（本仓 `count` 已见三义）。
2. ★★★★ `limit` 与 `offset` **都回显** ⇒ 分页可精确判定（本族能判「还有下一页」，
   批 79/80/81 都只能靠「拿满上限」反推）。`limit` 缺省 50 / 上界 500；
   `offset` 缺省 0、**无上界**（`offset=999999` 照发，返回空数组但 `count` 仍是全量）。
3. ★★★★ `provider_code` 走 `LEFT JOIN providers` 补值，**展示与过滤用同一个 COALESCE**
   （`:119` 与 `:81`）⇒ ★ 可自验：**`provider_code` 键缺失的行不可能是 provider 过滤的结果**
   （`NULL = $1` 不为真）。
4. ★★★★ `response_structure` 是 `map[string]any` + omitempty ⇒ **空 map 整个键被省略**
   ⇒ **第十种 nil 编码的第二次出现**（批 79 的 `by_supplier` 同款）
   ⇒ 推论：「键在」蕴含「非空」⇒「结构为空」这个判据**恒真**，按纪律**不提供**。
5. ★★★★ `request_id` 是**非指针 string** ⇒ 恒在键，**但可以是空串**
   （建表 `NOT NULL` 只保证不是 NULL）⇒ 与批 81 的 `RequestID *string` **正好相反**。
6. ★★★ Go 字段 `ContentSize` 的 JSON 键是 **`content_size_bytes`**。
   ⇒ ★★★ 它是**条件键** ⇒ **写错键名不抛错、静默返 `undefined`**
   ⇒ 模块提供 `formatNumericValue()` 作为取值入口，spec 里有专属用例钉住。
7. ★★★★ `anomaly_type` 与 `severity` 在这张表都是**开放域**：写入入口
   `RecordDataAnomaly(ctx, anomalyType, severity, …)` **接受任意字符串**，建表也没有 CHECK。
   ⇒ ★★★ 与批 81 正好相反（那里 `anomaly_type` 被 SQL 硬编码 ⇒ **可**严格校验）。
   ⇒ ⇒ 只提供「是否落在已知集合内」（12 个字面量）的判据，解包器**不**拒绝未知取值。
8. ★★★ 两张表的 `severity` **建表缺省不同**：`medium` vs `low`。
9. ★★★★ summary 的三个 AVG 是 `*float64` + omitempty ⇒ **`AVG(...)` 返回 NULL 时键被省略**
   （不是 0、不是 null）⇒ 算均值时不能把键缺当 0 参与求和。
10. ★★★ `COUNT(DISTINCT request_id)` ⇒ 恒 **`affected_requests <= anomaly_count`**；
    `FILTER (WHERE resolved)` ⇒ 恒 `resolved_count <= anomaly_count`。
11. ★★★★ 排序是 `ORDER BY hour DESC, anomaly_count DESC` —— **不是**整体按计数降序。
    ⇒ ★★★ 只能断言「`hour` 全局非增」+「**同一 hour 内** `anomaly_count` 非增」。
    ⇒ 这是本族最容易被误用的不变量。
12. ★★★ summary 的 `LIMIT 200` **硬编码在 SQL 里**（`:231`），不参数化、不回显
    ⇒ 只能靠 `count === 200` 反推可能被截断（与 list 的可调 `limit` 鲜明对照）。
13. ★★★ `hours` 回显生效值，上界是 **`24*30 = 720`** 小时
    ⇒ 与批 81 的 `days <= 30` 是两套窗口语义。
14. ★★★★ **503 检查排在 405 检查之前**（`:52` 在 `:56`）
    ⇒ 「方法不是 GET **且** db 未配置」时拿到 **503 而不是 405**。
15. ★★★★ 三条 500 文案**各不相同**，客户端可区分失败发生在哪一步：
    `count query failed` / `list query failed` / `summary query failed`
    ⇒ ★★ 与第八十批「两条子路径文案**完全相同**」正好相反。

★ 又一次 `withAllTenantReadOnlyTx`（`:106` / `:143` / `:213`）⇒ **本仓第六次「不隔离」**。

### 验证

- 用例 **137 条全绿**
- 变异 `/tmp/mut-co82.mjs`，见下节
- 三门 rc=0；`vue-tsc` rc=0（首跑报 1 条 `TS2345`：用例里用错类型的夹具）；`npm run build` rc=0
- U+FFFD 自查：源与用例均 0

### 变异验证暴露的判据缺陷（67 条 → 首跑 59 有牙，修到 65）

1. **★★★ 两条 STILL_GREEN 是可证等价，其余 7 条首跑绿全是夹具与锚点问题。**

   **可证等价（2 条，保留 + 注释写明理由）**：
   | # | 变异 | 为什么等价 |
   |---|---|---|
   | 20 | 去掉 `formatLimitIsSendable` 的 `Number.isFinite` | `Infinity` 被 `<= 500` 挡住、`NaN` 被 `>= 1` 挡住 |
   | 53 | 把 `request_id === ''` 改成 `request_id.length === 0` | `request_id` 是**七恒在键之一**且解包器已校过是 string ⇒ 该判据**不可能**收到 `undefined` |

   **夹具/锚点（7 条，逐条换夹具或改锚点后有牙）**：
   | 变异 | 问题 | 修法 |
   |---|---|---|
   | #11 整型条件键列表丢掉 `content_size_bytes` | 锚点指的是 `provider_id` 类型错那条（两条共用循环） | 锚点改到专属的 `content_size_bytes` 类型错用例 |
   | #31 删掉两个 time 字段的类型检查 | 锚点指「缺 `created_at`」，被 `requireKeys` 兜住 | 锚点改到「键齐全但类型错」那条 |
   | #42 「还有下一页」漏掉 `offset` | `offset=0` 时两式相同 | 补「**中间页**（`offset>0`）」并把锚点指过去 |
   | #48 `'k' in row` → `!!row[k]` | `context` 那条用 `{}`，`!!{}` 也是 true | 补「**键在但值是 `null`**」那一格（唯一能区分的输入） |
   | #55 已知类型改成大小写不敏感 | 用的未知值小写形态也命中不了 | 补「**大写变体**」那条 |
   | #59/#60 | **两条是同一个变异**（`from`/`to` 完全相同） | 删掉重复的 #59 |
   | #61 | `from` 与 `to` 只差一句注释 ⇒ **行为根本没变** | 改成真变异（`>=` → `<=`） |

   ⇒ ★★ #61 是「**注入标记 ≠ 变异**」的又一例：我以为改了一行，其实 `from`/`to` 只差注释，
     `ORIG.replace` 产出的文件与原文件行为一致 ⇒ 必然 STILL_GREEN。
     **dry 阶段的 `NO_EFFECT` 检查只能发现「完全没变」，发现不了「只变了注释」。**

2. **★★ 改源码会让变异的 `from` 片段失配。**
   为修构建门（`noUncheckedIndexedAccess` 下 `TS2345`）把
   `if (typeof requestedHours !== 'number' …) / Math.trunc(requestedHours)`
   改成局部常量 `asked`，⇒ 变异 #67 / #68 的 `from` 立刻 `NO_MATCH`。
   ⇒ ★ 收尾顺序必须是：**先把源码改到最终形态，再跑一次变异**，
     或改完源码后**逐条确认 `from` 仍能匹配**（脚本的 `NO_MATCH` 分支正是为此存在）。

3. **★ `npm run build` 的类型门比 `vue-tsc` 更严（又一次，且是同一形态）。**
   `vue-tsc --noEmit` rc=0，但 `npm run build` 报
   `src/api/formatAnomalies.ts(596,21): error TS2345: 'number | undefined' is not assignable to 'number'`。
   ⇒ 这是可选参数在严格模式下**没有**被 `typeof !== 'number'` 收窄到位；
     显式 `const asked: number | undefined = requestedHours` 之后两者都绿。
   ⇒ ★ 推论：**「vue-tsc 通过」不等于「构建通过」**，两个门都要跑。

4. **★★ 条件键写错键名不会被解包器发现 ⇒ 必须给取值入口。**
   `content_size_bytes`（键名与 Go 字段 `ContentSize` 不一致）是**条件键** ⇒ 缺键合法 ⇒
   用错键名（如 `content_size`）**不抛错**、静默拿到 `undefined`。
   ⇒ 模块提供 `formatNumericValue(row, key)` 作为唯一取值入口，
     并写两条用例钉住：写错键名拿 `undefined`、走入口拿 2048。
   ⇒ ★ 与已记的「`x ?? null` 会把字段名打错整个吞掉」同源，
     但这次是**解包器的设计使然**（条件键不该强制存在）⇒ 必须在 API 层补入口。

5. **★★ 一个族里的两个端点，503/405 的检查顺序可能不同。**
   本族把 `h.db == nil` 放在方法检查**之前**（`:52` 在 `:56`）⇒ 「非 GET 且 db 未配置」得 503。
   ⇒ 与批 81 的 dispatcher 表现相同，但**本族在 handler 里自己也有这道检查**（不只在 dispatcher）
   ⇒ 这种顺序客户端观察不到（除非两种错误同时发生），但值得记下来。
