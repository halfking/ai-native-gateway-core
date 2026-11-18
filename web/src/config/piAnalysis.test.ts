import { describe, it, expect, beforeEach, vi } from 'vitest'
import {
  _resetPIAnalysisForTests,
  onPIAnalysisAvailabilityChange,
  probePIAnalysisAvailable,
  showPIAnalysis,
} from './piAnalysis'
import { canShowNavItem, NAV_GROUPS } from './appNav'

/**
 * ★ 2026-11-18 审计轮 P2 的钉测。
 *
 * 缺陷本体：「PI 分析」菜单项无条件展示，而它背后的
 * `/api/admin/pi-analysis/*` 只在「有 DB + admin 鉴权材料齐备」时注册
 * （后端 fail-closed）⇒ 无 DB 的部署上菜单项点了就是 404 空页。
 *
 * 钉住两件事：
 *   ① 探测到 404 ⇒ 该菜单项**不再展示**（不再宣传不存在的功能）；
 *   ② 未探测 / 探测到 200 ⇒ 照常展示（不许把功能误藏）。
 */
const PI_ITEM = NAV_GROUPS.flatMap((g) => g.items).find((i) => i.path === '/admin/pi-analysis')!

function stubFetch(status: number) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => ({
      ok: status >= 200 && status < 300,
      status,
      json: async () => ({}),
    })),
  )
}

describe('pi-analysis nav availability', () => {
  beforeEach(() => {
    _resetPIAnalysisForTests()
    vi.unstubAllGlobals()
  })

  it('marks the nav item as runtime-gated', () => {
    expect(PI_ITEM).toBeTruthy()
    expect(PI_ITEM.piAnalysis).toBe(true)
  })

  it('keeps the item visible before probing (unknown must not hide the feature)', () => {
    expect(showPIAnalysis()).toBe(true)
    expect(
      canShowNavItem(PI_ITEM, {
        isSuperAdmin: true,
        isPlatformOps: true,
        isTenantPortal: false,
      }),
    ).toBe(true)
  })

  it('hides the item when the API answers 404 (routes not registered)', async () => {
    stubFetch(404)
    const changed = vi.fn()
    onPIAnalysisAvailabilityChange(changed)

    const available = await probePIAnalysisAvailable(true)

    expect(available).toBe(false)
    expect(showPIAnalysis()).toBe(false)
    expect(changed).toHaveBeenCalled()
    expect(
      canShowNavItem(PI_ITEM, {
        isSuperAdmin: true,
        isPlatformOps: true,
        isTenantPortal: false,
        piAnalysisAvailable: false,
      }),
    ).toBe(false)
  })

  it('keeps the item visible when the API answers 200', async () => {
    stubFetch(200)
    const available = await probePIAnalysisAvailable(true)

    expect(available).toBe(true)
    expect(showPIAnalysis()).toBe(true)
    expect(
      canShowNavItem(PI_ITEM, {
        isSuperAdmin: true,
        isPlatformOps: true,
        isTenantPortal: false,
        piAnalysisAvailable: true,
      }),
    ).toBe(true)
  })
})
