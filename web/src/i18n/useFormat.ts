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

  return { fmtDateTime, fmtDate, fmtNumber, locale: currentLocale }
}
