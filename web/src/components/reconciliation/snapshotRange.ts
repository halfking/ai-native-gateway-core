// snapshotRange.ts — reconciliation date presets on the shared range picker.
// The panel matches the dashboard picker. Presets stop at yesterday UTC because
// daily snapshots are not closed for the current day.
import type { KxDateRange, KxDateRangePreset } from '../ui/kx-date-types'

const DAY_MS = 86_400_000

function utcDayStart(ms: number): number {
  const d = new Date(ms)
  return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate())
}

function utcDateStr(ms: number): string {
  return new Date(ms).toISOString().slice(0, 10)
}

export function snapshotDatePresets(now = Date.now()): KxDateRangePreset[] {
  const end = utcDayStart(now) - DAY_MS
  const endStr = utcDateStr(end)
  const span = (days: number): KxDateRange => ({
    start: utcDateStr(end - (days - 1) * DAY_MS),
    end: endStr,
  })
  const endDate = new Date(end)
  const monthStart = Date.UTC(endDate.getUTCFullYear(), endDate.getUTCMonth(), 1)
  const prevStart = Date.UTC(endDate.getUTCFullYear(), endDate.getUTCMonth() - 1, 1)
  const prevEnd = Date.UTC(endDate.getUTCFullYear(), endDate.getUTCMonth(), 0)
  return [
    {
      id: 'yesterday',
      labelKey: 'common.dateRange.preset.yesterday',
      resolve: () => ({ start: endStr, end: endStr }),
    },
    { id: 'last7d', labelKey: 'common.dateRange.preset.last7d', resolve: () => span(7) },
    { id: 'last30d', labelKey: 'common.dateRange.preset.last30d', resolve: () => span(30) },
    {
      id: 'thisMonth',
      labelKey: 'common.dateRange.preset.thisMonth',
      resolve: () => ({ start: utcDateStr(monthStart), end: endStr }),
    },
    {
      id: 'lastMonth',
      labelKey: 'common.dateRange.preset.lastMonth',
      resolve: () => ({ start: utcDateStr(prevStart), end: utcDateStr(prevEnd) }),
    },
  ]
}

export function defaultSnapshotRange(now = Date.now()): [string, string] {
  const range = snapshotDatePresets(now).find((item) => item.id === 'last7d')?.resolve()
  return range ? [range.start, range.end] : ['', '']
}

/** Inclusive latest day a reconciliation query may ask for: UTC yesterday. */
export function snapshotNotAfter(now = Date.now()): string {
  return utcDateStr(utcDayStart(now) - DAY_MS)
}
