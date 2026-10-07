import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchRoutingBlockedDiagnostic,
  unwrapRoutingBlocked,
  routingBlockedTotalsDisagree,
  routingBlockedBlockedNegative,
  routingBlockedTruncated,
  routingBlockedStateUnavailable,
  routingBlockedManualDisabledUnreliable,
  routingBlockedReasonAbsent,
  routingBlockedReasonEmpty,
  routingBlockedBreakdownEmptyKey,
  routingBlockedSumMismatch,
  routingBlockedCredInternalMismatch,
  ROUTING_BLOCKED_MAX_BINDINGS,
  ROUTING_BLOCKED_REQUIRED_KEYS,
  ROUTING_BLOCKED_CRED_REQUIRED_KEYS,
  ROUTING_BLOCKED_BINDING_REQUIRED_KEYS,
  type RoutingBlockedDiagnostic,
  type RoutingBlockedCredential,
} from './routingBlocked'

/**
 * 路由阻塞诊断（2026-10-08）。
 *
 * 夹具**逐字照抄** `admin/diagnostics_routing.go:22-53` 的三个 struct：
 * 字段名、json tag、`omitempty` 位置全部照搬，连 `unavailable_reason` 的
 * 「键不存在」形态都单独造了一份。
 *
 * 这组判据守的是**语义**而不是渲染：
 *   ① 三层结构各自有必填键清单，缺任一个都必须抛错
 *   ② ★★★ 顶层三个计数互相矛盾 / blocked 为负 —— 后端钳位只钳了一半
 *   ③ ★★★★★★ 凭据状态整段缺失时 `manual_disabled: false` 是**危险错值**
 *   ④ unavailable_reason 的「键不存在」与「空串」语义不同
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

/** 逐字照抄 routingBlockedBinding（:22-28）。 */
function binding(over: Record<string, unknown> = {}) {
  return {
    credential_id: 9,
    credential_label: 'tok-a',
    raw_model_name: 'gpt-4o',
    is_routable: false,
    unavailable_reason: 'quota_exhausted',
    ...over,
  }
}

/**
 * ★ 「unavailable_reason 键不存在」的**唯一正确造法**。
 *
 * 踩过的坑：写 `binding({ is_routable: false })` 想表达「键不存在」——
 * 但展开运算符只覆盖显式给的键，`unavailable_reason` 仍然带着默认值。
 * ⇒ **要让键不存在，只能把它从对象上删掉**，不能用「不传」表达。
 * 这正是 Go `omitempty` 在 JSON 里的真实形态。
 */
function bindingWithoutReasonKey(over: Record<string, unknown> = {}) {
  const b = binding(over) as Record<string, unknown>
  delete b.unavailable_reason
  return b
}

/** 逐字照抄 routingBlockedCredential（:30-42）。 */
function cred(over: Record<string, unknown> = {}) {
  return {
    credential_id: 9,
    credential_label: 'tok-a:小米大模型',
    status: 'ok',
    availability_state: 'ready',
    health_status: 'healthy',
    manual_disabled: false,
    lifecycle_status: 'active',
    bindings_total: 2,
    bindings_routable: 1,
    bindings_blocked: 1,
    bindings: [binding(), binding({ is_routable: true, unavailable_reason: undefined })],
    ...over,
  }
}

/** 逐字照抄 routingBlockedDiagnostic（:44-53）。 */
function diag(over: Record<string, unknown> = {}) {
  return {
    provider_id: 3,
    provider_name: '小米大模型',
    bindings_total: 2,
    bindings_routable: 1,
    bindings_blocked: 1,
    block_reason_breakdown: { quota_exhausted: 1 },
    credentials: [cred()],
    ...over,
  } as unknown as RoutingBlockedDiagnostic
}

describe('routing-blocked：URL 与前端守卫', () => {
  it('★ URL 带 provider_id，且不发 body', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(diag()))
    await fetchRoutingBlockedDiagnostic(3)
    expect(lastCall().url).toBe('/api/admin/diagnostics/routing-blocked?provider_id=3')
    expect(lastCall().init.body).toBeUndefined()
  })

  it('★★★★ provider_id 非正整数 / 非整数 ⇒ 前端先拒（后端 :65-70 返同一个 400）', async () => {
    for (const bad of [0, -1, 1.5, NaN]) {
      const err = await fetchRoutingBlockedDiagnostic(bad).then(
        () => null,
        (e: unknown) => e as Error,
      )
      expect(err, `provider_id=${bad} 应被前端拒`).toBeInstanceOf(Error)
      expect(err!.message).toMatch(/missing or invalid provider_id/)
    }
    // ★ 前端拒 ⇒ 一次请求都不该发出去
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('★ 走 fetch 的端到端路径也过解包', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(diag()))
    const d = await fetchRoutingBlockedDiagnostic(3)
    expect(d.provider_name).toBe('小米大模型')
    expect(d.credentials[0]?.bindings.length).toBe(2)
  })
})

describe('routing-blocked：三层解包', () => {
  it('★★ 顶层 7 个恒存在键缺任一个都抛错（truncated 因 omitempty 不在内）', () => {
    expect(ROUTING_BLOCKED_REQUIRED_KEYS.length).toBe(7)
    expect(ROUTING_BLOCKED_REQUIRED_KEYS).not.toContain('truncated')
    for (const k of ROUTING_BLOCKED_REQUIRED_KEYS) {
      const item = { ...diag() } as Record<string, unknown>
      delete item[k]
      expect(() => unwrapRoutingBlocked(item), `缺 ${k} 应抛错`).toThrow(/缺 1 个/)
    }
  })

  it('★★ 凭据层 11 个键缺任一个都抛错', () => {
    expect(ROUTING_BLOCKED_CRED_REQUIRED_KEYS.length).toBe(11)
    const base = diag()
    for (const k of ROUTING_BLOCKED_CRED_REQUIRED_KEYS) {
      const c = { ...base.credentials[0] } as Record<string, unknown>
      delete c[k]
      const item = { ...base, credentials: [c] } as unknown
      expect(() => unwrapRoutingBlocked(item), `credentials 缺 ${k} 应抛错`).toThrow(/缺 1 个/)
    }
  })

  it('★★ 绑定层 4 个键缺任一个都抛错（unavailable_reason 因 omitempty 不在内）', () => {
    expect(ROUTING_BLOCKED_BINDING_REQUIRED_KEYS.length).toBe(4)
    expect(ROUTING_BLOCKED_BINDING_REQUIRED_KEYS).not.toContain('unavailable_reason')
    const base = diag()
    for (const k of ROUTING_BLOCKED_BINDING_REQUIRED_KEYS) {
      const b = { ...base.credentials[0]!.bindings[0] } as Record<string, unknown>
      delete b[k]
      const item = {
        ...base,
        credentials: [{ ...base.credentials[0], bindings: [b] }],
      } as unknown
      expect(() => unwrapRoutingBlocked(item), `bindings 缺 ${k} 应抛错`).toThrow(/缺 1 个/)
    }
  })

  it('★★★ unavailable_reason 键不存在是合法形态（NULL 被 omitempty）', () => {
    // 后端 unavailable_reason 是 *string + omitempty ⇒ NULL 时整个键消失
    const item = diag({
      credentials: [{ ...cred(), bindings: [bindingWithoutReasonKey({ is_routable: false })] }],
    })
    expect(() => unwrapRoutingBlocked(item)).not.toThrow()
    const d = item as RoutingBlockedDiagnostic
    expect('unavailable_reason' in d.credentials[0]!.bindings[0]!).toBe(false)
  })

  it('★★ 非对象 / null / 数组 必须抛错，不返回空诊断', () => {
    expect(() => unwrapRoutingBlocked(null)).toThrow(/实得 null/)
    expect(() => unwrapRoutingBlocked([])).toThrow(/实得 array/)
    expect(() => unwrapRoutingBlocked('x')).toThrow(/形状不符/)
    // 顶层形状对但内层坏 ⇒ 也要抛，不能只校验第一层
    expect(() => unwrapRoutingBlocked({ ...diag(), credentials: [null] })).toThrow(/不是对象/)
    expect(() => unwrapRoutingBlocked({ ...diag(), credentials: [{ ...cred(), bindings: [1] }] })).toThrow(
      /不是对象/,
    )
  })

  it('★★★ 顶层键类型不对必须抛错（键在但类型错，比缺键更隐蔽）', () => {
    // ★ 这条是补的：此前只钉了「缺键」，而整段类型校验可以被整段删掉而全绿。
    expect(() => unwrapRoutingBlocked({ ...diag(), provider_id: '3' })).toThrow(/类型不对/)
    expect(() => unwrapRoutingBlocked({ ...diag(), provider_name: 7 })).toThrow(/类型不对/)
    expect(() => unwrapRoutingBlocked({ ...diag(), bindings_total: '2' })).toThrow(/类型不对/)
    expect(() => unwrapRoutingBlocked({ ...diag(), bindings_routable: null })).toThrow(/类型不对/)
    expect(() => unwrapRoutingBlocked({ ...diag(), bindings_blocked: undefined })).toThrow(/类型不对/)
    // breakdown 是数组 / credentials 不是数组
    expect(() => unwrapRoutingBlocked({ ...diag(), block_reason_breakdown: [] })).toThrow(/类型不对/)
    expect(() => unwrapRoutingBlocked({ ...diag(), credentials: {} })).toThrow(/类型不对/)
  })
})

describe('routing-blocked：★★★ 钳位不一致导致的三个数字互相矛盾', () => {
  /**
   * 逐字复刻后端 `:144-147` + `:243` 的钳位 bug：
   *   LIMIT 501 行 → total=501 → truncated → total 被钳成 500
   *   但 routable 是**遍历 501 行**累加的，没钳
   *   ⇒ bindings_blocked = 500 - 501 = **-1**
   */
  const truncatedAllRoutable = diag({
    bindings_total: 500,
    bindings_routable: 501,
    bindings_blocked: -1,
    block_reason_breakdown: {},
    credentials: [],
    truncated: true,
  })

  it('★★★★★ 子集大于全集（routable > total）⇒ 判为计数矛盾', () => {
    expect(routingBlockedTotalsDisagree(truncatedAllRoutable)).toBe(true)
    expect(routingBlockedTotalsDisagree(diag())).toBe(false)
  })

  it('★★★★★ ⚠️ 恒等式 routable+blocked===total 检测不出异常（第一版判据的错误）', () => {
    // blocked 就是用钳后的 total 减出来的 ⇒ 这个等式**恒成立**，
    // 哪怕 total/routable/blocked 三个数已经彻底矛盾。
    const d = truncatedAllRoutable
    expect(d.bindings_routable + d.bindings_blocked).toBe(d.bindings_total)
    // ⇒ 用等式当判据会全绿；必须查「子集 > 全集」
    expect(routingBlockedTotalsDisagree(d)).toBe(true)
  })

  it('★★★★★ bindings_blocked 为负 ⇒ 必须检出', () => {
    expect(routingBlockedBlockedNegative(truncatedAllRoutable)).toBe(true)
    expect(routingBlockedBlockedNegative(diag())).toBe(false)
  })

  it('★ truncated 键存在时判为截断；键不存在（omitempty）时不算', () => {
    expect(routingBlockedTruncated(truncatedAllRoutable)).toBe(true)
    const noKey = { ...diag() } as Record<string, unknown>
    delete noKey.truncated
    // ★ 不能写成 `resp.truncated === false` 当「没截断」—— 那是恒假的坑
    expect(routingBlockedTruncated(unwrapRoutingBlocked(noKey))).toBe(false)
  })

  it('★★★★ 逐凭据求和与顶层 total 对不上（顶层被钳、逐条没钳）', () => {
    // 逐凭据 bindings_total 加起来是未钳的真实行数
    const mixed = diag({
      bindings_total: 500,
      bindings_routable: 500,
      bindings_blocked: 0,
      credentials: [
        cred({ bindings_total: 501, bindings_routable: 501, bindings_blocked: 0, bindings: [] }),
      ],
      truncated: true,
    })
    expect(routingBlockedSumMismatch(mixed)).toBe(true)
    expect(routingBlockedSumMismatch(diag())).toBe(false)
  })

  it('★ 逐凭据内部自相矛盾也能检出', () => {
    expect(routingBlockedCredInternalMismatch(cred({ bindings_total: 5 }) as RoutingBlockedCredential)).toBe(true)
    expect(routingBlockedCredInternalMismatch(cred() as RoutingBlockedCredential)).toBe(false)
  })

  it('★ 上限常量与后端 maxBindings 一致', () => {
    expect(ROUTING_BLOCKED_MAX_BINDINGS).toBe(500)
  })
})

describe('routing-blocked：★★★★★ 凭据状态整段缺失时 manual_disabled 是危险错值', () => {
  /** 后端 :173-196：凭据状态查询失败只 slog.Warn，五个字段全落回零值。 */
  const stateGone = diag({
    credentials: [
      cred({
        status: '',
        availability_state: '',
        health_status: '',
        lifecycle_status: '',
        // ★★ 真被手动停用的凭据在这里会显示成「未停用」
        manual_disabled: false,
      }),
    ],
  })

  it('★★★ 五个状态字段全空 ⇒ 判为「状态不可用」，不是「状态正常」', () => {
    expect(routingBlockedStateUnavailable(stateGone)).toBe(true)
    expect(routingBlockedStateUnavailable(diag())).toBe(false)
  })

  it('★★★★★ manual_disabled=false 且状态缺失 ⇒ 该字段不可信', () => {
    expect(routingBlockedManualDisabledUnreliable(stateGone.credentials[0]!)).toBe(true)
    // 状态齐全时 false 是真结论
    expect(routingBlockedManualDisabledUnreliable(diag().credentials[0]!)).toBe(false)
  })

  it('★★ 空 credentials 不算「状态不可用」（那是真结论：一个绑定都没有）', () => {
    const none = diag({ credentials: [] })
    expect(routingBlockedStateUnavailable(none)).toBe(false)
  })
})

describe('routing-blocked：unavailable_reason 的两种「拿不到原因」', () => {
  it('★★★★ 键不存在 ⇔ SQL NULL（后端 :131 会换成 "unknown"）', () => {
    const b = binding({ unavailable_reason: undefined })
    expect(routingBlockedReasonAbsent(b)).toBe(true)
    expect(routingBlockedReasonEmpty(b)).toBe(false)
  })

  it('★★★★ 空串 ⇔ 后端确实存了空串 —— 与 NULL 不是一回事', () => {
    const b = binding({ unavailable_reason: '' })
    expect(routingBlockedReasonEmpty(b)).toBe(true)
    expect(routingBlockedReasonAbsent(b)).toBe(false)
  })

  it('★★ 可路由的绑定两种判据都不命中', () => {
    const b = binding({ is_routable: true, unavailable_reason: undefined })
    expect(routingBlockedReasonAbsent(b)).toBe(false)
    expect(routingBlockedReasonEmpty(b)).toBe(false)
  })

  it('★★★ 可路由**且**原因为空串 ⇒ 也不能算「原因为空」（补：此前样本覆盖不到）', () => {
    // ★ 这条是补的：原样本用 unavailable_reason=undefined，于是「判据不看
    // is_routable」这种变异照样全绿 —— 测的是巧合，不是判据。
    const b = binding({ is_routable: true, unavailable_reason: '' })
    expect(routingBlockedReasonEmpty(b)).toBe(false)
    expect(routingBlockedReasonAbsent(b)).toBe(false)
  })

  it('★★★ breakdown 里可能有空字符串键（后端只把 NULL 换成 "unknown"）', () => {
    expect(routingBlockedBreakdownEmptyKey(diag({ block_reason_breakdown: { '': 2 } }))).toBe(true)
    expect(routingBlockedBreakdownEmptyKey(diag())).toBe(false)
  })
})