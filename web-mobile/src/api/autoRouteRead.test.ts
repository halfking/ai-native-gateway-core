import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchAutoRouteIndex,
  unwrapAutoRouteIndex,
  autoRouteIndexAwaitingFirstRefresh,
  autoRouteIndexTopAccepted,
  AUTO_ROUTE_INDEX_REQUIRED_KEYS,
  AUTO_ROUTE_INDEX_WARNING_KEY,
  fetchAutoRouteAudit,
  unwrapAutoRouteAudit,
  autoRouteAuditMissingBlocks,
  autoRouteAuditTotalsDisagree,
  autoRouteAuditHasNoTraffic,
  autoRouteAuditNumbersStale,
  AUTO_ROUTE_AUDIT_REQUIRED_KEYS,
  AUTO_ROUTE_AUDIT_OPTIONAL_KEYS,
  AUTO_ROUTE_OUTCOME_KEYS,
  fetchAutoRouteCustomerCost,
  unwrapAutoRouteCustomerCost,
  autoRouteCustomerCostContradicts,
  AUTO_ROUTE_CUSTOMER_COST_REQUIRED_KEYS,
  fetchAutoRouteModelCost,
  unwrapAutoRouteModelCost,
  autoRouteModelCostPerRequest,
  autoRouteCostTopAccepted,
  AUTO_ROUTE_MODEL_COST_REQUIRED_KEYS,
  fetchAutoRouteAccuracy,
  unwrapAutoRouteAccuracy,
  autoRouteAccuracyDaysAccepted,
  autoRouteAccuracyBucketGranularity,
  autoRouteAccuracyAveragesMayBeZeroPlaceholders,
  AUTO_ROUTE_ACCURACY_REQUIRED_KEYS,
  AUTO_ROUTE_ACCURACY_ROW_KEYS,
  fetchAutoRouteDecision,
  unwrapAutoRouteDecision,
  autoRouteDecisionHasL2,
  autoRouteDecisionCanHaveL2,
  autoRouteDecisionModelsBothEmpty,
  autoRouteDecisionL1SplatKeys,
  AUTO_ROUTE_L1_DB_KEYS,
  AUTO_ROUTE_DECISION_REQUIRED_KEYS,
  AUTO_ROUTE_L1_REQUIRED_KEYS,
  AUTO_ROUTE_L2_REQUIRED_KEYS,
  type AutoRouteIndexRow,
  type AutoRouteAuditResponse,
  type AutoRouteDecisionResponse,
} from './autoRouteRead'

/**
 * 自动路由读面（2026-10-08，第五十五批）。
 *
 * ★ 本批最该被钉住的六条：
 *   ① **权限档位是 superAdmin 不是 admin** —— 形参名 `adminWrap` 是假名，
 *      实际绑定 `h.superAdmin`（handler.go:1381/:1430）。tenant_admin 直接 403。
 *      （这一条没有运行时断言能测，它是对「读代码」的要求，写在这里当锚点。）
 *   ② **稀疏键 map**：`index` 每行只有 4 个键无条件，两条 cost 每行**只有 1 个**。
 *      键缺失 = 无数据，不是 0。
 *   ③ **异构哨兵**：`index` 全空时返回 `[{warning}]`，不是 `[]`。
 *   ④ **audit 的三个条件键**：查询失败时键直接缺失，HTTP 仍 200 ——
 *      「没有数据」与「查询挂了」在响应里长得一样。
 *   ⑤ **l1 被 auto_decision blob 逐键覆盖**（splat 发生在赋值之后）。
 *   ⑥ **accuracy 的五个 avg_* 全部 COALESCE 到 0** —— 编造的 0。
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

/* ── 夹具：逐字照抄 auto_route.go:352-399 的 entry map（全部 17 个键都非 NULL） ── */
function indexRowFull() {
  return {
    bucket: '2026-10-08T03:00:00Z',
    credential_id: 42,
    raw_model: 'gpt-4o-2024-11-20',
    canonical_id: 7,
    canonical_name: 'GPT-4o',
    billing_mode: 'per_token',
    unit_price_in_per_1m: 2.5,
    unit_price_out_per_1m: 10,
    context_window: 128000,
    success_rate: 0.97,
    p95_latency_ms: 2400,
    active_sessions: 12,
    concurrency_limit: 64,
    pressure_ratio: 0.19,
    score_smart: 0.93,
    score_speed_first: 0.71,
    score_cost_first: 0.55,
    updated_at: '2026-10-08T03:04:12Z',
  }
}

/** ★ 逐字照抄 :352-356 + :399 —— 只有 4 个无条件键（DB 全 NULL 的那一行）。 */
function indexRowSparse() {
  return {
    bucket: '2026-10-08T03:00:00Z',
    credential_id: 43,
    raw_model: 'unknown-model',
    updated_at: '2026-10-08T03:04:12Z',
  }
}

/** ★ 逐字照抄 auto_route.go:296 的空索引哨兵。 */
function indexSentinel() {
  return {
    warning:
      'credential_model_index is empty; awaiting first bg worker refresh (within 5 minutes of gateway start)',
  }
}

/** ★ 逐字照抄 auto_route_outcome_freshness.go:70-76（as_of / age_seconds 走 omitempty）。 */
function outcomeSourceFresh() {
  return {
    available: true,
    as_of: '2026-10-08T02:55:00Z',
    age_seconds: 3600,
    stale: false,
    stale_after_seconds: 14400,
    reason: 'live',
  }
}

/** ★ 逐字照抄 auto_route.go:571-578 + :761 的 5 个无条件键 + 3 个条件键。 */
function auditFull() {
  return {
    total_requests: 1200,
    total_auto_requests: 900,
    specified_model_requests: 300,
    success_rate: 0.9816666666666667,
    task_distribution: { code: 520, __specified__: 300 },
    profile_distribution: { smart: 400, cost_first: 500 },
    top_chosen_models: [
      { model: 'gpt-4o', count: 610 },
      { model: 'claude-sonnet', count: 290 },
    ],
    outcome_source: outcomeSourceFresh(),
  }
}

/** ★ 查询失败分支：三个条件键**全部缺失**，HTTP 仍 200（:627/:665/:724/:758）。 */
function auditAllBlocksMissing() {
  return {
    total_requests: 0,
    total_auto_requests: 0,
    specified_model_requests: 0,
    success_rate: 0,
    outcome_source: {
      available: false,
      stale: true,
      stale_after_seconds: 14400,
      reason: 'query_failed',
    },
  }
}

/** ★ 逐字照抄 auto_route.go:914-947 的 customer cost 行（全键）。 */
function customerCostRowFull() {
  return {
    api_key_id: 501,
    key_alias: 'prod-billing',
    tenant_id: 'acme',
    application_id: 9,
    cost_usd_1h: 1.25,
    cost_usd_24h: 30.5,
    cost_usd_7d: 210.75,
    total_auto_requests: 4000,
    total_auto_success: 3950,
    active_concurrent: 17,
    avg_pressure_1h: 0.27,
    best_score_smart: 0.95,
    best_score_speed_first: 0.8,
    best_score_cost_first: 0.62,
    last_request_at: '2026-10-08T02:59:00Z',
  }
}

/** ★ 只有唯一无条件键 api_key_id（:914-916）。 */
function customerCostRowSparse() {
  return { api_key_id: 502 }
}

/** ★ 逐字照抄 auto_route.go:1006-1026 的 model cost 行（全键）。 */
function modelCostRowFull() {
  return {
    raw_model: 'gpt-4o-2024-11-20',
    canonical_id: 7,
    total_cost_usd: 88.25,
    total_tokens: 12345678,
    avg_cost_per_1m_usd: 7.15,
    success_rate: 0.99,
    avg_latency_ms: 1800,
    total_requests: 9000,
    unique_api_keys: 12,
  }
}

/** ★ 只有唯一无条件键 raw_model（:1006-1008）。 */
function modelCostRowSparse() {
  return { raw_model: 'mystery-model' }
}

/** ★ 逐字照抄 auto_route_tuning.go:791-800 的 accuracyRow（8 个键全无条件）。 */
function accuracyRow() {
  return {
    task_type: 'code',
    classifier: 'fuzzy_v2',
    total: 812,
    avg_quality: 0.88,
    avg_success: 0.96,
    avg_latency: 2300,
    avg_cost: 0.021,
    drift_rate: 0.04,
  }
}

/** ★ 逐字照抄 auto_route.go:817-841 的决策回放顶层（无 l2）。 */
function decisionNoL2() {
  return {
    request_id: 'a1b2c3d4e5f60718293a4b5c6d7e8f90',
    ts: '2026-10-08T02:31:00Z',
    success: true,
    client_model: 'gpt-4o',
    outbound_model: 'gpt-4o-2024-11-20',
    api_key_id: 501,
    credential_id: 42,
    latency_ms: 1870,
    l1: { task_type: 'code', profile: 'smart', confidence: 0.91 },
  }
}

/** ★ 逐字照抄 analytics.go:889-916 的 l2（decision_trace 非空才出现）。 */
function decisionWithL2() {
  return {
    ...decisionNoL2(),
    l2: {
      ts: '2026-10-08T02:31:00Z',
      success: true,
      chosen_credential_id: 42,
      chosen_provider_id: 3,
      tier: 1,
      candidates_tried: 4,
      resolution_path: 'direct',
      canonical_model: 'gpt-4o',
      decision_trace: { planned_candidates: 4, blocked_candidates: 1 },
    },
  }
}

/* ═══════════════════════════════════════════════════════════════════════════
 * A. index
 * ═════════════════════════════════════════════════════════════════════════ */

describe('autoRoute / index', () => {
  it('全键行与稀疏行都接受 —— 后端逐键条件写入，不是固定 schema', () => {
    const rows = unwrapAutoRouteIndex([indexRowFull(), indexRowSparse()])
    expect(rows).toHaveLength(2)
    expect(rows[0]!.success_rate).toBe(0.97)
    expect(rows[1]).toEqual(indexRowSparse())
    expect('success_rate' in rows[1]!).toBe(false)
  })

  it('空索引哨兵 [{warning}] 必须放行，且能被识别出来', () => {
    const rows = unwrapAutoRouteIndex([indexSentinel()])
    expect(rows).toHaveLength(1)
    expect(autoRouteIndexAwaitingFirstRefresh(rows)).toBe(true)
    // ★ 真数组（零行）不是哨兵
    expect(autoRouteIndexAwaitingFirstRefresh([])).toBe(false)
    expect(autoRouteIndexAwaitingFirstRefresh([indexRowFull()])).toBe(false)
  })

  it('★ 哨兵与真实行混在同一个数组里也能分开识别', () => {
    const rows = unwrapAutoRouteIndex([indexSentinel(), indexRowFull()])
    // 只有全部元素都是哨兵才算「等待首刷」
    expect(autoRouteIndexAwaitingFirstRefresh(rows)).toBe(false)
  })

  it('缺任一必填键即抛错（逐个键各钉一条）', () => {
    for (const k of AUTO_ROUTE_INDEX_REQUIRED_KEYS) {
      const row: Record<string, unknown> = { ...indexRowFull() }
      delete row[k]
      expect(() => unwrapAutoRouteIndex([row])).toThrow(new RegExp(`缺 1 个键（${k}）`))
    }
  })

  it('元素不是对象时抛错，且错误信息带下标', () => {
    expect(() => unwrapAutoRouteIndex([null])).toThrow('rows[0] 不是对象')
    expect(() => unwrapAutoRouteIndex([indexRowFull(), 'x'])).toThrow('rows[1] 不是对象')
  })

  it('顶层不是数组即抛错', () => {
    expect(() => unwrapAutoRouteIndex(null)).toThrow('期望数组，实得 null')
    expect(() => unwrapAutoRouteIndex({})).toThrow('期望数组，实得 object')
  })

  it('★ top：越界静默回落 100（不是 clamp、不是报错）', () => {
    expect(autoRouteIndexTopAccepted(undefined)).toBe(true)
    expect(autoRouteIndexTopAccepted(1)).toBe(true)
    expect(autoRouteIndexTopAccepted(1000)).toBe(true)
    expect(autoRouteIndexTopAccepted(0)).toBe(false)
    expect(autoRouteIndexTopAccepted(-1)).toBe(false)
    expect(autoRouteIndexTopAccepted(1001)).toBe(false)
    // ★ 非整数样本：后端是 `strconv.Atoi`，Atoi("0.5") 直接报错 ⇒ 回落 100。
    //   没有这条，去掉 Number.isInteger 的变异就测不出来。
    expect(autoRouteIndexTopAccepted(0.5)).toBe(false)
    expect(autoRouteIndexTopAccepted(10.5)).toBe(false)
    expect(autoRouteIndexTopAccepted(NaN)).toBe(false)
    // ★ cost 的 top 同族判据也必须带非整数样本，否则同一个变异会从另一个口子漏过去
    expect(autoRouteCostTopAccepted(0.5)).toBe(false)
    expect(autoRouteCostTopAccepted(NaN)).toBe(false)
    // ★ 与两条 cost 的上限 500 不同，不能跨端点类推
    expect(autoRouteCostTopAccepted(1000)).toBe(false)
  })

  it('fetch 打 GET 且带 canonical_id / top 查询参数', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([indexRowFull()]))
    await fetchAutoRouteIndex({ canonicalId: 'gpt-4o', top: 20 })
    const { url, init } = lastCall()
    expect(init.method).toBe('GET')
    expect(url).toContain('/api/admin/auto-route/index?')
    expect(url).toContain('canonical_id=gpt-4o')
    expect(url).toContain('top=20')
  })

  it('fetch 无参数时不带问号', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await fetchAutoRouteIndex()
    expect(lastCall().url).toContain('/api/admin/auto-route/index')
    expect(lastCall().url).not.toContain('?')
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * B. audit
 * ═════════════════════════════════════════════════════════════════════════ */

describe('autoRoute / audit', () => {
  it('完整响应通过，三个条件键都在', () => {
    const a = unwrapAutoRouteAudit(auditFull())
    expect(a.total_requests).toBe(1200)
    expect(a.outcome_source.reason).toBe('live')
    expect(autoRouteAuditMissingBlocks(a)).toEqual([])
  })

  it('★ 三个条件键全缺失也是合法形态（查询失败，HTTP 仍 200）', () => {
    const a = unwrapAutoRouteAudit(auditAllBlocksMissing())
    expect(autoRouteAuditMissingBlocks(a)).toEqual([
      'task_distribution', 'profile_distribution', 'top_chosen_models',
    ])
  })

  it('★ 只缺中间一个块时，只报那一个', () => {
    const partial = { ...auditFull() } as Record<string, unknown>
    delete partial.profile_distribution
    const a = unwrapAutoRouteAudit(partial)
    expect(autoRouteAuditMissingBlocks(a)).toEqual(['profile_distribution'])
  })

  it('缺任一必填键即抛错（逐个键各钉一条）', () => {
    for (const k of AUTO_ROUTE_AUDIT_REQUIRED_KEYS) {
      const a: Record<string, unknown> = { ...auditFull() }
      delete a[k]
      expect(() => unwrapAutoRouteAudit(a)).toThrow(new RegExp(`缺 1 个键（${k}）`))
    }
  })

  it('outcome_source 缺必填键即抛错（as_of / age_seconds 是 omitempty，不在其列）', () => {
    for (const k of AUTO_ROUTE_OUTCOME_KEYS) {
      const a: Record<string, unknown> = { ...auditFull() }
      a.outcome_source = { ...outcomeSourceFresh() }
      delete (a.outcome_source as Record<string, unknown>)[k]
      expect(() => unwrapAutoRouteAudit(a)).toThrow(
        new RegExp(`outcome_source 缺 1 个键（${k}）`),
      )
    }
  })

  it('outcome_source 缺 as_of / age_seconds（available=false 时）合法', () => {
    const a = unwrapAutoRouteAudit(auditAllBlocksMissing())
    expect('as_of' in a.outcome_source).toBe(false)
    expect('age_seconds' in a.outcome_source).toBe(false)
  })

  it('★ total = auto + specified 是真不变量（基表与 MV 两侧都成立）', () => {
    expect(autoRouteAuditTotalsDisagree(unwrapAutoRouteAudit(auditFull()))).toBe(false)
    expect(autoRouteAuditTotalsDisagree(unwrapAutoRouteAudit(auditAllBlocksMissing()))).toBe(false)
    // 只命中一半的漂移样本 —— 恒等式被打破必须被抓到
    const skewed: Record<string, unknown> = { ...auditFull(), specified_model_requests: 999 }
    expect(autoRouteAuditTotalsDisagree(unwrapAutoRouteAudit(skewed))).toBe(true)
  })

  it('★ success_rate=0 有两种来源：有流量全失败 vs 根本没有流量', () => {
    const noTraffic = unwrapAutoRouteAudit(auditAllBlocksMissing())
    expect(noTraffic.success_rate).toBe(0)
    expect(autoRouteAuditHasNoTraffic(noTraffic)).toBe(true)

    const allFailed = unwrapAutoRouteAudit({ ...auditFull(), success_rate: 0 })
    expect(autoRouteAuditHasNoTraffic(allFailed)).toBe(false)
  })

  it('★ outcome_source.stale=true ⇒ 屏幕上的数字不可信', () => {
    expect(autoRouteAuditNumbersStale(unwrapAutoRouteAudit(auditFull()))).toBe(false)
    const stale = unwrapAutoRouteAudit({
      ...auditFull(),
      outcome_source: { ...outcomeSourceFresh(), stale: true, reason: 'stale' },
    })
    expect(autoRouteAuditNumbersStale(stale)).toBe(true)
  })

  it('数字键类型不对即抛错', () => {
    expect(() => unwrapAutoRouteAudit({ ...auditFull(), total_requests: '1200' })).toThrow(
      'total_requests 不是 number',
    )
    expect(() => unwrapAutoRouteAudit({ ...auditFull(), success_rate: '0.98' })).toThrow(
      'success_rate 不是 number',
    )
  })

  it('outcome_source 不是对象即抛错', () => {
    expect(() => unwrapAutoRouteAudit({ ...auditFull(), outcome_source: null })).toThrow(
      'outcome_source 不是对象',
    )
  })

  it('顶层不是对象即抛错', () => {
    expect(() => unwrapAutoRouteAudit([])).toThrow('期望对象，实得 array')
    expect(() => unwrapAutoRouteAudit(null)).toThrow('期望对象，实得 null')
  })

  it('条件键常量与必填键常量不重叠', () => {
    const req = new Set<string>(AUTO_ROUTE_AUDIT_REQUIRED_KEYS)
    for (const k of AUTO_ROUTE_AUDIT_OPTIONAL_KEYS) {
      expect(req.has(k)).toBe(false)
    }
  })

  it('fetch 打 GET 到 audit 路径', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(auditFull()))
    await fetchAutoRouteAudit()
    const { url, init } = lastCall()
    expect(init.method).toBe('GET')
    expect(url).toContain('/api/admin/auto-route/audit')
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * C. cost/customer
 * ═════════════════════════════════════════════════════════════════════════ */

describe('autoRoute / cost/customer', () => {
  it('全键行与只有 api_key_id 的稀疏行都接受', () => {
    const rows = unwrapAutoRouteCustomerCost([customerCostRowFull(), customerCostRowSparse()])
    expect(rows).toHaveLength(2)
    expect(rows[1]!.cost_usd_24h).toBeUndefined()
  })

  it('缺唯一必填键 api_key_id 即抛错', () => {
    for (const k of AUTO_ROUTE_CUSTOMER_COST_REQUIRED_KEYS) {
      expect(() => unwrapAutoRouteCustomerCost([{ key_alias: 'x' }])).toThrow(
        new RegExp(`缺 1 个键（${k}）`),
      )
    }
  })

  it('★ 计数器自相矛盾（success > requests）被抓到；缺键则不算矛盾', () => {
    expect(autoRouteCustomerCostContradicts(customerCostRowFull() as never)).toBe(false)
    expect(
      autoRouteCustomerCostContradicts({ api_key_id: 1, total_auto_requests: 10, total_auto_success: 11 }),
    ).toBe(true)
    // ★ 漂移样本：只给一半 —— 不能误报
    expect(autoRouteCustomerCostContradicts({ api_key_id: 1, total_auto_success: 999 })).toBe(false)
    expect(autoRouteCustomerCostContradicts(customerCostRowSparse())).toBe(false)
  })

  it('顶层不是数组即抛错', () => {
    expect(() => unwrapAutoRouteCustomerCost({})).toThrow('期望数组，实得 object')
  })

  it('fetch 带 api_key_id / top', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([customerCostRowFull()]))
    await fetchAutoRouteCustomerCost({ apiKeyId: '501', top: 10 })
    const { url, init } = lastCall()
    expect(init.method).toBe('GET')
    expect(url).toContain('/api/admin/auto-route/cost/customer?')
    expect(url).toContain('api_key_id=501')
    expect(url).toContain('top=10')
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * D. cost/model
 * ═════════════════════════════════════════════════════════════════════════ */

describe('autoRoute / cost/model', () => {
  it('全键行与只有 raw_model 的稀疏行都接受', () => {
    const rows = unwrapAutoRouteModelCost([modelCostRowFull(), modelCostRowSparse()])
    expect(rows).toHaveLength(2)
    expect(rows[1]!.total_cost_usd).toBeUndefined()
  })

  it('缺唯一必填键 raw_model 即抛错', () => {
    for (const k of AUTO_ROUTE_MODEL_COST_REQUIRED_KEYS) {
      expect(() => unwrapAutoRouteModelCost([{ canonical_id: 7 }])).toThrow(
        new RegExp(`缺 1 个键（${k}）`),
      )
    }
  })

  it('★ 单价：分子或分母缺失 / 分母为 0 都必须返 null（不能算出 NaN/Infinity）', () => {
    expect(autoRouteModelCostPerRequest(modelCostRowFull())).toBeCloseTo(88.25 / 9000)
    expect(autoRouteModelCostPerRequest({ raw_model: 'a', total_cost_usd: 10 })).toBeNull()
    expect(autoRouteModelCostPerRequest({ raw_model: 'a', total_requests: 5 })).toBeNull()
    expect(
      autoRouteModelCostPerRequest({ raw_model: 'a', total_cost_usd: 10, total_requests: 0 }),
    ).toBeNull()
  })

  it('top 上限 500（与 index 的 1000 不同）', () => {
    expect(autoRouteCostTopAccepted(500)).toBe(true)
    expect(autoRouteCostTopAccepted(501)).toBe(false)
    expect(autoRouteCostTopAccepted(0)).toBe(false)
    expect(autoRouteCostTopAccepted(undefined)).toBe(true)
  })

  it('fetch 带 canonical_id', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([modelCostRowFull()]))
    await fetchAutoRouteModelCost({ canonicalId: '7' })
    const { url, init } = lastCall()
    expect(init.method).toBe('GET')
    expect(url).toContain('/api/admin/auto-route/cost/model?')
    expect(url).toContain('canonical_id=7')
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * E. tuning/accuracy
 * ═════════════════════════════════════════════════════════════════════════ */

describe('autoRoute / tuning/accuracy', () => {
  it('完整响应通过（与前四条相反：这里是无条件 typed struct）', () => {
    const r = unwrapAutoRouteAccuracy({
      window_days: 7,
      breakdown: [accuracyRow()],
      generated_at: '2026-10-08T03:00:00.123456Z',
    })
    expect(r.breakdown[0]!.classifier).toBe('fuzzy_v2')
    expect(r.breakdown[0]!.total).toBe(812)
  })

  it('空窗口必须是 []（后端 make(...,0) 的显式契约）', () => {
    const r = unwrapAutoRouteAccuracy({ window_days: 7, breakdown: [], generated_at: 'x' })
    expect(r.breakdown).toEqual([])
  })

  it('breakdown 行缺任一键即抛错（逐个键各钉一条）', () => {
    for (const k of AUTO_ROUTE_ACCURACY_ROW_KEYS) {
      const row: Record<string, unknown> = { ...accuracyRow() }
      delete row[k]
      expect(() =>
        unwrapAutoRouteAccuracy({ window_days: 7, breakdown: [row], generated_at: 'x' }),
      ).toThrow(new RegExp(`breakdown\\[0\\] 缺 1 个键（${k}）`))
    }
  })

  it('顶层缺任一必填键即抛错（逐个键各钉一条）', () => {
    for (const k of AUTO_ROUTE_ACCURACY_REQUIRED_KEYS) {
      const e: Record<string, unknown> = {
        window_days: 7, breakdown: [], generated_at: 'x',
      }
      delete e[k]
      expect(() => unwrapAutoRouteAccuracy(e)).toThrow(new RegExp(`缺 1 个键（${k}）`))
    }
  })

  it('breakdown 不是数组即抛错', () => {
    expect(() =>
      unwrapAutoRouteAccuracy({ window_days: 7, breakdown: null, generated_at: 'x' }),
    ).toThrow('breakdown 不是数组')
  })

  it('★ days 越界是 400 报错（与 top 静默回落完全相反）', () => {
    expect(autoRouteAccuracyDaysAccepted(undefined)).toBe(true)
    expect(autoRouteAccuracyDaysAccepted(1)).toBe(true)
    expect(autoRouteAccuracyDaysAccepted(90)).toBe(true)
    expect(autoRouteAccuracyDaysAccepted(0)).toBe(false)
    expect(autoRouteAccuracyDaysAccepted(91)).toBe(false)
    expect(autoRouteAccuracyDaysAccepted(7.5)).toBe(false)
  })

  it('★ 窗口长度换物化视图：≤7 走 5 分钟桶，8..90 走天桶 ⇒ avg_* 口径不可直接比', () => {
    expect(autoRouteAccuracyBucketGranularity(1)).toBe('5m')
    expect(autoRouteAccuracyBucketGranularity(7)).toBe('5m')
    expect(autoRouteAccuracyBucketGranularity(8)).toBe('daily')
    expect(autoRouteAccuracyBucketGranularity(90)).toBe('daily')
  })

  it('★ 五个 avg_* 全部为 0 而 total>0 ⇒ 可能是 COALESCE 编造的 0', () => {
    expect(
      autoRouteAccuracyAveragesMayBeZeroPlaceholders({
        task_type: 'code', classifier: 'c', total: 10,
        avg_quality: 0, avg_success: 0, avg_latency: 0, avg_cost: 0, drift_rate: 0,
      }),
    ).toBe(true)
    // ★ 反向样本：只要有一个非 0，就不是「全部编造」
    expect(
      autoRouteAccuracyAveragesMayBeZeroPlaceholders({
        task_type: 'code', classifier: 'c', total: 10,
        avg_quality: 0, avg_success: 0, avg_latency: 1, avg_cost: 0, drift_rate: 0,
      }),
    ).toBe(false)
    // ★ total=0 时那一行本身就没有意义，不算编造
    expect(autoRouteAccuracyAveragesMayBeZeroPlaceholders({ ...accuracyRow(), total: 0, avg_quality: 0, avg_success: 0, avg_latency: 0, avg_cost: 0, drift_rate: 0 })).toBe(false)
  })

  it('fetch 带 days', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ window_days: 14, breakdown: [], generated_at: 'x' }))
    await fetchAutoRouteAccuracy({ days: 14 })
    const { url, init } = lastCall()
    expect(init.method).toBe('GET')
    expect(url).toContain('/api/admin/auto-route/tuning/accuracy?')
    expect(url).toContain('days=14')
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * F. analytics/decision/{request_id}
 * ═════════════════════════════════════════════════════════════════════════ */

describe('autoRoute / analytics/decision', () => {
  it('无 l2 的响应通过（l2 是条件键）', () => {
    const d = unwrapAutoRouteDecision(decisionNoL2())
    expect(d.l1.task_type).toBe('code')
    expect(autoRouteDecisionHasL2(d)).toBe(false)
  })

  it('带 l2 的响应通过，且 decision_trace 被展开成对象', () => {
    const d = unwrapAutoRouteDecision(decisionWithL2())
    expect(autoRouteDecisionHasL2(d)).toBe(true)
    expect(d.l2?.decision_trace).toEqual({ planned_candidates: 4, blocked_candidates: 1 })
  })

  it('★ decision_trace 被后端跳过（"" / "{}" / 解析失败）时 l2 仍然合法', () => {
    // 后端是「不写这个键」，不是「写 undefined」⇒ 夹具必须真的缺键
    const { decision_trace: _omitted, ...l2WithoutTrace } = decisionWithL2().l2 as Record<string, unknown>
    const d = unwrapAutoRouteDecision({ ...decisionNoL2(), l2: l2WithoutTrace })
    expect('decision_trace' in (d.l2 as object)).toBe(false)
    expect(autoRouteDecisionHasL2(d)).toBe(true)
  })

  it('缺顶层任一必填键即抛错（逐个键各钉一条）', () => {
    for (const k of AUTO_ROUTE_DECISION_REQUIRED_KEYS) {
      const d: Record<string, unknown> = { ...decisionNoL2() }
      delete d[k]
      expect(() => unwrapAutoRouteDecision(d)).toThrow(new RegExp(`缺 1 个键（${k}）`))
    }
  })

  it('l1 缺 task_type / profile 即抛错（逐个键各钉一条）', () => {
    for (const k of AUTO_ROUTE_L1_REQUIRED_KEYS) {
      const l1: Record<string, unknown> = { ...decisionNoL2().l1 }
      delete l1[k]
      expect(() => unwrapAutoRouteDecision({ ...decisionNoL2(), l1 })).toThrow(
        new RegExp(`l1 缺 1 个键（${k}）`),
      )
    }
  })

  it('l2 存在时缺 ts / success 即抛错（逐个键各钉一条）', () => {
    for (const k of AUTO_ROUTE_L2_REQUIRED_KEYS) {
      const l2: Record<string, unknown> = { ts: 'x', success: true }
      delete l2[k]
      expect(() => unwrapAutoRouteDecision({ ...decisionNoL2(), l2 })).toThrow(
        new RegExp(`l2 缺 1 个键（${k}）`),
      )
    }
  })

  it('l2 为 null / 数组即抛错', () => {
    expect(() => unwrapAutoRouteDecision({ ...decisionNoL2(), l2: null })).toThrow(
      'l2 不是对象',
    )
    expect(() => unwrapAutoRouteDecision({ ...decisionNoL2(), l2: [] })).toThrow('l2 不是对象')
  })

  it('success 不是 boolean 即抛错', () => {
    expect(() => unwrapAutoRouteDecision({ ...decisionNoL2(), success: 'true' })).toThrow(
      'success 不是 boolean',
    )
  })

  it('★ client_model/outbound_model 的 "" 是 NULL 还是真空串分不出来', () => {
    const nullDerived = unwrapAutoRouteDecision({
      ...decisionNoL2(),
      client_model: '',
      outbound_model: '',
    })
    expect(autoRouteDecisionModelsBothEmpty(nullDerived)).toBe(true)
    const realEmpty = unwrapAutoRouteDecision({
      ...decisionNoL2(),
      client_model: '',
      outbound_model: 'gpt-4o-2024-11-20',
    })
    expect(autoRouteDecisionModelsBothEmpty(realEmpty)).toBe(false)
  })

  it('★ blob 带来新键 ⇒ splat 跑过，三个库列的读数都不可当可信来源', () => {
    // ★ 漂移样本：blob 既带来新键、又带来与库列同名的键
    const shadowed = unwrapAutoRouteDecision({
      ...decisionNoL2(),
      l1: { task_type: 'from-blob', profile: 'from-blob', confidence: 0.1, reason: 'quota' },
    })
    expect(autoRouteDecisionL1SplatKeys(shadowed.l1)).toEqual(['reason'])
    expect(shadowed.l1.task_type).toBe('from-blob')
  })

  it('★ 只有库列、没有新键 ⇒ splat 没留下证据 —— 但这**不能**当成「没被覆盖」', () => {
    const d = unwrapAutoRouteDecision({
      ...decisionNoL2(),
      l1: { task_type: 'code', profile: 'smart', confidence: 0.5 },
    })
    // 空结果：只能说「看不到证据」，不能推出「值可信」
    expect(autoRouteDecisionL1SplatKeys(d.l1)).toEqual([])
  })

  it('★ 真正的盲区样本：blob 只带同名键、不带任何新键 —— 谓词返回空但值已被顶掉', () => {
    const blind = unwrapAutoRouteDecision({
      ...decisionNoL2(),
      l1: { task_type: 'from-blob', profile: 'smart' },
    })
    expect(autoRouteDecisionL1SplatKeys(blind.l1)).toEqual([])
    expect(blind.l1.task_type).toBe('from-blob')
  })

  it('l1 键集合：DB 键常量与必填键常量一致（confidence 可选故不在必填里）', () => {
    for (const k of AUTO_ROUTE_L1_REQUIRED_KEYS) {
      expect(AUTO_ROUTE_L1_DB_KEYS as readonly string[]).toContain(k)
    }
    expect(AUTO_ROUTE_L1_DB_KEYS).toContain('confidence')
  })

  it('fetch 把 request_id 放进路径并做 URL 编码', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(decisionNoL2()))
    await fetchAutoRouteDecision('a1b2/c3 d4')
    const { url, init } = lastCall()
    expect(init.method).toBe('GET')
    expect(url).toContain('/api/admin/auto-route/analytics/decision/a1b2%2Fc3%20d4')
  })
})

describe('autoRoute / l2 形态预判', () => {
  it('★ 32 位 hex（bare）⇒ 形态上可能有 l2', () => {
    expect(autoRouteDecisionCanHaveL2('a1b2c3d4e5f60718293a4b5c6d7e8f90')).toBe(true)
  })

  it('★ dashed uuid ⇒ 形态上可能有 l2', () => {
    expect(autoRouteDecisionCanHaveL2('a1b2c3d4-e5f6-0718-293a-4b5c6d7e8f90')).toBe(true)
  })

  it('★ 大写 / 前后空白 / 混合连字符都算（后端先 ToLower + ReplaceAll）', () => {
    expect(autoRouteDecisionCanHaveL2('A1B2C3D4E5F60718293A4B5C6D7E8F90')).toBe(true)
    expect(autoRouteDecisionCanHaveL2('  a1b2c3d4e5f60718293a4b5c6d7e8f90  ')).toBe(true)
    expect(autoRouteDecisionCanHaveL2('a1b2-c3d4-e5f6-0718-293a-4b5c6d7e8f90')).toBe(true)
  })

  it('★ 探测 id / 带前缀 id ⇒ **永远不会有 l2**（后端连查询都不发）', () => {
    expect(autoRouteDecisionCanHaveL2('probe-20261008-001')).toBe(false)
    expect(autoRouteDecisionCanHaveL2('self-check-abc123')).toBe(false)
  })

  it('★ 长度不对 / 含非 hex ⇒ false', () => {
    expect(autoRouteDecisionCanHaveL2('a1b2c3d4')).toBe(false)
    expect(autoRouteDecisionCanHaveL2('z'.repeat(32))).toBe(false)
    expect(autoRouteDecisionCanHaveL2('a1b2c3d4e5f60718293a4b5c6d7e8f9z')).toBe(false)
    // ★ 32 位但带一个非 hex 字符
    expect(autoRouteDecisionCanHaveL2('g1b2c3d4e5f60718293a4b5c6d7e8f90')).toBe(false)
  })

  it('非字符串 / 空 ⇒ false', () => {
    expect(autoRouteDecisionCanHaveL2(null)).toBe(false)
    expect(autoRouteDecisionCanHaveL2(undefined)).toBe(false)
    expect(autoRouteDecisionCanHaveL2('')).toBe(false)
  })

  it('★ 形态预判只是**必要条件**：形态对但没 l2 也要如实显示「没有 L2 记录」', () => {
    const noL2 = unwrapAutoRouteDecision(decisionNoL2())
    expect(autoRouteDecisionCanHaveL2(noL2.request_id)).toBe(true)
    expect(autoRouteDecisionHasL2(noL2)).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 跨端点：三条 top / days 口径互不相同
 * ═════════════════════════════════════════════════════════════════════════ */

describe('autoRoute / 跨端点口径', () => {
  it('★ 同一个 100：index 接受，两条 cost 拒绝 —— 不能跨端点类推', () => {
    expect(autoRouteIndexTopAccepted(100)).toBe(true)
    expect(autoRouteCostTopAccepted(100)).toBe(true)
    // index 上限 1000，cost 上限 500
    expect(autoRouteIndexTopAccepted(900)).toBe(true)
    expect(autoRouteCostTopAccepted(900)).toBe(false)
  })

  it('稀疏键常量确实不在必填键常量里（index）', () => {
    const req = new Set<string>(AUTO_ROUTE_INDEX_REQUIRED_KEYS)
    for (const k of ['success_rate', 'p95_latency_ms', 'canonical_id', 'score_smart']) {
      expect(req.has(k)).toBe(false)
    }
  })

  it('哨兵键不在必填键里', () => {
    expect(AUTO_ROUTE_INDEX_REQUIRED_KEYS as readonly string[]).not.toContain(
      AUTO_ROUTE_INDEX_WARNING_KEY,
    )
  })

  it('类型导出与解包器结果兼容（调用方无需 cast 就能用谓词）', () => {
    const rows: AutoRouteIndexRow[] = unwrapAutoRouteIndex([indexRowFull()])
    expect(autoRouteIndexAwaitingFirstRefresh(rows)).toBe(false)
    const a: AutoRouteAuditResponse = unwrapAutoRouteAudit(auditFull())
    expect(autoRouteAuditTotalsDisagree(a)).toBe(false)
    const d: AutoRouteDecisionResponse = unwrapAutoRouteDecision(decisionWithL2())
    expect(autoRouteDecisionHasL2(d)).toBe(true)
  })
})