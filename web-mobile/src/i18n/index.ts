import { ref } from 'vue'
import { zhCN } from './zh-CN'
import { enUS } from './en-US'

export type Locale = 'zh-CN' | 'en-US'

export const LOCALE_OPTIONS: ReadonlyArray<{ code: Locale; label: string }> = [
  { code: 'zh-CN', label: '简体中文' },
  { code: 'en-US', label: 'English' },
]

const LOCALE_KEY = 'llmgw_mobile_locale'

function detectLocale(): Locale {
  try {
    const stored = localStorage.getItem(LOCALE_KEY)
    if (stored === 'zh-CN' || stored === 'en-US') return stored
  } catch { /* localStorage may be unavailable */ }
  const language = typeof navigator === 'undefined' ? 'zh-CN' : navigator.language.toLowerCase()
  return language.startsWith('zh') ? 'zh-CN' : 'en-US'
}

export const locale = ref<Locale>(detectLocale())

export function getLocale(): Locale { return locale.value }

export function setLocale(next: Locale): void {
  locale.value = next === 'zh-CN' ? 'zh-CN' : 'en-US'
  try { localStorage.setItem(LOCALE_KEY, locale.value) } catch { /* persistence is optional */ }
  if (typeof document !== 'undefined') {
    document.documentElement.lang = locale.value
    document.documentElement.dir = 'ltr'
  }
}

function lookup(dict: unknown, path: string): string | undefined {
  let node: unknown = dict
  for (const segment of path.split('.')) {
    if (node && typeof node === 'object' && segment in (node as Record<string, unknown>)) {
      node = (node as Record<string, unknown>)[segment]
    } else return undefined
  }
  return typeof node === 'string' ? node : undefined
}

export function t(key: string, params?: Record<string, string | number>): string {
  const primary = locale.value === 'zh-CN' ? zhCN : enUS
  const fallback = locale.value === 'zh-CN' ? enUS : zhCN
  const raw = lookup(primary, key) ?? lookup(fallback, key) ?? key
  return params ? raw.replace(/\{(\w+)\}/g, (match, name: string) => name in params ? String(params[name]) : match) : raw
}

/** Requests advertise the selected locale to gateway admin APIs. */
export function acceptLanguageHeader(): string { return locale.value }

// Apply the saved language before first render, including restored sessions.
setLocale(locale.value)
