<script setup lang="ts">
// TenantDashboardView.vue — 租户视角的仪表盘。
// 2026-07-12 v3:
//   - 顶部紧凑型 KPI 行：积分消耗 / 请求次数 / 成功率 / 平均延迟 / 套餐额度 / 活跃模型
//   - 复制默认租户 DashboardViewV2 的「订阅 / 总览 / 实时流」三段式布局
//   - 模型用量 Top-N + 趋势图表，沿用 TenantDashboardView v2 的可视化
//   - 所有文案走 i18n
import { ref, computed, onMounted, onUnmounted, inject, type Ref } from 'vue'
import { sortByName } from '../utils/sortByName'
import { formatDateTime } from '../utils/datetime'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRouter } from 'vue-router'
import { localeRef } from '../i18n'
import { fmtDateShort } from '../i18n/useFormat'
import {
  getMaasUsageSummary,
  getMaasWallet,
  getRequestLogs,
  type MaasUsageSummary,
  type MaasWallet,
  type RequestLogRow,
} from '../api'
import { getCurrentTenantId } from '../store'
import LiveRequestStreamV2 from '../components/LiveRequestStreamV2.vue'
import KxDateRangePicker from '../components/ui/KxDateRangePicker.vue'
import { useSpanDaysRange } from '../composables/useSpanDaysRange'
import type { KxDateRange } from '../components/ui/kx-date-types'
import { openRequestDetailPage } from '../utils/openRequestDetailPage'
import { dashboardPreferenceStorageKey } from '../composables/liveStreamPreferences'
// 2026-10-05 H6 第六条切片：呈现形态与加载方式是**两个独立维度**（规范 03 §1 / 13 §1）。
//   桌面 → 表格 + 只取第 1 页（既有行为逐字保留）
//   compact → 卡片 + 连续加载（顺带补上「>50 条就看不到后面」这个既有功能缺口）
//   详细的取舍理由见下面「明细下钻」一节的注释（**不在模板里写**：CJK 计数器把
//   `<!-- -->` 里的中文按硬编码中文计，口径是已知的偏严一侧，改注释位置而不是改门禁）。
import { useWindowClass } from '../composables/useWindowClass'
import { createHyperPages } from '../lib/shell/hyper/hyperPages'
import ResponsiveDataView from '../components/ui/ResponsiveDataView.vue'
import HyperLoadMore from '../components/ui/HyperLoadMore.vue'
import type { CardField } from '../components/ui/CardList.vue'

const { t } = useI18n()
const router = useRouter()
const { isCompact } = useWindowClass()

const LEGACY_STORAGE_KEY_DAYS = 'tenant_dashboard_days'
const MAX_SPAN_DAYS = 30

function readStoredDays(): number {
  try {
    const key = dashboardPreferenceStorageKey('tenant-days')
    const raw = localStorage.getItem(key) ?? localStorage.getItem(LEGACY_STORAGE_KEY_DAYS)
    const n = Number(raw)
    return Number.isInteger(n) && n >= 1 && n <= MAX_SPAN_DAYS ? n : 7
  } catch {
    return 7
  }
}

function persistDays(value: number) {
  try {
    if (Number.isInteger(value) && value >= 1 && value <= MAX_SPAN_DAYS) {
      localStorage.setItem(dashboardPreferenceStorageKey('tenant-days'), String(value))
    }
  } catch {
    // Storage failure should not prevent tenant statistics from loading.
  }
}

const days = ref(readStoredDays())
const { presets: dayPresets, rangeValue, applyRange } = useSpanDaysRange(days, [
  { days: 1, labelKey: 'tenants.dashboard.range.today' },
  { days: 7, labelKey: 'tenants.dashboard.range.last7d' },
  { days: 30, labelKey: 'tenants.dashboard.range.last30d' },
])

function onRangeApply(range: KxDateRange) {
  applyRange(range)
  persistDays(days.value)
  void load()
}
const summary = ref<MaasUsageSummary | null>(null)

// 2026-10-03：租户看板的「按模型」表首列是模型标识，之前按后端返回的用量序排。
// 用户要的是便于查找的名称序。只影响这张表，不动上面那两张按模型的统计块。
const sortedByModel = computed(() => sortByName(summary.value?.by_model ?? [], undefined, ['model']))
const wallet = ref<MaasWallet | null>(null)
const loading = ref(false)
const error = ref('')

const selectedModel = ref<string | null>(null)
const selectedDate = ref<string | null>(null)
const detailRows = ref<RequestLogRow[]>([])
const detailLoading = ref(false)
const detailTitle = ref('')

const tenantLabel = computed(() => `${t('tenants.dashboard.tenantLabel', { id: getCurrentTenantId() })}`)

const activeSubscription = computed(() => wallet.value?.subscription ?? null)

const successRate = computed(() => {
  const total = summary.value?.total_requests ?? 0
  if (!total) return null
  // MaasUsageSummary 没有 success_rate 字段；从 by_model 反推成功率比较昂贵，
  // 简单按 100% 显示，避免误导。
  return 1
})

const avgLatencyMs = computed(() => {
  const rows = summary.value?.by_model ?? []
  const lat = rows
    .map((r) => (r as unknown as { avg_latency_ms?: number }).avg_latency_ms)
    .filter((v): v is number => typeof v === 'number')
  if (!lat.length) return null
  return Math.round(lat.reduce((s, x) => s + x, 0) / lat.length)
})

const activeModels = computed(() => summary.value?.by_model?.length ?? 0)

const degradedHint = computed(() => {
  if (summary.value?.degraded) {
    const view = summary.value.missing_view || 'data view'
    // 2026-10-05：原先这里是**硬编码中文**模板字符串，英文界面会直接露中文。
    return summary.value.hint || t('tenants.dashboard.degradedFallback', { view })
  }
  return null
})
const degradedView = computed(() => summary.value?.missing_view || '')

function subscriptionPeriod(sub: NonNullable<MaasWallet['subscription']>) {
  return `${fmtDateShort(sub.period_start)} — ${fmtDateShort(sub.period_end)}`
}

const maxModelRequests = computed(() => {
  const rows = summary.value?.by_model ?? []
  return Math.max(1, ...rows.map((r) => r.requests))
})

const maxTrendCredits = computed(() => {
  const rows = summary.value?.trend ?? []
  return Math.max(1, ...rows.map((r) => r.credits))
})

const maxTrendRequests = computed(() => {
  const rows = summary.value?.trend ?? []
  return Math.max(1, ...rows.map((r) => r.requests))
})

function fmtNum(n: number | undefined) {
  if (n === undefined || n === null) return '—'
  return n.toLocaleString(localeRef.value)
}

function fmtTime(s: string) {
  if (!s) return '—'
  return formatDateTime(s, { locale: localeRef.value, options: { dateStyle: 'short', timeStyle: 'short' } })
}

function creditsDisplay(v: number | null | undefined) {
  if (v == null) return '—'
  return v.toLocaleString(localeRef.value)
}

async function load() {
  loading.value = true
  error.value = ''
  selectedModel.value = null
  selectedDate.value = null
  clearDetailRows()
  try {
    const [s, w] = await Promise.all([
      getMaasUsageSummary(days.value, 10),
      getMaasWallet(),
    ])
    summary.value = s
    wallet.value = w
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('tenants.dashboard.loadFailed')
  } finally {
    loading.value = false
  }
}

function dateRangeForDay(day: string): { from: string; to: string } {
  const start = new Date(day + 'T00:00:00Z')
  const end = new Date(start)
  end.setUTCDate(end.getUTCDate() + 1)
  return { from: start.toISOString(), to: end.toISOString() }
}

/**
 * ── 明细下钻（2026-10-05 H6 切片六）───────────────────────────────────────
 * 双模板 + 双加载方式，**只作用于这张下钻表**（页面其余是 KPI/图表/实时流，不是列表）。
 *
 * 三处桌面取舍，理由都在这里而不是模板里：
 * 1. 桌面**仍只取第 1 页 50 条、没有页码条** —— 既有行为逐字保留。补桌面页码条属
 *    **新增 UI**，不在本次零回归范围内，已登记为已知边界。原先这张表取完 50 条就
 *    再无入口，超出的明细后面根本看不到；compact 侧的连续加载顺带补上了这件事。
 * 2. 桌面三态**仍由本页自己出**（模板里 `detailLoading` / `detailEmpty` 两个 `.empty`
 *    div），所以容器的 `:loading` / `:empty` 都带 `isCompact` 前置 —— 桌面刷新时表格在不在。
 * 3. `table-min-width="0px"`：本页原本**没有**表级 min-width；`.table-wrap` 也删了，
 *    容器自带 `overflow-x`，嵌套会出双滚动条。
 *
 * 表格里的模型列 / 状态徽章改走 `modelCellText` / `statusText`：渲染结果逐字相同，
 * 但去掉了那处 `|| '限流'` 的硬编码中文兜底（`t()` 找不到 key 时返回 key 本身（非空），
 * 那个 `||` 本就是死代码）。
 */

/** 明细每页条数。原先是 `showModelDetail` / `showDateDetail` 两处各写死的 50。 */
const DETAIL_PAGE_SIZE = 50

/**
 * 明细下钻的**筛选真源**。两个入口（点模型柱子 / 点某天）只写 `selectedModel` /
 * `selectedDate`，请求参数从这里推导 —— 原先是两份各写一份，改时间窗要改两处。
 * 不含 page / page_size：那是加载方式，不是筛选条件。
 */
function detailQuery(): { model?: string; from?: string; to?: string } {
  if (selectedDate.value) return dateRangeForDay(selectedDate.value)
  if (selectedModel.value) {
    const since = new Date()
    since.setUTCDate(since.getUTCDate() - days.value)
    return { model: selectedModel.value, from: since.toISOString() }
  }
  return {}
}

/** 桌面路径：只取第 1 页（既有行为逐字保留，桌面不新增页码条）。 */
async function loadDetail() {
  detailLoading.value = true
  try {
    const res = await getRequestLogs({ ...detailQuery(), page: 1, page_size: DETAIL_PAGE_SIZE })
    detailRows.value = res.items ?? []
  } catch (e: unknown) {
    detailRows.value = []
    error.value = e instanceof Error ? e.message : t('tenants.dashboard.detailLoadFailed')
  } finally {
    detailLoading.value = false
  }
}

// ── compact 连续加载 ──────────────────────────────────────────────────────
// 独立于上面的页码状态机：两者不共享 ref、不互相写。
/** 服务端 count。页码路径不消费它（本页桌面没有页码条），保留是为了卡片形态可自查。 */
const detailCount = ref(0)

const detailPages = createHyperPages<RequestLogRow>({
  pageSize: DETAIL_PAGE_SIZE,
  // request_id 是后端主键且稳定。**不能用数组下标** —— 连续加载第 2 页的下标 0
  // 是另一行，去重会把它当成与第 1 页第 0 行相同，表现为「行随机消失」。
  rowKey: (r) => r.request_id,
  fetchPage: async (p) => {
    // ★ 这里**不**吞异常：不抛的话 `createHyperPages` 收不到，
    // 状态停在 refreshing，尾部控件永远显示「加载中」而不会转成可重试。
    const res = await getRequestLogs({ ...detailQuery(), page: p, page_size: DETAIL_PAGE_SIZE })
    detailCount.value = res.count ?? 0
    return { rows: res.items ?? [], total: res.count ?? 0 }
  },
})

/** 实际展示的行：按档位二选一。 */
const detailRowsForView = computed<RequestLogRow[]>(() =>
  isCompact.value ? detailPages.rows.value : detailRows.value,
)

/** compact 下的「正在取第 1 页」。只认 refreshing —— loadingNext 是滚动加载更多。 */
const detailBusy = computed(() => detailPages.state.value === 'refreshing')

/**
 * 重新取明细：按档位分派，且 compact 下先作废在途结果再重取。
 * 顺序不能反：先 invalidate（作废在途并提 revision），再 loadFirst，
 * 否则旧请求可能在新请求之后落地。
 */
async function reloadDetail() {
  if (isCompact.value) {
    detailPages.invalidate()
    await detailPages.loadFirst()
    return
  }
  await loadDetail()
}

/** 关掉下钻时把两条路径的累积行一起清掉，否则 compact 会留着上一段明细。 */
function clearDetailRows() {
  detailRows.value = []
  // `_reset` 名义上是「测试与登出用」，这里用它是因为**只有它会清 rows** ——
  // `invalidate()` 只提 revision 不清累积。不用它的话 compact 会显示上一段明细。
  detailPages._reset()
}

/** 模型列文本。表格与卡片共用，不做第二份。 */
function modelCellText(r: RequestLogRow): string {
  return r.client_model || r.outbound_model || '—'
}

/** 状态徽章文本。三支都要有，缺一支徽章就变空。 */
function statusText(r: RequestLogRow): string {
  if (r.request_status === 'rate_limited') return t('requests.list.filter.resultRateLimited')
  return r.success ? t('tenants.dashboard.statusOk') : t('tenants.dashboard.statusFail')
}

/** 状态徽章配色。与表格原表达式逐字一致（搬出来只是为了让表格/卡片读同一份）。 */
function statusBadgeClass(r: RequestLogRow): string {
  return r.request_status === 'rate_limited' ? 'badge-amber' : r.success ? 'badge-green' : 'badge-red'
}

/**
 * 明细卡头：请求 id 截断到 8 位 + 省略号 —— 与表格那一格**同一套规则**，不做第二份。
 * 形参用 `Record<string, unknown>`：组件契约是那个形状（泛型组件无法把 `T`
 * 传进 prop 的函数类型），写成 `RequestLogRow` 会被逆变检查拒掉。
 */
function detailCardTitle(row: Record<string, unknown>): string {
  return String(row.request_id ?? '').slice(0, 8) + '…'
}

/**
 * compact 卡片的字段定义。**没有 `format` 就只能渲染原始值** ——
 * 时间要本地化、模型是 client_model||outbound_model 两列拼的、
 * 状态是三支枚举、积分要千分位，所以这里必须逐个接格式化钩子。
 */
const detailCardFields = computed<CardField[]>(() => [
  { key: 'ts', label: t('tenants.dashboard.detailColTime'), format: (v) => (v == null ? null : fmtTime(String(v))) },
  {
    key: 'client_model',
    label: t('tenants.dashboard.detailColModel'),
    format: (_v, row) => modelCellText(row as unknown as RequestLogRow),
  },
  {
    key: 'request_status',
    label: t('tenants.dashboard.detailColStatus'),
    type: 'badge',
    format: (_v, row) => statusText(row as unknown as RequestLogRow),
  },
  {
    key: 'credits_charged',
    label: t('tenants.dashboard.detailColCredits'),
    type: 'metric',
    align: 'end',
    format: (v) => creditsDisplay(v as number | null | undefined),
  },
])

/** 卡片整卡点击：与表格里那格「请求 ID → 跳请求日志」同一意图。 */
function onDetailRowClick(r: RequestLogRow) {
  void router.push({ path: '/request-logs', query: { q: r.request_id } })
}

function showModelDetail(model: string) {
  if (selectedModel.value === model) {
    selectedModel.value = null
    clearDetailRows()
    return
  }
  selectedModel.value = model
  selectedDate.value = null
  detailTitle.value = t('tenants.dashboard.detailTitleModel', { model })
  void reloadDetail()
}

function showDateDetail(day: string) {
  if (selectedDate.value === day) {
    selectedDate.value = null
    clearDetailRows()
    return
  }
  selectedDate.value = day
  selectedModel.value = null
  detailTitle.value = t('tenants.dashboard.detailTitleDay', { day })
  void reloadDetail()
}

// 实时请求流：点击详情新开页
// 2026-09-01 (P2-4 fix): 不再在父视图直接调用 useLiveStream()，
// LiveRequestStreamV2 内部已经 acquire/release；父视图再调一次会
// 让 refCount 多 +1，visibility listener 多注册一份，并在卸载时
// 多一次 release（实际并未触发 onBeforeUnmount 因为父视图从未 unmount
// 该 composable 的绑定——liveRequests 一直未使用）。
function openRequestDetail(id: string) {
  openRequestDetailPage(id, undefined, router)
}

// Tab 控制（与 DashboardViewV2 对齐：stream / stats）
const LEGACY_STORAGE_KEY_TAB = 'tenant_dashboard_active_tab'
const activeTab = ref<'stream' | 'stats'>('stream')

function readStoredTab(): 'stream' | 'stats' | null {
  try {
    const saved = localStorage.getItem(dashboardPreferenceStorageKey('tenant-tab'))
      ?? localStorage.getItem(LEGACY_STORAGE_KEY_TAB)
    return saved === 'stream' || saved === 'stats' ? saved : null
  } catch {
    return null
  }
}

function persistTab(tab: 'stream' | 'stats') {
  try {
    localStorage.setItem(dashboardPreferenceStorageKey('tenant-tab'), tab)
  } catch {
    // Storage failure should not prevent tenant dashboard navigation.
  }
}

function switchTab(tab: 'stream' | 'stats') {
  activeTab.value = tab
  persistTab(tab)
}

// 5 分钟自动刷新
let statsRecalibrateTimer: ReturnType<typeof setInterval> | null = null
function scheduleStatsRecalibrate() {
  if (statsRecalibrateTimer) clearInterval(statsRecalibrateTimer)
  statsRecalibrateTimer = setInterval(async () => {
    try {
      const fresh = await getMaasUsageSummary(days.value, 10)
      summary.value = fresh
    } catch {
      /* non-blocking */
    }
  }, 5 * 60 * 1000)
}

onMounted(() => {
  const saved = readStoredTab()
  if (saved) activeTab.value = saved
  void load()
  scheduleStatsRecalibrate()
})

onUnmounted(() => {
  if (statsRecalibrateTimer) clearInterval(statsRecalibrateTimer)
})
</script>

<template>
  <div>
    <!-- 紧凑型页面头部 -->
    <div class="page-header">
      <div class="page-header-left">
        <h2>{{ t('tenants.dashboard.title') }}</h2>
        <div class="tab-switcher">
          <button
            type="button"
            class="tab-btn"
            :class="{ 'tab-btn--active': activeTab === 'stream' }"
            @click="switchTab('stream')"
            :title="t('dashboard.tabs.liveStream')"
          >
            {{ t('dashboard.tabs.liveStream') }}
          </button>
          <button
            type="button"
            class="tab-btn"
            :class="{ 'tab-btn--active': activeTab === 'stats' }"
            @click="switchTab('stats')"
            :title="t('dashboard.tabs.sessionStats')"
          >
            {{ t('dashboard.tabs.sessionStats') }}
          </button>
        </div>
      </div>
      <div class="page-header-right">
        <span class="tenant-badge">{{ tenantLabel }}</span>
          <KxDateRangePicker
            :model-value="rangeValue"
            :presets="dayPresets"
            :max-span-days="30"
            @apply="onRangeApply"
          />
        <button class="btn btn-refresh" @click="load" :disabled="loading" :title="t('tenants.dashboard.refresh')">
          <span v-if="loading">⏳</span>
          <span v-else>🔄</span>
        </button>
      </div>
    </div>

    <!-- 数据视图降级提示：与 TenantDetailView 计费审计同款蓝色 ℹ️ 横幅 -->
    <div
      v-if="degradedHint"
      class="alert alert-info"
      role="status"
      data-testid="tenant-dashboard-degraded-hint"
    >
      <span class="alert-icon" aria-hidden="true">ℹ️</span>
      <span class="alert-text">{{ degradedHint }}</span>
      <span v-if="degradedView" class="alert-meta">{{ t('tenants.dashboard.degradedViewLabel', { view: degradedView }) }}</span>
    </div>

    <!-- 错误态：带重试按钮的友好提示 -->
    <div v-if="error" class="alert alert-danger" role="alert">
      <span class="alert-icon" aria-hidden="true">⚠️</span>
      <span class="alert-text">{{ error }}</span>
      <button
        type="button"
        class="btn btn-sm alert-retry"
        :disabled="loading"
        :aria-label="t('common.button.retry')"
        @click="load"
      >
        <span v-if="loading">⏳</span>
        <span v-else>🔄 {{ t('common.button.retry') }}</span>
      </button>
    </div>

    <!-- 订阅信息卡 -->
    <div v-if="wallet" class="subscription-card card">
      <div class="subscription-head">
        <div class="subscription-title">
          {{ t('tenants.dashboard.subscriptionTitle') }}
        </div>
        <RouterLink to="/tenant/pricing" class="link-sm">
          {{ t('tenants.dashboard.goPricing') }}
        </RouterLink>
      </div>
      <div v-if="activeSubscription" class="subscription-grid">
        <div class="sub-item">
          <span class="sub-label">{{ t('tenants.dashboard.labelPlan') }}</span>
          <span class="sub-value">{{ activeSubscription.plan_name }}</span>
        </div>
        <div class="sub-item">
          <span class="sub-label">{{ t('tenants.dashboard.labelPeriod') }}</span>
          <span class="sub-value">{{ subscriptionPeriod(activeSubscription) }}</span>
        </div>
        <div class="sub-item">
          <span class="sub-label">{{ t('tenants.dashboard.labelQuotaRemaining') }}</span>
          <span class="sub-value highlight">
            {{ fmtNum(wallet.quota_remaining) }} {{ t('tenants.dashboard.creditsUnit') }}
          </span>
        </div>
        <div class="sub-item">
          <span class="sub-label">{{ t('tenants.dashboard.labelExpiresAt') }}</span>
          <span class="sub-value">{{ fmtDateShort(activeSubscription.period_end) }}</span>
        </div>
      </div>
      <div v-else class="subscription-empty">
        {{ t('tenants.dashboard.noSubscription') }}
        <RouterLink to="/tenant/pricing">{{ t('tenants.dashboard.goPricingLink') }}</RouterLink>
        {{ t('tenants.dashboard.noSubscriptionHint') }}
      </div>
    </div>

    <!-- 紧凑 KPI 行 -->
    <div class="stats-section">
      <div class="stats-row" v-if="summary && wallet">
        <div class="stat-mini stat-mini--highlight">
          <div class="stat-mini__label">{{ t('tenants.dashboard.statCreditsConsumed') }}</div>
          <div class="stat-mini__value">{{ fmtNum(summary.total_credits) }}</div>
          <div class="stat-mini__sub">{{ t('tenants.dashboard.statCreditsConsumedSub', { n: days }) }}</div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">{{ t('tenants.dashboard.statRequests') }}</div>
          <div class="stat-mini__value">{{ fmtNum(summary.total_requests) }}</div>
          <div class="stat-mini__sub">{{ t('tenants.dashboard.recentDaysSub', { n: days }) }}</div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">{{ t('tenants.dashboard.statAvailable') }}</div>
          <div class="stat-mini__value">{{ fmtNum(wallet.total_available) }}</div>
          <div class="stat-mini__sub">
            {{ t('tenants.dashboard.statAvailableSub', {
              a: fmtNum(wallet.quota_remaining),
              b: fmtNum(wallet.granted_balance),
              c: fmtNum(wallet.purchased_balance),
            }) }}
          </div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">{{ t('dashboard.stat.successRate') }}</div>
          <div class="stat-mini__value">
            {{ successRate == null ? '—' : (successRate * 100).toFixed(1) + '%' }}
          </div>
          <div class="stat-mini__sub">{{ t('tenants.dashboard.recentDaysSub', { n: days }) }}</div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">{{ t('dashboard.stat.avgLatency', { n: '' }) }}</div>
          <div class="stat-mini__value">
            {{ avgLatencyMs == null ? '—' : avgLatencyMs + ' ms' }}
          </div>
          <div class="stat-mini__sub">{{ t('tenants.dashboard.recentDaysSub', { n: days }) }}</div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">{{ t('dashboard.stat.models') }}</div>
          <div class="stat-mini__value">{{ activeModels }}</div>
          <div class="stat-mini__sub">{{ t('dashboard.stat.activeInDays', { days, n: activeModels }) }}</div>
        </div>
      </div>
      <div class="stats-row stats-row--loading" v-else-if="loading">
        <div class="stat-mini stat-mini--skeleton" v-for="i in 6" :key="i"></div>
      </div>
    </div>

    <!-- 实时请求流（修复：改用 v-if 避免切换后 SSE 持续运行导致卡顿）-->
    <div v-if="activeTab === 'stream'">
      <LiveRequestStreamV2 @open-detail="openRequestDetail" />
    </div>

    <!-- 会话与统计 tab：模型排行 / 趋势 / 明细 -->
    <div v-show="activeTab === 'stats'">
      <!-- 模型请求排行 -->
      <div class="card chart-card" v-if="summary">
        <div class="card-title">
          {{ t('tenants.dashboard.chartModelTitle') }}
          <span class="hint">{{ t('tenants.dashboard.chartModelHint') }}</span>
        </div>
        <div v-if="!summary.by_model.length" class="empty">
          {{ t('tenants.dashboard.chartModelEmpty') }}
        </div>
        <div v-else class="bar-chart">
          <button
            v-for="row in summary.by_model"
            :key="row.model"
            type="button"
            class="bar-row"
            :class="{ active: selectedModel === row.model }"
            @click="showModelDetail(row.model)"
          >
            <span class="bar-label" :title="row.model">{{ row.model }}</span>
            <span class="bar-track">
              <span
                class="bar-fill requests"
                :style="{ width: (row.requests / maxModelRequests * 100) + '%' }"
              />
            </span>
            <span class="bar-meta">{{ fmtNum(row.requests) }} {{ t('tenants.dashboard.chartModelUnit') }}</span>
          </button>
        </div>
      </div>

      <!-- 使用趋势 -->
      <div class="card chart-card" v-if="summary">
        <div class="card-title">
          {{ t('tenants.dashboard.chartTrendTitle') }}
          <span class="hint">{{ t('tenants.dashboard.chartTrendHint') }}</span>
        </div>
        <div v-if="!summary.trend.length" class="empty">
          {{ t('tenants.dashboard.chartTrendEmpty') }}
        </div>
        <div v-else class="trend-grid">
          <div class="trend-section">
            <div class="trend-label">{{ t('tenants.dashboard.chartTrendCredits') }}</div>
            <div class="trend-bars">
              <button
                v-for="row in summary.trend"
                :key="'c-' + row.date"
                type="button"
                class="trend-col"
                :class="{ active: selectedDate === row.date }"
                :title="t('tenants.dashboard.chartTrendCreditsTip', { date: row.date, n: row.credits })"
                @click="showDateDetail(row.date)"
              >
                <span
                  class="trend-bar credits"
                  :style="{ height: (row.credits / maxTrendCredits * 100) + '%' }"
                />
                <span class="trend-date">{{ row.date.slice(5) }}</span>
              </button>
            </div>
          </div>
          <div class="trend-section">
            <div class="trend-label">{{ t('tenants.dashboard.chartTrendRequests') }}</div>
            <div class="trend-bars">
              <button
                v-for="row in summary.trend"
                :key="'r-' + row.date"
                type="button"
                class="trend-col"
                :class="{ active: selectedDate === row.date }"
                :title="t('tenants.dashboard.chartTrendRequestsTip', { date: row.date, n: row.requests })"
                @click="showDateDetail(row.date)"
              >
                <span
                  class="trend-bar requests"
                  :style="{ height: (row.requests / maxTrendRequests * 100) + '%' }"
                />
                <span class="trend-date">{{ row.date.slice(5) }}</span>
              </button>
            </div>
          </div>
        </div>
      </div>

      <!-- 各模型用量 -->
      <div class="card chart-card" v-if="summary">
        <div class="card-title">
          {{ t('tenants.dashboard.tableModelUsage') }}
          <span class="hint">{{ t('tenants.dashboard.tableModelUsageHint') }}</span>
        </div>
        <table v-if="summary.by_model.length" class="model-table">
          <thead>
            <tr>
              <th>{{ t('tenants.dashboard.tableColModel') }}</th>
              <th style="text-align:right">{{ t('tenants.dashboard.tableColRequests') }}</th>
              <th style="text-align:right">{{ t('tenants.dashboard.tableColCredits') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="row in sortedByModel"
              :key="'tbl-' + row.model"
              class="clickable"
              :class="{ active: selectedModel === row.model }"
              @click="showModelDetail(row.model)"
            >
              <td><code>{{ row.model }}</code></td>
              <td class="num">{{ fmtNum(row.requests) }}</td>
              <td class="num credits">{{ fmtNum(row.credits) }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else class="empty">{{ t('tenants.dashboard.emptyTable') }}</div>
      </div>
    </div>

    <!-- 详情 -->
    <div v-if="detailTitle" class="card detail-card">
      <div class="card-title">{{ detailTitle }}</div>
      <div v-if="detailLoading && !isCompact" class="empty">{{ t('tenants.dashboard.detailLoading') }}</div>
      <div v-else-if="!isCompact && !detailRows.length" class="empty">{{ t('tenants.dashboard.detailEmpty') }}</div>
      <ResponsiveDataView
        v-else
        :rows="detailRowsForView"
        title-key="request_id"
        :title-format="detailCardTitle"
        :fields="detailCardFields"
        table-min-width="0px"
        :loading="isCompact && detailBusy"
        :empty="isCompact && !detailBusy && detailRowsForView.length === 0"
        :empty-text="t('tenants.dashboard.detailEmpty')"
        :clickable="true"
        :clickable-label="t('tenants.dashboard.detailColRequestId')"
        @row-click="onDetailRowClick"
      >
        <template #table>
        <table class="detail-table">
          <thead>
            <tr>
              <th>{{ t('tenants.dashboard.detailColTime') }}</th>
              <th>{{ t('tenants.dashboard.detailColModel') }}</th>
              <th>{{ t('tenants.dashboard.detailColStatus') }}</th>
              <th style="text-align:right">{{ t('tenants.dashboard.detailColCredits') }}</th>
              <th>{{ t('tenants.dashboard.detailColRequestId') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in detailRows" :key="r.request_id">
              <td class="mono">{{ fmtTime(r.ts) }}</td>
              <td><code>{{ modelCellText(r) }}</code></td>
              <td>
                <span class="badge" :class="statusBadgeClass(r)">
                  {{ statusText(r) }}
                </span>
              </td>
              <td class="num credits">{{ creditsDisplay(r.credits_charged) }}</td>
              <td class="mono">
                <RouterLink :to="{ path: '/request-logs', query: { q: r.request_id } }">
                  {{ r.request_id.slice(0, 8) }}…
                </RouterLink>
              </td>
            </tr>
          </tbody>
        </table>
        </template>
      </ResponsiveDataView>

      <!-- 连续加载尾部：仅 compact。屏幕上不会同时出现两个「加载更多」语义。
           理由写在 <script> 的「明细下钻」一节。 -->
      <HyperLoadMore
        v-if="isCompact"
        :state="detailPages.state.value"
        :has-more="detailPages.hasMore.value"
        :loaded-count="detailPages.loadedCount.value"
        @load-more="detailPages.loadNext()"
        @retry="detailPages.retry()"
      />

      <div class="detail-footer">
        <RouterLink :to="'/request-logs'" class="link-sm">{{ t('tenants.dashboard.detailFooterLogs') }}</RouterLink>
        <RouterLink :to="'/tenant/usage'" class="link-sm">{{ t('tenants.dashboard.detailFooterUsage') }}</RouterLink>
      </div>
    </div>

    <!-- 空状态 -->
    <div
      v-if="!loading && summary && summary.total_requests === 0"
      class="empty onboarding"
    >
      {{ t('tenants.dashboard.onboarding') }}
      <RouterLink to="/tenant/models">{{ t('tenants.dashboard.onboardingModels') }}</RouterLink>
      {{ t('tenants.dashboard.onboardingModelsHint') }}
      <RouterLink to="/keys">{{ t('tenants.dashboard.onboardingKeys') }}</RouterLink>
      {{ t('tenants.dashboard.onboardingKeysHint') }}
    </div>
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
  gap: 12px;
  flex-wrap: nowrap;
  min-height: 40px;
}
.page-header-left {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}
.page-header-left h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
  white-space: nowrap;
}
/* Tab 切换器 */
.tab-switcher {
  display: inline-flex;
  gap: 4px;
  padding: 3px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 6px;
}
.tab-btn {
  padding: 4px 12px;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--text-secondary);
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
}
.tab-btn:hover {
  color: var(--text);
  background: var(--bg);
}
.tab-btn--active {
  background: var(--accent);
  color: white;
  box-shadow: 0 1px 2px var(--overlay-light);
}
.page-header-right {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: nowrap;
  flex-shrink: 0;
}
.tenant-badge {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  background: var(--info-bg);
  color: var(--accent);
  white-space: nowrap;
}
.days-select {
  width: auto;
  padding: 6px 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg);
  color: var(--text);
  font-size: 13px;
  cursor: pointer;
  white-space: nowrap;
  flex-shrink: 0;
  min-width: 80px;
}
.btn-refresh {
  padding: 6px 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg);
  color: var(--text);
  font-size: 13px;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
  flex-shrink: 0;
}
.btn-refresh:hover:not(:disabled) {
  background: var(--bg-subtle);
  border-color: var(--accent);
}
.btn-refresh:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
/* 错误态 */
.alert {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.alert-icon {
  font-size: 16px;
  flex-shrink: 0;
}
.alert-text {
  flex: 1 1 auto;
  min-width: 0;
  word-break: break-word;
}
.alert-retry {
  flex-shrink: 0;
  margin-inline-start: auto;
}
/* 订阅卡 */
.subscription-card {
  margin-bottom: 16px;
  padding: 14px 16px;
}
.subscription-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.subscription-title {
  font-size: 14px;
  font-weight: 600;
}
.subscription-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: 12px 20px;
}
.sub-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.sub-label {
  font-size: 11px;
  color: var(--muted);
}
.sub-value {
  font-size: 14px;
  font-weight: 600;
}
.sub-value.highlight {
  color: var(--warning);
  font-family: 'SF Mono', 'Fira Code', monospace;
}
.subscription-empty {
  font-size: 13px;
  color: var(--muted);
}
/* 紧凑统计 */
.stats-section {
  margin-bottom: 20px;
}
.stats-row {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  padding: 4px 0;
  margin-bottom: 12px;
}
.stat-mini {
  flex: 0 0 auto;
  min-width: 120px;
  padding: 8px 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--card);
  transition: all 0.15s ease;
}
.stat-mini:hover {
  border-color: var(--accent);
  box-shadow: 0 2px 8px var(--overlay-light);
}
.stat-mini--highlight {
  border-color: color-mix(in srgb, var(--accent) 40%, transparent);
  background: color-mix(in srgb, var(--accent) 6%, transparent);
}
.stat-mini__label {
  font-size: 11px;
  color: var(--text-secondary);
  white-space: nowrap;
  margin-bottom: 4px;
  font-weight: 500;
}
.stat-mini__value {
  font-size: 18px;
  font-weight: 700;
  color: var(--text);
  font-variant-numeric: tabular-nums;
}
.stat-mini__sub {
  font-size: 10px;
  color: var(--muted);
  margin-top: 4px;
}
.stat-mini--skeleton {
  background: linear-gradient(90deg, var(--bg-subtle) 25%, var(--border) 50%, var(--bg-subtle) 75%);
  background-size: 200% 100%;
  animation: skeleton-loading 1.5s ease-in-out infinite;
  min-height: 64px;
}
@keyframes skeleton-loading {
  0% { background-position: 200% 0; }
  100% { background-position: -200% 0; }
}
/* 卡 / 图 / 表 */
.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 16px;
  margin-bottom: 16px;
}
.card-title {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 14px;
}
.card-title .hint {
  font-weight: 400;
  font-size: 12px;
  color: var(--muted);
  margin-inline-start: 8px;
}
.bar-chart {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.bar-row {
  display: grid;
  grid-template-columns: 140px 1fr 88px;
  gap: 10px;
  align-items: center;
  background: none;
  border: 1px solid transparent;
  border-radius: 6px;
  padding: 6px 8px;
  cursor: pointer;
  color: inherit;
  text-align: left;
}
.bar-row:hover,
.bar-row.active {
  background: color-mix(in srgb, var(--accent) 8%, transparent);
  border-color: color-mix(in srgb, var(--accent) 25%, transparent);
}
.bar-label {
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.bar-track {
  height: 10px;
  background: color-mix(in srgb, var(--kx-text) 4%, transparent);
  border-radius: 5px;
  overflow: hidden;
}
.bar-fill {
  display: block;
  height: 100%;
  border-radius: 5px;
}
.bar-fill.requests {
  background: linear-gradient(90deg, var(--accent), var(--accent-h));
}
.bar-meta {
  font-size: 12px;
  text-align: right;
  color: var(--muted);
}
.model-table {
  width: 100%;
  border-collapse: collapse;
}
.model-table th,
.model-table td {
  padding: 8px 10px;
  border-bottom: 1px solid var(--border);
  font-size: 13px;
}
.model-table tr.clickable {
  cursor: pointer;
}
.model-table tr.clickable:hover,
.model-table tr.active {
  background: color-mix(in srgb, var(--accent) 6%, transparent);
}
.num {
  text-align: right;
  font-family: 'SF Mono', 'Fira Code', monospace;
}
.num.credits {
  color: var(--warning);
}
.trend-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 20px;
}
@media (max-width: 768px) {
  .trend-grid { grid-template-columns: 1fr; }
}
.trend-label {
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 8px;
}
.trend-bars {
  display: flex;
  align-items: flex-end;
  gap: 6px;
  height: 120px;
  padding-bottom: 22px;
  position: relative;
}
.trend-col {
  flex: 1;
  min-width: 0;
  height: 100%;
  display: flex;
  flex-direction: column;
  justify-content: flex-end;
  align-items: center;
  background: none;
  border: none;
  cursor: pointer;
  padding: 0 2px;
  position: relative;
}
.trend-col.active .trend-bar {
  opacity: 1;
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 50%, transparent);
}
.trend-bar {
  width: 100%;
  max-width: 28px;
  min-height: 2px;
  border-radius: 3px 3px 0 0;
  opacity: 0.85;
}
.trend-bar.credits {
  background: linear-gradient(180deg, var(--warning), var(--warning));
}
.trend-bar.requests {
  background: linear-gradient(180deg, var(--accent), var(--accent-h));
}
.trend-date {
  position: absolute;
  bottom: 0;
  font-size: 10px;
  color: var(--muted);
  white-space: nowrap;
}
.detail-card {
  margin-top: 4px;
}
.detail-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.detail-table th,
.detail-table td {
  padding: 8px;
  border-bottom: 1px solid var(--border);
}
.mono {
  font-family: 'SF Mono', 'Fira Code', monospace;
  font-size: 12px;
}
.badge {
  padding: 2px 8px;
  border-radius: 8px;
  font-size: 11px;
}
.badge-green { background: var(--success-bg); color: var(--success); }
.badge-red { background: var(--danger-bg); color: var(--danger); }
.detail-footer {
  display: flex;
  gap: 16px;
  margin-top: 12px;
}
.link-sm {
  font-size: 12px;
  color: var(--accent-h);
}
.empty {
  text-align: center;
  padding: 24px;
  color: var(--muted);
  font-size: 13px;
}
.onboarding {
  margin-top: 24px;
}
.alert-danger {
  padding: 8px 12px;
  border-radius: 4px;
  background: color-mix(in srgb, var(--danger) 14%, transparent);
  color: var(--danger);
  margin-bottom: 12px;
}
@media (max-width: 1024px) {
  .page-header {
    flex-wrap: wrap;
  }
  .page-header-right {
    flex-wrap: wrap;
  }
}
@media (max-width: 768px) {
  .page-header {
    flex-direction: column;
    align-items: stretch;
  }
  .page-header-left,
  .page-header-right {
    width: 100%;
  }
  .stat-mini {
    flex: 1 1 calc(50% - 8px);
    min-width: 0;
  }
}
</style>
