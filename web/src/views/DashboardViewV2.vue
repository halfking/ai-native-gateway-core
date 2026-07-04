<script setup lang="ts">
// DashboardViewV2.vue — 新版仪表盘（紧凑统计 + 泳道系统）
// 2026-07-05: 单行统计卡片 + 多泳道实时请求流

import { ref, onMounted, onUnmounted, computed, watch } from 'vue'
import { RouterLink } from 'vue-router'
import MemoraStatusButton from '../components/MemoraStatusButton.vue'
import LiveRequestStreamV2 from '../components/LiveRequestStreamV2.vue'
import StatsDrawer from '../components/StatsDrawer.vue'
import RequestLogDrawer from '../components/RequestLogDrawer.vue'
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
import { isSuperAdmin, isDefaultTenant, getCurrentTenantId } from '../store'

const days = ref(7)
const summary = ref<UsageSummary | null>(null)
const overview = ref<DashboardOverview | null>(null)
const models = ref<ModelUsage[]>([])
const hotKeys = ref<HotApiKeyEntry[]>([])
const compStats = ref<CompressionStats | null>(null)
const loading = ref(false)
const error = ref('')

const statsDrawerRef = ref<InstanceType<typeof StatsDrawer> | null>(null)
const activeRequestId = ref<string | null>(null)

let statsRecalibrateTimer: ReturnType<typeof setInterval> | null = null

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

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [s, m, o, h] = await Promise.all([
      getUsageSummary(days.value),
      getUsageByModel(days.value),
      getDashboardOverview(days.value),
      getHotApiKeys(days.value, 10),
    ])
    summary.value = s
    models.value = m
    overview.value = o
    hotKeys.value = h
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

// Live stream integration
const {
  requests: liveRequests,
  onRequestEvicted,
  reset: resetLiveStream,
} = useLiveStream()

const seenLiveRequestIds = new Set<string>()

onRequestEvicted((id: string) => {
  seenLiveRequestIds.delete(id)
})

function applyIncrementalStats() {
  if (!summary.value) return
  const items = liveRequests.value
  const added: typeof items = []
  for (const r of items) {
    if (r.type === 'idle_marker' || !r.request_id) continue
    if (seenLiveRequestIds.has(r.request_id)) continue
    seenLiveRequestIds.add(r.request_id)
    added.push(r)
  }
  if (added.length === 0) return

  const costDelta = added.reduce((s, r) => s + (r.cost_usd ?? 0), 0)
  const successes = added.filter((r) => r.status === 'success').length

  summary.value.total_requests = (summary.value.total_requests ?? 0) + added.length
  summary.value.total_prompt_tokens = (summary.value.total_prompt_tokens ?? 0) +
    added.reduce((s, r) => s + (r.prompt_tokens ?? 0), 0)
  summary.value.total_completion_tokens = (summary.value.total_completion_tokens ?? 0) +
    added.reduce((s, r) => s + (r.completion_tokens ?? 0), 0)
  summary.value.total_cost_usd = (summary.value.total_cost_usd ?? 0) + costDelta

  // Latency
  const addedLatency = added.reduce((s, r) => s + (r.latency_ms ?? 0), 0)
  const addedLatencyN = added.filter((r) => r.latency_ms != null).length
  if (addedLatencyN > 0) {
    const prevAvg = summary.value.avg_latency_ms ?? 0
    const prevN = Math.max(0, (summary.value.total_requests ?? 0) - added.length)
    const totalN = prevN + addedLatencyN
    if (totalN > 0) {
      summary.value.avg_latency_ms = Math.round((prevAvg * prevN + addedLatency) / totalN)
    }
  }

  // Success rate
  const knownSuccesses = Math.round((summary.value.success_rate ?? 1) * (summary.value.total_requests - added.length))
  const newSuccesses = knownSuccesses + successes
  if (summary.value.total_requests > 0) {
    summary.value.success_rate = newSuccesses / summary.value.total_requests
  }
}

watch(liveRequests, applyIncrementalStats, { deep: true })

function scheduleStatsRecalibrate() {
  if (statsRecalibrateTimer) clearInterval(statsRecalibrateTimer)
  statsRecalibrateTimer = setInterval(async () => {
    try {
      const fresh = await getUsageSummary(days.value)
      summary.value = fresh
      seenLiveRequestIds.clear()
      resetLiveStream()
    } catch {
      /* non-blocking */
    }
  }, 5 * 60 * 1000)
}

onMounted(() => {
  void load()
  scheduleStatsRecalibrate()
})

onUnmounted(() => {
  if (statsRecalibrateTimer) clearInterval(statsRecalibrateTimer)
})

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
    <div class="page-header">
      <div class="page-header-title">
        <h2>仪表盘</h2>
        <span class="version-badge">V2</span>
        <MemoraStatusButton />
      </div>
      <div class="page-header-actions">
        <span class="tenant-badge" :class="{ 'tenant-badge--admin': isSuperAdmin(), 'tenant-badge--default': isDefaultTenant() }">
          {{ tenantLabel }}
        </span>
        <select v-model.number="days" class="days-select" @change="load">
          <option :value="1">今日</option>
          <option :value="7">近 7 天</option>
          <option :value="30">近 30 天</option>
          <option :value="90">近 90 天</option>
        </select>
        <button class="btn btn-ghost btn-sm" @click="load" :disabled="loading">刷新</button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <!-- 紧凑统计行 + 快捷按钮 -->
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
      
      <!-- 快捷按钮 -->
      <div class="quick-actions">
        <button 
          type="button" 
          class="quick-btn"
          @click="openStatsDrawer('apikeys')"
          :disabled="loading"
        >
          📊 API Key 排行
        </button>
        <button 
          type="button" 
          class="quick-btn"
          @click="openStatsDrawer('models')"
          :disabled="loading"
        >
          📈 模型统计
        </button>
      </div>
    </div>

    <!-- 实时请求流V2 -->
    <LiveRequestStreamV2 @open-detail="openRequestDetail" />

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
  flex-wrap: wrap;
}

.page-header-title {
  display: flex;
  align-items: center;
  gap: 10px;
}

.page-header-title h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.version-badge {
  display: inline-flex;
  align-items: center;
  padding: 2px 8px;
  border-radius: 10px;
  font-size: 11px;
  font-weight: 600;
  background: rgba(99, 102, 241, 0.15);
  color: #6366f1;
}

.page-header-actions {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
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
  padding: 6px 12px;
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  font-size: 13px;
  cursor: pointer;
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

.quick-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.quick-btn {
  padding: 8px 16px;
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.15s ease;
}

.quick-btn:hover:not(:disabled) {
  background: var(--bg-subtle, #161b22);
  border-color: var(--accent, #6366f1);
  transform: translateY(-1px);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.2);
}

.quick-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

@media (max-width: 768px) {
  .page-header {
    flex-direction: column;
    align-items: stretch;
  }
  
  .page-header-actions {
    flex-direction: column;
  }
  
  .stat-mini {
    min-width: 90px;
  }
}
</style>
