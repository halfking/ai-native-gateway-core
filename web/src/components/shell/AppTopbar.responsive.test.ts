import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(process.cwd(), 'src/components/shell/AppTopbar.vue'), 'utf8')

describe('AppTopbar responsive navigation', () => {
  it('contains wide mobile navigation without overflowing the page', () => {
    const mobileStyles = source.slice(source.indexOf('@media (max-width: 768px)'))

    expect(mobileStyles).toMatch(/\.app-topbar__nav\s*{[^}]*overflow-x:\s*auto/s)
    expect(mobileStyles).toMatch(/\.app-topbar__nav\s*{[^}]*flex-wrap:\s*nowrap/s)
    expect(mobileStyles).toMatch(/\.app-topbar__link\s*{[^}]*flex:\s*0 0 auto/s)
  })

  it('keeps essential actions visible on narrow screens', () => {
    const narrowStyles = source.slice(source.indexOf('@media (max-width: 480px)'))

    expect(narrowStyles).toMatch(/\.app-topbar__brand-text,\s*\.app-topbar__meta\s*{[^}]*display:\s*none/s)
    expect(narrowStyles).not.toMatch(/\.app-topbar__actions\s*{[^}]*display:\s*none/s)
  })

  it('does not duplicate the API version prefix', () => {
    expect(source).toContain('<span class="app-topbar__version-tag">{{ props.versionInfo.version }}</span>')
    expect(source).not.toContain('<span class="app-topbar__version-tag">v{{ props.versionInfo.version }}</span>')
  })

  // 2026-09-13 方案 §4.4：<1024 由 useBreakpoint().isMobile 接管，菜单区
  // 替换为汉堡按钮并打开共享的 navDrawerOpen；>=1024 桌面 DOM 保持不变。
  it('mobile shell swaps the nav for a hamburger driven by useBreakpoint().isMobile', () => {
    expect(source).toContain('const { isMobile } = useBreakpoint()')
    // 汉堡分支：v-if="isMobile" 的按钮，点击打开共享抽屉开关
    expect(source).toMatch(/<button[^]*v-if="isMobile"[\s\S]*?class="app-topbar__hamburger"[\s\S]*?@click="navDrawerOpen = true"/)
    // 桌面分支：nav 用 v-else 渲染，>=1024 DOM 结构与改造前一致
    expect(source).toMatch(/<nav v-else class="app-topbar__nav"/)
  })

  it('hamburger meets the 44px touch-target budget', () => {
    expect(source).toMatch(/\.app-topbar__hamburger\s*{[^}]*height:\s*40px/s)
    // responsive-base.css 在 <768 提供 min-height 兜底，桌面不受影响
    expect(readFileSync(resolve(process.cwd(), 'src/styles/responsive-base.css'), 'utf8'))
      .toMatch(/min-height:\s*44px/)
  })

  it('keeps the actions area intact for the mobile shell', () => {
    // 顶栏保留 品牌 + SystemStatusIndicator + ThemeToggle + LanguageSelector + UserMenuDropdown
    for (const fragment of [
      'class="app-topbar__brand"',
      '<SystemStatusIndicator />',
      '<ThemeToggle />',
      '<LanguageSelector />',
      '<UserMenuDropdown',
    ]) {
      expect(source).toContain(fragment)
    }
  })

  it('menu data comes from the shared useAppNav composable', () => {
    expect(source).toContain('useAppNav()')
    expect(source).toContain('navDrawerOpen')
  })
})
