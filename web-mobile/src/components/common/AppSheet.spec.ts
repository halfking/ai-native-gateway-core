import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { h, nextTick } from 'vue'
import { setLocale } from '@/i18n'
import AppSheet from './AppSheet.vue'

vi.mock('@/hyper', () => ({
  useHyperOverlay: () => ({
    present: vi.fn(),
    close: vi.fn(),
    release: vi.fn(),
  }),
}))

describe('AppSheet mobile focus behavior', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    vi.unstubAllGlobals()
    setLocale('en-US')
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
})
