// useFormat.ts — locale-aware date and number formatting helpers.
//
// Locale coverage caveat (2026-08-31, audit P3-2):
//   All `Intl.DateTimeFormat` calls below set `hour12: false`. V8 / ICU
//   honours this for every locale we currently ship (zh-CN, zh-TW, en-US,
//   en-GB, ja-JP, ko-KR, fr-FR, de-DE). If a future locale is added where
//   24-hour notation is not the cultural default (e.g. en-US users who
//   prefer "1:00 PM"), `hour12: false` may be silently overridden by the
//   engine. Callers must NOT assume the rendered string is always 24-hour
//   when the locale changes; pin to a specific option set if that contract
//   matters for the surface.

import { computed } from 'vue'
import { localeRef } from './index'
import { systemTimezone } from '../utils/systemTimezone'

// 时区审计轮（2026-11-21）F6：本文件全部 Intl.DateTimeFormat 注入系统时区
// （原先缺省 = 浏览器本地时区 ⇒ +08 服务器管理 +08 用户时，显示的「日」
// 与统计日切可能差一天）。调用方显式传 timeZone 的场景不受影响（本文件
// 无一处显式传）。回退链：systemTimezone() 未加载时回退浏览器本地时区，
// 与旧行为兼容。

/** 选项注入系统时区（不覆盖显式 timeZone——本文件无显式传参，防御性保留）。 */
function tz(options: Intl.DateTimeFormatOptions): Intl.DateTimeFormatOptions {
  if (options.timeZone) return options
  return { ...options, timeZone: systemTimezone() }
}

export function useFormat() {
  const currentLocale = computed(() => localeRef.value || 'en')

  // R12 候选21: the composable functions below used to return '' on
  // null/invalid input while the rest of the repo (utils/datetime.ts, the
  // module-level P1-7 helpers) uses the '—' sentinel. '—' is the mainstream
  // sentinel — all four now agree, so empty table cells render the same dash
  // everywhere.

  /**
   * Format a date-time value in the current locale.
   * If value is missing, returns '—'.
   *
   * See module-level locale caveat regarding `hour12: false`.
   */
  function fmtDateTime(value?: string | number | Date | null): string {
    if (value === undefined || value === null || value === '') return '—'
    const date = value instanceof Date ? value : new Date(value)
    if (Number.isNaN(date.getTime())) return '—'
    try {
      return new Intl.DateTimeFormat(currentLocale.value, tz({
        year: 'numeric',
        month: 'short',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        hour12: false,
      })).format(date)
    } catch {
      return date.toISOString()
    }
  }

  /**
   * Format a date-time value with seconds.
   * Used by detail views where a single-request timestamp needs to be
   * precise enough to correlate against logs (HH:MM alone collides for
   * requests that finish within the same minute).
   */
  function fmtDateTimeWithSeconds(value?: string | number | Date | null): string {
    if (value === undefined || value === null || value === '') return '—'
    const date = value instanceof Date ? value : new Date(value)
    if (Number.isNaN(date.getTime())) return '—'
    try {
      return new Intl.DateTimeFormat(currentLocale.value, tz({
        year: 'numeric',
        month: 'short',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
        hour12: false,
      })).format(date)
    } catch {
      return date.toISOString()
    }
  }

  function fmtDate(value?: string | number | Date | null): string {
    if (value === undefined || value === null || value === '') return '—'
    const date = value instanceof Date ? value : new Date(value)
    if (Number.isNaN(date.getTime())) return '—'
    try {
      return new Intl.DateTimeFormat(currentLocale.value, tz({
        year: 'numeric',
        month: 'short',
        day: '2-digit',
      })).format(date)
    } catch {
      return date.toISOString().slice(0, 10)
    }
  }

  function fmtNumber(n?: number | null, opts?: Intl.NumberFormatOptions): string {
    if (n === undefined || n === null || Number.isNaN(n)) return '—'
    try {
      return new Intl.NumberFormat(currentLocale.value, opts).format(n)
    } catch {
      return String(n)
    }
  }

  return { fmtDateTime, fmtDateTimeWithSeconds, fmtDate, fmtNumber, locale: currentLocale }
}

/* === P1-7: locale-aware formatting helpers (P1-7 提供)
 * 取代 24 个 Vue 文件中重复的本地 fmtDate / fmtDateTime 实现。语义保留:
 * - 空值/无效返回 '—' (替代原来分散的 '—' / '-')
 * - locale 始终跟随 i18n (替代原来硬锁 'zh-CN' 或 runtime default)
 * - 这些函数是 reactive-free 直接函数，调用方在组件 setup 时仅需 import,
 *   不需要 useFormat() 的 setup 上下文。
 */

export function fmtDateTimeShort(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  try {
    return new Intl.DateTimeFormat(localeRef.value || 'en', tz({
      dateStyle: 'short',
      timeStyle: 'short',
    })).format(d)
  } catch {
    return d.toISOString()
  }
}

export function fmtDateTime24h(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  try {
    return new Intl.DateTimeFormat(localeRef.value || 'en', tz({
      hour12: false,
    })).format(d)
  } catch {
    return d.toISOString()
  }
}

export function fmtDateShort(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  try {
    return new Intl.DateTimeFormat(localeRef.value || 'en', tz({
      year: 'numeric',
      month: 'short',
      day: 'numeric',
    })).format(d)
  } catch {
    return d.toISOString().slice(0, 10)
  }
}

export function fmtDateCompact(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  try {
    return new Intl.DateTimeFormat(localeRef.value || 'en', tz({
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
    })).format(d)
  } catch {
    return d.toISOString()
  }
}

export function fmtDateMedium(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  try {
    return new Intl.DateTimeFormat(localeRef.value || 'en', tz({
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    })).format(d)
  } catch {
    return d.toISOString()
  }
}
