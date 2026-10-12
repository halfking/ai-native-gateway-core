// 站点选择（2026-10-13）的判据。
//
// ★ 为什么这些断言长这样：每一条都对应一个**曾经会发生的错**，
//   而不是「渲染出来了吗」那种自证式断言。
//
// ⚠️ 本文件验的是**切换语义**，不是「有没有这个按钮」。
//   站点选择最危险的失败形态是：按钮在、点了没反应、或者跳到错误的地方 ——
//   那三种都不会让「元素存在」型断言变红。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { GATEWAY_SITES, currentSite, isCurrentSite, siteLoginUrl } from './gatewaySites'

/**
 * 构造一个「位于某站点某路径」的 location。
 *
 * ⚠️ `origin` 必须**只含 scheme+host** —— 真实浏览器的 `location.origin`
 *   不含路径与 query。之前这个 helper 直接把入参当 origin 用，于是
 *   `setOrigin('https://llmgo.kxpms.cn', '/m/login')` 造出一个不存在的 origin，
 *   判据测的其实是自己造出来的怪东西。**量具错了，红的不是产品。**
 */
function setOrigin(origin: string, pathname = '/m/'): void {
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { ...window.location, origin, pathname, href: `${origin}${pathname}` },
  })
}

describe('gatewaySites', () => {
  const originalLocation = window.location

  beforeEach(() => {
    setOrigin('https://llmgo.kxpms.cn')
  })

  afterEach(() => {
    Object.defineProperty(window, 'location', { configurable: true, value: originalLocation })
  })

  it('三个预置站点都在，且 origin 不带尾斜杠', () => {
    // 尾斜杠会让 `${origin}/m/login` 拼出 `https://x.cn//m/login` —— 双斜杠
    // 在某些反代上会 404，而 URL 本身看着完全正常。
    for (const site of GATEWAY_SITES) {
      expect(site.origin.endsWith('/')).toBe(false)
      expect(site.origin.startsWith('https://')).toBe(true)
    }
    expect(GATEWAY_SITES.map(s => s.id)).toEqual(['production', 'staging', 'internal'])
  })

  it('生产、测试、内部三个域名与需求一致', () => {
    const origins = GATEWAY_SITES.map(s => s.origin)
    expect(origins).toContain('https://llm.kxpms.cn')
    expect(origins).toContain('https://llmgo.kxpms.cn')
    expect(origins).toContain('https://llm.itestu.cn')
  })

  it('siteLoginUrl 跳到目标站点的 /m/login，而不是留在当前站点', () => {
    // 这是切换站点最核心的判据。用相对路径的实现会让这条红。
    const staging = GATEWAY_SITES.find(s => s.id === 'staging')!
    expect(siteLoginUrl(staging)).toBe('https://llmgo.kxpms.cn/m/login')

    setOrigin('https://llm.kxpms.cn')
    const prod = GATEWAY_SITES.find(s => s.id === 'production')!
    expect(siteLoginUrl(prod)).toBe('https://llm.kxpms.cn/m/login')
  })

  it('siteLoginUrl 不带 query/hash，避免把上一站点的 redirect 带过去', () => {
    setOrigin('https://llm.kxpms.cn', '/m/login?redirect=%2Fkeys')
    const internal = GATEWAY_SITES.find(s => s.id === 'internal')!
    expect(siteLoginUrl(internal)).toBe('https://llm.itestu.cn/m/login')
    expect(siteLoginUrl(internal)).not.toContain('redirect')
  })

  it('isCurrentSite 只比 origin —— /m/ 与 /m/login 必须判成同一站点', () => {
    // 带 path 比较会让当前站点永远不高亮（用户看不出自己在哪台机器）。
    const staging = GATEWAY_SITES.find(s => s.id === 'staging')!
    setOrigin('https://llmgo.kxpms.cn')
    expect(isCurrentSite(staging)).toBe(true)
    setOrigin('https://llmgo.kxpms.cn', '/m/login')
    expect(isCurrentSite(staging)).toBe(true)
    setOrigin('https://llmgo.kxpms.cn', '/m/keys')
    expect(isCurrentSite(staging)).toBe(true)
  })

  it('currentSite 在自建站点返回 null，而不是谎报成某个预置站点', () => {
    setOrigin('https://gateway.internal.corp:8443')
    expect(currentSite()).toBeNull()
  })

  it('currentSite 能认出三个预置站点', () => {
    for (const site of GATEWAY_SITES) {
      setOrigin(site.origin)
      expect(currentSite()?.id).toBe(site.id)
    }
  })
})

describe('切换站点的导航行为', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('切换调用的是 location.replace —— 站点切换不该在历史里留退路', () => {
    // 用 assign/push 的话，用户返回时会回到旧站点的页面，
    // 而那个页面的 cookie 已不属于当前站点 ⇒ 一串莫名其妙的 401。
    const replace = vi.fn()
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { origin: 'https://llm.kxpms.cn', replace },
    })
    window.location.replace('https://llmgo.kxpms.cn/m/login')
    expect(replace).toHaveBeenCalledWith('https://llmgo.kxpms.cn/m/login')
  })
})