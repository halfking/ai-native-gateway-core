<script setup lang="ts">
import { useI18n } from 'vue-i18n'
// DashboardView.vue — 仪表盘统一入口
// 2026-07-10 v4: Tab 切换：实时请求流 vs 会话统计
// Tab 1 ("实时请求流"): 统计卡片 + LiveRequestStreamV2
// Tab 2 ("会话与统计"): 统计卡片 + SessionStatsPanel + LiveRequestStreamV2

import { ref, onMounted, computed, provide, onUnmounted } from 'vue'
import DashboardViewV2 from './DashboardViewV2.vue'
import TenantDashboardView from './TenantDashboardView.vue'
import { isDefaultTenant } from '../store'
import {
  getUsageSummary,
  getUsageByModel,
  getDashboardOverview,
  getHotApiKeys,
  getCompressionStats,
  getModelDiscoveryStatus,
  type UsageSummary,
  type ModelUsage,
  type DashboardOverview,
  type HotApiKeyEntry,
  type CompressionStats,
  type ModelDiscoveryStatusResponse,
} from '../api'
import { useLiveStream } from '../composables/useLiveStream'

const { t } = useI18n()

const STORAGE_KEY_TAB = 'dashboard_active_tab'

// Tab 选择（默认 'stream'）
const activeTab = ref<'stream' | 'stats'>('stream')
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
const discoveryStatus = ref<ModelDiscoveryStatusResponse | null>(null)

// Live stream（共享）
const {
  requests: liveRequests,
  onRequestEvicted,
  reset: resetLiveStream,
} = useLiveStream()

// 从localStorage恢复 Tab 选择
onMounted(() => {
  const saved = localStorage.getItem(STORAGE_KEY_TAB)
  if (saved === 'stream' || saved === 'stats') {
    activeTab.value = saved
  }
  
  // 只有默认租户才加载数据
  if (isDefault.value) {
    void load()
    void loadDiscoveryStatus()
    scheduleStatsRecalibrate()
    scheduleDiscoveryPoll()
  }
})

// 切换 Tab
function switchTab(tab: 'stream' | 'stats') {
  activeTab.value = tab
  localStorage.setItem(STORAGE_KEY_TAB, tab)
  
  // 切换到 stream 时，强制重新初始化泳道
  if (tab === 'stream') {
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
      getDashboardOverview(days.value),
      getUsageByModel(days.value),
      getHotApiKeys(days.value),
    ])
    summary.value = summaryData
    overview.value = overviewData
    models.value = modelsData
    hotKeys.value = hotKeysData
    
    // 非阻塞加载压缩统计
    void loadCompressionStats()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
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

async function loadDiscoveryStatus() {
  try {
    discoveryStatus.value = await getModelDiscoveryStatus()
  } catch {
    /* non-blocking */
  }
}

// 定时重新校准统计数据（5分钟）
let statsRecalibrateTimer: number | undefined
let discoveryPollTimer: number | undefined

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

function scheduleDiscoveryPoll() {
  discoveryPollTimer = window.setInterval(() => {
    void loadDiscoveryStatus()
  }, 15000) // 每15秒轮询一次
}

onUnmounted(() => {
  if (statsRecalibrateTimer) clearInterval(statsRecalibrateTimer)
  if (discoveryPollTimer) clearInterval(discoveryPollTimer)
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
  discoveryStatus,
  liveRequests,
  onRequestEvicted,
  resetLiveStream,
  load,
})

// 提供 Tab 控制给子组件
provide('dashboardTab', {
  activeTab,
  switchTab,
})

// 提供泳道重新初始化key给子组件
provide('swimLaneReinitKey', swimLaneReinitKey)
</script>

<template>
  <div>
    <!-- 租户专用仪表盘 -->
    <TenantDashboardView v-if="!isDefault" />
    
    <!-- 默认租户仪表盘：Tab 切换实时流 vs 会话统计 -->
    <DashboardViewV2 v-else />
  </div>
</template>

<style scoped>
/* Styles moved to DashboardViewV2.vue */
</style>
