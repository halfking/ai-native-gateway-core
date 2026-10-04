# 06 — Hyper 导航上下文

> 状态标记：**[已落地]** 全部有实现且有门禁。
> 实现：`src/lib/shell/hyper/`（`types` / `title` / `context` / `overlay` / `back` / `capabilities` / `index`）
> 门禁：4 个 `*.spec.ts`，合计 **77 例**（`title` 17 / `context` 25 / `back` 20 / `capabilities` 15）。
> 跨文件门禁 `gates.spec.ts` 另计 22 例，见 [05 §6](./05-落地清单.md)。

Hyper 导航上下文回答四个问题：**标题显示什么、返回去哪、去敏状态存什么、原生能力有没有。**
这四件事在桌面浏览器里是隐式的，在壳内必须显式 —— 否则系统返回键没有确定的语义。

## 1. 标题：8 级解析次序

**[已落地]** `src/lib/shell/hyper/title.ts` 的 `TitleResolver.resolve()`

| 级 | 来源 | `TitleSource` |
| --- | --- | --- |
| 1 | 覆盖层显式 `title` | `registered` |
| 2 | 覆盖层自身 DOM / aria | `aria` |
| 3 | **继承打开前的父层快照** | `inherited` |
| 4 | 当前页面显式登记 | `registered` |
| 5 | 当前页面 DOM 回退 | `dom` |
| 6 | 路由 meta 翻译值 | `route` |
| 7 | `document.title` | `document` |
| 8 | 应用名 | `document` |

### 1.1 三条不可省的约束

**（1）覆盖层不向 `document` 兜底。**
`resolveDomTitle(root)` 只在**传入的 root 内**查，找不到就返回 `null`。
若允许兜底到 `document.title`，一个没有标题的弹层会把整页标题顶掉，
而用户看到的是"弹窗标题变成了上一页的名字"。

**（2）继承必须在"打开那一刻"取快照，不能现算。**
弹层打开期间背景页可能已经换页。现算会把**背景页的新标题**当成"父层标题"继承过来。
所以 `resolve()` 的 `inheritedTitle` 由调用方在打开时传入。

**（3）继承来源要记录"产生标题的那一层"。**
上层也是弹层时记它的 `overlayId`，否则记页面 `entryId`。
这样"返回"时才知道该退到哪，而不是退到页面根。

**冷启动直接开弹层**（第 3 级没有快照）时继续往下走 4→8，
但**不置空、不填"弹窗"二字** —— 填"弹窗"是伪造信息。

### 1.2 `sanitizeTitle`

去标签、限长 **200**、去控制字符、入参类型为 `unknown`（任何东西传进来都不会炸）。
**导航栏渲染失败不该连带整页崩**，所以最后一级返回空串而不是抛错。

### 1.3 epoch 闸门

**异步结果必须携带自己那份 epoch。**
`registerPageTitle(entryId, patch)` 在 `patch.epoch < prev.epoch` 时**返回 false 且不写入**。

场景：进入详情页时异步取项目名，拿到之前用户已经退出到列表页。
没有 epoch 闸门时，晚到的项目名会把**列表页的标题**覆盖成详情页的项目名。

## 2. 导航上下文：`NavigationStore`

**[已落地]** `src/lib/shell/hyper/context.ts`

### 2.1 id 是访问身份，不是 URL

条目 id 标识**一次访问**，不是地址。同一个详情页打开两次是两个条目。

- `replace` 复用**同一个 id**（同一访问只是换了呈现，不是新访问）
- 从某条目发起新访问时，**截断前进分支**（不能留着一条去不回来的历史）
- 外部 popstate 只有 `applyExternalPop()` 一个写入口

### 2.2 去敏持久化

导航状态会写进 `sessionStorage`，因此必须去敏。

| 机制 | 值 |
| --- | --- |
| 白名单 `PERSISTED_QUERY_WHITELIST` | `tab` `view` `mode` `start` `end` `page` `metric` `app` |
| 黑名单 `PERSISTED_QUERY_DENYLIST` | `q` `filter` `match` `slist` `set` `search` `keyword` `redirect` `login` … |
| 值形状闸门 | 只接受 `^[A-Za-z0-9_.:-]{1,64}$` |
| 标题 | **只存 i18n key**，不存渲染后的文案 |
| 分区 | 按 scope 分区，不跨 scope 复用 |

**三条都必要**：白名单挡新加的敏感键；黑名单挡历史遗留的；
**值形状闸门**挡"白名单键被塞进任意字符串"（例如把 token 塞进 `page`）。
标题只存 key 是因为渲染后的标题可能含实体名（如租户名），跨账号复用会泄露。

### 2.3 容量上限

`MAX_ENTRIES = 80`、`MAX_OPERATIONS = 100`。超出后丢最旧的。
**无上限的导航历史会在长会话里把 `sessionStorage` 撑满**，配额满时写入会静默失败。

## 3. 覆盖层：`OverlayRegistry`

**[已落地]** `src/lib/shell/hyper/overlay.ts` — **覆盖层的唯一真源**。

| 能力 | 说明 |
| --- | --- |
| 幂等 `register` / `unregister` | 同 id 重复登记不压入新条目 |
| `top` by priority | 不是按登记顺序，是按显式 priority |
| `closeTop()` 三条拒绝路径 | 无守卫 / 守卫拒绝 / 非顶层 |
| `upgrade()` | 同 id 重入时**升级元数据**而不是压入新条目 |
| 订阅者异常隔离 | 一个订阅者抛错不影响其它层 |

`composables/useOverlayStack.ts` 已改为它的门面。改造前 ESC 与系统返回依据
**两个独立栈**，会关掉不同的层。

## 4. 返回：`BackDispatcher`

**[已落地]** `src/lib/shell/hyper/back.ts`

分派链（按序，先命中先处理）：

1. **子层 consumer** — 正在交互但不是一层（浮层、下拉）
2. **覆盖层 top** — 有守卫则先问守卫
3. **entry 前驱** — 导航栈里的上一个访问
4. **路由登记的安全 fallback** — 用 `replace`，避免「首页→详情→首页」循环
5. **交还系统** — 根页无层时，**Web 留在原地；原生把意图交还系统，不默认杀进程**

- **单飞**：并发返回请求只处理一次（`inFlight`）。
- **两阶段提交**：`request()` 只登记意图，cursor 由 `afterEach` 在导航成功后**唯一**改写。

> ### ★ 两阶段提交（曾经写错过）
>
> 改造前 `request()` **先改 store 再调路由**。若路由守卫拒绝导航，
> 页面没退但上下文以为退了 —— 下次返回会跳层。
>
> 现改为：`request()` 登记意图 → 调路由 → `afterEach` 成功后才
> `applyExternalPop()` 改写 cursor。守卫拒绝时 store 与页面**保持一致**。

> ### ★ 「无端口」不是「成功」
>
> 根页无层且无前驱时，`request()` 返回 `'unsupported'`，**不是** `'navigated'`。
> 把 no-op 报成 success，会让调用方以为可以继续做后续动作。

## 5. 能力握手：`capabilities.ts`

**[已落地]** `PROTOCOL_VERSION = 2`。**四关全部 fail-closed**，任一关不过即整体降级。

| 关 | 判定 |
| --- | --- |
| 桥存在 | `window.Capacitor?.isNativePlatform?.()` |
| 协议版本 | 双方 `PROTOCOL_VERSION` 相符 |
| origin 精确匹配 | 非 `*` 通配 |
| 注册表存在 | 能力表可读且非空 |

### `hyperMode` ≠ `capabilities`

**`hyperMode` 是形态开关**（在壳内，或 viewport 是 compact），
**`capabilities` 才证明原生能力可用**。

两者必须分离：一个页面在桌面窄窗口里也是 `hyperMode: true`，
但它没有任何原生能力。若把形态开关当能力证明，页面会去调不存在的插件，
然后在**用户看不到的地方**吞掉异常。

握手失败时前端**不假设**自己留在 WebView —— 见
[04 §3](./04-Hybrid外壳.md)。

## 6. 装配：`installHyper(router, opts)`

**[已落地]** `src/lib/shell/hyper/index.ts`

- **幂等**：`routerHookInstalled` 守卫，重复调用不重复挂适配器
- `hyperActive: false` 时**不挂**路由适配器、**不写**持久化、**不绑** Esc
- `App.vue` 用 `watch(isCompact)` 重装配（compact ⇄ 桌面切换）

> **已知取舍（K-3）**：compact → 桌面时 Hyper 路由适配器**不拆卸**。
> 影响仅是桌面下仍记录导航条目到 `sessionStorage`。
> 代码里有注释标注。拆卸逻辑见 [10 路线](./10-审计与实施路线.md)。

## 7. 改这一篇的同轮义务

改标题解析、导航去敏、返回分派或能力握手时，**同轮**：

1. 本篇对应小节
2. [00 §9.2 缺陷表](./00-需求与优化方案.md)
3. 对应 `*.spec.ts`，并**做变异验证**确认新判据有牙
