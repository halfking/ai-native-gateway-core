import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { Hyper, resetHyperForTests } from '@/hyper'
import HyperApp from './HyperApp.vue'

vi.mock('@/hyper/update/useDeploySeqUpdate', () => ({
  useDeploySeqUpdate: () => ({
    status: { value: 'latest' },
    reason: { value: '' },
    checkCount: { value: 0 },
    lastCheckedAt: { value: null },
    localSeq: { value: 0 },
    start: vi.fn(),
    checkNow: vi.fn(),
  }),
}))

vi.mock('@/composables/useWindowClass', () => ({
  useWindowClass: () => ({ windowClass: { value: 'compact' } }),
}))

describe('HyperApp overlay close wiring', () => {
  beforeEach(() => {
    resetHyperForTests()
  })
  afterEach(() => {
    resetHyperForTests()
  })

  async function mountApp() {
    const router = createRouter({
      history: createMemoryHistory('/m/'),
      routes: [{ path: '/', name: 'home', component: { template: '<div>home</div>' }, meta: { requiresAuth: true } }],
    })
    await router.push('/')
    await router.isReady()
    return mount(HyperApp, {
      global: {
        plugins: [router],
        stubs: {
          AppBottomNav: true,
          AppDrawer: true,
          AppAccountSheet: true,
          AppUpdateBanner: true,
          RouterView: true,
          AppIcon: true,
        },
      },
    })
  }

  it('shows topbar X while a Hyper AppSheet/Focus overlay is registered and closes it', async () => {
    const wrapper = await mountApp()
    expect(wrapper.find('#topbar-close-overlay').exists()).toBe(false)

    const closed = vi.fn()
    Hyper.presentOverlay({
      id: 'sheet-test',
      presentation: 'sheet',
      title: () => '详情',
      close: closed,
    })
    await nextTick()

    expect(wrapper.find('#topbar-close-overlay').exists()).toBe(true)
    await wrapper.find('#topbar-close-overlay').trigger('click')
    expect(closed).toHaveBeenCalledTimes(1)
    expect(Hyper.topOverlay.value).toBeNull()
    await nextTick()
    expect(wrapper.find('#topbar-close-overlay').exists()).toBe(false)
    wrapper.unmount()
  })

  it('opens account sheet on llmgw:open-account (me/account route bridge)', async () => {
    const wrapper = await mountApp()
    window.dispatchEvent(new CustomEvent('llmgw:open-account'))
    await nextTick()
    // remount with real account stub to observe v-model
    wrapper.unmount()
    const router = createRouter({
      history: createMemoryHistory('/m/'),
      routes: [{ path: '/', name: 'home', component: { template: '<div>home</div>' }, meta: { requiresAuth: true } }],
    })
    await router.push('/')
    await router.isReady()
    const w2 = mount(HyperApp, {
      global: {
        plugins: [router],
        stubs: {
          AppBottomNav: true,
          AppDrawer: true,
          AppUpdateBanner: true,
          RouterView: true,
          AppIcon: true,
          AppAccountSheet: defineComponent({
            props: { modelValue: { type: Boolean, default: false } },
            setup(props) {
              return () => h('div', { 'data-testid': 'account', 'data-open': String(props.modelValue) })
            },
          }),
        },
      },
    })
    await flushPromises()
    window.dispatchEvent(new CustomEvent('llmgw:open-account'))
    await nextTick()
    expect(w2.find('[data-testid=account]').attributes('data-open')).toBe('true')
    w2.unmount()
  })
})
