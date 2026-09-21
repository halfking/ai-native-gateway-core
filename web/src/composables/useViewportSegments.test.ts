// useViewportSegments.test.ts — jsdom 下特性检测不支持的降级路径。
import { describe, expect, it } from 'vitest'
import { useViewportSegments } from './useViewportSegments'
import { mount } from '@vue/test-utils'

const Host = {
  template: '<div />',
  setup() {
    const vp = useViewportSegments()
    return { vp }
  },
}

describe('useViewportSegments', () => {
  it('jsdom 无 viewport.segments：supported=false、segments 恒空、isSpanning 恒 false', () => {
    const w = mount(Host)
    const vp = (w.vm.vp as ReturnType<typeof useViewportSegments>)
    expect(vp.supported.value).toBe(false)
    expect(vp.segments.value).toEqual([])
    expect(vp.isSpanning.value).toBe(false)
  })

  it('window resize 触发重读且保持零值降级（无异常抛出）', async () => {
    const w = mount(Host)
    window.dispatchEvent(new Event('resize'))
    await w.vm.$nextTick()
    const vp = (w.vm.vp as ReturnType<typeof useViewportSegments>)
    expect(vp.isSpanning.value).toBe(false)
    w.unmount()
  })
})
