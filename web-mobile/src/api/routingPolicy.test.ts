import { describe, it, expect, vi, beforeEach } from 'vitest'
import * as api from './routingPolicy'

vi.mock('./client', () => ({ req: vi.fn(), ApiError: class extends Error {} }))

import { req } from './client'
const rq = vi.mocked(req)

function ok(payload: unknown): void {
  rq.mockResolvedValueOnce(payload)
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 夹具：逐字照抄 01-schema.sql 的 routing_policy 与 handler 的赋值
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * `row_to_json(rp)` 的产物（`routing.go:2527`）。
 * 七个 NOT NULL 列给实值，十四个可空列**给 null**（row_to_json 对可空列输出 null）。
 */
function policyRow(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    // —— NOT NULL 七列 ——
    id: 1,
    tenant_id: 'default',
    weights_json: {},
    sticky_ttl_seconds: 1800,
    local_bonus: 0,
    updated_at: '2026-10-07T08:00:00Z',
    transient_fail_threshold: 2,
    // —— 可空十四列：一律 null ——
    notes: null,
    algorithm_version: null,
    retry_per_credential: null,
    tier_fallback_max: null,
    slot_soft_limit_ratio: null,
    slot_hard_limit_ratio: null,
    slot_wait_max_ms: null,
    circuit_open_seconds: null,
    circuit_failure_threshold: null,
    circuit_max_open_seconds: null,
    featured_models: null,
    stats_window_minutes: null,
    stats_update_interval_seconds: null,
    scoring_weights_json: null,
    ...over,
  }
}

/** 同一行，但十四个可空列都有值（运维改过配置的情形）。 */
function policyRowFilled(over: Record<string, unknown> = {}): Record<string, unknown> {
  return policyRow({
    notes: 'gateway-a',
    algorithm_version: 2,
    retry_per_credential: 1,
    tier_fallback_max: 4,
    slot_soft_limit_ratio: 1,
    slot_hard_limit_ratio: 1.5,
    slot_wait_max_ms: 200,
    circuit_open_seconds: 300,
    circuit_failure_threshold: 5,
    circuit_max_open_seconds: 1800,
    featured_models: ['gpt-4o', 'claude-3-5-sonnet-20241022'],
    stats_window_minutes: 10,
    stats_update_interval_seconds: 60,
    scoring_weights_json: { price: 10, session_load: 5, failure_penalty: 20 },
    ...over,
  })
}

/** `getScoringWeights`（`:4049-4053`）回填后的形状 + 两个披露键。 */
function scoringWeights(
  over: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    price: 10,
    session_load: 5,
    failure_penalty: 20,
    default_price_cny: 5,
    default_price_usd: 5,
    display_only: true,
    note: api.SCORING_WEIGHTS_DISPLAY_ONLY_NOTE,
    ...over,
  }
}

/** featuredModel（`routing.go:4075-4080`）。 */
function featuredModel(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    name: 'gpt-4o',
    standardized_name: 'gpt-4o',
    count: 0,
    source: 'policy',
    ...over,
  }
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 常量对齐后端字面量
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('常量对齐后端字面量', () => {
  it('note 逐字对齐 routing.go:3912', () => {
    expect(api.SCORING_WEIGHTS_DISPLAY_ONLY_NOTE).toBe(
      'these weights only affect /api/routing/resolve and /api/routing/score-details previews, not live routing',
    )
  })

  it('★ 默认权重五个键与值逐字对齐 :4027-4033', () => {
    expect([...api.SCORING_WEIGHTS_DEFAULT_KEYS]).toEqual([
      'price', 'session_load', 'failure_penalty', 'default_price_cny', 'default_price_usd',
    ])
    expect(api.SCORING_WEIGHT_DEFAULTS).toEqual({
      price: 10, session_load: 5, failure_penalty: 20,
      default_price_cny: 5.0, default_price_usd: 5.0,
    })
  })

  it('★ routing_policy 七必填 + 十四可空 = 二十一列', () => {
    expect(api.ROUTING_POLICY_REQUIRED_KEYS).toHaveLength(7)
    expect(api.ROUTING_POLICY_NULLABLE_KEYS).toHaveLength(14)
    expect(api.ROUTING_POLICY_KEYS).toHaveLength(21)
  })

  it('★ 必填与可空两集无交集', () => {
    const req = new Set<string>(api.ROUTING_POLICY_REQUIRED_KEYS)
    expect(api.ROUTING_POLICY_NULLABLE_KEYS.filter((k) => req.has(k))).toEqual([])
  })

  it('★ source 是两值封闭枚举', () => {
    expect([...api.FEATURED_MODEL_SOURCES]).toEqual(['policy', 'usage'])
  })

  it('usage 侧 limit 是 20', () => {
    expect(api.FEATURED_MODELS_USAGE_LIMIT).toBe(20)
  })

  it('★ 硬编码 default 租户的是三个端点，走调用方的是 featured-models', () => {
    expect([...api.ROUTING_DEFAULT_TENANT_ONLY_ENDPOINTS]).toEqual([
      'policy', 'featured', 'scoring-weights',
    ])
    expect([...api.ROUTING_CALLER_SCOPED_ENDPOINTS]).toEqual(['featured-models'])
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 解包 · policy（row_to_json）
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('解包 · 路由策略', () => {
  it('★ 空对象解成 null（没有行 / 查询失败 / 空文本三合一）', () => {
    expect(api.unwrapRoutingPolicy({})).toBeNull()
  })

  it('整行载荷原样通过', () => {
    const r = api.unwrapRoutingPolicy(policyRow())
    expect(r).not.toBeNull()
    expect(r!.tenant_id).toBe('default')
  })

  it('十四个可空列都有值时也通过', () => {
    const r = api.unwrapRoutingPolicy(policyRowFilled())
    expect(r!.notes).toBe('gateway-a')
    expect(r!.featured_models).toEqual(['gpt-4o', 'claude-3-5-sonnet-20241022'])
  })

  it('★ 可空列的键缺失时抛错（row_to_json 不省略可空列）', () => {
    const row = policyRow()
    delete row.notes
    expect(() => api.unwrapRoutingPolicy(row)).toThrow(/缺键 notes/)
  })

  it('★ 十四列一起缺时点名数量', () => {
    const row = policyRowFilled()
    delete row.slot_wait_max_ms
    delete row.circuit_open_seconds
    expect(() => api.unwrapRoutingPolicy(row)).toThrow(/缺键 slot_wait_max_ms/)
  })

  it('必填列缺失时抛错', () => {
    const row = policyRow()
    delete row.sticky_ttl_seconds
    expect(() => api.unwrapRoutingPolicy(row)).toThrow(/缺 1 个键（sticky_ttl_seconds）/)
  })

  it('id 不是数字时抛错', () => {
    expect(() => api.unwrapRoutingPolicy(policyRow({ id: '1' }))).toThrow(/id 不是数字/)
  })

  it('tenant_id 不是字符串时抛错', () => {
    expect(() => api.unwrapRoutingPolicy(policyRow({ tenant_id: 1 }))).toThrow(
      /tenant_id 不是字符串/,
    )
  })

  it('local_bonus 不是数字时抛错', () => {
    expect(() => api.unwrapRoutingPolicy(policyRow({ local_bonus: '0.000' }))).toThrow(
      /local_bonus 不是数字/,
    )
  })

  it('updated_at 不是字符串时抛错', () => {
    expect(() => api.unwrapRoutingPolicy(policyRow({ updated_at: 1 }))).toThrow(
      /updated_at 不是字符串/,
    )
  })

  it('weights_json 不是对象时抛错', () => {
    expect(() => api.unwrapRoutingPolicy(policyRow({ weights_json: [] }))).toThrow(
      /weights_json 不是对象/,
    )
  })

  it('weights_json 为 null 时抛错（它是 NOT NULL 列）', () => {
    expect(() => api.unwrapRoutingPolicy(policyRow({ weights_json: null }))).toThrow(
      /weights_json 不是对象/,
    )
  })

  it('★ notes 为数字时抛错（可空但类型仍要校）', () => {
    expect(() => api.unwrapRoutingPolicy(policyRow({ notes: 7 }))).toThrow(
      /notes 不是字符串也不是 null/,
    )
  })

  it('★ algorithm_version 为字符串时抛错', () => {
    expect(() => api.unwrapRoutingPolicy(policyRow({ algorithm_version: '2' }))).toThrow(
      /algorithm_version 不是数字也不是 null/,
    )
  })

  it('★ featured_models 含非字符串时抛错并点名下标', () => {
    expect(() =>
      api.unwrapRoutingPolicy(policyRow({ featured_models: ['gpt-4o', 7] })),
    ).toThrow(/featured_models\[1\] 不是字符串/)
  })

  it('★ featured_models 不是数组也不是 null 时抛错', () => {
    expect(() => api.unwrapRoutingPolicy(policyRow({ featured_models: 'gpt-4o' }))).toThrow(
      /featured_models 不是数组也不是 null/,
    )
  })

  it('★ scoring_weights_json 为 null 时通过', () => {
    expect(() => api.unwrapRoutingPolicy(policyRow({ scoring_weights_json: null }))).not.toThrow()
  })

  it('scoring_weights_json 为数组时抛错', () => {
    expect(() => api.unwrapRoutingPolicy(policyRow({ scoring_weights_json: [] }))).toThrow(
      /scoring_weights_json 不是对象也不是 null/,
    )
  })

  it('响应形状不是裸对象时抛错', () => {
    expect(() => api.unwrapRoutingPolicy(null)).toThrow(/期望裸对象，实得 null/)
    expect(() => api.unwrapRoutingPolicy([])).toThrow(/期望裸对象，实得 array/)
  })

  it('★ 响应是 undefined 时也抛错', () => {
    expect(() => api.unwrapRoutingPolicy(undefined)).toThrow(
      /期望裸对象，实得 undefined/,
    )
  })

  it('★★ 拿到 featured 响应时报错（反向检测）', () => {
    expect(() => api.unwrapRoutingPolicy({ featured_models: ['gpt-4o'] })).toThrow(
      /拿到的是别的形状/,
    )
  })

  it('★ 拿到有 status 的对象时报错', () => {
    expect(() => api.unwrapRoutingPolicy({ status: 'ok' })).toThrow(/拿到的是别的形状/)
  })
})

describe('解包 · 路由精选模型（固定）', () => {
  it('健康载荷原样通过', () => {
    const r = api.unwrapRoutingFeatured({ featured_models: ['gpt-4o', 'qwen-plus'] })
    expect(r.featured_models).toHaveLength(2)
  })

  it('★ 查询失败时的空数组也通过（恒数组不是 null）', () => {
    expect(api.unwrapRoutingFeatured({ featured_models: [] }).featured_models).toEqual([])
  })

  it('缺键抛错', () => {
    expect(() => api.unwrapRoutingFeatured({})).toThrow(/缺 1 个键（featured_models）/)
  })

  it('不是数组时抛错', () => {
    expect(() => api.unwrapRoutingFeatured({ featured_models: null })).toThrow(
      /featured_models 不是数组/,
    )
  })

  it('★ 元素不是字符串时抛错并点名下标', () => {
    expect(() => api.unwrapRoutingFeatured({ featured_models: ['gpt-4o', 7] })).toThrow(
      /featured_models\[1\] 不是字符串/,
    )
  })
})

describe('解包 · 路由打分权重', () => {
  it('健康载荷原样通过', () => {
    const w = api.unwrapRoutingScoringWeights(scoringWeights())
    expect(w.price).toBe(10)
    expect(w.display_only).toBe(true)
  })

  it('★ jsonb 的额外数字键原样通过（开放形状）', () => {
    const w = api.unwrapRoutingScoringWeights(scoringWeights({ quota_penalty: 3 }))
    expect(w.quota_penalty).toBe(3)
  })

  it('★ 缺一个保证键时抛错（默认值回填保证它存在）', () => {
    const w = scoringWeights()
    delete w.price
    expect(() => api.unwrapRoutingScoringWeights(w)).toThrow(/缺 1 个键（price）/)
  })

  it('price 不是数字时抛错', () => {
    expect(() => api.unwrapRoutingScoringWeights(scoringWeights({ price: '10' }))).toThrow(
      /price 不是数字/,
    )
  })

  it('★ 缺 display_only 时抛错', () => {
    const w = scoringWeights()
    delete w.display_only
    expect(() => api.unwrapRoutingScoringWeights(w)).toThrow(/缺 1 个键（display_only）/)
  })

  it('★ display_only 为数字时抛错（只校类型不校取值）', () => {
    expect(() => api.unwrapRoutingScoringWeights(scoringWeights({ display_only: 1 }))).toThrow(
      /display_only 不是布尔值/,
    )
  })

  it('★ display_only 为 false 时照样通过（后端只写 true，客户端不判取值）', () => {
    expect(() =>
      api.unwrapRoutingScoringWeights(scoringWeights({ display_only: false })),
    ).not.toThrow()
  })

  it('note 不是字符串时抛错', () => {
    expect(() => api.unwrapRoutingScoringWeights(scoringWeights({ note: 7 }))).toThrow(
      /note 不是字符串/,
    )
  })

  it('★ 额外键是对象时抛错并点名该键', () => {
    expect(() =>
      api.unwrapRoutingScoringWeights(scoringWeights({ weird: { a: 1 } })),
    ).toThrow(/额外键 weird 类型不支持/)
  })

  it('★ 额外键为 null 时抛错（后端只摊平标量）', () => {
    expect(() =>
      api.unwrapRoutingScoringWeights(scoringWeights({ weird: null })),
    ).toThrow(/额外键 weird 类型不支持（object）/)
  })

  it('响应形状不是裸对象时抛错', () => {
    expect(() => api.unwrapRoutingScoringWeights([])).toThrow(/期望裸对象，实得 array/)
  })
})

describe('解包 · 路由精选模型（动态）', () => {
  it('健康载荷原样通过', () => {
    const r = api.unwrapRoutingFeaturedModels({ models: [featuredModel()] })
    expect(r.models).toHaveLength(1)
  })

  it('★ 空数组不是 null', () => {
    expect(api.unwrapRoutingFeaturedModels({ models: [] }).models).toEqual([])
  })

  it('缺 models 时抛错', () => {
    expect(() => api.unwrapRoutingFeaturedModels({})).toThrow(/缺 1 个键（models）/)
  })

  it('models 不是数组时抛错', () => {
    expect(() => api.unwrapRoutingFeaturedModels({ models: null })).toThrow(
      /models 不是数组/,
    )
  })

  it('★ 元素缺键抛错并点名下标', () => {
    expect(() =>
      api.unwrapRoutingFeaturedModels({ models: [featuredModel(), { name: 'x' }] }),
    ).toThrow(/models\[1\] 缺 3 个键/)
  })

  it('★ source 是未知取值时抛错（封闭枚举）', () => {
    expect(() =>
      api.unwrapRoutingFeaturedModels({
        models: [featuredModel({ source: 'manual' })],
      }),
    ).toThrow(/source 不是已知来源（manual）/)
  })

  it('usage 来源通过', () => {
    expect(() =>
      api.unwrapRoutingFeaturedModels({
        models: [featuredModel({ source: 'usage' })],
      }),
    ).not.toThrow()
  })

  it('★ name 不是字符串时抛错（四键齐全，只错类型）', () => {
    expect(() =>
      api.unwrapRoutingFeaturedModels({ models: [featuredModel({ name: 7 })] }),
    ).toThrow(/models\[0\].name 不是字符串/)
  })

  it('source 不是字符串时抛错', () => {
    expect(() =>
      api.unwrapRoutingFeaturedModels({ models: [featuredModel({ source: 1 })] }),
    ).toThrow(/source 不是字符串/)
  })

  it('count 不是数字时抛错', () => {
    expect(() =>
      api.unwrapRoutingFeaturedModels({ models: [featuredModel({ count: null })] }),
    ).toThrow(/count 不是数字/)
  })

  it('★ standardized_name 不是字符串时抛错（虽恒等于 name）', () => {
    expect(() =>
      api.unwrapRoutingFeaturedModels({
        models: [featuredModel({ standardized_name: null })],
      }),
    ).toThrow(/standardized_name 不是字符串/)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 语义判定
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('语义判定 · 策略行', () => {
  it('★ null 判为拿不到（不是说「未配置」）', () => {
    expect(api.routingPolicyIsDefaultTenant(null)).toBe(false)
  })

  it('default 租户成立', () => {
    expect(api.routingPolicyIsDefaultTenant(api.unwrapRoutingPolicy(policyRow()))).toBe(true)
  })

  it('★★ 别的租户不成立（说明服务端被换过）', () => {
    expect(
      api.routingPolicyIsDefaultTenant(api.unwrapRoutingPolicy(policyRow({ tenant_id: 'acme' }))),
    ).toBe(false)
  })
})

describe('语义判定 · 打分权重', () => {
  it('★ 全等于默认值时判为「可能是兜底」', () => {
    expect(api.routingScoringWeightsMayBeDefaults(api.unwrapRoutingScoringWeights(scoringWeights()))).toBe(
      true,
    )
  })

  it('★ 改过任一键后不再判为可能是兜底', () => {
    const w = api.unwrapRoutingScoringWeights(scoringWeights({ price: 12 }))
    expect(api.routingScoringWeightsMayBeDefaults(w)).toBe(false)
  })

  it('★ 兜底值不可能带额外键（默认分支不产出额外键）', () => {
    expect(api.routingScoringWeightsExtraKeyCount(api.unwrapRoutingScoringWeights(scoringWeights()))).toBe(
      0,
    )
  })

  it('★ 带额外键时额外键数为 1', () => {
    const w = api.unwrapRoutingScoringWeights(scoringWeights({ quota_penalty: 3 }))
    expect(api.routingScoringWeightsExtraKeyCount(w)).toBe(1)
  })

  it('★ 只改最后一个键也不判为可能是兜底', () => {
    // ★ 逐键都要有专属用例：只改 price 时，把 default_price_usd 从判据里
    //   去掉是打不出差异的。
    const w = api.unwrapRoutingScoringWeights(scoringWeights({ default_price_usd: 6 }))
    expect(api.routingScoringWeightsMayBeDefaults(w)).toBe(false)
  })

  it('★ 带额外键就不是默认值兜底', () => {
    const w = api.unwrapRoutingScoringWeights(scoringWeights({ quota_penalty: 3 }))
    expect(api.routingScoringWeightsMayBeDefaults(w)).toBe(false)
  })

  it('披露键不计入额外键', () => {
    const w = api.unwrapRoutingScoringWeights(scoringWeights())
    expect(api.routingScoringWeightsExtraKeyCount(w)).toBe(0)
  })
})

describe('语义判定 · 精选模型', () => {
  it('★ 名字对得上时不算漂移', () => {
    const r = api.unwrapRoutingFeaturedModels({ models: [featuredModel()] })
    expect(api.featuredModelNameMismatch(r.models[0]!)).toBe(false)
  })

  it('★ 名字对不上时判为契约漂移', () => {
    const r = api.unwrapRoutingFeaturedModels({
      models: [featuredModel({ standardized_name: 'gpt-4o-2024' })],
    })
    expect(api.featuredModelNameMismatch(r.models[0]!)).toBe(true)
  })

  it('policy 来源成立', () => {
    const r = api.unwrapRoutingFeaturedModels({ models: [featuredModel({ source: 'policy' })] })
    expect(api.featuredModelIsPolicyPinned(r.models[0]!)).toBe(true)
  })

  it('usage 来源不成立', () => {
    const r = api.unwrapRoutingFeaturedModels({ models: [featuredModel({ source: 'usage' })] })
    expect(api.featuredModelIsPolicyPinned(r.models[0]!)).toBe(false)
  })

  it('★ 空数组只能说「看起来没配」', () => {
    const r = api.unwrapRoutingFeatured({ featured_models: [] })
    expect(api.routingFeaturedLooksUnconfigured(r)).toBe(true)
  })

  it('有精选模型时不成立', () => {
    const r = api.unwrapRoutingFeatured({ featured_models: ['gpt-4o'] })
    expect(api.routingFeaturedLooksUnconfigured(r)).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 取数
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('取数', () => {
  beforeEach(() => {
    rq.mockReset()
  })

  it('★ policy 打 superAdmin 档的裸路径', async () => {
    ok({})
    await api.fetchRoutingPolicy()
    expect(rq.mock.calls[0]![0]).toBe('GET')
    expect(rq.mock.calls[0]![1]).toBe('/api/routing/policy')
  })

  it('★ featured 打裸路径', async () => {
    ok({ featured_models: [] })
    await api.fetchRoutingFeatured()
    expect(rq.mock.calls[0]![1]).toBe('/api/routing/featured')
  })

  it('★ scoring-weights 打裸路径', async () => {
    ok(scoringWeights())
    await api.fetchRoutingScoringWeights()
    expect(rq.mock.calls[0]![1]).toBe('/api/routing/scoring-weights')
  })

  it('★ featured-models 打裸路径（admin 档）', async () => {
    ok({ models: [] })
    await api.fetchRoutingFeaturedModels()
    expect(rq.mock.calls[0]![1]).toBe('/api/routing/featured-models')
  })

  it('★ policy 会把空对象解成 null', async () => {
    ok({})
    await expect(api.fetchRoutingPolicy()).resolves.toBeNull()
  })

  it('★ featured 会拒绝非数组', async () => {
    ok({ featured_models: null })
    await expect(api.fetchRoutingFeatured()).rejects.toThrow(/featured_models 不是数组/)
  })

  it('★ scoring-weights 会拒绝缺保证键', async () => {
    const w = scoringWeights()
    delete w.session_load
    ok(w)
    await expect(api.fetchRoutingScoringWeights()).rejects.toThrow(/缺 1 个键（session_load）/)
  })

  it('★ featured-models 会拒绝未知 source', async () => {
    ok({ models: [featuredModel({ source: 'manual' })] })
    await expect(api.fetchRoutingFeaturedModels()).rejects.toThrow(/source 不是已知来源/)
  })

  it('★ policy 会拒绝 featured 形状', async () => {
    ok({ featured_models: ['gpt-4o'] })
    await expect(api.fetchRoutingPolicy()).rejects.toThrow(/拿到的是别的形状/)
  })
})