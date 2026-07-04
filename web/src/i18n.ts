// i18n.ts — vue-i18n instance + locale resolution + lazy loading (44-module structure).
//
// 2026-07-05: Migrated from flat locale files to 44-module layered architecture
// (copied from llm-gateway-go-2). Loading strategy: zh-CN and en-US are bundled;
// the other six locales are lazy-loaded on first switch.
//
// Locale codes:
//   Current (8): en, zh-CN, zh-TW, ja, de, fr, es, ar
//   Reference (8): en-US, zh-CN, zh-TW, ja-JP, de-DE, fr-FR, es-ES, ar-SA
// We map reference codes → current codes in LAZY_LOADERS to match existing localStorage keys.

import { createI18n } from 'vue-i18n'
import type { Ref } from 'vue'
import zhCN from './locales/zh-CN'
import enUS from './locales/en-US'

/** Locales bundled into the initial chunk (full coverage). */
const STATIC_LOCALES = {
  'zh-CN': zhCN,
  'en': enUS, // Map en-US → en for backward compat
}

/** Dynamic-import loaders for the lazily-fetched locales. */
const LAZY_LOADERS: Record<string, () => Promise<{ default: Record<string, unknown> }>> = {
  'zh-TW': () => import('./locales/zh-TW'),
  'ja': () => import('./locales/ja-JP'),    // Map ja-JP → ja
  'de': () => import('./locales/de-DE'),    // Map de-DE → de
  'fr': () => import('./locales/fr-FR'),    // Map fr-FR → fr
  'es': () => import('./locales/es-ES'),    // Map es-ES → es
  'ar': () => import('./locales/ar-SA'),    // Map ar-SA → ar
}

/** Locales already loaded into vue-i18n messages. */
const loaded = new Set<string>(Object.keys(STATIC_LOCALES))

/**
 * Pick the initial locale: localStorage → browser → 'zh-CN'.
 * Runs once at module load (before app mount).
 */
function detectInitialLocale(): string {
  const saved = localStorage.getItem('llmgw_locale')
  if (saved) return saved
  // Simple browser language matching (en-US → en, zh → zh-CN, etc.)
  if (typeof navigator !== 'undefined' && navigator.language) {
    const nav = navigator.language.toLowerCase()
    if (nav.startsWith('zh-tw') || nav.startsWith('zh-hk')) return 'zh-TW'
    if (nav.startsWith('zh')) return 'zh-CN'
    if (nav.startsWith('ja')) return 'ja'
    if (nav.startsWith('de')) return 'de'
    if (nav.startsWith('fr')) return 'fr'
    if (nav.startsWith('es')) return 'es'
    if (nav.startsWith('ar')) return 'ar'
    if (nav.startsWith('en')) return 'en'
  }
  return 'zh-CN'
}

export const i18n = createI18n({
  legacy: false,
  globalInjection: true, // exposes $t / $tc in templates
  locale: detectInitialLocale(),
  fallbackLocale: 'en',
  messages: STATIC_LOCALES,
  missingWarn: false,
  fallbackWarn: false,
})

/** Apply <html lang> and <html dir> for the given locale. */
export function applyDocumentLocale(code: string): void {
  if (typeof document === 'undefined') return
  const langMap: Record<string, { lang: string; dir: string }> = {
    'zh-CN': { lang: 'zh-CN', dir: 'ltr' },
    'en': { lang: 'en', dir: 'ltr' },
    'zh-TW': { lang: 'zh-TW', dir: 'ltr' },
    'ja': { lang: 'ja', dir: 'ltr' },
    'de': { lang: 'de', dir: 'ltr' },
    'fr': { lang: 'fr', dir: 'ltr' },
    'es': { lang: 'es', dir: 'ltr' },
    'ar': { lang: 'ar', dir: 'rtl' },
  }
  const meta = langMap[code]
  if (meta) {
    document.documentElement.lang = meta.lang
    document.documentElement.dir = meta.dir
  }
}

/**
 * Switch the active locale, fetching the message chunk on first use.
 * Also updates localStorage and <html lang/dir>.
 */
export async function setLocale(code: string): Promise<void> {
  if (!loaded.has(code)) {
    const loader = LAZY_LOADERS[code]
    if (loader) {
      const mod = await loader()
      i18n.global.setLocaleMessage(code, mod.default as never)
      loaded.add(code)
    }
  }
  ;(i18n.global.locale as unknown as { value: string }).value = code
  localStorage.setItem('llmgw_locale', code)
  applyDocumentLocale(code)
}

// Language picker config (kept for backward compat with LanguageSelector.vue)
export const languages = [
  { code: 'zh-CN', name: '简体中文', flag: '🇨🇳' },
  { code: 'en', name: 'English', flag: '🇺🇸' },
  { code: 'ja', name: '日本語', flag: '🇯🇵' },
  { code: 'de', name: 'Deutsch', flag: '🇩🇪' },
  { code: 'fr', name: 'Français', flag: '🇫🇷' },
  { code: 'es', name: 'Español', flag: '🇪🇸' },
  { code: 'zh-TW', name: '繁體中文', flag: '🇹🇼' },
  { code: 'ar', name: 'العربية', flag: '🇸🇦' },
]

// Reactive locale ref for callers (ProvidersView, useFormat.ts, etc.)
export const localeRef = i18n.global.locale as unknown as Ref<string>

// Apply <html lang/dir> immediately so first paint (incl. login) is correct
applyDocumentLocale(detectInitialLocale())
