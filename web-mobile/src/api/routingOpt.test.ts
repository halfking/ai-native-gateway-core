import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchRoutingOptStats,
  fetchRoutingOptAccuracy,
  fetchRoutingOptParameters,
  fetchRoutingOptMetrics,
  formatRoutingOptAccuracy,
  accuracyIsLive,
  accuracyIsStaleFallback,
  accuracyHasNoSource,
  accuracySourceKnown,
  metricsLikelyTruncated,
  metricsRowDim,
  metricsRowIsAggregate,
  ROUTING_OPT_METRICS_DIMS,
  ACCURACY_SOURCES,
  ROUTING_OPT_STATS_WINDOW_HOURS,
  ROUTING_OPT_METRICS_ROW_CAP,
  ROUTING_OPT_HOURS_DEFAULT,
  ROUTING_OPT_HOURS_MAX,
  ROUTING_OPT_ACCURACY_IS_WEIGHTED_2X,
  ROUTING_OPT_AUTO_AND_HUMAN_DIFFER,
  ROUTING_OPT_BUCKETS_ALWAYS_HAVE_SAMPLES,
  type RoutingOptStats,
} from './routingOpt'

/**
 * 路由优化器读面的契约测试（2026-10-06）。
 *
 * 五条重点：
 * 1. ★★★★ 准确率是 **1:2 加权平均**（人工单条算 2 条），且响应**不给命中数**；
 * 2. ★★★★ auto 与 human 的「正确」是**两种定义**；
 * 3. ★★★★ `overall_accuracy` / `accuracy` 是 **0..1 比率**（量纲第 5 处）；
 * 4. ★★★ `accuracy` 桶**恒有样本** ⇒ 桶里 `accuracy: 0` 是真的 0%；
 * 5. ★★★ `accuracy_source` 三种来源语义完全不同；
 *    `hours` 是静默回落/静默 clamp，`stats` 窗口写死 24。
 */

const fetchMock = vi.fn()
beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})
function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}
function lastUrl(): string {
  return String(fetchMock.mock.calls[0]![0])
}

function stats(over: Partial<RoutingOptStats> = {}): RoutingOptStats {
  return {
    overall_accuracy: 0.87,
    accuracy_source: 'weighted_feedback',
    parameter_version: 7,
    human_annotations_used: 4,
    window_hours: ROUTING_OPT_STATS_WINDOW_HOURS,
    auto_samples: 120,
    human_samples: 4,
    ...over,
  }
}

describe('URL 与参数', () => {
  it('stats 裸路径，且**没有** hours 参数（窗口写死 24）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(stats()))
    await fetchRoutingOptStats()
    expect(lastUrl()).toBe('/api/admin/routing-opt/stats')
    expect(ROUTING_OPT_STATS_WINDOW_HOURS).toBe(24)
  })

  it('accuracy 无参数 ⇒ 裸路径', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ hours: 24, since: 'x', buckets: [] }))
    await fetchRoutingOptAccuracy()
    expect(lastUrl()).toBe('/api/admin/routing-opt/accuracy')
  })

  it('★ hours=168 发得出去', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ hours: 168, since: 'x', buckets: [] }))
    await fetchRoutingOptAccuracy({ hours: 168 })
    expect(lastUrl()).toContain('hours=168')
  })

  it('★★★ 省略 hours 时**不发**参数 ⇒ 后端按 ROUTING_OPT_HOURS_DEFAULT 回落到 24', async () => {
    // ★ 把常量接进判据，而不是删掉 import（TS6133）——
    //   这条判据同时钉住「后端默认是 24」这个已实测的契约。
    expect(ROUTING_OPT_HOURS_DEFAULT).toBe(24)
    fetchMock.mockResolvedValueOnce(jsonResponse({ hours: ROUTING_OPT_HOURS_DEFAULT, since: 'x', buckets: [] }))
    await fetchRoutingOptAccuracy()
    expect(lastUrl()).toBe('/api/admin/routing-opt/accuracy')
  })

  it('★★ hours=0 / 负数 / NaN **不发**（后端会静默回落 24，不是报错）', async () => {
    for (const hours of [0, -1, NaN]) {
      fetchMock.mockResolvedValueOnce(jsonResponse({ hours: 24, since: 'x', buckets: [] }))
      await fetchRoutingOptAccuracy({ hours })
      expect(lastUrl()).not.toContain('hours=')
    }
  })

  it('★★ hours=721 **不发**（后端会静默 clamp 到 720）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ hours: 24, since: 'x', buckets: [] }))
    await fetchRoutingOptAccuracy({ hours: ROUTING_OPT_HOURS_MAX + 1 })
    expect(lastUrl()).not.toContain('hours=')
  })

  it('metrics：task_type / provider 空白 ⇒ 不发（否则变成「查空白名字」）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ hours: 24, since: 'x', rows: [], truncated: false }))
    await fetchRoutingOptMetrics({ taskType: '   ', provider: '' })
    expect(lastUrl()).not.toContain('task_type')
    expect(lastUrl()).not.toContain('provider')
  })

  it('metrics：非空 ⇒ 精确匹配发送', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ hours: 24, since: 'x', rows: [], truncated: false }))
    await fetchRoutingOptMetrics({ taskType: ' chat ', provider: 'openai' })
    const u = decodeURIComponent(lastUrl())
    expect(u).toContain('task_type=chat')
    expect(u).toContain('provider=openai')
  })

  it('parameters 裸路径', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ version: 1 }))
    await fetchRoutingOptParameters()
    expect(lastUrl()).toBe('/api/admin/routing-opt/parameters')
  })
})

describe('★★★★ 判据 1/2/3：加权、两种定义、量纲', () => {
  it('★ 三个口径标记都在', () => {
    expect(ROUTING_OPT_ACCURACY_IS_WEIGHTED_2X).toBe(true)
    expect(ROUTING_OPT_AUTO_AND_HUMAN_DIFFER).toBe(true)
  })

  it('★★★ 0.87 ⇒ 87.0%（**不是** 0.87%）', () => {
    expect(formatRoutingOptAccuracy(0.87)).toBe('87.0%')
  })

  it('★★★ 1 ⇒ 100.0%；0 ⇒ 0.0%', () => {
    expect(formatRoutingOptAccuracy(1)).toBe('100.0%')
    expect(formatRoutingOptAccuracy(0)).toBe('0.0%')
  })

  it('★ 喂进 87（0..100 的值）会得到 8700.0% —— 这就是量纲用反的信号', () => {
    expect(formatRoutingOptAccuracy(87)).toBe('8700.0%')
  })

  it('★ 缺失 / null / NaN ⇒ —，不是 0.0%', () => {
    expect(formatRoutingOptAccuracy(undefined)).toBe('—')
    expect(formatRoutingOptAccuracy(null)).toBe('—')
    expect(formatRoutingOptAccuracy(NaN)).toBe('—')
  })

  it('★★ 响应**只给样本量、不给命中数** ⇒ 客户端无法验证加权值', () => {
    const s = stats()
    expect(s.auto_samples).toBe(120)
    expect(s.human_samples).toBe(4)
    // ★ 没有任何 correct 字段可用
    expect(Object.keys(s).some((k) => k.includes('correct'))).toBe(false)
  })

  it('★★ human_annotations_used 与 human_samples **恒等**（后端都取 humanTotal）', () => {
    // ★ 注意：真实响应里两者永远相等 —— 测试也必须按真实形态构造，
    //   造出「只改一个」的载荷等于在测一个后端产生不了的状态。
    expect(stats().human_annotations_used).toBe(stats().human_samples)
    const s = stats({ human_samples: 9, human_annotations_used: 9 })
    expect(s.human_annotations_used).toBe(9)
    expect(s.human_samples).toBe(9)
  })

  it('★ 加权口径下「合并准确率」与「加权准确率」不是同一个数（记录该事实）', () => {
    const autoCorrect = 108, autoTotal = 120, humanCorrect = 2, humanTotal = 4
    const weighted = (autoCorrect + 2 * humanCorrect) / (autoTotal + 2 * humanTotal)
    const pooled = (autoCorrect + humanCorrect) / (autoTotal + humanTotal)
    expect(weighted).not.toBeCloseTo(pooled, 6)
  })
})

describe('★★★ 判据 4：accuracy 桶恒有样本', () => {
  it('★ 常量在位', () => {
    expect(ROUTING_OPT_BUCKETS_ALWAYS_HAVE_SAMPLES).toBe(true)
  })

  it('★★ 桶里 samples ≥ 1 ⇒ accuracy=0 是真的 0%', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        hours: 24,
        since: 'x',
        buckets: [{ hour: '2026-10-07T10:00:00Z', task_type: 'chat', accuracy: 0, samples: 5, human_samples: 0 }],
      }),
    )
    const r = await fetchRoutingOptAccuracy()
    expect(r.buckets[0]!.samples).toBeGreaterThanOrEqual(1)
    expect(formatRoutingOptAccuracy(r.buckets[0]!.accuracy)).toBe('0.0%')
  })

  it('★ 空数组正常解析（不崩）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ hours: 24, since: 'x', buckets: [] }))
    const r = await fetchRoutingOptAccuracy()
    expect(r.buckets).toEqual([])
  })
})

describe('★★★ 判据 5：accuracy_source 三种来源', () => {
  it('★ 三个来源枚举齐', () => {
    expect([...ACCURACY_SOURCES]).toEqual(['weighted_feedback', 'persisted_state', 'none'])
  })

  it('★★ 只有 weighted_feedback 算「实时」', () => {
    expect(accuracyIsLive('weighted_feedback')).toBe(true)
    expect(accuracyIsLive('persisted_state')).toBe(false)
    expect(accuracyIsLive('none')).toBe(false)
    expect(accuracyIsLive(undefined)).toBe(false)
  })

  it('★★ persisted_state = 旧值回落，必须标出来', () => {
    expect(accuracyIsStaleFallback('persisted_state')).toBe(true)
    expect(accuracyIsStaleFallback('weighted_feedback')).toBe(false)
  })

  it('★ none = 完全没有来源', () => {
    expect(accuracyHasNoSource('none')).toBe(true)
    expect(accuracyHasNoSource('weighted_feedback')).toBe(false)
  })

  it('★★ 未知来源**不能**当实时', () => {
    expect(accuracySourceKnown('brand_new')).toBe(false)
    expect(accuracyIsLive('brand_new')).toBe(false)
  })
})

describe('metrics 截断与维度', () => {
  it('★★ 24 小时 ⇒ 按时间桶估算不会撞 2000', () => {
    expect(metricsLikelyTruncated(24)).toBe(false)
  })

  it('★★★ 720 小时（30 天）⇒ 时间桶就 8640 个，**必然**撞上限', () => {
    expect(metricsLikelyTruncated(720)).toBe(true)
  })

  it('★ 上限常量 = 2000', () => {
    expect(ROUTING_OPT_METRICS_ROW_CAP).toBe(2000)
  })

  it('★ 非法 hours ⇒ 不判截断', () => {
    expect(metricsLikelyTruncated(NaN)).toBe(false)
    expect(metricsLikelyTruncated(-1)).toBe(false)
  })

  it('★ 后端自带 truncated 标记，原样透传', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ hours: 720, since: 'x', rows: [], truncated: true }))
    const r = await fetchRoutingOptMetrics({ hours: 720 })
    expect(r.truncated).toBe(true)
  })

  it('★★★★ GROUPING SETS 产出四类行，逐类判定（含旧实现会判错的那类）', () => {
    // ★ 旧实现把「有一个维度缺失」一律当 global，
    //   于是 ('chat', undefined) 这类**任务汇总行**被误标成全局汇总 —— 这条判据就是守它的。
    expect(metricsRowDim('chat', 'openai')).toBe('task_provider')
    expect(metricsRowDim('chat', undefined)).toBe('task')
    expect(metricsRowDim(undefined, 'openai')).toBe('provider')
    expect(metricsRowDim(undefined, undefined)).toBe('global')
    // ★★ 空串**不是**维度缺失：`*string` + omitempty 只在 NULL 时丢键，
    //   存在但为空的维度会带着 `""` 出来 ⇒ 它仍算「这一维有值」。
    //   （写断言时我一度把它当成缺失，那是错的；这里把区别钉住。）
    expect(metricsRowDim('', '')).toBe('task_provider')
    expect(metricsRowDim('', undefined)).toBe('task')
  })

  it('★★★ 只有 task_provider 行不是汇总行', () => {
    expect(metricsRowIsAggregate('chat', 'openai')).toBe(false)
    expect(metricsRowIsAggregate('chat', undefined)).toBe(true)
    expect(metricsRowIsAggregate(undefined, 'openai')).toBe(true)
    expect(metricsRowIsAggregate(undefined, undefined)).toBe(true)
  })

  it('★ ROUTING_OPT_METRICS_DIMS 与 metricsRowDim 的取值集合一致（防漂）', () => {
    const seen = new Set<string>()
    for (const tt of ['chat', undefined]) {
      for (const pv of ['openai', undefined]) seen.add(metricsRowDim(tt, pv))
    }
    expect([...seen].sort()).toEqual([...ROUTING_OPT_METRICS_DIMS].sort())
  })
})