import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { useBreakpoint, _resetForTests } from './useBreakpoint'
import { BREAKPOINTS } from '../config/breakpoints'

/**
 * useBreakpoint 单测：用可操控的 matchMedia fake 验证三档判定
 * 与响应式变化（改变 matches 后 ref 更新）。
 */

type FakeMql = {
  matches: boolean
  media: string
  addEventListener: (type: string, listener: (e: { matches: boolean }) => void) => void
  removeEventListener: (type: string, listener: (e: { matches: boolean }) => void) => void
  fire: (matches: boolean) => void
}

function createFakeMql(media: string, initialMatches: boolean): FakeMql {
  const listeners = new Set<(e: { matches: boolean }) => void>()
  const mql: FakeMql = {
    matches: initialMatches,
    media,
    addEventListener: (_type, listener) => listeners.add(listener),
    removeEventListener: (_type, listener) => listeners.delete(listener),
    fire(next: boolean) {
      mql.matches = next
      listeners.forEach((l) => l({ matches: next }))
    },
  }
  return mql
}

/** 场景化构造：按"当前视口宽度"推导三个查询的 matches。 */
function installMatchMediaForWidth(width: number) {
  const created: FakeMql[] = []
  const matchMedia = (query: string) => {
    const matches = widthMatchesQuery(width, query)
    const mql = createFakeMql(query, matches)
    created.push(mql)
    return mql
  }
  Object.defineProperty(window, 'matchMedia', { writable: true, configurable: true, value: matchMedia })
  return created
}

function widthMatchesQuery(width: number, query: string): boolean {
  const max = query.match(/max-width:\s*(\d+)px/)
  if (max) return width <= parseInt(max[1], 10)
  const min = query.match(/min-width:\s*(\d+)px/)
  if (min) return width >= parseInt(min[1], 10)
  return false
}

describe('useBreakpoint', () => {
  beforeEach(() => {
    _resetForTests()
  })

  afterEach(() => {
    _resetForTests()
  })

  it('查询字符串严格来自 breakpoints.ts 单一事实源', () => {
    const created = installMatchMediaForWidth(1440)
    useBreakpoint()
    const medias = created.map((m) => m.media).sort()
    expect(medias).toEqual(
      [
        `(max-width: ${BREAKPOINTS.desktop - 1}px)`, // <1024
        `(max-width: ${BREAKPOINTS.small - 1}px)`, // <480
        `(min-width: ${BREAKPOINTS.tablet}px)`, // >=768
      ].sort(),
    )
  })

  it('桌面宽度 1440：isDesktop=true，非 mobile/small', () => {
    installMatchMediaForWidth(1440)
    const { isMobile, isTablet, isSmall, isDesktop } = useBreakpoint()
    expect(isMobile.value).toBe(false)
    expect(isTablet.value).toBe(true)
    expect(isSmall.value).toBe(false)
    expect(isDesktop.value).toBe(true)
  })

  it('平板宽度 800（768~1023）：isMobile 布局口径 + isTablet=true', () => {
    installMatchMediaForWidth(800)
    const { isMobile, isTablet, isSmall, isDesktop } = useBreakpoint()
    expect(isMobile.value).toBe(true)
    expect(isTablet.value).toBe(true)
    expect(isSmall.value).toBe(false)
    expect(isDesktop.value).toBe(false)
  })

  it('手机宽度 375：isMobile=true，且 <480 命中 isSmall', () => {
    installMatchMediaForWidth(375)
    const { isMobile, isTablet, isSmall } = useBreakpoint()
    expect(isMobile.value).toBe(true)
    expect(isTablet.value).toBe(false)
    expect(isSmall.value).toBe(true)
  })

  it('手机宽度 600（480~767）：isMobile=true 但 isSmall=false', () => {
    installMatchMediaForWidth(600)
    const { isMobile, isSmall } = useBreakpoint()
    expect(isMobile.value).toBe(true)
    expect(isSmall.value).toBe(false)
  })

  it('响应式变化：改变 matches 并派发 change 后 ref 更新', () => {
    const created = installMatchMediaForWidth(1440)
    const bp = useBreakpoint()
    expect(bp.isDesktop.value).toBe(true)

    // 视口缩到手机 375px：mobile(<1024) 与 small(<480) 查询翻转为 matches
    const mobile = created.find((m) => m.media.includes('max-width: 1023px'))
    const small = created.find((m) => m.media.includes('max-width: 479px'))
    const tablet = created.find((m) => m.media.includes('min-width: 768px'))
    expect(mobile && small && tablet).toBeTruthy()

    mobile!.fire(true)
    small!.fire(true)
    tablet!.fire(false)

    expect(bp.isMobile.value).toBe(true)
    expect(bp.isSmall.value).toBe(true)
    expect(bp.isTablet.value).toBe(false)
    expect(bp.isDesktop.value).toBe(false)
  })

  it('全局单例：多次调用共享同一份 ref，一处更新处处可见', () => {
    const created = installMatchMediaForWidth(1440)
    const a = useBreakpoint()
    const b = useBreakpoint()
    expect(a.isMobile).toBe(b.isMobile)

    const mobile = created.find((m) => m.media.includes('max-width: 1023px'))
    mobile!.fire(true)
    expect(b.isMobile.value).toBe(true)
  })

  it('window.matchMedia 不可用时安全降级（全 false，不抛错）', () => {
    Object.defineProperty(window, 'matchMedia', { writable: true, configurable: true, value: undefined })
    const bp = useBreakpoint()
    expect(bp.isMobile.value).toBe(false)
    expect(bp.isTablet.value).toBe(false)
    expect(bp.isSmall.value).toBe(false)
    expect(bp.isDesktop.value).toBe(true)
  })
})
