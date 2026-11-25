import { describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import SessionAnalyticsFilterOptionsView from './SessionAnalyticsFilterOptionsView.vue'
import { testI18n } from '../test-utils/i18n'

// SessionAnalyticsFilterOptionsView.test.ts — 批 158a（2026-10-17）
// 关键安全判据：3 列表渲染 + 选中跳路由 + 错误显示。

vi.mock('../api/sessionAnalyticsFilterOptions', () => ({
  listSessionAnalyticsFilterOptions: vi.fn(async () => ({
    clients: ['cli-A', 'cli-B'],
    tasks: ['task-X', 'task-Y', 'task-Z'],
    users: [{ owner: 'user-1', tenant_id: 'tenant-b' }],
    refreshedAt: '2026-10-17T00:00:00.000Z',
    startDate: '2026-10-17',
    endDate: '2026-10-17',
  })),
}))

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/admin/session-analytics/filter-options', component: SessionAnalyticsFilterOptionsView },
      { path: '/admin/session-analytics/clients/:id', component: { template: 'cli' } },
      { path: '/admin/session-analytics/tasks/:id', component: { template: 'task' } },
      { path: '/admin/session-analytics/user-ranking/:owner', name: 'session-analytics-user-detail', component: { template: 'user' } },
    ],
  })
}

describe('SessionAnalyticsFilterOptionsView', () => {
  it('渲染 3 列表', async () => {
    const router = makeRouter()
    router.push('/admin/session-analytics/filter-options')
    await router.isReady()
    const wrapper = mount(SessionAnalyticsFilterOptionsView, {
      global: { plugins: [testI18n, router] },
    })
    await flushPromises()
    expect(wrapper.html()).toContain('cli-A')
    expect(wrapper.html()).toContain('task-X')
    expect(wrapper.html()).toContain('user-1')
  })

  it('点 cli 跳 /clients/:id', async () => {
    const router = makeRouter()
    router.push('/admin/session-analytics/filter-options')
    await router.isReady()
    const wrapper = mount(SessionAnalyticsFilterOptionsView, {
      global: { plugins: [testI18n, router] },
    })
    await flushPromises()
    const btns = wrapper.findAll('button.filter-options-link')
    expect(btns.length).toBe(6) // 2 clients + 3 tasks + 1 user
    await btns[0].trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/admin/session-analytics/clients/cli-A')
    expect(router.currentRoute.value.query).toEqual({ start_date: '2026-10-17', end_date: '2026-10-17' })
  })

  it('keeps the tenant and selected day when opening a user', async () => {
    const router = makeRouter()
    await router.push('/admin/session-analytics/filter-options')
    await router.isReady()
    const wrapper = mount(SessionAnalyticsFilterOptionsView, {
      global: { plugins: [testI18n, router] },
    })
    await flushPromises()
    await wrapper.findAll('button.filter-options-link').at(-1)!.trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/admin/session-analytics/user-ranking/user-1')
    expect(router.currentRoute.value.query).toEqual({ tenant_id: 'tenant-b', start_date: '2026-10-17', end_date: '2026-10-17' })
  })

  it('加载失败显示 error', async () => {
    const { listSessionAnalyticsFilterOptions } = await import('../api/sessionAnalyticsFilterOptions')
    ;(listSessionAnalyticsFilterOptions as any).mockRejectedValueOnce(new Error('boom'))
    const router = makeRouter()
    router.push('/admin/session-analytics/filter-options')
    await router.isReady()
    const wrapper = mount(SessionAnalyticsFilterOptionsView, {
      global: { plugins: [testI18n, router] },
    })
    await flushPromises()
    expect(wrapper.html()).toContain('boom')
  })
})
