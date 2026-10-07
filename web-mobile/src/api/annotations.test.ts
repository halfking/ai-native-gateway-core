import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { req } from '@/api/client'
import {
  fetchAnnotationStats,
  unwrapAnnotationStats,
  annotationAccuracyMeaningless,
  annotationCountsContradict,
  annotationOverallUnavailable,
  annotationAllDistributionsEmpty,
  providerAccuracyContradicts,
  fetchSamples,
  unwrapSamples,
  samplesPageEffective,
  samplesSizeEffective,
  samplesPerStrataEffective,
  samplesStrategyValid,
  samplesStrategyEffective,
  samplesHasNextPage,
  fetchFirstTurnSamples,
  unwrapFirstTurnSamples,
  firstTurnDateValid,
  ANNOTATION_STATS_KEYS,
  ANNOTATION_STATS_OVERALL_KEYS,
  PROVIDER_ACCURACY_KEYS,
  ANNOTATOR_STATS_KEYS,
  REASON_DISTRIBUTION_KEYS,
  SAMPLES_KEYS,
  FIRST_TURN_KEYS,
  SAMPLES_SIZE_DEFAULT,
  SAMPLES_SIZE_MAX,
  SAMPLES_PER_STRATA_DEFAULT,
  SAMPLES_PER_STRATA_MAX,
} from '@/api/annotations'

/**
 * annotations API 的不变量（2026-10-08，第六十一批）。
 *
 * ★ 本族最该被钉住的：
 *   ① **零标注 ⇒ stats 整条 500**（`annotation_stats` 是无聚合的单行汇总表，
 *      空表返 `pgx.ErrNoRows`，handler.go:1136-1139 直接 500）
 *      ⇒ 客户端**不能**把失败读成「统计为零」。
 *   ② **`overall` 是指针** ⇒ 唯一可能为 `null` 的块，null 时不得要求子键。
 *   ③ **字段名 ≠ JSON 键**：`ReasonDistribution.Percent` 的 tag 是 `percentage`。
 *   ④ **三个参数三种越界行为**（page 静默 / size 双向钳位 / per_strata 钳位）
 *      且 **`strategy` 是唯一会 400 的**。
 *   ⑤ **`strategy` 是条件键**：键缺失 = recent。
 *   ⑥ **first-turn 的缺省日期是「今天」**（不是「不限」），
 *      且日期格式错会 400 —— 与 samples 的缺省不限窗口语义相反。
 */

vi.mock('@/api/client', () => ({
  req: vi.fn(),
}))

const reqMock = req as unknown as ReturnType<typeof vi.fn>

/** 取得第 n 次调用实际发出的 URL 路径（不含 origin）。 */
function urlOf(n = 0): string {
  return reqMock.mock.calls[n]![1] as string
}

beforeEach(() => {
  reqMock.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

/* ── 夹具：逐字照抄后端 ─────────────────────────────────────────────── */

/** annotation/types.go:60-68 + 71-78 + 81-91 + 94-98。 */
const STATS_FULL = {
  overall: {
    total_annotations: 120,
    correct_count: 100,
    incorrect_count: 20,
    accuracy_percent: 83.33,
    num_annotators: 4,
    first_annotation_at: '2026-10-01T02:00:00Z',
    last_annotation_at: '2026-10-08T02:00:00Z',
  },
  by_provider: [
    { provider: 'openai', total_predictions: 80, correct_predictions: 70, incorrect_predictions: 10, accuracy_percent: 87.5, avg_confidence: 0.9 },
  ],
  by_annotator: [
    {
      annotator: 'alice',
      total_annotations: 60,
      correct_count: 50,
      incorrect_count: 10,
      accuracy_percent: 83.33,
      first_annotation_at: '2026-10-01T02:00:00Z',
      last_annotation_at: '2026-10-07T02:00:00Z',
      hours_span: 140,
    },
  ],
  by_reason: [{ reason: 'wrong_model', count: 12, percentage: 60 }],
}

/** ★ 零标注：accuracy_percent 的 0 与「无意义」在值上不可分。 */
const STATS_ZERO = {
  ...STATS_FULL,
  overall: {
    total_annotations: 0,
    correct_count: 0,
    incorrect_count: 0,
    accuracy_percent: 0,
    num_annotators: 0,
    first_annotation_at: null,
    last_annotation_at: null,
  },
  by_provider: [],
  by_annotator: [],
  by_reason: [],
}

/** ★ `overall` 是指针字段 ⇒ 可以整个块是 null。 */
const STATS_NULL_OVERALL = { ...STATS_FULL, overall: null }

/** ★ 计数器自相矛盾：correct+incorrect > total。 */
const STATS_CONTRADICT = {
  ...STATS_FULL,
  overall: { ...STATS_FULL.overall, correct_count: 100, incorrect_count: 40, total_annotations: 120 },
}

const SAMPLES_FULL = {
  samples: [
    { request_id: 'r1', model: 'gpt-4o', confidence: 0.9 },
    { request_id: 'r2', model: 'claude', confidence: 0.7 },
  ],
  total: 2,
}

/** ★ `strategy` 条件键：只在非 recent 时出现（handler.go:225-227）。 */
const SAMPLES_DISAGREEMENT = { ...SAMPLES_FULL, strategy: 'disagreement' }

const FIRST_TURN_FULL = {
  samples: [{ request_id: 'r1', turn_no: 1 }],
  total: 1,
}

/* ═══════════════════════════════════════════════════════════════════════
 * A. stats
 * ═══════════════════════════════════════════════════════════════════════ */

describe('annotations stats（admin/handler.go:1390）', () => {
  it('正常载荷逐键解包通过', () => {
    const r = unwrapAnnotationStats(STATS_FULL)
    expect(r.overall!.total_annotations).toBe(120)
    expect(r.by_provider[0]!.provider).toBe('openai')
    expect(r.by_annotator[0]!.annotator).toBe('alice')
    expect(r.by_reason[0]!.percentage).toBe(60)
  })

  it('fetchAnnotationStats 走裸 JSON 路径（不带信封）', async () => {
    reqMock.mockResolvedValue(STATS_FULL)
    await fetchAnnotationStats()
    expect(reqMock).toHaveBeenCalledWith('GET', '/api/admin/annotations/stats', undefined, undefined)
  })

  it('★★ overall 为 null 时不得要求它的子键', () => {
    // handler.go:131 是 `*AnnotationStats` ⇒ 序列化成 null
    const r = unwrapAnnotationStats(STATS_NULL_OVERALL)
    expect(r.overall).toBeNull()
    expect(annotationOverallUnavailable(r)).toBe(true)
  })

  it('★ overall 为 null 时其余三块仍要逐键钉', () => {
    expect(() => unwrapAnnotationStats({
      ...STATS_NULL_OVERALL,
      by_provider: [{ provider: 'openai' }],
    })).toThrow(/by_provider\[0\] 缺 5 个键/)
  })

  it('★★ 零标注（total=0）⇒ 百分数被标成无意义', () => {
    const r = unwrapAnnotationStats(STATS_ZERO)
    expect(annotationAccuracyMeaningless(r.overall!)).toBe(true)
    expect(annotationAllDistributionsEmpty(r)).toBe(true)
  })

  it('有标注 ⇒ 百分数有意义', () => {
    const r = unwrapAnnotationStats(STATS_FULL)
    expect(annotationAccuracyMeaningless(r.overall!)).toBe(false)
    expect(annotationAllDistributionsEmpty(r)).toBe(false)
  })

  it('★★ 正确+错误 > 总数 ⇒ 计数器自相矛盾', () => {
    const r = unwrapAnnotationStats(STATS_CONTRADICT)
    expect(annotationCountsContradict(r.overall!)).toBe(true)
  })

  it('逐 provider 的正确+错误 > 预测总数 ⇒ 自相矛盾', () => {
    expect(providerAccuracyContradicts(STATS_FULL.by_provider[0]!)).toBe(false)
    expect(providerAccuracyContradicts({
      ...STATS_FULL.by_provider[0]!,
      correct_predictions: 70,
      incorrect_predictions: 30,
      total_predictions: 80,
    })).toBe(true)
  })

  it('★ 缺 overall 键（不是 null，是缺键）⇒ 抛错', () => {
    const { overall, ...noOverall } = STATS_FULL
    void overall
    expect(() => unwrapAnnotationStats(noOverall)).toThrow(/缺 1 个键（overall）/)
  })

  it('★ 拿到 dashboardapi 信封 ⇒ 报错（跨家族误用）', () => {
    expect(() => unwrapAnnotationStats({
      success: true, data: STATS_FULL, timestamp: '2026-10-08T00:00:00Z',
    })).toThrow(/dashboardapi 信封形状/)
  })

  it('★ 数组/标量载荷 ⇒ 报错而不是当成空数组', () => {
    expect(() => unwrapAnnotationStats([])).toThrow(/期望裸对象，实得 array/)
    expect(() => unwrapAnnotationStats(null)).toThrow(/期望裸对象，实得 null/)
    expect(() => unwrapAnnotationStats('x')).toThrow(/期望裸对象，实得 string/)
  })

  it('★★ 零标注时后端会 500 —— 客户端必须让它以错误形态冒出来，不得降级成 0', async () => {
    // ★ 这条钉的是「不写兜底」：本族**故意没有** try/catch 降级，
    //   因为 500 与「统计为零」在响应上不可分，降级就是把故障讲成业务事实。
    reqMock.mockRejectedValue(Object.assign(new Error('failed to get overall stats'), { status: 500 }))
    await expect(fetchAnnotationStats()).rejects.toThrow('failed to get overall stats')
  })

  it('by_provider 元素不是对象 ⇒ 抛错', () => {
    expect(() => unwrapAnnotationStats({ ...STATS_FULL, by_provider: [null] }))
      .toThrow(/by_provider\[0\] 不是对象/)
  })

  it('by_reason 缺 percentage（写成 percent）⇒ 抛错', () => {
    // ★ Go 字段名是 Percent，JSON 键是 percentage（types.go:97）
    expect(() => unwrapAnnotationStats({
      ...STATS_FULL,
      by_reason: [{ reason: 'x', count: 1, percent: 50 }],
    })).toThrow(/by_reason\[0\] 缺 1 个键（percentage）/)
  })

  it('by_annotator 缺 hours_span ⇒ 抛错', () => {
    const { hours_span, ...noSpan } = STATS_FULL.by_annotator[0]!
    void hours_span
    expect(() => unwrapAnnotationStats({ ...STATS_FULL, by_annotator: [noSpan] }))
      .toThrow(/by_annotator\[0\] 缺 1 个键（hours_span）/)
  })

  it('导出的键清单与后端 struct tag 一一对应', () => {
    expect([...ANNOTATION_STATS_KEYS]).toEqual(['overall', 'by_provider', 'by_annotator', 'by_reason'])
    expect(ANNOTATION_STATS_OVERALL_KEYS.length).toBe(7)
    expect(PROVIDER_ACCURACY_KEYS.length).toBe(6)
    expect(ANNOTATOR_STATS_KEYS.length).toBe(8)
    expect([...REASON_DISTRIBUTION_KEYS]).toEqual(['reason', 'count', 'percentage'])
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * B. samples：三个参数三种越界行为 + strategy 会 400
 * ═══════════════════════════════════════════════════════════════════════ */

describe('annotations samples（admin/handler.go:1386）', () => {
  it('正常载荷解包通过', () => {
    const r = unwrapSamples(SAMPLES_FULL)
    expect(r.samples.length).toBe(2)
    expect(r.total).toBe(2)
  })

  it('★★ page 越界：静默回落 1（不是 400）', () => {
    expect(samplesPageEffective(0)).toBe(1)
    expect(samplesPageEffective(-5)).toBe(1)
    expect(samplesPageEffective(3)).toBe(3)
    expect(samplesPageEffective(undefined)).toBe(1)
    expect(samplesPageEffective(1.5)).toBe(1)
  })

  it('★ size 越界：两个方向都静默钳位（<1 → 50，>200 → 200）', () => {
    expect(samplesSizeEffective(0)).toBe(SAMPLES_SIZE_DEFAULT)
    expect(samplesSizeEffective(-1)).toBe(SAMPLES_SIZE_DEFAULT)
    expect(samplesSizeEffective(201)).toBe(SAMPLES_SIZE_MAX)
    expect(samplesSizeEffective(200)).toBe(200)
    expect(samplesSizeEffective(50)).toBe(50)
  })

  it('★ per_strata 越界：钳位到 [1,20]', () => {
    expect(samplesPerStrataEffective(0)).toBe(SAMPLES_PER_STRATA_DEFAULT)
    expect(samplesPerStrataEffective(21)).toBe(SAMPLES_PER_STRATA_MAX)
    expect(samplesPerStrataEffective(20)).toBe(20)
    expect(samplesPerStrataEffective(undefined)).toBe(SAMPLES_PER_STRATA_DEFAULT)
  })

  it('★★★ 非法 strategy 前端就拦下，不发必 400 的请求', async () => {
    await expect(fetchSamples({ strategy: 'nope' as never })).rejects.toThrow(/采样策略非法/)
    expect(reqMock).not.toHaveBeenCalled()
  })

  it('三个合法 strategy 都放行', async () => {
    reqMock.mockResolvedValue(SAMPLES_FULL)
    for (const s of ['recent', 'disagreement', 'stratified'] as const) {
      expect(samplesStrategyValid(s)).toBe(true)
      await fetchSamples({ strategy: s })
    }
    expect(samplesStrategyValid('nope')).toBe(false)
    expect(samplesStrategyValid(undefined)).toBe(true)
    expect(samplesStrategyValid(null)).toBe(false)
  })

  it('★ strategy 是条件键：键缺失 ⇒ recent（不是「策略未知」）', () => {
    expect(samplesStrategyEffective(unwrapSamples(SAMPLES_FULL))).toBe('recent')
    expect(samplesStrategyEffective(unwrapSamples(SAMPLES_DISAGREEMENT))).toBe('disagreement')
  })

  it('★ query 参数逐个发对，annotated 必须是字面量', async () => {
    reqMock.mockResolvedValue(SAMPLES_FULL)
    await fetchSamples({
      page: 2, size: 30, startDate: '2026-10-01', endDate: '2026-10-08',
      minConfidence: 0.5, maxConfidence: 0.9, annotated: true, annotator: 'alice',
      strategy: 'stratified', perStrata: 8,
    })
    const u = urlOf()
    expect(u).toContain('page=2')
    expect(u).toContain('size=30')
    expect(u).toContain('start_date=2026-10-01')
    expect(u).toContain('end_date=2026-10-08')
    expect(u).toContain('min_confidence=0.5')
    expect(u).toContain('max_confidence=0.9')
    // ★ 后端是 `query.Get("annotated") == "true"`，必须发字面量
    expect(u).toContain('annotated=true')
    expect(u).toContain('annotator=alice')
    expect(u).toContain('strategy=stratified')
    expect(u).toContain('per_strata=8')
  })

  it('★ annotated=false 也要发（`query.Has` 判断的是存在性）', async () => {
    reqMock.mockResolvedValue(SAMPLES_FULL)
    await fetchSamples({ annotated: false })
    expect(urlOf()).toContain('annotated=false')
  })

  it('★ 不填 annotated 时整个参数不出现（否则会把「不筛选」变成「筛未标注」）', async () => {
    reqMock.mockResolvedValue(SAMPLES_FULL)
    await fetchSamples({ page: 1 })
    expect(urlOf()).not.toContain('annotated')
  })

  it('★ 本页取满且 total 还有更多 ⇒ 有下一页', () => {
    const full = { samples: new Array(50).fill({ request_id: 'r' }), total: 120 }
    expect(samplesHasNextPage(unwrapSamples(full), 50)).toBe(true)
  })

  it('本页取不满 ⇒ 没有下一页', () => {
    expect(samplesHasNextPage(unwrapSamples(SAMPLES_FULL), 50)).toBe(false)
  })

  it('★ 取满但 total 就是本页数 ⇒ 没有下一页', () => {
    const full = { samples: new Array(50).fill({ request_id: 'r' }), total: 50 }
    expect(samplesHasNextPage(unwrapSamples(full), 50)).toBe(false)
  })

  it('缺 total ⇒ 抛错', () => {
    const { total, ...noTotal } = SAMPLES_FULL
    void total
    expect(() => unwrapSamples(noTotal)).toThrow(/缺 1 个键（total）/)
  })

  it('samples 不是数组 ⇒ 抛错而不是当空', () => {
    expect(() => unwrapSamples({ ...SAMPLES_FULL, samples: {} })).toThrow(/samples 不是数组/)
  })

  it('samples 元素是 null ⇒ 抛错（模板会炸）', () => {
    expect(() => unwrapSamples({ ...SAMPLES_FULL, samples: [null] }))
      .toThrow(/samples\[0\] 不是对象/)
  })

  it('strategy 不是字符串 ⇒ 抛错', () => {
    expect(() => unwrapSamples({ ...SAMPLES_FULL, strategy: 3 })).toThrow(/strategy 不是字符串/)
  })

  it('导出的必填键与后端 struct tag 一致', () => {
    expect([...SAMPLES_KEYS]).toEqual(['samples', 'total'])
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * C. first-turn-samples：缺省日期 = 今天，格式错 400
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('annotations first-turn-samples（admin/handler.go:1389）', () => {
  it('正常载荷解包通过', () => {
    const r = unwrapFirstTurnSamples(FIRST_TURN_FULL)
    expect(r.samples.length).toBe(1)
    expect(r.total).toBe(1)
  })

  it('★ 缺省不发日期 ⇒ 后端按「今天（UTC）」处理，不是「不限」', async () => {
    reqMock.mockResolvedValue(FIRST_TURN_FULL)
    await fetchFirstTurnSamples()
    const u = urlOf()
    expect(u).not.toContain('start_date')
    expect(u).not.toContain('end_date')
  })

  it('★★ 日期格式错 ⇒ 前端就拦下（后端会 400）', async () => {
    await expect(fetchFirstTurnSamples({ startDate: '2026/10/01' })).rejects.toThrow(/YYYY-MM-DD/)
    expect(reqMock).not.toHaveBeenCalled()
  })

  it('endDate 格式错同样拦下', async () => {
    await expect(fetchFirstTurnSamples({ endDate: '10-01-2026' })).rejects.toThrow(/YYYY-MM-DD/)
    expect(reqMock).not.toHaveBeenCalled()
  })

  it('合法日期放行', () => {
    expect(firstTurnDateValid('2026-10-01')).toBe(true)
    expect(firstTurnDateValid('2026-10-01T00:00:00Z')).toBe(false)
    expect(firstTurnDateValid(undefined)).toBe(true)
  })

  it('★ 拿到 samples 的形状（有 strategy 键）⇒ 报错（串了端点）', () => {
    expect(() => unwrapFirstTurnSamples(SAMPLES_DISAGREEMENT))
      .toThrow(/本端点没有 strategy 键/)
  })

  it('必填键只有 samples/total', () => {
    expect([...FIRST_TURN_KEYS]).toEqual(['samples', 'total'])
  })

  it('分页参数照发', async () => {
    reqMock.mockResolvedValue(FIRST_TURN_FULL)
    await fetchFirstTurnSamples({ page: 3, size: 20, startDate: '2026-10-01', endDate: '2026-10-02' })
    const u = urlOf()
    expect(u).toContain('page=3')
    expect(u).toContain('size=20')
    expect(u).toContain('start_date=2026-10-01')
    expect(u).toContain('end_date=2026-10-02')
  })

  it('samples 元素不是对象 ⇒ 抛错', () => {
    expect(() => unwrapFirstTurnSamples({ samples: ['x'], total: 1 }))
      .toThrow(/samples\[0\] 不是对象/)
  })
})
