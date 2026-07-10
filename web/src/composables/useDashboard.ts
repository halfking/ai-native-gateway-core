// web/src/composables/useDashboard.ts
// 首页 Dashboard 数据管理 Composable
//
// 提供：
//   - 统一的数据加载和管理
//   - 自动重试和错误处理
//   - 缓存和刷新策略
//   - 加载状态管理

import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  fetchSessionOverview,
  fetchSessionTrend,
  fetchActiveSessions,
  fetchModuleStats,
  fetchErrorStats,
  fetchPerformanceStats,
  getHealthDistribution,
  DashboardApiError,
  type SessionOverviewData,
  type SessionTrendData,
  type ActiveSessionItem,
  type HealthDistributionData,
  type ModuleStatsData,
  type ErrorStatsData,
  type PerformanceData,
  type QueryParams,
} from '../api/dashboard'

export interface UseDashboardOptions {
  autoRefresh?: boolean          // 是否自动刷新
  refreshInterval?: number       // 刷新间隔（毫秒）
  defaultDays?: number           // 默认时间范围
  onError?: (error: Error) => void
}

export function useDashboard(options: UseDashboardOptions = {}) {
  const { t } = useI18n()

  // ════════════════════════════════════════════════════════════
  // 配置
  // ════════════════════════════════════════════════════════════
  const autoRefresh = options.autoRefresh ?? false
  const refreshInterval = options.refreshInterval ?? 5 * 60 * 1000  // 默认 5 分钟
  const defaultDays = options.defaultDays ?? 7

  // ════════════════════════════════════════════════════════════
  // 状态
  // ════════════════════════════════════════════════════════════
  const loading = ref(false)
  const error = ref<string | null>(null)
  const days = ref(defaultDays)

  const overview = ref<SessionOverviewData | null>(null)
  const trend = ref<SessionTrendData | null>(null)
  const activeSessions = ref<ActiveSessionItem[]>([])
  const healthDistribution = ref<HealthDistributionData | null>(null)
  const moduleStats = ref<ModuleStatsData | null>(null)
  const errorStats = ref<ErrorStatsData | null>(null)
  const performanceStats = ref<PerformanceData | null>(null)

  const lastUpdated = ref<Date | null>(null)
  const responseTime = ref<number>(0)

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
    loading.value = true
    error.value = null
    const startTime = Date.now()

    try {
      const queryParams = {
        days: days.value,
        ...params,
      }

      // 并行加载所有数据
      const [overviewResult, trendResult, activeResult, healthResult, moduleResult, errorResult, perfResult] = await Promise.allSettled([
        fetchSessionOverview(queryParams),
        fetchSessionTrend(queryParams),
        fetchActiveSessions({ ...queryParams, size: 20 }),
        getHealthDistribution(queryParams),
        fetchModuleStats(queryParams),
        fetchErrorStats(queryParams),
        fetchPerformanceStats(queryParams),
      ])

      // 处理结果
      if (overviewResult.status === 'fulfilled') {
        overview.value = overviewResult.value.data
      } else {
        console.error('Failed to load overview:', overviewResult.reason)
      }

      if (trendResult.status === 'fulfilled') {
        trend.value = trendResult.value
      } else {
        console.error('Failed to load trend:', trendResult.reason)
      }

      if (activeResult.status === 'fulfilled') {
        const response = await activeResult.value
        activeSessions.value = response.data.sessions
      }

      if (healthResult.status === 'fulfilled') {
        const response = healthResult.value
        if (response.success && response.data) {
          healthDistribution.value = response.data
        }
      }

      if (moduleResult.status === 'fulfilled') {
        moduleStats.value = moduleResult.value
      } else {
        console.error('Failed to load module stats:', moduleResult.reason)
      }

      if (errorResult.status === 'fulfilled') {
        errorStats.value = errorResult.value
      } else {
        console.error('Failed to load error stats:', errorResult.reason)
      }

      if (perfResult.status === 'fulfilled') {
        performanceStats.value = perfResult.value
      } else {
        console.error('Failed to load performance stats:', perfResult.reason)
      }

      lastUpdated.value = new Date()
      responseTime.value = Date.now() - startTime

      // 如果所有请求都失败，则抛出错误
      if (
        overviewResult.status === 'rejected' &&
        trendResult.status === 'rejected'
      ) {
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
      loading.value = false
    }
  }

  /**
   * 加载会话总览
   */
  async function loadOverview(params?: QueryParams) {
    loading.value = true
    error.value = null
    try {
      const result = await fetchSessionOverview({ days: days.value, ...params })
      overview.value = result.data
      lastUpdated.value = new Date()
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : t('dashboard.loadError')
    } finally {
      loading.value = false
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

  function startAutoRefresh() {
    if (refreshTimer) {
      clearInterval(refreshTimer)
    }
    if (autoRefresh) {
      refreshTimer = window.setInterval(() => {
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
  })

  onUnmounted(() => {
    stopAutoRefresh()
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
    activeSessions,
    healthDistribution,
    moduleStats,
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