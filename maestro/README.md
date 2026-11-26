# Maestro 逐页浏览器验证资产（llm-gateway-go 管理后台）

本目录是「逐页浏览器验证」的 **Maestro flow 资产**。

> ★ **执行状态声明（重要，先读这段）**
>
> - **2026-10-28（R176）起：本目录的 flow 已经真实跑过。** 10 个 flow（112 条运行时路由）全部在
>   本机 Chrome 156 + Maestro 2.11.0 上实跑，读数见 `docs/audit/2026-10-28-R176-Maestro逐页实跑.md`。
> - **但「跑过」不等于「全绿」**：实跑暴露了 **9 处资产自身的错** 与 **2 处产品缺陷**，
>   本轮已逐条修正（详见审计档「失败清单」一节）。资产里写的「跑法」在实跑前**三处是错的**
>   （见下面「★ 跑法一节的三个坑」）。
> - **Maestro CLI 的能力是实测出来的，不是读文档读出来的**：
>   `url:` 头判定 web flow、`evalScript` 跑在 Maestro 侧 JS 沙箱（**页面里没有 `location`/`performance`**）、
>   `--env-file` **不存在**、`-e KEY=VALUE` 是唯一的变量通道 —— 这四条都只有实跑才能发现。
> - 因此：**下面所有 flow 的语法结构已用 YAML 解析器逐个校验通过；所有断言文案已逐条回源码核对并按实跑结果订正。**
>
> ★ **跑法一节的三个坑（第一次实跑时踩到的）**
>
> | # | 坑 | 正确做法 |
> |---|---|---|
> | 1 | YAML 头写 `appId: ${BASE_URL}` ⇒ 被判成**移动端** flow，去「安装」http://127.0.0.1:8782 | 头必须写 **`url: ${BASE_URL}`**（`YamlConfig.getUrl` ⇒ `FileUtils.isWebFlow`） |
> | 2 | `--env-file maestro/env.local.yaml` ⇒ **CLI 里没有这个选项**；且 shell 环境变量**不参与** flow 的 `${}` 解析 | 用 **`-e KEY=VALUE`** 逐个传；仓库提供了 `maestro/run-local.sh` 代劳 |
> | 3 | 加 `-p web` ⇒ 「0 devices connected」 | **不要加 `-p`**：让 Maestro 从 flow 内容自动判定 web；用 `--driver-host-port <port>` 连一个已开的 CDP Chrome |

---

## 1. 路由清单（唯一来源：`web/src/router.ts`）

### 1.1 实测统计

> ★ **2026-11-23 口径重算（L10 复核轮）**：本表的数字**改由工具产出**，不再是手工抄的。
> ⇒ 复核命令（输出即本表全部数字）：
> ```bash
> node maestro/tools/audit-route-coverage.mjs --json     # 机读 JSON
> node maestro/tools/audit-route-coverage.mjs --check    # ★ 与本表对拍，漂移即红（退出码 1）
> ```
> ⇒ ★ **工具口径的可信度已被两点自证**：
>   ① 把同一工具指向本表**上一次（上一轮之前）**更新所在提交（`bd8d39757`）的 `router.ts`
>      ⇒ 12 个统计量与**那时**的公布值**逐项相等**（125/5/130/19/19/2/17/113 + 53/47/11/2）；
>   ② 把同一工具指向本表**上一轮**更新所在提交（`8e0baf950`）的 `router.ts`
>      ⇒ 12 个统计量与**上一版的**公布值**逐项相等**（129/5/134/20/22/2/17/117 + 54/50/11/2）。
>   ⇒ ⇒ **两个基准点都复现** ⇒ 当前树上的新数是**同一把尺子量出来的**，
>     不是「另一套口径的另一组数」。
>   详见 `maestro/L10-uncovered-routes.md` §2（含逐项对照表与复现命令）。
>
> ★ **数字为什么变（不是口径漂移，是树真的多了路由）**：**两次**上游漂移，逐条归因：
>   ① **第一次**（2026-11-20/21）：上游新增 4 条路由
>      （`/admin/error-playbook`、`/admin/error-playbook/:id`、
>      `/admin/analytics/:dimension/:id`、`/admin/board-operational/:dimension`），
>      2026-11-23 审计轮把 `/admin/session-analytics` 由「总览页」改成**重定向**
>      ⇒ path 条目 125→129、运行时 130→134、纯 redirect 19→20、动态段 19→22。
>   ② **第二次**（2026-11-23，上游 `41fb572f3`「团队层级管理前端切片落地」）：
>      新增 `/admin/teams` 与 `/admin/teams/:id`（两条都带 `meta: { requiresAuth: true }`）
>      ⇒ path 条目 129→**131**、运行时 134→**136**、动态段 22→**23**、
>        需要登录 117→**119**、任意登录用户档 54→**56**
>        （纯 redirect 仍 20、公开仍 17 ⇒ 这两项不变）。
>   ③ **第三次**（2026-11-23，**L14 落地轮**）：★ L14 把用户画像列表页挂回原 URL、
>      **另立整合页** `/admin/session-analytics/users/overview`（★ `router.ts:362`）
>      ⇒ ★ path 条目 131→**132**、运行时 136→**137**、需要登录 119→**120**、
>        任意登录用户档 56→**57**（★ 纯 redirect 仍 20、动态段仍 23、公开仍 17）
>      ⇒ ★★★ **★ 而这一次的漂移【当场被 `--check` 抓到】（★ 前两次没有工具、★ 这次有）**：
>        ★ 落地时只改了 `router.ts`、★ **`maestro/README.md` 一个字没改**
>        ⇒ ★★ `--check` 的 5 条判据红（★ 每条都正好差 1）⇒ ★★ ⇒ **红灯在、★ 但它没进闸门**

| 指标 | 数值 | 来源 |
|---|---|---|
| `path:` 条目 | **132** | `web/src/router.ts` 的 `routes:` 数组（行 241–468）**AST 逐条计数**。★ 不能用正则数：`redirect: { path: '/x' }` 里的 `path:` 也会被数上；且 dev-only 的 3 条是 `...(import.meta.env.DEV ? [...] : [])` 条件展开，正则与朴素 AST 都会漏 |
| `externalMaintainRedirect(...)` 调用 | **5** | `router.ts`（`/ops/licenses`、`/ops/faults`、`/ops/autoupdate`、`/ops/center`、`/ops`） |
| **运行时路由总数** | **137** | 132 + 5 |
| 其中纯 redirect（没有自己的组件，只跳走） | **20** | 数组内带 `redirect:` 的条目（其中 1 条是 catch-all `/:pathMatch(.*)*`）。★ 含 6 条**箭头函数形态**的 redirect（`/catalog`、`/maas/*` 族） |
| 其中带命名动态段（`:param`，不含 glob） | **23** | 见 §1.3 |
| 其中 glob 兜底（`:pathMatch(.*)*`） | **2** | `/maintain/:pathMatch(.*)*`、`/:pathMatch(.*)*` |
| **不需要登录** | **17** | 12 条 `meta.public === true`（含 3 条 dev-only `/dev/*`）+ 5 条「自身无 `meta`、但重定向到公开页」的路由（见下方口径说明） |
| **需要登录** | **120** | 137 − 17 |

需要登录的 119 条，按 `router.ts` 的 `beforeEach` 再分档：

| 档位 | 条数 | 守卫出处 |
|---|---|---|
| 任意登录用户（目标路由无 `meta` 权限位，**或**显式 `requiresAuth`） | **57** | `router.ts` 的 `isAuthed()` 早退（无 meta 的 35 + `requiresAuth` 族的 21） |
| `requiresSuper`（仅 super_admin） | **50** | `router.ts` 的 super 闸 |
| `requiresPlatformOps`（super_admin + default 租户） | **11** | `router.ts` 的 platformOps 闸 |
| `requiresProviderConsole`（super_admin 或 default 租户 tenant_admin） | **2** | `router.ts` 的 providerConsole 闸 |

> 57 + 50 + 11 + 2 = **120** ✔ 与「需要登录 120」自洽（可作口径自检）。

> ★ **计数口径（重要，和「直接看 `meta`」会差 5 条）**：
> vue-router 4 在 `pushWithRedirect()` 里**先**执行 `handleRedirectRecord()`、**后**才跑导航守卫
> （`web/node_modules/vue-router/dist/vue-router.mjs:1282-1296`，★ `handleRedirectRecord` 的定义在 1262），
> 所以一条 `redirect:` 路由**由它跳转到的目标路由的 `meta` 决定**要不要登录
> —— ★ **仓内判据**就是 `beforeEach` 里的这一行：`router.ts:556` 的 `if (!to.meta.public && !isAuthed())`
> （★ **复核轮订正**：原写的「`router.ts:241-246` 的注释」**在任何提交上都不存在**
>   ⇒ 同属「手抄行号」那类缺陷 ⇒ **行号要么逐条核验，要么别给**）。
> 于是 5 条「自身无 `meta`、但重定向到公开页」的路由实际**不需要登录**：
> `/customer/site`、`/customer/activate`、`/customer/license`、`/customer/agreement`
> （→ `/customer/update-activate`，★ 当前树 `router.ts:436-439`）与 `/:pathMatch(.*)*`
> （→ `/`，★ `router.ts:467`）。
> 这 5 条被放进了 `01-public-guest.yaml`（不登录的 flow）。
>
> 另外：`/ops/*` 那 5 条**不是** `redirect:` 路由，而是 `externalMaintainRedirect()` 造的
> 「`component: {render: () => null}` + `beforeEnter` 里 `window.location.replace`」的路由记录（`router.ts:117-127`），
> 所以守卫按**它们自己**的 `meta`（无 ⇒ 需要登录）判，但还会被 `router.ts:488` 的 `startsWith('/ops')` 规则改写。
>
> 17 条公开路由清单：`/login`、`/forbidden`、`/`、`/bootstrap`、`/customer/update-activate`、
> `/customer/site`、`/customer/activate`、`/customer/license`、`/customer/agreement`、`/customer/offline-activation`、
> `/maintain`、`/maintain/:pathMatch(.*)*`、`/:pathMatch(.*)*`、`/public/quote/:token`、以及 3 条 dev-only 的 `/dev/*`。

### 1.2 纯 redirect 路由（20 条）

这 20 条**没有自己的组件**，断言点落在它们跳转到的目标页面上（这样「跳对了」和「目标页渲染出来了」是同一条证据）。

> ★ **2026-11-23 复核轮修正**：① 本节「router.ts 行」列此前是**手抄**的，
>   抄自 2026-10-18 前后的某个快照 ⇒ **在任何提交上都对不上**
>   （逐提交扫描 `web/src/router.ts` 的历史：`/catalog` 只在 `d82a9fb06` 是 220，
>   而同一次扫描里 catch-all 是 374、不是本节写的 435 ⇒ 两个值来自**不同时期**）
>   ⇒ 现已全部改为**当前树**的 `path:` 字面量所在行；② 正文原写「这 **19** 条」，
>   与标题「20 条」自相矛盾 ⇒ catch-all `/:pathMatch(.*)*`（`{ path: ..., redirect: '/' }`）
>   **也没有自己的组件** ⇒ 正确值是 **20**。

| 源路由 | 跳转目标 | router.ts 行（当前树 `path:` 行） |
|---|---|---|
| `/catalog` | `/models` | 270 |
| `/probe-health` | `/routing-v2/credentials` | 276 |
| `/system-monitor` | `/dashboard` | 280 |
| `/routing-v2/annotations` | `/routing-v2/auto-ops` | 289 |
| `/routing-v2/annotations/stats` | `/routing-v2/auto-ops` | 290 |
| `/routing-v2/task-profile` | `/routing-v2/auto-ops` | 297 |
| `/routing-v2/auto-tuning` | `/routing-v2/auto-ops` | 298 |
| `/maas/models` | `/tenant/models` | 324 |
| `/maas/account` | `/tenant/account` | 325 |
| `/maas/pricing` | `/tenant/pricing` | 326 |
| `/maas/orders/:id` | `/tenant/orders/:id` | 327 |
| `/maas/usage` | `/tenant/usage` | 328 |
| `/routing` | `/routing-v2` | 334 |
| `/admin/session-analytics` | `/admin/session-analytics/users/overview` | 373（★ 2026-11-23 审计轮：原「会话分析总览页」按 owner 指令取消，内容已重组迁入【整合页】⇒ 路由改为重定向，收藏链接兼容；★ ★ L30 轮·补②④：★ L14 轮把整合页迁到 `/users/overview` 后，本重定向随之改指 —— ★ 否则旧 URL 落到【列表页】、且打断 `16-session-analytics-system.yaml` 的 3 条断言） |
| `/operations/price-compare` | `/model-pricing` | 412（2026-11-19 合并轮：价格对比面并入比较与定价页） |
| `/customer/site` | `/customer/update-activate` | 436 |
| `/customer/activate` | `/customer/update-activate` | 437 |
| `/customer/license` | `/customer/update-activate` | 438 |
| `/customer/agreement` | `/customer/update-activate` | 439 |
| `/:pathMatch(.*)*` | `/` | 467 |

### 1.3 带命名动态段的路由（23 条）

每条在 flow 里都用**一个具体的样例 id** 占位，占位值可用环境变量覆盖（对照表见 §5）。

| 路由 | 动态段名 | flow 里用的 URL |
|---|---|---|
| `/live/request/:requestId` | `requestId` | `/live/request/${REQUEST_ID}` |
| `/providers/:id` | `id` | `/providers/${PROVIDER_ID}` |
| `/tenants/:tenantId` | `tenantId` | `/tenants/${TENANT_ID}` |
| `/tenant/orders/:id` | `id` | `/tenant/orders/${ORDER_ID}` |
| `/maas/orders/:id` | `id` | `/maas/orders/${ORDER_ID}` |
| `/routing-v2/work-types/:key` | `key` | `/routing-v2/work-types/${WORK_TYPE_KEY}` |
| `/keys/:id` | `id` | `/keys/${API_KEY_ID}` |
| `/request-detail/:requestId` | `requestId` | `/request-detail/${REQUEST_ID}` |
| `/admin/session-analytics/users/:owner` | `owner` | `/admin/session-analytics/users/${SESSION_OWNER}` |
| `/admin/session-analytics/clients/:id` | `id` | `/admin/session-analytics/clients/${CLIENT_ID}` |
| `/admin/session-analytics/tasks/:id` | `id` | `/admin/session-analytics/tasks/${TASK_ID}` |
| `/admin/error-playbook/:id` | `id` | `/admin/error-playbook/${PLAYBOOK_ID}`（★ 2026-11-21 错误处理手册轮新增；**当前无 flow 覆盖**，见 `maestro/L10-uncovered-routes.md` §3.1） |
| `/admin/board-operational/:dimension` | `dimension` | `/admin/board-operational/${DIMENSION}`（★ 2026-11-20 运营看板详情轮新增；**当前无 flow 覆盖**） |
| `/admin/analytics/:dimension/:id` | `dimension` + `id` | `/admin/analytics/${DIMENSION}/${ID}`（★ 2026-11-20 运营看板详情轮新增；**当前无 flow 覆盖**） |
| `/admin/session-analytics/user-ranking/:owner` | `owner` | `/admin/session-analytics/user-ranking/${SESSION_OWNER}`（★ 2026-11-20 会话分析前端轮新增；同名的 `${SESSION_OWNER}` 变量由 `run-session-analytics.sh` 从**真数据**解析） |
| `/public/quote/:token` | `token` | `/public/quote/${QUOTE_TOKEN}` |
| `/admin/approvals/:id` | `id` | `/admin/approvals/${APPROVAL_ID}` |
| `/admin/sessions/:id` | `id` | `/admin/sessions/${SESSION_ID}` |
| `/admin/request-actions/:id` | `id` | `/admin/request-actions/${REQUEST_ID}` |
| `/admin/request-registry/journey/:requestId` | `requestId` | `/admin/request-registry/journey/${REQUEST_ID}` |
| `/admin/connection-registry/:credentialId/recovery` | `credentialId/recovery` | `/admin/connection-registry/${CREDENTIAL_ID}/recovery` |
| `/dev/live-request-preview/:requestId` | `requestId` | `/dev/live-request-preview/${REQUEST_ID}` |
| `/admin/teams/:id` | `id` | （★ 2026-11-23 上游 `41fb572f3`「团队层级管理前端切片落地」新增；**当前无 flow 覆盖**，且 `appNav.ts` 未挂菜单入口 ⇒ 见 §3.13） |

---

## 2. Flow 清单

每个 flow 是一个 YAML 文件，flow 内用 `openLink` 逐页串起来；需要登录的 flow 开头用 `runFlow` 调共享登录流。

| flow 文件 | 覆盖 | 页数 | 登录 | 说明 |
|---|---|---|---|---|
| `maestro/flows/01-public-guest.yaml` | 公开页 / 访客面（无需登录） | 12 | 否 | router.ts 里 `meta.public === true` 的页面 + 4 条 `/customer/*` 重定向 + `/maintain` 兜底页 + 404 兜底。 |
| `maestro/flows/02-overview-live.yaml` | 总览 / 实时拓扑 / 接入引导 | 6 | 是 | 首页、仪表盘、实时拓扑、请求详情全页、示例、对话。 |
| `maestro/flows/03-providers-models.yaml` | 供应商 / 模型 / 定价 | 9 | 是 | providerConsole 与 platformOps 档的供应商、模型目录、定价、免费池、密钥申请。 |
| `maestro/flows/04-tenants-portal.yaml` | 租户 / 租户自助门户 | 13 | 是 | 租户管理、用户管理、`/tenant/*` 门户与 `/maas/*` 旧路径重定向。 |
| `maestro/flows/05-routing.yaml` | 路由域 | 22 | 是 | `/routing-v2` 工作台、凭据监控、探测健康、工作类型、AUTO 运营、路由策略与关联分析。 |
| `maestro/flows/06-keys-requests.yaml` | 密钥 / 请求日志 / 会话分析 | 9 | 是 | API 密钥、请求日志与全屏请求详情、用户/客户端/任务画像。 |
| `maestro/flows/07-admin-config.yaml` | 后台配置域 | 13 | 是 | 系统设置、模块、Agent 注册表、审批、清洗规则、合规、压缩、数据生命周期。 |
| `maestro/flows/08-admin-data-ops.yaml` | 后台数据 / 用量 / 审计域 | 19 | 是 | 用量三件套、运营与运维总览、订阅层级、轮次、会话、请求注册表、连接注册台、对账、审计。 |
| `maestro/flows/09-ops-maintain.yaml` | 运维中心外跳 / maintain 兜底 | 6 | 是 | `/ops/*` 全页外跳到 maintain SPA；Maintain 不可用时守卫改写到 `/customer/update-activate`。 |
| `maestro/flows/90-dev-only.yaml` | 仅 dev 构建存在的路由 | 3 | 否 | router.ts 里 `import.meta.env.DEV` 包裹的三条路由，生产构建不注册 ⇒ 单独成 flow，默认不参与批量跑。 |
| `maestro/flows/12-live-stage-tree.yaml` | 请求阶段树（`/live2`）+ `/live` 顶栏入口 | 2 | 是 | 阶段树页 + `/live` 页的「请求阶段树」顶栏入口。★ 2026-11-17 新增：`/live2` 此前**零 Maestro 覆盖**（路由在、页面从没被浏览器验过）。 |
| `maestro/flows/13-session-analytics-users.yaml` | 会话分析「用户画像」族（`/admin/session-analytics/users`） | 2 | 是 | ★ 2026-11-20 数字出处轮：用户画像列表页 + 详情页（**上游资产**，与本轮的聚合分析族路径不重叠）。openLink 步数 3。 |
| `maestro/flows/13-board-operational.yaml` | 运营与运维总览（`/admin/board-operational`） | 1 | 是 | openLink 步数 1。 |
| `maestro/flows/14-user-analytics-enhancement.yaml` | 用户分析增强（画像下钻） | 2 | 是 | openLink 步数 2。 |
| `maestro/flows/14-stats-consistency-test.yaml` | 统计口径一致性 | 5 | 是 | openLink 步数 5。 |
| `maestro/flows/15-board-probe-scope.yaml` | 看板探测口径筛选 | 2 | 是 | openLink 步数 2。 |
| `maestro/flows/16-session-analytics-system.yaml` | **会话分析系统聚合族**（总览 / 人员排行 / 人员详情 / 智能体排行 / 模型排行） | 5 | 是 | ★ 2026-11-20 会话分析前端交付轮新增：本轮 5 个新页面的**第一层**（浏览器真的打开并渲染）。第二层数字对拍在 `maestro/tools/assert-session-analytics.mjs`（两层分工见该 flow 头注）。openLink 步数 6。★ **必须经 `maestro/run-session-analytics.sh` 跑**（它负责喂动态段 owner、钉 zh-CN 界面语言、起全新 Chrome）—— 前置条件见 §4.4。 |
| `maestro/flows/18-stats-range-and-scope-hint.yaml` | **时间范围统一 + 统计说明提示符**（看板 / 会话统计 / 会话分析用户页 / 运营面板） | 4 | 是 | ★ 2026-11-24~25 统一轮新增：第一层证明「浏览器真的能打开这四页、点到日期控件、面板真的展开」（判据全用 `data-testid` ⇒ 与界面语言解耦）。第二层语义对拍在 `maestro/tools/assert-stats-range-picker.mjs`（26 条）与 `maestro/tools/assert-board-op-custom-range.mjs`（5 条：自定义区间 + 7 档位逐个对拍）与 `maestro/tools/assert-board-op-list-deeplink.mjs`（4 条：**列表页** `/admin/board-operational/:dimension` 的 `?start=&end=` **深链**——深链到达后端 / 显示=数据 / 深链优先于 days / 默认 7 天；★ 2026-11-25 第 30 轮新增，★ 列表页并入共享日期组件后这条能力**只有它有** ⇒ 必须有独立证据）。★ **必须经 `maestro/run-stats-range-picker.sh` 跑**（它负责起全新 Chrome、顺延端口、注入凭据与视口，并按 `result.mjs` 的 `mergeExitCodes` 合并三层退出码）。★ **2026-11-25 起默认走「CDP 预置会话」**：runner 在 211 本机现签 JWT ⇒ 注入浏览器 ⇒ **完全不走 UI 登录** ⇒ 因此**不受生产 captcha 闸门约束**（机制与计数清零条件见 §6.4，实现与两态实测表见 §6.5.1）。 |
| `maestro/flows/19-request-logs-time-ranges.yaml` | **请求日志：切换时间范围后数据正确 ＋ 查询不超时 ＋ 加载态可见**（`/request-logs`） | 1 | 是 | ★ 2026-11-25 request-logs 根修轮新增：第一层证明「真实浏览器能点到八个档位、连续快速切换停在最后选的档、含探测口径、查询按钮与翻页，且每次都是加载收尾 / 有数据 / 无报错」（共享子流程 `_shared/request-logs-range.yaml`）。第二层语义对拍在 `maestro/tools/assert-request-logs-time-ranges.mjs`（请求窗口 == 档位窗口、响应窗口 == 请求窗口、界面 == 响应、被取代的 fetch 被中止、加载态可见有读数、多条件矩阵）。★ **必须经 `maestro/run-request-logs-time-ranges.sh` 跑**（同款预置会话 / 全新 Chrome / `mergeExitCodes`；并按 API 实况决定「今天」档位）。 |
| `maestro/flows/20-user-role-governance.yaml` | **租户详情页的「变更角色」＋ 非 default 租户不得是超级管理员**（`/tenants/:code`） | 3 | 是 | ★ 2026-11-26 角色治理轮新增（★ **2026-11-26 L135 补登记**：★ 该 flow 落地时漏登记进本表 ★）：在 `acme`（租户内无超管）验「建号下拉里没有超级管理员、且有普通用户」、在 `chenb` 验「变更角色入口存在、可打开、下拉只有租户管理员/普通用户」。★ **该页用 element-plus `<el-select>` ⇒ option 只在展开时渲染 ⇒ 「超级管理员不可见」可以用 `assertNotVisible` 干净断言**（★ 与 `21` 的口径**相反** ★）。 |
| `maestro/flows/21-users-page-role-governance.yaml` | **平台级用户目录页（`/users`）的「变更角色」入口可达性** | 1 | 是 | ★ 2026-11-26 L135 补真机覆盖轮新增：★ 用户诉求「**在用户管理中**增加变更角色功能」落在 `/users`（★ `router.ts:310` ⇒ `UsersView.vue` ★），而上一轮的真机证据只在租户详情页 ⇒ ★ 本 flow 补上这一页。★ ★ **该页用原生 `<select>` ⇒ option 常驻 DOM ⇒ 「超级管理员不可见」在该页是【恒红的假门】**（★ 必然被筛选区角色下拉的 option 命中 ★）⇒ ★★ **控件级断言改由 `maestro/tools/assert-users-role-options.mjs` 承担**（★ 读该 select 的 **option 集合**、★ 并用「同一控件在两个租户上得出相反结果」互为对照 ★）；★ 本 flow **只管真实鼠标可达性**（页能开 / 按钮能点 / 弹窗能开 / 「普通用户」在界面上真的可见 / 保存按钮在）。★ **必须经 `maestro/run-users-role-governance.sh` 跑**（三层合并：flow ＋ 探针 ＋ 探针 `--negate` 自证层）。★ **2026-11-26 改写：默认走「CDP 预置会话」**（runner 在 211 本机现签 JWT `maestro/tools/mint-jwt-token.py` ⇒ 注入浏览器 ⇒ **完全不走 UI 登录** ⇒ 不受生产 captcha 闸门约束；机制与计数清零条件见 §6.4）。 |
| `maestro/flows/_shared/login.yaml` | 共享登录流 | 1（`/login`） | — | 被其它 flow 用 `runFlow` 调 |

合计：**运行时路由 136 条**（§1.1 口径）。

> ★ **2026-11-20 订正（会话分析前端交付轮）**：原记「**112 条路由 = 112 条在 11 个功能 flow 里**
>   + 1 条 `/login` 在共享登录流里」——**已不成立**，两个原因：
>   ① 路由数随上游增长到 **136**（上游新增 8 条 + 本轮 5 条，见 §1.1 的重算表）；
>   ② `13-` / `14-` / `15-` 三个 flow **此前根本没登记进本表**（连同它们覆盖的路由）。
> ⇒ 本轮**只承诺**「`16-session-analytics-system.yaml` 覆盖的 5 条被浏览器打开过」；
>   其余路由的 flow 覆盖情况**未逐条复核**（登记为遗留项，见实施报告 §七）。
>   ★ 通用式（沿用 2026-11-17 的那条）：**「清单声称全覆盖」与「真的全覆盖」是两条独立的断言** ——
>     清单对得上数，不等于每条路由都被浏览器开过（少一条时两边都会自洽地绿）。

> `/` 被访问了两次：**访客态**（在 `01-public-guest`，断言落地页 hero 文案）与**登录态**（在 `02-overview-live`，断言仪表盘标题）。
> 原因是 `HomeView` 按登录态分叉（`web/src/views/HomeView.vue:1-8`：登录 ⇒ `DashboardView`，未登录 ⇒ `LandingView`），两种渲染结果不同，值得各验一次。

---

## 3. 逐条路由核对表

列含义：**路由 path** ｜ **是否需要登录**（按 `router.ts` 的 `meta` + `beforeEach` 判定）｜ **flow 文件** ｜ **断言点**（页面上必须出现的文案）｜ **源码位置**（该文案的出处）。

> 断言点全部是**该页面特有**的可见文案，没有一条是「页面加载成功」这类空断言。
> 文案本身要么是 `web/src/views/**` / `web/src/components/**` 里的硬编码中文，要么是语言包 `web/src/locales/zh-CN/**` 的值（表里标了 locale 键）。

### 3.1 `01-public-guest.yaml` — 公开页 / 访客面（无需登录）

router.ts 里 `meta.public === true` 的页面 + 4 条 `/customer/*` 重定向 + `/maintain` 兜底页 + 404 兜底。

| 路由 path | 需要登录 | flow 文件 | 断言点（可见文案） | 源码位置 |
|---|---|---|---|---|
| `/forbidden` | 否（公开） | `01-public-guest.yaml` | 403 - 访问被拒绝 | web/src/views/ForbiddenView.vue:29 → locale forbidden.title |
| `/` | 否（公开） | `01-public-guest.yaml` | 把公司里所有大模型调用，收进一个能管、能查、能关的入口 | web/src/views/LandingView.vue:100 → locale landing.title |
| `/bootstrap` | 否（公开） | `01-public-guest.yaml` | 本地网关安装激活 | web/src/views/bootstrap/BootstrapWizardView.vue:432 |
| `/customer/update-activate` | 否（公开） | `01-public-guest.yaml` | 更新与激活 | web/src/views/lifecycle/UpdateActivateView.vue:339 → locale nav.item.updateActivate |
| `/customer/site` | 否（公开）（重定向到 `/customer/update-activate`，守卫按目标路由的 meta 判） | `01-public-guest.yaml` | 更新与激活 | router.ts:343 → /customer/update-activate（断言落在 /customer/update-activate 页面）；web/src/views/lifecycle/UpdateActivateView.vue:339 → locale nav.item.updateActivate |
| `/customer/activate` | 否（公开）（重定向到 `/customer/update-activate`，守卫按目标路由的 meta 判） | `01-public-guest.yaml` | 更新与激活 | router.ts:344 → /customer/update-activate（断言落在 /customer/update-activate 页面）；web/src/views/lifecycle/UpdateActivateView.vue:339 → locale nav.item.updateActivate |
| `/customer/license` | 否（公开）（重定向到 `/customer/update-activate`，守卫按目标路由的 meta 判） | `01-public-guest.yaml` | 更新与激活 | router.ts:345 → /customer/update-activate（断言落在 /customer/update-activate 页面）；web/src/views/lifecycle/UpdateActivateView.vue:339 → locale nav.item.updateActivate |
| `/customer/agreement` | 否（公开）（重定向到 `/customer/update-activate`，守卫按目标路由的 meta 判） | `01-public-guest.yaml` | 更新与激活 | router.ts:346 → /customer/update-activate（断言落在 /customer/update-activate 页面）；web/src/views/lifecycle/UpdateActivateView.vue:339 → locale nav.item.updateActivate |
| `/customer/offline-activation` | 否（公开） | `01-public-guest.yaml` | 离线激活 | web/src/views/lifecycle/OfflineActivationView.vue:126 |
| `/maintain` | 否（公开） | `01-public-guest.yaml` | 503 · 运维平台不可用 | web/src/views/MaintainUnavailableView.vue:41 |
| `/maintain/:pathMatch(.*)*` | 否（公开） | `01-public-guest.yaml` | 503 · 运维平台不可用 | web/src/views/MaintainUnavailableView.vue:41 |
| `/:pathMatch(.*)*` | 否（公开）（重定向到 `/`，守卫按目标路由的 meta 判） | `01-public-guest.yaml` | 把公司里所有大模型调用，收进一个能管、能查、能关的入口 | router.ts:374 → /（断言落在 / 页面）；web/src/views/LandingView.vue:100 → locale landing.title |

### 3.2 `02-overview-live.yaml` — 总览 / 实时拓扑 / 接入引导

首页、仪表盘、实时拓扑、请求详情全页、示例、对话。

| 路由 path | 需要登录 | flow 文件 | 断言点（可见文案） | 源码位置 |
|---|---|---|---|---|
| `/` | 否（公开） | `02-overview-live.yaml` | 仪表盘 | web/src/views/DashboardViewV2.vue:97 → locale dashboard.title |
| `/dashboard` | 是 · 任意登录用户 | `02-overview-live.yaml` | 仪表盘 | web/src/views/DashboardViewV2.vue:97 → locale dashboard.title |
| `/live` | 是 · 任意登录用户 | `02-overview-live.yaml` | 实时拓扑 | web/src/views/LiveTopologyView.vue:236 → locale liveTopology.title |
| `/live/request/:requestId` | 是 · 任意登录用户 | `02-overview-live.yaml` | 请求详情 | web/src/views/RequestDetailPage.vue:41 → locale liveTopology.zoom.title |
| `/examples` | 是 · 任意登录用户 | `02-overview-live.yaml` | 接入指南 | web/src/views/ExamplesView.vue:310 → locale examples.title |
| `/chat` | 是 · 任意登录用户 | `02-overview-live.yaml` | 对话 | web/src/views/ChatView.vue:459 → locale chat.page.title |

### 3.3 `03-providers-models.yaml` — 供应商 / 模型 / 定价

providerConsole 与 platformOps 档的供应商、模型目录、定价、免费池、密钥申请。

| 路由 path | 需要登录 | flow 文件 | 断言点（可见文案） | 源码位置 |
|---|---|---|---|---|
| `/providers` | 是 · super_admin / default 租户 tenant_admin | `03-providers-models.yaml` | 供应商管理 | web/src/views/ProvidersView.vue:879 → locale providers.page.title |
| `/providers/:id` | 是 · super_admin / default 租户 tenant_admin | `03-providers-models.yaml` | 供应商名称（Name） | web/src/views/provider-detail/SettingsTab.vue:304 |
| `/models` | 是 · super_admin + default 租户 | `03-providers-models.yaml` | 模型与目录 | web/src/views/ModelsView.vue:834 → locale models.page.title |
| `/catalog` | 是 · super_admin + default 租户（重定向到 `/models`，守卫按目标路由的 meta 判） | `03-providers-models.yaml` | 模型与目录 | router.ts:220 → /models（断言落在 /models 页面）；web/src/views/ModelsView.vue:834 → locale models.page.title |
| `/model-pricing` | 是 · super_admin + default 租户 | `03-providers-models.yaml` | 定价管理 | web/src/views/StandardModelPricingView.vue:499 → locale standardModelPricing.page.title |
| `/pricing` | 是 · super_admin + default 租户 | `03-providers-models.yaml` | 成本价格 | web/src/views/PricingManagementView.vue:4 → locale pricingManagement.page.title |
| `/free-pool` | 是 · super_admin | `03-providers-models.yaml` | 写入免费池凭据（加密存 DB） | web/src/views/FreePoolView.vue:749 → locale freePool.keyForm.title |
| `/free-discovery` | 是 · super_admin | `03-providers-models.yaml` | 免费资源发现 | web/src/views/FreeDiscoveryView.vue:572 → locale freeDiscovery.page.title |
| `/key-applications` | 是 · super_admin | `03-providers-models.yaml` | 密钥申请 | web/src/views/KeyApplicationsView.vue:146 → locale keys.applications.title |

### 3.4 `04-tenants-portal.yaml` — 租户 / 租户自助门户

租户管理、用户管理、`/tenant/*` 门户与 `/maas/*` 旧路径重定向。

| 路由 path | 需要登录 | flow 文件 | 断言点（可见文案） | 源码位置 |
|---|---|---|---|---|
| `/tenants` | 是 · super_admin | `04-tenants-portal.yaml` | 租户管理 | web/src/views/TenantsView.vue:136 → locale tenants.list.title |
| `/tenants/:tenantId` | 是 · super_admin | `04-tenants-portal.yaml` | 模型管控 | web/src/views/TenantDetailView.vue:482 |
| `/users` | 是 · 任意登录用户 | `04-tenants-portal.yaml` | 用户管理 | web/src/views/UsersView.vue:316 → locale users.title |
| `/tenant/models` | 是 · 任意登录用户 | `04-tenants-portal.yaml` | 标准模型 | web/src/views/tenant/TenantModelsView.vue:24 → locale tenantModels.page.title（渲染在同文件 :157 的 <h2>{{ pageTitle }}</h2>） |
| `/tenant/account` | 是 · 任意登录用户 | `04-tenants-portal.yaml` | 我的账户 | web/src/views/tenant/MaaSAccountView.vue:29 → locale tenants.account.title |
| `/tenant/pricing` | 是 · 任意登录用户 | `04-tenants-portal.yaml` | 套餐与充值 | web/src/views/tenant/MaaSPricingView.vue:24 → locale tenants.pricing.title |
| `/tenant/orders/:id` | 是 · 任意登录用户 | `04-tenants-portal.yaml` | 订单支付 | web/src/views/tenant/MaaSOrderView.vue:103 → locale tenants.order.title |
| `/tenant/usage` | 是 · 任意登录用户 | `04-tenants-portal.yaml` | 积分流水 | web/src/views/tenant/MaaSUsageView.vue:354 → locale tenants.usage.ledgerTitle |
| `/maas/models` | 是 · 任意登录用户（重定向到 `/tenant/models`，守卫按目标路由的 meta 判） | `04-tenants-portal.yaml` | 标准模型 | router.ts:272 → /tenant/models（断言落在 /tenant/models 页面）；web/src/views/tenant/TenantModelsView.vue:24 → locale tenantModels.page.title |
| `/maas/account` | 是 · 任意登录用户（重定向到 `/tenant/account`，守卫按目标路由的 meta 判） | `04-tenants-portal.yaml` | 我的账户 | router.ts:273 → /tenant/account（断言落在 /tenant/account 页面）；web/src/views/tenant/MaaSAccountView.vue:29 → locale tenants.account.title |
| `/maas/pricing` | 是 · 任意登录用户（重定向到 `/tenant/pricing`，守卫按目标路由的 meta 判） | `04-tenants-portal.yaml` | 套餐与充值 | router.ts:274 → /tenant/pricing（断言落在 /tenant/pricing 页面）；web/src/views/tenant/MaaSPricingView.vue:24 → locale tenants.pricing.title |
| `/maas/orders/:id` | 是 · 任意登录用户（重定向到 `/tenant/orders/:id`，守卫按目标路由的 meta 判） | `04-tenants-portal.yaml` | 订单支付 | router.ts:275 → /tenant/orders/:id（断言落在 /tenant/orders/:id 页面）；web/src/views/tenant/MaaSOrderView.vue:103 → locale tenants.order.title |
| `/maas/usage` | 是 · 任意登录用户（重定向到 `/tenant/usage`，守卫按目标路由的 meta 判） | `04-tenants-portal.yaml` | 积分流水 | router.ts:276 → /tenant/usage（断言落在 /tenant/usage 页面）；web/src/views/tenant/MaaSUsageView.vue:354 → locale tenants.usage.ledgerTitle |

### 3.5 `05-routing.yaml` — 路由域

`/routing-v2` 工作台、凭据监控、探测健康、工作类型、AUTO 运营、路由策略与关联分析。

| 路由 path | 需要登录 | flow 文件 | 断言点（可见文案） | 源码位置 |
|---|---|---|---|---|
| `/routing-v2` | 是 · super_admin | `05-routing.yaml` | 路由全景 | web/src/views/RoutingDashboardView.vue:981 → locale routing.dashboard.title |
| `/routing-v2/credentials` | 是 · 任意登录用户 | `05-routing.yaml` | 凭据监控 | web/src/views/CredentialMonitorView.vue:209 → locale credentialMonitor.page.title |
| `/probe-health` | 是 · 任意登录用户（重定向到 `/routing-v2/credentials`，守卫按目标路由的 meta 判） | `05-routing.yaml` | 探测健康度 | router.ts:226 → /routing-v2/credentials（断言落在 /routing-v2/credentials 页面）；web/src/views/probe/ProbeHealthPanel.vue:329 → locale probeHealth.title |
| `/probe-health/detail` | 是 · super_admin | `05-routing.yaml` | 输入价 | web/src/views/ProbeHealthDetailView.vue:540 |
| `/routing-v2/work-types` | 是 · super_admin | `05-routing.yaml` | 工作类型列表 | web/src/views/WorkTypesView.vue:834 → locale workTypes.list.title |
| `/routing-v2/work-types/settings` | 是 · super_admin | `05-routing.yaml` | 工作类型列表 | web/src/views/WorkTypesView.vue:834 → locale workTypes.list.title |
| `/routing-v2/work-types/:key` | 是 · super_admin | `05-routing.yaml` | 工作类型列表 | web/src/views/WorkTypesView.vue:834 → locale workTypes.list.title |
| `/routing-v2/auto-ops` | 是 · 任意登录用户 | `05-routing.yaml` | AUTO 路由运营 | web/src/views/AutoRoutingOpsView.vue:144 → locale autoOps.title |
| `/routing-v2/annotations` | 是 · 任意登录用户（重定向到 `/routing-v2/auto-ops`，守卫按目标路由的 meta 判） | `05-routing.yaml` | 标注工作台 | router.ts:239 → /routing-v2/auto-ops（断言落在 /routing-v2/auto-ops 页面）；web/src/views/AutoRoutingOpsView.vue:113 → locale autoOps.tab.annotate |
| `/routing-v2/annotations/stats` | 是 · 任意登录用户（重定向到 `/routing-v2/auto-ops`，守卫按目标路由的 meta 判） | `05-routing.yaml` | 标注统计 | router.ts:240 → /routing-v2/auto-ops（断言落在 /routing-v2/auto-ops 页面）；web/src/views/AutoRoutingOpsView.vue:114 → locale autoOps.tab.stats |
| `/routing-v2/task-profile` | 是 · 任意登录用户（重定向到 `/routing-v2/auto-ops`，守卫按目标路由的 meta 判） | `05-routing.yaml` | 任务档案 | router.ts:247 → /routing-v2/auto-ops（断言落在 /routing-v2/auto-ops 页面）；web/src/views/AutoRoutingOpsView.vue:118 → locale autoOps.tab.profiles |
| `/routing-v2/auto-tuning` | 是 · 任意登录用户（重定向到 `/routing-v2/auto-ops`，守卫按目标路由的 meta 判） | `05-routing.yaml` | 调参审批 | router.ts:248 → /routing-v2/auto-ops（断言落在 /routing-v2/auto-ops 页面）；web/src/views/AutoRoutingOpsView.vue:119 → locale autoOps.tab.tuning |
| `/routing-policy` | 是 · super_admin | `05-routing.yaml` | 路由策略 | web/src/views/RoutingPolicyView.vue:153 → locale routing.policy.title |
| `/routing` | 是 · super_admin（重定向到 `/routing-v2`，守卫按目标路由的 meta 判） | `05-routing.yaml` | 路由全景 | router.ts:281 → /routing-v2（断言落在 /routing-v2 页面）；web/src/views/RoutingDashboardView.vue:981 → locale routing.dashboard.title |
| `/routing-overview` | 是 · super_admin + default 租户 | `05-routing.yaml` | 模型路由全景 | web/src/views/RoutingOverviewView.vue:255 → locale routing.overview.title |
| `/routing-decisions` | 是 · super_admin + default 租户 | `05-routing.yaml` | 路由决策日志 | web/src/views/DecisionsView.vue:320 → locale decisionsView.title |
| `/correlations` | 是 · super_admin | `05-routing.yaml` | Auto-Route 关联分析 (Correlations) | web/src/views/CorrelationsView.vue:261 → locale correlations.title |
| `/routing/overrides` | 是 · super_admin | `05-routing.yaml` | 路由覆盖 (Routing Overrides) | web/src/views/RoutingOverrideView.vue:194 → locale routingOverride.title |
| `/routing/overrides/audit` | 是 · super_admin | `05-routing.yaml` | 路由覆盖审计 | web/src/views/RoutingAuditView.vue:202 → locale routingAudit.title |
| `/quality-correlations` | 是 · super_admin | `05-routing.yaml` | 质量关联分析 | web/src/views/QualityCorrelationsView.vue:186 → locale qualityCorrelations.title |
| `/system-monitor` | 是 · 任意登录用户（重定向到 `/dashboard`，守卫按目标路由的 meta 判） | `05-routing.yaml` | 仪表盘 | router.ts:230 → /dashboard（断言落在 /dashboard 页面）；web/src/views/DashboardViewV2.vue:97 → locale dashboard.title |
| `/dispatch/waterfall` | 是 · super_admin + default 租户 | `05-routing.yaml` | 队列瀑布图 | web/src/views/DispatchWaterfallView.vue:122 |

### 3.6 `06-keys-requests.yaml` — 密钥 / 请求日志 / 会话分析

API 密钥、请求日志与全屏请求详情、用户/客户端/任务画像。

| 路由 path | 需要登录 | flow 文件 | 断言点（可见文案） | 源码位置 |
|---|---|---|---|---|
| `/keys` | 是 · 任意登录用户 | `06-keys-requests.yaml` | API 密钥管理 | web/src/views/KeysView.vue:601 → locale keys.list.title |
| `/keys/:id` | 是 · 任意登录用户 | `06-keys-requests.yaml` | 密钥统计 | web/src/views/KeyDetailView.vue:567 → locale keys.detail.title |
| `/request-logs` | 是 · 任意登录用户 | `06-keys-requests.yaml` | 请求日志 | web/src/views/RequestLogsView.vue:1160 |
| `/request-detail/:requestId` | 是 · 任意登录用户 | `06-keys-requests.yaml` | 单请求 | web/src/views/RequestDetailFullscreenView.vue:195 |
| `/admin/session-analytics/users` | 是 · 任意登录用户 | `06-keys-requests.yaml` | 用户画像 | web/src/views/UserProfileListView.vue:91 → locale sessions.userProfile.title |
| `/admin/session-analytics/users/:owner` | 是 · 任意登录用户 | `06-keys-requests.yaml` | 用户画像详情 | web/src/views/UserProfileView.vue:221 → locale sessions.userProfile.detailTitle |
| `/admin/session-analytics/clients/:id` | 是 · 任意登录用户 | `06-keys-requests.yaml` | 客户端分析 | web/src/views/ClientAnalyticsView.vue:151 → locale sessions.clientAnalytics.title |
| `/admin/session-analytics/tasks/:id` | 是 · 任意登录用户 | `06-keys-requests.yaml` | 任务分析 | web/src/views/TaskAnalyticsView.vue:151 → locale sessions.taskAnalytics.title |
| `/admin/session-analytics/filter-options` | 是 · 任意登录用户 | `06-keys-requests.yaml` | 会话分析筛选 | web/src/views/SessionAnalyticsFilterOptionsView.vue:63 |

### 3.7 `07-admin-config.yaml` — 后台配置域

系统设置、模块、Agent 注册表、审批、清洗规则、合规、压缩、数据生命周期。

| 路由 path | 需要登录 | flow 文件 | 断言点（可见文案） | 源码位置 |
|---|---|---|---|---|
| `/admin/settings` | 是 · super_admin | `07-admin-config.yaml` | 当前值 | web/src/views/SettingsView.vue:279 |
| `/admin/modules` | 是 · super_admin | `07-admin-config.yaml` | 模块管理 | web/src/views/ModulesView.vue:402 → locale modulesView.pageTitle |
| `/admin/agents` | 是 · super_admin | `07-admin-config.yaml` | Agent Registry | web/src/views/AgentRegistryView.vue:310 → locale agentRegistryView.pageTitle |
| `/admin/prompt-injection` | 是 · super_admin | `07-admin-config.yaml` | 提示词注入检测 | web/src/views/PromptInjectionSettingsView.vue:4 → locale sessions.promptInjectionFull.title |
| `/admin/approval-config` | 是 · super_admin | `07-admin-config.yaml` | 审批模式 | web/src/views/ApprovalConfigView.vue:140 → locale approval.config.mode.label |
| `/admin/approvals` | 是 · super_admin | `07-admin-config.yaml` | 审批请求 | web/src/views/ApprovalListView.vue:355 → locale approval.list.title |
| `/admin/approvals/:id` | 是 · super_admin | `07-admin-config.yaml` | 审批请求详情 | web/src/views/ApprovalDetailView.vue:181 → locale approval.detail.title |
| `/admin/sanitize-rules` | 是 · 任意登录用户 | `07-admin-config.yaml` | 敏感信息规则 | web/src/views/SanitizeRulesView.vue:537 → locale sanitizeRules.title |
| `/admin/output-compliance` | 是 · super_admin | `07-admin-config.yaml` | 输出合规 | web/src/views/OutputComplianceView.vue:315 → locale outputCompliance.title |
| `/admin/compression` | 是 · super_admin + default 租户 | `07-admin-config.yaml` | 压缩概览 | web/src/views/CompressionView.vue:434 → locale compression.title |
| `/admin/data-lifecycle` | 是 · super_admin + default 租户 | `07-admin-config.yaml` | 数据生命周期管理 | web/src/views/DataLifecycleView.vue:4 |
| `/admin/data-lifecycle/migration-state` | 是 · super_admin + default 租户 | `07-admin-config.yaml` | 存储迁移状态 | web/src/views/StorageMigrationStateView.vue:83 |
| `/admin/storage-and-log-config` | 是 · super_admin | `07-admin-config.yaml` | 存储与日志配置 | web/src/views/StorageAndLogConfigView.vue:63 |

### 3.8 `08-admin-data-ops.yaml` — 后台数据 / 用量 / 审计域

用量三件套、运营与运维总览、订阅层级、轮次、会话、请求注册表、连接注册台、对账、审计。

| 路由 path | 需要登录 | flow 文件 | 断言点（可见文案） | 源码位置 |
|---|---|---|---|---|
| `/admin/usage` | 是 · super_admin | `08-admin-data-ops.yaml` | 成本归因 | web/src/views/admin/UsageCost.vue:437 → locale dataLifecycle.usageCost.attribution.title |
| `/admin/usage-trends` | 是 · 任意登录用户 | `08-admin-data-ops.yaml` | 用量趋势分析 | web/src/views/admin/UsageTrendExplorer.vue:329 → locale usageTrend.pageTitle |
| `/admin/usage-enhanced` | 是 · super_admin | `08-admin-data-ops.yaml` | 用量增强分析 | web/src/views/admin/UsageEnhancedView.vue:96 → locale dataLifecycle.pages.usageEnhanced |
| `/admin/board-operational` | 是 · 任意登录用户 | `08-admin-data-ops.yaml` | 运营面板 | web/src/views/BoardOperationalView.vue:79 |
| `/admin/ops-overview` | 是 · super_admin | `08-admin-data-ops.yaml` | 运维总览 | web/src/views/OpsOverviewView.vue:76 |
| `/admin/subscription-tiers` | 是 · super_admin | `08-admin-data-ops.yaml` | 订阅层级 | web/src/views/SubscriptionTiersView.vue:49 |
| `/admin/turns/filter-options` | 是 · super_admin | `08-admin-data-ops.yaml` | 轮次筛选选项 | web/src/views/TurnsFilterOptionsView.vue:62 |
| `/admin/turns` | 是 · super_admin | `08-admin-data-ops.yaml` | 会话与轮次 | web/src/views/TurnsListView.vue:319 |
| `/admin/sessions/:id` | 是 · super_admin | `08-admin-data-ops.yaml` | 轮次时间线 | web/src/components/session/SessionTurnsTimeline.vue:164 |
| `/admin/request-actions/:id` | 是 · super_admin | `08-admin-data-ops.yaml` | 请求动作时间线 | web/src/views/RequestActionsView.vue:78 |
| `/admin/request-registry` | 是 · super_admin | `08-admin-data-ops.yaml` | 请求注册表 | web/src/views/RequestRegistryView.vue:391 → locale requestRegistry.title |
| `/admin/request-registry/journey/:requestId` | 是 · super_admin | `08-admin-data-ops.yaml` | 请求 Journey 详情 | web/src/views/RequestJourneyDetailView.vue:89 → locale requestJourneyDetail.title |
| `/admin/connection-registry` | 是 · super_admin | `08-admin-data-ops.yaml` | 连接注册台 | web/src/views/ConnectionRegistryView.vue:241 → locale connectionRegistry.title |
| `/admin/connection-registry/:credentialId/recovery` | 是 · super_admin | `08-admin-data-ops.yaml` | 节点恢复时间线 | web/src/views/NodeHealthTimelineView.vue:99 → locale nodeHealthTimeline.title |
| `/admin/reconciliation` | 是 · super_admin | `08-admin-data-ops.yaml` | 按供应商 | web/src/views/admin/ReconciliationReport.vue:68 → locale reports.byProvider |
| `/admin/proxy` | 是 · super_admin | `08-admin-data-ops.yaml` | 代理管理 | web/src/views/ProxyView.vue:439 → locale proxy.title |
| `/audit-logs` | 是 · super_admin | `08-admin-data-ops.yaml` | 审计日志 | web/src/views/AuditLogView.vue:300 → locale auditLog.page.title |
| `/format-anomalies` | 是 · super_admin | `08-admin-data-ops.yaml` | 格式异常监控 | web/src/views/FormatAnomaliesView.vue:366 → locale formatAnomaliesView.pageTitle |
| `/model-integrity` | 是 · super_admin | `08-admin-data-ops.yaml` | 模型完整性监控 | web/src/views/ModelIntegrityView.vue:451 → locale modelIntegrityView.pageTitle |

### 3.9 `09-ops-maintain.yaml` — 运维中心外跳 / maintain 兜底

`/ops/*` 全页外跳到 maintain SPA；Maintain 不可用时守卫改写到 `/customer/update-activate`。

| 路由 path | 需要登录 | flow 文件 | 断言点（可见文案） | 源码位置 |
|---|---|---|---|---|
| `/ops/licenses` | 是 · 任意登录用户（无 meta ⇒ 守卫要求登录） | `09-ops-maintain.yaml` | 更新与激活 | router.ts:354 → /customer/update-activate（断言落在 /customer/update-activate 页面）；web/src/views/lifecycle/UpdateActivateView.vue:339 → locale nav.item.updateActivate |
| `/ops/faults` | 是 · 任意登录用户（无 meta ⇒ 守卫要求登录） | `09-ops-maintain.yaml` | 更新与激活 | router.ts:355 → /customer/update-activate（断言落在 /customer/update-activate 页面）；web/src/views/lifecycle/UpdateActivateView.vue:339 → locale nav.item.updateActivate |
| `/ops/autoupdate` | 是 · 任意登录用户（无 meta ⇒ 守卫要求登录） | `09-ops-maintain.yaml` | 更新与激活 | router.ts:356 → /customer/update-activate（断言落在 /customer/update-activate 页面）；web/src/views/lifecycle/UpdateActivateView.vue:339 → locale nav.item.updateActivate |
| `/ops/center` | 是 · 任意登录用户（无 meta ⇒ 守卫要求登录） | `09-ops-maintain.yaml` | 更新与激活 | router.ts:357 → /customer/update-activate（断言落在 /customer/update-activate 页面）；web/src/views/lifecycle/UpdateActivateView.vue:339 → locale nav.item.updateActivate |
| `/ops` | 是 · 任意登录用户（无 meta ⇒ 守卫要求登录） | `09-ops-maintain.yaml` | 更新与激活 | router.ts:358 → /customer/update-activate（断言落在 /customer/update-activate 页面）；web/src/views/lifecycle/UpdateActivateView.vue:339 → locale nav.item.updateActivate |
| `/ops/vibecoding` | 是 · super_admin | `09-ops-maintain.yaml` | 更新与激活 | web/src/views/lifecycle/UpdateActivateView.vue:339 → locale nav.item.updateActivate |

### 3.10 `90-dev-only.yaml` — 仅 dev 构建存在的路由

router.ts 里 `import.meta.env.DEV` 包裹的三条路由，生产构建不注册 ⇒ 单独成 flow，默认不参与批量跑。

| 路由 path | 需要登录 | flow 文件 | 断言点（可见文案） | 源码位置 |
|---|---|---|---|---|
| `/dev/waterfall-preview` | 否（公开） | `90-dev-only.yaml` | 队列瀑布图 · 本地预览 | web/src/views/DispatchWaterfallPreview.vue:47 |
| `/dev/live-topology-preview` | 否（公开） | `90-dev-only.yaml` | 实时拓扑 | web/src/views/LiveTopologyView.vue:236 → locale liveTopology.title |
| `/dev/live-request-preview/:requestId` | 否（公开） | `90-dev-only.yaml` | 请求详情 | web/src/views/RequestDetailPage.vue:41 → locale liveTopology.zoom.title |

### 3.11 `12-live-stage-tree.yaml` — 请求阶段树（`/live2`）

★ 2026-11-17 新增（owner「打开功能」轮）。`/live2` 此前**没有任何 Maestro 资产** ——
路由在 `web/src/router.ts:233`、页面在 `web/src/views/LiveStageTreeView.vue`，
但**从没被浏览器打开过** ⇒ 「功能开着」这件事只有源码与菜单配置能证明，没有浏览器证据。

| 路由 path | 需要登录 | flow 文件 | 断言点（可见文案） | 源码位置 |
|---|---|---|---|---|
| `/live2` | 是 · 任意登录用户 | `12-live-stage-tree.yaml` | 统计胶囊 `.*条 · 成功率.*`、过滤栏「全部客户端/全部供应商/全部模型/全部节点/全部状态」、「显示探测请求」、「显示无效节点」、浮层标题「实时统计」、统计面板「请求数/成功率/客户端/供应商/模型」 | web/src/views/LiveStageTreeView.vue:304（`<template>`）→ locale `liveStageTree.*` |
| `/live` | 是 · 任意登录用户 | `12-live-stage-tree.yaml` | 页面标题「实时拓扑」+ 顶栏「请求阶段树」入口（`[data-testid="live-stage-tree-link"]`，精确存在性由页面内断言验） | web/src/views/LiveTopologyView.vue:360 → locale `liveStageTree.title` |

★ **这个 flow 的分工**（与 §6.7 同一口径）：
- **flow 验行为**：入口可见可点、落地页的可见结构、统计浮层能点开；
- **页面内 CDP**（`maestro/tools/assert-stage-tree.mjs`）验**结构与口径**：
  页面身份（pathname）、DOM 契约（data-testid 在不在）、**统计口径对拍**
  （胶囊读数 ↔ 实际阶段带条数/成功/在途徽章数）、数据是活的、SSE 已连接、
  `/live` 顶栏入口的精确存在性、浮层的 Esc 出口与关闭按钮。

★ 为什么「请求阶段树」这个文案**不能单独**当页面身份判据：
它**同时**是顶栏菜单项与本页 h2 标题 ⇒ 一条 `text:` 断言分不清「菜单在」还是「页面在」。
⇒ flow 用「菜单项可见 → 点进去 → 统计胶囊（/live2 独有的合成读数形态）→
浮层标题『实时统计』」四条组合定位；精确身份（pathname）交给页面内断言。

```bash
# 跑法（必须带 1440x1400 视口，见 §6.6 ④）
BASE_URL=https://llm.kxpms.cn ADMIN_USER=admin ADMIN_PASSWORD='***' \
  bash maestro/run-stage-tree.sh

# 页面内断言（需要一条已签发的 JWT）
node maestro/tools/assert-stage-tree.mjs \
  --url https://llm.kxpms.cn --token-file /tmp/llmgw-jwt.txt --page /live2
node maestro/tools/assert-stage-tree.mjs \
  --url https://llm.kxpms.cn --token-file /tmp/llmgw-jwt.txt --page /live
```

### 3.12 `16-session-analytics-system.yaml` — 会话分析**聚合分析族**（5 条路由）

★ 2026-11-23（L10 复核轮）补记：这 5 条路由**早已被 flow 16 覆盖**，
  但 §3 一直没有对应小节 ⇒ 「路由在、flow 在、清单缺」的三方不一致。
  ⇒ 本节按 `maestro/tools/audit-route-coverage.mjs` 的实测映射补齐。

| 路由 path | 需要登录 | flow 文件 | 断言点（data-testid） | 源码位置 |
|---|---|---|---|---|
| `/admin/session-analytics` | 是 · 任意登录用户 | `16-session-analytics-system.yaml` | ★ **重定向落点**：Top10 表头 `sa-users-col-rank`（表头出现＝数据到位）+ `sa-page-users` / `sa-range-label` / `sa-refresh-btn` / `sa-top5-section` / `sa-users-table` | web/src/views/admin/session-analytics/SessionAnalyticsUsersView.vue（★ 路由 `web/src/router.ts:373` 重定向到 `/admin/session-analytics/users/overview` —— ★ ★ 2026-11-23 L30 轮·补②④：★ L14 轮把整合页迁到 `/users/overview` 后、★ 本重定向随之改指，★ **旧 URL 的行为与 L14 之前一致**、★ 且 `16-session-analytics-system.yaml` 的 3 条断言恢复可达） |
| `/admin/session-analytics/user-ranking` | 是 · 任意登录用户 | `16-session-analytics-system.yaml` | 排名列头 `sa-user-ranking-col-rank` + `sa-page-user-ranking` / `sa-user-ranking-filters` | web/src/views/admin/session-analytics/SessionAnalyticsUserRankingView.vue |
| `/admin/session-analytics/user-ranking/:owner` | 是 · 任意登录用户 | `16-session-analytics-system.yaml` | 指标卡 `sa-metric-rank`（+ `sa-user-detail-identity` / `sa-back-btn` / `sa-metric-session-count`） | web/src/views/admin/session-analytics/SessionAnalyticsUserDetailView.vue |
| `/admin/session-analytics/agent-ranking` | 是 · 任意登录用户 | `16-session-analytics-system.yaml` | 排名列头 `sa-agent-ranking-col-rank` + `sa-page-agent-ranking` | web/src/views/admin/session-analytics/SessionAnalyticsAgentRankingView.vue |
| `/admin/session-analytics/model-ranking` | 是 · 任意登录用户 | `16-session-analytics-system.yaml` | 排名列头 `sa-model-ranking-col-rank` + `sa-page-model-ranking` | web/src/views/admin/session-analytics/SessionAnalyticsModelRankingView.vue |

★ **断言判据是 `data-testid` 而不是中文文案**（2026-11-23 L13 轮）：文案随 locale 变
  ⇒ 中文文案断言在非 zh-CN 界面语言下**恒红**（假绿级的反向：恒红）⇒ 换成结构判据。
  ★ flow ↔ 前端 testid 的双向对应由钉测守着：
  `web/src/views/admin/session-analytics/saTestIds.contract.spec.ts`。

★ **两层分工不可合并**（与 §3.11 同一口径）：
- **flow 层**：证明「浏览器真的打开并渲染了」（宽表右侧列在内部滚动容器里，Maestro 够不着）；
- **页面内 CDP 探针**（`maestro/tools/assert-session-analytics.mjs`）：证明「页面上的数是对的」
  —— 对拍基准是**页面自己收到的那份响应**（CDP Network 域 + getResponseBody），
  而不是探针另发一次 API（活流量下必然假红）⇒ 严格相等、无需容差。

### 3.13 已知缺口：**未被任何 flow 的 `openLink` 直接打开**的 21 条路由

★ 2026-11-23（L10 复核轮）补记。清单由
  `node maestro/tools/audit-route-coverage.mjs` 实测产出（**可复跑**），
  完整分析见 `maestro/L10-uncovered-routes.md` §3。

★ ★ **本清单的条数随树走**（可复跑 ⇒ 必然如此）：
  基准提交 `bd8d39757` 时是 **15** 条 → L10 首轮回填时是 **18** 条
  → 2026-11-23 复核轮（上游 `41fb572f3` 新增团队层级管理 2 条）是 **20** 条。
  ⇒ **每次上游加路由，都要重跑本清单** ⇒ `--check` 模式就是为这件事准备的。

★ ★ **口径边界（先读，否则会误判）**：本清单数的是「**flow 用 `openLink`
  直接打开过**」，**不含「在页面里点击进入」的路由** ⇒
  - 静态工具**无法**判定点击可达性（Maestro 的 `tapOn` 不携带 URL）；
  - ⇒ 被点击进入的路由会**落进本清单**，但它们**其实已被覆盖**。
  ⇒ 本清单按「证据强度」分三档，**不要把 B/C 档当成「完全没验过」**。
  ★ ★ **与 `maestro/L10-uncovered-routes.md` §3.1 的分档关系**：那边分**四档**（A/B/C/D），
    本节把它的 **C 与 D 两档合并**成「C 真实缺口」（并按处置优先级标 P1/P2/P3）
    ⇒ ⇒ ★ **两文档的路由集合逐条相同**，只是**分档粒度不同**（读哪边都不矛盾）。

| 路由 | 类别 | 说明 / 处置 |
|---|---|---|
| `/:pathMatch(.*)*` | A 兜底（设计如此） | 404 兜底路由；由「访问不存在路径」触达 ⇒ 不单独写 flow（写了反而盖住「兜底没兜住」这个缺陷） |
| `/maintain/:pathMatch(.*)*` | A 兜底（设计如此） | maintain 兜底路由；maintain 不可达时由守卫改写 |
| `/admin/error-playbook/:id` | **B 点击可达（已覆盖）** | flow `17-error-playbook.yaml` §④ 点种子行「鉴权类失败」进入详情页（断「执行留痕时间线」）⇒ **已被覆盖**，只是没走 `openLink` ⇒ 工具看不见 |
| `/admin/analytics/:dimension/:id` | **B 点击可达（已覆盖）** | 运营看板列表行点击深链进入（`config/analyticsRegistry.ts` 的 `detailPath`；见 `navCoverage.test.ts` 的孤儿登记）⇒ 静态不可判定 |
| `/admin/board-operational/:dimension` | **B 点击可达（待证）** | 运营看板各维度完整列表页；由看板内跳转进入 ⇒ 静态不可判定，**建议人工确认一次** |
| `/login` | C **真实缺口** | 共享登录流走 `openLink: ${BASE_URL}/?login=1`（首页登录弹层）⇒ **`/login` 这条路由本身从未被直接打开过** ⇒ 「登录弹层能用」与「登录页能打开」是两条独立断言 |
| `/admin/pi-analysis` | C 真实缺口 | P1（super 档 + 运行时可用性闸门） |
| `/admin/self-update` | C 真实缺口 | P1（super 档） |
| `/help` | C 真实缺口 | P1（任何登录用户可见） |
| `/public/quote/:token` | C 真实缺口 | P1（**免登录可达** ⇒ 失效时用户完全打不开） |
| `/operations/market-prices` / `/operations/marketing` / `/operations/sales` | C 真实缺口 | P2（运营域三件套） |
| `/model-credibility` | C 真实缺口 | P2（super 档） |
| `/admin/plugin-settings` | C 真实缺口 | P3（super 档；菜单归属待产品确认） |
| `/simple/keys` / `/tenant/bills` | C 真实缺口 | P3（简易模式密钥签发 / 月账单） |
| `/operations/price-compare` | C 真实缺口 | P3（重定向到 `/model-pricing` ⇒ 只需断言「跳转到位」） |
| `/admin/teams` | C 真实缺口 | P3（★ 2026-11-23 上游 `41fb572f3`「团队层级管理前端切片落地」新增；`appNav.ts` **无菜单入口** ⇒ 上游自己已在 `web/src/config/navCoverage.test.ts` 把它登记为**孤儿路由**（菜单归属待产品确认）⇒ 与 `/admin/plugin-settings` 同族口径） |
| `/admin/teams/:id` | C 真实缺口 | P3（★ 详情页**只能从列表页点击**到达（上游注释：详情页用路径字面量 `/admin/teams/${teamId}`）⇒ 静态工具不可判定；★ 但**列表页本身也没有任何 flow** ⇒ 团队层级这族两条路由**都没有 flow 覆盖**） |
| `/admin/session-analytics/users/overview` | C **真实缺口** | P2（★ **2026-11-23 L14 落地轮新增**（`router.ts:362`、★ `component: SessionAnalyticsUsersView`、★ `meta.requiresAuth`）⇒ ★ 它是【会话分析整合页】、★ 可从 `appNav.ts` 的菜单点入、★ 但【没有任何 flow 直接打开它】） ⇒ ★ ★ **本行的存在本身就是一次【登记漏项】的证据**：★ 该路由落地时只改了 `router.ts`、★ **`maestro/README.md` 一个字没改** ⇒ ★★ ⇒ `audit-route-coverage.mjs --check` 的 5 条判据当场红（★ 每条都正好差 1）⇒ ★★★ **⇒ 纪律：★ 「新增路由」与「登记路由」是同一件事的两半 —— ★ 只做一半、★ 门会红** |

★ 「缺口」是**事实登记**，不是**红门**：本 README 不对覆盖率判红
  （哪些路由值得写 flow 属产品优先级，见 `maestro/L10-uncovered-routes.md` §3.1）。

### 3.14 ★ PR 模板「页面改动 Checklist」与当前树的对拍（L12 补轮，2026-11-23）

> ★ **为什么有这一节**：L12 的诉求是「会话分析 E2E 没接入 CI ⇒ 改了页面但没跑 E2E 不会被拦住」。
> 上游 2026-11-20 的收口（`035b0c4b7`）走 **PR checklist** 方案：在
> `.github/pull_request_template.md` 里列出「改了这些页面就必须复跑会话分析 E2E」。
> ★ ★ 但那份清单是**手写的**，且 **2026-11-23 实测漂移了两处**：
>
> | 漂移 | 事实 |
> |---|---|
> | **幽灵条目** | 模板列了 `SessionAnalyticsOverviewView.vue` —— 上游 2026-11-23 审计轮把「会话分析总览页」**取消了**（路由改 redirect 到 `/admin/session-analytics/users`）⇒ 该文件**在仓内已不存在** |
> | **漏项** | 上游把总览页内容**重组迁入** `SessionAnalyticsUsersView.vue` ⇒ ★ **这个新页面不在模板里** ⇒ ★ **改了最需要复跑 E2E 的那个页面，checklist 反而不会要求复跑** |
>
> ⇒ **L12 的保护恰好在最需要的地方失效**。

**对拍工具**（★ 零第三方依赖，python3）：

```bash
python3 maestro/tools/audit-page-change-checklist.py          # Markdown 报告
python3 maestro/tools/audit-page-change-checklist.py --json   # 机读 JSON
```

三条判据（退出码 0 = 一致；1 = 漂移）：

| 判据 | 内容 |
|---|---|
| A **幽灵文件** | 模板列的每个 `.vue` 在仓内**都存在** |
| B **漏项文件** | 仓内每个会话分析 `.vue` **都被模板覆盖**（逐个列出，或其目录被整体列出） |
| C **幽灵路径** | 模板列的每个 `/admin/session-analytics*` 路径在 `router.ts` 里**都存在** |

★ **为什么是 python3 而不是 node**：★ 本工具要挂在**真正在跑的 CI 载体**上
⇒ ★ **必须零依赖**（`typescript` 装在 `web/node_modules` ⇒ CI 精简镜像里没有
⇒ ⇒ ★★ **「工具不可运行 ⇒ 它的产出不可复现 ⇒ 门会被判成恒绿或误红」** —— L10 的那条教训）；
且 ★ **node 本身也不可靠**（仓内既有 CI 门对 node 是「有就跑、没有就 skip」语义）
⇒ ⇒ 门以 node 为前提 ⇒ CI 上要么恒红、要么被 skip ⇒ **门等于没接**。
⇒ **两把尺子的分工**：精确路由计数/redirect 形态判别用 AST 工具
（`maestro/tools/audit-route-coverage.mjs`，**只在本地跑，不进 CI 载体**）；
CI 上跑的必须是零依赖的这把。

**门与接线**：

| 项 | 内容 |
|---|---|
| 门 | `scripts/checks/session-analytics-checklist-gate.sh`（行为判据 + **对照臂** + **反向断言**） |
| 真载体 | `.workflow/main-verify.yml` 的 `session-analytics-checklist-gate` step（★ **非注释行**） |
| ★ 为什么不挂 `.github/workflows/` | 本仓远端是 `codeup.aliyun.com` ⇒ ★ **`.github/workflows/` 从来不在执行路径上** ⇒ 挂那儿 = 「文件在、门绿、CI 根本不跑它」（`scripts/ursmcheck/ci_carrier_truth_test.go` 记的「自证型门第四形态」） |
| 对照臂 | 注入一个**仓内不存在**的会话分析视图名 ⇒ 工具**必须红**；且必须**红在幽灵条目上**（否则「随便红一下」也算过） |

---

### 3.15 ★★ flow 断言点 × 现役渲染闭包（四方对拍，L14 复核轮新增，2026-11-23）

> ★★ §3.1–§3.14 全部按**路径**核对（flow 打开过哪些 URL、README 登记了哪些 URL）。
> ★★ **本节按「断言点」核对**：★ **flow 打开某个 URL 后，断言的那个字/那个 testid，
> 在那个 URL 的【现役渲染闭包】里到底还在不在？**
>
> ```bash
> node maestro/tools/audit-flow-assertions.mjs            # Markdown 报告
> node maestro/tools/audit-flow-assertions.mjs --json     # 机读
> node maestro/tools/audit-flow-assertions.mjs --flow 13   # 只看某个 flow（文件名前缀）
> ```
>
> ★ **它补的是第四个维度**（前三维已被存量工具占满）：
>
> | 维度 | 谁在判 | 为什么看不见 L14 |
> | --- | --- | --- |
> | ① | `navCoverage.test.ts` | 判**路由**可达性 ⇒ 组件被 import 却没路由，它看不见 |
> | ② | `pageHelpRegistry.test.ts` | 判**存在性** ⇒ 正文与页面形态是否一致不在判据里 |
> | ③ | `maestro/tools/audit-route-coverage.mjs` | 判**路径**三方对拍 ⇒ **同一个 URL 换了组件、断言随之失效，它看不见** |
> | ④ | ★ **本工具** | 判**断言点 × 该 URL 的现役渲染闭包** | —— |
>
> ★ **四档判定**：`reachable`（可达）｜`not-found`（★ **仓内确实存在、但该页渲染不出** ⇒ **强候选**）｜
> `regex-mismatch`（★ 字面量**就在页面上**、但断言把它**写成了正则** ⇒ **该断言恒红**）｜
> `undecidable`（运行期数据 / `${ENV}` 占位符 / 跨 token 正则 ⇒ **不作为候选**）。
>
> ★ **强弱候选分区**：★ 页面闭包里若出现**真正的动态组件解析**（`defineAsyncComponent` /
> `resolveComponent` / `Vue.component` / **glob 组件**）⇒ ★ 静态闭包不完整 ⇒
> 「渲染不出」的推论**证据强度较低** ⇒ ★ 这类候选单列为「弱候选」。
>
> ★★ **本工具是只读的事实登记工具，★ 不判红绿、★ 不进 CI 载体**
> （★ 与同族 `audit-route-coverage.mjs` 同口径；★ 误报率尚未量化 ⇒ ★ 按仓内纪律
> 「门自己的误报比没门更坏」⇒ ★ **升级为红门之前必须先人工过一遍候选清单**）。
>
> ★★ **当前树的实测产出**（★ 全文见 `docs/audit/evidence/2026-11-23-session-analytics-l14-flow-assertion-sweep.txt`）：
> 全仓 **30 条 flow / 128 条路由**扫完 ⇒ `not-found` 候选 **68**（★ **其中 17 条指向
> `/admin/session-analytics/users`** ⇒ ★★ **L14 家族＝4 条 flow**：`13-session-analytics-users` /
> `06-keys-requests` / `14-stats-consistency-test` / `14-user-analytics-enhancement`）、
> `regex-mismatch` **2**、`undecidable` **46**、其它（无路由/无组件）**21**。
> ⇒ ★★ **「只重写 flow 13 段」口径不足** ⇒ 要么按 flow 族一起改、要么恢复路由让它们一起转绿。
> 详见主档 `docs/audit/2026-11-20-会话分析系统前端实施报告.md` §7.26。

## 4. 前置条件

### 4.1 Maestro CLI

```bash
export JAVA_HOME="$HOME/kaixuan/jdk17/Contents/Home"   # Temurin 17.0.21
export PATH="$JAVA_HOME/bin:$HOME/kaixuan/bin:$PATH"
maestro --version                                      # 2.11.0
```

★ 本机现状：`maestro` 装在 `~/.maestro/bin/maestro`（CLI **2.11.0**）。
**JDK 是 2026-10-28（R176）为这次实跑现装的**：brew 的 `openjdk@17` 在本机拉不动，
改用清华 Adoptium 镜像装了 **Temurin 17.0.21** 到 `~/kaixuan/jdk17`
（`JAVA_HOME` 指向 `~/kaixuan/jdk17/Contents/Home`）。
★ `/usr/bin/java` 只是 macOS 的占位 stub，`java_home -V` 报 `Unable to locate a Java Runtime` ⇒
不设 `JAVA_HOME` 时 `maestro` 直接起不来。

### 4.2 本地网关

管理后台跑在 `http://127.0.0.1:8782`（OpenAI 兼容 API 在同源 `/v1`）。flow 用 `BASE_URL` 指向它。

### 4.3 ★ 变量通道：**只有 `-e KEY=VALUE`**

★★ **Maestro CLI 2.11.0 没有 `--env-file` 选项**（实跑：`Unknown option '--env-file'`），
而且 **shell 环境变量不参与 flow 的 `${}` 解析** ⇒ 把变量 export 到 shell 里是**无效**的。
⇒ **唯一通道是 `-e KEY=VALUE`，且要逐个传。**

仓库提供了 `maestro/run-local.sh` 代劳：它从**仓根 `.env.local`** 读
`LLM_GATEWAY_ADMIN_USER` / `LLM_GATEWAY_ADMIN_PASSWORD`，再拼成 `-e` 传给 Maestro。
⇒ **口令不落在仓内任何文件里**（`.env.local` 已被 `.gitignore` 覆盖；
`maestro/env.local.yaml` 只留样例，口令是空串占位）。

★ 代价要说清楚：`-e` 的值会出现在**进程 argv** 里 ⇒ 同机其它用户可用 `ps` 看到。
这是「用 CLI 提供的唯一通道」与「口令不进仓内文件」之间的取舍，本仓选了后者。

### 4.4 ★ 会话分析两层 E2E 的运行前提（2026-11-20 批判式审计补）

`maestro/flows/16-session-analytics-system.yaml` + `maestro/tools/assert-session-analytics.mjs`
这一对**必须经 `maestro/run-session-analytics.sh` 跑**，不能直接 `maestro test`。
三个前提（缺一个 ⇒ 结论不可信）：

| 前提 | 为什么 | 做法 |
|---|---|---|
| **界面语言 = zh-CN** | 应用 locale 取自 `navigator.language`（`web/src/i18n.ts:82` `defaultLocale()`）且持久化在 `localStorage['llmgw_locale']` ⇒ 浏览器语言一变，**整页文案换语种** ⇒ 断言里的中文判据（数据卡 label / 表头 / 区块标题）要么失效（假红），要么——状态判据失效——**假绿** | runner 起 Chrome 时带 `--lang=zh-CN`；探针再写一次 localStorage 并读回 `<html lang>` 复核（`web/src/i18n.ts:121` 会写它）；钉不住 ⇒ 判**量具失效**（退出码 2），不判产品红 |
| **全新浏览器 profile** | 复用的浏览器带着上一轮的 cookie / localStorage ⇒ 旧会话 cookie 与旧 `llmgw_locale` 静默生效 ⇒ 数据源降级、断言读到的不是被测状态（AGENTS.md ④-1 的实测坑） | CDP 端口被占用就**顺延换端口**（绝不复用那个浏览器）+ profile 目录带时间戳 |
| **动态段 `${SESSION_OWNER}` 喂真值** | owner 为空 ⇒ 详情页 URL 变成 `/admin/session-analytics/user-ranking/` ⇒ 断的是「路由不存在」而不是「详情页坏了」⇒ **量具错** | runner 从 `/api/admin/session-analytics/user-ranking` 取首行 owner；窗口内无人 ⇒ 判量具失效（退出码 2） |

★ **退出码三态**（与 `maestro/tools/result.mjs` 一致，**唯一的判定口径真源**）：

| 码 | 含义 | 处置 |
|---|---|---|
| 0 | 两层全绿 | 通过 |
| 1 | 有红（产品问题） | 修产品 |
| 2 | **量具失效**（窗口内无数据 ⇒ 断言**未评估**） | 换窗口 / 补数据，**不是**产品缺陷 |
| 不在 0/1/2 | **某一层异常退出或被杀**（137 / 143 / 130 等，或没返回有效值） | 合并后报**量具失效**，并在结论行点出是哪一层 ⇒ 先查工具；别层已确证的红仍优先。★ 2026-11-25 第 28 轮补上：此前会被当绿 |

★ 通用式：**「没测到」与「测出来不合格」必须是两种不同的退出码** ——
把它们压平成同一个码，上游就分不清该改产品还是该换环境。

### 4.5 ★ 状态判据用**结构类名**，不用中文文案

会话分析页的状态横幅是 `SaStateBanner`，有稳定的类名：

| 语义 | 结构选择器 | 来源 |
|---|---|---|
| 错误横幅 | `.sa-banner--error` | `web/src/views/admin/session-analytics/components/SaStateBanner.vue:25` |
| 陈旧横幅（503 `analytics_view_stale`） | `.sa-banner--stale` | 同上 |
| 加载占位 | `.sa-loading` | `SessionAnalyticsOverviewView.vue:34` |
| 跨单位口径说明 | `.sa-note` | 各视图 `<p class="sa-note">` |
| 空态 | `.app-empty-state` | `web/src/components/EmptyState.vue:35` |

★ **文案是展示层（随 locale 变），类名是契约层（不会）** ⇒ 状态判据必须用类名。
★ 反例（本轮真踩过）：旧版探针用 `body.includes('没能加载出来')` 判错误横幅 ——
换语种后**永远找不到** ⇒ 「无错误横幅」恒真 ⇒ **假绿**。


---

## 5. 动态路由 → 占位值对照表

flow 里的 URL 用 `${ENV_VAR}` 引用**运行时传入的变量**（通道 = `-e KEY=VALUE`，见 §4.3）。
★ **实跑取真值**：`maestro/tools/resolve-ids.mjs` 从**活着的网关**解析下列变量的真实值
（`run-local.sh` 会自动调用并逐个 `-e` 传给 Maestro）⇒ 下表里的「默认样例值」只是
「变量名 + 形态」说明，**不是**本机可用的值。

| 路由 | 动态段 | 环境变量 | 默认样例值 | 说明 |
|---|---|---|---|---|
| `/live/request/:requestId` | `requestId` | `REQUEST_ID` | `req-0001` | 实时拓扑里点进某条请求；样例 id 通常不存在 ⇒ 页面显示「没有数据」而不是详情。断言只认页面标题。 |
| `/providers/:id` | `id` | `PROVIDER_ID` | `1` | 供应商 id；建议改成本机真实存在的供应商 id，否则详情页是空态。 |
| `/tenants/:tenantId` | `tenantId` | `TENANT_ID` | `default` | 租户 code；`default` 是整站租户。 |
| `/tenant/orders/:id` | `id` | `ORDER_ID` | `1` | 订单 id；租户门户里的订单号。 |
| `/maas/orders/:id` | `id` | `ORDER_ID` | `1` | 同上（旧路径，会先 redirect 到 `/tenant/orders/:id`）。 |
| `/routing-v2/work-types/:key` | `key` | `WORK_TYPE_KEY` | `chat` | 工作类型 key；`chat` 是常见默认值。 |
| `/keys/:id` | `id` | `API_KEY_ID` | `1` | API 密钥 id。 |
| `/request-detail/:requestId` | `requestId` | `REQUEST_ID` | `req-0001` | 请求 id（全屏请求详情）。 |
| `/admin/session-analytics/users/:owner` | `owner` | `SESSION_OWNER` | `default` | 会话所有者标识。 |
| `/admin/session-analytics/clients/:id` | `id` | `CLIENT_ID` | `client-0001` | 客户端 id。 |
| `/admin/session-analytics/tasks/:id` | `id` | `TASK_ID` | `task-0001` | 任务 id。 |
| `/admin/approvals/:id` | `id` | `APPROVAL_ID` | `1` | 审批单 id。 |
| `/admin/sessions/:id` | `id` | `SESSION_ID` | `sess-0001` | 会话 id。 |
| `/admin/request-actions/:id` | `id` | `REQUEST_ID` | `req-0001` | 请求 id（Redis 里的动作时间线按 request_id 取）。 |
| `/admin/request-registry/journey/:requestId` | `requestId` | `REQUEST_ID` | `req-0001` | 请求 id。 |
| `/admin/connection-registry/:credentialId/recovery` | `credentialId/recovery` | `CREDENTIAL_ID` | `1` | 凭据 id。 |
| `/dev/live-request-preview/:requestId` | `requestId` | `REQUEST_ID` | `req-0001` | 仅 dev 构建；免登录别名。 |

另外两个非「id 段」但需要上下文的参数：

| 参数 | 环境变量 | 默认值 | 用在哪 |
|---|---|---|---|
| 租户门户上下文 | `TENANT_CODE` | `acme` | `/tenant/*` 与 `/maas/*` —— 见 §7 的「平台运维身份不能裸进租户门户」 |

---

## 6. 登录态怎么拿

### 6.1 主路径：UI 登录（已实现，`maestro/flows/_shared/login.yaml`）

所有需要登录的 flow 开头都是：

```yaml
- runFlow:
    file: _shared/login.yaml
    env:
      ADMIN_USER: ${ADMIN_USER}
      ADMIN_PASSWORD: ${ADMIN_PASSWORD}
```

共享登录流做的事（**全程只用驱动原生命令，一条 `evalScript` 都没有** —— 理由见 §6.3 与 §6.6）：

1. `openLink ${BASE_URL}/?login=1`。`App.vue` 的 watcher 看到 `login=1` 就 `openLogin()`（`web/src/App.vue:244`），
   于是登录弹窗出现（`/login` 路由也行：`LoginView.vue` 会 `router.replace('/?login=1')`）。
2. 等 `#login-modal-title` 可见 —— 弹窗标题是「登录控制面」（`web/src/components/LoginModal.vue`，locale `login.title`）。
3. 点 `#login-username` 填 `${ADMIN_USER}`，点 `#login-password` 填 `${ADMIN_PASSWORD}`
   （★ 用户名框默认预填 `admin` ⇒ 必须先 `eraseText`，`inputText` 是**追加**）。
4. **`pressKey: Enter`** 提交（理由见 §6.2）⇒ 走 SPA 自己的 `<form @submit.prevent="handleLogin">`
   ⇒ **服务端会话（cookie）与 SPA 内存态同源**。
5. **三段判据**（缺一段都算红；全部是驱动原生断言 ⇒ 不会静默空转）：
   ① `notVisible: id: login-modal-title` —— 弹窗关闭 ⇒ `handleLogin` 成功
      （失败时弹窗不关，顶部有红色横幅）；
   ② `visible: text: "系统管理员"` —— 顶栏用户菜单出现 ⇒ SPA 内存态已更新
      （`store.userInfo` 有值 ⇒ `isLoggedIn` 为真；「系统管理员」是种子账号的
      `display_name`，`admin/users.go:247` ⇒ 与环境无关）；
   ③ `openLink ${BASE_URL}/dashboard` 后 `visible: text: "系统管理员"` 仍在
      —— **服务端会话真的建立了**：web 驱动的 `openLink` 清 localStorage 但**保留 cookie**
      （见 `_shared/switch-simple-mode.yaml` 头注）⇒ 重载后只能靠 cookie + `/api/auth/me`
      水合出用户 ⇒ 排除「只有内存态、cookie 没落袋」。

★ 判据 ③ 就是「HTTP 200 但没有会话 cookie」那个坑的**真判据**；它用驱动原生方式实现，
  不依赖任何页面内 JS（页面内 JS 在 web 驱动下**根本跑不起来**，见 §6.3/§6.6）。

★ ★★ **2026-11-26 L135 轮加固（★ 共享资产，改动影响**所有**需登录的 flow ★）★★**：
判据 ①（`notVisible: id: login-modal-title`）现在**带一次重试**（★ `retry: {maxRetries: "2"}`，
★ **只包「等弹窗关闭」这一步**、★ 超时 20s ⇒ 最长等 40s ★）。

★ ★ **为什么只重试「等」而不重试整段输入（★ 实测撞出来的 ★）**：
★ 有一次失败的现场层级里 ★ **页面其实已经登录了**（顶栏「系统管理员 · 超级管理员」、
  `app-user-menu-name` 可见、★ **登录弹窗根本不在**）⇒ ★★ **登录是成功的**、★ 只是
  「弹窗关得慢」让判据 ① 超时 ⇒ ★★ 若把「输入 ＋ 等待」整段包进 retry ⇒ ★ retry 的第一步
  `tapOn login-username` ★ **必然扑空**（★ 弹窗已经关了 ★）⇒ ★★ **retry 会把「成功」变成「失败」**。
⇒ ★ **只重试「等」**：① 已关闭 ⇒ 立即过；② 关得慢 ⇒ 继续等；③ 真登录失败 ⇒ 等满仍红
⇒ ★★ **判据语义完全不变**。

★ ★★ **另一种已知间歇性故障（★ 「请求没发出」型 ★）**：★ 实测有一次失败现场是
★ **弹窗仍在、无错误横幅、无验证码输入框** ⇒ ★★ **后端根本没收到请求** ⇒ ★ **不是口令错、
也不是 captcha 闸门** ⇒ ★★ 而是 Maestro 的「键入/提交」没生效（★ 最可能：SPA 尚未 hydrate 时
`inputText` 写进了 DOM、但 v-model 没收到事件 ★）⇒ ★ 这种情况下本 flow 只能判红 ⇒
★ **调用方须按「量具失效」归类、而不是「产品红」**（★ 范式见
`maestro/run-users-role-governance.sh` 的「登录段红 ⇒ 量具失效」 ★）。
★ ★ **要彻底绕开 UI 登录 ⇒ 走 §6.5 的 CDP 预置会话**（★ 不受生产 captcha 闸门约束 ★）。

### 6.2 为什么提交用 Enter 而不是点「登录」按钮

**要点的控件**是弹窗底部 `.login-modal__actions` 里的 `<button type="submit">`，可见文案「登录」
（`web/src/components/LoginModal.vue:160-162`，locale `login.submit = '登录'`）。

但**不能**用 `tapOn: "登录"`：同一个页面上「登录」二字至少出现两处 ——
访客头部的 `<button class="guest-login-btn">{{ t('login.submit') }}</button>`（`web/src/App.vue:386`）和弹窗里的提交按钮。
Maestro 的 `tapOn` 文本匹配按 DOM 顺序取第一个命中，会打到访客头部那个按钮上 —— 它的行为只是「再次打开弹窗」，**不会提交表单**。
焦点停在密码框时按 Enter 会触发表单的 `@submit.prevent="handleLogin"`，是当前唯一确定能提交的路径。

### 6.3 ★ 备选：Cookie 注入 —— **在 web 驱动下不可用**（不要再照抄）

★ **先读这一段再写任何登录流**：web 驱动**没有 `evaluateScript`**（§6.6）⇒ flow 里
  `evalScript` 的脚本**压根不会发到浏览器**；`${output.*}` 那种形式只在 **JVM 侧**求值
  （没有 `document`/`fetch`/`location`）⇒ **在 flow 里没有任何办法让页面执行一次 `fetch`**。
  ⇒ 下面那段曾经流传的「页内 fetch 注入 cookie」配方**是跑不起来的**：
  它既不会注入 cookie，也不会报错（异常被吞），**flow 照样报绿** ⇒ 浏览器其实还是访客。

后端 `POST /api/auth/token`（`admin/auth.go:951` 注册路由、`admin/auth.go:174` 处理）会：

- 下发 **HttpOnly cookie `llmgw_session`**（`admin/auth_cookie_helpers.go:86` 的 `setSessionCookieRemember`），
- 同时在响应体里返回 `access_token`（`admin/auth.go:284-298`）。

前端所有 API 调用都带 `credentials: 'same-origin'`（`web/src/api/_core.ts:404`），所以**cookie 就是真正的会话**；
`jwtToken` 只存在内存里、不落 localStorage（`web/src/store.ts:118`）。

★ 2026-11-18 实测（154 生产站 / Maestro 2.11.0 web 驱动）：

| 探针 | 结果 |
|---|---|
| `evalScript` 往 `document.body` 插一个可见 div | 视口层级里**没有它**（页面一字未变），命令报 COMPLETED |
| `evalScript` 里**同步** `throw new Error(...)` | **仍报 COMPLETED / rc=0**（异常被吞） |
| `evalScript: '${output.probe = ...}'` 后 `openLink: ${output.probe}` | **不插值**（原样当 URL 打开 ⇒ `ERR_NAME_NOT_RESOLVED`）⇒ 值是只写的 |

★ 可用的两条路：
  - **有口令** ⇒ §6.1 的 UI 登录（唯一在 web 驱动下真正能建立会话的路径）。
  - **无口令** ⇒ §6.5 的 CDP 预置会话（`maestro/tools/seed-session.mjs`，
    用 `Network.setCookie` 直接写 HttpOnly cookie —— **不经页面 JS**，所以不受本节限制）。

### 6.4 已知坑：自适应验证码（★ 2026-11-25 逐行核实：这是一个**死锁**，不是「等一等就好」）

后端有自适应验证码闸门。★ 旧记载里的「等窗口过期」是**错的**——失败计数**没有 TTL**。

**确切机制（三段，逐行可查）**：

1. **计数是纯内存态、无 TTL**：`admin/auth_captcha.go:217`
   `loginFailuresGlobal = newLoginFailureCounter()`；阈值
   `CaptchaFailureThreshold = 3`（`admin/auth_captcha.go:74`）；
   判定 `required(key) = consecutive(key) >= 阈值`（`admin/auth_captcha.go:202`）。
   ⇒ 进程不重启 ⇒ 计数永不过期。
2. **闸门挂在密码校验之前**：`admin/auth.go:229`
   `if !h.verifyLoginCaptcha(w, clientIP, req.CaptchaID, req.CaptchaCode) { return }`
   —— 该处上方注释明写「刻意放在任何 DB 查询与 bcrypt 之前」。
   ⇒ `captcha_required` 时**走不到密码校验** ⇒ 就算口令是对的也过不去。
3. **清零只发生在「成功登录」**：`clearLoginFailures(clientIP)`
   （`admin/auth_captcha.go:509`）只在 `admin/auth.go` 的四条成功路径上被调用。
   ⇒ 第 2 条让「成功」在闸门打开后**不可达** ⇒ **计数永远清不掉**。

⇒ ★ **结论：某个来源 IP 一旦连续失败到阈值，它就被永久锁死**
  （直到网关进程重启把内存计数清掉）。仓内**没有任何管理端点能重置它**
  （`grep clearLoginFailures` 只命中成功路径与定义本身）。
  ⇒ 症状：登录弹窗里多出验证码输入框（`LoginModal.vue` 的
  `<CaptchaInput v-if="showCaptcha">`，服务端回 `captcha_required` 时置真），
  判据①（弹窗关闭）必红；**且重试多少次都不会好**。
  ⇒ **正解不是去解验证码**（验证码要人看图作答，Maestro 不会看图），
    而是**别走这条登录路径**：用 §6.5 的 CDP 预置会话
    （绕开 `/api/auth/token` ⇒ 压根不碰闸门 ⇒ 与该状态**无关**）。

★ **排查/验证用的隔离手法**（避免污染真实用户桶）：
  失败计数按 `clientIPFromRequest` 分桶，而它取的是 **`X-Forwarded-For` 的第一段**
  （`admin/auth.go:639`）；nginx 用 `$proxy_add_x_forwarded_for` ⇒ 真实 IP 被追加到
  末尾 ⇒ **第一段是客户端可控的** ⇒ 给请求加 `X-Forwarded-For: 10.99.99.99`
  就能把失败计数打进一个**隔离桶**，不碰任何真实用户的桶。
  实测（2026-11-25）：隔离桶连错 3 次后，同桶 `POST /api/auth/token` 返回
  **400 `captcha_required`**（连**正确口令**也一样 ⇒ 死锁成立），
  同桶 `GET /api/auth/me` 带预置 cookie 仍 **200** ⇒ 会话路径不受闸门约束；
  而不带 XFF 的真实桶 `required=false` ⇒ **没被污染**。

### 6.5 无口令路径：CDP 预置会话（`maestro/tools/seed-session.mjs`）

§6.1 的 UI 登录要管理员口令；§6.3 的 fetch 注入同样要口令。**没有口令时**
（CI、临时机器、值班机）还有一条路：用 Chrome DevTools Protocol 预置会话。

它做的事（零依赖，Node ≥ 22 自带 `WebSocket`）：

1. 连浏览器级 CDP（`/json/version` 的 `webSocketDebuggerUrl`）；
2. `Target.createTarget` + `Target.attachToTarget`（flatten）；
3. `Network.setCookie`：把一枚 **short-lived JWT** 写成 HttpOnly 的 `llmgw_session`
   （域/路径/secure 与后端 `setSessionCookieRemember` 对齐）；
4. `Page.navigate` 到目标站，`Runtime.evaluate` 写 localStorage
   （`llmgw_work_mode` / `llmgw_user_info`）；
5. 探测一次 `/api/auth/me` —— **确认注入的 cookie 真被后端认**（不是只写进了浏览器）。

★ JWT 从哪来：用 211 容器里的 `LLM_GATEWAY_SECRET_KEY` **在 211 本机现签**
（`scripts/audit/api-211-admin-probe.py` 的 `mint_token()`）⇒ **签名密钥不出机器**，
本地只拿到一枚 30 分钟的会话令牌。

```bash
# ① 在 211 上现签（密钥不出机器）
scp scripts/audit/api-211-admin-probe.py 211:/tmp/ && \
  ssh 211 'python3 -c "
import importlib.util,sys
spec=importlib.util.spec_from_file_location(\"p\",\"/tmp/api-211-admin-probe.py\")
m=importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
sys.stdout.write(m.mint_token(ttl=1800))"' > /tmp/llmgw-jwt.txt
chmod 600 /tmp/llmgw-jwt.txt

# ② 起 Chrome（CDP），再预置会话
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
  --headless=new --remote-debugging-port=9222 \
  --user-data-dir=/tmp/maestro-local-chrome-profile about:blank &
node maestro/tools/seed-session.mjs --cdp http://127.0.0.1:9222 \
  --url https://llm.itestu.cn --token-file /tmp/llmgw-jwt.txt --mode simple

# ③ 跑 flow（复用同一个 Chrome profile ⇒ 会话已在）
maestro test --driver-host-port 9222 --headless --no-ansi \
  -e BASE_URL=https://llm.itestu.cn maestro/flows/11-simple-overview.yaml
```

★ 两种路径的取舍：
- **有口令** ⇒ 走 §6.1 的 UI 登录（更真实，顺带把登录链路也测了）。
- **无口令** ⇒ 走 §6.5（CDP 预置），会话有效期 = JWT 的 TTL（建议 30 分钟），
  跑完 `rm -f /tmp/llmgw-jwt.txt`。

★ 工作模式（简易/专家）是**本机选择**（localStorage `llmgw_work_mode`），
不是服务端设置 ⇒ 换机器就是另一个模式，CI 上必须显式设置（见
`maestro/flows/11-simple-overview.yaml` 开头：`launchApp` 带 `clearState: true`
把 cookie 与 localStorage 一起清干净 ⇒ 登录后必定落在专家面，再点顶栏的
切换按钮（`data-testid="work-mode-switch"`）切到简易模式）。

#### 6.5.1 ★ runner 已内置这条路径（2026-11-25 captcha 死锁根治轮）

`maestro/run-stats-range-picker.sh` 现在**默认**走预置会话（不再依赖管理员口令）：

| 旋钮 | 默认 | 含义 |
|---|---|---|
| `SEED_ENABLE` | `1` | `0` ⇒ 退回纯 UI 登录路径（老行为） |
| `SEED_SSH_HOST` | `211` | 现签 JWT 的机器；**签名密钥不出这台机器** |
| `SEED_TOKEN_FILE` | 空 | 已备好的令牌文件（给了就不现签；**调用方自己负责删**） |
| `SEED_MODE` | `expert` | 注入的工作模式 |
| `SEED_TTL` | `1800` | 现签令牌的 TTL（秒） |

流程：**现签 → 验令牌 → 注入两个浏览器 → 跑 flow 与探针 → trap 删令牌文件**。
★ **要注入的是「两个浏览器」，但它们不是同一对**（★ 2026-11-25 实测踩出来的）：
  - **CDP 探针用的那个 Chrome**（runner 自己起的，`--remote-debugging-port=PROBE_CDP_PORT`）
    ⇒ 直接 `--cdp http://127.0.0.1:<port>` 注入 ✓
  - **Maestro 自己起的那个 Chrome**（★ flow 真正跑的那个 ★）
    ⇒ ⚠️ **不是** runner 起的那一个！Maestro 的 web 驱动用 Selenium 的 `ChromeDriver`
      **自起** Chrome（临时 profile `org.chromium.Chromium.scoped_dir.*`），
      ⇒ **`--driver-host-port` 对 web 完全无效**（那是给 Android adb forward 用的）
      ⇒ ⇒ **注入到 runner 的 Chrome 上＝注入了一个没人用的浏览器**（静默失效，不报错！）
    ⇒ 正确做法：**顺着父子关系认领**（`maestro` → `chromedriver` → `Chrome`）
      → 从 Chrome 命令行取 `--user-data-dir` → 读 `<profile>/DevToolsActivePort` 拿 CDP 端口
      → `seed-session.mjs --reuse-tab` 接管它的页（**不关它**，那是 flow 要用的）
⇒ ★ 注入**必须发生在 flow 的第一次导航之前** ⇒ flow 的第一步是一个
  `extendedWaitUntil {visible: app-user-menu-name, optional: true}`：
  注入成功后那一页会显示已登录 ⇒ 顶栏出现用户菜单 ⇒ 这个等待**提前结束**；
  没预置时它等满 timeout 也**不判红**（实测：optional ⇒ AssertionFailure 降级为 warning）
  ⇒ 后面自然走 UI 登录。★ flow 的第二段因此是条件分流（见下）。
★ 令牌落在 **0600 临时文件**里、**不打印**、**跑完即删**（trap 负责；
调用方自带的 `SEED_TOKEN_FILE` 不归脚本管 ⇒ 不删）。
★ 注入前先 `GET /api/auth/me` 验一次（只取状态码）：令牌不被后端认就别注入
——否则等于把一张废卡塞进浏览器，后面照样走成访客 ⇒ 又回到 captcha 死锁。
★ ★ **子进程必须有「超时 ＋ 强制退出」**：实测 `seed-session.mjs` 的 WebSocket
  **close 握手可能不返回** ⇒ Node 进程不退出 ⇒ **上游 runner 被卡死**（只能人工杀）
  ⇒ 工具内已加**显式 `process.exit` ＋ unref 看门狗**，runner 侧再加 `run_with_timeout`
  （★ macOS 的 bash 3.2 **没有 `timeout` 命令** ⇒ 自己实现 ★）。

★ **flow 里怎么区分「已预置」与「要 UI 登录」？**
  判据只有一个：**登录弹窗出没出现**（`when: visible: id: login-modal-title`）。
  两态都实测过（2026-11-25，探针直读 DOM）：

  | 状态 | 访问 `/?login=1` | `login-modal-title` | `app-user-menu-name` | `llmgw_user_info` |
  |---|---|---|---|---|
  | 访客（无预置） | 停在 `/?login=1` | **在** | 不在 | absent |
  | 预置会话 | 停在 `/?login=1` | **不在** | **在** | present |

  机制：预置时 `seed-session.mjs` 往 localStorage 写了 `llmgw_user_info`
  ⇒ `web/src/store.ts:123` 在**模块加载时**回填 `store.userInfo`
  ⇒ `App.vue:58` 的 `isLoggedIn` 在 setup 时已为真
  ⇒ `App.vue:247` 的 `watch(route.query.login)` 不再调 `openLogin()` ⇒ 弹窗不开。
  ⇒ 这是**确定性**判据（不靠「等多久」），且天然降级：
  令牌失效时弹窗会出现 ⇒ 自动退回 UI 登录路径。

★ **为什么把 flow 18 原 §0 的「起点归零（登出）」删掉了？**
  因为它和预置会话**互斥**：登出会把刚注入的会话打死 ⇒ 预置路径根本走不到 §2。
  而它原本要防的「上一轮留下登录态」在本 runner 里**不存在**
  （每轮起全新 Chrome + 全新 profile）⇒ 该步骤在新设计下**没有存在价值**，保留即事故。

★ Maestro 的 `when` 语义（2.11.0 源码核实，`maestro-orchestra/Orchestra.kt` 的
  `runFlowCommand`）：条件为假 ⇒ 整段 `runFlow` 记为 **SKIPPED**（不是 FAILED）
  ⇒ 预置会话下登录段被跳过，flow 仍然全绿。
  ★ `runFlow` 的 `when` 只认 `visible` / `notVisible` / `scriptCondition`（元素/脚本条件），
  **不支持**按 env 变量分支 ⇒ 「有预置就跳过登录」只能落在**页面状态**上
  ⇒ 这就是上面那张表存在的理由。

### 6.6 ★ web 驱动的能力边界（2026-11-17 逐条实测，**写 flow 前必读**）

★ Maestro 2.11.0 的 **web 驱动**（`CdpWebDriver`）与移动端差别很大，下列边界都是
**实测**出来的（证据见 `docs/audit/2026-11-17-简易总览页优化-主档.md` §7.4）：

| 能力 | 实测 | 怎么用 |
|---|---|---|
| `evalScript` | **no-op**：命令报 COMPLETED，页面一字未变，**异常也不报** | **不要用**。`javap -p maestro.drivers.CdpWebDriver` 的方法表里根本没有 `evaluateScript`；★ **2026-11-18 复测（154 生产站）**：往 `document.body` 插一个可见 div ⇒ 视口层级里**没有它**（Maestro 自启 Chrome / 复用 9222 的 Chrome 两种配置都一样）；脚本里**同步** `throw new Error(...)` ⇒ **仍报 COMPLETED / rc=0**（异常被吞） |
| `${output.x = ...}` | 只在 **JVM 侧**求值 ⇒ 拿不到 DOM（`document` 未定义）；★ 且**只写不读**（放进 `openLink` 不插值 ⇒ 原样当 URL 打开） | 只适合纯算术（计时），**不可用于任何页面内判据** |
| `assertVisible: { css }` | 可用但**极慢**（单条实测 2 分 23 秒） | 少用；能换 `id:` 就换 |
| `assertNotVisible: { css }` | **直接挂住**（无超时上限） | 不要用 |
| `assertVisible: { text }` | 可用，**秒级**；但**只匹配可见文本**且是**元素完整文本相等**（**不是子串包含**！实测「下一步该做什么」这种半句匹配不到）；`aria-label` / `title` **不参与**（`tapOn "切到专家模式"` ⇒ Element not found）。★ **2026-11-17 补测：它是「全匹配正则」**（命令 JSON 里字段名就是 `textRegex`）⇒ `.*….*` 可以用来选**读数会变**的元素（实测 `text: ".*条 · 成功率.*"` 命中统计胶囊，而 `text: "成功率"` 这种裸子串不命中） | 判据一律用**整句**（`text: "工作模式 · 简易模式"`、`text: "今日请求"`）；读数会变的元素用 `.*….*` 包起来 |
| `assertVisible: { id }` | 可用，秒级。★ **2026-11-17 复测定论**：`id:` **能**选到 `data-testid`（实测 `id: stage-tree-canvas`（div）、`id: toggle-probe`（label）均命中）—— 此前「只匹配 HTML `id`」的记载是**错的**。★★ **2026-11-23（L13 补轮）更正后半句**：原文写「**`<button>` 命中不了**」⇒ ★★ **该推广是错的** —— 判别因素**不是元素类型**，而是 ★ **元素的 `title` / `aria-label` 会盖掉 `data-testid`**（web 驱动取 hierarchy 的 `resource-id` 时，`aria-label` > `title` > `data-testid`）。实测：`<button data-testid="sa-tab-models">`（**无** title/aria-label）⇒ `id:` **命中**（真机 COMPLETED）；`<button data-testid="stage-stats-open" :title="…">` ⇒ hierarchy 的 resource-id 是「展开统计（全页）」⇒ `id:` **命中不了**；`<button data-testid="work-mode-switch" :title="…" :aria-label="…">` ⇒ resource-id 是「切到简易模式」⇒ 同样命中不了 | 需要 testid 级精确定位 ⇒ `id:` 可用于 **div/label/span/article/button**；★ **但被 `title`/`aria-label` 遮蔽的元素除外** —— 那类元素要么去掉 title/aria-label，要么把 data-testid 挪到**不带 title 的外层包裹元素**上，要么走 `text:`。★ 全仓遮蔽审计：`python3 maestro/tools/audit-maestro-id-selectors.py`（门：`scripts/checks/maestro-id-selector-shadow-gate.sh`）｜★ ★★ **2026-11-24（L34 九轮）补「存在性」那半**：★ 上面的审计只答「**能不能被 `id:` 命中**」（★ 遮蔽 ★），★ 不答「**这个锚点在前端存不存在**」⇒ ★★ 而「不存在 ＋ `optional: true`」＝ **断言恒真**（★ 真机上「找不到」也不判红 ⇒ 从没验证过任何东西 ★★）⇒ ★⇒ 全仓**锚点存在性**审计：`python3 maestro/tools/audit-flow-anchor-existence.py`（★ 门：`scripts/checks/maestro-flow-anchor-gate.sh`、★ **只有 python3 ⇒ 进得了 CI 载体** ★★；★ 豁免表 `maestro/flow-anchor-allowlist.tsv`、★ **只许收窄** ★★）★ ★★ **两半必须一起看**：★ 「存在」与「可命中」是两件事（★ 实测：把一个幽灵锚点换成真实存在的 `gateway-core-box` 后、★ 遮蔽门当场红 —— 那个 `<section>` 带 `:aria-label` ⇒ `id:` 选不到 ⇒ **等于把一个静默故障换成另一个** ★★）★★ |
| `pressKey: Escape` | **不支持**（实测报 `Keycode ESCAPE is not supported on web`）⇒ 组件的「Esc 出口」在 flow 里**验不了** | 走页面内 CDP（`maestro/tools/assert-stage-tree.mjs` 在页面里派发 `keydown`） |
| **view hierarchy 只覆盖视口** | 视口固定 **1024×625**（hierarchy 根节点 `bounds = [0,0][1024,625]`）⇒ 折叠线以下的内容**不进树** ⇒ `assertVisible` 对视口外元素**一律判 false**（哪怕它已渲染在 DOM 里） | 视口外的元素先 `scrollUntilVisible`（真滚动：`window.scrollY + innerHeight/2`）→ 再断言 → 再滚回顶部 |
| `css` + `text` 组合 | **不可用**（实测 FAILED） | 一条断言只用一种 |
| `--driver-host-port <port>` | 对 web 驱动**无效** | web 驱动自己起 Chrome（独立 profile/端口）⇒ 别传 |
| `--screen-size WxH` | **有效**（Web only），可调视口 | 默认 1024×625；本项目两栏在 `@media (max-width: 1024px)` 下塌成一栏 ⇒ 想测两栏布局就传 `--screen-size 1440x900` |

★ 「报 COMPLETED 但什么都没做」是这里最危险的失败模式：一条**从未执行过**的断言
看起来通过了。凡是依赖 `${output.*}` 的断言，在 web 驱动下都是**静默空转**。

★ 需要「页面内的数值逐字对拍」（统计口径、请求扇出、精确计数）时，
  flow 做不了 ⇒ 用 `maestro/tools/assert-overview.mjs`（见 6.7）。

### 6.7 ★ 页面内数值对拍：`maestro/tools/assert-overview.mjs`

★ 为什么单独一个脚本：有些断言**只能在页面内做** —— 例如「总览页上的今日请求
  必须与 `/api/admin/dashboard/board` 的 `summary.total_requests` **逐字一致**」。
  在 flow 外面用 curl 对拍会**假红**（两次请求之间流量在涨）；
  而在页面内用**同一个响应**对拍，口径天然一致。

它做（一次页面内求值）：
1. 统计卡 / 快捷操作的**精确计数**（flow 没有 count 断言）；
2. 六张卡的 label 与 value 逐字抓出；
3. 与 board API 的 `total_requests` / `active_api_keys` / `active_models` **同页同刻**对拍；
4. 「读不到 ≠ 0」：源不可读时必须是占位符，不是 0；
5. **不得**出现 per-key 的 `/api/usage/{id}/models` 扇出；
6. 状态条 `data-tone` ∈ {ok, warn, blocked}、三步接入「不存在或恰好三步」、
   上游健康四档分级（`可用` / `有告警` / `不可达` / `未探活`，未探活必须单列）。

```bash
node maestro/tools/assert-overview.mjs \
  --cdp http://127.0.0.1:9222 \
  --url https://llm.itestu.cn \
  --token-file /tmp/llmgw-jwt.txt
# 退出码 0 = 全绿；1 = 有断言失败（报告里逐条列出）
```

★ 公共件在 `maestro/tools/cdp-client.mjs`（零依赖，Node ≥ 22 自带 WebSocket）：
`connect()` / `openPage()` / `evaluate()` / `waitFor()`，`seed-session.mjs` 与
`assert-overview.mjs` 共用。
★ 安全约束：JWT 只从 `--token-file` 读（0600），**不打印**；用完删掉那个文件。

---

## 7. 计时机制：「>2 秒打开的页面」怎么被筛出来

### 7.1 机制

每个页面在 flow 里是同一套四步：

```yaml
# ── /admin/settings ─────────────────────────────────────────────
- evalScript: "${output.t0 = Date.now()}"          # ① 导航前打点
- openLink: "${BASE_URL}/admin/settings"           # ② 导航
- extendedWaitUntil:                              # ③ 等该页特有的文案出现
    visible: '当前值'
    timeout: 20000
- evalScript: "${output.ms__admin_settings = ...}"  # ④ 算毫秒
- assertTrue:                                     # ⑤ 2 秒软阈值
    condition: "${output.ms__admin_settings < 2000}"
    optional: true
    label: "slow-check:/admin/settings"
```

第 ④ 步的表达式（逐字）：

```js
output.ms__admin_settings = Math.max(
  output.t0 ? Date.now() - output.t0 : 0,
  (typeof performance !== 'undefined' && performance.now) ? Math.round(performance.now()) : 0
) || -1
```

三个设计点：

1. **取两个来源的较大值**，任何一个可用就行：
   - `Date.now() - output.t0`：导航前打点到页面关键文案可见的**墙上时钟差**。
   - `performance.now()`：**当前文档从导航开始**到此刻的毫秒数（浏览器页面时钟）。
   - 之所以要双来源：`output.*` 存在 Maestro 的 **宿主 JS 引擎**里
     （`~/.maestro/lib/maestro-client.jar` 的 `maestro/js/GraalJsEngine` 里有 `outputBinding`），
     所以跨导航**不会丢**，`Date.now()` 差值一定可用；
     而宿主沙箱里没有 `performance`（浏览器 API），此时该项退化为 0，不影响结果。
     ★★ **这一点在 2026-10-28 实测坐实**：在 web flow 的 `runScript` 里写
     `console.log(location.pathname)` 会得到
     `TypeError: Cannot read property 'pathname' of undefined`
     ⇒ ⇒ **web 模式下 Maestro 的 JS 沙箱里没有 `location` / `performance`**，
     ⇒ ⇒ **实测值完全来自 `Date.now() - output.t0` 这一路**（纯墙钟）。
     ⇒ ⇒ **这条计时通道是真实可信的**（不是假红）。
   - 万一 `output.t0` 因任何原因不可用，`performance.now()` 仍然能给出正确的「页面打开耗时」。
2. **`|| -1` 是哨兵**：两项都拿不到时记 -1（= 没测到），不会伪装成 0ms。
3. **阈值断言是 `optional: true`** ⇒ 慢页**只标记、不让 flow 失败**。
   这正是「事后筛出来」而不是「当场卡死」的前提。

### 7.2 变量命名

`output.ms__<slug>`，`<slug>` = 路由 path 去掉首斜杠、`:param` 去掉冒号段、`/` 换成 `_`、其余非字母数字换成 `_`。
例如：

| 路由 | slug |
|---|---|
| `/forbidden` | `forbidden` |
| `/` | `root` |
| `/bootstrap` | `bootstrap` |
| `/customer/update-activate` | `customer_update_activate` |
| `/admin/data-lifecycle/migration-state` | `admin_data_lifecycle_migration_state` |

### 7.3 事后筛选

★ **2026-10-28（R176）实跑改写了这一节**：旧的「从 CLI 日志里正则抠 `ms__`」是**假通道** ——
Maestro 只把 evalScript 打成 `Run ${...}... COMPLETED`，**从不回显赋值结果** ⇒ 旧脚本永远读不到数。
真正可用的机器可读通道是 Maestro 的调试产物里的 **`commands.json`**：每条命令都带
`metadata.duration`（毫秒）⇒

> **页面打开耗时 = `openLink` 的 duration + 其后 `extendedWaitUntil` 的 duration**

（后者就是「等该页关键文案出现」花掉的墙钟）。

实测对拍（2026-10-28，修复前的那台网关）：`/forbidden` 的
`openLink=813ms` + `extendedWaitUntil=9275ms` = `10088ms`，
与 flow 里 `output.ms__forbidden` 实测的 `10463ms` 同量级 ⇒ **通道可信**。

```bash
# 推荐入口：跑法见 §8（run-local.sh 会把产物落到 maestro/reports/<时间戳>/）
node maestro/tools/slow-pages.mjs --artifacts maestro/reports/<时间戳> --threshold 2000
```

`maestro/tools/slow-pages.mjs` 会打印一张按耗时倒序的表，并单独列出超过阈值的路由；
**退出码 1 = 有页面超阈值**（可以直接当 CI 卡口用），0 = 全部达标，2 = 用法错。

> ★ 备用路径 `--junit` 仍在（读 slow-check 软断言的失败 testcase），
> 但**主路径是 `--artifacts`**：JUnit 只在断言失败时才有信息，
> 而 `--artifacts` 读的是**每条命令的 duration** ⇒ 能给出**全部**页面的耗时分布，
> 不只是「慢的那几个」。


---

## 8. 跑法

### 8.1 推荐入口：`maestro/run-local.sh`

```bash
# 全部功能 flow（不含 dev-only）；自动解析动态路由 ID、自动起/复用 CDP Chrome
bash maestro/run-local.sh

# 连 90-dev-only.yaml 一起跑
bash maestro/run-local.sh --with-dev

# 产物：maestro/reports/<时间戳>/{01..09,90}.log + summary.txt
# 慢页筛选（读产物里的 commands.json）
node maestro/tools/slow-pages.mjs --artifacts maestro/reports/<时间戳> --threshold 2000
```

脚本做四件事：① 探测 JDK → ② 起/复用 headless Chrome（CDP） → ③ 从**活着的网关**解析
动态路由的真实 ID → ④ 逐 flow 实跑并留档。

### 8.2 手工跑单个 flow

```bash
export JAVA_HOME="$HOME/kaixuan/jdk17/Contents/Home"
export PATH="$JAVA_HOME/bin:$HOME/kaixuan/bin:$PATH"

maestro test \
  --driver-host-port 9222 --headless --no-ansi \
  --test-output-dir maestro/reports/manual \
  -e BASE_URL=http://127.0.0.1:8782 \
  -e "ADMIN_USER=<本机管理员>" \
  -e "ADMIN_PASSWORD=<本机口令>" \
  -e PROVIDER_ID=2 -e REQUEST_ID=... \
  maestro/flows/02-overview-live.yaml
```

### 8.3 ★ 五个实测踩过的坑（2026-10-28；2026-11-18 补第 5 条）

| # | 坑 | 现象 | 正解 |
|---|---|---|---|
| 1 | **YAML 头必须是 `url:`** | 写了 `appId:` ⇒ Maestro 判成**移动端 flow** ⇒ 报「Web support is in Beta」且一条命令都不跑 | 头部写 `url: ${BASE_URL}`（`YamlConfig.getUrl` ⇒ `FileUtils.isWebFlow`） |
| 2 | **CLI 没有 `--env-file`** | `--env-file` 直接被拒；而且 **shell 环境变量不参与 `${}` 解析** | 只能用 `-e KEY=VALUE` 逐个传；口令从仓根 `.env.local` 读、由 `run-local.sh` 代传（**口令不落仓内任何文件**） |
| 3 | **不要加 `-p web`** | `-p web` 会把 flow 筛成 0 个设备 ⇒ 「no flows to run」 | 不加 `-p`；改用 `--driver-host-port 9222` 连自起的 CDP Chrome（该选项 help 里没列，但确实被接受） |
| 4 | **`evalScript` 跑在 Maestro 侧沙箱** | web flow 的 `runScript` 里 `location` / `performance` 都是 `undefined` | 计时只用 `Date.now() - output.t0`（跨导航不丢）；**页面内部时间线要另用 CDP 探针** |
| 5 | **★ flow 的 `env:` 块会【压过】CLI 的 `-e`** | flow 里写了 `env: BASE_URL: https://llm.kxpms.cn`，同时 `-e BASE_URL=https://override.example` ⇒ 实测 **`-e` 不生效**（`-e` 放 flow 前 / 后都一样） | **不要在 flow 里写变量的默认值** ⇒ 默认值放**调用侧**（`run-local.sh` 已有 `BASE_URL="${BASE_URL:-http://127.0.0.1:8782}"`）；否则本机 mock 跑法会被**静默改成打生产** |

★ 第 5 条是 2026-11-18 用探针 flow 实测出来的（4 个环节逐条验：
`--help` 只有 `-e` / shell env 不参与解析 / flow `env:` 压过 `-e` / flow `env:` 确实能提供默认值），
详见 `docs/audit/2026-11-17-live阶段树设计对齐-主档.md` §7.5.18。

★ **只想跑一个 flow（对着 154 生产站）**：

```bash
BASE_URL=https://llm.kxpms.cn bash maestro/run-local.sh maestro/flows/12-live-stage-tree.yaml
```

★ **若坚持直接用 `maestro test`，口令必须这样传（`export` 不能省）** ——
bash 的**前缀赋值 + 同命令引用 = 旧值（常为空）** ⇒ 漏了 `export` 会把空串传给 Maestro
⇒ 浏览器去开 `https://dashboard/` ⇒ **第 3 步就红，且症状极具误导性**：

```bash
cd <repo> && set -a && . ./.env.local && set +a && \
export BASE_URL=https://llm.kxpms.cn \
       ADMIN_USER="$LLM_GATEWAY_ADMIN_USER" ADMIN_PASSWORD="$LLM_GATEWAY_ADMIN_PASSWORD" && \
maestro test --headless --no-ansi --screen-size 1440x1400 \
  -e BASE_URL="$BASE_URL" -e ADMIN_USER="$ADMIN_USER" -e "ADMIN_PASSWORD=$ADMIN_PASSWORD" \
  maestro/flows/12-live-stage-tree.yaml
```

★ **一条命令跑完 15 条验收证据**（含上面这条 flow、两层页面内断言、线上身份、bundle 契约串、
可追溯性、合并试演、**F1 专项**）：`bash maestro/verify-stage-tree-evidence.sh --with-browser`

★ **「当前在役是哪一版」是动态取的**（脚本自动取 `deploy-154/seq*` 里 seq 最大的 tag）
⇒ **每次发版后不必改脚本**；★ 若你手写新检查，**不要把 tag 名写死**（写死 ⇒ 永远指向旧发布）。
★ 采集器的 ⑩ 项在**有冲突**时会输出**冲突文件 + 两侧差异**（不只给退出码）⇒ 红的归因直接可读。

★ `maestro test` **没有 `--dry-run`**（本机 CLI 2.11.0 的 `TestCommand` 里没有这个选项；
只有一个独立的 `check-syntax` 子命令）。语法校验是**用 YAML 解析器逐个文件解析 + 结构断言**做的：

```bash
python3 - <<'PY'
import yaml, glob
for f in sorted(glob.glob('maestro/flows/**/*.yaml', recursive=True)):
    docs = list(yaml.safe_load_all(open(f, encoding='utf-8')))
    assert isinstance(docs[1], list), f
    print(f, len(docs[1]))
PY
```

### 8.4 ★ 断言文案的匹配语义（2026-10-28 实测）

Maestro 的 `visible:` 是**全匹配**，且**不 trim**、取的是元素的 `textContent`（**不是** `innerText`）。
⇒ 页面上真实渲染的文本只要带**前后空白 / 上下文前缀 / 徽标数字**，写「干净的短文案」就会红。

实测到的三种污染形态（全部已用 CDP 探针取到原文）：

| 形态 | 页面 | 元素原文 | 写法 |
|---|---|---|---|
| 前后空白 | `/routing-v2/annotations` 的页签按钮 | `" 标注工作台 "` | `visible: '.*标注工作台.*'` |
| 上下文前缀 | `/tenant/pricing` 的标题 | `"默认租户 · 套餐与充值"` | `visible: '.*套餐与充值.*'` |
| 徽标数字 | `/key-applications` 的 H2 | `"密钥申请 1 待审"` | `visible: '.*密钥申请.*'` |

⇒ **规则：能用正则就用两侧 `.*` 的正则，只锚住稳定的那一段。**
★ 正则可用性有**阳性 + 阴性对照**的实测（2026-10-28）：`.*标注工作台.*` 与
`.*套餐与充值.*` 均 COMPLETED，而 `.*绝不存在的文案ZZZQQQ.*` **FAILED**
⇒ 不是「什么都能过」。


---

## 9. 路由层面的坑（逐页验证时会撞上的）

1. **`/` 和 `/dashboard` 是同一个组件但渲染不同**。`HomeView` 按登录态分叉（`web/src/views/HomeView.vue:1-8`）：
   登录 ⇒ `DashboardView`（标题「仪表盘」），未登录 ⇒ `LandingView`（hero 文案）。所以访客态与登录态要各验一次。
2. **★ 重定向路由由「目标路由的 `meta`」决定要不要登录**，不是自己的 `meta`。
   vue-router 4 在 `pushWithRedirect()` 里先 `handleRedirectRecord()`、后跑守卫
   （`web/node_modules/vue-router/dist/vue-router.mjs:1282-1296`）。所以 `/customer/site` 等 4 条虽然自己没写 `meta.public`，
   却因为跳到公开的 `/customer/update-activate` 而**不需要登录** —— 这就是它们被放进不登录的 `01-public-guest.yaml` 的原因。
   反过来 `/catalog` 跳 `/models`（`requiresPlatformOps`）、`/routing` 跳 `/routing-v2`（`requiresSuper`）⇒ **需要**登录。
3. **`/ops/*` 有两条互斥的出路**（`router.ts:488` + `router.ts:117-127`）：
   - Maintain **不可达**（本机独立网关的常态）⇒ 守卫改写到 `/customer/update-activate`；
   - Maintain **可达** ⇒ `window.location.replace('/maintain/ops/*')` 全页跳到 maintain SPA（**不是**网关这个 SPA，断言文案会全部落空）。
   `09-ops-maintain.yaml` 的主断言按「不可达」写（文案「更新与激活」），并额外挂了一条 optional 断言兜住 Maintain 不可用时渲染的
   「503 · 运维平台不可用」。**如果本机起了 maintain，这 5 条要改断言。**
4. **`/ops/vibecoding` 带 `requiresOpsPlatform`**（`router.ts:359`）⇒ Maintain 不可达时会被守卫改写到 `/customer/update-activate`，
   根本到不了 `VibeCodingView`。同理。
5. **`/tenant/*` 在「平台运维身份」下必须带 `?tenant=<code>`**（`router.ts:496-502`）：
   super_admin + default 租户如果不带 `?tenant=`，访问 `/tenant/*` 会被弹回 `/`。
   所以 `04-tenants-portal.yaml` 里这几条都带了 `?tenant=${TENANT_CODE}`。
6. **软 bootstrap 闸门可能把深链弹到 `/bootstrap`**（`router.ts:382-434`）：
   未激活（`localStorage.llmgw_activated !== '1'`）且后端 `/api/system/bootstrap/status` 返回 `activated=false` 时，
   任何路径都会先跳 `/bootstrap?redirect=...`。**首次装机的机器上所有深链都会走进 bootstrap 流程**，
   断言前要先完成激活，或让后端返回 `activated=true`。
7. **未登录深链不是跳 `/login`，而是跳 `/?login=1&redirect=...`**（`router.ts:465-467`）。
   这就是登录流走 `/login` → 弹窗这条路径的原因（`LoginView` 自己也会 replace 到 `/?login=1`）。
8. **404 兜底 `/:pathMatch(.*)*` redirect 到 `/`**（`router.ts:374`）—— 不是 404 页。
   `01-public-guest.yaml` 里专门有一条用不存在的路径探它，断言落地页 hero 文案。
9. **`/maintain/:pathMatch(.*)*` 是 glob 兜底**，会吃掉所有 `/maintain/**`；`MaintainUnavailableView` 的标题是
   「503 · 运维平台不可用」（`web/src/views/MaintainUnavailableView.vue:41`）。若 maintain 真的部署了，nginx 直接把 `/maintain/*` 路由给 maintain SPA，这条断言会失效。
10. **三条 `/dev/*` 路由只在 dev 构建注册**（`router.ts:361-373` 的 `import.meta.env.DEV`）。
   生产构建下访问它们会落到 catch-all ⇒ 断言的是落地页而不是 dev 页。已单独拆到 `90-dev-only.yaml`。
11. **动态段路由的样例 id 很可能不存在**。这些页面（`/providers/:id`、`/keys/:id`、`/admin/sessions/:id` 等）在 id 不存在时渲染的是**空态/错误态**；
    所以断言一律挑「页面骨架上恒定存在」的文案（页标题、列头、切换按钮），**不挑数据相关的文案**。
    要验数据态就跑 `maestro/run-local.sh` —— 它会用 `resolve-ids.mjs` 解析出的**真 id** 覆盖样例值。
12. **`/routing-v2/work-types/settings` 和 `/routing-v2/work-types/:key` 与 `/routing-v2/work-types` 是同一个组件**
    （`router.ts:231-233`，都是 `WorkTypesView`）⇒ 三条断言点相同，这是事实，不是漏写。
13. **`/routing-v2/annotations`（含 `/stats`）、`/routing-v2/task-profile`、`/routing-v2/auto-tuning` 四个旧路由**
    都 redirect 到 `/routing-v2/auto-ops` 的不同 `?tab=`（`router.ts:239-248`）。
    非 super_admin 深链 `?tab=profiles|tuning` 会被页内 `normalizeTab` 弹回标注页签 ⇒ 断言用的是**页签文案**（标注工作台/标注统计/任务档案/调参审批），
    这正好能区分「跳对了且 tab 也对」。
14. **`/probe-health` redirect 到 `/routing-v2/credentials?tab=probe-health`**（`router.ts:226`），
    而「探测健康」页签**只有 super_admin 看得到**（`web/src/views/CredentialMonitorWithTabs.vue:33-36`）。
    非 super 会被 `normalizeTab` 弹回「列表视图」⇒ 断言「探测健康度」会失败。用 super_admin 账号跑。
15. **`/admin/usage-trends` 带 `meta.fillViewport`**（`router.ts:317`）⇒ 该页铺满主区、不走普通容器，布局与其它页不同。
16. **未登录时 `isAuthed()` 还有 localStorage 兜底**（`router.ts:129-147` 读 `llmgw_user_info`）：
    localStorage 里有旧的 `role`+`id` 但 cookie 已过期时，前端守卫会放行、而后端 API 返 401。
    看到「页面进去了但全是错误态」先怀疑会话过期，别怀疑路由。

---

## 10. 目录结构

```
maestro/
├── README.md                  ← 本文件
├── .gitignore                 ← 忽略 env.local.yaml 与 reports/
├── env.yaml                   ← 环境变量模板（★ 无口令）
├── run-local.sh               ← 本地逐页入口（默认 BASE_URL=http://127.0.0.1:8782）
├── run-simple-overview.sh     ← 简易模式总览页入口（--screen-size 1440x1400）
├── run-stage-tree.sh          ← 请求阶段树入口（--screen-size 1440x1400，2026-11-17 新增）
├── run-stats-range-picker.sh  ← ★ 时间范围统一 + 统计说明（三层：flow 18 / 主探针 / 自定义区间补充）
├── run-request-logs-time-ranges.sh  ← ★ 请求日志「时间范围 / 超时 / 加载态」（两层：flow 19 / CDP 探针，2026-11-25）
├── flows/
│   ├── _shared/login.yaml     ← 共享登录流（被 runFlow 调）
│   ├── _shared/switch-simple-mode.yaml ← 幂等切到简易模式
│   ├── _shared/request-logs-range.yaml ← 请求日志「选档位 ⇒ 加载收尾 ⇒ 有数据 ⇒ 无报错」共享子流（被 flow 19 调）
│   ├── 01-public-guest.yaml
│   ├── 02-overview-live.yaml
│   ├── 03-providers-models.yaml
│   ├── 04-tenants-portal.yaml
│   ├── 05-routing.yaml
│   ├── 06-keys-requests.yaml
│   ├── 07-admin-config.yaml
│   ├── 08-admin-data-ops.yaml
│   ├── 09-ops-maintain.yaml
│   ├── 11-simple-overview.yaml
│   ├── 12-live-stage-tree.yaml ← 请求阶段树 /live2（2026-11-17 新增）
│   ├── 18-stats-range-and-scope-hint.yaml ← ★ 时间范围统一 + 统计说明（2026-11-25 统一轮）
│   ├── 19-request-logs-time-ranges.yaml ← ★ 请求日志：切换时间范围后数据正确 / 不超时 / 加载态可见（2026-11-25 根修轮）
│   └── 90-dev-only.yaml
└── tools/
    ├── cdp-client.mjs         ← 零依赖 CDP 公共件（connect/evaluate/openPage/waitFor）
    ├── result.mjs             ← ★★ 断言**判定口径的唯一真源**：summarize（工具内口径）＋
    │                             mergeExitCodes（跨工具口径：红 > 量具失效 > 绿；未知退出码不算绿）
    ├── selftest.mjs           ← ★ 判定口径自检（不需浏览器；把三种结局与合并优先级钉死）
    ├── assert-overview.mjs    ← 简易总览页的页面内数值对拍
    ├── assert-stage-tree.mjs  ← 请求阶段树的页面内结构/口径对拍（2026-11-17 新增）
    ├── assert-stats-range-picker.mjs  ← ★ 时间范围四页的结构/语义对拍（26 条，2026-11-25 统一轮）
    ├── assert-board-op-custom-range.mjs ← ★ 运营面板**自定义区间 + 7 档位逐个**对拍（5 条，2026-11-25）
    ├── assert-board-op-list-deeplink.mjs ← ★ 运营看板**列表页深链**对拍（4 条：start/end 深链到达后端 / 显示=数据 / 深链优先于 days / 默认 7 天，2026-11-25 第 30 轮）
    ├── assert-request-logs-time-ranges.mjs ← ★ 请求日志「窗口 / 界面 / 中止 / 加载态 / 多条件矩阵」语义对拍（2026-11-25 根修轮）
    ├── probe-selectors.mjs    ← 选择器语义实测探针（2026-11-17 新增）
    ├── seed-session.mjs       ← 无口令路径：CDP 预置登录态（★ `--reuse-tab`：接管**已有**的页而不新建 ⇒ 用来给「Maestro 自起的那个 Chrome」注入）
    └── slow-pages.mjs         ← 从运行产物里筛「>2 秒」的页面
```

★ 目录树只列**主干资产**；`maestro/flows/` 与 `maestro/tools/` 下的**完整清单**以目录实际内容为准
（★ 2026-11-25 审计轮如实登记：本表与实际目录仍有若干条目未登记，属既有文档滞后，未擅自扩面补录）。
