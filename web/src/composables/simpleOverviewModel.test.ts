import { describe, expect, it } from 'vitest'
import {
  deriveOnboardingSteps,
  deriveOverviewStats,
  deriveOverviewStatus,
  deriveProviderHealth,
  formatCompact,
  formatCount,
  formatPercent,
  formatUsd,
  onboardingComplete,
  paginateClientRows,
  sortClientRows,
  type ClientRow,
  type ProviderLike,
} from './simpleOverviewModel'

function provider(over: Partial<ProviderLike> = {}): ProviderLike {
  return {
    id: 1,
    display_name: 'p',
    enabled: true,
    health_status: 'healthy',
    active_credential_count: 1,
    healthy_credential_count: 1,
    warning_credential_count: 0,
    unreachable_credential_count: 0,
    health_checked_at: '2026-11-17T00:00:00Z',
    ...over,
  }
}

describe('formatters', () => {
  it('groups thousands and keeps the sign', () => {
    expect(formatCount(16201)).toBe('16,201')
    expect(formatCount(0)).toBe('0')
    expect(formatCount(999)).toBe('999')
    expect(formatCount(-1234)).toBe('-1,234')
  })

  it('returns null for unreadable values instead of 0', () => {
    expect(formatCount(null)).toBeNull()
    expect(formatCount(undefined)).toBeNull()
    expect(formatCount(Number.NaN)).toBeNull()
    expect(formatPercent(null)).toBeNull()
    expect(formatUsd(null)).toBeNull()
    expect(formatCompact(null)).toBeNull()
  })

  it('compacts large token counts', () => {
    expect(formatCompact(740385219)).toBe('740.4M')
    expect(formatCompact(1234)).toBe('1.2K')
    expect(formatCompact(999)).toBe('999')
    expect(formatCompact(2_500_000_000)).toBe('2.5B')
  })

  it('renders a success ratio as a percentage', () => {
    expect(formatPercent(0.959940744398494)).toBe('96.0%')
    expect(formatPercent(1)).toBe('100.0%')
    expect(formatPercent(0)).toBe('0.0%')
  })

  it('renders usd with two decimals', () => {
    expect(formatUsd(2665.17697)).toBe('$2,665.18')
    expect(formatUsd(0)).toBe('$0.00')
    expect(formatUsd(0.5)).toBe('$0.50')
  })
})

describe('deriveProviderHealth', () => {
  it('separates usable from warning, unreachable and unknown', () => {
    const summary = deriveProviderHealth([
      provider({ id: 1, display_name: 'ok', health_status: 'healthy', healthy_credential_count: 2 }),
      provider({ id: 2, display_name: 'warn', health_status: 'warning', healthy_credential_count: 0, warning_credential_count: 2 }),
      provider({ id: 3, display_name: 'down', health_status: 'unreachable', healthy_credential_count: 0, unreachable_credential_count: 1 }),
      provider({ id: 4, display_name: 'never', active_credential_count: 0, healthy_credential_count: 0 }),
      provider({ id: 5, display_name: 'off', enabled: false }),
    ])
    expect(summary.total).toBe(5)
    expect(summary.usable).toBe(1)
    expect(summary.warning).toBe(1)
    expect(summary.unreachable).toBe(1)
    expect(summary.unknown).toBe(1)
    expect(summary.disabled).toBe(1)
    expect(summary.unavailable).toBe(false)
  })

  it('never counts a provider without credentials as healthy', () => {
    // 「没探活」并进「健康」= 主动宣称健康（本仓反复踩的坑）
    const summary = deriveProviderHealth([
      provider({ active_credential_count: 0, healthy_credential_count: 0, health_status: 'healthy' }),
    ])
    expect(summary.usable).toBe(0)
    expect(summary.unknown).toBe(1)
  })

  it('never counts a healthy provider with zero healthy credentials as usable', () => {
    const summary = deriveProviderHealth([
      provider({ health_status: 'healthy', healthy_credential_count: 0 }),
    ])
    expect(summary.usable).toBe(0)
    expect(summary.unknown).toBe(1)
  })

  it('marks the summary unavailable when the source cannot be read', () => {
    const summary = deriveProviderHealth(null, { unavailable: true })
    expect(summary.unavailable).toBe(true)
    expect(summary.total).toBe(0)
    expect(summary.rows).toEqual([])
  })

  it('orders the detail rows by severity', () => {
    const summary = deriveProviderHealth([
      provider({ id: 1, display_name: 'ok' }),
      provider({ id: 2, display_name: 'down', health_status: 'unreachable', healthy_credential_count: 0 }),
      provider({ id: 3, display_name: 'warn', health_status: 'warning', healthy_credential_count: 0 }),
    ])
    expect(summary.rows.map((r) => r.tone)).toEqual(['danger', 'warn', 'ok'])
  })

  // ★ 截断必须**可数**（2026-11-17 审计轮撞出）：
  //   明细按严重度排序只留前 N 行 ⇒ 被切掉的几乎全是「可用」的
  //   （线上：徽标「19 个上游可用」而列表只有 3 行「可用」）。
  //   不披露 ⇒ 用户以为数据对不上 ⇒ hidden 必须能算出来给页面披露用。
  it('reports how many providers the detail list leaves out', () => {
    const many = Array.from({ length: 20 }, (_, i) =>
      provider({ id: i + 1, display_name: `ok-${i + 1}` }),
    )
    const summary = deriveProviderHealth(many)
    expect(summary.total).toBe(20)
    expect(summary.rows.length).toBe(12)
    expect(summary.hidden).toBe(8)
  })

  it('reports no hidden providers when the list is not truncated', () => {
    const summary = deriveProviderHealth([provider({ id: 1, display_name: 'ok' })])
    expect(summary.hidden).toBe(0)
  })

  it('reports nothing hidden when the source cannot be read', () => {
    const summary = deriveProviderHealth(null, { unavailable: true })
    expect(summary.hidden).toBe(0)
  })
})

describe('deriveOverviewStatus', () => {
  const healthy = deriveProviderHealth([provider()])

  it('is ok when everything is healthy', () => {
    const status = deriveOverviewStatus({ providerHealth: healthy })
    expect(status.tone).toBe('ok')
    expect(status.issues).toEqual([])
    expect(status.confident).toBe(true)
  })

  it('is blocked when there is no usable upstream', () => {
    const empty = deriveProviderHealth([provider({ active_credential_count: 0, healthy_credential_count: 0 })])
    const status = deriveOverviewStatus({ providerHealth: empty })
    expect(status.tone).toBe('blocked')
    expect(status.issues).toContain('no_usable_provider')
  })

  it('treats an unreadable upstream status as unknown, not as broken', () => {
    // ★ 「读不到」渲染成「用不了」会让用户去修一个没坏的组件
    const status = deriveOverviewStatus({
      providerHealth: deriveProviderHealth(null, { unavailable: true }),
    })
    expect(status.tone).toBe('warn')
    expect(status.issues).toContain('provider_status_unavailable')
    expect(status.issues).not.toContain('no_usable_provider')
    expect(status.confident).toBe(false)
  })

  it('warns on unreachable upstreams and on a failed self check', () => {
    const status = deriveOverviewStatus({
      providerHealth: deriveProviderHealth([
        provider({ id: 1, display_name: 'ok' }),
        provider({ id: 2, display_name: 'down', health_status: 'unreachable', healthy_credential_count: 0 }),
      ]),
      selfcheck: { last_status: 'failed' },
    })
    expect(status.tone).toBe('warn')
    expect(status.issues).toContain('provider_unreachable')
    expect(status.issues).toContain('selfcheck_failed')
  })

  it('warns when the self check has not run for more than 7 days', () => {
    const stale = new Date(Date.now() - 8 * 24 * 60 * 60 * 1000).toISOString()
    const status = deriveOverviewStatus({
      providerHealth: healthy,
      selfcheck: { last_status: 'success', last_run_at: stale },
    })
    expect(status.issues).toContain('selfcheck_stale')
  })

  it('does not report a stale self check when the self check data is degraded', () => {
    const stale = new Date(Date.now() - 8 * 24 * 60 * 60 * 1000).toISOString()
    const status = deriveOverviewStatus({
      providerHealth: healthy,
      selfcheck: { degraded: true, last_status: 'success', last_run_at: stale },
    })
    expect(status.issues).not.toContain('selfcheck_stale')
  })

  it('flags degraded statistics and loses confidence', () => {
    const status = deriveOverviewStatus({ providerHealth: healthy, statsDegraded: true })
    expect(status.tone).toBe('warn')
    expect(status.issues).toContain('stats_unavailable')
    expect(status.confident).toBe(false)
  })
})

describe('deriveOverviewStats', () => {
  const summary = {
    total_requests: 16201,
    total_tokens: 740385219,
    total_cost_usd: 2665.17697,
    success_rate: 0.959940744398494,
    active_api_keys: 118,
    active_models: 21,
  }

  it('renders the today scope', () => {
    const stats = deriveOverviewStats({ summary, degraded: false, keyTotal: 208, keyEnabled: 118 })
    const byKey = Object.fromEntries(stats.map((s) => [s.key, s]))
    expect(byKey.requests.value).toBe('16,201')
    expect(byKey.successRate.value).toBe('96.0%')
    expect(byKey.tokens.value).toBe('740.4M')
    expect(byKey.cost.value).toBe('$2,665.18')
    expect(byKey.activeKeys.value).toBe('118 / 208')
    expect(byKey.models.value).toBe('21')
    expect(byKey.requests.scope).toBe('today')
  })

  it('blanks every number when the summary is degraded instead of showing 0', () => {
    // ★ 「读不到」渲染成 0 = 静默错账（本仓反复修过的那一类）
    const stats = deriveOverviewStats({ summary, degraded: true })
    for (const stat of stats) {
      expect(stat.value).toBeNull()
      expect(stat.scope).toBeNull()
    }
  })

  it('scores the success rate tone by threshold', () => {
    const toneFor = (rate: number) =>
      deriveOverviewStats({ summary: { ...summary, success_rate: rate }, degraded: false }).find(
        (s) => s.key === 'successRate',
      )?.tone
    expect(toneFor(0.995)).toBe('success')
    expect(toneFor(0.95)).toBe('warning')
    expect(toneFor(0.5)).toBe('danger')
  })

  it('falls back to the enabled key count when the board has no active key figure', () => {
    const stats = deriveOverviewStats({
      summary: { total_requests: 1 },
      degraded: false,
      keyTotal: 208,
      keyEnabled: 118,
    })
    const activeKeys = stats.find((s) => s.key === 'activeKeys')
    expect(activeKeys?.value).toBe('118 / 208')
  })
})

describe('deriveOnboardingSteps', () => {
  it('derives each step from the real system state', () => {
    const steps = deriveOnboardingSteps({
      providerHealth: deriveProviderHealth([provider()]),
      keysEnabled: 3,
      keysInUse: 1,
    })
    expect(steps.map((s) => [s.key, s.done])).toEqual([
      ['provider', true],
      ['key', true],
      ['client', true],
    ])
  })

  it('keeps the steps pending when nothing is connected yet', () => {
    const steps = deriveOnboardingSteps({
      providerHealth: deriveProviderHealth([]),
      keysEnabled: 0,
      keysInUse: 0,
    })
    expect(steps.every((s) => !s.done)).toBe(true)
    expect(onboardingComplete(steps)).toBe(false)
  })

  it('is not complete while a key exists but no client calls the gateway', () => {
    const steps = deriveOnboardingSteps({
      providerHealth: deriveProviderHealth([provider()]),
      keysEnabled: 2,
      keysInUse: 0,
    })
    expect(steps[1].done).toBe(true)
    expect(steps[2].done).toBe(false)
    expect(onboardingComplete(steps)).toBe(false)
  })

  it('completes when every step is done', () => {
    const steps = deriveOnboardingSteps({
      providerHealth: deriveProviderHealth([provider()]),
      keysEnabled: 1,
      keysInUse: 1,
    })
    expect(onboardingComplete(steps)).toBe(true)
  })
})

// ─── 接入清单分页轮（2026-11-17）：排序与分页的契约 ────────────────────────
describe('sortClientRows', () => {
  const row = (id: number, over: Partial<ClientRow> = {}): ClientRow => ({
    id,
    name: `client-${id}`,
    stateKey: 'ok',
    requests: 0,
    spend: 0,
    lastUsedAt: null,
    createdAt: null,
    ...over,
  })

  it('sorts by the most recent usage and pushes never-used clients to the end', () => {
    const sorted = sortClientRows(
      [
        row(1, { lastUsedAt: '2026-11-10T00:00:00Z' }),
        row(2, { lastUsedAt: null }),
        row(3, { lastUsedAt: '2026-11-17T00:00:00Z' }),
      ],
      'recent-used',
    )
    expect(sorted.map((r) => r.id)).toEqual([3, 1, 2])
  })

  it('sorts by the most requests', () => {
    const sorted = sortClientRows(
      [row(1, { requests: 5 }), row(2, { requests: 90 }), row(3, { requests: 12 })],
      'busiest',
    )
    expect(sorted.map((r) => r.id)).toEqual([2, 3, 1])
  })

  it('sorts by the sign-up time when that is the chosen lens', () => {
    const sorted = sortClientRows(
      [
        row(1, { createdAt: '2026-11-01T00:00:00Z' }),
        row(2, { createdAt: '2026-11-16T00:00:00Z' }),
        row(3, { createdAt: null }),
      ],
      'recent-created',
    )
    expect(sorted.map((r) => r.id)).toEqual([2, 1, 3])
  })

  // ★ 契约：并列时次序必须稳定 —— 否则翻页时同一行会在两页之间跳。
  it('breaks ties deterministically so pages never reshuffle rows', () => {
    const input = [row(3, { requests: 7 }), row(1, { requests: 7 }), row(2, { requests: 7 })]
    const a = sortClientRows(input, 'busiest')
    const b = sortClientRows([...input].reverse(), 'busiest')
    expect(a.map((r) => r.id)).toEqual([1, 2, 3])
    expect(b.map((r) => r.id)).toEqual([1, 2, 3])
  })

  it('never mutates the input array', () => {
    const input = [row(1, { requests: 1 }), row(2, { requests: 9 })]
    sortClientRows(input, 'busiest')
    expect(input.map((r) => r.id)).toEqual([1, 2])
  })
})

describe('paginateClientRows', () => {
  const rows = (n: number): ClientRow[] =>
    Array.from({ length: n }, (_, i) => ({
      id: i + 1,
      name: `client-${i + 1}`,
      stateKey: 'ok' as const,
      requests: n - i,
      spend: 0,
      lastUsedAt: null,
      createdAt: null,
    }))

  it('slices the requested page and reports the paging state', () => {
    const p = paginateClientRows(rows(25), 2, 10)
    expect(p.rows.map((r) => r.id)).toEqual([11, 12, 13, 14, 15, 16, 17, 18, 19, 20])
    expect(p.page).toBe(2)
    expect(p.pageCount).toBe(3)
    expect(p.total).toBe(25)
    expect(p.hasPrev).toBe(true)
    expect(p.hasNext).toBe(true)
  })

  it('marks the first and the last page', () => {
    const first = paginateClientRows(rows(25), 1, 10)
    const last = paginateClientRows(rows(25), 3, 10)
    expect(first.hasPrev).toBe(false)
    expect(first.hasNext).toBe(true)
    expect(last.hasPrev).toBe(true)
    expect(last.hasNext).toBe(false)
  })

  // ★ 列表变短时（例如数据源刷新后少了几行）页码越界 ⇒ 必须落到最后一页，
  //   否则会渲染出**空页**，用户会以为「这一页没有数据」。
  it('clamps an out-of-range page to the last one instead of showing an empty page', () => {
    const p = paginateClientRows(rows(12), 9, 10)
    expect(p.page).toBe(2)
    expect(p.rows.length).toBe(2)
  })

  it('never produces a NaN page count for an illegal page size', () => {
    const p = paginateClientRows(rows(3), 1, 0)
    expect(Number.isFinite(p.pageCount)).toBe(true)
    expect(p.pageCount).toBe(3)
    expect(p.pageSize).toBe(1)
  })

  it('handles an empty list as one empty page', () => {
    const p = paginateClientRows([], 1, 10)
    expect(p.rows).toEqual([])
    expect(p.pageCount).toBe(1)
    expect(p.hasNext).toBe(false)
  })
})
