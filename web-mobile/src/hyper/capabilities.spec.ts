import { describe, expect, it } from 'vitest'
import { resolveCapabilities, webCapabilities } from './capabilities'

describe('capabilities 协商（08 §3 / 17 §4-R3）', () => {
  it('Web 基线：back=commit-only，原生能力全 false', () => {
    const caps = webCapabilities()
    expect(caps.protocolVersion).toBe(2)
    expect(caps.platform).toBe('web')
    expect(caps.navigation).toBe(true)
    expect(caps.back).toBe('commit-only')
    expect(caps.focusWorkspace).toBe(true)
    expect(caps.insets).toBe('css-only')
    expect(caps.keyboard).toBe('viewport-only')
    expect(caps.haptics).toBe(false)
    expect(caps.appLifecycle).toBe(false)
    expect(caps.tasks.durableLocal).toBe(false)
    expect(caps.tasks.cloudDetached).toBe(true)
    expect(caps.recording.available).toBe(false)
    expect(caps.recognition.ocr).toBe('none')
    expect(caps.agent.available).toBe(false)
  })

  it('resolveCapabilities 无壳时回落 web 基线（不以 window.Shell 推能力）', () => {
    expect(resolveCapabilities()).toEqual(webCapabilities())
    ;(window as unknown as Record<string, unknown>).Shell = { platform: 'ios' }
    try {
      // 存在 Shell 不证明能力（14 §2 四步验证）
      expect(resolveCapabilities()).toEqual(webCapabilities())
    } finally {
      delete (window as unknown as Record<string, unknown>).Shell
    }
  })
})
