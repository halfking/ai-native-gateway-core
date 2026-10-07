import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchOpsOverview,
  unwrapOpsOverview,
  opsHasSection,
  opsMissingSectionKeys,
  opsIsFullyDegraded,
  opsSectionCount,
  opsGeneratedAtIsSecondPrecision,
  opsRegionIsAlwaysPresent,
  opsRegionIsPlaceholder,
  opsRegionPlaceholderIsAllZero,
  opsRegionPreambleCoversExpected,
  opsRegionExtraRowsFollowPreamble,
  opsDataPlaneCountIsUnavailable,
  opsDataPlaneCountIsAvailable,
  opsOfflineTimestampMirrorsCreatedAt,
  opsOfflineIsApproved,
  opsFaultStatusIsNew,
  opsRecentFaultsWithinOpenEvents,
  opsRecentFaultsAreAllNew,
  opsFaultSectionListKey,
  opsUpgradeSectionListKey,
  opsRuntimeLastUpdateIsNull,
  opsRegionLastHeartbeatIsAbsent,
  opsRuntimeRowMayBeOffline,
  opsRuntimeZeroIsAmbiguous,
  opsRuntimeHasMetrics,
  OPS_OVERVIEW_CACHE_TTL_SECONDS,
  OPS_OVERVIEW_ALWAYS_KEY,
  OPS_OVERVIEW_SECTION_KEYS,
  OPS_EXPECTED_REGIONS,
  OPS_DATA_PLANE_UNAVAILABLE,
  OPS_DATA_PLANE_KEYS,
  OPS_FAULT_OPEN_STATUSES,
  OPS_RECENT_FAULT_STATUS,
  OPS_OFFLINE_STATUS_FALLBACK,
  type OpsOverviewResponse,
  type OpsRegionStat,
  type OpsOfflineRequest,
  type OpsRuntimeMetric,
  type OpsRecentUpgrade,
} from './opsOverview'

/**
 * 运维总览的契约测试（2026-10-08，第八十三批）。
 *
 * 后端：`admin/handler.go:1070`（`admin(...)` ⇒ **admin 档**）
 * → `admin/ops_overview.go`（11 个并发子查询合进一个开放形状的 map）。
 *
 * 重点是源文件头写明的十七件事 (1)…(17)。带 ★ 的自校验判据都能被变异打掉。
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
function lastUrl(): string {
  return String(fetchMock.mock.calls[0]![0])
}

// ── 夹具：全部十一个子查询键都齐全的「满配」响应 ──

function region(over: Partial<OpsRegionStat> = {}): OpsRegionStat {
  return {
    region: 'local',
    total_instances: 1,
    online_instances: 1,
    offline_instances: 0,
    degraded_instances: 0,
    last_heartbeat: '2026-10-08T02:00:00Z',
    ...over,
  }
}

function placeholder(r: string): OpsRegionStat {
  return {
    region: r,
    total_instances: 0,
    online_instances: 0,
    offline_instances: 0,
    degraded_instances: 0,
    missing: true,
  }
}

function offline(over: Partial<OpsOfflineRequest> = {}): OpsOfflineRequest {
  return {
    license_key: 'LIC-1',
    hardware_hash: 'hw-abc',
    instance_id: 'inst-1',
    device_name: 'ops-laptop',
    request_id: 'req-1',
    timestamp: '2026-10-08T01:00:00Z',
    created_at: '2026-10-08T01:00:00Z',
    status: OPS_OFFLINE_STATUS_FALLBACK,
    ...over,
  }
}

function runtime(over: Partial<OpsRuntimeMetric> = {}): OpsRuntimeMetric {
  return {
    instance_id: 'inst-1',
    hostname: 'node-a',
    region: 'local',
    version: 'v1.2.3',
    status: 'online',
    avg_cpu_pct: 12.5,
    avg_mem_pct: 40.25,
    avg_tps: 3.5,
    max_p99_ms: 820,
    last_update: '2026-10-08T02:00:00Z',
    ...over,
  }
}

function upgrade(over: Partial<OpsRecentUpgrade> = {}): OpsRecentUpgrade {
  return {
    instance_id: 'inst-1',
    status: 'completed',
    version: 'v1.2.3',
    started_at: '2026-10-08T00:00:00Z',
    completed_at: '2026-10-08T00:05:00Z',
    ...over,
  }
}

/** 满配响应：十一个子查询键 + generated_at（12 键，键数上限）。 */
function full(over: Partial<OpsOverviewResponse> = {}): OpsOverviewResponse {
  return {
    generated_at: '2026-10-08T02:00:00Z',
    center_stats: {
      total_instances: 3,
      online_instances: 2,
      offline_instances: 1,
      degraded_instances: 0,
    },
    region_stats: [region(), placeholder('245'), placeholder('154')],
    deployment_nodes: [
      {
        instance_id: 'inst-1',
        hostname: 'node-a',
        ip_address: '10.0.0.1',
        region: 'local',
        version: 'v1.2.3',
        build_seq: 42,
        status: 'online',
        started_at: '2026-10-01T00:00:00Z',
        last_heartbeat: '2026-10-08T02:00:00Z',
      },
    ],
    data_plane_tables: {
      gateway_instances: 3,
      instance_heartbeats: 120,
      download_events: 8,
      offline_activation_requests: 1,
      license_devices: 2,
      licenses: 5,
    },
    license_total: 5,
    offline_requests: [offline()],
    fault_stats: { open_events: 7 },
    recent_faults: {
      events: [
        {
          id: 91,
          rule_id: 12,
          rule_name: '磁盘水位',
          severity: 'high',
          status: OPS_RECENT_FAULT_STATUS,
          title: '磁盘将满',
          description: '90% 已用',
          source: 'disk',
          detected_at: '2026-10-08T01:30:00Z',
        },
      ],
      total: 1,
    },
    recent_upgrades: { items: [upgrade()], total: 1 },
    download_stats: {
      today_downloads: 1,
      week_downloads: 4,
      total_downloads: 8,
      supporter_count: 3,
    },
    runtime_metrics_summary: [runtime()],
    ...over,
  }
}

function okFull(): void {
  fetchMock.mockResolvedValueOnce(jsonResponse(full()))
}

// ═════════════════════════════════════════════════════════════════════════════
// URL 与常量
// ═════════════════════════════════════════════════════════════════════════════

describe('URL 与常量', () => {
  it('★ 无参数 ⇒ 裸路径', async () => {
    okFull()
    await fetchOpsOverview()
    expect(lastUrl()).toBe('/api/admin/ops/overview')
  })

  it('后端常量：缓存 TTL 15 秒', () => {
    expect(OPS_OVERVIEW_CACHE_TTL_SECONDS).toBe(15)
  })

  it('后端常量：唯一恒在的键是 generated_at', () => {
    expect(OPS_OVERVIEW_ALWAYS_KEY).toBe('generated_at')
  })

  it('后端常量：十一个子查询键', () => {
    expect(OPS_OVERVIEW_SECTION_KEYS).toHaveLength(11)
    expect([...OPS_OVERVIEW_SECTION_KEYS]).toContain('runtime_metrics_summary')
    expect([...OPS_OVERVIEW_SECTION_KEYS]).toContain('data_plane_tables')
  })

  it('后端常量：三个硬编码 region', () => {
    expect([...OPS_EXPECTED_REGIONS]).toEqual(['local', '245', '154'])
  })

  it('后端常量：不可用计数哨兵是 -1', () => {
    expect(OPS_DATA_PLANE_UNAVAILABLE).toBe(-1)
  })

  it('后端常量：data_plane_tables 的六个计数项', () => {
    expect([...OPS_DATA_PLANE_KEYS]).toEqual([
      'gateway_instances',
      'instance_heartbeats',
      'download_events',
      'offline_activation_requests',
      'license_devices',
      'licenses',
    ])
  })

  it('后端常量：fault_stats 的三个开放状态', () => {
    expect([...OPS_FAULT_OPEN_STATUSES]).toEqual(['new', 'acknowledged', 'resolving'])
  })

  it('后端常量：recent_faults 的硬编码状态是 new', () => {
    expect(OPS_RECENT_FAULT_STATUS).toBe('new')
  })

  it('后端常量：offline status 的 COALESCE 兜底是 pending', () => {
    expect(OPS_OFFLINE_STATUS_FALLBACK).toBe('pending')
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 形状校验：开放形状
// ═════════════════════════════════════════════════════════════════════════════

describe('形状校验：开放形状', () => {
  it('★ 响应不是裸对象 ⇒ 抛错', () => {
    expect(() => unwrapOpsOverview(null)).toThrow(/运维总览 响应形状不符：期望裸对象，实得 null/)
  })

  it('★ 响应是数组 ⇒ 抛错（点明实得 array）', () => {
    expect(() => unwrapOpsOverview([])).toThrow(/实得 array/)
  })

  it('★ 缺 generated_at ⇒ 抛错并点名它', () => {
    const d = { ...full() } as Record<string, unknown>
    delete d['generated_at']
    expect(() => unwrapOpsOverview(d)).toThrow(/缺 1 个键（generated_at）/)
  })

  it('★ generated_at 是数字 ⇒ 抛错点名它', () => {
    expect(() => unwrapOpsOverview(full({ generated_at: 1757000000 as unknown as string }))).toThrow(
      /的 generated_at 不是字符串/,
    )
  })

  it('★ ★ 只有 generated_at 一个键也放行（缺子查询键是合法形状）', () => {
    const r = unwrapOpsOverview({ generated_at: '2026-10-08T02:00:00Z' })
    expect(opsSectionCount(r)).toBe(0)
  })

  it('★ center_stats 四个键缺一 ⇒ 抛错点名它', () => {
    const c = { ...full().center_stats! } as Record<string, unknown>
    delete c['degraded_instances']
    expect(() => unwrapOpsOverview(full({ center_stats: c as never }))).toThrow(/degraded_instances 不是数字/)
  })

  it('★ region_stats 不是数组 ⇒ 抛错', () => {
    expect(() => unwrapOpsOverview(full({ region_stats: null as unknown as OpsRegionStat[] }))).toThrow(
      /region_stats 不是数组/,
    )
  })

  it('★ region 行的 total_instances 是字符串 ⇒ 抛错点名它', () => {
    expect(() =>
      unwrapOpsOverview(full({ region_stats: [{ ...region(), total_instances: '1' as unknown as number }] })),
    ).toThrow(/region_stats\[0\] 的 total_instances 不是数字/)
  })

  it('★ region 行的 last_heartbeat 是数字 ⇒ 抛错点名它', () => {
    expect(() =>
      unwrapOpsOverview(
        full({ region_stats: [{ ...region(), last_heartbeat: 1757000000 as unknown as string }] }),
      ),
    ).toThrow(/region_stats\[0\] 的 last_heartbeat 不是字符串/)
  })

  it('★ ★ region 行的 missing 键在但不是 true ⇒ 抛错', () => {
    expect(() =>
      unwrapOpsOverview(full({ region_stats: [{ ...region(), missing: 'yes' as unknown as true }] })),
    ).toThrow(/region_stats\[0\] 的 missing 不是 true/)
  })

  it('★ data_plane_tables 的值是字符串 ⇒ 抛错点名该项', () => {
    expect(() =>
      unwrapOpsOverview(
        full({ data_plane_tables: { ...full().data_plane_tables!, licenses: '5' as unknown as number } }),
      ),
    ).toThrow(/data_plane_tables 的 licenses 不是数字/)
  })

  it('★ license_total 是字符串 ⇒ 抛错点名它', () => {
    expect(() => unwrapOpsOverview(full({ license_total: '5' as unknown as number }))).toThrow(
      /license_total 不是数字/,
    )
  })

  it('★ recent_faults 缺 total ⇒ 抛错点名它', () => {
    const s = { events: [] } as Record<string, unknown>
    expect(() => unwrapOpsOverview(full({ recent_faults: s as never }))).toThrow(/recent_faults 的 total 不是数字/)
  })

  it('★ recent_upgrades 缺 total ⇒ 抛错点名它', () => {
    const s = { items: [] } as Record<string, unknown>
    expect(() => unwrapOpsOverview(full({ recent_upgrades: s as never }))).toThrow(
      /recent_upgrades 的 total 不是数字/,
    )
  })

  it('★ runtime_metrics_summary 不是数组 ⇒ 抛错', () => {
    expect(() =>
      unwrapOpsOverview(full({ runtime_metrics_summary: null as unknown as OpsRuntimeMetric[] })),
    ).toThrow(/runtime_metrics_summary 不是数组/)
  })

  it('★ 缺 license_total 键 ⇒ 放行（子查询失败是合法形状）', () => {
    const d = { ...full() } as Record<string, unknown>
    delete d['license_total']
    expect(unwrapOpsOverview(d).license_total).toBeUndefined()
  })

  it('★ 缺 runtime_metrics_summary 键 ⇒ 放行', () => {
    const d = { ...full() } as Record<string, unknown>
    delete d['runtime_metrics_summary']
    expect(unwrapOpsOverview(d).runtime_metrics_summary).toBeUndefined()
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (1)(2)：子查询的缺失即失败
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★★ (1)(2) 键缺失 = 该子查询失败', () => {
  it('★ 满配响应里十一个子查询键全在', () => {
    expect(opsSectionCount(full())).toBe(11)
  })

  it('★ 键在 ⇒ 判该子查询成功', () => {
    expect(opsHasSection(full(), 'center_stats')).toBe(true)
  })

  it('★ 键缺 ⇒ 判该子查询失败（反向）', () => {
    const d = { ...full() } as Record<string, unknown>
    delete d['center_stats']
    expect(opsHasSection(d as unknown as OpsOverviewResponse, 'center_stats')).toBe(false)
  })

  it('★ ★ 键在但值是 0 ⇒ 仍判「成功」（真值判断会把 0 当失败）', () => {
    // ★ 这条才是「把 in 改成真值」的牙：
    //   license_total 在真实响应里可以是 **0**（一个许可证都没有）⇒ 真值为假。
    const r = full({ license_total: 0 })
    expect(opsHasSection(r, 'license_total')).toBe(true)
    expect(Boolean(r.license_total)).toBe(false)
  })

  it('★ ★ 键在但值是空数组 ⇒ 仍判「成功」（空数组是真值，但要与 0 一起覆盖）', () => {
    const r = full({ offline_requests: [] })
    expect(opsHasSection(r, 'offline_requests')).toBe(true)
  })

  it('★ 缺 center_stats ⇒ 缺失清单里有它', () => {
    const d = { ...full() } as Record<string, unknown>
    delete d['center_stats']
    delete d['download_stats']
    expect(opsMissingSectionKeys(d as unknown as OpsOverviewResponse)).toEqual([
      'center_stats',
      'download_stats',
    ])
  })

  it('★ 满配响应 ⇒ 缺失清单为空', () => {
    expect(opsMissingSectionKeys(full())).toEqual([])
  })

  it('★ ★ 全部子查询都失败 ⇒ 判「全降级」（后端这种情形会 500，所以 200 里不该出现）', () => {
    expect(opsIsFullyDegraded({ generated_at: '2026-10-08T02:00:00Z' })).toBe(true)
  })

  it('★ 有任一子查询 ⇒ 不判全降级（反向）', () => {
    expect(opsIsFullyDegraded(full())).toBe(false)
  })

  it('★ 只剩一个子查询 ⇒ 成功数为 1', () => {
    const r = { generated_at: 'x', license_total: 5 }
    expect(opsSectionCount(r as unknown as OpsOverviewResponse)).toBe(1)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (3)：generated_at 是秒级
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★ (3) generated_at 是 RFC3339 秒级', () => {
  it('★ "2026-10-08T02:00:00Z" ⇒ 判秒级', () => {
    expect(opsGeneratedAtIsSecondPrecision('2026-10-08T02:00:00Z')).toBe(true)
  })

  it('★ ★ 带小数（RFC3339Nano）⇒ 不判秒级', () => {
    // ★ 这一格才是「去掉小数检查」能打掉的：
    //   其它端点用 time.Time 直序列化 ⇒ 就有小数部分。
    expect(opsGeneratedAtIsSecondPrecision('2026-10-08T02:00:00.123456Z')).toBe(false)
  })

  it('★ 带 +08:00 偏移 ⇒ 不判秒级（后端写死 UTC）', () => {
    expect(opsGeneratedAtIsSecondPrecision('2026-10-08T10:00:00+08:00')).toBe(false)
  })

  it('★ 没有时区 ⇒ 不判（反向）', () => {
    expect(opsGeneratedAtIsSecondPrecision('2026-10-08T02:00:00')).toBe(false)
  })

  it('★ 空串 ⇒ 不判（反向）', () => {
    expect(opsGeneratedAtIsSecondPrecision('')).toBe(false)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (5)：region 硬编码三行 + 占位行
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★★ (5) region 的三个硬编码行与占位行', () => {
  it('★ 库里一个实例都没有时三个 region 仍恒在', () => {
    const regions = [placeholder('local'), placeholder('245'), placeholder('154')]
    expect(opsRegionIsAlwaysPresent(regions, 'local')).toBe(true)
    expect(opsRegionIsAlwaysPresent(regions, '245')).toBe(true)
    expect(opsRegionIsAlwaysPresent(regions, '154')).toBe(true)
  })

  it('★ 缺失某个硬编码 region ⇒ 判不存在（反向，但真实后端不会产出）', () => {
    expect(opsRegionIsAlwaysPresent([region()], '245')).toBe(false)
  })

  it('★ 占位行：missing 键在 ⇒ 判占位', () => {
    expect(opsRegionIsPlaceholder(placeholder('245'))).toBe(true)
  })

  it('★ 真实行没有 missing 键 ⇒ 不判占位（反向）', () => {
    expect(opsRegionIsPlaceholder(region())).toBe(false)
  })

  it('★ ★ 陷阱对照：`!!undefined` 与 `undefined === true` 都是 false（这一格区分不开）', () => {
    // ★ 说明：missing 的取值恒为字面量 `true`，不存在「非真值但在键里」的形态
    //   ⇒ 「in 换成真值」在本族的判据上**可证等价**，保留 `=== true` 是为了
    //   让解包器能拒绝 `{missing: "yes"}` 这种形状不符的载荷。
    const r = region()
    expect(Boolean(r.missing)).toBe(false)
    expect(r.missing === true).toBe(false)
  })

  it('★ 占位行的四个计数恒为 0', () => {
    expect(opsRegionPlaceholderIsAllZero(placeholder('154'))).toBe(true)
  })

  it('★ ★ 占位行里有一个非零计数 ⇒ 不成立', () => {
    // ★ 这条才是「四个条件各一条」的专属：漏掉任一 && 的变异都会打掉它
    expect(opsRegionPlaceholderIsAllZero({ ...placeholder('154'), degraded_instances: 1 })).toBe(false)
  })

  it('★ 占位行有 online_instances ⇒ 不成立（反向）', () => {
    expect(opsRegionPlaceholderIsAllZero({ ...placeholder('154'), online_instances: 2 })).toBe(false)
  })

  it('★ 占位行有 total_instances ⇒ 不成立（反向）', () => {
    expect(opsRegionPlaceholderIsAllZero({ ...placeholder('154'), total_instances: 5 })).toBe(false)
  })

  it('★ 前三行按 local/245/154 的固定顺序 ⇒ 覆盖判据成立', () => {
    const regions = [region({ region: 'local' }), placeholder('245'), placeholder('154')]
    expect(opsRegionPreambleCoversExpected(regions)).toBe(true)
  })

  it('★ 前三行顺序被打乱 ⇒ 不成立（反向）', () => {
    const regions = [placeholder('245'), region({ region: 'local' }), placeholder('154')]
    expect(opsRegionPreambleCoversExpected(regions)).toBe(false)
  })

  it('★ 前三行含未知 region ⇒ 不成立', () => {
    const regions = [region({ region: 'local' }), placeholder('zzz'), placeholder('154')]
    expect(opsRegionPreambleCoversExpected(regions)).toBe(false)
  })

  it('★ ★ 第三行不是 154（前两行对）⇒ 不成立', () => {
    // ★ 这条才是「只检查前两个」能打掉的：
    //   前两行完全正确、第三行错位 ⇒ 只查 slice(0,2) 的实现会返回 true。
    const regions = [region({ region: 'local' }), placeholder('245'), placeholder('zzz')]
    expect(opsRegionPreambleCoversExpected(regions)).toBe(false)
  })

  it('★ ★ 第二行不是 245（前一行对）⇒ 不成立', () => {
    const regions = [region({ region: 'local' }), placeholder('zzz'), placeholder('154')]
    expect(opsRegionPreambleCoversExpected(regions)).toBe(false)
  })

  it('★ ★ 额外行排在硬编码三行之后 ⇒ 成立', () => {
    const regions = [placeholder('local'), placeholder('245'), placeholder('154'), region({ region: 'unknown' })]
    expect(opsRegionExtraRowsFollowPreamble(regions)).toBe(true)
  })

  it('★ ★ 两个额外行里混进一个硬编码 region ⇒ 不成立', () => {
    // ★ 这条才是「every 改成 some」的牙：
    //   tail 全是额外行时两式同 true ⇒ 指不到牙。
    const regions = [
      placeholder('local'),
      placeholder('245'),
      placeholder('154'),
      region({ region: 'unknown' }),
      region({ region: '245' }),
    ]
    expect(opsRegionExtraRowsFollowPreamble(regions)).toBe(false)
  })

  it('★ 硬编码行出现在额外段 ⇒ 不成立（反向）', () => {
    // ★ 必须凑够 4 行：`slice(3)` 对三元素数组是空的 ⇒ every 返回 true ⇒ 指不到牙。
    const regions = [
      region({ region: 'unknown' }),
      placeholder('zzz'),
      placeholder('yyy'),
      region({ region: 'local' }),
    ]
    expect(opsRegionExtraRowsFollowPreamble(regions)).toBe(false)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (6)：-1 哨兵
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★ (6) data_plane_tables 的 -1 是「查询失败」哨兵', () => {
  it('★ 值是 -1 ⇒ 判不可用', () => {
    expect(opsDataPlaneCountIsUnavailable(-1)).toBe(true)
  })

  it('★ 值是 0 ⇒ **不**判不可用（反向：0 是真实计数）', () => {
    expect(opsDataPlaneCountIsUnavailable(0)).toBe(false)
  })

  it('★ 值是 3 ⇒ 判可用', () => {
    expect(opsDataPlaneCountIsAvailable(3)).toBe(true)
  })

  it('★ 值是 -1 ⇒ 不判可用（反向）', () => {
    expect(opsDataPlaneCountIsAvailable(-1)).toBe(false)
  })

  it('★ ★ 值是 0 ⇒ 判可用（能是 0 计数，哨兵只有 -1）', () => {
    // ★ 这条才是「可用判据改成 > 0」的牙：真实计数 0 必须算可用。
    expect(opsDataPlaneCountIsAvailable(0)).toBe(true)
    expect(opsDataPlaneCountIsUnavailable(0)).toBe(false)
  })

  it('★ ★ 值是 -2（比哨兵更小）⇒ 不可用判据的 === 与 < 0 分歧', () => {
    // ★ 这条才是「=== -1 改成 < 0」的牙。
    //   后端只可能写 -1，但判据写成 <0 之后任何更小的负数也会被算成「不可用」。
    expect(opsDataPlaneCountIsUnavailable(-2)).toBe(false)
    expect(opsDataPlaneCountIsUnavailable(-1)).toBe(true)
  })

  it('★ 六项里一项 -1 ⇒ 只那项不可用', () => {
    const t = full().data_plane_tables!
    const keys = Object.keys(t) as (keyof typeof t)[]
    const flagged = keys.filter((k) => opsDataPlaneCountIsUnavailable(t[k]))
    expect(flagged).toEqual([])
    expect(opsDataPlaneCountIsUnavailable({ ...t, licenses: -1 }.licenses)).toBe(true)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (7)(8)(9)：offline_requests / recent_upgrades
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★ (7) timestamp 与 created_at 必然相等', () => {
  it('★ 相等 ⇒ 判成立', () => {
    expect(opsOfflineTimestampMirrorsCreatedAt(offline())).toBe(true)
  })

  it('★ 不等 ⇒ 判 false（反向，后端同源值不会不等）', () => {
    expect(
      opsOfflineTimestampMirrorsCreatedAt(
        offline({ timestamp: '2026-10-08T01:00:00Z', created_at: '2026-10-08T02:00:00Z' }),
      ),
    ).toBe(false)
  })

  it('★ ★ 毫秒级不同也不等（判据是严格相等，不是「同一时刻」）', () => {
    expect(
      opsOfflineTimestampMirrorsCreatedAt(
        offline({ timestamp: '2026-10-08T01:00:00.001Z', created_at: '2026-10-08T01:00:00Z' }),
      ),
    ).toBe(false)
  })
})

describe('★★ (8) approved_at 是条件键', () => {
  it('★ approved_at 键在 ⇒ 判已审批', () => {
    expect(opsOfflineIsApproved(offline({ approved_at: '2026-10-08T02:00:00Z' }))).toBe(true)
  })

  it('★ approved_at 键缺 ⇒ 不判已审批（反向）', () => {
    expect(opsOfflineIsApproved(offline())).toBe(false)
  })

  it('★ ★ 陷阱对照：approved_at 是非空串 ⇒ 真值判断与 `in` 同结果（可证等价）', () => {
    const r = offline({ approved_at: '2026-10-08T02:00:00Z' })
    expect(Boolean(r.approved_at)).toBe(true)
    expect(opsOfflineIsApproved(r)).toBe(true)
  })
})

describe('★★ (9) recent_upgrades 的 version 可能是空串', () => {
  it('★ version 是空串仍然放行（COALESCE 兜底）', () => {
    expect(unwrapOpsOverview(full({ recent_upgrades: { items: [upgrade({ version: '' })], total: 1 } }))).toBeTruthy()
  })

  it('★ completed_at 键缺 ⇒ 放行（进行中的升级）', () => {
    const u = upgrade()
    delete (u as unknown as Record<string, unknown>)['completed_at']
    expect(unwrapOpsOverview(full({ recent_upgrades: { items: [u], total: 1 } }))).toBeTruthy()
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (10)(11)：跨子查询不变式与容器键名
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★ (10) recent_faults 是 fault_stats 的子集', () => {
  it('★ recent.total 小于 open_events ⇒ 成立', () => {
    expect(opsRecentFaultsWithinOpenEvents(full())).toBe(true)
  })

  it('★ 两者相等也成立（全部 open 事件都是 new）', () => {
    const r = full({
      recent_faults: { events: [], total: 7 },
    })
    expect(opsRecentFaultsWithinOpenEvents(r)).toBe(true)
  })

  it('★ recent.total 大于 open_events ⇒ 不成立（反向，后端不可能产出）', () => {
    const r = full({ recent_faults: { events: [], total: 8 } })
    expect(opsRecentFaultsWithinOpenEvents(r)).toBe(false)
  })

  it('★ ★ 缺任一子查询 ⇒ 不成立（无法比较）', () => {
    const d = { ...full() } as Record<string, unknown>
    delete d['fault_stats']
    expect(opsRecentFaultsWithinOpenEvents(d as unknown as OpsOverviewResponse)).toBe(false)
  })

  it('★ 缺 recent_faults ⇒ 不成立（反向）', () => {
    const d = { ...full() } as Record<string, unknown>
    delete d['recent_faults']
    expect(opsRecentFaultsWithinOpenEvents(d as unknown as OpsOverviewResponse)).toBe(false)
  })

  it('★ 每一行的 status 必然是 new ⇒ 判成立', () => {
    expect(opsRecentFaultsAreAllNew(full())).toBe(true)
  })

  it('★ 单行 status 是 new ⇒ 判成立', () => {
    expect(opsFaultStatusIsNew(full().recent_faults!.events[0]!)).toBe(true)
  })

  it('★ ★ 第二行的 status 不是 new ⇒ 判 false（SQL 硬编码，后端不可能）', () => {
    const r = full()
    const evs = r.recent_faults!.events
    r.recent_faults = {
      events: [evs[0]!, { ...evs[0]!, id: 92, status: 'acknowledged' }],
      total: 2,
    }
    expect(opsRecentFaultsAreAllNew(r)).toBe(false)
  })

  it('★ 空 events ⇒ 全部成立（空集恒真）', () => {
    expect(opsRecentFaultsAreAllNew(full({ recent_faults: { events: [], total: 0 } }))).toBe(true)
  })
})

describe('★★ (11) 两个列表容器的键名不同', () => {
  it('★ recent_faults 的容器键是 events', () => {
    expect(opsFaultSectionListKey()).toBe('events')
  })

  it('★ recent_upgrades 的容器键是 items', () => {
    expect(opsUpgradeSectionListKey()).toBe('items')
  })

  it('★ ★ 两个键名确实不同（这正是不能共用取列表函数的原因）', () => {
    expect(opsFaultSectionListKey()).not.toBe(opsUpgradeSectionListKey())
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (12)(13)(14)：两种 nil 编码 / 离线在榜 / 0 二义
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★★ (12) last_heartbeat 键缺 vs last_update 裸 null', () => {
  it('★ last_heartbeat 键缺 ⇒ 判缺失', () => {
    // ★ 键缺必须用 delete 构造：`{ ...row, last_heartbeat: undefined }` 会**造出这个键**。
    const r = region()
    delete (r as unknown as Record<string, unknown>)['last_heartbeat']
    expect(opsRegionLastHeartbeatIsAbsent(r)).toBe(true)
  })

  it('★ ★ 陷阱对照：spread + undefined 会造出键，in 判成「存在」', () => {
    // ★★ 这就是为什么本模块的条件键判据一律用 `in` 而不是真值判断：
    //   JSON 反序列化出来的「缺键」与「值为 undefined」在 JS 里是**不同的形状**，
    //   而手写夹具很容易意外造出后一种。
    const r = region({ last_heartbeat: undefined })
    expect('last_heartbeat' in r).toBe(true)
    expect(r.last_heartbeat).toBeUndefined()
    expect(opsRegionLastHeartbeatIsAbsent(r)).toBe(false)
  })

  it('★ last_heartbeat 有值 ⇒ 不判缺失（反向）', () => {
    expect(opsRegionLastHeartbeatIsAbsent(region())).toBe(false)
  })

  it('★ last_update 是 null ⇒ 判为 null', () => {
    expect(opsRuntimeLastUpdateIsNull(runtime({ last_update: null }))).toBe(true)
  })

  it('★ ★ last_update 有值 ⇒ 不判 null（反向）', () => {
    // ★ 这条才是「把 === null 换成真值判断」的牙：时间戳字符串是真值。
    expect(opsRuntimeLastUpdateIsNull(runtime())).toBe(false)
  })

  it('★ ★★ last_update 是空串 ⇒ 也不判 null', () => {
    // ★ 空串与 null 是两种不同的形状：空串在 Go 侧只能是 time.Time 零值序列化出来的
    const r = runtime({ last_update: '' })
    expect(opsRuntimeLastUpdateIsNull(r)).toBe(false)
  })
})

describe('★★★ (13) 离线实例也可能出现在指标榜上', () => {
  it('★ status 是 offline ⇒ 判「可能是离线但在榜」', () => {
    expect(opsRuntimeRowMayBeOffline(runtime({ status: 'offline' }))).toBe(true)
  })

  it('★ status 是 online ⇒ 不判（反向）', () => {
    expect(opsRuntimeRowMayBeOffline(runtime())).toBe(false)
  })

  it('★ offline 且有 last_update ⇒ 这种行真实存在（WHERE 的 OR 分支）', () => {
    const r = runtime({ status: 'offline', last_update: '2026-10-08T02:00:00Z' })
    expect(opsRuntimeRowMayBeOffline(r)).toBe(true)
    expect(opsRuntimeHasMetrics(r)).toBe(true)
  })
})

describe('★★★ (14) 全 0 的三个指标是二义的', () => {
  it('★ 三个都 0 ⇒ 判二义（可能真 0，也可能无指标）', () => {
    expect(opsRuntimeZeroIsAmbiguous(runtime({ avg_cpu_pct: 0, avg_mem_pct: 0, avg_tps: 0 }))).toBe(true)
  })

  it('★ ★ 三个都 0 且 last_update 是 null ⇒ 确定是「无指标」', () => {
    const r = runtime({ avg_cpu_pct: 0, avg_mem_pct: 0, avg_tps: 0, last_update: null })
    expect(opsRuntimeZeroIsAmbiguous(r)).toBe(true)
    expect(opsRuntimeHasMetrics(r)).toBe(false)
  })

  it('★ 任一非 0 ⇒ 不判二义（反向）', () => {
    expect(opsRuntimeZeroIsAmbiguous(runtime({ avg_tps: 0.1 }))).toBe(false)
  })

  it('★ ★ 只有 avg_tps 非 0（另两个是 0）⇒ 不判二义', () => {
    // ★ 这条才是「漏掉 avg_tps」的牙：
    //   若实现只查 cpu+mem，这三个数会被判成「全 0」⇒ 返回 true。
    expect(opsRuntimeZeroIsAmbiguous(runtime({ avg_cpu_pct: 0, avg_mem_pct: 0, avg_tps: 0.5 }))).toBe(false)
  })

  it('★ ★ 只有 avg_cpu_pct 非 0（另两个是 0）⇒ 不判二义', () => {
    // ★ 这条才是「漏掉 avg_cpu_pct」的牙：
    //   「avg_cpu_pct 非 0」那条用例里 mem/tps 也非 0 ⇒ 漏掉 cpu 检查后仍返回 false。
    expect(opsRuntimeZeroIsAmbiguous(runtime({ avg_cpu_pct: 0.5, avg_mem_pct: 0, avg_tps: 0 }))).toBe(false)
  })

  it('★ ★ 只有 avg_mem_pct 非 0（另两个是 0）⇒ 不判二义', () => {
    expect(opsRuntimeZeroIsAmbiguous(runtime({ avg_cpu_pct: 0, avg_mem_pct: 0.5, avg_tps: 0 }))).toBe(false)
  })

  it('★ avg_cpu_pct 非 0 ⇒ 不判二义（反向）', () => {
    expect(opsRuntimeZeroIsAmbiguous(runtime({ avg_cpu_pct: 0.5 }))).toBe(false)
  })

  it('★ last_update 非 null ⇒ 判有指标', () => {
    expect(opsRuntimeHasMetrics(runtime())).toBe(true)
  })

  it('★ last_update 是 null ⇒ 不判有指标（反向）', () => {
    expect(opsRuntimeHasMetrics(runtime({ last_update: null }))).toBe(false)
  })

  it('★ ★ 空串是**不可达**形态 ⇒ `!== null` 与真值判断在本族可证等价', () => {
    // ★ `last_update` 是 Go 的 `*time.Time` 直接塞进 map：nil ⇒ JSON `null`；
    //   非 nil ⇒ RFC3339 串（零值也是 `"0001-01-01T00:00:00Z"`，不是空串）。
    //   ⇒ 可达集合只有 {null, 非空串}，两者真值与 `!== null` 完全一致
    //   ⇒ 变异「改成真值」属**可证等价**（#52 实测 STILL_GREEN）。
    const r = runtime({ last_update: '' })
    expect(Boolean(r.last_update)).toBe(false)
    expect(opsRuntimeHasMetrics(r)).toBe(true) // ← 按契约，空串不在这族里
    expect(opsRuntimeLastUpdateIsNull(r)).toBe(false)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// fetch 端到端
// ═════════════════════════════════════════════════════════════════════════════

describe('fetch 端到端', () => {
  it('★ 满配响应被解包器放行', async () => {
    okFull()
    const r = await fetchOpsOverview()
    expect(opsSectionCount(r)).toBe(11)
    expect(opsGeneratedAtIsSecondPrecision(r.generated_at)).toBe(true)
  })

  it('★ 形状不符 ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ nope: true }))
    await expect(fetchOpsOverview()).rejects.toThrow(/运维总览 缺/)
  })

  it('★ 单个子查询失败（键缺）⇒ 仍然 200 且能解析', async () => {
    const d = { ...full() } as Record<string, unknown>
    delete d['fault_stats']
    fetchMock.mockResolvedValueOnce(jsonResponse(d))
    const r = await fetchOpsOverview()
    expect(opsHasSection(r, 'fault_stats')).toBe(false)
    expect(opsRecentFaultsWithinOpenEvents(r)).toBe(false)
  })

  it('★ ★ 只有 generated_at ⇒ 仍然 200（后端只在全失败时才 500）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ generated_at: '2026-10-08T02:00:00Z' }))
    const r = await fetchOpsOverview()
    expect(opsIsFullyDegraded(r)).toBe(true)
    expect(r.generated_at).toBe('2026-10-08T02:00:00Z')
  })
})