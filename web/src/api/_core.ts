import { store, clearApiKey, clearAll, authBearer, getLocale, isAuthenticated } from '../store'
import type { UserInfo } from '../store'

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
  const bearer = authBearer()
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
  constructor(status: number, detail: string) {
    super(detail)
    this.name = 'ApiError'
    this.status = status
    this.detail = detail
  }
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

export async function req<T>(method: string, path: string, body?: unknown, options?: RequestOptions): Promise<T> {
  const hasBody = body !== undefined
  const r = await fetch(BASE + path, {
    method,
    headers: headers(method, hasBody),
    // Rule 20 §6.1: send HttpOnly session cookie (llmgw_session) so the
    // server's AdminMiddleware can authenticate JWT logins via cookie.
    credentials: 'same-origin',
    body: hasBody ? JSON.stringify(body) : undefined,
    signal: options?.signal,
  })
  if (options?.signal?.aborted) {
    throw new DOMException('The operation was aborted', 'AbortError')
  }
  if (r.status === 401) {
    const msg = errorMessage(r.statusText, await r.text())
    if (isAdminProtectedPath(path) && typeof window !== 'undefined' && !window.location.pathname.startsWith('/login')) {
      redirectAfterAuthLoss('/login')
    } else if (isSessionExpiredAuthLoss(path) && typeof window !== 'undefined' && !isOnInlineLoginScreen()) {
      const redirect = window.location.pathname + window.location.search
      redirectAfterAuthLoss('/?login=1&redirect=' + encodeURIComponent(redirect))
    }
    // Public endpoints and the hydration probe only surface their status.
    throw new ApiError(401, msg || 'Unauthorized')
  }
  if (!r.ok) {
    let msg = r.statusText
    try {
      msg = errorMessage(r.statusText, await r.text())
    } catch {
      // network/abort error reading body; keep statusText
    }
    throw new ApiError(r.status, msg)
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
