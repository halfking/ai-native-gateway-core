import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import DispatchWaterfallView from './DispatchWaterfallView.vue'
import { fetchWaterfall, fetchDispatchQueues } from '@/api/dispatchWaterfall'
import { setLocale, locale } from '@/i18n'

/**
 * 调度瀑布：**不可观测 ≠ 没有数据**。
 *
 * 本面**没有** degraded 字段（不像 usage / board）。不可观测只由
 * `wired === false` 或 `source === 'none'` 表达。把它渲染成空列表，
 * 等于对运维说「一切正常」，而真相是**这个观测面没在工作** ——
 * 那恰恰是排障最需要的信号。桌面端也踩过同族（降级被显示成「真的一分钱没花」）。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/dispatchWaterfall', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/dispatchWaterfall')>()
  return { ...actual, fetchWaterfall: vi.fn(), fetchDispatchQueues: vi.fn() }
})

const wf = fetchWaterfall as unknown as ReturnType<typeof vi.fn>
const dq = fetchDispatchQueues as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(DispatchWaterfallView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  // ★ 显式钉 zh-CN：jsdom 的 navigator.language 是 en-US，不钉住断言会全跑英文
  //   （与 KeysView.spec / RequestJourneyView.spec 同源，见 §11.29(3)）
  setLocale('zh-CN')
  vi.clearAllMocks()
  dq.mockResolvedValue({ enabled: true, wired: true, models: [], credentials: [] })
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

const BASE = { enabled: true, bottleneck_diagnosis: { bottleneck: '', message: '' } }

describe('DispatchWaterfallView 不可观测 ≠ 无数据', () => {
  it('wired:false ⇒ 显示「投影未接上」警告', async () => {
    wf.mockResolvedValueOnce({ ...BASE, requests: [], wired: false, source: 'memory' })
    const w = await mountView()
    const banner = w.find('.wf__banner--warn')
    expect(banner.exists()).toBe(true)
    expect(banner.text()).toContain('队列投影未接上')
  })

  it("source:'none' ⇒ 显示「没有可用数据源」警告", async () => {
    wf.mockResolvedValueOnce({ ...BASE, requests: [], wired: true, source: 'none' })
    const w = await mountView()
    expect(w.find('.wf__banner--warn').text()).toContain('没有可用数据源')
  })

  // ★ 反向锁定：空数组 + wired:true 是「真的没有调度记录」的真结论。
  //   把它渲染成不可观测会掩盖真实问题，方向正好反了。
  it('★ wired:true 且 requests 为空 ⇒ 不得出现不可观测警告', async () => {
    wf.mockResolvedValueOnce({ ...BASE, requests: [], wired: true, source: 'memory' })
    const w = await mountView()
    expect(w.find('.wf__banner--warn').exists()).toBe(false)
  })

  it('bottleneck 与 suggestion 有值时透出', async () => {
    wf.mockResolvedValueOnce({
      ...BASE,
      requests: [],
      wired: true,
      source: 'memory',
      bottleneck_diagnosis: { bottleneck: 'model_queue', message: 'm', suggestion: '扩容模型队列' },
    })
    const w = await mountView()
    expect(w.text()).toContain('model_queue')
    expect(w.text()).toContain('扩容模型队列')
  })
})

describe('DispatchWaterfallView 队列端点失败必须显式说', () => {
  // ★ 桌面端用 `.catch(() => null)` 静默吞掉队列失败（fetchDispatchWaterfall
  //   旁边的 Promise.all）。移动端不照抄：静默吞掉会让「队列指标挂了」与
  //   「队列是空的」在界面上完全一样。
  it('队列端点 500 ⇒ 显示错误而不是假装没有队列', async () => {
    wf.mockResolvedValueOnce({ ...BASE, requests: [], wired: true, source: 'memory' })
    dq.mockRejectedValueOnce(new Error('queues boom'))
    const w = await mountView()
    expect(w.find('.wf__queues-err').exists()).toBe(true)
    expect(w.text()).toContain('queues boom')
  })

  it('enabled:false ⇒ 显示「队列指标未开启」而不是空白', async () => {
    wf.mockResolvedValueOnce({ ...BASE, requests: [], wired: true, source: 'memory' })
    dq.mockResolvedValueOnce({ enabled: false, wired: true, models: [], credentials: [] })
    const w = await mountView()
    expect(w.text()).toContain('队列指标未开启')
  })

  it('队列有深度 ⇒ 渲染泳道且不显示未开启提示', async () => {
    wf.mockResolvedValueOnce({ ...BASE, requests: [], wired: true, source: 'memory' })
    dq.mockResolvedValueOnce({
      enabled: true,
      wired: true,
      models: [{ model: 'gpt-4o', depth: 3, limit: 10 }],
      credentials: [{ credential: 'prod-key-a', depth: 0 }],
    })
    const w = await mountView()
    expect(w.text()).toContain('gpt-4o')
    expect(w.text()).toContain('prod-key-a')
    expect(w.text()).not.toContain('队列指标未开启')
  })
})
