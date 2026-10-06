import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import DashboardOpsView from './DashboardOpsView.vue'
import { useAuthStore } from '@/stores/auth'
import {
  fetchSessionOverview,
  fetchSessionTrend,
  fetchSessionHealth,
  fetchSessionActive,
  fetchModuleStats,
  fetchDashboardErrors,
  fetchDashboardPerformance,
} from '@/api/dashboard'
import { fetchDashboardOperational, fetchErrorDrill } from '@/api/dashboardBoard'
import { setLocale, locale } from '@/i18n'

/**
 * DashboardOpsView 的不变量（2026-10-08，第六十批）。
 *
 * ★ 本批钉住的是**九条端点接上 UI 后最容易糊掉的六处**：
 *   ① **降级 = HTTP 200 + success:true + data 全零**（errors.go:319-334）
 *      ⇒ 数字必须渲染成「无数据」而不是 0，且必须挂免责句。
 *      ⚠️ `degraded` 带 omitempty ⇒ 正常时是**键缺失**而非 false。
 *   ② **「从未运行过」不是降级**（aux.go:66 拿原始 err 算，ErrNoRows 也算 degraded）
 *      ⇒ 措辞必须更弱，且**不能**出现「降级」二字。
 *   ③ **tenant_id 筛选框只给 super_admin / admin_key**
 *      （auth.go:39-45 会把 tenant_admin 填的值静默改写）。
 *   ④ **drill 的 `source` 是条件键**：缺失 = 现算，不是「来源未知」。
 *   ⑤ **days 四种口径**：信封族静默回落 7，drill clamp [1,90]。
 *   ⑥ **session-active 分页在 data 与 metadata 各有一份** ⇒ 不一致要报警。
 *
 * ★ 另有一条来自**类型门**的发现（写用例时才暴露的同类问题）：
 *   `probe_loop` 恒是单键 map（aux.go:61），没有 `running`；
 *   `checks_last_10m` / `total_runs_24h` / `success_rate` 都是 Go 零值，
 *   **恒非 null** ⇒ 判 `=== null` 是恒真判据，必须靠 degraded 兄弟键。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/dashboard', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/dashboard')>()
  return {
    ...actual,
    fetchSessionOverview: vi.fn(),
    fetchSessionTrend: vi.fn(),
    fetchSessionHealth: vi.fn(),
    fetchSessionActive: vi.fn(),
    fetchModuleStats: vi.fn(),
    fetchDashboardErrors: vi.fn(),
    fetchDashboardPerformance: vi.fn(),
  }
})

vi.mock('@/api/dashboardBoard', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/dashboardBoard')>()
  return {
    ...actual,
    fetchDashboardOperational: vi.fn(),
    fetchErrorDrill: vi.fn(),
  }
})

const overviewMock = fetchSessionOverview as unknown as ReturnType<typeof vi.fn>
const trendMock = fetchSessionTrend as unknown as ReturnType<typeof vi.fn>
const healthMock = fetchSessionHealth as unknown as ReturnType<typeof vi.fn>
const activeMock = fetchSessionActive as unknown as ReturnType<typeof vi.fn>
const moduleMock = fetchModuleStats as unknown as ReturnType<typeof vi.fn>
const errorsMock = fetchDashboardErrors as unknown as ReturnType<typeof vi.fn>
const perfMock = fetchDashboardPerformance as unknown as ReturnType<typeof vi.fn>
const operationalMock = fetchDashboardOperational as unknown as ReturnType<typeof vi.fn>
const drillMock = fetchErrorDrill as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

/**
 * 挂载并设置角色。
 * ★ auth store 的 role 来自 userInfo（localStorage + hydrate），
 *   这里直接写 userInfo 以免依赖网络。
 */
async function mountView(role = 'super_admin'): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const auth = useAuthStore()
  auth.userInfo = { username: 'u', role } as never
  const w = mount(DashboardOpsView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  return w
}

type W = ReturnType<typeof mount>

/** 段顺序即模板顺序：0 overview / 1 trend / 2 health / 3 active / 4 modules / 5 errors / 6 perf / 7 operational / 8 drill */
function sectionOf(w: W, index: number) {
  return w.findAll('.do__section')[index]!
}

/** 第 n 个「加载/重新加载」按钮（0..6 是前七段，drill 段按钮在 .do__filters 内）。 */
function loadBtn(w: W, n: number) {
  return w.findAll('.do__load')[n]!
}
async function clickLoad(w: W, n: number): Promise<void> {
  await loadBtn(w, n).trigger('click')
  await flushPromises()
  await flushPromises()
}

function countOccurrences(haystack: string, needle: string): number {
  return haystack.split(needle).length - 1
}

/* ── 夹具：逐字照抄后端 ─────────────────────────────────────────────── */

/** 正常信封（types.go:42-72 的 Response + Metadata）。★ 不带 degraded 键（omitempty）。 */
function envelope(data: unknown, metadata: Record<string, unknown> = {}) {
  return {
    success: true,
    data,
    metadata: { generated_at: '2026-10-08T03:00:00Z', ...metadata },
    timestamp: '2026-10-08T03:00:00Z',
  }
}

const OVERVIEW_FULL = {
  total_sessions: 1200,
  active_sessions: 40,
  new_sessions_24h: 90,
  closed_sessions_24h: 70,
  health_distribution: { a: 10 },
  compliance_stats: { total: 1200 },
  cost_stats: { total_cost_usd: 42 },
  model_usage: [],
  top_clients: [],
  top_tasks: [],
  cost_trend: [],
  session_trend: [],
  generated_at: '2026-10-08T03:00:00Z',
  period_start: '2026-10-01T00:00:00Z',
  period_end: '2026-10-08T00:00:00Z',
}

/** ★ writeDegraded 的形状（errors.go:319-334）：data 全零 + degraded:true。 */
const OVERVIEW_DEGRADED = {
  ...OVERVIEW_FULL,
  total_sessions: 0,
  active_sessions: 0,
  new_sessions_24h: 0,
  closed_sessions_24h: 0,
}

const ACTIVE_FULL = {
  sessions: [
    { session_id: 's1', request_count: 3, total_cost: 1.5, primary_model: 'gpt-4o' },
    { session_id: 's2', request_count: 1, total_cost: 0.2, primary_model: 'claude' },
  ],
  total_active: 2,
  page: 1,
  size: 2,
}

const ERRORS_FULL = {
  summary: { total_errors: 12, error_rate: 0.04, total_requests: 300, avg_error_latency_ms: 500 },
  distribution: [{ error_type: 'timeout', count: 8 }],
  recent_errors: [],
  top_errors: [{ error_message: 'upstream timeout', count: 8 }],
}

/** ★ 分母为 0 ⇒ error_rate 的 0 无意义（与「真的 0% 错误率」不可分）。 */
const ERRORS_NO_REQUESTS = {
  ...ERRORS_FULL,
  summary: { ...ERRORS_FULL.summary, total_errors: 0, error_rate: 0, total_requests: 0 },
}

const PERF_FULL = {
  summary: { avg_latency_ms: 300, p50_latency_ms: 200, p95_latency_ms: 900, p99_latency_ms: 1500 },
  latency_distribution: {},
  throughput: [{ date: '2026-10-08', request_count: 10 }],
  slow_queries: [{ session_key: 'k1', duration_ms: 9000 }],
}

/** ★ p50 > p95 ⇒ 契约漂移。 */
const PERF_OUT_OF_ORDER = {
  ...PERF_FULL,
  summary: { ...PERF_FULL.summary, p50_latency_ms: 2000, p95_latency_ms: 900 },
}

/** operational 正常形态（aux.go:23-74 逐字照抄）。 */
const OPERATIONAL_NORMAL = {
  background_tasks: {
    discovery: { running: true, status: 'running', trigger: 'schedule' },
    probe_loop: { checks_last_10m: 12 },
    degraded: false,
  },
  selfcheck: {
    total_runs_24h: 24,
    success_rate: 0.98,
    last_status: 'success',
    degraded: false,
  },
}

/** ★★ 「从未运行过」：ErrNoRows ⇒ degraded=true 且 status=null。 */
const OPERATIONAL_NEVER_RAN = {
  background_tasks: {
    discovery: { running: false, status: null, trigger: null },
    probe_loop: { checks_last_10m: 0 },
    degraded: true,
    degraded_reason: 'discovery status unavailable',
  },
  selfcheck: {
    total_runs_24h: 0,
    success_rate: 0,
    last_status: null,
    degraded: true,
    degraded_reason: 'self-check summary unavailable',
  },
}

/** ★ 真故障：discovery 有 status（非 null）但 degraded=true。 */
const OPERATIONAL_TRUE_DEGRADE = {
  ...OPERATIONAL_NORMAL,
  background_tasks: {
    ...OPERATIONAL_NORMAL.background_tasks,
    degraded: true,
    degraded_reason: 'discovery status unavailable',
  },
}

const DRILL_FULL = {
  error_kind: 'timeout',
  dimension: 'model',
  items: [{ key: 'gpt-4o', requests: 8, tokens: 1200, credits: 40, cost_usd: 1.25 }],
}

/** ★ 命中缓存：多了 source:"redis"（board.go:187）。 */
const DRILL_CACHED = { ...DRILL_FULL, source: 'redis' }

beforeEach(() => {
  setLocale('zh-CN')
  overviewMock.mockReset()
  trendMock.mockReset()
  healthMock.mockReset()
  activeMock.mockReset()
  moduleMock.mockReset()
  errorsMock.mockReset()
  perfMock.mockReset()
  operationalMock.mockReset()
  drillMock.mockReset()
})

afterEach(() => {
  mountedList.forEach((w) => w.unmount())
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  document.body.innerHTML = ''
})

/* ═══════════════════════════════════════════════════════════════════════
 * ① 降级：HTTP 200 + 全零 data，唯一信号是 metadata.degraded
 * ═══════════════════════════════════════════════════════════════════════ */

describe('DashboardOpsView ① 降级免责', () => {
  it('dashboardapi errors.go:319-334 降级响应不得把编造的 0 渲染成业务归零', async () => {
    overviewMock.mockResolvedValue(envelope(OVERVIEW_DEGRADED, { degraded: true, missing_view: 'request_stats_hourly' }))
    const w = await mountView()
    await clickLoad(w, 0)

    const sec = sectionOf(w, 0)
    // ★ 数字位置必须是无数据占位，不是 "0"
    const kpiValues = sec.findAll('.do__kpi-value')
    expect(kpiValues.length).toBe(4)
    kpiValues.forEach((el) => {
      expect(el.element.textContent).not.toBe('0')
      expect(el.classes()).toContain('do__nodata')
    })
    // ★ 且必须出现降级免责句
    expect(sec.text()).toContain('数据降级')
  })

  it('降级时 missing_view 要被点名，缺失_view 的降级走 unknown 文案', async () => {
    overviewMock.mockResolvedValue(envelope(OVERVIEW_DEGRADED, { degraded: true, missing_view: 'request_stats_hourly' }))
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('request_stats_hourly')
  })

  it('★ 降级但无 missing_view ⇒ 用「说不出缺哪个」而不是留白', async () => {
    overviewMock.mockResolvedValue(envelope(OVERVIEW_DEGRADED, { degraded: true }))
    const w = await mountView()
    await clickLoad(w, 0)
    const txt = sectionOf(w, 0).text()
    expect(txt).toContain('没能说出缺哪个视图')
  })

  it('★ 非降级（degraded 键缺失，omitempty）时不得出现任何免责句', async () => {
    overviewMock.mockResolvedValue(envelope(OVERVIEW_FULL))
    const w = await mountView()
    await clickLoad(w, 0)
    const sec = sectionOf(w, 0)
    expect(sec.text()).not.toContain('数据降级')
    // 且 0 只在真的为 0 时才是 0
    expect(sec.findAll('.do__nodata').length).toBe(0)
    expect(sec.findAll('.do__kpi-value')[0]!.element.textContent).toContain('1,200')
  })

  it('★ 七个信封段各自的降级都要独立生效，不是只对第一段有效', async () => {
    overviewMock.mockResolvedValue(envelope(OVERVIEW_FULL))
    trendMock.mockResolvedValue(envelope({ trend: [{ date: 'd' }], summary: {}, period_start: 'a', period_end: 'b' }, { degraded: true }))
    moduleMock.mockResolvedValue(envelope({
      modules: [],
      summary: { total_modules: 0, total_executions: 0, avg_cache_hit_rate: 0, avg_duration_ms: 0 },
      period_start: 'a', period_end: 'b',
    }, { degraded: true }))
    const w = await mountView()

    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).not.toContain('数据降级')

    await clickLoad(w, 1)
    expect(sectionOf(w, 1).text()).toContain('数据降级')

    await clickLoad(w, 4)
    expect(sectionOf(w, 4).text()).toContain('数据降级')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ② 「从未运行过」不是降级
 * ═══════════════════════════════════════════════════════════════════════ */

describe('DashboardOpsView ② 「从未运行过」的措辞', () => {
  it('aux.go:66 从未运行过 ⇒ 出现「状态未知」且**不得**说「降级」', async () => {
    operationalMock.mockResolvedValue(OPERATIONAL_NEVER_RAN)
    const w = await mountView()
    await clickLoad(w, 7)

    const sec = sectionOf(w, 7)
    const txt = sec.text()
    expect(txt).toContain('从未运行过')
    expect(txt).toContain('状态未知')
    // ★ 判别方向：真降级那段文案（operational.degradedWarn）绝不能出现
    expect(txt).not.toContain('发现/自检存在不可用项')
    // 且「状态」行必须说「状态未知」而不是「降级」
    const statusRow = sec.findAll('.do__kv-row').find((r) => r.text().includes('总体状态'))!
    expect(statusRow.text()).toContain('状态未知')
    expect(statusRow.text()).not.toContain('降级')
  })

  it('★ discovery 有 status 但 degraded ⇒ 真降级，说「降级」不说「从未运行过」', async () => {
    // ⚠️ 这条是「从未运行过」判据的判别样本：只改 degraded 一个维度，
    //    如果判据写错（只判 degraded）它会与上一条同结论。
    operationalMock.mockResolvedValue(OPERATIONAL_TRUE_DEGRADE)
    const w = await mountView()
    await clickLoad(w, 7)

    const sec = sectionOf(w, 7)
    expect(sec.text()).toContain('发现/自检存在不可用项')
    expect(sec.text()).not.toContain('从未运行过')
    const statusRow = sec.findAll('.do__kv-row').find((r) => r.text().includes('总体状态'))!
    expect(statusRow.text()).toContain('降级')
  })

  it('正常形态 ⇒ 既不说从未运行过也不说降级', async () => {
    operationalMock.mockResolvedValue(OPERATIONAL_NORMAL)
    const w = await mountView()
    await clickLoad(w, 7)
    const sec = sectionOf(w, 7)
    expect(sec.text()).not.toContain('从未运行过')
    expect(sec.text()).not.toContain('发现/自检存在不可用项')
  })

  it('★ probe_loop 没有 running 字段 ⇒ 「是否在跑」只能读 discovery', async () => {
    // aux.go:61 probe_loop 恒是单键 map。没有它就不会挂 —— 挂了必红。
    operationalMock.mockResolvedValue(OPERATIONAL_NORMAL)
    const w = await mountView()
    await clickLoad(w, 7)
    const sec = sectionOf(w, 7)
    const row = sec.findAll('.do__kv-row').find((r) => r.text().includes('发现任务是否在跑'))!
    expect(row.text()).toContain('运行中')
  })

  it('★ discovery.running=false ⇒ 说「已停止」（不能恒说运行中）', async () => {
    // ★ 这条是上一条的**判别样本**：上一条只喂 running=true，
    //   把模板改成恒「运行中」照样全绿 —— 只测一种取值等于没测这个分支。
    operationalMock.mockResolvedValue({
      ...OPERATIONAL_NORMAL,
      background_tasks: {
        ...OPERATIONAL_NORMAL.background_tasks,
        discovery: { running: false, status: 'stopped', trigger: null },
      },
    })
    const w = await mountView()
    await clickLoad(w, 7)
    const sec = sectionOf(w, 7)
    const row = sec.findAll('.do__kv-row').find((r) => r.text().includes('发现任务是否在跑'))!
    expect(row.text()).toContain('已停止')
    expect(row.text()).not.toContain('运行中')
  })

  it('★ checks_last_10m 查询失败（probe_degraded）⇒ 渲染无数据而不是 0', async () => {
    // Go 侧 var checksLast10m int，Scan 失败留 0（aux.go:53-58 只 slog）。
    // ⇒ 0 与「真的 0 次」在值上不可分，唯一信号是 probe_degraded。
    operationalMock.mockResolvedValue({
      ...OPERATIONAL_NEVER_RAN,
      background_tasks: {
        ...OPERATIONAL_NEVER_RAN.background_tasks,
        degraded: false,
        degraded_reason: undefined,
        probe_degraded: true,
      },
    })
    const w = await mountView()
    await clickLoad(w, 7)
    const sec = sectionOf(w, 7)
    const row = sec.findAll('.do__kv-row').find((r) => r.text().includes('10 分钟检查数'))!
    expect(row.find('dd').element.textContent).toBe('—')
    expect(row.find('dd').classes()).toContain('do__nodata')
  })

  it('★ 24 小时自检 0 次 ⇒ 显示「无自检记录」而不是 0（比值无意义）', async () => {
    operationalMock.mockResolvedValue(OPERATIONAL_NEVER_RAN)
    const w = await mountView()
    await clickLoad(w, 7)
    const sec = sectionOf(w, 7)
    const row = sec.findAll('.do__kv-row').find((r) => r.text().includes('24 小时自检次数'))!
    expect(row.find('dd').element.textContent).toBe('无自检记录（比值无意义）')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ③ tenant_id 筛选框的可见性
 * ═══════════════════════════════════════════════════════════════════════ */

describe('DashboardOpsView ③ tenant 筛选按角色', () => {
  it('super_admin ⇒ 给筛选框', async () => {
    const w = await mountView('super_admin')
    const labels = w.findAll('.do__field-label').map((l) => l.text())
    expect(labels).toContain('租户筛选')
  })

  it('admin_key ⇒ 也给筛选框（auth.go:39-45 同时放行）', async () => {
    const w = await mountView('admin_key')
    const labels = w.findAll('.do__field-label').map((l) => l.text())
    expect(labels).toContain('租户筛选')
  })

  it('★ tenant_admin ⇒ 不给筛选框，并说明原因', async () => {
    const w = await mountView('tenant_admin')
    const labels = w.findAll('.do__field-label').map((l) => l.text())
    expect(labels).not.toContain('租户筛选')
    expect(w.text()).toContain('只能看本租户数据')
  })

  it('★ tenant_admin 不得把 tenantId 发出去（输入框隐藏时天然为空）', async () => {
    overviewMock.mockResolvedValue(envelope(OVERVIEW_FULL))
    const w = await mountView('tenant_admin')
    await clickLoad(w, 0)
    expect(overviewMock).toHaveBeenCalled()
    const q = overviewMock.mock.calls[0]![0] as Record<string, unknown>
    expect(q.tenantId).toBeUndefined()
  })

  it('★★ tenant_id 残留时仍不得发出去（角色从 super_admin 降为 tenant_admin）', async () => {
    // ★★★ 这条才是 #9 那条变异的**判别样本**：
    //   上一条里 tenant_admin 看不到输入框 ⇒ tenantId 恒为 '' ⇒
    //   把 `tenantFilterVisible.value &&` 整个去掉也照样全绿。
    //   真实可达路径：先以 super_admin 登录填了租户号，token 过期后
    //   服务端把角色降级，页面上的 ref 仍留着旧值 —— 这时若还照发，
    //   后端会静默改写（auth.go:39-45），用户看到的是**别的租户**的数据
    //   却以为自己在筛选。
    overviewMock.mockResolvedValue(envelope(OVERVIEW_FULL))
    const w = await mountView('super_admin')
    const tenantInput = w.findAll('.do__input')[1]!
    await tenantInput.setValue('acme')
    await flushPromises()

    // 角色降级（auth store 里的 role 变了）
    const auth = useAuthStore()
    auth.userInfo = { username: 'u', role: 'tenant_admin' } as never
    await flushPromises()

    // ★ 筛选框应当随之消失
    expect(w.findAll('.do__field-label').map((l) => l.text())).not.toContain('租户筛选')

    await clickLoad(w, 0)
    const q = overviewMock.mock.calls[0]![0] as Record<string, unknown>
    expect(q.tenantId).toBeUndefined()
  })

  it('super_admin 填了 tenant_id ⇒ 真发出去', async () => {
    overviewMock.mockResolvedValue(envelope(OVERVIEW_FULL))
    const w = await mountView('super_admin')
    const tenantInput = w.findAll('.do__input')[1]!
    await tenantInput.setValue('acme')
    await clickLoad(w, 0)
    const q = overviewMock.mock.calls[0]![0] as Record<string, unknown>
    expect(q.tenantId).toBe('acme')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ④⑤ drill：source 条件键 + days 口径
 * ═══════════════════════════════════════════════════════════════════════ */

describe('DashboardOpsView ④⑤ drill 的条件键与 days 口径', () => {
  async function loadDrill(w: W): Promise<void> {
    const kindInput = w.findAll('.do__input')[2]!
    await kindInput.setValue('timeout')
    // drill 段的按钮在第 8 个 .do__load（drill 段内）
    await clickLoad(w, 8)
  }

  it('★ 未填 error_kind 时不发请求（后端 400）', async () => {
    const w = await mountView()
    const drillSec = sectionOf(w, 8)
    expect(drillSec.text()).toContain('错误类型是必填项')
    expect(drillSec.find('.do__load').attributes('disabled')).toBeDefined()
    expect(drillMock).not.toHaveBeenCalled()
  })

  it('board.go:198-202 现算路径无 source 键 ⇒ 说「现算」而不是「来源未知」', async () => {
    drillMock.mockResolvedValue(DRILL_FULL)
    const w = await mountView()
    await loadDrill(w)
    const sec = sectionOf(w, 8)
    // ★ 否定断言必须对准**真正要排除的措辞**，不能只判「未知」两个字 ——
    //   本段自己的免责句里就有「不是『来源未知』」，一判就误伤。
    //   要排除的是「把键缺失说成来源不明」这种措辞。
    expect(sec.text()).toContain('现算')
    expect(sec.text()).toContain('不是「来源未知」')
    expect(sec.text()).not.toContain('来自看板缓存')
  })

  it('board.go:187 命中缓存 ⇒ 说来自缓存', async () => {
    drillMock.mockResolvedValue(DRILL_CACHED)
    const w = await mountView()
    await loadDrill(w)
    expect(sectionOf(w, 8).text()).toContain('看板缓存')
  })

  it('★ days 越界：信封族回显回落 7（静默改写）', async () => {
    const w = await mountView()
    const daysInput = w.findAll('.do__input')[0]!
    await daysInput.setValue('9999')
    await flushPromises()
    expect(w.text()).toContain('本段后端实际生效：7 天')
  })

  it('★ drill 的 days 是**独立**输入框，越界回显钳位到 90（不是回落）', async () => {
    // ⚠️ drillDays 与顶部 days 是两个独立 ref：board/error-drill 走 clamp 口径
    //   （board.go:204-213），信封族走静默回落（types.go:119-148）。
    //   两者**不可**联动，也不可共用一个回显。
    const w = await mountView()
    // drill 段内三个输入：kind / dimension / days（第 3 个）
    const drillDaysInput = sectionOf(w, 8).findAll('.do__input')[2]!
    await drillDaysInput.setValue('9999')
    await flushPromises()
    expect(sectionOf(w, 8).text()).toContain('下钻实际生效：90 天')
    // 且顶部那个不受影响
    expect(w.text()).toContain('本段后端实际生效：7 天')
  })

  it('★ drill 越界时回显与实际发值同源（都按 clamp 理解）', async () => {
    drillMock.mockResolvedValue(DRILL_FULL)
    const w = await mountView()
    const drillDaysInput = sectionOf(w, 8).findAll('.do__input')[2]!
    await drillDaysInput.setValue('9999')
    await loadDrill(w)
    const q = drillMock.mock.calls[0]![0] as Record<string, unknown>
    // ★ 发出去的是用户填的原值（前端不改写），由后端 clamp；
    //   回显告诉运维「后端实际会当成 90」—— 两者不矛盾，是两件事。
    expect(q.days).toBe(9999)
    expect(sectionOf(w, 8).text()).toContain('下钻实际生效：90 天')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ⑥ session-active：分页双份 + 失败路径
 * ═══════════════════════════════════════════════════════════════════════ */

describe('DashboardOpsView ⑥ 活跃会话分页', () => {
  it('正常分页 ⇒ 不报警，翻页按钮按可达性启用', async () => {
    activeMock.mockResolvedValue(envelope(ACTIVE_FULL, { page: 1, size: 2 }))
    const w = await mountView()
    await clickLoad(w, 3)
    const sec = sectionOf(w, 3)
    expect(sec.text()).not.toContain('契约已漂移')
    // 第一页：上一页禁用，下一页可用（取满 2 条）
    const btns = sec.findAll('.do__pager-btn')
    expect(btns[0]!.attributes('disabled')).toBeDefined()
    expect(btns[1]!.attributes('disabled')).toBeUndefined()
  })

  it('★ metadata.page 与 data.page 不一致 ⇒ 报警（两者本应同源）', async () => {
    activeMock.mockResolvedValue(envelope(ACTIVE_FULL, { page: 7, size: 2 }))
    const w = await mountView()
    await clickLoad(w, 3)
    expect(sectionOf(w, 3).text()).toContain('分页自相矛盾')
  })

  it('★ 翻页会把新的 page 真的发出去', async () => {
    activeMock.mockResolvedValue(envelope(ACTIVE_FULL, { page: 1, size: 2 }))
    const w = await mountView()
    await clickLoad(w, 3)
    const sec = sectionOf(w, 3)
    await sec.findAll('.do__pager-btn')[1]!.trigger('click')
    await flushPromises()
    await flushPromises()
    const q = activeMock.mock.calls[1]![0] as Record<string, unknown>
    expect(q.page).toBe(2)
  })

  it('★ 失败 ⇒ 清掉旧数据，且不得渲染任何空态文案', async () => {
    // ★ 必须「先成功、再失败」：只测首次失败时「失败后是否清空」是恒真的。
    activeMock.mockResolvedValueOnce(envelope(ACTIVE_FULL, { page: 1, size: 2 }))
    const w = await mountView()
    await clickLoad(w, 3)
    expect(sectionOf(w, 3).findAll('.do__item').length).toBe(2)

    activeMock.mockRejectedValueOnce(Object.assign(new Error('boom'), { status: 500 }))
    await clickLoad(w, 3)

    const sec = sectionOf(w, 3)
    expect(sec.text()).toContain('boom')
    expect(sec.findAll('.do__item').length).toBe(0)
    // ★ 错误不等于零行
    expect(sec.text()).not.toContain('没有数据')
  })

  it('★ 加载成功后按钮文案切到「重新加载」', async () => {
    activeMock.mockResolvedValue(envelope(ACTIVE_FULL, { page: 1, size: 2 }))
    const w = await mountView()
    expect(loadBtn(w, 3).text()).toBe('加载')
    await clickLoad(w, 3)
    expect(loadBtn(w, 3).text()).toBe('重新加载')
  })

  it('★ 行内缺键 ⇒ 无数据占位带 class，不显示 0', async () => {
    activeMock.mockResolvedValue(envelope({
      ...ACTIVE_FULL,
      sessions: [{ session_id: 's1' }],
    }))
    const w = await mountView()
    await clickLoad(w, 3)
    const sec = sectionOf(w, 3)
    const nodata = sec.findAll('.do__kv-row').filter((r) => r.find('dd').classes().includes('do__nodata'))
    // request_count / total_cost / primary_model 三个键都缺
    expect(nodata.length).toBe(3)
    nodata.forEach((r) => {
      expect(r.find('dd').element.textContent).toBe('—')
    })
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * errors / performance 的判据
 * ═══════════════════════════════════════════════════════════════════════ */

describe('DashboardOpsView errors 与 performance', () => {
  it('★ 分母为 0 ⇒ 错误率显示「无请求」而不是 0%', async () => {
    errorsMock.mockResolvedValue(envelope(ERRORS_NO_REQUESTS))
    const w = await mountView()
    await clickLoad(w, 5)
    const sec = sectionOf(w, 5)
    expect(sec.text()).toContain('无请求')
    expect(sec.text()).not.toContain('0.00%')
  })

  it('分母非 0 ⇒ 显示真实错误率', async () => {
    errorsMock.mockResolvedValue(envelope(ERRORS_FULL))
    const w = await mountView()
    await clickLoad(w, 5)
    expect(sectionOf(w, 5).text()).toContain('4.00%')
  })

  it('★ P50 ≤ P95 ≤ P99 被破坏 ⇒ 报警', async () => {
    perfMock.mockResolvedValue(envelope(PERF_OUT_OF_ORDER))
    const w = await mountView()
    await clickLoad(w, 6)
    expect(sectionOf(w, 6).text()).toContain('分位数乱序')
  })

  it('分位数正常 ⇒ 不报警', async () => {
    perfMock.mockResolvedValue(envelope(PERF_FULL))
    const w = await mountView()
    await clickLoad(w, 6)
    expect(sectionOf(w, 6).text()).not.toContain('分位数乱序')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * health / trend / modules 三段的空态
 * ═══════════════════════════════════════════════════════════════════════ */

describe('DashboardOpsView 其余段', () => {
  it('health 空分布 ⇒ 渲染「没有数据」', async () => {
    healthMock.mockResolvedValue(envelope({ distribution: {}, trend: [], top_issues: [] }))
    const w = await mountView()
    await clickLoad(w, 2)
    expect(sectionOf(w, 2).text()).toContain('没有数据')
  })

  it('health 有分布 ⇒ 渲染成 chip', async () => {
    healthMock.mockResolvedValue(envelope({ distribution: { a: 10, b: 2 }, trend: [], top_issues: [] }))
    const w = await mountView()
    await clickLoad(w, 2)
    const sec = sectionOf(w, 2)
    expect(sec.findAll('.do__chip').length).toBe(2)
    expect(sec.findAll('.do__chip')[0]!.text()).toContain('a · 10')
  })

  it('trend 报点数，不编字段', async () => {
    trendMock.mockResolvedValue(envelope({
      trend: [{ date: 'd1' }, { date: 'd2' }, { date: 'd3' }],
      summary: {}, period_start: 'a', period_end: 'b',
    }))
    const w = await mountView()
    await clickLoad(w, 1)
    const sec = sectionOf(w, 1)
    expect(sec.text()).toContain('趋势点')
    expect(countOccurrences(sec.findAll('.do__kv-row')[0]!.element.textContent ?? '', '3')).toBeGreaterThan(0)
  })

  it('modules 空列表 ⇒ 渲染「没有数据」', async () => {
    moduleMock.mockResolvedValue(envelope({
      modules: [],
      summary: { total_modules: 0, total_executions: 0, avg_cache_hit_rate: 0, avg_duration_ms: 0 },
      period_start: 'a', period_end: 'b',
    }))
    const w = await mountView()
    await clickLoad(w, 4)
    expect(sectionOf(w, 4).text()).toContain('没有数据')
  })

  it('★ 403 ⇒ 显示无权文案而不是裸错误串', async () => {
    overviewMock.mockRejectedValue(Object.assign(new Error('Forbidden'), { status: 403 }))
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('无权查看看板读面')
  })

  it('★ 九段互不串扰：只加载一段时其余段不渲染数字', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    // 只加载了 overview ⇒ errors 段不该有任何 KPI
    expect(sectionOf(w, 5).findAll('.do__kpi').length).toBe(0)
    expect(sectionOf(w, 6).findAll('.do__kpi').length).toBe(0)
  })
})
