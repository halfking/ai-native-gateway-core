import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { store } from '../../../store'
import en from '../../../locales/en-US/errorPlaybook'
import ErrorPlaybookListView from './ErrorPlaybookListView.vue'

const { listExecutions } = vi.hoisted(() => ({ listExecutions: vi.fn() }))

vi.mock('../../../api/errorPlaybook', () => ({
  listEntries: vi.fn().mockResolvedValue({ entries: [], total: 0 }),
  getStats: vi.fn().mockResolvedValue({ engine_wired: true }),
  listExecutions,
}))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('../../../store', async () => {
  const { reactive } = await import('vue')
  return { store: reactive({ userInfo: { role: 'super_admin', tenant_id: 'default' } }) }
})

function openPlaybook() {
  return mount(ErrorPlaybookListView, {
    global: {
      plugins: [createI18n({ legacy: false, locale: 'en', messages: { en: { errorPlaybook: en } } })],
      stubs: {
        PageHeader: true,
        DataTable: { template: '<div><slot /></div>' },
        PaginationBar: true,
        StatsRow: true,
        SaStateBanner: true,
        PlanEditor: true,
      },
    },
  })
}

describe('Error playbook cooldown suggestions', () => {
  beforeEach(() => {
    listExecutions.mockReset().mockResolvedValue({
      executions: [{
        id: 1,
        executed_by: 'plugin:upstream-cooldown',
        context: {
          schema_version: 1,
          tenant_id: 'tenant-a',
          provider_id: 7,
          window_start_unix: 1795611900,
          count_429: 3,
          count_502: 0,
          distinct_requests: 3,
          recommendation: 'review_next_model_ladder_step',
          cooldown_seconds: 300,
          template_version: '1.0.0',
        },
      }],
      total: 1,
    })
  })

  it('shows a platform operator the aggregate and read-only suggestion', async () => {
    const wrapper = openPlaybook()
    await flushPromises()
    expect(listExecutions).toHaveBeenCalledWith({
      recommendations: 'only', status: 'awaiting_approval', limit: 20,
    })
    const recommendation = wrapper.get('section')
    expect(recommendation.text()).toContain('tenant-a / #7')
    expect(recommendation.text()).toContain('Consider a 300-second cooldown')
    expect(recommendation.find('button').exists()).toBe(false)
    wrapper.unmount()
  })

  it('displays SSE and config recommendations together without action buttons', async () => {
    listExecutions.mockResolvedValue({
      executions: [
        {
          id: 2,
          executed_by: 'plugin:sse-break-analysis',
          context: {
            schema_version: 1, template_version: '1.0.0', tenant_id: 'tenant-b',
            provider_id: 7, window_start_unix: 1795611900, distinct_requests: 3,
            upstream: 0, idle_timeout: 3, network: 0,
            recommendation: 'review_proxy_idle_and_upstream_flush',
          },
        },
        {
          id: 3,
          executed_by: 'plugin:config-drift',
          created_at: '2026-11-25T13:35:00Z',
          context: {
            schema_version: 1, template_version: '1.0.0',
            recommendation: 'compare_with_last_accepted_configuration',
          },
        },
      ],
      total: 2,
    })
    const wrapper = openPlaybook()
    await flushPromises()
    const recommendations = wrapper.get('section')
    expect(recommendations.text()).toContain('Idle 3')
    expect(recommendations.text()).toContain('Check proxy idle timeouts and upstream flush intervals.')
    expect(recommendations.text()).toContain('Compare with the last accepted configuration')
    expect(recommendations.find('button').exists()).toBe(false)
    wrapper.unmount()
  })

  it('does not fetch proposals for a tenant-scoped admin', async () => {
    const user = store.userInfo!
    user.tenant_id = 'tenant-a'
    try {
      const wrapper = openPlaybook()
      await flushPromises()
      expect(listExecutions).not.toHaveBeenCalled()
      expect(wrapper.find('section').exists()).toBe(false)
      wrapper.unmount()
    } finally {
      user.tenant_id = 'default'
    }
  })
})
