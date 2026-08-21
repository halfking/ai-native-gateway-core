/**
 * Pure helpers for QueueWaterfallTimeline — synthesise absolute stage
 * windows from ms fields when RFC3339 timestamps are missing.
 */
export type StageKey = 'total' | 'model' | 'cred' | 'upstream' | 'stream'

export interface StageWindow {
  key: StageKey
  label: string
  color: string
  startMs: number
  endMs: number
  durationMs: number
}

export interface WaterfallTimingInput {
  arrived_at?: string
  total_enqueued_at?: string
  total_dequeued_at?: string
  model_enqueued_at?: string
  model_dequeued_at?: string
  cred_enqueued_at?: string
  cred_dequeued_at?: string
  forward_start_at?: string
  response_start_at?: string
  response_end_at?: string
  waiting_in_total_ms?: number
  waiting_in_model_ms?: number
  waiting_in_node_ms?: number
  upstream_latency_ms?: number
  streaming_duration_ms?: number
  total_ms?: number
}

export const WATERFALL_STAGES = [
  { key: 'total' as const, start: 'total_enqueued_at', end: 'total_dequeued_at', color: '#409EFF', label: '总队列', msKey: 'waiting_in_total_ms' as const },
  { key: 'model' as const, start: 'model_enqueued_at', end: 'model_dequeued_at', color: '#67C23A', label: '模型队列', msKey: 'waiting_in_model_ms' as const },
  { key: 'cred' as const, start: 'cred_enqueued_at', end: 'cred_dequeued_at', color: '#E6A23C', label: '凭据队列', msKey: 'waiting_in_node_ms' as const },
  { key: 'upstream' as const, start: 'forward_start_at', end: 'response_start_at', color: '#F56C6C', label: '上游TTFB', msKey: 'upstream_latency_ms' as const },
  { key: 'stream' as const, start: 'response_start_at', end: 'response_end_at', color: '#909399', label: '流式传输', msKey: 'streaming_duration_ms' as const },
]

function parseTS(s?: string): number | null {
  if (!s) return null
  const t = Date.parse(s)
  return Number.isFinite(t) ? t : null
}

function numMS(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) ? Math.max(0, v) : 0
}

/**
 * Build stage windows for one request. Prefer real timestamps; fall back to
 * chaining ms durations from arrived_at (or Date.now()).
 */
export function buildStageWindows(r: WaterfallTimingInput, nowMs = Date.now()): StageWindow[] {
  const windows: StageWindow[] = []
  for (const st of WATERFALL_STAGES) {
    const start = parseTS((r as Record<string, unknown>)[st.start] as string | undefined)
    const end = parseTS((r as Record<string, unknown>)[st.end] as string | undefined)
    if (start != null && end != null && end >= start) {
      windows.push({
        key: st.key,
        label: st.label,
        color: st.color,
        startMs: start,
        endMs: end,
        durationMs: numMS((r as Record<string, unknown>)[st.msKey]) || end - start,
      })
    }
  }
  if (windows.length > 0) return windows

  // Synthesise from ms chain when timestamps are absent.
  let cursor = parseTS(r.arrived_at) ?? nowMs
  const chain: Array<{ key: StageKey; label: string; color: string; ms: number }> = [
    { key: 'total', label: '总队列', color: '#409EFF', ms: numMS(r.waiting_in_total_ms) },
    { key: 'model', label: '模型队列', color: '#67C23A', ms: numMS(r.waiting_in_model_ms) },
    { key: 'cred', label: '凭据队列', color: '#E6A23C', ms: numMS(r.waiting_in_node_ms) },
    { key: 'upstream', label: '上游TTFB', color: '#F56C6C', ms: numMS(r.upstream_latency_ms) },
    { key: 'stream', label: '流式传输', color: '#909399', ms: numMS(r.streaming_duration_ms) },
  ]
  const sum = chain.reduce((s, c) => s + c.ms, 0)
  if (sum <= 0 && numMS(r.total_ms) <= 0) return []
  if (sum <= 0) {
    // Single bar for total_ms when stage ms missing.
    return [{
      key: 'stream',
      label: '总耗时',
      color: '#909399',
      startMs: cursor,
      endMs: cursor + numMS(r.total_ms),
      durationMs: numMS(r.total_ms),
    }]
  }
  for (const c of chain) {
    if (c.ms <= 0) continue
    const startMs = cursor
    const endMs = cursor + c.ms
    windows.push({ key: c.key, label: c.label, color: c.color, startMs, endMs, durationMs: c.ms })
    cursor = endMs
  }
  return windows
}
