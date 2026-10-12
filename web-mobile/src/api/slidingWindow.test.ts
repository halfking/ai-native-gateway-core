import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchSlidingWindow,
  unwrapSlidingWindow,
  unwrapCallEntry,
  slidingWindowTotalMatchesEntries,
  slidingWindowStatsTotalMatchesEntries,
  slidingWindowSuccessPlusFailedEqualsTotal,
  slidingWindowErrorKindSum,
  slidingWindowErrorKindsAtMostFailed,
  slidingWindowErrorKindsCoverEveryFailure,
  slidingWindowFailureRateMatches,
  slidingWindowRedisSourceImpliesEntries,
  slidingWindowFallbackSourceStillHasEntries,
  slidingWindowEntriesAreNonAscending,
  slidingWindowLimitIsInRange,
  slidingWindowCredentialIdIsRejected,
  SLIDING_WINDOW_PATH,
  SLIDING_WINDOW_DEFAULT_MINUTES,
  SLIDING_WINDOW_DEFAULT_LIMIT,
  SLIDING_WINDOW_LIMIT_MIN,
  SLIDING_WINDOW_LIMIT_MAX,
  SLIDING_WINDOW_SOURCES,
  SLIDING_WINDOW_KEYS,
  CALL_ENTRY_KEYS,
  CALL_ENTRY_OPTIONAL_KEYS,
  WINDOW_STATS_KEYS,
  type CallEntry,
  type SlidingWindowResponse,
  type WindowStats,
} from './slidingWindow'

/**
 * 凭据模型滑窗调用明细的契约测试（2026-10-08，第九十四批）。
 *
 * 后端：`admin/credential_monitor.go:156`（`mux.HandleFunc` 在 RegisterMonitorRoutes
 * 方法里，第五种注册形态）+ `:697-788`（handler）+ `:790-809`（回退查询）
 * + `credentialhealth/recorder.go:16-22`（CallEntry）+ `:155-177`（ComputeStats），
 * 由 `admin/handler.go:1453` 用 **`h.admin`** 挂载（★ 与批 92 同一注册处、同一档位）。
 *
 * 重点是源文件头写明的十四件事 (1)…(14)。带 ★ 的自校验判据都能被变异打掉。
 * ⚠️ 全部用例标题**字面量**写（不用 `it.each`）—— 变异 harness 靠标题取锚点。
 * ⚠️ 所有「逐个都要检查」的循环都刻意用**字面量数组**，不用被测常量。
 * ⚠️ 夹具里的 `stats` 一律由 `ComputeStats` 的算法现算，不手抄数字。
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

// ── 夹具：逐字照抄 `credentialhealth/recorder.go:16-22`（★ json tag 是缩写）──

function entryOf(over: Partial<CallEntry> = {}): CallEntry {
  return { rid: 'req-0001', ts: 1767225600000, ok: true, lat: 420, ...over }
}

/** 失败的条目带 `err`（`ErrorKind` 的 omitempty 让成功条目没有这个键）。 */
function failedEntryOf(kind: string, over: Partial<CallEntry> = {}): CallEntry {
  return { rid: 'req-0002', ts: 1767225601000, ok: false, lat: 1300, err: kind, ...over }
}

/** ★ 复刻 `ComputeStats`（`recorder.go:155-177`），夹具不手抄数字。 */
function computeStatsOf(entries: CallEntry[]): WindowStats {
  const stats: WindowStats = { total: entries.length, success: 0, failed: 0, failure_rate: 0, error_kinds: {} }
  for (const e of entries) {
    if (e.ok) {
      stats.success++
    } else {
      stats.failed++
      if (e.err !== undefined && e.err !== '') stats.error_kinds[e.err] = (stats.error_kinds[e.err] ?? 0) + 1
    }
  }
  if (stats.total > 0) stats.failure_rate = stats.failed / stats.total
  return stats
}

function respOf(entries: CallEntry[], over: Partial<SlidingWindowResponse> = {}): SlidingWindowResponse {
  return {
    credential_id: 7,
    model: 'gpt-4o',
    window_minutes: 60,
    limit: 50,
    source: 'redis',
    total_returned: entries.length,
    entries,
    stats: computeStatsOf(entries),
    ...over,
  }
}

// ══════════════════════════════════════════════════════════════════════════
// envelope 解包（8 个恒在键）
// ══════════════════════════════════════════════════════════════════════════

describe('滑窗 envelope 解包', () => {
  it('★ 满配响应被放行（8 键）', () => {
    expect(unwrapSlidingWindow(respOf([entryOf()])).total_returned).toBe(1)
  })

  it('★ 顶层是 null ⇒ 抛', () => {
    expect(() => unwrapSlidingWindow(null)).toThrow(/滑窗 响应形状不符：期望裸对象，实得 null/)
  })

  it('★ ★ 顶层是数组 ⇒ 抛「实得 array」', () => {
    expect(() => unwrapSlidingWindow([] as never)).toThrow(/滑窗 响应形状不符：期望裸对象，实得 array/)
  })

  it('★ ★★ 八个键逐个都要检查（★ 桌面类型只声明了 6 个）', () => {
    for (const k of [
      'credential_id', 'model', 'window_minutes', 'limit',
      'source', 'total_returned', 'entries', 'stats',
    ]) {
      expect(() => unwrapSlidingWindow(del(respOf([entryOf()]) as never, k) as never)).toThrow(
        new RegExp(`滑窗 缺 1 个键（${k}）`),
      )
    }
  })

  it('★ ★★ 顶层四个数字键逐个都要校验', () => {
    for (const k of ['credential_id', 'window_minutes', 'limit', 'total_returned'] as const) {
      expect(() => unwrapSlidingWindow({ ...respOf([entryOf()]), [k]: 'x' } as never)).toThrow(
        new RegExp(`滑窗 的 ${k} 不是数字`),
      )
    }
  })

  it('★ ★ 两个字符串键逐个都要校验', () => {
    expect(() => unwrapSlidingWindow({ ...respOf([entryOf()]), model: 1 } as never)).toThrow(/的 model 不是字符串/)
    expect(() => unwrapSlidingWindow({ ...respOf([entryOf()]), source: 1 } as never)).toThrow(/的 source 不是字符串/)
  })

  it('★ ★★ entries 为空数组时被放行（★ 后端强制非 nil，见 (8)）', () => {
    expect(unwrapSlidingWindow(respOf([])).entries).toEqual([])
  })

  it('★ ★ entries 为 null ⇒ 抛', () => {
    expect(() => unwrapSlidingWindow(respOf([entryOf()], { entries: null as never }))).toThrow(
      /entries 不是数组/,
    )
  })

  it('★ ★ stats 不是对象 ⇒ 抛', () => {
    expect(() => unwrapSlidingWindow(respOf([entryOf()], { stats: null as never }))).toThrow(
      /滑窗 的 stats 响应形状不符/,
    )
  })

  it('★ ★ stats 项不是对象 ⇒ 抛', () => {
    expect(() => unwrapSlidingWindow(respOf([entryOf()], { entries: [null] as never }))).toThrow(
      /entries\[0\] 响应形状不符/,
    )
  })
})

// ══════════════════════════════════════════════════════════════════════════
// stats 解包（5 键全恒在）
// ══════════════════════════════════════════════════════════════════════════

describe('滑窗 stats 解包', () => {
  it('★ ★★ stats 的五个键逐个都要检查', () => {
    for (const k of ['total', 'success', 'failed', 'failure_rate', 'error_kinds']) {
      expect(() => unwrapSlidingWindow(respOf([entryOf()], { stats: del(computeStatsOf([entryOf()]) as never, k) as never }))).toThrow(
        new RegExp(`滑窗 的 stats 缺 1 个键（${k}）`),
      )
    }
  })

  it('★ ★★ stats 的四个数字键逐个都要校验', () => {
    for (const k of ['total', 'success', 'failed', 'failure_rate'] as const) {
      expect(() =>
        unwrapSlidingWindow(respOf([entryOf()], { stats: { ...computeStatsOf([entryOf()]), [k]: 'x' } as never })),
      ).toThrow(new RegExp(`stats 的 ${k} 不是数字`))
    }
  })

  it('★ ★★ error_kinds 不是对象 ⇒ 抛', () => {
    expect(() =>
      unwrapSlidingWindow(respOf([entryOf()], { stats: { ...computeStatsOf([entryOf()]), error_kinds: [] as never } })),
    ).toThrow(/error_kinds 不是对象/)
  })

  it('★ ★ error_kinds 的值不是数字 ⇒ 抛', () => {
    expect(() =>
      unwrapSlidingWindow(
        respOf([entryOf()], { stats: { ...computeStatsOf([entryOf()]), error_kinds: { timeout: 'x' } as never } }),
      ),
    ).toThrow(/error_kinds 的 timeout 不是数字/)
  })

  it('★ error_kinds 为空对象时也被放行（无失败 ⇒ 无归类）', () => {
    expect(unwrapSlidingWindow(respOf([entryOf()])).stats.error_kinds).toEqual({})
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (13) CallEntry：五个缩写键，err 带 omitempty
// ══════════════════════════════════════════════════════════════════════════

describe('(13) CallEntry 的缩写键', () => {
  it('★ 满配条目被放行（成功条目没有 err 键）', () => {
    expect('err' in (entryOf() as unknown as Record<string, unknown>)).toBe(false)
    expect(unwrapCallEntry(entryOf(), 'e')).toBeTruthy()
  })

  it('★ ★★ 四个必在键逐个都要检查', () => {
    for (const k of ['rid', 'ts', 'ok', 'lat']) {
      expect(() => unwrapCallEntry(del(entryOf() as never, k) as never, 'e')).toThrow(
        new RegExp(`e 缺 1 个键（${k}）`),
      )
    }
  })

  it('★ rid 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapCallEntry({ ...entryOf(), rid: 1 } as never, 'e')).toThrow(/e 的 rid 不是字符串/)
  })

  it('★ ★ ts 不是数字 ⇒ 抛', () => {
    expect(() => unwrapCallEntry({ ...entryOf(), ts: '1767225600000' } as never, 'e')).toThrow(/e 的 ts 不是数字/)
  })

  it('★ ★ lat 不是数字 ⇒ 抛', () => {
    expect(() => unwrapCallEntry({ ...entryOf(), lat: '420' } as never, 'e')).toThrow(/e 的 lat 不是数字/)
  })

  it('★ ★ ok 不是布尔 ⇒ 抛', () => {
    expect(() => unwrapCallEntry({ ...entryOf(), ok: 'yes' } as never, 'e')).toThrow(/e 的 ok 不是布尔/)
  })

  it('★ ★★ err 存在但不是字符串 ⇒ 抛（成功条目本就没有这个键）', () => {
    expect(() => unwrapCallEntry({ ...entryOf(), err: 1 } as never, 'e')).toThrow(/e 的 err 不是字符串/)
  })

  it('★ ★ err 缺席 ⇒ 放行（ok=true 的实际形状）', () => {
    expect(() => unwrapCallEntry(entryOf(), 'e')).not.toThrow()
  })

  it('★ ★★ rid 为空串时被放行（★ COALESCE(request_id,…) 的合法值）', () => {
    expect(unwrapCallEntry(entryOf({ rid: '' }), 'e')).toBeTruthy()
  })

  it('★ ★ 失败条目带 err ⇒ 被放行', () => {
    expect(unwrapCallEntry(failedEntryOf('timeout'), 'e')).toBeTruthy()
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (3) 四个数字互锁
// ══════════════════════════════════════════════════════════════════════════

describe('(3) total_returned / entries / stats.total 互锁', () => {
  it('★ ★★★ total_returned 等于 entries.length ⇒ 成立', () => {
    expect(slidingWindowTotalMatchesEntries(respOf([entryOf(), failedEntryOf('timeout')]))).toBe(true)
  })

  it('★ ★★ total_returned 大于 entries.length ⇒ 不成立', () => {
    expect(slidingWindowTotalMatchesEntries(respOf([entryOf()], { total_returned: 5 }))).toBe(false)
  })

  it('★ ★★ stats.total 等于 entries.length ⇒ 成立', () => {
    expect(slidingWindowStatsTotalMatchesEntries(respOf([entryOf(), failedEntryOf('timeout')]))).toBe(true)
  })

  it('★ ★ stats.total 与 entries.length 不符 ⇒ 不成立', () => {
    const s = computeStatsOf([entryOf()])
    expect(slidingWindowStatsTotalMatchesEntries(respOf([entryOf(), failedEntryOf('x')], { stats: s }))).toBe(false)
  })

  it('★ ★★ success 加 failed 等于 total ⇒ 成立', () => {
    expect(slidingWindowSuccessPlusFailedEqualsTotal(respOf([entryOf(), failedEntryOf('timeout')]))).toBe(true)
  })

  it('★ ★★ success 加 failed 大于 total ⇒ 不成立（★ if/else 是完备二分）', () => {
    const s = { ...computeStatsOf([entryOf()]), success: 2, failed: 1 }
    expect(slidingWindowSuccessPlusFailedEqualsTotal(respOf([entryOf()], { stats: s }))).toBe(false)
  })

  it('★ ★★ success 加 failed 小于 total ⇒ 不成立', () => {
    const s = { ...computeStatsOf([entryOf(), failedEntryOf('timeout')]), success: 0, failed: 0 }
    expect(slidingWindowSuccessPlusFailedEqualsTotal(respOf([entryOf(), failedEntryOf('timeout')], { stats: s }))).toBe(
      false,
    )
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (4) error_kinds 的和 ≤ failed
// ══════════════════════════════════════════════════════════════════════════

describe('(4) 失败原因分布与失败数不等', () => {
  it('★ ★★ 求和 helper 本身算得对', () => {
    expect(slidingWindowErrorKindSum({ total: 3, success: 0, failed: 3, failure_rate: 1, error_kinds: { timeout: 2, upstream: 1 } })).toBe(3)
  })

  it('★ ★★★ 求和等于 failed ⇒ 成立（每个失败都有非空 err）', () => {
    const entries = [failedEntryOf('timeout'), failedEntryOf('timeout'), failedEntryOf('upstream')]
    expect(slidingWindowErrorKindsAtMostFailed(computeStatsOf(entries))).toBe(true)
  })

  it('★ ★★★★ 求和小于 failed ⇒ 成立（★ 有一个失败没归类）', () => {
    // ★ 后端只为 ErrorKind != "" 的失败计数（recorder.go:166-168）
    const entries = [failedEntryOf('timeout'), { rid: 'req-9', ts: 1, ok: false, lat: 5 } as CallEntry]
    const s = computeStatsOf(entries)
    expect(s.failed).toBe(2)
    expect(slidingWindowErrorKindSum(s)).toBe(1)
    expect(slidingWindowErrorKindsAtMostFailed(s)).toBe(true)
  })

  it('★ ★ 求和大于 failed ⇒ 不成立', () => {
    const s = { total: 2, success: 0, failed: 1, failure_rate: 0.5, error_kinds: { timeout: 2 } }
    expect(slidingWindowErrorKindsAtMostFailed(s)).toBe(false)
  })

  it('★ ★★★ 每个失败都有非空 err ⇒ 判为完全覆盖', () => {
    const entries = [failedEntryOf('timeout'), failedEntryOf('upstream'), entryOf()]
    expect(slidingWindowErrorKindsCoverEveryFailure(entries)).toBe(true)
  })

  it('★ ★★★ 有一个失败没有 err 键 ⇒ 判为未完全覆盖', () => {
    const entries = [failedEntryOf('timeout'), { rid: 'req-9', ts: 1, ok: false, lat: 5 } as CallEntry]
    expect(slidingWindowErrorKindsCoverEveryFailure(entries)).toBe(false)
  })

  it('★ ★ err 是空串时也判为未完全覆盖（★ 后端那一行不计入）', () => {
    const entries = [{ rid: 'r', ts: 1, ok: false, lat: 5, err: '' } as CallEntry]
    expect(slidingWindowErrorKindsCoverEveryFailure(entries)).toBe(false)
  })

  it('★ ★★ 全部成功 ⇒ 判为完全覆盖（failed 为 0，无失败可漏）', () => {
    expect(slidingWindowErrorKindsCoverEveryFailure([entryOf(), entryOf()])).toBe(true)
  })

  it('★ ★ 空数组 ⇒ 判为完全覆盖（反向）', () => {
    expect(slidingWindowErrorKindsCoverEveryFailure([])).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (5) failure_rate 是派生值
// ══════════════════════════════════════════════════════════════════════════

describe('(5) failure_rate 的派生关系', () => {
  it('★ ★★ total 为 0 时 rate 恒为 0（★ 不是 NaN、不是 null）', () => {
    expect(slidingWindowFailureRateMatches(computeStatsOf([]))).toBe(true)
  })

  it('★ ★ total 为 0 而 rate 非 0 ⇒ 不成立', () => {
    // ★ 样本必须是 failure_rate === 1 那一格：=== 与 ===1 的唯一分叉点。
    expect(slidingWindowFailureRateMatches({ total: 0, success: 0, failed: 0, failure_rate: 1, error_kinds: {} })).toBe(
      false,
    )
  })

  it('★ ★★ 全成功 ⇒ rate 为 0 ⇒ 成立', () => {
    expect(slidingWindowFailureRateMatches(computeStatsOf([entryOf(), entryOf()]))).toBe(true)
  })

  it('★ ★★ 全失败 ⇒ rate 为 1 ⇒ 成立', () => {
    expect(slidingWindowFailureRateMatches(computeStatsOf([failedEntryOf('timeout')]))).toBe(true)
  })

  it('★ ★★★ 三成一失败 ⇒ rate 等于 1/4 ⇒ 成立', () => {
    const entries = [entryOf(), entryOf(), entryOf(), failedEntryOf('timeout')]
    expect(slidingWindowFailureRateMatches(computeStatsOf(entries))).toBe(true)
  })

  it('★ ★★ rate 与 failed/total 不符 ⇒ 不成立', () => {
    expect(
      slidingWindowFailureRateMatches({ total: 4, success: 3, failed: 1, failure_rate: 0.5, error_kinds: {} }),
    ).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (6) source 的语义
// ══════════════════════════════════════════════════════════════════════════

describe('(6) source 的语义与二义', () => {
  it('★ ★★★ source 是 redis 且有数据 ⇒ 成立', () => {
    expect(slidingWindowRedisSourceImpliesEntries(respOf([entryOf()]))).toBe(true)
  })

  it('★ ★★★ source 是 redis 却空 ⇒ 不成立（★ 后端空必回退，这是恒被保证的一格）', () => {
    expect(slidingWindowRedisSourceImpliesEntries(respOf([], { source: 'redis' }))).toBe(false)
  })

  it('★ source 是 request_logs ⇒ 放行（该方向不断言）', () => {
    expect(slidingWindowRedisSourceImpliesEntries(respOf([entryOf()], { source: 'request_logs' }))).toBe(true)
  })

  // ★★ 这条是「去掉 source 守卫」那条变异的**唯一区分格**：
  //   非 redis **且** entries 为空 ⇒ 原版放行（该方向不断言），守卫被去掉后就不放行。
  it('★ ★★ source 是 request_logs 而 entries 为空 ⇒ 仍放行（★ 非 redis 的行不参与断言）', () => {
    expect(slidingWindowRedisSourceImpliesEntries(respOf([], { source: 'request_logs' }))).toBe(true)
  })

  it('★ ★★★ source 是 request_logs 却有数据 ⇒ 判为「仍可有数据」（★ 二义那一格）', () => {
    expect(slidingWindowFallbackSourceStillHasEntries(respOf([entryOf()], { source: 'request_logs' }))).toBe(true)
  })

  it('★ ★ source 是 request_logs 且空 ⇒ 不成立', () => {
    expect(slidingWindowFallbackSourceStillHasEntries(respOf([], { source: 'request_logs' }))).toBe(false)
  })

  it('★ source 是 redis ⇒ 不算回退（反向）', () => {
    expect(slidingWindowFallbackSourceStillHasEntries(respOf([entryOf()]))).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (14) 时间序无 tiebreak
// ══════════════════════════════════════════════════════════════════════════

describe('(14) 时间序无 tiebreak', () => {
  it('★ ★ 非升序 ⇒ 成立', () => {
    const entries = [entryOf({ ts: 2000 }), entryOf({ ts: 1000 })]
    expect(slidingWindowEntriesAreNonAscending(entries)).toBe(true)
  })

  it('★ ★ 出现升序 ⇒ 不成立', () => {
    const entries = [entryOf({ ts: 1000 }), entryOf({ ts: 2000 })]
    expect(slidingWindowEntriesAreNonAscending(entries)).toBe(false)
  })

  it('★ ★ 同毫秒并列 ⇒ 仍成立（无 tiebreak，不做更严的断言）', () => {
    const entries = [entryOf({ ts: 1000 }), entryOf({ ts: 1000 })]
    expect(slidingWindowEntriesAreNonAscending(entries)).toBe(true)
  })

  it('★ ★ 空数组 ⇒ 成立', () => {
    expect(slidingWindowEntriesAreNonAscending([])).toBe(true)
  })

  it('★ ★ 单行 ⇒ 成立', () => {
    expect(slidingWindowEntriesAreNonAscending([entryOf()])).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (9)(10) 查询参数边界
// ══════════════════════════════════════════════════════════════════════════

describe('(9)(10) limit 与 credential_id 的边界', () => {
  it('★ ★ 下界 1 ⇒ 成立', () => {
    expect(slidingWindowLimitIsInRange(1)).toBe(true)
  })

  it('★ ★ 上界 500 ⇒ 成立', () => {
    expect(slidingWindowLimitIsInRange(500)).toBe(true)
  })

  it('★ ★★ limit 区间下界那一格：0 ⇒ 不成立', () => {
    expect(slidingWindowLimitIsInRange(0)).toBe(false)
  })

  it('★ ★ 501 ⇒ 不成立', () => {
    expect(slidingWindowLimitIsInRange(501)).toBe(false)
  })

  it('★ ★ 负数 ⇒ 不成立', () => {
    expect(slidingWindowLimitIsInRange(-1)).toBe(false)
  })

  it('★ ★ credential_id 为 0 ⇒ 判为被拒', () => {
    expect(slidingWindowCredentialIdIsRejected(0)).toBe(true)
  })

  it('★ ★★ 负数 ⇒ 不判为被拒（后端只拦 == 0，不是 < 1）', () => {
    expect(slidingWindowCredentialIdIsRejected(-1)).toBe(false)
  })

  it('★ 正数 ⇒ 不判为被拒', () => {
    expect(slidingWindowCredentialIdIsRejected(7)).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// fetch 端到端
// ══════════════════════════════════════════════════════════════════════════

describe('fetch 端到端', () => {
  it('★ ★★ 四个查询参数都被拼上', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(respOf([entryOf()])))
    await fetchSlidingWindow({ credentialId: 7, model: 'gpt-4o', minutes: 15, limit: 20 })
    const u = String(fetchMock.mock.calls[0]![0])
    expect(u).toContain('/api/credentials/sliding-window?')
    expect(u).toContain('credential_id=7')
    expect(u).toContain('model=gpt-4o')
    expect(u).toContain('minutes=15')
    expect(u).toContain('limit=20')
  })

  it('★ ★ 不给 minutes 与 limit 时用缺省 60 与 50', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(respOf([entryOf()])))
    await fetchSlidingWindow({ credentialId: 7, model: 'gpt-4o' })
    const u = String(fetchMock.mock.calls[0]![0])
    expect(u).toContain('minutes=60')
    expect(u).toContain('limit=50')
  })

  it('★ ★★ 含斜杠与空格的 model 被 URL 编码（后端虽做 lower 匹配，但查询串必须先切对）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(respOf([entryOf()], { model: 'a/b c' })))
    await fetchSlidingWindow({ credentialId: 7, model: 'a/b c' })
    const u = String(fetchMock.mock.calls[0]![0])
    expect(u).toContain('model=a%2Fb+c')
    expect(u).not.toContain('model=a/b')
  })

  it('★ ★★ 端到端：顶层缺键 ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(del(respOf([entryOf()]) as never, 'total_returned')))
    await expect(fetchSlidingWindow({ credentialId: 7, model: 'gpt-4o' })).rejects.toThrow(/缺 1 个键/)
  })

  it('★ ★★ 端到端：条目缺键 ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(respOf([del(entryOf() as never, 'lat') as never])))
    await expect(fetchSlidingWindow({ credentialId: 7, model: 'gpt-4o' })).rejects.toThrow(/缺 1 个键/)
  })

  it('★ ★★ 端到端：四个数字互锁不一致时也能取到（解包器不管语义）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(respOf([entryOf()], { total_returned: 99 })))
    const r = await fetchSlidingWindow({ credentialId: 7, model: 'gpt-4o' })
    expect(r.total_returned).toBe(99)
    expect(slidingWindowTotalMatchesEntries(r)).toBe(false)
  })

  it('★ ★★ 端到端：空 entries 被放行且 rate 为 0', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(respOf([])))
    const r = await fetchSlidingWindow({ credentialId: 7, model: 'gpt-4o' })
    expect(r.entries).toEqual([])
    expect(r.stats.failure_rate).toBe(0)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 常量取值
// ══════════════════════════════════════════════════════════════════════════

describe('常量取值', () => {
  it('★ 路径不带查询串', () => {
    expect(SLIDING_WINDOW_PATH).toBe('/api/credentials/sliding-window')
  })

  it('★ ★★ minutes 与 limit 的缺省、limit 的上下界', () => {
    expect(SLIDING_WINDOW_DEFAULT_MINUTES).toBe(60)
    expect(SLIDING_WINDOW_DEFAULT_LIMIT).toBe(50)
    expect(SLIDING_WINDOW_LIMIT_MIN).toBe(1)
    expect(SLIDING_WINDOW_LIMIT_MAX).toBe(500)
  })

  it('★ ★★ source 两值', () => {
    expect([...SLIDING_WINDOW_SOURCES]).toEqual(['redis', 'request_logs'])
  })

  it('★ ★★ envelope 八键（★ 桌面类型只声明了 6 个）', () => {
    expect(SLIDING_WINDOW_KEYS).toHaveLength(8)
    expect([...SLIDING_WINDOW_KEYS]).toEqual([
      'credential_id', 'model', 'window_minutes', 'limit',
      'source', 'total_returned', 'entries', 'stats',
    ])
  })

  it('★ ★★ CallEntry 五个缩写键（★ 与 Go 字段名完全不同）', () => {
    expect([...CALL_ENTRY_KEYS]).toEqual(['rid', 'ts', 'ok', 'lat', 'err'])
    expect([...CALL_ENTRY_OPTIONAL_KEYS]).toEqual(['err'])
  })

  it('★ ★ stats 五键', () => {
    expect([...WINDOW_STATS_KEYS]).toEqual(['total', 'success', 'failed', 'failure_rate', 'error_kinds'])
  })
})
