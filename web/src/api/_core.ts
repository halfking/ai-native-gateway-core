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

// 2026-07-10: 401 redirect 现在只针对 admin 端点。
// /healthz?full=true / /api/system/version 等公共或半公开端点的 401
// 不应该触发强制重定向，否则会把用户弹到 /login 形成 loop。
// /api/auth/me 也包括在内：用于 App.vue hydration 探测 cookie 状态，
// 401 应当抛错让调用方处理（App.vue 会显式调 clearJwt），而不是全屏跳登录。
function isAdminProtectedPath(path: string): boolean {
  return (
    path.startsWith('/api/admin/') ||
    path.startsWith('/api/users') ||
    path.startsWith('/api/keys') ||
    path.startsWith('/api/auth/logout') ||
    path.startsWith('/api/auth/change-password') ||
    path.startsWith('/api/routing/') ||
    path.startsWith('/api/admin')
  )
}

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

// isSessionExpiredAuthLoss reports whether a 401 from `path` should be
// treated as "the session died mid-page" for a user the SPA still
// considers authenticated. Conditions:
//  - the endpoint is an /api/* call (same family as the admin surface,
//    authenticated by the llmgw_session cookie), and
//  - the SPA still believes the user is logged in (stale localStorage
//    userInfo while the cookie expired), and
//  - the endpoint is not the auth hydration probe (/api/auth/me), whose
//    401 is the EXPECTED "logged out" answer and is handled explicitly
//    by App.vue (see comment above isAdminProtectedPath).
// When true the caller clears auth state and bounces to the inline
// login so mounted pollers stop hammering protected endpoints.
function isSessionExpiredAuthLoss(path: string): boolean {
  const pathname = path.split('?', 1)[0]
  if (!pathname.startsWith('/api/')) return false
  // Authentication endpoints deliberately return 401 for an invalid login or
  // an already-cleared session; neither means the current page lost its auth.
  if (pathname === '/api/auth/me' || pathname === '/api/auth/token' || pathname === '/api/auth/logout') {
    return false
  }
  return isAuthenticated()
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
  constructor(status: number, detail: string, code = '') {
    super(detail)
    this.name = 'ApiError'
    this.status = status
    this.detail = detail
    this.code = code
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
    const msg = errorMessage(r.statusText, await r.text())
    // Always bounce to the inline-login home with the original SPA path in
    // `redirect`. A hard jump to `/login` used to clear the request-detail
    // URL; the /login route then treats an already-valid cookie as "already
    // authed" and sends the user to the dashboard overview.
    if (
      typeof window !== 'undefined'
      && (isAdminProtectedPath(path) || isSessionExpiredAuthLoss(path))
      && !window.location.pathname.startsWith('/login')
      && !isOnInlineLoginScreen()
    ) {
      redirectAfterAuthLoss(inlineLoginPath(window.location.pathname + window.location.search))
    }
    // Public endpoints and the hydration probe only surface their status.
    throw new ApiError(401, msg || 'Unauthorized')
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
