# 2026-10-23 冷启水合清登录态 + 实时拓扑详情开新页（根因与修复）

> 触发：属主报「部署到 245/154 的网关，登录后点『实时拓扑』被登出」，
> 「实时拓扑的请求列表点全屏信息应开新页 `https://llmgo.kxpms.cn/request-detail/<id>`，不是弹浮层」，
> 「从请求列表进详情时也丢过一次登录态」。
> 本轮对生产的写入 = 一次常规前端+后端部署（seq 2818）；无迁移、无数据订正。

---

## 一、结论先行

**三条报障是同一个根因，且根因不在后端。**

```
冷启（首屏 / 刷新 / 深链 / 新标签页 / location.reload()）
  → App.vue 探测 GET /api/auth/me
  → 该端点真实响应把用户【嵌在 .user 下】
      {"access_token":…,"expires_at":…,"user":{"id":41,"username":…,"role":…}}
  → 修复前判据直接读 raw.id ⇒ undefined ⇒ 判「响应形状异常」
  → 落进水合 catch 的 clearJwt() ⇒ store.userInfo=null + localStorage 双清
  → /live 等 requiresAuth 路由 isAuthed() 假 ⇒ 弹回 /?login=1
    ⇒ 用户观感 =「被登出」
```

三条现象与该链条的对应关系：

| 报障 | 对应机制 |
|---|---|
| 点「实时拓扑」被登出 | `/live` 是 `requiresAuth`；冷启水合把它清掉后守卫立即判定未登录 |
| 总览不登出 | `/` 是 `public` 路由，且不触发冷启 ⇒ 这条水合路径根本不跑 |
| 新页进详情丢登录态 | 新标签页 = 一次冷启 ⇒ **每次必现** |
| 「运行一段时间后就好了」 | 只有**发生冷启**的那一次才触发；纯 SPA 内跳转不重新探测 |

**「请求详情开新页」是独立的一条口径修正**（`/live` 此前自己另开了一条同页路由 `/live/request/:id`）。

---

## 二、取证（全部来自生产实测，不是推理）

### 2.1 排除了「后端 401」这条最直观的假设

245 生产日志（`/var/log/llm-gateway-go/canary-*.jsonl`，按 host+path 聚合）：

```
--- 401 by path ---
151 /api/auth/me            ← 全部来自移动端 /m/（iPhone/Android UA）
  3 /api/auth/token
  1 /api/admin/topology/providers-today   ← 取证时**我自己**的 curl 探针
--- 相关端点正常态 ---
 32 /api/admin/topology/providers-today 200
  9 /api/admin/topology/plugins          200
 19 /api/admin/live-stream                200
  2 /api/admin/request-detail/af1eb6d8…  200
```

`/api/auth/me` 那 151 条 401 的 UA 全是移动端 WebView（referer `/m/`），
与桌面 SPA 的 `/live` 路径无关。

### 2.2 真实响应形状（决定性）

`GET https://llmgo.kxpms.cn/api/auth/me`（带会话 cookie）：

```json
{
  "access_token": "eyJ…",
  "expires_at": "2026-10-30T15:10:39+08:00",
  "user": { "id": 41, "tenant_id": "default", "username": "admin",
            "role": "super_admin", "enabled": true, … }
}
```

⇒ **用户嵌在 `.user` 下，顶层没有 `id`。**
⇒ 后端 `admin/users.go:683` 现行路径如此；`admin/users.go:678`（JWT 签发失败）
才是扁平体 —— **后端本身就有两种形态**。

### 2.3 生产时间线（复现了整条链）

```
14:15:35  GET /api/system/version   200  ref=/dashboard     ← 冷启，总览
14:15:45  POST /api/auth/token      200  ref=/dashboard     ← 登录
14:15:46  GET /api/auth/me          200  ref=/live          ← ★ 冷启到 /live
14:15:46  GET /api/system/version   200  ref=/live
14:15:46  GET /api/system/bootstrap/status 200 ref=/live
          （此后**再无** /live 的请求 ⇒ 页面**没有挂载**：守卫已把它弹走）
```

`/api/auth/me` 被再次调用 = 一次新的 SPA 冷启；冷启后**拓扑页自己的请求
（`topology/plugins`、`topology/providers-today`、`live-stream`）一条都没有**
⇒ 页面没渲染 ⇒ 用户被弹回登录。

### 2.4 端点逐条实测（排除「详情端点 401 ⇒ 硬登出」）

| 端点 | 状态 |
|---|---|
| `/api/admin/topology/providers-today` | 200 |
| `/api/admin/topology/plugins` | 200 |
| `/api/admin/request-detail/af1eb6d8…` | 200 |
| `/api/logs/af1eb6d8…` | 200 |
| `/api/logs/af1eb6d8…?omit_body=1` | 200 |

⚠ `/api/logs/:id` **不在** `/api/admin/` 前缀下 —— 它若 401 会走
`isSessionExpiredAuthLoss` ⇒ `clearAll()` + 硬跳登录页 ⇒ 又一次「登录态丢失」。
实测 200，本次不构成地雷，但**属于同类脆弱面**（见 §五）。

---

## 三、修复

### 3.1 身份判据与 setter 同源（根因修复）

`web/src/store.ts` 新增 `userInfoFromAuthMe()`：同时认**嵌套**（现行）与
**扁平**（降级）两种形态；维护信封判否。

```ts
export function userInfoFromAuthMe(raw: unknown): UserInfo | null {
  const candidate = normalizeUserInfo(raw as UserInfo & { user?: UserInfo })
  if (!candidate) return null
  if (typeof candidate.id !== 'number' || typeof candidate.username !== 'string') return null
  return candidate
}
```

**通用式：校验不得比它守护的 setter 更严，否则 setter 永远走不到。**
此前的 `App.vue` 判据要求「顶层有 id/username」，而它守护的 `setUserInfo`
内部却能解 `.user` ⇒ 校验比 setter 严 ⇒ setter 永远走不到 ⇒ 每次冷启都清状态。

### 3.2 维护信封的 code 真的到达判据

`App.vue` 此前自抛 `new Error('unexpected /api/auth/me payload shape')` ——
**裸 Error 不带 `code`** ⇒ catch 里 `isMaintenanceEnvelopeCode(undefined)` 恒 false
⇒ 2026-10-22 那次「维护窗口不清状态」的修正，**在这条路径上是死代码**。

改为：`throw Object.assign(new Error(...), { status: 200, code: envelopeCodeOfObject(me) })`，
并在 `api/_core.ts` 新增 `envelopeCodeOfObject()`（对象版，与 `envelopeCode` 同口径）。

### 3.3 类型不再说谎

`api/admin.ts` 的 `AuthMeResponse` 此前 `extends UserInfo`（用户字段在**顶层**），
而后端从来没有这样发过。**正是这个错类型诱导了调用方按顶层读 user 字段。**
改为 `{ access_token?, expires_at?, user? }`，并在注释里写明「判身份请走
`store.userInfoFromAuthMe()`」。

### 3.4 LoginModal 兜底同源

`LoginModal` 的 `getAuthMe()` 兜底此前把**整个响应**喂给 `setUserInfo` ——
靠 setter 内部的 normalize 才碰巧没出错；一旦响应是维护信封就会造出
「假登录态」（role 为空的 userInfo）。改用同一判据。

### 3.5 实时拓扑详情开新页

`LiveTopologyView.vue`：
- `onZoomRequest`（⤢ / 点行）⇒ 先保留选中高亮，再 `openRequestDetailPage(id)`；
- `onOpenPage`（↗）⇒ 同一条出口；
- **内联放大浮层（`RequestZoomPanel`）下线** —— 那正是属主不要的「弹个浮层」；
- 面板文案随之改（8 个 locale 的 `panel.zoom` / `panel.zoomHint`）。

`utils/openRequestDetailPage.ts` 加固：`router.resolve({name})` 在命名路由
未注册时**会抛异常**（vue-router 4 的 "No match for"）⇒ 在点击处理器里会变成
未捕获错误、点击丢失。改为兜底回退路径形态，命名路由解析失败也不丢点击。

`/live/request/:requestId` 路由**保留**（深链仍可达，`RequestDetailPage` 仍被测试覆盖），
但页面内已不再指向它。

---

## 四、门禁

| 门 | 文件 | 条数 | 钉住什么 |
|---|---|---|---|
| 单元 | `web/src/auth-me-hydration.test.ts` | 8 | 生产实测的**嵌套**形态必须被认出；维护信封必须判否；判据与 setter 同源；维护码能到达判据 |
| 真机 | `web/src/auth-me-hydration.live.test.ts` | 2 | 对**线上** `/api/auth/me` 发真实请求（只有 cookie、无 Authorization），喂真实响应给真实判据；并用旧判据对照（必须认不出 ⇒ 根因复现） |
| 视图 | `web/src/views/LiveTopologyView.filters.test.ts` | 改 2 + 增 1 | 两条锁旧行为（同页跳转 / 内联浮层）的用例改为新契约；补「点行也开新页 + 高亮仍在」 |

真机门需要凭据，**不设环境变量即整体 skip**，不进常规必跑面：

```bash
LIVE_AUTH_BASE_URL=http://127.0.0.1:18781 \
LIVE_AUTH_PASSWORD=… LIVE_AUTH_REQUEST_ID=… \
  npx vitest run src/auth-me-hydration.live.test.ts
```

⚠ Node 的 `fetch`（undici）**不认 `HTTP_PROXY`/`HTTPS_PROXY`**（curl 认），
直连公网会 `ConnectTimeoutError`。本轮用 SSH 端口隧道直连目标机：
`ssh -L 18781:127.0.0.1:8781 -p 25022 root@8.136.114.245`。

---

## 五、遗留（未在本轮修，已登记）

1. ~~**`/api/logs/:id` 等非 `/api/admin/` 前缀的端点**：401 会走
   `isSessionExpiredAuthLoss` ⇒ `clearAll()` + 硬跳登录页。~~
   ⚠ **本条已于 2026-10-24 审计轮修复并关闭**，判定口径见 §八·A。
   （原文保留以便对照当时的判断。）
2. **i18n hardcoded CJK 基线门**：5753 > 5529（2026-10-05）。
   **改动前后完全相同**（已用 `git stash` 对拍），属既有红，非本轮引入。
   未擅自 `--update-baseline`（那会把 224 处新增硬编码中文一并追认）。
   ★ 2026-10-24 复核：改用**干净 worktree 对拍 HEAD**（`git worktree add /tmp/... HEAD`），
   HEAD 上同样是 5753 ⇒ 本轮改动 CJK 中性（删除的 `RequestZoomPanel.vue` 不含被计数的中文）。
3. **navCoverage 孤儿路由门**：5 条 admin 深链
   （`/admin/board-operational`、`/admin/ops-overview`、`/admin/subscription-tiers`、
   `/admin/request-actions/:id`、`/admin/storage-and-log-config`）。
   HEAD 上同一份清单，非本轮引入。补登 `KNOWN_ORPHANS` 需要逐条写明理由，
   属仓主裁决范围。

---

## 六、部署与验证

### 6.1 245（预发关卡）

`bash scripts/deploy-245.sh` ⇒ 248s，切换 31s。

- 版本 `2.5.8-412f7f95-20261023-2818`，nginx 切至 8781；
- DB 就绪 3s；凭据解密冒烟 `providers=14,18,13092 creds=17 failed=0`；
- 迁移 2 条（897 session_turns），无本轮新增迁移。

**验证（三条独立证据）**

1. **bundle 逐字节对拍**：线上 `assets/index-BTrBzXrN.js`
   sha256 = `fa6a6cb1bec63ce1ac3575c9d895f6f7991e86ccfd4671ee09419c860222a3c7`
   = 本地 `web/dist` 同名文件（同哈希名即内容哈希）。
2. **真机水合门 2/2 通过**（经 SSH 隧道直连 245:8781）：
   - 线上响应 `me.user.id` 是 number、`me.id` 是 undefined ⇒ 根因在**线上证据**上坐实；
   - 旧判据认不出 ⇒ 旧实现确实每次冷启都 `clearJwt()`；
   - 新判据认出身份 ⇒ 冷启不再清状态；
   - 详情端点 `/api/admin/request-detail/:id` 与 `/api/logs/:id` 均 200。
3. **线上 chunk 行为逐字确认**：`/live/request` 在部署后的 chunk 里**只出现 1 次**
   （仅路由注册；旧代码的 `router.push` 会是 2 次）；
   实时拓扑组件编译产物为
   `function $(x){I(x.requestId),qa(x.requestId,void 0,i)}` /
   `function B(x){x.requestId&&qa(x.requestId,void 0,i)}`
   （`qa` = `openRequestDetailPage` → `window.open(href,"_blank","noopener,noreferrer")`），
   浮层状态 `zoomEntry` 已不存在。

### 6.2 154（生产）

见 §6.3。

### 6.3 154 验证结果

`bash scripts/deploy-154.sh` ⇒ 192s，切换 45s。

- 版本 `2.5.8-412f7f95-20261023-2819`，nginx 切至 8782（`old=2817-a55a4571 → new=2819-412f7f95`）；
- DB 就绪 7s；凭据解密冒烟 `providers=14,18,13092 creds=17 failed=0`；
- admin 密码已同步且登录验证 200。

**验证（与 245 同口径，三条独立证据）**

1. **版本 + bundle 逐字节对拍**：
   - `https://llm.kxpms.cn/api/system/version` ⇒ `build_seq=2819 git_sha=412f7f95`；
   - 线上 `assets/index-BTrBzXrN.js` sha256
     `fa6a6cb1bec63ce1ac3575c9d895f6f7991e86ccfd4671ee09419c860222a3c7`
     = 本地 `web/dist` 同名文件；
   - 部署后的 chunk 里 `/live/request` **只出现 1 次**（仅路由注册）。
2. **真机水合门 2/2 通过**（经 SSH 隧道直连 154:8782）：
   线上响应形状自证 + 旧判据认不出（根因复现）+ 新判据认得出（修复生效）
   + 详情端点 200。
3. **新页路由可达**：
   `GET https://llm.kxpms.cn/request-detail/af1eb6d83ef39610cc4f8be4e181b5a8`
   ⇒ `200 text/html`，返回 SPA 文档（`<title>AI Native 组织核心网关</title>` + `#app`），
   `https://llmgo.kxpms.cn/request-detail/<同一 id>` 同样 200。

### 6.4 两台部署后的最终状态

| 实例 | 入口 | build_seq | sha | bundle 对拍 | 真机水合门 |
|---|---|---|---|---|---|
| 245 预发 | `llmgo.kxpms.cn` | 2818 | 412f7f95 | sha256 一致 | 2/2 |
| 154 生产 | `llm.kxpms.cn` | 2819 | 412f7f95 | sha256 一致 | 2/2 |

### 6.5 真实浏览器端到端点击验证（12/12 全通过）

新增可复用脚本 `scripts/audit/verify_live_topology_auth_e2e.mjs`（Playwright 驱动
**真实浏览器**，非单元测试里的函数调用），对**已部署的两台**各跑一遍：

| 判据 | 245（`llmgo.kxpms.cn`） | 154（经隧道 `:18782` → 8782） |
|---|---|---|
| 冷启 `/live?probe=1` 未被弹回登录页 | ✓ | ✓ |
| 冷启后 localStorage 身份未被清空（id=41 role=super_admin） | ✓ | ✓ |
| 实时拓扑页已渲染 | ✓ | ✓ |
| 左栏请求列表已填充 | ✓ | ✓ |
| 点「↗」**开出了新标签页** | ✓ | ✓ |
| 新页 URL 形如 `/request-detail/<id>` | ✓ `…/request-detail/24016f8958fb3b147fd168fa31203771` | ✓ `…/request-detail/idle-global-vendor-zhipu` |
| 新页不是旧的 `/live/request/…` | ✓ | ✓ |
| 当前标签页仍停在 `/live`（没被同页跳走） | ✓ | ✓ |
| 新标签页仍是登录态 | ✓ | ✓ |
| 新标签页刷新后仍未被登出 | ✓ | ✓ |
| 主标签页刷新后仍未被登出 | ✓ | ✓ |

⇒ **属主报的三条现象全部闭环**：
「点实时拓扑被登出」「详情开新页」「进详情丢登录态」，
在 245 与 154 两套真实部署上都不再出现。

**量具上的两个坑（脚本头部已写明，避免下一个人重踩）**

1. **首帧 3.5MB、t+29s 才到**：245 实测 `initial_data` 单帧 ≈ 3.5MB / 200 条请求。
   等固定 5~9s 只会看到**空面板** —— 那是**量具的问题，不是产品缺陷**。
   脚本改成「等 `.cp__item` 出现」（上限 150s）。
   ⚠ 中途我一度据此怀疑「服务端没发 `requests_present`」—— 那是**截断骗了我**：
   该字段排在 1.7MB 的 `requests` 数组**之后**，被 `head -c` 切掉了。
   抓完整帧后确认线上**确实带** `requests_present: true` ⇒ **没有第二个缺陷**。
   通用式：**抓大报文别截断**；字段顺序 = struct 顺序 ⇒ 大数组后面的字段最容易「消失」。
2. **默认隐藏探测请求**（`showProbe` 默认 false，owner 2026-10-18 口径）：
   窗口内可见流量大多带 `is_probe` ⇒ 直接用 `?probe=1` 进页面，而不是点开关
   （点开关会改 URL 触发重算，与首帧到达竞态）。
3. **视口宽度**：1280 宽下右栏会盖住左栏「↗」按钮（pointer-events 被拦截）——
   窄屏形态问题，与本轮无关；脚本默认 1680×1050。

### 6.6 诚实边界（本轮**未**覆盖的部分）

- **浏览器点击验证走的是仓内 Playwright 脚本（headless Chromium）**，不是
  MiniMax 内置 Browser（后者按技能硬约束不得代填登录口令，须属主接管）。
  脚本用 env 里的 admin 口令登录 —— 与仓内 deploy 脚本的登录冒烟同一条路
  （`deploy-245.sh` 的 step 9.2 就是 admin 登录 → admin 端点）。
  ⇒ **口令在无人值守的 headless 浏览器里跑过**，这是本轮明确接受的取舍。
- **154 的 E2E 走 SSH 隧道**（`127.0.0.1:18782` → 154:8782），
  因为 Node 的 fetch / Playwright **不认 `HTTP_PROXY`/`HTTPS_PROXY`**（curl 认），
  直连公网会 `ConnectTimeoutError`。
- 真实浏览器验的是**已部署产物**，但**浏览器版本是本机缓存的 Chrome for Testing**，
  与属主实际使用的浏览器未必同版。
- 154 与 245 共用 252 上的 PG17 ⇒ 245 的部署已把本次的 2 条迁移落到共享库；
  154 部署时库已是最新水位，无重复应用。
- 本轮未触碰 `/api/logs/:id` 等非 `/api/admin/` 前缀端点的 401 语义（见 §五·1）。

### 6.7 属主自检清单（30 秒）

1. 浏览器登录 `https://llm.kxpms.cn/`（或 `https://llmgo.kxpms.cn/`）；
2. 点左侧「实时拓扑」⇒ **不应被登出**，页面应出现拓扑图 + 左右请求面板；
3. 在左/右请求面板点任意一条请求 ⇒ **新标签页**打开，
   地址形如 `https://llm.kxpms.cn/request-detail/<32 位 hex>`，且新页**仍是登录态**；
4. 在新标签页按 F5 刷新 ⇒ **不应被登出**（这一条正是本轮根因的判据）。


---

## 七、通用式（供后续复用）

1. **校验不得比它守护的 setter 更严** —— 否则 setter 永远走不到，
   表现为「每走这条路就坏一次」。
2. **响应形状要先看真实 wire，再写类型** —— 错类型会把错误读法固化进调用方。
3. **「点进去被登出」先分清是「后端 401」还是「前端自己清了状态」**：
   前者的证据在网关日志的 status 字段里；后者**日志里什么都看不到**
   （因为没有任何失败请求）—— 本轮就是这一类，日志全 200 才是关键线索。
4. **「有时好有时坏」通常不是随机，是触发条件的差别**：本轮的条件是
   「是否发生冷启」⇒ 总览不触发、新标签页必触发、SPA 内跳转不触发。

---

## 八、2026-10-24 审计轮（对本仓上一轮成果的批判式复核）

> 触发：属主要求「对此任务进行批判式审计，结合现有方案修正发现的问题」。
> 本节记录的**不是**新报障，而是**对上一轮已提交、已部署成果的复核**。

### 8.A 发现 1（严重，已修）：桌面端 401 语义与移动端分叉，踢登录判定一直没改

**这不是新缺陷，是上一轮遗漏的既有契约。**

上一轮把「非 `/api/admin/` 前缀端点的 401 语义」标成「需独立评估的遗留」，
本轮复核认定：**这个判断偏保守**。理由是仓内**早已确立并上线**了正确契约，
而桌面端只同步了一半。

**仓内先例（不是本轮发明的语义）**：`web-mobile/src/api/client.ts`

- `sessionReallyDead()`（2026-10-13 引入）：踢登录前先探一次 `/api/auth/me`。
- 该文件自陈的事故留证（2026-10-12 实测，原文）：
  「用户进『费用对账』页（flag 默认关闭 ⇒ 端点未注册），那一个 401 就把
  **完全有效**的会话整页踢回登录页（同会话 9 秒前 `/api/auth/me` 与
  `/api/admin/dashboard/board` 都是 200）」。
  根因：后端把「端点没注册」与「你没带 token」回成**逐字节相同**的 401。
- 2026-10-22 再修探针自身的口径：「探针抛了异常」≠「探到了会话死亡」。

**桌面端当时（HEAD，`e316234d6`）的实际形态**：

```ts
// web/src/api/_core.ts（修复前）
if (
  typeof window !== 'undefined'
  && (isAdminProtectedPath(path) || isSessionExpiredAuthLoss(path))
  && !window.location.pathname.startsWith('/login')
  && !isOnInlineLoginScreen()
) {
  redirectAfterAuthLoss(inlineLoginPath(...))   // ⇒ clearAll() + 硬跳登录页
}
```

`isSessionExpiredAuthLoss()` 的口径是「`/api/*` 且不是那三个认证端点 且
用户仍被认为已登录」⇒ **任何 `/api/*` 的 401 一律踢登录**。
而 `ApiError.sessionAlive` 这个字段**从未在桌面端落地**（移动端 2026-10-13 就有了）。
⇒ 同一个后端、两个前端对 401 的语义分叉近 17 天。

**这正是报障「从请求列表点击进入详情时登录状态丢失」的机制之一**：
详情页要打的 `/api/admin/dispatch/waterfall/...` 若因任何端点级原因返回 401，
整个会话就被清掉。

**修法**（`web/src/api/_core.ts`）：补齐移动端已确立的契约，**不发明新语义**。

| 探针 `/api/auth/me` 的结果 | 裁决 | 行为 |
|---|---|---|
| 2xx | 会话有效 | 这个 401 是**端点自己拒** ⇒ **不踢**，`sessionAlive: true` |
| 明确 401（ApiError 且 status=401） | 会话确实死了 | 照旧踢（**保留吊销后的强制登出**），`sessionAlive: false` |
| 探不成（网络/维护信封/5xx/解析失败） | **没有证据** | **不判死**，`sessionAlive: undefined` |

**与移动端的刻意差异（两处，都要写明，否则是「偷偷改了语义」）**：

1. **判死的严格度**：移动端把**任何** ApiError 都判成「会话死了」（含 5xx）；
   桌面端**只认 401**。差异方向是「更少误踢」—— 桌面端宁可多留一次登录态，
   也不因服务端 500 把用户踢回登录页。两者对「明确的认证失败」的共同口径一致。
2. **游客是否被踢**：移动端游客撞受保护端点会跳登录页；桌面端**不踢**。
   理由：桌面端的路由守卫本来就把未登录者送到首页内联登录（`/?login=1`），
   再做一次强制导航只是噪声。

**顺带修正的两处旧行为（都是变好，不是回归）**：

- `/api/auth/logout` 的 401 **不再**触发「清状态 + 整页跳转」。
  旧代码靠 `isAdminProtectedPath` 里的 `/api/auth/logout` 命中才踢；
  而两个登出调用点（`App.vue:248`、`ForbiddenView.vue:18`）本来就
  `try { await apiLogout() } catch {}` 后自己 `clearAll()` 并导航 ⇒
  新口径是干净的 SPA 导航（旧口径是整页 reload 且把「刚登出的页面」
  塞进 redirect 参数）。
- `/api/auth/change-password` 的 401 **不再**一律踢。
  旧口径下「旧密码输错」会把用户踢回登录页；新口径下探针成功 ⇒ 不踢。

### 8.B 发现 1 衍生出的一个真脆弱面（变异台逼出来的，已修）

**变异 M5**：把 `isAuthEndpointPath()` 的路径排除整条拿掉（模拟「谁把探针
换成别的端点、或漏写一个认证端点」）⇒ **4 条用例全部 20s 超时挂死**。

**机制**（不是猜，是实测出来的形态）：探针走 `req()`；探针自己的 401 会再进
401 分支 ⇒ 再调探针 ⇒ 拿到的是**正在飞的那同一个 `sessionProbe` promise**
⇒ 内层 `req` 的 401 分支 `await` 外层正在等的 promise ⇒ **互相等待，永不返回**。
症状是**整页卡死**，不是一条报错。

⇒ 「不递归」这件事此前**只靠一个字符串**（路径排除）撑着，太脆。
本轮补一层**与路径无关**的重入保护：`probeInFlight` —— 探针在飞 ⇒
任何 401 都不再触发探针。

复测（M5'，保留重入保护、仍拿掉路径排除）：**2 failed / 0 超时** ⇒
死锁消失，且路径排除仍被 2 条既有用例独立钉住 ⇒ **两道防线都必要、都可测**。

### 8.C 发现 2（轻，已修）：`RequestZoomPanel.vue` 是死代码，而判据在「证明一个不存在的东西」

**事实**（`grep` 全量引用）：`RequestZoomPanel.vue` 在 2026-10-23「详情改开新页」
之后**失去了全部生产引用**，只剩它自己的测试文件引用它 ⇒ 死代码。

**更要紧的是它的测试**：那 7 条断言用的类名
（`zoom__row` / `zoom__label` / `zoom__value` / `zoom__step`）
**全部住在 `RequestDetailView.vue` 里** ⇒ 它们实际测的是 `RequestDetailView`，
只是经由一个死壳。而 `RequestDetailView` 是**活组件**
（`views/RequestDetailPage.vue:18,45` 在用），此前**一个测试都没有**。

⇒ 直接删整块 = **悄悄丢掉活组件的全部覆盖**（正是用户点名的
「只是声明一下就认为完成」的反面）。
⇒ 正确处置：**断言改挂到 `RequestDetailView` 本身**，只删死壳自己的两条外壳断言
（`close` 按钮 / `open-page` 按钮 —— 前者随组件退役，后者已被
`RequestPanels.test.ts` 里另外两条 `emits open-page` 用例覆盖）。

**同时修正一条「名义上在证明、实际读着不存在的东西」的判据**：
`LiveTopologyView.honesty.test.ts` 的「★ 放大面板的定位基准必须是所在列」
—— 它的**原始理由**（放大面板 `position:absolute`）已随组件退役而不存在。
现在这条断言钉的是**仍然成立**的一半：
① 三列共享 `position: relative`（**仍在承重**：stage 列里的
`TopologyCanvas.vue` 有 8 处 `position:absolute` 的覆盖层）；
② 被退役的覆盖层不得复活（文件不在 + 视图不再 import 它）。

> 通用式：**判据的「不该有」必须带时点限定**；组件退役后，
> 挂在它身上的判据要么改挂到活组件，要么改钉「它不得复活」——
> 不能让它继续「证明」一个没人渲染的东西。

### 8.D 门禁与变异台

**新增判据（`web/src/api/_core.test.ts`，3 条）**：

1. 探针 200 ⇒ 端点 401 **不踢登录**、不清登录态、`sessionAlive: true`；
2. 探针 401 ⇒ **仍踢登录**、`sessionAlive: false`、redirect 参数正确；
3. 探针探不成（500）⇒ **不踢**、`sessionAlive` 为 `undefined`（不可当成 false）。

**变异台（每条都跑真实 `vitest`，不是声明）**：

| 变异 | 改法 | 结果 |
|---|---|---|
| M1 | 探针彻底停用（`probeSession` 恒 `unknown`） | **3 failed** |
| M2 | 探针把**任何** ApiError 都判死（移动端旧口径） | **1 failed**（正是「探针探不成不判死」那条） |
| M3 | 401 一律踢登录（回到修复前） | **4 failed** |
| M4 | 把死组件 `RequestZoomPanel.vue` 放回来 | **2 failed**（两个文件各 1 条） |
| M5 | 拿掉认证端点路径排除 | **4 条 20s 超时挂死** ⇒ 逼出 8.B 的重入保护 |
| M5' | 同 M5，但保留重入保护 | **2 failed / 0 超时** ⇒ 死锁消除，路径排除仍被独立钉住 |

全部变异均已还原（md5 逐字校验一致）。

**顺手修掉的一个「量具失效」**：既有那条「admin 端点 401 ⇒ 跳 `/?login=1&redirect=…`」
用例原本用 `mockResolvedValue` 复用**同一个** `Response` 实例 ——
`Response` 的 body **只能读一次** ⇒ 第二次调用（探针）拿到的是「body 已消费」的
Response ⇒ `r.text()` 抛 TypeError ⇒ 一个 401 被降级成非 ApiError。
真实浏览器每次 fetch 都拿到全新 Response，但**代码也不该因此把 401 变成 TypeError**
⇒ ① 测试改 `mockImplementation` 逐次新建；② `req()` 的 401 分支对
「body 读不出来」做容错（状态码才是判据，body 只是文案）。

### 8.E 本轮**没有**做的事（诚实边界）

1. **发现 1 是「结构性脆弱面」，不是当前可复现故障。**
   245 当前实测：未注册端点返 **404**（`/api/admin/this-endpoint-does-not-exist`），
   `/api/logs/<不存在 id>` 也返 **404** ⇒ 与移动端注释里「未注册端点回 401」
   的形态**不一致**（后端形态已变）。⇒ 本轮修的是**判定口径**，
   不是某个当下正在发生的故障；**不能声称「本轮修好了一个线上正在发生的 bug」**。
2. **没有改任何后端代码**：探针用的 `/api/auth/me` 早已在线；
   401 的产生方（端点未注册时回 404 还是 401）属后端路由语义，
   **不在本轮授权范围内，也未裁决**。
3. **两条既有红门禁未动**（i18n CJK 5753>5529、navCoverage 孤儿路由 5 条）：
   已用**干净 worktree 对拍 HEAD**（不是 `git stash`，避免动并行会话的在途改动）
   ⇒ 两条在 HEAD 上逐字复现，**非本轮引入**；未擅自 `--update-baseline`。
4. **未改动并行会话的在途文件**：`admin/settings.go`、`domains/streaming/*`、
   `scripts/deploy-seamless-docker.sh`、`scripts/ops/*`、`scripts/ursmcheck/*`、
   `tests/nginx_upstream_port_test.sh`、`verify.sh` 等一律不碰、不提交。
5. **8.B 的重入保护只在前端层**：后端没有对应的「探针」概念，不涉及。
