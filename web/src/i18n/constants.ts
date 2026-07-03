// constants.ts — canonical list of UI locales supported by the console.

export type LocaleDir = 'ltr' | 'rtl'

export interface LocaleMeta {
  code: string
  short: string
  nativeName: string
  englishName: string
  dir: LocaleDir
  flag: string
}

export const SUPPORTED_LOCALES: LocaleMeta[] = [
  { code: 'en', short: 'EN',  nativeName: 'English',   englishName: 'English',              dir: 'ltr', flag: '🇺🇸' },
  { code: 'zh-CN', short: '简中', nativeName: '简体中文',  englishName: 'Chinese (Simplified)', dir: 'ltr', flag: '🇨🇳' },
  { code: 'zh-TW', short: '繁中', nativeName: '繁體中文',  englishName: 'Chinese (Traditional)', dir: 'ltr', flag: '🇹🇼' },
  { code: 'ja', short: '日',  nativeName: '日本語',     englishName: 'Japanese',             dir: 'ltr', flag: '🇯🇵' },
  { code: 'de', short: 'DE',  nativeName: 'Deutsch',   englishName: 'German',               dir: 'ltr', flag: '🇩🇪' },
  { code: 'fr', short: 'FR',  nativeName: 'Français',  englishName: 'French',               dir: 'ltr', flag: '🇫🇷' },
  { code: 'es', short: 'ES',  nativeName: 'Español',   englishName: 'Spanish',              dir: 'ltr', flag: '🇪🇸' },
  { code: 'ar', short: 'ع',   nativeName: 'العربية',    englishName: 'Arabic',               dir: 'rtl', flag: '🇸🇦' },
]

export const DEFAULT_LOCALE = 'en'
export const FALLBACK_LOCALE = 'en'

export const RTL_LOCALES: string[] = SUPPORTED_LOCALES.filter((l: LocaleMeta) => l.dir === 'rtl').map((l: LocaleMeta) => l.code)

export function getLocaleMeta(code: string): LocaleMeta | undefined {
  return SUPPORTED_LOCALES.find((l: LocaleMeta) => l.code === code)
}

export function isSupportedLocale(code: string): boolean {
  return SUPPORTED_LOCALES.some((l: LocaleMeta) => l.code === code)
}

export function isRTL(code: string): boolean {
  return RTL_LOCALES.includes(code)
}

export function matchLocale(tag: string): string | undefined {
  const lower = tag.toLowerCase()
  const exact = SUPPORTED_LOCALES.find((l: LocaleMeta) => l.code.toLowerCase() === lower)
  if (exact) return exact.code
  const primary = lower.split('-')[0]
  const partial = SUPPORTED_LOCALES.find((l: LocaleMeta) => l.code.toLowerCase().split('-')[0] === primary)
  return partial?.code
}
