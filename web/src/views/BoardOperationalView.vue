<script setup lang="ts">
// BoardOperationalView — 运营面板（2026-11-19 优化）
// 从运营角度汇总分析系统整体状况，整合多维度数据
//
// 端点：
// - GET /api/admin/dashboard/operational（后台任务、自检）
// - GET /api/admin/dashboard/board（完整看板数据：summary、pies、trends）
//
// 优化：整合运营关键指标、流量分析、成本分析、错误分析、趋势图表
import { onMounted, ref, computed } from 'vue'
import { surfaceError } from '../i18n/errorSurface'
import {
  fetchBoardOperational,
  fetchDashboardBoard,
  type BoardOperationalPayload,
  type BoardPayload,
} from '../api/board'

const loading = ref(false)
const error = ref('')
const operationalData = ref<BoardOperationalPayload | null>(null)
const boardData = ref<BoardPayload | null>(null)
const timeRange = ref(7)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [opData, bdData] = await Promise.all([
      fetchBoardOperational(),
      fetchDashboardBoard({ days: timeRange.value }),
    ])
    operationalData.value = opData
    boardData.value = bdData
  } catch (e) {
    error.value = surfaceError(e)
  } finally {
    loading.value = false
  }
}

function formatTimestamp(s: string | undefined | null): string {
  if (!s) return '—'
  return s.replace('T', ' ').replace(/\.\d+Z$/, 'Z').replace('Z', '')
}

function statusLabel(s: string | undefined | null): string {
  if (!s) return '未知'
  if (s === 'running') return '运行中'
  if (s === 'success') return '成功'
  if (s === 'failed') return '失败'
  if (s === 'pending') return '等待中'
  if (s === 'cancelled') return '已取消'
  return s
}

function formatPct(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return (n * 100).toFixed(1) + '%'
}

function formatNumber(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return n.toLocaleString('en-US')
}

function formatCost(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return '$' + n.toFixed(2)
}

function formatLatency(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return n.toFixed(0) + 'ms'
}

const derivedMetrics = computed(() => {
  const summary = boardData.value?.summary
  if (!summary) return null

  const avgTokensPerRequest = summary.total_requests
    ? (summary.total_tokens || 0) / summary.total_requests
    : 0
  
  const avgCostPerRequest = summary.total_requests
    ? (summary.total_cost_usd || 0) / summary.total_requests
    : 0

  const avgCreditsPerRequest = summary.total_requests
    ? (summary.total_credits_charged || 0) / summary.total_requests
    : 0

  return {
    avgTokensPerRequest,
    avgCostPerRequest,
    avgCreditsPerRequest,
  }
})

function topItems(items: any[] | undefined, n: number = 5) {
  if (!items || items.length === 0) return []
  return items.slice(0, n)
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="board-operational-view">
    <header class="bo-header">
      <div>
        <h1 class="bo-title">{{ $t('dashboard.operational.title') }}</h1>
        <p class="bo-subtitle">{{ $t('dashboard.operational.subtitle') }}</p>
      </div>
      <div class="bo-header-actions">
        <select v-model="timeRange" class="bo-time-select" @change="load">
          <option :value="1">{{ $t('dashboard.operational.timeRange1d') }}</option>
          <option :value="7">{{ $t('dashboard.operational.timeRange7d') }}</option>
          <option :value="30">{{ $t('dashboard.operational.timeRange30d') }}</option>
        </select>
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
          {{ loading ? $t('common.feedback.loading') : $t('common.button.refresh') }}
        </button>
      </div>
    </header>

    <p v-if="error" class="bo-error">{{ error }}</p>

    <div v-if="loading && !operationalData && !boardData" class="bo-loading">
      {{ $t('common.feedback.loading') }}
    </div>

    <template v-else-if="operationalData || boardData">
      <!-- 1. 运营概览 - 关键指标 -->
      <section v-if="boardData?.summary" class="bo-section">
        <h2 class="bo-section-title">{{ $t('dashboard.operational.overviewDays', { days: timeRange }) }}</h2>
        <p class="bo-time-range-hint">{{ $t('dashboard.operational.dataTimeRange', { days: timeRange }) }}</p>
        
        <div class="bo-metrics-grid">
          <div class="bo-metric-card">
            <div class="bo-metric-label">{{ $t('dashboard.operational.totalRequests') }}</div>
            <div class="bo-metric-value">{{ formatNumber(boardData.summary.total_requests) }}</div>
            <div v-if="boardData.summary.success_rate != null" class="bo-metric-sub">
              {{ $t('dashboard.operational.successRate') }} {{ formatPct(boardData.summary.success_rate) }}
            </div>
          </div>

          <div class="bo-metric-card">
            <div class="bo-metric-label">{{ $t('dashboard.operational.totalTokens') }}</div>
            <div class="bo-metric-value">{{ formatNumber(boardData.summary.total_tokens) }}</div>
            <div v-if="derivedMetrics" class="bo-metric-sub">
              {{ $t('dashboard.operational.avgPerRequest', { value: formatNumber(derivedMetrics.avgTokensPerRequest) }) }}
            </div>
          </div>

          <div class="bo-metric-card">
            <div class="bo-metric-label">{{ $t('dashboard.operational.totalCost') }}</div>
            <div class="bo-metric-value">{{ formatCost(boardData.summary.total_cost_usd) }}</div>
            <div v-if="derivedMetrics" class="bo-metric-sub">
              {{ $t('dashboard.operational.avgPerRequest', { value: formatCost(derivedMetrics.avgCostPerRequest) }) }}
            </div>
          </div>

          <div class="bo-metric-card">
            <div class="bo-metric-label">{{ $t('dashboard.operational.totalCredits') }}</div>
            <div class="bo-metric-value">{{ formatNumber(boardData.summary.total_credits_charged) }}</div>
            <div v-if="derivedMetrics" class="bo-metric-sub">
              {{ $t('dashboard.operational.avgPerRequest', { value: formatNumber(derivedMetrics.avgCreditsPerRequest) }) }}
            </div>
          </div>

          <div class="bo-metric-card">
            <div class="bo-metric-label">{{ $t('dashboard.operational.avgLatency') }}</div>
            <div class="bo-metric-value">{{ formatLatency(boardData.summary.avg_latency_ms) }}</div>
          </div>

          <div class="bo-metric-card">
            <div class="bo-metric-label">{{ $t('dashboard.operational.activeResources') }}</div>
            <div class="bo-metric-value">
              {{ $t('dashboard.operational.modelsCount', { count: formatNumber(boardData.summary.active_models) }) }}
            </div>
            <div class="bo-metric-sub">
              {{ $t('dashboard.operational.keysCount', { count: formatNumber(boardData.summary.active_api_keys) }) }}
            </div>
          </div>
        </div>

        <p v-if="boardData.summary.degraded_summary" class="bo-section-warning">
          {{ $t('dashboard.operational.degradedSummary', { hint: boardData.summary.summary_hint || '' }) }}
        </p>
      </section>

      <!-- 2. 分布分析 -->
      <section v-if="boardData?.pies" class="bo-section">
        <h2 class="bo-section-title">{{ $t('dashboard.operational.distributionTitle') }}</h2>
        <p class="bo-time-range-hint">{{ $t('dashboard.operational.dataTimeRange', { days: timeRange }) }}</p>
        
        <div class="bo-pies-grid">
          <!-- 模型分布 -->
          <div class="bo-pie-card">
            <h3 class="bo-pie-title">{{ $t('dashboard.operational.topModels') }}</h3>
            <div v-if="topItems(boardData.pies.models, 5).length > 0" class="bo-pie-list">
              <div v-for="item in topItems(boardData.pies.models, 5)" :key="item.key" class="bo-pie-item">
                <span class="bo-pie-key">{{ item.key }}</span>
                <span class="bo-pie-value">{{ $t('dashboard.operational.requestsCount', { count: formatNumber(item.requests) }) }}</span>
              </div>
            </div>
            <p v-else class="bo-pie-empty">{{ $t('dashboard.operational.emptyNoData') }}</p>
          </div>

          <!-- 供应商分布 -->
          <div class="bo-pie-card">
            <h3 class="bo-pie-title">{{ $t('dashboard.operational.topProviders') }}</h3>
            <div v-if="topItems(boardData.pies.providers, 5).length > 0" class="bo-pie-list">
              <div v-for="item in topItems(boardData.pies.providers, 5)" :key="item.key" class="bo-pie-item">
                <span class="bo-pie-key">{{ item.key }}</span>
                <span class="bo-pie-value">{{ $t('dashboard.operational.requestsCount', { count: formatNumber(item.requests) }) }}</span>
              </div>
            </div>
            <p v-else class="bo-pie-empty">{{ $t('dashboard.operational.emptyNoData') }}</p>
          </div>

          <!-- 客户端分布 -->
          <div class="bo-pie-card">
            <h3 class="bo-pie-title">{{ $t('dashboard.operational.topClients') }}</h3>
            <div v-if="topItems(boardData.pies.clients, 5).length > 0" class="bo-pie-list">
              <div v-for="item in topItems(boardData.pies.clients, 5)" :key="item.key" class="bo-pie-item">
                <span class="bo-pie-key">{{ item.key }}</span>
                <span class="bo-pie-value">{{ $t('dashboard.operational.requestsCount', { count: formatNumber(item.requests) }) }}</span>
              </div>
            </div>
            <p v-else class="bo-pie-empty">{{ $t('dashboard.operational.emptyNoData') }}</p>
          </div>

          <!-- 租户分布 -->
          <div class="bo-pie-card">
            <h3 class="bo-pie-title">{{ $t('dashboard.operational.topTenants') }}</h3>
            <div v-if="topItems(boardData.pies.tenants, 5).length > 0" class="bo-pie-list">
              <div v-for="item in topItems(boardData.pies.tenants, 5)" :key="item.key" class="bo-pie-item">
                <span class="bo-pie-key">{{ item.key }}</span>
                <span class="bo-pie-value">{{ $t('dashboard.operational.requestsCount', { count: formatNumber(item.requests) }) }}</span>
              </div>
            </div>
            <p v-else class="bo-pie-empty">{{ $t('dashboard.operational.emptyNoData') }}</p>
          </div>
        </div>
      </section>

      <!-- 3. 错误分析 -->
      <section v-if="boardData?.pies?.errors && boardData.pies.errors.length > 0" class="bo-section">
        <h2 class="bo-section-title">{{ $t('dashboard.operational.errorAnalysisTitle') }}</h2>
        <p class="bo-time-range-hint">{{ $t('dashboard.operational.dataTimeRange', { days: timeRange }) }}</p>
        
        <div class="bo-error-list">
          <div v-for="item in topItems(boardData.pies.errors, 10)" :key="item.key" class="bo-error-item">
            <div class="bo-error-key">{{ item.key }}</div>
            <div class="bo-error-value">{{ formatNumber(item.requests) }}</div>
          </div>
        </div>
      </section>

      <!-- 4. 趋势分析 -->
      <section v-if="boardData?.trends && boardData.trends.length > 0" class="bo-section">
        <h2 class="bo-section-title">{{ $t('dashboard.operational.trendAnalysisTitle') }}</h2>
        <p class="bo-time-range-hint">{{ $t('dashboard.operational.dataTimeRange', { days: timeRange }) }}</p>
        
        <div class="bo-trend-summary">
          <p>{{ $t('dashboard.operational.trendDataPoints', { count: boardData.trends.length }) }}</p>
          <p v-if="boardData.degraded_trends" class="bo-section-warning">
            {{ $t('dashboard.operational.degradedTrends', { reason: boardData.degraded_trends_reason || '' }) }}
          </p>
        </div>

        <div class="bo-trend-chart">
          <div v-for="(point, idx) in boardData.trends" :key="idx" class="bo-trend-point">
            <div class="bo-trend-time">{{ point.bucket.split('T')[0] }}</div>
            <div class="bo-trend-bars">
              <div class="bo-trend-bar" :style="{ width: (point.requests / (boardData.summary?.total_requests || 1) * 100) + '%' }">
                {{ formatNumber(point.requests) }}
              </div>
            </div>
          </div>
        </div>
      </section>

      <!-- 5. 系统健康 - 后台任务 -->
      <section v-if="operationalData?.background_tasks" class="bo-section">
        <h2 class="bo-section-title">{{ $t('dashboard.operational.systemHealthTasks') }}</h2>

        <div class="bo-cards">
          <div class="bo-card">
            <h3 class="bo-card-title">{{ $t('dashboard.operational.modelDiscovery') }}</h3>
            <div v-if="operationalData.background_tasks?.discovery" class="bo-card-body">
              <div class="bo-row">
                <span class="bo-row-label">{{ $t('common.terms.status') }}</span>
                <span class="bo-row-value" :class="{ 'is-running': operationalData.background_tasks.discovery.running }">
                  {{ operationalData.background_tasks.discovery.running ? $t('common.status.running') : statusLabel(operationalData.background_tasks.discovery.status) }}
                </span>
              </div>
              <div class="bo-row">
                <span class="bo-row-label">{{ $t('dashboard.operational.triggerMethod') }}</span>
                <span class="bo-row-value">{{ operationalData.background_tasks.discovery.trigger ?? '—' }}</span>
              </div>
              <div class="bo-row">
                <span class="bo-row-label">{{ $t('common.terms.startTime') }}</span>
                <span class="bo-row-value">{{ formatTimestamp(operationalData.background_tasks.discovery.started_at) }}</span>
              </div>
              <div class="bo-row">
                <span class="bo-row-label">{{ $t('common.terms.recentHeartbeat') }}</span>
                <span class="bo-row-value">{{ formatTimestamp(operationalData.background_tasks.discovery.heartbeat_at) }}</span>
              </div>
            </div>
            <p v-else class="bo-card-empty">{{ $t('dashboard.board.emptyNoResultDiscovery') }}</p>
            <p v-if="operationalData.background_tasks?.degraded" class="bo-card-degraded">
              {{ $t('dashboard.operational.degradedReason', { reason: operationalData.background_tasks.degraded_reason ?? '' }) }}
            </p>
          </div>

          <div class="bo-card">
            <h3 class="bo-card-title">{{ $t('dashboard.operational.probeLoop') }}</h3>
            <div v-if="operationalData.background_tasks?.probe_loop" class="bo-card-body">
              <div class="bo-row">
                <span class="bo-row-label">{{ $t('dashboard.operational.checksLast10m') }}</span>
                <span class="bo-row-value">{{ formatNumber(operationalData.background_tasks.probe_loop.checks_last_10m) }}</span>
              </div>
            </div>
            <p v-else class="bo-card-empty">{{ $t('dashboard.board.emptyNoResultProbe') }}</p>
            <p v-if="operationalData.background_tasks?.probe_degraded" class="bo-card-degraded">
              {{ $t('dashboard.operational.degradedProbe') }}
            </p>
          </div>
        </div>
      </section>

      <!-- 6. 系统健康 - 自检 -->
      <section v-if="operationalData?.selfcheck" class="bo-section">
        <h2 class="bo-section-title">{{ $t('dashboard.operational.systemHealthSelfcheck') }}</h2>

        <div class="bo-metrics">
          <div class="bo-metric">
            <span class="bo-metric-label">{{ $t('dashboard.operational.totalRuns24h') }}</span>
            <span class="bo-metric-value">{{ formatNumber(operationalData.selfcheck?.total_runs_24h) }}</span>
          </div>
          <div class="bo-metric">
            <span class="bo-metric-label">{{ $t('common.terms.successRate') }}</span>
            <span class="bo-metric-value">{{ formatPct(operationalData.selfcheck?.success_rate) }}</span>
          </div>
          <div class="bo-metric">
            <span class="bo-metric-label">{{ $t('dashboard.operational.lastStatus') }}</span>
            <span class="bo-metric-value">{{ statusLabel(operationalData.selfcheck?.last_status) }}</span>
          </div>
          <div class="bo-metric">
            <span class="bo-metric-label">{{ $t('dashboard.operational.lastRunAt') }}</span>
            <span class="bo-metric-value">{{ formatTimestamp(operationalData.selfcheck?.last_run_at) }}</span>
          </div>
        </div>

        <p v-if="operationalData.selfcheck?.degraded" class="bo-section-degraded">
          {{ $t('dashboard.operational.degradedSelfcheck', { reason: operationalData.selfcheck.degraded_reason ?? '' }) }}
        </p>
      </section>

      <footer class="bo-footer">
        <p>{{ $t('dashboard.operational.footerHint') }}</p>
        <p>{{ $t('dashboard.operational.dataTimeRange', { days: timeRange }) }}</p>
      </footer>
    </template>
  </div>
</template>

<style scoped>
.board-operational-view {
  padding: var(--space-4);
}

.bo-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  margin-bottom: var(--space-4);
  gap: var(--space-3);
}

.bo-title {
  font-size: var(--text-xl);
  font-weight: var(--font-semibold);
  margin-bottom: var(--space-1);
}

.bo-subtitle {
  font-size: var(--text-sm);
  color: var(--text-muted);
}

.bo-header-actions {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

.bo-time-select {
  padding: var(--space-1) var(--space-2);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg-surface);
  color: var(--text);
  font-size: var(--text-sm);
}

.bo-error {
  color: var(--danger);
  padding: var(--space-2);
  background: var(--danger-bg);
  border-radius: var(--radius);
  margin-bottom: var(--space-3);
}

.bo-loading {
  color: var(--text-muted);
  padding: var(--space-3);
  text-align: center;
}

.bo-section {
  margin-bottom: var(--space-4);
}

.bo-section-title {
  font-size: var(--text-lg);
  font-weight: var(--font-semibold);
  margin-bottom: var(--space-3);
  padding-bottom: var(--space-2);
  border-bottom: 2px solid var(--border);
}

.bo-time-range-hint {
  color: var(--text-muted);
  font-size: var(--text-sm);
  margin-top: calc(var(--space-2) * -1);
  margin-bottom: var(--space-3);
  padding: var(--space-2);
  background: var(--bg-surface);
  border-left: 3px solid var(--primary);
  border-radius: var(--radius);
}

.bo-section-warning,
.bo-section-degraded {
  margin-top: var(--space-2);
  color: var(--warning);
  font-size: var(--text-sm);
  padding: var(--space-2);
  background: var(--warning-bg);
  border-radius: var(--radius);
}

/* 指标网格 */
.bo-metrics-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: var(--space-3);
}

.bo-metric-card {
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: var(--space-3);
}

.bo-metric-label {
  font-size: var(--text-sm);
  color: var(--text-muted);
  margin-bottom: var(--space-1);
}

.bo-metric-value {
  font-size: var(--text-2xl);
  font-weight: var(--font-semibold);
  color: var(--text);
  font-variant-numeric: tabular-nums;
}

.bo-metric-sub {
  font-size: var(--text-sm);
  color: var(--text-muted);
  margin-top: var(--space-1);
}

/* 分布分析网格 */
.bo-pies-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: var(--space-3);
}

.bo-pie-card {
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: var(--space-3);
}

.bo-pie-title {
  font-size: var(--text-md);
  font-weight: var(--font-semibold);
  margin-bottom: var(--space-2);
  color: var(--text);
}

.bo-pie-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}

.bo-pie-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: var(--space-2);
  background: var(--bg);
  border-radius: var(--radius-sm);
  font-size: var(--text-sm);
}

.bo-pie-key {
  color: var(--text);
  font-weight: var(--font-medium);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
}

.bo-pie-value {
  color: var(--text-muted);
  font-variant-numeric: tabular-nums;
  margin-left: var(--space-2);
}

.bo-pie-empty {
  color: var(--text-muted);
  font-style: italic;
  text-align: center;
  padding: var(--space-3);
}

/* 错误列表 */
.bo-error-list {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: var(--space-2);
}

.bo-error-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: var(--space-2);
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  font-size: var(--text-sm);
}

.bo-error-key {
  color: var(--text);
  font-family: var(--font-mono);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
}

.bo-error-value {
  color: var(--danger);
  font-weight: var(--font-semibold);
  font-variant-numeric: tabular-nums;
  margin-left: var(--space-2);
}

/* 趋势图表 */
.bo-trend-summary {
  margin-bottom: var(--space-3);
  padding: var(--space-2);
  background: var(--bg-surface);
  border-radius: var(--radius);
  font-size: var(--text-sm);
  color: var(--text-muted);
}

.bo-trend-chart {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}

.bo-trend-point {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--text-sm);
}

.bo-trend-time {
  width: 100px;
  color: var(--text-muted);
  font-variant-numeric: tabular-nums;
}

.bo-trend-bars {
  flex: 1;
  height: 24px;
  background: var(--bg-surface);
  border-radius: var(--radius-sm);
  overflow: hidden;
}

.bo-trend-bar {
  height: 100%;
  background: var(--primary);
  display: flex;
  align-items: center;
  padding: 0 var(--space-2);
  color: white;
  font-size: var(--text-xs);
  font-variant-numeric: tabular-nums;
  transition: width 0.3s ease;
}

/* 后台任务卡片 */
.bo-cards {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: var(--space-3);
}

.bo-card {
  background: var(--bg-surface);
  border: 1px solid var(--border-muted);
  border-radius: var(--radius);
  padding: var(--space-3);
}

.bo-card-title {
  font-size: var(--text-sm);
  color: var(--text-muted);
  margin-bottom: var(--space-2);
}

.bo-card-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}

.bo-row {
  display: grid;
  grid-template-columns: 1fr 2fr;
  gap: var(--space-2);
  align-items: center;
  font-size: var(--text-sm);
}

.bo-row-label {
  color: var(--text-muted);
}

.bo-row-value {
  color: var(--text);
}

.bo-row-value.is-running {
  color: var(--success);
  font-weight: var(--font-semibold);
}

.bo-card-empty {
  color: var(--text-muted);
  font-style: italic;
}

.bo-card-degraded {
  margin-top: var(--space-2);
  color: var(--danger);
  font-size: var(--text-sm);
}

/* 指标行（自检） */
.bo-metrics {
  display: flex;
  flex-direction: column;
}

.bo-metric {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-1) 0;
  border-bottom: 1px solid var(--border-muted);
  font-size: var(--text-sm);
}

.bo-metric:last-child {
  border-bottom: none;
}

.bo-metric-label {
  color: var(--text-muted);
}

.bo-metric-value {
  color: var(--text);
  font-size: var(--text-lg);
  font-weight: var(--font-semibold);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.bo-footer {
  margin-top: var(--space-4);
  padding-top: var(--space-3);
  border-top: 1px solid var(--border);
  color: var(--text-muted);
  font-size: var(--text-sm);
}

.bo-footer p {
  margin: var(--space-1) 0;
}

@media (max-width: 768px) {
  .bo-cards,
  .bo-pies-grid,
  .bo-metrics-grid {
    grid-template-columns: 1fr;
  }

  .bo-header {
    flex-direction: column;
    align-items: stretch;
  }

  .bo-header-actions {
    justify-content: flex-end;
  }

  .bo-trend-point {
    flex-direction: column;
    align-items: stretch;
  }

  .bo-trend-time {
    width: auto;
  }
}
</style>
