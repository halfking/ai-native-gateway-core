import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchSystemMonitorStats,
  fetchRecentProbeRuns,
  recentRunsTruncated,
  statsAccountedRuns,
  statusIsUnaccounted,
  statusCountsAsFailed,
  runTone,
  runDurationMs,
  PROBE_RUN_STATUSES,
  STATS_FAILED_STATUSES,
  STATS_CONCURRENCY_FALLBACK,
  RECENT_RUNS_LIMIT_DEFAULT,
  RECENT_RUNS_LIMIT_MAX,
  PROBE_RUNS_ONLY_SETTLED,
  STATS_MAY_NOT_BE_WIRED,
  STATS_1H_COUNTS_MAY_BE_UNAVAILABLE,
  type SystemMonitorStats,
  type SystemProbeRun,
} from './systemMonitor'

/**
 * 系统监控面的契约测试（2026-10-06）。
 *
 * 四条重点：
 * 1. ★★★ `expired` 是**后端三个计数都没统计**的状态 ⇒ 三项之和**不是**总数；
 * 2. ★★★ 1 小时四个计数的 Scan **错误被丢弃** ⇒ 全 0 可能是查询失败；
 * 3. ★★ `total` 取自 `len(out)`（跳行后）⇒ 不是数据库计数；
 * 4. ★★ 清单只含**已结束**的运行（两处写入方都在结束后才 INSERT）。
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
function okStats(): void {
  fetchMock.mockResolvedValueOnce(
    jsonResponse({
      queue_size: 3,
      running_size: 1,
      in_fallback: false,
      monitor_concurrency: 8,
      completed_total_1h: 10,
      failed_total_1h: 2,
      skipped_total_1h: 1,
      total_tokens_1h: 5000,
      snapshot_at: '2026-10-07T10:00:00Z',
    }),
  )
}
function okRuns(): void {
  fetchMock.mockResolvedValueOnce(jsonResponse({ runs: [], total: 0, limit: 50 }))
}

function stats(over: Partial<SystemMonitorStats> = {}): SystemMonitorStats {
  return {
    queue_size: 0,
    running_size: 0,
    in_fallback: false,
    monitor_concurrency: STATS_CONCURRENCY_FALLBACK,
    completed_total_1h: 0,
    failed_total_1h: 0,
    skipped_total_1h: 0,
    total_tokens_1h: 0,
    snapshot_at: '2026-10-07T10:00:00Z',
    ...over,
  }
}

function run(over: Partial<SystemProbeRun> = {}): SystemProbeRun {
  return {
    id: 1,
    task_id: 9,
    task_type: 'chat',
    automaticity: 'mandatory',
    credential_id: 7,
    raw_model: 'gpt-4o',
    source: 'system_monitor',
    worker_id: 'w1',
    status: 'success',
    attempt: 1,
    err_code: '',
    skip_reason: '',
    started_at: '2026-10-07T09:59:00Z',
    finished_at: '2026-10-07T10:00:00Z',
    recent_request_id: 'req-1',
    ...over,
  }
}

describe('URL 与参数', () => {
  it('stats 无参数 ⇒ 裸路径', async () => {
    okStats()
    await fetchSystemMonitorStats()
    expect(lastUrl()).toBe('/api/admin/system-monitor/stats')
  })

  it('recent-runs 无参数 ⇒ 裸路径', async () => {
    okRuns()
    await fetchRecentProbeRuns()
    expect(lastUrl()).toBe('/api/admin/system-monitor/recent-runs')
  })

  it('★ limit=100 发得出去', async () => {
    okRuns()
    await fetchRecentProbeRuns({ limit: 100 })
    expect(lastUrl()).toContain('limit=100')
  })

  it('★ limit=201 **不发**（上限 200，越界静默回落 50）', async () => {
    okRuns()
    await fetchRecentProbeRuns({ limit: RECENT_RUNS_LIMIT_MAX + 1 })
    expect(lastUrl()).not.toContain('limit=')
  })

  it('★ limit=0 / 负数 / NaN 一律不发', async () => {
    for (const limit of [0, -1, NaN]) {
      fetchMock.mockResolvedValueOnce(jsonResponse({ runs: [], total: 0, limit: 50 }))
      await fetchRecentProbeRuns({ limit })
      expect(lastUrl()).not.toContain('limit=')
    }
  })

  it('后端常量：默认 50 / 上限 200（与 sessions 的 50/500、online 的 20/100 都不同）', () => {
    expect(RECENT_RUNS_LIMIT_DEFAULT).toBe(50)
    expect(RECENT_RUNS_LIMIT_MAX).toBe(200)
  })
})

describe('★★★ 判据 1：expired 不被任何计数统计', () => {
  it('★ 六个合法 status（CHECK 约束）', () => {
    expect([...PROBE_RUN_STATUSES]).toEqual([
      'success',
      'failed',
      'expired',
      'skipped',
      'timeout',
      'network_error',
    ])
  })

  it('★ 后端 failed 口径**不含 expired**', () => {
    expect([...STATS_FAILED_STATUSES]).not.toContain('expired')
    expect(statusCountsAsFailed('expired')).toBe(false)
    expect(statusCountsAsFailed('timeout')).toBe(true)
    expect(statusCountsAsFailed('network_error')).toBe(true)
    expect(statusCountsAsFailed('failed')).toBe(true)
  })

  it('★★ expired 是唯一「未被统计」的状态', () => {
    expect(statusIsUnaccounted('expired')).toBe(true)
    for (const s of PROBE_RUN_STATUSES) {
      if (s !== 'expired') expect(statusIsUnaccounted(s)).toBe(false)
    }
  })

  it('★★ 三项之和**不是**总数（一条 expired 只出现在明细、不在任何计数里）', () => {
    const s = stats({ completed_total_1h: 10, failed_total_1h: 2, skipped_total_1h: 1 })
    // 若 UI 把它当「一小时 13 次」，而库里其实还有 expired 行，就是编造
    expect(statsAccountedRuns(s)).toBe(13)
    expect(statusIsUnaccounted('expired')).toBe(true)
  })

  it('★ expired 单独一档色（既非成功也非失败）', () => {
    expect(runTone(run({ status: 'expired' }))).toBe('warning')
  })

  it('success / failed / skipped 各自档位', () => {
    expect(runTone(run({ status: 'success' }))).toBe('success')
    expect(runTone(run({ status: 'failed' }))).toBe('danger')
    expect(runTone(run({ status: 'timeout' }))).toBe('danger')
    expect(runTone(run({ status: 'network_error' }))).toBe('danger')
    expect(runTone(run({ status: 'skipped' }))).toBe('muted')
  })

  it('★ 未知 status ⇒ muted（不崩、不猜）', () => {
    expect(runTone(run({ status: 'brand_new' }))).toBe('muted')
    expect(runTone(null)).toBe('muted')
  })
})

describe('★★★ 判据 2：0 可能是「不知道」', () => {
  it('★ 三个「可能未知」标记都在', () => {
    expect(STATS_MAY_NOT_BE_WIRED).toBe(true)
    expect(STATS_1H_COUNTS_MAY_BE_UNAVAILABLE).toBe(true)
    expect(STATS_CONCURRENCY_FALLBACK).toBe(5)
  })

  it('★ 未接线时的全 0 载荷也能正常解析（不崩、不当成异常）', () => {
    const s = stats()
    expect(s.queue_size).toBe(0)
    expect(s.in_fallback).toBe(false)
    expect(statsAccountedRuns(s)).toBe(0)
  })

  it('★ statsAccountedRuns 对 null 安全', () => {
    expect(statsAccountedRuns(null)).toBe(0)
  })
})

describe('★★ 判据 3：total 是 len(out)', () => {
  it('★ total === 回显 limit ⇒ 判截断', () => {
    expect(recentRunsTruncated({ total: 50, limit: 50, runs: [] })).toBe(true)
  })

  it('★ total < limit ⇒ 不判截断', () => {
    expect(recentRunsTruncated({ total: 49, limit: 50, runs: [] })).toBe(false)
  })

  it('★ limit 缺失/0 ⇒ 不判（没有基准就别说截断）', () => {
    expect(recentRunsTruncated({ total: 99, limit: 0, runs: [] })).toBe(false)
  })

  it('★ 空 / null ⇒ 不判', () => {
    expect(recentRunsTruncated(null)).toBe(false)
    expect(recentRunsTruncated({ total: 0, limit: 50, runs: [] })).toBe(false)
  })
})

describe('★★ 判据 4：只含已结束的运行', () => {
  it('★ 常量在位', () => {
    expect(PROBE_RUNS_ONLY_SETTLED).toBe(true)
  })

  it('★ runDurationMs 按起止时间差算', () => {
    expect(
      runDurationMs(run({ started_at: '2026-10-07T09:59:00Z', finished_at: '2026-10-07T10:00:00Z' })),
    ).toBe(60000)
  })

  it('★ 时间解析不了 ⇒ null（不是 0ms）', () => {
    expect(runDurationMs(run({ started_at: 'not-a-date' }))).toBeNull()
    expect(runDurationMs(null)).toBeNull()
  })

  it('★ 结束早于开始 ⇒ null（不显示负时长）', () => {
    expect(
      runDurationMs(run({ started_at: '2026-10-07T10:00:00Z', finished_at: '2026-10-07T09:00:00Z' })),
    ).toBeNull()
  })

  it('★ 可空键缺失不影响解析', () => {
    const r = run({ http_status: undefined, latency_ms: undefined })
    expect(r.http_status).toBeUndefined()
    expect(r.latency_ms).toBeUndefined()
  })
})