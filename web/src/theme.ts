/** Shared theme with Maintain — storage key `llmgw_theme`. */

export type ThemeMode = 'light' | 'dark'

export const THEME_STORAGE_KEY = 'llmgw_theme'

function systemTheme(): ThemeMode {
  if (typeof window === 'undefined' || !window.matchMedia) return 'light'
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

export function detectTheme(): ThemeMode {
  try {
    const saved = localStorage.getItem(THEME_STORAGE_KEY) as ThemeMode | null
    if (saved === 'light' || saved === 'dark') return saved
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
  return theme === 'dark' ? '/logo-icon-dark.png' : '/logo-icon.png'
}

applyTheme(detectTheme())
