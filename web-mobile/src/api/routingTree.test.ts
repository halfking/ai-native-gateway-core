import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchModelTree,
  unwrapModelTree,
  modelTreeIsRedacted,
  modelTreeVariants,
  modelTreeVariantAvailable,
  isSimpleVariant,
  modelTreeAllCredentialsAvailable,
  modelTreeMetricsMayBePlaceholder,
  modelTreeAvailabilityFabricated,
  modelTreeFeaturedFilterInert,
  fetchAvailableModelsRaw,
  unwrapAvailableModelsRaw,
  availableModelsHasDuplicates,
  fetchRoutingHealth,
  unwrapRoutingHealth,
  routingHealthSummaryDisagrees,
  routingHealthCoolingDesynced,
  MODEL_TREE_ENVELOPE_KEYS,
  ROUTING_HEALTH_CRED_KEYS,
  type ModelTreeFullResponse,
  type ModelTreeSimpleResponse,
  type ModelTreeCredential,
  type RoutingHealthResponse,
} from './routingTree'

/**
 * 模型路由树 / 可用模型原始名单 / 熔断健康（2026-10-08）。
 *
 * ★ 三条最该被钉住的：
 *   ① `model-tree` **同一端点按角色返回两种形状**，`available` 两侧层级与语义都不同；
 *   ② `available-models/raw` **零行时是 `null` 不是 `[]`**（后端 nil 切片）；
 *   ③ `model-tree` 把 `availability_state` 的 NULL 写成 **"ready"** ——
 *      状态未知被渲染成就绪，比编造成功率更危险。
 */

const fetchMock = vi.fn()
function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
function lastCall(): { url: string; init: RequestInit } {
  const c = fetchMock.mock.calls.at(-1) as unknown as [string, RequestInit]
  return { url: c[0], init: c[1] }
}

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})

/** ★ 逐字照抄 routing.go:2297-2313 的 credentialEntry。 */
function cred(over: Record<string, unknown> = {}) {
  return {
    credential_id: 9,
    credential_label: 'tok-a',
    credential_status: 'active',
    provider_id: 3,
    provider_name: '小米大模型',
    available: true,
    tier: 1,
    weight: 100,
    unit_price_in_per_1m: 1.5,
    unit_price_out_per_1m: 6,
    success_rate: 0.98,
    p95_latency_ms: 820,
    currency: 'USD',
    availability_state: 'ready',
    ...over,
  } as unknown as ModelTreeCredential
}

/** ★ 逐字照抄 routing.go:2297-2330 的完整树。 */
function treeFull(over: Record<string, unknown> = {}) {
  return {
    featured: ['gpt-4o'],
    series: [
      {
        series: 'gpt',
        generations: [
          {
            generation: 'gpt-4',
            variants: [
              {
                variant: 'gpt-4o',
                canonical_name: 'gpt-4o',
                tags: ['fast'],
                credentials: [cred()],
              },
            ],
          },
          {
            // ★★ 第二个 generation：没有它，「只取第一层」这种变异照样全绿
            generation: 'gpt-3.5',
            variants: [
              {
                variant: 'gpt-35-turbo',
                canonical_name: 'gpt-35-turbo',
                tags: ['legacy'],
                credentials: [cred({ credential_id: 11 })],
              },
            ],
          },
        ],
      },
    ],
    unmapped: [],
    ...over,
  } as unknown as ModelTreeFullResponse
}

/** ★ 逐字照抄 routing.go:2462-2471 的裁剪树 + readonly 标记。 */
function treeSimple(over: Record<string, unknown> = {}) {
  return {
    featured: ['gpt-4o'],
    series: [
      {
        series: 'gpt',
        generations: [
          {
            generation: 'gpt-4',
            variants: [{ variant: 'gpt-4o', canonical_name: 'gpt-4o', tags: ['fast'], available: true, credential_count: 2 }],
          },
        ],
      },
    ],
    unmapped: [],
    readonly: true,
    ...over,
  } as unknown as ModelTreeSimpleResponse
}

describe('model-tree：★★ 同一端点两种形状', () => {
  it('★ URL 与 featured_only 参数', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(treeFull()))
    await fetchModelTree()
    expect(lastCall().url).toBe('/api/routing/model-tree')
    fetchMock.mockResolvedValueOnce(jsonResponse(treeFull()))
    await fetchModelTree({ featuredOnly: true })
    expect(lastCall().url).toBe('/api/routing/model-tree?featured_only=true')
  })

  it('★★★ readonly:true 是两侧唯一的区分标记', () => {
    expect(modelTreeIsRedacted(treeSimple())).toBe(true)
    expect(modelTreeIsRedacted(treeFull())).toBe(false)
    // ⚠️ 裁剪形状必须带这个标记，否则客户端与后端都无法判断拿到的是哪种
    const noMark = treeSimple({ readonly: undefined }) as unknown as Record<string, unknown>
    expect(modelTreeIsRedacted(noMark as never)).toBe(false)
    // ★★ 这条是补的：后端只发 true 或「键不存在」，此时 `=== true` 与 `!!` 恒等；
    //   但契约漂移可能给出真值非布尔（如字符串 "true"）⇒ 不能当成裁剪形状。
    //   没有这条，「判据只看真值」这种变异就永远测不出来。
    const truthyString = treeSimple({ readonly: 'true' }) as unknown as Record<string, unknown>
    expect(modelTreeIsRedacted(truthyString as never)).toBe(false)
  })

  it('★★★★★ `available` 两侧层级与语义都不同', () => {
    const fullVariants = modelTreeVariants(treeFull().series) as never as Array<Record<string, unknown>>
    const simpleVariants = modelTreeVariants(treeSimple().series) as never as Array<Record<string, unknown>>
    // 完整形状：available 挂在 credentials[] 上，**不是** variant 级
    expect('available' in fullVariants[0]!).toBe(false)
    // 裁剪形状：available 挂在 variant 级，且是「全部凭据都可用」
    expect(simpleVariants[0]!.available).toBe(true)
    expect(simpleVariants[0]!.credential_count).toBe(2)
  })

  it('★★★★ 完整形状要算「全部可用」必须自己 fold（不能借用裁剪形状的 available）', () => {
    const v = { variant: 'v', canonical_name: 'v', tags: [], credentials: [cred(), cred({ credential_id: 10, available: false })] }
    expect(modelTreeAllCredentialsAvailable(v as never)).toBe(false)
    expect(modelTreeAllCredentialsAvailable({ ...v, credentials: [cred()] } as never)).toBe(true)
    // 空凭据列表不是「全部可用」——那会把「没有任何凭据」读成「全部都可用」
    expect(modelTreeAllCredentialsAvailable({ ...v, credentials: [] } as never)).toBe(false)
  })

  it('★★★★★ 变体展开必须跨全部 generation（只取第一层会丢变体）', () => {
    // ★ 这条是补的：原用例只看 [0]，于是「只取第一个 generation」恒等。
    //   夹具已加第二个 generation（gpt-35-turbo）。
    expect(modelTreeVariants(treeFull().series).length).toBe(2)
    expect(modelTreeVariants(treeSimple().series).length).toBe(1)
    expect(modelTreeVariants(treeFull().series).map((v) => v.variant)).toContain('gpt-35-turbo')
  })

  it('★★★ variantAvailable 对完整形状返回 null（不假装能给出答案）', () => {
    const full = modelTreeVariants(treeFull().series)[0]!
    const simple = modelTreeVariants(treeSimple().series)[0]!
    expect(modelTreeVariantAvailable(full)).toBeNull()
    expect(modelTreeVariantAvailable(simple)).toBe(true)
    expect(isSimpleVariant(simple)).toBe(true)
    expect(isSimpleVariant(full)).toBe(false)
  })
})

describe('model-tree：★★ 编造值与过滤失效', () => {
  it('★★★★★ availability_state 的 NULL 被写成 ready —— 状态未知被渲染成就绪', () => {
    // SQL: COALESCE(c.availability_state, 'ready') 而相邻的 status 兜 'unknown'
    const unknown = cred({ credential_status: 'unknown', availability_state: 'ready' })
    expect(modelTreeAvailabilityFabricated(unknown)).toBe(true)
    // 状态齐全时不命中
    expect(modelTreeAvailabilityFabricated(cred())).toBe(false)
    // 只有 status 缺失才算 —— 单纯 availability_state 为 ready 不能判
    expect(modelTreeAvailabilityFabricated(cred({ credential_status: 'active', availability_state: 'ready' }))).toBe(false)
  })

  it('★★★★ 编造默认值 0.9 / 9999', () => {
    expect(modelTreeMetricsMayBePlaceholder(cred({ success_rate: 0.9, p95_latency_ms: 9999 }))).toBe(true)
    expect(modelTreeMetricsMayBePlaceholder(cred())).toBe(false)
    // 只命中一半不算
    expect(modelTreeMetricsMayBePlaceholder(cred({ success_rate: 0.9, p95_latency_ms: 120 }))).toBe(false)
  })

  it('★★★★ featured 为空 ⇒ 过滤整个不下发，静默返回全量', () => {
    expect(modelTreeFeaturedFilterInert(treeFull({ featured: [] }))).toBe(true)
    expect(modelTreeFeaturedFilterInert(treeFull())).toBe(false)
    expect(modelTreeFeaturedFilterInert(treeSimple({ featured: [] }))).toBe(true)
  })
})

describe('model-tree：信封校验', () => {
  it('★★ 3 个恒存在键缺任一个都抛错；readonly 不在其中', () => {
    expect(MODEL_TREE_ENVELOPE_KEYS).toEqual(['featured', 'series', 'unmapped'])
    for (const k of MODEL_TREE_ENVELOPE_KEYS) {
      const item = { ...treeFull() } as Record<string, unknown>
      delete item[k]
      expect(() => unwrapModelTree(item), `缺 ${k} 应抛错`).toThrow(/缺 1 个/)
    }
  })

  it('★★ 非对象 / null / 数组 抛错', () => {
    expect(() => unwrapModelTree(null)).toThrow(/实得 null/)
    expect(() => unwrapModelTree([])).toThrow(/实得 array/)
    expect(() => unwrapModelTree({ ...treeFull(), series: {} })).toThrow(/必须是数组/)
  })

  it('★★ 走 fetch 的端到端路径也过解包', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(treeSimple()))
    const r = await fetchModelTree()
    expect(modelTreeIsRedacted(r)).toBe(true)
  })
})

describe('available-models/raw：★★★ 零行是 null 不是 []', () => {
  it('★★★★★ null / undefined 都放行成 []（后端 nil 切片序列化成 null）', async () => {
    expect(unwrapAvailableModelsRaw(null)).toEqual([])
    expect(unwrapAvailableModelsRaw(undefined)).toEqual([])
    // ★ 而 audit 那条走 make(...) 返 [] —— 同一族两种表示
    fetchMock.mockResolvedValueOnce(new Response('null', { status: 200, headers: { 'Content-Type': 'application/json' } }))
    expect(await fetchAvailableModelsRaw()).toEqual([])
  })

  it('★★★ 非数组且非 null 抛错（不静默返回空）', () => {
    expect(() => unwrapAvailableModelsRaw({ models: [] })).toThrow(/形状不符/)
    expect(() => unwrapAvailableModelsRaw('x')).toThrow(/形状不符/)
  })

  it('★★★ 名单里混入非字符串元素必须被滤掉（契约漂移不能被伪装成正常名单）', () => {
    // ★ 这条是补的：夹具全是字符串时，「原样返回」与「过滤非字符串」恒等，
    //   于是那条变异照样全绿 —— 测的是巧合。
    expect(unwrapAvailableModelsRaw(['a', 1, 'b', null, {}])).toEqual(['a', 'b'])
  })

  it('★★ 正常名单原样返回，且能检出重复', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(['a', 'b']))
    const names = await fetchAvailableModelsRaw()
    expect(names).toEqual(['a', 'b'])
    expect(availableModelsHasDuplicates(names)).toBe(false)
    expect(availableModelsHasDuplicates(['a', 'a'])).toBe(true)
  })
})

describe('routing/health：信封与可复算的 summary', () => {
  function health(over: Record<string, unknown> = {}) {
    return {
      credentials: [
        {
          credential_id: 9, label: 'tok-a', status: 'active', circuit_state: 'open',
          consecutive_failures: 3, circuit_open_count_window: 2, cooling_until: '2026-10-08T00:05:00Z',
          provider_name: '小米大模型', catalog_code: 'xiaoai',
        },
        {
          credential_id: 10, label: 'tok-b', status: 'active', circuit_state: 'closed',
          consecutive_failures: 0, circuit_open_count_window: 0, cooling_until: null,
          provider_name: '小米大模型', catalog_code: null,
        },
      ],
      summary: { total: 2, open: 1, closed: 1 },
      ...over,
    } as unknown as RoutingHealthResponse
  }

  it('★ URL 固定无 query', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(health()))
    await fetchRoutingHealth()
    expect(lastCall().url).toBe('/api/routing/health')
  })

  it('★★ 信封与 summary 类型不对都抛错', () => {
    expect(() => unwrapRoutingHealth(null)).toThrow(/实得 null/)
    expect(() => unwrapRoutingHealth({ ...health(), summary: { total: '2', open: 1, closed: 1 } })).toThrow(
      /类型不对/,
    )
    expect(() => unwrapRoutingHealth({ credentials: {} })).toThrow(/缺 credentials\/summary/)
  })

  it('★★ 凭据行 9 个恒存在键缺任一个都抛错', () => {
    expect(ROUTING_HEALTH_CRED_KEYS.length).toBe(9)
    for (const k of ROUTING_HEALTH_CRED_KEYS) {
      const item = health() as unknown as { credentials: Record<string, unknown>[] }
      const c = { ...item.credentials[0] } as Record<string, unknown>
      delete c[k]
      expect(() => unwrapRoutingHealth({ ...item, credentials: [c] }), `缺 ${k} 应抛错`).toThrow(/缺 1 个/)
    }
  })

  it('★★★★★ summary 三个数是**可复算**的，对不上即契约漂移', () => {
    expect(routingHealthSummaryDisagrees(health())).toBe(false)
    expect(routingHealthSummaryDisagrees(health({ summary: { total: 5, open: 1, closed: 1 } }))).toBe(true)
    expect(routingHealthSummaryDisagrees(health({ summary: { total: 2, open: 0, closed: 2 } }))).toBe(true)
    expect(routingHealthSummaryDisagrees(health({ summary: { total: 2, open: 1, closed: 2 } }))).toBe(true)
  })

  it('★★★ cooling_until 非空但状态不是 open ⇒ 冷却窗口与状态不同步', () => {
    const h = health()
    expect(routingHealthCoolingDesynced(h.credentials[0]!)).toBe(false)
    expect(routingHealthCoolingDesynced(h.credentials[1]!)).toBe(false)
    expect(
      routingHealthCoolingDesynced({ ...h.credentials[0]!, circuit_state: 'closed' }),
    ).toBe(true)
  })
})