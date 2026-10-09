import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import FocusLayer from './FocusLayer.vue'

vi.mock('@/hyper', () => ({
  FocusMachine: class {
    async enter() {}
    async exit() {}
    reset() {}
  },
  Hyper: {
    presentOverlay: vi.fn(),
    dismissOverlay: vi.fn(),
  },
  lockScroll: vi.fn(() => vi.fn()),
}))

function touch(type: 'touchstart' | 'touchend', x: number, y: number): Event {
  const event = new Event(type, { bubbles: true, cancelable: true })
  Object.defineProperty(event, 'changedTouches', { value: [{ clientX: x, clientY: y }] })
  return event
}

describe('FocusLayer mobile exit controls', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('exposes an accessible close button that exits the focus layer', async () => {
    const wrapper = mount(FocusLayer, {
      props: { active: true, title: '用量明细' },
      slots: { default: '<div class="table-scroll"><table><tbody><tr><td>row</td></tr></tbody></table></div>' },
      attachTo: document.body,
    })

    const close = document.body.querySelector<HTMLButtonElement>('.focus-layer__exit')
    expect(close).not.toBeNull()
    expect(close?.getAttribute('aria-label')).toBeTruthy()
    close?.click()
    expect(wrapper.emitted('update:active')).toEqual([[false]])

    wrapper.unmount()
  })

  it('exits on a deliberate left swipe outside the horizontally scrollable table', () => {
    const wrapper = mount(FocusLayer, {
      props: { active: true, title: '用量明细' },
      slots: { default: '<div class="table-scroll"><table><tbody><tr><td>row</td></tr></tbody></table></div>' },
      attachTo: document.body,
    })
    const layer = document.body.querySelector<HTMLElement>('.focus-layer')!

    layer.dispatchEvent(touch('touchstart', 220, 40))
    layer.dispatchEvent(touch('touchend', 100, 44))

    expect(wrapper.emitted('update:active')).toEqual([[false]])
    wrapper.unmount()
  })

  it('keeps horizontal table scrolling from closing the fullscreen layer', () => {
    const wrapper = mount(FocusLayer, {
      props: { active: true, title: '用量明细' },
      slots: { default: '<div class="table-scroll"><table><tbody><tr><td>row</td></tr></tbody></table></div>' },
      attachTo: document.body,
    })
    const table = document.body.querySelector<HTMLElement>('.table-scroll')!

    table.dispatchEvent(touch('touchstart', 220, 40))
    table.dispatchEvent(touch('touchend', 100, 44))

    expect(wrapper.emitted('update:active')).toBeUndefined()
    wrapper.unmount()
  })
})
