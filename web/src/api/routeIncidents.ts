// api/routeIncidents.ts — typed HTTP client for the read-only
// route-incident API. All endpoints are super-admin only; a 401/403
// from the server means the caller's session lacks super-admin role
// and the drawer is not opened.
//
// Every fetch is bounded by a 5–10s timeout so a slow DB never
// freezes the dashboard. The functions return null on transient
// errors so the UI can render an "insufficient data" banner.

import type {
  RouteIncidentDetail,
  RouteIncidentEventsResponse,
  RouteIncidentListResponse,
  RouteIncidentTimelineResponse,
} from '../types/routeIncident'

const DEFAULT_TIMEOUT_MS = 8_000

async function fetchJSON<T>(url: string, signal?: AbortSignal): Promise<T | null> {
  try {
    const res = await fetch(url, {
      credentials: 'include',
      headers: { Accept: 'application/json' },
      signal: signal ?? AbortSignal.timeout(DEFAULT_TIMEOUT_MS),
    })
    if (!res.ok) {
      // 404 is normal for cross-tenant / not-found; the caller
      // decides how to render it.
      if (res.status === 404) return null
      return null
    }
    return (await res.json()) as T
  } catch {
    return null
  }
}

export function listRouteIncidents(params: {
  state?: 'active' | 'recovering' | 'recovered'
  visibleOnly?: boolean
  limit?: number
  tenantId?: string
} = {}): Promise<RouteIncidentListResponse | null> {
  const q = new URLSearchParams()
  if (params.state) q.set('state', params.state)
  if (params.visibleOnly) q.set('visible', '1')
  if (params.limit) q.set('limit', String(params.limit))
  if (params.tenantId) q.set('tenant_id', params.tenantId)
  const qs = q.toString()
  return fetchJSON<RouteIncidentListResponse>(
    `/api/admin/route-incidents${qs ? `?${qs}` : ''}`,
  )
}

export function getRouteIncident(
  id: string,
  tenantId?: string,
): Promise<RouteIncidentDetail | null> {
  const q = new URLSearchParams()
  if (tenantId) q.set('tenant_id', tenantId)
  const qs = q.toString()
  return fetchJSON<RouteIncidentDetail>(
    `/api/admin/route-incidents/${encodeURIComponent(id)}${qs ? `?${qs}` : ''}`,
  )
}

export function getRouteIncidentEvents(
  id: string,
  limit = 200,
  tenantId?: string,
): Promise<RouteIncidentEventsResponse | null> {
  const q = new URLSearchParams()
  if (tenantId) q.set('tenant_id', tenantId)
  q.set('limit', String(limit))
  return fetchJSON<RouteIncidentEventsResponse>(
    `/api/admin/route-incidents/${encodeURIComponent(id)}/events?${q.toString()}`,
  )
}

export function getRouteIncidentTimeline(
  id: string,
  tenantId?: string,
): Promise<RouteIncidentTimelineResponse | null> {
  const q = new URLSearchParams()
  if (tenantId) q.set('tenant_id', tenantId)
  const qs = q.toString()
  return fetchJSON<RouteIncidentTimelineResponse>(
    `/api/admin/route-incidents/${encodeURIComponent(id)}/timeline${qs ? `?${qs}` : ''}`,
  )
}
