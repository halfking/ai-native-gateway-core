<script setup lang="ts">
// BoardOperationalView — 运营面板（2026-11-19 优化，时间范围统一）
// 从运营角度汇总分析系统整体状况，整合多维度数据
//
// 端点：
// - GET /api/admin/dashboard/operational（后台任务、自检）
// - GET /api/admin/dashboard/board（完整看板数据：summary、pies、trends）
//
// 优化：整合运营关键指标、流量分析、成本分析、错误分析、趋势图表
// 2026-11-20: 增加"更多"按钮和详情页导航
import { onMounted, onBeforeUnmount, ref, computed, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { surfaceError } from '../i18n/errorSurface'
import { ANALYTICS_REGISTRY } from '../config/analyticsRegistry'
import {
  fetchBoardOperational,
  fetchDashboardBoard,
  type BoardOperationalPayload,
  type BoardPayload,
} from '../api/board'
import StatsDateRangePicker from '../components/analytics/StatsDateRangePicker.vue'
import { useI18n } from 'vue-i18n'
import { boardRangeFromKxRange, toBoardTimeQuery, type BoardTimeRange } from '../utils/boardTimeRange'
import { rangeFromSpanDays } from '../components/ui/kxDatePresets'
import type { KxDateRange } from '../components/ui/kx-date-types'
import * as echarts from 'echarts'
import type { EChartsOption } from 'echarts'

const router = useRouter()
const { t } = useI18n()
const loading = ref(false)
const error = ref('')
const operationalData = ref<BoardOperationalPayload | null>(null)
const boardData = ref<BoardPayload | null>(null)
const trendChartRef = ref<HTMLDivElement>()
let trendChartInstance: echarts.ECharts | null = null

// ★★★ 2026-11-25 统一轮：时间范围控件改用**全站统一封装**
//   components/analytics/StatsDateRangePicker.vue —— 与「看板页」「会话与统计」页
//   「会话分析·用户」页**同一个组件文件** ⇒ 四处的档位、跨度上限、交互形态
//   **结构同构**（改一处 ⇒ 四处同步；不会再有第五套各写各的预设）。
//   ★ 本页此前是**第四套预设**（今天/近7/近30/近90，跨度上限 365 天）⇒ 与其余三页
//     形态不同（缺 昨天/近14/本月/上月）；统一后档位集合**逐字同序相等**。
//   ★ 窗口与数据同尺：控件给的是**日历日区间**（含首尾）⇒ 用看板页同一把尺
//     boardRangeFromKxRange 归位（今天/近7/近30 走 days，其余走 start/end），
//     查询参数由 toBoardTimeQuery 产出 ⇒ 界面显示的区间 = 数据窗口。
//   ★ 「近 90 天」档位随统一而消失（共享组件的 7 个档位里没有它）⇒
//     但自定义区间的上限是 92 天 ⇒ 同等窗口仍可通过自定义到达（不是能力回退）。
const rangeValue = ref<KxDateRange>(rangeFromSpanDays(7))
const boardRange = computed<BoardTimeRange>(() => boardRangeFromKxRange(rangeValue.value))
/** 天数 = 当前生效窗口的**真实跨度**（含首尾）——文案用它，不再另编一个数。 */
const timeRange = computed(() => boardRange.value.days)

// ★ 文案不许说假话：预设档位说「最近 N 天」；**自定义区间说起止日期**
//   （「最近 N 天」对一个历史区间是**假的**——用户选 09-01~09-10，
//   看到的却写着「最近 10 天」⇒ 同一个「日期范围」在两处指的不是同一段区间）。
//   ⇒ 复用看板页既有的 rangeCustom 键（8 语种齐备 ⇒ 零新增文案、零 CJK 增量）。
const rangeHint = computed(() => {
  const r = boardRange.value
  if (r.preset === 'custom' && r.start && r.end) {
    return t('dashboard.board.rangeCustom', { start: r.start, end: r.end })
  }
  return t('dashboard.operational.dataTimeRange', { days: r.days })
})

function onRangeApply(range: KxDateRange) {
  rangeValue.value = range
  void load()
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [opData, bdData] = await Promise.all([
      fetchBoardOperational(),
      // ★ 窗口与界面显示同尺：预设走 days、自定义走 start/end（看板页同一把尺）。
      fetchDashboardBoard(toBoardTimeQuery(boardRange.value)),
    ])
    operationalData.value = opData
    boardData.value = bdData
    
    // 初始化趋势图表
    await nextTick()
    initTrendChart()
  } catch (e) {
    error.value = surfaceError(e)
  } finally {
    loading.value = false
  }
}

function initTrendChart() {
  if (!trendChartRef.value || !boardData.value?.trends || boardData.value.trends.length === 0) {
    return
  }

  // 测试环境检查：如果容器没有实际尺寸，跳过初始化
  const rect = trendChartRef.value.getBoundingClientRect()
  if (rect.width === 0 || rect.height === 0) {
    return
  }

  // 销毁旧实例
  if (trendChartInstance) {
    trendChartInstance.dispose()
  }

  try {
    // 创建新实例
    trendChartInstance = echarts.init(trendChartRef.value)

    // 聚合数据：按天汇总
    const dailyData = aggregateTrendsByDay(boardData.value.trends)
    
    const dates = dailyData.map(d => d.date)
    const requests = dailyData.map(d => d.requests)
    const successRates = dailyData.map(d => d.successRate)
    const costs = dailyData.map(d => d.cost)

    const option: EChartsOption = {
      title: {
        show: false
      },
      tooltip: {
        trigger: 'axis',
        axisPointer: {
          type: 'cross'
        }
      },
      legend: {
        data: ['请求数', '成功率', '花费(USD)']
      },
      grid: {
        left: '3%',
        right: '4%',
        bottom: '3%',
        containLabel: true
      },
      xAxis: {
        type: 'category',
        boundaryGap: false,
        data: dates
      },
      yAxis: [
        {
          type: 'value',
          name: '请求数',
          position: 'left',
          axisLabel: {
            formatter: '{value}'
          }
        },
        {
          type: 'value',
          name: '成功率',
          position: 'right',
          axisLabel: {
            formatter: '{value}%'
          },
          min: 0,
          max: 100
        }
      ],
      series: [
        {
          name: '请求数',
          type: 'line',
          data: requests,
          smooth: true,
          areaStyle: {
            opacity: 0.3
          }
        },
        {
          name: '成功率',
          type: 'line',
          yAxisIndex: 1,
          data: successRates,
          smooth: true
        },
        {
          name: '花费(USD)',
          type: 'line',
          data: costs,
          smooth: true
        }
      ]
    }

    trendChartInstance.setOption(option)
  } catch (err) {
    // 在测试环境或其他异常情况下静默失败
    console.warn('Failed to initialize trend chart:', err)
  }
}

// 聚合趋势数据：按天汇总
function aggregateTrendsByDay(trends: any[]) {
  const dailyMap = new Map<string, { requests: number; success: number; cost: number }>()

  for (const point of trends) {
    // 提取日期部分 (YYYY-MM-DD)
    const date = point.bucket.split('T')[0]
    
    if (!dailyMap.has(date)) {
      dailyMap.set(date, { requests: 0, success: 0, cost: 0 })
    }

    const day = dailyMap.get(date)!
    day.requests += point.requests || 0
    day.success += point.success || 0
    day.cost += point.cost_usd || 0
  }

  // 转换为数组并排序
  const result = Array.from(dailyMap.entries())
    .map(([date, data]) => ({
      date,
      requests: data.requests,
      successRate: data.requests > 0 ? (data.success / data.requests * 100) : 0,
      cost: data.cost
    }))
    .sort((a, b) => a.date.localeCompare(b.date))

  return result
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

// 检查分布分析区域是否有任何数据
const hasDistributionData = computed(() => {
  const pies = boardData.value?.pies
  if (!pies) return false
  
  const hasModels = pies.models && pies.models.length > 0
  const hasProviders = pies.providers && pies.providers.length > 0
  const hasClients = pies.clients && pies.clients.length > 0
  const hasTenants = pies.tenants && pies.tenants.length > 0
  
  return hasModels || hasProviders || hasClients || hasTenants
})

function topItems(items: any[] | undefined, n: number = 5) {
  if (!items || items.length === 0) return []
  return items.slice(0, n)
}

// 导航函数 (2026-11-20)
//
// ★ 2026-11-23：自定义区间下钻时把 start/end 一起带过去（下游列表页已支持
//   读取），否则「看板选的历史区间 → 下钻页变回最近 N 天」，同一页两个口径。
function rangeQuery(): Record<string, string> {
  const r = boardRange.value
  if (r.preset === 'custom' && r.start && r.end) {
    return { start: r.start, end: r.end }
  }
  return { days: String(r.days) }
}

function navigateToList(dimension: 'model' | 'provider' | 'client' | 'tenant') {
  const registry = ANALYTICS_REGISTRY[dimension]
  router.push({
    path: registry.listPath,
    query: rangeQuery()
  })
}

function navigateToDetail(dimension: 'model' | 'provider' | 'client' | 'tenant', itemKey: string) {
  const registry = ANALYTICS_REGISTRY[dimension]
  router.push({
    path: registry.detailPath(itemKey),
    query: rangeQuery()
  })
}

// 检查是否有更多项(超过5个)
function hasMore(items: any[] | undefined): boolean {
  return items ? items.length > 5 : false
}

onMounted(() => {
  void load()
})

onBeforeUnmount(() => {
  if (trendChartInstance) {
    trendChartInstance.dispose()
    trendChartInstance = null
  }
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
        <StatsDateRangePicker
          :model-value="rangeValue"
          :disabled="loading"
          @apply="onRangeApply"
        />
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
        <p class="bo-time-range-hint">{{ rangeHint }}</p>
        
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
      <section v-if="hasDistributionData" class="bo-section">
        <h2 class="bo-section-title">{{ $t('dashboard.operational.distributionTitle') }}</h2>
        <p class="bo-time-range-hint">{{ rangeHint }}</p>
        
        <div class="bo-pies-grid">
          <!-- 模型分布 -->
          <div class="bo-pie-card">
            <div class="bo-pie-header">
              <h3 class="bo-pie-title">{{ $t('dashboard.operational.topModels') }}</h3>
              <button
                v-if="hasMore(boardData?.pies?.models)"
                class="bo-more-btn"
                @click="navigateToList('model')"
              >
                {{ $t('board.operational.more') }} →
              </button>
            </div>
            <div v-if="topItems(boardData?.pies?.models, 5).length > 0" class="bo-pie-list">
              <div
                v-for="item in topItems(boardData?.pies?.models, 5)"
                :key="item.key"
                class="bo-pie-item bo-pie-item-clickable"
                @click="navigateToDetail('model', item.key)"
              >
                <span class="bo-pie-key">{{ item.key }}</span>
                <span class="bo-pie-value">{{ $t('dashboard.operational.requestsCount', { count: formatNumber(item.requests) }) }}</span>
              </div>
            </div>
            <p v-else class="bo-pie-empty">{{ $t('dashboard.operational.emptyNoData') }}</p>
          </div>

          <!-- 供应商分布 -->
          <div class="bo-pie-card">
            <div class="bo-pie-header">
              <h3 class="bo-pie-title">{{ $t('dashboard.operational.topProviders') }}</h3>
              <button
                v-if="hasMore(boardData?.pies?.providers)"
                class="bo-more-btn"
                @click="navigateToList('provider')"
              >
                {{ $t('board.operational.more') }} →
              </button>
            </div>
            <div v-if="topItems(boardData?.pies?.providers, 5).length > 0" class="bo-pie-list">
              <div
                v-for="item in topItems(boardData?.pies?.providers, 5)"
                :key="item.key"
                class="bo-pie-item bo-pie-item-clickable"
                @click="navigateToDetail('provider', item.key)"
              >
                <span class="bo-pie-key">{{ item.key }}</span>
                <span class="bo-pie-value">{{ $t('dashboard.operational.requestsCount', { count: formatNumber(item.requests) }) }}</span>
              </div>
            </div>
            <p v-else class="bo-pie-empty">{{ $t('dashboard.operational.emptyNoData') }}</p>
          </div>

          <!-- 客户端分布 -->
          <div class="bo-pie-card">
            <div class="bo-pie-header">
              <h3 class="bo-pie-title">{{ $t('dashboard.operational.topClients') }}</h3>
              <button
                v-if="hasMore(boardData?.pies?.clients)"
                class="bo-more-btn"
                @click="navigateToList('client')"
              >
                {{ $t('board.operational.more') }} →
              </button>
            </div>
            <div v-if="topItems(boardData?.pies?.clients, 5).length > 0" class="bo-pie-list">
              <div
                v-for="item in topItems(boardData?.pies?.clients, 5)"
                :key="item.key"
                class="bo-pie-item bo-pie-item-clickable"
                @click="navigateToDetail('client', item.key)"
              >
                <span class="bo-pie-key">{{ item.key }}</span>
                <span class="bo-pie-value">{{ $t('dashboard.operational.requestsCount', { count: formatNumber(item.requests) }) }}</span>
              </div>
            </div>
            <p v-else class="bo-pie-empty">{{ $t('dashboard.operational.emptyNoData') }}</p>
          </div>

          <!-- 租户分布 -->
          <div class="bo-pie-card">
            <div class="bo-pie-header">
              <h3 class="bo-pie-title">{{ $t('dashboard.operational.topTenants') }}</h3>
              <button
                v-if="hasMore(boardData?.pies?.tenants)"
                class="bo-more-btn"
                @click="navigateToList('tenant')"
              >
                {{ $t('board.operational.more') }} →
              </button>
            </div>
            <div v-if="topItems(boardData?.pies?.tenants, 5).length > 0" class="bo-pie-list">
              <div
                v-for="item in topItems(boardData?.pies?.tenants, 5)"
                :key="item.key"
                class="bo-pie-item bo-pie-item-clickable"
                @click="navigateToDetail('tenant', item.key)"
              >
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
        <p class="bo-time-range-hint">{{ rangeHint }}</p>
        
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
        <p class="bo-time-range-hint">{{ rangeHint }}</p>
        
        <div class="bo-trend-summary">
          <p>{{ $t('dashboard.operational.trendDataPoints', { count: boardData.trends.length }) }}</p>
          <p v-if="boardData.degraded_trends" class="bo-section-warning">
            {{ $t('dashboard.operational.degradedTrends', { reason: boardData.degraded_trends_reason || '' }) }}
          </p>
        </div>

        <div ref="trendChartRef" class="bo-trend-chart-container"></div>
      </section>

      <footer class="bo-footer">
        <p>{{ $t('dashboard.operational.footerHint') }}</p>
        <p>{{ rangeHint }}</p>
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

.bo-pie-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--space-2);
}

.bo-pie-title {
  font-size: var(--text-md);
  font-weight: var(--font-semibold);
  color: var(--text);
  margin: 0;
}

.bo-more-btn {
  padding: var(--space-1) var(--space-2);
  font-size: var(--text-xs);
  color: var(--primary);
  background: transparent;
  border: 1px solid var(--primary);
  border-radius: var(--radius);
  cursor: pointer;
  transition: all 0.15s;
  font-weight: var(--font-medium);
}

.bo-more-btn:hover {
  background: var(--primary);
  color: white;
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
  border-radius: var(--radius);
  font-size: var(--text-sm);
}

.bo-pie-item-clickable {
  cursor: pointer;
  transition: background-color 0.15s, transform 0.1s;
}

.bo-pie-item-clickable:hover {
  background: var(--bg-hover);
  transform: translateX(2px);
}

.bo-pie-item-clickable .bo-pie-key {
  color: var(--primary);
}

.bo-pie-item-clickable:hover .bo-pie-key {
  text-decoration: underline;
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

.bo-trend-chart-container {
  width: 100%;
  height: 400px;
  background: var(--bg-surface);
  border-radius: var(--radius);
  padding: var(--space-3);
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

  .bo-trend-chart-container {
    height: 300px;
  }
}
</style>
