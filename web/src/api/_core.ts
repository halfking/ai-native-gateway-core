import { store, clearApiKey, clearAll, authBearer, getLocale, isAuthenticated } from '../store'
import type { UserInfo } from '../store'
import { inlineLoginPath } from '../utils/safeRedirect'

// _core.ts — v6.0 audit T12 (2026-06-22)
// Low-level fetch plumbing shared by every other api/* module.
// Re-exports `req<T>(method, path, body?)` plus `headers()` so domain
// modules can call `req('GET', '/api/foo')` without re-implementing
// the 401-redirect + JSON-parse error path.
//
// Before this split, api.ts was a single 4176-line file with all
// helpers at the top. Moving them here lets each domain file stay
// focused on its own endpoints.

export const BASE = '' // same origin in prod; proxied in dev

export function headers(method: string, hasBody = false): Record<string, string> {
  const h: Record<string, string> = {}
  // Some middleware/WAFs reject application/json when there is no body.
  // The decision must be based on the payload, not the HTTP method.
  if (hasBody) {
    h['Content-Type'] = 'application/json'
  }
  // Add Accept-Language header for i18n support
  h['Accept-Language'] = getLocale()
  // Same-origin admin calls use the HttpOnly session cookie. Do not let the
  // in-memory JWT shadow a newer cookie from another tab; legacy sk-* auth has
  // no session cookie and must still use Authorization.
  //
  // 2026-08-31: read authBearer() (jwtToken || apiKey) instead of store.apiKey.
  // Reading only store.apiKey broke every JWT login: apiKey was always empty,
  // so /healthz?full=true (and any admin endpoint called without a cookie
  // shell) returned 401 even when the user was authed via /api/auth/token.
  // See store.ts:authBearer for the source of truth.
  //
  // Phase 1.4 follow-up: if store.userInfo is populated we know the JWT login
  // succeeded and the backend set the HttpOnly `llmgw_session` cookie; let
  // that cookie carry auth and skip the in-memory Bearer to avoid shadowing
  // a fresher cookie from another tab. Legacy apiKey users (no userInfo, no
  // cookie) still rely on the Authorization header.
  const bearer = isAuthenticated() && store.userInfo ? '' : authBearer()
  if (bearer) h['Authorization'] = `Bearer ${bearer}`
  return h
}

// 2026-07-10 起的「按路径前缀挑 401 要不要踢」口径（isAdminProtectedPath）
// 已在 2026-10-24 审计中**退役**：它的前提是「这个前缀的 401 就是会话死了」，
// 而移动端 2026-10-12 已实测证明该前提不成立（端点未注册与未认证回成
// 逐字节相同的 401）。现在统一走**会话探针**裁决，见本文件下方
// 「401 会话裁决」。

// isOnInlineLoginScreen reports whether the SPA is already showing the
// home inline-login (router sends unauthenticated users to /?login=1).
// Loop guard: a 401 arriving while already on that screen must not
// trigger another redirect.
function isOnInlineLoginScreen(): boolean {
  return (
    window.location.pathname === '/' &&
    window.location.search.includes('login=1')
  )
}

// ── 401 会话裁决（2026-10-24 审计修复）──────────────────────────────────
//
// ★ **401 不等于会话过期** —— 踢回登录页之前必须先确认会话是否真的死了。
//
// 仓内先例（不是本轮发明的语义，是仓内**已经确立并上线**的契约）：
//   web-mobile/src/api/client.ts 的 sessionReallyDead
//   · 2026-10-12 实测事故（该文件注释留证）：用户进「费用对账」页，
//     该页端点因 feature flag 关闭而**未注册**，后端把「端点没注册」与
//     「你没带 token」回成**逐字节相同**的 401 ⇒ 那个 401 把**完全有效**的
//     会话整页踢回登录页（同会话 9 秒前 /api/auth/me 与 dashboard/board 都 200）。
//   · 2026-10-13 加会话探针；2026-10-22 修探针自身的口径。
//
// 桌面端此前只同步了「探针」的一半（ApiError.sessionAlive 从未落地），
// **踢登录的判定一直没改** ⇒ 同一个后端、两个前端对 401 的语义分叉，
// 且桌面端仍停在「任何 /api/* 的 401 一律 clearAll() + 硬跳登录页」。
// 这正是 2026-10-23 报障里「从请求列表点击进入详情时登录状态丢失」的机制之一。
//
// 本轮把桌面端对齐到移动端口径：
//   · 探针 GET /api/auth/me 成功 ⇒ 会话是好的 ⇒ 这个 401 是**端点自己拒**
//     ⇒ **不踢**，按 ApiError(401) 走页面级错误态。
//   · 探针**明确认证失败**（ApiError 且 status=401）⇒ 会话确实死了
//     ⇒ 照旧踢（**保留吊销后的强制登出**）。
//   · 探针**没探成**（网络/维护信封/非 ApiError）⇒ **没有任何证据**说会话死了
//     ⇒ 不判死（与移动端 2026-10-22 的同一条纪律）。
//
// ⚠ 探针不会递归，靠**两道互相独立**的闸门（缺一道就会挂死，变异台 M5 实测）：
//   ① isAuthEndpointPath()：探针走 /api/auth/me，它自己的 401 被显式排除；
//   ② probeInFlight：探针在飞 ⇒ 任何 401 都不再触发探针（与路径无关）。

/** 认证端点自身的 401 不构成「会话死了」的证据（探针 / 登录 / 登出）。 */
function isAuthEndpointPath(path: string): boolean {
  const pathname = path.split('?', 1)[0]
  return pathname === '/api/auth/me' || pathname === '/api/auth/token' || pathname === '/api/auth/logout'
}

/**
 * 会话探针的**三态**结论。
 *
 * ★ 为什么要三态而不是布尔：「探针抛了异常」这件事本身**不是**会话死亡的证据
 *   —— 只有**明确的认证失败信号**（ApiError 且 status=401）才算。
 *   维护态信封 / 5xx / 断网 / 响应体不是 JSON（SyntaxError）都没有携带
 *   「会话已失效」的信号 ⇒ 把它们一律判成「会话死了」就是**误杀好会话**。
 *   移动端 2026-10-22 已经因为这个缺陷吃过亏（维护页 ⇄ 登录页死循环）。
 *
 *   · `alive`   —— 探针 2xx ⇒ 会话确实是好的。
 *   · `dead`    —— 探针明确 401 ⇒ 会话确实死了。
 *   · `unknown` —— 探针没探成（网络/维护/5xx/解析失败）⇒ **没有证据**。
 */
type SessionVerdict = 'alive' | 'dead' | 'unknown'

/** 会话探针单飞：并发 401 只多探一次 /api/auth/me。 */
let sessionProbe: Promise<SessionVerdict> | null = null

/**
 * 探针**在飞**标志（与路径无关的重入保护）。
 *
 * ★ 为什么不能只靠 isAuthEndpointPath 挡递归：
 *   探针走 req()，探针自己的 401 会再进 401 分支 ⇒ 再调探针。
 *   路径排除（/api/auth/me 等）能挡住**当前**这条路径，但它把「不递归」
 *   这件事绑死在一个字符串上：谁把探针换成别的端点、或漏写一个认证端点，
 *   就会变成**探针自递归 + 单飞闩锁自锁** ——
 *   实测（2026-10-24 变异台 M5：拿掉路径排除）：
 *     内层 req 的 401 分支 await 正在飞的那同一个 sessionProbe promise
 *     ⇒ 互相等待 ⇒ 永不返回 ⇒ 用例 20s 超时挂死（4 条全挂）。
 *   而路径排除被拿掉的那一刻，症状是**整页卡死**，不是一条报错。
 *   ⇒ 这里补一层与路径无关的闸门：探针在飞 ⇒ 任何 401 都不再触发探针。
 */
let probeInFlight = false

/**
 * 探针结论。
 *
 * ⚠ 与移动端 `web-mobile/src/api/client.ts` 的 `sessionReallyDead` **有意不同**：
 *   移动端把**任何** ApiError 都判成「会话死了」（含 5xx），桌面端只认 401。
 *   差异的方向是「更少误踢」：桌面端宁可多留一次登录态，也不因服务端 500
 *   把用户踢回登录页。两者对「明确的认证失败」的共同口径完全一致。
 */
function probeSession(): Promise<SessionVerdict> {
  if (!sessionProbe) {
    probeInFlight = true
    sessionProbe = (async () => {
      try {
        await req<unknown>('GET', '/api/auth/me')
        return 'alive'
      } catch (err) {
        // 只有明确的认证失败（ApiError 且 status=401）才算会话死亡。
        if (err instanceof ApiError && err.status === 401) return 'dead'
        return 'unknown'
      } finally {
        // 结论只对这一轮有效：下一次 401 必须重新探，不能永久粘住。
        sessionProbe = null
        probeInFlight = false
      }
    })()
  }
  return sessionProbe
}

/**
 * 这次 401 的**会话裁决结论**（三态，直接映射到 ApiError.sessionAlive）：
 *   · `undefined` —— 本次**没有拿到「会话有效」的证据**（没探 / 探了但探不成）。
 *     ★ 不能当成 false：「没探成」≠「探了说是死的」。
 *   · `true`  —— 探针成功 ⇒ 会话有效，这个 401 是**端点自己拒**。
 *   · `false` —— 探针明确认证失败 ⇒ 会话确实死了 ⇒ 踢登录。
 */
async function probeSessionFor401(path: string): Promise<boolean | undefined> {
  if (typeof window === 'undefined') return undefined
  if (authRedirectStarted) return undefined
  // 探针在飞 ⇒ 本次 401 来自探针自身（或与探针并发）⇒ 不得再探（否则自锁）。
  if (probeInFlight) return undefined
  if (window.location.pathname.startsWith('/login')) return undefined
  if (isOnInlineLoginScreen()) return undefined
  // 认证端点自身的 401（探针失败 / 口令错 / 登出后重复登出）不是「会话死了」。
  if (isAuthEndpointPath(path)) return undefined
  // 本来就没登录 ⇒ 没探的必要（也没有会话可清），游客撞到受保护端点
  // 应该看到错误态，而不是被弹去登录页。
  //
  // ★ 这里与移动端**刻意不同**：移动端游客撞受保护端点会跳登录页；
  //   桌面端的路由守卫本来就把未登录者送到首页内联登录（`/?login=1`），
  //   再做一次强制导航只是噪声 ⇒ 桌面端对游客**不踢**。
  if (!isAuthenticated()) return undefined
  const verdict = await probeSession()
  if (verdict === 'alive') return true
  if (verdict === 'dead') return false
  return undefined
}

let authRedirectStarted = false

function redirectAfterAuthLoss(destination: string): void {
  if (authRedirectStarted) return
  authRedirectStarted = true
  clearAll()
  if (typeof window !== 'undefined') {
    window.location.href = destination
  }
}

/** Test helper — 401 bounce is latched for the lifetime of a page. */
export function resetAuthRedirectForTests(): void {
  authRedirectStarted = false
  sessionProbe = null
  probeInFlight = false
}

export function isAbortError(error: unknown): boolean {
  return !!error && typeof error === 'object' && (error as { name?: unknown }).name === 'AbortError'
}

export interface RequestOptions {
  signal?: AbortSignal
}

// ApiError carries the HTTP status code alongside the server-provided
// detail string so callers can branch on status (e.g. stale-revision 409)
// without parsing free-form messages.
export class ApiError extends Error {
  status: number
  detail: string
  /**
   * 2026-10-22 审计修正：维护/边缘组件下发的是**顶层**信封
   * {"code":"maintenance_mode"|"maintain.*","message":…,"request_id":…}
   * （无 .error 包装）。桌面端此前**不提取** code ⇒ 任何按 code 分支的
   * UI 都认不出维护态 ⇒ 水合失败一律走 clearJwt() ⇒ 维护窗口把用户踢登录。
   * 与移动端 web-mobile/src/api/client.ts 的 errorCode() 同一口径。
   */
  code: string
  /**
   * 401 的**会话裁决结果**（会话探针的结论）。
   *
   * 三态而不是布尔：
   *   · `undefined` —— 本次**没有拿到「会话有效」的证据**（没探，或探了但探不成：
   *     网络/维护信封/5xx/解析失败）。「没探成」既可能是会话真的死了，也可能是
   *     压根不适用，**不可当成 false**。
   *   · `true`  —— 探针 /api/auth/me 成功 ⇒ 会话是好的，这个 401 是**端点自己拒**
   *     （典型：feature flag 关闭 ⇒ 端点未注册，后端仍回 401）。
   *   · `false` —— 探针也明确认证失败（401）⇒ 会话确实死了。
   *
   * 为什么必须随错误传下去：把它一律说成「登录状态已过期」= **文案与事实相反**
   * —— 用户的下一步动作完全不同（重登 vs 找运维/换参数）。
   * 与移动端 web-mobile/src/api/client.ts 的同名字段同一口径。
   */
  sessionAlive?: boolean
  constructor(status: number, detail: string, code = '', sessionAlive?: boolean) {
    super(detail)
    this.name = 'ApiError'
    this.status = status
    this.detail = detail
    this.code = code
    if (sessionAlive !== undefined) this.sessionAlive = sessionAlive
  }
}

/** 从错误响应体里取业务错误码（`error.code` 或顶层 `code`；取不到就是空串）。 */
export function envelopeCode(text: string): string {
  if (!text) return ''
  try {
    return envelopeCodeOfObject(JSON.parse(text))
  } catch {
    /* 非 JSON 错误体：没有 code */
    return ''
  }
}

/**
 * envelopeCode 的对象版：输入**已解析**的对象。
 *
 * ★ 为什么要有它：App.vue 的水合判据自己 throw 一个 Error 来表达「响应不是
 * UserInfo」。那个 Error 不带 `code` ⇒ catch 里的 `isMaintenanceEnvelopeCode(code)`
 * 恒 false ⇒ **维护窗口下的 2xx 信封照样 clearJwt()**。
 * 调方需要能从**原始响应对象**上把 code 取出来挂到自造 Error 上，
 * 否则那条「维护态不清状态」的分支对这条路径是死代码。
 * 口径与 envelopeCode 逐字一致（`error.code` 优先，其次顶层 `code`）。
 */
export function envelopeCodeOfObject(parsed: unknown): string {
  if (!parsed || typeof parsed !== 'object') return ''
  const j = parsed as Record<string, unknown>
  const err = j.error
  if (err && typeof err === 'object') {
    const code = (err as Record<string, unknown>).code
    if (typeof code === 'string') return code
  }
  if (typeof j.code === 'string' && j.code) return j.code
  return ''
}

/**
 * 2026-10-22 R107 轮（12h 审计）提取：判断一个业务错误码是否属于
 * **维护态信封族**。
 *
 * ★ 为什么要有这个函数（而不是把判据内联在调用点）：
 *   判据原本内联在 `web/src/App.vue` 的水合 catch 里 ⇒
 *   **桌面端整条「维护态 ⇒ 不踢登录」的决策逻辑没有任何测试覆盖**
 *   （`envelope_code.test.ts` 只测了 `envelopeCode` 的**提取**，
 *     没有测过「提取出来之后怎么决策」）
 *   ⇒ 谁把 `code.startsWith('maintain.')` 删掉、或把
 *     `if (!maintenance && !transient) clearJwt()` 改回无条件 `clearJwt()`，
 *     **桌面端测试套件仍然全绿** ⇒ 而症状是「维护窗口把用户踢回登录页」
 *     —— 正是本轮要治的那个缺陷原样复发。
 *   ⇒ 移动端同名判据 `isMaintenanceMode`（`web-mobile/src/api/client.ts`）
 *     **已经是导出函数 + 有族级负控的 spec**；桌面端此前是「内联 + 无测试」
 *     ⇒ 两边的保护强度不对等 ⇒ 正是仓里 B5「补漏」那一轮踩过的形态
 *     （只补一处 = 修法自身是半截的）。
 *
 * ★ 判据本身（与移动端 `isMaintenanceMode` 逐字同口径）：
 *   - 部署版边缘组件下发 `{"code":"maintenance_mode"}`（2026-10-22 实测）
 *   - 本仓 `cmd/gateway/maintain_proxy.go` 下发 `maintain.upstream_unavailable` /
 *     `maintain.proxy_unavailable` / `maintain.not_configured`
 *   ⇒ 认**整族**（前缀），不认单字面量。
 *
 * ★ 取向：宁可放过一次真维护态，也不误杀一个好会话。
 */
export function isMaintenanceEnvelopeCode(code: unknown): boolean {
  if (typeof code !== 'string' || !code) return false
  return code === 'maintenance_mode' || code.startsWith('maintain.')
}

function errorMessage(statusText: string, text: string): string {
  if (!text) return statusText
  try {
    const j = JSON.parse(text)
    return (j && typeof j.error === 'string') ? j.error :
      (j && j.error && typeof j.error.message === 'string') ? j.error.message :
      (j && j.error && typeof j.error.detail === 'string') ? j.error.detail :
      (j && typeof j.detail === 'string') ? j.detail : text
  } catch {
    return text
  }
}

// ── 全局同源并发闸门（2026-10-16）──────────────────────────────────────────
//
// 为什么需要：一次首屏会并发打出 ~20 个 /api 请求（version / auth/me /
// data-horizon / plugin-nav ×3 / background-tasks / anomalies / keys /
// body-cache-stats / providers / credentials / logs …），实测任意 100ms 窗口内
// 峰值 **13 个** in-flight。浏览器对同源 HTTP/1.1 只有约 **6** 条并发连接，
// 超出部分由浏览器排队或直接放弃 —— 放弃的那些在服务端留下
// `context canceled`，前端表现为「页面完整、无报错、但数据恒为空」
// （/request-logs 的 `/api/logs` 就是这样被饿死的）。
//
// 这里把排队**显式化**：超过上限的请求进 FIFO 队列排队，而不是丢给浏览器
// 去竞争那 6 条连接。浏览器本来就在排队，区别只在于排队失败会变成取消，
// 而这里排队一定会拿到连接。
//
// 上限取 4（低于浏览器的 6）留出余量：静态资源与页面级请求不经过本函数，
// 仍需要那几条连接。
const MAX_SAME_ORIGIN_CONCURRENCY = 4
let inFlight = 0
const waiters: (() => void)[] = []

function acquireSlot(): Promise<void> {
  if (inFlight < MAX_SAME_ORIGIN_CONCURRENCY) {
    inFlight++
    return Promise.resolve()
  }
  return new Promise<void>((resolve) => {
    waiters.push(() => {
      inFlight++
      resolve()
    })
  })
}

function releaseSlot(): void {
  inFlight--
  const next = waiters.shift()
  if (next) next()
}

// throttledFetch —— 给**不经过 req()** 的原生 fetch 调用点用的同一把闸门。
//
// 为什么需要单独导出：首屏有若干请求直接用原生 `fetch`，绕过了 req()：
//   - App.vue            → /api/system/version
//   - composables/useAppNav.ts → /api/system/bootstrap/status
//   - router.ts          → /api/system/bootstrap/status（同一个端点首屏被调两次）
// 它们与 req() 的请求竞争**同一条**浏览器同源连接池，所以闸门必须覆盖它们，
// 否则「限了 req() 的并发」只是把压力挪到了没被限的那几个上。
//
// 语义与 req() 中的 fetch 完全一致：排队拿到连接 → 发请求 → 无论成败都释放。
// 不解析响应、不处理错误，调用方继续按原生 fetch 的方式自己判 status。
export function throttledFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  return (async () => {
    await acquireSlot()
    try {
      return await fetch(input, init)
    } finally {
      releaseSlot()
    }
  })()
}

export async function req<T>(method: string, path: string, body?: unknown, options?: RequestOptions): Promise<T> {
  const hasBody = body !== undefined
  await acquireSlot()
  let r: Response
  try {
    r = await fetch(BASE + path, {
      method,
      headers: headers(method, hasBody),
      // Rule 20 §6.1: send HttpOnly session cookie (llmgw_session) so the
      // server's AdminMiddleware can authenticate JWT logins via cookie.
      credentials: 'same-origin',
      body: hasBody ? JSON.stringify(body) : undefined,
      signal: options?.signal,
    })
  } finally {
    // 无论成功、失败还是 fetch 抛异常都必须让出连接，否则队列会死锁。
    releaseSlot()
  }
  if (options?.signal?.aborted) {
    throw new DOMException('The operation was aborted', 'AbortError')
  }
  if (r.status === 401) {
    // ⚠ 响应体可能读不出来（body 已被消费 / 连接被重置）。此时**仍然按 401 处理**
    //   —— 状态码才是判据，body 只是给人看的文案。早期写法让 `await r.text()`
    //   的异常直接冲出 req() ⇒ 一个 401 变成了 TypeError ⇒ 上层拿不到 status。
    let raw = ''
    try {
      raw = await r.text()
    } catch {
      /* 读不到 body：文案退化成 statusText，判定不受影响 */
    }
    const msg = errorMessage(r.statusText, raw)
    // ★ 先确认会话是否真的死了，再决定要不要把用户踢回登录页。
    //   端点级 401（端点未注册 / feature flag 关 / 资源无权）与
    //   「会话过期」在响应上不可区分 ⇒ 必须探一次 /api/auth/me。
    //   见本文件上方「401 会话裁决」注释与移动端 sessionReallyDead。
    //
    //   `alive` 三态：
    //     undefined ⇒ 没探 / 探不成 ⇒ **没有任何证据**说会话死了 ⇒ 不踢
    //     true      ⇒ 探针成功 ⇒ 会话有效 ⇒ 这个 401 是端点自己拒 ⇒ 不踢
    //     false     ⇒ 探针明确 401 ⇒ 会话确实死了 ⇒ 踢（保留吊销后的强制登出）
    const alive = await probeSessionFor401(path)
    if (alive === false) {
      redirectAfterAuthLoss(inlineLoginPath(window.location.pathname + window.location.search))
    }
    // 会话裁决结论随错误传下去：页面级错误态据此区分
    // 「会话有效但端点拒绝」与「会话真的过期」——两者对用户的下一步动作完全不同。
    throw new ApiError(401, msg || 'Unauthorized', envelopeCode(raw), alive)
  }
  if (!r.ok) {
    let msg = r.statusText
    let code = ''
    try {
      const body = await r.text()
      msg = errorMessage(r.statusText, body)
      code = envelopeCode(body)
    } catch {
      // network/abort error reading body; keep statusText
    }
    throw new ApiError(r.status, msg, code)
  }
  if (r.status === 204 || r.status === 205) return undefined as T
  const text = await r.text()
  if (!text) return undefined as T
  return JSON.parse(text) as T
}

// Re-export shared store types that some domain files reference in
// their function signatures (e.g. ApiKey, UserInfo). Keeping them here
// avoids circular imports between api/* and store.
export type { UserInfo }
export { store, clearApiKey, clearAll, authBearer, getLocale }
