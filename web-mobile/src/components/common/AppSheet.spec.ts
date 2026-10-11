import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { h, nextTick } from 'vue'
import { setLocale } from '@/i18n'
import AppSheet from './AppSheet.vue'

const closeOverlay = vi.hoisted(() => vi.fn())
const topOverlay = vi.hoisted(() => ({ value: false }))
const presentOverlay = vi.hoisted(() => vi.fn(() => { topOverlay.value = true }))

vi.mock('@/hyper', () => ({
  useHyperOverlay: () => ({
    isTop: { get value() { return topOverlay.value } },
    present: presentOverlay,
    close: closeOverlay,
    release: vi.fn(),
  }),
}))

describe('AppSheet mobile focus behavior', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    vi.unstubAllGlobals()
    closeOverlay.mockClear()
    presentOverlay.mockClear()
    topOverlay.value = false
    setLocale('en-US')
  })

  it('registers a sheet that is mounted already open', async () => {
    const wrapper = mount(AppSheet, {
      props: { modelValue: true, title: '供应商详情' },
      attachTo: document.body,
    })

    await nextTick()

    expect(presentOverlay).toHaveBeenCalledTimes(1)
    expect(wrapper.find('.app-sheet--inactive').exists()).toBe(false)
    wrapper.unmount()
  })

  it('does not focus a text/select control when opened on a touch device', async () => {
    setLocale('zh-CN')
    vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: true, addEventListener: vi.fn(), removeEventListener: vi.fn() })))
    const focused: HTMLElement[] = []
    vi.spyOn(HTMLElement.prototype, 'focus').mockImplementation(function (this: HTMLElement) {
      focused.push(this)
    })
    const wrapper = mount(AppSheet, {
      props: { modelValue: false, title: '筛选条件' },
      slots: {
        default: () => h('div', [
          h('input', { id: 'search-choice' }),
          h('select', { id: 'choice' }, [h('option', '全部')]),
        ]),
      },
      attachTo: document.body,
    })

    await wrapper.setProps({ modelValue: true })
    await nextTick()
    await flushPromises()

    expect(document.body.querySelector('#search-choice')).not.toBeNull()
    expect(focused).toHaveLength(1)
    expect(focused[0]).toBe(document.body.querySelector('.app-sheet__close'))

    wrapper.unmount()
  })

  it('wires the close icon to the overlay close action', async () => {
    const wrapper = mount(AppSheet, {
      props: { modelValue: false, title: '设置' },
      attachTo: document.body,
    })

    await wrapper.setProps({ modelValue: true })
    await nextTick()
    document.body.querySelector<HTMLButtonElement>('.app-sheet__close')?.click()

    expect(closeOverlay).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('does not let a lower sheet close when its scrim receives a nested-overlay gesture', async () => {
    const wrapper = mount(AppSheet, {
      props: { modelValue: false, title: '账户设置' },
      attachTo: document.body,
    })

    await wrapper.setProps({ modelValue: true })
    topOverlay.value = false
    document.body.querySelector<HTMLElement>('.app-sheet__scrim')?.dispatchEvent(new MouseEvent('click', { bubbles: true }))

    expect(closeOverlay).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
