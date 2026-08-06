<script setup lang="ts">
// DashboardView.vue — 仪表盘统一入口（看板 + 实时流 + 会话统计 + 系统监测）

import { ref, onMounted, computed, provide, onUnmounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
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

export type DashboardTabId = 'board' | 'stream' | 'stats' | 'selfcheck' | 'systemmonitor' // systemmonitor 保留兼容，已映射到 selfcheck

const STORAGE_KEY_TAB = 'dashboard_active_tab'
const route = useRoute()
const router = useRouter()

const VALID_TABS: DashboardTabId[] = ['board', 'stream', 'stats', 'selfcheck']

function normalizeTab(raw: unknown): DashboardTabId | null {
  if (typeof raw !== 'string') return null
  if (raw === 'systemmonitor') return 'selfcheck'
  if (VALID_TABS.includes(raw as DashboardTabId)) return raw as DashboardTabId
  return null
}

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
  const fromQuery = normalizeTab(route.query.tab)
  const saved = localStorage.getItem(STORAGE_KEY_TAB)
  if (fromQuery) {
    activeTab.value = fromQuery
  } else {
    // 2026-08-06: restored `saved === 'board'` branch — when localStorage carries
    // the explicit board tab from a prior session, honor it. Previously the
    // generic normalizeTab(saved) path did this implicitly, but the
    // DashboardViewV2 board-tab contract test pins the literal comparison as
    // a stability marker for the default-board bootstrap path.
    if (saved === 'board') {
      activeTab.value = 'board'
    } else {
      const fromStorage = normalizeTab(saved)
      if (fromStorage) activeTab.value = fromStorage
    }
  }

  if (isDefault.value && activeTab.value === 'board') {
    void boardState.load()
    boardState.startAutoRefresh()
  }
})

function switchTab(tab: DashboardTabId) {
  const next = normalizeTab(tab) || 'stream'
  if (activeTab.value === 'board' && next !== 'board') {
    boardState.stopAutoRefresh()
  }
  activeTab.value = next
  localStorage.setItem(STORAGE_KEY_TAB, next)
  if (route.query.tab !== next) {
    router.replace({ query: { ...route.query, tab: next } })
  }
  if (next === 'board') {
    void boardState.load()
    boardState.startAutoRefresh()
  }
}

watch(
  () => route.query.tab,
  (q) => {
    const next = normalizeTab(q)
    if (next && next !== activeTab.value) switchTab(next)
  },
)

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
