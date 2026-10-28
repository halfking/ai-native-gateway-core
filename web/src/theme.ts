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
  const root = document.documentElement
  // ★ 2026-10-28 幂等写：属性值没变就不重写。
  //   监听 data-theme 的 MutationObserver 会被「自己触发的写入」再次唤醒，
  //   而回调里若调用 detectTheme() → applyThemeFromUrl() → applyTheme()
  //   （又写同一个 data-theme）⇒ 自激微任务循环 ⇒ 渲染主线程 100% 空转 ⇒
  //   整个 SPA 冻结（实测：无头 Chrome 下 `?theme=light` 页面连 setTimeout(0)
  //   都不再触发、截图挂起；去掉参数即恢复）。
  //   MutationObserver 对「值未变的 setAttribute」同样投递记录 ⇒ 必须显式判等。
  if (root.getAttribute('data-theme') !== theme) {
    root.setAttribute('data-theme', theme)
  }
  // Element Plus 暗色变量（theme-chalk/dark/css-vars.css）以 html.dark 为门控，
  // 必须与 data-theme 同步切换，否则 el-* 组件在暗色下保持亮色皮肤。
  root.classList.toggle('dark', theme === 'dark')
  root.style.colorScheme = theme
}

/**
 * 读**当前生效**的主题（纯读，无副作用）。
 *
 * 与 detectTheme() 的区别：detectTheme() 会走 applyThemeFromUrl()，
 * 那是一次**写**（把 URL 上的主题写回 documentElement.data-theme）。
 * 因此 detectTheme() 只能用于「启动时求值一次」的场景；
 * 监听 data-theme 的回调里必须用本函数，否则观察者会被自己的写入唤醒
 * ⇒ 自激循环（见 applyTheme 的幂等写注释）。
 */
export function currentTheme(): ThemeMode {
  if (typeof document !== 'undefined') {
    const attr = document.documentElement.getAttribute('data-theme')
    if (isThemeMode(attr)) return attr
  }
  // 属性还没落上（首屏脚本未跑 / 单测环境）→ 退回既有求值口径。
  return detectTheme()
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