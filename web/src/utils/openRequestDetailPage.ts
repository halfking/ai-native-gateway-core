// openRequestDetailPage.ts — open fullscreen request detail in a new browser tab.
import type { Router } from 'vue-router'

export type RequestDetailOpenQuery = {
  mode?: 'request' | 'session-turns'
  tab?: string
}

/** Build the absolute path for /request-detail/:id (respects router base). */
export function requestDetailPath(
  requestId: string,
  query?: RequestDetailOpenQuery,
  router?: Pick<Router, 'resolve'>,
): string {
  const id = String(requestId || '').trim()
  if (!id) return ''
  const qs = new URLSearchParams()
  if (query?.mode) qs.set('mode', query.mode)
  if (query?.tab) qs.set('tab', query.tab)
  const q = qs.toString()
  // Fallback shape, also used when the named route cannot be resolved (see below).
  const fallback = `/request-detail/${encodeURIComponent(id)}${q ? `?${q}` : ''}`
  if (!router) return fallback
  try {
    const loc = router.resolve({
      name: 'request-detail',
      params: { requestId: id },
      query: {
        ...(query?.mode ? { mode: query.mode } : {}),
        ...(query?.tab ? { tab: query.tab } : {}),
      },
    })
    return loc.href
  } catch {
    // ★ `router.resolve({name})` **throws** when the named route is not registered
    // (vue-router 4 raises a "No match for" Error). This runs inside a click
    // handler, so a throw would surface as an unhandled error and the user would
    // get no navigation at all. The path form is equivalent apart from a
    // non-default `base`, and the caller can retry — never drop the click.
    return fallback
  }
}

/** Open request detail in a new tab; returns false if popup blocked / empty id. */
export function openRequestDetailPage(
  requestId: string,
  query?: RequestDetailOpenQuery,
  router?: Pick<Router, 'resolve'>,
): boolean {
  const href = requestDetailPath(requestId, query, router)
  if (!href) return false
  const win = window.open(href, '_blank', 'noopener,noreferrer')
  return win != null
}
