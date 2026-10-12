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

  // 2026-10-12 审计（2543 轮 GUI 验收，1280 视口实测）：EN 菜单总宽 ~922px，
  // 单行需 ~1569px；nowrap + min-width:0 让 nav 从操作区底下穿过，版本徽章
  // 盖住「Integration Guide / 运维中心」并拦截点击（实测重叠 273px）。
  // 修复 = 顶栏允许换行 + nav 以 fit-content 为最小宽：放不下整体落到第二行，
  // 放得下单行布局不变（宽屏零回归）。
  it('desktop nav wraps to its own row instead of sliding under the actions', () => {
    // 只断言基础规则区（第一个 @media 之前）：≤768 块里本来就有一条
    // .app-topbar { flex-wrap: wrap }，全文件搜索会拿它冒充基础规则（变异验证实录）。
    const baseStyles = source.slice(0, source.indexOf('@media'))
    expect(baseStyles).toMatch(/\.app-topbar\s*{[^}]*flex-wrap:\s*wrap/s)
    expect(baseStyles).toMatch(/\.app-topbar\s*{[^}]*flex-shrink:\s*0/s)
    expect(baseStyles).toMatch(/\.app-topbar__nav\s*{[^}]*min-width:\s*fit-content/s)
    // ≤768 兜底块里 fit-content 会与 width:100% 打架（min-width 赢家导致横向撑破），
    // 必须回置 min-width: 0（该块自带 overflow-x:auto 兜底）。
    const mobileStyles = source.slice(source.indexOf('@media (max-width: 768px)'))
    expect(mobileStyles).toMatch(/\.app-topbar__nav\s*{[^}]*min-width:\s*0/s)
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
