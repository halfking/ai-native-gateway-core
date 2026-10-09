import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import AppTopbar from './AppTopbar.vue'

const hyperMock = vi.hoisted(() => ({
  title: { value: { title: '日志管理' } },
  back: vi.fn(),
}))
vi.mock('@/hyper', () => ({
  Hyper: hyperMock,
}))

describe('AppTopbar page actions', () => {
  it('renders a labelled page action in the right-side action area', async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/', component: { template: '<div />' } },
        { path: '/log-admin', component: { template: '<div />' } },
      ],
    })
    await router.push('/log-admin')
    await router.isReady()
    const refresh = vi.fn()
    const wrapper = mount(AppTopbar, {
      props: {
        onMenu: vi.fn(),
        onAccount: vi.fn(),
        actions: [{ key: 'refresh', label: '刷新', icon: 'refresh', onClick: refresh }],
      },
      global: { plugins: [router], stubs: { AppIcon: true } },
    })

    const action = wrapper.find('button.topbar__action[aria-label="刷新"]')
    expect(action.exists()).toBe(true)
    await action.trigger('click')
    expect(refresh).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })
})
