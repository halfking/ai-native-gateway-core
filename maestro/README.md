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

| 指标 | 数值 | 来源 |
|---|---|---|
| `path:` 条目 | **107** | `web/src/router.ts` `routes:` 数组里逐条计数（`router.ts:200-374`） |
| `externalMaintainRedirect(...)` 调用 | **5** | `router.ts:354-358`（`/ops/licenses`、`/ops/faults`、`/ops/autoupdate`、`/ops/center`、`/ops`） |
| **运行时路由总数** | **112** | 107 + 5 |
| 其中纯 redirect（没有自己的组件，只跳走） | **18** | 18 条都带 `redirect:` 字段（其中 1 条是 catch-all `/:pathMatch(.*)*`） |
| 其中带命名动态段（`:param`） | **17** | 见 §1.3 |
| 其中 glob 兜底（`:pathMatch(.*)*`） | **2** | `/maintain/:pathMatch(.*)*`（:351）、`/:pathMatch(.*)*`（:374） |
| **不需要登录**（`meta.public === true`） | **16** | 含 5 条「重定向到公开页」的路由（见下方口径说明） |
| **需要登录** | **96** | 112 − 16 |

需要登录的 96 条，按 `router.ts` 的 `beforeEach` 再分档：

| 档位 | 条数 | 守卫出处 |
|---|---|---|
| 任意登录用户（目标路由无 `meta` 权限位） | 41 | `router.ts:465` `if (!to.meta.public && !isAuthed())` |
| `requiresSuper`（仅 super_admin） | 43 | `router.ts:476` |
| `requiresPlatformOps`（super_admin + default 租户） | 10 | `router.ts:492` |
| `requiresProviderConsole`（super_admin 或 default 租户 tenant_admin） | 2 | `router.ts:481` |

> ★ **计数口径（重要，和「直接看 `meta`」会差 5 条）**：
> vue-router 4 在 `pushWithRedirect()` 里**先**执行 `handleRedirectRecord()`、**后**才跑导航守卫
> （`web/node_modules/vue-router/dist/vue-router.mjs:1282-1296`），
> 所以一条 `redirect:` 路由**由它跳转到的目标路由的 `meta` 决定**要不要登录 —— `router.ts:241-246` 的注释也是这么写的。
> 于是 5 条「自身无 `meta`、但重定向到公开页」的路由实际**不需要登录**：
> `/customer/site`、`/customer/activate`、`/customer/license`、`/customer/agreement`（→ `/customer/update-activate`）与 `/:pathMatch(.*)*`（→ `/`）。
> 这 5 条被放进了 `01-public-guest.yaml`（不登录的 flow）。
>
> 另外：`/ops/*` 那 5 条**不是** `redirect:` 路由，而是 `externalMaintainRedirect()` 造的
> 「`component: {render: () => null}` + `beforeEnter` 里 `window.location.replace`」的路由记录（`router.ts:117-127`），
> 所以守卫按**它们自己**的 `meta`（无 ⇒ 需要登录）判，但还会被 `router.ts:488` 的 `startsWith('/ops')` 规则改写。
>
> 16 条公开路由清单：`/login`、`/forbidden`、`/`、`/bootstrap`、`/customer/update-activate`、
> `/customer/site`、`/customer/activate`、`/customer/license`、`/customer/agreement`、`/customer/offline-activation`、
> `/maintain`、`/maintain/:pathMatch(.*)*`、`/:pathMatch(.*)*`、以及 3 条 dev-only 的 `/dev/*`。

### 1.2 纯 redirect 路由（18 条）

这 18 条**没有自己的组件**，断言点落在它们跳转到的目标页面上（这样「跳对了」和「目标页渲染出来了」是同一条证据）。

| 源路由 | 跳转目标 | router.ts 行 |
|---|---|---|
| `/catalog` | `/models` | 220 |
| `/probe-health` | `/routing-v2/credentials` | 226 |
| `/system-monitor` | `/dashboard` | 230 |
| `/routing-v2/annotations` | `/routing-v2/auto-ops` | 239 |
| `/routing-v2/annotations/stats` | `/routing-v2/auto-ops` | 240 |
| `/routing-v2/task-profile` | `/routing-v2/auto-ops` | 247 |
| `/routing-v2/auto-tuning` | `/routing-v2/auto-ops` | 248 |
| `/maas/models` | `/tenant/models` | 272 |
| `/maas/account` | `/tenant/account` | 273 |
| `/maas/pricing` | `/tenant/pricing` | 274 |
| `/maas/orders/:id` | `/tenant/orders/:id` | 275 |
| `/maas/usage` | `/tenant/usage` | 276 |
| `/routing` | `/routing-v2` | 281 |
| `/customer/site` | `/customer/update-activate` | 343 |
| `/customer/activate` | `/customer/update-activate` | 344 |
| `/customer/license` | `/customer/update-activate` | 345 |
| `/customer/agreement` | `/customer/update-activate` | 346 |
| `/:pathMatch(.*)*` | `/` | 374 |

### 1.3 带命名动态段的路由（17 条）

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
| `/admin/approvals/:id` | `id` | `/admin/approvals/${APPROVAL_ID}` |
| `/admin/sessions/:id` | `id` | `/admin/sessions/${SESSION_ID}` |
| `/admin/request-actions/:id` | `id` | `/admin/request-actions/${REQUEST_ID}` |
| `/admin/request-registry/journey/:requestId` | `requestId` | `/admin/request-registry/journey/${REQUEST_ID}` |
| `/admin/connection-registry/:credentialId/recovery` | `credentialId/recovery` | `/admin/connection-registry/${CREDENTIAL_ID}/recovery` |
| `/dev/live-request-preview/:requestId` | `requestId` | `/dev/live-request-preview/${REQUEST_ID}` |

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
| `maestro/flows/_shared/login.yaml` | 共享登录流 | 1（`/login`） | — | 被其它 flow 用 `runFlow` 调 |

合计：**112 条路由 = 111 条在 10 个功能 flow 里 + 1 条 `/login` 在共享登录流里**。

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

---

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

共享登录流做的事：

1. `openLink ${BASE_URL}/login`。`LoginView.vue` 会 `router.replace('/?login=1')`（`web/src/views/LoginView.vue:20-29`），
   `App.vue` 的 watcher 看到 `login=1` 就 `openLogin()`（`web/src/App.vue:244`），于是登录弹窗出现。
2. 等 `#login-modal-title` 可见 —— 弹窗标题是「登录控制面」（`web/src/components/LoginModal.vue:125`，locale `login.title`）。
3. 点 `#login-username` 填 `${ADMIN_USER}`，点 `#login-password` 填 `${ADMIN_PASSWORD}`。
4. **`pressKey: Enter`** 提交（理由见 §6.3）。
5. 等弹窗消失（`notVisible: id: login-modal-title`），再等「仪表盘」可见 —— 这是登录成功的硬判据
   （`web/src/views/DashboardViewV2.vue:97` 的 `<h2>{{ t('dashboard.title') }}</h2>`）。

### 6.2 为什么提交用 Enter 而不是点「登录」按钮

**要点的控件**是弹窗底部 `.login-modal__actions` 里的 `<button type="submit">`，可见文案「登录」
（`web/src/components/LoginModal.vue:160-162`，locale `login.submit = '登录'`）。

但**不能**用 `tapOn: "登录"`：同一个页面上「登录」二字至少出现两处 ——
访客头部的 `<button class="guest-login-btn">{{ t('login.submit') }}</button>`（`web/src/App.vue:386`）和弹窗里的提交按钮。
Maestro 的 `tapOn` 文本匹配按 DOM 顺序取第一个命中，会打到访客头部那个按钮上 —— 它的行为只是「再次打开弹窗」，**不会提交表单**。
焦点停在密码框时按 Enter 会触发表单的 `@submit.prevent="handleLogin"`，是当前唯一确定能提交的路径。

### 6.3 备选：Cookie 注入（不用 UI）

后端 `POST /api/auth/token`（`admin/auth.go:951` 注册路由、`admin/auth.go:174` 处理）会：

- 下发 **HttpOnly cookie `llmgw_session`**（`admin/auth_cookie_helpers.go:86` 的 `setSessionCookieRemember`），
- 同时在响应体里返回 `access_token`（`admin/auth.go:284-298`）。

前端所有 API 调用都带 `credentials: 'same-origin'`（`web/src/api/_core.ts:404`），所以**cookie 就是真正的会话**；
`jwtToken` 只存在内存里、不落 localStorage（`web/src/store.ts:118`）。

因此「不点 UI 也能拿到登录态」的可靠做法是：在浏览器同源上下文里 `fetch` 一次登录接口，浏览器会自动收下 `Set-Cookie`：

```yaml
- evalScript: "await fetch('/api/auth/token', {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'}, body: JSON.stringify({username:'${ADMIN_USER}', password:'${ADMIN_PASSWORD}'})})"
```

（`fetch` 成功后 cookie 已落袋；`localStorage.llmgw_user_info` 由 `/api/auth/me` 的 hydration 补上，
`web/src/router.ts:129-147` 的 `isAuthed()` 有 localStorage 兜底。）

★ 前提：这条 `fetch` 必须在**已经打开网关页面**的上下文里执行（同源），不能是 Maestro 的宿主 JS 沙箱。

### 6.4 已知坑：自适应验证码

后端有自适应验证码闸门（`admin/auth_captcha.go:474`）：**连续登录失败达到阈值后强制要求 captcha**，
而登录弹窗当前**没有渲染验证码输入框** ⇒ 那种状态下 UI 登录流必然失败（后端返 `captcha_required`）。
先把失败计数清掉（换 IP / 等窗口过期），或改用 §6.3 的 cookie 注入方案。

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

### 8.3 ★ 四个实测踩过的坑（2026-10-28）

| # | 坑 | 现象 | 正解 |
|---|---|---|---|
| 1 | **YAML 头必须是 `url:`** | 写了 `appId:` ⇒ Maestro 判成**移动端 flow** ⇒ 报「Web support is in Beta」且一条命令都不跑 | 头部写 `url: ${BASE_URL}`（`YamlConfig.getUrl` ⇒ `FileUtils.isWebFlow`） |
| 2 | **CLI 没有 `--env-file`** | `--env-file` 直接被拒；而且 **shell 环境变量不参与 `${}` 解析** | 只能用 `-e KEY=VALUE` 逐个传；口令从仓根 `.env.local` 读、由 `run-local.sh` 代传（**口令不落仓内任何文件**） |
| 3 | **不要加 `-p web`** | `-p web` 会把 flow 筛成 0 个设备 ⇒ 「no flows to run」 | 不加 `-p`；改用 `--driver-host-port 9222` 连自起的 CDP Chrome（该选项 help 里没列，但确实被接受） |
| 4 | **`evalScript` 跑在 Maestro 侧沙箱** | web flow 的 `runScript` 里 `location` / `performance` 都是 `undefined` | 计时只用 `Date.now() - output.t0`（跨导航不丢）；**页面内部时间线要另用 CDP 探针** |

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
├── flows/
│   ├── _shared/login.yaml     ← 共享登录流（被 runFlow 调）
│   ├── 01-public-guest.yaml
│   ├── 02-overview-live.yaml
│   ├── 03-providers-models.yaml
│   ├── 04-tenants-portal.yaml
│   ├── 05-routing.yaml
│   ├── 06-keys-requests.yaml
│   ├── 07-admin-config.yaml
│   ├── 08-admin-data-ops.yaml
│   ├── 09-ops-maintain.yaml
│   └── 90-dev-only.yaml
└── tools/
    └── slow-pages.mjs         ← 从运行产物里筛「>2 秒」的页面
```
