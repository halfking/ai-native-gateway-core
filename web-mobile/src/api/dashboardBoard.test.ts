import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchDashboardOperational,
  unwrapOperational,
  operationalDiscoveryNeverRan,
  operationalSelfCheckMeaningless,
  operationalAnyDegraded,
  fetchErrorDrill,
  unwrapErrorDrill,
  errorDrillServedFromCache,
  errorDrillDaysEffective,
  errorDrillDimensionEffective,
  errorDrillKindValid,
  errorDrillEmpty,
  OPERATIONAL_KEYS,
  OPERATIONAL_DISCOVERY_KEYS,
  OPERATIONAL_BG_KEYS,
  OPERATIONAL_SELFCHECK_KEYS,
  ERROR_DRILL_KEYS,
  ERROR_DRILL_ITEM_KEYS,
  type OperationalBackgroundTasks,
  type OperationalSelfCheck,
  type ErrorDrillResponse,
} from './dashboardBoard'

/**
 * 看板两条裸 JSON 端点（2026-10-08，第五十九批）。
 *
 * ★ 本族最该被钉住的两条：
 *   ① **「从未运行过」被后端算成 `degraded`**（aux.go:66 用了原始 err，
 *      而 `… ORDER BY started_at DESC LIMIT 1` 一行都没有时返 pgx.ErrNoRows）
 *      ⇒ 一套从未跑过 discovery 的新网关会永远挂着一个红的降级提示。
 *   ② **`source` 是条件键**：命中缓存才有（= "redis"），现算时整个键不存在。
 *      而真正的取数来源（分钟视图 vs hot-log 兜底）**根本没下发**
 *      —— `boardSource()` 只在 `/dashboard/board` 用。
 */

const fetchMock = vi.fn()
function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
function lastCall(): { url: string; init: RequestInit } {
  const c = fetchMock.mock.calls.at(-1) as unknown as [string, RequestInit]
  return { url: c[0], init: c[1] }
}

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})

/* ── 夹具：逐字照抄 dashboard_board_aux.go:40-73 / :99-112 ─────── */

/** 一切正常的 payload。`degraded` 恒发。 */
const OPERATIONAL_OK = {
  background_tasks: {
    discovery: {
      running: true,
      status: 'running',
      trigger: 'scheduled',
      started_at: '2026-10-08T02:00:00Z',
      heartbeat_at: '2026-10-08T02:59:00Z',
    },
    probe_loop: { checks_last_10m: 42 },
    degraded: false,
  },
  selfcheck: {
    total_runs_24h: 12,
    success_rate: 0.916,
    last_status: 'success',
    degraded: false,
    last_run_at: '2026-10-08T01:00:00Z',
  },
}

/** ★ 一行都没跑过：ErrNoRows ⇒ status/trigger 为 null（strPtrVal），但 degraded=true。 */
const OPERATIONAL_NEVER_RAN = {
  background_tasks: {
    discovery: { running: false, status: null, trigger: null },
    probe_loop: { checks_last_10m: 0 },
    degraded: true,
    degraded_reason: 'discovery status unavailable',
  },
  selfcheck: { total_runs_24h: 0, success_rate: 0, last_status: null, degraded: false },
}

function bg(over: Partial<OperationalBackgroundTasks> = {}): OperationalBackgroundTasks {
  return { ...OPERATIONAL_OK.background_tasks, ...over } as OperationalBackgroundTasks
}
function sc(over: Partial<OperationalSelfCheck> = {}): OperationalSelfCheck {
  return { ...OPERATIONAL_OK.selfcheck, ...over } as OperationalSelfCheck
}

/** boardPieItem（dashboard_board.go:10-16）。 */
const DRILL_DB = {
  error_kind: 'rate_limit',
  dimension: 'model',
  items: [
    { key: 'gpt-4o', requests: 120, tokens: 90000, credits: 40, cost_usd: 3.21 },
    { key: 'claude-sonnet', requests: 40, tokens: 20000, credits: 12, cost_usd: 0.9 },
  ],
}

/** ★ 缓存命中分支多一个 `source`（dashboard_board.go:187）。 */
const DRILL_CACHED = { ...DRILL_DB, source: 'redis' }

describe('dashboardBoard / operational 形状', () => {
  it('正常 payload 通过', () => {
    const r = unwrapOperational(OPERATIONAL_OK)
    expect(r.background_tasks.probe_loop.checks_last_10m).toBe(42)
    expect(r.selfcheck.success_rate).toBeCloseTo(0.916)
  })

  it('★ 拿到 dashboardapi 信封形状要报错（同一前缀两种契约）', () => {
    expect(() =>
      unwrapOperational({ success: true, data: {}, timestamp: '2026-10-08T03:00:00Z' }),
    ).toThrow('本端点应为裸对象')
  })

  it('顶层 / 子对象缺键即抛错（逐个键各钉一条）', () => {
    for (const k of OPERATIONAL_KEYS) {
      const bad: Record<string, unknown> = { ...OPERATIONAL_OK }
      delete bad[k]
      expect(() => unwrapOperational(bad)).toThrow(new RegExp(`缺 1 个键（${k}）`))
    }
    for (const k of OPERATIONAL_BG_KEYS) {
      const b: Record<string, unknown> = { ...OPERATIONAL_OK.background_tasks }
      delete b[k]
      expect(() => unwrapOperational({ ...OPERATIONAL_OK, background_tasks: b })).toThrow(
        new RegExp(`background_tasks 缺 1 个键（${k}）`),
      )
    }
    for (const k of OPERATIONAL_SELFCHECK_KEYS) {
      const s: Record<string, unknown> = { ...OPERATIONAL_OK.selfcheck }
      delete s[k]
      expect(() => unwrapOperational({ ...OPERATIONAL_OK, selfcheck: s })).toThrow(
        new RegExp(`selfcheck 缺 1 个键（${k}）`),
      )
    }
  })

  it('discovery 的三个必填键逐个钉住（started_at/heartbeat_at 是可选）', () => {
    for (const k of OPERATIONAL_DISCOVERY_KEYS) {
      const d: Record<string, unknown> = { ...OPERATIONAL_OK.background_tasks.discovery }
      delete d[k]
      expect(() =>
        unwrapOperational({
          ...OPERATIONAL_OK,
          background_tasks: { ...OPERATIONAL_OK.background_tasks, discovery: d },
        }),
      ).toThrow(new RegExp(`discovery 缺 1 个键（${k}）`))
    }
  })

  it('顶层不是裸对象即抛错', () => {
    expect(() => unwrapOperational(null)).toThrow('实得 null')
    expect(() => unwrapOperational([])).toThrow('实得 array')
  })
})

/**
 * 第一百零四批（2026-10-08）新增两组判据。
 *
 * 缺陷 1：三级子对象在 `k in x` 之前**没有对象守卫** ——
 *   而 `'a' in null` 抛的是 `TypeError: Cannot use 'in' operator to search for 'a' in null`，
 *   **不是本模块的契约错误**。调用方只看到一个无法归因的栈，线上排查时连是哪一层都定位不到。
 *   ⇒ 判据必须区分「抛出的是契约错误」与「抛出的是 TypeError」——
 *     两者都叫「抛错了」，只断言 toThrow 等于没判。
 *
 * 缺陷 2：原先**只校键、完全不校类型**。
 *   区分格取在 `strPtrVal` 那两个字段上：后端 `strPtrVal(nil)` 合法产出 `null`
 *   （aux.go:196-201），所以 **`null` 必须过、别的类型必须抛** ——
 *   一条只断言「null 通过」的判据恒真，锚点得落在「null 之外的那个格」。
 */
describe('dashboardBoard / operational 子对象不是对象时给契约错误（第一百零四批）', () => {
  const LEVELS: Array<[string, unknown, string]> = [
    ['background_tasks', null, '看板运维面 background_tasks 响应形状不符：期望裸对象，实得 null'],
    ['background_tasks', 'oops', '看板运维面 background_tasks 响应形状不符：期望裸对象，实得 string'],
    ['background_tasks', [], '看板运维面 background_tasks 响应形状不符：期望裸对象，实得 array'],
  ]

  for (const [k, bad, msg] of LEVELS) {
    it(`★★★ ${k} = ${JSON.stringify(bad)} 抛的是契约错误且带层级名，不是 in operator 的 TypeError`, () => {
      let caught: unknown = null
      try {
        unwrapOperational({ ...OPERATIONAL_OK, [k]: bad })
      } catch (e) {
        caught = e
      }
      // ★ 关键：先证明它**不是** TypeError。旧实现正是栽在这里。
      expect(caught).toBeInstanceOf(Error)
      expect((caught as Error).constructor.name).not.toBe('TypeError')
      expect((caught as Error).message).not.toContain('in operator')
      // ★ 再证明它是**带层级名的**契约错误。
      expect((caught as Error).message).toBe(msg)
    })
  }

  it('★★★ discovery 不是对象时同样给契约错误（层级名要指到 discovery）', () => {
    for (const bad of [null, 'oops', [], 7] as const) {
      let caught: unknown = null
      try {
        unwrapOperational({
          ...OPERATIONAL_OK,
          background_tasks: { ...OPERATIONAL_OK.background_tasks, discovery: bad },
        })
      } catch (e) {
        caught = e
      }
      expect((caught as Error).constructor.name).not.toBe('TypeError')
      expect((caught as Error).message).toContain('background_tasks.discovery 响应形状不符')
    }
  })

  it('★★ selfcheck 不是对象时同样给契约错误', () => {
    for (const bad of [null, 'oops', []] as const) {
      let caught: unknown = null
      try {
        unwrapOperational({ ...OPERATIONAL_OK, selfcheck: bad })
      } catch (e) {
        caught = e
      }
      expect((caught as Error).constructor.name).not.toBe('TypeError')
      expect((caught as Error).message).toContain('selfcheck 响应形状不符')
    }
  })

  it('★★ probe_loop 不是对象时同样给契约错误', () => {
    let caught: unknown = null
    try {
      unwrapOperational({
        ...OPERATIONAL_OK,
        background_tasks: { ...OPERATIONAL_OK.background_tasks, probe_loop: null },
      })
    } catch (e) {
      caught = e
    }
    expect((caught as Error).constructor.name).not.toBe('TypeError')
    expect((caught as Error).message).toContain('probe_loop 响应形状不符')
  })

  it('★★ 形状守卫在**键存在性**之前：键在但值是 null，不得报成「缺 1 个键」', () => {
    // ★ 区分格方向：键缺失与「键在但值不是对象」必须给出**不同的**消息 ——
    //   两者都归到「缺键」就会把形状问题误报成结构问题。
    expect(() => unwrapOperational({ ...OPERATIONAL_OK, selfcheck: null })).not.toThrow(/缺 1 个键/)
  })
})

describe('dashboardBoard / operational 恒在键的类型校验（第一百零四批）', () => {
  /** 后端 `strPtrVal(nil)` 合法产出 null ⇒ null 必须过（这一格是「不该抛」的那一半）。 */
  it('★★ status / trigger / last_status 为 null 时通过（strPtrVal 的合法产出）', () => {
    const r = unwrapOperational({
      ...OPERATIONAL_OK,
      background_tasks: {
        ...OPERATIONAL_OK.background_tasks,
        discovery: { running: false, status: null, trigger: null },
      },
      selfcheck: { ...OPERATIONAL_OK.selfcheck, last_status: null },
    })
    expect(r.background_tasks.discovery.status).toBeNull()
    expect(r.selfcheck.last_status).toBeNull()
  })

  /** 区分格：同一批字段，**换成别的类型必须抛**。 */
  it('★★★ 同一批字段换成数字即抛错（不是「null 恒真」而是类型被校了）', () => {
    expect(() =>
      unwrapOperational({
        ...OPERATIONAL_OK,
        background_tasks: {
          ...OPERATIONAL_OK.background_tasks,
          discovery: { running: false, status: 42, trigger: null },
        },
      }),
    ).toThrow('discovery 的 status 不是字符串也不是 null')
    expect(() =>
      unwrapOperational({ ...OPERATIONAL_OK, selfcheck: { ...OPERATIONAL_OK.selfcheck, last_status: 7 } }),
    ).toThrow('selfcheck 的 last_status 不是字符串也不是 null')
  })

  it('★★ discovery.running 非布尔即抛错', () => {
    expect(() =>
      unwrapOperational({
        ...OPERATIONAL_OK,
        background_tasks: {
          ...OPERATIONAL_OK.background_tasks,
          discovery: { running: 'true', status: 'running', trigger: 'scheduled' },
        },
      }),
    ).toThrow('running 不是布尔')
  })

  it('★★ 两处 degraded 非布尔即抛错（它是显式 map 赋值，恒发）', () => {
    expect(() =>
      unwrapOperational({
        ...OPERATIONAL_OK,
        background_tasks: { ...OPERATIONAL_OK.background_tasks, degraded: 1 },
      }),
    ).toThrow('background_tasks 的 degraded 不是布尔')
    expect(() =>
      unwrapOperational({ ...OPERATIONAL_OK, selfcheck: { ...OPERATIONAL_OK.selfcheck, degraded: 'no' } }),
    ).toThrow('selfcheck 的 degraded 不是布尔')
  })

  it('★★ checks_last_10m 非数字即抛错（Go 侧 var int，查询失败也是数字 0）', () => {
    expect(() =>
      unwrapOperational({
        ...OPERATIONAL_OK,
        background_tasks: {
          ...OPERATIONAL_OK.background_tasks,
          probe_loop: { checks_last_10m: '42' },
        },
      }),
    ).toThrow('checks_last_10m 不是数字')
  })

  it('★★ selfcheck 两个计数非数字即抛错', () => {
    expect(() =>
      unwrapOperational({ ...OPERATIONAL_OK, selfcheck: { ...OPERATIONAL_OK.selfcheck, total_runs_24h: '12' } }),
    ).toThrow('total_runs_24h 不是数字')
    expect(() =>
      unwrapOperational({ ...OPERATIONAL_OK, selfcheck: { ...OPERATIONAL_OK.selfcheck, success_rate: null } }),
    ).toThrow('success_rate 不是数字')
  })

  it('★ 键齐全、只错类型 ⇒ 报的是类型而不是缺键（两条分支不串味）', () => {
    // ★ 手写夹具的「键缺」必须用 delete；这里反过来用「键齐全 + 类型错」，
    //   确保走的是类型那条分支。
    const bad: Record<string, unknown> = {
      ...OPERATIONAL_OK.background_tasks,
      degraded: 'false',
    }
    expect(Object.keys(bad)).toEqual(expect.arrayContaining([...OPERATIONAL_BG_KEYS]))
    expect(() => unwrapOperational({ ...OPERATIONAL_OK, background_tasks: bad })).not.toThrow(/缺 \d+ 个键/)
    expect(() => unwrapOperational({ ...OPERATIONAL_OK, background_tasks: bad })).toThrow('degraded 不是布尔')
  })

  it('★★ 条件键（degraded_reason / probe_degraded / started_at）不参与类型校验', () => {
    // ★ 它们是**条件键**（仅在对应查询失败时写入），存在时的类型由后端保证 ——
    //   但我们不校验它们，**也不校验它们的存在**，这与「恒在键」是两回事。
    const r = unwrapOperational({ ...OPERATIONAL_OK })
    expect(r.background_tasks.degraded_reason).toBeUndefined()
    expect(r.background_tasks.probe_degraded).toBeUndefined()
  })
})

describe('dashboardBoard / 「从未运行过」被算成降级', () => {
  it('★ degraded + status=null ⇒ 疑似从未运行，不是真故障', () => {
    expect(operationalDiscoveryNeverRan(OPERATIONAL_NEVER_RAN.background_tasks)).toBe(true)
  })

  it('★★ 真故障（status 有值但 degraded）**不算**「从未运行」', () => {
    // ★ 反向样本：degraded 可能来自 probe_loop 那条查询，discovery 本身是好的
    const probeBroken = bg({ degraded: true, probe_degraded: true })
    expect(operationalDiscoveryNeverRan(probeBroken)).toBe(false)
  })

  it('正常态不算「从未运行」', () => {
    expect(operationalDiscoveryNeverRan(OPERATIONAL_OK.background_tasks)).toBe(false)
  })

  it('degraded=false 时即便 status=null 也不算（字段缺失 ≠ 没运行过）', () => {
    const noStatus: OperationalBackgroundTasks = {
      ...OPERATIONAL_OK.background_tasks,
      discovery: { running: false, status: null, trigger: null },
    } as OperationalBackgroundTasks
    expect(operationalDiscoveryNeverRan(noStatus)).toBe(false)
  })
})

describe('dashboardBoard / selfcheck 与整体降级', () => {
  it('total_runs_24h=0 ⇒ 比率无意义，与 degraded 分开', () => {
    expect(operationalSelfCheckMeaningless(OPERATIONAL_NEVER_RAN.selfcheck)).toBe(true)
    expect(operationalSelfCheckMeaningless(OPERATIONAL_OK.selfcheck)).toBe(false)
  })

  it('★ 两块任一降级 ⇒ 整屏不可全信', () => {
    expect(operationalAnyDegraded({ background_tasks: bg(), selfcheck: sc() })).toBe(false)
    expect(
      operationalAnyDegraded({
        background_tasks: bg(),
        selfcheck: sc({ degraded: true, degraded_reason: 'self-check summary unavailable' }),
      }),
    ).toBe(true)
    expect(
      operationalAnyDegraded({
        background_tasks: bg({ degraded: true }),
        selfcheck: sc(),
      }),
    ).toBe(true)
  })
})

describe('dashboardBoard / error-drill 形状', () => {
  it('现算形态（无 source）通过', () => {
    const r = unwrapErrorDrill(DRILL_DB)
    expect(r.items).toHaveLength(2)
    expect('source' in r).toBe(false)
    expect(errorDrillServedFromCache(r)).toBe(false)
  })

  it('★ 缓存形态多一个 source=redis', () => {
    const r = unwrapErrorDrill(DRILL_CACHED)
    expect(r.source).toBe('redis')
    expect(errorDrillServedFromCache(r)).toBe(true)
  })

  it('★ source 缺失 = 现算，**不是**「未知来源」', () => {
    expect('source' in DRILL_DB).toBe(false)
    expect(errorDrillServedFromCache(unwrapErrorDrill(DRILL_DB))).toBe(false)
  })

  it('顶层三个必填键逐个钉住（source 不在其中）', () => {
    expect(ERROR_DRILL_KEYS).toHaveLength(3)
    for (const k of ERROR_DRILL_KEYS) {
      const bad: Record<string, unknown> = { ...DRILL_DB }
      delete bad[k]
      expect(() => unwrapErrorDrill(bad)).toThrow(new RegExp(`缺 1 个键（${k}）`))
    }
  })

  it('items 行的五个键逐个钉住', () => {
    for (const k of ERROR_DRILL_ITEM_KEYS) {
      const item: Record<string, unknown> = { ...DRILL_DB.items[0] }
      delete item[k]
      expect(() => unwrapErrorDrill({ ...DRILL_DB, items: [item] })).toThrow(
        new RegExp(`items\\[0\\] 缺 1 个键（${k}）`),
      )
    }
  })

  it('items 不是数组 / 元素不是对象即抛错', () => {
    expect(() => unwrapErrorDrill({ ...DRILL_DB, items: null })).toThrow('items 不是数组')
    expect(() => unwrapErrorDrill({ ...DRILL_DB, items: [null] })).toThrow('items[0] 不是对象')
  })

  it('拿到信封形状要报错', () => {
    expect(() =>
      unwrapErrorDrill({ success: true, data: {}, timestamp: 'x' }),
    ).toThrow('本端点应为裸对象')
  })

  it('零行时 empty 判据成立', () => {
    expect(errorDrillEmpty(unwrapErrorDrill({ ...DRILL_DB, items: [] }))).toBe(true)
    expect(errorDrillEmpty(unwrapErrorDrill(DRILL_DB))).toBe(false)
  })
})

describe('dashboardBoard / 参数', () => {
  it('★★ days 是 clamp 到 [1,90]，默认 1（不是 7）', () => {
    expect(errorDrillDaysEffective(undefined)).toBe(1)
    expect(errorDrillDaysEffective(7)).toBe(7)
    expect(errorDrillDaysEffective(90)).toBe(90)
    expect(errorDrillDaysEffective(0)).toBe(1)
    expect(errorDrillDaysEffective(-3)).toBe(1)
    expect(errorDrillDaysEffective(91)).toBe(90)
    expect(errorDrillDaysEffective(1.5)).toBe(1)
  })

  it('dimension 缺省/空白 ⇒ "model"', () => {
    expect(errorDrillDimensionEffective(undefined)).toBe('model')
    expect(errorDrillDimensionEffective('')).toBe('model')
    expect(errorDrillDimensionEffective('   ')).toBe('model')
    expect(errorDrillDimensionEffective('provider')).toBe('provider')
  })

  it('error_kind 必填，空/空白不算合法', () => {
    expect(errorDrillKindValid('rate_limit')).toBe(true)
    expect(errorDrillKindValid('')).toBe(false)
    expect(errorDrillKindValid('   ')).toBe(false)
    expect(errorDrillKindValid(null)).toBe(false)
    expect(errorDrillKindValid(undefined)).toBe(false)
  })
})

describe('dashboardBoard / fetch', () => {
  it('operational 打 GET 到正确路径', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(OPERATIONAL_OK))
    await fetchDashboardOperational()
    const { url, init } = lastCall()
    expect(init.method).toBe('GET')
    expect(url).toContain('/api/admin/dashboard/operational')
  })

  it('★ error-drill 必须带 error_kind', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(DRILL_DB))
    await fetchErrorDrill({ errorKind: 'rate_limit' })
    const { url, init } = lastCall()
    expect(init.method).toBe('GET')
    expect(url).toContain('/api/admin/dashboard/board/error-drill?')
    expect(url).toContain('error_kind=rate_limit')
    // ★ 不带 dimension ⇒ 后端用默认 "model"
    expect(url).not.toContain('dimension=')
  })

  it('带 dimension / days / tenant_id', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(DRILL_DB))
    await fetchErrorDrill({ errorKind: 'timeout', dimension: 'provider', days: 14, tenantId: 'acme' })
    const { url } = lastCall()
    expect(url).toContain('error_kind=timeout')
    expect(url).toContain('dimension=provider')
    expect(url).toContain('days=14')
    expect(url).toContain('tenant_id=acme')
  })

  it('★ 信封不合法时 fetch 也抛错（坏形状不递给调用方）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ success: true, timestamp: 'x' }))
    await expect(fetchDashboardOperational()).rejects.toThrow('本端点应为裸对象')
  })

  it('ErrorDrillResponse 类型与解包结果兼容', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(DRILL_CACHED))
    const r: ErrorDrillResponse = await fetchErrorDrill({ errorKind: 'rate_limit' })
    expect(errorDrillServedFromCache(r)).toBe(true)
  })
})