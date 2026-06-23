<script setup lang="ts">
import { ref, onMounted, computed, onUnmounted, watch } from 'vue'
import { getCredentialMonitorSummary, getSlidingWindow, promoteCredential, demoteCredential, setConcurrencyAuto, toggleModelAvailability, getModelHistory, getCredentialFpSlotStats, getCredentialDecisions, clearManualDisabled, setManualDisabled, type CredentialMonitorSummary, type CredentialModelStatus, type CallEntry, type ModelHistoryEvent, type ModelToggleAction, type FpSlotStats, type CredentialRoutingDecision } from '../api'
import { Chart, registerables } from 'chart.js'
import FpSlotVisualizer from '../components/FpSlotVisualizer.vue'

Chart.register(...registerables)

const loading = ref(false)
const credentials = ref<CredentialMonitorSummary[]>([])
const selectedCred = ref<CredentialMonitorSummary | null>(null)
const selectedModel = ref('')
const windowEntries = ref<CallEntry[]>([])
const windowSource = ref<'redis' | 'request_logs'>('redis')
const windowLoading = ref(false)

const providerFilter = ref(0)
const availStateFilter = ref('')
const healthFilter = ref('')
const quickFilter = ref<'none' | 'broken' | 'low-rate'>('none')

const demoteDialogOpen = ref(false)
const demoteReason = ref('')
const demoteHours = ref(2)

const promoteDialogOpen = ref(false)
const promoteReason = ref('')

const concurrencyDialogOpen = ref(false)
const concurrencyValue = ref(5)
const concurrencyReason = ref('')

// ── 2026-06-23: per-model manual online/offline + state-change history ──
const toggleBusy = ref<Record<string, boolean>>({})
const toggleDialogOpen = ref(false)
const toggleTarget = ref<{
  credId: number
  rawModel: string
  action: ModelToggleAction
  prevReason: string | null
} | null>(null)
const toggleReason = ref('')
const historyLoading = ref(false)
const historyEvents = ref<ModelHistoryEvent[]>([])

// Auto refresh (main list)
const autoRefresh = ref(false)
const refreshInterval = ref(30) // seconds
let refreshTimer: number | null = null

// Detail drawer auto-refresh (2026-06-23)
const detailAutoRefresh = ref(false)
const detailRefreshInterval = ref(5) // seconds
let detailRefreshTimer: number | null = null

// Routing decisions for credential (2026-06-23)
const credentialDecisions = ref<CredentialRoutingDecision[]>([])
const credentialDecisionsLoading = ref(false)
async function loadCredentialDecisions() {
  if (!selectedCred.value) return
  credentialDecisionsLoading.value = true
  try {
    const res = await getCredentialDecisions(selectedCred.value.id, 50)
    credentialDecisions.value = res.decisions
  } catch (e) {
    console.error('credential decisions load failed', e)
  } finally {
    credentialDecisionsLoading.value = false
  }
}

// Fingerprint slot visualization (2026-06-23)
const fpSlotStats = ref<FpSlotStats | null>(null)
const fpSlotStatsLoading = ref(false)
async function loadFpSlotStats() {
  if (!selectedCred.value) return
  fpSlotStatsLoading.value = true
  try {
    fpSlotStats.value = await getCredentialFpSlotStats(
      selectedCred.value.provider_id,
      selectedCred.value.id,
    )
  } catch (e) {
    console.error('fp slot stats load failed', e)
  } finally {
    fpSlotStatsLoading.value = false
  }
}

// Clear manual_disabled (2026-06-23)
const clearDisabledDialogOpen = ref(false)
const clearDisabledReason = ref('')

// Set manual_disabled (2026-06-23)
const setManualDisabledDialogOpen = ref(false)
const setManualDisabledTargetValue = ref(false)
const setManualDisabledReason = ref('')

function openClearDisabledDialog() {
  clearDisabledDialogOpen.value = true
  clearDisabledReason.value = ''
}

async function submitClearDisabled() {
  if (!selectedCred.value) return
  try {
    await clearManualDisabled(selectedCred.value.id, clearDisabledReason.value)
    clearDisabledDialogOpen.value = false
    await refreshDetailDrawer()
  } catch (e) {
    alert('清除失败: ' + (e instanceof Error ? e.message : String(e)))
  }
}

// Set manual_disabled (2026-06-23)
function openSetManualDisabledDialog(targetValue: boolean) {
  setManualDisabledTargetValue.value = targetValue
  setManualDisabledReason.value = ''
  setManualDisabledDialogOpen.value = true
}

async function submitSetManualDisabled() {
  if (!selectedCred.value || !setManualDisabledReason.value.trim()) return
  try {
    await setManualDisabled(selectedCred.value.id, setManualDisabledTargetValue.value, setManualDisabledReason.value)
    setManualDisabledDialogOpen.value = false
    await refreshDetailDrawer()
  } catch (e) {
    alert('操作失败: ' + (e instanceof Error ? e.message : String(e)))
  }
}

// Refresh detail drawer content (2026-06-23)
async function refreshDetailDrawer() {
  if (!selectedCred.value) return
  // Reload summary to update selectedCred
  await load()
  const updatedCred = credentials.value.find(c => c.id === selectedCred.value?.id)
  if (updatedCred) {
    selectedCred.value = updatedCred
  }
  // Reload all drawer sections
  if (selectedModel.value) {
    await Promise.all([
      loadSlidingWindow(selectedCred.value.id, selectedModel.value),
      loadHistory(),
      loadCredentialDecisions(),
      loadFpSlotStats(),
    ])
  } else {
    await Promise.all([
      loadCredentialDecisions(),
      loadFpSlotStats(),
    ])
  }
}

function startDetailAutoRefresh() {
  if (detailRefreshTimer) return
  detailAutoRefresh.value = true
  detailRefreshTimer = window.setInterval(() => refreshDetailDrawer(), detailRefreshInterval.value * 1000)
}

function stopDetailAutoRefresh() {
  if (detailRefreshTimer) {
    clearInterval(detailRefreshTimer)
    detailRefreshTimer = null
  }
  detailAutoRefresh.value = false
}

function toggleDetailAutoRefresh() {
  detailAutoRefresh.value ? stopDetailAutoRefresh() : startDetailAutoRefresh()
}

// Watch selectedCred changes to stop auto-refresh when drawer closes
watch(selectedCred, (newVal) => {
  if (!newVal) {
    stopDetailAutoRefresh()
  }
})

// Batch operations
const selectedIds = ref<Set<number>>(new Set())
const batchDialogOpen = ref(false)
const batchAction = ref<'promote' | 'demote'>('promote')
const batchReason = ref('')
const batchHours = ref(2)

// Error pie chart
let errorPieChart: Chart | null = null

async function load() {
  loading.value = true
  try {
    const res = await getCredentialMonitorSummary({
      provider_id: providerFilter.value || undefined,
      include_window_stats: true,
    })
    credentials.value = res.credentials
  } catch (e) {
    console.error('load failed', e)
  } finally {
    loading.value = false
  }
}

// ── Derived summary cards ──────────────────────────────────────────────
const summary = computed(() => {
  const all = credentials.value
  const total = all.length
  const ready = all.filter(c => c.availability_state === 'ready').length
  const abnormal = all.filter(c =>
    ['unreachable', 'cooling', 'rate_limited', 'auth_failed', 'suspended'].includes(c.availability_state)
  ).length
  let brokenModels = 0
  for (const c of all) {
    for (const m of c.models || []) {
      if (m.probe_state === 'broken_confirmed') brokenModels++
    }
  }
  return { total, ready, abnormal, brokenModels }
})

const filteredCreds = computed(() => {
  let result = credentials.value
  if (availStateFilter.value) {
    result = result.filter(c => c.availability_state === availStateFilter.value)
  }
  if (healthFilter.value) {
    result = result.filter(c => c.health_status === healthFilter.value)
  }
  if (quickFilter.value === 'broken') {
    result = result.filter(c => (c.models || []).some(m => m.probe_state === 'broken_confirmed'))
  }
  if (quickFilter.value === 'low-rate') {
    result = result.filter(c => c.aggregated_success_rate != null && c.aggregated_success_rate < 0.5)
  }
  return result
})

const allSelected = computed(() => {
  return filteredCreds.value.length > 0 && filteredCreds.value.every(c => selectedIds.value.has(c.id))
})

function toggleSelectAll() {
  if (allSelected.value) {
    selectedIds.value.clear()
  } else {
    filteredCreds.value.forEach(c => selectedIds.value.add(c.id))
  }
}

function toggleSelect(id: number) {
  if (selectedIds.value.has(id)) {
    selectedIds.value.delete(id)
  } else {
    selectedIds.value.add(id)
  }
}

// ── Per-credential model helpers ───────────────────────────────────────
function modelCount(c: CredentialMonitorSummary) {
  const models = c.models || []
  const total = models.length
  const avail = models.filter(m => m.offer_available && m.binding_available).length
  return { avail, total }
}

function brokenModels(c: CredentialMonitorSummary): CredentialModelStatus[] {
  return (c.models || []).filter(m => m.probe_state === 'broken_confirmed')
}

// First 3 broken model names for the table cell (the rest are hidden behind an
// ellipsis to keep the row readable when a credential has many broken models;
// the drawer shows the full list).
function brokenPreview(c: CredentialMonitorSummary): string[] {
  return brokenModels(c).slice(0, 3).map(m => m.raw_model_name)
}

function openDetail(cred: CredentialMonitorSummary) {
  selectedCred.value = cred
  // default the window to the first broken model, else the lowest-rate model
  const models = cred.models || []
  const broken = models.find(m => m.probe_state === 'broken_confirmed')
  const pick = broken || models.slice().sort((a, b) => (a.recent_success_rate ?? 1) - (b.recent_success_rate ?? 1))[0]
  selectedModel.value = pick?.raw_model_name || ''
  if (selectedModel.value) {
    loadSlidingWindow(cred.id, selectedModel.value)
    loadHistory()
  } else {
    windowEntries.value = []
    historyEvents.value = []
  }
  // Load additional drawer data (2026-06-23)
  loadCredentialDecisions()
  loadFpSlotStats()
}

async function loadSlidingWindow(credId: number, model: string) {
  if (!model) return
  windowLoading.value = true
  try {
    const res = await getSlidingWindow(credId, model, 60)
    windowEntries.value = res.entries
    windowSource.value = res.source
    setTimeout(() => renderErrorPieChart(res.stats.error_kinds), 100)
  } catch (e) {
    console.error('sliding window failed', e)
  } finally {
    windowLoading.value = false
  }
}

function selectModel(model: string) {
  if (!selectedCred.value || model === selectedModel.value) return
  selectedModel.value = model
  loadSlidingWindow(selectedCred.value.id, model)
  loadHistory()
}

function renderErrorPieChart(errorKinds: Record<string, number>) {
  const canvas = document.getElementById('errorPieChart') as HTMLCanvasElement
  if (!canvas) return

  if (errorPieChart) {
    errorPieChart.destroy()
  }

  const labels = Object.keys(errorKinds)
  const data = Object.values(errorKinds)

  if (labels.length === 0) return

  errorPieChart = new Chart(canvas, {
    type: 'pie',
    data: {
      labels: labels,
      datasets: [{
        data: data,
        backgroundColor: [
          '#ef4444', '#f97316', '#f59e0b', '#eab308', '#84cc16',
          '#22c55e', '#10b981', '#14b8a6', '#06b6d4', '#0ea5e9',
        ],
      }],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: { position: 'right' },
        title: { display: true, text: '错误类型分布' },
      },
    },
  })
}

function startAutoRefresh() {
  if (refreshTimer) return
  autoRefresh.value = true
  refreshTimer = window.setInterval(() => load(), refreshInterval.value * 1000)
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

function openBatchDialog(action: 'promote' | 'demote') {
  if (selectedIds.value.size === 0) {
    alert('请先选择凭据')
    return
  }
  batchAction.value = action
  batchReason.value = ''
  batchHours.value = 2
  batchDialogOpen.value = true
}

async function submitBatch() {
  const ids = Array.from(selectedIds.value)
  const promises = ids.map(id =>
    batchAction.value === 'promote'
      ? promoteCredential(id, batchReason.value)
      : demoteCredential(id, batchReason.value, batchHours.value)
  )
  try {
    await Promise.all(promises)
    batchDialogOpen.value = false
    selectedIds.value.clear()
    load()
  } catch (e) {
    alert('批量操作失败: ' + (e instanceof Error ? e.message : String(e)))
  }
}

function openDemoteDialog() {
  demoteDialogOpen.value = true
  demoteReason.value = ''
  demoteHours.value = 2
}

async function submitDemote() {
  if (!selectedCred.value) return
  try {
    await demoteCredential(selectedCred.value.id, demoteReason.value, demoteHours.value)
    demoteDialogOpen.value = false
    load()
    selectedCred.value = null
  } catch (e) {
    alert('降级失败: ' + (e instanceof Error ? e.message : String(e)))
  }
}

function openPromoteDialog() {
  promoteDialogOpen.value = true
  promoteReason.value = ''
}

async function submitPromote() {
  if (!selectedCred.value) return
  try {
    await promoteCredential(selectedCred.value.id, promoteReason.value)
    promoteDialogOpen.value = false
    load()
    selectedCred.value = null
  } catch (e) {
    alert('升级失败: ' + (e instanceof Error ? e.message : String(e)))
  }
}

function openConcurrencyDialog() {
  concurrencyDialogOpen.value = true
  concurrencyValue.value = selectedCred.value?.concurrency_limit_auto || selectedCred.value?.effective_concurrency || 5
  concurrencyReason.value = ''
}

async function submitConcurrency() {
  if (!selectedCred.value) return
  try {
    await setConcurrencyAuto(selectedCred.value.id, concurrencyValue.value, concurrencyReason.value)
    concurrencyDialogOpen.value = false
    load()
  } catch (e) {
    alert('设置失败: ' + (e instanceof Error ? e.message : String(e)))
  }
}

// ── 2026-06-23: per-model toggle + history helpers ────────────────────────
function openToggleDialog(m: CredentialModelStatus, action: ModelToggleAction) {
  if (!selectedCred.value) return
  toggleTarget.value = {
    credId: selectedCred.value.id,
    rawModel: m.raw_model_name,
    action,
    prevReason: m.binding_unavailable_reason ?? null,
  }
  toggleReason.value = ''
  toggleDialogOpen.value = true
}

async function submitToggle() {
  if (!toggleTarget.value || !toggleReason.value.trim()) return
  const t = toggleTarget.value
  const key = `${t.credId}|${t.rawModel}`
  toggleBusy.value[key] = true
  try {
    await toggleModelAvailability(t.credId, t.rawModel, t.action, toggleReason.value.trim())
    toggleDialogOpen.value = false
    await load() // refresh summary so the row badge updates
    await loadHistory() // refresh history with the new manual event on top
  } catch (e) {
    alert(`${t.action === 'offline' ? '下线' : '上线'}失败: ` + (e instanceof Error ? e.message : String(e)))
  } finally {
    toggleBusy.value[key] = false
  }
}

async function loadHistory() {
  if (!selectedCred.value || !selectedModel.value) {
    historyEvents.value = []
    return
  }
  historyLoading.value = true
  try {
    const res = await getModelHistory(selectedCred.value.id, selectedModel.value, 50)
    historyEvents.value = res.events
  } catch (e) {
    console.error('history failed', e)
    historyEvents.value = []
  } finally {
    historyLoading.value = false
  }
}

function formatTs(ts: string) {
  // '2026-06-23T10:00:00Z' -> '06-23 10:00'
  const d = new Date(ts)
  if (isNaN(d.getTime())) return ts
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  const h = String(d.getHours()).padStart(2, '0')
  const min = String(d.getMinutes()).padStart(2, '0')
  return `${m}-${day} ${h}:${min}`
}

// ── Badge / color helpers ──────────────────────────────────────────────
function statusBadge(state: string) {
  if (state === 'ready') return 'badge-green'
  if (['degraded', 'cooling', 'rate_limited'].includes(state)) return 'badge-amber'
  if (['unreachable', 'auth_failed', 'suspended'].includes(state)) return 'badge-red'
  return 'badge-gray'
}

function healthBadge(h: string) {
  if (h === 'healthy') return 'badge-green'
  if (h === 'warning') return 'badge-amber'
  if (h === 'unreachable') return 'badge-red'
  return 'badge-gray'
}

function probeBadge(state: string) {
  if (state === 'broken_confirmed') return 'badge-red'
  if (state === 'recovering') return 'badge-amber'
  if (state === 'healthy_confirmed') return 'badge-green'
  return 'badge-gray'
}

function rateClass(rate: number | null | undefined) {
  if (rate == null) return 'rate-none'
  if (rate >= 0.9) return 'rate-good'
  if (rate >= 0.5) return 'rate-warn'
  return 'rate-bad'
}

function rateText(rate: number | null | undefined) {
  if (rate == null) return '—'
  return (rate * 100).toFixed(1) + '%'
}

onMounted(() => load())

onUnmounted(() => {
  stopAutoRefresh()
  stopDetailAutoRefresh()
  if (errorPieChart) errorPieChart.destroy()
})
</script>

<template>
  <div class="page-container">
    <!-- Unified top bar: title + auto-refresh + filters + batch actions.
         Mirrors /routing-v2's .top-bar pattern (one card instead of three
         stacked rows), saving ~50px of vertical real estate and aligning
         with the reference page's visual rhythm. -->
    <div class="top-bar">
      <div class="top-bar-head">
        <h1>凭据监控</h1>
        <div class="refresh-group">
          <label>
            <input type="checkbox" :checked="autoRefresh" @change="toggleAutoRefresh" />
            自动刷新
          </label>
          <select v-model.number="refreshInterval" class="field-input">
            <option :value="10">10秒</option>
            <option :value="30">30秒</option>
            <option :value="60">60秒</option>
          </select>
          <button class="btn btn-primary btn-sm" @click="load">手动刷新</button>
        </div>
      </div>
      <div class="filter-toolbar">
        <span class="label">可用性:</span>
        <select v-model="availStateFilter" class="field-input">
          <option value="">全部</option>
          <option value="ready">ready</option>
          <option value="degraded">degraded</option>
          <option value="cooling">cooling</option>
          <option value="unreachable">unreachable</option>
        </select>
        <span class="label">健康:</span>
        <select v-model="healthFilter" class="field-input">
          <option value="">全部</option>
          <option value="healthy">healthy</option>
          <option value="warning">warning</option>
          <option value="unreachable">unreachable</option>
        </select>
        <div class="quick-filter-group">
          <button class="btn btn-sm btn-ghost" :class="quickFilter === 'none' ? 'qf-active' : ''" @click="quickFilter = 'none'">全部</button>
          <button class="btn btn-sm btn-ghost" :class="quickFilter === 'broken' ? 'qf-active qf-bad' : ''" @click="quickFilter = 'broken'">只看 broken</button>
          <button class="btn btn-sm btn-ghost" :class="quickFilter === 'low-rate' ? 'qf-active qf-warn' : ''" @click="quickFilter = 'low-rate'">成功率&lt;50%</button>
        </div>
        <span class="spacer"></span>
        <button class="btn btn-sm btn-success" :disabled="selectedIds.size === 0" @click="openBatchDialog('promote')">
          批量恢复 ({{ selectedIds.size }})
        </button>
        <button class="btn btn-sm btn-danger" :disabled="selectedIds.size === 0" @click="openBatchDialog('demote')">
          批量降级 ({{ selectedIds.size }})
        </button>
      </div>
    </div>

    <!-- Summary cards -->
    <div class="summary-row">
      <div class="summary-card">
        <div class="summary-label">总凭据</div>
        <div class="summary-value">{{ summary.total }}</div>
      </div>
      <div class="summary-card summary-good">
        <div class="summary-label">可用 (ready)</div>
        <div class="summary-value">{{ summary.ready }}</div>
      </div>
      <div class="summary-card" :class="summary.abnormal > 0 ? 'summary-warn' : ''">
        <div class="summary-label">异常</div>
        <div class="summary-value">{{ summary.abnormal }}</div>
        <div class="summary-sub">unreachable/cooling/rate_limited</div>
      </div>
      <div class="summary-card" :class="summary.brokenModels > 0 ? 'summary-bad' : ''">
        <div class="summary-label">broken 模型</div>
        <div class="summary-value">{{ summary.brokenModels }}</div>
        <div class="summary-sub">probe 确认坏掉</div>
      </div>
    </div>

    <div v-if="loading" style="text-align:center;padding:32px">加载中...</div>
    <div v-else-if="!filteredCreds.length" style="text-align:center;padding:32px">暂无凭据</div>

    <div v-else class="card" style="overflow-x:auto;padding:0">
      <table class="data-table dense">
        <thead>
          <tr>
            <th style="width:40px">
              <input type="checkbox" :checked="allSelected" @change="toggleSelectAll" />
            </th>
            <th>凭据</th>
            <th>供应商</th>
            <th>可用性</th>
            <th>健康</th>
            <th>模型 (可用/总数)</th>
            <th>最近成功率</th>
            <th>broken 模型</th>
            <th>并发</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="c in filteredCreds" :key="c.id" class="clickable-row" @click="openDetail(c)">
            <td @click.stop>
              <input type="checkbox" :checked="selectedIds.has(c.id)" @change="toggleSelect(c.id)" />
            </td>
            <td>
              <div>{{ c.label || `#${c.id}` }}</div>
              <div class="cell-sub">ID: {{ c.id }}</div>
            </td>
            <td>{{ c.provider_name }}</td>
            <td>
              <span class="badge" :class="statusBadge(c.availability_state)">{{ c.availability_state }}</span>
              <div v-if="c.state_reason_code" class="cell-sub">{{ c.state_reason_code }}</div>
            </td>
            <td>
              <span class="badge" :class="healthBadge(c.health_status)">{{ c.health_status }}</span>
            </td>
            <td>
              <span :class="modelCount(c).avail < modelCount(c).total ? 'rate-warn' : ''">
                {{ modelCount(c).avail }}/{{ modelCount(c).total }}
              </span>
            </td>
            <td>
              <span class="rate-cell" :class="rateClass(c.aggregated_success_rate)">
                {{ rateText(c.aggregated_success_rate) }}
              </span>
            </td>
            <td>
              <span v-if="brokenModels(c).length === 0" class="cell-muted">—</span>
              <div v-else style="display:flex;flex-wrap:wrap;gap:4px;align-items:center">
                <span v-for="name in brokenPreview(c)" :key="name" class="badge badge-red model-badge">{{ name }}</span>
                <span v-if="brokenModels(c).length > 3" class="badge badge-gray model-badge" :title="brokenModels(c).map(m => m.raw_model_name).join(', ')">
                  +{{ brokenModels(c).length - 3 }}
                </span>
              </div>
            </td>
            <td>
              <div>手动: {{ c.concurrency_limit || '—' }}</div>
              <div class="cell-sub">生效: {{ c.effective_concurrency }}</div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Detail Drawer -->
    <div v-if="selectedCred" class="drawer-backdrop" @click="selectedCred = null">
      <div class="drawer-panel card drawer-panel-wide" @click.stop>
        <div class="drawer-header">
          <div>
            <h3 style="margin:0">{{ selectedCred.label || `凭据 #${selectedCred.id}` }}</h3>
            <div class="drawer-sub">{{ selectedCred.provider_name }}</div>
          </div>
          <div style="display:flex;gap:8px;align-items:center">
            <label style="display:flex;align-items:center;gap:4px;font-size:13px;cursor:pointer">
              <input type="checkbox" :checked="detailAutoRefresh" @change="toggleDetailAutoRefresh" />
              自动刷新
            </label>
            <select v-model.number="detailRefreshInterval" class="field-input" style="width:auto;font-size:13px;padding:2px 6px">
              <option :value="5">5秒</option>
              <option :value="10">10秒</option>
              <option :value="30">30秒</option>
            </select>
            <button class="btn btn-sm btn-ghost" @click="refreshDetailDrawer" title="刷新详情">
              <span style="font-size:16px">↻</span>
            </button>
            <button class="btn btn-ghost btn-sm" @click="selectedCred = null">关闭</button>
          </div>
        </div>

        <div class="drawer-body">
          <div style="display:grid;grid-template-columns:1fr 1fr;gap:16px">
            <!-- Left column: status + model table -->
            <div>
              <div class="drawer-section">
                <div class="drawer-section-title">状态概览</div>
                <div style="display:grid;grid-template-columns:repeat(2,1fr);gap:12px">
                  <div>
                    <label class="field-label">可用性</label>
                    <span class="badge" :class="statusBadge(selectedCred.availability_state)">{{ selectedCred.availability_state }}</span>
                  </div>
                  <div>
                    <label class="field-label">健康</label>
                    <span class="badge" :class="healthBadge(selectedCred.health_status)">{{ selectedCred.health_status }}</span>
                  </div>
                  <div>
                    <label class="field-label">配额</label>
                    <span>{{ selectedCred.quota_state }}</span>
                  </div>
                  <div>
                    <label class="field-label">连续失败</label>
                    <span>{{ selectedCred.consecutive_failures }}</span>
                  </div>
                  <div>
                    <label class="field-label">manual_disabled</label>
                    <span :class="selectedCred.manual_disabled ? 'badge badge-red' : 'badge badge-gray'">
                      {{ selectedCred.manual_disabled ? 'YES' : 'NO' }}
                    </span>
                  </div>
                </div>
                <div v-if="selectedCred.state_reason_detail" class="cell-sub" style="margin-top:8px">
                  {{ selectedCred.state_reason_detail }}
                </div>
                <div v-if="selectedCred.manual_disabled" style="margin-top:8px">
                  <button class="btn btn-xs btn-warning" @click="openClearDisabledDialog">
                    🔓 清除 manual_disabled
                  </button>
                </div>
              </div>

              <div class="drawer-section">
                <div class="drawer-section-title">并发限流</div>
                <div style="display:grid;grid-template-columns:repeat(3,1fr);gap:12px">
                  <div>
                    <label class="field-label">手动</label>
                    <div>{{ selectedCred.concurrency_limit || '未设置' }}</div>
                  </div>
                  <div>
                    <label class="field-label">自动</label>
                    <div>{{ selectedCred.concurrency_limit_auto || '未设置' }}</div>
                  </div>
                  <div>
                    <label class="field-label">生效</label>
                    <div class="badge badge-blue">{{ selectedCred.effective_concurrency }}</div>
                  </div>
                </div>
                <div style="display:flex;gap:8px;margin-top:8px;flex-wrap:wrap">
                  <button class="btn btn-sm" @click="openConcurrencyDialog">调整自动值</button>
                  <button class="btn btn-sm btn-danger" @click="openDemoteDialog">临时降级</button>
                  <button class="btn btn-sm btn-success" @click="openPromoteDialog">恢复上线</button>
                </div>
              </div>

              <!-- Model availability table -->
              <div class="drawer-section">
                <div class="drawer-section-title">模型可用性 ({{ (selectedCred.models || []).length }})</div>
                <div v-if="!(selectedCred.models || []).length" class="cell-muted">无模型</div>
                <div v-else style="overflow-x:auto">
                  <table class="model-table">
                    <thead>
                      <tr>
                        <th>模型</th>
                        <th>probe</th>
                        <th>成功率</th>
                        <th>样本</th>
                        <th>操作</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr v-for="m in selectedCred.models" :key="m.raw_model_name"
                          :class="{ 'model-row-selected': m.raw_model_name === selectedModel }"
                          @click="selectModel(m.raw_model_name)">
                        <td>
                          <code class="mono-sm">{{ m.raw_model_name }}</code>
                          <span v-if="!m.offer_available || !m.binding_available" class="badge badge-gray" style="margin-left:4px">unavail</span>
                        </td>
                        <td><span class="badge" :class="probeBadge(m.probe_state)">{{ m.probe_state }}</span></td>
                        <td><span class="rate-cell" :class="rateClass(m.recent_success_rate)">{{ rateText(m.recent_success_rate) }}</span></td>
                        <td class="cell-sub">{{ m.recent_samples }}</td>
                        <td @click.stop>
                          <button
                            v-if="m.binding_available && m.binding_unavailable_reason !== 'manual_offline'"
                            class="btn btn-xs btn-ghost"
                            :disabled="toggleBusy[selectedCred.id + '|' + m.raw_model_name]"
                            :title="`下线后自动探测将不再触碰该模型 (原因 = manual_offline)，直到你重新上线`"
                            @click="openToggleDialog(m, 'offline')"
                          >🔴 下线</button>
                          <button
                            v-else-if="m.binding_unavailable_reason === 'manual_offline'"
                            class="btn btn-xs btn-ghost"
                            :disabled="toggleBusy[selectedCred.id + '|' + m.raw_model_name]"
                            title="恢复后下一轮自动探测（~10 min）会重新评估"
                            @click="openToggleDialog(m, 'online')"
                          >🟢 上线</button>
                          <span
                            v-else
                            class="cell-muted"
                            :title="`由自动探测控制: ${m.binding_unavailable_reason || '—'}（不可手动）`"
                          >auto</span>
                        </td>
                      </tr>
                    </tbody>
                  </table>
                  <div class="cell-sub" style="margin-top:4px">点击模型行查看其滑动窗口 →</div>
                </div>
              </div>
            </div>

            <!-- Right column: sliding window + error pie -->
            <div>
              <div class="drawer-section">
                <div class="drawer-section-title">
                  滑动窗口 (最近 1 小时)
                  <span class="source-tag" :class="windowSource === 'redis' ? 'src-redis' : 'src-rl'">
                    {{ windowSource === 'redis' ? 'Redis' : 'request_logs' }}
                  </span>
                </div>
                <div v-if="!selectedModel" class="cell-muted">点击左侧模型查看</div>
                <div v-else>
                  <div style="margin-bottom:8px">
                    <label class="field-label">模型:</label>
                    <code class="mono-sm">{{ selectedModel }}</code>
                  </div>
                  <div v-if="windowLoading">加载中...</div>
                  <div v-else-if="!windowEntries.length" class="cell-muted">无数据</div>
                  <div v-else>
                    <div style="display:flex;gap:4px;overflow-x:auto;padding:8px 0">
                      <div
                        v-for="(e, i) in windowEntries.slice(0, 100)"
                        :key="i"
                        :style="{
                          width: '4px',
                          height: '40px',
                          background: e.ok ? '#10b981' : '#ef4444',
                          opacity: 0.8,
                        }"
                        :title="`${e.ok ? '✓' : '✗'} ${e.lat}ms ${e.err || ''}`"
                      ></div>
                    </div>
                    <div style="display:flex;gap:16px;margin-top:8px;font-size:13px;flex-wrap:wrap">
                      <span>总计: {{ windowEntries.length }}</span>
                      <span style="color:#10b981">成功: {{ windowEntries.filter(e => e.ok).length }}</span>
                      <span style="color:#ef4444">失败: {{ windowEntries.filter(e => !e.ok).length }}</span>
                      <span>失败率: {{ ((windowEntries.filter(e => !e.ok).length / windowEntries.length) * 100).toFixed(1) }}%</span>
                    </div>
                  </div>
                </div>
              </div>

              <div class="drawer-section">
                <div class="drawer-section-title">错误分布</div>
                <div style="height:200px;position:relative">
                  <canvas id="errorPieChart"></canvas>
                </div>
              </div>

              <div class="drawer-section">
                <div class="drawer-section-title" style="display:flex;align-items:center;gap:8px">
                  状态变化历史
                  <span v-if="historyEvents.length" class="cell-sub">({{ historyEvents.length }})</span>
                  <button
                    class="btn btn-xs btn-ghost"
                    :disabled="historyLoading || !selectedModel"
                    style="margin-left:auto"
                    @click="loadHistory"
                  >↻ 刷新</button>
                </div>
                <div v-if="!selectedModel" class="cell-muted">点击模型查看</div>
                <div v-else-if="historyLoading">加载中...</div>
                <div v-else-if="!historyEvents.length" class="cell-muted">无状态变化记录</div>
                <table v-else class="history-table">
                  <thead>
                    <tr>
                      <th>时间</th>
                      <th>来源</th>
                      <th>事件</th>
                      <th>详情</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="(ev, i) in historyEvents" :key="i" :class="`hist-${ev.event}`">
                      <td class="mono-sm">{{ formatTs(ev.ts) }}</td>
                      <td>
                        <span
                          v-if="ev.source === 'auto'"
                          class="badge"
                          :class="ev.event === 'broke' ? 'badge-red' : 'badge-green'"
                        >自动 · {{ ev.triggered_by || 'scheduler' }}</span>
                        <span
                          v-else
                          class="badge"
                          :class="ev.event === 'offline' ? 'badge-red' : 'badge-green'"
                        >手动 · {{ ev.actor || 'admin' }}</span>
                      </td>
                      <td><code class="mono-sm">{{ ev.event }}</code></td>
                      <td class="cell-sub">
                        <template v-if="ev.source === 'auto' && ev.error_code">
                          {{ ev.error_code }}{{ ev.http_status ? ' (' + ev.http_status + ')' : '' }}
                        </template>
                        <template v-else-if="ev.reason">{{ ev.reason }}</template>
                        <template v-else>—</template>
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>

            <!-- Fingerprint slot visualization (2026-06-23) -->
            <div class="drawer-section" style="grid-column:1 / -1">
              <div class="drawer-section-title" style="display:flex;justify-content:space-between;align-items:center">
                <span>并发槽位与指纹分配</span>
                <button class="btn btn-sm" @click="loadFpSlotStats" :disabled="fpSlotStatsLoading">
                  {{ fpSlotStatsLoading ? '加载中…' : '↻ 刷新' }}
                </button>
              </div>
              <div v-if="!fpSlotStats" class="cell-muted" style="margin-top:8px">
                点击「刷新」加载指纹槽位图，查看每个会话的指纹分配情况
              </div>
              <FpSlotVisualizer
                v-else-if="fpSlotStats.slot_limit && fpSlotStats.details"
                :details="fpSlotStats.details"
                :slot-limit="fpSlotStats.slot_limit"
              />
              <div v-else-if="fpSlotStats.unlimited" class="cell-muted">{{ fpSlotStats.message }}</div>
            </div>

            <!-- Routing decisions for this credential (2026-06-23) -->
            <div class="drawer-section" style="grid-column:1 / -1">
              <div class="drawer-section-title" style="display:flex;justify-content:space-between;align-items:center">
                <span>最近路由决策 (50条)</span>
                <button
                  class="btn btn-xs btn-ghost"
                  :disabled="credentialDecisionsLoading"
                  @click="loadCredentialDecisions"
                >↻ 刷新</button>
              </div>
              <div v-if="credentialDecisionsLoading">加载中...</div>
              <div v-else-if="!credentialDecisions.length" class="cell-muted">无路由决策记录</div>
              <div v-else style="overflow-x:auto">
                <table class="decision-table">
                  <thead>
                    <tr>
                      <th>时间</th>
                      <th>请求ID</th>
                      <th>模型</th>
                      <th>Tier</th>
                      <th>结果</th>
                      <th>延迟</th>
                      <th>错误</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="(d, i) in credentialDecisions" :key="i" :class="d.success ? 'decision-success' : 'decision-fail'">
                      <td class="mono-sm">{{ formatTs(d.ts) }}</td>
                      <td class="mono-sm" style="font-size:10px">{{ d.request_id.substring(0, 8) }}</td>
                      <td>
                        <div class="mono-sm" style="font-size:11px">{{ d.client_model || d.model }}</div>
                        <div v-if="d.outbound_model && d.outbound_model !== d.client_model" class="cell-sub" style="font-size:10px">
                          → {{ d.outbound_model }}
                        </div>
                      </td>
                      <td class="mono-sm">{{ d.tier ?? '—' }}</td>
                      <td>
                        <span v-if="d.success" class="badge badge-green">✓</span>
                        <span v-else class="badge badge-red">✗</span>
                        <span v-if="d.sticky_hit" class="badge badge-blue" style="margin-left:4px;font-size:9px">sticky</span>
                      </td>
                      <td class="mono-sm">{{ d.latency_ms != null ? d.latency_ms + 'ms' : '—' }}</td>
                      <td class="cell-sub" style="font-size:10px">{{ d.error_class || '—' }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Batch Dialog -->
    <div v-if="batchDialogOpen" class="drawer-backdrop" @click="batchDialogOpen = false">
      <div class="card" @click.stop style="max-width:500px;margin:auto;margin-top:100px;padding:24px">
        <h3 style="margin-top:0">批量{{ batchAction === 'promote' ? '恢复' : '降级' }} ({{ selectedIds.size }} 个凭据)</h3>
        <div style="margin-bottom:16px">
          <label class="field-label">原因</label>
          <input v-model="batchReason" class="field-input" placeholder="请输入原因" />
        </div>
        <div v-if="batchAction === 'demote'" style="margin-bottom:16px">
          <label class="field-label">自动恢复时间 (小时)</label>
          <input v-model.number="batchHours" type="number" min="0.5" step="0.5" class="field-input" />
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end">
          <button class="btn btn-ghost" @click="batchDialogOpen = false">取消</button>
          <button :class="batchAction === 'promote' ? 'btn btn-success' : 'btn btn-danger'" @click="submitBatch">
            确认{{ batchAction === 'promote' ? '恢复' : '降级' }}
          </button>
        </div>
      </div>
    </div>

    <!-- Demote / Promote / Concurrency dialogs -->
    <div v-if="demoteDialogOpen" class="drawer-backdrop" @click="demoteDialogOpen = false">
      <div class="card" @click.stop style="max-width:500px;margin:auto;margin-top:100px;padding:24px">
        <h3 style="margin-top:0">临时降级</h3>
        <div style="margin-bottom:16px">
          <label class="field-label">降级原因</label>
          <input v-model="demoteReason" class="field-input" placeholder="请输入原因" />
        </div>
        <div style="margin-bottom:16px">
          <label class="field-label">自动恢复时间 (小时)</label>
          <input v-model.number="demoteHours" type="number" min="0.5" step="0.5" class="field-input" />
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end">
          <button class="btn btn-ghost" @click="demoteDialogOpen = false">取消</button>
          <button class="btn btn-danger" @click="submitDemote">确认降级</button>
        </div>
      </div>
    </div>

    <div v-if="promoteDialogOpen" class="drawer-backdrop" @click="promoteDialogOpen = false">
      <div class="card" @click.stop style="max-width:500px;margin:auto;margin-top:100px;padding:24px">
        <h3 style="margin-top:0">恢复上线</h3>
        <div style="margin-bottom:16px">
          <label class="field-label">恢复原因</label>
          <input v-model="promoteReason" class="field-input" placeholder="请输入原因" />
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end">
          <button class="btn btn-ghost" @click="promoteDialogOpen = false">取消</button>
          <button class="btn btn-success" @click="submitPromote">确认恢复</button>
        </div>
      </div>
    </div>

    <div v-if="concurrencyDialogOpen" class="drawer-backdrop" @click="concurrencyDialogOpen = false">
      <div class="card" @click.stop style="max-width:500px;margin:auto;margin-top:100px;padding:24px">
        <h3 style="margin-top:0">手动调整并发自动值</h3>
        <div style="margin-bottom:16px">
          <label class="field-label">并发上限</label>
          <input v-model.number="concurrencyValue" type="number" min="1" class="field-input" />
        </div>
        <div style="margin-bottom:16px">
          <label class="field-label">调整原因</label>
          <input v-model="concurrencyReason" class="field-input" placeholder="请输入原因" />
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end">
          <button class="btn btn-ghost" @click="concurrencyDialogOpen = false">取消</button>
          <button class="btn btn-primary" @click="submitConcurrency">确认</button>
        </div>
      </div>
    </div>

    <!-- 2026-06-23: per-model toggle dialog -->
    <div v-if="toggleDialogOpen" class="drawer-backdrop" @click="toggleDialogOpen = false">
      <div class="card" @click.stop style="max-width:480px;margin:auto;margin-top:120px;padding:20px">
        <h3 style="margin-top:0">
          确认{{ toggleTarget?.action === 'offline' ? '下线' : '上线' }}
        </h3>
        <div class="cell-sub" style="margin-bottom:12px">
          <code class="mono-sm">{{ toggleTarget?.rawModel }}</code> · 凭据 #{{ toggleTarget?.credId }}
        </div>
        <div v-if="toggleTarget?.action === 'offline'" class="cell-sub" style="margin-bottom:12px">
          下线后自动探测将不再触碰该模型（原因 = <code>manual_offline</code>），需你手动恢复。
        </div>
        <div v-else class="cell-sub" style="margin-bottom:12px">
          恢复后下一轮自动探测（~10 min）会重新评估。
        </div>
        <label class="field-label">原因（必填）</label>
        <input
          v-model="toggleReason"
          class="field-input"
          placeholder="例如: 误判 broken / 紧急封禁 / 灰度验证"
          @keyup.enter="submitToggle"
        />
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-ghost" @click="toggleDialogOpen = false">取消</button>
          <button
            :class="toggleTarget?.action === 'offline' ? 'btn btn-danger' : 'btn btn-success'"
            :disabled="!toggleReason.trim()"
            @click="submitToggle"
          >确认{{ toggleTarget?.action === 'offline' ? '下线' : '上线' }}</button>
        </div>
      </div>
    </div>

    <!-- Clear manual_disabled dialog (2026-06-23) -->
    <!-- Clear manual_disabled dialog (2026-06-23) -->
    <div v-if="clearDisabledDialogOpen" class="drawer-backdrop" @click="clearDisabledDialogOpen = false">
      <div class="card" @click.stop style="max-width:480px;margin:auto;margin-top:120px;padding:20px">
        <h3 style="margin-top:0">清除 manual_disabled</h3>
        <div class="cell-sub" style="margin-bottom:12px">
          凭据 #{{ selectedCred?.id }} - {{ selectedCred?.label || '无标签' }}
        </div>
        <div style="margin-bottom:12px;padding:12px;background:rgba(251,191,36,0.1);border:1px solid rgba(251,191,36,0.3);border-radius:6px;font-size:13px">
          ⚠️ 此操作将立即恢复凭据到正常路由池，manual_disabled 标志将被清除。请确认此凭据已经可以正常使用。
        </div>
        <label class="field-label">操作原因（必填）</label>
        <input
          v-model="clearDisabledReason"
          class="field-input"
          placeholder="例如: 供应商恢复正常 / 误操作修正 / 灰度验证完成"
          @keyup.enter="submitClearDisabled"
        />
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-ghost" @click="clearDisabledDialogOpen = false">取消</button>
          <button
            class="btn btn-warning"
            :disabled="!clearDisabledReason.trim()"
            @click="submitClearDisabled"
          >确认清除</button>
        </div>
      </div>
    </div>

    <!-- Set manual_disabled dialog (2026-06-23) -->
    <div v-if="setManualDisabledDialogOpen" class="drawer-backdrop" @click="setManualDisabledDialogOpen = false">
      <div class="card" @click.stop style="max-width:480px;margin:auto;margin-top:120px;padding:20px">
        <h3 style="margin-top:0">{{ setManualDisabledTargetValue ? '禁用凭据' : '启用凭据' }}</h3>
        <div class="cell-sub" style="margin-bottom:12px">
          凭据 #{{ selectedCred?.id }} - {{ selectedCred?.label || '无标签' }}
        </div>
        <div v-if="setManualDisabledTargetValue" style="margin-bottom:12px;padding:12px;background:rgba(239,68,68,0.1);border:1px solid rgba(239,68,68,0.3);border-radius:6px;font-size:13px">
          ⚠️ 此操作将设置 manual_disabled = true，凭据将从路由池移除，不再处理任何流量，直到手动恢复。
        </div>
        <div v-else style="margin-bottom:12px;padding:12px;background:rgba(16,185,129,0.1);border:1px solid rgba(16,185,129,0.3);border-radius:6px;font-size:13px">
          ✓ 此操作将设置 manual_disabled = false，凭据将恢复到正常路由池。
        </div>
        <label class="field-label">操作原因（必填）</label>
        <input
          v-model="setManualDisabledReason"
          class="field-input"
          :placeholder="setManualDisabledTargetValue ? '例如: 供应商维护 / 配额耗尽 / 临时下线' : '例如: 供应商恢复 / 维护完成 / 测试通过'"
          @keyup.enter="submitSetManualDisabled"
        />
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-ghost" @click="setManualDisabledDialogOpen = false">取消</button>
          <button
            :class="setManualDisabledTargetValue ? 'btn btn-danger' : 'btn btn-success'"
            :disabled="!setManualDisabledReason.trim()"
            @click="submitSetManualDisabled"
          >确认{{ setManualDisabledTargetValue ? '禁用' : '启用' }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Outer layout — matches /routing-v2 (RoutingDashboardView): rely on the
   global .main-body padding (24px) and only cap the content width. The
   previous `padding: 24px; max-width: 1400px` was double-padding the
   content area, which is what made the right edge feel far from the
   sidebar/header. */
.page-container {
  max-width: 1200px;
  margin: 0 auto;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

/* Unified top bar — mirrors the .top-bar pattern in RoutingDashboardView:
   card background, tight padding, and the title / refresh / filter
   controls share one horizontal row to reduce vertical real estate. */
.top-bar {
  padding: 8px 10px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.top-bar-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.top-bar-head h1 {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
  flex-shrink: 0;
}
.top-bar-head .refresh-group {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-left: auto;
  flex-wrap: wrap;
}
.top-bar-head .refresh-group label {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: var(--muted);
}
.top-bar-head .refresh-group .field-input {
  width: auto;
  font-size: 12px;
  padding: 2px 6px;
}

/* Filter row inside the top bar — uses toolbar pattern from reference */
.filter-toolbar {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  font-size: 11px;
  color: var(--muted);
}
.filter-toolbar > .label {
  font-size: 11px;
}
.filter-toolbar .field-input { font-size: 11px; padding: 2px 6px; }
.filter-toolbar .spacer { flex: 1; }
.filter-toolbar .btn-sm { font-size: 11px; padding: 2px 8px; }

/* Page header kept for backward compat in case anything still references it */
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 0;
}
.page-header h1 {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
}

/* Summary cards — compact, matches the density of /routing-v2's hero chips
   and AnalyticsKpiBar. The previous 16px padding + 28px value font + 20px
   section gap was too airy for an operations dashboard. */
.summary-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 8px;
}
.summary-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 8px 12px;
}
.summary-label {
  font-size: 11px;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.summary-value {
  font-size: 20px;
  font-weight: 700;
  margin-top: 2px;
  line-height: 1.1;
}
.summary-sub {
  font-size: 10px;
  color: var(--muted);
  margin-top: 2px;
}
.summary-good { border-color: rgba(63, 185, 80, 0.4); }
.summary-good .summary-value { color: var(--success); }
.summary-warn { border-color: rgba(210, 153, 34, 0.4); }
.summary-warn .summary-value { color: var(--warning); }
.summary-bad { border-color: rgba(248, 81, 73, 0.4); }
.summary-bad .summary-value { color: var(--danger); }

/* Quick filter pills */
.quick-filter-group {
  display: inline-flex;
  gap: 4px;
}
.qf-active {
  border-color: var(--accent);
  color: var(--accent-h);
}
.qf-active.qf-bad { border-color: var(--danger); color: var(--danger); }
.qf-active.qf-warn { border-color: var(--warning); color: var(--warning); }

/* Rate coloring */
.rate-cell { font-weight: 600; }
.rate-good { color: var(--success); }
.rate-warn { color: var(--warning); }
.rate-bad { color: var(--danger); }
.rate-none { color: var(--muted); }

/* Main credentials data table — denser than the global style.css default
   (which is 13px / 10px 12px). Mirrors the .dense-table pattern from
   /routing-v2's overview tab so the credentials list can show more rows
   without the right edge pushing past the sidebar. */
.data-table.dense thead th {
  padding: 5px 8px;
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--muted);
  border-bottom: 1px solid var(--border);
  background: var(--bg-subtle);
}
.data-table.dense tbody td {
  padding: 5px 8px;
  font-size: 12px;
  border-bottom: 1px solid var(--border);
  vertical-align: middle;
}
.data-table.dense tbody tr:last-child td { border-bottom: none; }

.model-badge {
  font-size: 10px;
  padding: 1px 6px;
}

/* Clickable table rows (click opens the detail drawer) */
.clickable-row {
  cursor: pointer;
}
.clickable-row:hover {
  background: rgba(255, 255, 255, 0.04) !important;
}

/* Model table in drawer */
.model-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}
.model-table th {
  text-align: left;
  font-size: 11px;
  font-weight: 600;
  color: var(--muted);
  padding: 6px 8px;
  border-bottom: 1px solid var(--border);
}
.model-table td {
  padding: 6px 8px;
  border-bottom: 1px solid var(--border);
}
.model-table tbody tr {
  cursor: pointer;
}
.model-table tbody tr:hover {
  background: rgba(255, 255, 255, 0.03);
}
.model-row-selected {
  background: rgba(99, 102, 241, 0.12) !important;
}

.mono-sm {
  font-family: 'SF Mono', Menlo, Consolas, monospace;
  font-size: 12px;
}

/* Sliding window source tag */
.source-tag {
  display: inline-block;
  margin-left: 8px;
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 600;
  vertical-align: middle;
}
.src-redis { background: rgba(63, 185, 80, 0.15); color: var(--success); }
.src-rl { background: rgba(99, 102, 241, 0.15); color: var(--accent-h); }

.cell-sub { font-size: 11px; color: var(--muted); }
.cell-muted { color: var(--muted); }

.drawer-panel-wide {
  width: min(1000px, 95vw);
}

.drawer-section {
  margin-bottom: 16px;
}
.drawer-section-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 8px;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--border);
}

.field-label {
  display: block;
  font-size: 11px;
  color: var(--muted);
  margin-bottom: 2px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

@media (max-width: 900px) {
  .summary-row {
    grid-template-columns: repeat(2, 1fr);
  }
}

/* 2026-06-23: per-model toggle + state-change history */
.history-table {
  width: 100%;
  font-size: 12px;
  border-collapse: collapse;
}
.history-table th {
  text-align: left;
  font-size: 11px;
  color: var(--muted);
  padding: 4px 6px;
  border-bottom: 1px solid var(--border);
}
.history-table td {
  padding: 4px 6px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}
.history-table tr.hist-broke td:nth-child(3),
.history-table tr.hist-offline td:nth-child(3) {
  color: var(--danger);
  font-weight: 600;
}
.history-table tr.hist-recovered td:nth-child(3),
.history-table tr.hist-online td:nth-child(3) {
  color: var(--success);
  font-weight: 600;
}
.btn-xs {
  padding: 2px 6px;
  font-size: 11px;
}

/* Decision table (2026-06-23) */
.decision-table {
  width: 100%;
  font-size: 12px;
  border-collapse: collapse;
  margin-top: 8px;
}
.decision-table th {
  text-align: left;
  font-size: 11px;
  color: var(--muted);
  padding: 6px 8px;
  border-bottom: 1px solid var(--border);
  font-weight: 600;
}
.decision-table td {
  padding: 6px 8px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}
.decision-table tbody tr.decision-success {
  background: rgba(16, 185, 129, 0.03);
}
.decision-table tbody tr.decision-fail {
  background: rgba(239, 68, 68, 0.03);
}
.decision-table tbody tr:hover {
  background: rgba(255, 255, 255, 0.05) !important;
}
</style>
