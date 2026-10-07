import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { req } from '@/api/client'
import {
  MISSING_REQUEST_ID_MESSAGE,
  LIVE_ACTIONS_NOT_WIRED_MESSAGE,
  LIVE_ACTIONS_UNAVAILABLE_MESSAGE,
  LIVE_ACTIONS_REDIS_KEY,
  LIVE_ACTIONS_REDIS_MAX_LEN,
  LIVE_ACTIONS_SCAN_TIMEOUT_MS,
  ACTION_DETAIL_KEY,
  ACTION_CREDENTIAL_LABEL_KEY,
  ACTION_ALWAYS_KEYS,
  ACTION_OPTIONAL_KEYS,
  REQUEST_ACTIONS_KEYS,
  KNOWN_ACTIONS,
  GO_ZERO_TIME,
  fetchRequestActions,
  unwrapRequestActions,
  requestActionsMatchesRequest,
  requestActionsCountAgrees,
  requestActionsHasDetailKey,
  requestActionsHasCredentialLabel,
  actionHasZeroTs,
  requestActionsHasZeroTs,
  actionIsKnown,
  requestActionsHasUnknownAction,
  actionExtraKeys,
  requestActionsSortKeysPresent,
  requestActionsSorted,
  requestActionsAscending,
  requestActionsSeqUnique,
  requestActionsIsShortLivedReplay,
  requestActionsUnavailableReason,
  requestActionsIsMissingId,
} from '@/api/requestActions'
import type { RequestActionsResponse } from '@/api/requestActions'

/**
 * 请求动作时间线的不变量（2026-10-07，第七十四批）。
 *
 * ★ 本族最该被钉住的：
 *   ① ★★★★★ **actions[i] 的键集不可穷举** —— `detail` 的键被摊平到顶层
 *      （live_stream_lifecycle.go:107-114），且提升时**不覆盖**已有顶层键。
 *      这是本仓第一次出现「载荷形状开放」的端点。
 *   ② ★★★★★ **`detail` 键永远不出现**（:108 delete 掉了）。
 *   ③ ★★★★★ **`action` 是开放字符串**：后端只判 `!= ""`，不校验枚举
 *      （request_actions.go:90-92）⇒ 解包器绝不能校验枚举。
 *   ④ ★★★★ **`count` 是去重后的条数**（按 seq 去重，:65-68），不是分页元信息。
 *   ⑤ ★★★★ **短期回放不是历史**：LRange 扫整个 LTRIM 5000 的队列。
 *   ⑥ ★★★★ **失败编码有 502**（Redis 读失败），本仓少见的网关错误码。
 *   ⑦ ★★★ **`credential_label` 永远不出现**（labels 传的是 nil，:69）。
 *
 * 夹具纪律：数字与字面量一律硬写后端源码值（5000 / 2000 / 四个恒在键 /
 * 若干 action 取值 / 零值时间），不用被测常量造数据。
 */

vi.mock('@/api/client', () => ({ req: vi.fn() }))

const reqMock = req as unknown as ReturnType<typeof vi.fn>

beforeEach(() => {
  reqMock.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

function ok(payload: unknown): void {
  reqMock.mockResolvedValue(payload)
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 夹具：liveactions.ActionEvent（:98-109）+ flattenActionEvent 的摊平结果
 * ═══════════════════════════════════════════════════════════════════════════ */

/** 只有四个恒在键的条目（所有条件键零值 / detail 为空）。 */
function entry(
  seq: number,
  action: string,
  ts: string,
  over: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    request_id: 'req-abc',
    seq,
    action,
    ts,
    ...over,
  }
}

/**
 * 构造**故意非法**的条目载荷（字段类型错、键缺…）。
 * ★ 不能靠 `entry(seq, action, ts)` 传错类型来造 —— 那个签名有严格类型，
 *   TS 会先在测试代码上报错（`vue-tsc` rc≠0），而 `vitest` 不做类型检查，
 *   两道门的结果会不一致。
 */
function entryRaw(o: Record<string, unknown>): Record<string, unknown> {
  return o
}

/** 带 `ActionEvent` 条件键的条目。 */
function entryFull(seq: number, action: string, ts: string): Record<string, unknown> {
  return entry(seq, action, ts, {
    model: 'gpt-4o',
    credential_id: 42,
    error_kind: 'timeout',
    retry_seq: 1,
    retry: true,
  })
}

/**
 * ★ 带 `detail` 摊平结果的条目：`detail` 本身**不存在**，
 * 它的键被提升到顶层（`live_stream_lifecycle.go:107-114`）。
 */
function entryWithDetail(
  seq: number,
  action: string,
  ts: string,
  detail: Record<string, unknown>,
  over: Record<string, unknown> = {},
): Record<string, unknown> {
  const m = entry(seq, action, ts, over)
  for (const [k, v] of Object.entries(detail)) {
    if (!(k in m)) m[k] = v
  }
  return m
}

/** admin/request_actions.go:77-81。 */
function actionsOk(
  actions: Record<string, unknown>[],
  over: Partial<RequestActionsResponse> = {},
): Record<string, unknown> {
  return {
    request_id: 'req-abc',
    actions,
    count: actions.length,
    ...over,
  }
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 常量对齐后端字面量
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('常量对齐后端字面量', () => {
  it('Redis 键名是 llmgw:live:actions', () => {
    // internal/liveactions/liveactions.go:115
    expect(LIVE_ACTIONS_REDIS_KEY).toBe('llmgw:live:actions')
  })

  it('队列上界是 5000', () => {
    // :117 RedisMaxLen = 5000
    expect(LIVE_ACTIONS_REDIS_MAX_LEN).toBe(5000)
  })

  it('扫描超时是 2 秒', () => {
    // admin/request_actions.go:29
    expect(LIVE_ACTIONS_SCAN_TIMEOUT_MS).toBe(2000)
  })

  it('三条 message 各有原文', () => {
    expect(MISSING_REQUEST_ID_MESSAGE).toBe('missing request id')
    expect(LIVE_ACTIONS_NOT_WIRED_MESSAGE).toBe('live actions store not wired')
    expect(LIVE_ACTIONS_UNAVAILABLE_MESSAGE).toBe('live actions store unavailable')
  })

  it('恒在键恰好四个', () => {
    // ActionEvent 里不带 omitempty 的四个
    expect([...ACTION_ALWAYS_KEYS].sort()).toEqual(['action', 'request_id', 'seq', 'ts'])
  })

  it('条件键五个且与恒在键不相交', () => {
    expect([...ACTION_OPTIONAL_KEYS].sort()).toEqual([
      'credential_id', 'error_kind', 'model', 'retry', 'retry_seq',
    ])
    const always = new Set<string>(ACTION_ALWAYS_KEYS)
    expect(ACTION_OPTIONAL_KEYS.filter((k) => always.has(k))).toEqual([])
  })

  it('容器键恰好三个', () => {
    expect([...REQUEST_ACTIONS_KEYS].sort()).toEqual(['actions', 'count', 'request_id'])
  })

  it('detail 键名就是 detail', () => {
    expect(ACTION_DETAIL_KEY).toBe('detail')
  })

  it('credential_label 键名是下划线形式', () => {
    expect(ACTION_CREDENTIAL_LABEL_KEY).toBe('credential_label')
  })

  it('已知 action 取值里包含 no_route', () => {
    // liveactions.go:74 —— 但这只是展示分组，不是封闭枚举
    expect(KNOWN_ACTIONS).toContain('no_route')
    expect(KNOWN_ACTIONS).toContain('route_resolved')
  })

  it('Go 零值时间是 0001-01-01T00:00:00Z', () => {
    expect(GO_ZERO_TIME).toBe('0001-01-01T00:00:00Z')
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ①③ 解包：开放形状 + 开放字符串
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('解包：开放形状', () => {
  it('只有四个恒在键的条目通过', () => {
    const r = unwrapRequestActions(actionsOk([entry(1, 'arrive', '2026-10-07T10:00:00Z')]))
    expect(r.actions).toHaveLength(1)
  })

  it('★★ 带 detail 摊平出来的任意键时通过', () => {
    // ★★ 键集不可穷举 ⇒ 不能对全部键做 requireKeys
    const r = unwrapRequestActions(
      actionsOk([entryWithDetail(1, 'node_switch', '2026-10-07T10:00:00Z', {
        from_credential: 7, to_credential: 9, switch_reason: 'circuit_open',
      })]),
    )
    expect(actionExtraKeys(r.actions[0]!)).toEqual([
      'from_credential', 'switch_reason', 'to_credential',
    ])
  })

  it('条件键全在时通过', () => {
    expect(unwrapRequestActions(actionsOk([entryFull(1, 'reply', '2026-10-07T10:00:00Z')])).actions)
      .toHaveLength(1)
  })

  it('空动作数组通过', () => {
    // ★ make(...,0,16) 恒非 nil
    expect(unwrapRequestActions(actionsOk([])).actions).toEqual([])
  })

  it('★★ action 是未知取值时通过', () => {
    // ★★ 后端只判 != ""（:90-92）⇒ 校验枚举会误报
    const r = unwrapRequestActions(actionsOk([entry(1, 'brand_new_action', '2026-10-07T10:00:00Z')]))
    expect(r.actions[0]!.action).toBe('brand_new_action')
  })

  it('action 为空串时抛错', () => {
    expect(() => unwrapRequestActions(actionsOk([entry(1, '', '2026-10-07T10:00:00Z')]))).toThrow(
      /action 不是非空字符串/,
    )
  })

  it('action 不是字符串时抛错', () => {
    expect(() =>
      unwrapRequestActions(
        actionsOk([entryRaw({ request_id: 'req-abc', seq: 1, action: 7, ts: '2026-10-07T10:00:00Z' })]),
      ),
    ).toThrow(/action 不是非空字符串/)
  })

  it('缺 action 时抛错', () => {
    const e = entry(1, 'arrive', '2026-10-07T10:00:00Z')
    delete e.action
    expect(() => unwrapRequestActions(actionsOk([e]))).toThrow(/action/)
  })

  it('缺 seq 时抛错', () => {
    const e = entry(1, 'arrive', '2026-10-07T10:00:00Z')
    delete e.seq
    expect(() => unwrapRequestActions(actionsOk([e]))).toThrow(/seq/)
  })

  it('缺 ts 时抛错', () => {
    const e = entry(1, 'arrive', '2026-10-07T10:00:00Z')
    delete e.ts
    expect(() => unwrapRequestActions(actionsOk([e]))).toThrow(/ts/)
  })

  it('缺条目级 request_id 时抛错', () => {
    const e = entry(1, 'arrive', '2026-10-07T10:00:00Z')
    delete e.request_id
    expect(() => unwrapRequestActions(actionsOk([e]))).toThrow(/request_id/)
  })

  it('ts 不是字符串时抛错', () => {
    // ★ 夹具是「键在但类型错」——与「删键」用例走的是**不同的**检查分支
    expect(() => unwrapRequestActions(
        actionsOk([entryRaw({ request_id: 'req-abc', seq: 1, action: 'arrive', ts: 7 })]),
      )).toThrow(
      /ts 不是字符串/,
    )
  })

  it('容器 request_id 不是字符串时抛错', () => {
    // ★ 同上：容器的类型检查与条目级的缺键检查是两条分支
    expect(() => unwrapRequestActions(actionsOk([], { request_id: 7 as never }))).toThrow(
      /request_id 不是字符串/,
    )
  })

  it('两个恒在键一起缺失时报出两个键名', () => {
    // ★ 夹具只缺两个键 ⇒ 只查第一个键的实现在这里会露馅
    const e = entry(1, 'arrive', '2026-10-07T10:00:00Z')
    delete e.seq
    delete e.ts
    expect(() => unwrapRequestActions(actionsOk([e]))).toThrow(/seq, ts/)
  })

  it('seq 不是数字时抛错', () => {
    expect(() => unwrapRequestActions(
        actionsOk([entryRaw({ request_id: 'req-abc', seq: 'x', action: 'arrive', ts: '2026-10-07T10:00:00Z' })]),
      ))
      .toThrow(/seq 不是数字/)
  })

  it('条目不是对象时抛错', () => {
    expect(() => unwrapRequestActions(actionsOk(['x'] as never))).toThrow(
      /actions\[0\] 不是对象/,
    )
  })

  it('count 与 actions 长度不一致时抛错', () => {
    // ★ :80 的 count = len(actions) ⇒ 不等说明载荷被换过
    expect(() => unwrapRequestActions(actionsOk([], { count: 3 }))).toThrow(/不一致/)
  })

  it('actions 不是数组时抛错', () => {
    // ★ 不能用 actionsOk(null)：它内部算 actions.length，会在夹具里先抛
    //   TypeError，根本走不到解包器 ⇒ 测的其实不是被测代码。
    expect(() =>
      unwrapRequestActions({ request_id: 'req-abc', actions: null, count: 0 }),
    ).toThrow(/actions 不是数组/)
  })

  it('actions 是对象时抛错', () => {
    expect(() =>
      unwrapRequestActions({ request_id: 'req-abc', actions: {}, count: 0 }),
    ).toThrow(/actions 不是数组/)
  })

  it('缺 count 时抛错', () => {
    const d = actionsOk([])
    delete d.count
    expect(() => unwrapRequestActions(d)).toThrow(/缺 1 个键/)
  })

  it('数组形状抛错', () => {
    expect(() => unwrapRequestActions([])).toThrow(/形状不符/)
  })

  it('null 响应抛错', () => {
    expect(() => unwrapRequestActions(null)).toThrow(/形状不符/)
  })

  it('dashboardapi 信封形状抛错', () => {
    expect(() => unwrapRequestActions({ success: true, timestamp: 1 })).toThrow(/信封/)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ②⑦ detail 与 credential_label
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('不该出现的键', () => {
  it('正常载荷里没有 detail 键', () => {
    // ★ :108 的 delete(m, "detail") ⇒ 键本身消失
    const r = unwrapRequestActions(
      actionsOk([entryWithDetail(1, 'node_switch', '2026-10-07T10:00:00Z', { from_credential: 7 })]),
    )
    expect(requestActionsHasDetailKey(r)).toBe(false)
  })

  it('正常载荷里没有 credential_label 键', () => {
    // ★ labels 传的是 nil（:69）⇒ 永远不会有
    const r = unwrapRequestActions(actionsOk([entry(1, 'arrive', '2026-10-07T10:00:00Z')]))
    expect(requestActionsHasCredentialLabel(r)).toBe(false)
  })

  it('契约漂了冒出 detail 键时能被检出', () => {
    const r = unwrapRequestActions(
      actionsOk([entry(1, 'arrive', '2026-10-07T10:00:00Z', { detail: { a: 1 } })]),
    )
    expect(requestActionsHasDetailKey(r)).toBe(true)
  })

  it('契约漂了冒出 credential_label 时能被检出', () => {
    const r = unwrapRequestActions(
      actionsOk([entry(1, 'arrive', '2026-10-07T10:00:00Z', { credential_label: 'ok-gpt4o' })]),
    )
    expect(requestActionsHasCredentialLabel(r)).toBe(true)
  })

  it('已知条件键不算额外键', () => {
    const r = unwrapRequestActions(actionsOk([entryFull(1, 'reply', '2026-10-07T10:00:00Z')]))
    expect(actionExtraKeys(r.actions[0]!)).toEqual([])
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ③ 未知取值
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('未知 action 取值', () => {
  it('已知取值判定为已知', () => {
    const r = unwrapRequestActions(actionsOk([entry(1, 'no_route', '2026-10-07T10:00:00Z')]))
    expect(actionIsKnown(r.actions[0]!)).toBe(true)
  })

  it('未知取值判定为未知', () => {
    const r = unwrapRequestActions(actionsOk([entry(1, 'brand_new', '2026-10-07T10:00:00Z')]))
    expect(actionIsKnown(r.actions[0]!)).toBe(false)
  })

  it('存在未知取值时能被检出', () => {
    const r = unwrapRequestActions(
      actionsOk([
        entry(1, 'arrive', '2026-10-07T10:00:00Z'),
        entry(2, 'brand_new', '2026-10-07T10:01:00Z'),
      ]),
    )
    expect(requestActionsHasUnknownAction(r)).toBe(true)
  })

  it('全已知时判定为无未知取值', () => {
    const r = unwrapRequestActions(
      actionsOk([
        entry(1, 'arrive', '2026-10-07T10:00:00Z'),
        entry(2, 'reply', '2026-10-07T10:01:00Z'),
      ]),
    )
    expect(requestActionsHasUnknownAction(r)).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ④⑤⑦ 计数 / 排序 / 短期回放
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('计数与排序', () => {
  it('count 与长度自洽', () => {
    const r = unwrapRequestActions(actionsOk([entry(1, 'arrive', '2026-10-07T10:00:00Z')]))
    expect(requestActionsCountAgrees(r)).toBe(true)
  })

  it('request_id 与请求一致时判定成立', () => {
    const r = unwrapRequestActions(actionsOk([]))
    expect(requestActionsMatchesRequest(r, 'req-abc')).toBe(true)
  })

  it('request_id 与请求不符时判定不成立', () => {
    // ★ :78 是原样回显 ⇒ 不符就说明串号了
    const r = unwrapRequestActions(actionsOk([], { request_id: 'req-xyz' }))
    expect(requestActionsMatchesRequest(r, 'req-abc')).toBe(false)
  })

  it('ts 与 seq 都在时排序键齐全', () => {
    const r = unwrapRequestActions(actionsOk([entry(1, 'arrive', '2026-10-07T10:00:00Z')]))
    expect(requestActionsSortKeysPresent(r)).toBe(true)
  })

  it('★ ts 不是字符串时排序键不齐全', () => {
    // ★★ 负控：上面那条单独放就是恒真，必须配这一条
    //   （解包器会拦 ts 非字符串 ⇒ 这里必须绕过解包器直接构造）
    const bad = {
      request_id: 'req-abc',
      count: 1,
      actions: [{ request_id: 'req-abc', seq: 1, action: 'arrive', ts: 7 }],
    } as unknown as RequestActionsResponse
    expect(requestActionsSortKeysPresent(bad)).toBe(false)
  })

  it('★ seq 不是数字时排序键不齐全', () => {
    const bad = {
      request_id: 'req-abc',
      count: 1,
      actions: [{ request_id: 'req-abc', seq: 'x', action: 'arrive', ts: '2026-10-07T10:00:00Z' }],
    } as unknown as RequestActionsResponse
    expect(requestActionsSortKeysPresent(bad)).toBe(false)
  })

  it('按 ts 升序排好', () => {
    const r = unwrapRequestActions(
      actionsOk([
        entry(2, 'reply', '2026-10-07T12:00:00Z'),
        entry(1, 'arrive', '2026-10-07T08:00:00Z'),
      ]),
    )
    expect(requestActionsSorted(r).map((a) => a.seq)).toEqual([1, 2])
  })

  it('★ ts 相同时按 seq 排', () => {
    // ★ 夹具只差「ts 相同」一个条件
    const r = unwrapRequestActions(
      actionsOk([
        entry(2, 'b', '2026-10-07T10:00:00Z'),
        entry(1, 'a', '2026-10-07T10:00:00Z'),
      ]),
    )
    expect(requestActionsSorted(r).map((a) => a.seq)).toEqual([1, 2])
  })

  it('排序不改动原数组', () => {
    const r = unwrapRequestActions(
      actionsOk([
        entry(2, 'reply', '2026-10-07T12:00:00Z'),
        entry(1, 'arrive', '2026-10-07T08:00:00Z'),
      ]),
    )
    requestActionsSorted(r)
    expect(r.actions[0]!.seq).toBe(2)
  })

  it('升序排列时判定成立', () => {
    const r = unwrapRequestActions(
      actionsOk([
        entry(1, 'arrive', '2026-10-07T08:00:00Z'),
        entry(2, 'reply', '2026-10-07T12:00:00Z'),
      ]),
    )
    expect(requestActionsAscending(r)).toBe(true)
  })

  it('★ ts 相同但 seq 倒退时判定不成立', () => {
    // ★ 夹具只差「ts 相同」一个条件 ⇒ 只比 ts 的实现在这里会露馅
    const r = unwrapRequestActions(
      actionsOk([
        entry(2, 'b', '2026-10-07T10:00:00Z'),
        entry(1, 'a', '2026-10-07T10:00:00Z'),
      ]),
    )
    expect(requestActionsAscending(r)).toBe(false)
  })

  it('ts 倒退时判定不成立', () => {
    const r = unwrapRequestActions(
      actionsOk([
        entry(1, 'arrive', '2026-10-07T12:00:00Z'),
        entry(2, 'reply', '2026-10-07T08:00:00Z'),
      ]),
    )
    expect(requestActionsAscending(r)).toBe(false)
  })

  it('空数组满足升序', () => {
    expect(requestActionsAscending(unwrapRequestActions(actionsOk([])))).toBe(true)
  })

  it('★ 零值 ts 条目判定为有零值时间', () => {
    // ★ Go 零值时间会排到最前
    const r = unwrapRequestActions(actionsOk([entry(1, 'arrive', GO_ZERO_TIME)]))
    expect(actionHasZeroTs(r.actions[0]!)).toBe(true)
    expect(requestActionsHasZeroTs(r)).toBe(true)
  })

  it('真实时间判定为没有零值时间', () => {
    const r = unwrapRequestActions(actionsOk([entry(1, 'arrive', '2026-10-07T10:00:00Z')]))
    expect(requestActionsHasZeroTs(r)).toBe(false)
  })

  it('seq 唯一时判定成立', () => {
    // ★ :65-68 按 seq 去重 ⇒ 正常响应里应当唯一
    const r = unwrapRequestActions(
      actionsOk([
        entry(1, 'a', '2026-10-07T10:00:00Z'),
        entry(2, 'b', '2026-10-07T10:01:00Z'),
      ]),
    )
    expect(requestActionsSeqUnique(r)).toBe(true)
  })

  it('seq 重复时判定不成立', () => {
    const r = unwrapRequestActions(
      actionsOk([
        entry(1, 'a', '2026-10-07T10:00:00Z'),
        entry(1, 'b', '2026-10-07T10:01:00Z'),
      ]),
    )
    expect(requestActionsSeqUnique(r)).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ⑤⑥ 失败编码
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('失败编码区分', () => {
  it('503 配 not wired 被识别', () => {
    expect(requestActionsUnavailableReason(503, LIVE_ACTIONS_NOT_WIRED_MESSAGE)).toBe('not_wired')
  })

  it('★ 502 配 store unavailable 被识别', () => {
    // ★ 本仓少见的 502
    expect(requestActionsUnavailableReason(502, LIVE_ACTIONS_UNAVAILABLE_MESSAGE)).toBe(
      'store_unavailable',
    )
  })

  it('★★ 两种不可用不被混为一谈', () => {
    expect(requestActionsUnavailableReason(503, LIVE_ACTIONS_NOT_WIRED_MESSAGE)).not.toBe(
      requestActionsUnavailableReason(502, LIVE_ACTIONS_UNAVAILABLE_MESSAGE),
    )
  })

  it('503 配错 message 判为不可识别', () => {
    expect(requestActionsUnavailableReason(503, LIVE_ACTIONS_UNAVAILABLE_MESSAGE)).toBeNull()
  })

  it('502 配错 message 判为不可识别', () => {
    expect(requestActionsUnavailableReason(502, LIVE_ACTIONS_NOT_WIRED_MESSAGE)).toBeNull()
  })

  it('400 配 missing request id 被识别', () => {
    expect(requestActionsIsMissingId(400, MISSING_REQUEST_ID_MESSAGE)).toBe(true)
  })

  it('500 不判为缺 id', () => {
    expect(requestActionsIsMissingId(500, MISSING_REQUEST_ID_MESSAGE)).toBe(false)
  })

  it('本端点恒为短期回放', () => {
    // ★ LTRIM 5000 的有界队列 ⇒ 更老的事件已被丢弃
    expect(requestActionsIsShortLivedReplay()).toBe(true)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * fetch
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('fetch 打到正确 URL', () => {
  it('把 request_id 放进路径段并转义', async () => {
    ok(actionsOk([]))
    await fetchRequestActions('req/abc')
    expect(reqMock.mock.calls[0]![1]).toBe('/api/admin/requests/req%2Fabc/actions')
  })

  it('fetch 会过解包器', async () => {
    ok(actionsOk([entry(1, '', '2026-10-07T10:00:00Z')]))
    await expect(fetchRequestActions('req-abc')).rejects.toThrow(/action 不是非空字符串/)
  })

  it('fetch 会过计数一致性检查', async () => {
    ok(actionsOk([], { count: 5 }))
    await expect(fetchRequestActions('req-abc')).rejects.toThrow(/不一致/)
  })
})
