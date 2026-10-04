import { req, type RequestOptions } from './client'

// board.ts — /api/admin/dashboard/board（形状对齐 web/src/api/board.ts，字段裁剪到移动端渲染面）。

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
  /** 汇总整体降级：首屏所有汇总数字不可作为结论（web/board.ts 同源语义）。 */
  degraded_summary?: boolean
  summary_hint?: string
  credits_missing_view?: string
  credits_hint?: string
}

export interface BoardPieItem {
  key: string
  requests: number
  tokens: number
  credits?: number
  cost_usd: number
}

export interface BoardTrendPoint {
  bucket: string
  requests: number
  tokens: number
  cost_usd: number
}

export interface BoardBackgroundTasks {
  discovery?: { running?: boolean; heartbeat_at?: string }
  probe_loop?: { running?: boolean; checks_last_10m?: number }
  degraded?: boolean
}

export interface BoardPayload {
  summary?: BoardSummary
  pies?: {
    models?: BoardPieItem[]
    clients?: BoardPieItem[]
    providers?: BoardPieItem[]
    errors?: BoardPieItem[]
  }
  trends?: BoardTrendPoint[]
  background_tasks?: BoardBackgroundTasks
  degraded?: boolean
}

export function fetchBoard(days = 7, options?: RequestOptions): Promise<BoardPayload> {
  return req<BoardPayload>('GET', `/api/admin/dashboard/board?days=${days}&include_operational=1`, undefined, options)
}
