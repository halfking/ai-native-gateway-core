<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { fmtDateCompact } from '../i18n/useFormat'
import { ref, computed, watch, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import {
  getCompressionStats,
  getCompressionSessions,
  type CompressionStats,
  type CompressionSessionItem,
} from '../api'
import { getSetting } from '../api/settings'
import { parseLocalMinute } from '../utils/datetime'
import KxDateRangePicker from '../components/ui/KxDateRangePicker.vue'
import type { KxDateRange } from '../components/ui/kx-date-types'
// 2026-10-05 H6 第五条切片：呈现形态与加载方式是**两个独立维度**（规范 03 §1 / 13 §1）。
//   桌面 → 表格 + 页码（既有表格、策略徽章、行样式与页码条逐字保留）
//   compact → 卡片 + 连续加载
import { useWindowClass } from '../composables/useWindowClass'
import { createHyperPages } from '../lib/shell/hyper/hyperPages'
import ResponsiveDataView from '../components/ui/ResponsiveDataView.vue'
import HyperLoadMore from '../components/ui/HyperLoadMore.vue'
import type { CardField } from '../components/ui/CardList.vue'

const { t } = useI18n()
const { isCompact } = useWindowClass()


const router = useRouter()

const loading = ref(false)
const stats = ref<CompressionStats | null>(null)
const sessions = ref<CompressionSessionItem[]>([])
const sessionsCount = ref(0)
const sessionsLoading = ref(false)

// 2026-10-03：三处静默吞错的代价是页面在说假话。
//   · loadStats 失败   → stats-row 整块不渲染（v-if="stats"），
//                        页面看起来像「这段时间没有任何压缩」
//   · loadSessions 失败 → empty-hint 显示「没有会话」，
//                        这是**直接的事实错误**：不是没有，是没查到
//   · loadCurrentConfig 失败 → 配置条停在硬编码默认值，
//                        把默认值当成服务端当前配置讲出去
// 三者互相独立，所以分开记；成功时各自清空。
const statsError = ref('')
const sessionsError = ref('')
const configError = ref('')

/** 合并成一句横幅：哪几块没查到要说清楚，不能只说「加载失败」。 */
const loadError = computed(() =>
  [statsError.value, sessionsErrorText.value, configError.value].filter(Boolean).join('；'),
)

// Current compression configuration (read-only chips), kept in sync with
// the editable copy in Session Configuration → Compression.
const showCurrentConfig = ref(false)
const currentConfig = ref<{
  enabled: boolean
  mode: string
  window: number
  model: string
}>({ enabled: true, mode: 'smart', window: 0.8, model: '' })

type TabId = '24h' | '7d' | '30d' | 'custom'
const activeTab = ref<TabId>('24h')
const customFrom = ref('')
const customTo = ref('')
const showCustom = ref(false)

const sessionPage = ref(1)
const sessionPageSize = 50

const displayHours = computed(() => {
  switch (activeTab.value) {
    case '24h': return 24
    case '7d': return 168
    case '30d': return 720
    default: return undefined
  }
})

const totalPages = computed(() => Math.ceil(sessionsCount.value / sessionPageSize) || 1)

/**
 * 时间区间的**唯一真源**：三个调用方（统计 / 页码路径的列表 / 连续加载）都从这里取。
 * 原本 `loadStats` 与 `loadSessions` 各写了一份一模一样的 if/else ——
 * 改 custom 分支要改两处，漏改的症状是「统计按 24h 算、列表按自定义区间取」。
 *
 * 不含 page / page_size：那是加载方式，不是筛选条件。
 */
function timeRangeParams(): { hours?: number; from?: string; to?: string } {
  const params: { hours?: number; from?: string; to?: string } = {}
  if (activeTab.value === 'custom') {
    // 'YYYY-MM-DDTHH:mm' 原样透传 —— 本页与 API 的既有契约（见下方 KxDateRangePicker 注释）
    if (customFrom.value) params.from = customFrom.value
    if (customTo.value) params.to = customTo.value
  } else {
    params.hours = displayHours.value
  }
  return params
}

async function loadStats() {
  loading.value = true
  try {
    stats.value = await getCompressionStats(timeRangeParams())
    statsError.value = ''
  } catch (e: unknown) {
    // 原来只写 `// non-blocking`：面板整块消失，页面与「真的没有数据」同形。
    statsError.value = e instanceof Error && e.message
      ? e.message
      : t('compression.load.statsFailed')
  } finally {
    loading.value = false
  }
}

/** 桌面页码路径。表格与分页条的既有行为逐字保留。 */
async function loadSessions() {
  sessionsLoading.value = true
  try {
    const resp = await getCompressionSessions({
      ...timeRangeParams(),
      page: sessionPage.value,
      page_size: sessionPageSize,
    })
    sessions.value = resp.items
    sessionsCount.value = resp.count
    sessionsError.value = ''
  } catch (e: unknown) {
    // 原来只写 `// non-blocking`：随后渲染的是「没有会话」这句事实陈述。
    sessionsError.value = e instanceof Error && e.message
      ? e.message
      : t('compression.load.sessionsFailed')
  } finally {
    sessionsLoading.value = false
  }
}

// ── compact 连续加载 ──────────────────────────────────────────────────────
// 独立于上面的页码状态机：两者不共享 ref、不互相写。
/** 服务端 count。页码路径有自己的 `sessionsCount` ref，两条路径不共享。 */
const continuousCount = ref(0)
const continuous = createHyperPages<CompressionSessionItem>({
  pageSize: sessionPageSize,
  // gw_session_id 是后端主键且稳定（表格的 :key 也是它）。
  // **不能用数组下标** —— 连续加载第 2 页的下标 0 是另一行。
  rowKey: (s) => s.gw_session_id,
  fetchPage: async (p) => {
    // ★ 这里**不**吞异常：吞了 `createHyperPages` 收不到，状态会停在 refreshing，
    // 尾部控件永远显示「加载中」而不会转成可重试。页码路径的 `loadSessions`
    // 保持原有的「non-blocking」静默 —— 两条路径的错误呈现本就不同档。
    const resp = await getCompressionSessions({
      ...timeRangeParams(),
      page: p,
      page_size: sessionPageSize,
    })
    continuousCount.value = resp.count || 0
    return { rows: resp.items || [], total: resp.count || 0 }
  },
})

/** compact 失败态：容器此时不得宣称「空」（那是把「不知道」讲成「知道」），
 *  错误呈现交给 Hyper 底栏的可重试提示；横幅侧由 sessionsErrorText 折算入列。 */
const continuousFailed = computed(() => continuous.state.value === 'failed')

/** 会话块的失败文案，两条加载路径共用一个出口：
 *  桌面页码路径写 sessionsError；compact 连续路径不写它（Hyper 底栏自会呈现），
 *  但横幅契约要求三块失败都入列，这里折算成同一句。 */
const sessionsErrorText = computed(() =>
  sessionsError.value
    || (continuousFailed.value ? t('compression.load.sessionsFailed') : ''),
)

/** 实际展示的行：按档位二选一。 */
const rows = computed<CompressionSessionItem[]>(() =>
  isCompact.value ? continuous.rows.value : sessions.value,
)

/** 卡片计数条：桌面取页码路径的 count，compact 取连续加载带回来的服务端 count。 */
const countForBadge = computed(() => (isCompact.value ? continuousCount.value : sessionsCount.value))

/**
 * 卡头：会话 id 必须**截断**。`gw_session_id` 是完整长串，直接当标题会把整张卡撑爆。
 * 截断规则与表格单元格共用 `shortID`，不做第二份。
 *
 * 形参用 `Record<string, unknown>`：组件契约是那个形状（泛型组件无法把 `T`
 * 传进 prop 的函数类型），写成 `CompressionSessionItem` 会被逆变检查拒掉。
 */
function cardTitle(row: Record<string, unknown>): string {
  return shortID(String(row.gw_session_id ?? ''))
}

/**
 * compact 卡片的字段定义。**没有 `format` 就只能渲染原始值** ——
 * 策略是枚举代码（要译名）、token 要千分位缩写、节省数要带负号且 0 时出破折号、
 * 时间要本地化，所以这里必须逐个接格式化钩子。
 */
const cardFields = computed<CardField[]>(() => [
  { key: 'gw_session_id', label: t('compression.table.sessionId'), format: (v) => (v == null ? null : shortID(String(v))) },
  {
    key: 'compression_strategy',
    label: t('compression.table.strategy'),
    type: 'badge',
    format: (v) => (v == null ? null : strategyLabel(String(v))),
  },
  { key: 'request_count', label: t('compression.table.requests'), type: 'metric', align: 'end' },
  {
    key: 'outbound_msg_count',
    label: t('compression.table.compressedMsgs'),
    type: 'metric',
    align: 'end',
    format: (v) => (v == null ? null : fmtNum(v as number)),
  },
  {
    key: 'outbound_token_est',
    label: t('compression.table.compressedTokens'),
    type: 'metric',
    align: 'end',
    format: (v) => (v == null ? null : fmtNum(v as number)),
  },
  {
    key: 'msg_reduction',
    label: t('compression.table.msgSaved'),
    type: 'metric',
    align: 'end',
    // 与表格一致：只有 > 0 才出负号，其余出破折号（不是 0）
    format: (v) => (typeof v === 'number' && v > 0 ? `-${v}` : null),
  },
  { key: 'last_ts', label: t('compression.table.lastTime'), format: (v) => (v == null ? null : fmtDateCompact(String(v))) },
])

/** 页码条：仅桌面。compact 走连续加载，两条路径不同时出现在屏幕上。 */
const showPager = computed(() => !isCompact.value && sessionsCount.value > sessionPageSize)

/**
 * compact 下的「正在取第 1 页」。**只认 refreshing** ——
 * `loadingNext` 是滚动加载更多，不该把「取数中」的提示按到所有控件上。
 */
const compactBusy = computed(() => continuous.state.value === 'refreshing')

/** 刷新钮忙碌态。桌面上退化为既有的 `loading`；compact 下额外看连续加载。 */
const refreshBusy = computed(() => loading.value || (isCompact.value && compactBusy.value))

/**
 * 重新取数：按档位分派。
 * 统计与图表**不分档** —— 它们不是列表，两条路径共用同一份。
 */
async function reload() {
  // 统计与图表先发（不 await）：它们与列表无依赖关系
  void loadStats()
  if (isCompact.value) {
    // 顺序不能反：先 invalidate（作废在途结果并提 revision），再 loadFirst，
    // 否则旧请求可能在新请求之后落地。
    continuous.invalidate()
    await continuous.loadFirst()
    return
  }
  sessionPage.value = 1
  await loadSessions()
}

// Load the current compression configuration for the read-only chip bar.
//
// 2026-10-03：原来注释写「Failures are non-blocking — the bar simply stays
// at defaults」。但这条配置条**不是装饰**：`getSetting` 返回的
// `spec.default` 是**该设置的出厂默认值**，不是服务端当前值。
// 读失败时把默认值当成「当前配置」讲出去，就是一句假话。
async function loadCurrentConfig() {
  try {
    const [en, mode, win, model] = await Promise.all([
      getSetting('compression.enabled'),
      getSetting('compression.mode'),
      getSetting('compression.window_fraction'),
      getSetting('compression.llm_model'),
    ])
    currentConfig.value = {
      enabled: (en.value ?? en.spec.default) === true,
      mode: mode.value ?? mode.spec.default ?? 'smart',
      window: win.value ?? win.spec.default ?? 0.8,
      model: model.value ?? model.spec.default ?? '',
    }
    configError.value = ''
  } catch (e: unknown) {
    configError.value = e instanceof Error && e.message
      ? e.message
      : t('compression.load.configFailed')
  }
}

function modeChipLabel(m: string): string {
  const map: Record<string, string> = {
    off: t('settings.compression.enumLabels.off'),
    auto_threshold: t('settings.compression.enumLabels.auto'),
    on_4xx: t('settings.compression.enumLabels.on4xx'),
    smart: t('settings.compression.enumLabels.smart'),
    aggressive: t('settings.compression.enumLabels.aggressive'),
  }
  return map[m] || m
}

function switchTab(tab: TabId) {
  activeTab.value = tab
  showCustom.value = tab === 'custom'
  // ★ 这里**不再**调 reload —— 下面的 `watch(activeTab, reload)` 已经会调。
  //   两处都调的话，每次切档位都发**两遍**统计 + 两遍列表（既有缺陷，顺手修掉）。
  //   `sessionPage` 的复位由 `reload()` 统一负责（页码路径里已有一行）。
}

// 2026-09-30 统一日历轮：custom 范围换 KxDateRangePicker（datetime 精度，面板内显式「应用」，
// 与 RequestLogsView custom 分支同款交互）。组件值 'YYYY-MM-DD HH:mm' ↔ 页面 customFrom/customTo
// 维持原 datetime-local 的 'YYYY-MM-DDTHH:mm' 契约（API from/to 与时长计算不变）。
const customRangeValue = computed<KxDateRange | null>(() => {
  if (!customFrom.value || !customTo.value) return null
  return { start: customFrom.value.replace('T', ' '), end: customTo.value.replace('T', ' ') }
})

function applyCustom(range: KxDateRange) {
  customFrom.value = range.start.replace(' ', 'T')
  customTo.value = range.end.replace(' ', 'T')
  void reload()
}

function goPage(p: number) {
  if (p < 1 || p > totalPages.value) return
  sessionPage.value = p
  loadSessions()
}

function viewSession(sessionId: string) {
  router.push({ path: '/request-logs', query: { gw_session_id: sessionId } })
}

/** 卡片整卡点击：与桌面行点击同一意图（跳到该会话的请求日志）。 */
function onCardClick(s: CompressionSessionItem) {
  viewSession(s.gw_session_id)
}

function fmtNum(n: number | undefined | null, decimals = 0): string {
  if (n === undefined || n === null) return '—'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return Number(n).toFixed(decimals)
}

function fmtPct(v: number | undefined | null): string {
  if (v === undefined || v === null) return '—'
  return (Number(v) * 100).toFixed(1) + '%'
}

const strategyLabels: Record<string, string> = {
  delta_append: t('compression.delta_append'),
  sliding_window_token: t('compression.sliding_window_token'),
  sliding_window_count: t('compression.sliding_window_count'),
  sliding_window_idle: t('compression.sliding_window_idle'),
  mechanical_trim: t('compression.mechanical_trim'),
  memora_l1_inject: t('compression.memora_l1_inject'),
  llm_summary: t('compression.llm_summary'),
  noop: t('compression.noop'),
  none: t('compression.none'),
}

const strategyColors: Record<string, string> = {
  delta_append: 'var(--success)',
  sliding_window_token: 'var(--info)',
  sliding_window_count: 'var(--accent)',
  sliding_window_idle: 'var(--cyan)',
  mechanical_trim: 'var(--warning)',
  memora_l1_inject: 'var(--pink)',
  llm_summary: 'var(--danger)',
  noop: 'var(--text-secondary)',
  none: 'var(--text-muted)',
}

const strategyEntries = computed(() => {
  if (!stats.value) return []
  return Object.entries(stats.value.strategy_distribution)
    .sort((a, b) => b[1] - a[1])
})

const strategyMaxCount = computed(() => {
  if (!strategyEntries.value.length) return 1
  return Math.max(...strategyEntries.value.map(([, v]) => v))
})

// Time series chart
const timeBucketLabel = computed(() => {
  if (!stats.value?.hourly_series?.length) return ''
  const hours = activeTab.value === 'custom'
    ? (customFrom.value && customTo.value
        // 'YYYY-MM-DDTHH:mm' 无秒非规范格式，补秒解析（P3-3）
        ? (parseLocalMinute(customTo.value).getTime() - parseLocalMinute(customFrom.value).getTime()) / 3600000
        : 24)
    : (displayHours.value || 24)
  if (hours <= 48) return t('compression.timeBucketHour')
  if (hours <= 168) return t('compression.timeBucket6Hour')
  return t('compression.timeBucketDay')
})

const chartBuckets = computed(() => {
  const series = stats.value?.hourly_series
  if (!series?.length) return []
  // Limit to at most 48 buckets for display
  if (series.length <= 48) return series
  const step = Math.ceil(series.length / 48)
  return series.filter((_, i) => i % step === 0)
})

// Shorten session ID for display (show first 8 chars + ellipsis)
function shortID(id: string): string {
  if (!id || id.length <= 12) return id || '—'
  return id.slice(0, 8) + '…'
}

function strategyLabel(s: string): string {
  return strategyLabels[s] || s
}

function strategyColor(s: string): string {
  return strategyColors[s] || 'var(--text-secondary)'
}

onMounted(() => {
  void reload()
  loadCurrentConfig()
})
watch(activeTab, () => { void reload() })
</script>

<template>
  <div class="compression-view">
    <div class="page-header">
      <h2>{{ t('compression.title') }}</h2>
      <div class="time-range-tabs">
        <button
          v-for="tab in ([
            { id: '24h' as TabId, label: t('compression.h24') },
            { id: '7d' as TabId, label: t('compression.d7') },
            { id: '30d' as TabId, label: t('compression.d30') },
            { id: 'custom' as TabId, label: t('compression.tabs.custom') },
          ])"
          :key="tab.id"
          class="tab-btn"
          :class="{ active: activeTab === tab.id }"
          @click="switchTab(tab.id)"
        >
          {{ tab.label }}
        </button>
      </div>
      <div v-if="showCustom" class="custom-range">
        <KxDateRangePicker
          :model-value="customRangeValue"
          :presets="[]"
          precision="datetime"
          @apply="applyCustom"
        />
      </div>
      <!--
        忙碌态：桌面上 `isCompact` 为 false，表达式退化成既有的 `loading` ⇒ 渲染逐字不变；
        compact 下额外看连续加载的 `refreshing`（统计的 `loading` 只管统计那一半）。
      -->
      <button class="btn btn-ghost btn-sm refresh-btn" @click="reload" :disabled="refreshBusy">
        {{ refreshBusy ? t('compression.loading') : t('compression.refresh') }}
      </button>
    </div>

    <!--
      2026-10-03：新增。三处加载失败原来都只写 `// non-blocking`，
      页面因此把「没查到」讲成「没有」：统计块整块不渲染、会话表显示
      「没有会话」、配置条显示出厂默认值。横幅要说清**哪几块**没查到。
    -->
    <div v-if="loadError" class="load-error-banner" role="status">{{ loadError }}</div>

    <!-- Current configuration chips (read-only; editable in Session Configuration → Compression) -->
    <div class="current-config-bar">
      <button class="config-toggle" @click="showCurrentConfig = !showCurrentConfig">
        <span class="caret" :class="{ open: showCurrentConfig }">▶</span>
        <span class="config-toggle-label">{{ t('sessions.config.basicSettings') }}</span>
        <span class="chip" :class="currentConfig.enabled ? 'chip-on' : 'chip-off'">
          {{ currentConfig.enabled ? t('sessions.config.enabled') : t('sessions.config.disabled') }}
        </span>
        <span class="chip">{{ modeChipLabel(currentConfig.mode) }}</span>
      </button>
      <div v-if="showCurrentConfig" class="config-chips">
        <div class="chip-item">
          <span class="chip-lbl">{{ t('sessions.config.compressionWindowLabel') }}</span>
          <span class="chip">{{ (currentConfig.window * 100).toFixed(0) }}%</span>
        </div>
        <div class="chip-item">
          <span class="chip-lbl">{{ t('sessions.config.compressionModelLabel') }}</span>
          <code class="chip code-chip">{{ currentConfig.model || '—' }}</code>
        </div>
        <a class="btn btn-ghost btn-sm" href="/plugins/ai-session-manager/settings">
          {{ t('sessions.config.viewDetails') }} →
        </a>
      </div>
    </div>

    <!-- Summary Cards -->
    <div class="stats-row" v-if="stats" :class="{ loading }">
      <div class="stat-card">
        <div class="stat-label">{{ t('compression.stats.totalRequests') }}</div>
        <div class="stat-value">{{ fmtNum(stats.total_requests) }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('compression.stats.compressed') }}</div>
        <div class="stat-value" style="color:var(--success)">{{ fmtNum(stats.compressed_total) }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('compression.stats.compressionRate') }}</div>
        <div class="stat-value">{{ fmtPct(stats.compression_rate) }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('compression.stats.estimatedSaved') }}</div>
        <div class="stat-value" style="color:var(--warning)">
          {{ stats.estimated_tokens_saved != null ? fmtNum(stats.estimated_tokens_saved) : '—' }}
        </div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('compression.stats.tokenBands') }}</div>
        <div class="stat-value" style="font-size:14px">
          {{ t('compression.stats.tokenBandBelow') }} {{ fmtNum(stats.token_band_below ?? 0) }}
          · {{ t('compression.stats.tokenBandPreliminary') }} {{ fmtNum(stats.token_band_preliminary ?? 0) }}
          · {{ t('compression.stats.tokenBandForced') }} {{ fmtNum(stats.token_band_forced ?? 0) }}
        </div>
      </div>
    </div>

    <!-- Strategy Distribution + Time Series -->
    <div class="charts-row" v-if="stats">
      <div class="card chart-card">
        <h3 class="card-title">{{ t('compression.charts.strategyDistribution') }}</h3>
        <div class="strategy-bars">
          <div
            v-for="[strategy, count] in strategyEntries"
            :key="strategy"
            class="strategy-bar-row"
            :title="t('compression.charts.titleSuffix', { strategy: strategyLabel(strategy), count, pct: fmtPct(stats ? count / stats.total_requests : 0) })"
          >
            <span class="strategy-label">{{ strategyLabel(strategy) }}</span>
            <div class="bar-track">
              <div
                class="bar-fill"
                :style="{
                  width: (count / strategyMaxCount * 100) + '%',
                  background: strategyColor(strategy)
                }"
              />
            </div>
            <span class="strategy-count">{{ fmtNum(count) }}</span>
          </div>
        </div>
      </div>
      <div class="card chart-card">
        <h3 class="card-title">{{ t('compression.charts.compressionRateTrend') }} <span class="badge">{{ timeBucketLabel }}</span></h3>
        <div class="time-series" v-if="chartBuckets.length">
          <div class="chart-y-axis">
            <span>{{ fmtPct(1) }}</span>
            <span>{{ fmtPct(0.75) }}</span>
            <span>{{ fmtPct(0.5) }}</span>
            <span>{{ fmtPct(0.25) }}</span>
            <span>{{ fmtPct(0) }}</span>
          </div>
          <div class="chart-bars">
            <div
              v-for="bucket in chartBuckets"
              :key="bucket.hour"
              class="chart-bar-col"
              :title="t('compression.charts.barTitle', { hour: bucket.hour, n: fmtNum(bucket.total), pct: fmtPct(bucket.rate) })"
            >
              <div class="rate-bar" :style="{ height: (bucket.rate * 100) + '%' }" />
            </div>
          </div>
        </div>
        <div v-else class="empty-hint">{{ t('compression.charts.noData') }}</div>
      </div>
    </div>

    <!-- Session Detail Table -->
    <div class="card session-card">
      <div class="card-header">
        <h3 class="card-title">{{ t('compression.table.title') }}</h3>
        <span class="count-badge">{{ t('compression.table.count', { n: countForBadge }) }}</span>
      </div>
      <!--
        2026-10-05 H6：双模板 + 双加载方式。
        · 桌面   → 表格（7 列表头 / 策略徽章 / 行样式逐字未改）+ 页码条
        · compact → 卡片 + 连续加载 + 底部 sentinel

        ★ 桌面三态**仍由本页自己出**（`.loading-hint` / `.empty-hint` 原样留着），
          容器只裁 compact 的：`:loading` / `:empty` 都带 `isCompact` 前置。
          与切片四（AuditLogView）同一取舍 —— 桌面刷新时那句「加载中…」
          不该被换成一个转圈；桌面空态那行文案也不该被换成一个通用组件。

        ★ `.table-wrap` 删掉了：容器自带 `overflow-x`，两个横滚容器嵌套会出双滚动条。
        ★ `table-min-width="0px"`：本页原本**没有**表级 min-width（列宽靠内容撑），
          传组件默认的 720px 会在窄一点的桌面内容宽度上多出一条原本不存在的横滚动条。
      -->
      <div v-if="sessionsLoading && !isCompact" class="loading-hint">{{ t('compression.loading') }}</div>
      <!-- 降级时**不说「没有会话」**——那是「不知道」被讲成「知道」；compact 也命中此分支，避免容器把错误裁成空态 -->
      <div v-else-if="sessionsError || continuousFailed" class="empty-hint load-error-hint">{{ sessionsErrorText }}</div>
      <div v-else-if="!isCompact && !sessions.length" class="empty-hint">{{ t('compression.table.empty') }}</div>
      <ResponsiveDataView
        v-else
        :rows="rows"
        title-key="gw_session_id"
        :title-format="cardTitle"
        :fields="cardFields"
        table-min-width="0px"
        :loading="isCompact && compactBusy"
        :empty="isCompact && !compactBusy && rows.length === 0"
        :empty-text="t('compression.table.empty')"
        :clickable="true"
        :clickable-label="t('compression.table.sessionId')"
        @row-click="onCardClick"
      >
        <template #table>
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('compression.table.sessionId') }}</th>
              <th>{{ t('compression.table.strategy') }}</th>
              <th>{{ t('compression.table.requests') }}</th>
              <th>{{ t('compression.table.compressedMsgs') }}</th>
              <th>{{ t('compression.table.compressedTokens') }}</th>
              <th>{{ t('compression.table.msgSaved') }}</th>
              <th>{{ t('compression.table.lastTime') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="s in sessions"
              :key="s.gw_session_id"
              class="session-row"
              @click="viewSession(s.gw_session_id)"
            >
              <td class="cell-session-id" :title="s.gw_session_id">{{ shortID(s.gw_session_id) }}</td>
              <td><span class="strategy-badge" :style="{ background: strategyColor(s.compression_strategy) }">{{ strategyLabel(s.compression_strategy) }}</span></td>
              <td>{{ s.request_count }}</td>
              <td>{{ s.outbound_msg_count != null ? s.outbound_msg_count : '—' }}</td>
              <td>{{ s.outbound_token_est != null ? fmtNum(s.outbound_token_est) : '—' }}</td>
              <td>
                <template v-if="s.msg_reduction != null && s.msg_reduction > 0">
                  <span class="saved-badge">-{{ s.msg_reduction }}</span>
                </template>
                <span v-else class="text-muted">—</span>
              </td>
              <td>{{ fmtDateCompact(s.last_ts) }}</td>
            </tr>
          </tbody>
        </table>
        </template>
      </ResponsiveDataView>

      <!-- 连续加载尾部：仅 compact。屏幕上不会同时出现两个「加载更多」语义。 -->
      <HyperLoadMore
        v-if="isCompact"
        :state="continuous.state.value"
        :has-more="continuous.hasMore.value"
        :loaded-count="continuous.loadedCount.value"
        @load-more="continuous.loadNext()"
        @retry="continuous.retry()"
      />

      <div v-if="showPager" class="pagination">
        <button
          class="btn btn-sm"
          :disabled="sessionPage <= 1"
          @click="goPage(sessionPage - 1)"
        >
          {{ t('compression.pagination.previous') }}
        </button>
        <span class="page-info">{{ t('compression.pagination.pageInfo', { current: sessionPage, total: totalPages }) }}</span>
        <button
          class="btn btn-sm"
          :disabled="sessionPage >= totalPages"
          @click="goPage(sessionPage + 1)"
        >
          {{ t('compression.pagination.next') }}
        </button>
      </div>
    </div>
  </div>
</template>

.load-error-banner {
  margin: 0 0 12px;
  padding: 8px 12px;
  border: 1px solid var(--warning);
  border-radius: 6px;
  color: var(--warning);
  font-size: 12px;
}
.load-error-hint {
  color: var(--warning);
}

<style scoped>
.compression-view {
  padding: 16px;
}

.page-header {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 16px;
  flex-wrap: wrap;
}

.page-header h2 {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
  white-space: nowrap;
}

.time-range-tabs {
  display: flex;
  gap: 4px;
  background: var(--bg-card);
  border-radius: 8px;
  padding: 3px;
  border: 1px solid var(--border);
}

.tab-btn {
  padding: 4px 12px;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--text-secondary);
  font-size: 13px;
  cursor: pointer;
  transition: all 0.15s;
}
.tab-btn.active {
  background: var(--accent);
  color: var(--bg);
}
.tab-btn:hover:not(.active) {
  background: var(--bg-hover);
}

.custom-range {
  display: flex;
  align-items: center;
  gap: 6px;
}

.refresh-btn {
  margin-left: auto;
}

.stats-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 12px;
  margin-bottom: 16px;
}
.stats-row.loading {
  opacity: 0.6;
  pointer-events: none;
}

.stat-card {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 16px;
}
.stat-label {
  font-size: 12px;
  color: var(--text-secondary);
  margin-bottom: 6px;
}
.stat-value {
  font-size: 22px;
  font-weight: 700;
  color: var(--text-primary);
  font-variant-numeric: tabular-nums;
}

.charts-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  margin-bottom: 16px;
}

.card {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 16px;
}
.card-title {
  margin: 0 0 12px;
  font-size: 14px;
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: 6px;
}
.card-title .badge {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 4px;
  background: var(--bg-hover);
  color: var(--text-secondary);
  font-weight: 400;
}
.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}
.count-badge {
  font-size: 12px;
  color: var(--text-secondary);
}

/* Strategy bars */
.strategy-bars {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.strategy-bar-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}
.strategy-label {
  width: 120px;
  flex-shrink: 0;
  color: var(--text-primary);
  text-align: right;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.bar-track {
  flex: 1;
  height: 18px;
  background: var(--bg-hover);
  border-radius: 4px;
  overflow: hidden;
}
.bar-fill {
  height: 100%;
  border-radius: 4px;
  transition: width 0.3s;
  min-width: 2px;
}
.strategy-count {
  width: 50px;
  flex-shrink: 0;
  color: var(--text-secondary);
  text-align: right;
  font-variant-numeric: tabular-nums;
}

/* Time series chart */
.time-series {
  display: flex;
  gap: 2px;
  height: 100px;
  align-items: stretch;
}
.chart-y-axis {
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  padding-right: 4px;
  font-size: 9px;
  color: var(--text-secondary);
  width: 32px;
  flex-shrink: 0;
}
.chart-bars {
  display: flex;
  flex: 1;
  gap: 2px;
  align-items: flex-end;
}
.chart-bar-col {
  flex: 1;
  display: flex;
  align-items: flex-end;
  min-width: 4px;
  cursor: default;
}
.rate-bar {
  width: 100%;
  min-height: 1px;
  background: var(--primary);
  border-radius: 2px 2px 0 0;
  opacity: 0.8;
  transition: height 0.3s;
}

/* Table */
.data-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.data-table th {
  text-align: left;
  padding: 8px 10px;
  color: var(--text-secondary);
  font-weight: 500;
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
.data-table td {
  padding: 8px 10px;
  border-bottom: 1px solid var(--border);
  color: var(--text-primary);
}
.session-row {
  cursor: pointer;
  transition: background 0.15s;
}
.session-row:hover {
  background: var(--bg-hover);
}
.cell-session-id {
  font-family: monospace;
  font-size: 12px;
}
.strategy-badge {
  display: inline-block;
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 11px;
  color: var(--on-primary);
  font-weight: 500;
}

.saved-badge {
  color: var(--success);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.text-muted {
  color: var(--text-secondary);
}

/* Current configuration chip bar */
.current-config-bar {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 8px 12px;
  margin-bottom: 16px;
}
.config-toggle {
  display: flex;
  align-items: center;
  gap: 10px;
  background: none;
  border: none;
  cursor: pointer;
  color: var(--text-secondary);
  font-size: 13px;
  padding: 0;
  width: 100%;
  text-align: left;
}
.config-toggle:hover { color: var(--text-primary); }
.config-toggle-label { font-weight: 600; color: var(--text-primary); }
.caret {
  font-size: 9px;
  transition: transform 0.2s;
  display: inline-block;
}
.caret.open { transform: rotate(90deg); }
.config-chips {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-top: 10px;
  flex-wrap: wrap;
}
.chip {
  display: inline-block;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: 11px;
  background: var(--bg-hover);
  color: var(--text-primary);
  border: 1px solid var(--border);
}
.chip-on { background: var(--success-bg); color: var(--success); border-color: var(--success-bd); }
.chip-off { background: var(--neutral-bg); color: var(--muted); border-color: var(--neutral-bd); }
.chip-item { display: flex; align-items: center; gap: 6px; }
.chip-lbl { font-size: 11px; color: var(--text-secondary); }
.code-chip { font-family: ui-monospace, SFMono-Regular, monospace; font-size: 11px; max-width: 340px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.pagination {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  margin-top: 12px;
}
.page-info {
  font-size: 13px;
  color: var(--text-secondary);
}

.loading-hint,
.empty-hint {
  text-align: center;
  padding: 32px;
  color: var(--text-secondary);
  font-size: 13px;
}

@media (max-width: 768px) {
  .stats-row {
    grid-template-columns: repeat(2, 1fr);
  }
  .charts-row {
    grid-template-columns: 1fr;
  }
}
</style>
