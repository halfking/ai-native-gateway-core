/** Shared format helpers for NodeDetailDrawer panels. */

export function fmtTime(value: string | number | null | undefined): string {
  if (value == null || value === '') return '—'
  const date = new Date(typeof value === 'number' ? value : value)
  return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString()
}

export function pct(value: number | null | undefined): string {
  return value == null ? '—' : `${(value * 100).toFixed(1)}%`
}

export function statusClass(value: string | null | undefined): string {
  if (['ready', 'healthy', 'active', 'closed', 'ok', 'available'].includes(value || '')) return 'is-ok'
  if (['cooling', 'half_open', 'low', 'warning', 'degraded'].includes(value || '')) return 'is-warn'
  return 'is-bad'
}
