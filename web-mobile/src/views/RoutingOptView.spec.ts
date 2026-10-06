import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import RoutingOptView from './RoutingOptView.vue'
import {
  fetchRoutingOptStats,
  fetchRoutingOptAccuracy,
  fetchRoutingOptParameters,
  fetchRoutingOptMetrics,
} from '@/api/routingOpt'
import { setLocale, locale } from '@/i18n'

/**
 * RoutingOptView 的不变量（2026-10-07）。
 *
 * 1. ★★★★ `accuracy_source` 三种来源语义完全不同，必须分别渲染：
 *    `weighted_feedback` 实时 / `persisted_state` **旧值回落** / `none` 无来源；
 * 2. ★★★★ 加权口径（人工单条算 2 条）+ 「客户端无法验证」必须**常驻**；
 * 3. ★★★★ 量纲 0..1 ⇒ `0.87` 必须渲染成 `87.0%`（不是 `8700%`）；
 * 4. ★★★ 准确率桶**恒有样本** ⇒ 0% 是真的 0%，页面必须说明；
 * 5. ★★★ `parameters` 的 404 = 「还没配置」，不是错误；
 * 6. ★★★ `metrics.truncated` 是**后端自带**的标记；未命中但窗口够长时给预估提示；
 * 7. ★★ metrics 只渲染前 50 行，且必须**明说**；
 * 8. ★★ 指针字段键缺失 ⇒ 显示「—」，不是 0；
 * 9. ★★ `stats` 窗口**写死 24**，页面要说清不能改；
 * 10. ★★ 四个端点**各自独立**失败，一个失败不许清空/覆盖其它三个。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/routingOpt', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/routingOpt')>()
  return {
    ...actual,
    fetchRoutingOptStats: vi.fn(),
    fetchRoutingOptAccuracy: vi.fn(),
    fetchRoutingOptParameters: vi.fn(),
    fetchRoutingOptMetrics: vi.fn(),
  }
})

const statsMock = fetchRoutingOptStats as unknown as ReturnType<typeof vi.fn>
const accMock = fetchRoutingOptAccuracy as unknown as ReturnType<typeof vi.fn>
const paramsMock = fetchRoutingOptParameters as unknown as ReturnType<typeof vi.fn>
const metricsMock = fetchRoutingOptMetrics as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(RoutingOptView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

function stats(over: Record<string, unknown> = {}) {
  return {
    overall_accuracy: 0.87,
    accuracy_source: 'weighted_feedback',
    parameter_version: 12,
    human_annotations_used: 7,
    window_hours: 24,
    auto_samples: 120,
    human_samples: 7,
    state_updated_at: '2026-10-07T09:00:00Z',
    ...over,
  }
}

function bucket(over: Record<string, unknown> = {}) {
  return {
    hour: '2026-10-07T09:00:00Z',
    task_type: 'chat',
    accuracy: 0.87,
    samples: 10,
    human_samples: 2,
    ...over,
  }
}

function accResp(over: Record<string, unknown> = {}) {
  return {
    hours: 24,
    since: '2026-10-06T10:00:00Z',
    buckets: [bucket()],
    ...over,
  }
}

function params(over: Record<string, unknown> = {}) {
  return {
    version: 12,
    classifier_weights: { a: 1 },
    confidence_thresholds: { b: 0.7 },
    recommender_weights: { c: 2 },
    exploration_rate: 0.15,
    learning_rate: 0.01,
    adaptation_window: 168,
    overall_accuracy: 0.87,
    activated_at: '2026-10-06T10:00:00Z',
    created_by: 'ops@example.com',
    notes: '夜间回归',
    ...over,
  }
}

function mRow(over: Record<string, unknown> = {}) {
  return {
    time_bucket: '2026-10-07T09:55:00Z',
    task_type: 'chat',
    predicted_provider: 'openai',
    total_requests: 30,
    successful_requests: 28,
    failed_requests: 2,
    accuracy_rate: 0.5,
    avg_confidence: 0.9,
    avg_latency_ms: 800,
    avg_cost: 0.02,
    p50_latency_ms: 600,
    p95_latency_ms: 1200,
    p99_latency_ms: 2000,
    human_corrections: 1,
    human_accuracy_rate: 0.5,
    ...over,
  }
}

function metricsResp(over: Record<string, unknown> = {}) {
  return { hours: 24, since: '2026-10-06T10:00:00Z', rows: [mRow()], truncated: false, ...over }
}

/** 404 形状的拒绝（后端这一族走 http.Error ⇒ text/plain）。 */
function httpErr(status: number, msg: string): Error {
  return Object.assign(new Error(msg), { status })
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  statsMock.mockResolvedValue(stats())
  paramsMock.mockResolvedValue(params())
  accMock.mockResolvedValue(accResp())
  metricsMock.mockResolvedValue(metricsResp())
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

describe('入口与窗口控件', () => {
  it('挂载即请求四个端点，accuracy/metrics 默认 24h', async () => {
    await mountView()
    expect(statsMock).toHaveBeenCalledTimes(1)
    expect(paramsMock).toHaveBeenCalledTimes(1)
    expect(accMock.mock.calls[0]![0]).toEqual({ hours: 24 })
    expect(metricsMock.mock.calls[0]![0]).toEqual({ hours: 24, taskType: undefined, provider: undefined })
  })

  it('★★★ stats 窗口写死 24 ⇒ 页面明说「不能改」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('统计窗口 24 小时（后端写死，不能改）')
  })

  it('★★★ 响应窗口与写死常量不一致 ⇒ 必须告警（后端改过常量）', async () => {
    statsMock.mockResolvedValue(stats({ window_hours: 72 }))
    const w = await mountView()
    expect(w.text()).toContain('后端返回的窗口是 72 小时')
    expect(w.text()).toContain('与已知的写死值 24 小时不一致')
  })

  it('★ 窗口一致时不报失配（证明上一条不是恒真）', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('与已知的写死值')
  })

  it('★ 准确率切到 168h ⇒ 带新值重查', async () => {
    const w = await mountView()
    const chips = w.findAll('.ro__chip').filter((c) => c.text() === '168h')
    expect(chips.length).toBe(2) // 准确率 + metrics 各一组
    await chips[0]!.trigger('click')
    await flushPromises()
    // ★★ 单次 flushPromises 不保证第 2 次请求已发出：视图取数链要走两轮微任务，
    //   机器负载高时 calls[1] 会短暂是 undefined（十连跑 run#8 实测偶发，2/3801）。
    await vi.waitFor(() => {
      expect(accMock.mock.calls.length).toBeGreaterThanOrEqual(2)
    })
    expect(accMock.mock.calls[1]![0]).toEqual({ hours: 168 })
  })

  it('★ metrics 窗口切到 720h ⇒ 带新值重查', async () => {
    const w = await mountView()
    const chips = w.findAll('.ro__chip').filter((c) => c.text() === '720h')
    await chips[1]!.trigger('click')
    await flushPromises()
    // ★★ 单次 flushPromises 不保证第 2 次请求已发出：视图取数链要走两轮微任务，
    //   机器负载高时 calls[1] 会短暂是 undefined（十连跑 run#8 实测偶发，2/3801）。
    await vi.waitFor(() => {
      expect(metricsMock.mock.calls.length).toBeGreaterThanOrEqual(2)
    })
    expect(metricsMock.mock.calls[1]![0]).toEqual({ hours: 720, taskType: undefined, provider: undefined })
  })

  it('★ 填精确匹配条件后提交 ⇒ 带 taskType/provider 重查', async () => {
    const w = await mountView()
    const inputs = w.findAll('input')
    await inputs[0]!.setValue(' chat ')
    await inputs[1]!.setValue('openai')
    await w.find('form').trigger('submit')
    await flushPromises()
    // ★★ 单次 flushPromises 不保证第 2 次请求已发出：视图取数链要走两轮微任务，
    //   机器负载高时 calls[1] 会短暂是 undefined（十连跑 run#8 实测偶发，2/3801）。
    await vi.waitFor(() => {
      expect(metricsMock.mock.calls.length).toBeGreaterThanOrEqual(2)
    })
    expect(metricsMock.mock.calls[1]![0]).toEqual({ hours: 24, taskType: 'chat', provider: 'openai' })
  })
})

describe('★★★★ 判据 1：三种 accuracy_source 分别渲染', () => {
  it('★★★ persisted_state ⇒ 明说「旧值」', async () => {
    statsMock.mockResolvedValue(stats({ accuracy_source: 'persisted_state' }))
    const w = await mountView()
    expect(w.text()).toContain('当前窗口没有新的反馈样本')
    expect(w.text()).toContain('持久化状态里的旧值')
  })

  it('★★★ none ⇒ 明说「没有数据来源」', async () => {
    statsMock.mockResolvedValue(stats({ accuracy_source: 'none' }))
    const w = await mountView()
    expect(w.text()).toContain('既没有反馈样本，也没有持久化状态')
  })

  it('★★★ 实时来源**不**显示任何回落警告（证明不是恒真）', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('当前窗口没有新的反馈样本')
    expect(w.text()).not.toContain('既没有反馈样本')
    expect(w.text()).not.toContain('准确率来源无法识别')
  })

  it('★★★ 未知来源如实显示来源名，**不**当实时', async () => {
    statsMock.mockResolvedValue(stats({ accuracy_source: 'brand_new_source' }))
    const w = await mountView()
    expect(w.text()).toContain('准确率来源无法识别：brand_new_source')
    expect(w.text()).not.toContain('当前窗口没有新的反馈样本')
  })

  it('★ 三档之间互斥：persisted_state 不同时说 none', async () => {
    statsMock.mockResolvedValue(stats({ accuracy_source: 'persisted_state' }))
    const w = await mountView()
    expect(w.text()).not.toContain('既没有反馈样本，也没有持久化状态')
  })
})

describe('★★★★ 判据 2/3：加权口径 + 无法验证 + 量纲', () => {
  it('★★★ 加权口径常驻（人工单条算 2 条）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('1 条人工标注按 2 条计入')
    expect(w.text()).toContain('不等于「命中数 ÷ 样本数」')
  })

  it('★★★ 「客户端无法验证」常驻', async () => {
    const w = await mountView()
    expect(w.text()).toContain('只返回样本量、不返回命中数')
    expect(w.text()).toContain('没法在客户端核对')
  })

  it('★★★ 两种「正确」定义常驻', async () => {
    const w = await mountView()
    expect(w.text()).toContain('自动那半数的是请求有没有成功')
    expect(w.text()).toContain('人工那半数的是预测有没有命中人工标注')
  })

  it('★★★★ 0.87 ⇒ 87.0%（不是 8700%）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('87.0%')
    expect(w.text()).not.toContain('8700')
  })

  it('★★★ 桶里 0.5 ⇒ 50.0%', async () => {
    accMock.mockResolvedValue(accResp({ buckets: [bucket({ accuracy: 0.5 })] }))
    const w = await mountView()
    expect(w.text()).toContain('50.0%')
  })

  it('★★★ 桶 accuracy 为 0 ⇒ 渲染 0.0%，且页面说明这是真的 0%', async () => {
    accMock.mockResolvedValue(accResp({ buckets: [bucket({ accuracy: 0, samples: 3 })] }))
    const w = await mountView()
    expect(w.text()).toContain('0.0%')
    expect(w.text()).toContain('每个时间桶都有样本')
    expect(w.text()).toContain('列表里的 0% 是真的 0%')
  })

  it('★ 0.87 不被写成 87.00%（一位小数）', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('87.00%')
  })
})

describe('★★★ 判据 5：parameters 的 404 是「还没配置」', () => {
  it('★★★ 404 ⇒ 显示「还没有激活」，且**不**显示成错误', async () => {
    paramsMock.mockRejectedValueOnce(httpErr(404, 'No active optimization state'))
    const w = await mountView()
    expect(w.text()).toContain('还没有激活的优化器参数版本')
    expect(w.text()).not.toContain('No active optimization state')
  })

  it('★ 正常 200 ⇒ 渲染参数（版本/探索率/学习率/备注）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('探索率')
    expect(w.text()).toContain('0.15')
    expect(w.text()).toContain('夜间回归')
  })

  it('★ notes 键缺失 ⇒ 不渲染备注行（不是「null」）', async () => {
    paramsMock.mockResolvedValue(params({ notes: undefined }))
    const w = await mountView()
    expect(w.text()).not.toContain('夜间回归')
    expect(w.text()).not.toContain('null')
  })

  it('★★ parameters 非 404 失败 ⇒ 在自己面板里报错，不动 stats', async () => {
    paramsMock.mockRejectedValueOnce(httpErr(500, 'params boom'))
    const w = await mountView()
    expect(w.text()).toContain('params boom')
    expect(w.text()).not.toContain('还没有激活的优化器参数版本')
    // stats 那一块仍然完好
    expect(w.text()).toContain('87.0%')
  })
})

describe('★★★ 判据 6/7：metrics 截断与显示封顶', () => {
  it('★★★ truncated=true ⇒ 明说已达 2000 行上限、丢最旧的', async () => {
    metricsMock.mockResolvedValue(metricsResp({ truncated: true }))
    const w = await mountView()
    expect(w.text()).toContain('已达后端 2000 行上限')
    expect(w.text()).toContain('最旧的数据被丢掉了')
  })

  it('★★★ truncated=false 但窗口够长 ⇒ 给预估提示', async () => {
    const w = await mountView()
    const chips = w.findAll('.ro__chip').filter((c) => c.text() === '720h')
    await chips[1]!.trigger('click')
    await flushPromises()
    expect(w.text()).toContain('可能撞上后端 2000 行上限')
    expect(w.text()).toContain('丢的是最旧的数据')
    expect(w.text()).not.toContain('已达后端 2000 行上限')
  })

  it('★ 短窗口（24h）⇒ 不出截断提示（证明不是恒真）', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('可能撞上后端 2000 行上限')
    expect(w.text()).not.toContain('已达后端 2000 行上限')
  })

  it('★★★ 超过 50 行 ⇒ 只渲染 50 行并明说隐藏了几行', async () => {
    const rows = Array.from({ length: 73 }, (_, i) =>
      mRow({ time_bucket: `2026-10-07T09:${String(59 - i).padStart(2, '0')}:00Z` }),
    )
    metricsMock.mockResolvedValue(metricsResp({ rows }))
    const w = await mountView()
    expect(w.findAll('.ro__panel')[3]!.findAll('.ro__item').length).toBe(50)
    expect(w.text()).toContain('另有 23 行没有显示，这里只列了最新的 50 行')
  })

  it('★ 恰好 50 行 ⇒ 不出「未显示」提示（证明不是恒真）', async () => {
    const rows = Array.from({ length: 50 }, (_, i) =>
      mRow({ time_bucket: `2026-10-07T09:${String(59 - i).padStart(2, '0')}:00Z` }),
    )
    metricsMock.mockResolvedValue(metricsResp({ rows }))
    const w = await mountView()
    expect(w.text()).not.toContain('行没有显示')
  })
})

describe('★★ 判据 8：指针字段键缺失 ⇒ 「—」', () => {
  function kvOf(w: ReturnType<typeof mount>, label: string): string[] {
    return w
      .findAll('.ro__kv-item')
      .filter((e) => e.find('.ro__kv-l').text() === label)
      .map((e) => e.find('.ro__kv-v').text())
  }

  it('★★ p95_latency_ms 键缺失 ⇒ 「—」（不是 0ms）', async () => {
    const r = mRow()
    delete (r as Record<string, unknown>).p95_latency_ms
    metricsMock.mockResolvedValue(metricsResp({ rows: [r] }))
    const w = await mountView()
    expect(kvOf(w, 'P95 延时')).toEqual(['—'])
  })

  it('★ p95 有值 ⇒ 渲染 Nms（证明上一条不是恒真）', async () => {
    const w = await mountView()
    expect(kvOf(w, 'P95 延时')).toEqual(['1200ms'])
  })

  it('★★ accuracy_rate 键缺失 ⇒ 「—」（不是 0.0%）', async () => {
    const r = mRow()
    delete (r as Record<string, unknown>).accuracy_rate
    metricsMock.mockResolvedValue(metricsResp({ rows: [r] }))
    const w = await mountView()
    expect(kvOf(w, '准确率')).toEqual(['—'])
  })

  it('★★ 四类行分别标注（后端 GROUPING SETS，不是 task×provider 交叉）', async () => {
    const rows = [
      mRow({ time_bucket: '2026-10-07T09:55:00Z', task_type: 'chat', predicted_provider: 'openai' }),
      mRow({ time_bucket: '2026-10-07T09:50:00Z', task_type: 'chat', predicted_provider: undefined }),
      mRow({ time_bucket: '2026-10-07T09:45:00Z', task_type: undefined, predicted_provider: 'openai' }),
      (() => {
        const r = mRow({ time_bucket: '2026-10-07T09:40:00Z' })
        delete (r as Record<string, unknown>).task_type
        delete (r as Record<string, unknown>).predicted_provider
        return r
      })(),
    ]
    metricsMock.mockResolvedValue(metricsResp({ rows }))
    const w = await mountView()
    const txt = w.text()
    expect(txt).toContain('任务 × 供应商')
    expect(txt).toContain('任务汇总')
    expect(txt).toContain('供应商汇总')
    expect(txt).toContain('全局汇总')
  })

  it('★★★ 三类汇总行标「汇总行」，task×provider 行**不**标', async () => {
    const rows = [
      mRow({ time_bucket: '2026-10-07T09:55:00Z', task_type: 'chat', predicted_provider: 'openai' }),
      mRow({ time_bucket: '2026-10-07T09:50:00Z', task_type: 'chat', predicted_provider: undefined }),
    ]
    metricsMock.mockResolvedValue(metricsResp({ rows }))
    const w = await mountView()
    // 2 行里只有 1 个「汇总行」标签
    expect(w.findAll('.ro__agg').length).toBe(1)
  })

  it('★★ 供应商汇总行显示 provider，且不再说成「全部任务」（那会误导）', async () => {
    const r = mRow({ time_bucket: '2026-10-07T09:45:00Z', task_type: undefined, predicted_provider: 'anthropic' })
    metricsMock.mockResolvedValue(metricsResp({ rows: [r] }))
    const w = await mountView()
    expect(w.text()).toContain('供应商汇总')
    expect(w.text()).toContain('anthropic')
    expect(w.text()).not.toContain('全部任务')
  })

  it('★★★ metrics 读物化聚合表 ⇒ 面板必须说明它可能与实时面板对不上', async () => {
    const w = await mountView()
    expect(w.text()).toContain('后台定时汇总出来的聚合表')
    expect(w.text()).toContain('这里会和上面的准确率对不上')
  })

  it('★ 四类行说明常驻', async () => {
    const w = await mountView()
    expect(w.text()).toContain('四种行')
    expect(w.text()).toContain('别当成又一条预测')
  })
})

describe('★★ 判据 10：四端点各自独立失败', () => {
  it('★★ stats 失败 ⇒ 其余三个照常显示', async () => {
    statsMock.mockRejectedValueOnce(new Error('stats boom'))
    const w = await mountView()
    expect(w.text()).toContain('stats boom')
    expect(w.text()).toContain('87.0%') // 桶仍在
    expect(w.text()).toContain('探索率') // parameters 仍在
    expect(w.text()).toContain('1200ms') // metrics 仍在
  })

  it('★★ accuracy 失败 ⇒ 不清空 metrics', async () => {
    accMock.mockRejectedValueOnce(new Error('acc boom'))
    const w = await mountView()
    expect(w.text()).toContain('acc boom')
    expect(w.text()).toContain('1200ms')
    expect(w.text()).toContain('87.0%')
  })

  it('★★ metrics 失败 ⇒ 不清空准确率桶', async () => {
    metricsMock.mockRejectedValueOnce(new Error('metrics boom'))
    const w = await mountView()
    expect(w.text()).toContain('metrics boom')
    expect(w.text()).toContain('87.0%')
  })

  it('★★ 空桶 ⇒ 「没有反馈样本」而不是空白', async () => {
    accMock.mockResolvedValue(accResp({ buckets: [] }))
    const w = await mountView()
    expect(w.text()).toContain('这个窗口内没有反馈样本')
  })

  it('★★ 空 metrics ⇒ 「没有查到聚合明细」', async () => {
    metricsMock.mockResolvedValue(metricsResp({ rows: [] }))
    const w = await mountView()
    expect(w.text()).toContain('没有查到聚合明细')
  })
})