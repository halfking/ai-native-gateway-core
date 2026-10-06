import { describe, expect, it } from 'vitest'
import { BREAKPOINT_EXPANDED_PX, BREAKPOINT_LARGE_PX, BREAKPOINT_MEDIUM_PX, currentWindowClass } from './useWindowClass'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

// 双镜像防漂移（17 §4-R11）：TS 断点常量必须与 theme.css 的 --app-bp-* 一致。
// ⚠️ 这条断言只保证**两侧互不漂移**，不保证它们等于 01 §2.1 的 768/1024/1440 ——
// 那是 web/ 桌面侧的 SSOT，web-mobile/ 用的是 600/960/1280 自有断点（17 §4-R11）。
// 哪天真要改数值，改完这三条断言会自己提醒你 theme.css 还没跟上。
describe('useWindowClass 断点双镜像', () => {
  it('theme.css token 与 TS 常量一致', () => {
    // vitest root = web-mobile/（jsdom 下 import.meta.url 非 file 协议，走 cwd 解析）
    const css = readFileSync(resolve(process.cwd(), 'src/styles/theme.css'), 'utf8')
    expect(css).toContain(`--app-bp-medium: ${BREAKPOINT_MEDIUM_PX}px`)
    expect(css).toContain(`--app-bp-expanded: ${BREAKPOINT_EXPANDED_PX}px`)
    expect(css).toContain(`--app-bp-large: ${BREAKPOINT_LARGE_PX}px`)
  })

  it('无 window 回落 compact（SSR 安全）', () => {
    expect(currentWindowClass()).toBe('compact')
  })
})

describe('currentWindowClass 分类', () => {
  function withWidth(px: number, fn: () => void): void {
    const original = window.matchMedia
    window.matchMedia = ((q: string) => {
      const min = Number(/min-width:\s*(\d+)px/.exec(q)?.[1] ?? 0)
      return { matches: px >= min, media: q, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false, onchange: null } as unknown as MediaQueryList
    }) as typeof window.matchMedia
    try {
      fn()
    } finally {
      window.matchMedia = original
    }
  }

  it('599 = compact、600 = medium、959 仍 medium、960 = expanded、1280 = large', () => {
    const cases: Array<[number, string]> = [
      [320, 'compact'],
      [599, 'compact'],
      [600, 'medium'],
      [959, 'medium'],
      [960, 'expanded'],
      [1279, 'expanded'],
      [1280, 'large'],
    ]
    for (const [px, expected] of cases) {
      withWidth(px, () => {
        expect(currentWindowClass(), `width ${px}`).toBe(expected)
      })
    }
  })
})
