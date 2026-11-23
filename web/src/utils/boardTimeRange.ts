export type BoardPeriodPreset = 'today' | '7d' | '30d' | 'custom'

// ★ 时区轮（2026-11-20）：本模块全部日切从 UTC 改为**系统时区**
// （settings.system.timezone，默认 Asia/Shanghai）——与后端
// boardPresetTimeRange / resolveUsageTimeRange 同口径。看板「今天」=
// 系统时区日历日；UTC 回退值仅在后端未响应时生效（fetchSystemTimezone
// 失败静默，浏览器本地时区兜底）。测试统一注入固定时区后断言。

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
  /** 2026-11-20 口径对齐轮：true → 英雄区汇总切供应商对账口径（含探测）。 */
  include_probes?: boolean
}

import { systemTzDateStr, systemTzDayStartMs, systemTzNoonMs } from '@/components/ui/kxDatePresets'
import type { KxDateRange } from '@/components/ui/kx-date-types'

const DAY_MS = 86_400_000

/**
 * BoardTimeRange → KxDateRange（控件展示值）。
 *
 * ★ 2026-11-23 日期范围对齐轮：看板页与「会话与统计」页共用这一对映射，
 *   两页的日期范围控件因此**形态与取值完全一致**（同一套预设 + 同一份状态）。
 */
export function boardRangeToKxRange(range: BoardTimeRange): KxDateRange {
  const { startMs, endMs } = resolveBoardRangeMs(range)
  return { start: systemTzDateStr(startMs), end: systemTzDateStr(endMs - 1) }
}

/**
 * KxDateRange（控件选值）→ BoardTimeRange（共享状态）。
 *
 * 能对齐内置预设的按预设归位（保留 SSE 门控与偏好语义），其余一律 custom
 * {start,end}（走既有 start/end 查询通道）——与看板既有行为逐字一致。
 */
export function boardRangeFromKxRange(range: KxDateRange): BoardTimeRange {
  const todayMs = resolveBoardRangeMs({ preset: 'today', days: 1 }).startMs
  const spanDays = Math.max(
    1,
    Math.round((Date.parse(`${range.end}T00:00:00Z`) - Date.parse(`${range.start}T00:00:00Z`)) / DAY_MS) + 1,
  )
  if (range.start === systemTzDateStr(todayMs) && range.end === systemTzDateStr(todayMs)) {
    return { preset: 'today', days: 1 }
  }
  if (range.start === systemTzDateStr(todayMs - 6 * DAY_MS) && range.end === systemTzDateStr(todayMs)) {
    return { preset: '7d', days: 7 }
  }
  if (range.start === systemTzDateStr(todayMs - 29 * DAY_MS) && range.end === systemTzDateStr(todayMs)) {
    return { preset: '30d', days: 30 }
  }
  return { preset: 'custom', days: spanDays, start: range.start, end: range.end }
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
  const today = systemTzDateStr()
  return range.end >= today
}

/** Chart bucket size aligned with backend: today=5m, 7d=15m, 30d+=1h. */
export function boardTrendBucketMinutes(range: BoardTimeRange): number {
  if (range.preset === 'custom' && range.start && range.end) {
    const { startMs, endMs } = resolveBoardRangeMs(range)
    const spanMs = endMs - startMs
    if (spanMs <= 48 * 86_400_000) return 5
    if (spanMs <= 14 * 86_400_000) return 15
    return 60
  }
  if (range.days <= 1) return 5
  if (range.days <= 7) return 15
  return 60
}

export function alignToTrendBucket(ts: string | Date, range: BoardTimeRange): string {
  const d = ts instanceof Date ? ts : new Date(ts)
  if (Number.isNaN(d.getTime())) return new Date().toISOString()
  const mins = boardTrendBucketMinutes(range)
  const utc = new Date(Date.UTC(
    d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate(),
    d.getUTCHours(), d.getUTCMinutes(), 0, 0,
  ))
  if (mins >= 60) {
    utc.setUTCMinutes(0, 0, 0)
    return utc.toISOString()
  }
  utc.setUTCMinutes(Math.floor(utc.getUTCMinutes() / mins) * mins, 0, 0)
  return utc.toISOString()
}

export function toBoardTimeQuery(range: BoardTimeRange): BoardTimeQuery {
  if (range.preset === 'custom' && range.start && range.end) {
    return { start: range.start, end: range.end, days: range.days }
  }
  return { days: range.days }
}

/** 用量接口在只有 days 时，起点是系统时区「今天」零点回退 N-1 天（含今天的
 * N 日历日窗——与后端 resolveUsageTimeRange 的 days 回退语义一致）。
 * days 字段留下供展示，usageTimeQs 在有 start/end 时不会把它发给接口。 */
export function usageQueryForBoardRange(range: BoardTimeRange, now = Date.now()): BoardTimeQuery {
  if (range.preset === 'custom' && range.start && range.end) {
    return { start: range.start, end: range.end, days: range.days }
  }
  const { startMs, endMs } = resolveBoardRangeMs(range, now)
  const start = systemTzDateStr(startMs)
  const end = systemTzDateStr(Math.max(startMs, endMs - 1))
  return { start, end, days: range.days }
}

/** 系统时区「今天」零点的毫秒时刻（回退 N-1 天前）。 */
export function utcRollingStartMs(days: number, endMs = Date.now()): number {
  const dayStart = systemTzDayStartMs(endMs)
  return dayStart - Math.max(0, days - 1) * 86_400_000
}

export function resolveBoardRangeMs(range: BoardTimeRange, endMs = Date.now()): { startMs: number; endMs: number } {
  if (range.preset === 'custom' && range.start && range.end) {
    // 时区审计轮（2026-11-21）F2：custom 的日期串按系统时区日切解释
    //（原先 Date.parse('T00:00:00Z') 硬编码 UTC ⇒ +08 下整个窗口后移
    // 8 小时，起点漏当天 0-8 点、终点多含次日 0-8 点）。后端
    // resolveUsageTimeRange 对同一串按系统时区解析 ⇒ 两侧必须同刻度。
    return {
      startMs: systemTzNoonMs(range.start) - 12 * 3_600_000,
      endMs: systemTzNoonMs(range.end) - 12 * 3_600_000 + 86_400_000,
    }
  }
  return { startMs: utcRollingStartMs(range.days || 1, endMs), endMs }
}

/** 窗口里已经发生的分钟数，给 RPM/TPM 当分母。
 * custom 的 endMs 是结束日的次日 00:00，含今天时不能把还没到的小时算进去。 */
export function boardRangeElapsedMinutes(range: BoardTimeRange, now = Date.now()): number {
  const { startMs, endMs } = resolveBoardRangeMs(range, now)
  const cappedEnd = Math.min(endMs, now)
  return Math.max(1, (cappedEnd - startMs) / 60_000)
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
