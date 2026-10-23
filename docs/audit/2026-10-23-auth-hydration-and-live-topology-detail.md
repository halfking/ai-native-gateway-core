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

1. **`/api/logs/:id` 等非 `/api/admin/` 前缀的端点**：401 会走
   `isSessionExpiredAuthLoss` ⇒ `clearAll()` + 硬跳登录页。
   口径上「任何 `/api/*` 的 401 都等于会话已死」过宽 —— 端点级的 401
   （资源不存在、无权、参数非法）不应等价于「登录态丢失」。
   本轮实测这些端点均 200，未构成当前故障；修它会改变全局 401 语义，
   属需要独立评估的改动。
2. **i18n hardcoded CJK 基线门**：5753 > 5529（2026-10-05）。
   **改动前后完全相同**（已用 `git stash` 对拍），属既有红，非本轮引入。
   未擅自 `--update-baseline`（那会把 224 处新增硬编码中文一并追认）。
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

### 6.5 诚实边界（本轮**未**覆盖的部分）

- **没有做「真人浏览器点击」的端到端点击验证**：按 Browser 技能的硬约束，
  不得代填登录口令（须由属主在浏览器侧接管）。本轮改为用
  ① 部署产物逐字节对拍 ② 对**线上真实端点**跑真实判据 ③ 部署后 chunk 的
  编译产物逐字确认，三条独立证据覆盖了「代码已上线」与「判据在真实响应上成立」；
  但**「鼠标点下去、页面上确实开出新标签页」这一步没有由我亲手点过**。
  属主按 §6.6 的三步自检即可闭环。
- 154 与 245 共用 252 上的 PG17 ⇒ 245 的部署已把本次的 2 条迁移落到共享库；
  154 部署时库已是最新水位，无重复应用。
- 本轮未触碰 `/api/logs/:id` 等非 `/api/admin/` 前缀端点的 401 语义（见 §五·1）。

### 6.6 属主自检清单（30 秒）

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
