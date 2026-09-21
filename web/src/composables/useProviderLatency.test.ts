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
    const wrapper = mountHarness(groupBy, apiRef)
    await nextTick()
    expect(mockedFetch).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('onMounted: fetches immediately when initial groupBy is provider', async () => {
    groupBy.value = 'provider'
    mockedFetch.mockResolvedValue({
      entries: [
        { provider_id: 1, provider_name: 'OpenAI', provider_code: 'openai', latency_ms: 120, probed_at: '' },
      ],
    })
    const wrapper = mountHarness(groupBy, apiRef)
    await flushPromises()
    expect(mockedFetch).toHaveBeenCalledTimes(1)
    expect(apiRef.current!.providerLatencyMap.value).toEqual({ openai: 120, OpenAI: 120 })
    wrapper.unmount()
  })

  it('map uses provider_code primary + provider_name fallback', async () => {
    groupBy.value = 'provider'
    mockedFetch.mockResolvedValue({
      entries: [
        { provider_id: 1, provider_name: 'OpenAI', provider_code: 'openai', latency_ms: 120, probed_at: '' },
        { provider_id: 2, provider_name: 'Anthropic', provider_code: 'anthropic', latency_ms: 300, probed_at: '' },
      ],
    })
    const wrapper = mountHarness(groupBy, apiRef)
    await flushPromises()
    expect(apiRef.current!.providerLatencyMap.value).toEqual({
      openai: 120,
      OpenAI: 120,
      anthropic: 300,
      Anthropic: 300,
    })
    wrapper.unmount()
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
    const wrapper = mountHarness(groupBy, apiRef)
    await flushPromises()
    expect(apiRef.current!.providerLatencyMap.value).toEqual({ deepseek: 50, DeepSeek: 50 })
    wrapper.unmount()
  })

  it('silently fails when fetch rejects (endpoint may not exist)', async () => {
    groupBy.value = 'provider'
    mockedFetch.mockRejectedValue(new Error('not found'))
    const wrapper = mountHarness(groupBy, apiRef)
    await flushPromises()
    expect(apiRef.current!.providerLatencyMap.value).toEqual({})
    wrapper.unmount()
  })

  it('groupBy switch to provider triggers immediate fetch', async () => {
    const wrapper = mountHarness(groupBy, apiRef)
    await nextTick()
    expect(mockedFetch).not.toHaveBeenCalled()
    groupBy.value = 'provider'
    await nextTick()
    expect(mockedFetch).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('groupBy switch away from provider does not re-fetch', async () => {
    groupBy.value = 'provider'
    const wrapper = mountHarness(groupBy, apiRef)
    await nextTick()
    expect(mockedFetch).toHaveBeenCalledTimes(1)
    groupBy.value = 'model'
    await nextTick()
    expect(mockedFetch).toHaveBeenCalledTimes(1)
    wrapper.unmount()
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

  // 2026-09-01 P2-6: visibilitychange 时暂停 5 分钟轮询，避免 idle tab 累积 timer。
  // jsdom 的 visibilitychange 不会从 document bubble 到 window，
  // 监听器注册在 document 上时必须从 document 派发。
  describe('visibility awareness', () => {
    function setVisibility(state: 'visible' | 'hidden') {
      Object.defineProperty(document, 'visibilityState', {
        configurable: true,
        get: () => state,
      })
      Object.defineProperty(document, 'hidden', {
        configurable: true,
        get: () => state === 'hidden',
      })
      document.dispatchEvent(new Event('visibilitychange'))
    }

    afterEach(() => {
      // 还原 visibilityState 默认值
      Object.defineProperty(document, 'visibilityState', {
        configurable: true,
        get: () => 'visible',
      })
      Object.defineProperty(document, 'hidden', {
        configurable: true,
        get: () => false,
      })
    })

    it('hidden event pauses the polling timer (no fetch after 5 min while hidden)', async () => {
      useFakeTimers()
      groupBy.value = 'provider'
      // mockClear：忽略之前测试可能遗留的 fetch 调用（防御性）
      mockedFetch.mockClear()
      mockedFetch.mockResolvedValue({ entries: [] })
      const wrapper = mountHarness(groupBy, apiRef)
      await nextTick()
      const callsAtMount = mockedFetch.mock.calls.length

      setVisibility('hidden')

      // hidden 期间推进 5/10/15 分钟 → 不应该再拉
      vi.advanceTimersByTime(15 * 60 * 1000)
      await nextTick()
      expect(mockedFetch.mock.calls.length).toBe(callsAtMount)

      wrapper.unmount()
    })

    it('hidden → visible restores polling and triggers an immediate fetch', async () => {
      useFakeTimers()
      groupBy.value = 'provider'
      // mockClear：忽略之前测试可能遗留的 fetch 调用（防御性）
      mockedFetch.mockClear()
      mockedFetch.mockResolvedValue({ entries: [] })
      const wrapper = mountHarness(groupBy, apiRef)
      await nextTick()
      const callsAtMount = mockedFetch.mock.calls.length // 1 (onMounted)

      setVisibility('hidden')
      vi.advanceTimersByTime(15 * 60 * 1000)
      await nextTick()
      expect(mockedFetch.mock.calls.length).toBe(callsAtMount)

      // 恢复 visible：应立即拉一次
      setVisibility('visible')
      await flushPromises()
      const callsAfterVisible = mockedFetch.mock.calls.length
      expect(callsAfterVisible).toBe(callsAtMount + 1)

      // 再 5 分钟应继续按节奏轮询
      vi.advanceTimersByTime(5 * 60 * 1000)
      await nextTick()
      expect(mockedFetch.mock.calls.length).toBe(callsAfterVisible + 1)

      wrapper.unmount()
    })

    it('unmount removes the visibilitychange listener (no leak)', async () => {
      groupBy.value = 'provider'
      const addSpy = vi.spyOn(document, 'addEventListener')
      const removeSpy = vi.spyOn(document, 'removeEventListener')

      const wrapper = mountHarness(groupBy, apiRef)
      await nextTick()

      const addCount = addSpy.mock.calls.filter((c) => c[0] === 'visibilitychange').length
      const removeCount = removeSpy.mock.calls.filter((c) => c[0] === 'visibilitychange').length
      expect(addCount).toBeGreaterThanOrEqual(1)
      expect(removeCount).toBe(0)

      wrapper.unmount()

      const addCountAfter = addSpy.mock.calls.filter((c) => c[0] === 'visibilitychange').length
      const removeCountAfter = removeSpy.mock.calls.filter((c) => c[0] === 'visibilitychange').length
      // unmount 必须恰好调用一次 removeEventListener('visibilitychange', ...)
      expect(removeCountAfter - removeCount).toBe(1)
      // unmount 之后 attach 数量不再增长
      expect(addCountAfter).toBe(addCount)

      addSpy.mockRestore()
      removeSpy.mockRestore()
    })
  })
})
