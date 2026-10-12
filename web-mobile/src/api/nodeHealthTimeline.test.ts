import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { req } from '@/api/client'
import {
  NODE_TIMELINE_SINCE_DEFAULT,
  NODE_TIMELINE_SINCE_MAX,
  NODE_TIMELINE_SINCE_MAX_MS,
  NODE_TIMELINE_SINCE_MAX_DAYS,
  NODE_TIMELINE_EVENTS_CAP,
  INVALID_CREDENTIAL_ID_MESSAGE,
  DB_NOT_CONFIGURED_MESSAGE,
  TIMELINE_UNAVAILABLE_MESSAGE,
  NODE_TIMELINE_EVENT_TYPES,
  NODE_TIMELINE_FORCE_RECOVERY_TRIGGER,
  NODE_TIMELINE_REASON_NONE_SENTINEL,
  NODE_TIMELINE_OBSERVATION_STATUS,
  NODE_EVENT_ALWAYS_KEYS,
  NODE_EVENT_OPTIONAL_KEYS,
  NODE_TIMELINE_KEYS,
  nodeTimelineSinceEffective,
  nodeTimelineCredentialIdInvalid,
  nodeTimelineCredentialIdText,
  fetchNodeHealthTimeline,
  unwrapNodeRecoveryTimeline,
  nodeTimelineIsRecovered,
  nodeTimelineIsSuccess,
  nodeTimelineIsForcedRecovery,
  nodeTimelineIsFailure,
  nodeTimelineNoteModel,
  nodeTimelineNoteTrigger,
  nodeTimelineReasonOrNull,
  nodeTimelineDurationMs,
  nodeTimelineDurationUnknown,
  nodeTimelinePossiblyTruncated,
  nodeTimelineSortedByOccurredAt,
  nodeTimelineAscendingByOccurredAt,
  nodeTimelineMatchesCredential,
  nodeTimelineIsClaimingComplete,
  nodeTimelineIsEmpty,
  nodeTimelineUnavailableReason,
} from '@/api/nodeHealthTimeline'
import type {
  NodeRecoveryEvent,
  NodeRecoveryTimelineResponse,
} from '@/api/nodeHealthTimeline'

/**
 * 节点恢复时间线的不变量（2026-10-07，第七十三批）。
 *
 * ★ 本族最该被钉住的：
 *   ① ★★★★★ **完全没有租户过滤**（node_health.go:183-184 的 WHERE 只有两个条件）
 *      而注册是 admin 档 ⇒ tenant_admin 能查任意 credential_id 的完整探测记录。
 *   ② ★★★★★ **`observation_status` 硬编码 "complete"**，查询失败走 503
 *      ⇒ 「部分观测」不可表达 ⇒ 解包器只校验类型不校验取值。
 *   ③ ★★★★★ **`event_type` 三态，且「恢复」有两个值**：
 *      `recovered` 与 `reconnected` 都是成功，后者对应**强制恢复**那条路径。
 *   ④ ★★★★ **三个条件键全是「指针 + omitempty」**，填充条件各不相同：
 *      duration_ms 仅 > 0；reason_code 额外排除哨兵 "none"；note 两半都空才缺。
 *   ⑤ ★★★★ **查询失败是 503 不是 500**，且与「数据库没配」同为 503、靠 message 区分。
 *   ⑥ ★★★★ **since 按后缀分派两套语法，超上限静默 clamp 到 7 天**；缺省 24h。
 *   ⑦ ★★★ **排序键（started_at）与展示键（occurred_at）不是同一个字段**。
 *   ⑧ ★★★ **上限 200 不回显，被丢的是最早的**。
 *
 * 夹具纪律：数字与字面量一律硬写后端源码值（24h / 7d / 200 / 三个事件类型 /
 * "credential_recovery" / "none" / "complete"），不用被测常量造数据。
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
 * 夹具：admin/node_health.go:26-38 逐字抄
 * ═══════════════════════════════════════════════════════════════════════════ */

/** :26-32 `nodeRecoveryEvent`，条件键按需给出。 */
function ev(
  eventType: string,
  occurredAt: string,
  over: Partial<NodeRecoveryEvent> = {},
): Record<string, unknown> {
  return {
    credential_id: '42',
    event_type: eventType,
    occurred_at: occurredAt,
    ...over,
  }
}

/** 三个条件键全在的「满配」事件。 */
function evFull(eventType: string, occurredAt: string): Record<string, unknown> {
  return ev(eventType, occurredAt, {
    duration_ms: 128,
    reason_code: 'upstream_timeout',
    note: 'gpt-4o · scheduled',
  })
}

/** 三个条件键全缺失的事件（指针全为 nil 的真实形状）。 */
function evMinimal(eventType: string, occurredAt: string): Record<string, unknown> {
  return ev(eventType, occurredAt)
}

/** :34-38 `nodeRecoveryTimelineResponse`。 */
function tl(
  events: Record<string, unknown>[],
  over: Partial<NodeRecoveryTimelineResponse> = {},
): Record<string, unknown> {
  return {
    credential_id: '42',
    observation_status: 'complete',
    events,
    ...over,
  }
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 常量对齐后端字面量
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('常量对齐后端字面量', () => {
  it('since 缺省是 24h 不是 7d', () => {
    // node_health.go:17 nodeHealthDefaultSince = 24 * time.Hour
    expect(NODE_TIMELINE_SINCE_DEFAULT).toBe('24h')
  })

  it('since 上界是 7 天', () => {
    // node_health.go:18 nodeHealthMaxSince = 7 * 24 * time.Hour
    expect(NODE_TIMELINE_SINCE_MAX).toBe('7d')
    expect(NODE_TIMELINE_SINCE_MAX_DAYS).toBe(7)
    expect(NODE_TIMELINE_SINCE_MAX_MS).toBe(604800000)
  })

  it('事件上限是 200', () => {
    // node_health.go:21 nodeHealthTimelineCap = 200
    expect(NODE_TIMELINE_EVENTS_CAP).toBe(200)
  })

  it('三条 message 各有原文', () => {
    expect(INVALID_CREDENTIAL_ID_MESSAGE).toBe('invalid credential_id')
    expect(DB_NOT_CONFIGURED_MESSAGE).toBe('database not configured')
    expect(TIMELINE_UNAVAILABLE_MESSAGE).toBe('node-health timeline unavailable')
  })

  it('事件类型只有三个取值', () => {
    // node_health.go:82-90
    expect([...NODE_TIMELINE_EVENT_TYPES]).toEqual(['failed', 'recovered', 'reconnected'])
  })

  it('强制恢复的触发来源字面量是 credential_recovery', () => {
    expect(NODE_TIMELINE_FORCE_RECOVERY_TRIGGER).toBe('credential_recovery')
  })

  it('哨兵值是 none', () => {
    expect(NODE_TIMELINE_REASON_NONE_SENTINEL).toBe('none')
  })

  it('观测状态常量是 complete', () => {
    expect(NODE_TIMELINE_OBSERVATION_STATUS).toBe('complete')
  })

  it('事件恒在键恰好三个且与条件键不相交', () => {
    expect([...NODE_EVENT_ALWAYS_KEYS].sort()).toEqual([
      'credential_id', 'event_type', 'occurred_at',
    ])
    const always = new Set<string>(NODE_EVENT_ALWAYS_KEYS)
    expect(NODE_EVENT_OPTIONAL_KEYS.filter((k) => always.has(k))).toEqual([])
  })

  it('事件条件键恰好三个', () => {
    expect([...NODE_EVENT_OPTIONAL_KEYS].sort()).toEqual([
      'duration_ms', 'note', 'reason_code',
    ])
  })

  it('时间线容器键恰好三个', () => {
    expect([...NODE_TIMELINE_KEYS].sort()).toEqual([
      'credential_id', 'events', 'observation_status',
    ])
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ⑥ since：两套语法 + 静默 clamp
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('since 缺省与 clamp', () => {
  it('缺省回落到 24h', () => {
    expect(nodeTimelineSinceEffective()).toBe('24h')
    expect(nodeTimelineSinceEffective('')).toBe('24h')
  })

  it('30d 被夹到 7d', () => {
    // ★ 后端 parseTimelineSince 静默 clamp，不报错
    expect(nodeTimelineSinceEffective('30d')).toBe('7d')
  })

  it('7d 恰好在界上原样通过', () => {
    expect(nodeTimelineSinceEffective('7d')).toBe('7d')
  })

  it('1d 不被夹', () => {
    expect(nodeTimelineSinceEffective('1d')).toBe('1d')
  })

  it('0d 原样发出让后端 400', () => {
    // ★ 后端 days < 1 ⇒ 400，客户端不静默改
    expect(nodeTimelineSinceEffective('0d')).toBe('0d')
    expect(nodeTimelineSinceEffective('-3d')).toBe('-3d')
  })

  it('时长语法原样透传不做本地解析', () => {
    // ★ 客户端不重新实现 time.ParseDuration，避免把合法值变成 400
    expect(nodeTimelineSinceEffective('30m')).toBe('30m')
    expect(nodeTimelineSinceEffective('1h30m')).toBe('1h30m')
    expect(nodeTimelineSinceEffective('500ms')).toBe('500ms')
    expect(nodeTimelineSinceEffective('-2h')).toBe('-2h')
  })

  it('非法字符串原样发出', () => {
    expect(nodeTimelineSinceEffective('abc')).toBe('abc')
    expect(nodeTimelineSinceEffective('7')).toBe('7')
    expect(nodeTimelineSinceEffective('3 d')).toBe('3 d')
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * credential_id 必须是正整数
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('credential_id 校验', () => {
  it('正整数不算失效', () => {
    expect(nodeTimelineCredentialIdInvalid(42)).toBeNull()
    expect(nodeTimelineCredentialIdInvalid(1)).toBeNull()
  })

  it('零构成失效', () => {
    // ★ 后端判的是 credID <= 0
    expect(nodeTimelineCredentialIdInvalid(0)).not.toBeNull()
  })

  it('负数构成失效', () => {
    expect(nodeTimelineCredentialIdInvalid(-5)).not.toBeNull()
  })

  it('小数构成失效', () => {
    expect(nodeTimelineCredentialIdInvalid(1.5)).not.toBeNull()
  })

  it('非数字构成失效', () => {
    expect(nodeTimelineCredentialIdInvalid('42')).not.toBeNull()
    expect(nodeTimelineCredentialIdInvalid(undefined)).not.toBeNull()
  })

  it('响应里的 credential_id 是十进制字符串', () => {
    // ★ 后端用 strconv.FormatInt ⇒ 文本不是数字
    expect(nodeTimelineCredentialIdText(42)).toBe('42')
  })

  it('响应 credential_id 与请求一致时判定成立', () => {
    const r = unwrapNodeRecoveryTimeline(tl([evFull('recovered', '2026-10-07T10:00:00.000000000Z')]))
    expect(nodeTimelineMatchesCredential(r, 42)).toBe(true)
  })

  it('响应 credential_id 与请求不符时判定不成立', () => {
    // ★ 能抓到后端路由错位
    const r = unwrapNodeRecoveryTimeline(tl([], { credential_id: '7' }))
    expect(nodeTimelineMatchesCredential(r, 42)).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 解包
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('时间线解包', () => {
  it('满配载荷原样通过', () => {
    const r = unwrapNodeRecoveryTimeline(
      tl([evFull('recovered', '2026-10-07T10:00:00.000000000Z')]),
    )
    expect(r.events).toHaveLength(1)
    expect(r.credential_id).toBe('42')
  })

  it('三个条件键全缺失时通过', () => {
    // ★ 指针全 nil ⇒ 键缺失才是合法形状
    const r = unwrapNodeRecoveryTimeline(tl([evMinimal('failed', '2026-10-07T10:00:00Z')]))
    expect('duration_ms' in (r.events[0] as object)).toBe(false)
  })

  it('空事件数组通过', () => {
    // ★ make(...,0,32) 恒非 nil ⇒ 恒 []
    expect(unwrapNodeRecoveryTimeline(tl([])).events).toEqual([])
  })

  it('缺 observation_status 时抛错', () => {
    // ★ 收紧成「缺 N 个键」：与下面那条「不是字符串」共用 observation_status 一个词，
    //   宽松正则会让类型校验的分支把缺键的用例兜住。
    const d = tl([])
    delete d.observation_status
    expect(() => unwrapNodeRecoveryTimeline(d)).toThrow(/缺 1 个键/)
  })

  it('observation_status 不是字符串时抛错', () => {
    expect(() => unwrapNodeRecoveryTimeline(tl([], { observation_status: 1 as never }))).toThrow(
      /observation_status 不是字符串/,
    )
  })

  it('credential_id 不是字符串时抛错', () => {
    expect(() => unwrapNodeRecoveryTimeline(tl([], { credential_id: 42 as never }))).toThrow(
      /credential_id 不是字符串/,
    )
  })

  it('缺 events 时抛错', () => {
    const d = tl([])
    delete d.events
    expect(() => unwrapNodeRecoveryTimeline(d)).toThrow(/events/)
  })

  it('events 不是数组时抛错', () => {
    expect(() => unwrapNodeRecoveryTimeline(tl(null as never))).toThrow(/events 不是数组/)
  })

  it('事件项不是对象时抛错', () => {
    expect(() => unwrapNodeRecoveryTimeline(tl(['x'] as never))).toThrow(/events\[0\] 不是对象/)
  })

  it('缺 event_type 时抛错', () => {
    const d = tl([ev('failed', '2026-10-07T10:00:00Z')])
    delete (d.events as Record<string, unknown>[])[0]!.event_type
    expect(() => unwrapNodeRecoveryTimeline(d)).toThrow(/event_type/)
  })

  it('缺 occurred_at 时抛错', () => {
    const d = tl([ev('failed', '2026-10-07T10:00:00Z')])
    delete (d.events as Record<string, unknown>[])[0]!.occurred_at
    expect(() => unwrapNodeRecoveryTimeline(d)).toThrow(/occurred_at/)
  })

  it('缺 credential_id 时抛错', () => {
    const d = tl([ev('failed', '2026-10-07T10:00:00Z')])
    delete (d.events as Record<string, unknown>[])[0]!.credential_id
    expect(() => unwrapNodeRecoveryTimeline(d)).toThrow(/credential_id/)
  })

  it('两个恒在键一起缺失时报出两个键名', () => {
    // ★ 夹具只缺两个**不做取值校验**的键（event_type 有取值校验会兜住，测不出差异）
    const e = ev('failed', '2026-10-07T10:00:00Z')
    delete e.credential_id
    delete e.occurred_at
    expect(() => unwrapNodeRecoveryTimeline(tl([e]))).toThrow(/credential_id, occurred_at/)
  })

  it('event_type 出现第四个取值时抛错', () => {
    expect(() => unwrapNodeRecoveryTimeline(tl([ev('degraded', '2026-10-07T10:00:00Z')]))).toThrow(
      /event_type 不是三个合法取值/,
    )
  })

  it('三个合法取值都通过', () => {
    for (const t of ['failed', 'recovered', 'reconnected']) {
      expect(unwrapNodeRecoveryTimeline(tl([ev(t, '2026-10-07T10:00:00Z')])).events).toHaveLength(1)
    }
  })

  it('数组形状抛错', () => {
    expect(() => unwrapNodeRecoveryTimeline([])).toThrow(/形状不符/)
  })

  it('null 响应抛错', () => {
    expect(() => unwrapNodeRecoveryTimeline(null)).toThrow(/形状不符/)
  })

  it('dashboardapi 信封形状抛错', () => {
    expect(() => unwrapNodeRecoveryTimeline({ success: true, timestamp: 1 })).toThrow(/信封/)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ③ event_type 三态
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('事件类型判定', () => {
  it('failed 判为失败', () => {
    expect(nodeTimelineIsFailure(unwrapNodeRecoveryTimeline(tl([ev('failed', '2026-10-07T10:00:00Z')])).events[0]!)).toBe(true)
  })

  it('recovered 判为恢复', () => {
    expect(nodeTimelineIsRecovered(unwrapNodeRecoveryTimeline(tl([ev('recovered', '2026-10-07T10:00:00Z')])).events[0]!)).toBe(true)
  })

  it('★★ reconnected 也判为恢复', () => {
    // ★★ 漏掉它就会把「强制恢复」那一类整批丢掉
    expect(nodeTimelineIsRecovered(unwrapNodeRecoveryTimeline(tl([ev('reconnected', '2026-10-07T10:00:00Z')])).events[0]!)).toBe(true)
  })

  it('failed 不判为恢复', () => {
    expect(nodeTimelineIsRecovered(unwrapNodeRecoveryTimeline(tl([ev('failed', '2026-10-07T10:00:00Z')])).events[0]!)).toBe(false)
  })

  it('reconnected 判为强制恢复', () => {
    expect(nodeTimelineIsForcedRecovery(unwrapNodeRecoveryTimeline(tl([ev('reconnected', '2026-10-07T10:00:00Z')])).events[0]!)).toBe(true)
  })

  it('recovered 不判为强制恢复', () => {
    expect(nodeTimelineIsForcedRecovery(unwrapNodeRecoveryTimeline(tl([ev('recovered', '2026-10-07T10:00:00Z')])).events[0]!)).toBe(false)
  })

  it('reconnected 判为成功', () => {
    expect(nodeTimelineIsSuccess(unwrapNodeRecoveryTimeline(tl([ev('reconnected', '2026-10-07T10:00:00Z')])).events[0]!)).toBe(true)
  })

  it('failed 不判为成功', () => {
    expect(nodeTimelineIsSuccess(unwrapNodeRecoveryTimeline(tl([ev('failed', '2026-10-07T10:00:00Z')])).events[0]!)).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ④ 三个条件键的填充条件各不相同
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('条件键取值判定', () => {
  it('note 带分隔符时能取出模型名', () => {
    // ★ formatProbeNote 拼的是 "model · trigger"
    const e = unwrapNodeRecoveryTimeline(tl([evFull('recovered', '2026-10-07T10:00:00Z')])).events[0]!
    expect(nodeTimelineNoteModel(e)).toBe('gpt-4o')
  })

  it('note 带分隔符时能取出触发来源', () => {
    const e = unwrapNodeRecoveryTimeline(tl([evFull('recovered', '2026-10-07T10:00:00Z')])).events[0]!
    expect(nodeTimelineNoteTrigger(e)).toBe('scheduled')
  })

  it('note 只有一半时两边都返回那一半', () => {
    const e = unwrapNodeRecoveryTimeline(
      tl([ev('failed', '2026-10-07T10:00:00Z', { note: 'credential_recovery' })]),
    ).events[0]!
    expect(nodeTimelineNoteModel(e)).toBe('credential_recovery')
    expect(nodeTimelineNoteTrigger(e)).toBe('credential_recovery')
  })

  it('note 缺失时两边都返回 null', () => {
    const e = unwrapNodeRecoveryTimeline(tl([evMinimal('failed', '2026-10-07T10:00:00Z')])).events[0]!
    expect(nodeTimelineNoteModel(e)).toBeNull()
    expect(nodeTimelineNoteTrigger(e)).toBeNull()
  })

  it('reason_code 有值时返回该值', () => {
    const e = unwrapNodeRecoveryTimeline(tl([evFull('failed', '2026-10-07T10:00:00Z')])).events[0]!
    expect(nodeTimelineReasonOrNull(e)).toBe('upstream_timeout')
  })

  it('reason_code 缺失时返回 null', () => {
    const e = unwrapNodeRecoveryTimeline(tl([evMinimal('failed', '2026-10-07T10:00:00Z')])).events[0]!
    expect(nodeTimelineReasonOrNull(e)).toBeNull()
  })

  it('duration_ms 有值时返回该值', () => {
    const e = unwrapNodeRecoveryTimeline(tl([evFull('recovered', '2026-10-07T10:00:00Z')])).events[0]!
    expect(nodeTimelineDurationMs(e)).toBe(128)
  })

  it('★★ duration_ms 缺失时返回 null 而不是 0', () => {
    // ★★ 后端只在 > 0 时才设指针 ⇒ 缺失不等于 0 毫秒
    const e = unwrapNodeRecoveryTimeline(tl([evMinimal('failed', '2026-10-07T10:00:00Z')])).events[0]!
    expect(nodeTimelineDurationMs(e)).toBeNull()
  })

  it('duration_ms 缺失判定为未知', () => {
    const e = unwrapNodeRecoveryTimeline(tl([evMinimal('failed', '2026-10-07T10:00:00Z')])).events[0]!
    expect(nodeTimelineDurationUnknown(e)).toBe(true)
  })

  it('duration_ms 有值时不判定为未知', () => {
    const e = unwrapNodeRecoveryTimeline(tl([evFull('failed', '2026-10-07T10:00:00Z')])).events[0]!
    expect(nodeTimelineDurationUnknown(e)).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ② ⑦ ⑧ 空数组 / 排序 / 截断
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('空数组与截断', () => {
  it('空事件数组判定为空', () => {
    expect(nodeTimelineIsEmpty(unwrapNodeRecoveryTimeline(tl([])))).toBe(true)
  })

  it('有事件时不判定为空', () => {
    expect(nodeTimelineIsEmpty(unwrapNodeRecoveryTimeline(tl([evMinimal('failed', '2026-10-07T10:00:00Z')])))).toBe(false)
  })

  it('满 200 条时提示可能截断', () => {
    // 夹具硬写 200 条（nodeHealthTimelineCap 的字面量）
    const events = Array.from({ length: 200 }, (_, i) =>
      evMinimal('failed', `2026-10-07T10:00:${String(i % 60).padStart(2, '0')}Z`),
    )
    const r = unwrapNodeRecoveryTimeline(tl(events))
    expect(r.events).toHaveLength(200)
    expect(nodeTimelinePossiblyTruncated(r)).toBe(true)
  })

  it('199 条时判定未触上限', () => {
    // ★ 负控
    const events = Array.from({ length: 199 }, () => evMinimal('failed', '2026-10-07T10:00:00Z'))
    expect(nodeTimelinePossiblyTruncated(unwrapNodeRecoveryTimeline(tl(events)))).toBe(false)
  })

  it('按 occurred_at 升序排好', () => {
    const r = unwrapNodeRecoveryTimeline(
      tl([
        evMinimal('failed', '2026-10-07T12:00:00Z'),
        evMinimal('failed', '2026-10-07T08:00:00Z'),
      ]),
    )
    const sorted = nodeTimelineSortedByOccurredAt(r)
    expect(sorted.map((e) => e.occurred_at)).toEqual([
      '2026-10-07T08:00:00Z', '2026-10-07T12:00:00Z',
    ])
  })

  it('排序不改动原数组', () => {
    const r = unwrapNodeRecoveryTimeline(
      tl([
        evMinimal('failed', '2026-10-07T12:00:00Z'),
        evMinimal('failed', '2026-10-07T08:00:00Z'),
      ]),
    )
    nodeTimelineSortedByOccurredAt(r)
    expect(r.events[0]!.occurred_at).toBe('2026-10-07T12:00:00Z')
  })

  it('升序排列时判定成立', () => {
    const r = unwrapNodeRecoveryTimeline(
      tl([
        evMinimal('failed', '2026-10-07T08:00:00Z'),
        evMinimal('failed', '2026-10-07T12:00:00Z'),
      ]),
    )
    expect(nodeTimelineAscendingByOccurredAt(r)).toBe(true)
  })

  it('★★ 出现时间倒退时判定不成立', () => {
    // ★★ 后端按 started_at 排，occurred_at 可能用 completed_at ⇒ 可能倒退
    const r = unwrapNodeRecoveryTimeline(
      tl([
        evMinimal('failed', '2026-10-07T12:00:00Z'),
        evMinimal('failed', '2026-10-07T08:00:00Z'),
      ]),
    )
    expect(nodeTimelineAscendingByOccurredAt(r)).toBe(false)
  })

  it('★ 中段时间倒退但首尾正常时判定不成立', () => {
    // ★ 夹具只差「中段」一个条件 ⇒ 只比首尾的实现会被这条抓住
    const r = unwrapNodeRecoveryTimeline(
      tl([
        evMinimal('failed', '2026-10-07T08:00:00Z'),
        evMinimal('failed', '2026-10-07T12:00:00Z'),
        evMinimal('failed', '2026-10-07T10:00:00Z'),
      ]),
    )
    expect(nodeTimelineAscendingByOccurredAt(r)).toBe(false)
  })

  it('空数组满足升序', () => {
    expect(nodeTimelineAscendingByOccurredAt(unwrapNodeRecoveryTimeline(tl([])))).toBe(true)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ② ⑤ 观测状态与 503 的两种 message
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('观测状态与失败区分', () => {
  it('观测状态恒为 complete', () => {
    expect(nodeTimelineIsClaimingComplete(unwrapNodeRecoveryTimeline(tl([])))).toBe(true)
  })

  it('观测状态换成别的值时判定不成立', () => {
    const r = unwrapNodeRecoveryTimeline(tl([], { observation_status: 'partial' }))
    expect(nodeTimelineIsClaimingComplete(r)).toBe(false)
  })

  it('数据库没配的 message 被识别', () => {
    expect(nodeTimelineUnavailableReason(DB_NOT_CONFIGURED_MESSAGE)).toBe('db_not_configured')
  })

  it('查询失败的 message 被识别', () => {
    expect(nodeTimelineUnavailableReason(TIMELINE_UNAVAILABLE_MESSAGE)).toBe('query_failed')
  })

  it('两条 503 的 message 不被混为一谈', () => {
    // ★ 状态码相同，只能靠文案区分
    expect(nodeTimelineUnavailableReason(DB_NOT_CONFIGURED_MESSAGE)).not.toBe(
      nodeTimelineUnavailableReason(TIMELINE_UNAVAILABLE_MESSAGE),
    )
  })

  it('其它 message 判为不可识别', () => {
    expect(nodeTimelineUnavailableReason('something else')).toBeNull()
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * fetch
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('fetch 打到正确 URL', () => {
  it('缺省 since 打到 24h', async () => {
    ok(tl([]))
    await fetchNodeHealthTimeline(42)
    expect(reqMock.mock.calls[0]![1]).toBe('/api/admin/node-health/42/timeline?since=24h')
  })

  it('30d 被夹到 7d 后发出', async () => {
    ok(tl([]))
    await fetchNodeHealthTimeline(42, { since: '30d' })
    expect(reqMock.mock.calls[0]![1]).toBe('/api/admin/node-health/42/timeline?since=7d')
  })

  it('时长语法原样发出', async () => {
    ok(tl([]))
    await fetchNodeHealthTimeline(42, { since: '1h30m' })
    expect(reqMock.mock.calls[0]![1]).toBe('/api/admin/node-health/42/timeline?since=1h30m')
  })

  it('★ since 含空格时被转义，URL 不被空格撑坏', async () => {
    // ★ 夹具只差「有无空格」一个条件 ⇒ 不转义时 query 会被空格截断
    ok(tl([]))
    await fetchNodeHealthTimeline(42, { since: '1h 30m' })
    expect(reqMock.mock.calls[0]![1]).toBe('/api/admin/node-health/42/timeline?since=1h%2030m')
  })

  it('非法 since 原样发出让后端 400', async () => {
    ok(tl([]))
    await fetchNodeHealthTimeline(42, { since: 'abc' })
    expect(reqMock.mock.calls[0]![1]).toBe('/api/admin/node-health/42/timeline?since=abc')
  })

  it('fetch 会过解包器', async () => {
    const d = tl([ev('degraded', '2026-10-07T10:00:00Z')])
    ok(d)
    await expect(fetchNodeHealthTimeline(42)).rejects.toThrow(/event_type 不是三个合法取值/)
  })
})
