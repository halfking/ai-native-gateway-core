<script setup lang="ts">
// DashboardView.vue — 仪表盘统一入口（看板 + 实时流 + 会话统计 + 系统监测）

import { ref, onMounted, computed, provide, onUnmounted, watch } from 'vue'
import DashboardViewV2 from './DashboardViewV2.vue'
import TenantDashboardView from './TenantDashboardView.vue'
import { isDefaultTenant, store } from '../store'
import {
  getUsageByModel,
  getHotApiKeys,
  type ModelUsage,
  type HotApiKeyEntry,
} from '../api'
import { useDashboardBoard } from '../composables/useDashboardBoard'

export type DashboardTabId = 'board' | 'stream' | 'stats' | 'selfcheck' | 'systemmonitor'

const STORAGE_KEY_TAB = 'dashboard_active_tab'

// 2026-07-23: 默认 tab 改为 'stream'（实时流），
// 之前默认是 'board' 导致用户进入 dashboard 后看不到实时流数据。
// 后端 SSE 数据流正常（已验证），但 LiveRequestStreamV2 只在 stream tab 才渲染。
const activeTab = ref<DashboardTabId>('stream')

const isDefault = computed(() => isDefaultTenant())

const boardState = useDashboardBoard()
const drawerLoading = ref(false)
const models = ref<ModelUsage[]>([])
const hotKeys = ref<HotApiKeyEntry[]>([])

onMounted(() => {
  const saved = localStorage.getItem(STORAGE_KEY_TAB)
<<<<<<< Updated upstream
  if (saved === 'board' || saved === 'stream' || saved === 'stats' || saved === 'selfcheck' || saved === 'systemmonitor') {
    activeTab.value = saved
  }

  if (isDefault.value && activeTab.value === 'board') {
    void boardState.load()
    boardState.startAutoRefresh()
  }
})

function switchTab(tab: DashboardTabId) {
  if (activeTab.value === 'board' && tab !== 'board') {
    boardState.stopAutoRefresh()
  }
  activeTab.value = tab
  localStorage.setItem(STORAGE_KEY_TAB, tab)
  if (tab === 'board') {
    void boardState.load()
    boardState.startAutoRefresh()
  }
}

watch(
  () => store.jwtToken,
  (token, prev) => {
    if (token && !prev && isDefault.value && activeTab.value === 'board') {
      void boardState.load()
      void boardState.startAutoRefresh()
    }
  },
)

watch(
  () => boardState.timeRange.value,
  () => {
    if (isDefault.value && activeTab.value === 'board') {
      void boardState.load()
      void boardState.startAutoRefresh()
    }
  },
  { deep: true },
)

async function loadDrawerData() {
  drawerLoading.value = true
  try {
    const [modelsData, hotKeysData] = await Promise.all([
      getUsageByModel(boardState.days.value),
      getHotApiKeys(boardState.days.value),
    ])
    models.value = modelsData
    hotKeys.value = hotKeysData
  } catch {
    /* non-blocking */
  } finally {
    drawerLoading.value = false
  }
}

async function refreshBoard() {
  await boardState.load()
}

onUnmounted(() => {
  boardState.stopAutoRefresh()
})

provide('dashboardBoard', {
  board: boardState.board,
  operational: boardState.operational,
  days: boardState.days,
  timeRange: boardState.timeRange,
  loading: boardState.loading,
  error: boardState.error,
  load: boardState.load,
  setTimeRange: boardState.setTimeRange,
  startAutoRefresh: boardState.startAutoRefresh,
})

provide('dashboardDrawer', {
  models,
  hotKeys,
  drawerLoading,
  loadDrawerData,
})

provide('dashboardTab', {
  activeTab,
  switchTab,
})

provide('dashboardActions', {
  refreshBoard,
})
</script>

<template>
  <div>
    <TenantDashboardView v-if="!isDefault" />
    <DashboardViewV2 v-else />
  </div>
</template>
