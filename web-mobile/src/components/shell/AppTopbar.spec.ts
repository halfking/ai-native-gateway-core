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

describe('AppTopbar overlay close button (2026-10-10)', () => {
  // 任务原始清单第 4 项：「在弹窗时，导航栏的左边按钮会是一个叉叉，但是点击却
  // 没有效果，需要加上事件，用于关闭弹窗」。
  //
  // ★ 判据：左按钮在 overlay 打开时**必须变 X** 且点击走 onCloseOverlay；
  //        没传 onCloseOverlay 或 overlayActive=false 时退回原菜单/返回路径。
  //        反向：若把 onCloseOverlay 拆出去，本组用例会红。

  async function makeWrapper(props: Record<string, unknown>) {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/', component: { template: '<div />' } },
        { path: '/log-admin', component: { template: '<div />' } },
      ],
    })
    await router.push('/log-admin')
    await router.isReady()
    return mount(AppTopbar, {
      props: {
        onMenu: vi.fn(),
        onAccount: vi.fn(),
        actions: [],
        ...props,
      },
      global: { plugins: [router], stubs: { AppIcon: true } },
    })
  }

  it('overlay 关闭按钮存在 id=topbar-close-overlay 且调用 onCloseOverlay', async () => {
    const onClose = vi.fn()
    const onMenu = vi.fn()
    const w = await makeWrapper({ onCloseOverlay: onClose, overlayActive: true, onMenu })
    expect(w.find('#topbar-close-overlay').exists()).toBe(true)
    expect(w.find('#topbar-menu').exists()).toBe(false)
    await w.find('#topbar-close-overlay').trigger('click')
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(onMenu).not.toHaveBeenCalled()
    w.unmount()
  })

  it('overlay 未激活时退回原菜单按钮', async () => {
    const onClose = vi.fn()
    const onMenu = vi.fn()
    const w = await makeWrapper({ onCloseOverlay: onClose, overlayActive: false, onMenu })
    expect(w.find('#topbar-close-overlay').exists()).toBe(false)
    expect(w.find('#topbar-menu').exists()).toBe(true)
    await w.find('#topbar-menu').trigger('click')
    expect(onMenu).toHaveBeenCalledTimes(1)
    expect(onClose).not.toHaveBeenCalled()
    w.unmount()
  })

  it('overlayActive=true 但未传 onCloseOverlay 时退回原菜单按钮（不报错）', async () => {
    const onMenu = vi.fn()
    const w = await makeWrapper({ overlayActive: true, onMenu })
    expect(w.find('#topbar-close-overlay').exists()).toBe(false)
    expect(w.find('#topbar-menu').exists()).toBe(true)
    await w.find('#topbar-menu').trigger('click')
    expect(onMenu).toHaveBeenCalledTimes(1)
    w.unmount()
  })
})
