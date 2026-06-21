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

export async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const r = await fetch(BASE + path, {
    method,
    headers: headers(method),
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (r.status === 401) {
    // Token expired or invalid. Clear credentials and redirect to /login
    // so the user can re-authenticate instead of seeing a cascade of 401s.
    // Using window.location to force a full page reset (clears all
    // in-flight requests that would also 401 with the now-empty store).
    clearAll()
    if (typeof window !== 'undefined' && !window.location.pathname.startsWith('/login')) {
      window.location.href = '/login'
    }
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
export { store, clearApiKey, clearAll, authBearer }