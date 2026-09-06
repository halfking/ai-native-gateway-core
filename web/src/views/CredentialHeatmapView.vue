<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getCredentialHeatmap, type HeatmapCredential, type HeatmapBucket, type HeatmapModel } from '../api'
import { useCredentialLabels } from '../composables/useCredentialLabels'

const { t } = useI18n()
const { credentialDisplayName, loadCredentialLabels } = useCredentialLabels()

// Time range presets
type TimeRangePreset = 'today' | '1h' | '6h' | '24h' | 'yesterday' | '7d' | 'month' | 'custom'
const timeRangePreset = ref<TimeRangePreset>('today')
const customTimeStart = ref('')
const customTimeEnd = ref('')

// Granularity
type Granularity = '1m' | '5m' | '15m' | '1h' | '1d'
const granularity = ref<Granularity>('5m')

// Filters
const providerFilter = ref(0)
const modelFilter = ref<string[]>([])
const showAnomaliesOnly = ref(false)
const excludeSelfTest = ref(true)

// Data
const loading = ref(false)
const heatmapData = ref<HeatmapCredential[]>([])
const meta = ref<any>(null)
const error = ref<string | null>(null)

// Expanded credentials (store in localStorage)
const expandedCredentials = ref<Set<number>>(new Set())
const EXPANDED_STORAGE_KEY = 'credential_heatmap_expanded'

// Auto refresh
const autoRefresh = ref(false)
const refreshInterval = ref(30) // seconds
let refreshTimer: number | null = null

// Load expanded state from localStorage
function loadExpandedState() {
  try {
    const stored = localStorage.getItem(EXPANDED_STORAGE_KEY)
    if (stored) {
      const ids = JSON.parse(stored) as number[]
      expandedCredentials.value = new Set(ids)
    }
  } catch (e) {
    console.error('Failed to load expanded state', e)
  }
}

// Save expanded state to localStorage
function saveExpandedState() {
  try {
    localStorage.setItem(EXPANDED_STORAGE_KEY, JSON.stringify(Array.from(expandedCredentials.value)))
  } catch (e) {
    console.error('Failed to save expanded state', e)
  }
}

// Toggle credential expansion
function toggleExpanded(credentialId: number) {
  if (expandedCredentials.value.has(credentialId)) {
    expandedCredentials.value.delete(credentialId)
  } else {
    expandedCredentials.value.add(credentialId)
  }
  saveExpandedState()
}

// Compute time range
const computedTimeRange = computed(() => {
  const now = new Date()
  let start: Date
  let end: Date = now

  switch (timeRangePreset.value) {
    case 'today':
      start = new Date(now.getFullYear(), now.getMonth(), now.getDate(), 0, 0, 0)
      break
    case '1h':
      start = new Date(now.getTime() - 60 * 60 * 1000)
      break
    case '6h':
      start = new Date(now.getTime() - 6 * 60 * 60 * 1000)
      break
    case '24h':
      start = new Date(now.getTime() - 24 * 60 * 60 * 1000)
      break
    case 'yesterday':
      start = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1, 0, 0, 0)
      end = new Date(now.getFullYear(), now.getMonth(), now.getDate(), 0, 0, 0)
      break
    case '7d':
      start = new Date(now.getTime() - 7 * 24 * 60 * 60 * 1000)
      break
    case 'month':
      start = new Date(now.getFullYear(), now.getMonth(), 1, 0, 0, 0)
      break
    case 'custom':
      if (!customTimeStart.value || !customTimeEnd.value) {
        start = new Date(now.getTime() - 24 * 60 * 60 * 1000)
      } else {
        start = new Date(customTimeStart.value)
        end = new Date(customTimeEnd.value)
      }
      break
    default:
      start = new Date(now.getTime() - 24 * 60 * 60 * 1000)
  }

  return {
    start: start.toISOString(),
    end: end.toISOString(),
  }
})

// Auto-suggest granularity based on time range
const suggestedGranularity = computed(() => {
  const range = computedTimeRange.value
  const start = new Date(range.start)
  const end = new Date(range.end)
  const durationHours = (end.getTime() - start.getTime()) / (1000 * 60 * 60)

  if (durationHours <= 1) return '1m'
  if (durationHours <= 6) return '5m'
  if (durationHours <= 24) return '15m'
  if (durationHours <= 168) return '1h' // 7 days
  return '1d'
})

// Watch time range and suggest granularity
watch(timeRangePreset, () => {
  granularity.value = suggestedGranularity.value
})

// Status color mapping
const statusColors: Record<string, string> = {
  ready: '#10b981',
  degraded: '#f59e0b',
  cooling: '#f97316',
  rate_limited: '#f59e0b',
  unreachable: '#ef4444',
  auth_failed: '#dc2626',
  manual_disabled: '#6b7280',
  no_data: '#e5e7eb',
}

function getStatusColor(status: string): string {
  return statusColors[status] || '#9ca3af'
}

// Load heatmap data
async function loadHeatmap() {
  loading.value = true
  error.value = null
  
  try {
    const range = computedTimeRange.value
    const response = await getCredentialHeatmap(
      range.start,
      range.end,
      granularity.value,
      {
        excludeSelfTest: excludeSelfTest.value,
        models: modelFilter.value.length ? modelFilter.value : undefined,
      }
    )
    
    heatmapData.value = response.credentials
    meta.value = response.meta
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    console.error('Failed to load heatmap', e)
  } finally {
    loading.value = false
  }
}

// Filtered credentials
const filteredCredentials = computed(() => {
  let result = heatmapData.value

  if (providerFilter.value > 0) {
    result = result.filter(c => c.credential_id === providerFilter.value)
  }

  if (showAnomaliesOnly.value) {
    result = result.filter(c => {
      return c.models.some(m => 
        m.buckets.some(b => 
          b.status !== 'ready' && b.status !== 'no_data'
        )
      )
    })
  }

  return result
})

// Auto refresh
function startAutoRefresh() {
  if (refreshTimer) return
  autoRefresh.value = true
  refreshTimer = window.setInterval(() => loadHeatmap(), refreshInterval.value * 1000)
}

function stopAutoRefresh() {
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
  autoRefresh.value = false
}

function toggleAutoRefresh() {
  autoRefresh.value ? stopAutoRefresh() : startAutoRefresh()
}

// Cell click handler
const selectedBucket = ref<{
  bucket: HeatmapBucket
  credential: HeatmapCredential
  model: string
} | null>(null)

function onCellClick(bucket: HeatmapBucket, credential: HeatmapCredential, model: string) {
  selectedBucket.value = { bucket, credential, model }
}

function closeDetailPopover() {
  selectedBucket.value = null
}

// Format timestamp
function formatTime(ts: string): string {
  const d = new Date(ts)
  const h = String(d.getHours()).padStart(2, '0')
  const m = String(d.getMinutes()).padStart(2, '0')
  return `${h}:${m}`
}

// Lifecycle
onMounted(() => {
  loadExpandedState()
  loadCredentialLabels()
  loadHeatmap()
})

onUnmounted(() => {
  stopAutoRefresh()
})
</script>

<template>
  <div class="heatmap-container">
    <!-- Toolbar -->
    <div class="heatmap-toolbar">
      <div class="toolbar-row">
        <span class="label">时间范围</span>
        <select v-model="timeRangePreset" class="field-input">
          <option value="today">今天</option>
          <option value="1h">最近1小时</option>
          <option value="6h">最近6小时</option>
          <option value="24h">最近24小时</option>
          <option value="yesterday">昨天</option>
          <option value="7d">最近7天</option>
          <option value="month">本月</option>
          <option value="custom">自定义</option>
        </select>

        <span class="label">粒度</span>
        <select v-model="granularity" class="field-input">
          <option value="1m">1分钟</option>
          <option value="5m">5分钟</option>
          <option value="15m">15分钟</option>
          <option value="1h">1小时</option>
          <option value="1d">1天</option>
        </select>
        <span class="hint-text" v-if="granularity !== suggestedGranularity">
          (建议: {{ suggestedGranularity }})
        </span>

        <label class="checkbox-label">
          <input type="checkbox" v-model="excludeSelfTest" />
          排除自检
        </label>

        <label class="checkbox-label">
          <input type="checkbox" v-model="showAnomaliesOnly" />
          仅显示异常
        </label>

        <span class="spacer"></span>

        <label class="checkbox-label">
          <input type="checkbox" :checked="autoRefresh" @change="toggleAutoRefresh" />
          自动刷新
        </label>

        <select v-model.number="refreshInterval" class="field-input" :disabled="!autoRefresh">
          <option :value="10">10秒</option>
          <option :value="30">30秒</option>
          <option :value="60">60秒</option>
        </select>

        <button class="btn btn-sm btn-primary" @click="loadHeatmap" :disabled="loading">
          {{ loading ? '加载中...' : '刷新' }}
        </button>
      </div>

      <div class="toolbar-row" v-if="timeRangePreset === 'custom'">
        <span class="label">开始时间</span>
        <input type="datetime-local" v-model="customTimeStart" class="field-input" />
        <span class="label">结束时间</span>
        <input type="datetime-local" v-model="customTimeEnd" class="field-input" />
      </div>
    </div>

    <!-- Error message -->
    <div v-if="error" class="error-banner">
      ⚠️ {{ error }}
    </div>

    <!-- Meta info -->
    <div v-if="meta" class="meta-info">
      时间范围: {{ formatTime(meta.time_start) }} - {{ formatTime(meta.time_end) }} ·
      粒度: {{ meta.granularity }} ·
      时间桶: {{ meta.bucket_count }} ·
      {{ meta.cache_hit ? '缓存命中' : '实时生成' }} ·
      {{ meta.duration_ms }}ms
    </div>

    <!-- Loading state -->
    <div v-if="loading && !heatmapData.length" class="loading-state">
      <div class="spinner"></div>
      <p>加载热力图数据...</p>
    </div>

    <!-- Empty state -->
    <div v-else-if="!loading && !filteredCredentials.length" class="empty-state">
      <p>暂无数据</p>
      <p class="hint-text">请调整时间范围或筛选条件</p>
    </div>

    <!-- Heatmap grid -->
    <div v-else class="heatmap-grid">
      <div v-for="cred in filteredCredentials" :key="cred.credential_id" class="credential-row">
        <div class="credential-header">
          <button class="expand-btn" @click="toggleExpanded(cred.credential_id)">
            {{ expandedCredentials.has(cred.credential_id) ? '▼' : '▶' }}
          </button>
          <div class="credential-info">
            <div class="credential-label">
              {{ credentialDisplayName(cred.credential_id, cred.label || `凭据 #${cred.credential_id}`) }}
            </div>
            <div class="credential-provider">{{ cred.provider_name }}</div>
          </div>
        </div>

        <!-- Aggregate row (all models combined) -->
        <div class="heatmap-row">
          <div class="row-label">汇总</div>
          <div class="cells-container">
            <!-- Aggregate logic: show worst status across all models for each time bucket -->
            <div v-for="(bucket, idx) in cred.models[0]?.buckets || []" :key="idx" 
                 class="heatmap-cell"
                 :style="{ backgroundColor: getStatusColor(bucket.status) }"
                 :title="`${formatTime(bucket.time_bucket)}: ${bucket.status}`">
            </div>
          </div>
        </div>

        <!-- Expanded model rows -->
        <template v-if="expandedCredentials.has(cred.credential_id)">
          <div v-for="model in cred.models" :key="model.raw_model_name" class="heatmap-row model-row">
            <div class="row-label model-label">
              <code>{{ model.raw_model_name }}</code>
            </div>
            <div class="cells-container">
              <div v-for="(bucket, idx) in model.buckets" :key="idx"
                   class="heatmap-cell"
                   :style="{ backgroundColor: getStatusColor(bucket.status) }"
                   :title="`${formatTime(bucket.time_bucket)}: ${bucket.total_requests} 请求, ${(bucket.success_rate * 100).toFixed(1)}% 成功率`"
                   @click="onCellClick(bucket, cred, model.raw_model_name)">
              </div>
            </div>
          </div>
        </template>
      </div>
    </div>

    <!-- Legend -->
    <div class="legend">
      <span class="legend-title">图例:</span>
      <span v-for="(color, status) in statusColors" :key="status" class="legend-item">
        <span class="legend-color" :style="{ backgroundColor: color }"></span>
        {{ status }}
      </span>
    </div>

    <!-- Detail popover -->
    <div v-if="selectedBucket" class="drawer-backdrop" @click="closeDetailPopover">
      <div class="drawer-panel card" @click.stop>
        <div class="drawer-header">
          <h3>色块详情</h3>
          <button class="btn btn-ghost btn-sm" @click="closeDetailPopover">关闭</button>
        </div>
        <div class="drawer-body">
          <div class="detail-section">
            <div class="detail-row">
              <span class="detail-label">时间范围</span>
              <span>{{ selectedBucket.bucket.time_bucket }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">凭据</span>
              <span>{{ selectedBucket.credential.label || `#${selectedBucket.credential.credential_id}` }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">模型</span>
              <code>{{ selectedBucket.model }}</code>
            </div>
            <div class="detail-row">
              <span class="detail-label">状态</span>
              <span class="status-badge" :style="{ backgroundColor: getStatusColor(selectedBucket.bucket.status) }">
                {{ selectedBucket.bucket.status }}
              </span>
            </div>
          </div>

          <div class="detail-section">
            <h4>统计指标</h4>
            <div class="detail-row">
              <span class="detail-label">总请求数</span>
              <span>{{ selectedBucket.bucket.total_requests }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">成功数</span>
              <span>{{ selectedBucket.bucket.success_count }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">失败数</span>
              <span>{{ selectedBucket.bucket.failed_count }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">成功率</span>
              <span>{{ (selectedBucket.bucket.success_rate * 100).toFixed(1) }}%</span>
            </div>
            <div class="detail-row" v-if="selectedBucket.bucket.avg_latency_ms">
              <span class="detail-label">平均延迟</span>
              <span>{{ selectedBucket.bucket.avg_latency_ms }}ms</span>
            </div>
            <div class="detail-row" v-if="selectedBucket.bucket.p95_latency_ms">
              <span class="detail-label">P95 延迟</span>
              <span>{{ selectedBucket.bucket.p95_latency_ms }}ms</span>
            </div>
          </div>

          <div class="detail-section" v-if="Object.keys(selectedBucket.bucket.error_distribution || {}).length">
            <h4>错误分布</h4>
            <div v-for="(count, errorType) in selectedBucket.bucket.error_distribution" :key="errorType" class="detail-row">
              <span class="detail-label">{{ errorType }}</span>
              <span>{{ count }} 次</span>
            </div>
          </div>

          <div class="detail-section" v-if="selectedBucket.bucket.sample_request_ids?.length">
            <h4>失败请求样本 (最多10条)</h4>
            <div v-for="reqId in selectedBucket.bucket.sample_request_ids" :key="reqId" class="request-id-link">
              <router-link :to="`/request-detail/${reqId}`" target="_blank">
                {{ reqId.substring(0, 16) }}...
              </router-link>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.heatmap-container {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-height: 600px;
}

.heatmap-toolbar {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}

.toolbar-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.label {
  font-size: 12px;
  color: var(--muted);
  font-weight: 600;
}

.hint-text {
  font-size: 11px;
  color: var(--muted);
}

.field-input {
  padding: 4px 8px;
  font-size: 12px;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg);
  color: var(--text);
}

.checkbox-label {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: var(--text);
  cursor: pointer;
}

.spacer {
  flex: 1;
}

.error-banner {
  padding: 12px;
  background: rgba(239, 68, 68, 0.1);
  border: 1px solid rgba(239, 68, 68, 0.3);
  border-radius: var(--radius);
  color: var(--danger);
  font-size: 13px;
}

.meta-info {
  padding: 8px 12px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  font-size: 11px;
  color: var(--muted);
}

.loading-state,
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 60px;
  text-align: center;
  color: var(--muted);
}

.spinner {
  width: 40px;
  height: 40px;
  border: 3px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: spin 1s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

.heatmap-grid {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.credential-row {
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--card);
  padding: 12px;
}

.credential-header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}

.expand-btn {
  background: transparent;
  border: none;
  cursor: pointer;
  font-size: 14px;
  color: var(--text);
  padding: 4px 8px;
}

.credential-info {
  flex: 1;
}

.credential-label {
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
}

.credential-provider {
  font-size: 11px;
  color: var(--muted);
}

.heatmap-row {
  display: grid;
  grid-template-columns: 120px 1fr;
  gap: 8px;
  align-items: center;
  margin-bottom: 4px;
}

.model-row {
  margin-left: 20px;
}

.row-label {
  font-size: 12px;
  color: var(--muted);
  font-weight: 600;
}

.model-label code {
  font-size: 11px;
}

.cells-container {
  display: flex;
  gap: 2px;
  overflow-x: auto;
}

.heatmap-cell {
  min-width: 8px;
  width: 12px;
  height: 32px;
  border-radius: 2px;
  cursor: pointer;
  transition: opacity 0.2s;
}

.heatmap-cell:hover {
  opacity: 0.8;
  outline: 2px solid var(--accent);
}

.legend {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  flex-wrap: wrap;
  font-size: 12px;
}

.legend-title {
  font-weight: 600;
  color: var(--text);
}

.legend-item {
  display: flex;
  align-items: center;
  gap: 4px;
  color: var(--muted);
}

.legend-color {
  width: 16px;
  height: 16px;
  border-radius: 3px;
  border: 1px solid var(--border);
}

.detail-section {
  margin-bottom: 16px;
}

.detail-section h4 {
  margin: 0 0 8px 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
}

.detail-row {
  display: flex;
  justify-content: space-between;
  padding: 6px 0;
  border-bottom: 1px solid var(--border);
  font-size: 12px;
}

.detail-label {
  color: var(--muted);
  font-weight: 600;
}

.status-badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
  color: white;
}

.request-id-link {
  padding: 4px 0;
  font-size: 11px;
  font-family: monospace;
}

.request-id-link a {
  color: var(--accent);
  text-decoration: none;
}

.request-id-link a:hover {
  text-decoration: underline;
}
</style>
