import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ProbeView from './ProbeView.vue'
import { fetchProbeQueueTasks, fetchProviderLatency } from '@/api/probeOps'
import { setLocale, locale } from '@/i18n'

/**
 * ProbeView 的两条不变量：
 *
 * 1. **两个端点分别记错误**。桌面端这里是 `Promise.all` + `.catch(() => null)`
 *    静默吞掉（见 fetchDispatchWaterfall 旁的同款写法）。移动端不照抄：
 *    queue-tasks 挂了不代表 provider-latency 挂了，合并成一个 error 会把
 *    「一段挂了」显示成「整页都挂了」。
 * 2. **「没出现 ≠ 不存在」必须在空态旁边说**。provider-latency 只统计
 *    1 小时内 direct_ok 且延时>0 的记录；不说清楚，运维会把
 *    「最近没探测成功」读成「这个供应商没了」。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/probeOps', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/probeOps')>()
  // ★ 这个 `});` 后面必须显式分号：下一行以 return 开头，
  //   缺分号会被 ASI 拼成 `})(return …)` 而报一个与真实原因无关的解析错。
  return { ...actual, fetchProbeQueueTasks: vi.fn(), fetchProviderLatency: vi.fn() };
})

const qt = fetchProbeQueueTasks as unknown as ReturnType<typeof vi.fn>
const pl = fetchProviderLatency as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(ProbeView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  // ★ 显式钉 zh-CN：jsdom 的 navigator.language 是 en-US（第四次踩，见 §11.29(3)）
  setLocale('zh-CN')
  vi.clearAllMocks()
  qt.mockResolvedValue({ tasks: [], total: 0 })
  pl.mockResolvedValue({ entries: [], total: 0 })
  for (const w of mountedList) {
    try {
      w.unmount()
    } catch {
      /* ignore */
    }
  }
  mountedList = []
  document.body.innerHTML = ''
})
afterEach(() => {
  setLocale(ORIGIN_LOCALE)
  vi.clearAllMocks()
})

const TASK = {
  id: 1,
  credential_id: 3,
  provider_id: 7,
  provider_name: 'Anthropic',
  provider_code: 'anthropic',
  raw_model: 'claude-sonnet-4.6',
  standardized_name: 'claude-sonnet-4-6',
  status: 'retrying',
  attempt: 3,
  priority: 10,
  reason_code: 'upstream_5xx',
  next_run_at: '2026-10-07T10:05:00Z',
  result_latency_ms: 820,
  result_http_status: 502,
}

describe('ProbeView 两个端点分别记错误', () => {
  // ★ 队列挂、延时正常 ⇒ 只有队列那段报错，延时段的清单仍要出
  it('仅 queue-tasks 失败 ⇒ 延时段照常渲染', async () => {
    qt.mockRejectedValueOnce(new Error('queue boom'))
    pl.mockResolvedValueOnce({
      entries: [
        { provider_id: 7, provider_name: 'Anthropic', provider_code: 'anthropic', latency_ms: 320, probed_at: '2026-10-07T10:00:00Z' },
      ],
      total: 1,
    })
    const w = await mountView()
    expect(w.text()).toContain('queue boom')
    // 关键：整页没有变成「全部失败」，延时那条还在
    expect(w.text()).not.toContain('整页')
    expect(w.text()).toContain('Anthropic')
    expect(w.text()).toContain('320ms')
  })

  it('仅 provider-latency 失败 ⇒ 队列段照常渲染', async () => {
    qt.mockResolvedValueOnce({ tasks: [TASK], total: 1 })
    pl.mockRejectedValueOnce(new Error('latency boom'))
    const w = await mountView()
    expect(w.text()).toContain('latency boom')
    expect(w.text()).toContain('claude-sonnet-4-6')
  })

  it('两个都失败 ⇒ 显示整页错误', async () => {
    qt.mockRejectedValueOnce(new Error('queue boom'))
    pl.mockRejectedValueOnce(new Error('latency boom'))
    const w = await mountView()
    expect(w.find('.pb__msg--err').exists()).toBe(true)
  })
})

describe('ProbeView 「没出现 ≠ 不存在」', () => {
  // ★ 反向锁定：口径说明必须**无条件**渲染，不能只在出错时出现。
  //   它要防的是「看空列表的人以为供应商没了」，与端点是否报错无关。
  it('延时段为空时口径说明仍在', async () => {
    const w = await mountView()
    expect(w.text()).toContain('仅统计最近 1 小时内')
    expect(w.text()).toContain('不代表该供应商不存在')
    expect(w.text()).toContain('最近 1 小时内没有成功的直连探测记录')
  })

  it('有数据时口径说明同样在（不是「出错才提示」）', async () => {
    pl.mockResolvedValueOnce({
      entries: [
        { provider_id: 7, provider_name: 'Anthropic', provider_code: 'anthropic', latency_ms: 320, probed_at: '2026-10-07T10:00:00Z' },
      ],
      total: 1,
    })
    const w = await mountView()
    expect(w.find('.pb__hint').exists()).toBe(true)
  })
})

describe('ProbeView 队列任务渲染', () => {
  it('展示尝试次数与上次结果', async () => {
    qt.mockResolvedValueOnce({ tasks: [TASK], total: 1 })
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('第 3 次')
    expect(text).toContain('claude-sonnet-4-6')
    expect(text).toContain('upstream_5xx')
    expect(text).toContain('502')
  })

  it('标准化名为空白时回落到 raw_model（不渲染成空）', async () => {
    qt.mockResolvedValueOnce({ tasks: [{ ...TASK, standardized_name: '  ', raw_model: 'gpt-4o' }], total: 1 })
    const w = await mountView()
    expect(w.text()).toContain('gpt-4o')
  })
})
