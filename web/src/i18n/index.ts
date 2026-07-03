// index.ts — directory index. Re-exports the single i18n instance from
// `../i18n.ts` so all import paths converge on the same vue-i18n instance
// (avoids accidentally creating a second isolated i18n when components import
// from both `'../i18n'` and `'../i18n/index'`).
//
// Locale-aware formatting helpers live in `useFormat.ts` next to this file.

import { i18n as _i18n, localeRef as _localeRef } from '../i18n'
import { setLocale as persistLocaleToStore } from '../store'
import { SUPPORTED_LOCALES } from './constants'

export const i18n = _i18n
export const localeRef = _localeRef

// Apply <html lang> and <html dir> for the given locale (no-op in non-browser).
export function applyDocumentLocale(code: string): void {
  if (typeof document === 'undefined') return
  const meta = SUPPORTED_LOCALES.find((l) => l.code === code)
  if (!meta) return
  document.documentElement.lang = meta.code
  ;(document.documentElement as HTMLElement).dir = meta.dir
}

// Persist the active locale through both the vue-i18n reactive state AND the
// legacy `setLocale(...)` store helper (used by LanguageSelector and other
// components that import from `../store`).
export async function setLocale(code: string): Promise<void> {
  const meta = SUPPORTED_LOCALES.find((l) => l.code === code)
  if (!meta) return
  applyDocumentLocale(code)
  ;(_i18n.global.locale as unknown as { value: string }).value = code
  persistLocaleToStore(code)
}
