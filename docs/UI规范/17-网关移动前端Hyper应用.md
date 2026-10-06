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
