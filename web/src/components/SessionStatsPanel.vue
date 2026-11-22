<script setup lang="ts">
/**
 * SessionStatsPanel — 首页 stats tab 会话基础统计总览
 * KPI / 趋势对齐 session_summaries + days；细分块复用已拉的 perf/error。
 *
 * 2026-11-02：与看板共用天数；消费 data-horizon；turns 对拍 degraded；
 * 探测流量入口；perf/error 标明模块执行宇宙。
 *
 * 2026-11-23 审计轮（★ 撤销上一轮的实现）：
 *   上一轮在此处接了 session-analytics API 取用户/供应商 Top5，有三处硬伤：
 *   ① **权限错配**——session-analytics 端点要求 admin 角色
 *      （isAdminFromRequest: super_admin/tenant_admin/admin_key），而本面板
 *      对**所有登录用户**可见 ⇒ 普通用户必然 403，只会看到两个空卡片 +
 *      控制台报错；
 *   ② **双数据源混排**——topClients/topTasks/modelUsage 来自 dashboard 实时
 *      腿，topUsers/topProviders 来自物化视图缓存腿（15min）⇒ 同一区块内
 *      不同卡片的时间戳不同，读者无从判断哪个是哪个口径；
 *   ③ **供应商口径错**——provider 只能由 model_usage_aggregates.provider 聚合，
 *      前端 split('/') 会把模型名当 provider（详见 api/sessionAnalytics.ts 注释）。
 *   ⇒ 本面板回到**单一数据源**（dashboard 实时腿）；「模型/用户/供应商/任务」
 *   Top5 四件套统一落在 /admin/session-analytics/users（admin 域，
 *   口径与 /admin/session-analytics 完全一致）。
 *   口径一致的证据仍在本面板：对齐指标行（aligned）读的正是
 *   domains/analytics 单一真源，与会话分析页同一实现。
 */
import { computed, inject, ref, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Plus, Minus, Money, CircleCheck } from '@element-plus/icons-vue'
import { useDashboard } from '../composables/useDashboard'
import { useDataHorizon } from '../composables/useDataHorizon'
import DashboardStatsRow, { type DashboardStats } from './analytics/DashboardStatsRow.vue'
import SessionTrendChart from './analytics/SessionTrendChart.vue'
import HealthGradeChart from './analytics/HealthGradeChart.vue'
import SessionStatsSignals from './analytics/SessionStatsSignals.vue'
import SessionStatsRankings from './analytics/SessionStatsRankings.vue'
import PageHelpDrawer from './help/PageHelpDrawer.vue'
import type { TrendDataPoint } from './analytics/SessionTrendChart.vue'

import { ElIcon } from 'element-plus'
import KxDateRangePicker from './ui/KxDateRangePicker.vue'
import { rangeFromSpanDays, rangeSpanDays } from './ui/kxDatePresets'
import type { KxDateRange, KxDateRangePreset } from './ui/kx-date-types'
import { localDateStamp } from '../i18n/localDate'
const { t } = useI18n()
const router = useRouter()

// 2026-11-23 owner 图批（紧凑轮）：「统计标准」入口上移到仪表盘页头
// （与「整站数据」同一行）。本页被两处复用：
//   · 仪表盘「会话与统计」页签（DashboardView2）—— 入口由页头提供，
//     本面板传 showStandardsHelp=false 关掉自己那份（避免同一页两个入口）；
//   · 会话分析用户页（SessionAnalyticsUsersView）—— 入口仍由本面板提供。
// ⇒ 「统计标准」正文 id 两处必须一致（否则用户看到两份各自为政的说明），
// 由 StatsStandardsHelpWiring.test.ts 钉住。
const props = withDefaults(defineProps<{ showStandardsHelp?: boolean }>(), {
  showStandardsHelp: true,
})

// 2026-11-23 统计标准轮：页面内嵌的「统计标准」帮助入口。
// 「会话与统计」是会话级口径（一个会话一条、取会话累计值）、窗口是滚动
// N 天；「看板」是请求级口径、窗口是日历日 ⇒ 两页数字本来就不该相等。
// 这枚按钮打开《看板与会话统计的统计标准》正文（含逐项对照表）。
const statsStandardsHelpId = 'dashboard-stats-standards'

const boardState = inject<{
  days: Ref<number>
} | null>('dashboardBoard', null)

const boardDays = computed(() => {
  const n = boardState?.days?.value
  return typeof n === 'number' && n > 0 ? n : null
})

// 2026-11-21 统计口径对齐轮：包含探测开关（与 board/request-logs 同语义同命名；
// 缺省排除探测 = 与 /admin/session-analytics 同宇宙）。持久化偏好独立键。
// extraParams 走 useDashboard 常驻参数，自动刷新不会丢。
const SESSION_PROBE_SCOPE_KEY = 'llmgw_session_probe_scope'
const includeProbes = ref(false)
try {
  includeProbes.value = localStorage.getItem(SESSION_PROBE_SCOPE_KEY) === 'included'
} catch { /* localStorage 不可用 ⇒ 保持缺省业务宇宙 */ }

const {
  loading,
  error,
  overview,
  trend,
  performanceStats,
  errorStats,
  refresh,
  changeDays,
  days,
} = useDashboard({
  autoRefresh: true,
  refreshInterval: 5 * 60 * 1000,
  // 与看板同源：有 board days 时用它，否则默认 7（不再静默 30）
  defaultDays: boardDays.value ?? 7,
  extraParams: computed(() => ({ include_probes: includeProbes.value || undefined })),
})

// ★ 2026-11-23（L7 修复）：口径开关变化**必须触发重载**。
// 原实现里这个 watch 只写 localStorage、不重载 ⇒ 勾选「包含探测」后页面继续
// 显示旧口径的数字，看起来就像「include_probes 参数没生效」（实际参数已进
// URL，但请求根本没重新发）。watch 放在 useDashboard 之后：refresh 由它解构
// 出来，提前声明会踩 TS 的 block-scoped-before-declaration。
watch(includeProbes, (v) => {
  try {
    if (v) localStorage.setItem(SESSION_PROBE_SCOPE_KEY, 'included')
    else localStorage.removeItem(SESSION_PROBE_SCOPE_KEY)
  } catch { /* 同上 */ }
  void refresh()
})

watch(
  boardDays,
  (next) => {
    if (next == null || next === days.value) return
    void changeDays(next)
  },
  { immediate: true },
)

const { state: horizonState } = useDataHorizon()
const showHorizonHint = computed(() => horizonState.value === 'stale' || horizonState.value === 'failed')
const turnsDegraded = computed(() => overview.value?.turns_alignment?.degraded === true)

// 会话分析对齐行（缓存面 15 分钟一刷；缺席=缓存面未就绪，显式披露不猜）。
const aligned = computed(() => overview.value?.aligned ?? null)
const alignedAvailable = computed(() => overview.value?.aligned_source === 'user_session_aggregates' && !!aligned.value)
const probeDegraded = computed(() => overview.value?.probe_scope === 'degraded_included')
const includeProbesActive = computed(() => overview.value?.probe_scope === 'included' || overview.value?.probe_scope === 'degraded_included')
// 对齐窗口的日历日（与后端 shanghaiPresetRange 同折算：系统日历 [today-(N-1), today]）。
const trendWindow = computed(() => {
  const n = Math.max(1, days.value)
  const end = new Date()
  const start = new Date(end)
  start.setDate(start.getDate() - (n - 1))
  return { start: localDateStamp(start), end: localDateStamp(end) }
})

function openAlignedAnalytics() {
  const a = aligned.value
  if (!a) return
  void router.push({
    path: '/admin/session-analytics/users',
    query: { start_date: a.start_date, end_date: a.end_date },
  })
}

const dashboardStats = computed<DashboardStats>(() => {
  const ov = overview.value
  const cost = ov?.cost_stats
  const fromTrend = ov?.cost_trend?.reduce((sum, p) => sum + (p.requests ?? 0), 0) ?? 0
  return {
    totalSessions: ov?.total_sessions ?? 0,
    totalSessionsChange: (() => {
      const g = trend.value?.summary?.growth_rate
      return g != null && Math.abs(g) > 1e-9 ? g : null
    })(),
    activeSessions: ov?.active_sessions ?? 0,
    totalCost: cost?.total_cost_usd ?? 0,
    totalCostChange: (() => {
      const g = cost?.cost_growth_pct
      return g != null && Math.abs(g) > 1e-9 ? g : null
    })(),
    complianceRate: ov?.compliance_stats?.compliance_rate ?? 0,
    complianceRateChange: null,
    avgHealthScore: ov?.health_distribution?.avg_score ?? null,
    avgHealthScoreChange: null,
    avgLatency: cost?.avg_latency_ms ?? 0,
    avgLatencyChange: null,
    totalRequests: cost?.total_requests ?? fromTrend,
    totalRequestsChange: null,
    totalTokens: 0,
    totalTokensChange: null,
  }
})

const pipelineLatency = computed(() => performanceStats.value?.summary ?? null)
const errorSummary = computed(() => errorStats.value?.summary ?? null)

const trendChartData = computed<TrendDataPoint[]>(() => {
  if (trend.value?.trend?.length) {
    return trend.value.trend.map((p) => ({
      date: p.date,
      new_sessions: p.new_sessions,
      active_sessions: p.active_sessions,
      closed_sessions: p.closed_sessions,
      total_cost: p.total_cost ?? 0,
      total_requests: p.total_requests ?? 0,
    }))
  }
  const ov = overview.value
  if (!ov) return []
  const costByDate = new Map(ov.cost_trend.map(p => [p.date, p]))
  return ov.session_trend.map(st => {
    const cost = costByDate.get(st.date)
    return {
      date: st.date,
      new_sessions: st.new_sessions,
      active_sessions: st.active_count,
      closed_sessions: st.closed_count,
      total_cost: cost?.cost ?? 0,
      total_requests: cost?.requests ?? 0,
    }
  })
})

function formatUsd(value: number) {
  return `$${value.toFixed(4)}`
}

function formatPct(value: number) {
  const sign = value > 0 ? '+' : ''
  return `${sign}${value.toFixed(1)}%`
}

// 2026-11-19 时间范围统一轮：7/30 两天按钮组 → 全站统一 KxDateRangePicker
// （1/7/30/90 档 + 自定义区间）。days 与看板 boardDays 双向联动保持不变。
//
// ★ 2026-11-23 审计轮（P2-1 / P2-3）：本面板的统计窗口在服务端是**滚动窗口**
//   —— admin/dashboardapi/session_overview.go 的每条腿都是
//   `first_request_at >= NOW() - INTERVAL 'N days'`（450/638/725/807/898/919 行），
//   响应里也自报 window_mode="rolling_days"。
//   ⇒ 真实起点比「含首尾的 N 个日历日」**早一天**（N=7 ⇒ 真实起点 = 今天前 7 天，
//     而日历日 7 天窗的起点是今天前 6 天）。
//   ⇒ 而控件此前显示的是**日历日区间**（useSpanDaysRange ⇒ rangeFromSpanDays）
//   ⇒ 用户看到的起止与真实查询范围对不上（起点差一天）。
//
// ★ 修法按后端**既有语义**这一边（滚动窗口），**不改请求**
//   （改请求＝改口径＝两头改 ⇒ 数字会变、且与看板/会话分析页不同源）：
//   ① 预设与控件一律回填**滚动窗口的真实起止**（[today-N, today]）；
//   ② days=1 的档位改标「近24小时」（common.dateRange.preset.last24h，8 语种已有）
//      —— 滚动 24 小时窗口本来就不是「今天」，标「今天」才是**文案与事实相反**；
//   ③ 自定义入口禁用（:presets-only）—— 端点只收 days，手选历史区间会被
//      静默压成「跨度天数」（P2-1；同族做法见 AGENTS.md「时间范围静默改写收口轮」）。
const STATS_RANGE_BUCKETS = [
  { days: 1, labelKey: 'common.dateRange.preset.last24h' },
  { days: 7, labelKey: 'common.dateRange.preset.last7d' },
  { days: 30, labelKey: 'common.dateRange.preset.last30d' },
  { days: 90, labelKey: 'dashboard.range.last90d' },
] as const

/** 滚动窗口 [now-N 天, now] 的**日历日**跨度：[today-N, today]。 */
function rollingWindowRange(days: number): KxDateRange {
  // rangeFromSpanDays(n) = [today-(n-1), today] ⇒ n = days+1 即 [today-days, today]
  return rangeFromSpanDays(days + 1)
}

const statsDayPresets = computed<KxDateRangePreset[]>(() =>
  STATS_RANGE_BUCKETS.map((bucket) => ({
    id: `span${bucket.days}d`,
    labelKey: bucket.labelKey,
    resolve: () => rollingWindowRange(bucket.days),
  })),
)
// 控件显示的区间 = 当前窗口的真实起止（与请求的 days 同一口径）。
const statsRange = computed<KxDateRange>(() => rollingWindowRange(days.value))

// 改 days 后必须走 changeDays（它会 loadAll 重取）；直接写 days.value 不会发请求。
function applyStatsRange(range: KxDateRange) {
  // ★ 滚动窗口的日历日跨度是 **N+1** 天 ⇒ 不能按区间跨度直接回写 days（会多一天）
  //   ⇒ 按命中的档位回写；命中不到（跨零点等边缘）时用「跨度 - 1」兜底。
  const bucket = STATS_RANGE_BUCKETS.find((b) => {
    const r = rollingWindowRange(b.days)
    return r.start === range.start && r.end === range.end
  })
  const next = bucket ? bucket.days : Math.max(1, rangeSpanDays(range) - 1)
  if (next === days.value) return
  void changeDays(next)
}

// Expose loading state and refresh function for parent components
defineExpose({
  loading,
  refresh,
})
</script>

<template>
  <div class="session-stats-panel">
    <div v-if="error" class="alert alert-danger" role="alert">
      <span class="alert-text">{{ error }}</span>
      <button type="button" class="alert-retry" :disabled="loading" @click="refresh">
        {{ t('sessions.stats.retry') }}
      </button>
    </div>

    <div
      v-if="showHorizonHint"
      class="alert alert-warn"
      role="status"
      data-testid="stats-horizon-hint"
    >
      {{ t('sessions.stats.dataHorizonStale') }}
    </div>

    <div
      v-if="turnsDegraded"
      class="alert alert-warn"
      role="status"
      data-testid="stats-turns-degraded"
    >
      {{ t('sessions.stats.turnsAlignmentDegraded') }}
      <span v-if="overview?.turns_alignment" class="alert-meta">
        (summaries={{ overview.turns_alignment.summary_requests.toLocaleString() }}
        · turns={{ overview.turns_alignment.turns_count.toLocaleString() }}
        · Δ={{ overview.turns_alignment.abs_delta.toLocaleString() }})
      </span>
    </div>

    <!-- 2026-11-21 对齐轮：宇宙依赖缺失自动降级（全量宇宙）——降级可见不是静默。 -->
    <div
      v-if="probeDegraded"
      class="alert alert-warn"
      role="status"
      data-testid="stats-probe-degraded"
    >
      {{ t('sessions.stats.probeScopeDegraded') }}
    </div>

    <div :class="{ 'session-stats-panel--has-error': error, 'session-stats-panel--loading': loading }">
      <!-- Loading indicator -->
      <div v-if="loading" class="loading-indicator" data-testid="stats-loading-indicator">
        <div class="loading-spinner"></div>
        <span class="loading-text">{{ t('sessions.stats.loading') }}</span>
      </div>

      <div class="panel-toolbar">
        <!-- 2026-11-20 owner 图批：时间选择器移到左上（原「统计周期」信息块的
             位置——批「移到之前的位置」）；「统计周期/周期新会话/日均新会话」
             信息块按图批删除（批「不需要」）。
             2026-11-23 owner 图批（紧凑轮）：右侧「最后更新 · Nms」与「查看探测
             流量」按批删除（批「不需要，请移除」）⇒ toolbar 的右侧整块 meta 区
             随之清空；「统计标准」入口按批上移到页头（与「整站数据」同一行，
             由 DashboardView2 渲染 —— 本面板用 showStandardsHelp=false 关掉自己
             那一份）；「包含探测」开关按批贴到时间范围选择的**右边**（原先被
             space-between 顶到页面中间，离控件很远）⇒ 时间选择器 + 探测开关
             合成一组靠左。 -->
        <div class="panel-toolbar__start">
          <!-- ★ 2026-11-23 审计轮（P2-1 / P2-3）：
               · :presets-only —— 端点只收 days（后端把 days 解释成**滚动窗口**），
                 自定义历史区间会被静默压成「跨度天数」⇒ 禁掉自定义入口
                 （与 AGENTS.md「时间范围静默改写收口轮」的方案 B 同款）。
               · :max-span-days="91" —— 滚动窗口 N 天的**日历日跨度是 N+1 天**
                 （[today-N, today]）⇒ 上限必须 ≥ 91，否则「近 90 天」档位
                 会被跨度校验挡住（点了没反应）。
               · 控件显示的区间 = 滚动窗口的真实起止（见上面的 rollingWindowRange）。 -->
          <KxDateRangePicker
            :model-value="statsRange"
            :presets="statsDayPresets"
            :max-span-days="91"
            :presets-only="true"
            :disabled="loading"
            @apply="applyStatsRange"
          />
          <!-- 2026-11-21 对齐轮：包含探测开关。缺省排除探测（与会话分析同宇宙）；
               开启后回到历史全量宇宙（数字会显著变大，徽标披露口径）。 -->
          <label
            class="session-probe-scope"
            data-testid="session-probe-scope"
            :title="t('sessions.stats.probeScopeHint')"
          >
            <input
              v-model="includeProbes"
              type="checkbox"
              data-testid="session-probe-scope-input"
            >
            <span>{{ t('sessions.stats.probeScopeLabel') }}</span>
            <span
              v-if="includeProbesActive"
              class="probe-scope-badge"
              data-testid="session-probe-scope-badge"
            >{{ t('sessions.stats.probeScopeBadge') }}</span>
          </label>
        </div>
        <!-- 「统计标准」入口的**页内**形态（仅当本页自己提供时渲染）。
             看板页由 BoardPanel 的筛选条插槽提供；仪表盘页由页头提供
             （showStandardsHelp=false）⇒ 同一时刻只有一处入口，不会重复。 -->
        <div v-if="props.showStandardsHelp" class="panel-toolbar__end">
          <PageHelpDrawer
            route-path="/dashboard"
            :content-id="statsStandardsHelpId"
            :label="t('pageHelp.button.standards')"
          />
        </div>
      </div>

      <DashboardStatsRow :stats="dashboardStats" :loading="loading" :show-tokens="false" />

      <!-- 2026-11-21 统计口径对齐轮：会话分析对齐指标行。
           与 /admin/session-analytics 总览页同一实现（domains/analytics 单一真源，
           物化视图缓存面 15 分钟一刷）；窗口=上海日历日 [start,end]（随天数预设）。
           单位纪律：内部成本=美元、外部成本=积分，禁止相加（framework §3.2）。 -->
      <div
        v-if="alignedAvailable && aligned"
        class="aligned-row"
        data-testid="stats-aligned-row"
      >
        <div class="aligned-row__header">
          <span class="aligned-row__title">{{ t('sessions.stats.alignedTitle') }}</span>
          <span class="aligned-row__window">{{ aligned.start_date }} ~ {{ aligned.end_date }}</span>
          <span class="aligned-row__source">{{ t('sessions.stats.alignedSource') }}</span>
          <button type="button" class="probe-link" data-testid="stats-aligned-open" @click="openAlignedAnalytics">
            {{ t('sessions.stats.alignedOpen') }}
          </button>
        </div>
        <div class="aligned-row__cards">
          <div class="aligned-cell">
            <div class="aligned-cell__label">{{ t('sessions.stats.alignedTotalSessions') }}</div>
            <div class="aligned-cell__value">{{ aligned.total_sessions.toLocaleString() }}</div>
          </div>
          <div class="aligned-cell">
            <div class="aligned-cell__label">{{ t('sessions.stats.alignedActiveUsers') }}</div>
            <div class="aligned-cell__value">{{ aligned.active_users.toLocaleString() }}</div>
          </div>
          <div class="aligned-cell">
            <div class="aligned-cell__label">{{ t('sessions.stats.alignedActiveProjects') }}</div>
            <div class="aligned-cell__value">{{ aligned.active_projects.toLocaleString() }}</div>
          </div>
          <div class="aligned-cell">
            <div class="aligned-cell__label">{{ t('sessions.stats.alignedActiveTasks') }}</div>
            <div class="aligned-cell__value">{{ aligned.active_tasks.toLocaleString() }}</div>
          </div>
          <div class="aligned-cell">
            <div class="aligned-cell__label">{{ t('sessions.stats.alignedAvgTokens') }}</div>
            <div class="aligned-cell__value">{{ aligned.avg_tokens.toLocaleString(undefined, { maximumFractionDigits: 1 }) }}</div>
          </div>
          <div class="aligned-cell">
            <div class="aligned-cell__label">{{ t('sessions.stats.alignedAvgTurns') }}</div>
            <div class="aligned-cell__value">{{ aligned.avg_turns.toFixed(1) }}</div>
          </div>
          <div class="aligned-cell">
            <div class="aligned-cell__label">{{ t('sessions.stats.alignedAvgDuration') }}</div>
            <div class="aligned-cell__value">{{ aligned.avg_duration_seconds.toFixed(0) }}s</div>
          </div>
          <div class="aligned-cell">
            <div class="aligned-cell__label">{{ t('sessions.stats.alignedSuccessRate') }}</div>
            <div class="aligned-cell__value">{{ aligned.success_rate_pct.toFixed(1) }}%</div>
          </div>
          <div class="aligned-cell">
            <div class="aligned-cell__label">{{ t('sessions.stats.alignedCacheRate') }}</div>
            <div class="aligned-cell__value">{{ aligned.cache_rate_pct.toFixed(1) }}%</div>
          </div>
          <div class="aligned-cell">
            <div class="aligned-cell__label">{{ t('sessions.stats.alignedInternalCost') }}</div>
            <div class="aligned-cell__value">${{ aligned.internal_cost_usd.toFixed(2) }}</div>
          </div>
          <div class="aligned-cell">
            <div class="aligned-cell__label">{{ t('sessions.stats.alignedExternalCredits') }}</div>
            <div class="aligned-cell__value">{{ aligned.external_credits.toLocaleString() }}</div>
          </div>
        </div>
      </div>
      <div
        v-else-if="overview?.aligned_source === 'unavailable'"
        class="aligned-row aligned-row--unavailable"
        role="status"
        data-testid="stats-aligned-unavailable"
      >
        {{ t('sessions.stats.alignedUnavailable') }}
      </div>

      <div class="secondary-kpi-row">
        <div class="mini-kpi">
          <div class="mini-kpi__icon mini-kpi__icon--new"><el-icon :size="24"><Plus /></el-icon></div>
          <div>
            <div class="mini-kpi__label">{{ t('sessions.stats.newSessions24h') }}</div>
            <div class="mini-kpi__value">{{ overview?.new_sessions_24h?.toLocaleString() ?? 0 }}</div>
          </div>
        </div>
        <div class="mini-kpi">
          <div class="mini-kpi__icon mini-kpi__icon--closed"><el-icon :size="24"><Minus /></el-icon></div>
          <div>
            <div class="mini-kpi__label">{{ t('sessions.stats.closedSessions24h') }}</div>
            <div class="mini-kpi__value">{{ overview?.closed_sessions_24h?.toLocaleString() ?? 0 }}</div>
          </div>
        </div>
        <div class="mini-kpi">
          <div class="mini-kpi__icon mini-kpi__icon--cost"><el-icon :size="24"><Money /></el-icon></div>
          <div>
            <div class="mini-kpi__label">{{ t('sessions.stats.avgCostPerSession') }}</div>
            <div class="mini-kpi__value">{{ formatUsd(overview?.cost_stats?.avg_cost_per_session ?? 0) }}</div>
          </div>
        </div>
        <div class="mini-kpi">
          <div class="mini-kpi__icon mini-kpi__icon--compliance"><el-icon :size="24"><CircleCheck /></el-icon></div>
          <div>
            <div class="mini-kpi__label">{{ t('sessions.stats.violations') }}</div>
            <div class="mini-kpi__value">{{ overview?.compliance_stats?.violation?.toLocaleString() ?? 0 }}</div>
          </div>
        </div>
      </div>

      <div class="charts-row">
        <div class="charts-row__trend">
          <SessionTrendChart :data="trendChartData" :loading="loading" />
        </div>
        <div class="charts-row__health">
          <HealthGradeChart
            :distribution="overview?.health_distribution ?? null"
            :avg-score="overview?.health_distribution?.avg_score"
            :loading="loading"
          />
        </div>
      </div>

      <div class="detail-grid">
        <div class="detail-card">
          <div class="detail-card__header">{{ t('sessions.stats.costBreakdown') }}</div>
          <dl class="kv-list">
            <div class="kv-row">
              <dt>{{ t('sessions.stats.totalCost') }}</dt>
              <dd>
                {{ formatUsd(overview?.cost_stats?.total_cost_usd ?? 0) }}
                <span
                  v-if="overview?.cost_stats?.cost_growth_pct != null"
                  class="growth-tag"
                  :class="(overview?.cost_stats?.cost_growth_pct ?? 0) > 0 ? 'growth-tag--bad' : 'growth-tag--good'"
                >{{ formatPct(overview?.cost_stats?.cost_growth_pct ?? 0) }}</span>
              </dd>
            </div>
            <div class="kv-row"><dt>{{ t('sessions.stats.inputCost') }}</dt><dd>{{ formatUsd(overview?.cost_stats?.input_cost_usd ?? 0) }}</dd></div>
            <div class="kv-row"><dt>{{ t('sessions.stats.outputCost') }}</dt><dd>{{ formatUsd(overview?.cost_stats?.output_cost_usd ?? 0) }}</dd></div>
            <div class="kv-row"><dt>{{ t('sessions.stats.avgCostPerRequest') }}</dt><dd>{{ formatUsd(overview?.cost_stats?.avg_cost_per_request ?? 0) }}</dd></div>
            <div class="kv-row"><dt>{{ t('sessions.stats.maxCostSession') }}</dt><dd>{{ formatUsd(overview?.cost_stats?.max_cost_session ?? 0) }}</dd></div>
          </dl>
        </div>
        <div class="detail-card">
          <div class="detail-card__header">{{ t('sessions.stats.complianceBreakdown') }}</div>
          <dl class="kv-list">
            <div class="kv-row"><dt>{{ t('sessions.stats.compliant') }}</dt><dd>{{ overview?.compliance_stats?.compliant?.toLocaleString() ?? 0 }}</dd></div>
            <div class="kv-row"><dt>{{ t('sessions.stats.warnings') }}</dt><dd>{{ overview?.compliance_stats?.warning?.toLocaleString() ?? 0 }}</dd></div>
            <div class="kv-row"><dt>{{ t('sessions.stats.violations') }}</dt><dd>{{ overview?.compliance_stats?.violation?.toLocaleString() ?? 0 }}</dd></div>
            <div class="kv-row"><dt>{{ t('sessions.stats.promptInjection') }}</dt><dd>{{ overview?.compliance_stats?.prompt_injection_detected ?? 0 }}</dd></div>
            <div class="kv-row"><dt>{{ t('sessions.stats.piiDetected') }}</dt><dd>{{ overview?.compliance_stats?.pii_detected ?? 0 }}</dd></div>
          </dl>
        </div>
      </div>

      <SessionStatsSignals :pipeline="pipelineLatency" :errors="errorSummary" />

      <SessionStatsRankings
        v-if="overview"
        :top-clients="overview.top_clients || []"
        :top-tasks="overview.top_tasks || []"
        :model-usage="overview.model_usage || []"
        :trend-window="trendWindow"
      />
    </div>
  </div>
</template>

<style scoped>
.session-stats-panel { padding: 0 0 16px; width: 100%; }
.session-stats-panel--has-error { opacity: 0.6; pointer-events: none; }
.session-stats-panel--loading { opacity: 0.85; }
.panel-toolbar { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; flex-wrap: wrap; gap: 12px; }
/* 2026-11-23 紧凑轮：时间选择器 + 「包含探测」开关合成一组靠左，
   开关紧贴选择器右边（原先被 space-between 顶到页面中间，离控件很远）。 */
.panel-toolbar__start { display: inline-flex; align-items: center; gap: 12px; flex-wrap: wrap; min-width: 0; }
.panel-toolbar__end { display: inline-flex; align-items: center; gap: 12px; flex-shrink: 0; }
.meta-text { font-size: 12px; color: var(--muted, var(--text-secondary)); }
.probe-link {
  align-self: flex-start;
  margin-top: 4px;
  border: 0;
  background: transparent;
  padding: 0;
  font-size: 12px;
  color: var(--accent, var(--accent));
  cursor: pointer;
  text-decoration: underline;
}
.alert { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; padding: 12px 16px; margin-bottom: 16px; border-radius: 6px; }
.alert-danger { background: var(--danger-bg); border: 1px solid var(--danger-bd); color: var(--danger); }
.alert-warn { background: var(--warning-bg, var(--kx-warning-bg, var(--surface-secondary))); border: 1px solid var(--warning-bd, var(--kx-warning-border, var(--border))); color: var(--warning, var(--kx-warning, var(--text))); }
.alert-meta { font-size: 12px; opacity: 0.85; }
.alert-retry { padding: 4px 12px; border: 1px solid var(--danger-bd); border-radius: 4px; background: white; cursor: pointer; }
.secondary-kpi-row {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 16px; margin-bottom: 16px; width: 100%;
}
.mini-kpi {
  display: flex; align-items: center; gap: 12px; padding: 12px 16px; border-radius: 10px;
  border: 1px solid var(--border, var(--surface-secondary)); background: var(--card, var(--on-primary));
}
.mini-kpi__icon { width: 44px; height: 44px; border-radius: 10px; display: flex; align-items: center; justify-content: center; color: white; flex-shrink: 0; }
.mini-kpi__icon--new { background: linear-gradient(135deg, var(--accent), var(--purple)); }
.mini-kpi__icon--closed { background: linear-gradient(135deg, var(--muted), var(--muted)); }
.mini-kpi__icon--cost { background: linear-gradient(135deg, var(--success), var(--probe-cyan-light)); }
.mini-kpi__icon--compliance { background: linear-gradient(135deg, var(--pink), var(--warning)); }
.mini-kpi__label { font-size: 13px; color: var(--muted, var(--text-secondary)); }
.mini-kpi__value { font-size: 24px; font-weight: 600; }
.charts-row {
  display: grid; grid-template-columns: minmax(0, 2fr) minmax(0, 1fr);
  gap: 16px; margin-bottom: 16px; width: 100%;
}
@media (max-width: 1024px) { .charts-row { grid-template-columns: 1fr; } }
.detail-grid {
  display: grid; grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px; margin-bottom: 16px; width: 100%;
}
@media (max-width: 1024px) { .detail-grid { grid-template-columns: 1fr; } }
.detail-card {
  border: 1px solid var(--border, var(--surface-secondary)); border-radius: 10px;
  background: var(--card, var(--on-primary)); padding: 12px 16px; min-width: 0;
}
.detail-card__header { font-weight: 600; font-size: 14px; margin-bottom: 12px; }
.kv-list { margin: 0; }
.kv-row { display: flex; justify-content: space-between; gap: 12px; padding: 8px 0; border-bottom: 1px solid var(--border, var(--surface-secondary)); font-size: 13px; }
.kv-row:last-child { border-bottom: 0; }
.kv-row dt { color: var(--muted, var(--text-secondary)); }
.kv-row dd { margin: 0; font-weight: 500; }
.growth-tag { margin-left: 8px; font-size: 12px; }
.growth-tag--bad { color: var(--danger); }
.growth-tag--good { color: var(--success); }

/* ── 2026-11-21 统计口径对齐轮 ── */
.session-probe-scope {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  font-size: 12px;
  color: var(--text-secondary);
  white-space: nowrap;
  user-select: none;
}
.session-probe-scope input {
  accent-color: var(--primary);
  margin: 0;
}
.probe-scope-badge {
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 8px;
  background: var(--primary-bg, rgba(59, 130, 246, 0.12));
  color: var(--primary);
}
.aligned-row {
  margin-top: 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--surface-secondary);
  padding: 10px 12px;
}
.aligned-row--unavailable {
  color: var(--text-secondary);
  font-size: 12px;
}
.aligned-row__header {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 8px;
  flex-wrap: wrap;
}
.aligned-row__title {
  font-weight: 600;
  font-size: 13px;
}
.aligned-row__window {
  font-size: 12px;
  color: var(--text-secondary);
}
.aligned-row__source {
  font-size: 11px;
  color: var(--text-secondary);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 1px 6px;
}
.aligned-row__cards {
  display: grid;
  gap: 8px;
  grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
}
.aligned-cell__label {
  font-size: 11px;
  color: var(--text-secondary);
}
.aligned-cell__value {
  font-size: 15px;
  font-weight: 600;
}
.loading-indicator {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  padding: 24px;
  background: var(--surface-secondary, rgba(0, 0, 0, 0.02));
  border-radius: 8px;
  margin-bottom: 16px;
}
.loading-spinner {
  width: 20px;
  height: 20px;
  border: 2px solid var(--border, #ddd);
  border-top-color: var(--primary, #409eff);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}
@keyframes spin {
  to { transform: rotate(360deg); }
}
.loading-text {
  font-size: 14px;
  color: var(--text-secondary);
}
</style>
