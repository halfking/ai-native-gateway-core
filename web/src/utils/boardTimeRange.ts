export type BoardPeriodPreset = 'today' | '7d' | '30d' | 'custom'

export interface BoardTimeRange {
  preset: BoardPeriodPreset
  days: number
  start?: string
  end?: string
}

export interface BoardTimeQuery {
  days?: number
  start?: string
  end?: string
}

export function defaultBoardTimeRange(): BoardTimeRange {
  return { preset: 'today', days: 1 }
}

export function presetToDays(preset: BoardPeriodPreset): number {
  switch (preset) {
    case 'today': return 1
    case '7d': return 7
    case '30d': return 30
    default: return 1
  }
}

export function boardRangeIncludesToday(range: BoardTimeRange): boolean {
  if (range.preset !== 'custom') return true
  if (!range.start || !range.end) return false
  const today = new Date().toISOString().slice(0, 10)
  return range.end >= today
}

export function toBoardTimeQuery(range: BoardTimeRange): BoardTimeQuery {
  if (range.preset === 'custom' && range.start && range.end) {
    return { start: range.start, end: range.end, days: range.days }
  }
  return { days: range.days }
}

export function utcRollingStartMs(days: number, endMs = Date.now()): number {
  const s = new Date(endMs - days * 86_400_000)
  s.setUTCHours(0, 0, 0, 0)
  return s.getTime()
}

export function resolveBoardRangeMs(range: BoardTimeRange, endMs = Date.now()): { startMs: number; endMs: number } {
  if (range.preset === 'custom' && range.start && range.end) {
    return {
      startMs: Date.parse(`${range.start}T00:00:00Z`),
      endMs: Date.parse(`${range.end}T00:00:00Z`) + 86_400_000,
    }
  }
  return { startMs: utcRollingStartMs(range.days || 1, endMs), endMs }
}

export function isWithinBoardRange(ts: string | undefined, range: BoardTimeRange): boolean {
  if (!ts) return false
  const when = Date.parse(ts)
  if (Number.isNaN(when)) return false
  const { startMs, endMs } = resolveBoardRangeMs(range)
  if (range.preset === 'custom' && range.start && range.end) {
    return when >= startMs && when < endMs
  }
  return when >= startMs && when <= endMs
}

export function formatBoardRangeLabel(range: BoardTimeRange, t: (key: string, params?: Record<string, unknown>) => string): string {
  if (range.preset === 'custom' && range.start && range.end) {
    return t('dashboard.board.rangeCustom', { start: range.start, end: range.end })
  }
  switch (range.preset) {
    case 'today': return t('dashboard.range.today')
    case '7d': return t('dashboard.range.last7d')
    case '30d': return t('dashboard.range.last30d')
    default: return t('dashboard.range.today')
  }
}
