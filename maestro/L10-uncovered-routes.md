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
| `router.ts` 路由对象数（AST 提取，含 dev-only 条件展开） | **131** | **131** ✔（2026-11-23 复核轮已回填一致） |
| `externalMaintainRedirect(...)` 调用 | **5** | 5 ✔ |
| **运行时路由总数** | **136** | **136** ✔ |
| 纯 redirect 路由 | **20** | 20 ✔ |
| 带命名动态段（不含 glob 兜底） | **23** | **23** ✔ |
| glob 兜底 | **2** | 2 ✔ |
| 不需要登录 | **17** | 17 ✔ |
| 需要登录 | **119** | **119** ✔ |
| 权限档（任意登录 / super / platformOps / providerConsole） | **56 / 50 / 11 / 2** | **56 / 50 / 11 / 2** ✔ |
| flow 里 `openLink:` 步数（全部 flow） | **186** | — |
| README §3 已登记的路由数 | **115** | — |
| **已被 flow 打开过的路由** | **116** | — |
| **未被任何 flow 打开的路由** | **20** | — |

★ 权限档自洽：56 + 50 + 11 + 2 = **119** ✔（与「需要登录」相等）。
★ ★ **本表在复核轮之前的版本写着「129 / 134 / 22 / 117 / 54 + 未覆盖 18」**
  ⇒ 那正是**上游第二次漂移**之后、README **尚未回填**时的树 ⇒ **「树变了、清单没变」的那一格**。

### 1.1 数字为什么变（逐条归因，非「口径不同」）

| 变化 | 数量 | 归因 |
|---|---|---|
| 路由对象 125 → 129 | +4 | 上游新增：`/admin/error-playbook`、`/admin/error-playbook/:id`（2026-11-21 错误处理手册轮）、`/admin/analytics/:dimension/:id`（2026-11-20 运营看板详情）、`/admin/board-operational/:dimension`（同轮） |
| 纯 redirect 19 → 20 | +1 | `/admin/session-analytics` 在 2026-11-23 审计轮由「总览页」改为**重定向**到 `/admin/session-analytics/users` |
| 动态段 19 → 22 | +3 | 同上 4 条里的 3 条带命名动态段 |
| 需要登录 113 → 117 | +4 | 上游新增的 4 条路由全部需要登录 |
| **路由对象 129 → 131** | **+2** | ★ **第二次漂移**：2026-11-23 上游 `41fb572f3`「团队层级管理前端切片落地」新增 `/admin/teams`、`/admin/teams/:id`（两条都带 `meta: { requiresAuth: true }` ⇒ 落在「任意登录用户」档） |
| **动态段 22 → 23** | **+1** | 同上的 `/admin/teams/:id` 带命名动态段 |
| **需要登录 117 → 119** | **+2** | 同上的两条新路由 |
| **任意登录用户 54 → 56** | **+2** | 同上的两条新路由（`requiresAuth` 族 19 → 21） |

⇒ **不是口径漂移，是树真的多了路由**（上游两次推进）。
⇒ ★ **纯 redirect 与「不需要登录」两次都没变**（20 / 17）⇒ 变的是**新增路由的条数**，
  这与「新增的都是页面路由」完全吻合 ⇒ 与「口径漂移」的特征（各档乱跳）可区分。

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

### 2.0 ★★ 两点自证（复核轮补的第二个基准点）

★ 复核轮发现：**§1.1 的数字在复核轮之前就已经又过期了一次**（上游 `41fb572f3` 加了 2 条路由）。
  ⇒ 于是「上一版公布的数」也需要被证明 ⇒ **补第二个基准点**：

| 统计量 | 基准 A：`bd8d39757` 的 router.ts | 基准 B：`8e0baf950` 的 router.ts | README §1.1 上一次公布的数 | |
|---|---|---|---|---|
| path 条目 | 125 | 129 | 129 | ✔ |
| externalMaintainRedirect | 5 | 5 | 5 | ✔ |
| 运行时路由总数 | 130 | 134 | 134 | ✔ |
| 纯 redirect | 19 | 20 | 20 | ✔ |
| 动态段 | 19 | 22 | 22 | ✔ |
| glob | 2 | 2 | 2 | ✔ |
| 不需要登录 | 17 | 17 | 17 | ✔ |
| 需要登录 | 113 | 117 | 117 | ✔ |
| 任意登录用户 | 53 | 54 | 54 | ✔ |
| requiresSuper | 47 | 50 | 50 | ✔ |
| requiresPlatformOps | 11 | 11 | 11 | ✔ |
| requiresProviderConsole | 2 | 2 | 2 | ✔ |

⇒ **两个基准点都逐项相等** ⇒ 「工具口径 ≡ README 口径」这件事**不是单点巧合**。
⇒ 复现命令（基准 B）：

```bash
git show 8e0baf950:web/src/router.ts > /tmp/router_base_b.ts
node maestro/tools/audit-route-coverage.mjs --router=/tmp/router_base_b.ts --json
```

### 2.2 ★ 同一轮查出的第二个静默缺陷：§1.2 的「router.ts 行」列是**手抄**的

★ §1.2 表里有一列「router.ts 行」，用来指回 `router.ts` 的具体行。
  复核轮把它逐条核对 ⇒ **19 / 20 行对不上**。
⇒ **取证方式不是「看起来不对」，而是逐提交扫描历史**：

```bash
git log --format=%H -- web/src/router.ts | while read c; do
  git show $c:web/src/router.ts > /tmp/rh.ts
  echo "$c $(grep -n "path: '/catalog'" /tmp/rh.ts | cut -d: -f1) $(grep -n "path: '/:pathMatch" /tmp/rh.ts | cut -d: -f1)"
done
```

⇒ 结果：`/catalog` **只在** `d82a9fb06`（2026-10-18）是 **220**，而**同一次扫描**里
  catch-all 是 **374**、不是表里写的 **435** ⇒ ★ **两个值来自不同时期**
  ⇒ ⇒ 这不是「树漂移导致过期」，是**当初就抄错了**（抄了 2026-10-18 的部分行、
  又抄了更晚的 catch-all 行）。
⇒ **处置**：全部改成当前树的 `path:` 字面量所在行，并在 §1.2 写下这处取证。
⇒ ★★ **通用式**：**「引用了行号」的文档，行号本身也会过期，而且过期得比数字更隐蔽**
  —— 数字过期时人会怀疑数字，行号过期时人只会「跳过去看那行」⇒
  ⇒ ★ **凡是要指回源码的引用，要么给「可复跑的校验」，要么别给行号**。

### 2.3 ★ 「清单过期」必须能**判红**，否则「可复跑」只是句口号

★ 复核轮实测的现状：README 的数字过期了，而**全仓没有任何一道门会红**
  ⇒ ⇒ 「可复跑」只解决了「能重算」，**没解决「有人会重算」**。
⇒ 复核轮补了两道（详见 §6）：
  ① `maestro/tools/audit-route-coverage.mjs --check` ⇒ ★ **AST 口径**（精确计数）
     把 README §1.1/§1.2/§1.3/§3.13 声明的值与树实测**逐项对拍**，漂移 ⇒ 退出码 1；
  ② `maestro/tools/audit-readme-route-stats.py` + `scripts/checks/maestro-readme-route-stats-gate.sh`
     ⇒ ★ **零依赖 python3 口径**（进 CI 载体）：7 条判据（内部算式自洽 / 标题数 vs 表内行数 /
     幽灵路由 / 未登记路由）。
⇒ ★ 两把尺子的分工：**集合**判定两把都能做（实测正则口径与 AST 口径**逐条相等**，
  136 = 136），**计数**只有 AST 能做（权限档依赖 `meta` 归属与守卫判据）⇒
  ⇒ CI 上跑零依赖那把（能抓「上游加路由没人登记」与「半回填」），
  本地跑 AST 那把（能抓 12 个统计量的逐项漂移）。

### 2.1 工具侧踩过的三个坑（写下来免得下一个人再踩）

① **AST 节点类型**：`routes:` 数组里的 `...cond ? [...] : []` 是 **SpreadElement**
   （不是对象字面量里的 SpreadAssignment）⇒ 只判后者会**漏掉 3 条 dev-only 路由**。
② **括号节点**：`(cond ? a : b)` 在 TS AST 里是 **ParenthesizedExpression**
   ⇒ 不解包就判不出条件表达式 ⇒ dev-only 三条继续漏。
③ **redirect 的四种形态**：字符串、对象 `{ path }`、**箭头函数** `redirect: (to) => ({ path })`、
   以及 `externalMaintainRedirect(path, target)` **函数调用**（5 条 `/ops/*`）。
   ⇒ 只认字符串/对象两种形态，会把 6 条箭头函数路由与 5 条 ops 路由整段漏掉
   ⇒ README §1.2 的 19 条会被数成 13 条。

## 3. 未被任何 flow 打开的路由（20 条）

| 路由 | 形态 |
|---|---|
| `/:pathMatch(.*)*` | 重定向 → `/` |
| `/admin/analytics/:dimension/:id` | 页面 |
| `/admin/board-operational/:dimension` | 页面 |
| `/admin/error-playbook/:id` | 页面 |
| `/admin/pi-analysis` | 页面 |
| `/admin/plugin-settings` | 页面 |
| `/admin/self-update` | 页面 |
| `/admin/teams` | 页面 |
| `/admin/teams/:id` | 页面 |
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

★ 条数随树走：基准 `bd8d39757` 是 **15** 条 → L10 首轮回填时 **18** 条
  → 复核轮（上游 `41fb572f3` 加了 2 条团队层级管理路由）**20** 条。

### 3.1 分类：哪些「不该」被 Maestro 打开，哪些是**真实缺口**

★ ★ **口径边界（先读，否则会误判）**：本清单数的是「**flow 用 `openLink` 直接打开过**」，
**不含「在页面里点击进入」的路由** ⇒
- 静态工具**无法**判定点击可达性（Maestro 的 `tapOn` 不携带 URL）；
- ⇒ 被点击进入的路由会**落进本清单**，但它们**其实已被覆盖**。
⇒ 下表按「证据强度」分四档。
★ ★ **与 `maestro/README.md` §3.13 的分档关系**：那边分**三档**（A/B/C），
  把本表的 **C 与 D 两档合并**成「C 真实缺口」（并按处置优先级标 P1/P2/P3）
  ⇒ ⇒ ★ **两文档的路由集合逐条相同**，只是**分档粒度不同**（读哪边都不矛盾）。

| 类别 | 路由 | 说明 |
|---|---|---|
| A 兜底路由（设计如此） | `/:pathMatch(.*)*`、`/maintain/:pathMatch(.*)*` | 404 / maintain 兜底 ⇒ 由「访问不存在路径」与「maintain 不可达」两条路径触达 ⇒ 专门为它写 flow 反而会盖住「兜底没兜住」这个缺陷 |
| B **点击可达（工具不可判定；事实已被覆盖）** | `/admin/error-playbook/:id` | flow `17-error-playbook.yaml` §④ 点种子行「鉴权类失败」进入详情页（断「执行留痕时间线」）⇒ **已被覆盖**，只是没走 `openLink` |
| B 同上 | `/admin/analytics/:dimension/:id` | 运营看板列表行点击深链进入（`config/analyticsRegistry.ts` 的 `detailPath`；见 `navCoverage.test.ts` 的孤儿登记） |
| B 同上（待证） | `/admin/board-operational/:dimension` | 运营看板各维度完整列表页；由看板内跳转进入 ⇒ 静态不可判定，**建议人工确认一次** |
| C 登录页（走快捷入口） | `/login` | 共享登录流用 `openLink: ${BASE_URL}/?login=1`（首页登录弹层）⇒ **`/login` 这条路由本身从未被直接打开过** ⇒ 它的渲染从未被 Maestro 验证过 |
| D **真实缺口**（14 条） | 见下表 | 路由存在、无 flow 打开 ⇒ 「页面在真实部署上能不能打开」从未被验证 |

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
| `/admin/teams`、`/admin/teams/:id` | P3（★ 2026-11-23 上游 `41fb572f3` 新增「团队层级管理」：★ `appNav.ts` **无菜单入口** ⇒ 上游自己已在 `web/src/config/navCoverage.test.ts` 把 `/admin/teams` 登记为**孤儿路由**（菜单归属待产品确认）⇒ 与 `/admin/plugin-settings` 同族口径；★ 详情页只能从列表页点击到达 ⇒ **静态不可判定**，但列表页本身也无 flow ⇒ 整族无覆盖） |

★ **C 类值得单独说**：`/login` 未被打开 ⇒ 登录页自身的渲染从未被验证。
  共享登录流验的是「**弹层登录**」这条主路径，登录页（`/login`）是**另一条**路径
  ⇒ ★ 「登录能用」与「登录页能打开」是两条独立断言。

## 4. router.ts 有、但 README §3 未登记（21 条）

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
| `/admin/teams` |
| `/admin/teams/:id` |
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
  ⇒ L10 首轮已补 **§3.12**（见 `maestro/README.md`）⇒ **从本清单移除**。
★ ★ **复核轮的净变化**：24 − 5（§3.12 已登记）＋ 2（上游新增团队层级管理）＝ **21**。

## 5. 首版报告（2026-11-20）为什么必然过期

| 缺陷 | 后果 | 本轮处置 |
|---|---|---|
| 工具跑不起来（`typescript` 解析位置错） | 报告不可复现 | 改 `createRequire(web/package.json)`，仓根可跑 |
| 口径不完整（漏 6 条箭头 redirect / 3 条 dev-only / 5 条 ops） | 路由数被少算 ⇒ 「未覆盖」清单也会偏 | 工具补齐四种路由形态；基准提交上逐项复现 README 的 12 个数 |
| 数字硬抄、不随树走 | 上游 +4 条路由后无人察觉 | 数字改由工具产出，并登记「复现命令」 |

★ 通用式：**「清单声称全覆盖」与「真的全覆盖」是两条独立的断言**（README §2 已记）；
  而**「清单本身可复跑」是第三条**——它决定清单过期后有没有人**发现**。

## 6. ★ 复核轮新增的两道门（让「过期」从静默变成会红）

| 门 | 工具 | 运行时依赖 | 判据 | 跑法 |
|---|---|---|---|---|
| （本地，AST 口径） | `maestro/tools/audit-route-coverage.mjs --check` | `web/node_modules`（typescript） | **18 条**：§1.1 的 12 个统计量 + §1.2/§1.3/§3.13 的条数，**逐项**与树实测对拍 | `node maestro/tools/audit-route-coverage.mjs --check` |
| （**进 CI 载体**，零依赖） | `maestro/tools/audit-readme-route-stats.py` + `scripts/checks/maestro-readme-route-stats-gate.sh` | **只有 python3** | **7 条**：D1/D2 内部算式自洽、D3/D4/D5 标题数 vs 表内行数（抓**半回填**）、D6 幽灵路由、D7 **未登记路由**（抓「上游加路由没人登记」） | `bash scripts/checks/maestro-readme-route-stats-gate.sh` |

★ ★ **两把尺子的分工（实测结论，不是推测）**：
  · **集合**判定：正则口径与 AST 口径**逐条相等**（136 = 136，两边零差集）
    ⇒ ⇒ **CI 上那把零依赖的尺子，足以抓住「上游新增路由但没人登记」**（D7）
  · **计数**判定：权限档那类数依赖 AST 的 `meta` 归属与 `beforeEach` 守卫判据
    ⇒ 正则算不准 ⇒ **只由本地 AST 那把守**（`--check` 的 12 个统计量）
  ⇒ ⇒ **两把都要**：CI 守「集合 + 内部自洽」，本地守「逐项计数」。

★ **变异自证（6 个臂，逐一精确报红）**：

| 臂 | 注入 | 期望 | 实测 |
|---|---|---|---|
| M1 | README §1.1「path 条目」131→130 | 红 | ✔ 红在「§1.1 path 条目」 |
| M2 | README §3.13 标题 20→18 | 红 | ✔ 红在「§3.13 标题条数」＋「表内路径数」 |
| M3 | README §1.2 表删 1 行 | 红 | ✔ 红在「§1.2 表内行数 = 标题条数」 |
| M4 | README §1.1「需要登录」119→117 | 红 | ✔ 红在「§1.1 需要登录」 |
| M5 | README §1.3 标题 23→22 | 红 | ✔ 红在「§1.3 标题条数」＋「表内行数」 |
| M6 | 把 `--router` 指向基准树（= 树退回、README 不退） | 红 | ✔ **10 条同时红**（12 个统计量里的 6 个 + 4 条条数判据） |

★ CI 门的三个对照臂（`scripts/checks/maestro-readme-route-stats-gate.sh`，7/7 绿）：
  A 幽灵路由（README 塞一条树里没有的）⇒ 红在 D6；
  B 未登记路由（router.ts 副本塞一条 README 没提的）⇒ 红在 D7；
  C 半回填（§1.2 标题改 19、表不动）⇒ 红在 D3。
  ⇒ ★ **每个对照臂都带反向断言**（「红在**那件事**上」而不是「随便红」）⇒
    ⇒ **门自己的误报比没门更坏**这条纪律，在本门上是落地的。

★ **复现命令**：

```bash
node maestro/tools/audit-route-coverage.mjs --check                       # 18/18
python3 maestro/tools/audit-readme-route-stats.py                        # 7/7
bash scripts/checks/maestro-readme-route-stats-gate.sh                   # 7/7（含 3 个对照臂）
```
