import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchV1DataHorizon,
  unwrapV1DataHorizon,
  deriveV1GateState,
  v1HorizonIsNotFrozen,
  v1HorizonIsFrozen,
  v1HorizonIsUnknown,
  v1HorizonExactlyOneFlag,
  v1HorizonFrozenFalseOnlyWhenUnknown,
  parseV1FreezeHeader,
  v1FreezeHeaderValueFor,
  v1FreezeHeaderMatchesBody,
  v1HorizonAffectsAreKnown,
  v1HorizonAffectsCountIs,
  v1GateSourceIsFallback,
  v1GateSourceIsExplicit,
  V1_FREEZE_HEADER,
  V1_HORIZON_GATE_KEY,
  V1_HORIZON_READ_SOURCE,
  V1_GATE_SOURCES,
  V1_GATE_STATES,
  V1_GATE_SOURCE_DEFAULT,
  V1_HORIZON_AFFECTS,
  V1_HORIZON_NOTICE_KEYS,
  V1_HORIZON_ENVELOPE_KEYS,
  V1_HORIZON_KEY,
  type V1DataHorizonNotice,
  type V1DataHorizonResponse,
} from './v1DataHorizon'

/**
 * v1 数据地平线告示的契约测试（2026-10-08，第八十六批）。
 *
 * 后端：`admin/handler.go:1088`（`admin(...)` ⇒ **admin 档**）
 * → `admin/v1_freeze_notice.go`（决策纯函数 `v1GateState` 在 `:147-155`）。
 *
 * 重点是源文件头写明的十一件事 (1)…(11)。带 ★ 的自校验判据都能被变异打掉。
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

/** 删键造「键缺」—— ★ 必须用 `delete`，`undefined` 会造出显式 undefined 键。 */
function del(obj: Record<string, unknown>, key: string): Record<string, unknown> {
  const c = { ...obj }
  delete c[key]
  return c
}

// ── 夹具：逐字照抄 `admin/v1_freeze_notice.go` ──

/** `:217-224` 的 **frozen 分支**（`Frozen: true`，`Unknown` 未设 ⇒ 零值 false）。 */
function frozenNotice(): V1DataHorizonNotice {
  return {
    frozen: true,
    unknown: false,
    source: 'request_logs',
    gate_key: 'storage.request_logs_write_enabled',
    effect: '本页及基于本页数据的判断只反映停写之前的流量；新请求不再进入这张表。',
    silence: '这一类读点没有任何错误信号：接口照常 200、结果集非空、字段齐全。接口没报错不代表数据是新的。',
    affects: ['silently_frozen', 'silently_degraded_content', 'silently_empty'],
  }
}

/** `:207-215` 的 **unknown 分支**（`Frozen: false`，`Unknown: true`）。 */
function unknownNotice(): V1DataHorizonNotice {
  return {
    frozen: false,
    unknown: true,
    source: 'request_logs',
    gate_key: 'storage.request_logs_write_enabled',
    effect: '无法确认 v1 读源族的停写状态：该 gate 键在配置里没有显式取值，当前读到的值来自默认值回落。',
    silence:
      '两种可能都未经验证：写入可能仍在继续，也可能已经停写。请按 GateKey 显式配置该键，让下一次读取给出确定答案。',
    affects: ['silently_frozen', 'silently_degraded_content', 'silently_empty'],
  }
}

/** `:258-261` / `:265-268` 的两键信封。 */
function envelope(notice: V1DataHorizonNotice | null): Record<string, unknown> {
  return {
    v1_data_horizon: notice,
    '//': 'frozen=true 表示 v1 读源族当前已停更；本对象不存在即表示未停更。',
  }
}

function bodyOf(notice: V1DataHorizonNotice | null): V1DataHorizonResponse {
  return unwrapV1DataHorizon(envelope(notice))
}

// ══════════════════════════════════════════════════════════════════════════
// (1) 主键恒在，值是对象或裸 null
// ══════════════════════════════════════════════════════════════════════════

describe('(1) 两键信封与主键的两种形态', () => {
  it('★ 主键为 null ⇒ 放行（未停更；**不是**键缺）', () => {
    const r = unwrapV1DataHorizon(envelope(null))
    expect(r.v1_data_horizon).toBeNull()
  })

  it('★ ★ 主键是 frozen 对象 ⇒ 放行', () => {
    const r = unwrapV1DataHorizon(envelope(frozenNotice()))
    expect(r.v1_data_horizon).not.toBeNull()
  })

  it('★ ★ 主键是 unknown 对象 ⇒ 放行', () => {
    const r = unwrapV1DataHorizon(envelope(unknownNotice()))
    expect(r.v1_data_horizon).not.toBeNull()
  })

  it('★ ★★ 缺主键 ⇒ 抛（与「值为 null」是两回事）', () => {
    const d = del(envelope(null), V1_HORIZON_KEY)
    expect(() => unwrapV1DataHorizon(d)).toThrow(/v1 数据地平线 缺 1 个键（v1_data_horizon）/)
  })

  it('★ ★★ 缺注释键 ⇒ 抛', () => {
    const d = del(envelope(null), '//')
    expect(() => unwrapV1DataHorizon(d)).toThrow(/v1 数据地平线 缺 1 个键（\/\/）/)
  })

  it('★ ★★ 两个键都缺 ⇒ 抛并报数量与顺序', () => {
    expect(() => unwrapV1DataHorizon({ nope: 1 })).toThrow(/v1 数据地平线 缺 2 个键（v1_data_horizon, \/\/）/)
  })

  it('★ 注释键不是字符串 ⇒ 抛', () => {
    const d = envelope(null)
    d['//'] = 1
    expect(() => unwrapV1DataHorizon(d)).toThrow(/v1 数据地平线 的 \/\/ 不是字符串/)
  })

  it('★ 顶层 null ⇒ 抛并报 null', () => {
    expect(() => unwrapV1DataHorizon(null)).toThrow(/v1 数据地平线 响应形状不符：期望裸对象，实得 null/)
  })

  it('★ 顶层数组 ⇒ 抛并报 array', () => {
    expect(() => unwrapV1DataHorizon([])).toThrow(/实得 array/)
  })

  it('★ 顶层字符串 ⇒ 抛并报 string', () => {
    expect(() => unwrapV1DataHorizon('x')).toThrow(/实得 string/)
  })

  it('★ ★ 主键是数组 ⇒ 抛并点名它', () => {
    const d = envelope(null)
    d[V1_HORIZON_KEY] = []
    expect(() => unwrapV1DataHorizon(d)).toThrow(/v1_data_horizon 响应形状不符：期望裸对象，实得 array/)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (11) notice 七键校验
// ══════════════════════════════════════════════════════════════════════════

describe('(11) notice 七键校验', () => {
  it('★ 缺一个键 ⇒ 抛并点名（无 omitempty ⇒ 七键恒在）', () => {
    const n = del(frozenNotice() as unknown as Record<string, unknown>, 'silence')
    expect(() => unwrapV1DataHorizon(envelope(n as unknown as V1DataHorizonNotice))).toThrow(
      /v1_data_horizon 缺 1 个键（silence）/,
    )
  })

  it('★ ★ 缺三个键 ⇒ 抛并报数量与键名顺序', () => {
    let n = frozenNotice() as unknown as Record<string, unknown>
    n = del(n, 'frozen')
    n = del(n, 'gate_key')
    n = del(n, 'affects')
    expect(() => unwrapV1DataHorizon(envelope(n as unknown as V1DataHorizonNotice))).toThrow(
      /v1_data_horizon 缺 3 个键（frozen, gate_key, affects）/,
    )
  })

  it('★ frozen 不是布尔 ⇒ 抛', () => {
    const n = frozenNotice()
    n.frozen = 1 as unknown as boolean
    expect(() => unwrapV1DataHorizon(envelope(n))).toThrow(/的 frozen 不是布尔/)
  })

  it('★ ★ unknown 不是布尔 ⇒ 抛（它是独立键，不能因 frozen 在就放过）', () => {
    const n = unknownNotice()
    n.unknown = 'true' as unknown as boolean
    expect(() => unwrapV1DataHorizon(envelope(n))).toThrow(/的 unknown 不是布尔/)
  })

  it('★ source 不是字符串 ⇒ 抛', () => {
    const n = frozenNotice()
    n.source = 1 as unknown as string
    expect(() => unwrapV1DataHorizon(envelope(n))).toThrow(/的 source 不是字符串/)
  })

  it('★ gate_key 不是字符串 ⇒ 抛', () => {
    const n = frozenNotice()
    n.gate_key = null as unknown as string
    expect(() => unwrapV1DataHorizon(envelope(n))).toThrow(/的 gate_key 不是字符串/)
  })

  it('★ effect 不是字符串 ⇒ 抛', () => {
    const n = frozenNotice()
    n.effect = [] as unknown as string
    expect(() => unwrapV1DataHorizon(envelope(n))).toThrow(/的 effect 不是字符串/)
  })

  it('★ ★ silence 不是字符串 ⇒ 抛', () => {
    const n = unknownNotice()
    n.silence = {} as unknown as string
    expect(() => unwrapV1DataHorizon(envelope(n))).toThrow(/的 silence 不是字符串/)
  })

  it('★ affects 不是数组 ⇒ 抛', () => {
    const n = frozenNotice()
    n.affects = 'x' as unknown as string[]
    expect(() => unwrapV1DataHorizon(envelope(n))).toThrow(/的 affects 不是数组/)
  })

  it('★ ★ affects 元素不是字符串 ⇒ 抛并点名下标', () => {
    const n = frozenNotice()
    n.affects = ['silently_frozen', 1 as unknown as string]
    expect(() => unwrapV1DataHorizon(envelope(n))).toThrow(/的 affects\[1\] 不是字符串/)
  })

  it('★ affects 是空数组 ⇒ 放行', () => {
    const n = frozenNotice()
    n.affects = []
    expect(() => unwrapV1DataHorizon(envelope(n))).not.toThrow()
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (4)(5) 三态判定：逐行镜像 v1GateState
// ══════════════════════════════════════════════════════════════════════════

describe('(4)(5) 门状态三态判定', () => {
  it('★ source=db 且写门开 ⇒ live', () => {
    expect(deriveV1GateState(true, 'db')).toBe('live')
  })

  it('★ ★ source=db 且写门关 ⇒ frozen', () => {
    expect(deriveV1GateState(false, 'db')).toBe('frozen')
  })

  it('★ source=env 且写门开 ⇒ live', () => {
    expect(deriveV1GateState(true, 'env')).toBe('live')
  })

  it('★ source=env 且写门关 ⇒ frozen', () => {
    expect(deriveV1GateState(false, 'env')).toBe('frozen')
  })

  it('★ ★★★ source=default 且写门**开** ⇒ unavailable（**不是 live**）', () => {
    // ★ 这是整个文件存在的理由：读点回落成 fallback=true，
    //   若当成 live 就会把「我没读到」显示成「数据是新的」。
    expect(deriveV1GateState(true, 'default')).toBe('unavailable')
  })

  it('★ source=default 且写门关 ⇒ unavailable', () => {
    expect(deriveV1GateState(false, 'default')).toBe('unavailable')
  })

  it('★ ★★★ source 是空串且写门**开** ⇒ unavailable（空串与 default 同义）', () => {
    expect(deriveV1GateState(true, '')).toBe('unavailable')
  })

  it('★ source 是空串且写门关 ⇒ unavailable', () => {
    expect(deriveV1GateState(false, '')).toBe('unavailable')
  })

  it('★ ★ 空串与 default 在写门关时同答案（专格）', () => {
    expect(deriveV1GateState(false, '')).toBe(deriveV1GateState(false, 'default'))
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (1)(2)(3) 响应侧三态判定
// ══════════════════════════════════════════════════════════════════════════

describe('(1)(2)(3) 响应侧三态判定', () => {
  it('★ 主键为 null ⇒ IsNotFrozen 为 true', () => {
    expect(v1HorizonIsNotFrozen(bodyOf(null))).toBe(true)
  })

  it('★ ★ 主键为对象 ⇒ IsNotFrozen 为 false（反向）', () => {
    expect(v1HorizonIsNotFrozen(bodyOf(frozenNotice()))).toBe(false)
    expect(v1HorizonIsNotFrozen(bodyOf(unknownNotice()))).toBe(false)
  })

  it('★ frozen 档 ⇒ IsFrozen 为 true', () => {
    expect(v1HorizonIsFrozen(bodyOf(frozenNotice()))).toBe(true)
  })

  it('★ ★★ unknown 档 ⇒ IsFrozen 为 **false**（不能只读 frozen 字段）', () => {
    // ★ unknown 档的 frozen 也是 false；若判据只看 frozen，
    //   再加上「值为 null ⇒ 未冻结」，unknown 就会被两边都漏掉。
    expect(v1HorizonIsFrozen(bodyOf(unknownNotice()))).toBe(false)
  })

  it('★ ★ 主键为 null ⇒ IsFrozen 为 false', () => {
    expect(v1HorizonIsFrozen(bodyOf(null))).toBe(false)
  })

  it('★ ★★ 两个标志都为 true ⇒ IsFrozen 与 IsUnknown 都必须是 false', () => {
    // ★ 专格：唯一能打掉「去掉合取项」的那一格。unknown 档的 frozen 本来就 false，
    //   frozen 档的 unknown 本来就 false —— 两边都不构成区分。
    const bad = { ...frozenNotice(), frozen: true, unknown: true }
    expect(v1HorizonIsFrozen(bodyOf(bad))).toBe(false)
    expect(v1HorizonIsUnknown(bodyOf(bad))).toBe(false)
  })

  it('★ unknown 档 ⇒ IsUnknown 为 true', () => {
    expect(v1HorizonIsUnknown(bodyOf(unknownNotice()))).toBe(true)
  })

  it('★ ★ frozen 档 ⇒ IsUnknown 为 false（反向）', () => {
    expect(v1HorizonIsUnknown(bodyOf(frozenNotice()))).toBe(false)
  })

  it('★ 主键为 null ⇒ IsUnknown 为 false', () => {
    expect(v1HorizonIsUnknown(bodyOf(null))).toBe(false)
  })
})

describe('(2) frozen 与 unknown 恰好一个为 true', () => {
  it('★ 主键为 null ⇒ ExactlyOneFlag 为 true（无对象即无标志）', () => {
    expect(v1HorizonExactlyOneFlag(bodyOf(null))).toBe(true)
  })

  it('★ frozen 档（true/false）⇒ ExactlyOneFlag 为 true', () => {
    expect(v1HorizonExactlyOneFlag(bodyOf(frozenNotice()))).toBe(true)
  })

  it('★ unknown 档（false/true）⇒ ExactlyOneFlag 为 true', () => {
    expect(v1HorizonExactlyOneFlag(bodyOf(unknownNotice()))).toBe(true)
  })

  it('★ ★★ 两个都是 false ⇒ **不成立**（后端 `:264` 刻意不产出）', () => {
    // ★ 手写夹具能造出这一格；它正是「有对象但两个标志都没设」的形态，
    //   后端把那种情形返回成 null，所以响应里不该出现。
    const bad = { ...frozenNotice(), frozen: false, unknown: false }
    expect(v1HorizonExactlyOneFlag(bodyOf(bad))).toBe(false)
  })

  it('★ ★ 两个都是 true ⇒ 不成立', () => {
    const bad = { ...frozenNotice(), frozen: true, unknown: true }
    expect(v1HorizonExactlyOneFlag(bodyOf(bad))).toBe(false)
  })

  it('★ ★★ frozen=false 且 unknown=false ⇒ FrozenFalseOnlyWhenUnknown 不成立', () => {
    const bad = { ...frozenNotice(), frozen: false, unknown: false }
    expect(v1HorizonFrozenFalseOnlyWhenUnknown(bodyOf(bad))).toBe(false)
  })

  it('★ 主键为 null ⇒ FrozenFalseOnlyWhenUnknown 为 true（无对象豁免）', () => {
    expect(v1HorizonFrozenFalseOnlyWhenUnknown(bodyOf(null))).toBe(true)
  })

  it('★ frozen 档（frozen=true）⇒ FrozenFalseOnlyWhenUnknown 为 true（豁免）', () => {
    expect(v1HorizonFrozenFalseOnlyWhenUnknown(bodyOf(frozenNotice()))).toBe(true)
  })

  it('★ ★ unknown 档（frozen=false 但 unknown=true）⇒ 为 true', () => {
    expect(v1HorizonFrozenFalseOnlyWhenUnknown(bodyOf(unknownNotice()))).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (6) 响应头：三值 + 缺失
// ══════════════════════════════════════════════════════════════════════════

describe('(6) 响应头解析与交叉校验', () => {
  it('★ ★★ 头不存在 ⇒ 解析为 null（live 时后端根本不设置该头）', () => {
    expect(parseV1FreezeHeader(() => null)).toBeNull()
  })

  it('★ 头是 1 ⇒ 解析为字符串 1', () => {
    expect(parseV1FreezeHeader(() => '1')).toBe('1')
  })

  it('★ ★ 头是 unknown ⇒ 解析为 unknown（与 1 用不同值）', () => {
    expect(parseV1FreezeHeader(() => 'unknown')).toBe('unknown')
  })

  it('★ ★★ 头是其它非空值 ⇒ 归到 1（非 unknown 的一律当停更）', () => {
    expect(parseV1FreezeHeader(() => 'true')).toBe('1')
  })

  it('★ live 态 ⇒ 期望头是 null', () => {
    expect(v1FreezeHeaderValueFor('live')).toBeNull()
  })

  it('★ ★ frozen 态 ⇒ 期望头是 1', () => {
    expect(v1FreezeHeaderValueFor('frozen')).toBe('1')
  })

  it('★ ★ unavailable 态 ⇒ 期望头是 unknown', () => {
    expect(v1FreezeHeaderValueFor('unavailable')).toBe('unknown')
  })

  it('★ ★ 头 null + body 为 null ⇒ 一致', () => {
    expect(v1FreezeHeaderMatchesBody(null, bodyOf(null))).toBe(true)
  })

  it('★ 头 1 + frozen body ⇒ 一致', () => {
    expect(v1FreezeHeaderMatchesBody('1', bodyOf(frozenNotice()))).toBe(true)
  })

  it('★ 头 unknown + unknown body ⇒ 一致', () => {
    expect(v1FreezeHeaderMatchesBody('unknown', bodyOf(unknownNotice()))).toBe(true)
  })

  it('★ ★★ 头 null + frozen body ⇒ **不一致**（头缺失但 body 说停更）', () => {
    expect(v1FreezeHeaderMatchesBody(null, bodyOf(frozenNotice()))).toBe(false)
  })

  it('★ ★★ 头 1 + body 为 null ⇒ **不一致**（头说停更但 body 没告示）', () => {
    expect(v1FreezeHeaderMatchesBody('1', bodyOf(null))).toBe(false)
  })

  it('★ ★ 头 unknown + frozen body ⇒ 不一致（两套表达分叉）', () => {
    expect(v1FreezeHeaderMatchesBody('unknown', bodyOf(frozenNotice()))).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (7)(9) 取值域
// ══════════════════════════════════════════════════════════════════════════

describe('(7)(9) 取值域判据', () => {
  it('★ affects 三值都已知 ⇒ 为 true', () => {
    expect(v1HorizonAffectsAreKnown(frozenNotice())).toBe(true)
  })

  it('★ ★ affects 含未知值 ⇒ 为 false', () => {
    const n = frozenNotice()
    n.affects = ['silently_frozen', 'silently_unknown']
    expect(v1HorizonAffectsAreKnown(n)).toBe(false)
  })

  it('★ affects 是空数组 ⇒ 已知（every 恒真）', () => {
    const n = frozenNotice()
    n.affects = []
    expect(v1HorizonAffectsAreKnown(n)).toBe(true)
  })

  it('★ affects 条数是 3 ⇒ 为 true', () => {
    expect(v1HorizonAffectsCountIs(frozenNotice(), 3)).toBe(true)
  })

  it('★ ★ affects 条数不是 3 ⇒ 为 false', () => {
    const n = frozenNotice()
    n.affects = ['silently_frozen']
    expect(v1HorizonAffectsCountIs(n, 3)).toBe(false)
  })

  it('★ gate source 是 default ⇒ 是回落值', () => {
    expect(v1GateSourceIsFallback('default')).toBe(true)
  })

  it('★ ★ gate source 是空串 ⇒ 也是回落值（与 default 同义）', () => {
    expect(v1GateSourceIsFallback('')).toBe(true)
  })

  it('★ ★ gate source 是 db ⇒ 不是回落值（反向）', () => {
    expect(v1GateSourceIsFallback('db')).toBe(false)
  })

  it('★ gate source 是 db ⇒ 显式', () => {
    expect(v1GateSourceIsExplicit('db')).toBe(true)
  })

  it('★ gate source 是 env ⇒ 显式', () => {
    expect(v1GateSourceIsExplicit('env')).toBe(true)
  })

  it('★ ★ gate source 是 default ⇒ **不**显式（专格）', () => {
    expect(v1GateSourceIsExplicit('default')).toBe(false)
  })

  it('★ ★ gate source 是空串 ⇒ 不显式', () => {
    expect(v1GateSourceIsExplicit('')).toBe(false)
  })

  it('★ ★★ gate source 是**域外非空值** ⇒ 不显式（专打「改成恒真」那条变异）', () => {
    // ★ 空串两式都 false，区分不了；只有「非空但不在域里」才能区分。
    expect(v1GateSourceIsExplicit('unknown-source')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// fetch 端到端
// ══════════════════════════════════════════════════════════════════════════

describe('fetch 端到端', () => {
  it('★ 未停更响应（主键 null）被解包器放行', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope(null)))
    const r = await fetchV1DataHorizon()
    expect(v1HorizonIsNotFrozen(r)).toBe(true)
  })

  it('★ ★ frozen 响应被解包器放行且判为已停更', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope(frozenNotice())))
    const r = await fetchV1DataHorizon()
    expect(v1HorizonIsFrozen(r)).toBe(true)
  })

  it('★ ★ unknown 响应被解包器放行且判为「读不到」', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope(unknownNotice())))
    const r = await fetchV1DataHorizon()
    expect(v1HorizonIsUnknown(r)).toBe(true)
  })

  it('★ ★★ 三种响应都能通过「恰好一个标志」校验', async () => {
    for (const n of [null, frozenNotice(), unknownNotice()]) {
      fetchMock.mockResolvedValueOnce(jsonResponse(envelope(n)))
      const r = await fetchV1DataHorizon()
      expect(v1HorizonExactlyOneFlag(r)).toBe(true)
    }
  })

  it('★ ★★ 形状不符（缺主键）⇒ 抛错（**不**静默降级成「未冻结」）', async () => {
    // ★★ 这是本端点唯一真正危险的错误方向：按「没取到 = 没停更」处理，
    //   就会在停写期间把冻结当正常展示。
    fetchMock.mockResolvedValueOnce(jsonResponse({ nope: 1 }))
    await expect(fetchV1DataHorizon()).rejects.toThrow(/缺 2 个键/)
  })

  it('★ 请求路径固定且不带查询串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope(null)))
    await fetchV1DataHorizon()
    expect(String(fetchMock.mock.calls[0]![0])).not.toContain('?')
    expect(String(fetchMock.mock.calls[0]![0])).toContain('/api/admin/v1-data-horizon')
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 常量取值（逐条对齐后端字面量）
// ══════════════════════════════════════════════════════════════════════════

describe('常量取值', () => {
  it('★ V1_HORIZON_GATE_KEY === settings 里的字面量', () => {
    expect(V1_HORIZON_GATE_KEY).toBe('storage.request_logs_write_enabled')
  })

  it('★ V1_HORIZON_READ_SOURCE === 两处硬编码的读源族名', () => {
    expect(V1_HORIZON_READ_SOURCE).toBe('request_logs')
  })

  it('★ V1_FREEZE_HEADER === v1_freeze_notice.go:71 的头名', () => {
    expect(V1_FREEZE_HEADER).toBe('X-LLM-Gateway-V1-Data-Frozen')
  })

  it('★ V1_GATE_SOURCES 三值与 spec.go:283 的 source 域一致', () => {
    expect([...V1_GATE_SOURCES]).toEqual(['db', 'env', 'default'])
  })

  it('★ V1_GATE_STATES 三值与 :121-130 一致', () => {
    expect([...V1_GATE_STATES]).toEqual(['live', 'frozen', 'unavailable'])
  })

  it('★ V1_GATE_SOURCE_DEFAULT === :134 的字面量', () => {
    expect(V1_GATE_SOURCE_DEFAULT).toBe('default')
  })

  it('★ V1_HORIZON_AFFECTS 三值与两个分支的切片一致', () => {
    expect([...V1_HORIZON_AFFECTS]).toEqual(['silently_frozen', 'silently_degraded_content', 'silently_empty'])
  })

  it('★ V1_HORIZON_NOTICE_KEYS 恰是 7 个键（无 omitempty）', () => {
    expect(V1_HORIZON_NOTICE_KEYS.length).toBe(7)
  })

  it('★ V1_HORIZON_ENVELOPE_KEYS 恰是两个恒在键', () => {
    expect([...V1_HORIZON_ENVELOPE_KEYS]).toEqual(['v1_data_horizon', '//'])
  })

  it('★ ★★ 两个分支的 affects **字面量完全相同**（判据能自证）', () => {
    expect(frozenNotice().affects).toEqual(unknownNotice().affects)
  })

  it('★ ★★ 两个分支的 gate_key 与 source 也相同（只有两个布尔与两段文案不同）', () => {
    const f = frozenNotice()
    const u = unknownNotice()
    expect(f.gate_key).toBe(u.gate_key)
    expect(f.source).toBe(u.source)
    expect(f.effect).not.toBe(u.effect)
    expect(f.silence).not.toBe(u.silence)
  })
})
