import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import OpsOverviewView from './OpsOverviewView.vue'
import { setLocale, t } from '@/i18n'
import { DRAWER_NAV } from '@/config/appNav'
import {
  fetchOpsOverview,
  opsOverviewRoleIsAllowed,
  opsOverviewDbNotConfiguredMessage,
  opsMissingSectionKeys,
  opsSectionCount,
  opsResponseKeyCountIsInRange,
  opsDataPlaneKeysAreComplete,
  opsCenterStatusCountsFitTotal,
  opsListSectionTotalsMatchLengths,
  opsListSectionMismatchKinds,
  opsRegionPreambleCoversExpected,
  opsOfflineTimestampMirrorsCreatedAt,
  opsRuntimeHasMetrics,
  opsRuntimeLastUpdateIsNull,
  OPS_OVERVIEW_SECTION_KEYS,
  OPS_DATA_PLANE_KEYS,
  OPS_EXPECTED_REGIONS,
  OPS_DATA_PLANE_UNAVAILABLE,
  type OpsOverviewResponse,
  type OpsRegionStat,
  type OpsOfflineRequest,
  type OpsRuntimeMetric,
  type OpsRecentUpgrade,
} from '@/api/opsOverview'

/**
 * 运维总览面回归护栏（2026-10-14，第一百一十七批）。
 *
 * ★★★★★★ **权限档是 `'super_admin'`，但成因与批 113/114 相反、也与批 116 不同。**
 *   批 113/114 是「服务端**有**租户维度、只是租户键可能返空串」⇒ 用 `'admin'`；
 *   批 116 是「`SuperAdminMiddleware` **逐字比 role**，档位本身就是真门」；
 *   本批是**第三类**：服务端**完全没有租户维度** ⇒ 只能**过度限制**。
 *   四处逐字核过，写在 ① 组：
 *   ① `admin/handler.go:1091` 传 `admin(...)` = `h.admin` → `AdminMiddleware`；
 *   ② `admin/auth.go:85-140` 全文**没有** `claims.Role` 比较；
 *   ③ `admin/ops_overview.go` 全文**没有** `AuthContext` / `IsTenantAdmin` / `Role`；
 *   ④ **取租户键的代码根本不存在** —— `grep tenant` 打不到不是遗漏。
 *
 * ★★★★ **本族「零调用方」不是缺陷**：桌面把 `/ops` 页面 `externalMaintainRedirect`
 *   到独立 maintain 服务（`api/opsOverview.ts` 模块头 (2)），但**这个 API 仍由本进程
 *   的 admin mux 提供**（`ops` 不在 `cmd/gateway/maintain_proxy.go:37-54` 的
 *   `maintainCompatPrefixes` 里）⇒ 移动端这一页是**唯一**能读到它的 UI。
 *
 * 判据钉十二组不变量：
 *  ① ★★★★★★ 权限档 = `'super_admin'`（四处源码断言）+ 深链兜底**连请求都不发**。
 *  ② ★★★★★ 键数恒在 2..12；缺段可列；`generated_at` 恒秒精度；全降级要报出来。
 *  ③ ★★★★★ `data_plane_tables` **恒在且六项齐全**（错误被吞成 `-1`）。
 *  ④ ★★★★ `center_stats` 三态之和 ≤ total。
 *  ⑤ ★★★★ `region_stats` 前三行恒是硬编码三region；占位行恒全 0。
 *  ⑥ ★★★ 部署节点空数组与非 GET 到的缺失**分开显示**。
 *  ⑦ ★★★★★ 故障：`recent_faults.total ≤ open_events`、每行 `status === 'new'`、
 *     `total === events.length`。
 *  ⑧ ★★★★ 升级：`version` 可能是**空串**，`completed_at` 是条件键。
 *  ⑨ ★★★★ 离线激活：`timestamp === created_at`；条件键缺席要显式说「无」。
 *  ⑩ ★★★★ 运行时：裸 `null` vs 键缺失**两种 nil 编码**、0 的二义、离线也上榜。
 *  ⑪ ★★★ 错误归因 + 请求失败后**清空**上一轮数据，不留陈旧。
 *  ⑫ ★★★ 触控 ≥48 CSS px、路由/抽屉/i18n 接线、编码门。
 */

// ── 夹具：逐字照抄 Go 侧的构造，并**守住**模块头写明的不变式 ────────────────

function region(over: Partial<OpsRegionStat> = {}): OpsRegionStat {
  return {
    region: 'local',
    total_instances: 2,
    online_instances: 1,
    offline_instances: 1,
    degraded_instances: 0,
    last_heartbeat: '2026-10-14T02:00:00Z',
    ...over,
  }
}

/** 占位行（`ops_overview.go:211-218`）：四个计数恒 0 + `missing: true`，**没有** last_heartbeat。 */
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
    // ★★ 同一个值写了两遍（`:328-329`）—— 判据会逐行钉它相等。
    timestamp: '2026-10-14T01:00:00Z',
    created_at: '2026-10-14T01:00:00Z',
    status: 'pending',
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
    last_update: '2026-10-14T02:00:00Z',
    ...over,
  }
}

function upgrade(over: Partial<OpsRecentUpgrade> = {}): OpsRecentUpgrade {
  return {
    instance_id: 'inst-1',
    status: 'completed',
    version: 'v1.2.3',
    started_at: '2026-10-14T00:00:00Z',
    completed_at: '2026-10-14T00:05:00Z',
    ...over,
  }
}

/** 满配响应：十一个子查询键 + generated_at（12 键 = 键数上限）。 */
function full(over: Partial<OpsOverviewResponse> = {}): OpsOverviewResponse {
  return {
    // ★ 秒级、无小数（`ops_overview.go:115` 的 `Format(time.RFC3339)`）。
    generated_at: '2026-10-14T02:00:00Z',
    center_stats: { total_instances: 3, online_instances: 2, offline_instances: 1, degraded_instances: 0 },
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
        last_heartbeat: '2026-10-14T02:00:00Z',
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
          // ★ SQL 硬编码 status = 'new'（`:363`）。
          status: 'new',
          title: '磁盘将满',
          description: '90% 已用',
          source: 'disk',
          detected_at: '2026-10-14T01:30:00Z',
        },
      ],
      total: 1,
    },
    recent_upgrades: { items: [upgrade()], total: 1 },
    download_stats: { today_downloads: 1, week_downloads: 4, total_downloads: 8, supporter_count: 3 },
    runtime_metrics_summary: [runtime()],
    ...over,
  }
}

// ── 装配 ──────────────────────────────────────────────────────────────────

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { role: 'super_admin' }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/opsOverview', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/opsOverview')>()
  return { ...actual, fetchOpsOverview: vi.fn() }
})

const mFetch = fetchOpsOverview as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(OpsOverviewView)
  await flushPromises()
  return w
}

function byId(w: W, id: string) {
  return w.find(`[data-testid="${id}"]`)
}
function hasId(w: W, id: string): boolean {
  return w.findAll(`[data-testid="${id}"]`).length > 0
}
function click(w: W, id: string) {
  return byId(w, id).trigger('click').then(() => flushPromises())
}
/** ★★ 文本断言一律用 `text()`，**不用** `html()` —— 后者会序列化模板注释。 */
function textOf(w: W, id: string): string {
  return byId(w, id).text()
}

function viewSrc(): string {
  return readFileSync(join(process.cwd(), 'src/views/OpsOverviewView.vue'), 'utf8')
}
/** ★ 读源码前先让路径写错**硬失败**，否则 expect 落在别处很难查。 */
function goSrc(rel: string): string {
  const p = join(process.cwd(), '..', rel)
  expect(existsSync(p), `源码路径不存在：${rel}`).toBe(true)
  return readFileSync(p, 'utf8')
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  authState.role = 'super_admin'
  mFetch.mockResolvedValue(full())
})

async function load(w: W) {
  await click(w, 'opsv-load')
}

// ══════════════════════════════════════════════════════════════════════════
// ① 权限档
// ══════════════════════════════════════════════════════════════════════════

describe('① 权限档 super_admin（第三类：服务端完全没有租户维度）', () => {
  it('①-1 ★★★★★★ 只有 super_admin 能进', () => {
    expect(opsOverviewRoleIsAllowed('super_admin')).toBe(true)
    expect(opsOverviewRoleIsAllowed('tenant_admin')).toBe(false)
    expect(opsOverviewRoleIsAllowed('user')).toBe(false)
    expect(opsOverviewRoleIsAllowed('')).toBe(false)
  })

  it('①-2 ★★★★★ 抽屉席存在且是 super_admin 档', () => {
    const seat = DRAWER_NAV.find((i) => i.key === 'ops-overview')
    expect(seat).toBeTruthy()
    expect(seat!.to).toBe('/ops-overview')
    expect(seat!.requiresRole).toBe('super_admin')
  })

  it('①-3 ★★★★★★ 读 Go 源码：注册处那一行**逐字**是 admin(...)（不是 superAdmin）', () => {
    const src = goSrc('admin/handler.go')
    // ★★ 钉住**注册处本身的形态**。这一族要是哪天改成 h.superAdmin，
    //   ①-1/①-2 仍然全绿（它们只看前端档位）⇒ 缺了这条就没人知道后端变严了。
    expect(src).toContain(
      'mux.HandleFunc("/api/admin/ops/overview", admin(h.handleOpsOverview))',
    )
    expect(src).not.toContain('mux.HandleFunc("/api/admin/ops/overview", h.superAdmin(')
  })

  it('①-4 ★★★★★★ 读 Go 源码：AdminMiddleware **不检 role**（这是本族归第三类的根据）', () => {
    const src = goSrc('admin/auth.go')
    const body = src.slice(src.indexOf('func AdminMiddleware('), src.indexOf('func isPasswordChangeAllowedPath('))
    // ★ 三条断言缺一不可：函数在、中间体里没有 role 比较、而 SuperAdmin 那一档**有**。
    expect(body).not.toContain('claims.Role')
    expect(body).not.toContain('super_admin')
    expect(src).toContain('if claims.Role != "super_admin" {')
  })

  it('①-5 ★★★★★★ 读 Go 源码：handler 自己既不检 role 也不取租户键', () => {
    const src = goSrc('admin/ops_overview.go')
    expect(src).not.toMatch(/claims\.Role|IsSuperAdmin|IsTenantAdmin|AuthContext/)
    // ★★ 「完全没有租户维度」的正向形态：**没有任何 tenant 条件**。
    //   用 `not.toContain('tenant')` 会被注释里的字顶红，所以钉在 SQL 形态上。
    expect(src).not.toMatch(/WHERE[^;]*tenant_id/i)
    expect(src).not.toMatch(/EffectiveTenantID/)
  })

  it('①-6 ★★★★★★ 11 条子查询全是全局表（逐字列出，别靠印象）', () => {
    const src = goSrc('admin/ops_overview.go')
    for (const t of [
      'gateway_instances',
      'licenses',
      'fault_events',
      'upgrade_logs',
      'download_events',
      'offline_activation_requests',
      'runtime_metrics',
    ]) {
      expect(src, `${t} 没出现在子查询里`).toContain(`FROM ${t}`)
    }
    // ★ 且子查询确实是 11 个（模块头原先写「10 个」是错的）。
    expect(OPS_OVERVIEW_SECTION_KEYS.length).toBe(11)
  })

  it('①-7 ★★★★★★ 深链兜底：非超管**连请求都不发**', async () => {
    authState.role = 'tenant_admin'
    const w = await mountView()
    expect(hasId(w, 'opsv-nope')).toBe(true)
    expect(mFetch).not.toHaveBeenCalled()
  })

  it('①-7b ★★★★★★ 兜底段下**没有按钮可点**（深链进来就点不着 —— 这才是「不发请求」）', async () => {
    // ★★★ 本页**不自动加载** ⇒ 只断言「挂载时没发请求」是**恒真**的半个判据
    //   （批 116 的 S4 变异 NO_EFFECT 就是这么顶出来的）。真正的风险面是
    //   「兜底段有没有把取数入口留在屏上」，所以钉按钮与输入框都不存在。
    authState.role = 'tenant_admin'
    const w = await mountView()
    expect(hasId(w, 'opsv-load')).toBe(false)
    expect(w.findAll('button').length).toBe(0)
  })

  it('①-7c ★★★★ 超管档下那个按钮**在**（与 ①-7b 成对的阴性对照）', () => {
    // ★ 若 ①-7b 只是因为「按钮本来就没渲染」而绿，这条会红。
    const w = mount(OpsOverviewView)
    expect(hasId(w, 'opsv-load')).toBe(true)
  })

  it('①-8 ★★★★ 兜底段要说清「是前端拦的」，且**不**谎称后端 403', async () => {
    authState.role = 'user'
    const w = await mountView()
    expect(textOf(w, 'opsv-nope-msg')).toBe(t('ops.nope.msg'))
    expect(textOf(w, 'opsv-nope-why')).toBe(t('ops.nope.why'))
    // ★★ 本族**没有** forbidden 分支（后端没有 role 门）⇒ 不能出现 403 归因。
    //   断言的是**代码形态**：`ops.err` 下不该有 forbidden 键。
    const zh = readFileSync(join(process.cwd(), 'src/i18n/zh-CN.ts'), 'utf8')
    expect(zh).not.toMatch(/ops: \{[\s\S]*?forbidden:/)
  })

  it('①-9 ★★★ 超管**不**看到兜底段', async () => {
    const w = await mountView()
    expect(hasId(w, 'opsv-nope')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ② 采集完整性
// ══════════════════════════════════════════════════════════════════════════

describe('② 采集完整性：键数 2..12 + 秒精度', () => {
  it('②-1 ★★★★★ 满配响应键数是 12 且在范围内', () => {
    expect(Object.keys(full()).length).toBe(12)
    expect(opsResponseKeyCountIsInRange(full())).toBe(true)
  })

  it('②-2 ★★★★★ 页面显示成功段数与响应键数', async () => {
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'opsv-section-count')).toBe('11')
    expect(textOf(w, 'opsv-sections')).toBe(t('ops.fresh.sections', { n: 11, total: 11 }))
    expect(textOf(w, 'opsv-key-count')).toBe(t('ops.fresh.keyCount', { n: 12 }))
    expect(textOf(w, 'opsv-generated-at')).toBe('2026-10-14T02:00:00Z')
  })

  it('②-3 ★★★★★ 缺一个子查询段 ⇒ 不报错，但要**列出缺的是哪一段**', async () => {
    const d = { ...full() }
    delete (d as Record<string, unknown>)['fault_stats']
    mFetch.mockResolvedValue(d)
    const w = await mountView()
    await load(w)
    expect(opsSectionCount(d)).toBe(10)
    expect(opsMissingSectionKeys(d)).toEqual(['fault_stats'])
    expect(textOf(w, 'opsv-section-count')).toBe('10')
    expect(textOf(w, 'opsv-missing')).toBe(t('ops.fresh.missing', { list: 'fault_stats' }))
    // ★ 「该出现」的一面：拿不到的那段要有**自己的缺席提示**。
    expect(hasId(w, 'opsv-fault-absent')).toBe(true)
  })

  it('②-4 ★★★★★ 键数 13（多一个键）⇒ 报越界', async () => {
    const d = { ...full(), nope: 1 } as unknown as OpsOverviewResponse
    mFetch.mockResolvedValue(d)
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-keycount-bad')).toBe(true)
  })

  it('②-5 ★★★★★ `generated_at` 带小数点 ⇒ 报精度异常（秒级才是正常形态）', async () => {
    mFetch.mockResolvedValue(full({ generated_at: '2026-10-14T02:00:00.123456Z' }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-generated-bad')).toBe(true)
  })

  it('②-6 ★★★★ 秒精度时**不**报精度异常（阴性对照）', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-generated-bad')).toBe(false)
  })

  it('②-7 ★★★★ 一个子查询都没成功 ⇒ 报「全降级」', async () => {
    mFetch.mockResolvedValue({ generated_at: '2026-10-14T02:00:00Z' })
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-degraded')).toBe(true)
    expect(hasId(w, 'opsv-keycount-bad')).toBe(true)
  })

  it('②-8 ★★★ 未读取时显示「尚未读取」，不是空壳', async () => {
    const w = await mountView()
    expect(mFetch).not.toHaveBeenCalled()
    expect(hasId(w, 'opsv-empty')).toBe(true)
    expect(hasId(w, 'opsv-generated-at')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ③ data_plane_tables
// ══════════════════════════════════════════════════════════════════════════

describe('③ data_plane_tables：恒在且六项齐全', () => {
  it('③-1 ★★★★★ 六项全渲染，且不报不齐', async () => {
    const w = await mountView()
    await load(w)
    for (const k of OPS_DATA_PLANE_KEYS) {
      expect(hasId(w, `opsv-dataplane-row-${k}`), `缺行 ${k}`).toBe(true)
    }
    expect(hasId(w, 'opsv-dataplane-bad')).toBe(false)
    expect(opsDataPlaneKeysAreComplete(full().data_plane_tables!)).toBe(true)
  })

  it('③-2 ★★★★★ 少一项 ⇒ 报不齐（这一段本应恒存在）', async () => {
    const d = { ...full().data_plane_tables! }
    delete (d as Record<string, unknown>)['license_devices']
    mFetch.mockResolvedValue(full({ data_plane_tables: d }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-dataplane-bad')).toBe(true)
  })

  it('③-3 ★★★★★ 值是 -1 时显示为「查询失败」计数，**不是**计数本身', async () => {
    const d = { ...full().data_plane_tables! }
    for (const k of OPS_DATA_PLANE_KEYS) d[k] = OPS_DATA_PLANE_UNAVAILABLE
    mFetch.mockResolvedValue(full({ data_plane_tables: d }))
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'opsv-dataplane-unavailable')).toBe(
      t('ops.dataPlane.unavailable', { n: OPS_DATA_PLANE_KEYS.length }),
    )
    // ★ 哨兵不是缺键 ⇒ **不该**报不齐。
    expect(hasId(w, 'opsv-dataplane-bad')).toBe(false)
  })

  it('③-4 ★★★ 整段缺失 ⇒ 显示缺席，且**不**报「不齐」（那是两件事）', async () => {
    mFetch.mockResolvedValue(full({ data_plane_tables: undefined }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-dataplane-absent')).toBe(true)
    expect(hasId(w, 'opsv-dataplane-bad')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ④ center_stats
// ══════════════════════════════════════════════════════════════════════════

describe('④ center_stats：三态之和 ≤ total', () => {
  it('④-1 ★★★★ 正常响应不报不自洽', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-center-bad')).toBe(false)
    expect(opsCenterStatusCountsFitTotal(full().center_stats!)).toBe(true)
    expect(hasId(w, 'opsv-center-card')).toBe(true)
  })

  it('④-2 ★★★★ 三态之和大于 total ⇒ 报错', async () => {
    mFetch.mockResolvedValue(
      full({ center_stats: { total_instances: 1, online_instances: 1, offline_instances: 1, degraded_instances: 0 } }),
    )
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-center-bad')).toBe(true)
  })

  it('④-3 ★★★ 和**小于** total 也合法（存在第四种状态）', async () => {
    mFetch.mockResolvedValue(
      full({ center_stats: { total_instances: 9, online_instances: 2, offline_instances: 1, degraded_instances: 0 } }),
    )
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-center-bad')).toBe(false)
  })

  it('④-4 ★★★ 两个统计段都缺 ⇒ 显示「本段没有取到」', async () => {
    mFetch.mockResolvedValue(full({ center_stats: undefined, download_stats: undefined }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-summary-empty')).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑤ region_stats
// ══════════════════════════════════════════════════════════════════════════

describe('⑤ region_stats：三个硬编码 region 恒在', () => {
  it('⑤-1 ★★★★★ 三个硬编码 region 恒在前三行（占位行也算）', async () => {
    const w = await mountView()
    await load(w)
    expect(opsRegionPreambleCoversExpected(full().region_stats!)).toBe(true)
    expect(hasId(w, 'opsv-region-preamble-bad')).toBe(false)
    for (const r of OPS_EXPECTED_REGIONS) {
      expect(textOf(w, `opsv-region-row-${r}-${OPS_EXPECTED_REGIONS.indexOf(r)}`)).toContain(r)
    }
  })

  it('⑤-2 ★★★★★ 前三行顺序被打乱 ⇒ 报前言坏', async () => {
    // ★★★ 判据首版把「占位行 ↔ 真实行」互换当破坏，那**不是**破坏：
    //   `opsRegionPreambleCoversExpected` 只看 `region` 名（`:497-500`）⇒ 名字没变就仍然成立
    //   ⇒ 首版这条会恒绿。真正的前言破坏是**顺序**不对。
    mFetch.mockResolvedValue(full({ region_stats: [region({ region: '245' }), placeholder('local'), placeholder('154')] }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-region-preamble-bad')).toBe(true)
  })

  it('⑤-3 ★★★★ 额外行排在硬编码三行之后 ⇒ **不**报穿插', async () => {
    const regions = [region(), placeholder('245'), placeholder('154'), region({ region: 'hk' })]
    mFetch.mockResolvedValue(full({ region_stats: regions }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-region-extra-bad')).toBe(false)
  })

  it('⑤-4 ★★★★ 额外行里混进硬编码 region ⇒ 报穿插', async () => {
    const regions = [region(), placeholder('245'), placeholder('154'), region({ region: '154' })]
    mFetch.mockResolvedValue(full({ region_stats: regions }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-region-extra-bad')).toBe(true)
  })

  it('⑤-5 ★★★★★ 占位行打标记，且计数**显示为 0**（不是「无」）', async () => {
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'opsv-region-placeholder')).toBe(t('ops.region.placeholder', { n: 2 }))
    expect(hasId(w, 'opsv-region-missing-tag')).toBe(true)
    const row = textOf(w, 'opsv-region-row-245-1')
    expect(row).toContain('0')
    // ★ 占位行**没有** `last_heartbeat` ⇒ 显示「（无）」，不是空串也不是 null。
    expect(row).toContain(t('ops.field.absent'))
  })

  it('⑤-6 ★★★★ 占位行却带非 0 计数 ⇒ 报错', async () => {
    mFetch.mockResolvedValue(
      full({ region_stats: [region(), { ...placeholder('245'), total_instances: 3 }, placeholder('154')] }),
    )
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'opsv-region-placeholder-bad')).toBe(t('ops.region.placeholderBad', { n: 1 }))
  })

  it('⑤-7 ★★★ 真实行的 `last_heartbeat` 是**键**存在（与运行时的裸 null 不同）', async () => {
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'opsv-region-row-local-0')).toContain('2026-10-14T02:00:00Z')
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑥⑦ 部署节点与故障
// ══════════════════════════════════════════════════════════════════════════

describe('⑥⑦ 部署节点与故障', () => {
  it('⑥-1 ★★★ 空数组 ⇒ 显示「没有节点记录」', async () => {
    mFetch.mockResolvedValue(full({ deployment_nodes: [] }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-nodes-empty')).toBe(true)
    expect(hasId(w, 'opsv-nodes-absent')).toBe(false)
  })

  it('⑥-2 ★★★★ 段缺失 ⇒ 显示缺席（与空数组**分开**）', async () => {
    mFetch.mockResolvedValue(full({ deployment_nodes: undefined }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-nodes-absent')).toBe(true)
    expect(hasId(w, 'opsv-nodes-empty')).toBe(false)
  })

  it('⑦-1 ★★★★★ 跨子查询上界：`recent_faults.total ≤ open_events`', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-fault-range-bad')).toBe(false)
    expect(textOf(w, 'opsv-fault-open')).toBe('7')
  })

  it('⑦-2 ★★★★★ 新故障数大于未闭环总数 ⇒ 报错', async () => {
    mFetch.mockResolvedValue(full({ fault_stats: { open_events: 0 } }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-fault-range-bad')).toBe(true)
  })

  it('⑦-3 ★★★★★ 存在 status 不是 new 的事件 ⇒ 报错（SQL 硬编码过）', async () => {
    const r = full()
    r.recent_faults!.events[0]!.status = 'resolving'
    mFetch.mockResolvedValue(r)
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-fault-status-bad')).toBe(true)
  })

  it('⑦-4 ★★★★★ total 与 events 长度不符 ⇒ 报错，并说清是哪一段', async () => {
    mFetch.mockResolvedValue(full({ recent_faults: { events: [], total: 3 } }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-totals-bad')).toBe(true)
    expect(textOf(w, 'opsv-totals-bad')).toBe(t('ops.fault.totalsBad', { list: 'recent_faults' }))
    // ★ 判据层也要能说出是哪一段（只给布尔的话用户看不出是谁不对）。
    expect(opsListSectionMismatchKinds(full({ recent_faults: { events: [], total: 3 } }))).toEqual(['recent_faults'])
  })

  it('⑦-5 ★★★★ 两段都在且都自洽 ⇒ 不报错（阴性对照）', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-totals-bad')).toBe(false)
    expect(opsListSectionTotalsMatchLengths(full())).toBe(true)
  })

  it('⑦-6 ★★★ 只有升级段对不上 ⇒ 清单里**只有**它', async () => {
    const r = full({ recent_upgrades: { items: [upgrade()], total: 0 } })
    mFetch.mockResolvedValue(r)
    const w = await mountView()
    await load(w)
    expect(opsListSectionMismatchKinds(r)).toEqual(['recent_upgrades'])
    expect(textOf(w, 'opsv-totals-bad')).toBe(t('ops.fault.totalsBad', { list: 'recent_upgrades' }))
  })

  it('⑦-7 ★★★ 两个容器都缺 ⇒ **不**报 total 不自洽（缺键不是违规）', async () => {
    // ★ 本批第一版注释在这里与代码矛盾，被判据顶红后改成
    //   「缺键不构成违规 + 单独给违规清单」的口径。这条钉住那个口径。
    // ★★ 而这条判据**自己**也曾是恒真的：红线当时嵌在 `<template v-if="recentFaults">` 里
    //   ⇒ 两个容器都缺时那一行不渲染 ⇒ 断言恒绿。是变异台 S25（把 totalsBad 改成恒真）
    //   只转红了 ⑦-5 而**没有**转红 ⑦-7 顶出来的 ⇒ 处置是把红线搬到采集完整性段。
    mFetch.mockResolvedValue(full({ recent_faults: undefined, recent_upgrades: undefined }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-totals-bad')).toBe(false)
    expect(opsListSectionMismatchKinds({ generated_at: 'x' })).toEqual([])
  })

  it('⑦-7b ★★★★★★ 那条红线**不依赖**任何一段存在（防止再被嵌回条件块里）', async () => {
    // ★★★★ 这条是 ⑦-7 的**阳性孪生**，也是把 ⑦-7 从恒真救回来的那一条：
    //   构造一份「故障段整段缺席、但**升级**段对不上」的响应。
    //   · 若红线仍在采集完整性段 ⇒ 它**必须**渲染（这是响应级不变式）；
    //   · 若有人把它挪回 `<template v-if="recentFaults">` ⇒ 故障段缺席 ⇒ 它**永远不渲染**
    //     ⇒ ⑦-7 会重新变成恒真而无人察觉，**这条立刻红**。
    mFetch.mockResolvedValue(
      // ★ 三项一起拿掉 ⇒ 故障段**整段**不存在（fault_stats 与 recent_faults 都缺）。
      full({ fault_stats: undefined, recent_faults: undefined, recent_upgrades: { items: [upgrade()], total: 5 } }),
    )
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-fault-absent')).toBe(true)
    // ★ 关键：故障段不存在，红线**照样**在屏上。
    expect(hasId(w, 'opsv-totals-bad')).toBe(true)
    expect(textOf(w, 'opsv-totals-bad')).toBe(t('ops.fault.totalsBad', { list: 'recent_upgrades' }))
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑧⑨⑩ 升级 / 离线激活 / 运行时指标
// ══════════════════════════════════════════════════════════════════════════

describe('⑧⑨⑩ 升级 / 离线激活 / 运行时指标', () => {
  it('⑧-1 ★★★★ `version` 是空串 ⇒ 显式说「空值」，不显示成「无」', async () => {
    const u = upgrade()
    delete (u as unknown as Record<string, unknown>)['completed_at']
    mFetch.mockResolvedValue(full({ recent_upgrades: { items: [{ ...u, version: '' }], total: 1 } }))
    const w = await mountView()
    await load(w)
    const row = textOf(w, 'opsv-upgrade-row-0')
    expect(row).toContain(t('ops.upgrade.versionEmpty'))
  })

  it('⑧-2 ★★★★ `completed_at` 缺席（进行中的升级）⇒ 显示「（无）」', async () => {
    const u = upgrade()
    delete (u as unknown as Record<string, unknown>)['completed_at']
    mFetch.mockResolvedValue(full({ recent_upgrades: { items: [u], total: 1 } }))
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'opsv-upgrade-row-0')).toContain(t('ops.field.absent'))
  })

  it('⑧-3 ★★★ 升级列表为空 ⇒ 显示「没有升级记录」', async () => {
    mFetch.mockResolvedValue(full({ recent_upgrades: { items: [], total: 0 } }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-upgrade-empty')).toBe(true)
  })

  it('⑨-1 ★★★★★ 离线激活：timestamp 与 created_at 不等 ⇒ 报错', async () => {
    const bad = offline()
    bad.timestamp = '2026-10-14T00:00:00Z'
    expect(opsOfflineTimestampMirrorsCreatedAt(bad)).toBe(false)
    mFetch.mockResolvedValue(full({ offline_requests: [bad] }))
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'opsv-offline-mirror-bad')).toBe(t('ops.offline.mirrorBad', { n: 1 }))
  })

  it('⑨-2 ★★★★ 相等时**不**报错（同一个值写两遍，本就相等）', async () => {
    mFetch.mockResolvedValue(full({ offline_requests: [offline()] }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-offline-mirror-bad')).toBe(false)
  })

  it('⑨-3 ★★★★★ 条件键缺席时不显示激活码；存在时显示', async () => {
    const a = offline({ approved_at: '2026-10-14T01:10:00Z', activation_code: 'CODE-1' })
    mFetch.mockResolvedValue(full({ offline_requests: [offline(), a] }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-offline-code')).toBe(true)
    expect(textOf(w, 'opsv-offline-approved')).toBe(t('ops.offline.approved', { n: 1 }))
    // ★ 未审批那行的审批时间显示「（无）」，不是空串。
    expect(textOf(w, 'opsv-offline-row-0')).toContain(t('ops.field.absent'))
  })

  it('⑨-4 ★★★ 页面明写本段含别人的密钥（敏感提示不是可选项）', async () => {
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'opsv-offline-note')).toBe(t('ops.offline.note'))
  })

  it('⑩-1 ★★★★★ `last_update` 是裸 null ⇒ 计数报出来，且**不**报成「键缺失」', async () => {
    const rows = [runtime({ instance_id: 'i1' }), runtime({ instance_id: 'i2', last_update: null })]
    expect(opsRuntimeLastUpdateIsNull(rows[1]!)).toBe(true)
    expect(opsRuntimeHasMetrics(rows[1]!)).toBe(false)
    mFetch.mockResolvedValue(full({ runtime_metrics_summary: rows }))
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'opsv-runtime-metrics')).toBe(t('ops.runtime.withMetrics', { n: 1 }))
    expect(textOf(w, 'opsv-runtime-null-update')).toBe(t('ops.runtime.nullUpdate', { n: 1 }))
  })

  it('⑩-2 ★★★★ 三个均值全 0 ⇒ 报「二义」而不是「真的是 0」', async () => {
    mFetch.mockResolvedValue(
      full({ runtime_metrics_summary: [runtime({ avg_cpu_pct: 0, avg_mem_pct: 0, avg_tps: 0 })] }),
    )
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'opsv-runtime-zero')).toBe(t('ops.runtime.zeroAmbiguous', { n: 1 }))
  })

  it('⑩-3 ★★★★★ 离线实例也在榜上 ⇒ 提示「出现在榜上不等于在线」', async () => {
    mFetch.mockResolvedValue(full({ runtime_metrics_summary: [runtime({ status: 'offline' })] }))
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'opsv-runtime-offline')).toBe(t('ops.runtime.offlineRows', { n: 1 }))
  })

  it('⑩-4 ★★★ 运行时段缺失 ⇒ 缺席提示（与「空数组」分开）', async () => {
    mFetch.mockResolvedValue(full({ runtime_metrics_summary: undefined }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-runtime-absent')).toBe(true)
    expect(hasId(w, 'opsv-runtime-empty')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑪ 错误归因与陈旧数据
// ══════════════════════════════════════════════════════════════════════════

describe('⑪ 错误归因与陈旧数据', () => {
  it('⑪-1 ★★★ 503 归因为「未配置数据库」', async () => {
    mFetch.mockRejectedValue(new Error('database not configured'))
    const w = await mountView()
    await load(w)
    expect(opsOverviewDbNotConfiguredMessage('database not configured')).toBe(true)
    expect(textOf(w, 'opsv-error')).toBe(t('ops.err.dbNotConfigured'))
  })

  it('⑪-2 ★★★ 未知错误原样透出（不吞）', async () => {
    mFetch.mockRejectedValue(new Error('boom 42'))
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'opsv-error')).toBe('boom 42')
  })

  it('⑪-3 ★★★★ 请求失败后清空上一轮数据，不留陈旧', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'opsv-node-row-inst-1')).toBe(true)
    mFetch.mockRejectedValue(new Error('boom'))
    await load(w)
    expect(hasId(w, 'opsv-node-row-inst-1')).toBe(false)
    expect(hasId(w, 'opsv-error')).toBe(true)
  })

  it('⑪-4 ★★★ 出错时**不**同时渲染旧正文', async () => {
    const w = await mountView()
    await load(w)
    mFetch.mockRejectedValue(new Error('boom'))
    await load(w)
    expect(hasId(w, 'opsv-generated-at')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑫ 触控与接线
// ══════════════════════════════════════════════════════════════════════════

describe('⑫ 触控与接线', () => {
  it('⑫-1 ★★★ 新增触控控件 ≥48 CSS px', () => {
    const src = viewSrc()
    const re = /\.opsv__btn[\s\S]{0,400}?min-height:\s*(\d+)px/
    const m = re.exec(src)
    expect(m, '找不到 opsv__btn 的 min-height').toBeTruthy()
    // ★ 把数值**解析出来**再比大小，不要 `toContain('48px')`（那样写 480 也过）。
    expect(Number(m![1]), '触控高度不足 48').toBeGreaterThanOrEqual(48)
  })

  it('⑫-2 ★★★★ 路由存在且 meta 是 super_admin', () => {
    const src = readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8')
    expect(src).toContain("path: '/ops-overview'")
    expect(src).toContain("component: () => import('@/views/OpsOverviewView.vue')")
    expect(src).toMatch(/ops\.title'[^}]*requiresRole: 'super_admin'/)
  })

  it('⑫-3 ★★★ i18n 两侧都有 ops 前缀且含 nav 标题键', () => {
    const zh = readFileSync(join(process.cwd(), 'src/i18n/zh-CN.ts'), 'utf8')
    const en = readFileSync(join(process.cwd(), 'src/i18n/en-US.ts'), 'utf8')
    expect(zh).toContain('  ops: {')
    expect(en).toContain('  ops: {')
    expect(zh).toContain("opsOverview: '运维总览',")
    expect(en).toContain("opsOverview: 'Ops overview',")
  })

  it('⑫-4 ★★★ 只发**一个**请求（聚合包端点，不拆成多次往返）', () => {
    const src = viewSrc()
    expect(src).toContain('fetchOpsOverview')
    // ★ 钉「只导入了那一个 fetch」：形态断言，不能用 not.toContain('fetch')
    //   —— 文件头那句「11 个子查询」里就有个 fetch 子串吗？这里是防未来再引入第二个 fetch。
    expect(src.match(/fetchOpsOverview\(/g)?.length).toBe(1)
  })

  it('⑫-5 ★★★★ 视图用到的 i18n 键两侧都存在（缺一侧会被 i18n:check 拦，这里再钉一次）', () => {
    const src = viewSrc()
    const keys = [...src.matchAll(/t\('(ops\.[a-zA-Z0-9_.]+)'/g)].map((m) => m[1]!)
    expect(keys.length).toBeGreaterThan(60)
    const zh = readFileSync(join(process.cwd(), 'src/i18n/zh-CN.ts'), 'utf8')
    const en = readFileSync(join(process.cwd(), 'src/i18n/en-US.ts'), 'utf8')
    for (const k of keys) {
      const leaf = k.split('.').pop()!
      expect(zh.includes(leaf), `zh 缺 ${k}`).toBe(true)
      expect(en.includes(leaf), `en 缺 ${k}`).toBe(true)
    }
  })

  it('⑫-6 ★★★★★★ 本批源文件里没有 U+FFFD，也没有 NUL', () => {
    for (const f of ['src/views/OpsOverviewView.vue', 'src/views/OpsOverviewView.spec.ts', 'src/api/opsOverview.ts']) {
      const p = join(process.cwd(), f)
      expect(existsSync(p), `文件不存在：${f}`).toBe(true)
      const s = readFileSync(p, 'utf8')
      expect(s.includes('\uFFFD'), `${f} 含 U+FFFD 替换字符（编码损坏）`).toBe(false)
      expect(s.includes('\u0000'), `${f} 含 NUL`).toBe(false)
    }
  })

  it('⑫-7 ★★★ 用例标题里不得出现替换字符（变异台按 fullName 子串匹配）', () => {
    const self = readFileSync(join(process.cwd(), 'src/views/OpsOverviewView.spec.ts'), 'utf8')
    let checked = 0
    for (const line of self.split('\n')) {
      if (/^\s*(describe|it)\(/.test(line)) {
        checked++
        expect(line, '用例标题行含 U+FFFD').not.toContain('\uFFFD')
      }
    }
    // ★ 必须确认**真的扫到了行**，否则这条会在「正则失配」时恒绿。
    expect(checked).toBeGreaterThan(50)
  })
})