// api/routeIncidents.ts — typed HTTP client for the read-only
// route-incident API. All endpoints are super-admin only; a 401/403
// from the server means the caller's session lacks super-admin role
// and the drawer is not opened.
//
// Every fetch is bounded by a 5–10s timeout so a slow DB never
// freezes the dashboard. The functions return null on transient
// errors so the UI can render an "insufficient data" banner.

import type {
  ActionKind,
  ActionRequest,
  ActionResponse,
  AuditLogEntry,
  DiagnosticRun,
  EvidenceExport,
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

async function postJSON<T>(
  url: string,
  body: unknown,
  signal?: AbortSignal,
): Promise<T | null> {
  try {
    const res = await fetch(url, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify(body),
      signal: signal ?? AbortSignal.timeout(DEFAULT_TIMEOUT_MS),
    })
    if (!res.ok) {
      if (res.status === 404) return null
      if (res.status === 409) return null
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

// ─── Phase 2: action / audit / runs / export ─────────────────────

const ACTION_SLUG: Record<ActionKind, string> = {
  direct_upstream_test: 'direct-upstream-test',
  through_gateway_test: 'through-gateway-test',
  reprobe: 'reprobe',
  release_slot: 'release-slot',
  reset_slots: 'reset-slots',
  reset_availability: 'reset-availability',
  recover: 'recover',
  evidence_export: 'export',
}

export interface DispatchActionOptions {
  tenantId?: string
  expectedVersion?: number
}

/**
 * dispatchAction is the canonical entry point for every mutating
 * action. The dashboard generates an idempotency_key (UUID v4) at
 * call time so retried clicks do not double-execute.
 */
export function dispatchAction(
  incidentId: string,
  kind: ActionKind,
  body: ActionRequest,
  opts: DispatchActionOptions = {},
): Promise<ActionResponse | null> {
  const url = `/api/admin/route-incidents/${encodeURIComponent(incidentId)}/${ACTION_SLUG[kind]}`
  const q = new URLSearchParams()
  if (opts.tenantId) q.set('tenant_id', opts.tenantId)
  if (opts.expectedVersion && opts.expectedVersion > 0) {
    q.set('expected_version', String(opts.expectedVersion))
  }
  const qs = q.toString()
  return postJSON<ActionResponse>(qs ? `${url}?${qs}` : url, body)
}

export function getAuditLog(
  incidentId: string,
  limit = 100,
  tenantId?: string,
): Promise<{ items: AuditLogEntry[]; count: number } | null> {
  const q = new URLSearchParams()
  if (tenantId) q.set('tenant_id', tenantId)
  q.set('limit', String(limit))
  return fetchJSON<{ items: AuditLogEntry[]; count: number }>(
    `/api/admin/route-incidents/${encodeURIComponent(incidentId)}/audit?${q.toString()}`,
  )
}

export function getDiagnosticRuns(
  incidentId: string,
  limit = 50,
  tenantId?: string,
): Promise<{ items: DiagnosticRun[]; count: number } | null> {
  const q = new URLSearchParams()
  if (tenantId) q.set('tenant_id', tenantId)
  q.set('limit', String(limit))
  return fetchJSON<{ items: DiagnosticRun[]; count: number }>(
    `/api/admin/route-incidents/${encodeURIComponent(incidentId)}/runs?${q.toString()}`,
  )
}

export function exportEvidence(
  incidentId: string,
  runId: string,
  reason: string,
  idempotencyKey: string,
  tenantId?: string,
): Promise<EvidenceExport | null> {
  const q = new URLSearchParams()
  q.set('run_id', runId)
  q.set('reason', reason)
  q.set('idempotency_key', idempotencyKey)
  if (tenantId) q.set('tenant_id', tenantId)
  return fetchJSON<EvidenceExport>(
    `/api/admin/route-incidents/${encodeURIComponent(incidentId)}/export?${q.toString()}`,
  )
}

// generateIdempotencyKey is a tiny client-side helper. The
// dashboard calls this when the operator clicks "Confirm" so a
// retried click never double-executes.
export function generateIdempotencyKey(prefix = 'ri'): string {
  // crypto.randomUUID is available in modern browsers; fall back
  // to a Math.random-based UUID-shaped string in test contexts
  // where crypto is shimmed but missing.
  const c = (globalThis as unknown as { crypto?: { randomUUID?: () => string } }).crypto
  if (c?.randomUUID) return `${prefix}-${c.randomUUID()}`
  const r = () => Math.random().toString(16).slice(2, 10)
  return `${prefix}-${Date.now().toString(16)}-${r()}${r()}`
}
