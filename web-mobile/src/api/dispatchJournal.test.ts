import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchDispatchJournal,
  dispatchJournalPath,
  unwrapDispatchJournalSnapshot,
  dispatchJournalTruncatedFillsWindow,
  dispatchJournalTruncatedCountMatchesFlag,
  dispatchJournalLeadingEntriesWereDropped,
  dispatchJournalEntryIsTerminal,
  dispatchJournalEndsWithTerminal,
  dispatchJournalSeqIsContiguous,
  dispatchJournalSeqStartsAtOne,
  dispatchJournalVersionMatchesLastSeq,
  dispatchJournalAttemptIsNonDecreasing,
  dispatchJournalCountsAreCumulative,
  dispatchJournalAtIsRFC3339,
  dispatchJournalOptionalNumberIsPresent,
  dispatchJournalRoleIsPrivileged,
  dispatchJournalRoleCanReadTenant,
  dispatchJournalSegmentIsAcceptable,
  dispatchJournalParamsAreAcceptable,
  dispatchJournalEmptyEntriesAreAmbiguous,
  DISPATCH_JOURNAL_PATH_PREFIX,
  DISPATCH_JOURNAL_BAD_PATH_MESSAGE,
  DISPATCH_JOURNAL_METHOD_MESSAGE,
  DISPATCH_JOURNAL_UNAVAILABLE_MESSAGE,
  DISPATCH_JOURNAL_NOT_FOUND_MESSAGE,
  DISPATCH_JOURNAL_UNAUTHORIZED_MESSAGE,
  DISPATCH_JOURNAL_ACTIONS,
  DISPATCH_JOURNAL_TERMINAL_ACTIONS,
  DISPATCH_JOURNAL_PRIVILEGED_ROLES,
  DISPATCH_JOURNAL_TENANT_ROLE,
  DISPATCH_JOURNAL_SNAPSHOT_MAX_ENTRIES,
  DISPATCH_JOURNAL_RING_CAPACITY,
  DISPATCH_JOURNAL_STORE_CAPACITY,
  DISPATCH_JOURNAL_SNAPSHOT_KEYS,
  DISPATCH_JOURNAL_ENTRY_REQUIRED_KEYS,
  DISPATCH_JOURNAL_ENTRY_OPTIONAL_KEYS,
  DISPATCH_JOURNAL_COUNTS_KEYS,
  type DispatchJournalSnapshot,
  type DispatchJournalEntry,
  type DispatchJournalCounts,
} from './dispatchJournal'

/**
 * 调度轨迹快照的契约测试（2026-10-08，第八十九批）。
 *
 * 后端：`cmd/gateway/main.go:7074-7075` 现构造 + `RegisterRoutes`（**第四种注册形态**）
 * → `admin/journal_handlers.go`（126 行）→ `domains/dispatch/journal_consumer.go`
 *    + `domains/dispatch/journal.go` + `domains/dispatch/pipeline.go:1852-1893`。
 *
 * 重点是源文件头写明的十五件事 (1)…(15)。带 ★ 的自校验判据都能被变异打掉。
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

// ── 夹具：逐字照抄 `domains/dispatch/journal.go` 的 struct ──

function countsOf(over: Partial<DispatchJournalCounts> = {}): DispatchJournalCounts {
  return { retries: 0, node_switches: 0, model_switches: 0, capacity_waits: 0, scheduled_waits: 0, ...over }
}

function entryOf(over: Partial<DispatchJournalEntry> = {}): DispatchJournalEntry {
  return {
    seq: 1,
    at: '2026-10-07T12:00:00.123456789Z',
    action: 'retry_same_cred',
    attempt: 1,
    counts: countsOf(),
    ...over,
  }
}

/** 默认快照：seq 1→3，末条 `completed`，`snapshot_version = 3`。 */
function snapOf(over: Partial<DispatchJournalSnapshot> = {}): DispatchJournalSnapshot {
  return {
    tenant_id: 'default',
    request_id: 'req-abc',
    entries: [
      entryOf({
        seq: 1,
        action: 'retry_same_cred',
        attempt: 1,
        model: 'gpt-4o',
        credential_id: 12,
        error_kind: 'upstream_5xx',
        http_status: 502,
        counts: countsOf({ retries: 1 }),
      }),
      entryOf({
        seq: 2,
        action: 'switch_model',
        attempt: 2,
        model: 'claude-sonnet',
        from_model: 'gpt-4o',
        to_model: 'claude-sonnet',
        counts: countsOf({ retries: 1, model_switches: 1 }),
      }),
      entryOf({ seq: 3, action: 'completed', attempt: 3, counts: countsOf({ retries: 1, model_switches: 1 }) }),
    ],
    truncated: false,
    truncated_count: 0,
    snapshot_version: 3,
    ...over,
  }
}

/** 造 n 条连续 seq 的条目（末条终态）。 */
function manyEntries(n: number, startSeq = 1): DispatchJournalEntry[] {
  const out: DispatchJournalEntry[] = []
  for (let i = 0; i < n; i++) {
    out.push(
      entryOf({
        seq: startSeq + i,
        attempt: i + 1,
        action: i === n - 1 ? 'completed' : 'retry_same_cred',
        counts: countsOf({ retries: i + 1 }),
      }),
    )
  }
  return out
}

// ══════════════════════════════════════════════════════════════════════════
// 快照解包
// ══════════════════════════════════════════════════════════════════════════

describe('快照解包', () => {
  it('★ 满配响应被放行（六键信封）', () => {
    expect(unwrapDispatchJournalSnapshot(snapOf())).toBeTruthy()
  })

  it('★ ★ entries 是空数组 ⇒ 放行（handler 显式把 nil 补成 [])', () => {
    expect(() => unwrapDispatchJournalSnapshot(snapOf({ entries: [] }))).not.toThrow()
  })

  it('★ 顶层不是对象 ⇒ 抛', () => {
    expect(() => unwrapDispatchJournalSnapshot([])).toThrow(
      /调度轨迹快照 响应形状不符：期望裸对象，实得 array/,
    )
  })

  // ★ 逐键钉住：★ 这里刻意用**字面量**而不是被测常量 ——
  //   用常量造夹具是自指恒真（把某个键从常量里删掉，循环就跟着少跑一次，仍然全绿）。
  it('★ ★★ 六个恒在键逐个都要检查', () => {
    for (const key of ['tenant_id', 'request_id', 'entries', 'truncated', 'truncated_count', 'snapshot_version']) {
      expect(() => unwrapDispatchJournalSnapshot(del(snapOf() as never, key))).toThrow(
        /调度轨迹快照 缺 1 个键/,
      )
    }
  })

  it('★ ★ 两个键都缺 ⇒ 抛并报数量与顺序', () => {
    const d = del(del(snapOf() as never, 'entries'), 'truncated_count')
    expect(() => unwrapDispatchJournalSnapshot(d)).toThrow(/调度轨迹快照 缺 2 个键/)
  })

  it('★ ★★ tenant_id 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapDispatchJournalSnapshot(snapOf({ tenant_id: 1 as never }))).toThrow(
      /调度轨迹快照 的 tenant_id 不是字符串/,
    )
  })

  it('★ ★ tenant_id 是空串 ⇒ 抛', () => {
    expect(() => unwrapDispatchJournalSnapshot(snapOf({ tenant_id: '' }))).toThrow(/tenant_id 是空串/)
  })

  it('★ request_id 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapDispatchJournalSnapshot(snapOf({ request_id: 1 as never }))).toThrow(
      /调度轨迹快照 的 request_id 不是字符串/,
    )
  })

  it('★ ★ request_id 是空串 ⇒ 抛', () => {
    expect(() => unwrapDispatchJournalSnapshot(snapOf({ request_id: '' }))).toThrow(/request_id 是空串/)
  })

  it('★ ★ truncated 不是布尔 ⇒ 抛', () => {
    expect(() => unwrapDispatchJournalSnapshot(snapOf({ truncated: 'false' as never }))).toThrow(
      /调度轨迹快照 的 truncated 不是布尔/,
    )
  })

  it('★ ★ truncated_count 不是数字 ⇒ 抛', () => {
    expect(() => unwrapDispatchJournalSnapshot(snapOf({ truncated_count: '0' as never }))).toThrow(
      /truncated_count 不是数字/,
    )
  })

  it('★ ★ snapshot_version 不是数字 ⇒ 抛', () => {
    expect(() => unwrapDispatchJournalSnapshot(snapOf({ snapshot_version: null as never }))).toThrow(
      /snapshot_version 不是数字/,
    )
  })

  it('★ entries 不是数组 ⇒ 抛', () => {
    expect(() => unwrapDispatchJournalSnapshot(snapOf({ entries: {} as never }))).toThrow(
      /调度轨迹快照 的 entries 不是数组/,
    )
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 条目解包
// ══════════════════════════════════════════════════════════════════════════

describe('条目解包', () => {
  function withEntry(e: Record<string, unknown>) {
    return snapOf({ entries: [e as never] })
  }
  function base(): Record<string, unknown> {
    return { ...(entryOf() as unknown as Record<string, unknown>) }
  }

  it('★ ★ 条目缺一个键 ⇒ 抛并点名下标', () => {
    const bad = del(base(), 'attempt')
    expect(() => unwrapDispatchJournalSnapshot(withEntry(bad))).toThrow(
      /entries\[0\] 缺 1 个键（attempt）/,
    )
  })

  it('★ ★★ 五个恒在键逐个都要检查', () => {
    for (const key of ['seq', 'at', 'action', 'attempt', 'counts']) {
      expect(() => unwrapDispatchJournalSnapshot(withEntry(del(base(), key)))).toThrow(
        /entries\[0\] 缺 1 个键/,
      )
    }
  })

  it('★ ★★ 四个标量键的类型逐个都要校验', () => {
    expect(() => unwrapDispatchJournalSnapshot(withEntry({ ...base(), seq: '1' }))).toThrow(
      /entries\[0\] 的 seq 不是数字/,
    )
    expect(() => unwrapDispatchJournalSnapshot(withEntry({ ...base(), at: 1 }))).toThrow(
      /entries\[0\] 的 at 不是字符串/,
    )
    expect(() => unwrapDispatchJournalSnapshot(withEntry({ ...base(), action: 1 }))).toThrow(
      /entries\[0\] 的 action 不是字符串/,
    )
    expect(() => unwrapDispatchJournalSnapshot(withEntry({ ...base(), attempt: '1' }))).toThrow(
      /entries\[0\] 的 attempt 不是数字/,
    )
  })

  it('★ ★ action 是未知取值也放行（后端可新增，硬拒会让客户端崩）', () => {
    expect(() => unwrapDispatchJournalSnapshot(withEntry({ ...base(), action: 'brand_new_action' }))).not.toThrow()
  })

  it('★ counts 不是对象 ⇒ 抛', () => {
    expect(() => unwrapDispatchJournalSnapshot(withEntry({ ...base(), counts: [] }))).toThrow(
      /entries\[0\] 的 counts 响应形状不符/,
    )
  })

  it('★ ★ counts 缺一个键 ⇒ 抛并点名', () => {
    const c = del({ ...countsOf() } as unknown as Record<string, unknown>, 'capacity_waits')
    expect(() => unwrapDispatchJournalSnapshot(withEntry({ ...base(), counts: c }))).toThrow(
      /entries\[0\] 的 counts 缺 1 个键（capacity_waits）/,
    )
  })

  it('★ ★★ counts 的五个计数器逐个都要校验', () => {
    for (const k of ['retries', 'node_switches', 'model_switches', 'capacity_waits', 'scheduled_waits']) {
      expect(() => unwrapDispatchJournalSnapshot(withEntry({ ...base(), counts: { ...countsOf(), [k]: 'x' } }))).toThrow(
        new RegExp(`entries\\[0\\] 的 counts 的 ${k} 不是数字`),
      )
    }
  })

  it('★ ★★ 五个字符串型可选键逐个都要校验类型', () => {
    for (const k of ['model', 'vendor', 'error_kind', 'from_model', 'to_model'] as const) {
      expect(() => unwrapDispatchJournalSnapshot(withEntry({ ...base(), [k]: 1 }))).toThrow(
        new RegExp(`entries\\[0\\] 的 ${k} 不是字符串`),
      )
    }
  })

  it('★ ★★ 六个数字型可选键逐个都要校验类型', () => {
    for (const k of [
      'credential_id',
      'provider_id',
      'http_status',
      'from_credential_id',
      'to_credential_id',
      'from_provider_id',
    ] as const) {
      expect(() => unwrapDispatchJournalSnapshot(withEntry({ ...base(), [k]: 'x' }))).toThrow(
        new RegExp(`entries\\[0\\] 的 ${k} 不是数字`),
      )
    }
  })

  it('★ ★★ 12 个 omitempty 键全部缺席也是合法形状', () => {
    const bare = { seq: 1, at: '2026-10-07T12:00:00Z', action: 'completed', attempt: 1, counts: countsOf() }
    expect(() => unwrapDispatchJournalSnapshot(withEntry({ ...bare }))).not.toThrow()
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (5)(6) 两段式截断
// ══════════════════════════════════════════════════════════════════════════

describe('(5)(6) 两段式截断', () => {
  it('★ ★★ 未截断且填满 50 条窗口 ⇒ 成立', () => {
    const r = snapOf({ entries: manyEntries(50), snapshot_version: 50 })
    expect(dispatchJournalTruncatedFillsWindow(r)).toBe(true)
  })

  it('★ ★ 截断但只有 3 条 ⇒ 不成立（切片是精确的，必须正好 50）', () => {
    expect(dispatchJournalTruncatedFillsWindow(snapOf({ truncated: true, truncated_count: 5 }))).toBe(false)
  })

  // ★ 没截断时**本来就不必**填满窗口 —— 真实快照里 3 条轨迹就是 truncated:false。
  it('★ ★★ 未截断但只有 3 条 ⇒ 成立（没截断不必填满窗口）', () => {
    expect(dispatchJournalTruncatedFillsWindow(snapOf())).toBe(true)
  })

  it('★ ★ 截断且正好 50 条 ⇒ 成立', () => {
    expect(dispatchJournalTruncatedFillsWindow(snapOf({ entries: manyEntries(50), truncated: true, truncated_count: 7 }))).toBe(
      true,
    )
  })

  it('★ ★★ 截断标记与计数一致 ⇒ 成立', () => {
    const r = snapOf({ entries: manyEntries(50), truncated: true, truncated_count: 12, snapshot_version: 62 })
    expect(dispatchJournalTruncatedCountMatchesFlag(r)).toBe(true)
  })

  it('★ ★ 标了截断但计数是 0 ⇒ 不成立', () => {
    expect(dispatchJournalTruncatedCountMatchesFlag(snapOf({ truncated: true }))).toBe(false)
  })

  it('★ ★ 未标截断但计数非 0 ⇒ 不成立', () => {
    expect(dispatchJournalTruncatedCountMatchesFlag(snapOf({ truncated_count: 3 }))).toBe(false)
  })

  it('★ ★★ 首条 seq 大于 1 ⇒ 第一段（环形 128）丢过东西', () => {
    expect(dispatchJournalLeadingEntriesWereDropped(snapOf({ entries: manyEntries(5, 40), snapshot_version: 44 }))).toBe(
      true,
    )
  })

  it('★ ★ 首条 seq 等于 1 ⇒ 没丢（反向）', () => {
    expect(dispatchJournalLeadingEntriesWereDropped(snapOf())).toBe(false)
  })

  it('★ ★ 首条 seq 恰好是 2 ⇒ 也算丢过（边界那一格）', () => {
    expect(dispatchJournalLeadingEntriesWereDropped(snapOf({ entries: manyEntries(3, 2) }))).toBe(true)
  })

  it('★ ★ empty entries 的首条判据返回 false（没有首条可谈）', () => {
    expect(dispatchJournalLeadingEntriesWereDropped(snapOf({ entries: [] }))).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (7)(8)(9) 终态、seq 与 snapshot_version 的自洽
// ══════════════════════════════════════════════════════════════════════════

describe('(7)(8)(9) 终态与 seq 自洽', () => {
  it('★ ★★ 末条是 completed ⇒ 判为终态', () => {
    expect(dispatchJournalEntryIsTerminal(entryOf({ action: 'completed' }))).toBe(true)
  })

  it('★ failed 也算终态', () => {
    expect(dispatchJournalEntryIsTerminal(entryOf({ action: 'failed' }))).toBe(true)
  })

  it('★ ★ canceled 也算终态', () => {
    expect(dispatchJournalEntryIsTerminal(entryOf({ action: 'canceled' }))).toBe(true)
  })

  it('★ ★ retry_same_cred 不算终态（反向）', () => {
    expect(dispatchJournalEntryIsTerminal(entryOf({ action: 'retry_same_cred' }))).toBe(false)
  })

  it('★ ★★ 末条恒为终态 ⇒ 成立', () => {
    expect(dispatchJournalEndsWithTerminal(snapOf())).toBe(true)
  })

  it('★ ★ 末条是非终态 ⇒ 不成立（后端不可能产出）', () => {
    const r = snapOf()
    expect(dispatchJournalEndsWithTerminal({ ...r, entries: [entryOf({ seq: 1, action: 'switch_cred' })] })).toBe(
      false,
    )
  })

  it('★ ★ empty entries ⇒ 末条判据为 false', () => {
    expect(dispatchJournalEndsWithTerminal(snapOf({ entries: [] }))).toBe(false)
  })

  it('★ ★★ seq 首条为 1 且步长为 1 ⇒ 成立', () => {
    expect(dispatchJournalSeqStartsAtOne(snapOf())).toBe(true)
    expect(dispatchJournalSeqIsContiguous(snapOf())).toBe(true)
  })

  it('★ ★ seq 中间有空洞 ⇒ 不成立', () => {
    const r = snapOf()
    const bad = [entryOf({ seq: 1 }), entryOf({ seq: 3, action: 'completed' })]
    expect(dispatchJournalSeqIsContiguous({ ...r, entries: bad })).toBe(false)
  })

  it('★ ★ seq 从 5 开始 ⇒ 首条判据为 false', () => {
    expect(dispatchJournalSeqStartsAtOne(snapOf({ entries: manyEntries(3, 5) }))).toBe(false)
  })

  it('★ ★★ 版本号恒等于末条 seq ⇒ 成立（自洽不变量）', () => {
    expect(dispatchJournalVersionMatchesLastSeq(snapOf())).toBe(true)
  })

  it('★ ★ 版本号与末条 seq 不等 ⇒ 不成立（反向）', () => {
    expect(dispatchJournalVersionMatchesLastSeq(snapOf({ snapshot_version: 99 }))).toBe(false)
  })

  it('★ ★ empty entries ⇒ 版本判据为 false', () => {
    expect(dispatchJournalVersionMatchesLastSeq(snapOf({ entries: [] }))).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (10) 累计量
// ══════════════════════════════════════════════════════════════════════════

describe('(10) attempt 与 counts 是累计量', () => {
  it('★ ★ attempt 逐条非递减 ⇒ 成立', () => {
    expect(dispatchJournalAttemptIsNonDecreasing(snapOf())).toBe(true)
  })

  it('★ ★ attempt 出现回退 ⇒ 不成立', () => {
    const r = snapOf()
    const bad = [entryOf({ seq: 1, attempt: 5 }), entryOf({ seq: 2, attempt: 2, action: 'completed' })]
    expect(dispatchJournalAttemptIsNonDecreasing({ ...r, entries: bad })).toBe(false)
  })

  it('★ ★★ counts 五个计数器逐条非递减 ⇒ 成立', () => {
    expect(dispatchJournalCountsAreCumulative(snapOf())).toBe(true)
  })

  it('★ ★★ 五个计数器逐个都要参与判定', () => {
    for (const k of ['retries', 'node_switches', 'model_switches', 'capacity_waits', 'scheduled_waits'] as const) {
      const bad = [
        entryOf({ seq: 1, counts: countsOf({ [k]: 5 } as never) }),
        entryOf({ seq: 2, action: 'completed', counts: countsOf({ [k]: 1 } as never) }),
      ]
      expect(dispatchJournalCountsAreCumulative(snapOf({ entries: bad }))).toBe(false)
    }
  })

  it('★ ★ attempt 持平 ⇒ 仍算非递减', () => {
    const flat = [
      entryOf({ seq: 1, attempt: 2 }),
      entryOf({ seq: 2, attempt: 2, action: 'completed' }),
    ]
    expect(dispatchJournalAttemptIsNonDecreasing(snapOf({ entries: flat }))).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 时戳与 omitempty
// ══════════════════════════════════════════════════════════════════════════

describe('时戳与 omitempty 吃掉 0', () => {
  it('★ ★ at 是 RFC3339Nano ⇒ 成立', () => {
    expect(dispatchJournalAtIsRFC3339(entryOf())).toBe(true)
  })

  it('★ at 无小数秒也是 RFC3339Nano', () => {
    expect(dispatchJournalAtIsRFC3339(entryOf({ at: '2026-10-07T12:00:00Z' }))).toBe(true)
  })

  it('★ ★★ at 只有日期没有时间 ⇒ 不成立', () => {
    expect(dispatchJournalAtIsRFC3339(entryOf({ at: '2026-10-07' }))).toBe(false)
  })

  it('★ ★★ at 尾部多出字符 ⇒ 不成立（行尾锚是唯一拦得住的那一位）', () => {
    expect(dispatchJournalAtIsRFC3339(entryOf({ at: '2026-10-07T12:00:00Zxx' }))).toBe(false)
  })

  it('★ ★★ at 是 Go 的零值时间 ⇒ 仍然成立（time.Time 从不为 null）', () => {
    expect(dispatchJournalAtIsRFC3339(entryOf({ at: '0001-01-01T00:00:00Z' }))).toBe(true)
  })

  it('★ ★★ 可选数字键存在 ⇒ 判为存在', () => {
    expect(dispatchJournalOptionalNumberIsPresent(entryOf({ credential_id: 12 }), 'credential_id')).toBe(true)
  })

  it('★ ★★ 可选数字键缺席（可能就是被 omitempty 吃掉的 0）⇒ 判为不存在', () => {
    expect(dispatchJournalOptionalNumberIsPresent(entryOf(), 'credential_id')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (13) 权限：角色 × 路径租户
// ══════════════════════════════════════════════════════════════════════════

describe('(13) 权限是二维的', () => {
  it('★ ★ super_admin 是特权角色', () => {
    expect(dispatchJournalRoleIsPrivileged('super_admin')).toBe(true)
  })

  it('★ admin_key 也是特权角色（可读任意租户）', () => {
    expect(dispatchJournalRoleIsPrivileged('admin_key')).toBe(true)
  })

  it('★ ★ tenant_admin 不是特权角色（反向）', () => {
    expect(dispatchJournalRoleIsPrivileged('tenant_admin')).toBe(false)
  })

  it('★ ★★ 特权角色可读任意租户', () => {
    expect(dispatchJournalRoleCanReadTenant('super_admin', 't-1', 't-2')).toBe(true)
  })

  it('★ ★ admin_key 同样可读别的租户', () => {
    expect(dispatchJournalRoleCanReadTenant('admin_key', 't-1', 't-2')).toBe(true)
  })

  it('★ ★ tenant_admin 读自己租户 ⇒ 可以', () => {
    expect(dispatchJournalRoleCanReadTenant('tenant_admin', 't-1', 't-1')).toBe(true)
  })

  it('★ ★★ tenant_admin 读别的租户 ⇒ 不行（会被掩蔽成 404）', () => {
    expect(dispatchJournalRoleCanReadTenant('tenant_admin', 't-1', 't-2')).toBe(false)
  })

  it('★ ★ 其它角色一律不行（反向）', () => {
    expect(dispatchJournalRoleCanReadTenant('viewer', 't-1', 't-1')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (14) 路径段
// ══════════════════════════════════════════════════════════════════════════

describe('(14) 路径段合法性', () => {
  it('★ ★ 普通段 ⇒ 合法', () => {
    expect(dispatchJournalSegmentIsAcceptable('default')).toBe(true)
    expect(dispatchJournalSegmentIsAcceptable('req-abc-123')).toBe(true)
  })

  it('★ ★ 前后空格会被后端 TrimSpace 掉 ⇒ 仍合法', () => {
    expect(dispatchJournalSegmentIsAcceptable('  default  ')).toBe(true)
  })

  it('★ ★ 空串与纯空格 ⇒ 不合法', () => {
    expect(dispatchJournalSegmentIsAcceptable('')).toBe(false)
    expect(dispatchJournalSegmentIsAcceptable('   ')).toBe(false)
  })

  it('★ ★ 含斜杠 ⇒ 不合法（会被切错路径）', () => {
    expect(dispatchJournalSegmentIsAcceptable('a/b')).toBe(false)
  })

  it('★ ★ 含反斜杠 ⇒ 不合法', () => {
    expect(dispatchJournalSegmentIsAcceptable('a\\b')).toBe(false)
  })

  it('★ ★ 含 NUL ⇒ 不合法', () => {
    expect(dispatchJournalSegmentIsAcceptable('a\u0000b')).toBe(false)
  })

  it('★ ★★ 点与点点 ⇒ 不合法（后端显式拒）', () => {
    expect(dispatchJournalSegmentIsAcceptable('.')).toBe(false)
    expect(dispatchJournalSegmentIsAcceptable('..')).toBe(false)
  })

  it('★ ★ 空格包着的点 ⇒ 也不合法（trim 之后它才是点）', () => {
    expect(dispatchJournalSegmentIsAcceptable(' . ')).toBe(false)
  })

  it('★ ★ 两段都合法 ⇒ 参数可用', () => {
    expect(dispatchJournalParamsAreAcceptable('default', 'req-1')).toBe(true)
  })

  it('★ ★ 任一段不合法 ⇒ 参数不可用（否则必然 400）', () => {
    expect(dispatchJournalParamsAreAcceptable('default', 'a/b')).toBe(false)
    expect(dispatchJournalParamsAreAcceptable('..', 'req-1')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (4) 空 entries 的二义
// ══════════════════════════════════════════════════════════════════════════

describe('(4) 空 entries 的二义', () => {
  it('★ ★★ 空 entries ⇒ 判为二义（无轨迹 / 已被淘汰 / 进程重启）', () => {
    expect(dispatchJournalEmptyEntriesAreAmbiguous(snapOf({ entries: [] }))).toBe(true)
  })

  it('★ 非空 ⇒ 不判为二义（反向）', () => {
    expect(dispatchJournalEmptyEntriesAreAmbiguous(snapOf())).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// fetch 与路径拼装
// ══════════════════════════════════════════════════════════════════════════

describe('fetch 端到端', () => {
  it('★ ★ 路径前缀带尾斜杠，两段用 encodeURIComponent', () => {
    const p = dispatchJournalPath('t-1', 'req 2')
    expect(p.startsWith(DISPATCH_JOURNAL_PATH_PREFIX)).toBe(true)
    expect(p).toContain('t-1/req%202')
  })

  it('★ ★★ 含斜杠的租户被编码成 %2F（不会切错路径）', () => {
    expect(dispatchJournalPath('a/b', 'r')).toContain('a%2Fb/r')
  })

  it('★ ★★ 含斜杠的 requestId 也被编码成 %2F（两段都要编码）', () => {
    expect(dispatchJournalPath('default', 'r/1')).toContain('default/r%2F1')
  })

  it('★ ★ fetch 打到正确路径', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(snapOf()))
    await fetchDispatchJournal({ tenantId: 'default', requestId: 'req-abc' })
    const u = String(fetchMock.mock.calls[0]![0])
    expect(u).toContain('/api/admin/dispatch/journal/default/req-abc')
  })

  it('★ ★ 端到端：解包后的末条仍是终态且版本自洽', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(snapOf()))
    const r = await fetchDispatchJournal({ tenantId: 'default', requestId: 'req-abc' })
    expect(dispatchJournalEndsWithTerminal(r)).toBe(true)
    expect(dispatchJournalVersionMatchesLastSeq(r)).toBe(true)
  })

  it('★ ★ 缺一个键 ⇒ 抛错（端到端）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(del(snapOf() as never, 'snapshot_version')))
    await expect(fetchDispatchJournal({ tenantId: 'default', requestId: 'r' })).rejects.toThrow(/缺 1 个键/)
  })

  it('★ ★ 全空的降级响应也能解包（200 不带错误）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(snapOf({ entries: [] })))
    const r = await fetchDispatchJournal({ tenantId: 'default', requestId: 'r' })
    expect(dispatchJournalEmptyEntriesAreAmbiguous(r)).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 常量取值
// ══════════════════════════════════════════════════════════════════════════

describe('常量取值', () => {
  it('★ ★★ 404 文案是 not found（五种成因共用同一句）', () => {
    expect(DISPATCH_JOURNAL_NOT_FOUND_MESSAGE).toBe('not found')
  })

  it('★ ★ 400 文案 === invalid journal path', () => {
    expect(DISPATCH_JOURNAL_BAD_PATH_MESSAGE).toBe('invalid journal path')
  })

  it('★ 405 文案 === method not allowed', () => {
    expect(DISPATCH_JOURNAL_METHOD_MESSAGE).toBe('method not allowed')
  })

  it('★ ★ 503 文案 === journal unavailable（两种成因共用）', () => {
    expect(DISPATCH_JOURNAL_UNAVAILABLE_MESSAGE).toBe('journal unavailable')
  })

  it('★ ★ 401 文案 === authentication required（中间件层）', () => {
    expect(DISPATCH_JOURNAL_UNAUTHORIZED_MESSAGE).toBe('authentication required')
  })

  it('★ ★★ 三个上限常量分别是 50 / 128 / 10000', () => {
    expect(DISPATCH_JOURNAL_SNAPSHOT_MAX_ENTRIES).toBe(50)
    expect(DISPATCH_JOURNAL_RING_CAPACITY).toBe(128)
    expect(DISPATCH_JOURNAL_STORE_CAPACITY).toBe(10000)
  })

  it('★ ★ 快照窗口 50 确实小于环形 128（两段截断真的分叉）', () => {
    expect(DISPATCH_JOURNAL_SNAPSHOT_MAX_ENTRIES).toBeLessThan(DISPATCH_JOURNAL_RING_CAPACITY)
  })

  it('★ ★ 六个动作取值与后端 notice.go 一致', () => {
    expect([...DISPATCH_JOURNAL_ACTIONS]).toEqual([
      'retry_same_cred',
      'switch_cred',
      'switch_model',
      'capacity_wait',
      'scheduled_wait',
      'completed',
      'failed',
      'canceled',
    ])
  })

  it('★ ★★ 终态三值是动作集合的尾部三个', () => {
    expect([...DISPATCH_JOURNAL_TERMINAL_ACTIONS]).toEqual(['completed', 'failed', 'canceled'])
  })

  it('★ ★ 特权两角色与租户档', () => {
    expect([...DISPATCH_JOURNAL_PRIVILEGED_ROLES]).toEqual(['super_admin', 'admin_key'])
    expect(DISPATCH_JOURNAL_TENANT_ROLE).toBe('tenant_admin')
  })

  it('★ ★★ 快照六键与条目五必填键逐一与后端对齐', () => {
    expect([...DISPATCH_JOURNAL_SNAPSHOT_KEYS]).toEqual([
      'tenant_id',
      'request_id',
      'entries',
      'truncated',
      'truncated_count',
      'snapshot_version',
    ])
    expect([...DISPATCH_JOURNAL_ENTRY_REQUIRED_KEYS]).toEqual(['seq', 'at', 'action', 'attempt', 'counts'])
  })

  it('★ ★★ 条目十二个 omitempty 可选键逐一与后端对齐', () => {
    expect([...DISPATCH_JOURNAL_ENTRY_OPTIONAL_KEYS]).toEqual([
      'model',
      'credential_id',
      'provider_id',
      'vendor',
      'error_kind',
      'http_status',
      'from_model',
      'to_model',
      'from_credential_id',
      'to_credential_id',
      'from_provider_id',
      'to_provider_id',
    ])
  })

  it('★ ★ counts 五键与后端 ActionCounts 对齐', () => {
    expect([...DISPATCH_JOURNAL_COUNTS_KEYS]).toEqual([
      'retries',
      'node_switches',
      'model_switches',
      'capacity_waits',
      'scheduled_waits',
    ])
  })

  it('★ ★ 路径前缀带尾斜杠（分发表按 TrimPrefix 切）', () => {
    expect(DISPATCH_JOURNAL_PATH_PREFIX).toBe('/api/admin/dispatch/journal/')
  })
})
