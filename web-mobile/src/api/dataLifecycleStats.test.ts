import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { req } from '@/api/client'
import {
  // A. stats
  fetchDataLifecycleStats,
  unwrapDataLifecycleStats,
  segmentUnavailable,
  anySegmentUnavailable,
  availableSegments,
  segmentPercentTotal,
  percentMeaningless,
  segmentRowsDisagree,
  byTenantTruncated,
  growthTrendTruncated,
  growthTrendIsNewestFirst,
  listEmptyIsAmbiguous,
  compressionRateSaturated,
  SEGMENT_DAYS,
  BY_TENANT_LIMIT,
  GROWTH_TREND_DAYS,
  DATA_LIFECYCLE_STATS_KEYS,
  // B. metrics
  fetchDataLifecycleMetrics,
  unwrapDataLifecycleMetrics,
  metricsCleanupTimeAbsent,
  metricsArchiveTimeAbsent,
  metricsRowsDisagree,
  DATA_LIFECYCLE_METRICS_KEYS,
  // C. jobs
  fetchLifecycleJobs,
  unwrapLifecycleJobs,
  jobRunning,
  jobFailed,
  jobProgressMeaningless,
  LIFECYCLE_JOBS_HISTORY_LIMIT,
  LIFECYCLE_JOBS_KEYS,
  JOB_RUN_REQUIRED_KEYS,
  // D. blobs/top
  fetchBlobTop,
  unwrapBlobTop,
  blobModelUnknown,
  blobRowHasNoBody,
  blobTopLikelyTruncated,
  blobsTopLimitEffective,
  BLOB_TOP_LIMIT_DEFAULT,
  BLOB_TOP_LIMIT_MAX,
  type DataLifecycleStats,
} from '@/api/dataLifecycleStats'

/**
 * data-lifecycle 只读四条端点的不变量（2026-10-08，第六十六批）。
 *
 * ★ 本族最该被钉住的（按危险程度）：
 *   ① ★★★★ **`metrics` 恒不提供清理/归档时间** ——
 *      `LastCleanupAt`/`LastArchiveAt`（data_lifecycle_metrics.go:33-34）声明了，
 *      但 `handleDataLifecycleMetrics`（:39-86）**没有任何赋值点**，
 *      全仓 grep 只有那两行声明 ⇒ 键**永远不存在**。
 *      ⇒ 键缺失**不能**说成「从未清理过」。
 *   ② ★★★★ **`jobs.running` 空时是 `null` 而 `history` 空时是 `[]`** ——
 *      `listJobs` 的 `var running []*JobRun`（data_lifecycle_jobs.go:237）是
 *      nil 切片，无任务时 `append` 一次都不跑 ⇒ 编码成 `null`；
 *      而 `hs := make([]*JobRun, 0, …)`（:247）非 nil ⇒ 恒 `[]`。
 *      「刚重启没起过任务」是常态 ⇒ **`running: null` 是高频合法响应**。
 *   ③ ★★★★ **`metrics` 不做租户隔离但注册是 `admin` 档** ——
 *      SQL 是 `FROM request_logs`（:63）无 WHERE；文件头注释
 *      「super-admin only」（:9）与注册（handler.go:962）矛盾 ⇒ **以注册为准**。
 *   ④ ★★★★ **`stats` 的四个分段可为 `null`** ——
 *      逐行 `Scan` 出错走 `warnRowSkip` 后 `continue`
 *      （data_lifecycle.go:146-149）⇒ 该段整个是 `null`，不是 0。
 *   ⑤ ★★★ **`stats.total_rows` 是租户口径，`total_size_bytes` 是全表口径** ——
 *      后者是 `pg_total_relation_size('request_logs')`（:76），不带任何过滤。
 *   ⑥ ★★★ **一个端点里三种「查不出来」编码** ——
 *      总量失败 500 / 分段行失败 `null` / by_tenant·growth_trend 失败**静默 `[]`**。
 *   ⑦ ★★★ **`compression_rate` 被后端夹到 100**（:280-282）。
 *   ⑧ ★★ **`by_tenant` LIMIT 10、`growth_trend` LIMIT 7 且新的一天在前**。
 *   ⑨ ★★ **`blobs/top` 的 `total_bytes` 是 N 行合计**（:135 逐行累加）。
 *   ⑩ ★★ **`occurred_at` 秒级（:132 手写 Format）而 `collected_at` 纳秒级**（Go time.Time 直编）。
 *
 * 夹具说明：所有字段名逐字取自后端 struct 的 json tag；
 * 数值按后端 SQL 的计算式构造（`percent_of_total = rows/total*100`，
 * `size_bytes = pg_total_relation_size * rows/total`）。
 */

vi.mock('@/api/client', () => ({ req: vi.fn() }))

const reqMock = req as unknown as ReturnType<typeof vi.fn>

beforeEach(() => {
  reqMock.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

function stub(payload: unknown): void {
  reqMock.mockResolvedValue(payload)
}

function arg(n = 0): { method: string; path: string; body: unknown } {
  const c = reqMock.mock.calls[n]!
  return { method: c[0] as string, path: c[1] as string, body: c[2] }
}

function clone<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T
}

/** 造破损夹具用：绕开接口类型，直接当普通对象改键。 */
function raw(v: unknown): Record<string, unknown> {
  return v as Record<string, unknown>
}

/* ── 夹具：stats ──────────────────────────────────────────────────────── */

/**
 * data_lifecycle.go:65-87 + :144-177 + :197-295，tenant_admin 视角。
 * total_rows=1000 是**租户口径**；total_size_bytes=1073741824 是
 * `pg_total_relation_size('request_logs')` 的**全表**值（:76，不过滤）。
 * 四段 days 由 :165/168/171/174 写死为 7/23/60/999。
 */
function statsFixture(): DataLifecycleStats {
  return {
    total_rows: 1000,
    total_size_bytes: 1073741824,
    total_size_human: '1024 MB',
    hot_data: { rows: 500, size_bytes: 536870912, size_human: '512 MB', days: 7, percent_of_total: 50 },
    warm_data: { rows: 300, size_bytes: 322122547, size_human: '307 MB', days: 23, percent_of_total: 30 },
    cold_data: { rows: 150, size_bytes: 161061273, size_human: '154 MB', days: 60, percent_of_total: 15 },
    expired_data: { rows: 50, size_bytes: 53687091, size_human: '51 MB', days: 999, percent_of_total: 5 },
    by_tenant: [
      { tenant_id: 'acme', rows: 700, size_bytes: 751619276, size_human: '717 MB' },
      { tenant_id: 'default', rows: 300, size_bytes: 322122547, size_human: '307 MB' },
    ],
    // :245/:258 ORDER BY day DESC ⇒ 2026-10-08 在最前
    growth_trend: [
      { date: '2026-10-08', requests: 120, compressed: 60, compression_rate: 50 },
      { date: '2026-10-07', requests: 90, compressed: 45, compression_rate: 50 },
      { date: '2026-10-06', requests: 80, compressed: 32, compression_rate: 40 },
    ],
  }
}

/* ── 夹具：metrics ────────────────────────────────────────────────────── */

/**
 * data_lifecycle_metrics.go:22-35 + :51-75。
 * ★ 末尾两个时间键**不在夹具里**，因为后端从不对它们赋值。
 */
function metricsFixture() {
  return {
    total_rows: 50000,
    total_size_bytes: 21474836480,
    hot_data_rows: 20000,
    hot_data_size_bytes: 8589934592,
    warm_data_rows: 15000,
    warm_data_size_bytes: 6442450944,
    cold_data_rows: 10000,
    cold_data_size_bytes: 4294967296,
    expired_data_rows: 5000,
    expired_data_size_bytes: 2147483648,
  }
}

/* ── 夹具：jobs ───────────────────────────────────────────────────────── */

/**
 * data_lifecycle_jobs.go:87-114 造出的运行中任务：
 * `Status: queued`、`Message: "queued"`、`StartedAt` 已设、
 * `DurationMS` 为 0（无 omitempty ⇒ 恒存在）、
 * **无 `finished_at`**（指针 + omitempty，finalizeJob 之前不设）。
 */
function runningJobFixture() {
  return {
    run_id: 'job-promote_hot-1791400000-1',
    op: 'promote_hot',
    status: 'running',
    params: { table: 'request_logs_hot' },
    progress: { done: 120, total: 600, percent: 20, batches: 6, message: 'copying' },
    message: 'copying',
    started_at: '2026-10-08T01:00:00Z',
    heartbeat_at: '2026-10-08T01:04:00Z',
    duration_ms: 0,
    operator: 'admin@acme',
  }
}

/** :200-219 finalizeJob：入历史前必设 `finished_at` 与 `duration_ms`。 */
function historyJobFixture() {
  return {
    run_id: 'job-vacuum-1791300000-7',
    op: 'vacuum',
    status: 'succeeded',
    result: { freed_bytes: 1048576 },
    message: 'done',
    started_at: '2026-10-07T10:00:00Z',
    finished_at: '2026-10-07T10:00:42Z',
    duration_ms: 42000,
  }
}

/* ── 夹具：blobs/top ──────────────────────────────────────────────────── */

/**
 * data_lifecycle_blobs.go:99-148。
 * `occurred_at` 由 :132 `ts.UTC().Format(time.RFC3339)` 生成 ⇒ **秒级 + Z**；
 * `collected_at` 是 Go `time.Time` 直编 ⇒ **纳秒级**。
 * 第二行 `outbound_model` 为 NULL ⇒ COALESCE 成 `''` ⇒ `model` 键被 omitempty 丢掉。
 * 第三行无关联 body ⇒ 两个 `pg_column_size` 都 COALESCE 到 0。
 */
function blobTopFixture() {
  return {
    rows: [
      {
        request_id: 'req-0001',
        session_key: 'sess-a',
        tenant_id: 'acme',
        occurred_at: '2026-10-08T01:30:00Z',
        request_body_bytes: 120000,
        outbound_body_bytes: 80000,
        total_bytes: 200000,
        total_human: '195 KB',
        model: 'claude-opus-5',
      },
      {
        request_id: 'req-0002',
        session_key: '',
        tenant_id: '',
        occurred_at: '2026-10-08T00:10:00Z',
        request_body_bytes: 50000,
        outbound_body_bytes: 5000,
        total_bytes: 55000,
        total_human: '54 KB',
      },
      {
        request_id: 'req-0003',
        session_key: 'sess-b',
        tenant_id: 'acme',
        occurred_at: '2026-10-07T23:00:00Z',
        request_body_bytes: 0,
        outbound_body_bytes: 0,
        total_bytes: 0,
        total_human: '0 B',
      },
    ],
    total_bytes: 255000,
    total_human: '249 KB',
    collected_at: '2026-10-08T01:30:00.123456789Z',
  }
}

/* ═══════════════════════════════════════════════════════════════════════
 * A. stats
 * ═══════════════════════════════════════════════════════════════════════ */

describe('data-lifecycle / stats 解包与形状守卫', () => {
  it('data_lifecycle.go:13-23 顶层十个键齐全时正常解包', () => {
    const s = unwrapDataLifecycleStats(statsFixture())
    expect(s.total_rows).toBe(1000)
    expect(s.total_size_human).toBe('1024 MB')
    expect(s.by_tenant).toHaveLength(2)
    expect(s.growth_trend).toHaveLength(3)
  })

  it('fetchDataLifecycleStats 打 GET /api/admin/data-lifecycle/stats 且不带 body', async () => {
    stub(statsFixture())
    await fetchDataLifecycleStats()
    expect(arg().method).toBe('GET')
    expect(arg().path).toBe('/api/admin/data-lifecycle/stats')
    expect(arg().body).toBeUndefined()
  })

  it('data_lifecycle.go:13-22 必检顶层键恰好九个，与夹具一一对应', () => {
    const f = statsFixture()
    expect(DATA_LIFECYCLE_STATS_KEYS).toHaveLength(9)
    // 少一个必检键，真实响应就会在别处被误放行 ⇒ 这里钉住「一个不多一个不少」
    for (const k of DATA_LIFECYCLE_STATS_KEYS) {
      expect(Object.keys(f)).toContain(k)
    }
  })

  it('data_lifecycle.go:20 四个分段可为 null：null 段被接受而不是抛错', () => {
    const f = statsFixture()
    f.cold_data = null
    const s = unwrapDataLifecycleStats(f)
    expect(s.cold_data).toBeNull()
  })

  it('data_lifecycle.go:17-20 四个段键都没有 omitempty ⇒ 真实响应里键恒存在（值为 null）', () => {
    const f = statsFixture()
    expect(Object.keys(f)).toEqual(expect.arrayContaining([
      'hot_data', 'warm_data', 'cold_data', 'expired_data',
    ]))
  })

  it('段键整体缺失是契约破损（后端不可能这样发）⇒ 抛错', () => {
    const f = raw(statsFixture())
    delete f.warm_data
    expect(() => unwrapDataLifecycleStats(f)).toThrow(/warm_data/)
  })

  it('段既不是对象也不是 null（如数字）⇒ 抛错并点名该段', () => {
    const f = statsFixture() as unknown as Record<string, unknown>
    f.hot_data = 42
    expect(() => unwrapDataLifecycleStats(f)).toThrow(/hot_data 不是对象也不是 null/)
  })

  it('data_lifecycle.go:25-31 段缺子键 ⇒ 抛错且错误信息列出缺哪个键', () => {
    const f = statsFixture()
    delete raw(f.hot_data).percent_of_total
    expect(() => unwrapDataLifecycleStats(f)).toThrow(/percent_of_total/)
  })

  it('顶层缺键 ⇒ 抛错并给出缺失键名', () => {
    const f = raw(statsFixture())
    delete f.total_size_human
    expect(() => unwrapDataLifecycleStats(f)).toThrow(/total_size_human/)
  })

  it('拿到 dashboardapi 信封形状 ⇒ 明确报错而不是把信封当数据', () => {
    expect(() => unwrapDataLifecycleStats({ success: true, timestamp: 1, data: {} }))
      .toThrow(/dashboardapi 信封形状/)
  })

  it('非对象响应（数组/null/字符串）⇒ 报出实际形状', () => {
    expect(() => unwrapDataLifecycleStats([])).toThrow(/期望裸对象，实得 array/)
    expect(() => unwrapDataLifecycleStats(null)).toThrow(/期望裸对象，实得 null/)
    expect(() => unwrapDataLifecycleStats('nope')).toThrow(/期望裸对象，实得 string/)
  })

  it('by_tenant 不是数组 ⇒ 抛错', () => {
    const f = statsFixture() as unknown as Record<string, unknown>
    f.by_tenant = { tenant_id: 'acme' }
    expect(() => unwrapDataLifecycleStats(f)).toThrow(/by_tenant 不是数组/)
  })

  it('by_tenant[0] 缺子键 ⇒ 抛错并带下标', () => {
    const f = statsFixture()
    delete raw(f.by_tenant[0]).size_human
    expect(() => unwrapDataLifecycleStats(f)).toThrow(/by_tenant\[0\]/)
  })

  it('growth_trend[1] 缺子键 ⇒ 抛错并带下标', () => {
    const f = statsFixture()
    delete raw(f.growth_trend[1]).compression_rate
    expect(() => unwrapDataLifecycleStats(f)).toThrow(/growth_trend\[1\]/)
  })

  it('by_tenant 元素不是对象 ⇒ 抛错', () => {
    const f = statsFixture() as unknown as Record<string, unknown>
    f.by_tenant = ['acme']
    expect(() => unwrapDataLifecycleStats(f)).toThrow(/by_tenant\[0\] 不是对象/)
  })
})

describe('stats 分段语义：null ≠ 0', () => {
  it('data_lifecycle.go:146-149 某段 null ⇒ segmentUnavailable 为真', () => {
    const s = unwrapDataLifecycleStats({ ...statsFixture(), expired_data: null })
    expect(segmentUnavailable(s, 'expired_data')).toBe(true)
  })

  it('段为 0 行（rows:0）≠ 不可用：段可用但内容为 0', () => {
    const f = statsFixture()
    f.expired_data = { rows: 0, size_bytes: 0, size_human: '0 bytes', days: 999, percent_of_total: 0 }
    const s = unwrapDataLifecycleStats(f)
    expect(segmentUnavailable(s, 'expired_data')).toBe(false)
    expect(segmentUnavailable(s, 'hot_data')).toBe(false)
  })

  it('任一段缺失 ⇒ anySegmentUnavailable 为真', () => {
    const s = unwrapDataLifecycleStats({ ...statsFixture(), cold_data: null })
    expect(anySegmentUnavailable(s)).toBe(true)
  })

  it('四段齐全 ⇒ anySegmentUnavailable 为假', () => {
    expect(anySegmentUnavailable(unwrapDataLifecycleStats(statsFixture()))).toBe(false)
  })

  it('availableSegments 只列出真正可展示的分段', () => {
    const s = unwrapDataLifecycleStats({ ...statsFixture(), warm_data: null })
    expect(availableSegments(s)).toEqual(['hot_data', 'cold_data', 'expired_data'])
  })

  it('缺段时 segmentPercentTotal 返回 null 而不是残缺百分数', () => {
    const s = unwrapDataLifecycleStats({ ...statsFixture(), cold_data: null })
    expect(segmentPercentTotal(s)).toBeNull()
  })

  it('四段齐全时 segmentPercentTotal 等于四段占比之和（50+30+15+5）', () => {
    expect(segmentPercentTotal(unwrapDataLifecycleStats(statsFixture()))).toBeCloseTo(100, 10)
  })
})

describe('stats 口径：分母、边界重复、截断', () => {
  it('data_lifecycle.go:151-154 total_rows=0 ⇒ 四个占比都是 0 且 percentMeaningless 为真', () => {
    const f = statsFixture()
    f.total_rows = 0
    for (const k of ['hot_data', 'warm_data', 'cold_data', 'expired_data'] as const) {
      raw(f[k]).percent_of_total = 0
    }
    const s = unwrapDataLifecycleStats(f)
    expect(percentMeaningless(s)).toBe(true)
    expect((s.hot_data as { percent_of_total: number }).percent_of_total).toBe(0)
  })

  it('total_rows>0 ⇒ percentMeaningless 为假', () => {
    expect(percentMeaningless(unwrapDataLifecycleStats(statsFixture()))).toBe(false)
  })

  it('四段之和等于 total_rows ⇒ segmentRowsDisagree 为假', () => {
    expect(segmentRowsDisagree(unwrapDataLifecycleStats(statsFixture()))).toBe(false)
  })

  it('data_lifecycle.go:116/124 30 天双侧闭区间 ⇒ 边界行被数两次，和会超过 total_rows', () => {
    const f = statsFixture()
    f.total_rows = 999
    const s = unwrapDataLifecycleStats(f)
    expect(segmentRowsDisagree(s)).toBe(true)
  })

  it('有段缺失时不做「和 vs 总量」的判断（缺段不代表多算了）', () => {
    const s = unwrapDataLifecycleStats({ ...statsFixture(), expired_data: null })
    expect(segmentRowsDisagree(s)).toBe(false)
  })

  it('data_lifecycle.go:195 LIMIT 10 ⇒ by_tenant 满 10 条即视为截断', () => {
    // ⚠️ 夹具条数必须**硬写 10**（后端 SQL 的字面量），不能用 BY_TENANT_LIMIT 造 ——
    //   那样改常量会两边一起变，判据就成了自指的恒真（第六十六批变异 #22 实测漏网）。
    const f = statsFixture()
    f.by_tenant = Array.from({ length: 10 }, (_, i) => ({
      tenant_id: `t${i}`, rows: 1, size_bytes: 1, size_human: '1 kB',
    }))
    expect(byTenantTruncated(unwrapDataLifecycleStats(f))).toBe(true)
  })

  it('常量必须等于后端字面量：BY_TENANT_LIMIT === 10', () => {
    expect(BY_TENANT_LIMIT).toBe(10)
  })

  it('by_tenant 只有 2 条 ⇒ 未截断（负控）', () => {
    expect(byTenantTruncated(unwrapDataLifecycleStats(statsFixture()))).toBe(false)
  })

  it('data_lifecycle.go:246/259 LIMIT 7 ⇒ growth_trend 满 7 条即视为截断', () => {
    // ⚠️ 同上：硬写 7，不用 GROWTH_TREND_DAYS 造夹具（否则改常量测不出来）。
    const f = statsFixture()
    f.growth_trend = Array.from({ length: 7 }, (_, i) => ({
      date: `2026-10-0${i + 1}`, requests: 1, compressed: 0, compression_rate: 0,
    }))
    expect(growthTrendTruncated(unwrapDataLifecycleStats(f))).toBe(true)
  })

  it('常量必须等于后端字面量：GROWTH_TREND_DAYS === 7', () => {
    expect(GROWTH_TREND_DAYS).toBe(7)
  })

  it('growth_trend 只有 3 天 ⇒ 未截断（负控，缺这条则判据无牙）', () => {
    expect(growthTrendTruncated(unwrapDataLifecycleStats(statsFixture()))).toBe(false)
  })

  it('data_lifecycle.go:245/258 ORDER BY day DESC ⇒ 返回序新的一天在前', () => {
    expect(growthTrendIsNewestFirst(unwrapDataLifecycleStats(statsFixture()))).toBe(true)
  })

  it('若后端改成升序，growthTrendIsNewestFirst 必须转为假（折线图会倒着走）', () => {
    const f = statsFixture()
    f.growth_trend.reverse()
    expect(growthTrendIsNewestFirst(unwrapDataLifecycleStats(f))).toBe(false)
  })

  it('空趋势 / 单点趋势都算「新在前」', () => {
    const f = statsFixture()
    f.growth_trend = []
    expect(growthTrendIsNewestFirst(unwrapDataLifecycleStats(f))).toBe(true)
    f.growth_trend = [{ date: '2026-10-08', requests: 1, compressed: 0, compression_rate: 0 }]
    expect(growthTrendIsNewestFirst(unwrapDataLifecycleStats(f))).toBe(true)
  })
})

describe('stats 失败编码：三种「查不出来」', () => {
  it('data_lifecycle.go:197-201 by_tenant 查询失败是 non-fatal ⇒ 客户端只看到 []', () => {
    const f = statsFixture()
    f.by_tenant = []
    const s = unwrapDataLifecycleStats(f)
    expect(s.by_tenant).toEqual([])
  })

  it('列表为空时 listEmptyIsAmbiguous 为真（[] 可能是查询失败，也可能是真无数据）', () => {
    const f = statsFixture()
    f.growth_trend = []
    expect(listEmptyIsAmbiguous(unwrapDataLifecycleStats(f))).toBe(true)
  })

  it('两个列表都非空 ⇒ 不必用二义措辞', () => {
    expect(listEmptyIsAmbiguous(unwrapDataLifecycleStats(statsFixture()))).toBe(false)
  })

  it('data_lifecycle.go:280-282 compression_rate 被夹到 100：compressed>requests 且 rate=100 ⇒ 饱和', () => {
    expect(compressionRateSaturated({ date: '2026-10-08', requests: 10, compressed: 14, compression_rate: 100 })).toBe(true)
  })

  it('rate=100 但 compressed<=requests ⇒ 不是夹出来的', () => {
    expect(compressionRateSaturated({ date: '2026-10-08', requests: 10, compressed: 10, compression_rate: 100 })).toBe(false)
  })

  it('普通压缩率不是饱和', () => {
    expect(compressionRateSaturated({ date: '2026-10-08', requests: 100, compressed: 50, compression_rate: 50 })).toBe(false)
  })

  it('data_lifecycle.go:165/168/171/174 days 写死为 7/23/60/999，不是区间上界', () => {
    expect(SEGMENT_DAYS).toEqual({ hot_data: 7, warm_data: 23, cold_data: 60, expired_data: 999 })
  })

  it('夹具里四段 days 与 SEGMENT_DAYS 一致（夹具逐字照抄后端写死值）', () => {
    const s = unwrapDataLifecycleStats(statsFixture())
    for (const k of Object.keys(SEGMENT_DAYS) as (keyof typeof SEGMENT_DAYS)[]) {
      expect((s[k] as { days: number }).days).toBe(SEGMENT_DAYS[k])
    }
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * B. metrics
 * ═══════════════════════════════════════════════════════════════════════ */

describe('data-lifecycle / metrics：★ 恒不提供清理/归档时间', () => {
  it('data_lifecycle_metrics.go:33-34 真实响应里没有 last_cleanup_at ⇒ 键缺失而非 null', () => {
    const m = unwrapDataLifecycleMetrics(metricsFixture())
    expect('last_cleanup_at' in m).toBe(false)
    expect(metricsCleanupTimeAbsent(m)).toBe(true)
  })

  it('同上，last_archive_at 键也恒不存在', () => {
    const m = unwrapDataLifecycleMetrics(metricsFixture())
    expect('last_archive_at' in m).toBe(false)
    expect(metricsArchiveTimeAbsent(m)).toBe(true)
  })

  it('前向兼容：将来后端真填上时间键，客户端照单全收不抛错', () => {
    const m = unwrapDataLifecycleMetrics({
      ...metricsFixture(),
      last_cleanup_at: '2026-10-08T01:00:00Z',
      last_archive_at: '2026-10-07T01:00:00Z',
    })
    expect(metricsCleanupTimeAbsent(m)).toBe(false)
    expect(metricsArchiveTimeAbsent(m)).toBe(false)
    expect(m.last_cleanup_at).toBe('2026-10-08T01:00:00Z')
  })

  it('时间键显式为 null 时不算「提供了」', () => {
    const m = unwrapDataLifecycleMetrics({ ...metricsFixture(), last_cleanup_at: null })
    expect(metricsCleanupTimeAbsent(m)).toBe(false)
  })

  it('十个必检键齐备时正常解包', () => {
    expect(DATA_LIFECYCLE_METRICS_KEYS).toHaveLength(10)
    expect(unwrapDataLifecycleMetrics(metricsFixture()).total_rows).toBe(50000)
  })

  it('缺任一必检键 ⇒ 抛错并点名', () => {
    const f = clone(metricsFixture()) as Record<string, unknown>
    delete f.cold_data_size_bytes
    expect(() => unwrapDataLifecycleMetrics(f)).toThrow(/cold_data_size_bytes/)
  })

  it('metrics 拿到信封形状 ⇒ 拒绝', () => {
    expect(() => unwrapDataLifecycleMetrics({ success: true, timestamp: 2, data: metricsFixture() }))
      .toThrow(/dashboardapi 信封形状/)
  })

  it('fetchDataLifecycleMetrics 打 GET /api/admin/data-lifecycle/metrics', async () => {
    stub(metricsFixture())
    await fetchDataLifecycleMetrics()
    expect(arg().method).toBe('GET')
    expect(arg().path).toBe('/api/admin/data-lifecycle/metrics')
  })

  it('四段行数之和等于 total_rows ⇒ metricsRowsDisagree 为假', () => {
    expect(metricsRowsDisagree(unwrapDataLifecycleMetrics(metricsFixture()))).toBe(false)
  })

  it('四段之和超过 total_rows ⇒ metricsRowsDisagree 为真（同一 30 天边界成因）', () => {
    const f = { ...metricsFixture(), total_rows: 49999 }
    expect(metricsRowsDisagree(unwrapDataLifecycleMetrics(f))).toBe(true)
  })

  it('★ metrics 与 stats 同名 total_rows 覆盖不同行集，客户端不得跨端点合并', () => {
    // metrics 读裸表 request_logs（:63），stats 读当月视图（:78）。
    // 这里的保证是**静态**的：本模块没有任何导出函数同时接受两个族的参数，
    // 所以「拿 stats.total_rows 减 metrics.total_rows」在类型层面就写不出来。
    // 运行期只能钉住各自的口径值，跨端点的算术留给被禁止的用法之外的地方。
    const m = unwrapDataLifecycleMetrics(metricsFixture())
    const s = unwrapDataLifecycleStats(statsFixture())
    expect(m.total_rows).toBe(50000) // 裸表口径
    expect(s.total_rows).toBe(1000)  // 租户 + 当月视图口径
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * C. jobs
 * ═══════════════════════════════════════════════════════════════════════ */

describe('data-lifecycle / jobs：★ running 空时是 null', () => {
  it('data_lifecycle_jobs.go:237 running 是 nil 切片 ⇒ 编码为 null，必须被接受', () => {
    const r = unwrapLifecycleJobs({ running: null, history: [] })
    expect(r.running).toEqual([])
  })

  it('这是常态而非边角：刚重启、一个任务都没起过时就是 running:null', () => {
    const r = unwrapLifecycleJobs({ running: null, history: [] })
    expect(r.running).toHaveLength(0)
    expect(r.history).toHaveLength(0)
  })

  it('history 空时是 []（make 非 nil 切片，:247），同样接受', () => {
    const r = unwrapLifecycleJobs({ running: [], history: [] })
    expect(r.history).toEqual([])
  })

  it('history 为 null 也不抛错（宽容但归一）', () => {
    expect(unwrapLifecycleJobs({ running: null, history: null }).history).toEqual([])
  })

  it('两键都缺失 ⇒ 抛错并点名', () => {
    expect(() => unwrapLifecycleJobs({ running: [] })).toThrow(/history/)
  })

  it('running 是字符串 ⇒ 仍然抛错（null 合法但形状错不合法）', () => {
    expect(() => unwrapLifecycleJobs({ running: 'none', history: [] })).toThrow(/running 不是数组也不是 null/)
  })

  it('running[0] 缺 duration_ms ⇒ 抛错（该键无 omitempty，恒存在）', () => {
    const j = runningJobFixture() as Record<string, unknown>
    delete j.duration_ms
    expect(() => unwrapLifecycleJobs({ running: [j], history: [] })).toThrow(/duration_ms/)
  })

  it('JOB_RUN_REQUIRED_KEYS 只钉四个无 omitempty 的键', () => {
    expect(JOB_RUN_REQUIRED_KEYS).toEqual(['run_id', 'op', 'status', 'duration_ms'])
    expect(LIFECYCLE_JOBS_KEYS).toEqual(['running', 'history'])
  })

  it('running[0] 不是对象 ⇒ 抛错', () => {
    expect(() => unwrapLifecycleJobs({ running: ['job-1'], history: [] }))
      .toThrow(/running\[0\] 不是对象/)
  })

  it('jobs 拿到信封形状 ⇒ 拒绝', () => {
    expect(() => unwrapLifecycleJobs({ success: true, timestamp: 3, data: {} }))
      .toThrow(/dashboardapi 信封形状/)
  })

  it('data_lifecycle_jobs.go:322 fetchLifecycleJobs 刻意不发 limit', async () => {
    stub({ running: null, history: [] })
    await fetchLifecycleJobs()
    expect(arg().path).toBe('/api/admin/data-lifecycle/jobs')
    expect(arg().path).not.toContain('limit')
  })

  it('★ 后端硬编码 listJobs(50) 且内部再钳到 maxKeep ⇒ 上限恒为 50', () => {
    expect(LIFECYCLE_JOBS_HISTORY_LIMIT).toBe(50)
  })
})

describe('jobs 任务状态语义', () => {
  it('data_lifecycle_jobs.go:39-54 运行中任务无 finished_at ⇒ jobRunning 为真', () => {
    const r = unwrapLifecycleJobs({ running: [runningJobFixture()], history: [] })
    expect(jobRunning(r.running[0]!)).toBe(true)
  })

  it('历史任务必有 finished_at ⇒ jobRunning 为假', () => {
    const r = unwrapLifecycleJobs({ running: [], history: [historyJobFixture()] })
    expect(jobRunning(r.history[0]!)).toBe(false)
  })

  it('data_lifecycle_jobs.go:180 失败任务带非空 error ⇒ jobFailed 为真', () => {
    const failed = { ...historyJobFixture(), status: 'failed', error: 'panic: boom' }
    const r = unwrapLifecycleJobs({ running: null, history: [failed] })
    expect(jobFailed(r.history[0]!)).toBe(true)
  })

  it('error 为空串 ⇒ 不算失败（omitempty 语义：空串等于没填）', () => {
    const j = { ...historyJobFixture(), error: '' }
    const r = unwrapLifecycleJobs({ running: null, history: [j] })
    expect(jobFailed(r.history[0]!)).toBe(false)
  })

  it('成功任务没有 error 键 ⇒ jobFailed 为假', () => {
    const r = unwrapLifecycleJobs({ running: null, history: [historyJobFixture()] })
    expect(jobFailed(r.history[0]!)).toBe(false)
  })

  it('data_lifecycle_jobs.go:127-130 progress.total=0 ⇒ percent 恒 0，判为无意义', () => {
    const j = { ...runningJobFixture(), progress: { done: 0, total: 0, percent: 0, batches: 0 } }
    const r = unwrapLifecycleJobs({ running: [j], history: [] })
    expect(jobProgressMeaningless(r.running[0]!)).toBe(true)
  })

  it('没有 progress 键 ⇒ 也算无意义（不能显示 0%）', () => {
    const { progress, ...noProgress } = runningJobFixture()
    void progress
    const r = unwrapLifecycleJobs({ running: [noProgress], history: [] })
    expect(jobProgressMeaningless(r.running[0]!)).toBe(true)
  })

  it('正常进度 ⇒ 有意义', () => {
    const r = unwrapLifecycleJobs({ running: [runningJobFixture()], history: [] })
    expect(jobProgressMeaningless(r.running[0]!)).toBe(false)
  })

  it('data_lifecycle_jobs.go:31-37 状态取值封闭集（queued/running/succeeded/failed/cancelled）', () => {
    const statuses = ['queued', 'running', 'succeeded', 'failed', 'cancelled']
    for (const status of statuses) {
      const r = unwrapLifecycleJobs({
        running: [{ ...runningJobFixture(), status }],
        history: [],
      })
      expect(r.running[0]!.status).toBe(status)
    }
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * D. blobs/top
 * ═══════════════════════════════════════════════════════════════════════ */

describe('data-lifecycle / blobs/top', () => {
  it('data_lifecycle_blobs.go:45-49 正常解包三行', () => {
    const r = unwrapBlobTop(blobTopFixture())
    expect(r.rows).toHaveLength(3)
    expect(r.total_bytes).toBe(255000)
  })

  it('data_lifecycle_blobs.go:133 后端在 Go 里现算 total_bytes ⇒ 逐行一致性成立', () => {
    const r = unwrapBlobTop(blobTopFixture())
    for (const row of r.rows) {
      expect(row.total_bytes).toBe(row.request_body_bytes + row.outbound_body_bytes)
    }
  })

  it('逐行一致性校验不是恒真：total_bytes 对不上时抛错', () => {
    const f = clone(blobTopFixture())
    f.rows[0]!.total_bytes = 999
    expect(() => unwrapBlobTop(f)).toThrow(/rows\[0\] total_bytes 与两个分量之和不符/)
  })

  it('row 缺 total_human ⇒ 抛错并带下标', () => {
    const f = clone(blobTopFixture()) as unknown as { rows: Record<string, unknown>[] }
    delete f.rows[1]!.total_human
    expect(() => unwrapBlobTop(f)).toThrow(/rows\[1\]/)
  })

  it('rows 不是数组 ⇒ 抛错', () => {
    const f = clone(blobTopFixture()) as unknown as Record<string, unknown>
    f.rows = null
    expect(() => unwrapBlobTop(f)).toThrow(/rows 不是数组/)
  })

  it('★ rows 为 null 不可接受：blobs 侧后端预置的是 make 非 nil（:120），与 jobs.running 相反', () => {
    const f = clone(blobTopFixture()) as unknown as Record<string, unknown>
    f.rows = null
    expect(() => unwrapBlobTop(f)).toThrow(/rows 不是数组/)
  })

  it('顶层缺 collected_at ⇒ 抛错', () => {
    const f = clone(blobTopFixture()) as Record<string, unknown>
    delete f.collected_at
    expect(() => unwrapBlobTop(f)).toThrow(/collected_at/)
  })

  it('blobs/top 拿到信封形状 ⇒ 拒绝（虽用 writeJSON，但不产生信封）', () => {
    expect(() => unwrapBlobTop({ success: true, timestamp: 4, data: blobTopFixture() }))
      .toThrow(/dashboardapi 信封形状/)
  })

  it('data_lifecycle_blobs.go:41/103 model 缺键 ⇒ blobModelUnknown 为真', () => {
    const r = unwrapBlobTop(blobTopFixture())
    expect(blobModelUnknown(r.rows[1]!)).toBe(true)
  })

  it('model 为空串 ⇒ 同样是「没有模型名」', () => {
    expect(blobModelUnknown({ ...blobTopFixture().rows[0]!, model: '' })).toBe(true)
  })

  it('model 有值 ⇒ 不算未知', () => {
    const r = unwrapBlobTop(blobTopFixture())
    expect(blobModelUnknown(r.rows[0]!)).toBe(false)
  })

  it('data_lifecycle_blobs.go:105-106 无 body 的行两个分量都是 0 ⇒ blobRowHasNoBody', () => {
    const r = unwrapBlobTop(blobTopFixture())
    expect(blobRowHasNoBody(r.rows[2]!)).toBe(true)
    expect(blobRowHasNoBody(r.rows[0]!)).toBe(false)
  })

  it('data_lifecycle_blobs.go:105-106 session_key/tenant_id 的 COALESCE 兜底是空串而非 null', () => {
    const r = unwrapBlobTop(blobTopFixture())
    expect(r.rows[1]!.session_key).toBe('')
    expect(r.rows[1]!.tenant_id).toBe('')
  })

  it('data_lifecycle_blobs.go:132 occurred_at 秒级 vs :147 collected_at 纳秒级', () => {
    const r = unwrapBlobTop(blobTopFixture())
    expect(r.rows[0]!.occurred_at).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/)
    expect(r.collected_at).toMatch(/\.\d+Z$/)
  })
})

describe('blobs/top limit 越界口径（静默回落 20）', () => {
  it('不传 limit ⇒ 后端默认 20', () => {
    expect(blobsTopLimitEffective(undefined)).toBe(BLOB_TOP_LIMIT_DEFAULT)
  })

  it('合法值原样采用', () => {
    expect(blobsTopLimitEffective(20)).toBe(20)
    expect(blobsTopLimitEffective(1)).toBe(1)
    expect(blobsTopLimitEffective(BLOB_TOP_LIMIT_MAX)).toBe(200)
  })

  it('0 / 负数 / 超 200 ⇒ 全部静默回落 20（不是回落 50）', () => {
    expect(blobsTopLimitEffective(0)).toBe(20)
    expect(blobsTopLimitEffective(-5)).toBe(20)
    expect(blobsTopLimitEffective(201)).toBe(20)
    expect(blobsTopLimitEffective(100000)).toBe(20)
  })

  it('非整数（Atoi 失败）⇒ 同样回落 20', () => {
    expect(blobsTopLimitEffective(1.5)).toBe(20)
    expect(blobsTopLimitEffective(Number.NaN)).toBe(20)
  })

  it('撞到 limit ⇒ blobTopLikelyTruncated 为真（total 一定只是部分和）', () => {
    const f = clone(blobTopFixture())
    f.rows = Array.from({ length: 20 }, (_, i) => ({ ...f.rows[0]!, request_id: `r${i}` }))
    expect(blobTopLikelyTruncated(unwrapBlobTop(f), 20)).toBe(true)
  })

  it('未取满 ⇒ 不能反推「表里没有更大的」，但也不能断言已截断', () => {
    expect(blobTopLikelyTruncated(unwrapBlobTop(blobTopFixture()), 20)).toBe(false)
  })

  it('不传 limit 时按默认 20 判截断', () => {
    expect(blobTopLikelyTruncated(unwrapBlobTop(blobTopFixture()))).toBe(false)
  })

  it('fetchBlobTop 无 query 时不带 ?limit', async () => {
    stub(blobTopFixture())
    await fetchBlobTop()
    expect(arg().path).toBe('/api/admin/data-lifecycle/blobs/top')
  })

  it('fetchBlobTop 带 limit 时拼 ?limit=N（原样发，不做客户端钳位）', async () => {
    stub(blobTopFixture())
    await fetchBlobTop({ limit: 500 })
    expect(arg().path).toBe('/api/admin/data-lifecycle/blobs/top?limit=500')
  })
})
