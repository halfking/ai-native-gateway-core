// format.ts — reconciliation display helpers (no Vue, unit-tested).

export type QuickRange = 'yesterday' | '7d' | '30d' | 'month'

export function fmtDay(d: Date): string {
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${d.getFullYear()}-${m}-${day}`
}

/** T+1 window: end is yesterday. Inclusive day counts match the toolbar chips. */
export function quickRange(kind: QuickRange, now = new Date()): [string, string] {
  const end = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1)
  const start = new Date(end)
  if (kind === '7d') start.setDate(end.getDate() - 6)
  else if (kind === '30d') start.setDate(end.getDate() - 29)
  else if (kind === 'month') {
    start.setDate(1)
    if (start > end) start.setMonth(start.getMonth() - 1)
  }
  return [fmtDay(start), fmtDay(end)]
}

export function matchQuick(range: [string, string], now = new Date()): QuickRange | '' {
  const kinds: QuickRange[] = ['yesterday', '7d', '30d', 'month']
  for (const kind of kinds) {
    const [start, end] = quickRange(kind, now)
    if (start === range[0] && end === range[1]) return kind
  }
  return ''
}

export function fmtInt(n: number | null | undefined): string {
  if (n == null || Number.isNaN(n)) return '—'
  return Math.round(n).toLocaleString('en-US')
}

export function fmtPct(ratio: number | null | undefined): string {
  if (ratio == null || Number.isNaN(ratio)) return '—'
  return `${(ratio * 100).toFixed(2)}%`
}

function trimNum(n: number): string {
  return n.toFixed(2).replace(/\.?0+$/, '')
}

export function fmtCompact(n: number | null | undefined): string {
  if (n == null || Number.isNaN(n)) return '—'
  const sign = n < 0 ? '-' : ''
  const abs = Math.abs(n)
  if (abs >= 1e9) return `${sign}${trimNum(abs / 1e9)}B`
  if (abs >= 1e6) return `${sign}${trimNum(abs / 1e6)}M`
  if (abs >= 1e3) return `${sign}${trimNum(abs / 1e3)}K`
  return Math.round(n).toLocaleString('en-US')
}

export function fmtUsd(cents: number | null | undefined): string {
  if (cents == null || Number.isNaN(cents)) return '—'
  return `$${(cents / 100).toFixed(2)}`
}

export function fmtCny(cents: number | null | undefined): string {
  if (cents == null || Number.isNaN(cents)) return '—'
  return `¥${(cents / 100).toFixed(2)}`
}

export function fmtDuration(ms: number | null | undefined): string {
  if (ms == null || Number.isNaN(ms)) return '—'
  if (ms < 1000) return `${Math.round(ms)}ms`
  const seconds = ms / 1000
  return seconds >= 10 ? `${seconds.toFixed(1)}s` : `${seconds.toFixed(2)}s`
}

export function failTone(errorRate: number | null | undefined): 'ok' | 'warn' | 'bad' {
  const pct = (errorRate ?? 0) * 100
  if (pct > 10) return 'bad'
  if (pct > 7) return 'warn'
  return 'ok'
}

export function weightedQuality(
  rows: { quality_score?: number; totals: { request_count: number } }[],
): number | null {
  let weight = 0
  let sum = 0
  for (const row of rows) {
    if (row.quality_score == null || !(row.totals.request_count > 0)) continue
    weight += row.totals.request_count
    sum += row.quality_score * row.totals.request_count
  }
  return weight > 0 ? sum / weight : null
}

export function topReasons(
  breakdown: Record<string, number> | undefined,
  limit = 8,
): { code: string; count: number }[] {
  return Object.entries(breakdown ?? {})
    .filter(([, count]) => count > 0)
    .sort((a, b) => (b[1] === a[1] ? a[0].localeCompare(b[0]) : b[1] - a[1]))
    .slice(0, limit)
    .map(([code, count]) => ({ code, count }))
}
