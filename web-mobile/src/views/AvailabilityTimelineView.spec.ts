import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import AvailabilityTimelineView from './AvailabilityTimelineView.vue'
import { fetchAvailabilityTimeline, AVAILABILITY_TIMELINE_ROW_CAP } from '@/api/probeTimelineCache'
import { setLocale, locale } from '@/i18n'

/**
 * AvailabilityTimelineView 的五条不变量（2026-10-07）。
 *
 * 1. ★★ 撞上写死的 `LIMIT 500` ⇒ 必须说「可能被截断」，不能说「共 N 条，全部如下」；
 * 2. ★★★ `success_rate` 视图里**已经乘过 100** ⇒ 显示时**不能再乘**；
 * 3. ★ `avg_latency_ms` 缺失 = 该小时没有成功探测，不是 0ms；
 * 4. ★ `timeline` 是 nil slice ⇒ 空时是 `null` 不是 `[]`；
 * 5. ★ 本页 model 是**精确匹配**，与 /probe-model 的子串匹配不同 ⇒ 必须说明。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
});

vi.mock('@/api/probeTimelineCache', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/probeTimelineCache')>()
  return { ...actual, fetchAvailabilityTimeline: vi.fn() };
});

const tlMock = fetchAvailabilityTimeline as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(AvailabilityTimelineView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

function point(over: Record<string, unknown> = {}) {
  return {
    raw_model_name: 'gpt-4o',
    outbound_model_name: 'gpt-4o',
    hour_bucket: '2026-10-07T10:00:00Z',
    total_probes: 10,
    successful_probes: 9,
    failed_probes: 1,
    success_rate: 90.0,
    avg_latency_ms: 1200,
    probed_credentials: 3,
    successful_credentials: 2,
    failed_credentials: 1,
    ...over,
  }
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  tlMock.mockResolvedValue({ timeline: [point()], total: 1 })
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) {
    try {
      w.unmount()
    } catch {
      /* ignore */
    }
  }
  mountedList = []
  document.body.innerHTML = ''
  setLocale(ORIGIN_LOCALE as 'zh-CN' | 'en-US')
})

describe('入口与筛选', () => {
  it('挂载即请求一次，不带 model', async () => {
    await mountView()
    expect(tlMock).toHaveBeenCalledTimes(1)
    expect(tlMock.mock.calls[0]![0]).toEqual({})
  })

  it('★ 无条件说明「精确匹配」（与模型健康页的子串匹配不同）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('精确匹配')
  })

  it('填模型后带参数', async () => {
    const w = await mountView()
    await w.find('input.tl__input').setValue('gpt-4o')
    await w.findAll('button').find((b) => b.text() === '筛选')!.trigger('click')
    await flushPromises()
    expect(tlMock.mock.calls[1]![0]).toEqual({ model: 'gpt-4o' })
  })

  it('403 ⇒ 报「没有权限」', async () => {
    tlMock.mockRejectedValue(Object.assign(new Error('forbidden'), { status: 403 }))
    const w = await mountView()
    expect(w.text()).toContain('没有查看可用性时间线的权限')
  })
})

describe('★★★ 判据 1：success_rate 已经是百分数', () => {
  it('★ 90 显示成 90.0%，**不是** 9000%', async () => {
    const w = await mountView()
    expect(w.text()).toContain('90.0%')
    expect(w.text()).not.toContain('9000')
  })

  it('★ 0.9 显示成 0.9%（证明没有多乘 100）', async () => {
    tlMock.mockResolvedValue({ timeline: [point({ success_rate: 0.9 })], total: 1 })
    const w = await mountView()
    expect(w.text()).toContain('0.9%')
    expect(w.text()).not.toContain('90.0%')
  })

  it('100 显示成 100.0%', async () => {
    tlMock.mockResolvedValue({ timeline: [point({ success_rate: 100 })], total: 1 })
    const w = await mountView()
    expect(w.text()).toContain('100.0%')
  })
})

describe('★★ 判据 2：撞上限必须说「可能被截断」', () => {
  it('★ total=500 ⇒ 提示截断，且**不**显示「共 500 个小时桶」', async () => {
    tlMock.mockResolvedValue({ timeline: [point()], total: AVAILABILITY_TIMELINE_ROW_CAP })
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('可能被截断')
    expect(text).not.toContain('共 500 个小时桶')
  })

  it('★ total=499 ⇒ 显示正常计数（证明上一条不是恒真）', async () => {
    tlMock.mockResolvedValue({ timeline: [point()], total: 499 })
    const w = await mountView()
    expect(w.text()).toContain('共 499 个小时桶')
    expect(w.text()).not.toContain('可能被截断')
  })
})

describe('★ 判据 3：avg_latency_ms 缺失 = 没有成功探测', () => {
  it('★ 字段缺失 ⇒ 显示「该小时无成功探测」，不是 0ms', async () => {
    tlMock.mockResolvedValue({ timeline: [point({ avg_latency_ms: undefined })], total: 1 })
    const w = await mountView()
    expect(w.text()).toContain('该小时无成功探测')
    expect(w.text()).not.toContain('0ms')
  })

  it('★ 真的是 0ms ⇒ 显示 0ms（与缺失区分开）', async () => {
    tlMock.mockResolvedValue({ timeline: [point({ avg_latency_ms: 0 })], total: 1 })
    const w = await mountView()
    expect(w.text()).toContain('0ms')
    expect(w.text()).not.toContain('该小时无成功探测')
  })

  it('有值 ⇒ 显示数值', async () => {
    const w = await mountView()
    expect(w.text()).toContain('1200ms')
  })
})

describe('★ 判据 4：nil slice 不崩', () => {
  it('★ timeline=null ⇒ 空态而不是崩溃', async () => {
    tlMock.mockResolvedValue({ timeline: null, total: 0 })
    const w = await mountView()
    expect(w.text()).toContain('近 24 小时内没有探测记录')
  })

  it('空数组 ⇒ 同样空态', async () => {
    tlMock.mockResolvedValue({ timeline: [], total: 0 })
    const w = await mountView()
    expect(w.text()).toContain('近 24 小时内没有探测记录')
  })
})

describe('分组与定性档位', () => {
  it('按模型分组，组标题是模型名', async () => {
    tlMock.mockResolvedValue({
      timeline: [point({ raw_model_name: 'gpt-4o' }), point({ raw_model_name: 'claude' })],
      total: 2,
    })
    const w = await mountView()
    const titles = w.findAll('.tl__group-title').map((e) => e.text())
    expect(titles).toEqual(['gpt-4o', 'claude'])
  })

  it('★ 全成功 ⇒ 成功率显示 success 色', async () => {
    tlMock.mockResolvedValue({
      timeline: [point({ failed_probes: 0, success_rate: 100 })],
      total: 1,
    })
    const w = await mountView()
    expect(w.find('.tl__hour-rate--success').exists()).toBe(true)
  })

  it('有失败 ⇒ warning 色', async () => {
    const w = await mountView()
    expect(w.find('.tl__hour-rate--warning').exists()).toBe(true)
  })

  it('零探测 ⇒ muted 色', async () => {
    tlMock.mockResolvedValue({
      timeline: [point({ total_probes: 0, failed_probes: 0, success_rate: 0 })],
      total: 1,
    })
    const w = await mountView()
    expect(w.find('.tl__hour-rate--muted').exists()).toBe(true)
  })
})
