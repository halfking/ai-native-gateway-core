<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import {
  getCredentialHeatmap,
  toggleModelAvailability,
  promoteCredential,
  demoteCredential,
  type HeatmapCredential,
  type HeatmapBucket,
  type HeatmapModel,
} from '../api'
import { useCredentialLabels } from '../composables/useCredentialLabels'
import { useFilterChips, type FilterChip } from '../composables/useFilterChips'
import ActiveFilterChips from '../components/ActiveFilterChips.vue'

// CredentialHeatmapView — 热力图 tab
// docs/FEATURE-REQ-credential-heatmap-routing-log.md §4:
//   - 完整时间轴:无请求的桶渲染为空白占位格(§4.5)
//   - 汇总行:有绿即绿(任一模型 ready 则汇总绿色),色块内 n/m = 绿色模型数/有数据模型数
//   - 模型视角:选择模型后仅显示该模型行(§4.4)
//   - 色块详情抽屉带状态修正操作(§4.7)
// UI 约束(2026-09-07 用户反馈):
//   - 筛选条件默认折叠,折叠头外部仅以 chips 展示已选条件
//   - 热力图为表格形式:横向时间座标列,所有行的色块按列对齐

const { credentialDisplayName, loadCredentialLabels } = useCredentialLabels()

// Time range presets
type TimeRangePreset = 'today' | '1h' | '6h' | '24h' | 'yesterday' | '7d' | 'month' | 'custom'
const timeRangePreset = ref<TimeRangePreset>('today')
const customTimeStart = ref('')
const customTimeEnd = ref('')

const TIME_PRESET_LABELS: Record<TimeRangePreset, string> = {
  today: '今天',
  '1h': '最近1小时',
  '6h': '最近6小时',
  '24h': '最近24小时',
  yesterday: '昨天',
  '7d': '最近7天',
  month: '本月',
  custom: '自定义',
}

// Granularity
type Granularity = '1m' | '5m' | '15m' | '1h' | '1d'
const granularity = ref<Granularity>('15m')

// Filters
const modelFilter = ref<string>('')
const modelOptions = ref<string[]>([])
const showAnomaliesOnly = ref(false)
const excludeSelfTest = ref(true)

// Collapsible filter panel (collapsed by default; chips outside show active
// conditions — 2026-09-07 feedback)
const filtersOpen = ref(false)

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

function expandAll() {
  expandedCredentials.value = new Set(filteredCredentials.value.map(c => c.credential_id))
  saveExpandedState()
}

function collapseAll() {
  expandedCredentials.value = new Set()
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

const granularityMs = computed(() => {
  const secs: Record<Granularity, number> = { '1m': 60, '5m': 300, '15m': 900, '1h': 3600, '1d': 86400 }
  return secs[granularity.value] * 1000
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

// worst-status ranking: drives the aggregate 汇总 row when no model is green
const statusRank: Record<string, number> = {
  ready: 1,
  manual_disabled: 1,
  cooling: 2,
  rate_limited: 2,
  degraded: 2,
  unreachable: 3,
  auth_failed: 3,
}

function getStatusColor(status: string): string {
  return statusColors[status] || '#9ca3af'
}

function worstStatus(statuses: string[]): string {
  let worst = 'no_data'
  for (const s of statuses) {
    if ((statusRank[s] ?? 0) > (statusRank[worst] ?? 0)) worst = s
  }
  return worst
}

// ── Complete timeline axis (§4.5) ────────────────────────────────────────
// Buckets are epoch-aligned server-side (floor(epoch/N)*N); blank slots are
// rendered for windows with no traffic. Capped to keep the DOM sane.
const MAX_AXIS_BUCKETS = 720
const timeAxis = computed<number[]>(() => {
  if (!meta.value) return []
  const start = new Date(meta.value.time_start).getTime()
  const end = new Date(meta.value.time_end).getTime()
  const step = granularityMs.value
  const axis: number[] = []
  for (let t = Math.floor(start / step) * step; t < end && axis.length < MAX_AXIS_BUCKETS; t += step) {
    axis.push(t)
  }
  return axis
})

const axisTruncated = computed(() => {
  if (!meta.value) return false
  const start = new Date(meta.value.time_start).getTime()
  const end = new Date(meta.value.time_end).getTime()
  const step = granularityMs.value
  return Math.floor((end - start) / step) + 1 > MAX_AXIS_BUCKETS
})

function bucketKey(ts: string | number): number {
  const t = typeof ts === 'number' ? ts : new Date(ts).getTime()
  return Math.floor(t / granularityMs.value) * granularityMs.value
}

interface IndexedModel {
  rawModelName: string
  byBucket: Map<number, HeatmapBucket>
}

function indexModel(model: HeatmapModel): IndexedModel {
  const byBucket = new Map<number, HeatmapBucket>()
  for (const b of model.buckets) byBucket.set(bucketKey(b.time_bucket), b)
  return { rawModelName: model.raw_model_name, byBucket }
}

// Aggregate 汇总 cell: "有绿即绿" — any ready model paints the cell green;
// otherwise the worst remaining status wins. n/m = green models / models
// with traffic in this bucket (2026-09-07 feedback).
interface AggregateCell {
  bucket: HeatmapBucket
  readyCount: number
  modelCount: number
}

function buildAggregate(cred: HeatmapCredential): Map<number, AggregateCell> {
  const byBucket = new Map<number, AggregateCell>()
  const merged = new Map<number, HeatmapBucket[]>()
  for (const model of cred.models) {
    for (const b of model.buckets) {
      const k = bucketKey(b.time_bucket)
      const arr = merged.get(k) || []
      arr.push(b)
      merged.set(k, arr)
    }
  }
  for (const [k, arr] of merged) {
    const total = arr.reduce((s, b) => s + b.total_requests, 0)
    const success = arr.reduce((s, b) => s + b.success_count, 0)
    const failed = arr.reduce((s, b) => s + b.failed_count, 0)
    const errDist: Record<string, number> = {}
    for (const b of arr) {
      for (const [kind, cnt] of Object.entries(b.error_distribution || {})) {
        errDist[kind] = (errDist[kind] || 0) + cnt
      }
    }
    const sampleIds = arr.flatMap(b => b.sample_request_ids || []).slice(0, 10)
    const statuses = arr.map(b => b.status)
    const readyCount = statuses.filter(s => s === 'ready').length
    byBucket.set(k, {
      bucket: {
        time_bucket: new Date(k).toISOString(),
        status: readyCount > 0 ? 'ready' : worstStatus(statuses),
        total_requests: total,
        success_count: success,
        failed_count: failed,
        success_rate: total > 0 ? success / total : 0,
        avg_latency_ms: null,
        p95_latency_ms: null,
        error_distribution: errDist,
        sample_request_ids: sampleIds,
      },
      readyCount,
      modelCount: arr.length,
    })
  }
  return byBucket
}

// Pre-index every (credential, model) once per load; templates read maps.
const indexedRows = computed(() => {
  return filteredCredentials.value.map(cred => ({
    cred,
    models: cred.models.map(indexModel),
    aggregate: buildAggregate(cred),
  }))
})

// ── Collapsed-bar condition chips ─────────────────────────────────────────
const filterChips = useFilterChips((): Array<FilterChip | false | null | undefined> => [
  timeRangePreset.value !== 'today' && {
    key: 'time',
    label: `时间: ${TIME_PRESET_LABELS[timeRangePreset.value]}`,
    onRemove: () => {
      timeRangePreset.value = 'today'
      customTimeStart.value = ''
      customTimeEnd.value = ''
      granularity.value = suggestedGranularity.value as Granularity
      loadHeatmap()
    },
  },
  granularity.value !== suggestedGranularity.value && {
    key: 'granularity',
    label: `粒度: ${granularity.value}`,
    onRemove: () => { granularity.value = suggestedGranularity.value as Granularity },
  },
  !!modelFilter.value && {
    key: 'model',
    label: `模型: ${modelFilter.value}`,
    onRemove: () => { modelFilter.value = ''; loadHeatmap() },
  },
  showAnomaliesOnly.value && {
    key: 'anomalies',
    label: '仅显示异常',
    onRemove: () => { showAnomaliesOnly.value = false },
  },
  !excludeSelfTest.value && {
    key: 'selftest',
    label: '含自检流量',
    onRemove: () => { excludeSelfTest.value = true; loadHeatmap() },
  },
])

const activeFilterCount = computed(() => filterChips.value.length)

// ── Load heatmap data ────────────────────────────────────────────────────
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
        models: modelFilter.value ? [modelFilter.value] : undefined,
      }
    )

    heatmapData.value = response.credentials
    meta.value = response.meta

    // Keep a stable model list gathered from unfiltered loads so the model
    // selector still offers alternatives while a filter is active.
    if (!modelFilter.value) {
      const names = new Set<string>()
      for (const c of response.credentials) {
        for (const m of c.models) names.add(m.raw_model_name)
      }
      modelOptions.value = Array.from(names).sort()
    }
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
  actionMessage.value = null
}

function closeDetailPopover() {
  selectedBucket.value = null
}

// ── Status correction actions (§4.7) ─────────────────────────────────────
const actionReason = ref('')
const actionBusy = ref(false)
const actionMessage = ref<string | null>(null)

async function runAction(kind: 'model-online' | 'model-offline' | 'promote' | 'demote') {
  if (!selectedBucket.value || actionBusy.value) return
  actionBusy.value = true
  actionMessage.value = null
  const credId = selectedBucket.value.credential.credential_id
  const model = selectedBucket.value.model
  const reason = actionReason.value.trim() || '凭据监控页热力图操作'
  try {
    if (kind === 'model-online' || kind === 'model-offline') {
      if (model === '__aggregate__') {
        actionMessage.value = '汇总行不针对单一模型,请展开后在具体模型行选择色块'
        return
      }
      await toggleModelAvailability(credId, model, kind === 'model-online' ? 'online' : 'offline', reason)
      actionMessage.value = `已${kind === 'model-online' ? '上线' : '下线'}模型 ${model}`
    } else if (kind === 'promote') {
      await promoteCredential(credId, reason)
      actionMessage.value = `已恢复凭据 #${credId}`
    } else {
      await demoteCredential(credId, reason)
      actionMessage.value = `已降级凭据 #${credId}`
    }
    await loadHeatmap()
  } catch (e) {
    actionMessage.value = `操作失败: ${e instanceof Error ? e.message : String(e)}`
  } finally {
    actionBusy.value = false
  }
}

// Format timestamp
function formatTime(ts: string | number): string {
  const d = new Date(ts)
  const pad = (n: number) => String(n).padStart(2, '0')
  if (granularity.value === '1d') return `${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
  if (granularity.value === '1h') return `${pad(d.getDate())}日${pad(d.getHours())}时`
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function formatFullTime(ts: string | number): string {
  const d = new Date(ts)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// Lifecycle
onMounted(() => {
  loadExpandedState()
  loadCredentialLabels()
  // 初始粒度与时间范围建议值对齐，避免默认状态就出现"粒度"条件 chip
  granularity.value = suggestedGranularity.value as Granularity
  loadHeatmap()
})

onUnmounted(() => {
  stopAutoRefresh()
})
</script>

<template>
  <div class="heatmap-container">
    <!-- Collapsible toolbar: header row always visible; conditions inside the
         panel; active conditions shown as chips when collapsed -->
    <div class="heatmap-toolbar">
      <div class="toolbar-head" @click="filtersOpen = !filtersOpen">
        <span class="filter-toggle">
          <span class="chevron" aria-hidden="true">{{ filtersOpen ? '▲' : '▼' }}</span>
          筛选条件
          <span v-if="activeFilterCount" class="active-count">{{ activeFilterCount }} 项生效</span>
        </span>
        <span class="spacer"></span>
        <label class="checkbox-label" @click.stop>
          <input type="checkbox" :checked="autoRefresh" @change="toggleAutoRefresh" />
          自动刷新
        </label>
        <select v-model.number="refreshInterval" class="field-input interval-select" :disabled="!autoRefresh" @click.stop>
          <option :value="10">10秒</option>
          <option :value="30">30秒</option>
          <option :value="60">60秒</option>
        </select>
        <button class="btn btn-sm btn-primary" @click.stop="loadHeatmap" :disabled="loading">
          {{ loading ? '加载中...' : '刷新' }}
        </button>
        <span class="toggle-hint">{{ filtersOpen ? '收起' : '展开' }}</span>
      </div>

      <!-- Active-condition chips (visible when collapsed) -->
      <ActiveFilterChips v-if="!filtersOpen && filterChips.length" :chips="filterChips" class="chips-row" />

      <!-- Expanded conditions panel -->
      <div v-show="filtersOpen" class="toolbar-panel">
        <div class="panel-row">
          <span class="label">时间</span>
          <select v-model="timeRangePreset" class="field-input w-time">
            <option v-for="(label, value) in TIME_PRESET_LABELS" :key="value" :value="value">{{ label }}</option>
          </select>
          <template v-if="timeRangePreset === 'custom'">
            <input type="datetime-local" v-model="customTimeStart" class="field-input w-datetime" />
            <span class="label">→</span>
            <input type="datetime-local" v-model="customTimeEnd" class="field-input w-datetime" />
          </template>
          <span class="v-sep" aria-hidden="true"></span>
          <span class="label">粒度</span>
          <select v-model="granularity" class="field-input w-granularity">
            <option value="1m">1分钟</option>
            <option value="5m">5分钟</option>
            <option value="15m">15分钟</option>
            <option value="1h">1小时</option>
            <option value="1d">1天</option>
          </select>
          <span class="hint-text" v-if="granularity !== suggestedGranularity">
            建议 {{ suggestedGranularity }}
          </span>
        </div>
        <div class="panel-row">
          <span class="label">模型</span>
          <select v-model="modelFilter" class="field-input w-model" @change="loadHeatmap">
            <option value="">全部模型</option>
            <option v-for="m in modelOptions" :key="m" :value="m">{{ m }}</option>
            <option v-if="modelFilter && !modelOptions.includes(modelFilter)" :value="modelFilter">{{ modelFilter }}</option>
          </select>
          <label class="checkbox-label">
            <input type="checkbox" v-model="excludeSelfTest" @change="loadHeatmap" />
            排除自检
          </label>
          <label class="checkbox-label">
            <input type="checkbox" v-model="showAnomaliesOnly" />
            仅显示异常
          </label>
          <span class="spacer"></span>
          <button class="btn btn-sm" @click="expandAll">全部展开</button>
          <button class="btn btn-sm" @click="collapseAll">全部收起</button>
        </div>
      </div>
    </div>

    <!-- Error message -->
    <div v-if="error" class="error-banner">
      ⚠️ {{ error }}
    </div>

    <!-- Meta info -->
    <div v-if="meta" class="meta-info">
      时间范围: {{ formatFullTime(meta.time_start) }} - {{ formatFullTime(meta.time_end) }} ·
      粒度: {{ meta.granularity }} ·
      时间桶: {{ timeAxis.length }}<template v-if="axisTruncated">(已截断)</template> ·
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

    <!-- Heatmap table: horizontal time axis columns, cells aligned per column -->
    <div v-else class="heatmap-scroll">
      <table class="heatmap-table">
        <colgroup>
          <col class="col-label" />
          <col v-for="t in timeAxis" :key="t" class="col-bucket" />
        </colgroup>
        <thead>
          <tr>
            <th class="corner-cell">时间 →</th>
            <th v-for="t in timeAxis" :key="t" class="axis-cell" :title="formatFullTime(t)">{{ formatTime(t) }}</th>
          </tr>
        </thead>
        <tbody v-for="row in indexedRows" :key="row.cred.credential_id" class="cred-section">
          <tr class="cred-header-row">
            <th :colspan="timeAxis.length + 1" class="cred-header-cell" @click="toggleExpanded(row.cred.credential_id)">
              <span class="expand-icon" aria-hidden="true">{{ expandedCredentials.has(row.cred.credential_id) ? '▼' : '▶' }}</span>
              <span class="credential-label">
                {{ credentialDisplayName(row.cred.credential_id, row.cred.label || `凭据 #${row.cred.credential_id}`) }}
              </span>
              <span class="credential-provider">{{ row.cred.provider_name }} · {{ row.cred.models.length }} 模型</span>
            </th>
          </tr>
          <!-- Summary row: 有绿即绿; n/m = 绿色模型数/有数据模型数 -->
          <tr class="summary-row">
            <td class="row-label">汇总</td>
            <td v-for="t in timeAxis" :key="t"
                class="cell summary-cell"
                :class="{ blank: !row.aggregate.get(t) }"
                :style="row.aggregate.get(t) ? { backgroundColor: getStatusColor(row.aggregate.get(t)!.bucket.status) } : {}"
                :title="row.aggregate.get(t)
                  ? `${formatFullTime(t)}: ${row.aggregate.get(t)!.bucket.status} · 绿色 ${row.aggregate.get(t)!.readyCount}/${row.aggregate.get(t)!.modelCount} 模型 · ${row.aggregate.get(t)!.bucket.total_requests} 请求`
                  : `${formatFullTime(t)}: 无请求`"
                @click="row.aggregate.get(t) && onCellClick(row.aggregate.get(t)!.bucket, row.cred, '__aggregate__')">
              <span v-if="row.aggregate.get(t)" class="cell-count">{{ row.aggregate.get(t)!.readyCount }}/{{ row.aggregate.get(t)!.modelCount }}</span>
            </td>
          </tr>
          <!-- Expanded model rows: each model shows its own true status color -->
          <template v-if="expandedCredentials.has(row.cred.credential_id)">
            <tr v-for="model in row.models" :key="model.rawModelName" class="model-row">
              <td class="row-label model-label" :title="model.rawModelName"><code>{{ model.rawModelName }}</code></td>
              <td v-for="t in timeAxis" :key="t"
                  class="cell model-cell"
                  :class="{ blank: !model.byBucket.get(t) }"
                  :style="model.byBucket.get(t) ? { backgroundColor: getStatusColor(model.byBucket.get(t)!.status) } : {}"
                  :title="model.byBucket.get(t)
                    ? `${formatFullTime(t)}: ${model.byBucket.get(t)!.status}, ${model.byBucket.get(t)!.total_requests} 请求, ${(model.byBucket.get(t)!.success_rate * 100).toFixed(1)}% 成功率`
                    : `${formatFullTime(t)}: 无请求`"
                  @click="model.byBucket.get(t) && onCellClick(model.byBucket.get(t)!, row.cred, model.rawModelName)">
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>

    <!-- Legend -->
    <div class="legend">
      <span class="legend-title">图例:</span>
      <span v-for="(color, status) in statusColors" :key="status" class="legend-item">
        <span class="legend-color" :style="{ backgroundColor: color }"></span>
        {{ status }}
      </span>
      <span class="legend-item">
        <span class="legend-color legend-blank"></span>
        无数据(空白)
      </span>
      <span class="legend-item legend-rule">
        汇总行: 任一模型绿色即显示绿色 · 色块内 n/m = 绿色模型数/有数据模型数
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
              <span>{{ formatFullTime(selectedBucket.bucket.time_bucket) }} 起 1 个 {{ granularity }} 桶</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">凭据</span>
              <span>{{ selectedBucket.credential.label || `#${selectedBucket.credential.credential_id}` }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">模型</span>
              <code>{{ selectedBucket.model === '__aggregate__' ? '全部模型(汇总)' : selectedBucket.model }}</code>
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

          <div class="detail-section correction-section">
            <h4>状态修正</h4>
            <input
              v-model="actionReason"
              class="field-input reason-input"
              placeholder="操作原因 (可选)"
            />
            <div class="action-btns">
              <button class="btn btn-sm" :disabled="actionBusy" @click="runAction('model-online')">模型上线</button>
              <button class="btn btn-sm" :disabled="actionBusy" @click="runAction('model-offline')">模型下线</button>
              <button class="btn btn-sm" :disabled="actionBusy" @click="runAction('promote')">恢复凭据</button>
              <button class="btn btn-sm" :disabled="actionBusy" @click="runAction('demote')">降级凭据</button>
            </div>
            <p v-if="actionMessage" class="action-message" :class="{ fail: actionMessage.startsWith('操作失败') }">
              {{ actionMessage }}
            </p>
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

/* ── Collapsible toolbar ─────────────────────────────────────────────── */
.heatmap-toolbar {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 8px 12px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}

.toolbar-head {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  min-height: 28px;
}

.filter-toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  white-space: nowrap;
}

.chevron {
  font-size: 10px;
  color: var(--muted);
}

.active-count {
  font-size: 10px;
  font-weight: 600;
  padding: 2px 7px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--accent) 15%, transparent);
  border: 1px solid color-mix(in srgb, var(--accent) 40%, transparent);
  color: var(--accent-h);
  white-space: nowrap;
}

.chips-row {
  padding-left: 2px;
}

.toolbar-panel {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--border);
}

.panel-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.label {
  font-size: 12px;
  color: var(--muted);
  font-weight: 600;
  white-space: nowrap;
}

.hint-text {
  font-size: 11px;
  color: var(--muted);
}

/* width:auto overrides the global input/select width:100% */
.field-input {
  width: auto;
  padding: 4px 8px;
  font-size: 12px;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg);
  color: var(--text);
}

.w-time { width: 128px; flex-shrink: 0; }
.w-granularity { width: 92px; flex-shrink: 0; }
.w-model { width: 220px; max-width: 320px; }
.w-datetime { width: 190px; flex-shrink: 0; }
.interval-select { width: 76px; flex-shrink: 0; }

.v-sep {
  width: 1px;
  height: 18px;
  background: var(--border);
  flex-shrink: 0;
  margin: 0 2px;
}

.checkbox-label {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: var(--text);
  cursor: pointer;
  white-space: nowrap;
}

.spacer { flex: 1; }

.toggle-hint {
  font-size: 11px;
  color: var(--muted);
  white-space: nowrap;
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

/* ── Heatmap table ───────────────────────────────────────────────────── */
.heatmap-scroll {
  overflow: auto;
  max-height: 72vh;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--card);
}

.heatmap-table {
  border-collapse: separate;
  border-spacing: 1px;
  table-layout: fixed;
  width: max-content;
  min-width: 100%;
}

.heatmap-table col.col-label { width: 130px; }
.heatmap-table col.col-bucket { width: 38px; }

.heatmap-table thead th {
  position: sticky;
  top: 0;
  z-index: 3;
  background: var(--bg-subtle, var(--card));
  border-bottom: 1px solid var(--border);
}

.corner-cell {
  position: sticky;
  left: 0;
  z-index: 4;
  font-size: 10px;
  font-weight: 600;
  color: var(--muted);
  text-align: left;
  padding: 4px 8px;
  white-space: nowrap;
}

.axis-cell {
  font-size: 9px;
  font-family: ui-monospace, monospace;
  color: var(--muted);
  text-align: center;
  padding: 4px 0;
  white-space: nowrap;
  overflow: hidden;
}

.cred-section + .cred-section {
  border-top: 2px solid var(--border);
}

.cred-header-row th.cred-header-cell {
  position: sticky;
  top: 26px;
  z-index: 2;
  background: var(--bg-subtle, var(--card));
  text-align: left;
  padding: 5px 8px;
  cursor: pointer;
  border-bottom: 1px solid var(--border);
}

.expand-icon {
  display: inline-block;
  width: 16px;
  font-size: 10px;
  color: var(--muted);
}

.credential-label {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  margin-left: 4px;
}

.credential-provider {
  font-size: 11px;
  color: var(--muted);
  margin-left: 8px;
}

.heatmap-table td.row-label {
  position: sticky;
  left: 0;
  z-index: 2;
  background: var(--card);
  font-size: 12px;
  font-weight: 600;
  color: var(--muted);
  padding: 0 8px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 130px;
}

.model-label code {
  font-size: 10px;
  font-weight: 400;
}

/* Cells */
.heatmap-table td.cell {
  height: 22px;
  padding: 0;
  border-radius: 2px;
  cursor: pointer;
  transition: opacity 0.15s;
  overflow: hidden;
}

.heatmap-table tr.model-row td.cell {
  height: 16px;
}

.heatmap-table td.cell:hover {
  opacity: 0.8;
  outline: 2px solid var(--accent);
  outline-offset: -2px;
}

.heatmap-table td.cell.blank {
  background: transparent;
  border: 1px dashed var(--border);
  cursor: default;
}

.heatmap-table td.cell.blank:hover {
  outline: none;
  opacity: 1;
}

.cell-count {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  font-size: 9px;
  font-weight: 700;
  font-family: ui-monospace, monospace;
  color: rgba(15, 23, 42, 0.82);
  text-shadow: 0 0 2px rgba(255, 255, 255, 0.35);
  user-select: none;
  pointer-events: none;
}

/* ── Legend ──────────────────────────────────────────────────────────── */
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

.legend-blank {
  background: transparent;
  border-style: dashed;
}

.legend-rule {
  margin-left: auto;
  font-size: 11px;
}

/* ── Detail drawer ───────────────────────────────────────────────────── */
.drawer-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.45);
  z-index: 50;
  display: flex;
  justify-content: flex-end;
}

.drawer-panel {
  width: 420px;
  max-width: 92vw;
  height: 100%;
  border-radius: 0;
  overflow-y: auto;
}

.drawer-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px 16px;
  border-bottom: 1px solid var(--border);
}

.drawer-header h3 {
  margin: 0;
  font-size: 15px;
}

.drawer-body {
  padding: 16px;
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

.correction-section .reason-input {
  width: 100%;
  margin-bottom: 8px;
}

.action-btns {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.action-message {
  margin: 8px 0 0 0;
  font-size: 12px;
  color: var(--success);
}

.action-message.fail {
  color: var(--danger);
}
</style>
