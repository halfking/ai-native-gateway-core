import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { setLocale } from '@/i18n'
import DataTableFocus from './DataTableFocus.vue'

const viewport = vi.hoisted(() => ({ value: 'compact' }))

vi.mock('@/composables/useWindowClass', () => ({
  useWindowClass: () => ({ windowClass: { value: viewport.value } }),
}))

vi.mock('./FocusLayer.vue', () => ({
  default: {
    props: ['active', 'title'],
    emits: ['update:active'],
    template: '<section v-if="active" role="dialog" :aria-label="title"><button @click="$emit(\'update:active\', false)">关闭</button><slot /></section><div v-else><slot /></div>',
  },
}))

describe('DataTableFocus', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    viewport.value = 'compact'
    setLocale('en-US')
  })

  it('offers a fullscreen entry on compact widths and preserves the table content', async () => {
    setLocale('zh-CN')
    const wrapper = mount(DataTableFocus, {
      props: { title: '用量明细' },
      slots: { default: '<table><tbody><tr><td>模型 A</td></tr></tbody></table>' },
    })

    const open = wrapper.find('button[aria-label="专注查看"]')
    expect(open.exists()).toBe(true)
    expect(wrapper.text()).toContain('模型 A')
    await open.trigger('click')
    expect(wrapper.find('[role="dialog"][aria-label="用量明细"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('does not expose the fullscreen entry at medium and larger widths', () => {
    setLocale('zh-CN')
    viewport.value = 'medium'
    const wrapper = mount(DataTableFocus, {
      props: { title: '用量明细' },
      slots: { default: '<table><tbody><tr><td>模型 A</td></tr></tbody></table>' },
    })

    expect(wrapper.find('button[aria-label="专注查看"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
