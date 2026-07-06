<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { Chart, registerables } from 'chart.js'
import {
  getCostTrend,
  getPeriodCompare,
  getCacheEconomics,
  type CostTrendResponse,
  type PeriodCompareResponse,
  type CacheEconomicsResponse,
  type CostTrendGroupBy,
} from '../../api/usage'

// 注册 Chart.js 组件
Chart.register(...registerables)

// 状态
const loading = ref(false)
const error = ref('')

// 归因维度选择器
const attributionDimension = ref<CostTrendGroupBy>('model')
const costTrendData = ref<CostTrendResponse | null>(null)

// 同比环比数据
const currentPeriod = ref('')
const previousPeriod = ref('')
const periodCompareData = ref<PeriodCompareResponse | null>(null)

// 缓存经济学数据
const cacheEconomicsData = ref<CacheEconomicsResponse | null>(null)

// 图表实例
let pieChartInstance: Chart | null = null
let trendChartInstance: Chart | null = null

// 计算当前月份和上个月份
const getCurrentPeriod = () => {
  const now = new Date()
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}`
}

const getPreviousPeriod = () => {
  const now = new Date()
  const prev = new Date(now.getFullYear(), now.getMonth() - 1, 1)
  return `${prev.getFullYear()}-${String(prev.getMonth() + 1).padStart(2, '0')}`
}

// 初始化周期
currentPeriod.value = getCurrentPeriod()
previousPeriod.value = getPreviousPeriod()

// 格式化金额
const formatCurrency = (value: number) => {
  return `$${value.toFixed(2)}`
}

// 格式化百分比
const formatPercent = (value: number) => {
  return `${value.toFixed(1)}%`
}

// 格式化数字
const formatNumber = (value: number) => {
  return value.toLocaleString()
}

// 趋势类名
const trendClass = computed(() => {
  if (!periodCompareData.value) return ''
  const trend = periodCompareData.value.trend
  if (trend === 'up') return 'trend-up'
  if (trend === 'down') return 'trend-down'
  return 'trend-flat'
})

// 趋势图标
const trendIcon = computed(() => {
  if (!periodCompareData.value) return ''
  const trend = periodCompareData.value.trend
  if (trend === 'up') return '↑'
  if (trend === 'down') return '↓'
  return '→'
})

// API 调用
const fetchCostTrend = async () => {
  try {
    costTrendData.value = await getCostTrend(attributionDimension.value)
  } catch (e) {
    console.error('Cost trend fetch error:', e)
    error.value = e instanceof Error ? e.message : 'Failed to fetch cost trend'
  }
}

const fetchPeriodCompare = async () => {
  try {
    periodCompareData.value = await getPeriodCompare(currentPeriod.value, previousPeriod.value)
  } catch (e) {
    console.error('Period compare fetch error:', e)
    error.value = e instanceof Error ? e.message : 'Failed to fetch period comparison'
  }
}

const fetchCacheEconomics = async () => {
  try {
    cacheEconomicsData.value = await getCacheEconomics()
  } catch (e) {
    console.error('Cache economics fetch error:', e)
    error.value = e instanceof Error ? e.message : 'Failed to fetch cache economics'
  }
}

const loadAll = async () => {
  loading.value = true
  error.value = ''
  try {
    await Promise.all([
      fetchCostTrend(),
      fetchPeriodCompare(),
      fetchCacheEconomics(),
    ])
  } finally {
    loading.value = false
  }
}

// 渲染环形图
const renderPieChart = () => {
  if (!costTrendData.value || !costTrendData.value.entries.length) return

  const canvas = document.getElementById('costPieChart') as HTMLCanvasElement
  if (!canvas) return

  // 销毁旧实例
  if (pieChartInstance) {
    pieChartInstance.destroy()
  }

  const ctx = canvas.getContext('2d')
  if (!ctx) return

  const data = costTrendData.value.entries
  const labels = data.map(e => e.dimension_value)
  const values = data.map(e => e.total_cost_usd)
  const colors = [
    '#6366f1', '#8b5cf6', '#ec4899', '#f43f5e', '#f97316',
    '#f59e0b', '#84cc16', '#22c55e', '#14b8a6', '#06b6d4',
  ]

  // 如果有 other 成本，添加到图表
  if (costTrendData.value.other_cost > 0) {
    labels.push('其他')
    values.push(costTrendData.value.other_cost)
  }

  pieChartInstance = new Chart(ctx, {
    type: 'doughnut',
    data: {
      labels,
      datasets: [{
        data: values,
        backgroundColor: colors.slice(0, labels.length),
        borderWidth: 2,
        borderColor: '#fff',
      }],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: {
          position: 'right',
          labels: {
            boxWidth: 12,
            padding: 10,
            font: { size: 11 },
          },
        },
        tooltip: {
          callbacks: {
            label: (context) => {
              const label = context.label || ''
              const value = context.parsed
              const percentage = ((value / costTrendData.value!.total_cost) * 100).toFixed(1)
              return `${label}: $${value.toFixed(2)} (${percentage}%)`
            },
          },
        },
      },
    },
  })
}

// 渲染趋势图（简化版，显示按维度的成本对比）
const renderTrendChart = () => {
  if (!costTrendData.value || !costTrendData.value.entries.length) return

  const canvas = document.getElementById('costTrendChart') as HTMLCanvasElement
  if (!canvas) return

  // 销毁旧实例
  if (trendChartInstance) {
    trendChartInstance.destroy()
  }

  const ctx = canvas.getContext('2d')
  if (!ctx) return

  const data = costTrendData.value.entries.slice(0, 10) // 前10个
  const labels = data.map(e => e.dimension_value)
  const inputCosts = data.map(e => e.input_cost_usd)
  const outputCosts = data.map(e => e.output_cost_usd)

  trendChartInstance = new Chart(ctx, {
    type: 'bar',
    data: {
      labels,
      datasets: [
        {
          label: '输入成本',
          data: inputCosts,
          backgroundColor: '#6366f1',
          stack: 'stack1',
        },
        {
          label: '输出成本',
          data: outputCosts,
          backgroundColor: '#8b5cf6',
          stack: 'stack1',
        },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      scales: {
        x: {
          stacked: true,
          ticks: {
            maxRotation: 45,
            minRotation: 45,
            font: { size: 10 },
          },
        },
        y: {
          stacked: true,
          beginAtZero: true,
          ticks: {
            callback: (value) => `$${value}`,
          },
        },
      },
      plugins: {
        legend: {
          position: 'top',
          labels: {
            boxWidth: 12,
            padding: 10,
            font: { size: 11 },
          },
        },
        tooltip: {
          callbacks: {
            label: (context) => {
              return `${context.dataset.label}: $${context.parsed.y.toFixed(2)}`
            },
          },
        },
      },
    },
  })
}

// 监听归因维度变化
watch(attributionDimension, async () => {
  await fetchCostTrend()
  renderPieChart()
  renderTrendChart()
})

// 监听周期变化
watch([currentPeriod, previousPeriod], async () => {
  await fetchPeriodCompare()
})

// 加载数据后渲染图表
watch(costTrendData, () => {
  setTimeout(() => {
    renderPieChart()
    renderTrendChart()
  }, 100)
})

onMounted(() => {
  loadAll()
})
</script>

<template>
  <div class="usage-cost-view">
    <div class="page-header">
      <h2>用量成本</h2>
      <div class="page-header-actions">
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="loadAll">
          {{ loading ? '加载中…' : '刷新' }}
        </button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <!-- 同比环比对比卡片 -->
    <div class="section">
      <h3 class="section-title">成本对比</h3>
      <div class="compare-cards">
        <div class="card compare-card">
          <div class="compare-period-selector">
            <label>本期：<input v-model="currentPeriod" type="month" class="period-input" /></label>
            <label>对比：<input v-model="previousPeriod" type="month" class="period-input" /></label>
          </div>

          <div v-if="periodCompareData" class="compare-content">
            <div class="compare-row">
              <div class="compare-col">
                <div class="compare-label">本期 ({{ periodCompareData.current.period }})</div>
                <div class="compare-value">{{ formatCurrency(periodCompareData.current.total_cost_usd) }}</div>
                <div class="compare-meta">
                  {{ formatNumber(periodCompareData.current.total_requests) }} 次请求
                  · {{ periodCompareData.current.unique_models }} 个模型
                </div>
              </div>

              <div class="compare-col">
                <div class="compare-label">对比期 ({{ periodCompareData.previous.period }})</div>
                <div class="compare-value">{{ formatCurrency(periodCompareData.previous.total_cost_usd) }}</div>
                <div class="compare-meta">
                  {{ formatNumber(periodCompareData.previous.total_requests) }} 次请求
                  · {{ periodCompareData.previous.unique_models }} 个模型
                </div>
              </div>

              <div class="compare-col compare-change">
                <div class="compare-label">变化</div>
                <div class="compare-value" :class="trendClass">
                  {{ trendIcon }} {{ formatPercent(Math.abs(periodCompareData.change_pct)) }}
                </div>
                <div class="compare-meta">
                  {{ periodCompareData.change_abs >= 0 ? '+' : '' }}{{ formatCurrency(periodCompareData.change_abs) }}
                  <span v-if="periodCompareData.significant" class="badge badge-warning">显著</span>
                </div>
              </div>
            </div>
          </div>
          <div v-else-if="loading" class="empty-state">加载中…</div>
        </div>
      </div>
    </div>

    <!-- 成本归因 -->
    <div class="section">
      <div class="section-header">
        <h3 class="section-title">成本归因</h3>
        <div class="attribution-selector">
          <label>
            分组维度：
            <select v-model="attributionDimension" class="dimension-select">
              <option value="model">按模型</option>
              <option value="provider">按提供商</option>
              <option value="intent">按意图</option>
            </select>
          </label>
        </div>
      </div>

      <div v-if="costTrendData" class="card chart-container">
        <div class="chart-grid">
          <div class="chart-panel">
            <h4 class="chart-title">成本占比</h4>
            <div class="chart-wrapper">
              <canvas id="costPieChart"></canvas>
            </div>
            <div v-if="costTrendData.other_count > 0" class="chart-note">
              其他 {{ costTrendData.other_count }} 项合计: {{ formatCurrency(costTrendData.other_cost) }}
            </div>
          </div>

          <div class="chart-panel">
            <h4 class="chart-title">成本明细（输入 vs 输出）</h4>
            <div class="chart-wrapper">
              <canvas id="costTrendChart"></canvas>
            </div>
          </div>
        </div>

        <!-- 成本明细表 -->
        <div class="cost-table-wrapper">
          <table class="cost-table">
            <thead>
              <tr>
                <th>{{ attributionDimension === 'model' ? '模型' : attributionDimension === 'provider' ? '提供商' : '意图' }}</th>
                <th class="num">请求数</th>
                <th class="num">总成本</th>
                <th class="num">输入成本</th>
                <th class="num">输出成本</th>
                <th class="num">占比</th>
                <th class="num">平均延迟</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="entry in costTrendData.entries" :key="entry.dimension_value">
                <td class="mono">{{ entry.dimension_value }}</td>
                <td class="num">{{ formatNumber(entry.request_count) }}</td>
                <td class="num">{{ formatCurrency(entry.total_cost_usd) }}</td>
                <td class="num">{{ formatCurrency(entry.input_cost_usd) }}</td>
                <td class="num">{{ formatCurrency(entry.output_cost_usd) }}</td>
                <td class="num">{{ formatPercent(entry.percentage) }}</td>
                <td class="num">{{ entry.avg_latency_ms }}ms</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <div v-else-if="loading" class="card empty-state">加载中…</div>
    </div>

    <!-- 缓存经济学面板 -->
    <div class="section">
      <h3 class="section-title">缓存经济学</h3>
      <div v-if="cacheEconomicsData" class="card cache-panel">
        <div class="cache-grid">
          <div class="cache-item">
            <div class="cache-label">缓存命中率</div>
            <div class="cache-value">{{ formatPercent(cacheEconomicsData.cache_hit_ratio * 100) }}</div>
            <div class="cache-hint">
              {{ formatNumber(cacheEconomicsData.cache_read_tokens) }} / 
              {{ formatNumber(cacheEconomicsData.cache_read_tokens + cacheEconomicsData.prompt_tokens) }} tokens
            </div>
          </div>

          <div class="cache-item">
            <div class="cache-label">缓存节省</div>
            <div class="cache-value cache-value--green">{{ formatCurrency(cacheEconomicsData.dollars_saved) }}</div>
            <div class="cache-hint">相对无缓存</div>
          </div>

          <div class="cache-item">
            <div class="cache-label">压缩节省</div>
            <div class="cache-value cache-value--green">{{ formatCurrency(cacheEconomicsData.compression_saved) }}</div>
            <div class="cache-hint">{{ formatNumber(cacheEconomicsData.compressed_requests) }} 次压缩</div>
          </div>

          <div class="cache-item">
            <div class="cache-label">综合节省率</div>
            <div class="cache-value cache-value--highlight">{{ formatPercent(cacheEconomicsData.savings_rate) }}</div>
            <div class="cache-hint">总节省 {{ formatCurrency(cacheEconomicsData.total_saved) }}</div>
          </div>

          <div class="cache-item">
            <div class="cache-label">实际支出</div>
            <div class="cache-value">{{ formatCurrency(cacheEconomicsData.dollars_spent) }}</div>
            <div class="cache-hint">占无优化成本 {{ formatPercent(cacheEconomicsData.effective_cost_ratio * 100) }}</div>
          </div>

          <div class="cache-item">
            <div class="cache-label">总请求数</div>
            <div class="cache-value">{{ formatNumber(cacheEconomicsData.total_requests) }}</div>
            <div class="cache-hint">{{ cacheEconomicsData.date_from }} ~ {{ cacheEconomicsData.date_to }}</div>
          </div>
        </div>
      </div>
      <div v-else-if="loading" class="card empty-state">加载中…</div>
    </div>
  </div>
</template>

<style scoped>
.usage-cost-view {
  padding: 20px;
  max-width: 1400px;
  margin: 0 auto;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 24px;
}

.page-header h2 {
  margin: 0;
  font-size: 24px;
  font-weight: 600;
  color: var(--text);
}

.page-header-actions {
  display: flex;
  gap: 8px;
}

.btn {
  padding: 6px 12px;
  border-radius: 6px;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  border: 1px solid var(--border);
  background: var(--card);
  color: var(--text);
  transition: all 0.2s;
}

.btn:hover:not(:disabled) {
  background: var(--surface-secondary);
}

.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.btn-ghost {
  border: 1px solid var(--border);
}

.alert {
  padding: 12px 16px;
  border-radius: 6px;
  margin-bottom: 16px;
}

.alert-danger {
  background: rgba(239, 68, 68, 0.1);
  color: #f87171;
  border: 1px solid rgba(239, 68, 68, 0.3);
}

.section {
  margin-bottom: 32px;
}

.section-title {
  font-size: 16px;
  font-weight: 600;
  color: var(--text);
  margin: 0 0 16px 0;
}

.section-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
}

.attribution-selector label {
  font-size: 13px;
  color: var(--text-secondary);
  display: flex;
  align-items: center;
  gap: 8px;
}

.dimension-select,
.period-input {
  padding: 4px 8px;
  border-radius: 4px;
  border: 1px solid var(--border);
  background: var(--card);
  color: var(--text);
  font-size: 13px;
}

.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 20px;
}

/* 对比卡片 */
.compare-cards {
  display: grid;
  gap: 16px;
}

.compare-card {
  padding: 24px;
}

.compare-period-selector {
  display: flex;
  gap: 16px;
  margin-bottom: 20px;
  padding-bottom: 16px;
  border-bottom: 1px solid var(--border);
}

.compare-period-selector label {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--text-secondary);
}

.compare-content {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.compare-row {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 24px;
}

.compare-col {
  text-align: center;
}

.compare-label {
  font-size: 12px;
  color: var(--text-secondary);
  margin-bottom: 8px;
}

.compare-value {
  font-size: 28px;
  font-weight: 700;
  color: var(--text);
  margin-bottom: 6px;
}

.compare-meta {
  font-size: 11px;
  color: var(--muted);
}

.compare-change .compare-value {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
}

.trend-up {
  color: #f87171 !important;
}

.trend-down {
  color: #4ade80 !important;
}

.trend-flat {
  color: var(--text-secondary) !important;
}

.badge {
  display: inline-block;
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 500;
  margin-left: 6px;
}

.badge-warning {
  background: rgba(251, 146, 60, 0.15);
  color: #fb923c;
}

/* 图表容器 */
.chart-container {
  padding: 24px;
}

.chart-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 32px;
  margin-bottom: 24px;
}

.chart-panel {
  min-height: 300px;
}

.chart-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
  margin: 0 0 16px 0;
}

.chart-wrapper {
  height: 280px;
  position: relative;
}

.chart-note {
  font-size: 11px;
  color: var(--muted);
  text-align: center;
  margin-top: 12px;
}

/* 成本表格 */
.cost-table-wrapper {
  overflow-x: auto;
  margin-top: 24px;
  border-top: 1px solid var(--border);
  padding-top: 16px;
}

.cost-table {
  width: 100%;
  border-collapse: collapse;
}

.cost-table th,
.cost-table td {
  padding: 10px 12px;
  text-align: left;
  border-bottom: 1px solid var(--border);
}

.cost-table th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary);
  background: var(--surface-secondary);
}

.cost-table td {
  font-size: 13px;
  color: var(--text);
}

.cost-table .num {
  text-align: right;
  font-family: 'SF Mono', 'Fira Code', monospace;
}

.cost-table .mono {
  font-family: 'SF Mono', 'Fira Code', monospace;
  font-size: 12px;
}

/* 缓存面板 */
.cache-panel {
  padding: 24px;
}

.cache-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 24px;
}

.cache-item {
  text-align: center;
  padding: 16px;
  background: var(--surface-secondary);
  border-radius: 8px;
}

.cache-label {
  font-size: 12px;
  color: var(--text-secondary);
  margin-bottom: 8px;
}

.cache-value {
  font-size: 24px;
  font-weight: 700;
  color: var(--text);
  margin-bottom: 6px;
}

.cache-value--green {
  color: #22c55e;
}

.cache-value--highlight {
  color: #6366f1;
}

.cache-hint {
  font-size: 11px;
  color: var(--muted);
}

.empty-state {
  text-align: center;
  padding: 40px;
  color: var(--muted);
}

@media (max-width: 1024px) {
  .chart-grid {
    grid-template-columns: 1fr;
  }
  
  .compare-row {
    grid-template-columns: 1fr;
    gap: 16px;
  }
}

@media (max-width: 768px) {
  .cache-grid {
    grid-template-columns: 1fr 1fr;
  }
}
</style>
