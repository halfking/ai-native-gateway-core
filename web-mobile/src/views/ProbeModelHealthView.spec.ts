import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ProbeModelHealthView from './ProbeModelHealthView.vue'
import { fetchProbeDashboard } from '@/api/probeModelHealth'
import { setLocale, locale } from '@/i18n'

/**
 * ProbeModelHealthView 的五条不变量（2026-10-07）。
 *
 * 这一页的核心是**后端把 SQL NULL 压成了 0**：
 * `nullFloat64`/`nullInt`（probe_dashboard.go:2336-2348）返回 0，
 * 而 `ModelHealthSummary` 的字段是普通 `float64`/`int`（无指针无 omitempty）
 * ⇒ 「0%」与「没数据」在响应里同形，客户端**拿不到区分依据**。
 *
 * 唯一可推导的判据：`total_credentials === 0`。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
});

vi.mock('@/api/probeModelHealth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/probeModelHealth')>()
  return { ...actual, fetchProbeDashboard: vi.fn() };
});

const dashMock = fetchProbeDashboard as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(ProbeModelHealthView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

async function searchWith(w: ReturnType<typeof mount>, q: string): Promise<void> {
  await w.find('input.pm__input').setValue(q)
  const btn = w.findAll('button').find((b) => b.text() === '筛选')
  await btn!.trigger('click')
  await flushPromises()
  await flushPromises()
}

const MODEL = {
  provider_model_id: 1,
  raw_model_name: 'gpt-4o',
  outbound_model_name: 'gpt-4o-2024-11',
  protocol: 'openai',
  provider_name: 'OpenAI',
  total_credentials: 12,
  healthy_count: 10,
  suspicious_count: 1,
  failing_count: 1,
  probing_count: 0,
  healthy_percentage: 83.3,
  failing_percentage: 8.3,
  urgent_count: 0,
  suspicious_priority_count: 1,
  failing_priority_count: 1,
  watchdog_count: 0,
  avg_success_rate_7d: 0.97,
  avg_verification_hours: 6.5,
  avg_consecutive_successes: 12,
  total_real_success_24h: 300,
  total_real_failure_24h: 5,
  real_success_rate_24h: 0.9836,
  last_verified_at: '2026-10-07T10:00:00Z',
  last_real_request_at: '2026-10-07T09:50:00Z',
  next_probe_at: '2026-10-07T11:00:00Z',
  critical_nodes: 0,
  pending_probes_5min: 0,
  overall_health: 'healthy',
}

/** ★ 分母为 0：后端 nullFloat64 把 SQL NULL 压成了 0。 */
const ZERO_DENOM = {
  ...MODEL,
  provider_model_id: 2,
  raw_model_name: 'new-model',
  total_credentials: 0,
  healthy_count: 0,
  suspicious_count: 0,
  failing_count: 0,
  probing_count: 0,
  healthy_percentage: 0,
  failing_percentage: 0,
  avg_success_rate_7d: 0,
  total_real_success_24h: 0,
  total_real_failure_24h: 0,
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  dashMock.mockResolvedValue({ models: [MODEL], total: 1 })
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
    expect(dashMock).toHaveBeenCalledTimes(1)
    expect(dashMock.mock.calls[0]![0]).toEqual({})
  })

  it('填了模型才带 model 参数', async () => {
    const w = await mountView()
    await searchWith(w, 'gpt-4')
    expect(dashMock.mock.calls[1]![0]).toEqual({ model: 'gpt-4' })
  })

  // ★ 后端是 ILIKE '%q%'：传 gpt-4 会命中 gpt-4o / gpt-4o-mini
  it('★ 填了模型后必须提示「子串匹配」', async () => {
    const w = await mountView()
    await searchWith(w, 'gpt-4')
    expect(w.text()).toContain('子串')
  })

  it('空结果 ⇒ 空态', async () => {
    dashMock.mockResolvedValue({ models: [], total: 0 })
    const w = await mountView()
    expect(w.text()).toContain('没有匹配的模型')
  })

  it('models 为 null ⇒ 不崩', async () => {
    dashMock.mockResolvedValue({ models: null, total: 0 })
    const w = await mountView()
    expect(w.text()).toContain('没有匹配的模型')
  })

  it('403 ⇒ 报「没有权限」', async () => {
    dashMock.mockRejectedValue(Object.assign(new Error('forbidden'), { status: 403 }))
    const w = await mountView()
    expect(w.text()).toContain('没有查看探测健康面板的权限')
  })
})

describe('★★★ 判据 1：total_credentials=0 ⇒ 派生统计显示「无数据」而不是 0', () => {
  it('★ 0 凭据那行显示「无数据」而不是 0.0%', async () => {
    dashMock.mockResolvedValue({ models: [ZERO_DENOM], total: 1 })
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('无数据')
    // ★ 0.0% 不该出现 —— 那是把 NULL 压平的 0 当成了测出来的值
    expect(text).not.toContain('0.0%')
  })

  // ★ 反向锁定：分母非 0 时 0 是真值，必须正常显示
  it('★ 分母非 0 时百分比正常显示（证明上一条不是恒真）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('83.3%')
    expect(w.text()).not.toContain('无数据')
  })

  it('★ 两行的读数确实不同（同一份模板、不同分母）', async () => {
    const w1 = await mountView()
    const normal = w1.find('.pm__item').text()
    dashMock.mockResolvedValue({ models: [ZERO_DENOM], total: 1 })
    const w2 = await mountView()
    const zero = w2.find('.pm__item').text()
    expect(normal).not.toBe(zero)
  })
})

describe('★★ 判据 2：real_success_rate_24h 三态', () => {
  it('有值 ⇒ 显示百分比', async () => {
    const w = await mountView()
    expect(w.text()).toContain('98.4%')
  })

  // ★ Go 侧 `*float64` + omitempty ⇒ 字段整个不存在
  it('★ 字段缺失 ⇒ 「24h 内没有真实请求」，不是 0.0%', async () => {
    const m = { ...MODEL }
    delete (m as Record<string, unknown>).real_success_rate_24h
    dashMock.mockResolvedValue({ models: [m], total: 1 })
    const w = await mountView()
    expect(w.text()).toContain('24h 内没有真实请求')
    expect(w.text()).not.toContain('0.0%')
  })

  it('★ 真的是 0（跑了且全失败）⇒ 显示 0.0%', async () => {
    dashMock.mockResolvedValue({ models: [{ ...MODEL, real_success_rate_24h: 0 }], total: 1 })
    const w = await mountView()
    expect(w.text()).toContain('0.0%')
    expect(w.text()).not.toContain('24h 内没有真实请求')
  })
})

describe('★ 判据 3：请求总量 0 有歧义 ⇒ 显示「无数据」', () => {
  it('★ 成功+失败都 0 ⇒ 显示无数据，不是 0', async () => {
    dashMock.mockResolvedValue({
      models: [{ ...MODEL, total_real_success_24h: 0, total_real_failure_24h: 0, real_success_rate_24h: 0.5 }],
      total: 1,
    })
    const w = await mountView()
    // ★ 关键：real_success_rate 有值（0.5）但总量是 0 —— 这两个数自相矛盾，
    //   所以请求量那一行必须显示「无数据」而不是 0
    expect(w.text()).toContain('50.0%')
    expect(w.text()).toContain('无数据')
  })
})

describe('★ 判据 4：overall_health 词表外不给绿色', () => {
  it('healthy ⇒ 绿点', async () => {
    dashMock.mockResolvedValue({ models: [{ ...MODEL, overall_health: 'healthy' }], total: 1 })
    const w = await mountView()
    expect(w.find('.status-dot--success').exists()).toBe(true)
  })

  // ★ 默认给 success 会把「看不懂的状态」显示成「健康」
  it('★ 词表外 ⇒ muted，不是 success', async () => {
    dashMock.mockResolvedValue({ models: [{ ...MODEL, overall_health: 'excellent' }], total: 1 })
    const w = await mountView()
    expect(w.find('.status-dot--success').exists()).toBe(false)
    expect(w.find('.pm__item').text()).toContain('未知')
  })

  it('critical ⇒ 危险色', async () => {
    dashMock.mockResolvedValue({ models: [{ ...MODEL, overall_health: 'critical' }], total: 1 })
    const w = await mountView()
    expect(w.find('.status-dot--danger').exists()).toBe(true)
  })
})

describe('★ 判据 5：明细与分母自相矛盾时必须报警', () => {
  it('★ 明细之和 > 分母 ⇒ 报「自相矛盾」', async () => {
    dashMock.mockResolvedValue({
      models: [{ ...MODEL, total_credentials: 5, healthy_count: 10 }],
      total: 1,
    })
    const w = await mountView()
    expect(w.text()).toContain('自相矛盾')
  })

  it('★ 分母 0 但明细有值 ⇒ 也报（分母 0 时不可能有明细）', async () => {
    dashMock.mockResolvedValue({
      models: [{ ...ZERO_DENOM, healthy_count: 3 }],
      total: 1,
    })
    const w = await mountView()
    expect(w.text()).toContain('自相矛盾')
  })

  it('正常行不报警（证明上一条不是恒真）', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('自相矛盾')
  })
})

describe('附属信息', () => {
  it('outbound 与 raw 不同时显示别名', async () => {
    const w = await mountView()
    expect(w.text()).toContain('gpt-4o-2024-11')
  })

  it('两者相同时不显示别名箭头', async () => {
    dashMock.mockResolvedValue({
      models: [{ ...MODEL, outbound_model_name: 'gpt-4o' }],
      total: 1,
    })
    const w = await mountView()
    expect(w.find('.pm__item-alias').exists()).toBe(false)
  })

  it('critical_nodes > 0 ⇒ 告警', async () => {
    dashMock.mockResolvedValue({ models: [{ ...MODEL, critical_nodes: 3 }], total: 1 })
    const w = await mountView()
    expect(w.text()).toContain('3 个危急节点')
  })

  it('critical_nodes=0 ⇒ 不告警', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('危急节点')
  })
})
