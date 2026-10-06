import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchRoutingOverview,
  unwrapRoutingOverview,
  overviewMetricsMayBePlaceholder,
  overviewIsRoutable,
  overviewBlockReason,
  overviewRoutableKeysDisagree,
  overviewFeaturedFilterInert,
  fetchRoutingDecisions,
  unwrapRoutingDecisions,
  routingDecisionsHasMore,
  routingDecisionsTotalUnreliable,
  fetchRoutingAuditLog,
  unwrapRoutingAudit,
  routingAuditEmptyIsUnreliable,
  routingAuditActorUnknown,
  ROUTING_OVERVIEW_REQUIRED_KEYS,
  ROUTING_OVERVIEW_ROW_REQUIRED_KEYS,
  ROUTING_DECISIONS_REQUIRED_KEYS,
  ROUTING_DECISIONS_MAX_LIMIT,
  ROUTING_DECISIONS_DEFAULT_LIMIT,
  ROUTING_DECISIONS_DEFAULT_SINCE_MINUTES,
  ROUTING_AUDIT_REQUIRED_KEYS,
  ROUTING_AUDIT_DEFAULT_LIMIT,
  ROUTING_AUDIT_MAX_LIMIT,
  type RoutingOverviewRow,
  type RoutingAuditRow,
} from './routingRead'

/**
 * 路由决策面三条只读端点（2026-10-08）。
 *
 * ★ 这组用例最该守的是「**编造的默认值**」与「**空结果有歧义**」：
 *   · overview 的 success_rate=0.9 / p95=9999 是 SQL COALESCE 兜的，
 *     后端不给任何标记 ⇒ 客户端无法与真值区分，只能加免责。
 *   · audit 查询失败回的是 **200 + []**，与「真的没记录」逐字节相同。
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

/** ★ 逐字照抄 routing.go:2176-2200 的 row map 字面量。 */
function overviewRow(over: Record<string, unknown> = {}) {
  return {
    model_name: 'gpt-4o',
    provider_id: 3,
    provider_name: '小米大模型',
    catalog_code: 'xiaoai',
    protocol: 'openai',
    base_url: 'https://api.mi.com/v1',
    provider_enabled: true,
    credential_id: 9,
    credential_label: 'tok-a',
    credential_status: 'active',
    lifecycle_status: 'active',
    availability_state: 'ready',
    availability_recover_at: null,
    quota_state: 'ok',
    quota_recover_at: null,
    balance_usd: 12.5,
    effective_at: null,
    expires_at: null,
    circuit_state: 'closed',
    cooling_until: null,
    available: true,
    tier: 1,
    weight: 100,
    unit_price_in_per_1m: 1.5,
    unit_price_out_per_1m: 6,
    currency: 'USD',
    success_rate: 0.98,
    p95_latency_ms: 820,
    standardized_name: 'gpt-4o',
    runtime_routable: true,
    routable: true,
    ...over,
  } as unknown as RoutingOverviewRow
}

function overviewOk(over: Record<string, unknown> = {}) {
  return { featured: ['gpt-4o'], rows: [overviewRow()], ...over }
}

function auditRow(over: Record<string, unknown> = {}) {
  return {
    id: 1,
    ts: '2026-10-08T00:00:00Z',
    actor: 'ops',
    action: 'routing_policy_update',
    target_type: 'policy',
    target_id: 1,
    before_json: { tier: 1 },
    after_json: { tier: 2 },
    ...over,
  } as unknown as RoutingAuditRow
}

describe('routing/overview：URL 与解包', () => {
  it('★ 无参数时 URL 不带 query；featured_only 才带', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(overviewOk()))
    await fetchRoutingOverview()
    expect(lastCall().url).toBe('/api/routing/overview')
    fetchMock.mockResolvedValueOnce(jsonResponse(overviewOk()))
    await fetchRoutingOverview({ featuredOnly: true })
    expect(lastCall().url).toBe('/api/routing/overview?featured_only=true')
  })

  it('★★ 信封 2 键缺任一个都抛错', () => {
    expect(ROUTING_OVERVIEW_REQUIRED_KEYS).toEqual(['featured', 'rows'])
    for (const k of ROUTING_OVERVIEW_REQUIRED_KEYS) {
      const item = { ...overviewOk() } as Record<string, unknown>
      delete item[k]
      expect(() => unwrapRoutingOverview(item), `缺 ${k} 应抛错`).toThrow(/缺 1 个/)
    }
  })

  it('★★★ 行内 31 个恒存在键缺任一个都抛错（runtime_block_reason 条件存在，不在内）', () => {
    expect(ROUTING_OVERVIEW_ROW_REQUIRED_KEYS.length).toBe(31)
    expect(ROUTING_OVERVIEW_ROW_REQUIRED_KEYS).not.toContain('runtime_block_reason')
    for (const k of ROUTING_OVERVIEW_ROW_REQUIRED_KEYS) {
      const item = { ...overviewOk(), rows: [{ ...overviewRow() } as Record<string, unknown>] } as unknown
      delete (item as { rows: Record<string, unknown>[] }).rows[0]![k]
      expect(() => unwrapRoutingOverview(item), `rows 缺 ${k} 应抛错`).toThrow(/缺 1 个/)
    }
  })

  it('★★ 非对象 / 数组 / null 抛错；featured 非数组也抛错', () => {
    expect(() => unwrapRoutingOverview(null)).toThrow(/实得 null/)
    expect(() => unwrapRoutingOverview([])).toThrow(/实得 array/)
    expect(() => unwrapRoutingOverview('x')).toThrow(/形状不符/)
    expect(() => unwrapRoutingOverview({ ...overviewOk(), featured: {} })).toThrow(/必须是数组/)
    expect(() => unwrapRoutingOverview({ ...overviewOk(), rows: null })).toThrow(/必须是数组/)
  })
})

describe('routing/overview：★★★ 编造的默认值', () => {
  it('★★★★★ success_rate=0.9 且 p95=9999 ⇒ 可能是 COALESCE 兜的，不是实测', () => {
    const neverProbed = overviewRow({ success_rate: 0.9, p95_latency_ms: 9999 })
    expect(overviewMetricsMayBePlaceholder(neverProbed)).toBe(true)
    // 有实测值时不命中
    expect(overviewMetricsMayBePlaceholder(overviewRow())).toBe(false)
    // ⚠️ 只命中一半**不算**：真值完全可能恰好等于 0.9 或 9999
    // ★ 这两条是补的：此前样本只覆盖「两个都不命中」与「两个都命中」，
    //   于是「判据只看其中一个」这种变异照样全绿。
    expect(overviewMetricsMayBePlaceholder(overviewRow({ success_rate: 0.9, p95_latency_ms: 120 }))).toBe(false)
    expect(overviewMetricsMayBePlaceholder(overviewRow({ success_rate: 0.99, p95_latency_ms: 9999 }))).toBe(false)
  })

  it('★★ ★ 但真值恰好是 0.9/9999 时**无法区分** —— 这正是要加免责的理由', () => {
    // 后端没给任何「这是默认值」的标记 ⇒ 客户端只能靠形状猜
    const ambiguous = overviewRow({ success_rate: 0.9, p95_latency_ms: 9999 })
    expect(overviewMetricsMayBePlaceholder(ambiguous)).toBe(true)
    // 文档层面承认这一点：命中不等于「一定是兜的」
  })
})

describe('routing/overview：可路由性与阻塞原因', () => {
  it('★★ 可路由的行**没有** runtime_block_reason 键', () => {
    const ok = overviewRow()
    expect('runtime_block_reason' in ok).toBe(false)
    expect(overviewIsRoutable(ok)).toBe(true)
    expect(overviewBlockReason(ok)).toBeNull()
  })

  it('★★★ 被阻塞的行才有原因，且原因不能当可路由的判据', () => {
    const blocked = overviewRow({
      available: false,
      runtime_routable: false,
      routable: false,
      runtime_block_reason: 'offer_unavailable',
    })
    expect(overviewIsRoutable(blocked)).toBe(false)
    expect(overviewBlockReason(blocked)).toBe('offer_unavailable')
    // ★ 判据看 runtime_routable，不看原因键在不在
    const blockedNoReason = overviewRow({ runtime_routable: false, routable: false })
    expect(overviewIsRoutable(blockedNoReason)).toBe(false)
    expect(overviewBlockReason(blockedNoReason)).toBeNull()
  })

  it('★★★ routable 与 runtime_routable 是同一个变量 ⇒ 不等即契约漂移', () => {
    expect(overviewRoutableKeysDisagree(overviewRow())).toBe(false)
    expect(overviewRoutableKeysDisagree(overviewRow({ routable: false }))).toBe(true)
  })

  it('★★★★★ 两键漂移时，分栏与原因判据必须以 runtime_routable 为准（补）', () => {
    // ★ 这条是补的：此前所有样本都让两键同值，于是「判据改看 routable 键」
    //   这种变异照样全绿 —— 测的是巧合。
    // 形态：后端某天只改了其中一个键，runtime_routable=false、routable=true。
    const drifted = overviewRow({
      runtime_routable: false,
      routable: true,
      runtime_block_reason: 'circuit_open',
    })
    // 判据看 runtime_routable ⇒ 这一行算**被阻塞**
    expect(overviewIsRoutable(drifted)).toBe(false)
    expect(overviewBlockReason(drifted)).toBe('circuit_open')
    // 反向漂移同理
    const drifted2 = overviewRow({ runtime_routable: true, routable: false })
    expect(overviewIsRoutable(drifted2)).toBe(true)
  })

  it('★★★★★ 可路由的行即使**带了**原因键，也不能返回原因（补）', () => {
    // ★ 这条是补的：后端 :2202 只在 !runtimeRoutable 时写这个键，
    //   但契约漂移/历史数据都可能让可路由行也带着它 ⇒ 判据必须以 runtime_routable 为准。
    const routableWithReason = overviewRow({ runtime_routable: true, routable: true, runtime_block_reason: 'circuit_open' })
    expect(overviewIsRoutable(routableWithReason)).toBe(true)
    expect(overviewBlockReason(routableWithReason)).toBeNull()
  })

  it('★★★★ featured 为空 ⇒ 「只看精选」的过滤整个不下发，返回全量且无提示', () => {
    expect(overviewFeaturedFilterInert(overviewOk({ featured: [] }))).toBe(true)
    expect(overviewFeaturedFilterInert(overviewOk())).toBe(false)
  })
})

describe('routing/decisions：URL 与解包', () => {
  it('★ 六个可选参数各自拼进 query，且都不给时不带 query', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ total: 0, offset: 0, limit: 100, decisions: [] }))
    await fetchRoutingDecisions()
    expect(lastCall().url).toBe('/api/routing/decisions')
    fetchMock.mockResolvedValueOnce(jsonResponse({ total: 0, offset: 0, limit: 100, decisions: [] }))
    await fetchRoutingDecisions({ sinceMinutes: 60, limit: 200, offset: 20, model: 'gpt-4o', canonical: 'gpt-4o', success: true })
    const q = new URLSearchParams(lastCall().url.split('?')[1])
    expect(q.get('since_minutes')).toBe('60')
    expect(q.get('limit')).toBe('200')
    expect(q.get('offset')).toBe('20')
    expect(q.get('model')).toBe('gpt-4o')
    expect(q.get('canonical')).toBe('gpt-4o')
    expect(q.get('success')).toBe('true')
  })

  it('★★ 信封 4 键缺任一个都抛错；类型不对也抛错', () => {
    expect(ROUTING_DECISIONS_REQUIRED_KEYS).toEqual(['total', 'offset', 'limit', 'decisions'])
    for (const k of ROUTING_DECISIONS_REQUIRED_KEYS) {
      const item = { total: 0, offset: 0, limit: 100, decisions: [] } as Record<string, unknown>
      delete item[k]
      expect(() => unwrapRoutingDecisions(item), `缺 ${k} 应抛错`).toThrow(/缺 1 个/)
    }
    expect(() => unwrapRoutingDecisions({ total: '0', offset: 0, limit: 100, decisions: [] })).toThrow(
      /类型不对/,
    )
    expect(() => unwrapRoutingDecisions({ total: 0, offset: 0, limit: 100, decisions: {} })).toThrow(
      /类型不对/,
    )
  })

  it('★ 信封里的 decisions **不再逐行校验** —— 与 overview 不同，别默认它更严', () => {
    const loose = { total: 1, offset: 0, limit: 100, decisions: [{ nope: 1 }] }
    expect(() => unwrapRoutingDecisions(loose)).not.toThrow()
  })

  it('★ 后端默认值：limit 100、since_minutes 30、limit>500 是 clamp', () => {
    expect(ROUTING_DECISIONS_DEFAULT_LIMIT).toBe(100)
    expect(ROUTING_DECISIONS_DEFAULT_SINCE_MINUTES).toBe(30)
    // ★ clamp 不是回落：routing.go:3222-3224 是 `if limit > 500 { limit = 500 }`
    expect(ROUTING_DECISIONS_MAX_LIMIT).toBe(500)
  })
})

describe('routing/decisions：分页与 total 的可信度', () => {
  it('★★ offset + 返回条数 < total ⇒ 还有下一页', () => {
    const resp = { total: 250, offset: 0, limit: 100, decisions: new Array(100).fill({}) } as never
    expect(routingDecisionsHasMore(resp)).toBe(true)
  })
  it('★★ 取满最后一页 ⇒ 没有下一页', () => {
    const resp = { total: 250, offset: 200, limit: 100, decisions: new Array(50).fill({}) } as never
    expect(routingDecisionsHasMore(resp)).toBe(false)
  })
  it('★★★ total=0 却有数据 ⇒ 计数查询的 error 被吞成 0，total 不可信', () => {
    const resp = { total: 0, offset: 0, limit: 100, decisions: [{}] } as never
    expect(routingDecisionsTotalUnreliable(resp)).toBe(true)
    expect(routingDecisionsTotalUnreliable({ total: 5, offset: 0, limit: 100, decisions: [{}] } as never)).toBe(false)
  })
})

describe('routing/audit：★ 裸数组（与同族 decisions 相反）', () => {
  it('★★ 形状必须是裸数组；信封会被拒', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([auditRow()]))
    const rows = await fetchRoutingAuditLog()
    expect(Array.isArray(rows)).toBe(true)
    expect(rows[0]?.action).toBe('routing_policy_update')
    // ★ 若后端哪天改成信封，这里会抛错而不是静默给出空列表
    expect(() => unwrapRoutingAudit({ audits: [] })).toThrow(/期望裸数组/)
  })

  it('★★ 行内 8 个恒存在键缺任一个都抛错', () => {
    expect(ROUTING_AUDIT_REQUIRED_KEYS.length).toBe(8)
    for (const k of ROUTING_AUDIT_REQUIRED_KEYS) {
      const item = [auditRow()] as unknown as Record<string, unknown>[]
      delete item[0]![k]
      expect(() => unwrapRoutingAudit(item), `缺 ${k} 应抛错`).toThrow(/缺 1 个/)
    }
    expect(() => unwrapRoutingAudit([null])).toThrow(/不是对象/)
  })

  it('★ target_type / target_id 可为 null（后端是指针）', () => {
    const rows = unwrapRoutingAudit([auditRow({ target_type: null, target_id: null })])
    expect(rows[0]?.target_type).toBeNull()
    expect(rows[0]?.target_id).toBeNull()
  })

  it('★ 后端默认值：limit 50、上限 500', () => {
    expect(ROUTING_AUDIT_DEFAULT_LIMIT).toBe(50)
    expect(ROUTING_AUDIT_MAX_LIMIT).toBe(500)
  })
})

describe('routing/audit：★★★★ 空列表**不可信**', () => {
  it('★★★★★ 查询失败回的是 200 + []，与「真的没记录」无法区分', () => {
    // routing.go:3460-3463 —— err != nil 时 writeJSON(200, []any{})
    expect(routingAuditEmptyIsUnreliable([], 200)).toBe(true)
    // 有记录时不算
    expect(routingAuditEmptyIsUnreliable([auditRow()], 200)).toBe(false)
    // 真正的错误状态码下，空数组是服务端明确说的「没有」
    expect(routingAuditEmptyIsUnreliable([], 500)).toBe(false)
  })

  it('★★ actor 空串 ⇔ SQL 里是 NULL（被 COALESCE 过），不是「真叫空」', () => {
    expect(routingAuditActorUnknown(auditRow({ actor: '' }))).toBe(true)
    expect(routingAuditActorUnknown(auditRow())).toBe(false)
  })

  it('★★★ before_json / after_json 的 null 有歧义：NULL 列与损坏的 jsonb 都是 null', () => {
    // jsoncol.Decode 的返回值在 routing.go:3480-3481 被忽略 ⇒ 损坏数据落成 null
    const rows = unwrapRoutingAudit([auditRow({ before_json: null, after_json: null })])
    expect(rows[0]?.before_json).toBeNull()
    // ⇒ 客户端没有任何办法区分这两种情况，文档层必须承认
  })
})
