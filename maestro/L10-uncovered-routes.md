# L10 路由覆盖核对报告

**生成时间**：2026-11-23（复核轮重算）
**方法**：TypeScript AST 解析 `web/src/router.ts` × `maestro/flows/` 的 `openLink:` × `maestro/README.md` §3 三方对拍
**复核命令**（可重复运行，输出即本报告的全部数字）：

```bash
node maestro/tools/audit-route-coverage.mjs            # Markdown 报告
node maestro/tools/audit-route-coverage.mjs --json     # 机读 JSON
node maestro/tools/audit-route-coverage.mjs --router=<path>  # 复核历史口径
```

## 0. 为什么这一版是「可复跑」的（首版不是）

★ 首版报告（2026-11-20，`fa2ed95b7` / `a8f62d3a7`）写「router.ts 实际路由总数 **110** 条」，
  但一周后 README §1.1 的同一口径已经是 **125 / 130** ⇒ **报告的数字当场过期**，
  而报告本身没写「怎么重算」⇒ 读者无法判断它是真是假。

★ 更要命的是：**首版的配套工具根本跑不起来**。
  `maestro/tools/extract-routes.mjs` 用 `import ts from 'typescript'`，
  而仓内 typescript 装在 `web/`（前端工作区）⇒ ESM 从 `maestro/tools/` 向上找
  node_modules 找不到 ⇒ 仓根执行直接 `ERR_MODULE_NOT_FOUND`。
  ⇒ **工具不可运行 ⇒ 它的产出不可复现 ⇒ 「可重复运行」这个核心价值当场失效。**
  ⇒ 本轮修法：用 `createRequire(web/package.json)` 显式指定解析位置
  （`maestro/tools/extract-routes.mjs` 与新工具 `maestro/tools/audit-route-coverage.mjs` 同款），
  **从仓根直接跑通**。

## 1. 统计概览（当前树）

| 指标 | 数值 | README §1.1 的同口径值 |
|---|---|---|
| `router.ts` 路由对象数（AST 提取，含 dev-only 条件展开） | **129** | 125（已过期，差 4 条上游新增） |
| `externalMaintainRedirect(...)` 调用 | **5** | 5 |
| **运行时路由总数** | **134** | 130（已过期） |
| 纯 redirect 路由 | **20** | 19（已过期） |
| 带命名动态段（不含 glob 兜底） | **22** | 19（已过期） |
| glob 兜底 | **2** | 2 |
| 不需要登录 | **17** | 17 |
| 需要登录 | **117** | 113（已过期） |
| 权限档（任意登录 / super / platformOps / providerConsole） | **54 / 50 / 11 / 2** | 53 / 47 / 11 / 2（已过期） |
| flow 里 `openLink:` 步数（全部 flow） | **186** | — |
| README §3 已登记的路由数 | **110** | — |
| **已被 flow 打开过的路由** | **116** | — |
| **未被任何 flow 打开的路由** | **18** | — |

★ 权限档自洽：54 + 50 + 11 + 2 = **117** ✔（与「需要登录」相等）。

### 1.1 数字为什么变（逐条归因，非「口径不同」）

| 变化 | 数量 | 归因 |
|---|---|---|
| 路由对象 125 → 129 | +4 | 上游新增：`/admin/error-playbook`、`/admin/error-playbook/:id`（2026-11-21 错误处理手册轮）、`/admin/analytics/:dimension/:id`（2026-11-20 运营看板详情）、`/admin/board-operational/:dimension`（同轮） |
| 纯 redirect 19 → 20 | +1 | `/admin/session-analytics` 在 2026-11-23 审计轮由「总览页」改为**重定向**到 `/admin/session-analytics/users` |
| 动态段 19 → 22 | +3 | 同上 4 条里的 3 条带命名动态段 |
| 需要登录 113 → 117 | +4 | 上游新增的 4 条路由全部需要登录 |

⇒ **不是口径漂移，是树真的多了 4 条路由**（上游推进）。

## 2. 可信度证据：工具在**基准提交**上逐项复现 README 已公布的数

★ 这是本报告最重要的自证：把同一工具指向 README §1.1 最后一次更新的提交
  （`bd8d39757`）的 `router.ts`，跑出来的 12 个统计量与 README 公布值**逐项相等**：

| 统计量 | 基准提交上工具的输出 | README 公布值 | |
|---|---|---|---|
| path 条目 | 125 | 125 | ✔ |
| externalMaintainRedirect | 5 | 5 | ✔ |
| 运行时路由总数 | 130 | 130 | ✔ |
| 纯 redirect | 19 | 19 | ✔ |
| 动态段 | 19 | 19 | ✔ |
| glob 兜底 | 2 | 2 | ✔ |
| 不需要登录 | 17 | 17 | ✔ |
| 需要登录 | 113 | 113 | ✔ |
| 任意登录用户 | 53 | 53 | ✔ |
| requiresSuper | 47 | 47 | ✔ |
| requiresPlatformOps | 11 | 11 | ✔ |
| requiresProviderConsole | 2 | 2 | ✔ |

⇒ **工具口径 ≡ README §1.1 已公布口径** ⇒ 当前树上的新数**不是「另一套口径的另一组数」**，
  而是**同一把尺子在同一棵树上量出来的**。

复现命令：

```bash
git show bd8d39757:web/src/router.ts > /tmp/router_base.ts
node maestro/tools/audit-route-coverage.mjs --router=/tmp/router_base.ts --json
```

### 2.1 工具侧踩过的三个坑（写下来免得下一个人再踩）

① **AST 节点类型**：`routes:` 数组里的 `...cond ? [...] : []` 是 **SpreadElement**
   （不是对象字面量里的 SpreadAssignment）⇒ 只判后者会**漏掉 3 条 dev-only 路由**。
② **括号节点**：`(cond ? a : b)` 在 TS AST 里是 **ParenthesizedExpression**
   ⇒ 不解包就判不出条件表达式 ⇒ dev-only 三条继续漏。
③ **redirect 的四种形态**：字符串、对象 `{ path }`、**箭头函数** `redirect: (to) => ({ path })`、
   以及 `externalMaintainRedirect(path, target)` **函数调用**（5 条 `/ops/*`）。
   ⇒ 只认字符串/对象两种形态，会把 6 条箭头函数路由与 5 条 ops 路由整段漏掉
   ⇒ README §1.2 的 19 条会被数成 13 条。

## 3. 未被任何 flow 打开的路由（18 条）

| 路由 | 形态 |
|---|---|
| `/:pathMatch(.*)*` | 重定向 → `/` |
| `/admin/analytics/:dimension/:id` | 页面 |
| `/admin/board-operational/:dimension` | 页面 |
| `/admin/error-playbook/:id` | 页面 |
| `/admin/pi-analysis` | 页面 |
| `/admin/plugin-settings` | 页面 |
| `/admin/self-update` | 页面 |
| `/help` | 页面 |
| `/login` | 页面 |
| `/maintain/:pathMatch(.*)*` | 页面 |
| `/model-credibility` | 页面 |
| `/operations/market-prices` | 页面 |
| `/operations/marketing` | 页面 |
| `/operations/price-compare` | 重定向 → `/model-pricing` |
| `/operations/sales` | 页面 |
| `/public/quote/:token` | 页面 |
| `/simple/keys` | 页面 |
| `/tenant/bills` | 页面 |

### 3.1 分类：哪些「不该」被 Maestro 打开，哪些是**真实缺口**

★ ★ **口径边界（先读，否则会误判）**：本清单数的是「**flow 用 `openLink` 直接打开过**」，
**不含「在页面里点击进入」的路由** ⇒
- 静态工具**无法**判定点击可达性（Maestro 的 `tapOn` 不携带 URL）；
- ⇒ 被点击进入的路由会**落进本清单**，但它们**其实已被覆盖**。
⇒ 下表按「证据强度」分四档。

| 类别 | 路由 | 说明 |
|---|---|---|
| A 兜底路由（设计如此） | `/:pathMatch(.*)*`、`/maintain/:pathMatch(.*)*` | 404 / maintain 兜底 ⇒ 由「访问不存在路径」与「maintain 不可达」两条路径触达 ⇒ 专门为它写 flow 反而会盖住「兜底没兜住」这个缺陷 |
| B **点击可达（工具不可判定；事实已被覆盖）** | `/admin/error-playbook/:id` | flow `17-error-playbook.yaml` §④ 点种子行「鉴权类失败」进入详情页（断「执行留痕时间线」）⇒ **已被覆盖**，只是没走 `openLink` |
| B 同上 | `/admin/analytics/:dimension/:id` | 运营看板列表行点击深链进入（`config/analyticsRegistry.ts` 的 `detailPath`；见 `navCoverage.test.ts` 的孤儿登记） |
| B 同上（待证） | `/admin/board-operational/:dimension` | 运营看板各维度完整列表页；由看板内跳转进入 ⇒ 静态不可判定，**建议人工确认一次** |
| C 登录页（走快捷入口） | `/login` | 共享登录流用 `openLink: ${BASE_URL}/?login=1`（首页登录弹层）⇒ **`/login` 这条路由本身从未被直接打开过** ⇒ 它的渲染从未被 Maestro 验证过 |
| D **真实缺口**（12 条） | 见下表 | 路由存在、无 flow 打开 ⇒ 「页面在真实部署上能不能打开」从未被验证 |

D 类明细（按处置优先级）：

| 路由 | 建议 |
|---|---|
| `/admin/pi-analysis` | P1 补 flow（2026-11-18 PI 分析管理面，super 档，且有运行时可用性闸门） |
| `/admin/self-update` | P1 补 flow（2026-11-16 自更新与升级，super 档） |
| `/help` | P1 补 flow（帮助手册，任何登录用户可见） |
| `/public/quote/:token` | P1 补 flow（公开报价页 ⇒ 免登录可达 ⇒ **失效时用户完全打不开**） |
| `/operations/market-prices`、`/operations/marketing`、`/operations/sales` | P2 运营域三件套（platformOps / requiresAuth 档） |
| `/model-credibility` | P2（super 档） |
| `/admin/plugin-settings` | P3（super 档；菜单归属尚待产品确认，见 `navCoverage.test.ts` 的孤儿登记） |
| `/simple/keys`、`/tenant/bills` | P3（简易模式密钥签发 / 月账单） |
| `/operations/price-compare` | P3（重定向到 `/model-pricing` ⇒ 只需断言「跳转到位」） |
| `/admin/board-operational/:dimension` | P2（B 档待证：静态不可判定 ⇒ 建议人工在运营看板点一次确认详情列表页能打开） |

★ **C 类值得单独说**：`/login` 未被打开 ⇒ 登录页自身的渲染从未被验证。
  共享登录流验的是「**弹层登录**」这条主路径，登录页（`/login`）是**另一条**路径
  ⇒ ★ 「登录能用」与「登录页能打开」是两条独立断言。

## 4. router.ts 有、但 README §3 未登记（24 条）

| 路由 |
|---|
| `/:pathMatch(.*)*` |
| `/admin/analytics/:dimension/:id` |
| `/admin/board-operational/:dimension` |
| `/admin/error-playbook` |
| `/admin/error-playbook/:id` |
| `/admin/pi-analysis` |
| `/admin/plugin-settings` |
| `/admin/self-update` |
| `/admin/session-analytics` |
| `/admin/session-analytics/agent-ranking` |
| `/admin/session-analytics/model-ranking` |
| `/admin/session-analytics/user-ranking` |
| `/admin/session-analytics/user-ranking/:owner` |
| `/help` |
| `/login` |
| `/maintain/:pathMatch(.*)*` |
| `/model-credibility` |
| `/operations/market-prices` |
| `/operations/marketing` |
| `/operations/price-compare` |
| `/operations/sales` |
| `/public/quote/:token` |
| `/simple/keys` |
| `/tenant/bills` |

★ 其中 **5 条会话分析聚合族路由**（`/admin/session-analytics` 及其 user/agent/model-ranking
  与 `:owner` 详情）**已被 flow 16 覆盖**，只是 README §3 没有对应小节
  ⇒ 本轮已补 **§3.12**（见 `maestro/README.md`）。

## 5. 首版报告（2026-11-20）为什么必然过期

| 缺陷 | 后果 | 本轮处置 |
|---|---|---|
| 工具跑不起来（`typescript` 解析位置错） | 报告不可复现 | 改 `createRequire(web/package.json)`，仓根可跑 |
| 口径不完整（漏 6 条箭头 redirect / 3 条 dev-only / 5 条 ops） | 路由数被少算 ⇒ 「未覆盖」清单也会偏 | 工具补齐四种路由形态；基准提交上逐项复现 README 的 12 个数 |
| 数字硬抄、不随树走 | 上游 +4 条路由后无人察觉 | 数字改由工具产出，并登记「复现命令」 |

★ 通用式：**「清单声称全覆盖」与「真的全覆盖」是两条独立的断言**（README §2 已记）；
  而**「清单本身可复跑」是第三条**——它决定清单过期后有没有人**发现**。
