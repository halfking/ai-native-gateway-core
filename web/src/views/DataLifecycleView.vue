<template>
  <div class="data-lifecycle-view">
    <div class="page-header">
      <h2 class="page-title">数据生命周期管理</h2>
      <div class="header-actions">
        <button class="btn btn-ghost btn-sm" @click="loadStats" :disabled="isLoading">
          {{ isLoading ? '加载中…' : '刷新' }}
        </button>
      </div>
    </div>

    <!-- 统计面板 -->
    <div class="stats-row" v-if="stats">
      <div class="stat-card">
        <div class="stat-label">总数据量</div>
        <div class="stat-value">{{ formatNumber(stats.total_rows) }}</div>
        <div class="stat-meta">{{ stats.total_size_human }}</div>
      </div>

      <div class="stat-card segment-hot">
        <div class="stat-label">热数据 (0-7天)</div>
        <div class="stat-value">{{ formatNumber(stats.hot_data?.rows || 0) }}</div>
        <div class="stat-meta">
          {{ stats.hot_data?.size_human || '0 B' }}
          <span class="stat-percent">({{ (stats.hot_data?.percent_of_total || 0).toFixed(1) }}%)</span>
        </div>
      </div>

      <div class="stat-card segment-warm">
        <div class="stat-label">温数据 (7-30天)</div>
        <div class="stat-value">{{ formatNumber(stats.warm_data?.rows || 0) }}</div>
        <div class="stat-meta">
          {{ stats.warm_data?.size_human || '0 B' }}
          <span class="stat-percent">({{ (stats.warm_data?.percent_of_total || 0).toFixed(1) }}%)</span>
        </div>
      </div>

      <div class="stat-card segment-cold">
        <div class="stat-label">冷数据 (30-90天)</div>
        <div class="stat-value">{{ formatNumber(stats.cold_data?.rows || 0) }}</div>
        <div class="stat-meta">
          {{ stats.cold_data?.size_human || '0 B' }}
          <span class="stat-percent">({{ (stats.cold_data?.percent_of_total || 0).toFixed(1) }}%)</span>
        </div>
      </div>

      <div class="stat-card segment-expired">
        <div class="stat-label">过期数据 (>90天)</div>
        <div class="stat-value">{{ formatNumber(stats.expired_data?.rows || 0) }}</div>
        <div class="stat-meta">
          {{ stats.expired_data?.size_human || '0 B' }}
          <span class="stat-percent">({{ (stats.expired_data?.percent_of_total || 0).toFixed(1) }}%)</span>
        </div>
      </div>
    </div>

    <!-- 数据分布图表 + 增长趋势 -->
    <div class="charts-row" v-if="stats">
      <div class="card chart-card">
        <h3 class="card-title">数据分布</h3>
        <div class="chart-container">
          <canvas ref="distributionChart"></canvas>
        </div>
      </div>

      <div class="card trend-card">
        <h3 class="card-title">增长趋势（最近7天）</h3>
        <div class="trend-table-wrap" v-if="stats.growth_trend.length">
          <table class="data-table">
            <thead>
              <tr>
                <th>日期</th>
                <th>请求数</th>
                <th>压缩数</th>
                <th>压缩率</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="trend in stats.growth_trend" :key="trend.date">
                <td>{{ trend.date }}</td>
                <td>{{ formatNumber(trend.requests) }}</td>
                <td>{{ formatNumber(trend.compressed) }}</td>
                <td>
                  <span class="rate-badge" :class="{ high: trend.compression_rate > 50 }">
                    {{ trend.compression_rate.toFixed(1) }}%
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="empty-hint">暂无趋势数据</div>
      </div>
    </div>

    <!-- 清理操作 -->
    <div class="card cleanup-card">
      <h3 class="card-title">清理操作</h3>

      <div class="cleanup-form">
        <div class="form-row">
          <label class="form-label">操作类型：</label>
          <select v-model="cleanupForm.action" class="form-select">
            <option value="archive">归档（推荐）</option>
            <option value="delete">删除（危险）</option>
            <option value="trim">裁剪（待实施）</option>
          </select>
        </div>

        <div class="form-row form-row-dates">
          <div class="date-group">
            <label class="form-label">起始日期：</label>
            <input type="date" v-model="cleanupForm.from" class="form-input" />
          </div>
          <div class="date-group">
            <label class="form-label">结束日期：</label>
            <input type="date" v-model="cleanupForm.to" class="form-input" />
          </div>
        </div>

        <div class="form-actions">
          <button class="btn btn-primary btn-sm" @click="previewCleanup" :disabled="isLoading">
            {{ isLoading ? '加载中…' : '预览影响' }}
          </button>
          <button class="btn btn-ghost btn-sm" @click="executeCleanup" :disabled="isLoading || !previewResult">
            执行清理
          </button>
        </div>
      </div>

      <!-- 预览结果 -->
      <div v-if="previewResult" class="preview-result">
        <div class="preview-item">
          <span class="preview-label">影响行数：</span>
          <span class="preview-value">{{ formatNumber(previewResult.affected_rows) }} 行</span>
        </div>
        <div class="preview-item">
          <span class="preview-label">预计释放：</span>
          <span class="preview-value highlight">{{ previewResult.estimated_freed_human }}</span>
        </div>
        <div v-if="previewResult.warning_message" class="preview-warning">
          ⚠️ {{ previewResult.warning_message }}
        </div>
      </div>
    </div>

    <!-- 租户统计（Top 10） -->
    <div class="card tenant-card" v-if="stats && stats.by_tenant.length">
      <h3 class="card-title">租户数据统计（Top 10）</h3>
      <div class="tenant-table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>租户 ID</th>
              <th>行数</th>
              <th>占用空间</th>
              <th>占比</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="tenant in stats.by_tenant" :key="tenant.tenant_id">
              <td><code class="tenant-code">{{ tenant.tenant_id }}</code></td>
              <td>{{ formatNumber(tenant.rows) }}</td>
              <td>{{ tenant.size_human }}</td>
              <td>
                <div class="tenant-bar-track">
                  <div
                    class="tenant-bar-fill"
                    :style="{ width: getTenantPercent(tenant.rows) + '%' }"
                  ></div>
                  <span class="tenant-bar-text">{{ getTenantPercent(tenant.rows).toFixed(1) }}%</span>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, nextTick } from 'vue'
import { Chart, ChartConfiguration, registerables } from 'chart.js'
import {
  dataLifecycleStats,
  dataLifecycleCleanupPreview,
  type DataLifecycleStatsResponse,
  type DataSegment,
  type TenantDataStats,
  type DailyGrowth,
} from '../api'

Chart.register(...registerables)

interface Stats extends DataLifecycleStatsResponse {}

interface CleanupPreview {
  affected_rows: number
  estimated_freed_bytes: number
  estimated_freed_human: string
  warning_message?: string
}

const stats = ref<Stats | null>(null)

const cleanupForm = ref({
  action: 'archive',
  from: '',
  to: '',
})

const previewResult = ref<CleanupPreview | null>(null)
const isLoading = ref(false)
const distributionChart = ref<HTMLCanvasElement | null>(null)
let chartInstance: Chart | null = null

function formatNumber(n: number): string {
  return n.toLocaleString('zh-CN')
}

function getTenantPercent(rows: number): number {
  if (!stats.value || stats.value.total_rows === 0) return 0
  return (rows / stats.value.total_rows) * 100
}

async function loadStats() {
  isLoading.value = true
  try {
    const data = await dataLifecycleStats()
    stats.value = data as Stats

    await nextTick()
    renderChart()
  } catch (error) {
    console.error('Failed to load stats:', error)
  } finally {
    isLoading.value = false
  }
}

function renderChart() {
  if (!distributionChart.value || !stats.value) return

  if (chartInstance) {
    chartInstance.destroy()
  }

  const ctx = distributionChart.value.getContext('2d')
  if (!ctx) return

  const data = [
    stats.value.hot_data?.rows || 0,
    stats.value.warm_data?.rows || 0,
    stats.value.cold_data?.rows || 0,
    stats.value.expired_data?.rows || 0,
  ]

  const config: ChartConfiguration = {
    type: 'doughnut',
    data: {
      labels: ['热数据 (0-7天)', '温数据 (7-30天)', '冷数据 (30-90天)', '过期数据 (>90天)'],
      datasets: [
        {
          data,
          backgroundColor: [
            'rgba(52, 211, 153, 0.7)',
            'rgba(251, 191, 36, 0.7)',
            'rgba(96, 165, 250, 0.7)',
            'rgba(248, 113, 113, 0.7)',
          ],
          borderColor: [
            'rgba(52, 211, 153, 1)',
            'rgba(251, 191, 36, 1)',
            'rgba(96, 165, 250, 1)',
            'rgba(248, 113, 113, 1)',
          ],
          borderWidth: 2,
        },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: {
          position: 'bottom',
          labels: {
            color: '#e6edf3',
            font: { size: 12 },
            padding: 12,
          },
        },
        tooltip: {
          backgroundColor: 'rgba(15, 17, 23, 0.95)',
          titleColor: '#e6edf3',
          bodyColor: '#e6edf3',
          borderColor: 'rgba(99, 102, 241, 0.5)',
          borderWidth: 1,
          callbacks: {
            label: (context) => {
              const label = context.label || ''
              const value = context.parsed
              const total = data.reduce((a, b) => a + b, 0)
              const percentage = total > 0 ? (value / total * 100).toFixed(1) : '0.0'
              return `${label}: ${formatNumber(value)} 行 (${percentage}%)`
            },
          },
        },
      },
    },
  }

  chartInstance = new Chart(ctx, config)
}

async function previewCleanup() {
  if (!cleanupForm.value.from || !cleanupForm.value.to) {
    alert('请选择起始日期和结束日期')
    return
  }

  isLoading.value = true
  try {
    const result = await dataLifecycleCleanupPreview(
      cleanupForm.value.action,
      cleanupForm.value.from,
      cleanupForm.value.to
    )
    previewResult.value = result
  } catch (error) {
    console.error('Preview failed:', error)
    alert('预览失败')
  } finally {
    isLoading.value = false
  }
}

async function executeCleanup() {
  if (!previewResult.value) {
    alert('请先预览影响')
    return
  }

  const confirmed = confirm(
    `确认${cleanupForm.value.action === 'delete' ? '删除' : '归档'} ${formatNumber(previewResult.value.affected_rows)} 行数据？\n` +
    `预计释放空间: ${previewResult.value.estimated_freed_human}\n\n` +
    `此操作不可逆！`
  )

  if (!confirmed) return

  alert('清理功能将在下一阶段实现。请使用命令行脚本执行清理操作。')
}

onMounted(() => {
  loadStats()

  const now = new Date()
  const from = new Date(now.getTime() - 90 * 24 * 60 * 60 * 1000)
  const to = new Date(now.getTime() - 30 * 24 * 60 * 60 * 1000)
  cleanupForm.value.from = from.toISOString().split('T')[0]
  cleanupForm.value.to = to.toISOString().split('T')[0]
})
</script>

<style scoped>
.data-lifecycle-view {
  padding: 16px;
  max-width: 1400px;
  margin: 0 auto;
}

.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 20px;
  flex-wrap: wrap;
  gap: 12px;
}

.page-title {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
  color: var(--text-primary, #e6edf3);
}

.header-actions {
  display: flex;
  gap: 8px;
}

/* ===== 统计卡片 ===== */
.stats-row {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}

.stat-card {
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 10px;
  padding: 16px;
  position: relative;
  overflow: hidden;
}

.stat-card::before {
  content: '';
  position: absolute;
  top: 0;
  left: 0;
  width: 3px;
  height: 100%;
  background: var(--text-secondary, #6b7280);
}

.stat-card.segment-hot::before { background: #34d399; }
.stat-card.segment-warm::before { background: #fbbf24; }
.stat-card.segment-cold::before { background: #60a5fa; }
.stat-card.segment-expired::before { background: #f87171; }

.stat-label {
  font-size: 12px;
  color: var(--text-secondary, #8b949e);
  margin-bottom: 6px;
}

.stat-value {
  font-size: 22px;
  font-weight: 700;
  color: var(--text-primary, #e6edf3);
  margin-bottom: 4px;
  font-variant-numeric: tabular-nums;
}

.stat-meta {
  font-size: 11px;
  color: var(--text-secondary, #8b949e);
}

.stat-percent {
  color: var(--text-tertiary, #6b7280);
  margin-left: 4px;
}

/* ===== 卡片通用 ===== */
.card {
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 10px;
  padding: 16px;
  margin-bottom: 16px;
}

.card-title {
  margin: 0 0 12px;
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary, #e6edf3);
  display: flex;
  align-items: center;
  gap: 6px;
}

/* ===== 图表行 ===== */
.charts-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  margin-bottom: 16px;
}

.chart-card {
  margin-bottom: 0;
}

.chart-container {
  height: 240px;
}

/* ===== 趋势表 ===== */
.trend-table-wrap {
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
  color: var(--text-secondary, #8b949e);
  font-weight: 500;
  border-bottom: 1px solid var(--border, #30363d);
  white-space: nowrap;
}

.data-table td {
  padding: 8px 10px;
  border-bottom: 1px solid var(--border, #30363d);
  color: var(--text-primary, #e6edf3);
  font-variant-numeric: tabular-nums;
}

.data-table tr:last-child td {
  border-bottom: none;
}

.rate-badge {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 4px;
  background: rgba(99, 102, 241, 0.15);
  color: var(--accent-h, #818cf8);
  font-weight: 500;
}

.rate-badge.high {
  background: rgba(52, 211, 153, 0.15);
  color: #34d399;
}

/* ===== 清理表单 ===== */
.cleanup-form {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.form-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.form-row-dates {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}

.date-group {
  display: flex;
  align-items: center;
  gap: 8px;
}

.form-label {
  font-size: 13px;
  color: var(--text-secondary, #8b949e);
  white-space: nowrap;
  min-width: 70px;
}

.form-select, .form-input {
  flex: 1;
  padding: 6px 10px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  color: var(--text-primary, #e6edf3);
  font-size: 13px;
}

.form-select:focus, .form-input:focus {
  outline: none;
  border-color: var(--accent, #6366f1);
}

.form-actions {
  display: flex;
  gap: 8px;
  margin-top: 4px;
}

.btn {
  padding: 6px 14px;
  border-radius: 6px;
  border: 1px solid transparent;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.15s;
}

.btn-sm {
  padding: 4px 10px;
  font-size: 12px;
}

.btn-primary {
  background: var(--accent, #6366f1);
  color: #fff;
}

.btn-primary:hover:not(:disabled) {
  background: var(--accent-h, #818cf8);
}

.btn-ghost {
  background: transparent;
  border-color: var(--border, #30363d);
  color: var(--text-primary, #e6edf3);
}

.btn-ghost:hover:not(:disabled) {
  background: var(--bg-hover, #21262d);
  border-color: var(--text-secondary, #8b949e);
}

.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* ===== 预览结果 ===== */
.preview-result {
  margin-top: 12px;
  padding: 12px;
  background: rgba(99, 102, 241, 0.08);
  border: 1px solid rgba(99, 102, 241, 0.25);
  border-radius: 6px;
}

.preview-item {
  display: flex;
  justify-content: space-between;
  margin-bottom: 6px;
  font-size: 13px;
}

.preview-item:last-child {
  margin-bottom: 0;
}

.preview-label {
  color: var(--text-secondary, #8b949e);
}

.preview-value {
  color: var(--text-primary, #e6edf3);
  font-weight: 500;
  font-variant-numeric: tabular-nums;
}

.preview-value.highlight {
  color: #fbbf24;
  font-weight: 600;
}

.preview-warning {
  margin-top: 8px;
  padding: 6px 10px;
  background: rgba(251, 191, 36, 0.1);
  border-left: 2px solid #fbbf24;
  color: #fbbf24;
  font-size: 12px;
  border-radius: 4px;
}

/* ===== 租户表 ===== */
.tenant-table-wrap {
  overflow-x: auto;
}

.tenant-code {
  font-family: ui-monospace, SFMono-Regular, monospace;
  font-size: 12px;
  padding: 2px 6px;
  background: var(--bg, #0f1117);
  border-radius: 4px;
  color: var(--accent-h, #818cf8);
}

.tenant-bar-track {
  position: relative;
  width: 100%;
  height: 18px;
  background: var(--bg, #0f1117);
  border-radius: 4px;
  overflow: hidden;
}

.tenant-bar-fill {
  position: absolute;
  top: 0;
  left: 0;
  height: 100%;
  background: linear-gradient(90deg, rgba(99, 102, 241, 0.5), rgba(99, 102, 241, 0.8));
  transition: width 0.3s;
}

.tenant-bar-text {
  position: absolute;
  top: 0;
  left: 8px;
  line-height: 18px;
  font-size: 11px;
  color: var(--text-primary, #e6edf3);
  font-weight: 500;
  font-variant-numeric: tabular-nums;
}

/* ===== 空状态 ===== */
.empty-hint {
  text-align: center;
  padding: 32px;
  color: var(--text-secondary, #8b949e);
  font-size: 13px;
}

/* ===== 响应式 ===== */
@media (max-width: 800px) {
  .stats-row {
    grid-template-columns: repeat(2, 1fr);
  }
  .charts-row {
    grid-template-columns: 1fr;
  }
  .form-row-dates {
    grid-template-columns: 1fr;
  }
}
</style>
