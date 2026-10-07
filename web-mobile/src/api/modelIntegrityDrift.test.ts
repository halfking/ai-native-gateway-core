import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchModelIntegrityFingerprintDrift,
  unwrapModelIntegrityDrift,
  driftDaysIsSendable,
  driftLimitIsSendable,
  driftCountMatchesEvents,
  modelIntegrityDriftAtCap,
  driftDaysWasRewritten,
  driftHasForeignTenants,
  driftHasNoTenantTags,
  driftHasNumericValue,
  driftNumericValueIsZero,
  driftHasContextKey,
  driftContextIsEmpty,
  driftHasResolutionTrace,
  driftHasFullResolution,
  driftIsUnresolved,
  driftSeverityIsKnown,
  driftSeverityIsUnknown,
  driftEventsAreAllFingerprintDrift,
  driftIsNewestFirst,
  driftIsEmptyWindow,
  DRIFT_DAYS_DEFAULT,
  DRIFT_DAYS_MAX,
  DRIFT_LIMIT_DEFAULT,
  DRIFT_LIMIT_MAX,
  DRIFT_ANOMALY_TYPE,
  DRIFT_SEVERITIES,
  DRIFT_RECORD_ALWAYS_KEYS,
  DRIFT_RECORD_OPTIONAL_KEYS,
  DRIFT_RECORD_NUMERIC_OPTIONAL_KEYS,
  DRIFT_KEYS,
  type ModelIntegrityDriftRecord,
  type ModelIntegrityDriftResponse,
} from './modelIntegrityDrift'

/**
 * 指纹漂移的契约测试（2026-10-08，第八十一批）。
 *
 * 后端：`admin/handler.go:924-925`（整个 `/api/admin/model-integrity/` 前缀都是
 * `h.superAdmin`）→ `admin/model_integrity.go:62-84` 的 switch 分发 →
 * `:314-388` 的 `handleModelIntegrityFingerprintDrift`。
 *
 * 重点是源文件头写明的九件事 (1)…(9)，其中带 ★ 的自校验判据都能被变异打掉。
 * ⚠️ 全部用例标题**字面量**写（不用 `it.each`）—— 变异 harness 靠标题取锚点。
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

// ── 夹具：五个恒在键 + 若干条件键（逐字照抄 model_integrity.go:19-40 的键集） ──

function ev(over: Partial<ModelIntegrityDriftRecord> = {}): ModelIntegrityDriftRecord {
  return {
    id: 1024,
    detected_at: '2026-10-08T02:00:00Z',
    anomaly_type: DRIFT_ANOMALY_TYPE,
    severity: 'high',
    resolved: false,
    request_id: 'req-abc',
    provider_id: 7,
    provider_code: 'openai',
    credential_id: 16,
    client_model: 'glm-5.2',
    outbound_model: 'glm-5.2',
    raw_model_name: 'glm-5.2',
    expected_value: 'model=fingerprint',
    actual_value: 'model=other',
    sample: 'system_fingerprint=fp-1',
    context: { system_fingerprint: 'fp-1' },
    tenant_id: 'default',
    ...over,
  }
}

/** 只有五个恒在键、没有任何条件键的最小记录（DB 里那些列全是 NULL）。 */
function bare(over: Partial<ModelIntegrityDriftRecord> = {}): ModelIntegrityDriftRecord {
  return {
    id: 1,
    detected_at: '2026-10-08T01:00:00Z',
    anomaly_type: DRIFT_ANOMALY_TYPE,
    severity: 'low',
    resolved: false,
    ...over,
  }
}

function resp(over: Partial<ModelIntegrityDriftResponse> = {}): ModelIntegrityDriftResponse {
  return {
    events: [ev()],
    count: 1,
    days: 7,
    ...over,
  }
}

function okResp(): void {
  fetchMock.mockResolvedValueOnce(jsonResponse(resp()))
}

// ═════════════════════════════════════════════════════════════════════════════
// URL 与参数
// ═════════════════════════════════════════════════════════════════════════════

describe('URL 与参数', () => {
  it('★ 无参数 ⇒ 裸路径', async () => {
    okResp()
    await fetchModelIntegrityFingerprintDrift()
    expect(lastUrl()).toBe('/api/admin/model-integrity/fingerprint-drift')
  })

  it('★ days=7 与 limit=200 都发得出去', async () => {
    okResp()
    await fetchModelIntegrityFingerprintDrift({ days: 7, limit: 200 })
    expect(lastUrl()).toContain('days=7')
    expect(lastUrl()).toContain('limit=200')
  })

  it('★ days=30 上界发得出去', async () => {
    expect(driftDaysIsSendable(30)).toBe(true)
  })

  it('★ days=31 不发（越界静默回落 7，不是 400）', () => {
    expect(driftDaysIsSendable(31)).toBe(false)
  })

  it('★ days=0 不发（后端 days <= 0 回落）', () => {
    expect(driftDaysIsSendable(0)).toBe(false)
  })

  it('★ days=1 下界发得出去', () => {
    expect(driftDaysIsSendable(1)).toBe(true)
  })

  it('★ days=-5 不发', () => {
    expect(driftDaysIsSendable(-5)).toBe(false)
  })

  it('★ days=NaN 不发', () => {
    expect(driftDaysIsSendable(Number.NaN)).toBe(false)
  })

  it('★ days=Infinity 不发', () => {
    expect(driftDaysIsSendable(Number.POSITIVE_INFINITY)).toBe(false)
  })

  it('★ days=undefined / null 不发', () => {
    expect(driftDaysIsSendable(undefined)).toBe(false)
    expect(driftDaysIsSendable(null)).toBe(false)
  })

  it('★ days=7.9 截断成 7 后发出', async () => {
    okResp()
    await fetchModelIntegrityFingerprintDrift({ days: 7.9 })
    expect(lastUrl()).toContain('days=7')
    expect(lastUrl()).not.toContain('7.9')
  })

  it('★ limit=500 上界发得出去', () => {
    expect(driftLimitIsSendable(500)).toBe(true)
  })

  it('★ limit=501 不发（越界静默回落 200）', () => {
    expect(driftLimitIsSendable(501)).toBe(false)
  })

  it('★ limit=0 不发', () => {
    expect(driftLimitIsSendable(0)).toBe(false)
  })

  it('★ limit=1 下界发得出去', () => {
    expect(driftLimitIsSendable(1)).toBe(true)
  })

  it('★ limit=-5 不发', () => {
    expect(driftLimitIsSendable(-5)).toBe(false)
  })

  it('★ limit=200 发得出去', () => {
    expect(driftLimitIsSendable(200)).toBe(true)
  })

  it('★ limit=undefined 不发', () => {
    expect(driftLimitIsSendable(undefined)).toBe(false)
  })

  it('后端常量：days 缺省 7 / 上界 30 / limit 缺省 200 / 上界 500', () => {
    expect(DRIFT_DAYS_DEFAULT).toBe(7)
    expect(DRIFT_DAYS_MAX).toBe(30)
    expect(DRIFT_LIMIT_DEFAULT).toBe(200)
    expect(DRIFT_LIMIT_MAX).toBe(500)
  })

  it('后端常量：硬编码的 anomaly_type 是 fingerprint_drift', () => {
    expect(DRIFT_ANOMALY_TYPE).toBe('fingerprint_drift')
  })

  it('后端常量：severity 四值域（依据是代码，不是 DB CHECK）', () => {
    expect([...DRIFT_SEVERITIES]).toEqual(['low', 'medium', 'high', 'critical'])
  })

  it('后端常量：五恒在键 / 十四条件键 / 两数字条件键', () => {
    expect([...DRIFT_RECORD_ALWAYS_KEYS]).toEqual([
      'id',
      'detected_at',
      'anomaly_type',
      'severity',
      'resolved',
    ])
    expect(DRIFT_RECORD_OPTIONAL_KEYS).toHaveLength(14)
    expect([...DRIFT_RECORD_NUMERIC_OPTIONAL_KEYS]).toEqual(['provider_id', 'credential_id'])
    expect([...DRIFT_KEYS]).toEqual(['events', 'count', 'days'])
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 形状校验
// ═════════════════════════════════════════════════════════════════════════════

describe('形状校验', () => {
  it('★ 响应不是裸对象 ⇒ 抛错', () => {
    expect(() => unwrapModelIntegrityDrift(null)).toThrow(
      /指纹漂移 响应形状不符：期望裸对象，实得 null/,
    )
  })

  it('★ 响应是数组 ⇒ 抛错（点明实得 array）', () => {
    expect(() => unwrapModelIntegrityDrift([])).toThrow(/实得 array/)
  })

  it('★ 顶层缺 events ⇒ 抛错并点名 events', () => {
    const d = { ...resp() } as Record<string, unknown>
    delete d['events']
    expect(() => unwrapModelIntegrityDrift(d)).toThrow(/缺 1 个键（events）/)
  })

  it('★ 顶层缺 count ⇒ 抛错并点名 count', () => {
    const d = { ...resp() } as Record<string, unknown>
    delete d['count']
    expect(() => unwrapModelIntegrityDrift(d)).toThrow(/缺 1 个键（count）/)
  })

  it('★ 顶层缺 days ⇒ 抛错并点名 days', () => {
    const d = { ...resp() } as Record<string, unknown>
    delete d['days']
    expect(() => unwrapModelIntegrityDrift(d)).toThrow(/缺 1 个键（days）/)
  })

  it('★ events 是空数组是合法的（后端 make(…, 0)）', () => {
    expect(unwrapModelIntegrityDrift(resp({ events: [], count: 0 })).events).toEqual([])
  })

  it('★ events 是 null ⇒ 抛错（不是 []，是 null）', () => {
    expect(() =>
      unwrapModelIntegrityDrift(resp({ events: null as unknown as ModelIntegrityDriftRecord[] })),
    ).toThrow(/events 不是数组/)
  })

  it('★ count 不是数字 ⇒ 抛错点名 count', () => {
    expect(() =>
      unwrapModelIntegrityDrift(resp({ count: '1' as unknown as number })),
    ).toThrow(/的 count 不是数字/)
  })

  it('★ days 不是数字 ⇒ 抛错点名 days', () => {
    expect(() => unwrapModelIntegrityDrift(resp({ days: '7' as unknown as number }))).toThrow(
      /的 days 不是数字/,
    )
  })
})

describe('行形状校验：五恒在键', () => {
  it('★ 行缺 id ⇒ 抛错并点名 id', () => {
    const e = { ...ev() } as Record<string, unknown>
    delete e['id']
    expect(() =>
      unwrapModelIntegrityDrift(resp({ events: [e as unknown as ModelIntegrityDriftRecord] })),
    ).toThrow(/events\[0\] 缺 1 个键（id）/)
  })

  it('★ 行缺 detected_at ⇒ 抛错并点名 detected_at', () => {
    const e = { ...ev() } as Record<string, unknown>
    delete e['detected_at']
    expect(() =>
      unwrapModelIntegrityDrift(resp({ events: [e as unknown as ModelIntegrityDriftRecord] })),
    ).toThrow(/缺 1 个键（detected_at）/)
  })

  it('★ 行缺 resolved ⇒ 抛错并点名 resolved', () => {
    const e = { ...ev() } as Record<string, unknown>
    delete e['resolved']
    expect(() =>
      unwrapModelIntegrityDrift(resp({ events: [e as unknown as ModelIntegrityDriftRecord] })),
    ).toThrow(/缺 1 个键（resolved）/)
  })

  it('★ id 是字符串 ⇒ 抛错点名 id（类型检查那一支）', () => {
    expect(() =>
      unwrapModelIntegrityDrift(resp({ events: [{ ...ev(), id: '1024' as unknown as number }] })),
    ).toThrow(/events\[0\] 的 id 不是数字/)
  })

  it('★ resolved 是数字 0 ⇒ 抛错点名 resolved（类型检查那一支）', () => {
    expect(() =>
      unwrapModelIntegrityDrift(resp({ events: [{ ...ev(), resolved: 0 as unknown as boolean }] })),
    ).toThrow(/events\[0\] 的 resolved 不是布尔/)
  })

  it('★ severity 是数字 ⇒ 抛错点名 severity', () => {
    expect(() =>
      unwrapModelIntegrityDrift(resp({ events: [{ ...ev(), severity: 3 as unknown as string }] })),
    ).toThrow(/events\[0\] 的 severity 不是字符串/)
  })

  it('★ 第二行错类型时报出下标 1', () => {
    expect(() =>
      unwrapModelIntegrityDrift(
        resp({ events: [ev(), { ...ev(), id: 'x' as unknown as number }] }),
      ),
    ).toThrow(/events\[1\] 的 id 不是数字/)
  })
})

describe('行形状校验：★ anomaly_type 是硬编码常量可校验取值', () => {
  it('★ anomaly_type 不是 fingerprint_drift ⇒ 抛错并点明实得值', () => {
    expect(() =>
      unwrapModelIntegrityDrift(resp({ events: [{ ...ev(), anomaly_type: 'model_mismatch' }] })),
    ).toThrow(/events\[0\] 的 anomaly_type 不是 fingerprint_drift（实得 model_mismatch）/)
  })

  it('★ 第二行的 anomaly_type 不对也要抓（不是只看第一行）', () => {
    expect(() =>
      unwrapModelIntegrityDrift(
        resp({ events: [ev(), { ...ev(), id: 2, anomaly_type: 'finish_refusal' }] }),
      ),
    ).toThrow(/events\[1\] 的 anomaly_type 不是 fingerprint_drift/)
  })

  it('★ 正常值通过', () => {
    expect(unwrapModelIntegrityDrift(resp()).events[0]!.anomaly_type).toBe('fingerprint_drift')
  })
})

describe('行形状校验：十四个条件键', () => {
  it('★ 十四个条件键全缺是合法的（DB 里全是 NULL）', () => {
    const r = unwrapModelIntegrityDrift(resp({ events: [bare()], count: 1 }))
    expect('provider_id' in r.events[0]!).toBe(false)
    expect('tenant_id' in r.events[0]!).toBe(false)
  })

  it('★ provider_id 是字符串 ⇒ 抛错点名 provider_id', () => {
    expect(() =>
      unwrapModelIntegrityDrift(
        resp({ events: [{ ...ev(), provider_id: '7' as unknown as number }] }),
      ),
    ).toThrow(/events\[0\] 的 provider_id 不是数字/)
  })

  it('★ credential_id 是字符串 ⇒ 抛错点名 credential_id', () => {
    expect(() =>
      unwrapModelIntegrityDrift(
        resp({ events: [{ ...ev(), credential_id: '16' as unknown as number }] }),
      ),
    ).toThrow(/events\[0\] 的 credential_id 不是数字/)
  })

  it('★ provider_id 是字符串 "0" ⇒ 抛错（0 本身是合法的，字符串不是）', () => {
    expect(() =>
      unwrapModelIntegrityDrift(
        resp({ events: [{ ...ev(), provider_id: '0' as unknown as number }] }),
      ),
    ).toThrow(/provider_id 不是数字/)
  })

  it('★ tenant_id 是数字 ⇒ 抛错点名 tenant_id', () => {
    expect(() =>
      unwrapModelIntegrityDrift(
        resp({ events: [{ ...ev(), tenant_id: 3 as unknown as string }] }),
      ),
    ).toThrow(/events\[0\] 的 tenant_id 不是字符串/)
  })

  it('★ request_id 是数字 ⇒ 抛错点名 request_id', () => {
    expect(() =>
      unwrapModelIntegrityDrift(resp({ events: [{ ...ev(), request_id: 9 as unknown as string }] })),
    ).toThrow(/request_id 不是字符串/)
  })

  it('★ resolved_at 是数字 ⇒ 抛错点名 resolved_at', () => {
    expect(() =>
      unwrapModelIntegrityDrift(
        resp({ events: [{ ...ev(), resolved_at: 1757000000 as unknown as string }] }),
      ),
    ).toThrow(/resolved_at 不是字符串/)
  })

  it('★ context 是字符串时**不抛**（它是 any，不按对象校验）', () => {
    const r = unwrapModelIntegrityDrift(resp({ events: [{ ...ev(), context: 'fp-1' }] }))
    expect(r.events[0]!.context).toBe('fp-1')
  })

  it('★ context 是数字时也不抛（any 就是要容忍）', () => {
    const r = unwrapModelIntegrityDrift(resp({ events: [{ ...ev(), context: 7 }] }))
    expect(r.events[0]!.context).toBe(7)
  })

  it('★ context 是 null 时通过（键在但值为 null 也不是形状错误）', () => {
    const r = unwrapModelIntegrityDrift(resp({ events: [{ ...ev(), context: null }] }))
    expect('context' in r.events[0]!).toBe(true)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (2)：指针型数字键的「0 是合法值」
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★★ (2) provider_id / credential_id 的 0 是合法值', () => {
  it('★ 键在且值为 0 ⇒ 判「有这个值」', () => {
    expect(driftHasNumericValue({ ...ev(), provider_id: 0 }, 'provider_id')).toBe(true)
  })

  it('★ 键缺失 ⇒ 判「没有这个值」（DB 里是 NULL）', () => {
    expect(driftHasNumericValue(bare(), 'provider_id')).toBe(false)
  })

  it('★ 键在且值为 0 ⇒ 是零值', () => {
    expect(driftNumericValueIsZero({ ...ev(), provider_id: 0 }, 'provider_id')).toBe(true)
  })

  it('★ 键在且值为 7 ⇒ 不是零值', () => {
    expect(driftNumericValueIsZero({ ...ev(), provider_id: 7 }, 'provider_id')).toBe(false)
  })

  it('★ 只有 provider_id=0 而 credential_id 键缺失 ⇒ 零值判定只认 provider_id', () => {
    // ★ 漏掉 `key in row` 那一项后，缺失键的 `undefined === 0` 仍是 false ⇒
    //   这一格两种实现相同。要打掉它，得让**另一个**键也是 0 而本键缺失。
    const row = { ...ev(), provider_id: 0, credential_id: 0 } as ModelIntegrityDriftRecord
    delete (row as unknown as Record<string, unknown>)['provider_id']
    expect(driftHasNumericValue(row, 'provider_id')).toBe(false)
    expect(driftNumericValueIsZero(row, 'provider_id')).toBe(false)
    expect(driftNumericValueIsZero(row, 'credential_id')).toBe(true)
  })

  it('★ 键缺失 ⇒ 既不是「有值」也不是「零值」', () => {
    const row = bare()
    expect(driftHasNumericValue(row, 'provider_id')).toBe(false)
    expect(driftNumericValueIsZero(row, 'provider_id')).toBe(false)
  })

  it('★ credential_id 同理：0 是合法值', () => {
    expect(driftHasNumericValue({ ...ev(), credential_id: 0 }, 'credential_id')).toBe(true)
    expect(driftNumericValueIsZero({ ...ev(), credential_id: 0 }, 'credential_id')).toBe(true)
  })

  it('★ 两个键互不影响：provider_id=0 时 credential_id 仍可缺失', () => {
    const row = { ...ev(), provider_id: 0 } as ModelIntegrityDriftRecord
    delete (row as unknown as Record<string, unknown>)['credential_id']
    expect(driftHasNumericValue(row, 'provider_id')).toBe(true)
    expect(driftHasNumericValue(row, 'credential_id')).toBe(false)
  })

  it('★★ 陷阱对照：真值判断会把真实的 0 当成「没有」', () => {
    const row = { ...ev(), provider_id: 0 }
    // 这就是不能用 `!!row.provider_id` 的理由：它返回 false，而值明明存在
    expect(Boolean(row.provider_id)).toBe(false)
    expect(driftHasNumericValue(row, 'provider_id')).toBe(true)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (1)：context 是 any + omitempty
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★ (1) context 键在时可能是空对象', () => {
  it('★ context 键缺 ⇒ 判「没有 context」', () => {
    expect(driftHasContextKey(bare())).toBe(false)
  })

  it('★ context 是有内容的对象 ⇒ 键在且不空', () => {
    const row = ev()
    expect(driftHasContextKey(row)).toBe(true)
    expect(driftContextIsEmpty(row)).toBe(false)
  })

  it('★ context 是空对象 {} ⇒ 键在但为空', () => {
    expect(driftContextIsEmpty({ ...ev(), context: {} })).toBe(true)
  })

  it('★ context 是空数组 [] ⇒ 键在但为空', () => {
    expect(driftContextIsEmpty({ ...ev(), context: [] })).toBe(true)
  })

  it('★ context 是非空数组 ⇒ 不算空', () => {
    expect(driftContextIsEmpty({ ...ev(), context: [1] })).toBe(false)
  })

  it('★ context 是空对象时键判定仍为真（真值判断也真 ⇒ 这一格区分不开）', () => {
    // 记录这一格：!!({}) 与 ({}) in row 都是 true ⇒ 它不能单独打掉 #35，
    // 真正的牙在下面「context 为 null」那条。
    expect(driftHasContextKey({ ...ev(), context: {} })).toBe(true)
  })

  it('★ context 是 null ⇒ 键在但真值判断为假', () => {
    // ★ 这条才是「把 in 换成 !!」能打掉的：键在（Go 的接口非 nil）但 JS 里为 null。
    const row = { ...ev(), context: null }
    expect(driftHasContextKey(row)).toBe(true)
    expect(Boolean(row.context)).toBe(false)
  })

  it('★ context 键缺 ⇒ 空对象判定返回 false（不是「空」）', () => {
    expect(driftContextIsEmpty(bare())).toBe(false)
  })

  it('★ context 是空数组 ⇒ 空对象判定成立（只有数组分支能打掉它）', () => {
    // ★ 对象分支对 [] 返回 false（typeof [] === 'object' 但 Array.isArray 先判）
    expect(driftContextIsEmpty({ ...ev(), context: [] })).toBe(true)
  })

  it('★ context 是字符串 ⇒ 不算空（any 容忍）', () => {
    expect(driftContextIsEmpty({ ...ev(), context: 'x' })).toBe(false)
  })

  it('★ context 键缺时的空判定：漏掉键缺守卫会让它去读 undefined', () => {
    // ★ 漏掉 `if (!('context' in row)) return false` 之后，
    //   row.context 是 undefined ⇒ 落进最后一行 return false ⇒ 输出仍是 false ⇒
    //   这一格区分不开；真正的牙是「键缺 + 有值的行」那一条。
    expect(driftContextIsEmpty(bare())).toBe(false)
    expect(bare().context).toBeUndefined()
  })

  it('★ context 键缺但行本身有值 ⇒ 仍不判空', () => {
    expect(driftContextIsEmpty(bare({ id: 42, severity: 'high' }))).toBe(false)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (3)：跨租户
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★ (3) 读路径跨全部租户', () => {
  it('★ 有行带 tenant_id ⇒ 判「含他租户记录」', () => {
    expect(driftHasForeignTenants(resp())).toBe(true)
  })

  it('★ 全部行都没有 tenant_id ⇒ 不判（反向）', () => {
    expect(driftHasForeignTenants(resp({ events: [bare()] }))).toBe(false)
  })

  it('★ 混合：一行带一行不带 ⇒ 判含他租户', () => {
    expect(driftHasForeignTenants(resp({ events: [bare(), ev()], count: 2 }))).toBe(true)
  })

  it('★ tenant_id 是空串 ⇒ 键在也算「有租户标记」', () => {
    // ★ 真值判断会把空串吞掉 ⇒ 这一格才是它的牙。
    expect(driftHasForeignTenants(resp({ events: [{ ...ev(), tenant_id: '' }] }))).toBe(true)
  })

  it('★ 空窗口 ⇒ 两个判据都不成立', () => {
    const empty = resp({ events: [], count: 0 })
    expect(driftHasForeignTenants(empty)).toBe(false)
    expect(driftHasNoTenantTags(empty)).toBe(false)
  })

  it('★ 有行但一条 tenant_id 都没有 ⇒ 「无租户标记」', () => {
    expect(driftHasNoTenantTags(resp({ events: [bare(), bare({ id: 2 })] }))).toBe(true)
  })

  it('★ 有行且带 tenant_id ⇒ 不判「无租户标记」（反向）', () => {
    expect(driftHasNoTenantTags(resp())).toBe(false)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (5)：severity 四值域
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★ (5) severity 的四值域只有代码依据', () => {
  it('★ low 已知', () => {
    expect(driftSeverityIsKnown({ ...ev(), severity: 'low' })).toBe(true)
  })

  it('★ medium 已知', () => {
    expect(driftSeverityIsKnown({ ...ev(), severity: 'medium' })).toBe(true)
  })

  it('★ high 已知', () => {
    expect(driftSeverityIsKnown({ ...ev(), severity: 'high' })).toBe(true)
  })

  it('★ critical 已知', () => {
    expect(driftSeverityIsKnown({ ...ev(), severity: 'critical' })).toBe(true)
  })

  it('★ 空串未知（DB NOT NULL 但可以写空串）', () => {
    expect(driftSeverityIsUnknown({ ...ev(), severity: '' })).toBe(true)
  })

  it('★ warn 未知（optimizer.go 用的是另一套 severity）', () => {
    expect(driftSeverityIsUnknown({ ...ev(), severity: 'warn' })).toBe(true)
  })

  it('★ HIGH 大写未知（大小写敏感）', () => {
    expect(driftSeverityIsUnknown({ ...ev(), severity: 'HIGH' })).toBe(true)
  })

  it('★ 两个判据互为反面', () => {
    expect(driftSeverityIsKnown({ ...ev(), severity: 'low' })).toBe(
      !driftSeverityIsUnknown({ ...ev(), severity: 'low' }),
    )
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据：处置痕迹（两个方向都不能单独断言）
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★ 处置痕迹：两个方向都得覆盖', () => {
  it('★ resolved=true 且有 resolved_at ⇒ 有痕迹', () => {
    expect(
      driftHasResolutionTrace({ ...ev(), resolved: true, resolved_at: '2026-10-08T03:00:00Z' }),
    ).toBe(true)
  })

  it('★ resolved=true 但没有 resolved_at ⇒ 仍有痕迹（另一个方向）', () => {
    expect(driftHasResolutionTrace({ ...ev(), resolved: true })).toBe(true)
  })

  it('★ resolved=false 但有 resolved_at ⇒ 仍有痕迹（DB 没有约束保证两者同步）', () => {
    expect(
      driftHasResolutionTrace({ ...ev(), resolved: false, resolved_at: '2026-10-08T03:00:00Z' }),
    ).toBe(true)
  })

  it('★ resolved=false 且无痕迹 ⇒ 没有处置', () => {
    expect(driftHasResolutionTrace(bare())).toBe(false)
  })

  it('★ 完整形态：三个键都齐', () => {
    expect(
      driftHasFullResolution({
        ...ev(),
        resolved: true,
        resolved_at: '2026-10-08T03:00:00Z',
        resolution_notes: '已确认供应商改模型',
      }),
    ).toBe(true)
  })

  it('★ 缺 resolution_notes ⇒ 不算完整形态', () => {
    expect(
      driftHasFullResolution({ ...ev(), resolved: true, resolved_at: '2026-10-08T03:00:00Z' }),
    ).toBe(false)
  })

  it('★ 未处置的精确口径：resolved=false 且两个痕迹键都缺', () => {
    expect(driftIsUnresolved(bare())).toBe(true)
  })

  it('★ 有 resolved_at ⇒ 不判未处置（反向）', () => {
    expect(
      driftIsUnresolved({ ...ev(), resolved: false, resolved_at: '2026-10-08T03:00:00Z' }),
    ).toBe(false)
  })

  it('★ resolved=true ⇒ 不判未处置（反向）', () => {
    expect(driftIsUnresolved({ ...ev(), resolved: true })).toBe(false)
  })

  it('★ 没有 resolved_at 但有 resolution_notes ⇒ 也不判未处置', () => {
    // ★ 这条才是「漏掉 resolution_notes」能打掉的：
    //   只有 resolved_at 那一条时两种实现都 false ⇒ 指不到牙。
    expect(driftIsUnresolved({ ...ev(), resolved: false, resolution_notes: '待跟进' })).toBe(false)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (7)(8)：窗口回显 / count / 截断
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★ (7) days 回显生效窗口', () => {
  it('★ 请求 7 拿到 7 ⇒ 没被改写', () => {
    expect(driftDaysWasRewritten(resp(), 7)).toBe(false)
  })

  it('★ 请求 31 拿到 7 ⇒ 被改写（后端静默回落）', () => {
    expect(driftDaysWasRewritten(resp({ days: 7 }), 31)).toBe(true)
  })

  it('★ 请求 0 拿到 7 ⇒ 被改写', () => {
    expect(driftDaysWasRewritten(resp(), 0)).toBe(true)
  })

  it('★ 没传 requestedDays ⇒ 不判', () => {
    expect(driftDaysWasRewritten(resp())).toBe(false)
    expect(driftDaysWasRewritten(resp(), undefined)).toBe(false)
  })

  it('★ 请求 30 拿到 7 ⇒ 被改写（窗口与请求完全不符）', () => {
    expect(driftDaysWasRewritten(resp({ days: 7 }), 30)).toBe(true)
  })
})

describe('★★★ (8) count 恒等于 events 长度', () => {
  it('★ 一行：count=1', () => {
    expect(driftCountMatchesEvents(resp())).toBe(true)
  })

  it('★ 三行：count=3', () => {
    expect(
      driftCountMatchesEvents(
        resp({ events: [ev(), ev({ id: 2 }), ev({ id: 3 })], count: 3 }),
      ),
    ).toBe(true)
  })

  it('★ count 对不上 ⇒ 判 false', () => {
    expect(driftCountMatchesEvents(resp({ count: 99 }))).toBe(false)
  })

  it('★ 零行窗口：count 必为 0', () => {
    expect(driftIsEmptyWindow(resp({ events: [], count: 0 }))).toBe(true)
  })

  it('★ 有行但 count=0 ⇒ 不是空窗口', () => {
    expect(driftIsEmptyWindow(resp({ count: 0 }))).toBe(false)
  })

  it('★ 有行但 count=5（大于行数）⇒ 也不是空窗口', () => {
    expect(driftIsEmptyWindow(resp({ count: 5 }))).toBe(false)
  })

  it('★ 无行但 count=3（不可能但形状合法）⇒ 也不是空窗口', () => {
    expect(driftIsEmptyWindow(resp({ events: [], count: 3 }))).toBe(false)
  })
})

describe('★★★ (8) 截断判定（响应不回显 limit）', () => {
  it('★ 拿满缺省上限 200 ⇒ 判可能截断', () => {
    const r = resp({ events: Array.from({ length: 200 }, (_, i) => ev({ id: i + 1 })), count: 200 })
    expect(modelIntegrityDriftAtCap(r)).toBe(true)
  })

  it('★ 少于缺省上限 ⇒ 不判', () => {
    expect(modelIntegrityDriftAtCap(resp())).toBe(false)
  })

  it('★ 按请求的 limit 判（切到 500 后 200 行不算截断）', () => {
    const r = resp({ events: Array.from({ length: 200 }, (_, i) => ev({ id: i + 1 })), count: 200 })
    expect(modelIntegrityDriftAtCap(r, 500)).toBe(false)
    expect(modelIntegrityDriftAtCap(r, 200)).toBe(true)
  })

  it('★ limit 非法（0）⇒ 不判（没有基准）', () => {
    expect(modelIntegrityDriftAtCap(resp(), 0)).toBe(false)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 排序
// ═════════════════════════════════════════════════════════════════════════════

describe('排序（ORDER BY ts DESC）', () => {
  it('★ detected_at 非增 ⇒ true', () => {
    const r = resp({
      events: [
        ev({ id: 1, detected_at: '2026-10-08T03:00:00Z' }),
        ev({ id: 2, detected_at: '2026-10-08T02:00:00Z' }),
        ev({ id: 3, detected_at: '2026-10-08T01:00:00Z' }),
      ],
      count: 3,
    })
    expect(driftIsNewestFirst(r)).toBe(true)
  })

  it('★ 同时刻两行 ⇒ 不判「严格递减」也算通过', () => {
    const r = resp({
      events: [
        ev({ id: 1, detected_at: '2026-10-08T02:00:00Z' }),
        ev({ id: 2, detected_at: '2026-10-08T02:00:00Z' }),
      ],
      count: 2,
    })
    expect(driftIsNewestFirst(r)).toBe(true)
  })

  it('★ 升序 ⇒ 判 false', () => {
    const r = resp({
      events: [
        ev({ id: 1, detected_at: '2026-10-08T01:00:00Z' }),
        ev({ id: 2, detected_at: '2026-10-08T03:00:00Z' }),
      ],
      count: 2,
    })
    expect(driftIsNewestFirst(r)).toBe(false)
  })

  it('★ 单行 ⇒ 非增（反向：没有第二行可比）', () => {
    expect(driftIsNewestFirst(resp())).toBe(true)
  })

  it('★ 全部是 fingerprint_drift ⇒ true', () => {
    expect(driftEventsAreAllFingerprintDrift(resp())).toBe(true)
  })

  it('★ 空窗口 ⇒ 全部成立的判断也成立（空集恒真）', () => {
    expect(driftEventsAreAllFingerprintDrift(resp({ events: [], count: 0 }))).toBe(true)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// fetch 端到端
// ═════════════════════════════════════════════════════════════════════════════

describe('fetch 端到端', () => {
  it('★ 正常响应被解包器放行', async () => {
    okResp()
    const r = await fetchModelIntegrityFingerprintDrift()
    expect(r.days).toBe(7)
    expect(r.events).toHaveLength(1)
    expect(driftCountMatchesEvents(r)).toBe(true)
  })

  it('★ 形状不符 ⇒ 抛错（不是把坏数据交给 UI）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ nope: true }))
    await expect(fetchModelIntegrityFingerprintDrift()).rejects.toThrow(/指纹漂移 缺/)
  })

  it('★ anomaly_type 不对 ⇒ 抛错点名该行下标', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        events: [ev(), { ...ev(), id: 2, anomaly_type: 'model_mismatch' }],
        count: 2,
        days: 7,
      }),
    )
    await expect(fetchModelIntegrityFingerprintDrift()).rejects.toThrow(
      /events\[1\] 的 anomaly_type 不是 fingerprint_drift/,
    )
  })

  it('★ 缺 id 的行 ⇒ 抛错点名该行下标', async () => {
    const e = { ...ev() } as Record<string, unknown>
    delete e['id']
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ events: [e], count: 1, days: 7 }),
    )
    await expect(fetchModelIntegrityFingerprintDrift()).rejects.toThrow(/events\[0\] 缺 1 个键（id）/)
  })

  it('★ days 与 limit 都合法时 URL 带两个参数', async () => {
    okResp()
    await fetchModelIntegrityFingerprintDrift({ days: 30, limit: 500 })
    expect(lastUrl()).toContain('days=30')
    expect(lastUrl()).toContain('limit=500')
  })

  it('★ 越界参数不发（后端静默回落，客户端不参与）', async () => {
    okResp()
    await fetchModelIntegrityFingerprintDrift({ days: 31, limit: 501 })
    expect(lastUrl()).not.toContain('days=')
    expect(lastUrl()).not.toContain('limit=')
  })
})