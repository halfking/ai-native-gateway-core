// useProviderLatency.test.ts — 供应商 HTTP 延时轮询 composable 单元测试
//
// 覆盖：仅 provider 维度拉取 / map 构建（code+name 双 key）/ 非正延时过滤 /
//       静默失败 / groupBy 切换到 provider 时立即拉取 / 生命周期轮询。

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { ref, nextTick, type Ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { useProviderLatency } from './useProviderLatency'
import { fetchProviderLatency } from '../api/provider-probe'
import type { GroupByDimension } from '../types/swimlane'

vi.mock('../api/provider-probe', () => ({
  fetchProviderLatency: vi.fn(),
}))

const mockedFetch = vi.mocked(fetchProviderLatency)

function makeApiRef() {
  return {
    current: null as ReturnType<typeof useProviderLatency> | null,
  }
}

// 挂载 harness 组件以激活 onMounted / onUnmounted / watch 生命周期
function mountHarness(groupBy: Ref<GroupByDimension>, apiRef: { current: ReturnType<typeof useProviderLatency> | null }) {
  const Comp = defineComponent({
    setup() {
      const api = useProviderLatency({ groupBy })
      apiRef.current = api
      return () => h('div', {}, 'harness')
    },
  })
  return mount(Comp)
}

// 用假时钟控制 interval 轮询
function useFakeTimers() {
  vi.useFakeTimers()
}

describe('useProviderLatency', () => {
  let groupBy: Ref<GroupByDimension>
  let apiRef: { current: ReturnType<typeof useProviderLatency> | null }

  beforeEach(() => {
    vi.resetModules()
    groupBy = ref<GroupByDimension>('model')
    apiRef = makeApiRef()
    mockedFetch.mockResolvedValue({ entries: [] })
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.clearAllMocks()
  })

  it('onMounted: does NOT fetch when initial groupBy is not provider', async () => {
    mountHarness(groupBy, apiRef)
    await nextTick()
    expect(mockedFetch).not.toHaveBeenCalled()
  })

  it('onMounted: fetches immediately when initial groupBy is provider', async () => {
    groupBy.value = 'provider'
    mockedFetch.mockResolvedValue({
      entries: [
        { provider_id: 1, provider_name: 'OpenAI', provider_code: 'openai', latency_ms: 120, probed_at: '' },
      ],
    })
    mountHarness(groupBy, apiRef)
    await flushPromises()
    expect(mockedFetch).toHaveBeenCalledTimes(1)
    expect(apiRef.current!.providerLatencyMap.value).toEqual({ openai: 120, OpenAI: 120 })
  })

  it('map uses provider_code primary + provider_name fallback', async () => {
    groupBy.value = 'provider'
    mockedFetch.mockResolvedValue({
      entries: [
        { provider_id: 1, provider_name: 'OpenAI', provider_code: 'openai', latency_ms: 120, probed_at: '' },
        { provider_id: 2, provider_name: 'Anthropic', provider_code: 'anthropic', latency_ms: 300, probed_at: '' },
      ],
    })
    mountHarness(groupBy, apiRef)
    await flushPromises()
    expect(apiRef.current!.providerLatencyMap.value).toEqual({
      openai: 120,
      OpenAI: 120,
      anthropic: 300,
      Anthropic: 300,
    })
  })

  it('skips entries with non-positive latency_ms', async () => {
    groupBy.value = 'provider'
    mockedFetch.mockResolvedValue({
      entries: [
        { provider_id: 1, provider_name: 'OpenAI', provider_code: 'openai', latency_ms: 0, probed_at: '' },
        { provider_id: 2, provider_name: 'Anthropic', provider_code: 'anthropic', latency_ms: -1, probed_at: '' },
        { provider_id: 3, provider_name: 'DeepSeek', provider_code: 'deepseek', latency_ms: 50, probed_at: '' },
      ],
    })
    mountHarness(groupBy, apiRef)
    await flushPromises()
    expect(apiRef.current!.providerLatencyMap.value).toEqual({ deepseek: 50, DeepSeek: 50 })
  })

  it('silently fails when fetch rejects (endpoint may not exist)', async () => {
    groupBy.value = 'provider'
    mockedFetch.mockRejectedValue(new Error('not found'))
    mountHarness(groupBy, apiRef)
    await flushPromises()
    expect(apiRef.current!.providerLatencyMap.value).toEqual({})
  })

  it('groupBy switch to provider triggers immediate fetch', async () => {
    mountHarness(groupBy, apiRef)
    await nextTick()
    expect(mockedFetch).not.toHaveBeenCalled()
    groupBy.value = 'provider'
    await nextTick()
    expect(mockedFetch).toHaveBeenCalledTimes(1)
  })

  it('groupBy switch away from provider does not re-fetch', async () => {
    groupBy.value = 'provider'
    mountHarness(groupBy, apiRef)
    await nextTick()
    expect(mockedFetch).toHaveBeenCalledTimes(1)
    groupBy.value = 'model'
    await nextTick()
    expect(mockedFetch).toHaveBeenCalledTimes(1)
  })

  it('polling: refreshProviderLatency runs on 5-minute interval', async () => {
    useFakeTimers()
    groupBy.value = 'provider'
    const wrapper = mountHarness(groupBy, apiRef)
    await nextTick()
    const callsAfterMount = mockedFetch.mock.calls.length

    // 5 分钟后触发一次轮询
    vi.advanceTimersByTime(5 * 60 * 1000)
    await nextTick()
    expect(mockedFetch.mock.calls.length).toBe(callsAfterMount + 1)

    // 再 5 分钟再触发一次
    vi.advanceTimersByTime(5 * 60 * 1000)
    await nextTick()
    expect(mockedFetch.mock.calls.length).toBe(callsAfterMount + 2)

    wrapper.unmount()
  })

  it('unmount clears the polling timer', async () => {
    useFakeTimers()
    groupBy.value = 'provider'
    const wrapper = mountHarness(groupBy, apiRef)
    await nextTick()
    const callsAfterMount = mockedFetch.mock.calls.length

    wrapper.unmount()
    vi.advanceTimersByTime(5 * 60 * 1000)
    expect(mockedFetch.mock.calls.length).toBe(callsAfterMount)
  })

  it('polling skips fetch when groupBy is not provider (guard inside refresh)', async () => {
    useFakeTimers()
    groupBy.value = 'model'
    const wrapper = mountHarness(groupBy, apiRef)
    await nextTick()

    vi.advanceTimersByTime(5 * 60 * 1000)
    await nextTick()
    expect(mockedFetch).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
