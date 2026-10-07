import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchModelHistory,
  unwrapModelHistory,
  unwrapModelHistoryEvent,
  modelHistoryCountMatchesEvents,
  modelHistoryAutoEventHasNullActorAndReason,
  modelHistoryManualEventHasNullProbeFields,
  modelHistoryEventMatchesItsSource,
  modelHistoryAutoEventKindIsKnown,
  modelHistoryManualEventKindIsKnown,
  modelHistoryEventsAreNonAscending,
  modelHistoryLimitIsInRange,
  modelHistoryCredentialIdIsRejected,
  MODEL_HISTORY_PATH,
  MODEL_HISTORY_DEFAULT_LIMIT,
  MODEL_HISTORY_LIMIT_MIN,
  MODEL_HISTORY_LIMIT_MAX,
  MODEL_HISTORY_ERROR_DETAILS,
  MODEL_HISTORY_SOURCES,
  MODEL_HISTORY_AUTO_EVENTS,
  MODEL_HISTORY_MANUAL_EVENTS,
  MODEL_HISTORY_EVENT_KEYS,
  MODEL_HISTORY_AUTOFILLED_KEYS,
  MODEL_HISTORY_RESPONSE_KEYS,
  type ModelHistoryEvent,
  type ModelHistoryResponse,
} from './modelHistory'

/**
 * 凭据模型状态变更历史的契约测试（2026-10-08，第九十二批）。
 *
 * 后端：`admin/credential_monitor.go:153-163`（`mux.HandleFunc` 在另一个文件的方法里，
 * 第五种注册形态）+ `:1304-1318` + `:1329-1430` + `:1433-1478`，
 * 由 `admin/handler.go:1453` 用 **`h.admin`** 挂载。
 *
 * 重点是源文件头写明的十四件事 (1)…(14)。带 ★ 的自校验判据都能被变异打掉。
 * ⚠️ 全部用例标题**字面量**写（不用 `it.each`）—— 变异 harness 靠标题取锚点。
 * ⚠️ 所有「逐个都要检查」的循环都刻意用**字面量数组**，不用被测常量。
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

/** 删键造「键缺」—— ★ 必须用 `delete`，`undefined` 会造出显式 undefined 键。 */
function del(obj: Record<string, unknown>, key: string): Record<string, unknown> {
  const c = { ...obj }
  delete c[key]
  return c
}

// ── 夹具：逐字照抄 `admin/credential_monitor.go:1332-1372` 两段 CTE ──

/** auto 段（`auto_events` CTE）：actor / reason 写死 `NULL::text`。 */
function autoEventOf(over: Partial<ModelHistoryEvent> = {}): ModelHistoryEvent {
  return {
    ts: '2026-10-07T08:00:00Z',
    source: 'auto',
    triggered_by: 'scheduler',
    event: 'broke',
    probe_status: 'broken_confirmed',
    http_status: 503,
    error_code: 'upstream_unavailable',
    error_message: 'connection refused',
    actor: null,
    reason: null,
    ...over,
  }
}

/** manual 段（`manual_events` CTE）：triggered_by / 探针四列全写死 `NULL`。 */
function manualEventOf(over: Partial<ModelHistoryEvent> = {}): ModelHistoryEvent {
  return {
    ts: '2026-10-07T09:00:00Z',
    source: 'manual',
    triggered_by: null,
    event: 'offline',
    probe_status: null,
    http_status: null,
    error_code: null,
    error_message: null,
    actor: 'admin',
    reason: 'maintenance window',
    ...over,
  }
}

function respOf(over: Partial<ModelHistoryResponse> = {}): ModelHistoryResponse {
  const events = [autoEventOf(), manualEventOf()]
  return { credential_id: 7, raw_model_name: 'gpt-4o', events, count: events.length, ...over }
}

// ══════════════════════════════════════════════════════════════════════════
// envelope 解包（4 键，全部恒在）
// ══════════════════════════════════════════════════════════════════════════

describe('模型历史 envelope 解包', () => {
  it('★ 满配响应被放行（auto + manual 两行）', () => {
    expect(unwrapModelHistory(respOf()).count).toBe(2)
  })

  it('★ 顶层是 null ⇒ 抛', () => {
    expect(() => unwrapModelHistory(null)).toThrow(/模型历史 响应形状不符：期望裸对象，实得 null/)
  })

  it('★ ★ 顶层是数组 ⇒ 抛「实得 array」', () => {
    expect(() => unwrapModelHistory([] as never)).toThrow(/模型历史 响应形状不符：期望裸对象，实得 array/)
  })

  it('★ ★★ 四个键逐个都要检查', () => {
    for (const k of ['credential_id', 'raw_model_name', 'events', 'count']) {
      expect(() => unwrapModelHistory(del(respOf() as never, k) as never)).toThrow(
        new RegExp(`模型历史 缺 1 个键（${k}）`),
      )
    }
  })

  it('★ credential_id 不是数字 ⇒ 抛', () => {
    expect(() => unwrapModelHistory({ ...respOf(), credential_id: '7' } as never)).toThrow(
      /credential_id 不是数字/,
    )
  })

  it('★ raw_model_name 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapModelHistory({ ...respOf(), raw_model_name: 7 } as never)).toThrow(
      /raw_model_name 不是字符串/,
    )
  })

  it('★ count 不是数字 ⇒ 抛', () => {
    expect(() => unwrapModelHistory({ ...respOf(), count: '2' } as never)).toThrow(/count 不是数字/)
  })

  it('★ ★ events 不是数组 ⇒ 抛（空 events 是合法数组，但 null 不是）', () => {
    expect(() => unwrapModelHistory({ ...respOf(), events: null as never })).toThrow(/events 不是数组/)
  })

  it('★ ★★ events 是空数组 ⇒ 放行（make(…, 0) ⇒ 空时是 [] 不是 null）', () => {
    expect(unwrapModelHistory(respOf({ events: [], count: 0 })).events).toEqual([])
  })

  it('★ ★ events 项不是对象 ⇒ 抛', () => {
    expect(() => unwrapModelHistory({ ...respOf(), events: [null] as never })).toThrow(
      /events\[0\] 响应形状不符/,
    )
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (2) 事件：十个键全部恒在，其中六个可为裸 null
// ══════════════════════════════════════════════════════════════════════════

describe('(2) 事件对象的恒在键与裸 null', () => {
  it('★ 满配 auto 事件被放行', () => {
    expect(unwrapModelHistoryEvent(autoEventOf(), 'e')).toBeTruthy()
  })

  it('★ ★★ 十个键逐个都要检查（缺一个就抛）', () => {
    const keys = [
      'ts', 'source', 'triggered_by', 'event', 'probe_status',
      'http_status', 'error_code', 'error_message', 'actor', 'reason',
    ]
    for (const k of keys) {
      expect(() => unwrapModelHistoryEvent(del(autoEventOf() as never, k) as never, 'e')).toThrow(
        new RegExp(`e 缺 1 个键（${k}）`),
      )
    }
  })

  it('★ ts 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapModelHistoryEvent(autoEventOf({ ts: 1 } as never), 'e')).toThrow(/e 的 ts 不是字符串/)
  })

  it('★ source 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapModelHistoryEvent(autoEventOf({ source: 1 } as never), 'e')).toThrow(
      /e 的 source 不是字符串/,
    )
  })

  it('★ event 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapModelHistoryEvent(autoEventOf({ event: 1 } as never), 'e')).toThrow(/e 的 event 不是字符串/)
  })

  it('★ ★★ triggered_by 既不是字符串也不是 null ⇒ 抛', () => {
    expect(() => unwrapModelHistoryEvent(autoEventOf({ triggered_by: 1 } as never), 'e')).toThrow(
      /e 的 triggered_by 不是字符串也不是 null/,
    )
  })

  it('★ ★ probe_status 既不是字符串也不是 null ⇒ 抛', () => {
    expect(() => unwrapModelHistoryEvent(autoEventOf({ probe_status: 1 } as never), 'e')).toThrow(
      /e 的 probe_status 不是字符串也不是 null/,
    )
  })

  it('★ ★ error_code 既不是字符串也不是 null ⇒ 抛', () => {
    expect(() => unwrapModelHistoryEvent(autoEventOf({ error_code: 1 } as never), 'e')).toThrow(
      /e 的 error_code 不是字符串也不是 null/,
    )
  })

  it('★ ★ error_message 既不是字符串也不是 null ⇒ 抛', () => {
    expect(() => unwrapModelHistoryEvent(autoEventOf({ error_message: 1 } as never), 'e')).toThrow(
      /e 的 error_message 不是字符串也不是 null/,
    )
  })

  it('★ ★★ actor 既不是字符串也不是 null ⇒ 抛（★ 与 reason 同循环，要各自的用例）', () => {
    expect(() => unwrapModelHistoryEvent(autoEventOf({ actor: 1 } as never), 'e')).toThrow(
      /e 的 actor 不是字符串也不是 null/,
    )
  })

  it('★ ★★ reason 既不是字符串也不是 null ⇒ 抛', () => {
    expect(() => unwrapModelHistoryEvent(manualEventOf({ reason: 1 } as never), 'e')).toThrow(
      /e 的 reason 不是字符串也不是 null/,
    )
  })

  it('★ ★ http_status 既不是数字也不是 null ⇒ 抛', () => {
    expect(() => unwrapModelHistoryEvent(autoEventOf({ http_status: '503' } as never), 'e')).toThrow(
      /e 的 http_status 不是数字也不是 null/,
    )
  })

  it('★ ★★ 五个字符串指针键全为 null ⇒ 放行（manual 行的实际形状）', () => {
    const e = manualEventOf()
    expect(e.triggered_by).toBeNull()
    expect(unwrapModelHistoryEvent(e, 'e')).toBeTruthy()
  })

  it('★ ★★ http_status 为 null ⇒ 放行', () => {
    expect(unwrapModelHistoryEvent(manualEventOf(), 'e')).toBeTruthy()
  })

  it('★ ★★ 键存在且为 null ≠ 键不存在：后者必抛（★ 对称的两段）', () => {
    const withNull = manualEventOf()
    expect('actor' in (withNull as unknown as Record<string, unknown>)).toBe(true)
    expect(() => unwrapModelHistoryEvent(withNull, 'e')).not.toThrow()
    expect(() => unwrapModelHistoryEvent(del(withNull as never, 'actor') as never, 'e')).toThrow(
      /e 缺 1 个键（actor）/,
    )
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (14) count 就是 len(events)
// ══════════════════════════════════════════════════════════════════════════

describe('(14) count 与 events 互锁', () => {
  it('★ ★ count 等于 events.length ⇒ 成立', () => {
    expect(modelHistoryCountMatchesEvents(respOf())).toBe(true)
  })

  it('★ ★ count 大于 events.length ⇒ 不成立', () => {
    expect(modelHistoryCountMatchesEvents(respOf({ count: 3 }))).toBe(false)
  })

  it('★ ★ count 小于 events.length ⇒ 不成立', () => {
    expect(modelHistoryCountMatchesEvents(respOf({ count: 1 }))).toBe(false)
  })

  it('★ ★ 空 events 且 count 为 0 ⇒ 成立', () => {
    expect(modelHistoryCountMatchesEvents(respOf({ events: [], count: 0 }))).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (4) 两段数据的字段形状互补
// ══════════════════════════════════════════════════════════════════════════

describe('(4) auto 与 manual 的 null 分布互补', () => {
  it('★ ★★★ auto 行 actor 与 reason 均为 null ⇒ 成立', () => {
    expect(modelHistoryAutoEventHasNullActorAndReason(autoEventOf())).toBe(true)
  })

  it('★ ★★ auto 行 actor 非 null ⇒ 不成立', () => {
    expect(modelHistoryAutoEventHasNullActorAndReason(autoEventOf({ actor: 'admin' }))).toBe(false)
  })

  it('★ ★★ auto 行 reason 非 null ⇒ 不成立（★ 与 actor 各一条）', () => {
    expect(modelHistoryAutoEventHasNullActorAndReason(autoEventOf({ reason: 'x' }))).toBe(false)
  })

  it('★ manual 行 ⇒ 不算 auto 行（反向）', () => {
    expect(modelHistoryAutoEventHasNullActorAndReason(manualEventOf())).toBe(false)
  })

  it('★ ★★★ manual 行五个探针字段均为 null ⇒ 成立', () => {
    expect(modelHistoryManualEventHasNullProbeFields(manualEventOf())).toBe(true)
  })

  it('★ ★ manual 行 triggered_by 非 null ⇒ 不成立', () => {
    expect(modelHistoryManualEventHasNullProbeFields(manualEventOf({ triggered_by: 'x' }))).toBe(false)
  })

  it('★ ★ manual 行 probe_status 非 null ⇒ 不成立', () => {
    expect(modelHistoryManualEventHasNullProbeFields(manualEventOf({ probe_status: 'x' }))).toBe(false)
  })

  it('★ ★ manual 行 http_status 非 null ⇒ 不成立', () => {
    expect(modelHistoryManualEventHasNullProbeFields(manualEventOf({ http_status: 200 }))).toBe(false)
  })

  it('★ ★ manual 行 error_code 非 null ⇒ 不成立', () => {
    expect(modelHistoryManualEventHasNullProbeFields(manualEventOf({ error_code: 'x' }))).toBe(false)
  })

  it('★ ★ manual 行 error_message 非 null ⇒ 不成立（★ 循环体里每项各有各的用例）', () => {
    expect(modelHistoryManualEventHasNullProbeFields(manualEventOf({ error_message: 'x' }))).toBe(false)
  })

  it('★ manual 行之外 ⇒ 不算 manual 行（反向）', () => {
    expect(modelHistoryManualEventHasNullProbeFields(autoEventOf())).toBe(false)
  })

  it('★ ★★ auto 行满足整条判据 ⇒ 成立', () => {
    expect(modelHistoryEventMatchesItsSource(autoEventOf())).toBe(true)
  })

  it('★ ★★ manual 行满足整条判据 ⇒ 成立', () => {
    expect(modelHistoryEventMatchesItsSource(manualEventOf())).toBe(true)
  })

  it('★ ★★ source 不在两值域内 ⇒ 不成立（两段都不满足）', () => {
    expect(modelHistoryEventMatchesItsSource(autoEventOf({ source: 'probe' }))).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (5) event 取值随 source 变
// ══════════════════════════════════════════════════════════════════════════

describe('(5) event 取值随 source 变', () => {
  it('★ ★★ auto 的两个取值都算已知', () => {
    expect(modelHistoryAutoEventKindIsKnown(autoEventOf({ event: 'recovered' }))).toBe(true)
    expect(modelHistoryAutoEventKindIsKnown(autoEventOf({ event: 'broke' }))).toBe(true)
  })

  it('★ ★ auto 行拿 manual 的取值 ⇒ 不算已知', () => {
    expect(modelHistoryAutoEventKindIsKnown(autoEventOf({ event: 'online' }))).toBe(false)
  })

  it('★ manual 行不算 auto 行（反向）', () => {
    expect(modelHistoryAutoEventKindIsKnown(manualEventOf({ event: 'broke' }))).toBe(false)
  })

  it('★ ★★ manual 的两个取值都算已知', () => {
    expect(modelHistoryManualEventKindIsKnown(manualEventOf({ event: 'online' }))).toBe(true)
    expect(modelHistoryManualEventKindIsKnown(manualEventOf({ event: 'offline' }))).toBe(true)
  })

  it('★ ★ manual 行拿 auto 的取值 ⇒ 不算已知', () => {
    expect(modelHistoryManualEventKindIsKnown(manualEventOf({ event: 'recovered' }))).toBe(false)
  })

  it('★ auto 行不算 manual 行（反向）', () => {
    expect(modelHistoryManualEventKindIsKnown(autoEventOf({ event: 'recovered' }))).toBe(false)
  })

  it('★ ★★ 取值合法但 source 不在值域内 ⇒ 两段判据都不算已知', () => {
    const e = manualEventOf({ source: 'probe' })
    expect(modelHistoryAutoEventKindIsKnown(e)).toBe(false)
    expect(modelHistoryManualEventKindIsKnown(e)).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (6) ORDER BY ts DESC 无 tiebreak
// ══════════════════════════════════════════════════════════════════════════

describe('(6) 时间序无 tiebreak', () => {
  it('★ ★ 非升序 ⇒ 成立', () => {
    const events = [manualEventOf({ ts: '2026-10-07T09:00:00Z' }), autoEventOf({ ts: '2026-10-07T08:00:00Z' })]
    expect(modelHistoryEventsAreNonAscending(events)).toBe(true)
  })

  it('★ ★ 出现升序 ⇒ 不成立', () => {
    const events = [autoEventOf({ ts: '2026-10-07T08:00:00Z' }), manualEventOf({ ts: '2026-10-07T09:00:00Z' })]
    expect(modelHistoryEventsAreNonAscending(events)).toBe(false)
  })

  it('★ ★ 同时间戳并列 ⇒ 仍成立（无 tiebreak，不做更严的断言）', () => {
    const events = [autoEventOf({ ts: '2026-10-07T08:00:00Z' }), manualEventOf({ ts: '2026-10-07T08:00:00Z' })]
    expect(modelHistoryEventsAreNonAscending(events)).toBe(true)
  })

  it('★ ★ 空数组 ⇒ 成立', () => {
    expect(modelHistoryEventsAreNonAscending([])).toBe(true)
  })

  it('★ ★ 单行 ⇒ 成立', () => {
    expect(modelHistoryEventsAreNonAscending([autoEventOf()])).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (9)(10) 查询参数边界
// ══════════════════════════════════════════════════════════════════════════

describe('(9)(10) limit 与 credential_id 的边界', () => {
  it('★ ★ 下界 1 ⇒ 成立', () => {
    expect(modelHistoryLimitIsInRange(1)).toBe(true)
  })

  it('★ ★ 上界 200 ⇒ 成立', () => {
    expect(modelHistoryLimitIsInRange(200)).toBe(true)
  })

  it('★ ★ 0 ⇒ 不成立', () => {
    expect(modelHistoryLimitIsInRange(0)).toBe(false)
  })

  it('★ ★ 201 ⇒ 不成立', () => {
    expect(modelHistoryLimitIsInRange(201)).toBe(false)
  })

  it('★ ★★ 负数 ⇒ 不成立', () => {
    expect(modelHistoryLimitIsInRange(-1)).toBe(false)
  })

  it('★ ★ credential_id 为 0 ⇒ 判为被拒', () => {
    expect(modelHistoryCredentialIdIsRejected(0)).toBe(true)
  })

  it('★ ★★ 负数 ⇒ 不判为被拒（后端只拦 == 0，不是 < 1）', () => {
    expect(modelHistoryCredentialIdIsRejected(-1)).toBe(false)
  })

  it('★ 正数 ⇒ 不判为被拒', () => {
    expect(modelHistoryCredentialIdIsRejected(7)).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// fetch 端到端
// ══════════════════════════════════════════════════════════════════════════

describe('fetch 端到端', () => {
  it('★ ★★ 三个查询参数都被拼上', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(respOf()))
    await fetchModelHistory({ credentialId: 7, rawModelName: 'gpt-4o', limit: 20 })
    const u = String(fetchMock.mock.calls[0]![0])
    expect(u).toContain('/api/credentials/model-history?')
    expect(u).toContain('credential_id=7')
    expect(u).toContain('raw_model_name=gpt-4o')
    expect(u).toContain('limit=20')
  })

  it('★ ★ 不给 limit 时用缺省 50', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(respOf()))
    await fetchModelHistory({ credentialId: 7, rawModelName: 'gpt-4o' })
    expect(String(fetchMock.mock.calls[0]![0])).toContain('limit=50')
  })

  it('★ ★ 含斜杠与空格的 model 名被 URL 编码（否则会切错查询串）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(respOf()))
    await fetchModelHistory({ credentialId: 7, rawModelName: 'a/b c' })
    const u = String(fetchMock.mock.calls[0]![0])
    expect(u).toContain('raw_model_name=a%2Fb+c')
    expect(u).not.toContain('raw_model_name=a/b')
  })

  it('★ ★★ 端到端：事件缺一个键 ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(respOf({ events: [del(autoEventOf() as never, 'actor') as never] })))
    await expect(fetchModelHistory({ credentialId: 7, rawModelName: 'gpt-4o' })).rejects.toThrow(/缺 1 个键/)
  })

  it('★ ★★ 端到端：count 对不上也能取到（解包器不管语义，语义由判据管）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(respOf({ count: 99 })))
    const r = await fetchModelHistory({ credentialId: 7, rawModelName: 'gpt-4o' })
    expect(r.count).toBe(99)
    expect(modelHistoryCountMatchesEvents(r)).toBe(false)
  })

  it('★ ★★ 端到端：events 为空数组时被放行', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(respOf({ events: [], count: 0 })))
    await expect(fetchModelHistory({ credentialId: 7, rawModelName: 'gpt-4o' })).resolves.toMatchObject({ count: 0 })
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 常量取值
// ══════════════════════════════════════════════════════════════════════════

describe('常量取值', () => {
  it('★ 路径不带查询串', () => {
    expect(MODEL_HISTORY_PATH).toBe('/api/credentials/model-history')
  })

  it('★ ★ limit 的缺省与上下界', () => {
    expect(MODEL_HISTORY_DEFAULT_LIMIT).toBe(50)
    expect(MODEL_HISTORY_LIMIT_MIN).toBe(1)
    expect(MODEL_HISTORY_LIMIT_MAX).toBe(200)
  })

  it('★ ★★ 五条错误文案与后端逐字一致', () => {
    expect([...Object.values(MODEL_HISTORY_ERROR_DETAILS)]).toEqual([
      'method not allowed',
      'database not configured',
      'credential_id required',
      'raw_model_name required',
      'limit must be 1-200',
    ])
  })

  it('★ ★ source 两值', () => {
    expect([...MODEL_HISTORY_SOURCES]).toEqual(['auto', 'manual'])
  })

  it('★ ★★ auto 与 manual 的 event 取值各两值', () => {
    expect([...MODEL_HISTORY_AUTO_EVENTS]).toEqual(['recovered', 'broke'])
    expect([...MODEL_HISTORY_MANUAL_EVENTS]).toEqual(['online', 'offline'])
  })

  it('★ ★★ 事件十个键（★ 一个 omitempty 都没有）', () => {
    expect([...MODEL_HISTORY_EVENT_KEYS]).toEqual([
      'ts', 'source', 'triggered_by', 'event', 'probe_status',
      'http_status', 'error_code', 'error_message', 'actor', 'reason',
    ])
  })

  it('★ ★★ 五个恒随 source 变 null 的键', () => {
    expect([...MODEL_HISTORY_AUTOFILLED_KEYS]).toEqual([
      'triggered_by', 'probe_status', 'http_status', 'error_code', 'error_message',
    ])
  })

  it('★ envelope 四键', () => {
    expect([...MODEL_HISTORY_RESPONSE_KEYS]).toEqual(['credential_id', 'raw_model_name', 'events', 'count'])
  })
})
