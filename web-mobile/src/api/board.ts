import { req } from './_core'

// board.ts — 总览数据（UI 规范 17 §2/§5），类型对齐桌面端 web/src/api/board.ts。

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
  discovery?: { running?: boolean; status?: string; heartbeat_at?: string }
  probe_loop?: { checks_last_10m?: number }
}

export interface BoardSelfcheck {
  total_runs_24h?: number
  success_rate?: number
  last_status?: string
  last_run_at?: string
}

export interface BoardPayload {
  summary: BoardSummary
  pies: { models: BoardPieItem[]; providers: BoardPieItem[]; errors: BoardPieItem[] }
  trends: BoardTrendPoint[]
  background_tasks?: BoardBackgroundTasks
  selfcheck?: BoardSelfcheck
  days: number
}

export function fetchBoard(days = 7, signal?: AbortSignal) {
  return req<BoardPayload>(
    'GET',
    `/api/admin/dashboard/board?days=${days}&include_operational=1`,
    undefined,
    signal,
  )
}
