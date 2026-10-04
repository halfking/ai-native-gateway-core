import { describe, expect, it } from 'vitest'
import { getCapabilities, resetCapabilitiesCache, webBaselineCapabilities } from './capabilities'

describe('capabilities v2（R3）', () => {
  it('Web 基线：back=commit-only、insets=css-only、原生项全 false', () => {
    const cap = webBaselineCapabilities()
    expect(cap.protocolVersion).toBe(2)
    expect(cap.platform).toBe('web')
    expect(cap.back).toBe('commit-only')
    expect(cap.insets).toBe('css-only')
    expect(cap.keyboard).toBe('viewport-only')
    expect(cap.haptics).toBe(false)
    expect(cap.tasks.durableLocal).toBe(false)
    expect(cap.recognition.ocr).toBe('none')
  })

  it('宿主注入白名单合并；未知字段忽略、未知能力默认不可用', () => {
    resetCapabilitiesCache()
    ;(globalThis as Record<string, unknown>)['__HYPER_CAPABILITIES__'] = {
      platform: 'ios',
      haptics: true,
      unknownField: 'evil',
      tasks: { durableLocal: true },
    }
    const cap = getCapabilities()
    expect(cap.platform).toBe('ios')
    expect(cap.haptics).toBe(true)
    expect(cap.tasks.durableLocal).toBe(true)
    // 未注入嵌套键保持基线 false。
    expect(cap.tasks.cloudDetached).toBe(false)
    expect(cap.recognition.ocr).toBe('none')
    expect((cap as unknown as Record<string, unknown>)['unknownField']).toBeUndefined()
    delete (globalThis as Record<string, unknown>)['__HYPER_CAPABILITIES__']
    resetCapabilitiesCache()
  })
})
