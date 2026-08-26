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
  if (router) {
    const loc = router.resolve({
      name: 'request-detail',
      params: { requestId: id },
      query: {
        ...(query?.mode ? { mode: query.mode } : {}),
        ...(query?.tab ? { tab: query.tab } : {}),
      },
    })
    return loc.href
  }
  const qs = new URLSearchParams()
  if (query?.mode) qs.set('mode', query.mode)
  if (query?.tab) qs.set('tab', query.tab)
  const q = qs.toString()
  return `/request-detail/${encodeURIComponent(id)}${q ? `?${q}` : ''}`
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
