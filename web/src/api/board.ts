import { req } from './_core'

export interface BoardPieItem {
  key: string
  requests: number
  tokens: number
  credits: number
  cost_usd: number
}

export interface BoardTrendPoint {
  bucket: string
  requests: number
  tokens: number
  credits: number
  cost_usd: number
}

export interface BoardSummary {
  total_requests?: number
  total_prompt_tokens?: number
  total_completion_tokens?: number
  total_tokens?: number
  total_cost_usd?: number
  total_credits_charged?: number
  success_rate?: number
  avg_latency_ms?: number
  active_api_keys?: number
  active_models?: number
  providers?: number
}

export interface BoardBackgroundTasks {
  discovery?: { running?: boolean; status?: string; trigger?: string; started_at?: string; heartbeat_at?: string }
  probe_loop?: { checks_last_10m?: number }
}

export interface BoardSelfcheck {
  total_runs_24h?: number
  success_rate?: number
  last_status?: string
  last_run_at?: string
}

export interface BoardOperationalPayload {
  background_tasks?: BoardBackgroundTasks
  selfcheck?: BoardSelfcheck
}

export interface BodySizeStats {
  avg_request_bytes?: number
  max_request_bytes?: number
  avg_response_bytes?: number
  max_response_bytes?: number
}

export interface BoardPayload {
  summary: BoardSummary
  pies: {
    clients: BoardPieItem[]
    virtual_ips: BoardPieItem[]
    identity_hashes: BoardPieItem[]
    models: BoardPieItem[]
    errors: BoardPieItem[]
    tenants: BoardPieItem[]
    providers: BoardPieItem[]
  }
  trends: BoardTrendPoint[]
  background_tasks?: BoardBackgroundTasks
  selfcheck?: BoardSelfcheck
  operational?: BoardOperationalPayload
  body_stats?: BodySizeStats
  days: number
  source?: 'postgresql_baseline' | 'redis_baseline_delta' | 'live_sse_delta' | string
  cache_meta?: {
    fold_unit?: string
    scope?: string
    built_at?: string
    source?: string
  }
}

export interface BoardQuery {
  days?: number
  start?: string
  end?: string
  tenant_id?: string
  provider_id?: number
}

export function fetchDashboardBoard(params: BoardQuery = {}, signal?: AbortSignal): Promise<BoardPayload> {
  const qs = new URLSearchParams()
  if (params.start && params.end) {
    qs.set('start', params.start)
    qs.set('end', params.end)
  } else if (params.days != null) {
    qs.set('days', String(params.days))
  }
  if (params.tenant_id) qs.set('tenant_id', params.tenant_id)
  if (params.provider_id != null) qs.set('provider_id', String(params.provider_id))
  const q = qs.toString()
  const suffix = q ? `?${q}&include_operational=1` : '?include_operational=1'
  return req<BoardPayload>('GET', `/api/admin/dashboard/board${suffix}`, undefined, { signal })
}

export function fetchBoardOperational(): Promise<BoardOperationalPayload> {
  return req<BoardOperationalPayload>('GET', '/api/admin/dashboard/operational')
}

export function fetchBoardErrorDrill(params: {
  error_kind: string
  days?: number
  dimension?: 'model' | 'provider' | 'client'
  tenant_id?: string
}): Promise<{ error_kind: string; dimension: string; items: BoardPieItem[] }> {
  const qs = new URLSearchParams()
  qs.set('error_kind', params.error_kind)
  if (params.days != null) qs.set('days', String(params.days))
  if (params.dimension) qs.set('dimension', params.dimension)
  if (params.tenant_id) qs.set('tenant_id', params.tenant_id)
  return req('GET', `/api/admin/dashboard/board/error-drill?${qs}`)
}
