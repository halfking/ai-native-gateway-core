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

const { t } = useI18n()


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
  [statsError.value, sessionsError.value, configError.value].filter(Boolean).join('；'),
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

async function loadStats() {
  loading.value = true
  try {
    const params: { hours?: number; from?: string; to?: string } = {}
    if (activeTab.value === 'custom') {
      if (customFrom.value) params.from = customFrom.value
      if (customTo.value) params.to = customTo.value
    } else {
      params.hours = displayHours.value
    }
    stats.value = await getCompressionStats(params)
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

async function loadSessions() {
  sessionsLoading.value = true
  try {
    const params: {
      hours?: number
      from?: string
      to?: string
      page: number
      page_size: number
    } = { page: sessionPage.value, page_size: sessionPageSize }
    if (activeTab.value === 'custom') {
      if (customFrom.value) params.from = customFrom.value
      if (customTo.value) params.to = customTo.value
    } else {
      params.hours = displayHours.value
    }
    const resp = await getCompressionSessions(params)
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

async function loadAll() {
  await Promise.all([loadStats(), loadSessions()])
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
  sessionPage.value = 1
  loadAll()
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
  sessionPage.value = 1
  loadAll()
}

function goPage(p: number) {
  if (p < 1 || p > totalPages.value) return
  sessionPage.value = p
  loadSessions()
}

function viewSession(sessionId: string) {
  router.push({ path: '/request-logs', query: { gw_session_id: sessionId } })
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
  loadAll()
  loadCurrentConfig()
})
watch(activeTab, loadAll)
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
      <button class="btn btn-ghost btn-sm refresh-btn" @click="loadAll" :disabled="loading">
        {{ loading ? t('compression.loading') : t('compression.refresh') }}
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
        <span class="count-badge">{{ t('compression.table.count', { n: sessionsCount }) }}</span>
      </div>
      <div v-if="sessionsLoading" class="loading-hint">{{ t('compression.loading') }}</div>
      <!-- 降级时**不说「没有会话」**——那是「不知道」被讲成「知道」 -->
      <div v-else-if="sessionsError" class="empty-hint load-error-hint">{{ sessionsError }}</div>
      <div v-else-if="!sessions.length" class="empty-hint">{{ t('compression.table.empty') }}</div>
      <div v-else class="table-wrap">
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
      </div>
      <div v-if="sessionsCount > sessionPageSize" class="pagination">
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
.table-wrap {
  overflow-x: auto;
}
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
