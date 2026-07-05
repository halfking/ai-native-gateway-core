<script setup lang="ts">
import { useI18n } from 'vue-i18n'
// DashboardView.vue — 仪表盘统一入口
// 2026-07-05 v3: 统一数据源 + 版本切换器集成到标题栏

import { ref, onMounted, computed, provide, onUnmounted, watch } from 'vue'
import DashboardViewV2 from './DashboardViewV2.vue'
import DashboardViewLegacy from './DashboardViewLegacy.vue'
import TenantDashboardView from './TenantDashboardView.vue'
import { isDefaultTenant } from '../store'
import {
  getUsageSummary,
  getUsageByModel,
  getDashboardOverview,
  getHotApiKeys,
  getCompressionStats,
  type UsageSummary,
  type ModelUsage,
  type DashboardOverview,
  type HotApiKeyEntry,
  type CompressionStats,
} from '../api'
import { useLiveStream } from '../composables/useLiveStream'

const { t } = useI18n()


const STORAGE_KEY = 'dashboard_version'

// 版本选择（默认V2）
const version = ref<'v1' | 'v2'>('v2')
const swimLaneReinitKey = ref(0) // 用于强制重新初始化泳道

// 是否为默认租户
const isDefault = computed(() => isDefaultTenant())

// 统一数据源状态
const days = ref(1)
const loading = ref(false)
const error = ref<string | null>(null)

const summary = ref<UsageSummary | null>(null)
const overview = ref<DashboardOverview | null>(null)
const models = ref<ModelUsage[]>([])
const hotKeys = ref<HotApiKeyEntry[]>([])
const compStats = ref<CompressionStats | null>(null)

// Live stream（共享）
const {
  requests: liveRequests,
  onRequestEvicted,
  reset: resetLiveStream,
} = useLiveStream()

// 从localStorage恢复版本选择
onMounted(() => {
  const saved = localStorage.getItem(STORAGE_KEY)
  if (saved === 'v1' || saved === 'v2') {
    version.value = saved
  }
  
  // 只有默认租户才加载数据
  if (isDefault.value) {
    void load()
    scheduleStatsRecalibrate()
  }
})

// 切换版本
function switchVersion(v: 'v1' | 'v2') {
  version.value = v
  localStorage.setItem(STORAGE_KEY, v)
  
  // 切换到V2时，强制重新初始化泳道
  // 这样可以从缓存的liveRequests中重新加载数据
  if (v === 'v2') {
    swimLaneReinitKey.value++
  }
}

// 加载数据
async function load() {
  loading.value = true
  error.value = null
  try {
    const [summaryData, overviewData, modelsData, hotKeysData] = await Promise.all([
      getUsageSummary(days.value),
      getDashboardOverview(),
      getUsageByModel(days.value),
      getHotApiKeys(days.value, 100),
    ])
    
    summary.value = summaryData
    overview.value = overviewData
    models.value = modelsData
    hotKeys.value = hotKeysData
    
    await loadCompressionStats()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('dashboard.loadError')
  } finally {
    loading.value = false
  }
}

async function loadCompressionStats() {
  try {
    compStats.value = await getCompressionStats({ hours: 24 })
  } catch {
    /* non-blocking */
  }
}

// 定时重新校准统计数据（5分钟）
let statsRecalibrateTimer: number | undefined

function scheduleStatsRecalibrate() {
  statsRecalibrateTimer = window.setInterval(async () => {
    try {
      const fresh = await getUsageSummary(days.value)
      summary.value = fresh
      resetLiveStream()
    } catch {
      /* non-blocking */
    }
  }, 5 * 60 * 1000)
}

onUnmounted(() => {
  if (statsRecalibrateTimer) clearInterval(statsRecalibrateTimer)
})

// 提供数据给子组件
provide('dashboardData', {
  days,
  loading,
  error,
  summary,
  overview,
  models,
  hotKeys,
  compStats,
  liveRequests,
  onRequestEvicted,
  resetLiveStream,
  load,
})

// 提供版本切换器给子组件
provide('versionSwitcher', {
  version,
  switchVersion,
})

// 提供泳道重新初始化key给V2
provide('swimLaneReinitKey', swimLaneReinitKey)
</script>

<template>
  <div>
    <!-- 租户专用仪表盘 -->
    <TenantDashboardView v-if="!isDefault" />
    
    <!-- 默认租户仪表盘（V1/V2共享数据源） -->
    <DashboardViewV2 v-else-if="version === 'v2'" />
    <DashboardViewLegacy v-else />
  </div>
</template>
