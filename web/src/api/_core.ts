import { store, clearApiKey, clearAll, authBearer, getLocale } from '../store'
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

export function headers(method: string): Record<string, string> {
  const h: Record<string, string> = {}
  // Only send Content-Type when we actually have a body — some
  // middleware/WAFs reject GETs with application/json content-type.
  if (method !== 'GET') {
    h['Content-Type'] = 'application/json'
  }
  // Add Accept-Language header for i18n support
  h['Accept-Language'] = getLocale()
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
    path.startsWith('/api/admin') ||
    path.startsWith('/api/auth/me')
  )
}

export async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const r = await fetch(BASE + path, {
    method,
    headers: headers(method),
    // Rule 20 §6.1: send HttpOnly session cookie (llmgw_session) so the
    // server's AdminMiddleware can authenticate JWT logins via cookie.
    credentials: 'same-origin',
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (r.status === 401) {
    if (isAdminProtectedPath(path)) {
      // 真正的 admin 端点 401：token 失效，clear + redirect 到 /login 让用户重新认证
      clearAll()
      if (typeof window !== 'undefined' && !window.location.pathname.startsWith('/login')) {
        window.location.href = '/login'
      }
    }
    // 公共/半公开端点 401（如 /healthz?full=true）只 throw，不强制 redirect，避免 loop
    throw new Error('Unauthorized')
  }
  if (!r.ok) {
    // Try to parse JSON error first (backend uses {"error": "..."}),
    // fall back to plain text.
    let msg = r.statusText
    try {
      const text = await r.text()
      if (text) {
        try {
          const j = JSON.parse(text)
          msg = (j && typeof j.error === 'string') ? j.error :
                (j && j.error && typeof j.error.detail === 'string') ? j.error.detail :
                text
        } catch {
          msg = text
        }
      }
    } catch {
      // network/abort error reading body; keep statusText
    }
    throw new Error(msg)
  }
  if (r.status === 204) return undefined as T
  return r.json()
}

// Re-export shared store types that some domain files reference in
// their function signatures (e.g. ApiKey, UserInfo). Keeping them here
// avoids circular imports between api/* and store.
export type { UserInfo }
export { store, clearApiKey, clearAll, authBearer, getLocale }