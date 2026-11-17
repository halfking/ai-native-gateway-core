# 简易模式 v2 · 与 magpie 的对比分析（借鉴与不照搬）

> 日期：2026-11-16 ｜ 会话：`mvs_63c95713f83a43deaf4269a0a582d785`（goal 会话）
> 目标：**分析我们简易模式的设计 → 与 magpie 对比、学习它的优点 → 但不丢失我们的功能特性 → 再出一版设计稿**
> 本文＝第 ①② 步的书面结论；第 ③④ 步的设计稿见
> [`simple-mode-concept-v2.html`](./simple-mode-concept-v2.html)（逐节 PNG 在
> `assets/simple-mode-concept-v2/`）。
>
> **口径**：
> - magpie 侧＝本机检出 `~/workspace/ai/magpie` HEAD `ca3e57f94`（含上游 `v0.1.3189` 合并，2026-11-07），
>   结论全部回源码核对（文件:行号见 §九）。
> - 本仓侧＝`llm-gateway-go-5` HEAD `14d165a7f`（2026-11-16）。
> - v1 稿＝本目录 `simple-expert-mode-design.html`（分析稿）、`simple-mode-ui-mockup.html`（效果稿）、
>   `simple-mode-concept.html`（整页与菜单概念稿）。

---

## 〇、结论（先读这一节）

1. **v1 稿的方向是对的，但停在了「控制台减法」这一步。**
   v1 做了三件硬事：把专家面入口压成「13 露出 + 折叠其余」（v1 稿当时记 61 入口 / 47 折叠，
   现行真源为 63 入口 / 62 落地路径 / 50 折叠，见 §一.3）、
   把功能分成**保留／降级／隐藏**三档、把「装到能用」拆成 5 步。
   ★ 它的三条纪律必须一条不丢：**模式是投影不是第二套实现**、**一个开关一个真源**、**降级必须可见**。

2. **magpie 的强项不在「字段少」，而在「默认值替用户做完决定」与「首页是一张清单」。**
   源码级证据：magpie 的导航只有 **4 组 9 项 + Settings**（`internal/gui/assets/index.html:69-85`），
   首页就是「每个客户端一行、行内选模型」（`index.html:117` + `docs/reference.md:7-13`）；
   供应商接入是「选一个 preset + 粘一个 key」（`docs/reference.md:56-60`），
   preset 直接决定 base URL／协议／模型清单，**其余 30+ 高级字段收在抽屉里**
   （`internal/gui/assets/app.js:9300` 的 `hiddenDrawers` 一族）。
   ⇒ ★ **我们要学的不是「删字段」，是「默认值填好 + 细节收抽屉 + 首页清单化」。**

3. **v2 的落点＝把首页从「仪表盘」改成「清单」，把接入从「三个表单」压成「两字段」。**
   其余全部不动：专家面 63 个入口（现行 `appNav.ts` 口径，见 §一.3）**一条不删**，
   v1 的三档功能清单**一条不改判**，降级项**照旧明示**。

4. ★★★ **不照搬清单（这一节是「不丢失功能特性」的正面回答）**：
   magpie 是**单机、单用户、无租户、无角色、无配额、无审计、无插件市场**的个人工具，
   它的「简单」是靠**砍掉企业面**得到的。
   我们是**多租户网关**：租户／用户／密钥／预算／对账／审计／插件／运维中心是我们的价值本身。
   ⇒ **照搬 magpie 的 IA 形状可以，照搬 magpie 的功能边界不行。**
   v2 逐项处置见 §六。

5. ★★ **一处基线漂移要在 v2 里订正**：v1 稿写「专家面 61 个入口 / 60 个落地路径」，
   那是 2026-11-16 上午的快照；现行 `web/src/config/appNav.ts` 是 **63 条目 / 62 个落地路径**
   （`/live` 与 `/live2` 于 2026-11-16 之后加入，「自动更新」跨两组重复挂载）。
   ⇒ v2 台账按 **63 / 62** 记，且**逐条可点回 `appNav.ts` 行号**。

---

## 一、我们的 v1 现状盘点

### 1.1 v1 已经做对的（v2 一条不动）

| # | v1 的决定 | 出处 | v2 处置 |
|---|---|---|---|
| P1 | **模式是投影，不是第二套实现**：两个模式跑同一份代码，差别只在装配点与面板 | `simple-expert-mode-design.html` §五条设计原理 | 沿用 |
| P2 | **一个开关，一个真源**：`gateway.mode` 单一注册项，可见性判定只有 `canShowNavItem()` 一处 | 同上 + `web/src/config/appNav.ts:291-300` | 沿用 |
| P3 | **显式高于隐式，两种强度分开**：`override`（模式决定，env 也盖不过） vs `default`（数据库 > env > 预设） | 同上 | 沿用 |
| P4 | **降级必须可见**：运行时改了行为，界面必须标「已降级」并给详情入口 | 同上 | 沿用 |
| P5 | **专家模式是严格超集，切换不丢数据**：模式自身写入面只有一个键 | 同上 | 沿用 |
| P6 | **折叠，不隐藏**：不露面的条目仍然可达，不删除 | 同上 §前端分层 | 沿用，并借 magpie 的「关停项带计数」加强 |
| P7 | **三档功能清单**：保留／降级／隐藏，第三档专门治「不可见的强制行为」 | 同上 §功能清单 | 沿用（v2 台账按此重列） |
| P8 | **接上游只填一个 Key、密钥只填两个字段**（别名 + 预算上限） | 同上 §使用流程 | 沿用，并在 UI 上兑现成真表单 |

### 1.2 v1 的四个问题（v2 要修的就是这四个）

**Q1 · 首屏仍然是「仪表盘」，不是「清单」。**
v1 概念稿的首页同时摆着 **8 个并列区块**：4 个 KPI 格（今日请求／成功率／Token 用量／首字延迟）、
引导条（还差一步就能用）、最近请求表、接入客户端面板、上游状态面板、工作模式面板
（`simple-mode-concept.html` §一）。
★ 用户心里的对象是「**我客户端现在在用哪个模型、能不能用**」，
而首页回答的是「**网关今天跑了多少**」——这是两类问题，前者决定要不要继续，后者只是背景。
magpie 恰恰相反：首页就是那一张清单（`index.html:117` 的 `view-agents`）。

**Q2 · 接一个上游要跨三个表单、跨两道门。**
本仓现状：新增供应商表单要填 目录代码／名称／协议／base URL／备注
（`web/src/views/ProvidersView.vue:1146-1246`）→ 再加凭据（API Key + 标签，`:1582-1600`）
→ 再建模型绑定；且可达性探测是**提交前的串行门**（v1 稿 §使用流程 第 3 步）。
★ 我们其实**已经有 magpie 那张 preset 表**：`provider_catalog` 表带
`base_url_template` / `protocol` / `models_manifest_json` / `models_endpoint_template` /
`discovery_strategy`（`sql/objects/tables/provider_catalog.sql:1-40`），
但表单**没有把 preset 的默认值真正填进输入框**，用户仍要逐项确认。
⇒ **v2 的修法不是砍字段，是「preset 选完自动填，其余进抽屉」。**

**Q3 · 模型清单对普通用户是「静态目录」。**
我们有真能力：`discovery` 包按 `discovery_strategy` 从厂商端点拉模型
（`discovery/discovery.go:326-347`），admin 里已有「从供应商读取」按钮
（`web/src/locales/zh-CN/providerDetail.ts:295`），
`cmd/verify-model-fetch/main.go` 的头注释明写它与那个按钮走**同一条** `resolveModelsForCredential` 路径。
★ 但这些都在专家面的供应商详情页里；简易模式首页与模型选择器看到的仍是静态目录。
magpie 的口径是「**Real model lists, nothing compiled in.**」
（`docs/reference.md:62-68`）：拿一个 key 就去问厂商它到底提供哪些模型。

**Q4 · 「关停 / 零流量」的对象在 v1 稿里没有专门位置。**
本仓 2026-11-16 审计刚修完一个真问题：**43 家在册但零流量的供应商此前完全不可见**
（`dc263b085`；候选侧修法＝`mergeIdleProviders` 把在册供应商并进候选、requests=0 排在有请求的之后）。
magpie 对应做法是 **「Turned off」折叠带计数**（`index.html:140` 的 `offHead` + `offCount`）。
⇒ v2 借它：**关停与零流量是一类可见对象，折叠显示但计数可见**，不删除、不隐藏。

### 1.3 现行基线数字（v2 台账口径）

| 口径 | 数字 | 证据 |
|---|---|---|
| 专家面导航条目 | **63 条** | `web/src/config/appNav.ts`，`path:` 出现 63 次 |
| 落地路径 | **62 个** | 同上，唯一重复是 `/maintain/ops/autoupdate`（运维中心 + 接入指南各挂一次） |
| 导航分组 | 8 个静态组 + 1 个插件合成组 | `appNav.ts:133,163,191,205,229,256,270,284` + `:430` |
| 顶层主链接 | 3 项（`/live`、`/live2`、`/dashboard`） | `appNav.ts:66-71` |
| v1 稿当时口径 | 61 条 / 60 路径 | v1 稿写在 `/live2` 加入之前 → **v2 订正为 63 / 62** |

---

## 二、magpie 的设计事实（源码级，10 条）

| # | 事实 | 证据 |
|---|---|---|
| **F1** | 导航只有 **4 组 9 项 + Settings**：Work（Agents/Sessions/Library）、Models（Providers/Routing/Routing groups/Gateway）、Insights（Usage/Masking）、Extend（Plugins） | `internal/gui/assets/index.html:69-85` |
| **F2** | **首页就是 Agents 页**：一屏列出「每个客户端 + 它当前用的模型」，点值即选模型 | `index.html:117`；`docs/reference.md:7-13`「That is the whole app.」 |
| **F3** | **供应商接入＝preset + key**：文档口径「Providers with one field. Pick a preset…, paste a key, done.」；源码里 preset 决定 `api`（base API）、`catalog`、`chosen` 模型，30+ 高级字段在 `hiddenDrawers` 抽屉里 | `docs/reference.md:56-60`；`internal/gui/assets/app.js:9300` |
| **F4** | **模型清单来自厂商**：拿 key 直接问厂商它提供哪些模型，`models.dev` 补齐厂商不报的；不编译进代码 | `docs/reference.md:62-68` |
| **F5** | **每个客户端有自己的模型清单**：「Showing 5 / 32 models」可逐个摘出/放回，并有「Only models I pick」开关 | `docs/reference.md:70-79` |
| **F6** | **关停对象折叠但计数可见**：「Turned off」折叠头带 `offCount` | `index.html:140` |
| **F7** | **一键遮蔽账号**：Providers 页有「Hide accounts」开关，遮蔽所有页面的邮箱与账号名，便于截图分享 | `index.html:137` |
| **F8** | **发现即导入**：扫到本机已有配置时，顶部出现提示条 +「Review and import…」/「Ignore」 | `index.html:133-136` |
| **F9** | **能力门按 entitlement**：`data-feature="sessions\|library\|routing\|gateway\|usage\|redact"` 直接控制导航项显隐 | `index.html:72-83` |
| **F10** | **页内标签而非新页面**：Agents 页下挂 Allowances / Usage / Routing 三个页签 | `index.html:110-114` |

---

## 三、逐维度对比（magpie × 我们 v1 × v2 动作）

| # | 维度 | magpie | 我们 v1 | v2 动作 | 照搬？ |
|---|---|---|---|---|---|
| **C1** | **首页形态** | 一张清单：客户端 × 当前模型（`index.html:117`） | 8 区块仪表盘（4 KPI + 引导 + 2 表 + 2 面板） | **首页改成「接入清单」**：一行一个网关密钥/客户端，行内显示 当前模型 / 健康 / 今日请求 / 错误；KPI 收成一行小字 | **采纳形状**（清单化），内容按我们的对象重写 |
| **C2** | **接入一个上游** | preset + key 两步，细节进抽屉（`app.js:9300`） | 供应商表单（5 项）+ 凭据表单 + 模型绑定；探测是提交前的门 | **快速接入面板**：选目录 → 贴 Key → 完成；base URL/协议/模型清单由 `provider_catalog` 自动填；探活转后台；细节进「更多设置」抽屉 | **采纳**（默认值 + 抽屉），不砍我们的绑定与限额维度 |
| **C3** | **模型清单来源** | 问厂商 + `models.dev` 补齐（`reference.md:62`） | 静态目录为主；「从供应商读取」埋在供应商详情页 | **模型选择器三段**：① 该供应商当前提供的（走 `discovery`）② 本区间有请求的（沿用 11-16 审计的置顶组）③ 完整目录（收在抽屉） | **采纳** |
| **C4** | **每个客户端自己的模型清单** | 「Showing 5 / 32」逐个摘出（`reference.md:70`） | 无对应面 | **供应商页**三段选择器 + 启停该供应商的 model_offers（写 `PATCH /api/providers/{id}/models/{offerId}/state`）；**密钥行**加「当前模型」只读列（从 per-key 用量取最近模型名，无写操作）。★ **取证订正（2026-11-17 02:08）**：设计稿写的「我们已有 per-key 约束面」在本仓**不成立** —— `applications.allowed_models_json` 是从未被读写的死列，`PATCH /api/keys/{id}` 只收 `profile/owner/remark/alias` ⇒ **没有任何 per-key 模型权限写面**。读面齐全（per-key/per-provider 用量、provider 的 `model_offers`、完整目录），per-provider 写面真实存在（路由会消费）⇒ L2 落供应商页 | **采纳思想**（三段选择器 + 启停对象），**写侧改为 per-provider**（密钥行只读当前模型） |
| **C5** | **关停对象的表达** | 「Turned off」折叠 + 计数（`index.html:140`） | 折叠不隐藏，无计数 | **「已关停 / 零流量」折叠 + 计数**，零流量排在有请求之后（11-16 审计的排序契约） | **采纳** |
| **C6** | **账号遮蔽** | 一键遮蔽（`index.html:137`） | 无 UI 面 | **上游与凭据页加「遮蔽账号」开关**（底层用 11-15 轮那套确定性假名化） | **采纳**（小项） |
| **C7** | **发现即导入** | 扫本机配置 → 建议导入（`index.html:133-136`） | 无 | **不照搬**（magpie 改的是**客户端**配置，我们是服务端，不该动用户机器上的文件）<br>★ 但保留心智：**把「已存在但未纳管」的东西变成候选清单**（如未挂凭据的目录项、0 流量的在册供应商） | **不照搬 / 改写** |
| **C8** | **能力门** | `data-feature` 按 entitlement 显隐（`index.html:72-83`） | 插件 entitlement 已有；导航可见性按角色/租户 | **简易模式的「可选块」按授权灰显并写明「需授权」**，不静默隐藏 | **采纳**（我们已有 entitlement 底座） |
| **C9** | **导航规模** | 4 组 9 项 + Settings | 4 直链 + 4 组 + 高级 50（露出 13） | **量级已追平，v2 不再压导航**：结构保持 4 直链 + 4 组 + 高级，省下的力气投到首页与接入（详见 v2 稿 §五） | **已追平，不动** |
| **C10** | **页内标签** | 页签代替新页面（`index.html:110-114`） | 页面清单为主 | **接入清单页内挂「用量 / 请求」两个页签**，不新增导航项 | **采纳**（小项） |
| **C11** | **多租户 / 配额 / 审计 / 插件 / 运维** | **没有**（单机个人工具） | **有，且是商业价值**（租户、用户、密钥、预算、对账、审计、插件、运维中心） | **全部保留**；专家面 63 条**一条不删**（高级折叠 49 项，v2 台账逐条可查；2026-11-17 /tenant/usage 提升进「请求」组后 50→49） | **绝不照搬（magpie 无此面）** |
| **C12** | **配置载体** | 改本机 `settings.json` / `config.toml`，只动一个键 | PG + Redis 事实源，模式写入面只有 `gateway.mode` 一个键 | **不动** | **不照搬** |

---

## 四、借鉴清单（v2 的五条来源）

| B# | 借鉴 | 来源 | 在 v2 里的落点 |
|---|---|---|---|
| **B1** | **首页清单化**：一行一个对象，行内可改关键属性 | F2 | 首页＝接入清单；点行进详情，不跳页 |
| **B2** | **默认值替用户做完决定**：preset 决定 base URL/协议/模型清单，细节进抽屉 | F3 | 快速接入面板 + 「更多设置」抽屉（我们复用 `provider_catalog`） |
| **B3** | **清单来自事实源**：模型清单问厂商，不编译进代码 | F4 | 模型选择器三段，第一段走 `discovery` |
| **B4** | **关停对象折叠且计数可见** | F6 | 「已关停 / 零流量」折叠头带计数 |
| **B5** | **一键遮蔽账号** + **能力门灰显** | F7 / F9 | 上游页遮蔽开关；授权不足的可选块灰显并写明原因 |

---

## 五、不照搬清单（magpie 有、我们不跟）

| N# | magpie 的做法 | 为什么我们不跟 |
|---|---|---|
| **N1** | 改客户端本机配置文件（`settings.json` / `config.toml`） | 我们的部署形态是服务端网关；客户端的配置文件不归我们写，也不该写（越权 + 不可回滚） |
| **N2** | 删掉租户 / 角色 / 配额 / 审计，换取界面干净 | 这是我们的产品边界与商业价值；「简单」必须靠**投影与折叠**，不能靠**删能力** |
| **N3** | 首页只有一个模型选择器，其余全在二级 | 我们的用户还要管预算、限额、密钥、路由；这些不能消失，只能被折叠 |
| **N4** | 文件态配置 + 单机订阅轮换 | 本仓事实源是 PG/Redis，多实例；文件态会引入一致性事故 |
| **N5** | 进程内 JS 中间件 | 本仓插件运行时是 supervisor + 进程外，形态不同（见 10-24 轮审计 §不照搬清单） |

---

## 六、v2 相对 v1 的五处改动（逐处都有「不丢什么」的交代）

| # | 改动 | v1 | v2 | **不丢什么** |
|---|---|---|---|---|
| **R1** | 首页形态 | 总览仪表盘（8 区块） | **接入清单**（一行一密钥/客户端）+ KPI 收成一行 | 请求日志、用量统计、对话、实时拓扑、接入示例、帮助手册 —— **入口全部保留**，只是首页换皮；被换掉的「最近请求表」并入接入清单页签 |
| **R2** | 接上游 | 3 表单 + 2 门 | **两字段快速接入**（选目录 + 贴 Key），探活转后台 | 凭据的并发/余额/标签/备注、模型绑定、绑定级限流 —— 全部进「更多设置」抽屉，**不删字段** |
| **R3** | 模型选择器 | 静态目录 | **三段**（落点：供应商页）：该供应商当前提供的 / 本区间有请求的 / 完整目录 + 启停供应商 model_offers | 完整目录在第三段；厂商那一段来自既有 `discovery`；**写侧＝per-provider**（`PATCH /api/providers/{id}/models/{offerId}/state`，路由会消费）；密钥行只读当前模型（per-key 用量取最近模型名） |
| **R4** | 关停与零流量 | 只在专家面可见 | **折叠 + 计数**进简易模式供应商页 | 11-16 审计修复的「零流量供应商必须可见」语义**继续生效**，只是折叠起来 |
| **R5** | 导航 | 4 直链 + 4 组 + 高级 47 | **结构不变**（4 直链 + 4 组 + 高级），高级计数按现行真源订正为 **49** | 折叠组的目录结构与专家面一致；`canShowNavItem()` 仍是唯一判定处；专家面 63 条一条不删 |

---

## 七、功能不丢失台账（摘要，全表在 v2 稿 §七）

### 7.1 v1 三档功能清单 → v2 处置

- **保留（6 项）**：数据面三套入向协议 / 配置供应商·凭据·模型目录 / 密钥签发（两字段）/ 观察总览·请求日志·用量统计 / 设置（通用·压缩·限流）/ 帮助·接入示例·对话
  ⇒ **v2 一条不改**，只换首页皮肤与供应商表单形状。
- **降级（9 项）**：限流（宽松默认）/ 熔断（关或改写为直连失败即失败）/ 输出合规（保留三项硬检查）/
  提示词注入检测（关）/ 审计（保留轻量）/ 脱敏（保留输入脱敏与占位符还原）/ 会话巡检（关）/
  会话摘要与全景分析（关）/ 项目归属推断（关）
  ⇒ **v2 一条不改判**，且「已降级」标记规则照旧（P4）。
- **隐藏（13 类子系统 + 4 块专家项）**：后台 worker 群 / 8 套探针矩阵 / URSM v2 / 多层队列调度 /
  插件运行时 / MaaS 计费订阅 / 销售报价 / 审批流与通知 / 目标编排 / 模型质量·可信度·供应商画像 /
  对账报表与统计 worker / 路由优化机器学习 / 会话取证归档摘要聚类 ＋ 运维中心整组 / 运营管理平台侧 /
  数据运维专家项 / 路由与观测高级项
  ⇒ **v2 一条不动**。

### 7.2 magpie 有、我们要么采纳要么改写的 5 项

见 §三 C7（改写为「未纳管对象候选清单」）、C4（采纳思想不照搬交互）、C6 / C8（采纳）、
C10（采纳小项）。

### 7.3 专家面 63 条 → v2 位置

v2 稿 §七给出**逐条台账**：每条写「中文名 / 落地路径 / 专家分组 / v2 位置」，
落点只有四种：**组内直链 / 组内下拉 / 高级折叠 / 站外链接**。
**判定标准（钉死）**：任何一条若在 v2 稿里找不到落点，即为设计缺陷，不得发布。

---

## 八、风险与未决项

| # | 风险 | 处置 |
|---|---|---|
| **G1** | 首页换成清单后，「今天花了多少」变弱 | KPI 不删，收成清单页头一行小字 + 点开看明细 |
| **G2** | 快速接入把「探测失败就拒」改成「后台探活」，可能让用户以为接好了 | 接入完成页显示**探测状态**（后台进行中 / 成功 / 失败可重试），失败时给「查看日志」入口 |
| **G3** | 「更多设置」抽屉可能把关键限额藏起来（并发/余额） | 抽屉内的**限流类字段默认展开**，其余收起；抽屉标题显示「已改 N 项」 |
| **G4** | 模型三段选择器与 11-16 审计的「本区间有请求的模型置顶组」可能重复实现 | v2 的第一段＝厂商提供的，第二段＝有请求的，**实现上共用路由页同一个 `ModelPicker`**，不新造组件 |
| **G5** | 63 条台账靠人工维护会漂移 | 台账须由 `appNav.ts` 生成（或加一道静态门比对），**不得手抄** |
| **G6** | 遮蔽开关与日志脱敏假名化是两套 | 遮蔽只做**展示层**，底层复用同一套假名化函数，不新造算法 |

---

## 九、证据索引

### 9.1 magpie 侧（`~/workspace/ai/magpie` @ `ca3e57f94`）

| 引用点 | 位置 |
|---|---|
| 导航 4 组 9 项 | `internal/gui/assets/index.html:69-85` |
| 页内标签 | `internal/gui/assets/index.html:110-114` |
| 首页＝Agents 清单 | `internal/gui/assets/index.html:117` |
| 发现即导入 | `internal/gui/assets/index.html:133-136` |
| 一键遮蔽账号 | `internal/gui/assets/index.html:137` |
| 关停折叠 + 计数 | `internal/gui/assets/index.html:140` |
| preset 决定 base API / 抽屉字段 | `internal/gui/assets/app.js:9300`、`:9339` |
| 「一屏 / That is the whole app」 | `docs/reference.md:7-13` |
| 「Providers with one field」 | `docs/reference.md:56-60` |
| 「Real model lists, nothing compiled in」 | `docs/reference.md:62-68` |
| 「Each agent's own model list」 | `docs/reference.md:70-79` |

### 9.2 本仓侧（`llm-gateway-go-5` @ `14d165a7f`）

| 引用点 | 位置 |
|---|---|
| 导航 63 条目 / 62 路径 / 8 静态组 | `web/src/config/appNav.ts`（组定义 `:133,163,191,205,229,256,270,284`；插件合成组 `:430`；顶层 3 项 `:66-71`） |
| 可见性唯一判定 | `web/src/config/appNav.ts:299`（`canShowNavItem`） |
| 新增供应商表单字段 | `web/src/views/ProvidersView.vue:1146-1246` |
| 新增凭据表单字段 | `web/src/views/ProvidersView.vue:1582-1600` |
| 供应商 preset 底表 | `sql/objects/tables/provider_catalog.sql:1-40` |
| 从厂商拉模型 | `discovery/discovery.go:326-347` |
| 「从供应商读取」按钮文案 | `web/src/locales/zh-CN/providerDetail.ts:295` |
| 该按钮与校验工具同路径 | `cmd/verify-model-fetch/main.go:1-6` |
| per-key 模型用量/成本表 | `sql/objects/tables/api_key_model_cost.sql` |
| v1 分析稿（三档清单 / 五条原理 / 运行时裁剪 13 类） | `docs/03-design/02-feature-design/simple-expert-mode-design.html` |
| v1 效果稿 | `docs/03-design/02-feature-design/simple-mode-ui-mockup.html` |
| v1 概念稿（首页 8 区块 / 61→14+47） | `docs/03-design/02-feature-design/simple-mode-concept.html` |
| 零流量供应商修复（排序契约） | 提交 `dc263b085`（11-16 轮审计，主档 `docs/audit/2026-11-16-对账页供应商与模型数据完整性审计-主档.md`） |
| 历史 magpie 对比（网关内核域，非 IA） | `docs/research/2026-10-30-magpie四域对比刷新与优化方案.md`、`docs/research/2026-10-24-magpie-v0.1.1367-能力刷新与网关流程优化（含插件扩展点专题）.md` |

---

## 十、交付物与复查方式

### 10.1 交付物

| 文件 | 是什么 |
|---|---|
| `docs/03-design/02-feature-design/simple-mode-v2-magpie-comparison.md` | 本文：对比分析（逐维度 + 借鉴 + 不照搬 + 台账摘要 + 证据索引） |
| `docs/03-design/02-feature-design/simple-mode-concept-v2.html` | **简易模式 v2 设计稿**：v1 诊断 → magpie 对比 → 借鉴/不照搬 → v2 整页视图 → 菜单全貌 → 关键页改造 → 功能台账 → 分期与风险 → 证据索引 |
| `docs/03-design/02-feature-design/assets/simple-mode-concept-v2/{dark,light}/*.png` | 设计稿逐节截图（深/浅两套），由导出工具生成并附布局体检报告 |
| `docs/03-design/02-feature-design/assets/simple-mode-concept-v2/layout-audit.json` | 布局体检报告（本次：两套主题各 6 项检查**全 0**） |
| `scripts/audit/gen-simple-mode-ledger.mjs` | **台账生成器**：从 `appNav.ts` 生成 63 条落点表，`--check` 比对行数 |
| `scripts/audit/export-concept-png.mjs` | **导出 + 体检工具**（本轮扩展了 `--sections` / `--sections-file`，使其可导别的稿子） |

### 10.2 三条复查命令（都可重复执行）

```bash
# ① 台账行数与真源一致？（手抄即红）
node scripts/audit/gen-simple-mode-ledger.mjs --check docs/03-design/02-feature-design/simple-mode-concept-v2.html

# ② 导出逐节 PNG 并跑布局体检（退出码 0 = 干净，2 = 有问题）
node scripts/audit/export-concept-png.mjs \
  --html docs/03-design/02-feature-design/simple-mode-concept-v2.html \
  --out  docs/03-design/02-feature-design/assets/simple-mode-concept-v2 \
  --sections-file /tmp/v2-sections.json --themes dark,light --scale 2

# ③ 看一眼真源数字（条目数 / 落地路径数 / 各落点计数 / 未解析中文名的条目）
node scripts/audit/gen-simple-mode-ledger.mjs
```

### 10.3 本轮的体检结果（实测，非承诺）

- 布局体检：深色 + 浅色两套主题，`clipped / tinyIcon / hugeIcon / danglingUse / hOverflow / microText`
  **各 6 项全部为 0**（见 `layout-audit.json`）。
- 台账自检：**63 条台账行 == 真源 63 条目 / 62 个落地路径**，绿。
- 目视复核：整页视图与 magpie 对比表两张图已人工看过（见交付 PNG），未发现压字、溢出、错位。
