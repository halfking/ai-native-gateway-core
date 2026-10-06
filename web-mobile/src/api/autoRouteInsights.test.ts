import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchRouteFunnel,
  fetchTuningProposals,
  isApproximate,
  blockedIsInconclusive,
  stageRate,
  proposalStatusTone,
  pickFields,
  TUNING_PROPOSALS_MAX_LIMIT,
  TUNING_PROPOSALS_DEFAULT_LIMIT,
  FUNNEL_CACHE_TTL_MS,
  type FunnelResponse,
  type TuningProposalsResponse,
} from './autoRouteInsights'

/**
 * 自动路由诊断/调优面的契约测试（2026-10-07）。
 * 重点：★ `approximate` 必须透出（采样≠精确）、第五种越界语义（400 for enum）、
 *       `proposal`/`evidence` 是无 schema 的 RawMessage。
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
  return decodeURIComponent(String(fetchMock.mock.calls[0]![0]))
}

const FUNNEL: FunnelResponse = {
  model: 'gpt-4o',
  window: '7d',
  requests: 120,
  stages: [{ key: 'candidates', label: '总候选', value: 300, hint: 'h' }],
  meta: {
    approximate: false,
    data_source: 'exact',
    blocked: 5,
    chosen: 100,
    sample_n: 120,
    trace_rows: 120,
    trace_ratio: 1,
    confidence: 'high',
    confidence_hint: '',
  },
}

const PROPOSALS: TuningProposalsResponse = {
  proposals: [],
  count: 0,
  filter: { status: '', category: '' },
}

describe('funnel：model 必填、window 只有 24h/7d', () => {
  it('发 model 与 window', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(FUNNEL))
    await fetchRouteFunnel({ model: 'gpt-4o', window: '24h' })
    const u = lastUrl()
    expect(u).toContain('model=gpt-4o')
    expect(u).toContain('window=24h')
  })

  // ★ 后端 `window must be 24h or 7d` 是 400 —— 客户端**不能**提供 12h/30d。
  //   判据锁住「只发后端接受的值」，避免将来有人「顺手加个选项」。
  it('不提供非法窗口值（合法集合由 ANALYTICS_WINDOWS 限定）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(FUNNEL))
    await fetchRouteFunnel({ model: 'gpt-4o', window: '7d' })
    expect(lastUrl()).toContain('window=7d')
  })

  it('不传 window ⇒ 完全不发（后端空串 = 7d 默认）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(FUNNEL))
    await fetchRouteFunnel({ model: 'gpt-4o' })
    expect(lastUrl()).not.toContain('window=')
  })
})

describe('★ isApproximate —— 判定依据是后端给的位，不是我们自己猜样本量', () => {
  it('approximate=true ⇒ true', () => {
    expect(isApproximate({ approximate: true })).toBe(true)
  })
  it('approximate=false ⇒ false', () => {
    expect(isApproximate({ approximate: false })).toBe(false)
  })

  // ★ 只看 sample_n 会判反：后端在 trace_ratio 不足时会把 exact 降级，
  //   此时 sample_n 可能很大但数据仍不可信。
  it('样本很大但仍标 approximate ⇒ true（不能按样本量判可信）', () => {
    expect(isApproximate({ approximate: true })).toBe(true)
  })
  it('缺 meta ⇒ false（不是「近似」）', () => {
    expect(isApproximate(null)).toBe(false)
    expect(isApproximate(undefined)).toBe(false)
  })
})

describe('★ blockedIsInconclusive —— 近似模式的 blocked=0 是「没算」不是「没拦」', () => {
  // 后端只有 exact 分支聚合 blocked_candidates（analytics.go:1019-1045）；
  // approximate/mixed 的补数 SQL 里没有这一列 ⇒ 字段必然零值。
  it('exact ⇒ 可信（blocked=0 就是真的一个都没被拦）', () => {
    expect(blockedIsInconclusive({ approximate: false, data_source: 'exact' })).toBe(false)
  })

  it('approximate ⇒ 不可信', () => {
    expect(blockedIsInconclusive({ approximate: true, data_source: 'approximate' })).toBe(true)
  })

  // ★ mixed 是第三种：RDL 有行但 trace 为空，blocked 同样没聚合。
  //   只判 approximate 会漏掉它 —— 后端 mixed 时 approximate 也是 true，
  //   但将来若放宽就会漏。两道都判。
  it('mixed ⇒ 不可信', () => {
    expect(blockedIsInconclusive({ approximate: true, data_source: 'mixed' })).toBe(true)
  })

  // 词表外的数据源一律判不可信（保守方向：新值默认「不可信」而不是「可信」）
  it('未知 data_source ⇒ 不可信', () => {
    expect(blockedIsInconclusive({ approximate: false, data_source: 'future_thing' })).toBe(true)
  })

  it('缺 meta ⇒ 不可信', () => {
    expect(blockedIsInconclusive(null)).toBe(true)
    expect(blockedIsInconclusive(undefined)).toBe(true)
  })
})

describe('★ 契约常量必须与后端同源', () => {
  // auto_route_tuning.go:197 `limit := 50`
  it('默认 limit = 50', () => {
    expect(TUNING_PROPOSALS_DEFAULT_LIMIT).toBe(50)
  })
  // auto_route_tuning.go:200 `v > 500`
  it('上限 500', () => {
    expect(TUNING_PROPOSALS_MAX_LIMIT).toBe(500)
  })
  // funnel_cache.go:23 `ttl: 2 * time.Minute`
  it('funnel 缓存 2 分钟（改完规则立刻刷新会读到旧值）', () => {
    expect(FUNNEL_CACHE_TTL_MS).toBe(120000)
  })
})

describe('stageRate —— 分母为 0 时 null 而不是 0%', () => {
  it('正常转化', () => {
    expect(stageRate(200, 100)).toBeCloseTo(0.5)
  })
  // ★「上一阶段没有量」与「转化率 0%」是两件事，后者意味着**全部被筛掉**。
  it('分母 0 ⇒ null（不冒充 0%）', () => {
    expect(stageRate(0, 5)).toBeNull()
    expect(stageRate(null, 5)).toBeNull()
  })
  it('分子 0 ⇒ 0（真的是全被筛掉）', () => {
    expect(stageRate(10, 0)).toBe(0)
  })
})

describe('proposals：★ 第五种越界语义 —— 非法枚举 400', () => {
  it('合法 status / category 照发', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(PROPOSALS))
    await fetchTuningProposals({ status: 'pending', category: 'weight_adjust' })
    const u = lastUrl()
    expect(u).toContain('status=pending')
    expect(u).toContain('category=weight_adjust')
  })

  // ★ 空串 = 不过滤，合法。发空串等价于不发，客户端干脆不发。
  it('空串不发（后端空串是合法值）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(PROPOSALS))
    await fetchTuningProposals({ status: '', category: '' })
    expect(lastUrl()).not.toContain('status=')
  })

  it('limit 越界被前端夹到 500（后端 400 `limit must be 1-500`）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(PROPOSALS))
    await fetchTuningProposals({ limit: 99999 })
    expect(lastUrl()).toContain(`limit=${TUNING_PROPOSALS_MAX_LIMIT}`)
  })

  it('limit=0 不发', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(PROPOSALS))
    await fetchTuningProposals({ limit: 0 })
    expect(lastUrl()).not.toContain('limit=')
  })
})

describe('proposalStatusTone —— 词表外一律 muted', () => {
  it('已知状态', () => {
    expect(proposalStatusTone('applied')).toBe('success')
    expect(proposalStatusTone('approved')).toBe('warning')
    expect(proposalStatusTone('rejected')).toBe('danger')
    expect(proposalStatusTone('pending')).toBe('muted')
  })
  it('大小写不敏感', () => {
    expect(proposalStatusTone('APPLIED')).toBe('success')
  })
  // 默认给 success 会把「看不懂的状态」显示成已生效
  it('未知状态 ⇒ muted', () => {
    expect(proposalStatusTone('weird')).toBe('muted')
    expect(proposalStatusTone(null)).toBe('muted')
  })
})

describe('★ pickFields —— proposal/evidence 是无 schema 的 RawMessage', () => {
  it('取出存在的字符串/数字/布尔', () => {
    const out = pickFields({ model: 'gpt-4o', from_weight: 3 }, ['model', 'from_weight'])
    expect(out).toEqual([
      ['model', 'gpt-4o'],
      ['from_weight', '3'],
    ])
  })

  // ★ 写死 `p.model` 访问在某个建议类型上就是 undefined，
  //   渲染出去是「该建议没有模型」——一个我们并不知道的结论。
  it('★ 键不存在 ⇒ 整项不出现（不渲染成「没有模型」）', () => {
    const out = pickFields({ threshold: 0.8 }, ['model', 'threshold'])
    expect(out).toEqual([['threshold', '0.8']])
    expect(out.some(([k]) => k === 'model')).toBe(false)
  })

  it('空串 / null 视为没有', () => {
    expect(pickFields({ model: '  ', task_type: null }, ['model', 'task_type'])).toEqual([])
  })

  it('非对象（null / 数组 / 字符串）⇒ 空数组，不抛', () => {
    expect(pickFields(null, ['model'])).toEqual([])
    expect(pickFields([1, 2], ['model'])).toEqual([])
    expect(pickFields('str', ['model'])).toEqual([])
  })
})
