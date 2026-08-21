/**
 * Pure helpers for dispatch waterfall chart bars.
 * When stage timestamps are missing, synthesize from arrived_at + duration ms.
 */
import type { WaterfallRequest } from '../api/dispatch'

export type WaterfallStageKey = 'total' | 'model' | 'cred' | 'upstream' | 'stream'

export interface WaterfallStageDef {
  key: WaterfallStageKey
  start: keyof WaterfallRequest | string
  end: keyof WaterfallRequest | string
  color: string
  label: string
  msKey: keyof WaterfallRequest | string
}

export const WATERFALL_STAGES: WaterfallStageDef[] = [
  { key: 'total', start: 'total_enqueued_at', end: 'total_dequeued_at', color: '#409EFF', label: '总队列', msKey: 'waiting_in_total_ms' },
  { key: 'model', start: 'model_enqueued_at', end: 'model_dequeued_at', color: '#67C23A', label: '模型队列', msKey: 'waiting_in_model_ms' },
  { key: 'cred', start: 'cred_enqueued_at', end: 'cred_dequeued_at', color: '#E6A23C', label: '凭据队列', msKey: 'waiting_in_node_ms' },
  { key: 'upstream', start: 'forward_start_at', end: 'response_start_at', color: '#F56C6C', label: '上游TTFB', msKey: 'upstream_latency_ms' },
  { key: 'stream', start: 'response_start_at', end: 'response_end_at', color: '#909399', label: '流式传输', msKey: 'streaming_duration_ms' },
]

export function parseTS(s?: string): number | null {
  if (!s) return null
  const t = Date.parse(s)
  return Number.isFinite(t) ? t : null
}

function fieldMS(r: WaterfallRequest, key: string): number {
  const v = (r as unknown as Record<string, unknown>)[key]
  return typeof v === 'number' && Number.isFinite(v) ? Math.max(0, v) : 0
}

/** Anchor for synthetic timeline: arrived_at, else first known TS, else now. */
export function waterfallAnchorMs(r: WaterfallRequest, fallbackNow = Date.now()): number {
  return (
    parseTS(r.arrived_at) ??
    parseTS(r.total_enqueued_at) ??
    parseTS(r.forward_start_at) ??
    parseTS(r.response_end_at) ??
    fallbackNow
  )
}

export interface StageBar {
  key: WaterfallStageKey
  label: string
  color: string
  start: number
  end: number
  ms: number
  synthesized: boolean
}

/**
 * Resolve absolute [start,end] for each stage. Prefer real timestamps;
 * otherwise place contiguous bars from the anchor using duration ms.
 */
export function resolveStageBars(r: WaterfallRequest, now = Date.now()): StageBar[] {
  const bars: StageBar[] = []
  let cursor = waterfallAnchorMs(r, now)

  for (const st of WATERFALL_STAGES) {
    const startTS = parseTS((r as unknown as Record<string, string | undefined>)[st.start as string])
    const endTS = parseTS((r as unknown as Record<string, string | undefined>)[st.end as string])
    const ms = fieldMS(r, st.msKey as string)

    if (startTS != null && endTS != null && endTS >= startTS) {
      bars.push({
        key: st.key,
        label: st.label,
        color: st.color,
        start: startTS,
        end: endTS,
        ms: ms > 0 ? ms : endTS - startTS,
        synthesized: false,
      })
      cursor = Math.max(cursor, endTS)
      continue
    }

    if (ms <= 0) continue
    const start = cursor
    const end = start + ms
    bars.push({
      key: st.key,
      label: st.label,
      color: st.color,
      start,
      end,
      ms,
      synthesized: true,
    })
    cursor = end
  }
  return bars
}

export function emptyStateMessage(opts: {
  wired?: boolean
  source?: string
  count: number
}): string {
  if (opts.wired === false) {
    return '调度 projection 未接线（wired=false），无法展示实时队列样本'
  }
  if (opts.count > 0) return ''
  if (opts.source === 'none' || !opts.source) {
    return '暂无样本：内存 ring 为空，且 request_logs_hot 尚无 t0_arrived_at 数据'
  }
  return '暂无已完成请求的时间戳样本'
}
