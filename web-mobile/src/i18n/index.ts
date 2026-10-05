import { ref } from 'vue'
import { zhCN } from './zh-CN'
import { enUS } from './en-US'

// 自制点路径词典（nbjl-3 同源手法，无 i18n 运行时依赖）。
// 缺失键回退 zh-CN，再回退键名本身——错误可见但不抛异常。

export type Locale = 'zh-CN' | 'en-US'

const dictionaries: Record<Locale, unknown> = { 'zh-CN': zhCN, 'en-US': enUS }
const LOCALE_KEY = 'llmgw_mobile_locale'

function detectLocale(): Locale {
  try {
    const stored = localStorage.getItem(LOCALE_KEY)
    if (stored === 'zh-CN' || stored === 'en-US') return stored
  } catch {
    /* localStorage 不可用 */
  }
  const nav = typeof navigator !== 'undefined' ? navigator.language : 'zh-CN'
  return nav && nav.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en-US'
}

export const locale = ref<Locale>(detectLocale())

export function getLocale(): Locale {
  return locale.value
}

export function setLocale(next: Locale): void {
  locale.value = next
  try {
    localStorage.setItem(LOCALE_KEY, next)
  } catch {
    /* 持久化失败不阻断切换 */
  }
  if (typeof document !== 'undefined') {
    document.documentElement.lang = next
  }
}

function lookup(dict: unknown, path: string): string | undefined {
  let node: unknown = dict
  for (const seg of path.split('.')) {
    if (node && typeof node === 'object' && seg in (node as Record<string, unknown>)) {
      node = (node as Record<string, unknown>)[seg]
    } else {
      return undefined
    }
  }
  return typeof node === 'string' ? node : undefined
}

export function t(key: string, params?: Record<string, string | number>): string {
  const raw = lookup(dictionaries[locale.value], key) ?? lookup(dictionaries['zh-CN'], key) ?? key
  if (!params) return raw
  return raw.replace(/\{(\w+)\}/g, (m, name: string) =>
    name in params ? String(params[name]) : m,
  )
}

/** 请求头携带（网关 admin 面支持 Accept-Language）。 */
export function acceptLanguageHeader(): string {
  return locale.value
}
