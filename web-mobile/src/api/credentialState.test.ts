import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchCredState,
  unwrapCredState,
  credStatePath,
  CRED_STATE_PATH_PREFIX,
  CRED_STATE_REQUIRED_KEYS,
  CRED_STATE_OPTIONAL_KEYS,
  CRED_STATE_SOURCE_DOCUMENTED,
  CRED_STATE_SOURCE_FROM_DB,
  CRED_STATE_SOURCE_OBSERVED,
  CRED_STATE_HEALTH_DOCUMENTED,
  CRED_STATE_HEALTH_UNDOCUMENTED,
  CRED_STATE_HEALTH_OBSERVED,
  CRED_STATE_METRIC_PLACEHOLDER_FIELDS,
  parseCredentialIdRejects,
  modelNeedsEncoding,
  emptyModelSegmentIsUnreachable,
  credStateNeverProbed,
  credStateMissingOptionalKeys,
  credStateRecoverAtAbsent,
  GO_ZERO_TIME,
  isGoZeroTime,
  credStateUpdatedAtIsUnreadable,
  credStateUpdatedAtIsQueryTime,
  credStateHealthUnset,
  credStateHealthUndocumented,
  credStateSourceUndocumented,
  credStateSourceSaysStorageLayer,
  credStateMetricsAllZero,
  credStateSuccessRateIsIndeterminate,
  credStateSignalsDisagree,
  credStateAvailableFalseWithHealthyLabel,
  credStateHasFails,
  credStateLastErrorMissing,
  HTTP_ERROR_TRAILING_NEWLINE,
  stripHttpErrorNewline,
  isPlainTextErrorBody,
  invalidCredentialIdMessage,
  stateServiceMissingMessage,
  getStateFailedMessage,
  superAdminOnlyMessage,
  modelRequiredMessage,
  credStateErrorKind,
  credStateRedisFailureIsInvisible,
  type CredState,
} from './credentialState'

/**
 * 凭据×模型状态读面的契约测试（第四十九批，2026-10-07）。
 *
 * 后端逐条对应：
 *   admin/credential_state_handlers.go:174-180  ★wrap := h.superAdmin（不是 admin！）
 *   admin/credential_state_handlers.go:136-168  四条路径都是 http.Error ⇒ **text/plain**
 *   admin/credential_state_handlers.go:182-189  parseCredentialID：Atoi 失败**或** <= 0
 *   domains/credentialstate/manager.go:738-765  GetState：三层 miss ⇒ (nil, nil)
 *   domains/credentialstate/cache.go:105-142    node_probe_state 支（只填 7 个字段）
 *   domains/credentialstate/cache.go:144-202    model_probe_state 支（只填 5 个字段）
 *   domains/credentialstate/state.go:11-28      State：12 恒存在 + 4 omitempty
 */

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

function lastUrl(): string {
  return String(fetchMock.mock.calls.at(-1)![0])
}

function wire<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T
}

/** ★ 缓存命中时由探测写入的**完整** State（五条指标有真值）。 */
function stateFromProbe(over: Record<string, unknown> = {}): CredState {
  return wire({
    credential_id: 12,
    model: 'llama-3.3-70b-versatile',
    available: true,
    health_status: 'healthy',
    success_rate: 0.87,
    avg_latency_ms: 420,
    p95_latency_ms: 1180,
    active_sessions: 3,
    concurrency_limit: 8,
    last_updated_at: '2026-10-07T00:00:00Z',
    consecutive_fails: 0,
    source: 'model_probe',
    last_success_at: '2026-10-07T00:00:00Z',
    ...over,
  }) as CredState
}

/** ★★ `cache.go:118-134` 的 node_probe 支：只填 7 个字段，五条指标全是 Go 零值。 */
function stateFromNodeProbeDb(over: Record<string, unknown> = {}): CredState {
  return wire({
    credential_id: 12,
    model: 'llama-3.3-70b-versatile',
    available: true,
    health_status: '',
    success_rate: 0,
    avg_latency_ms: 0,
    p95_latency_ms: 0,
    active_sessions: 0,
    concurrency_limit: 0,
    last_updated_at: '2026-10-07T09:00:00Z',
    consecutive_fails: 0,
    source: 'node_probe_db',
    recover_at: '2026-10-07T09:05:00Z',
    ...over,
  }) as CredState
}

/** ★★ `cache.go:144-202` 的 legacy 支：`last_attempt_at` 为 NULL ⇒ Go 零值时间。 */
function stateFromLegacyDb(over: Record<string, unknown> = {}): CredState {
  return wire({
    credential_id: 12,
    model: 'llama-3.3-70b-versatile',
    available: false,
    health_status: 'healthy_confirmed',
    success_rate: 0,
    avg_latency_ms: 0,
    p95_latency_ms: 0,
    active_sessions: 0,
    concurrency_limit: 0,
    last_updated_at: GO_ZERO_TIME,
    consecutive_fails: 3,
    source: 'db',
    recover_at: GO_ZERO_TIME,
    ...over,
  }) as CredState
}

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})

// ══ 路径 ═════════════════════════════════════════════════════════════

describe('路径与档位', () => {
  it('路径逐段对上 credential_state_handlers.go:179', () => {
    expect(CRED_STATE_PATH_PREFIX).toBe('/api/credentials')
    expect(credStatePath(12, 'gpt-4o')).toBe('/api/credentials/12/models/gpt-4o/state')
    expect(credStatePath(3, 'llama-3.3-70b-versatile')).toBe(
      '/api/credentials/3/models/llama-3.3-70b-versatile/state',
    )
  })

  it('★★ 这条是 superAdmin 档 ⇒ 前缀仍是 /api/credentials/，但**不是** admin 档', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ credential_id: 12, model: 'gpt-4o', state: stateFromProbe() }))
    await fetchCredState(12, 'gpt-4o')
    expect(lastUrl()).toBe('/api/credentials/12/models/gpt-4o/state')
    // ★ 前缀里没有 admin —— 别照抄成 /api/admin/credentials/...
    expect(lastUrl().startsWith('/api/admin/')).toBe(false)
  })

  it('★ 模型名与 id 都要 encode（模型名里带 / 时不打会打到别的路由）', () => {
    expect(credStatePath(12, 'vendor/model:v1')).toBe('/api/credentials/12/models/vendor%2Fmodel%3Av1/state')
    expect(modelNeedsEncoding('vendor/model:v1')).toBe(true)
    expect(modelNeedsEncoding('gpt-4o')).toBe(false)
  })

  it('空模型段不可达（Go 1.22 的 {model} 不匹配空段）', () => {
    expect(emptyModelSegmentIsUnreachable('')).toBe(true)
    expect(emptyModelSegmentIsUnreachable('gpt-4o')).toBe(false)
  })
})

// ══ 解包 ═════════════════════════════════════════════════════════════

describe('解包', () => {
  it('GET 合法信封解出 state', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ credential_id: 12, model: 'gpt-4o', state: stateFromProbe({ model: 'gpt-4o' }) }),
    )
    const r = await fetchCredState(12, 'gpt-4o')
    expect(r.credential_id).toBe(12)
    expect(r.state!.success_rate).toBe(0.87)
    expect(r.state!.last_success_at).toBe('2026-10-07T00:00:00Z')
  })

  it('★★★★★ state 为 null 是合法响应（三层缓存全 miss ⇒ 200，不是 404）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ credential_id: 12, model: 'gpt-4o', state: null }))
    const r = await fetchCredState(12, 'gpt-4o')
    expect(r.state).toBeNull()
    expect(credStateNeverProbed(r)).toBe(true)
  })

  it('★★ state 为 null 时不许因为「缺 12 个键」抛错', () => {
    expect(() => unwrapCredState({ credential_id: 12, model: 'gpt-4o', state: null })).not.toThrow()
  })

  it('★ 缺 12 个必填键中的任何一个都必须抛错', () => {
    for (const k of CRED_STATE_REQUIRED_KEYS) {
      const item = { ...stateFromProbe() } as Record<string, unknown>
      delete item[k]
      expect(
        () => unwrapCredState({ credential_id: 12, model: 'gpt-4o', state: item }),
        `缺 ${k} 时应抛错`,
      ).toThrow(/缺 1 个/)
    }
  })

  it('★★ 五条指标键恒存在（两条 DB 分支不赋值，但**键在**、值为 0）', () => {
    expect(CRED_STATE_METRIC_PLACEHOLDER_FIELDS).toEqual([
      'success_rate',
      'avg_latency_ms',
      'p95_latency_ms',
      'active_sessions',
      'concurrency_limit',
    ])
    const s = unwrapCredState({ credential_id: 12, model: 'x', state: stateFromNodeProbeDb({ model: 'x' }) }).state!
    expect(credStateMetricsAllZero(s)).toBe(true)
    for (const k of CRED_STATE_METRIC_PLACEHOLDER_FIELDS) {
      expect(s, `${k} 键必须在`).toHaveProperty(k)
      expect(s[k]).toBe(0)
    }
  })

  it('★ 4 个 omitempty 键不在必填集合里（缺它们合法）', () => {
    expect(CRED_STATE_OPTIONAL_KEYS).toEqual(['last_success_at', 'last_failure_at', 'recover_at', 'last_error'])
    for (const k of CRED_STATE_OPTIONAL_KEYS) {
      expect(CRED_STATE_REQUIRED_KEYS as readonly string[]).not.toContain(k)
      const item = { ...stateFromProbe() } as Record<string, unknown>
      delete item[k]
      expect(() => unwrapCredState({ credential_id: 1, model: 'x', state: item }), `缺 ${k} 合法`).not.toThrow()
    }
  })

  it('omitempty 键若出现必须是字符串', () => {
    for (const k of CRED_STATE_OPTIONAL_KEYS) {
      expect(
        () => unwrapCredState({ credential_id: 1, model: 'x', state: { ...stateFromProbe(), [k]: 7 } }),
        `${k} 类型错应抛错`,
      ).toThrow(/形状不符/)
    }
  })

  it('两个路径键类型不对 / state 是标量 / 完全不是对象，都抛错', () => {
    expect(() => unwrapCredState({ credential_id: '12', model: 'x', state: null })).toThrow(/形状不符/)
    expect(() => unwrapCredState({ credential_id: 12, model: 7, state: null })).toThrow(/形状不符/)
    expect(() => unwrapCredState({ credential_id: 12, model: 'x', state: 'ok' })).toThrow(/形状不符/)
    expect(() => unwrapCredState({ credential_id: 12, model: 'x', state: [] })).toThrow(/形状不符/)
    expect(() => unwrapCredState({ credential_id: 12, model: 'x' })).toThrow(/形状不符/)
    expect(() => unwrapCredState(null)).toThrow(/实得 null/)
    expect(() => unwrapCredState([])).toThrow(/实得 array/)
  })

  it('必需标量类型不符都抛错', () => {
    const base = stateFromProbe()
    expect(() => unwrapCredState({ credential_id: 1, model: 'x', state: { ...base, available: 'yes' } })).toThrow(
      /形状不符/,
    )
    expect(() => unwrapCredState({ credential_id: 1, model: 'x', state: { ...base, health_status: null } })).toThrow(
      /形状不符/,
    )
    expect(() => unwrapCredState({ credential_id: 1, model: 'x', state: { ...base, source: 3 } })).toThrow(/形状不符/)
    expect(() => unwrapCredState({ credential_id: 1, model: 'x', state: { ...base, last_updated_at: null } })).toThrow(
      /形状不符/,
    )
    expect(() => unwrapCredState({ credential_id: 1, model: 'x', state: { ...base, consecutive_fails: '0' } })).toThrow(
      /形状不符/,
    )
  })

  it('三条 DB/缓存分支的**必填键集合**一致（差别只在值与可选键）', () => {
    // ⚠️ 可选键本来就不同（缓存分支带 last_success_at，两条 DB 支带 recover_at）⇒ 只比必填键。
    const required = [...CRED_STATE_REQUIRED_KEYS].sort()
    for (const s of [stateFromProbe(), stateFromNodeProbeDb(), stateFromLegacyDb()]) {
      const unwrapped = unwrapCredState({ credential_id: 1, model: 'x', state: s }).state!
      expect(Object.keys(unwrapped).filter((k) => (CRED_STATE_REQUIRED_KEYS as readonly string[]).includes(k)).sort()).toEqual(
        required,
      )
    }
    // ★ 可选键**确实**各带各的
    expect('last_success_at' in stateFromProbe()).toBe(true)
    expect('recover_at' in stateFromNodeProbeDb()).toBe(true)
    expect('recover_at' in stateFromLegacyDb()).toBe(true)
  })
})

// ══ 请求侧 ═══════════════════════════════════════════════════════════

describe('凭据 ID 解析（后端 Atoi 失败或 <= 0）', () => {
  it('★ 非数字与 <= 0 都是同一句 400', () => {
    for (const v of ['abc', '', ' ', '1.5', '0x10', '1e3', 0, -1, -999, '0', '-7']) {
      expect(parseCredentialIdRejects(v), `${JSON.stringify(v)} 应被拒`).toBe(true)
    }
  })

  it('正整数通过', () => {
    for (const v of [1, 12, '12', ' 12 ', '007', +99]) {
      expect(parseCredentialIdRejects(v), `${JSON.stringify(v)} 应通过`).toBe(false)
    }
  })
})

// ══ 枚举：注释集合 ⊂ 实际集合 ══════════════════════════════════════════

describe('两处「实际取值超出注释」', () => {
  it('★★★★ source：注释 5 个，DB 分支又写了 2 个', () => {
    expect(CRED_STATE_SOURCE_DOCUMENTED).toEqual(['request', 'probe_v2', 'model_probe', 'passive', 'manual'])
    expect(CRED_STATE_SOURCE_FROM_DB).toEqual(['node_probe_db', 'db'])
    expect(CRED_STATE_SOURCE_OBSERVED).toHaveLength(7)
    // ★★ 两条 DB 分支写的值**都不在**注释里
    for (const s of [stateFromNodeProbeDb(), stateFromLegacyDb()]) {
      expect(credStateSourceUndocumented(s), `${s.source} 不在注释集合里`).toBe(true)
      expect(credStateSourceSaysStorageLayer(s)).toBe(true)
    }
    expect(credStateSourceUndocumented(stateFromProbe())).toBe(false)
    expect(credStateSourceSaysStorageLayer(stateFromProbe())).toBe(false)
  })

  it('★★★★ health_status：注释 4 个，实际还有 3 个未记录的 + 空串', () => {
    expect(CRED_STATE_HEALTH_DOCUMENTED).toEqual(['healthy', 'warning', 'degraded', 'unreachable'])
    expect(CRED_STATE_HEALTH_UNDOCUMENTED).toEqual(['healthy_confirmed', 'probing', 'available', ''])
    expect(CRED_STATE_HEALTH_OBSERVED).toHaveLength(8)
    // ★ node_probe 支未赋值 ⇒ 空串
    const np = stateFromNodeProbeDb()
    expect(credStateHealthUnset(np)).toBe(true)
    expect(credStateHealthUndocumented(np)).toBe(true)
    // ★ legacy 支可以是 healthy_confirmed
    expect(credStateHealthUndocumented(stateFromLegacyDb())).toBe(true)
    expect(credStateHealthUndocumented(stateFromProbe())).toBe(false)
  })

  it('★ 客户端按注释建枚举会把四种真实取值判成异常', () => {
    for (const h of [...CRED_STATE_HEALTH_UNDOCUMENTED]) {
      expect(credStateHealthUndocumented({ ...stateFromProbe(), health_status: h } as CredState), h).toBe(true)
    }
    for (const h of CRED_STATE_HEALTH_DOCUMENTED) {
      expect(credStateHealthUndocumented({ ...stateFromProbe(), health_status: h } as CredState), h).toBe(false)
    }
  })
})

// ══ 二义与零值 ═══════════════════════════════════════════════════════

describe('零值与二义', () => {
  it('★★★ success_rate === 0 有三种成因，分不开', () => {
    expect(credStateSuccessRateIsIndeterminate(stateFromNodeProbeDb())).toBe(true)
    expect(credStateSuccessRateIsIndeterminate(stateFromLegacyDb())).toBe(true)
    expect(credStateSuccessRateIsIndeterminate(stateFromProbe({ success_rate: 0 }))).toBe(true)
    expect(credStateSuccessRateIsIndeterminate(stateFromProbe({ success_rate: 0.5 }))).toBe(false)
  })

  it('★★ 同一个 (凭据,模型) 两次查询可以给出不同的 success_rate，都是 200', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ credential_id: 12, model: 'x', state: stateFromNodeProbeDb({ model: 'x' }) }),
    )
    const first = await fetchCredState(12, 'x')
    fetchMock.mockResolvedValueOnce(jsonResponse({ credential_id: 12, model: 'x', state: stateFromProbe({ model: 'x' }) }))
    const second = await fetchCredState(12, 'x')
    expect(first.state!.success_rate).toBe(0)
    expect(second.state!.success_rate).toBe(0.87)
    // ★★ 响应里没有任何字段能说明差异来自哪一层
    expect(CRED_STATE_REQUIRED_KEYS).not.toContain('layer')
    expect(CRED_STATE_REQUIRED_KEYS).not.toContain('from_cache')
  })

  it('★★★ legacy 支 last_attempt_at 为 NULL ⇒ last_updated_at 是 Go 零值', () => {
    expect(isGoZeroTime(GO_ZERO_TIME)).toBe(true)
    expect(GO_ZERO_TIME).toBe('0001-01-01T00:00:00Z')
    const s = stateFromLegacyDb()
    expect(credStateUpdatedAtIsUnreadable(s)).toBe(true)
    expect(credStateUpdatedAtIsUnreadable(stateFromProbe())).toBe(false)
  })

  it('★★★ node_probe 支的 last_updated_at 是**查询时刻**，不是探测时刻', () => {
    expect(credStateUpdatedAtIsQueryTime(stateFromNodeProbeDb())).toBe(true)
    expect(credStateUpdatedAtIsQueryTime(stateFromLegacyDb())).toBe(false)
    expect(credStateUpdatedAtIsQueryTime(stateFromProbe())).toBe(false)
  })

  it('★ health_status 空串不等于「健康」', () => {
    expect(credStateHealthUnset(stateFromNodeProbeDb())).toBe(true)
    // ★★ 空串那一支 available 仍可能是 true —— 两个信号独立
    expect(stateFromNodeProbeDb().available).toBe(true)
  })

  it('★★ 标了 unreachable 却说可用 ⇒ 自相矛盾', () => {
    expect(credStateSignalsDisagree({ ...stateFromProbe(), health_status: 'unreachable', available: true })).toBe(true)
    expect(credStateSignalsDisagree({ ...stateFromProbe(), health_status: 'unreachable', available: false })).toBe(false)
    expect(credStateSignalsDisagree(stateFromProbe())).toBe(false)
  })

  it('★★ available=false 配「健康类」标签 ≠ legacy 分支能产生的形状', () => {
    // legacy 夹具：available=false + health_status=healthy_confirmed
    expect(credStateAvailableFalseWithHealthyLabel(stateFromLegacyDb())).toBe(true)
    // ★ 反例要选**真不满足**的那个：available=false 但标签是 unreachable
    expect(
      credStateAvailableFalseWithHealthyLabel({ ...stateFromProbe(), available: false, health_status: 'unreachable' }),
    ).toBe(false)
    // ★ available=true 时永不触发
    expect(credStateAvailableFalseWithHealthyLabel(stateFromProbe())).toBe(false)
  })

  it('consecutive_fails 与 available 是两件事', () => {
    expect(credStateHasFails(stateFromLegacyDb())).toBe(true)
    expect(credStateHasFails(stateFromProbe())).toBe(false)
    // ★ 有失败但仍标健康 —— 两者不互相推导
    const withFails = { ...stateFromProbe(), consecutive_fails: 2 }
    expect(credStateHasFails(withFails)).toBe(true)
    expect(withFails.available).toBe(true)
    expect(withFails.health_status).toBe('healthy')
  })

  it('last_error 键缺失与空串同义', () => {
    // ★ 缓存夹具**没有** last_error ⇒ 判为「没有错误记录」
    expect(credStateLastErrorMissing(stateFromProbe())).toBe(true)
    expect(credStateLastErrorMissing(stateFromNodeProbeDb())).toBe(true)
    expect(credStateLastErrorMissing({ ...stateFromProbe(), last_error: '' } as CredState)).toBe(true)
    expect(credStateLastErrorMissing({ ...stateFromProbe(), last_error: 'upstream 401' } as CredState)).toBe(false)
  })

  it('recover_at 在两条 DB 分支里都必存在', () => {
    expect(credStateRecoverAtAbsent(stateFromNodeProbeDb())).toBe(false)
    expect(credStateRecoverAtAbsent(stateFromLegacyDb())).toBe(false)
    expect(credStateRecoverAtAbsent(stateFromProbe())).toBe(true)
  })

  it('缺失的可选键能逐个列出来', () => {
    expect(credStateMissingOptionalKeys(stateFromProbe())).toEqual([
      'last_failure_at',
      'recover_at',
      'last_error',
    ])
    expect(credStateMissingOptionalKeys(stateFromLegacyDb({ last_error: 'x' }))).toEqual([
      'last_success_at',
      'last_failure_at',
    ])
  })
})

// ══ 错误报文：text/plain + 尾换行 ═════════════════════════════════════

describe('错误报文（★ text/plain，且带尾换行）', () => {
  it('★★ http.Error 追加的换行符会被移动端原样带出来', () => {
    expect(HTTP_ERROR_TRAILING_NEWLINE).toBe('\n')
    const raw = `state service not available${HTTP_ERROR_TRAILING_NEWLINE}`
    expect(stripHttpErrorNewline(raw)).toBe('state service not available')
    // ★ 不去尾换行的话带 $ 锚点的正则匹配不上
    expect(/^state service not available$/.test(raw)).toBe(false)
    expect(/^state service not available/.test(raw)).toBe(true)
    expect(stripHttpErrorNewline('already clean')).toBe('already clean')
  })

  it('★★ 能认出「不是 JSON 的错误体」', () => {
    expect(isPlainTextErrorBody('state service not available\n')).toBe(true)
    expect(isPlainTextErrorBody('failed to get state\n')).toBe(true)
    expect(isPlainTextErrorBody('{"error":{"detail":"x"}}')).toBe(false)
    expect(isPlainTextErrorBody('   ')).toBe(false)
  })

  it('★★ 三句判据互斥', () => {
    expect(invalidCredentialIdMessage('invalid credential ID\n')).toBe(true)
    expect(stateServiceMissingMessage('invalid credential ID\n')).toBe(false)
    expect(getStateFailedMessage('invalid credential ID\n')).toBe(false)
    expect(stateServiceMissingMessage('state service not available\n')).toBe(true)
    expect(getStateFailedMessage('state service not available\n')).toBe(false)
    expect(getStateFailedMessage('failed to get state\n')).toBe(true)
    expect(stateServiceMissingMessage('failed to get state\n')).toBe(false)
  })

  it('判据对首尾空白都宽容（报文可能已 trim 过）', () => {
    for (const m of ['invalid credential ID', '  invalid credential ID  ', 'invalid credential ID\n']) {
      expect(invalidCredentialIdMessage(m), JSON.stringify(m)).toBe(true)
    }
    expect(invalidCredentialIdMessage('invalid model ID\n')).toBe(false)
  })

  it('★ model is required 那条 400 不可达，但判据仍在（形状存在）', () => {
    expect(modelRequiredMessage('model is required\n')).toBe(true)
    expect(modelRequiredMessage('invalid credential ID\n')).toBe(false)
  })

  it('★ superAdmin 判据（这条端点是 superAdmin 档）', () => {
    expect(superAdminOnlyMessage('forbidden: super_admin role required')).toBe(true)
    expect(superAdminOnlyMessage('FORBIDDEN')).toBe(true)
    expect(superAdminOnlyMessage('state service not available\n')).toBe(false)
  })

  it('★ 五类归并互斥，未知归 unknown', () => {
    expect(credStateErrorKind('invalid credential ID\n')).toBe('invalid-id')
    expect(credStateErrorKind('state service not available\n')).toBe('service-unavailable')
    expect(credStateErrorKind('failed to get state\n')).toBe('internal')
    expect(credStateErrorKind('forbidden: super_admin role required')).toBe('super-admin')
    expect(credStateErrorKind('something else\n')).toBe('unknown')
  })

  it('★★★ Redis 挂掉不可见：报文看不出是哪一层出的问题', () => {
    // manager.go:748 吞掉 Redis 错误 ⇒ 客户端只会看到一次成功的 DB 查询
    expect(credStateRedisFailureIsInvisible('')).toBe(true)
    expect(credStateRedisFailureIsInvisible('state service not available\n')).toBe(false)
    expect(credStateRedisFailureIsInvisible('failed to get state\n')).toBe(false)
  })
})
