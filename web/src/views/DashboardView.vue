<script setup lang="ts">
// DashboardView.vue — 仪表盘统一入口（看板 + 实时流 + 会话统计 + 系统监测）

import { ref, onMounted, computed, provide, onUnmounted, watch } from 'vue'
import DashboardViewV2 from './DashboardViewV2.vue'
import TenantDashboardView from './TenantDashboardView.vue'
import { isDefaultTenant } from '../store'
import {
  getUsageByModel,
  getHotApiKeys,
  type ModelUsage,
  type HotApiKeyEntry,
} from '../api'
import { useDashboardBoard } from '../composables/useDashboardBoard'

export type DashboardTabId = 'board' | 'stream' | 'stats' | 'selfcheck'

const STORAGE_KEY_TAB = 'dashboard_active_tab'

const activeTab = ref<DashboardTabId>('board')

const isDefault = computed(() => isDefaultTenant())

const boardState = useDashboardBoard()
const drawerLoading = ref(false)
const models = ref<ModelUsage[]>([])
const hotKeys = ref<HotApiKeyEntry[]>([])

onMounted(() => {
  const saved = localStorage.getItem(STORAGE_KEY_TAB)
  if (saved === 'board' || saved === 'stream' || saved === 'stats' || saved === 'selfcheck') {
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
  () => boardState.days.value,
  () => {
    if (isDefault.value && activeTab.value === 'board') {
      void boardState.load()
    }
  },
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
  days: boardState.days,
  loading: boardState.loading,
  error: boardState.error,
  load: boardState.load,
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
