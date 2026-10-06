import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ProbeView from './ProbeView.vue'
import { fetchProbeQueueTasks, fetchProviderLatency } from '@/api/probeOps'
import { fetchProbeNodeTasks } from '@/api/probeModelHealth'
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

// ★ node-tasks 来自另一个 API 模块，mock 要分开声明（probeModelHealth 的
//   其它导出——latencyOf / needsManualAction 等——必须保持真实现，
//   否则下面的判据量的是 mock 而不是被测行为）。
vi.mock('@/api/probeModelHealth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/probeModelHealth')>()
  return { ...actual, fetchProbeNodeTasks: vi.fn() };
});

const qt = fetchProbeQueueTasks as unknown as ReturnType<typeof vi.fn>
const pl = fetchProviderLatency as unknown as ReturnType<typeof vi.fn>
const nt = fetchProbeNodeTasks as unknown as ReturnType<typeof vi.fn>

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
  nt.mockResolvedValue({ tasks: [], total: 0 })
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

  // ★ 2026-10-07：加入 node-tasks 之后，「整页错误」的判据从
  //   「两个都失败」改成「**一个都没成功**」。
  //   这不是为了让旧判据变绿而改它 —— 三个端点里挂两个时，
  //   逐段报错比一条全局横幅**信息量更大**（用户能看出是哪一段挂了、
  //   哪一段还活着），而「整页都挂了」这个横幅只在真的全挂时才有意义。
  it('★ 三个里挂两个 ⇒ 不显示整页错误（逐段报错信息量更大）', async () => {
    qt.mockRejectedValueOnce(new Error('queue boom'))
    pl.mockRejectedValueOnce(new Error('latency boom'))
    const w = await mountView()
    expect(w.find('.pb__msg--err').exists()).toBe(false)
    // 挂掉的两段各自报错
    expect(w.text()).toContain('queue boom')
    expect(w.text()).toContain('latency boom')
    // 没挂的那段照常渲染
    expect(w.text()).toContain('节点探测队列')
  })

  it('★ 三个全挂 ⇒ 显示整页错误', async () => {
    qt.mockRejectedValueOnce(new Error('queue boom'))
    pl.mockRejectedValueOnce(new Error('latency boom'))
    nt.mockRejectedValueOnce(new Error('node boom'))
    const w = await mountView()
    expect(w.find('.pb__msg--err').exists()).toBe(true)
  })

  // ★ 反向锁定：只有一个挂 ⇒ 也**不是**整页错误
  it('★ 只挂一个 ⇒ 不是整页错误', async () => {
    nt.mockRejectedValueOnce(new Error('node boom'))
    const w = await mountView()
    expect(w.find('.pb__msg--err').exists()).toBe(false)
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

// ══════════════════════════════════════════════════════════════════════════
// node-tasks 段（2026-10-07 加入）
//
// ★ 这一段的核心不变量：**它和上面的完整性队列是两个不同的队列**。
//   · queue-tasks ← credential_probe_queue（完整性探测规划器），source="integrity"
//   · node-tasks  ← node_probe_state（错误触发的 NodeProbeWorker），source="node_probe"
//   渲染成一团就分不清「这是哪条队列在积压」。
// ══════════════════════════════════════════════════════════════════════════

const NODE_TASK = {
  credential_id: 7,
  provider_id: 1,
  provider_name: 'OpenAI',
  provider_code: 'openai',
  raw_model: 'gpt-4o',
  standardized_name: 'gpt-4o',
  status: 'paused',
  attempt: 5,
  consecutive_failures: 5,
  next_retry_at: '2026-10-07T11:00:00Z',
  last_direct_ok: false,
  last_gateway_ok: null,
  last_err_code: 'timeout',
  last_latency_ms: 1200,
  paused: true,
  updated_at: '2026-10-07T10:00:00Z',
  source: 'node_probe',
}

describe('node-tasks 段：第三个端点独立记错误', () => {
  it('挂载后三个端点各调一次', async () => {
    await mountView()
    expect(qt).toHaveBeenCalledTimes(1)
    expect(pl).toHaveBeenCalledTimes(1)
    expect(nt).toHaveBeenCalledTimes(1)
  })

  // ★ 不发 limit ⇒ 用后端自己的默认 120（与 queue-tasks 的 100 不同）
  it('★ 不发 limit（后端 node-tasks 默认 120，queue-tasks 默认 100）', async () => {
    await mountView()
    expect(nt).toHaveBeenCalledWith()
  })

  it('★ node-tasks 挂了但另两个正常 ⇒ 只报节点队列这一段', async () => {
    nt.mockRejectedValue(new Error('node boom'))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('node boom')
    // ★ 另两段仍在
    expect(text).toContain('探测队列')
    expect(text).toContain('供应商探测延时')
    // ★ 不是整页错误
    expect(w.find('.pb__msg--err').exists()).toBe(false)
  })

  it('三个都挂 ⇒ 整页错误', async () => {
    qt.mockRejectedValue(new Error('q'))
    pl.mockRejectedValue(new Error('p'))
    nt.mockRejectedValue(new Error('n'))
    const w = await mountView()
    expect(w.find('.pb__msg--err').exists()).toBe(true)
  })

  it('空队列 ⇒ 自己的空态文案（不是队列一号的）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('节点探测队列为空')
  })
})

describe('★ node-tasks：paused 与 pending 必须在视觉上分得开', () => {
  // ★「等一等就好」和「等再久也不会好」是两件事。
  //   paused = 已达重试上限，不会自愈 ⇒ 需要人工。
  it('★ paused 行标记为「需要人工介入」', async () => {
    nt.mockResolvedValue({ tasks: [NODE_TASK], total: 1 })
    const w = await mountView()
    expect(w.find('.pb__item--manual').exists()).toBe(true)
    expect(w.text()).toContain('需要人工介入')
  })

  it('★ pending 行不标记（会自己往前走）', async () => {
    nt.mockResolvedValue({ tasks: [{ ...NODE_TASK, status: 'pending', paused: false }], total: 1 })
    const w = await mountView()
    expect(w.find('.pb__item--manual').exists()).toBe(false)
    expect(w.text()).not.toContain('需要人工介入')
  })

  it('running 行不标记', async () => {
    nt.mockResolvedValue({ tasks: [{ ...NODE_TASK, status: 'running', paused: false }], total: 1 })
    const w = await mountView()
    expect(w.find('.pb__item--manual').exists()).toBe(false)
  })

  // ★ `last_latency_ms` 是 `*int` + omitempty ⇒ 缺失 ≠ 0ms
  it('★ 延时缺失 ⇒ 「无延时记录」，不是 0ms', async () => {
    nt.mockResolvedValue({ tasks: [{ ...NODE_TASK, last_latency_ms: undefined }], total: 1 })
    const w = await mountView()
    expect(w.text()).toContain('无延时记录')
    expect(w.text()).not.toContain('0ms')
  })

  it('★ 延时真的是 0 ⇒ 显示 0ms（与缺失区分开）', async () => {
    nt.mockResolvedValue({ tasks: [{ ...NODE_TASK, last_latency_ms: 0 }], total: 1 })
    const w = await mountView()
    expect(w.text()).toContain('0ms')
    expect(w.text()).not.toContain('无延时记录')
  })

  it('standardized_name 优先于 raw_model（后端是三级 COALESCE）', async () => {
    nt.mockResolvedValue({ tasks: [{ ...NODE_TASK, standardized_name: 'gpt-4o-canon' }], total: 1 })
    const w = await mountView()
    expect(w.text()).toContain('gpt-4o-canon')
  })

  it('standardized_name 为空 ⇒ 回落 raw_model', async () => {
    nt.mockResolvedValue({ tasks: [{ ...NODE_TASK, standardized_name: '', raw_model: 'gpt-4o-raw' }], total: 1 })
    const w = await mountView()
    expect(w.text()).toContain('gpt-4o-raw')
  })
})
