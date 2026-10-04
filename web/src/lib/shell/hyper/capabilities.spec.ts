/**
 * hyper/capabilities.spec.ts — 能力协商门禁（docs/UI规范/00 §5.4 · H1）。
 *
 * 这些用例的存在是因为**能力谎报**是 Hyper 最危险的失败模式：
 * UI 渲染了一个原生入口，但桥其实不通，用户一点就静默失败。
 * 规范要求「失败时按普通 Web 降级」，所以每一关都必须 fail-closed。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  PROTOCOL_VERSION,
  WEB_CAPABILITIES,
  can,
  capabilities,
  detectCapabilities,
  hasInteractiveBack,
  hyperMode,
  isTrustedOrigin,
  onCapabilitiesChange,
  setCapabilities,
  _resetCapabilities,
} from './capabilities'

type CapacitorLike = { isNativePlatform: () => boolean; getPlatform?: () => string }

function installNative(on: boolean, platform = 'android'): void {
  const g: CapacitorLike = { isNativePlatform: () => on, getPlatform: () => platform }
  Object.defineProperty(window, 'Capacitor', { writable: true, configurable: true, value: g })
}

function clearNative(): void {
  delete (window as unknown as { Capacitor?: unknown }).Capacitor
}

/** jsdom 的 location.origin 是 http://localhost:3000 之类，测试里取实际值。 */
const ACTUAL_ORIGIN = typeof window !== 'undefined' ? window.location.origin : ''

beforeEach(() => {
  _resetCapabilities()
  clearNative()
})

afterEach(() => {
  _resetCapabilities()
  clearNative()
})

describe('hyperMode ≠ capabilities', () => {
  it('hyperMode 只表示交互形态，与原生能力无关', () => {
    expect(hyperMode({ inShell: false, isCompact: true })).toBe(true)
    expect(hyperMode({ inShell: true, isCompact: false })).toBe(true)
    expect(hyperMode({ inShell: false, isCompact: false })).toBe(false)
    // 形态为 true 但能力全无 —— 这正是要防的误判
    expect(capabilities()).toEqual(WEB_CAPABILITIES)
    expect(can(capabilities(), 'haptics')).toBe(false)
  })
})

describe('detectCapabilities：fail-closed', () => {
  it('非原生环境直接给 Web 基线，不做任何桥调用', async () => {
    const handshake = vi.fn()
    const c = await detectCapabilities({ handshake })
    expect(c).toEqual(WEB_CAPABILITIES)
    expect(handshake).not.toHaveBeenCalled()
  })

  it('在壳内但没有握手实现 → 降级为 web，不谎称任何原生能力', async () => {
    installNative(true)
    const c = await detectCapabilities()
    expect(c.platform).toBe('web')
    expect(c.back).toBe('commit-only')
    expect(c.haptics).toBe(false)
    expect(c.appLifecycle).toBe(false)
  })

  it('握手抛错 → Web 降级', async () => {
    installNative(true)
    const c = await detectCapabilities({
      handshake: () => Promise.reject(new Error('no bridge')),
    })
    expect(c.platform).toBe('web')
  })

  it('握手超时 → Web 降级（不让 UI 一直等）', async () => {
    installNative(true)
    const c = await detectCapabilities({ handshake: () => new Promise(() => {}), timeoutMs: 30 })
    expect(c.platform).toBe('web')
  })

  it('协议版本不兼容 → Web 降级（不猜）', async () => {
    installNative(true)
    const c = await detectCapabilities({
      handshake: () => Promise.resolve({ protocolVersion: 99, platform: 'android', trustedOrigin: ACTUAL_ORIGIN }),
    })
    expect(c.protocolVersion).toBe(PROTOCOL_VERSION)
    expect(c.platform).toBe('web')
  })

  it('可信 origin 不匹配 → 即使桥通也不授予能力（外站/iframe 不继承）', async () => {
    installNative(true)
    const c = await detectCapabilities({
      handshake: () =>
        Promise.resolve({
          protocolVersion: PROTOCOL_VERSION,
          platform: 'android',
          back: 'interactive',
          haptics: true,
          trustedOrigin: 'https://evil.example.com',
        }),
    })
    expect(c.platform).toBe('web')
    expect(c.back).not.toBe('interactive')
    expect(c.haptics).toBe(false)
  })

  it('壳未声明 trustedOrigin → 不授予', async () => {
    installNative(true)
    const c = await detectCapabilities({
      handshake: () => Promise.resolve({ protocolVersion: PROTOCOL_VERSION, platform: 'ios' }),
    })
    expect(c.platform).toBe('web')
  })

  it('四关全过才授予能力', async () => {
    installNative(true)
    const c = await detectCapabilities({
      handshake: () =>
        Promise.resolve({
          protocolVersion: PROTOCOL_VERSION,
          platform: 'android',
          back: 'commit-only',
          insets: 'native-css-px',
          keyboard: 'native',
          haptics: true,
          appLifecycle: true,
          shellVersion: '1.0.0',
          trustedOrigin: ACTUAL_ORIGIN,
        }),
    })
    expect(c).toMatchObject({
      platform: 'android',
      back: 'commit-only',
      insets: 'native-css-px',
      keyboard: 'native',
      haptics: true,
      appLifecycle: true,
      shellVersion: '1.0.0',
    })
  })

  /**
   * R3（2026-10-04 吸收）：壳**谎报全部 R3 能力**时，未实现的那五项仍必须报「不可用」。
   *
   * 这条是 §4.6.5 评估 R3 时最要紧的一处：`detect()` 走的是**逐字段显式投影**，
   * 不是盲信 handshake 的 claim。若哪天有人图省事改成 `{ ...WEB, ...claimed }`，
   * 一个声称有 OCR/录音/Agent 的壳就会让 UI 渲染出**点了没反应**的原生入口 ——
   * 那正是本文件开头写的「能力谎报是 Hyper 最危险的失败模式」。
   * ⇒ 门禁（`gates.spec.ts` 的 R3 那条）只查 `WEB_CAPABILITIES` 静态值，
   *    **查不到壳声明路径**；这条行为测试才是那个路径的钉子。
   */
  it('★ 壳谎报全部 R3 能力时，未实现的五项仍报「不可用」', async () => {
    installNative(true)
    const c = await detectCapabilities({
      handshake: () =>
        Promise.resolve({
          protocolVersion: PROTOCOL_VERSION,
          platform: 'android',
          navigation: true,
          back: 'interactive',
          focusWorkspace: true,
          insets: 'native-css-px',
          keyboard: 'native',
          haptics: true,
          appLifecycle: true,
          tasks: { durableLocal: true, cloudDetached: true, continuation: 'osScheduled' as const },
          recording: { available: true, background: true },
          recognition: { pdfText: true, ocr: 'accurate' as const, asr: 'fast' as const },
          agent: { available: true, skillFormat: 'md' },
          shellVersion: '1.0.0',
          trustedOrigin: ACTUAL_ORIGIN,
        }),
    })
    // 前端**确实实现了**的那些，如实采信壳的值：
    expect(c.back).toBe('interactive')
    expect(c.haptics).toBe(true)
    // 但这五项本专题**没有任何代码** ⇒ 必须强制「不可用」，不采信 claim。
    expect(c.focusWorkspace).toBe(false)
    expect(c.tasks).toEqual({ durableLocal: false, cloudDetached: false, continuation: 'foregroundOnly' })
    expect(c.recording).toEqual({ available: false, background: false })
    expect(c.recognition).toEqual({ pdfText: false, ocr: 'none', asr: 'none' })
    expect(c.agent).toEqual({ available: false, skillFormat: '' })
  })

  it('平台名不认识时归 web，不猜', async () => {
    installNative(true)
    const c = await detectCapabilities({
      handshake: () =>
        Promise.resolve({
          protocolVersion: PROTOCOL_VERSION,
          platform: 'symbian' as unknown as 'ios',
          trustedOrigin: ACTUAL_ORIGIN,
        }),
    })
    expect(c.platform).toBe('web')
  })
})

describe('isTrustedOrigin：精确匹配', () => {
  it('scheme+host+port 全等才通过', () => {
    expect(isTrustedOrigin('https://a.com', 'https://a.com')).toBe(true)
    expect(isTrustedOrigin('https://a.com', 'http://a.com')).toBe(false)
    expect(isTrustedOrigin('https://a.com:8443', 'https://a.com')).toBe(false)
    expect(isTrustedOrigin('https://a.com', 'https://a.com.evil.net')).toBe(false)
    expect(isTrustedOrigin('https://evil.a.com', 'https://a.com')).toBe(false)
  })

  it('任一侧为空 → 不通过', () => {
    expect(isTrustedOrigin(undefined, 'https://a.com')).toBe(false)
    expect(isTrustedOrigin('https://a.com', '')).toBe(false)
  })
})

describe('能力快照与订阅', () => {
  it('setCapabilities 通知订阅者，unsubscribe 后不再通知', () => {
    const fn = vi.fn()
    const off = onCapabilitiesChange(fn)
    setCapabilities({ ...WEB_CAPABILITIES, haptics: true })
    expect(fn).toHaveBeenCalledTimes(1)
    off()
    setCapabilities({ ...WEB_CAPABILITIES, haptics: false })
    expect(fn).toHaveBeenCalledTimes(1)
  })

  it('订阅者抛错不阻断其他订阅者', () => {
    const good = vi.fn()
    onCapabilitiesChange(() => {
      throw new Error('bad')
    })
    onCapabilitiesChange(good)
    expect(() => setCapabilities(WEB_CAPABILITIES)).not.toThrow()
    expect(good).toHaveBeenCalled()
  })

  it('hasInteractiveBack 只在 interactive 为真（commit-only 不算）', () => {
    expect(hasInteractiveBack({ ...WEB_CAPABILITIES, back: 'none' })).toBe(false)
    expect(hasInteractiveBack({ ...WEB_CAPABILITIES, back: 'commit-only' })).toBe(false)
    expect(hasInteractiveBack({ ...WEB_CAPABILITIES, back: 'interactive' })).toBe(true)
  })
})
