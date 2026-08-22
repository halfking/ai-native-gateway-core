/**
 * Relative T0–T9 dispatch waterfall: sequential gaps, not overlapping routing.
 * Prefer timestamps; otherwise chain dedicated ms from arrived_at.
 */
import type { WaterfallRequest } from '../api/dispatch'

export type WaterfallStageKey =
  | 'arrive' | 'total' | 'admit' | 'model' | 'select'
  | 'cred' | 'acquire' | 'upstream' | 'stream'

export interface WaterfallStageDef {
  key: WaterfallStageKey
  start: keyof WaterfallRequest
  end: keyof WaterfallRequest
  color: string
  label: string
  msKey?: keyof WaterfallRequest
}

/** Sequential T0–T9 gaps. routing_ms (T2–T5) is omitted — it overlaps model queue. */
/** Shared grid columns: identity · result · track · total */
export const WATERFALL_GRID_COLUMNS =
  'minmax(140px, 200px) 78px minmax(240px, 1fr) 72px'

export const QUEUE_STAGE_KEYS: WaterfallStageKey[] = [
  'arrive', 'total', 'admit', 'model', 'select', 'cred',
]

export const EXEC_STAGE_KEYS: WaterfallStageKey[] = ['acquire', 'upstream', 'stream']

export const WATERFALL_STAGES: WaterfallStageDef[] = [
  { key: 'arrive', start: 'arrived_at', end: 'total_enqueued_at', color: '#94a3b8', label: '到达' },
  { key: 'total', start: 'total_enqueued_at', end: 'total_dequeued_at', color: '#409EFF', label: '总队列', msKey: 'waiting_in_total_ms' },
  { key: 'admit', start: 'total_dequeued_at', end: 'model_enqueued_at', color: '#7dd3fc', label: '入模' },
  { key: 'model', start: 'model_enqueued_at', end: 'model_dequeued_at', color: '#67C23A', label: '模型队列', msKey: 'waiting_in_model_ms' },
  { key: 'select', start: 'model_dequeued_at', end: 'cred_enqueued_at', color: '#a78bfa', label: '选凭据' },
  { key: 'cred', start: 'cred_enqueued_at', end: 'cred_dequeued_at', color: '#E6A23C', label: '凭据队列', msKey: 'waiting_in_node_ms' },
  { key: 'acquire', start: 'cred_dequeued_at', end: 'forward_start_at', color: '#2dd4bf', label: '获取', msKey: 'acquire_ms' },
  { key: 'upstream', start: 'forward_start_at', end: 'response_start_at', color: '#F56C6C', label: '上游TTFB', msKey: 'upstream_latency_ms' },
  { key: 'stream', start: 'response_start_at', end: 'response_end_at', color: '#64748b', label: '流式', msKey: 'streaming_duration_ms' },
]

export function parseTS(s?: string): number | null {
  if (!s) return null
  const t = Date.parse(s)
  return Number.isFinite(t) ? t : null
}

function fieldMS(r: WaterfallRequest, key?: keyof WaterfallRequest): number {
  if (!key) return 0
  const v = r[key]
  return typeof v === 'number' && Number.isFinite(v) ? Math.max(0, v) : 0
}

function strField(r: WaterfallRequest, key: keyof WaterfallRequest): string | undefined {
  const v = r[key]
  return typeof v === 'string' ? v : undefined
}

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

export interface LaidOutBar extends StageBar {
  leftPct: number
  widthPct: number
}

export interface LaidOutRow {
  request: WaterfallRequest
  bars: LaidOutBar[]
  origin: number
  spanMs: number
}

export function resolveStageBars(r: WaterfallRequest, now = Date.now()): StageBar[] {
  const bars: StageBar[] = []
  let cursor = waterfallAnchorMs(r, now)

  for (const st of WATERFALL_STAGES) {
    const startTS = parseTS(strField(r, st.start))
    const endTS = parseTS(strField(r, st.end))
    const ms = fieldMS(r, st.msKey)

    if (startTS != null && endTS != null && endTS > startTS) {
      bars.push({
        key: st.key, label: st.label, color: st.color,
        start: startTS, end: endTS,
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
      key: st.key, label: st.label, color: st.color,
      start, end, ms, synthesized: true,
    })
    cursor = end
  }
  return bars
}

export function rowSpanMs(bars: StageBar[], origin: number, totalMs: number): number {
  const fromBars = bars.length ? Math.max(...bars.map((b) => b.end)) - origin : 0
  return Math.max(fromBars, totalMs, 0)
}

export function layoutBar(bar: StageBar, origin: number, axisMax: number): LaidOutBar {
  const span = Math.max(axisMax, 1)
  let leftPct = ((bar.start - origin) / span) * 100
  let widthPct = (bar.ms / span) * 100
  if (bar.ms > 0 && widthPct < 0.4) widthPct = 0.4
  if (leftPct < 0) leftPct = 0
  if (leftPct > 100) leftPct = 100
  if (leftPct + widthPct > 100) widthPct = Math.max(0, 100 - leftPct)
  return { ...bar, leftPct, widthPct }
}

export function layoutRows(requests: WaterfallRequest[], now = Date.now()): {
  rows: LaidOutRow[]
  axisMax: number
} {
  const prepared = requests.map((request) => {
    const bars = resolveStageBars(request, now)
    const origin = waterfallAnchorMs(request, now)
    const spanMs = rowSpanMs(bars, origin, request.total_ms)
    return { request, bars, origin, spanMs }
  })
  const axisMax = Math.max(1, ...prepared.map((p) => p.spanMs))
  return {
    axisMax,
    rows: prepared.map((p) => ({
      ...p,
      bars: p.bars.map((b) => layoutBar(b, p.origin, axisMax)),
    })),
  }
}

function median(nums: number[]): number {
  if (!nums.length) return 0
  const s = [...nums].sort((a, b) => a - b)
  const mid = Math.floor(s.length / 2)
  return s.length % 2 ? s[mid] : (s[mid - 1] + s[mid]) / 2
}

export interface CompositionSlice {
  key: WaterfallStageKey
  label: string
  color: string
  ms: number
  pct: number
}

export function medianComposition(rows: StageBar[][]): CompositionSlice[] {
  const slices: CompositionSlice[] = []
  for (const st of WATERFALL_STAGES) {
    const values = rows
      .map((bars) => bars.find((b) => b.key === st.key)?.ms)
      .filter((n): n is number => typeof n === 'number' && n > 0)
    const ms = median(values)
    if (ms > 0) slices.push({ key: st.key, label: st.label, color: st.color, ms, pct: 0 })
  }
  const sum = slices.reduce((s, x) => s + x.ms, 0)
  if (sum <= 0) return []
  return slices.map((x) => ({ ...x, pct: (x.ms / sum) * 100 }))
}

export function formatAxisMs(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`
  const s = ms / 1000
  return Number.isInteger(s) ? `${s}s` : `${s.toFixed(1)}s`
}

export interface StageDetailRow {
  key: WaterfallStageKey | 'routing'
  label: string
  color: string
  ms: number
  synthesized: boolean
  /** false for routing_ms — number only, no bar */
  showBar: boolean
}

export function stageDetailRows(r: WaterfallRequest, now = Date.now()): StageDetailRow[] {
  const bars = resolveStageBars(r, now)
  const rows: StageDetailRow[] = WATERFALL_STAGES.map((st) => {
    const bar = bars.find((b) => b.key === st.key)
    const ms = bar?.ms ?? fieldMS(r, st.msKey)
    return {
      key: st.key,
      label: st.label,
      color: st.color,
      ms,
      synthesized: bar?.synthesized ?? (ms > 0 && bar == null),
      showBar: true,
    }
  })
  rows.push({
    key: 'routing',
    label: 'routing',
    color: 'transparent',
    ms: fieldMS(r, 'routing_ms'),
    synthesized: false,
    showBar: false,
  })
  return rows
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
