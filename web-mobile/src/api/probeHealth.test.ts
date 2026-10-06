import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchProbeSystemHealth,
  fetchProbeQueueSnapshot,
  legacySectionMayBeUnloaded,
  activeBacklogOf,
  leaseAnomaliesOf,
  hasLeaseAnomaly,
  successRate1hOf,
  type SystemHealthResponse,
  type QueueSnapshotResponse,
  type UnifiedProbeSystemHealth,
  type UnifiedProbeQueueStats,
} from './probeHealth'

/**
 * 探测双轨端点的契约测试（2026-10-07）。
 *
 * 这一组的重点不是「能不能拿到数据」，而是**别拿错源**：
 * 两个端点的顶层字段**全是 legacy 的**，而 legacy 里躺着
 * 572 行历史积压（后端自己写在注释里）。三条判据：
 *
 * 1. ★ 顶层 `total_nodes` / `queues` / `total` 不在类型里
 *    —— 用 `tsc` 之外的运行时手段验证「响应里有这些字段，但我们不读」。
 * 2. ★ `activeBacklogOf` 只由 unified 算，legacy 的 572 进不来。
 * 3. ★ `success_rate_last_1h` 三态：缺失 ≠ 0%。
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

const UNIFIED_HEALTH: UnifiedProbeSystemHealth = {
  total_credentials: 120,
  credentials_with_ursm: 115,
  credentials_no_ursm: 5,
  ursm_key_count: 118,
  queue_pending: 12,
  queue_in_flight: 3,
  queue_completed: 40,
  queue_failed: 2,
  queue_expired: 0,
  queue_total: 57,
  node_total: 30,
  node_healthy: 26,
  node_failing: 2,
  node_paused: 1,
  node_running: 1,
  node_due_now: 4,
  node_leased: 1,
  runs_last_1h: 18,
  runs_success_1h: 17,
  runs_failed_1h: 1,
  runs_last_at: '2026-10-07T10:00:00Z',
  success_rate_last_1h: 0.944,
  pseudo_success_count: 2,
  legacy: {
    total_nodes: 900,
    healthy_nodes: 880,
    failing_nodes: 20,
    suspicious_nodes: 0,
    probing_nodes: 0,
    urgent_queue_size: 572,
    ready_probes: 900,
    current_probing: 0,
    legacy_source: 'model_probe_state',
    legacy_mode_safe: false,
  },
  snapshot_at: '2026-10-07T10:05:00Z',
}

const UNIFIED_QUEUE: UnifiedProbeQueueStats = {
  queue_ready: 12,
  queue_running: 3,
  queue_finished: 40,
  queue_claims: 1,
  node_pending: 6,
  node_running: 1,
  node_paused: 1,
  node_due: 4,
  node_unclaimable: 2,
  stale_leases: 1,
  last_run_at: '2026-10-07T10:00:00Z',
  queue_size: 15,
  snapshot_at: '2026-10-07T10:05:00Z',
}

describe('两个端点都是 admin 档（不设 requiresRole）', () => {
  it('system-health 路径正确', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({}))
    await fetchProbeSystemHealth()
    expect(lastUrl()).toContain('/api/admin/probe/system-health')
  })

  it('queue-snapshot 路径正确', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({}))
    await fetchProbeQueueSnapshot()
    expect(lastUrl()).toContain('/api/admin/probe/queue-snapshot')
  })
})

describe('★★ 判据 1：响应里确实有 legacy 顶层字段，我们不读它们', () => {
  // ★ 这条用**运行时**断言，因为 TS 的类型不能被 vitest 验。
  //   构造一份**带**顶层 legacy 字段的响应（与后端实际形状一致），
  //   断言我们的接口只把显式的三个键当权威。
  const REAL_SHAPED_HEALTH: SystemHealthResponse = {
    // ↓ 这些顶层字段后端确实发了（兼容老客户端），但它们是 legacy 的
    total_nodes: 900,
    healthy_nodes: 880,
    urgent_queue_size: 572,
    ready_probes: 900,
    current_probing: 0,
    ...{
      unified: UNIFIED_HEALTH,
      legacy: UNIFIED_HEALTH.legacy,
      legacy_mode_safe: false,
      snapshot_at: '2026-10-07T10:05:00Z',
    },
  }

  it('★ 顶层的 legacy 字段与 unified 里的新字段数值不同（说明它们确实是两个源）', () => {
    const top = REAL_SHAPED_HEALTH.total_nodes as number
    expect(top).toBe(900)
    expect(REAL_SHAPED_HEALTH.unified!.total_credentials).toBe(120)
    // 同名不同义：`total_nodes` ≠ `total_credentials`
    expect(top).not.toBe(REAL_SHAPED_HEALTH.unified!.total_credentials)
  })

  it('★ 顶层 urgent_queue_size 是 572 的历史积压，unified 的活动队列是 12', () => {
    expect(REAL_SHAPED_HEALTH.urgent_queue_size as number).toBe(572)
    expect(REAL_SHAPED_HEALTH.unified!.queue_pending).toBe(12)
  })

  // ★ 形状不符要看得见：unified 缺失时不能静默当成「全 0」
  it('unified 缺失 ⇒ 接口仍然可用（调用方必须自己判空，不得默认 0）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ total_nodes: 900, legacy_mode_safe: false }))
    const resp = await fetchProbeSystemHealth()
    expect(resp.unified).toBeUndefined()
    expect(resp.total_nodes).toBe(900) // 存在但我们不读
  })
})

describe('★★ 判据 2：活动队列积压只能由 unified 算', () => {
  it('backlog = ready + running + node_pending + node_due', () => {
    // 12 + 3 + 6 + 4 = 25
    expect(activeBacklogOf(UNIFIED_QUEUE)).toBe(25)
  })

  it('★ legacy 的 572 进不来', () => {
    const resp: QueueSnapshotResponse = {
      unified: UNIFIED_QUEUE,
      // 顶层 queues/total 是 legacy 的（后端为了兼容老客户端摊平的）
      queues: [{ probe_priority: 'P1', state: 'pending', queue_size: 572, ready_now: 572, ready_1min: 0, ready_5min: 0 }],
      total: 572,
      legacy: {
        queues: [{ probe_priority: 'P1', state: 'pending', queue_size: 572, ready_now: 572, ready_1min: 0, ready_5min: 0 }],
        total: 572,
        legacy: true,
        legacy_mode_safe: false,
        legacy_source: 'model_probe_state',
      },
    }
    // ★ 顶层 total 是 572，但活动积压是 25 —— 差 23 倍。
    //   拿 resp.total 当「当前积压」就是这个错。
    expect(resp.total).toBe(572)
    expect(activeBacklogOf(resp.unified)).toBe(25)
  })

  it('unified 缺失 ⇒ 返回 null 而不是 0（0 会被读成「没积压」）', () => {
    expect(activeBacklogOf(null)).toBeNull()
    expect(activeBacklogOf(undefined)).toBeNull()
  })

  it('★ 空 unified 对象（字段全缺）⇒ 当 0 算，但源在，语义是「这项没有」', () => {
    expect(activeBacklogOf({} as UnifiedProbeQueueStats)).toBe(0)
  })
})

describe('★ 判据 3：租约异常三项分开返回', () => {
  it('三项各自独立', () => {
    const a = leaseAnomaliesOf({ queue: UNIFIED_QUEUE, health: UNIFIED_HEALTH })
    expect(a.nodeUnclaimable).toBe(2)
    expect(a.staleLeases).toBe(1)
    expect(a.queueExpired).toBe(0) // UNIFIED_HEALTH 里 queue_expired=0
  })

  // ★ 两个 unified 同名但字段集不同（queue-snapshot 没有 queue_expired，
  //   system-health 没有 node_unclaimable）。这三条把「分属两个源」钉住。
  it('★ node_unclaimable / stale_leases 只来自 queue-snapshot 的 unified', () => {
    const onlyHealth = leaseAnomaliesOf({ health: UNIFIED_HEALTH })
    expect(onlyHealth.nodeUnclaimable).toBe(0)
    expect(onlyHealth.staleLeases).toBe(0)
  })

  it('★ queue_expired 只来自 system-health 的 unified', () => {
    const onlyQueue = leaseAnomaliesOf({ queue: UNIFIED_QUEUE })
    expect(onlyQueue.queueExpired).toBe(0)
  })

  it('★ sourcesAvailable 区分「读到 0 个源」与「读到但都是 0」', () => {
    expect(leaseAnomaliesOf({}).sourcesAvailable).toBe(0)
    expect(leaseAnomaliesOf({ queue: UNIFIED_QUEUE, health: UNIFIED_HEALTH }).sourcesAvailable).toBe(2)
  })

  it('★ 任一 > 0 ⇒ 判为异常', () => {
    expect(hasLeaseAnomaly(leaseAnomaliesOf({ queue: UNIFIED_QUEUE, health: UNIFIED_HEALTH }))).toBe(true)
  })

  // ★ 逐项单独触发：UNIFIED_QUEUE 里 nodeUnclaimable=2 恒非 0，
  //   判据「任一 > 0」在它身上永远是 true ⇒ 只看第一项也能全绿。
  //   必须让每一项**各自**当主角，否则后两项从没被量过。
  it('★ 只有 stale_leases 非 0 ⇒ 仍判异常', () => {
    expect(
      hasLeaseAnomaly({ nodeUnclaimable: 0, staleLeases: 3, queueExpired: 0, sourcesAvailable: 2 }),
    ).toBe(true)
  })

  it('★ 只有 queue_expired 非 0 ⇒ 仍判异常', () => {
    expect(
      hasLeaseAnomaly({ nodeUnclaimable: 0, staleLeases: 0, queueExpired: 7, sourcesAvailable: 2 }),
    ).toBe(true)
  })

  it('★ 三项在 unified 里也各自可单独触发（端到端读一遍）', () => {
    const mkQ = (over: Partial<UnifiedProbeQueueStats>) =>
      hasLeaseAnomaly(
        leaseAnomaliesOf({
          queue: { ...UNIFIED_QUEUE, node_unclaimable: 0, stale_leases: 0, ...over },
          health: UNIFIED_HEALTH,
        }),
      )
    const mkH = (over: Partial<UnifiedProbeSystemHealth>) =>
      hasLeaseAnomaly(
        leaseAnomaliesOf({
          queue: { ...UNIFIED_QUEUE, node_unclaimable: 0, stale_leases: 0 },
          health: { ...UNIFIED_HEALTH, queue_expired: 0, ...over },
        }),
      )
    expect(mkQ({ node_unclaimable: 1 })).toBe(true)
    expect(mkQ({ stale_leases: 1 })).toBe(true)
    expect(mkH({ queue_expired: 1 })).toBe(true)
    expect(mkQ({})).toBe(false)
  })

  it('三项全 0 ⇒ 非异常（证明上一条不是恒真）', () => {
    expect(
      hasLeaseAnomaly({
        nodeUnclaimable: 0,
        staleLeases: 0,
        queueExpired: 0,
        sourcesAvailable: 2,
      }),
    ).toBe(false)
  })

  it('两个源都缺失 ⇒ 三项都是 0、判为「无异常」，但 sourcesAvailable=0', () => {
    // ★ 诚实说明：这里 0 的含义是「读不到」，不是「没有异常」。
    //   视图必须靠 sourcesAvailable 决定要不要显示这块 ——
    //   否则「端点挂了」会被显示成「队列很健康」。
    const a = leaseAnomaliesOf(null)
    expect(a.nodeUnclaimable).toBe(0)
    expect(a.staleLeases).toBe(0)
    expect(a.queueExpired).toBe(0)
    expect(a.sourcesAvailable).toBe(0)
    expect(hasLeaseAnomaly(a)).toBe(false)
  })
})

describe('★★ success_rate_last_1h 三态不可二元化', () => {
  it('有值 ⇒ real', () => {
    const r = successRate1hOf(UNIFIED_HEALTH)
    expect(r.kind).toBe('real')
    if (r.kind === 'real') expect(r.value).toBeCloseTo(0.944)
  })

  // ★ Go 侧是 `*float64` + omitempty ⇒ nil 时**字段整个不存在**。
  //   这是最容易被写成 `?? 0` 然后显示成「这一小时 0% 成功率」的地方。
  it('★ 字段缺失 ⇒ no_runs，不是 0%', () => {
    const noField = { ...UNIFIED_HEALTH }
    delete (noField as Record<string, unknown>).success_rate_last_1h
    const r = successRate1hOf(noField)
    expect(r.kind).toBe('no_runs')
  })

  it('★ 显式 null ⇒ 也是 no_runs', () => {
    expect(successRate1hOf({ ...UNIFIED_HEALTH, success_rate_last_1h: null }).kind).toBe('no_runs')
  })

  it('★ 真的是 0（跑了且全失败）⇒ real 0，不是 no_runs', () => {
    const r = successRate1hOf({ ...UNIFIED_HEALTH, success_rate_last_1h: 0 })
    expect(r.kind).toBe('real')
    if (r.kind === 'real') expect(r.value).toBe(0)
  })

  it('unified 缺失 ⇒ no_runs', () => {
    expect(successRate1hOf(null).kind).toBe('no_runs')
  })
})

describe('★ legacy 区块「可能未加载」的三态', () => {
  // 见文件头 (3)：system-health 的 legacy 查询软失败，只 slog.Warn，
  // 响应里没有任何字段能区分「失败」与「真的是全 0」。
  it('★ 全 0 ⇒ 判为「可能未加载」（不读成「一个节点都没有」）', () => {
    expect(
      legacySectionMayBeUnloaded({
        total_nodes: 0,
        healthy_nodes: 0,
        failing_nodes: 0,
        suspicious_nodes: 0,
        probing_nodes: 0,
        urgent_queue_size: 0,
        ready_probes: 0,
        current_probing: 0,
        legacy_mode_safe: false,
        legacy_source: 'model_probe_state',
      }),
    ).toBe(true)
  })

  it('有非零值 ⇒ 判为已加载', () => {
    expect(legacySectionMayBeUnloaded(UNIFIED_HEALTH.legacy)).toBe(false)
  })

  it('★ legacy 整个缺失 ⇒ 也算「可能未加载」', () => {
    expect(legacySectionMayBeUnloaded(null)).toBe(true)
    expect(legacySectionMayBeUnloaded(undefined)).toBe(true)
  })

  it('★ 只有一个非零也算已加载（判据不是「全等于 0」而是「全部等于 0」）', () => {
    expect(
      legacySectionMayBeUnloaded({
        total_nodes: 1,
        healthy_nodes: 0,
        failing_nodes: 0,
        suspicious_nodes: 0,
        probing_nodes: 0,
        urgent_queue_size: 0,
        ready_probes: 0,
        current_probing: 0,
        legacy_mode_safe: false,
        legacy_source: 'model_probe_state',
      }),
    ).toBe(false)
  })
})
