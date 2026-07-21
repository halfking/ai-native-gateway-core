/**
 * 主题切换 + logo 解析 — 与 ai-native-maintain 同源（storage key `llmgw_theme`）。
 * URL `?theme=light|dark` 透传：单页刷新立即覆盖 localStorage（URL 永远赢）。
 * 2026-07-21: 与 ai-native-maintain 主题统一 + URL 参数透传。
 */

export type ThemeMode = 'light' | 'dark'

export const THEME_STORAGE_KEY = 'llmgw_theme'
export const URL_THEME_KEY = 'theme'

function systemTheme(): ThemeMode {
  if (typeof window === 'undefined' || !window.matchMedia) return 'light'
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

export function isThemeMode(value: unknown): value is ThemeMode {
  return value === 'light' || value === 'dark'
}

function readUrlTheme(search?: string): ThemeMode | null {
  if (typeof window === 'undefined') return null
  const raw = (() => {
    if (search !== undefined) {
      try {
        return new URLSearchParams(search).get(URL_THEME_KEY)
      } catch {
        return null
      }
    }
    try {
      return new URLSearchParams(window.location.search).get(URL_THEME_KEY)
    } catch {
      return null
    }
  })()
  return isThemeMode(raw) ? raw : null
}

export function applyThemeFromUrl(search?: string): ThemeMode | null {
  const fromUrl = readUrlTheme(search)
  if (fromUrl) {
    try {
      localStorage.setItem(THEME_STORAGE_KEY, fromUrl)
    } catch {
      /* ignore */
    }
    applyTheme(fromUrl)
  }
  return fromUrl
}

export function detectTheme(): ThemeMode {
  // URL 永远赢：每次 detect 前先尝试 URL 透传
  const fromUrl = applyThemeFromUrl()
  if (fromUrl) return fromUrl

  try {
    const saved = localStorage.getItem(THEME_STORAGE_KEY) as ThemeMode | null
    if (isThemeMode(saved)) return saved
  } catch {
    /* ignore */
  }
  return systemTheme()
}

export function applyTheme(theme: ThemeMode) {
  if (typeof document === 'undefined') return
  document.documentElement.setAttribute('data-theme', theme)
  document.documentElement.style.colorScheme = theme
}

export function setTheme(theme: ThemeMode) {
  try {
    localStorage.setItem(THEME_STORAGE_KEY, theme)
  } catch {
    /* ignore */
  }
  applyTheme(theme)
}

export function toggleTheme(): ThemeMode {
  const next: ThemeMode = detectTheme() === 'dark' ? 'light' : 'dark'
  setTheme(next)
  return next
}

export function logoSrc(theme: ThemeMode = detectTheme()): string {
  // Gateway SPA 静态资源在根路径下，不带 /maintain/ 前缀。
  return theme === 'dark' ? '/logo-icon-dark.png' : '/logo-icon.png'
}

applyTheme(detectTheme())