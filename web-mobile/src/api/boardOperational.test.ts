import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { req } from '@/api/client'
import {
  BOARD_OPERATIONAL_CACHE_TTL_MS,
  BOARD_OPERATIONAL_CACHE_CONTROL,
  BOARD_OPERATIONAL_QUERY_TIMEOUT_MS,
  BOARD_DB_NOT_CONFIGURED_MESSAGE,
  BOARD_DISCOVERY_DEGRADED_REASON,
  BOARD_SELFCHECK_DEGRADED_REASON,
  INCLUDE_OPERATIONAL_PARAM,
  BOARD_DISCOVERY_HARDCODED_TENANT,
  BOARD_BG_ALWAYS_KEYS,
  BOARD_BG_OPTIONAL_KEYS,
  BOARD_DISCOVERY_ALWAYS_KEYS,
  BOARD_DISCOVERY_OPTIONAL_KEYS,
  BOARD_PROBE_LOOP_KEYS,
  BOARD_SELFCHECK_ALWAYS_KEYS,
  BOARD_SELFCHECK_OPTIONAL_KEYS,
  BOARD_OPERATIONAL_KEYS,
  fetchBoardOperational,
  unwrapBoardOperational,
  boardBgDiscoveryDegraded,
  boardBgProbeDegraded,
  boardBgDegradationIsAttributable,
  boardBgBothSourcesDegraded,
  boardBgDiscoveryReason,
  boardDiscoveryAmbiguousReasons,
  boardDiscoveryHasNoStatus,
  boardDiscoveryStartedAtOrNull,
  boardChecksCountMayBeFailed,
  boardChecksCountIsGenuineZero,
  boardProbeLoopUnreliable,
  boardSuccessRateIsMeaningless,
  boardSelfCheckNeverRan,
  boardSelfCheckDegraded,
  boardSelfCheckReason,
  boardSelfCheckHealthy,
  boardOperationalSampleMayBeCached,
  boardOperationalSampleStableAcrossTtl,
  boardNotConfigured,
} from '@/api/boardOperational'

/**
 * 看板运维芯片的不变量（2026-10-07，第七十五批）。
 *
 * ★ 本族最该被钉住的：
 *   ① ★★★★★ **三个子查询有三种租户口径，没有一种按调用方租户过滤**：
 *      model_discovery_runs 写死 tenant_id='default'；credential_health_checks
 *      与 self_check_runs 完全不过滤。而注册是 admin 档。
 *   ② ★★★★★ **degraded 是显式 map 赋值 ⇒ 恒发**；两个条件键各自对应一个查询。
 *   ③ ★★★★★ **degraded:true 可能是「表里从来没有记录」**：:35 排除了 ErrNoRows
 *      但 :66 没有 ⇒ 「没跑过 discovery」被标成降级。
 *   ④ ★★★★ checks_last_10m===0 是二义的（失败只 slog.Warn，值留 0）。
 *   ⑤ ★★★★ success_rate 是 0-1 比例，total===0 时留 0.0 ⇒ 与「全失败」同值。
 *   ⑥ ★★★ discovery 恒发键（status/trigger 可为 null）与两个条件键混排。
 *   ⑦ ★★★ 30 秒进程内缓存（两层）⇒ 相同样本不能证明「数据没变」。
 *   ⑧ ★★ include_operational 参数被本端点完全忽略。
 *
 * 夹具纪律：数字与字面量一律硬写后端源码值（30s / 5s / 两条 reason 原文 /
 * 'default'），不用被测常量造数据。
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
 * 夹具：admin/dashboard_board_aux.go:40-111 的 map 逐字抄
 * ═══════════════════════════════════════════════════════════════════════════ */

/** `discovery`（`:40-50`）。@param noStarted 走 `started_at` 不赋值的分支。 */
function discovery(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    running: false,
    status: 'completed',
    trigger: 'scheduled',
    started_at: '2026-10-07T09:00:00Z',
    heartbeat_at: '2026-10-07T09:30:00Z',
    ...over,
  }
}

/** `discovery` 的两个时间键都缺失 + status/trigger 为 null（`:42-43` 的 nil 分支）。 */
function discoveryNoTimes(): Record<string, unknown> {
  return { running: false, status: null, trigger: null }
}

/** selfcheck（`:101-111`）。 */
function selfcheck(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    total_runs_24h: 12,
    success_rate: 0.9166,
    last_status: 'pass',
    degraded: false,
    ...over,
  }
}

/** 完整载荷（全部健康）。 */
function payload(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    background_tasks: {
      discovery: discovery(),
      probe_loop: { checks_last_10m: 3 },
      degraded: false,
    },
    selfcheck: selfcheck(),
    ...over,
  }
}

/** background_tasks 降级：discovery 查询失败。 */
function payloadBgDiscoveryDown(over: Record<string, unknown> = {}): Record<string, unknown> {
  return payload({
    background_tasks: {
      discovery: discoveryNoTimes(),
      probe_loop: { checks_last_10m: 3 },
      degraded: true,
      degraded_reason: 'discovery status unavailable',
    },
    ...over,
  })
}

/** background_tasks 降级：只有 probe 查询失败。 */
function payloadBgProbeDown(): Record<string, unknown> {
  return payload({
    background_tasks: {
      discovery: discovery(),
      probe_loop: { checks_last_10m: 0 },
      degraded: true,
      probe_degraded: true,
    },
  })
}

/** selfcheck 降级。 */
function payloadSelfDown(): Record<string, unknown> {
  return payload({ selfcheck: selfcheck({ degraded: true, degraded_reason: 'self-check summary unavailable' }) })
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 常量对齐后端字面量
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('常量对齐后端字面量', () => {
  it('缓存 TTL 是 30 秒', () => {
    // dashboard_operational.go:11
    expect(BOARD_OPERATIONAL_CACHE_TTL_MS).toBe(30000)
  })

  it('Cache-Control 是 private, max-age=30', () => {
    // :77
    expect(BOARD_OPERATIONAL_CACHE_CONTROL).toBe('private, max-age=30')
  })

  it('查询超时是 5 秒', () => {
    // :74
    expect(BOARD_OPERATIONAL_QUERY_TIMEOUT_MS).toBe(5000)
  })

  it('503 的 message 原文', () => {
    expect(BOARD_DB_NOT_CONFIGURED_MESSAGE).toBe('database not configured')
  })

  it('两条 degraded_reason 原文不同', () => {
    // dashboard_board_aux.go:68 vs :108
    expect(BOARD_DISCOVERY_DEGRADED_REASON).toBe('discovery status unavailable')
    expect(BOARD_SELFCHECK_DEGRADED_REASON).toBe('self-check summary unavailable')
    expect(BOARD_DISCOVERY_DEGRADED_REASON).not.toBe(BOARD_SELFCHECK_DEGRADED_REASON)
  })

  it('include_operational 是下划线形式', () => {
    expect(INCLUDE_OPERATIONAL_PARAM).toBe('include_operational')
  })

  it('discovery 查询写死的租户是 default', () => {
    // dashboard_board_aux.go:32
    expect(BOARD_DISCOVERY_HARDCODED_TENANT).toBe('default')
  })

  it('background_tasks 恒在键三个、条件键两个', () => {
    expect([...BOARD_BG_ALWAYS_KEYS].sort()).toEqual(['degraded', 'discovery', 'probe_loop'])
    expect([...BOARD_BG_OPTIONAL_KEYS].sort()).toEqual(['degraded_reason', 'probe_degraded'])
  })

  it('discovery 恒在键三个、条件键两个', () => {
    expect([...BOARD_DISCOVERY_ALWAYS_KEYS].sort()).toEqual(['running', 'status', 'trigger'])
    expect([...BOARD_DISCOVERY_OPTIONAL_KEYS].sort()).toEqual(['heartbeat_at', 'started_at'])
  })

  it('probe_loop 只有一个键', () => {
    expect([...BOARD_PROBE_LOOP_KEYS]).toEqual(['checks_last_10m'])
  })

  it('selfcheck 恒在键四个、条件键两个', () => {
    expect([...BOARD_SELFCHECK_ALWAYS_KEYS].sort()).toEqual([
      'degraded', 'last_status', 'success_rate', 'total_runs_24h',
    ])
    expect([...BOARD_SELFCHECK_OPTIONAL_KEYS].sort()).toEqual(['degraded_reason', 'last_run_at'])
  })

  it('顶层两个键', () => {
    expect([...BOARD_OPERATIONAL_KEYS].sort()).toEqual(['background_tasks', 'selfcheck'])
  })

  it('两个条件键都不在必检键里', () => {
    const bg = new Set<string>(BOARD_BG_ALWAYS_KEYS)
    expect(BOARD_BG_OPTIONAL_KEYS.filter((k) => bg.has(k))).toEqual([])
    const sc = new Set<string>(BOARD_SELFCHECK_ALWAYS_KEYS)
    expect(BOARD_SELFCHECK_OPTIONAL_KEYS.filter((k) => sc.has(k))).toEqual([])
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 解包
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('解包', () => {
  it('健康载荷原样通过', () => {
    const r = unwrapBoardOperational(payload())
    expect(boardBgDegradationIsAttributable(r)).toBe(true)
  })

  it('★ status 与 trigger 为 null 时通过', () => {
    // ★ strPtrVal(nil) ⇒ JSON null，而键是恒发的
    const r = unwrapBoardOperational(payloadBgDiscoveryDown())
    expect(r.background_tasks.discovery.status).toBeNull()
  })

  it('★ 两个时间键都缺失时通过', () => {
    // ★ started_at / heartbeat_at 是条件键
    const r = unwrapBoardOperational(payloadBgDiscoveryDown())
    expect(boardDiscoveryStartedAtOrNull(r)).toBeNull()
  })

  it('status 不是字符串也不是 null 时抛错', () => {
    expect(() =>
      unwrapBoardOperational(payload({ background_tasks: { discovery: discovery({ status: 7 }), probe_loop: { checks_last_10m: 1 }, degraded: false } })),
    ).toThrow(/status 不是字符串也不是 null/)
  })

  it('running 不是布尔值时抛错', () => {
    expect(() =>
      unwrapBoardOperational(payload({ background_tasks: { discovery: discovery({ running: 'yes' }), probe_loop: { checks_last_10m: 1 }, degraded: false } })),
    ).toThrow(/running 不是布尔值/)
  })

  it('degraded 不是布尔值时抛错', () => {
    expect(() =>
      unwrapBoardOperational(payload({ background_tasks: { discovery: discovery(), probe_loop: { checks_last_10m: 1 }, degraded: 1 } })),
    ).toThrow(/background_tasks.degraded 不是布尔值/)
  })

  it('★ selfcheck 的 degraded 不是布尔值时抛错', () => {
    // ★ 两个 degraded **同名但语义不同** ⇒ 校验文案必须能区分是哪一层
    expect(() => unwrapBoardOperational(payload({ selfcheck: selfcheck({ degraded: 'x' }) }))).toThrow(
      /selfcheck.degraded 不是布尔值/,
    )
  })

  it('缺 degraded 时抛错', () => {
    const p = payload()
    delete (p.background_tasks as Record<string, unknown>).degraded
    expect(() => unwrapBoardOperational(p)).toThrow(/缺 1 个键/)
  })

  it('缺 probe_loop 时抛错', () => {
    const p = payload()
    delete (p.background_tasks as Record<string, unknown>).probe_loop
    expect(() => unwrapBoardOperational(p)).toThrow(/缺 1 个键/)
  })

  it('缺 discovery 的 status 时抛错', () => {
    const d = discovery()
    delete d.status
    expect(() =>
      unwrapBoardOperational(payload({ background_tasks: { discovery: d, probe_loop: { checks_last_10m: 1 }, degraded: false } })),
    ).toThrow(/缺 1 个键/)
  })

  it('缺 checks_last_10m 时抛错', () => {
    expect(() =>
      unwrapBoardOperational(payload({ background_tasks: { discovery: discovery(), probe_loop: {}, degraded: false } })),
    ).toThrow(/缺 1 个键/)
  })

  it('checks_last_10m 不是数字时抛错', () => {
    expect(() =>
      unwrapBoardOperational(payload({ background_tasks: { discovery: discovery(), probe_loop: { checks_last_10m: 'x' }, degraded: false } })),
    ).toThrow(/checks_last_10m 不是数字/)
  })

  it('success_rate 不是数字时抛错', () => {
    expect(() => unwrapBoardOperational(payload({ selfcheck: selfcheck({ success_rate: 'x' }) }))).toThrow(
      /success_rate 不是数字/,
    )
  })

  it('total_runs_24h 不是数字时抛错', () => {
    expect(() => unwrapBoardOperational(payload({ selfcheck: selfcheck({ total_runs_24h: 'x' }) }))).toThrow(
      /total_runs_24h 不是数字/,
    )
  })

  it('顶层缺 selfcheck 时抛错', () => {
    const p = payload()
    delete p.selfcheck
    expect(() => unwrapBoardOperational(p)).toThrow(/缺 1 个键/)
  })

  it('background_tasks 不是对象时抛错', () => {
    expect(() => unwrapBoardOperational(payload({ background_tasks: 7 }))).toThrow(/形状不符/)
  })

  it('数组形状抛错', () => {
    expect(() => unwrapBoardOperational([])).toThrow(/形状不符/)
  })

  it('null 响应抛错', () => {
    expect(() => unwrapBoardOperational(null)).toThrow(/形状不符/)
  })

  it('dashboardapi 信封形状抛错', () => {
    expect(() => unwrapBoardOperational({ success: true, timestamp: 1 })).toThrow(/信封/)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ②③ degraded 的来源与可归因性
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('degraded 的来源', () => {
  it('全健康时判定为可归因（无降级）', () => {
    expect(boardBgDegradationIsAttributable(unwrapBoardOperational(payload()))).toBe(true)
  })

  it('★ degraded 为真且带 reason 时判定为可归因', () => {
    expect(boardBgDegradationIsAttributable(unwrapBoardOperational(payloadBgDiscoveryDown()))).toBe(true)
  })

  it('★ degraded 为真且带 probe_degraded 时判定为可归因', () => {
    expect(boardBgDegradationIsAttributable(unwrapBoardOperational(payloadBgProbeDown()))).toBe(true)
  })

  it('★ degraded 为真但两个条件键都缺时判定为不可归因', () => {
    // :66 保证 degraded 为真必然至少有一个条件键 ⇒ 这是契约被破坏的形态
    const bad = payload({
      background_tasks: { discovery: discovery(), probe_loop: { checks_last_10m: 1 }, degraded: true },
    })
    expect(boardBgDegradationIsAttributable(unwrapBoardOperational(bad))).toBe(false)
  })

  it('★ degraded 为假却带条件键时判定为不可归因', () => {
    const bad = payload({
      background_tasks: {
        discovery: discovery(), probe_loop: { checks_last_10m: 1 },
        degraded: false, probe_degraded: true,
      },
    })
    expect(boardBgDegradationIsAttributable(unwrapBoardOperational(bad))).toBe(false)
  })

  it('discovery 降级判定为真', () => {
    expect(boardBgDiscoveryDegraded(unwrapBoardOperational(payloadBgDiscoveryDown()))).toBe(true)
  })

  it('只 probe 降级时 discovery 判定为假', () => {
    expect(boardBgDiscoveryDegraded(unwrapBoardOperational(payloadBgProbeDown()))).toBe(false)
  })

  it('probe 降级判定为真', () => {
    expect(boardBgProbeDegraded(unwrapBoardOperational(payloadBgProbeDown()))).toBe(true)
  })

  it('discovery 降级时 reason 返回原文', () => {
    const r = unwrapBoardOperational(payloadBgDiscoveryDown())
    expect(boardBgDiscoveryReason(r)).toBe('discovery status unavailable')
  })

  it('无降级时 reason 返回 null', () => {
    expect(boardBgDiscoveryReason(unwrapBoardOperational(payload()))).toBeNull()
  })

  it('两个来源同时降级判定为真', () => {
    const both = payload({
      background_tasks: {
        discovery: discoveryNoTimes(),
        probe_loop: { checks_last_10m: 0 },
        degraded: true,
        degraded_reason: 'discovery status unavailable',
        probe_degraded: true,
      },
    })
    expect(boardBgBothSourcesDegraded(unwrapBoardOperational(both))).toBe(true)
  })

  it('只一个来源降级时判定为假', () => {
    expect(boardBgBothSourcesDegraded(unwrapBoardOperational(payloadBgProbeDown()))).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ③ ErrNoRows 也会触发 degraded
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('never-ran 与 query-failed 的二义', () => {
  it('status 为 null 时判定为无状态', () => {
    expect(boardDiscoveryHasNoStatus(unwrapBoardOperational(payloadBgDiscoveryDown()))).toBe(true)
  })

  it('status 有值时判定为有状态', () => {
    expect(boardDiscoveryHasNoStatus(unwrapBoardOperational(payload()))).toBe(false)
  })

  it('started_at 缺失时返回 null', () => {
    expect(boardDiscoveryStartedAtOrNull(unwrapBoardOperational(payloadBgDiscoveryDown()))).toBeNull()
  })

  it('started_at 有值时返回该值', () => {
    const r = unwrapBoardOperational(payload())
    expect(boardDiscoveryStartedAtOrNull(r)).toBe('2026-10-07T09:00:00Z')
  })

  it('★ status 与 started_at 都缺时至少两种解释都站得住', () => {
    // ★ 「查询失败」「从没跑过（ErrNoRows）」「这条记录没有 started_at」
    const r = unwrapBoardOperational(payloadBgDiscoveryDown())
    expect(boardDiscoveryAmbiguousReasons(r)).toBeGreaterThan(1)
  })

  it('★ status 与 started_at 都在时只剩一种解释', () => {
    const r = unwrapBoardOperational(payload())
    expect(boardDiscoveryAmbiguousReasons(r)).toBe(1)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ④⑤ checks_last_10m 与 success_rate 的二义
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('计数与比例的二义', () => {
  it('计数为 0 且 probe 降级时判定为可能失败', () => {
    expect(boardChecksCountMayBeFailed(unwrapBoardOperational(payloadBgProbeDown()))).toBe(true)
  })

  it('计数为 0 且 probe 未降级时判定为真零', () => {
    const zero = payload({
      background_tasks: { discovery: discovery(), probe_loop: { checks_last_10m: 0 }, degraded: false },
    })
    expect(boardChecksCountIsGenuineZero(unwrapBoardOperational(zero))).toBe(true)
  })

  it('★ 计数为 0 但 probe 未降级时不判定为可能失败', () => {
    // ★★★ 「可能失败」的两个必要条件是 **0** 与 **probe 降级**。
    // 缺了后半句，这个函数会把「真的 0 次」也说成「查不出来」——
    // 正是 `:52` 计数失败留 0 与 `:68` 真的 0 次的**二义**（文件头第 (4) 条）。
    const zero = payload({
      background_tasks: { discovery: discovery(), probe_loop: { checks_last_10m: 0 }, degraded: false },
    })
    expect(boardChecksCountMayBeFailed(unwrapBoardOperational(zero))).toBe(false)
  })

  it('计数非零时判定为非真零', () => {
    const r = unwrapBoardOperational(payload())
    expect(boardChecksCountIsGenuineZero(r)).toBe(false)
    expect(boardChecksCountMayBeFailed(r)).toBe(false)
  })

  it('probe 降级时整块不可信', () => {
    expect(boardProbeLoopUnreliable(unwrapBoardOperational(payloadBgProbeDown()))).toBe(true)
  })

  it('probe 未降级时整块可信', () => {
    expect(boardProbeLoopUnreliable(unwrapBoardOperational(payload()))).toBe(false)
  })

  it('total 为 0 时成功率无意义', () => {
    const r = unwrapBoardOperational(payload({ selfcheck: selfcheck({ total_runs_24h: 0, success_rate: 0 }) }))
    expect(boardSuccessRateIsMeaningless(r)).toBe(true)
  })

  it('total 非 0 时成功率有意义', () => {
    expect(boardSuccessRateIsMeaningless(unwrapBoardOperational(payload()))).toBe(false)
  })

  it('★ total 为 0 且未降级时判定为从没跑过', () => {
    const r = unwrapBoardOperational(payload({ selfcheck: selfcheck({ total_runs_24h: 0, success_rate: 0 }) }))
    expect(boardSelfCheckNeverRan(r)).toBe(true)
  })

  it('total 为 0 且降级时不判定为从没跑过', () => {
    // ★ 二义：要靠 degraded 才能区分
    const r = unwrapBoardOperational(
      payload({ selfcheck: selfcheck({ total_runs_24h: 0, success_rate: 0, degraded: true }) }),
    )
    expect(boardSelfCheckNeverRan(r)).toBe(false)
  })

  it('自检降级判定为真', () => {
    expect(boardSelfCheckDegraded(unwrapBoardOperational(payloadSelfDown()))).toBe(true)
  })

  it('自检未降级判定为假', () => {
    expect(boardSelfCheckDegraded(unwrapBoardOperational(payload()))).toBe(false)
  })

  it('自检降级时 reason 返回自检那条原文', () => {
    const r = unwrapBoardOperational(payloadSelfDown())
    expect(boardSelfCheckReason(r)).toBe('self-check summary unavailable')
  })

  it('★ 降级判定与 background_tasks 互不影响', () => {
    // ★ 两个 degraded 同名但语义不同：一个挂了不代表另一个挂了
    const r = unwrapBoardOperational(payloadSelfDown())
    expect(boardSelfCheckDegraded(r)).toBe(true)
    expect(boardBgProbeDegraded(r)).toBe(false)
  })

  it('健康判定：有运行记录且比率达标', () => {
    expect(boardSelfCheckHealthy(unwrapBoardOperational(payload()), 0.8)).toBe(true)
  })

  it('★ 健康判定：降级时不成立', () => {
    expect(boardSelfCheckHealthy(unwrapBoardOperational(payloadSelfDown()), 0.8)).toBe(false)
  })

  it('★ 健康判定：total 为 0 时不成立', () => {
    const r = unwrapBoardOperational(payload({ selfcheck: selfcheck({ total_runs_24h: 0, success_rate: 0 }) }))
    expect(boardSelfCheckHealthy(r, 0.8)).toBe(false)
  })

  it('健康判定：比率不达标时不成立', () => {
    const r = unwrapBoardOperational(payload({ selfcheck: selfcheck({ success_rate: 0.5 }) }))
    expect(boardSelfCheckHealthy(r, 0.8)).toBe(false)
  })

  it('★ 健康判定：阈值放宽到 0 时 total 为 0 仍不成立', () => {
    // ★★ 「没跑过」必须与阈值**无关**地被排除。
    // 只靠 `success_rate >= minRate` 兜不住：`total === 0` 时后端留 `0.0`（`:96-99`），
    // 阈值一旦被调成 0，`0.0 >= 0` 就是真 ⇒ 会把「没跑过」说成「100% 不达标前的健康」。
    const r = unwrapBoardOperational(payload({ selfcheck: selfcheck({ total_runs_24h: 0, success_rate: 0 }) }))
    expect(boardSelfCheckHealthy(r, 0)).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ⑦ 缓存
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('30 秒缓存', () => {
  // ★★★ **必须惰性构造**：解包器一旦在 describe 体顶层调用，
  //   抛错就发生在**收集期** ⇒ 整份 spec 变成 `Tests  no tests`，
  //   所有具名归因全部丢失（变异 #6 / #8 最初就是这样被判成 STILL_GREEN 的）。
  const healthy = () => unwrapBoardOperational(payload())
  const selfDown = () => unwrapBoardOperational(payloadSelfDown())

  it('★ 间隔不足 TTL 时相同样本判为可能来自缓存', () => {
    expect(boardOperationalSampleMayBeCached(healthy(), healthy(), 5000)).toBe(true)
  })

  it('★ 间隔不足 TTL 时不判为稳定', () => {
    expect(boardOperationalSampleStableAcrossTtl(healthy(), healthy(), 5000)).toBe(false)
  })

  it('间隔超过 TTL 且完全相同才判为稳定', () => {
    expect(boardOperationalSampleStableAcrossTtl(healthy(), healthy(), 40000)).toBe(true)
  })

  it('间隔超过 TTL 但内容不同判为不稳定', () => {
    expect(boardOperationalSampleStableAcrossTtl(healthy(), selfDown(), 40000)).toBe(false)
  })

  it('间隔超过 TTL 时不判为可能来自缓存', () => {
    expect(boardOperationalSampleMayBeCached(healthy(), healthy(), 40000)).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 503 与 fetch
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('503 与 fetch', () => {
  it('503 配 database not configured 被识别', () => {
    expect(boardNotConfigured(503, BOARD_DB_NOT_CONFIGURED_MESSAGE)).toBe(true)
  })

  it('200 不判为未配置', () => {
    expect(boardNotConfigured(200, BOARD_DB_NOT_CONFIGURED_MESSAGE)).toBe(false)
  })

  it('★ 500 配同一 message 不判为未配置', () => {
    // ★★ 后端未配置只走 503（`dashboard_operational.go:159`）⇒ 「同一个 message」
    //   不足以判「未配置」。这里挡的是**别的** 5xx（网关 bug / 上游 500）撞上同一个文案，
    //   客户端若一律说成「数据库未配置」就会给出错误的处置指引。
    expect(boardNotConfigured(500, BOARD_DB_NOT_CONFIGURED_MESSAGE)).toBe(false)
  })

  it('打到固定路径', async () => {
    ok(payload())
    await fetchBoardOperational()
    expect(reqMock.mock.calls[0]![1]).toBe('/api/admin/dashboard/operational')
  })

  it('★ 不发 include_operational 参数', async () => {
    ok(payload())
    await fetchBoardOperational()
    expect(String(reqMock.mock.calls[0]![1])).not.toContain('include_operational')
  })

  it('fetch 会过解包器', async () => {
    ok(payload({ background_tasks: { discovery: discovery(), probe_loop: {}, degraded: false } }))
    await expect(fetchBoardOperational()).rejects.toThrow(/缺 1 个键/)
  })

  it('fetch 会过嵌套的类型检查', async () => {
    ok(payload({ selfcheck: selfcheck({ success_rate: 'x' }) }))
    await expect(fetchBoardOperational()).rejects.toThrow(/success_rate 不是数字/)
  })
})
