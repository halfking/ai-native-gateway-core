import { req } from './_core'

// compression.ts — v6.0 audit T12 (2026-06-22)
// Compression v7 (Round 47) stats endpoints. The compressor rewrites
// outbound request bodies when conversations get long; this surfaces
// the "how much did we save?" + "which strategy was used per session?"
// signals.
//
// Two endpoints: /stats (hourly aggregates) and /sessions (per-session
// detail). Both live under /api/admin/compression/... and are admin-only.

export interface HourBucket {
  hour: string
  total: number
  compressed: number
  rate: number
}

export interface CompressionStats {
  total_requests: number
  compressed_total: number
  compression_rate: number
  strategy_distribution: Record<string, number>
  total_outbound_tokens?: number
  estimated_original_tokens?: number
  estimated_tokens_saved?: number
  hourly_series: HourBucket[]
}

export function getCompressionStats(params: { hours?: number; from?: string; to?: string } = {}) {
  const qs = new URLSearchParams()
  if (params.hours) qs.set('hours', String(params.hours))
  if (params.from) qs.set('from', params.from)
  if (params.to) qs.set('to', params.to)
  const s = qs.toString()
  return req<CompressionStats>('GET', `/api/admin/compression/stats${s ? '?' + s : ''}`)
}

export interface CompressionSessionItem {
  gw_session_id: string
  compression_strategy: string
  request_count: number
  first_ts: string
  last_ts: string
  outbound_msg_count: number | null
  outbound_token_est: number | null
  estimated_original_msgs: number | null
  msg_reduction: number | null
  sample_request_id: string
}

export interface CompressionSessionsResponse {
  items: CompressionSessionItem[]
  count: number
}

export function getCompressionSessions(params: {
  hours?: number
  from?: string
  to?: string
  strategy?: string
  page?: number
  page_size?: number
} = {}) {
  const qs = new URLSearchParams()
  if (params.hours) qs.set('hours', String(params.hours))
  if (params.from) qs.set('from', params.from)
  if (params.to) qs.set('to', params.to)
  if (params.strategy) qs.set('strategy', params.strategy)
  if (params.page) qs.set('page', String(params.page))
  if (params.page_size) qs.set('page_size', String(params.page_size))
  const s = qs.toString()
  return req<CompressionSessionsResponse>('GET', `/api/admin/compression/sessions${s ? '?' + s : ''}`)
}