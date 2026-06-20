<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import {
  getCompressionStats,
  getCompressionSessions,
  type CompressionStats,
  type CompressionSessionItem,
} from '../api'

const router = useRouter()

const loading = ref(false)
const stats = ref<CompressionStats | null>(null)
const sessions = ref<CompressionSessionItem[]>([])
const sessionsCount = ref(0)
const sessionsLoading = ref(false)

type TabId = '24h' | '7d' | '30d' | 'custom'
const activeTab = ref<TabId>('24h')
const customFrom = ref('')
const customTo = ref('')
const customFromInput = ref('')
const customToInput = ref('')
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
  } catch {
    // non-blocking
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
  } catch {
    // non-blocking
  } finally {
    sessionsLoading.value = false
  }
}

async function loadAll() {
  await Promise.all([loadStats(), loadSessions()])
}

function switchTab(tab: TabId) {
  activeTab.value = tab
  showCustom.value = tab === 'custom'
  sessionPage.value = 1
  loadAll()
}

function applyCustom() {
  if (customFromInput.value) customFrom.value = customFromInput.value
  if (customToInput.value) customTo.value = customToInput.value
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

function fmtDate(v: string | null | undefined): string {
  if (!v) return '—'
  const d = new Date(v)
  return d.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}

const strategyLabels: Record<string, string> = {
  delta_append: '增量拼接',
  sliding_window_token: '滑动窗口(Token)',
  sliding_window_count: '滑动窗口(条数)',
  sliding_window_idle: '滑动窗口(空闲)',
  mechanical_trim: '机械裁剪',
  memora_l1_inject: 'Memora注入',
  llm_summary: 'LLM总结',
  noop: '空操作',
  none: '未压缩',
}

const strategyColors: Record<string, string> = {
  delta_append: '#22c55e',
  sliding_window_token: '#3b82f6',
  sliding_window_count: '#8b5cf6',
  sliding_window_idle: '#06b6d4',
  mechanical_trim: '#f59e0b',
  memora_l1_inject: '#ec4899',
  llm_summary: '#ef4444',
  noop: '#6b7280',
  none: '#374151',
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
const chartMax = computed(() => {
  if (!stats.value?.hourly_series?.length) return 1
  return Math.max(...stats.value.hourly_series.map(h => h.total))
})

const barHeight = 80

function barStyle(bucket: { total: number }) {
  const pct = chartMax.value > 0 ? (bucket.total / chartMax.value) * 100 : 0
  return { height: barHeight + 'px', width: pct + '%' }
}

function compressedBarStyle(bucket: { total: number; compressed: number }) {
  if (bucket.total === 0) return { width: '0%' }
  return { width: (bucket.compressed / bucket.total) * 100 + '%' }
}

const timeBucketLabel = computed(() => {
  if (!stats.value?.hourly_series?.length) return ''
  if (stats.value.hourly_series.length > 48) return '按天'
  if (stats.value.hourly_series.length > 24) return '每6小时'
  return '按小时'
})

const chartBuckets = computed(() => {
  const series = stats.value?.hourly_series
  if (!series?.length) return []
  // Limit to at most 48 buckets for display
  if (series.length <= 48) return series
  const step = Math.ceil(series.length / 48)
  return series.filter((_, i) => i % step === 0)
})

// truncated ID
function shortID(id: string): string {
  if (!id || id.length <= 12) return id || '—'
  return id.slice(0, 8) + '…'
}

function strategyLabel(s: string): string {
  return strategyLabels[s] || s
}

function strategyColor(s: string): string {
  return strategyColors[s] || '#6b7280'
}

onMounted(loadAll)
watch(activeTab, loadAll)
</script>

<template>
  <div class="compression-view">
    <div class="page-header">
      <h2>压缩概览</h2>
      <div class="time-range-tabs">
        <button
          v-for="tab in ([
            { id: '24h' as TabId, label: '24小时' },
            { id: '7d' as TabId, label: '7天' },
            { id: '30d' as TabId, label: '30天' },
            { id: 'custom' as TabId, label: '自定义' },
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
        <input
          v-model="customFromInput"
          type="datetime-local"
          class="input-sm"
          placeholder="开始时间"
        />
        <span class="range-sep">至</span>
        <input
          v-model="customToInput"
          type="datetime-local"
          class="input-sm"
          placeholder="结束时间"
        />
        <button class="btn btn-sm btn-primary" @click="applyCustom">查询</button>
      </div>
      <button class="btn btn-ghost btn-sm refresh-btn" @click="loadAll" :disabled="loading">
        {{ loading ? '加载中…' : '刷新' }}
      </button>
    </div>

    <!-- Summary Cards -->
    <div class="stats-row" v-if="stats" :class="{ loading }">
      <div class="stat-card">
        <div class="stat-label">总请求数</div>
        <div class="stat-value">{{ fmtNum(stats.total_requests) }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">已压缩</div>
        <div class="stat-value" style="color:var(--success,#22c55e)">{{ fmtNum(stats.compressed_total) }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">压缩率</div>
        <div class="stat-value">{{ fmtPct(stats.compression_rate) }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">预估节省 Token</div>
        <div class="stat-value" style="color:var(--warning,#f59e0b)">
          {{ stats.estimated_tokens_saved != null ? fmtNum(stats.estimated_tokens_saved) : '—' }}
        </div>
      </div>
    </div>

    <!-- Strategy Distribution + Time Series -->
    <div class="charts-row" v-if="stats">
      <div class="card chart-card">
        <h3 class="card-title">压缩策略分布</h3>
        <div class="strategy-bars">
          <div
            v-for="[strategy, count] in strategyEntries"
            :key="strategy"
            class="strategy-bar-row"
            :title="`${strategyLabel(strategy)}: ${count} 条 (${fmtPct(stats ? count / stats.total_requests : 0)})`"
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
        <h3 class="card-title">压缩率趋势 <span class="badge">{{ timeBucketLabel }}</span></h3>
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
              :title="`${bucket.hour}: ${fmtNum(bucket.total)} 请求, ${fmtPct(bucket.rate)} 压缩率`"
            >
              <div class="rate-bar" :style="{ height: (bucket.rate * 100) + '%' }" />
            </div>
          </div>
        </div>
        <div v-else class="empty-hint">暂无可用的时间序列数据</div>
      </div>
    </div>

    <!-- Session Detail Table -->
    <div class="card session-card">
      <div class="card-header">
        <h3 class="card-title">会话压缩详情</h3>
        <span class="count-badge">共 {{ sessionsCount }} 条</span>
      </div>
      <div v-if="sessionsLoading" class="loading-hint">加载中…</div>
      <div v-else-if="!sessions.length" class="empty-hint">所选时间段内没有压缩会话记录</div>
      <div v-else class="table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>会话ID</th>
              <th>策略</th>
              <th>请求数</th>
              <th>压缩消息数</th>
              <th>压缩Token数</th>
              <th>消息节省</th>
              <th>最后时间</th>
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
              <td>{{ fmtDate(s.last_ts) }}</td>
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
          上一页
        </button>
        <span class="page-info">{{ sessionPage }} / {{ totalPages }}</span>
        <button
          class="btn btn-sm"
          :disabled="sessionPage >= totalPages"
          @click="goPage(sessionPage + 1)"
        >
          下一页
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.compression-view {
  padding: 16px;
  max-width: 1400px;
  margin: 0 auto;
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
  background: var(--bg-card, #1e1e2e);
  border-radius: 8px;
  padding: 3px;
  border: 1px solid var(--border, #333);
}

.tab-btn {
  padding: 4px 12px;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--text-secondary, #6b7280);
  font-size: 13px;
  cursor: pointer;
  transition: all 0.15s;
}
.tab-btn.active {
  background: var(--primary, #6366f1);
  color: #fff;
}
.tab-btn:hover:not(.active) {
  background: var(--bg-hover, #2a2a3e);
}

.custom-range {
  display: flex;
  align-items: center;
  gap: 6px;
}
.custom-range .input-sm {
  padding: 4px 8px;
  border: 1px solid var(--border, #333);
  border-radius: 6px;
  background: var(--bg-card, #1e1e2e);
  color: var(--text-primary, #e5e7eb);
  font-size: 12px;
}
.range-sep {
  color: var(--text-secondary, #6b7280);
  font-size: 12px;
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
  background: var(--bg-card, #1e1e2e);
  border: 1px solid var(--border, #333);
  border-radius: 10px;
  padding: 16px;
}
.stat-label {
  font-size: 12px;
  color: var(--text-secondary, #6b7280);
  margin-bottom: 6px;
}
.stat-value {
  font-size: 22px;
  font-weight: 700;
  color: var(--text-primary, #e5e7eb);
  font-variant-numeric: tabular-nums;
}

.charts-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  margin-bottom: 16px;
}

.card {
  background: var(--bg-card, #1e1e2e);
  border: 1px solid var(--border, #333);
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
  background: var(--bg-hover, #2a2a3e);
  color: var(--text-secondary, #6b7280);
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
  color: var(--text-secondary, #6b7280);
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
  color: var(--text-primary, #e5e7eb);
  text-align: right;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.bar-track {
  flex: 1;
  height: 18px;
  background: var(--bg-hover, #2a2a3e);
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
  color: var(--text-secondary, #6b7280);
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
  color: var(--text-secondary, #6b7280);
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
  background: var(--primary, #6366f1);
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
  color: var(--text-secondary, #6b7280);
  font-weight: 500;
  border-bottom: 1px solid var(--border, #333);
  white-space: nowrap;
}
.data-table td {
  padding: 8px 10px;
  border-bottom: 1px solid var(--border, #333);
  color: var(--text-primary, #e5e7eb);
}
.session-row {
  cursor: pointer;
  transition: background 0.15s;
}
.session-row:hover {
  background: var(--bg-hover, #2a2a3e);
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
  color: #fff;
  font-weight: 500;
}

.saved-badge {
  color: var(--success, #22c55e);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.text-muted {
  color: var(--text-secondary, #6b7280);
}

.pagination {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  margin-top: 12px;
}
.page-info {
  font-size: 13px;
  color: var(--text-secondary, #6b7280);
}

.loading-hint,
.empty-hint {
  text-align: center;
  padding: 32px;
  color: var(--text-secondary, #6b7280);
  font-size: 13px;
}

@media (max-width: 800px) {
  .stats-row {
    grid-template-columns: repeat(2, 1fr);
  }
  .charts-row {
    grid-template-columns: 1fr;
  }
}
</style>
