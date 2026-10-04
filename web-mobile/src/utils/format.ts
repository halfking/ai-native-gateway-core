import { t } from '@/i18n'

// 展示层数字格式化（金额/Token tabular-nums 口径由样式配合）。

export function fmtInt(v: number | null | undefined): string {
  if (v == null || Number.isNaN(v)) return '—'
  return new Intl.NumberFormat('en-US').format(Math.round(v))
}

export function fmtNum(v: number | null | undefined, digits = 2): string {
  if (v == null || Number.isNaN(v)) return '—'
  return new Intl.NumberFormat('en-US', { maximumFractionDigits: digits, minimumFractionDigits: digits }).format(v)
}

export function fmtUsd(v: number | null | undefined): string {
  if (v == null || Number.isNaN(v)) return '—'
  if (Math.abs(v) >= 1000) return `$${fmtNum(v, 0)}`
  if (Math.abs(v) >= 1) return `$${fmtNum(v, 2)}`
  return `$${fmtNum(v, 4)}`
}

export function fmtCompact(v: number | null | undefined): string {
  if (v == null || Number.isNaN(v)) return '—'
  return new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 }).format(v)
}

/** 相对时间（zh：n 秒/分钟/小时/天前）。 */
export function relativeTime(iso: string | null | undefined): string {
  if (!iso) return t('common.never')
  const ts = Date.parse(iso)
  if (Number.isNaN(ts)) return iso
  const diffSec = Math.max(0, Math.round((Date.now() - ts) / 1000))
  if (diffSec < 60) return t('common.secondsAgo', { n: diffSec })
  const diffMin = Math.round(diffSec / 60)
  if (diffMin < 60) return t('common.minutesAgo', { n: diffMin })
  const diffHour = Math.round(diffMin / 60)
  if (diffHour < 24) return t('common.hoursAgo', { n: diffHour })
  return t('common.daysAgo', { n: Math.round(diffHour / 24) })
}

export function fmtTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  const ts = Date.parse(iso)
  if (Number.isNaN(ts)) return iso
  const d = new Date(ts)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}
