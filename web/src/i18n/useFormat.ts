// useFormat.ts — locale-aware date and number formatting helpers.

import { computed } from 'vue'
import { localeRef } from './index'

export function useFormat() {
  const currentLocale = computed(() => localeRef.value || 'en')

  /**
   * Format a date-time value in the current locale.
   * If value is missing, returns ''.
   */
  function fmtDateTime(value?: string | number | Date | null): string {
    if (value === undefined || value === null || value === '') return ''
    const date = value instanceof Date ? value : new Date(value)
    if (Number.isNaN(date.getTime())) return ''
    try {
      return new Intl.DateTimeFormat(currentLocale.value, {
        year: 'numeric',
        month: 'short',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        hour12: false,
      }).format(date)
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
    if (value === undefined || value === null || value === '') return ''
    const date = value instanceof Date ? value : new Date(value)
    if (Number.isNaN(date.getTime())) return ''
    try {
      return new Intl.DateTimeFormat(currentLocale.value, {
        year: 'numeric',
        month: 'short',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
        hour12: false,
      }).format(date)
    } catch {
      return date.toISOString()
    }
  }

  function fmtDate(value?: string | number | Date | null): string {
    if (value === undefined || value === null || value === '') return ''
    const date = value instanceof Date ? value : new Date(value)
    if (Number.isNaN(date.getTime())) return ''
    try {
      return new Intl.DateTimeFormat(currentLocale.value, {
        year: 'numeric',
        month: 'short',
        day: '2-digit',
      }).format(date)
    } catch {
      return date.toISOString().slice(0, 10)
    }
  }

  function fmtNumber(n?: number | null, opts?: Intl.NumberFormatOptions): string {
    if (n === undefined || n === null || Number.isNaN(n)) return ''
    try {
      return new Intl.NumberFormat(currentLocale.value, opts).format(n)
    } catch {
      return String(n)
    }
  }

  return { fmtDateTime, fmtDateTimeWithSeconds, fmtDate, fmtNumber, locale: currentLocale }
}

/* === P1-7: locale-aware formatting helpers (P1-7 提偼)
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
    return new Intl.DateTimeFormat(localeRef.value || 'en', {
      dateStyle: 'short',
      timeStyle: 'short',
    }).format(d)
  } catch {
    return d.toISOString()
  }
}

export function fmtDateTime24h(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  try {
    return new Intl.DateTimeFormat(localeRef.value || 'en', {
      hour12: false,
    }).format(d)
  } catch {
    return d.toISOString()
  }
}

export function fmtDateShort(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  try {
    return new Intl.DateTimeFormat(localeRef.value || 'en', {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
    }).format(d)
  } catch {
    return d.toISOString().slice(0, 10)
  }
}

export function fmtDateCompact(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  try {
    return new Intl.DateTimeFormat(localeRef.value || 'en', {
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
    }).format(d)
  } catch {
    return d.toISOString()
  }
}

export function fmtDateMedium(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  try {
    return new Intl.DateTimeFormat(localeRef.value || 'en', {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    }).format(d)
  } catch {
    return d.toISOString()
  }
}
