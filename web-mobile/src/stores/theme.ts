import { defineStore } from 'pinia'
import { ref } from 'vue'

// 主题 store — html.dark 切换 + meta theme-color 联动（UI规范 01 §5 / 14 §3）。
// 首帧 class 已由 index.html 内联脚本落位，这里只做用户显式切换。

export type Theme = 'light' | 'dark'

const THEME_KEY = 'llmgw_mobile_theme'
const META_COLOR: Record<Theme, string> = { light: '#f4f6f9', dark: '#0f141c' }

function detectTheme(): Theme {
  try {
    const stored = localStorage.getItem(THEME_KEY)
    if (stored === 'light' || stored === 'dark') return stored
  } catch {
    /* ignore */
  }
  if (typeof window !== 'undefined' && window.matchMedia('(prefers-color-scheme: dark)').matches) {
    return 'dark'
  }
  return 'light'
}

export const useThemeStore = defineStore('theme', () => {
  const theme = ref<Theme>(detectTheme())

  function apply(): void {
    if (typeof document === 'undefined') return
    document.documentElement.classList.toggle('dark', theme.value === 'dark')
    const meta = document.querySelector('meta[name="theme-color"]')
    if (meta) meta.setAttribute('content', META_COLOR[theme.value])
  }

  function setTheme(next: Theme): void {
    theme.value = next
    try {
      localStorage.setItem(THEME_KEY, next)
    } catch {
      /* ignore */
    }
    apply()
  }

  function toggleTheme(): void {
    setTheme(theme.value === 'dark' ? 'light' : 'dark')
  }

  return { theme, setTheme, toggleTheme }
})
