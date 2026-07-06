<script setup lang="ts">
// DashboardViewV2.vue — 新版仪表盘（紧凑统计 + 泳道系统）
// 2026-07-05: 单行统计卡片 + 多泳道实时请求流
// 2026-07-05 v3: 使用父组件提供的共享数据源

import { ref, computed, inject, type Ref } from 'vue'
import { RouterLink } from 'vue-router'
import { localeRef } from '../i18n'
import MemoraStatusButton from '../components/MemoraStatusButton.vue'
import LiveRequestStreamV2 from '../components/LiveRequestStreamV2.vue'
import StatsDrawer from '../components/StatsDrawer.vue'
import RequestLogDrawer from '../components/RequestLogDrawer.vue'
import type {
  UsageSummary,
  ModelUsage,
  DashboardOverview,
  HotApiKeyEntry,
  CompressionStats,
  ModelDiscoveryStatusResponse,
} from '../api'
import { isSuperAdmin, isDefaultTenant, getCurrentTenantId } from '../store'

// 从父组件注入共享数据
const dashboardData = inject<{
  days: Ref<number>
  loading: Ref<boolean>
  error: Ref<string | null>
  summary: Ref<UsageSummary | null>
  overview: Ref<DashboardOverview | null>
  models: Ref<ModelUsage[]>
  hotKeys: Ref<HotApiKeyEntry[]>
  compStats: Ref<CompressionStats | null>
  discoveryStatus: Ref<ModelDiscoveryStatusResponse | null>
  load: () => Promise<void>
}>('dashboardData')!

// 从父组件注入版本切换器
const versionSwitcher = inject<{
  version: Ref<'v1' | 'v2'>
  switchVersion: (v: 'v1' | 'v2') => void
}>('versionSwitcher')!

// 从父组件注入泳道重新初始化key
const swimLaneReinitKey = inject<Ref<number>>('swimLaneReinitKey')!

const statsDrawerRef = ref<InstanceType<typeof StatsDrawer> | null>(null)
const activeRequestId = ref<string | null>(null)

// 使用注入的数据
const days = dashboardData.days
const loading = dashboardData.loading
const error = dashboardData.error
const summary = dashboardData.summary
const overview = dashboardData.overview
const models = dashboardData.models
const hotKeys = dashboardData.hotKeys
const compStats = dashboardData.compStats
const discoveryStatus = dashboardData.discoveryStatus
const load = dashboardData.load

// 版本切换
const version = versionSwitcher.version
const switchVersion = versionSwitcher.switchVersion

// Tenant info
const tenantLabel = computed(() => {
  const tenantId = getCurrentTenantId()
  const isAdmin = isSuperAdmin()
  const isDefault = isDefaultTenant()
  
  if (isAdmin && isDefault) {
    return '整站数据'
  } else if (isDefault) {
    return '默认租户'
  } else {
    return `租户: ${tenantId}`
  }
})

function fmt(n: number | undefined, decimals = 0) {
  if (n === undefined || n === null) return '—'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return Number(n).toFixed(decimals)
}

function fmtCost(v: number | undefined) {
  if (v === undefined || v === null) return '—'
  return '$' + Number(v).toFixed(4)
}

function fmtPct(v: number | undefined) {
  if (v === undefined || v === null) return '—'
  return (Number(v) * 100).toFixed(1) + '%'
}

function fmtDate(v: string | null | undefined) {
  if (!v) return '—'
  return new Date(v).toLocaleString(localeRef.value, { dateStyle: 'short', timeStyle: 'short' })
}


function openRequestDetail(id: string) {
  activeRequestId.value = id
}

function closeRequestDrawer() {
  activeRequestId.value = null
}

function openStatsDrawer(tab: 'apikeys' | 'models') {
  statsDrawerRef.value?.open(tab)
}
</script>

<template>
  <div class="dashboard-v2">
    <!-- 紧凑型页面头部 - 单行布局 + 版本切换器 -->
    <div class="page-header">
      <div class="page-header-left">
        <h2>仪表盘</h2>
        
        <!-- 版本切换器（集成到标题旁） -->
        <div class="version-switcher">
          <button
            type="button"
            class="version-btn"
            :class="{ 'version-btn--active': version === 'v2' }"
            @click="switchVersion('v2')"
            title="新版仪表盘（推荐）- 泳道可视化"
          >
            V2
          </button>
          <button
            type="button"
            class="version-btn"
            :class="{ 'version-btn--active': version === 'v1' }"
            @click="switchVersion('v1')"
            title="旧版仪表盘"
          >
            V1
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
          title="查看API Key排行"
        >
          📊 API Key
        </button>
        <button 
          type="button" 
          class="quick-btn"
          @click="openStatsDrawer('models')"
          :disabled="loading"
          title="查看模型统计"
        >
          📈 模型
        </button>
        
        <!-- 租户标签 -->
        <span class="tenant-badge" :class="{ 'tenant-badge--admin': isSuperAdmin(), 'tenant-badge--default': isDefaultTenant() }">
          {{ tenantLabel }}
        </span>
        
        <!-- 时间范围选择 -->
        <select v-model.number="days" class="days-select" @change="load">
          <option :value="1">今日</option>
          <option :value="7">近 7 天</option>
          <option :value="30">近 30 天</option>
          <option :value="90">近 90 天</option>
        </select>
        
        <!-- 刷新按钮 -->
        <button class="btn btn-refresh" @click="load" :disabled="loading" title="刷新数据">
          <span v-if="loading">⏳</span>
          <span v-else>🔄</span>
        </button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <!-- 后台任务横幅 -->
    <div
      v-if="discoveryStatus?.running"
      class="background-tasks-banner background-tasks-banner--active"
    >
      <strong>后台任务进行中</strong>
      <span>模型发现（{{ discoveryStatus.running.trigger }}）</span>
      <span>开始 {{ fmtDate(discoveryStatus.running.started_at) }}</span>
      <span>心跳 {{ fmtDate(discoveryStatus.running.heartbeat_at) }}</span>
      <span class="background-tasks-hint">管理页可能变慢</span>
      <RouterLink to="/models">查看详情</RouterLink>
    </div>
    <div
      v-else-if="discoveryStatus?.latest"
      class="background-tasks-banner"
    >
      <span>最近模型发现：{{ discoveryStatus.latest.status }}</span>
      <span>{{ fmtDate(discoveryStatus.latest.finished_at || discoveryStatus.latest.started_at) }}</span>
      <RouterLink to="/models">模型页</RouterLink>
    </div>

    <!-- 紧凑统计行 -->
    <div class="stats-section">
      <div class="stats-row" v-if="summary && overview">
        <!-- 9个指标 -->
        <div class="stat-mini">
          <div class="stat-mini__label">总请求数</div>
          <div class="stat-mini__value">{{ fmt(summary.total_requests) }}</div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">总Token</div>
          <div class="stat-mini__value">{{ fmt((summary.total_prompt_tokens ?? 0) + (summary.total_completion_tokens ?? 0)) }}</div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">总费用</div>
          <div class="stat-mini__value">{{ fmtCost(summary.total_cost_usd) }}</div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">成功率</div>
          <div class="stat-mini__value" :style="{ color: (summary.success_rate ?? 1) > 0.95 ? 'var(--success)' : 'var(--warning)' }">
            {{ fmtPct(summary.success_rate) }}
          </div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">平均延迟</div>
          <div class="stat-mini__value">{{ fmt(summary.avg_latency_ms) }}ms</div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">API Key</div>
          <div class="stat-mini__value">{{ fmt(overview.active_api_keys) }}</div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">模型数</div>
          <div class="stat-mini__value">{{ fmt(overview.active_models_in_window) }}</div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">供应商</div>
          <div class="stat-mini__value">{{ fmt(overview.active_providers) }}</div>
        </div>
        <div class="stat-mini" v-if="compStats">
          <div class="stat-mini__label">会话压缩</div>
          <div class="stat-mini__value">{{ compStats.compressed_total }}</div>
        </div>
      </div>
      <div class="stats-row stats-row--loading" v-else-if="loading">
        <div class="stat-mini stat-mini--skeleton" v-for="i in 9" :key="i"></div>
      </div>
    </div>

    <!-- 实时请求流V2（带重新初始化key） -->
    <LiveRequestStreamV2 
      :key="swimLaneReinitKey" 
      @open-detail="openRequestDetail" 
    />

    <!-- 抽屉组件 -->
    <StatsDrawer
      ref="statsDrawerRef"
      :hot-keys="hotKeys"
      :models="models"
      :days="days"
      :loading="loading"
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

/* 版本切换器 */
.version-switcher {
  display: inline-flex;
  gap: 3px;
  padding: 2px;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 5px;
}

.version-btn {
  padding: 3px 10px;
  border: 1px solid transparent;
  border-radius: 3px;
  background: transparent;
  color: var(--text-secondary, #8b949e);
  font-size: 11px;
  font-weight: 600;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
  min-width: 36px;
}

.version-btn:hover {
  color: var(--text, #e6edf3);
  background: var(--bg, #0f1117);
}

.version-btn--active {
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
  overflow-x: auto;
  padding: 4px 0;
  margin-bottom: 12px;
}

.stats-row::-webkit-scrollbar {
  height: 6px;
}

.stats-row::-webkit-scrollbar-track {
  background: var(--bg-subtle, #161b22);
  border-radius: 3px;
}

.stats-row::-webkit-scrollbar-thumb {
  background: var(--border, #30363d);
  border-radius: 3px;
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
    min-width: 90px;
  }
}
</style>
