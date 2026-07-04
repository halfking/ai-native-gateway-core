// index.ts — directory index. Re-exports i18n instance + unified setLocale() that
// handles lazy loading, <html lang/dir>, vue-i18n reactive state, and localStorage.
//
// 2026-07-05: Updated to use the new lazy-loading setLocale from ../i18n.ts (which
// now supports 44-module architecture with dynamic imports for non-bundled locales).

import { i18n as _i18n, localeRef as _localeRef, setLocale as _setLocale, applyDocumentLocale as _applyDocumentLocale } from '../i18n'

export const i18n = _i18n
export const localeRef = _localeRef
export const applyDocumentLocale = _applyDocumentLocale

// Unified setLocale: lazy-loads locale chunk, updates vue-i18n + localStorage + <html>.
// Replaces the old `setLocale` from store.ts (which only updated localStorage).
export const setLocale = _setLocale
