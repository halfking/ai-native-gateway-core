import { store, clearApiKey, clearAll, authBearer } from '../store'
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

// ApiError is thrown by req() for non-2xx responses. It always carries
// the human-readable .message that older callers rely on; when the
// server emits the structured envelope ({"error": {"detail", "code",
// "context"}}), the typed fields are populated for callers that want
// to render targeted UI without parsing the message.
//
//   instanceof Error → true (backward compatible)
//   .message        → "fp_slot_limit (25) cannot exceed ..."
//   .status         → 400
//   .code           → "fp_slot_exceeds_concurrency" (or undefined)
//   .context        → { attempted_concurrency: 10, ... } (or undefined)
export class ApiError extends Error {
  status: number
  code?: string
  context?: unknown
  constructor(message: string, status: number, code?: string, context?: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.context = context
  }
}

export function headers(method: string): Record<string, string> {
  const h: Record<string, string> = {}
  // Only send Content-Type when we actually have a body — some
  // middleware/WAFs reject GETs with application/json content-type.
  if (method !== 'GET') {
    h['Content-Type'] = 'application/json'
  }
  const bearer = authBearer()
  if (bearer) h['Authorization'] = `Bearer ${bearer}`
  return h
}

/**
 * Whether the next 401 from `req()` should trigger a hard redirect to
 * /login. Disabled during the very first page load (before any view
 * has had a chance to render) so that a stale token does not produce
 * a one-frame flash of the protected page followed by an immediate
 * jump to /login — which the user reported as "页面闪了一下就消失了".
 *
 * Views flip this back on inside `onMounted` once their initial paint
 * is committed, so a subsequent 401 (e.g. token truly expired during
 * navigation) still triggers the redirect as before.
 */
let authRedirectEnabled = false

export function enableAuthRedirect(): void {
  authRedirectEnabled = true
}

export async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const r = await fetch(BASE + path, {
    method,
    headers: headers(method),
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (r.status === 401) {
    // Token expired or invalid. Clear credentials and (eventually)
    // redirect to /login so the user can re-authenticate instead of
    // seeing a cascade of 401s. We skip the redirect until the first
    // view has finished its initial paint — otherwise a stale token
    // causes the protected page to flash for one frame and then jump
    // to /login, which reads as "the page disappeared" (see issue:
    // 热力图没有数据显示 + 页面闪了一下就消失了, 2026-06-26).
    clearAll()
    if (
      authRedirectEnabled &&
      typeof window !== 'undefined' &&
      !window.location.pathname.startsWith('/login')
    ) {
      window.location.href = '/login'
    }
    throw new Error('Unauthorized')
  }
  if (!r.ok) {
    // Try to parse JSON error first (backend uses
    //   {"error": "..."}              // legacy shape
    //   {"error": {"detail": "..."}}  // envelope shape, all admin handlers
    //   {"error": {"detail": "...", "code": "...", "context": {...}}} // structured
    // ), fall back to plain text.
    let msg = r.statusText
    let code: string | undefined
    let context: unknown
    try {
      const text = await r.text()
      if (text) {
        try {
          const j = JSON.parse(text)
          if (j && typeof j.error === 'string') {
            msg = j.error
          } else if (j && j.error && typeof j.error.detail === 'string') {
            msg = j.error.detail
            if (typeof j.error.code === 'string') code = j.error.code
            if ('context' in j.error) context = j.error.context
          } else {
            msg = text
          }
        } catch {
          msg = text
        }
      }
    } catch {
      // network/abort error reading body; keep statusText
    }
    // Throw a richer error so callers that need the structured detail
    // (e.g. saveSelected wants to display the targeted message without
    // parsing) can instanceof-check it. Backward compatible: .message
    // is the human-readable string existing callers already use.
    throw new ApiError(msg, r.status, code, context)
  }
  if (r.status === 204) return undefined as T
  return r.json()
}

// Re-export shared store types that some domain files reference in
// their function signatures (e.g. ApiKey, UserInfo). Keeping them here
// avoids circular imports between api/* and store.
export type { UserInfo }
export { store, clearApiKey, clearAll, authBearer }