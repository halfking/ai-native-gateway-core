import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import ProxyView from './ProxyView.vue'

const getProxyStatusMock = vi.fn()
const getProxySubscriptionsMock = vi.fn()
const getProxyNodesMock = vi.fn()

vi.mock('../api', () => ({
  getProxyStatus: (...args: any[]) => getProxyStatusMock(...args),
  getProxySubscriptions: (...args: any[]) => getProxySubscriptionsMock(...args),
  getProxyNodes: (...args: any[]) => getProxyNodesMock(...args),
  createProxySubscription: vi.fn(),
  refreshProxySubscription: vi.fn(),
  deleteProxySubscription: vi.fn(),
  createProxyNode: vi.fn(),
  healthCheckProxyNode: vi.fn(),
  deleteProxyNode: vi.fn(),
}))

const i18n = createI18n({
  legacy: false,
  locale: 'en-US',
  messages: {
    'en-US': {
      common: { loading: 'Loading', refresh: 'Refresh', actions: 'Actions', never: 'Never', delete: 'Delete', yes: 'Yes', no: 'No', create: 'Create', cancel: 'Cancel' },
      proxy: {
        title: 'Proxy',
        tabs: { status: 'Status', subscriptions: 'Subscriptions', nodes: 'Nodes' },
        error: { loadStatusFailed: 'Status failed', loadSubsFailed: 'Subscriptions failed', loadNodesFailed: 'Nodes failed' },
        status: { overview: 'Overview', subscriptions: 'Subscriptions', nodes: 'Nodes', dialable: 'Dialable', healthy: 'Healthy', unhealthy: 'Unhealthy', selectedNode: 'Selected node', noNodeSelected: 'No node' },
        node: { name: 'Name', protocol: 'Protocol', server: 'Server', latency: 'Latency', dialable: 'Dialable', status: 'Status', failures: 'Failures' },
        subscriptions: { create: 'Create subscription', name: 'Name', url: 'URL', nodeCount: 'Nodes', status: 'Status', lastFetch: 'Last fetch', empty: 'No subscriptions', refresh: 'Refresh', createTitle: 'Create subscription', notes: 'Notes', urlHint: '' },
        nodes: { create: 'Create node', healthCheck: 'Health check', empty: 'No nodes', createTitle: 'Create node', subscription: 'Subscription', selectSubscription: 'Select', protocolHint: '' },
      },
    },
  },
})

const status = {
  subscription_count: 1,
  active_subscriptions: 1,
  node_count: 1,
  by_protocol: {},
  dialable_count: 1,
  unhealthy_count: 0,
  healthy_count: 1,
  selected_node: null,
  selection_error: '',
  warning: '',
}

const subscription = {
  id: 1,
  name: 'Subscription',
  subscribe_url: 'https://example.com/sub',
  status: 'active',
  node_count: 0,
  last_fetch_at: null,
}

const node = {
  id: 2,
  name: 'Node',
  protocol: 'http',
  server: '127.0.0.1',
  port: 8080,
  dialable: true,
  status: 'active',
  response_time_ms: 1,
  consecutive_failures: 0,
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

function mountView() {
  return mount(ProxyView, { global: { plugins: [i18n] } })
}

describe('ProxyView resilience', () => {
  beforeEach(() => {
    getProxyStatusMock.mockReset()
    getProxySubscriptionsMock.mockReset()
    getProxyNodesMock.mockReset()
    getProxyStatusMock.mockResolvedValue(status)
    getProxySubscriptionsMock.mockResolvedValue([])
    getProxyNodesMock.mockResolvedValue([])
  })

  it('renders valid rows without crashing when list responses contain invalid items', async () => {
    getProxySubscriptionsMock.mockResolvedValue([null, { ...subscription, name: 42 }, subscription])
    getProxyNodesMock.mockResolvedValue([null, { ...node, port: '8080' }, node])

    const wrapper = mountView()
    await flushPromises()

    await wrapper.findAll('.tabs button')[1].trigger('click')
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
    expect(wrapper.text()).toContain('Subscription')

    await wrapper.findAll('.tabs button')[2].trigger('click')
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
    expect(wrapper.text()).toContain('Node')
  })

  it('shows empty states when every returned item is invalid', async () => {
    getProxySubscriptionsMock.mockResolvedValue([null, { ...subscription, name: 42 }])
    getProxyNodesMock.mockResolvedValue([null, { ...node, id: 'not-a-number' }])

    const wrapper = mountView()
    await flushPromises()

    await wrapper.findAll('.tabs button')[1].trigger('click')
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
    expect(wrapper.text()).toContain('No subscriptions')
    expect(wrapper.text()).not.toContain('42')

    await wrapper.findAll('.tabs button')[2].trigger('click')
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
    expect(wrapper.text()).toContain('No nodes')
    expect(wrapper.text()).not.toContain('not-a-number')
  })

  it('keeps other tabs renderable when one initialization request fails', async () => {
    getProxyStatusMock.mockRejectedValue(new Error('status unavailable'))
    getProxySubscriptionsMock.mockResolvedValue([subscription])
    getProxyNodesMock.mockResolvedValue([node])

    const wrapper = mountView()
    await flushPromises()

    await wrapper.findAll('.tabs button')[1].trigger('click')
    expect(wrapper.text()).toContain('Subscription')
    expect(wrapper.find('.error-banner').exists()).toBe(false)

    await wrapper.findAll('.tabs button')[2].trigger('click')
    expect(wrapper.text()).toContain('Node')
  })

  it('does not let concurrent tab requests overwrite each other loading state', async () => {
    const statusRequest = deferred<typeof status>()
    const subscriptionsRequest = deferred<any[]>()
    getProxyStatusMock.mockReturnValue(statusRequest.promise)
    getProxySubscriptionsMock.mockReturnValue(subscriptionsRequest.promise)

    const wrapper = mountView()
    await wrapper.findAll('.tabs button')[1].trigger('click')
    expect(wrapper.find('.loading').exists()).toBe(true)

    statusRequest.resolve(status)
    await flushPromises()
    expect(wrapper.find('.loading').exists()).toBe(true)

    subscriptionsRequest.resolve([])
    await flushPromises()
    expect(wrapper.find('.loading').exists()).toBe(false)
  })

  it('keeps loading visible until all overlapping requests for the same tab finish', async () => {
    const firstRequest = deferred<any[]>()
    const secondRequest = deferred<any[]>()
    getProxySubscriptionsMock
      .mockReturnValueOnce(firstRequest.promise)
      .mockReturnValueOnce(secondRequest.promise)

    const wrapper = mountView()
    await wrapper.findAll('.tabs button')[1].trigger('click')
    expect(wrapper.find('.loading').exists()).toBe(true)

    await wrapper.find('.actions .btn-secondary').trigger('click')
    expect(getProxySubscriptionsMock).toHaveBeenCalledTimes(2)

    firstRequest.resolve([])
    await flushPromises()
    expect(wrapper.find('.loading').exists()).toBe(true)

    secondRequest.resolve([])
    await flushPromises()
    expect(wrapper.find('.loading').exists()).toBe(false)
  })
})
