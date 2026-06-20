<template>
  <div class="data-lifecycle-view">
    <h1 class="page-title">数据生命周期管理</h1>

    <!-- 统计面板 -->
    <div class="stats-panel">
      <div class="stat-card">
        <div class="stat-label">总数据量</div>
        <div class="stat-value">{{ formatNumber(stats.total_rows) }} 行</div>
        <div class="stat-meta">{{ stats.total_size_human }}</div>
      </div>

      <div class="stat-card hot">
        <div class="stat-label">热数据 (0-7天)</div>
        <div class="stat-value">{{ formatNumber(stats.hot_data?.rows || 0) }} 行</div>
        <div class="stat-meta">
          {{ stats.hot_data?.size_human || '0 B' }}
          ({{ (stats.hot_data?.percent_of_total || 0).toFixed(1) }}%)
        </div>
      </div>

      <div class="stat-card warm">
        <div class="stat-label">温数据 (7-30天)</div>
        <div class="stat-value">{{ formatNumber(stats.warm_data?.rows || 0) }} 行</div>
        <div class="stat-meta">
          {{ stats.warm_data?.size_human || '0 B' }}
          ({{ (stats.warm_data?.percent_of_total || 0).toFixed(1) }}%)
        </div>
      </div>

      <div class="stat-card cold">
        <div class="stat-label">冷数据 (30-90天)</div>
        <div class="stat-value">{{ formatNumber(stats.cold_data?.rows || 0) }} 行</div>
        <div class="stat-meta">
          {{ stats.cold_data?.size_human || '0 B' }}
          ({{ (stats.cold_data?.percent_of_total || 0).toFixed(1) }}%)
        </div>
      </div>

      <div class="stat-card expired">
        <div class="stat-label">过期数据 (>90天)</div>
        <div class="stat-value">{{ formatNumber(stats.expired_data?.rows || 0) }} 行</div>
        <div class="stat-meta">
          {{ stats.expired_data?.size_human || '0 B' }}
          ({{ (stats.expired_data?.percent_of_total || 0).toFixed(1) }}%)
        </div>
      </div>
    </div>

    <!-- 数据分布图表 -->
    <div class="chart-section">
      <h2>数据分布</h2>
      <div class="chart-container">
        <canvas ref="distributionChart"></canvas>
      </div>
    </div>

    <!-- 增长趋势 -->
    <div class="trend-section">
      <h2>增长趋势（最近7天）</h2>
      <div class="trend-table">
        <table>
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
              <td>{{ trend.compression_rate.toFixed(1) }}%</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 清理操作 -->
    <div class="cleanup-section">
      <h2>清理操作</h2>
      
      <div class="cleanup-form">
        <div class="form-row">
          <label>操作类型：</label>
          <select v-model="cleanupForm.action">
            <option value="archive">归档（Archive）</option>
            <option value="delete">删除（Delete）</option>
            <option value="trim">裁剪（Trim）</option>
          </select>
        </div>

        <div class="form-row">
          <label>起始日期：</label>
          <input type="date" v-model="cleanupForm.from" />
        </div>

        <div class="form-row">
          <label>结束日期：</label>
          <input type="date" v-model="cleanupForm.to" />
        </div>

        <div class="form-actions">
          <button @click="previewCleanup" :disabled="isLoading" class="btn-preview">
            {{ isLoading ? '加载中...' : '预览影响' }}
          </button>
          <button @click="executeCleanup" :disabled="isLoading || !previewResult" class="btn-execute">
            执行清理
          </button>
        </div>
      </div>

      <!-- 预览结果 -->
      <div v-if="previewResult" class="preview-result">
        <h3>预览结果</h3>
        <div class="preview-item">
          <span class="preview-label">影响行数：</span>
          <span class="preview-value">{{ formatNumber(previewResult.affected_rows) }} 行</span>
        </div>
        <div class="preview-item">
          <span class="preview-label">预计释放：</span>
          <span class="preview-value">{{ previewResult.estimated_freed_human }}</span>
        </div>
        <div v-if="previewResult.warning_message" class="preview-warning">
          ⚠️ {{ previewResult.warning_message }}
        </div>
      </div>
    </div>

    <!-- 租户统计（Top 10） -->
    <div class="tenant-section">
      <h2>租户数据统计（Top 10）</h2>
      <div class="tenant-table">
        <table>
          <thead>
            <tr>
              <th>租户 ID</th>
              <th>行数</th>
              <th>占用空间</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="tenant in stats.by_tenant" :key="tenant.tenant_id">
              <td>{{ tenant.tenant_id }}</td>
              <td>{{ formatNumber(tenant.rows) }}</td>
              <td>{{ tenant.size_human }}</td>
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
import { dataLifecycleStats, dataLifecycleCleanupPreview } from '../api'

Chart.register(...registerables)

interface DataSegment {
  rows: number
  size_bytes: number
  size_human: string
  days: number
  percent_of_total: number
}

interface TenantStats {
  tenant_id: string
  rows: number
  size_bytes: number
  size_human: string
}

interface GrowthTrend {
  date: string
  requests: number
  compressed: number
  compression_rate: number
}

interface Stats {
  total_rows: number
  total_size_bytes: number
  total_size_human: string
  hot_data: DataSegment | null
  warm_data: DataSegment | null
  cold_data: DataSegment | null
  expired_data: DataSegment | null
  by_tenant: TenantStats[]
  growth_trend: GrowthTrend[]
}

interface CleanupPreview {
  affected_rows: number
  estimated_freed_bytes: number
  estimated_freed_human: string
  warning_message?: string
}

const stats = ref<Stats>({
  total_rows: 0,
  total_size_bytes: 0,
  total_size_human: '0 B',
  hot_data: null,
  warm_data: null,
  cold_data: null,
  expired_data: null,
  by_tenant: [],
  growth_trend: []
})

const cleanupForm = ref({
  action: 'archive',
  from: '',
  to: ''
})

const previewResult = ref<CleanupPreview | null>(null)
const isLoading = ref(false)
const distributionChart = ref<HTMLCanvasElement | null>(null)
let chartInstance: Chart | null = null

function formatNumber(n: number): string {
  return n.toLocaleString('zh-CN')
}

async function loadStats() {
  try {
    const data = await dataLifecycleStats()
    stats.value = data
    
    // 渲染图表
    await nextTick()
    renderChart()
  } catch (error) {
    console.error('Failed to load stats:', error)
    alert('加载数据失败')
  }
}

function renderChart() {
  if (!distributionChart.value) return
  
  if (chartInstance) {
    chartInstance.destroy()
  }

  const ctx = distributionChart.value.getContext('2d')
  if (!ctx) return

  const data = [
    stats.value.hot_data?.rows || 0,
    stats.value.warm_data?.rows || 0,
    stats.value.cold_data?.rows || 0,
    stats.value.expired_data?.rows || 0
  ]

  const config: ChartConfiguration = {
    type: 'doughnut',
    data: {
      labels: ['热数据 (0-7天)', '温数据 (7-30天)', '冷数据 (30-90天)', '过期数据 (>90天)'],
      datasets: [{
        data,
        backgroundColor: [
          'rgba(52, 211, 153, 0.8)',
          'rgba(251, 191, 36, 0.8)',
          'rgba(96, 165, 250, 0.8)',
          'rgba(248, 113, 113, 0.8)'
        ],
        borderColor: [
          'rgba(52, 211, 153, 1)',
          'rgba(251, 191, 36, 1)',
          'rgba(96, 165, 250, 1)',
          'rgba(248, 113, 113, 1)'
        ],
        borderWidth: 2
      }]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: {
          position: 'bottom'
        },
        tooltip: {
          callbacks: {
            label: (context) => {
              const label = context.label || ''
              const value = context.parsed
              const total = data.reduce((a, b) => a + b, 0)
              const percentage = total > 0 ? (value / total * 100).toFixed(1) : '0.0'
              return `${label}: ${formatNumber(value)} 行 (${percentage}%)`
            }
          }
        }
      }
    }
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
  // TODO: 实现实际的清理执行
}

onMounted(() => {
  loadStats()
  
  // 默认设置日期范围（30-90天前）
  const now = new Date()
  const from = new Date(now.getTime() - 90 * 24 * 60 * 60 * 1000)
  const to = new Date(now.getTime() - 30 * 24 * 60 * 60 * 1000)
  cleanupForm.value.from = from.toISOString().split('T')[0]
  cleanupForm.value.to = to.toISOString().split('T')[0]
})
</script>

<style scoped>
.data-lifecycle-view {
  padding: 20px;
  max-width: 1400px;
  margin: 0 auto;
}

.page-title {
  font-size: 24px;
  font-weight: 600;
  margin-bottom: 24px;
  color: #1f2937;
}

.stats-panel {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 16px;
  margin-bottom: 32px;
}

.stat-card {
  background: white;
  border-radius: 8px;
  padding: 20px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.1);
  border-left: 4px solid #6b7280;
}

.stat-card.hot { border-left-color: #34d399; }
.stat-card.warm { border-left-color: #fbbf24; }
.stat-card.cold { border-left-color: #60a5fa; }
.stat-card.expired { border-left-color: #f87171; }

.stat-label {
  font-size: 14px;
  color: #6b7280;
  margin-bottom: 8px;
}

.stat-value {
  font-size: 24px;
  font-weight: 600;
  color: #1f2937;
  margin-bottom: 4px;
}

.stat-meta {
  font-size: 12px;
  color: #9ca3af;
}

.chart-section, .trend-section, .cleanup-section, .tenant-section {
  background: white;
  border-radius: 8px;
  padding: 24px;
  margin-bottom: 24px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.1);
}

h2 {
  font-size: 18px;
  font-weight: 600;
  margin-bottom: 16px;
  color: #1f2937;
}

.chart-container {
  height: 300px;
}

.trend-table table, .tenant-table table {
  width: 100%;
  border-collapse: collapse;
}

.trend-table th, .trend-table td,
.tenant-table th, .tenant-table td {
  padding: 12px;
  text-align: left;
  border-bottom: 1px solid #e5e7eb;
}

.trend-table th, .tenant-table th {
  background: #f9fafb;
  font-weight: 600;
  color: #374151;
}

.cleanup-form {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.form-row {
  display: flex;
  align-items: center;
  gap: 12px;
}

.form-row label {
  min-width: 100px;
  font-weight: 500;
  color: #374151;
}

.form-row select, .form-row input {
  padding: 8px 12px;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  font-size: 14px;
}

.form-actions {
  display: flex;
  gap: 12px;
  margin-top: 8px;
}

.btn-preview, .btn-execute {
  padding: 10px 20px;
  border: none;
  border-radius: 6px;
  font-size: 14px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.2s;
}

.btn-preview {
  background: #3b82f6;
  color: white;
}

.btn-preview:hover:not(:disabled) {
  background: #2563eb;
}

.btn-execute {
  background: #ef4444;
  color: white;
}

.btn-execute:hover:not(:disabled) {
  background: #dc2626;
}

.btn-preview:disabled, .btn-execute:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.preview-result {
  margin-top: 20px;
  padding: 16px;
  background: #f0fdf4;
  border: 1px solid #86efac;
  border-radius: 6px;
}

.preview-result h3 {
  font-size: 16px;
  font-weight: 600;
  margin-bottom: 12px;
  color: #166534;
}

.preview-item {
  display: flex;
  justify-content: space-between;
  margin-bottom: 8px;
}

.preview-label {
  font-weight: 500;
  color: #166534;
}

.preview-value {
  font-weight: 600;
  color: #15803d;
}

.preview-warning {
  margin-top: 12px;
  padding: 8px 12px;
  background: #fef3c7;
  border-left: 3px solid #f59e0b;
  color: #92400e;
  font-size: 14px;
}
</style>
