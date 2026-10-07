<script setup lang="ts">
// DashboardOpsView — 看板读面（/dashboard-ops，**admin 档**）。
//
// 数据源（九条，全部 admin 档，见 admin/handler.go:1052-1069）：
//   GET /api/admin/dashboard/session-overview   信封族（api/dashboard.ts）
//   GET /api/admin/dashboard/session-trend     信封族
//   GET /api/admin/dashboard/session-health    信封族
//   GET /api/admin/dashboard/session-active    信封族（带分页）
//   GET /api/admin/dashboard/module-stats      信封族
//   GET /api/admin/dashboard/errors            信封族
//   GET /api/admin/dashboard/performance       信封族
//   GET /api/admin/dashboard/operational       裸 JSON（api/dashboardBoard.ts）
//   GET /api/admin/dashboard/board/error-drill 裸 JSON（api/dashboardBoard.ts）
//
// 与既有页面的关系：/session-analytics 走的是 `/api/admin/session-analytics/*`
// （另一族前缀），本页走 `/api/admin/dashboard/*` —— 两条线的字段与降级语义
// **完全不同**，不能互相顶替。HomeView 已经消费 `/dashboard/board`（饼图那套），
// 本页不重复它。
//
// ## ⚠️ 权限档位
//
// handler.go:1052-1069 九条注册**全部**是 `admin(...)` ⇒ tenant_admin 可用。
// ⇒ 抽屉席**不设** `requiresRole`。
//
// ★ 这与 `/auto-route` 相反（那条线整族是 `h.superAdmin`）：
//   抽屉席把 requiresRole 设成 super_admin 会让 tenant_admin 在抽屉里**看不到本页**，
//   而端点本身是放行的 —— 那是一道比 403 更坏的墙（用户连「为什么进不去」都看不到）。
//
// ## ★★★★★ 六处「不能都渲染成同一个东西」
//
// (1) ★★★★★ **降级返回 HTTP 200 + success:true + data 全零**
//      （`writeDegraded`，errors.go:319-334）。唯一区分是 `metadata.degraded`。
//      ⇒ 每个数字块都必须挂降级免责，否则一次查询故障会被读成「业务归零」。
//      ⚠️ `degraded` 带 omitempty ⇒ **正常时是键缺失而非 false**，
//      用 `'degraded' in metadata` 判会全判成正常，`=== true` 才是对的。
//
// (2) ★★★★ **「从未运行过」被后端算成 `degraded`**
//      （`queryBoardBackgroundTasks`，aux.go:66 拿原始 err 算，而
//      `ORDER BY started_at DESC LIMIT 1` 无行时返 `pgx.ErrNoRows`）。
//      ⇒ 一套没跑过 discovery 的新网关会一直挂红。措辞只能说「状态未知」，
//      **不能**说「降级」—— 见 `operationalDiscoveryNeverRan`。
//
// (3) ★★★★ **`tenant_id` 筛选框只给 super_admin / admin_key**
//      （`normalizeDashboardScope`，auth.go:39-45 会把 tenant_admin 填的值
//      **静默改写**成他自己的租户，响应里没有任何标记）。
//      ⇒ 给 tenant_admin 一个填了会被忽略的筛选框，比不给更坏。
//
// (4) ★★★ **drill 的 `source` 是条件键**：命中缓存才有 `="redis"`，
//      现算路径整个键不存在（board.go:187 vs :198-202）。
//      ⇒ 键缺失 = 现算，**不是**「来源未知」。
//
// (5) ★★★ **`days` 在本仓有四种口径**：dashboardapi 越界静默回落 7、
//      tuning-accuracy 越界 400、board clamp 到 [1,90]、audit clamp 到 500。
//      ⇒ 顶部那个窗口选择器对信封族是「静默改写」，对 drill 是「钳位」。
//      两段各自显示后端**实际生效**的天数，不能共用一个回显。
//
// (6) ★★ **session-active 的分页在 data 与 metadata 里各有一份**
//      （两者本应同源）⇒ 不一致即契约漂移，要单独报警而不是静默取一个。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { fmtInt, fmtNum, fmtUsd } from '@/utils/format'
import { useAuthStore } from '@/stores/auth'
import {
  fetchSessionOverview,
  fetchSessionTrend,
  fetchSessionHealth,
  fetchSessionActive,
  fetchModuleStats,
  fetchDashboardErrors,
  fetchDashboardPerformance,
  dashboardIsDegraded,
  dashboardMissingView,
  dashboardHint,
  dashboardDegradedWithoutMissingView,
  dashboardErrorRatePercent,
  dashboardErrorRateMeaningless,
  dashboardLatencyQuantilesOutOfOrder,
  dashboardPaginationDisagrees,
  dashboardHasNextPage,
  dashboardDaysEffective,
  dashboardTenantFilterHonored,
  type DashboardEnvelope,
  type SessionOverviewData,
  type SessionTrendData,
  type SessionHealthData,
  type SessionActiveData,
  type ModuleStatsData,
  type ErrorStatsData,
  type PerformanceData,
} from '@/api/dashboard'
import {
  fetchDashboardOperational,
  fetchErrorDrill,
  operationalDiscoveryNeverRan,
  operationalSelfCheckMeaningless,
  operationalAnyDegraded,
  errorDrillServedFromCache,
  errorDrillDaysEffective,
  errorDrillEmpty,
  type OperationalResponse,
  type ErrorDrillResponse,
} from '@/api/dashboardBoard'

useHyperPage({ title: () => t('dashboardOps.title') })

const auth = useAuthStore()

/** 无数据占位。字形与 0 不同，且带独立 class（判据锚在原因上）。 */
const NO_DATA = '—'

type SectionKey =
  | 'overview' | 'trend' | 'health' | 'active'
  | 'moduleStats' | 'errors' | 'performance' | 'operational' | 'drill'

const overview = ref<DashboardEnvelope<SessionOverviewData> | null>(null)
const trend = ref<DashboardEnvelope<SessionTrendData> | null>(null)
const health = ref<DashboardEnvelope<SessionHealthData> | null>(null)
const active = ref<DashboardEnvelope<SessionActiveData> | null>(null)
const moduleStats = ref<DashboardEnvelope<ModuleStatsData> | null>(null)
const errors = ref<DashboardEnvelope<ErrorStatsData> | null>(null)
const performance = ref<DashboardEnvelope<PerformanceData> | null>(null)
const operational = ref<OperationalResponse | null>(null)
const drill = ref<ErrorDrillResponse | null>(null)

const loading = ref<SectionKey | null>(null)
const loaded = ref<Record<SectionKey, boolean>>({
  overview: false, trend: false, health: false, active: false,
  moduleStats: false, errors: false, performance: false, operational: false, drill: false,
})
const error = ref<Record<SectionKey, string | null>>({
  overview: null, trend: null, health: null, active: null,
  moduleStats: null, errors: null, performance: null, operational: null, drill: null,
})

/** 顶部窗口选择器。★ 对信封族是「静默回落 7」，对 drill 是「clamp [1,90]」。 */
const days = ref(7)
/** ★ 只有 super_admin / admin_key 才真拿到跨租户筛选能力。 */
const tenantFilterVisible = computed(() => dashboardTenantFilterHonored(auth.role))
const tenantId = ref('')

/** drill 的 error_kind 是**必填**，缺了后端 400 ⇒ 前端不填就别发。 */
const errorKind = ref('')
/** 选一个 kind 才允许加载 drill —— 这是唯一一条有前置输入的段。 */
const drillReady = computed(() => errorKind.value.trim().length > 0)

const drillDimension = ref('model')
const drillDays = ref(1)

const activePage = ref(1)

function describeError(err: unknown): string {
  const statusCode = (err as { status?: number })?.status
  if (statusCode === 403) return t('dashboardOps.errForbidden')
  return (err instanceof Error ? err.message : String(err)) || t('common.error')
}

/** ★ 公共查询参数：tenant_id 只在真被后端认的时候才发。 */
function baseQuery(): { days: number; tenantId?: string } {
  const q: { days: number; tenantId?: string } = { days: days.value }
  if (tenantFilterVisible.value && tenantId.value.trim()) q.tenantId = tenantId.value.trim()
  return q
}

async function load(section: SectionKey): Promise<void> {
  loading.value = section
  error.value[section] = null
  try {
    const q = baseQuery()
    if (section === 'overview') {
      overview.value = await fetchSessionOverview(q)
    } else if (section === 'trend') {
      trend.value = await fetchSessionTrend(q)
    } else if (section === 'health') {
      health.value = await fetchSessionHealth(q)
    } else if (section === 'active') {
      active.value = await fetchSessionActive({ ...q, page: activePage.value })
    } else if (section === 'moduleStats') {
      moduleStats.value = await fetchModuleStats(q)
    } else if (section === 'errors') {
      errors.value = await fetchDashboardErrors(q)
    } else if (section === 'performance') {
      performance.value = await fetchDashboardPerformance(q)
    } else if (section === 'operational') {
      operational.value = await fetchDashboardOperational()
    } else if (section === 'drill') {
      drill.value = await fetchErrorDrill({
        errorKind: errorKind.value.trim(),
        dimension: drillDimension.value,
        days: drillDays.value,
        ...(tenantFilterVisible.value && tenantId.value.trim()
          ? { tenantId: tenantId.value.trim() }
          : {}),
      })
    }
  } catch (err) {
    // ★ 失败 ⇒ 清空该段数据，绝不保留上一次的成功结果冒充本次。
    if (section === 'overview') overview.value = null
    if (section === 'trend') trend.value = null
    if (section === 'health') health.value = null
    if (section === 'active') active.value = null
    if (section === 'moduleStats') moduleStats.value = null
    if (section === 'errors') errors.value = null
    if (section === 'performance') performance.value = null
    if (section === 'operational') operational.value = null
    if (section === 'drill') drill.value = null
    error.value[section] = describeError(err)
  } finally {
    loading.value = null
    loaded.value[section] = true
  }
}

/* ── 降级免责（(1)） ────────────────────────────────────────────────── */

/**
 * ★ 降级时必须显示的那句话。
 * 三种情形要分开说：
 *   - 降级 + 有 missing_view ⇒ 知道是哪个视图缺
 *   - 降级 + 无 missing_view ⇒ 后端自己也说不出缺哪个（降级兜底路径）
 *   - 非降级 ⇒ null，不渲染
 */
function degradedText(env: DashboardEnvelope<unknown> | null): string | null {
  if (!env || !dashboardIsDegraded(env)) return null
  const view = dashboardMissingView(env)
  if (view) return t('dashboardOps.degraded.missingView', { view })
  return t('dashboardOps.degraded.unknown')
}

/** ★ 后端连降级原因都没说（writeDegraded 的兜底路径）—— 值得单独说一次。 */
function degradedBlind(env: DashboardEnvelope<unknown> | null): boolean {
  return env != null && dashboardDegradedWithoutMissingView(env)
}

/** ★ 降级时 data 的数字全是编造的 0，UI 必须显示「无数据」而不是 0。 */
function degradedNoData(env: DashboardEnvelope<unknown> | null): boolean {
  return env != null && dashboardIsDegraded(env)
}

/* ── overview ──────────────────────────────────────────────────────── */

const overviewGenerated = computed(() => overview.value?.data?.generated_at ?? NO_DATA)
const overviewPeriod = computed(() => {
  const d = overview.value?.data
  if (!d) return NO_DATA
  return `${d.period_start} → ${d.period_end}`
})

/* ── trend ─────────────────────────────────────────────────────────── */

/** ★ trend 数组的元素形状本仓没钉（RawTrendPoint 全可选）⇒ 只报条数不编字段。 */
const trendCount = computed(() => trend.value?.data?.trend.length ?? 0)

/* ── health ────────────────────────────────────────────────────────── */

/** distribution 的键（a/b/c/d/f 与百分比）在 API 层刻意没写死，这里只透传。 */
function distributionEntries(env: DashboardEnvelope<SessionHealthData> | null): Array<[string, unknown]> {
  const d = env?.data?.distribution
  if (!d || typeof d !== 'object') return []
  return Object.entries(d)
}

/* ── active ────────────────────────────────────────────────────────── */

const activePaginationDisagrees = computed(() =>
  active.value ? dashboardPaginationDisagrees(active.value) : false,
)
const activeHasNext = computed(() => (active.value ? dashboardHasNextPage(active.value) : false))
const activeRows = computed(() => active.value?.data?.sessions ?? [])

function activeCell(row: unknown, key: string): { text: string; nodata: boolean } {
  if (!row || typeof row !== 'object') return { text: NO_DATA, nodata: true }
  const v = (row as Record<string, unknown>)[key]
  if (v === undefined || v === null) return { text: NO_DATA, nodata: true }
  return { text: typeof v === 'number' ? fmtNum(v, 2) : String(v), nodata: false }
}

function gotoPage(delta: number): void {
  activePage.value = Math.max(1, activePage.value + delta)
  void load('active')
}

/* ── module-stats ──────────────────────────────────────────────────── */

const moduleRows = computed(() => moduleStats.value?.data?.modules ?? [])

function moduleCell(row: unknown, key: string): { text: string; nodata: boolean } {
  if (!row || typeof row !== 'object') return { text: NO_DATA, nodata: true }
  const v = (row as Record<string, unknown>)[key]
  if (v === undefined || v === null) return { text: NO_DATA, nodata: true }
  return { text: typeof v === 'number' ? fmtNum(v, 2) : String(v), nodata: false }
}

/* ── errors ────────────────────────────────────────────────────────── */

/** ★ 分母为 0 时 error_rate 无意义（后端已给 0），不能渲染成「0% 错误率」。 */
const errorRateMeaningless = computed(() =>
  errors.value?.data ? dashboardErrorRateMeaningless(errors.value.data) : false,
)
const errorRateText = computed(() => {
  if (!errors.value?.data) return NO_DATA
  if (errorRateMeaningless.value) return t('dashboardOps.errors.noRequests')
  return fmtNum(dashboardErrorRatePercent(errors.value.data), 2) + '%'
})
const errorRows = computed(() => errors.value?.data?.top_errors ?? [])
const errorDistRows = computed(() => errors.value?.data?.distribution ?? [])

function errorCell(row: unknown, key: string): { text: string; nodata: boolean } {
  if (!row || typeof row !== 'object') return { text: NO_DATA, nodata: true }
  const v = (row as Record<string, unknown>)[key]
  if (v === undefined || v === null) return { text: NO_DATA, nodata: true }
  return { text: typeof v === 'number' ? fmtNum(v, 2) : String(v), nodata: false }
}

/* ── performance ───────────────────────────────────────────────────── */

/** ★ p50/p95/p99 乱序即契约漂移，必须报警而不是照抄渲染。 */
const quantilesOutOfOrder = computed(() =>
  performance.value?.data ? dashboardLatencyQuantilesOutOfOrder(performance.value.data) : false,
)
const perfThroughput = computed(() => performance.value?.data?.throughput ?? [])
const perfSlow = computed(() => performance.value?.data?.slow_queries ?? [])

function perfCell(row: unknown, key: string): { text: string; nodata: boolean } {
  if (!row || typeof row !== 'object') return { text: NO_DATA, nodata: true }
  const v = (row as Record<string, unknown>)[key]
  if (v === undefined || v === null) return { text: NO_DATA, nodata: true }
  return { text: typeof v === 'number' ? fmtNum(v, 2) : String(v), nodata: false }
}

/* ── operational (2) ───────────────────────────────────────────────── */

const discoveryNeverRan = computed(() =>
  operational.value ? operationalDiscoveryNeverRan(operational.value.background_tasks) : false,
)
const selfCheckMeaningless = computed(() =>
  operational.value ? operationalSelfCheckMeaningless(operational.value.selfcheck) : false,
)
const operationalDegraded = computed(() =>
  operational.value ? operationalAnyDegraded(operational.value) : false,
)

/**
 * ★★★ 措辞判据：`discoveryNeverRan` 时**不能**说「降级」。
 * 后端把 ErrNoRows（一张都没跑过）也算进 degraded，说「降级」等于
 * 告诉运维「你这里有故障」—— 而它其实什么都没跑。
 */
const operationalStatusText = computed(() => {
  const o = operational.value
  if (!o) return NO_DATA
  if (discoveryNeverRan.value) return t('dashboardOps.operational.statusWordUnknown')
  if (operationalDegraded.value) return t('dashboardOps.operational.statusWordDegraded')
  return t('dashboardOps.operational.statusWordNormal')
})

/* ── drill (4)(5) ──────────────────────────────────────────────────── */

const drillFromCache = computed(() => (drill.value ? errorDrillServedFromCache(drill.value) : false))
const drillRows = computed(() => drill.value?.items ?? [])
const drillIsEmpty = computed(() => (drill.value ? errorDrillEmpty(drill.value) : false))

/** ★ drill 的 days 是 clamp [1,90]、默认 1 —— 回显**实际生效值**，不是用户填的值。 */
const drillDaysEcho = computed(() => errorDrillDaysEffective(drillDays.value))

onBeforeUnmount(() => {
  overview.value = null
  trend.value = null
  health.value = null
  active.value = null
  moduleStats.value = null
  errors.value = null
  performance.value = null
  operational.value = null
  drill.value = null
})
</script>

<template>
  <div class="view-root do">
    <!-- ══ 公共筛选栏 ═══════════════════════════════════════════════ -->
    <section class="do__filters">
      <label class="do__field">
        <span class="do__field-label">{{ t('dashboardOps.days') }}</span>
        <input
          v-model.number="days"
          type="number"
          class="do__input"
          min="1"
          max="90"
        />
      </label>
      <!-- ★ 只有 super_admin / admin_key 才给这个框；tenant_admin 填了会被静默改写 -->
      <label v-if="tenantFilterVisible" class="do__field">
        <span class="do__field-label">{{ t('dashboardOps.tenant') }}</span>
        <input v-model="tenantId" type="text" class="do__input" />
      </label>
      <p v-else class="do__msg">
        {{ t('dashboardOps.tenantHidden') }}
      </p>
      <!-- ★ 对信封族是「静默回落 7」，回显实际生效值而不是用户填的值 -->
      <p class="do__echo">{{ t('dashboardOps.daysEcho', { days: dashboardDaysEffective(days) }) }}</p>
    </section>

    <!-- ══ 1. 会话总览 ═════════════════════════════════════════════ -->
    <section class="do__section">
      <header class="do__head">
        <h2 class="do__title">{{ t('dashboardOps.overview.title') }}</h2>
        <button
          type="button"
          class="do__load"
          :disabled="loading === 'overview'"
          @click="load('overview')"
        >
          {{ loaded.overview && !error.overview ? t('dashboardOps.reload') : t('dashboardOps.load') }}
        </button>
      </header>

      <p v-if="error.overview" class="do__msg do__msg--err">{{ error.overview }}</p>
      <p v-if="loading === 'overview'" class="do__msg">{{ t('common.loading') }}</p>

      <template v-if="overview">
        <p v-if="degradedText(overview)" class="do__msg do__msg--warn">{{ degradedText(overview) }}</p>
        <p v-if="degradedBlind(overview)" class="do__msg do__msg--warn">{{ t('dashboardOps.degraded.blind') }}</p>
        <p v-if="dashboardHint(overview)" class="do__echo">{{ dashboardHint(overview) }}</p>

        <div class="do__kpis">
          <div class="do__kpi">
            <span class="do__kpi-label">{{ t('dashboardOps.overview.total') }}</span>
            <span class="do__kpi-value" :class="{ 'do__nodata': degradedNoData(overview) }">
              {{ degradedNoData(overview) ? NO_DATA : fmtInt(overview.data?.total_sessions ?? 0) }}
            </span>
          </div>
          <div class="do__kpi">
            <span class="do__kpi-label">{{ t('dashboardOps.overview.active') }}</span>
            <span class="do__kpi-value" :class="{ 'do__nodata': degradedNoData(overview) }">
              {{ degradedNoData(overview) ? NO_DATA : fmtInt(overview.data?.active_sessions ?? 0) }}
            </span>
          </div>
          <div class="do__kpi">
            <span class="do__kpi-label">{{ t('dashboardOps.overview.new24h') }}</span>
            <span class="do__kpi-value" :class="{ 'do__nodata': degradedNoData(overview) }">
              {{ degradedNoData(overview) ? NO_DATA : fmtInt(overview.data?.new_sessions_24h ?? 0) }}
            </span>
          </div>
          <div class="do__kpi">
            <span class="do__kpi-label">{{ t('dashboardOps.overview.closed24h') }}</span>
            <span class="do__kpi-value" :class="{ 'do__nodata': degradedNoData(overview) }">
              {{ degradedNoData(overview) ? NO_DATA : fmtInt(overview.data?.closed_sessions_24h ?? 0) }}
            </span>
          </div>
        </div>

        <dl class="do__kv">
          <div class="do__kv-row">
            <dt>{{ t('dashboardOps.overview.generatedAt') }}</dt>
            <dd>{{ overviewGenerated }}</dd>
          </div>
          <div class="do__kv-row">
            <dt>{{ t('dashboardOps.overview.period') }}</dt>
            <dd>{{ overviewPeriod }}</dd>
          </div>
        </dl>
      </template>
    </section>

    <!-- ══ 2. 会话趋势 ═════════════════════════════════════════════ -->
    <section class="do__section">
      <header class="do__head">
        <h2 class="do__title">{{ t('dashboardOps.trend.title') }}</h2>
        <button
          type="button"
          class="do__load"
          :disabled="loading === 'trend'"
          @click="load('trend')"
        >
          {{ loaded.trend && !error.trend ? t('dashboardOps.reload') : t('dashboardOps.load') }}
        </button>
      </header>

      <p v-if="error.trend" class="do__msg do__msg--err">{{ error.trend }}</p>
      <p v-if="loading === 'trend'" class="do__msg">{{ t('common.loading') }}</p>

      <template v-if="trend">
        <p v-if="degradedText(trend)" class="do__msg do__msg--warn">{{ degradedText(trend) }}</p>
        <p v-if="degradedBlind(trend)" class="do__msg do__msg--warn">{{ t('dashboardOps.degraded.blind') }}</p>
        <dl class="do__kv">
          <div class="do__kv-row">
            <dt>{{ t('dashboardOps.trend.points') }}</dt>
            <dd :class="{ 'do__nodata': degradedNoData(trend) }">
              {{ degradedNoData(trend) ? NO_DATA : fmtInt(trendCount) }}
            </dd>
          </div>
          <div class="do__kv-row">
            <dt>{{ t('dashboardOps.trend.period') }}</dt>
            <dd>{{ trend.data?.period_start }} → {{ trend.data?.period_end }}</dd>
          </div>
        </dl>
      </template>
    </section>

    <!-- ══ 3. 会话健康度 ═══════════════════════════════════════════ -->
    <section class="do__section">
      <header class="do__head">
        <h2 class="do__title">{{ t('dashboardOps.health.title') }}</h2>
        <button
          type="button"
          class="do__load"
          :disabled="loading === 'health'"
          @click="load('health')"
        >
          {{ loaded.health && !error.health ? t('dashboardOps.reload') : t('dashboardOps.load') }}
        </button>
      </header>

      <p v-if="error.health" class="do__msg do__msg--err">{{ error.health }}</p>
      <p v-if="loading === 'health'" class="do__msg">{{ t('common.loading') }}</p>

      <template v-if="health">
        <p v-if="degradedText(health)" class="do__msg do__msg--warn">{{ degradedText(health) }}</p>
        <p v-if="degradedBlind(health)" class="do__msg do__msg--warn">{{ t('dashboardOps.degraded.blind') }}</p>

        <ul v-if="distributionEntries(health).length" class="do__chips">
          <li v-for="[k, v] in distributionEntries(health)" :key="k" class="do__chip">
            {{ k }} · {{ degradedNoData(health) ? NO_DATA : String(v) }}
          </li>
        </ul>
        <p v-else class="do__msg">{{ t('dashboardOps.empty') }}</p>
      </template>
    </section>

    <!-- ══ 4. 活跃会话（带分页） ═══════════════════════════════════ -->
    <section class="do__section">
      <header class="do__head">
        <h2 class="do__title">{{ t('dashboardOps.active.title') }}</h2>
        <button
          type="button"
          class="do__load"
          :disabled="loading === 'active'"
          @click="load('active')"
        >
          {{ loaded.active && !error.active ? t('dashboardOps.reload') : t('dashboardOps.load') }}
        </button>
      </header>

      <p v-if="error.active" class="do__msg do__msg--err">{{ error.active }}</p>
      <p v-if="loading === 'active'" class="do__msg">{{ t('common.loading') }}</p>

      <template v-if="active">
        <p v-if="degradedText(active)" class="do__msg do__msg--warn">{{ degradedText(active) }}</p>
        <p v-if="degradedBlind(active)" class="do__msg do__msg--warn">{{ t('dashboardOps.degraded.blind') }}</p>
        <!-- ★ data 与 metadata 各有一份分页，不一致即契约漂移 -->
        <p v-if="activePaginationDisagrees" class="do__msg do__msg--err">
          {{ t('dashboardOps.active.paginationDisagrees') }}
        </p>

        <p class="do__echo">
          {{ t('dashboardOps.active.count', {
            total: degradedNoData(active) ? NO_DATA : fmtInt(active.data?.total_active ?? 0),
            page: active.data?.page ?? 0,
            size: active.data?.size ?? 0,
          }) }}
        </p>

        <ul v-if="activeRows.length" class="do__list">
          <li v-for="(r, idx) in activeRows" :key="idx" class="do__item">
            <div class="do__item-head">
              <span class="do__item-title">{{ activeCell(r, 'session_id').text }}</span>
            </div>
            <dl class="do__kv">
              <div class="do__kv-row">
                <dt>{{ t('dashboardOps.active.requests') }}</dt>
                <dd :class="{ 'do__nodata': activeCell(r, 'request_count').nodata }">
                  {{ activeCell(r, 'request_count').text }}
                </dd>
              </div>
              <div class="do__kv-row">
                <dt>{{ t('dashboardOps.active.cost') }}</dt>
                <dd :class="{ 'do__nodata': activeCell(r, 'total_cost').nodata }">
                  {{ activeCell(r, 'total_cost').nodata
                    ? NO_DATA
                    : fmtUsd(Number((r as Record<string, unknown>).total_cost)) }}
                </dd>
              </div>
              <div class="do__kv-row">
                <dt>{{ t('dashboardOps.active.model') }}</dt>
                <dd :class="{ 'do__nodata': activeCell(r, 'primary_model').nodata }">
                  {{ activeCell(r, 'primary_model').text }}
                </dd>
              </div>
            </dl>
          </li>
        </ul>
        <p v-else-if="loaded.active && !error.active" class="do__msg">{{ t('dashboardOps.empty') }}</p>

        <!-- ★ 翻页控件只有「本页取满」才显示 -->
        <div class="do__pager">
          <button
            type="button"
            class="do__pager-btn"
            :disabled="(active.data?.page ?? 1) <= 1"
            @click="gotoPage(-1)"
          >{{ t('dashboardOps.active.prev') }}</button>
          <span class="do__pager-label">{{ t('dashboardOps.active.page', { page: active.data?.page ?? 0 }) }}</span>
          <button
            type="button"
            class="do__pager-btn"
            :disabled="!activeHasNext"
            @click="gotoPage(1)"
          >{{ t('dashboardOps.active.next') }}</button>
        </div>
      </template>
    </section>

    <!-- ══ 5. 模块执行统计 ═════════════════════════════════════════ -->
    <section class="do__section">
      <header class="do__head">
        <h2 class="do__title">{{ t('dashboardOps.modules.title') }}</h2>
        <button
          type="button"
          class="do__load"
          :disabled="loading === 'moduleStats'"
          @click="load('moduleStats')"
        >
          {{ loaded.moduleStats && !error.moduleStats ? t('dashboardOps.reload') : t('dashboardOps.load') }}
        </button>
      </header>

      <p v-if="error.moduleStats" class="do__msg do__msg--err">{{ error.moduleStats }}</p>
      <p v-if="loading === 'moduleStats'" class="do__msg">{{ t('common.loading') }}</p>

      <template v-if="moduleStats">
        <p v-if="degradedText(moduleStats)" class="do__msg do__msg--warn">{{ degradedText(moduleStats) }}</p>
        <p v-if="degradedBlind(moduleStats)" class="do__msg do__msg--warn">{{ t('dashboardOps.degraded.blind') }}</p>

        <div class="do__kpis">
          <div class="do__kpi">
            <span class="do__kpi-label">{{ t('dashboardOps.modules.totalModules') }}</span>
            <span class="do__kpi-value" :class="{ 'do__nodata': degradedNoData(moduleStats) }">
              {{ degradedNoData(moduleStats) ? NO_DATA : fmtInt(moduleStats.data?.summary.total_modules ?? 0) }}
            </span>
          </div>
          <div class="do__kpi">
            <span class="do__kpi-label">{{ t('dashboardOps.modules.totalExec') }}</span>
            <span class="do__kpi-value" :class="{ 'do__nodata': degradedNoData(moduleStats) }">
              {{ degradedNoData(moduleStats) ? NO_DATA : fmtInt(moduleStats.data?.summary.total_executions ?? 0) }}
            </span>
          </div>
        </div>

        <ul v-if="moduleRows.length" class="do__list">
          <li v-for="(r, idx) in moduleRows" :key="idx" class="do__item">
            <div class="do__item-head">
              <span class="do__item-title">{{ moduleCell(r, 'module_name').text }}</span>
            </div>
            <dl class="do__kv">
              <div class="do__kv-row">
                <dt>{{ t('dashboardOps.modules.executions') }}</dt>
                <dd :class="{ 'do__nodata': moduleCell(r, 'total_executions').nodata }">
                  {{ moduleCell(r, 'total_executions').text }}
                </dd>
              </div>
              <div class="do__kv-row">
                <dt>{{ t('dashboardOps.modules.cacheHit') }}</dt>
                <dd :class="{ 'do__nodata': moduleCell(r, 'cache_hit_rate').nodata }">
                  {{ moduleCell(r, 'cache_hit_rate').text }}
                </dd>
              </div>
            </dl>
          </li>
        </ul>
        <p v-else-if="loaded.moduleStats && !error.moduleStats" class="do__msg">{{ t('dashboardOps.empty') }}</p>
      </template>
    </section>

    <!-- ══ 6. 错误统计 ═════════════════════════════════════════════ -->
    <section class="do__section">
      <header class="do__head">
        <h2 class="do__title">{{ t('dashboardOps.errors.title') }}</h2>
        <button
          type="button"
          class="do__load"
          :disabled="loading === 'errors'"
          @click="load('errors')"
        >
          {{ loaded.errors && !error.errors ? t('dashboardOps.reload') : t('dashboardOps.load') }}
        </button>
      </header>

      <p v-if="error.errors" class="do__msg do__msg--err">{{ error.errors }}</p>
      <p v-if="loading === 'errors'" class="do__msg">{{ t('common.loading') }}</p>

      <template v-if="errors">
        <p v-if="degradedText(errors)" class="do__msg do__msg--warn">{{ degradedText(errors) }}</p>
        <p v-if="degradedBlind(errors)" class="do__msg do__msg--warn">{{ t('dashboardOps.degraded.blind') }}</p>

        <div class="do__kpis">
          <div class="do__kpi">
            <span class="do__kpi-label">{{ t('dashboardOps.errors.total') }}</span>
            <span class="do__kpi-value" :class="{ 'do__nodata': degradedNoData(errors) }">
              {{ degradedNoData(errors) ? NO_DATA : fmtInt(errors.data?.summary.total_errors ?? 0) }}
            </span>
          </div>
          <div class="do__kpi">
            <span class="do__kpi-label">{{ t('dashboardOps.errors.rate') }}</span>
            <!-- ★ 分母为 0 时不渲染 0%，渲染「无请求」 -->
            <span class="do__kpi-value" :class="{ 'do__nodata': errorRateMeaningless }">
              {{ errorRateText }}
            </span>
          </div>
        </div>

        <ul v-if="errorRows.length" class="do__list">
          <li v-for="(r, idx) in errorRows" :key="idx" class="do__item">
            <div class="do__item-head">
              <span class="do__item-title">{{ errorCell(r, 'error_message').text }}</span>
            </div>
            <dl class="do__kv">
              <div class="do__kv-row">
                <dt>{{ t('dashboardOps.errors.count') }}</dt>
                <dd :class="{ 'do__nodata': errorCell(r, 'count').nodata }">
                  {{ errorCell(r, 'count').text }}
                </dd>
              </div>
            </dl>
          </li>
        </ul>
        <p v-else-if="loaded.errors && !error.errors" class="do__msg">{{ t('dashboardOps.empty') }}</p>

        <ul v-if="errorDistRows.length" class="do__chips">
          <li v-for="(r, idx) in errorDistRows" :key="idx" class="do__chip">
            {{ errorCell(r, 'error_type').text }} · {{ errorCell(r, 'count').text }}
          </li>
        </ul>
      </template>
    </section>

    <!-- ══ 7. 性能 ═════════════════════════════════════════════════ -->
    <section class="do__section">
      <header class="do__head">
        <h2 class="do__title">{{ t('dashboardOps.perf.title') }}</h2>
        <button
          type="button"
          class="do__load"
          :disabled="loading === 'performance'"
          @click="load('performance')"
        >
          {{ loaded.performance && !error.performance ? t('dashboardOps.reload') : t('dashboardOps.load') }}
        </button>
      </header>

      <p v-if="error.performance" class="do__msg do__msg--err">{{ error.performance }}</p>
      <p v-if="loading === 'performance'" class="do__msg">{{ t('common.loading') }}</p>

      <template v-if="performance">
        <p v-if="degradedText(performance)" class="do__msg do__msg--warn">{{ degradedText(performance) }}</p>
        <p v-if="degradedBlind(performance)" class="do__msg do__msg--warn">{{ t('dashboardOps.degraded.blind') }}</p>
        <!-- ★ p50 ≤ p95 ≤ p99 乱序即契约漂移 -->
        <p v-if="quantilesOutOfOrder" class="do__msg do__msg--err">
          {{ t('dashboardOps.perf.quantilesOutOfOrder') }}
        </p>

        <div class="do__kpis">
          <div class="do__kpi">
            <span class="do__kpi-label">{{ t('dashboardOps.perf.p50') }}</span>
            <span class="do__kpi-value" :class="{ 'do__nodata': degradedNoData(performance) }">
              {{ degradedNoData(performance) ? NO_DATA : fmtNum(performance.data?.summary.p50_latency_ms ?? 0, 0) }}
            </span>
          </div>
          <div class="do__kpi">
            <span class="do__kpi-label">{{ t('dashboardOps.perf.p95') }}</span>
            <span class="do__kpi-value" :class="{ 'do__nodata': degradedNoData(performance) }">
              {{ degradedNoData(performance) ? NO_DATA : fmtNum(performance.data?.summary.p95_latency_ms ?? 0, 0) }}
            </span>
          </div>
          <div class="do__kpi">
            <span class="do__kpi-label">{{ t('dashboardOps.perf.p99') }}</span>
            <span class="do__kpi-value" :class="{ 'do__nodata': degradedNoData(performance) }">
              {{ degradedNoData(performance) ? NO_DATA : fmtNum(performance.data?.summary.p99_latency_ms ?? 0, 0) }}
            </span>
          </div>
        </div>

        <dl class="do__kv">
          <div class="do__kv-row">
            <dt>{{ t('dashboardOps.perf.throughput') }}</dt>
            <dd :class="{ 'do__nodata': degradedNoData(performance) }">
              {{ degradedNoData(performance) ? NO_DATA : fmtInt(perfThroughput.length) }}
            </dd>
          </div>
          <div class="do__kv-row">
            <dt>{{ t('dashboardOps.perf.slow') }}</dt>
            <dd :class="{ 'do__nodata': degradedNoData(performance) }">
              {{ degradedNoData(performance) ? NO_DATA : fmtInt(perfSlow.length) }}
            </dd>
          </div>
        </dl>

        <ul v-if="perfSlow.length" class="do__list">
          <li v-for="(r, idx) in perfSlow" :key="idx" class="do__item">
            <div class="do__item-head">
              <span class="do__item-title">{{ perfCell(r, 'session_key').text }}</span>
            </div>
            <dl class="do__kv">
              <div class="do__kv-row">
                <dt>{{ t('dashboardOps.perf.duration') }}</dt>
                <dd :class="{ 'do__nodata': perfCell(r, 'duration_ms').nodata }">
                  {{ perfCell(r, 'duration_ms').text }}
                </dd>
              </div>
            </dl>
          </li>
        </ul>
      </template>
    </section>

    <!-- ══ 8. 运维面（operational） ════════════════════════════════ -->
    <section class="do__section">
      <header class="do__head">
        <h2 class="do__title">{{ t('dashboardOps.operational.title') }}</h2>
        <button
          type="button"
          class="do__load"
          :disabled="loading === 'operational'"
          @click="load('operational')"
        >
          {{ loaded.operational && !error.operational ? t('dashboardOps.reload') : t('dashboardOps.load') }}
        </button>
      </header>

      <p v-if="error.operational" class="do__msg do__msg--err">{{ error.operational }}</p>
      <p v-if="loading === 'operational'" class="do__msg">{{ t('common.loading') }}</p>

      <template v-if="operational">
        <!-- ★★★ 「从未运行过」不是说「降级」—— 措辞必须更弱 -->
        <p
          v-if="discoveryNeverRan"
          class="do__msg do__msg--warn do__unknown"
        >{{ t('dashboardOps.operational.neverRan') }}</p>
        <p
          v-else-if="operationalDegraded"
          class="do__msg do__msg--warn"
        >{{ t('dashboardOps.operational.degradedWarn') }}</p>

        <dl class="do__kv">
          <div class="do__kv-row">
            <dt>{{ t('dashboardOps.operational.status') }}</dt>
            <dd>{{ operationalStatusText }}</dd>
          </div>
          <div class="do__kv-row">
            <dt>{{ t('dashboardOps.operational.discovery') }}</dt>
            <dd>{{ operational.background_tasks.discovery.status ?? NO_DATA }}</dd>
          </div>
          <!-- ★ `running` 只存在于 discovery（aux.go:41-44）。
               probe_loop 恒是单键 map（aux.go:61），没有 running 字段 ——
               把它渲染成「运行中/已停止」会凭空造出一个后端从没说过的状态。 -->
          <div class="do__kv-row">
            <dt>{{ t('dashboardOps.operational.discoveryRunning') }}</dt>
            <dd>{{ operational.background_tasks.discovery.running
              ? t('dashboardOps.operational.running')
              : t('dashboardOps.operational.stopped') }}</dd>
          </div>
          <div class="do__kv-row">
            <dt>{{ t('dashboardOps.operational.checks10m') }}</dt>
            <!-- ★ Go 侧是 `var checksLast10m int`：查询失败时 Scan 不写，
                 零值 0 原样下发（aux.go:53-58 只 slog，不置错）。
                 ⇒ 这个键**恒是数字**，判 `=== null` 恒 false，是条恒真判据。
                 真正可用的信号是同级的 `probe_degraded`（仅 checksErr != nil 时出现），
                 所以用「是否 probe_degraded」决定渲染「无数据」还是 0。 -->
            <dd :class="{ 'do__nodata': operational.background_tasks.probe_degraded === true }">
              {{ operational.background_tasks.probe_degraded === true
                ? NO_DATA
                : fmtInt(operational.background_tasks.probe_loop.checks_last_10m) }}
            </dd>
          </div>
          <div class="do__kv-row">
            <dt>{{ t('dashboardOps.operational.selfcheck24h') }}</dt>
            <!-- ★ 同理：total_runs_24h 是 COUNT 的目标，查询失败时也是 0。
                 用 selfcheck.degraded 决定，而不是拿 0 冒充「真的 0 次」。 -->
            <dd :class="{ 'do__nodata': selfCheckMeaningless }">
              {{ selfCheckMeaningless
                ? t('dashboardOps.operational.noRuns')
                : fmtInt(operational.selfcheck.total_runs_24h) }}
            </dd>
          </div>
          <div class="do__kv-row">
            <dt>{{ t('dashboardOps.operational.successRate') }}</dt>
            <!-- ★ 同样是 float64 零值：`total > 0` 才算，否则留 0（aux.go:90-93）。
                 判 `=== null` 恒 false；分母为 0 时这个比值本身无意义。 -->
            <dd :class="{ 'do__nodata': selfCheckMeaningless }">
              {{ selfCheckMeaningless
                ? t('dashboardOps.operational.noRuns')
                : fmtNum(operational.selfcheck.success_rate, 2) }}
            </dd>
          </div>
          <div class="do__kv-row">
            <dt>{{ t('dashboardOps.operational.lastStatus') }}</dt>
            <dd>{{ operational.selfcheck.last_status ?? NO_DATA }}</dd>
          </div>
        </dl>
      </template>
    </section>

    <!-- ══ 9. 错误下钻 ═════════════════════════════════════════════ -->
    <section class="do__section">
      <header class="do__head">
        <h2 class="do__title">{{ t('dashboardOps.drill.title') }}</h2>
      </header>

      <div class="do__filters">
        <label class="do__field">
          <span class="do__field-label">{{ t('dashboardOps.drill.kind') }}</span>
          <input v-model="errorKind" type="text" class="do__input" />
        </label>
        <label class="do__field">
          <span class="do__field-label">{{ t('dashboardOps.drill.dimension') }}</span>
          <input v-model="drillDimension" type="text" class="do__input" />
        </label>
        <label class="do__field">
          <span class="do__field-label">{{ t('dashboardOps.drill.days') }}</span>
          <input v-model.number="drillDays" type="number" class="do__input" min="1" max="90" />
        </label>
        <p v-if="!drillReady" class="do__msg">{{ t('dashboardOps.drill.needKind') }}</p>
        <!-- ★ drill 的 days 是 clamp（不是静默回落），回显实际生效值 -->
        <p class="do__echo">{{ t('dashboardOps.drill.daysEcho', { days: drillDaysEcho }) }}</p>
        <button
          type="button"
          class="do__load"
          :disabled="loading === 'drill' || !drillReady"
          @click="load('drill')"
        >
          {{ loaded.drill && !error.drill ? t('dashboardOps.reload') : t('dashboardOps.load') }}
        </button>
      </div>

      <p v-if="error.drill" class="do__msg do__msg--err">{{ error.drill }}</p>
      <p v-if="loading === 'drill'" class="do__msg">{{ t('common.loading') }}</p>

      <template v-if="drill">
        <!-- ★ source 是条件键：缺失 = 现算，不是「来源未知」 -->
        <p class="do__echo">
          {{ drillFromCache
            ? t('dashboardOps.drill.fromCache')
            : t('dashboardOps.drill.computedNow') }}
        </p>
        <p v-if="drillIsEmpty" class="do__msg">{{ t('dashboardOps.empty') }}</p>

        <ul v-else class="do__list">
          <li v-for="it in drillRows" :key="it.key" class="do__item">
            <div class="do__item-head">
              <span class="do__item-title">{{ it.key }}</span>
            </div>
            <dl class="do__kv">
              <div class="do__kv-row">
                <dt>{{ t('dashboardOps.drill.requests') }}</dt>
                <dd>{{ fmtInt(it.requests) }}</dd>
              </div>
              <div class="do__kv-row">
                <dt>{{ t('dashboardOps.drill.tokens') }}</dt>
                <dd>{{ fmtInt(it.tokens) }}</dd>
              </div>
              <div class="do__kv-row">
                <dt>{{ t('dashboardOps.drill.cost') }}</dt>
                <dd>{{ fmtUsd(it.cost_usd) }}</dd>
              </div>
            </dl>
          </li>
        </ul>
      </template>
    </section>
  </div>
</template>

<style scoped>
/* DashboardOpsView — 触控热区一律 ≥48 CSS px（UI规范 17 §4-R1）。 */
.do__section {
  margin-bottom: 16px;
}
.do__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}
.do__title {
  font-size: 15px;
  font-weight: 600;
  margin: 0;
}
.do__load {
  min-height: 48px;
  min-width: 88px;
  padding: 0 16px;
  border: 1px solid var(--border, #d0d7de);
  border-radius: 8px;
  background: var(--surface, #fff);
  color: inherit;
  font-size: 14px;
  cursor: pointer;
}
.do__load:disabled {
  opacity: 0.6;
  cursor: default;
}
.do__filters {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  align-items: flex-end;
  margin-bottom: 12px;
}
.do__field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.do__field-label {
  font-size: 12px;
  opacity: 0.75;
}
.do__input {
  min-height: 48px;
  padding: 0 10px;
  border: 1px solid var(--border, #d0d7de);
  border-radius: 8px;
  font-size: 14px;
}
.do__msg {
  font-size: 13px;
  margin: 4px 0;
  opacity: 0.8;
}
.do__msg--err {
  color: var(--danger, #cf222e);
  opacity: 1;
}
.do__msg--warn {
  color: var(--warning, #9a6700);
  opacity: 1;
}
/* ★ 「状态未知」与「降级」在字形与底色上都要能一眼分开 */
.do__unknown {
  background: rgba(154, 103, 0, 0.08);
  padding: 8px;
  border-radius: 6px;
}
.do__echo {
  font-size: 12px;
  opacity: 0.7;
  margin: 4px 0;
}
.do__kpis {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}
.do__kpi {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 10px;
  border: 1px solid var(--border, #d0d7de);
  border-radius: 8px;
}
.do__kpi-label {
  font-size: 12px;
  opacity: 0.75;
}
.do__kpi-value {
  font-size: 18px;
  font-weight: 600;
}
/* ★ 无数据占位：字形与 class 双锚，避免「—」被当成 0 */
.do__nodata {
  opacity: 0.55;
  font-style: italic;
}
.do__kv {
  margin: 8px 0 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.do__kv-row {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  font-size: 13px;
}
.do__kv dt {
  opacity: 0.75;
}
.do__kv dd {
  margin: 0;
  text-align: right;
  word-break: break-all;
}
.do__list {
  list-style: none;
  margin: 8px 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.do__item {
  border: 1px solid var(--border, #d0d7de);
  border-radius: 8px;
  padding: 10px;
}
.do__item-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 4px;
}
.do__item-title {
  font-size: 14px;
  font-weight: 600;
  word-break: break-all;
}
.do__chips {
  list-style: none;
  margin: 8px 0 0;
  padding: 0;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.do__chip {
  font-size: 12px;
  padding: 8px 10px;
  border: 1px solid var(--border, #d0d7de);
  border-radius: 999px;
  min-height: 48px;
  display: inline-flex;
  align-items: center;
}
.do__pager {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 12px;
}
.do__pager-btn {
  min-height: 48px;
  min-width: 72px;
  padding: 0 14px;
  border: 1px solid var(--border, #d0d7de);
  border-radius: 8px;
  background: var(--surface, #fff);
  color: inherit;
  font-size: 14px;
  cursor: pointer;
}
.do__pager-btn:disabled {
  opacity: 0.5;
  cursor: default;
}
.do__pager-label {
  font-size: 13px;
  opacity: 0.75;
}
</style>