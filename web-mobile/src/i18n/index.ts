// i18n/index.ts — 点路径词典（无 i18n 运行时依赖，UI 规范 17 §6）。
// zh-CN + en-US 两语言；localStorage['llmgw_mobile_locale']；缺键回落 zh。

import { setLocaleProvider } from '../api/_core'
import { zhCN } from './zh-CN'
import { enUS } from './en-US'

export type Locale = 'zh-CN' | 'en-US'

const LOCALE_KEY = 'llmgw_mobile_locale'
const DICTS: Record<Locale, Record<string, string>> = {
  'zh-CN': zhCN,
  'en-US': enUS,
}

let currentLocale: Locale = readInitialLocale()

function readInitialLocale(): Locale {
  try {
    const saved = localStorage.getItem(LOCALE_KEY)
    if (saved === 'zh-CN' || saved === 'en-US') return saved
  } catch {
    /* ignore */
  }
  const nav = typeof navigator !== 'undefined' ? navigator.language : 'zh-CN'
  return nav && nav.startsWith('en') ? 'en-US' : 'zh-CN'
}

export function getLocale(): Locale {
  return currentLocale
}

/** BCP-47 tag for Accept-Language（桌面端同款约定）。 */
export function localeTag(): string {
  return currentLocale
}

export function setLocale(locale: Locale): void {
  currentLocale = locale
  try {
    localStorage.setItem(LOCALE_KEY, locale)
  } catch {
    /* ignore */
  }
  document.documentElement.setAttribute('lang', locale === 'en-US' ? 'en' : 'zh-CN')
}

/** 点路径取词：t('nav.overview')；缺键回落 zh-CN，再缺回落路径本身。 */
export function t(path: string): string {
  const dict = DICTS[currentLocale]
  return dict[path] ?? zhCN[path] ?? path
}

setLocaleProvider(localeTag)
