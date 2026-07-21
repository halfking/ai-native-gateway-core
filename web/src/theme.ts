/**
 * 主题切换 + logo 解析 — 与 ai-native-maintain / ai-session-manager 同源（storage key `llmgw_theme`）。
 *
 * 2026-07-21: 与 ai-native-maintain 主题统一：
 *  - 双套主题变量（CSS：[data-theme='light'] / [data-theme='dark']）
 *  - 单一图标 ☼/☾ 切换（ThemeToggle.vue）
 *  - URL `?theme=light|dark` 永远赢（写回 localStorage 后再 apply）
 *  - detectTheme() 优先级：URL > localStorage > prefers-color-scheme
 */

export type ThemeMode = 'light' | 'dark'

export const THEME_STORAGE_KEY = 'llmgw_theme'

function isValidTheme(v: unknown): v is ThemeMode {
  return v === 'light' || v === 'dark'
}

function systemTheme(): ThemeMode {
  if (typeof window === 'undefined' || !window.matchMedia) return 'light'
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

/** 解析 URL `?theme=light|dark`，命中即返回；否则返回 null。 */
export function readThemeFromUrl(search?: string): ThemeMode | null {
  if (typeof window === 'undefined' && search === undefined) return null
  const raw = typeof search === 'string' ? search : window.location?.search ?? ''
  if (!raw) return null
  try {
    const params = new URLSearchParams(raw)
    const v = params.get('theme')
    return isValidTheme(v) ? v : null
  } catch {
    return null
  }
}

export function detectTheme(): ThemeMode {
  // 优先级：URL > localStorage > 系统偏好
  const fromUrl = readThemeFromUrl()
  if (fromUrl) return fromUrl
  try {
    const saved = localStorage.getItem(THEME_STORAGE_KEY)
    if (isValidTheme(saved)) return saved
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

/**
 * 将 URL ?theme= 写回 localStorage 并立即 apply。
 * 用于首屏 IIFE 之后、App 启动时同步主题到 Vue state。
 */
export function applyThemeFromUrl(search?: string): ThemeMode | null {
  const next = readThemeFromUrl(search)
  if (!next) return null
  setTheme(next)
  return next
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

// IIFE in index.html 已经处理了首屏 data-theme；这里再确认一次，
// 确保 URL ?theme= 覆盖逻辑生效。
applyTheme(detectTheme())