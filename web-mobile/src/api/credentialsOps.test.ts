import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  unwrapDecisions,
  resetCredentialState,
  unwrapResetState,
  forceRecoverCredential,
  setManualDisabled,
  clearManualDisabled,
  submitCredentialProbe,
  submitBatchProbe,
  resetStateProbeIndeterminate,
  resetStateProbeOnlySubmitted,
  resetStateOutcomeAmbiguous,
  resetStateActorLooksLikeIp,
  resetStateIsWholeCredential,
  resetStateReasonMissing,
  RESET_STATE_REQUIRED_KEYS,
  RESET_STATE_DETAIL_REQUIRED_KEYS,
  RESET_STATE_DETAIL_OPTIONAL_KEYS,
  BATCH_PROBE_MAX,
  type CredentialDecisionsResponse,
  type ResetStateResult,
  type ResetStateDetails,
} from './credentialsOps'

/**
 * unwrapDecisions — decisions 端点的信封解包（2026-10-06）。
 *
 * 后端 admin/credential_monitor.go:1658 的实际返回是
 *   {credential_id, decisions, total}
 * 即**信封**，不是裸数组（这与 nodes.ts 的 unwrapMonitorSummary 是同一类坑：
 * 把信封当裸载荷 ⇒ 视图在对象上调 filter/filter ⇒ TypeError ⇒ 页面进错误态）。
 *
 * 立场：**形状不符抛错，不静默返回空数组**。返回 [] 会让「解包失败」和
 * 「这个凭据最近没有流量」在 UI 上完全一样 —— 后者是真结论，前者是契约漂移，
 * 混为一谈就会把一次后端改动误读成「节点不跑了」。
 */
describe('unwrapDecisions', () => {
  const DECISION = {
    ts: '2026-10-06T13:00:00Z',
    request_id: 'r1',
    model: 'claude-sonnet-4-6',
    tier: 1,
    success: true,
    latency_ms: 820,
    error_class: null,
    chosen_provider_id: 3,
    client_model: null,
    outbound_model: null,
    sticky_hit: null,
  }

  it('取信封里的 decisions（后端真实形态）', () => {
    const resp: CredentialDecisionsResponse = { credential_id: 9, decisions: [DECISION], total: 1 }
    const out = unwrapDecisions(resp)
    expect(out).toHaveLength(1)
    expect(out[0]?.request_id).toBe('r1')
  })

  it('空信封返回空数组 —— 这是「无记录」的真实结论', () => {
    const resp: CredentialDecisionsResponse = { credential_id: 9, decisions: [], total: 0 }
    expect(unwrapDecisions(resp)).toEqual([])
  })

  it('裸数组也容忍（防御后端形态漂移）', () => {
    expect(unwrapDecisions([DECISION])).toHaveLength(1)
  })

  it.each([
    ['null', null],
    ['字符串', 'oops'],
    ['数字', 42],
    ['缺 decisions 的对象', { credential_id: 9, total: 0 }],
    ['decisions 不是数组', { credential_id: 9, decisions: null, total: 0 }],
  ])('%s 必须抛错，不能静默返空数组', (_label, bad) => {
    // ★ 反向判据：若这里改成「形状不符返 []」，本用例会红。
    //   那样一来「后端改了返回结构」会被显示成「这个凭据最近没流量」。
    expect(() => unwrapDecisions(bad as never)).toThrow(/形状不符/)
  })
})

// ══════════════════════════════════════════════════════════════════════
// 写操作面的 API 层测试（2026-10-07 第四十九轮补）
//
// ★ 这批测试补上之前的一处真缺口：本文件**只测了 unwrapDecisions 一个只读解包**，
//   七个写操作函数（set/clear-manual-disabled、两个 probe、force-recover、reset-state）
//   **一个都没有 API 层测试** —— 其中 reset-state 连 UI 入口都没有。
//
// 后端逐条对应：
//   admin/routing_reset.go:52-153      reset-state（**superAdmin**）
//   admin/routing_reset.go:56-60        405 method not allowed
//   admin/routing_reset.go:62-65        400 id path param must be a positive integer
//   admin/routing_reset.go:69-72        400 reason is required for audit trail
//   admin/routing_reset.go:91-94        ★ actor 回退是 r.RemoteAddr（裸 IP）
//   admin/routing_reset.go:124-129      trigger_probe 的 submitter 守卫
//   admin/routing_reset.go:146-153      ★ 真实响应只有 6 个键
//   admin/handler.go:1517-1520          readJSON：body 为 nil 时返回 nil
//   admin/diagnostics_credential.go:227 force-recover（无请求体，只有 query id）
//   admin/credential_state_handlers.go:30-51  submit probe（202 异步）
//   admin/credential_monitor.go:1785-1792     reason 为空串 ⇒ 400
// ══════════════════════════════════════════════════════════════════════

const fetchMock = vi.fn()

function jsonResponse(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
    headers: new Headers({ 'content-type': 'application/json' }),
  } as unknown as Response
}

function lastCall(): { url: string; init: { method?: string; body?: string } } {
  const c = fetchMock.mock.calls.at(-1)!
  return { url: String(c[0]), init: (c[1] ?? {}) as { method?: string; body?: string } }
}

function lastBody(): Record<string, unknown> {
  const b = lastCall().init.body
  return b ? (JSON.parse(b) as Record<string, unknown>) : {}
}

function wire<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T
}

/** ★★ 逐字照抄 `routing_reset.go:146-153` 的 map 字面量。 */
function resetOk(over: Record<string, unknown> = {}): ResetStateResult {
  return wire({
    message: 'credential state reset',
    credential_id: 12,
    raw_model: '',
    actor: 'alice',
    probe_triggered: false,
    details: {
      credential_id: 12,
      raw_model: '',
      reason: '人工排障',
      endpoint: 'reset-state',
      actor: 'alice',
      db_committed: true,
    },
    ...over,
  }) as ResetStateResult
}

/**
 * ★★★ 这支的形状**逐字**取自 `routing_reset.go:110-119`：DB 效果已落地，
 * 但请求以 5xx 结束、审计照记。⇒ 客户端收到的是错误信封，**状态却已经改了**。
 *
 * details 用 `ResetStateDetails` 标注 ⇒ 夹具里打错键名会直接编译失败，
 * 而不是悄悄混进一个后端从不发出的键。
 */
const partialFailedDetails: ResetStateDetails = {
  credential_id: 12,
  raw_model: '',
  reason: '人工排障',
  endpoint: 'reset-state',
  actor: 'alice',
  db_committed: true,
  audit_outcome: 'partial_failed',
}

function resetPartialFailed(): ResetStateResult {
  return wire({
    message: 'credential state reset',
    credential_id: 12,
    raw_model: '',
    actor: 'alice',
    probe_triggered: false,
    details: partialFailedDetails,
  }) as ResetStateResult
}

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('reset-state：URL 与请求体', () => {
  it('★ URL、三个 body 键、reason 必填', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(resetOk()))
    await resetCredentialState(12, '人工排障', 'llama-3.3', true)
    expect(lastCall().url).toBe('/api/routing/credentials/12/reset-state')
    expect(lastCall().init.method).toBe('POST')
    expect(lastBody()).toEqual({ reason: '人工排障', raw_model: 'llama-3.3', trigger_probe: true })
  })

  it('★★ raw_model 缺省是空串（= 整凭据复位），不是 undefined', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(resetOk()))
    await resetCredentialState(12, '人工排障')
    expect(lastBody()).toEqual({ reason: '人工排障', raw_model: '', trigger_probe: false })
    expect(resetStateIsWholeCredential(resetOk())).toBe(true)
    expect(resetStateIsWholeCredential(resetOk({ raw_model: 'gpt-4o' }))).toBe(false)
  })

  it('★★★ reason 为空 ⇒ **前端先拒**，不发请求（后端会返 400）', async () => {
    expect(resetStateReasonMissing('')).toBe(true)
    expect(resetStateReasonMissing('x')).toBe(false)
    await expect(resetCredentialState(12, '')).rejects.toThrow(/reason is required/)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('★★ id 非正整数 ⇒ 前端先拒，不发请求', async () => {
    for (const bad of [0, -1, 1.5]) {
      await expect(resetCredentialState(bad, 'r')).rejects.toThrow(/must be a positive integer/)
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('reset-state：解包（★ 旧接口是凭空造的）', () => {
  it('★ 六个键恒存在', () => {
    expect(RESET_STATE_REQUIRED_KEYS).toEqual([
      'message',
      'credential_id',
      'raw_model',
      'actor',
      'probe_triggered',
      'details',
    ])
    expect(Object.keys(resetOk()).sort()).toEqual([...RESET_STATE_REQUIRED_KEYS].sort())
  })

  it('★★★ 后端**不发** success / reset_fields（旧接口凭空写的两个键）', () => {
    const ok = resetOk()
    expect(ok).not.toHaveProperty('success')
    expect(ok).not.toHaveProperty('reset_fields')
    // ★ 而这四个旧接口**全漏了**，现在都在
    for (const k of ['raw_model', 'actor', 'probe_triggered', 'details']) {
      expect(ok, `应含 ${k}`).toHaveProperty(k)
    }
  })

  it('★ 缺 6 个键中的任何一个都必须抛错', () => {
    for (const k of RESET_STATE_REQUIRED_KEYS) {
      const item = { ...resetOk() } as Record<string, unknown>
      delete item[k]
      expect(() => unwrapResetState(item), `缺 ${k} 应抛错`).toThrow(/缺 1 个/)
    }
  })

  it('★ details 的 5 个必填键缺任一个都抛错', () => {
    // ★★★ 这条**必须**逐字钉住清单内容，不能只靠下面的循环：
    //   循环遍历的就是这个常量本身 ⇒ 把它缩短一个键，循环**永远不会去试那个键**
    //   ⇒ 校验随之变弱而全绿。变异 A8 就是这么溜过去的。
    expect(RESET_STATE_DETAIL_REQUIRED_KEYS).toEqual([
      'credential_id',
      'raw_model',
      'reason',
      'endpoint',
      'actor',
    ])
    for (const k of RESET_STATE_DETAIL_REQUIRED_KEYS) {
      const d = { ...resetOk().details } as Record<string, unknown>
      delete d[k]
      expect(() => unwrapResetState({ ...resetOk(), details: d }), `details 缺 ${k} 应抛错`).toThrow(/缺 1 个/)
    }
  })

  it('★ details 的 3 个可选键缺任一个都合法', () => {
    expect(RESET_STATE_DETAIL_OPTIONAL_KEYS).toEqual([
      'db_committed',
      'auto_heal_pairs_submitted',
      'audit_outcome',
    ])
    for (const k of RESET_STATE_DETAIL_OPTIONAL_KEYS) {
      const d = { ...resetOk().details } as Record<string, unknown>
      delete d[k]
      expect(() => unwrapResetState({ ...resetOk(), details: d }), `details 缺 ${k} 合法`).not.toThrow()
    }
  })

  it('类型不符 / 非对象 都抛错', () => {
    expect(() => unwrapResetState({ ...resetOk(), probe_triggered: 'false' })).toThrow(/形状不符/)
    expect(() => unwrapResetState({ ...resetOk(), credential_id: '12' })).toThrow(/形状不符/)
    expect(() => unwrapResetState({ ...resetOk(), details: [] })).toThrow(/形状不符/)
    expect(() => unwrapResetState({ ...resetOk(), details: null })).toThrow(/形状不符/)
    expect(() => unwrapResetState(null)).toThrow(/实得 null/)
    expect(() => unwrapResetState([])).toThrow(/实得 array/)
  })

  it('走 fetch 的端到端路径也过解包', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(resetOk()))
    const r = await resetCredentialState(12, '人工排障')
    expect(r.message).toBe('credential state reset')
    expect(r.details.endpoint).toBe('reset-state')
  })
})

describe('reset-state：三种「探测有没有被触发」', () => {
  it('★★★ 请求了但 probe_triggered=false ⇒ 未能确定（提交器可能没接线）', () => {
    // ★★ 后端写的是 `req.TriggerProbe && probeSubmitter != nil`，客户端不知道后端接没接
    expect(resetStateProbeIndeterminate(resetOk({ probe_triggered: false }), true)).toBe(true)
    // 没请求 ⇒ 确定「没请求」，不是「不确定」
    expect(resetStateProbeIndeterminate(resetOk({ probe_triggered: false }), false)).toBe(false)
    // 请求了且 true ⇒ 确定已提交
    expect(resetStateProbeIndeterminate(resetOk({ probe_triggered: true }), true)).toBe(false)
  })

  it('★ probe_triggered=true 只代表「已提交」，不代表探测成功', () => {
    expect(resetStateProbeOnlySubmitted(resetOk({ probe_triggered: true }))).toBe(true)
    expect(resetStateProbeOnlySubmitted(resetOk())).toBe(false)
  })
})

describe('reset-state：出错后「状态是否可能已改」', () => {
  /**
   * ★★★★★★★ 钉住那条决定性事实：partial_failed 那一支走 `writeInternalErr`
   * ⇒ 响应体是 `{"error":{"detail":"internal error (see server logs)"}}`，
   *   **details / db_committed / audit_outcome 一个都不在里面**。
   *   所以客户端判不出「已改但报错」，只能对 5xx 一律假定「可能已改」。
   */
  it('★★★★★★ 5xx 响应里没有 details ⇒ 客户端读不到 audit_outcome', async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ error: { detail: 'internal error (see server logs)' } }), {
        status: 500,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    const err = await resetCredentialState(12, '人工排障').then(
      () => null,
      (e: unknown) => e as { status: number; message: string },
    )
    expect(err).not.toBeNull()
    expect(err!.status).toBe(500)
    // ★★★ 报文里就是没有 details —— 这是本批最硬的一条事实
    expect(err!.message).not.toMatch(/audit_outcome|db_committed|partial_failed/)
    // ⇒ 视图只能按 status 分档
    expect(resetStateOutcomeAmbiguous(err!.status)).toBe(true)
  })

  it('★★★★ 4xx 全部发生在 applyForceEnable 之前 ⇒ 能证明「什么都没发生」', () => {
    // 后端 :56-89 的 405/400(id)/400(reason)/404 都在写库之前 return
    expect(resetStateOutcomeAmbiguous(400)).toBe(false)
    expect(resetStateOutcomeAmbiguous(401)).toBe(false)
    expect(resetStateOutcomeAmbiguous(403)).toBe(false)
    expect(resetStateOutcomeAmbiguous(404)).toBe(false)
    expect(resetStateOutcomeAmbiguous(405)).toBe(false)
    // 其余 5xx 一律假定「可能已改」
    expect(resetStateOutcomeAmbiguous(500)).toBe(true)
    expect(resetStateOutcomeAmbiguous(502)).toBe(true)
    expect(resetStateOutcomeAmbiguous(503)).toBe(true)
  })

  it('★★★★ 传输层失败（status=0）与没有 status ⇒ 同样二义', () => {
    // client.ts:172 把断网归一成 ApiError(0, 'network_error')：
    // 请求可能已经到达服务端并落库，只是回程断了。
    expect(resetStateOutcomeAmbiguous(0)).toBe(true)
    // EpochError / 非 ApiError ⇒ 连状态码都没有
    expect(resetStateOutcomeAmbiguous(undefined)).toBe(true)
  })

  it('★★★ partial_failed 只对审计消费方可见（200 响应里不该出现）', () => {
    // 成功那一支永远不带 audit_outcome（后端只在 :113 那一支加）
    expect(resetOk().details.audit_outcome).toBeUndefined()
    // 但它确实是合法键之一 —— 消费方按它过滤审计流
    expect(resetPartialFailed().details.audit_outcome).toBe('partial_failed')
  })

  it('★★★ actor 回退成裸 IP ⇒ 这条审计是「无登录态」记的', () => {
    expect(resetStateActorLooksLikeIp('192.168.1.7')).toBe(true)
    expect(resetStateActorLooksLikeIp('10.0.0.1:54321')).toBe(true)
    expect(resetStateActorLooksLikeIp('[::1]:443')).toBe(true)
    expect(resetStateActorLooksLikeIp('alice')).toBe(false)
    expect(resetStateActorLooksLikeIp('user-42')).toBe(false)
    expect(resetStateActorLooksLikeIp('')).toBe(false)
  })
})

describe('force-recover：无请求体，只有 query id', () => {
  it('★ URL 带 query id，且**不带** body', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        triggered: true,
        credential_id: 12,
        timestamp: '2026-10-07T00:00:00Z',
        message: 'ok',
        key_cache_invalidated: true,
        key_rotator_reset: false,
      }),
    )
    await forceRecoverCredential(12)
    expect(lastCall().url).toBe('/api/admin/diagnostics/credential/force-recover?id=12')
    expect(lastCall().init.body).toBeUndefined()
  })

  it('★★ id 非正整数 ⇒ 前端先拒，不发请求', async () => {
    for (const bad of [0, -3, 2.5]) {
      await expect(forceRecoverCredential(bad)).rejects.toThrow(/invalid credential id/)
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('节点状态修改与探测提交', () => {
  it('★ set-manual-disabled 的 body 用 manual_disabled，且必须带 reason', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ success: true, message: 'ok' }))
    await setManualDisabled(12, true, '上游 401')
    expect(lastCall().url).toBe('/api/credentials/set-manual-disabled')
    expect(lastBody()).toEqual({ credential_id: 12, manual_disabled: true, reason: '上游 401' })
  })

  it('★★ clear 是**专用端点**，不是 set(false) —— body 里没有 manual_disabled', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ success: true, message: 'ok' }))
    await clearManualDisabled(12, '恢复了')
    expect(lastCall().url).toBe('/api/credentials/clear-manual-disabled')
    expect(lastBody()).toEqual({ credential_id: 12, reason: '恢复了' })
    // ★ 反向判据：若有人把它改成 set(false)，这条会红
    expect(lastBody()).not.toHaveProperty('manual_disabled')
  })

  it('★★ reason 为空的守卫在**视图层**（API 层故意不放行）', async () => {
    // 后端 credential_monitor.go:1785-1792 对空串直接 400；
    // 模块注释写明「视图层必须在提交前挡住」⇒ API 层**不**加守卫，
    // 这样断言的是「这层没拦」这个事实，而不是「这里该有守卫」。
    fetchMock.mockResolvedValue(jsonResponse({ success: true, message: 'ok' }))
    await setManualDisabled(12, true, '')
    expect(lastBody()).toEqual({ credential_id: 12, manual_disabled: true, reason: '' })
  })

  it('★ 单凭据探测走 /test 且**不带 body**；批量走 /test-batch', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ message: 'probe submitted', credential_id: 12, status: 'pending' }))
    await submitCredentialProbe(12)
    expect(lastCall().url).toBe('/api/credentials/12/test')
    expect(lastCall().init.body).toBeUndefined()
    fetchMock.mockResolvedValue(jsonResponse({ message: 'batch probe submitted', submitted: 3, total: 3 }))
    await submitBatchProbe([1, 2, 3])
    expect(lastCall().url).toBe('/api/credentials/test-batch')
    expect(lastBody()).toEqual({ credential_ids: [1, 2, 3] })
  })

  it('★★ 批量探测上限 100：超出的**在前端就截掉**，不发 400', async () => {
    expect(BATCH_PROBE_MAX).toBe(100)
    const ids = Array.from({ length: 150 }, (_, i) => i + 1)
    fetchMock.mockResolvedValue(jsonResponse({ submitted: 100, total: 100 }))
    await submitBatchProbe(ids)
    expect((lastBody().credential_ids as number[]).length).toBe(100)
    expect((lastBody().credential_ids as number[]).at(-1)).toBe(100)
    expect(lastBody().credential_ids).not.toContain(101)
  })

  it('★ 正好 100 个不被截（边界不是 `>` 也不是 `>=`，是 slice 语义）', async () => {
    const ids = Array.from({ length: 100 }, (_, i) => i + 1)
    fetchMock.mockResolvedValue(jsonResponse({ submitted: 100, total: 100 }))
    await submitBatchProbe(ids)
    expect((lastBody().credential_ids as number[]).length).toBe(100)
  })
})
