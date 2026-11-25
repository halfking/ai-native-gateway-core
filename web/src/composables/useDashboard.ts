// web/src/composables/useDashboard.ts
// 首页 Dashboard 数据管理 Composable
//
// 提供：
//   - 统一的数据加载和管理
//   - 自动重试和错误处理
//   - 缓存和刷新策略
//   - 加载状态管理

import { ref, computed, onMounted, onUnmounted, type Ref } from 'vue'
import { surfaceError } from '../i18n/errorSurface'
import { useI18n } from 'vue-i18n'
import {
  fetchSessionOverview,
  normalizeSessionTrendData,
  fetchErrorStats,
  fetchPerformanceStats,
  DashboardApiError,
  type SessionOverviewData,
  type SessionTrendData,
  type ErrorStatsData,
  type PerformanceData,
  type QueryParams,
} from '../api/dashboard'

export interface UseDashboardOptions {
  autoRefresh?: boolean          // 是否自动刷新
  refreshInterval?: number       // 刷新间隔（毫秒）
  defaultDays?: number           // 默认时间范围
  onError?: (error: Error) => void
  /**
   * 2026-11-21 统计口径对齐轮：常驻附加查询参数（如 include_probes）。
   * 每次加载（含自动刷新/页面可见性刷新）都合并进 queryParams——常驻口径
   * 不能依赖调用方每次手动传参，否则自动刷新会把它丢掉。
   */
  extraParams?: Ref<QueryParams>
}

export function useDashboard(options: UseDashboardOptions = {}) {
  const { t } = useI18n()

  // ════════════════════════════════════════════════════════════
  // 配置
  // ════════════════════════════════════════════════════════════
  const autoRefresh = options.autoRefresh ?? false
  const refreshInterval = options.refreshInterval ?? 5 * 60 * 1000  // 默认 5 分钟
  const defaultDays = options.defaultDays ?? 7
  // 常驻附加参数（Ref 展开为快照，merge 时取当次值）。
  const extraParams = options.extraParams

  // ════════════════════════════════════════════════════════════
  // 状态
  // ════════════════════════════════════════════════════════════
  const loading = ref(false)
  const error = ref<string | null>(null)
  const days = ref(defaultDays)

  const overview = ref<SessionOverviewData | null>(null)
  const trend = ref<SessionTrendData | null>(null)
  const errorStats = ref<ErrorStatsData | null>(null)
  const performanceStats = ref<PerformanceData | null>(null)

  const lastUpdated = ref<Date | null>(null)
  const responseTime = ref<number>(0)
  let requestVersion = 0

  // ════════════════════════════════════════════════════════════
  // 计算属性
  // ════════════════════════════════════════════════════════════

  const hasError = computed(() => !!error.value)
  const isEmpty = computed(() => !loading.value && !error.value && !overview.value)

  const totalStats = computed(() => {
    if (!overview.value) {
      return {
        sessions: 0,
        active: 0,
        cost: 0,
        complianceRate: 0,
      }
    }
    return {
      sessions: overview.value.total_sessions,
      active: overview.value.active_sessions,
      cost: overview.value.cost_stats.total_cost_usd,
      complianceRate: overview.value.compliance_stats.compliance_rate,
    }
  })

  // ════════════════════════════════════════════════════════════
  // 数据加载
  // ════════════════════════════════════════════════════════════

  /**
   * 加载所有数据
   */
  async function loadAll(params?: QueryParams) {
    const version = ++requestVersion
    loading.value = true
    error.value = null
    const startTime = Date.now()

    try {
      const queryParams = {
        days: days.value,
        ...(extraParams?.value ?? {}),
        ...params,
      }

      // 并行加载所有数据
      const [overviewResult, errorResult, perfResult] = await Promise.allSettled([
        fetchSessionOverview(queryParams),
        fetchErrorStats(queryParams),
        fetchPerformanceStats(queryParams),
      ])
      if (version !== requestVersion) return

      // 处理结果
      if (overviewResult.status === 'fulfilled') {
        applyOverview(overviewResult.value.data, queryParams.days)
      } else {
        overview.value = null
        trend.value = null
      }

      if (errorResult.status === 'fulfilled') {
        errorStats.value = errorResult.value
      } else {
        errorStats.value = null
        console.error('Failed to load error stats:', errorResult.reason)
      }

      if (perfResult.status === 'fulfilled') {
        performanceStats.value = perfResult.value
      } else {
        performanceStats.value = null
        console.error('Failed to load performance stats:', perfResult.reason)
      }

      lastUpdated.value = new Date()
      responseTime.value = Date.now() - startTime

      if (overviewResult.status === 'rejected') {
        throw overviewResult.reason
      }
    } catch (e: unknown) {
      const err = e instanceof Error ? e : new Error(String(e))
      error.value = err.message
      options.onError?.(err)

      // 显示错误提示
      if (e instanceof DashboardApiError) {
        console.error('Dashboard API Error:', e.code, e.message)
      }
    } finally {
      if (version === requestVersion) loading.value = false
    }
  }

  function applyOverview(data: SessionOverviewData, rangeDays: number) {
    overview.value = data
    if (!data.trend_summary) {
      trend.value = null
      return
    }
    trend.value = normalizeSessionTrendData({
      trend: data.session_trend,
      summary: data.trend_summary,
      period: data.period,
      period_start: data.period_start,
      period_end: data.period_end,
    }, rangeDays)
  }

  /**
   * 加载会话总览
   */
  async function loadOverview(params?: QueryParams) {
    const version = ++requestVersion
    loading.value = true
    error.value = null
    try {
      const query = { days: days.value, ...(extraParams?.value ?? {}), ...params }
      const result = await fetchSessionOverview(query)
      if (version !== requestVersion) return
      applyOverview(result.data, query.days)
      lastUpdated.value = new Date()
    } catch (e: unknown) {
      if (version === requestVersion) {
        overview.value = null
        trend.value = null
        error.value = surfaceError(e, { key: 'dashboard.loadError' })
      }
    } finally {
      if (version === requestVersion) loading.value = false
    }
  }

  /**
   * 刷新数据（强制刷新缓存）
   */
  async function refresh() {
    await loadAll({ refresh: true })
  }

  /**
   * 改变时间范围
   */
  async function changeDays(newDays: number) {
    if (newDays < 1 || newDays > 90) {
      console.warn('Invalid days:', newDays)
      return
    }
    days.value = newDays
    await loadAll()
  }

  // ════════════════════════════════════════════════════════════
  // 自动刷新
  // ════════════════════════════════════════════════════════════

  let refreshTimer: number | undefined

  function isPageVisible() {
    return typeof document === 'undefined' || !document.hidden
  }

  function onVisibilityChange() {
    if (isPageVisible()) void loadAll()
  }

  function startAutoRefresh() {
    if (refreshTimer) {
      clearInterval(refreshTimer)
    }
    if (autoRefresh) {
      refreshTimer = window.setInterval(() => {
        if (!isPageVisible()) return
        void loadAll()
      }, refreshInterval)
    }
  }

  function stopAutoRefresh() {
    if (refreshTimer) {
      clearInterval(refreshTimer)
      refreshTimer = undefined
    }
  }

  // ════════════════════════════════════════════════════════════
  // 生命周期
  // ════════════════════════════════════════════════════════════

  onMounted(() => {
    void loadAll()
    startAutoRefresh()
    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', onVisibilityChange)
    }
  })

  onUnmounted(() => {
    stopAutoRefresh()
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', onVisibilityChange)
    }
  })

  // ════════════════════════════════════════════════════════════
  // 返回
  // ════════════════════════════════════════════════════════════

  return {
    // 状态
    loading,
    error,
    days,
    hasError,
    isEmpty,
    lastUpdated,
    responseTime,

    // 数据
    overview,
    trend,
    errorStats,
    performanceStats,

    // 计算属性
    totalStats,

    // 方法
    loadAll,
    loadOverview,
    refresh,
    changeDays,
  }
}
