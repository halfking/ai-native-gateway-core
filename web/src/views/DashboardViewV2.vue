<script setup lang="ts">
// DashboardViewV2.vue — 看板 + 实时流 + 会话统计 + 系统监测

import { ref, computed, inject, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import MemoraStatusButton from '../components/MemoraStatusButton.vue'
import LiveRequestStreamV2 from '../components/LiveRequestStreamV2.vue'
import StatsDrawer from '../components/StatsDrawer.vue'
import RequestLogDrawer from '../components/RequestLogDrawer.vue'
import SessionStatsPanel from '../components/SessionStatsPanel.vue'
import BoardPanel from '../components/board/BoardPanel.vue'
import SelfCheckPanel from './SelfCheckPanel.vue'
import type { BoardPayload } from '../api/board'
import type { ModelUsage, HotApiKeyEntry } from '../api'
import type { DashboardTabId } from './DashboardView.vue'
import { isSuperAdmin, isDefaultTenant, getCurrentTenantId } from '../store'

const { t } = useI18n()

const boardState = inject<{
  board: Ref<BoardPayload | null>
  days: Ref<number>
  loading: Ref<boolean>
  error: Ref<string | null>
  load: () => Promise<void>
}>('dashboardBoard')!

const drawerState = inject<{
  models: Ref<ModelUsage[]>
  hotKeys: Ref<HotApiKeyEntry[]>
  drawerLoading: Ref<boolean>
  loadDrawerData: () => Promise<void>
}>('dashboardDrawer')!

const dashboardTab = inject<{
  activeTab: Ref<DashboardTabId>
  switchTab: (tab: DashboardTabId) => void
}>('dashboardTab')!

const dashboardActions = inject<{ refreshBoard: () => Promise<void> }>('dashboardActions')!

const statsDrawerRef = ref<InstanceType<typeof StatsDrawer> | null>(null)
const activeRequestId = ref<string | null>(null)

const days = boardState.days
const loading = boardState.loading
const error = boardState.error
const activeTab = dashboardTab.activeTab

const tenantLabel = computed(() => {
  const tenantId = getCurrentTenantId()
  if (isSuperAdmin() && isDefaultTenant()) return t('dashboard.tenantLabel.default')
  if (isDefaultTenant()) return t('dashboard.tenantLabel.super')
  return t('dashboard.tenantLabel.tenant', { tenantId })
})

function openRequestDetail(id: string) {
  activeRequestId.value = id
}

function closeRequestDrawer() {
  activeRequestId.value = null
}

async function openStatsDrawer(tab: 'apikeys' | 'models') {
  await drawerState.loadDrawerData()
  statsDrawerRef.value?.open(tab)
}

async function onRefresh() {
  await dashboardActions.refreshBoard()
}

async function onDaysChange() {
  if (activeTab.value === 'board') {
    await boardState.load()
  }
}
</script>

<template>
  <div class="dashboard-v2">
    <!-- 紧凑型页面头部 - 单行布局 -->
    <div class="page-header">
      <div class="page-header-left">
        <h2>{{ t('dashboard.title') }}</h2>
        
        <!-- Tab 切换器（集成到标题旁） -->
        <div class="tab-switcher">
          <button
            type="button"
            class="tab-btn"
            :class="{ 'tab-btn--active': activeTab === 'board' }"
            @click="dashboardTab.switchTab('board')"
            :title="$t('dashboard.tabs.board')"
          >
            {{ $t('dashboard.tabs.board') }}
          </button>
          <button
            type="button"
            class="tab-btn"
            :class="{ 'tab-btn--active': activeTab === 'stream' }"
            @click="dashboardTab.switchTab('stream')"
            :title="$t('dashboard.tabs.liveStream')"
          >
            {{ $t('dashboard.tabs.liveStream') }}
          </button>
          <button
            type="button"
            class="tab-btn"
            :class="{ 'tab-btn--active': activeTab === 'stats' }"
            @click="dashboardTab.switchTab('stats')"
            :title="$t('dashboard.tabs.sessionStats')"
          >
            {{ $t('dashboard.tabs.sessionStats') }}
          </button>
          <button
            type="button"
            class="tab-btn"
            :class="{ 'tab-btn--active': activeTab === 'selfcheck' }"
            @click="dashboardTab.switchTab('selfcheck')"
            :title="t('dashboard.tabs.selfcheck')"
          >
            {{ t('dashboard.tabs.selfcheck') }}
          </button>
        </div>
        
        <MemoraStatusButton />
      </div>
      
      <div class="page-header-right">
        <!-- 快捷按钮 -->
        <button 
          type="button" 
          class="quick-btn"
          @click="openStatsDrawer('apikeys')"
          :disabled="loading"
          :title="t('dashboard.v2.quickApiKeyTitle')"
        >
          {{ t('dashboard.v2.quickApiKey') }}
        </button>
        <button 
          type="button" 
          class="quick-btn"
          @click="openStatsDrawer('models')"
          :disabled="loading"
          :title="t('dashboard.v2.quickModelsTitle')"
        >
          {{ t('dashboard.v2.quickModels') }}
        </button>
        
        <!-- 租户标签 -->
        <span class="tenant-badge" :class="{ 'tenant-badge--admin': isSuperAdmin(), 'tenant-badge--default': isDefaultTenant() }">
          {{ tenantLabel }}
        </span>
        
        <!-- 时间范围选择 -->
        <select v-model.number="days" class="days-select" @change="onDaysChange">
          <option :value="1">{{ t('dashboard.range.today') }}</option>
          <option :value="7">{{ t('dashboard.range.last7d') }}</option>
          <option :value="30">{{ t('dashboard.range.last30d') }}</option>
          <option :value="90">{{ t('dashboard.range.last90d') }}</option>
        </select>
        
        <!-- 刷新按钮 -->
        <button class="btn btn-refresh" @click="onRefresh" :disabled="loading" :title="t('dashboard.v2.refreshData')">
          <span v-if="loading">⏳</span>
          <span v-else>🔄</span>
        </button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger" role="alert">
      <span class="alert-icon" aria-hidden="true">⚠️</span>
      <span class="alert-text">{{ error }}</span>
      <button
        type="button"
        class="btn btn-sm alert-retry"
        :disabled="loading"
        :aria-label="t('dashboard.v2.reloadAria')"
        @click="onRefresh"
      >
        <span v-if="loading">⏳</span>
        <span v-else>🔄 {{ t('dashboard.v2.retry') }}</span>
      </button>
    </div>

    <BoardPanel v-if="activeTab === 'board'" />

    <SessionStatsPanel v-if="activeTab === 'stats'" style="margin-bottom: 20px;" />

    <SelfCheckPanel v-if="activeTab === 'selfcheck'" />

    <LiveRequestStreamV2
      v-show="activeTab === 'stream'"
      @open-detail="openRequestDetail"
    />

    <StatsDrawer
      ref="statsDrawerRef"
      :hot-keys="drawerState.hotKeys.value"
      :models="drawerState.models.value"
      :days="days"
      :loading="drawerState.drawerLoading.value"
    />

    <!-- 请求详情抽屉 -->
    <RequestLogDrawer :request-id="activeRequestId" @close="closeRequestDrawer" />
  </div>
</template>

<style scoped>
.dashboard-v2 {
  max-width: 100%;
}

.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
  gap: 12px;
  flex-wrap: nowrap;
  min-height: 40px;
}

.page-header-left {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}

.page-header-left h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
  white-space: nowrap;
}

/* Tab 切换器 */
.tab-switcher {
  display: inline-flex;
  gap: 4px;
  padding: 3px;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
}

.tab-btn {
  padding: 4px 12px;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--text-secondary, #8b949e);
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
}

.tab-btn:hover {
  color: var(--text, #e6edf3);
  background: var(--bg, #0f1117);
}

.tab-btn--active {
  background: var(--accent, #6366f1);
  color: white;
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.2);
}

.page-header-right {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: nowrap;
  flex-shrink: 0;
}

.quick-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 6px 12px;
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
}

.quick-btn:hover:not(:disabled) {
  background: var(--bg-subtle, #161b22);
  border-color: var(--accent, #6366f1);
}

.quick-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.tenant-badge {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  background: var(--surface-secondary, #f3f4f6);
  color: var(--text-secondary, #6b7280);
  white-space: nowrap;
  flex-shrink: 0;
}

.tenant-badge--admin {
  background: rgba(59, 130, 246, 0.1);
  color: #3b82f6;
}

.tenant-badge--default {
  background: rgba(34, 197, 94, 0.1);
  color: #22c55e;
}

.days-select {
  width: auto;
  padding: 6px 12px;
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  font-size: 13px;
  cursor: pointer;
  white-space: nowrap;
  flex-shrink: 0;
  min-width: 80px;
}

.btn-refresh {
  padding: 6px 12px;
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  font-size: 13px;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
  flex-shrink: 0;
}

.btn-refresh:hover:not(:disabled) {
  background: var(--bg-subtle, #161b22);
  border-color: var(--accent, #6366f1);
}

.btn-refresh:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.background-tasks-banner {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 16px;
  padding: 10px 14px;
  margin-bottom: 16px;
  border-radius: var(--radius, 6px);
  font-size: 13px;
  background: rgba(99, 102, 241, 0.08);
  border: 1px solid rgba(99, 102, 241, 0.30);
  color: var(--text, #e6edf3);
}

.background-tasks-banner--active {
  background: rgba(251, 191, 36, 0.10);
  border: 1px solid rgba(251, 191, 36, 0.45);
}

.background-tasks-banner strong {
  color: var(--warning, #fbbf24);
  font-weight: 600;
}

.background-tasks-banner a {
  color: var(--accent, #6366f1);
  text-decoration: underline;
  font-size: 12px;
}

.background-tasks-banner a:hover {
  color: var(--accent-hover, #818cf8);
}

.background-tasks-hint {
  color: var(--text-secondary, #8b949e);
  font-size: 12px;
  font-style: italic;
}

.stats-section {
  margin-bottom: 20px;
}

.stats-row {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  padding: 4px 0;
  margin-bottom: 12px;
  /* 2026-07-07: 补上 flex-wrap: wrap, 移除 overflow-x: auto
     统计卡片行在窄屏时自动折行 */
}

.stat-mini {
  flex: 0 0 auto;
  min-width: 100px;
  padding: 8px 12px;
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  background: var(--card, #1c2128);
  transition: all 0.15s ease;
}

.stat-mini:hover {
  border-color: var(--accent, #6366f1);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.2);
}

.stat-mini__label {
  font-size: 11px;
  color: var(--text-secondary, #8b949e);
  white-space: nowrap;
  margin-bottom: 4px;
  font-weight: 500;
}

.stat-mini__value {
  font-size: 18px;
  font-weight: 700;
  color: var(--text, #e6edf3);
  font-variant-numeric: tabular-nums;
}

.stat-mini--skeleton {
  background: linear-gradient(90deg, var(--bg-subtle, #161b22) 25%, var(--border, #30363d) 50%, var(--bg-subtle, #161b22) 75%);
  background-size: 200% 100%;
  animation: skeleton-loading 1.5s ease-in-out infinite;
  min-height: 56px;
}

@keyframes skeleton-loading {
  0% { background-position: 200% 0; }
  100% { background-position: -200% 0; }
}

/* 错误态增强：图标 + 文本 + 重试按钮 */
.alert {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.alert-icon {
  font-size: 16px;
  flex-shrink: 0;
}

.alert-text {
  flex: 1 1 auto;
  min-width: 0;
  word-break: break-word;
}

.alert-retry {
  flex-shrink: 0;
  margin-left: auto;
}

/* 空状态 */
.empty-state {
  padding: 48px 20px;
  text-align: center;
  color: var(--text-secondary, #8b949e);
  border: 1px dashed var(--border, #30363d);
  border-radius: var(--radius, 6px);
  background: var(--bg-subtle, #161b22);
  margin-top: 24px;
}

.empty-state__icon {
  font-size: 36px;
  margin-bottom: 12px;
}

.empty-state__title {
  font-size: 16px;
  font-weight: 600;
  color: var(--text, #e6edf3);
  margin-bottom: 6px;
}

.empty-state__hint {
  font-size: 13px;
  line-height: 1.6;
  max-width: 480px;
  margin: 0 auto;
}

.empty-state__hint code {
  background: var(--bg, #0f1117);
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 12px;
  border: 1px solid var(--border, #30363d);
}

@media (max-width: 1024px) {
  .page-header {
    flex-wrap: wrap;
  }
  
  .page-header-right {
    flex-wrap: wrap;
  }
}

@media (max-width: 768px) {
  .page-header {
    flex-direction: column;
    align-items: stretch;
  }

  .page-header-left,
  .page-header-right {
    width: 100%;
  }

  .page-header-right {
    justify-content: space-between;
  }

  .stat-mini {
    /* 窄屏允许每张卡占约一半宽度，剩余自然折行 */
    flex: 1 1 calc(50% - 8px);
    min-width: 0;
  }
}

@media (max-width: 480px) {
  .stat-mini {
    /* 更窄屏幕：每张卡占满一行 */
    flex: 1 1 100%;
  }
}
</style>
