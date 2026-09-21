<script setup lang="ts">
/**
 * CredentialDetailDrawer — 凭据详情抽屉（2026-09-13 P3-5 第二批，
 * 自 CredentialMonitorView.vue 拆出，方案 §4.7-4 大文件拆分）。
 *
 * 承载：详情三个 tab（概览/模型可用性/历史）、滑动窗口与错误饼图、
 * 指纹槽位、6 个确认弹窗（降级/恢复/并发/模型上下线/清除禁用/手动禁用，
 * 2026-09-13 迁 ui/AppModal sm）。外层壳由手写遮罩+面板迁 ui/AppDrawer
 * （direction=auto：>=768 右侧 / <768 bottom-sheet，宽度保持 min(1000px, 95vw)）。
 *
 * 数据流：宿主视图通过 v-model（选中凭据，null=关闭）驱动；详情数据
 * 加载/自动刷新全部内聚在本组件，列表刷新通过 refresh-list 事件委托宿主。
 */
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Chart, registerables } from 'chart.js'
import {
  getCredentialMonitorSummary,
  getSlidingWindow,
  promoteCredential,
  demoteCredential,
  setConcurrencyAuto,
  toggleModelAvailability,
  getModelHistory,
  getCredentialFpSlotStats,
  getCredentialDecisions,
  clearManualDisabled,
  setManualDisabled,
  type CredentialMonitorSummary,
  type CredentialModelStatus,
  type CallEntry,
  type ModelHistoryEvent,
  type ModelToggleAction,
  type FpSlotStats,
  type CredentialRoutingDecision,
  type CredentialMonitorMeta,
} from '../../api'
import AppDrawer from '../ui/AppDrawer.vue'
import AppModal from '../ui/AppModal.vue'
import FpSlotVisualizer from '../FpSlotVisualizer.vue'
import SegTabs, { type SegTab } from '../SegTabs.vue'
import StatusBadge from '../StatusBadge.vue'
import CredentialStatusBar from '../CredentialStatusBar.vue'
import { useCredentialLabels } from '../../composables/useCredentialLabels'
import { isSuperAdmin } from '../../store'
import {
  effectiveCredentialReason,
  formatCacheMeta,
  formatRefreshAt,
  formatTs,
  healthBadge,
  p95Class,
  rateClass,
  rateText,
  statusBadge,
} from './helpers'

Chart.register(...registerables)

const props = defineProps<{
  /** 选中凭据（null = 抽屉关闭）；宿主 v-model */
  modelValue: CredentialMonitorSummary | null
}>()

const emit = defineEmits<{
  'update:modelValue': [value: CredentialMonitorSummary | null]
  /** 需要宿主重载凭据列表（原视图内 load() 的委托） */
  'refresh-list': []
}>()

const { t } = useI18n()

const { credentialDisplayName } = useCredentialLabels()
const canManageMonitor = computed(() => isSuperAdmin())

// 单向数据流：组件内不直接改选中凭据，统一经 emit 由宿主回写。
const selectedCred = computed(() => props.modelValue)

function closeDrawer() {
  emit('update:modelValue', null)
}

const detailLoading = ref(false)
const lastDetailRefreshAt = ref<Date | null>(null)
const detailMeta = ref<CredentialMonitorMeta | null>(null)
const selectedModel = ref('')
const windowEntries = ref<CallEntry[]>([])
const windowSource = ref<'redis' | 'request_logs'>('redis')
const windowLoading = ref(false)

type DetailTab = 'overview' | 'models' | 'history'
const detailActiveTab = ref<DetailTab>('overview')
const detailTabs: SegTab[] = [
  { value: 'overview', label: t('credentialMonitor.drawer.tab.overview') },
  { value: 'models',   label: t('credentialMonitor.drawer.tab.models') },
  { value: 'history',  label: t('credentialMonitor.drawer.tab.requests') },
]
// 打开详情时默认到第一个 tab；抽屉关闭时停掉自动刷新（原视图 watch 语义）
watch(selectedCred, (newVal) => {
  if (newVal) detailActiveTab.value = 'overview'
  else stopDetailAutoRefresh()
})

// ── 2026-06-24: models tab 三态布局 + localStorage 持久化 + 切换动画 ────
type LayoutMode = 'split' | 'list-full' | 'monitor-full'
const LAYOUT_STORAGE_KEY = 'cmc_models_layout'
function loadStoredLayout(): LayoutMode {
  if (typeof window === 'undefined') return 'split'
  try {
    const v = window.localStorage.getItem(LAYOUT_STORAGE_KEY)
    if (v === 'split' || v === 'list-full' || v === 'monitor-full') return v
  } catch { /* ignore */ }
  return 'split'
}
const modelsLayout = ref<LayoutMode>(loadStoredLayout())
const layoutAnimating = ref(false)
let layoutAnimTimer: number | null = null

function setLayout(mode: LayoutMode) {
  if (modelsLayout.value === mode) return
  modelsLayout.value = mode
  // 持久化 (split / monitor-full 都要保留;用户切换即写入)
  if (typeof window !== 'undefined') {
    try { window.localStorage.setItem(LAYOUT_STORAGE_KEY, mode) } catch { /* ignore */ }
  }
  // 200ms 反馈动画
  layoutAnimating.value = true
  if (layoutAnimTimer) clearTimeout(layoutAnimTimer)
  layoutAnimTimer = window.setTimeout(() => {
    layoutAnimating.value = false
    layoutAnimTimer = null
  }, 220)
  // Chart.js canvas resize (饼图在 split → monitor-full 时需要重排)
  if (errorPieChart) {
    window.setTimeout(() => errorPieChart?.resize(), 240)
  }
}

// split ⇄ monitor-full 一键切换 (◀/▶ 折叠按钮)
function toggleLeftPane() {
  if (modelsLayout.value === 'monitor-full') setLayout('split')
  else setLayout('monitor-full')
}

// Detail auto-refresh (2026-06-23)
const detailAutoRefresh = ref(false)
const detailRefreshInterval = ref(5) // seconds
let detailRefreshTimer: number | null = null
let detailRequestSeq = 0
let modelRequestSeq = 0
let decisionRequestSeq = 0
let fpSlotRequestSeq = 0

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

async function loadCredentialDecisionsFor(credentialId: number) {
  const requestSeq = ++decisionRequestSeq
  credentialDecisionsLoading.value = true
  try {
    const res = await getCredentialDecisions(credentialId, 50)
    if (requestSeq !== decisionRequestSeq || selectedCred.value?.id !== credentialId) return
    credentialDecisions.value = res.decisions
  } catch (e) {
    console.error('credential decisions load failed', e)
  } finally {
    if (requestSeq === decisionRequestSeq) {
      credentialDecisionsLoading.value = false
    }
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

async function loadFpSlotStatsFor(providerId: number, credentialId: number) {
  const requestSeq = ++fpSlotRequestSeq
  fpSlotStatsLoading.value = true
  try {
    const stats = await getCredentialFpSlotStats(providerId, credentialId)
    if (requestSeq !== fpSlotRequestSeq || selectedCred.value?.id !== credentialId) return
    fpSlotStats.value = stats
  } catch (e) {
    console.error('fp slot stats load failed', e)
  } finally {
    if (requestSeq === fpSlotRequestSeq) {
      fpSlotStatsLoading.value = false
    }
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
  if (!canManageMonitor.value) return
  clearDisabledDialogOpen.value = true
  clearDisabledReason.value = ''
}

async function submitClearDisabled() {
  if (!canManageMonitor.value || !selectedCred.value) return
  try {
    await clearManualDisabled(selectedCred.value.id, clearDisabledReason.value)
    clearDisabledDialogOpen.value = false
    await refreshDetailDrawer()
  } catch (e) {
    alert(t('credentialMonitor.error.clearFailed') + (e instanceof Error ? e.message : String(e)))
  }
}

// Set manual_disabled (2026-06-23)
function openSetManualDisabledDialog(targetValue: boolean) {
  if (!canManageMonitor.value) return
  setManualDisabledTargetValue.value = targetValue
  setManualDisabledReason.value = ''
  setManualDisabledDialogOpen.value = true
}

async function submitSetManualDisabled() {
  if (!canManageMonitor.value || !selectedCred.value || !setManualDisabledReason.value.trim()) return
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
  const currentId = selectedCred.value.id
  const currentProviderId = selectedCred.value.provider_id
  const requestSeq = ++detailRequestSeq
  detailLoading.value = true
  try {
    const decisionsP = loadCredentialDecisionsFor(currentId)
    const fpP = loadFpSlotStatsFor(currentProviderId, currentId)
    emit('refresh-list')
    const res = await getCredentialMonitorSummary({ credential_id: currentId })
    await Promise.all([decisionsP, fpP])
    if (requestSeq !== detailRequestSeq || selectedCred.value?.id !== currentId) return
    const detail = res.credentials[0]
    if (detail) {
      emit('update:modelValue', detail)
      detailMeta.value = res.meta || null
      lastDetailRefreshAt.value = new Date()
      if (selectedModel.value) {
        const stillExists = (detail.models || []).some((m) => m.raw_model_name === selectedModel.value)
        if (!stillExists) {
          const firstModel = (detail.models || [])[0]
          selectedModel.value = firstModel?.raw_model_name || ''
        }
      }
    }
  } catch (e) {
    console.error('detail refresh failed', e)
  } finally {
    if (requestSeq === detailRequestSeq) {
      detailLoading.value = false
    }
  }
  // Reload model-scoped sections after the detail payload picks the active model.
  if (selectedCred.value && selectedModel.value) {
    const modelSeq = ++modelRequestSeq
    await Promise.all([
      loadSlidingWindow(selectedCred.value.id, selectedModel.value, modelSeq),
      loadHistory(modelSeq),
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

// ── 打开新凭据：原视图 openDetail() 的加载序列（id 变化触发） ──
watch(() => props.modelValue?.id, (newId, oldId) => {
  if (newId == null || newId === oldId) return
  const row = props.modelValue
  if (!row) return
  void openDetail(row)
})

async function openDetail(cred: CredentialMonitorSummary) {
  const requestSeq = ++detailRequestSeq
  detailLoading.value = true
  selectedModel.value = ''
  windowEntries.value = []
  historyEvents.value = []
  try {
    const [res] = await Promise.all([
      getCredentialMonitorSummary({ credential_id: cred.id }),
      loadCredentialDecisionsFor(cred.id),
      loadFpSlotStatsFor(cred.provider_id, cred.id),
    ])
    if (requestSeq !== detailRequestSeq || selectedCred.value?.id !== cred.id) return
    const detail = res.credentials[0]
    if (!detail) return
    emit('update:modelValue', detail)
    detailMeta.value = res.meta || null
    lastDetailRefreshAt.value = new Date()
    const models = detail.models || []
    const broken = models.find(m => m.probe_state === 'broken_confirmed')
    const pick = broken || models.slice().sort((a, b) => (a.recent_success_rate ?? 1) - (b.recent_success_rate ?? 1))[0]
    selectedModel.value = pick?.raw_model_name || ''
    if (selectedModel.value) {
      const modelSeq = ++modelRequestSeq
      await Promise.all([
        loadSlidingWindow(detail.id, selectedModel.value, modelSeq),
        loadHistory(modelSeq),
      ])
    }
  } catch (e) {
    console.error('detail load failed', e)
  } finally {
    if (requestSeq === detailRequestSeq) {
      detailLoading.value = false
    }
  }
}

async function loadSlidingWindow(credId: number, model: string, requestSeq = ++modelRequestSeq) {
  if (!model) return
  windowLoading.value = true
  try {
    const res = await getSlidingWindow(credId, model, 60)
    if (requestSeq !== modelRequestSeq || selectedCred.value?.id !== credId || selectedModel.value !== model) return
    windowEntries.value = res.entries
    windowSource.value = res.source
    setTimeout(() => renderErrorPieChart(res.stats.error_kinds), 100)
  } catch (e) {
    console.error('sliding window failed', e)
  } finally {
    if (requestSeq === modelRequestSeq) {
      windowLoading.value = false
    }
  }
}

function selectModel(model: string) {
  if (!selectedCred.value || model === selectedModel.value) return
  selectedModel.value = model
  const requestSeq = ++modelRequestSeq
  loadSlidingWindow(selectedCred.value.id, model, requestSeq)
  loadHistory(requestSeq)
}

function refreshSelectedModelHistory() {
  const requestSeq = ++modelRequestSeq
  loadHistory(requestSeq)
}

// 🆕 2026-06-25: 当前选中模型对象 + 手工控制状态派生
// 详情页右列的模型名右侧要展示一组状态图标,其中"手工控制"图标 3 态可点击循环.
// 3 态 = 手工禁用 (binding_unavailable_reason='manual_offline') /
//       手工启动 (binding_available=true,reason 非 manual_offline) /
//       自动     (binding_available=false,reason 非 manual_offline,被探测系统下线)
const selectedModelObj = computed<CredentialModelStatus | null>(() => {
  if (!selectedCred.value || !selectedModel.value) return null
  return (selectedCred.value.models || []).find(m => m.raw_model_name === selectedModel.value) ?? null
})

type ManualControlState = 'manual_disabled' | 'manual_enabled' | 'auto_disabled'
const manualControlState = computed<ManualControlState | null>(() => {
  const m = selectedModelObj.value
  if (!m) return null
  if (m.binding_unavailable_reason === 'manual_offline') return 'manual_disabled'
  if (m.binding_available) return 'manual_enabled'
  return 'auto_disabled'
})

interface ManualControlMeta {
  label: string
  emoji: string
  bg: string
  border: string
  color: string
  tooltip: string
}
function manualControlMeta(state: ManualControlState): ManualControlMeta {
  if (state === 'manual_disabled') {
    return {
      label: '手工禁用',
      emoji: '🔴',
      bg: 'rgba(239, 68, 68, 0.12)',
      border: 'rgba(239, 68, 68, 0.4)',
      color: '#ef4444',
      tooltip: '已通过手工方式禁用,自动探测不会触碰. 点击 → 手工启动',
    }
  }
  if (state === 'manual_enabled') {
    return {
      label: '手工启动',
      emoji: '🟢',
      bg: 'rgba(16, 185, 129, 0.12)',
      border: 'rgba(16, 185, 129, 0.4)',
      color: '#10b981',
      tooltip: '当前可用,可点击 → 手工禁用',
    }
  }
  return {
    label: '自动',
    emoji: '⚙️',
    bg: 'rgba(139, 148, 158, 0.12)',
    border: 'rgba(139, 148, 158, 0.4)',
    color: '#8b949e',
    tooltip: '由自动探测控制 (broken_confirmed 等). 点击 → 手工禁用 (强制下线)',
  }
}

function onClickManualControl() {
  if (!canManageMonitor.value) return
  const m = selectedModelObj.value
  if (!m) return
  const state = manualControlState.value
  if (state === 'manual_disabled') {
    // 手工禁用 → 手工启动: 后端 toggle online (清掉 manual_offline 锁)
    openToggleDialog(m, 'online')
  } else if (state === 'manual_enabled' || state === 'auto_disabled') {
    // 手工启动 / 自动 → 手工禁用: 后端 toggle offline (置 manual_offline)
    openToggleDialog(m, 'offline')
  }
}

function openToggleDialog(m: CredentialModelStatus, action: ModelToggleAction) {
  if (!canManageMonitor.value || !selectedCred.value) return
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
  if (!canManageMonitor.value || !toggleTarget.value || !toggleReason.value.trim()) return
  const target = toggleTarget.value
  const key = `${target.credId}|${target.rawModel}`
  toggleBusy.value[key] = true
  try {
    await toggleModelAvailability(target.credId, target.rawModel, target.action, toggleReason.value.trim())
    toggleDialogOpen.value = false
    emit('refresh-list') // refresh summary so the row badge updates
    await loadHistory() // refresh history with the new manual event on top
  } catch (e) {
    alert(`${target.action === 'offline' ? '下线' : '上线'}失败: ` + (e instanceof Error ? e.message : String(e)))
  } finally {
    toggleBusy.value[key] = false
  }
}

async function loadHistory(requestSeq = modelRequestSeq) {
  if (!selectedCred.value || !selectedModel.value) {
    historyEvents.value = []
    return
  }
  const currentCredId = selectedCred.value.id
  const currentModel = selectedModel.value
  historyLoading.value = true
  try {
    const res = await getModelHistory(currentCredId, currentModel, 50)
    if (requestSeq !== modelRequestSeq || selectedCred.value?.id !== currentCredId || selectedModel.value !== currentModel) return
    historyEvents.value = res.events
  } catch (e) {
    console.error('history failed', e)
    if (requestSeq === modelRequestSeq) {
      historyEvents.value = []
    }
  } finally {
    if (requestSeq === modelRequestSeq) {
      historyLoading.value = false
    }
  }
}

// ── 降级 / 恢复 / 并发弹窗（2026-09-13 迁 ui/AppModal sm） ──
const demoteDialogOpen = ref(false)
const demoteReason = ref('')
const demoteHours = ref(2)

const promoteDialogOpen = ref(false)
const promoteReason = ref('')

const concurrencyDialogOpen = ref(false)
const concurrencyValue = ref(5)
const concurrencyReason = ref('')

// Error pie chart
let errorPieChart: Chart | null = null

function renderErrorPieChart(errorKinds: Record<string, number>) {
  const canvas = document.getElementById('errorPieChart') as HTMLCanvasElement
  if (!canvas) return

  if (errorPieChart) {
    errorPieChart.destroy()
    errorPieChart = null
  }

  const labels = Object.keys(errorKinds)
  const data = Object.values(errorKinds)

  // 🆕 2026-06-25: 无错误时显示绿色"全绿"饼图 (单段 100%),
  // 而不是返回空白;保留图例/标题,让用户明确看到"目前没有错误".
  if (labels.length === 0) {
    errorPieChart = new Chart(canvas, {
      type: 'pie',
      data: {
        labels: [t('credentialMonitor.chart.allHealthy')],
        datasets: [{
          data: [1],
          backgroundColor: ['#10b981'],
          borderColor: ['#059669'],
          borderWidth: 2,
        }],
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: {
          legend: { position: 'right' },
          title: { display: true, text: t('credentialMonitor.chart.errorsWhenHealthy') },
        },
      },
    })
    return
  }

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
        title: { display: true, text: t('credentialMonitor.chart.errorsTitle') },
      },
    },
  })
}

function openDemoteDialog() {
  if (!canManageMonitor.value) return
  demoteDialogOpen.value = true
  demoteReason.value = ''
  demoteHours.value = 2
}

async function submitDemote() {
  if (!canManageMonitor.value || !selectedCred.value) return
  try {
    await demoteCredential(selectedCred.value.id, demoteReason.value, demoteHours.value)
    demoteDialogOpen.value = false
    emit('refresh-list')
    closeDrawer()
  } catch (e) {
    alert('降级失败: ' + (e instanceof Error ? e.message : String(e)))
  }
}

function openPromoteDialog() {
  if (!canManageMonitor.value) return
  promoteDialogOpen.value = true
  promoteReason.value = ''
}

async function submitPromote() {
  if (!canManageMonitor.value || !selectedCred.value) return
  try {
    await promoteCredential(selectedCred.value.id, promoteReason.value)
    promoteDialogOpen.value = false
    emit('refresh-list')
    closeDrawer()
  } catch (e) {
    alert('升级失败: ' + (e instanceof Error ? e.message : String(e)))
  }
}

function openConcurrencyDialog() {
  if (!canManageMonitor.value) return
  concurrencyDialogOpen.value = true
  concurrencyValue.value = selectedCred.value?.concurrency_limit_auto || selectedCred.value?.effective_concurrency || 5
  concurrencyReason.value = ''
}

async function submitConcurrency() {
  if (!canManageMonitor.value || !selectedCred.value) return
  try {
    await setConcurrencyAuto(selectedCred.value.id, concurrencyValue.value, concurrencyReason.value)
    concurrencyDialogOpen.value = false
    emit('refresh-list')
  } catch (e) {
    alert('设置失败: ' + (e instanceof Error ? e.message : String(e)))
  }
}

onUnmounted(() => {
  stopDetailAutoRefresh()
  if (layoutAnimTimer) {
    clearTimeout(layoutAnimTimer)
    layoutAnimTimer = null
  }
  if (errorPieChart) errorPieChart.destroy()
})
</script>

<template>
  <AppDrawer
    :model-value="!!selectedCred"
    direction="auto"
    width="min(1000px, 95vw)"
    :title="selectedCred ? credentialDisplayName(selectedCred.id, selectedCred.label || '凭据') : ''"
    @update:model-value="(v: boolean) => { if (!v) emit('update:modelValue', null) }"
  >
    <template v-if="selectedCred">
      <!-- 工具栏：供应商 + 整凭据禁用入口 + 自动刷新控制（原 drawer-header 右侧）。
           🆕 2026-06-23: 整凭据 manual_disabled 抽屉入口 (一键禁用/解除).
           之前必须切到 Provider 详情页 / CredsTab 才能改, 现在抽屉内直接可点.
           原右端「关闭」按钮由 AppDrawer 头部 ✕ 承担。 -->
      <div class="detail-toolbar">
        <span class="drawer-sub">{{ selectedCred.provider_name }}</span>
        <div class="detail-toolbar-controls">
          <button
            v-if="selectedCred.manual_disabled"
            class="btn btn-xs btn-warning"
            :disabled="!canManageMonitor"
            title="解除整凭据禁用"
            @click="openClearDisabledDialog"
          >🔓 解除禁用</button>
          <button
            v-else
            class="btn btn-xs btn-danger"
            :disabled="!canManageMonitor"
            title="手动禁用此凭据 (路由时将不被选中)"
            @click="openSetManualDisabledDialog(true)"
          >⛔ 手动禁用</button>
          <label style="display:flex;align-items:center;gap:4px;font-size:13px;cursor:pointer">
            <input type="checkbox" :checked="detailAutoRefresh" @change="toggleDetailAutoRefresh" />
            自动刷新
          </label>
          <select v-model.number="detailRefreshInterval" class="field-input" style="width:auto;font-size:13px;padding:2px 6px">
            <option :value="5">5秒</option>
            <option :value="10">10秒</option>
            <option :value="30">30秒</option>
          </select>
          <button class="btn btn-sm btn-ghost" :disabled="detailLoading" @click="refreshDetailDrawer" title="刷新详情">
            <span style="font-size:16px">↻</span>
          </button>
        </div>
      </div>

      <!-- 🆕 2026-06-23: 4-tab segmented tabs 容器 (复用 RoutingDashboardView 的 .seg-tabs 风格) -->
      <div class="detail-tabs-row">
        <SegTabs v-model="detailActiveTab" :tabs="detailTabs" />
        <span class="cell-sub" style="margin-left:auto">
          详情 {{ detailLoading ? '刷新中...' : formatRefreshAt(lastDetailRefreshAt) }}
        </span>
        <span class="cell-sub">
          {{ formatCacheMeta(detailMeta) }}
        </span>
        <span class="cell-sub">
          凭据 ID: <code class="mono-sm">{{ selectedCred.id }}</code>
        </span>
      </div>

      <div class="drawer-body" :class="{ 'drawer-body-loading': detailLoading && !!(selectedCred.models || []).length }">
        <div v-if="detailLoading && !(selectedCred.models || []).length" class="detail-loading-state">
          <div class="detail-skeleton-grid">
            <div v-for="idx in 4" :key="idx" class="detail-skeleton-card">
              <div class="skeleton skeleton-title"></div>
              <div class="skeleton skeleton-line"></div>
              <div class="skeleton skeleton-line short"></div>
            </div>
          </div>
          <div class="detail-loading-hint">正在加载凭据详情与模型明细...</div>
        </div>
        <div v-else-if="detailLoading" class="detail-refresh-mask">
          <div class="detail-refresh-pill">详情刷新中...</div>
        </div>
        <!-- ════════════ Tab 1: 概览 (Overview) ════════════ -->
        <div v-if="detailActiveTab === 'overview'" style="display:grid;grid-template-columns:1fr 1fr;gap:16px">
          <div class="drawer-section">
            <div class="drawer-section-title">状态概览</div>
            <div style="display:grid;grid-template-columns:repeat(2,1fr);gap:12px">
              <div>
                <label class="field-label">有效状态</label>
                <CredentialStatusBar :credential="selectedCred" :reason="effectiveCredentialReason(selectedCred)" />
              </div>
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
              <div>
                <label class="field-label">总请求数</label>
                <span class="mono-sm">{{ selectedCred.total_requests }}</span>
              </div>
            </div>
            <div v-if="selectedCred.state_reason_detail" class="cell-sub" style="margin-top:8px">
              {{ selectedCred.state_reason_detail }}
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
        </div>

        <!-- ════════════ Tab 2: 模型可用性 + 监控 (Models + Monitoring, 三态布局) ════════════ -->
        <!--
          三态: split (默认,左右连动) / list-full (左 8 列全宽,右窄条) / monitor-full (右 3 section 全宽,左窄条).
          中间 layout picker 切换;右列顶部有 ◀/▶ 折叠按钮.
          切换时给外层加 .pane-anim 触发 200ms 渐入.
        -->
        <div v-else-if="detailActiveTab === 'models'"
             class="models-tab"
             :class="[
               `models-grid-${modelsLayout}`,
               { 'pane-anim': layoutAnimating },
             ]">
          <!-- 工具栏: 模型总数 + 三态切换 + 折叠按钮 -->
          <div class="models-toolbar">
            <div class="models-toolbar-left">
              <span class="drawer-section-title models-title">
                模型可用性 <span class="cell-muted">({{ (selectedCred.models || []).length }})</span>
              </span>
              <span class="cell-sub" v-if="modelsLayout !== 'monitor-full'">点击行联动查看右侧监控</span>
            </div>
            <div class="models-toolbar-right">
              <!-- 三态分段控件 -->
              <div class="layout-picker" role="group" aria-label="布局切换">
                <button
                  class="layout-btn"
                  :class="{ active: modelsLayout === 'list-full' }"
                  title="列表全宽 (重点排查模型列表)"
                  @click="setLayout('list-full')"
                >▮ 列表</button>
                <button
                  class="layout-btn"
                  :class="{ active: modelsLayout === 'split' }"
                  title="左右分屏 (默认)"
                  @click="setLayout('split')"
                >▣ 分屏</button>
                <button
                  class="layout-btn"
                  :class="{ active: modelsLayout === 'monitor-full' }"
                  title="监控全宽 (深度分析某个模型)"
                  @click="setLayout('monitor-full')"
                >▯ 监控</button>
              </div>
              <!-- 折叠按钮 (双入口:split ⇄ monitor-full 一键切换) -->
              <button
                v-if="modelsLayout !== 'list-full'"
                class="btn btn-xs btn-ghost fold-btn"
                :title="modelsLayout === 'monitor-full' ? '展开左列 (回到分屏)' : '折叠左列 (监控全宽)'"
                @click="toggleLeftPane"
              >{{ modelsLayout === 'monitor-full' ? '▶' : '◀' }}</button>
              <button
                v-else
                class="btn btn-xs btn-ghost fold-btn"
                title="展开右列 (回到分屏)"
                @click="setLayout('split')"
              >▶</button>
            </div>
          </div>

          <!-- ═══ 左列: 模型表 (split 全表 / list-full 全宽 / monitor-full 折叠窄条) ═══ -->
          <div class="pane pane-left">
            <div v-if="modelsLayout === 'monitor-full'" class="pane-collapsed">
              <div class="pane-collapsed-label">当前选中</div>
              <div class="pane-collapsed-value">
                <code class="mono-sm">{{ selectedModel || '—' }}</code>
              </div>
              <button class="btn btn-xs btn-ghost" @click="setLayout('split')" title="回到分屏">
                ▶ 展开
              </button>
            </div>
            <div v-else>
              <div v-if="!(selectedCred.models || []).length" class="cell-muted" style="padding:8px">无模型</div>
              <div v-else style="overflow-x:auto">
                <table class="model-table">
                  <thead>
                    <tr>
                      <th>模型</th>
                      <th>总状态</th>
                      <th>可用</th>
                      <th>来源</th>
                      <th>延迟 P95</th>
                      <th>成功率</th>
                      <th>样本</th>
                      <th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="m in selectedCred.models" :key="m.raw_model_name"
                        :class="{
                          'model-row-selected': m.raw_model_name === selectedModel,
                          'model-row-declared': m.data_source === 'declared',
                        }"
                        :title="m.model_disabled_reason || ''"
                        @click="selectModel(m.raw_model_name)">
                      <td>
                        <code class="mono-sm">{{ m.raw_model_name }}</code>
                        <div v-if="m.standardized_name || m.canonical_name" class="cell-muted" style="font-size:11px">
                          标准: {{ m.canonical_name || m.standardized_name }}
                        </div>
                        <span v-if="!m.offer_available || !m.binding_available" class="badge badge-gray" style="margin-left:4px">unavail</span>
                      </td>
                      <td>
                        <StatusBadge :state="m.effective_state" :reason="m.model_disabled_reason" />
                      </td>
                      <td>
                        <span v-if="m.offer_available && m.binding_available" style="color:var(--success)">✓</span>
                        <span v-else style="color:var(--danger)">✗</span>
                      </td>
                      <td>
                        <span class="source-chip" :class="`source-${m.data_source}`">{{ m.data_source }}</span>
                        <span v-if="m.last_used_at" class="cell-sub" style="margin-left:4px;font-size:10px">
                          {{ m.total_calls }}次
                        </span>
                      </td>
                      <td>
                        <span v-if="m.p95_latency_ms == null" class="cell-muted">N/A</span>
                        <span v-else class="mono-sm" :class="p95Class(m.p95_latency_ms)">
                          {{ m.p95_latency_ms }}ms
                          <span class="cell-sub" style="font-size:9px;margin-left:2px">({{ m.p95_source === 'bg_rollup' ? 'bg' : 'live' }})</span>
                        </span>
                      </td>
                      <td><span class="rate-cell" :class="rateClass(m.recent_success_rate)">{{ rateText(m.recent_success_rate) }}</span></td>
                      <td class="cell-sub">{{ m.recent_samples }}</td>
                      <td @click.stop>
                        <button
                          v-if="m.binding_available && m.binding_unavailable_reason !== 'manual_offline'"
                          class="btn btn-xs btn-ghost"
                          :disabled="!canManageMonitor || toggleBusy[selectedCred.id + '|' + m.raw_model_name]"
                          :title="`下线后自动探测将不再触碰该模型 (原因 = manual_offline)，直到你重新上线`"
                          @click="openToggleDialog(m, 'offline')"
                        >🔴 下线</button>
                        <button
                          v-else-if="m.binding_unavailable_reason === 'manual_offline'"
                          class="btn btn-xs btn-ghost"
                          :disabled="!canManageMonitor || toggleBusy[selectedCred.id + '|' + m.raw_model_name]"
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
              </div>
            </div>
          </div>

          <!-- ═══ 右列: 监控 (split 与 monitor-full 显示 / list-full 折叠窄条) ═══ -->
          <div class="pane pane-right">
            <div v-if="modelsLayout === 'list-full'" class="pane-collapsed">
              <div class="pane-collapsed-label">当前选中</div>
              <div class="pane-collapsed-value">
                <code class="mono-sm">{{ selectedModel || '—' }}</code>
              </div>
              <button class="btn btn-xs btn-ghost" @click="setLayout('split')" title="回到分屏">
                ◀ 展开
              </button>
            </div>
            <div v-else class="monitor-stack">
              <!-- 滑动窗口 -->
              <div class="drawer-section">
                <div class="drawer-section-title">
                  滑动窗口 (最近 1 小时)
                  <span class="source-tag" :class="windowSource === 'redis' ? 'src-redis' : 'src-rl'">
                    {{ windowSource === 'redis' ? 'Redis' : 'request_logs' }}
                  </span>
                </div>
                <div v-if="!selectedModel" class="cell-muted">点击左侧模型行查看</div>
                <div v-else>
                  <div style="margin-bottom:8px;display:flex;align-items:center;gap:8px;flex-wrap:wrap">
                    <label class="field-label">模型:</label>
                    <code class="mono-sm">{{ selectedModel }}</code>
                    <!-- 🆕 2026-06-25: 模型名称右侧状态图标组
                         - 手工控制 (3 态可点击循环): 手工禁用 / 手工启动 / 自动
                         - 总状态 badge (来自 StatusBadge): available / probe_broken / offer_missing / binding_missing
                         多个图标横向排列,鼠标悬停显示详情;只有"手工控制"可点击切换 -->
                    <div v-if="selectedModelObj" class="model-status-icons">
                      <!-- 手工控制按钮 (3 态) -->
                      <button
                        v-if="manualControlState"
                        type="button"
                        class="status-icon-btn"
                        :class="`status-icon-${manualControlState}`"
                        :style="{
                          background: manualControlMeta(manualControlState).bg,
                          borderColor: manualControlMeta(manualControlState).border,
                          color: manualControlMeta(manualControlState).color,
                        }"
                        :title="manualControlMeta(manualControlState).tooltip"
                        :disabled="!canManageMonitor || toggleBusy[selectedCred.id + '|' + selectedModel]"
                        @click="onClickManualControl"
                      >
                        <span class="status-icon-emoji">{{ manualControlMeta(manualControlState).emoji }}</span>
                        <span class="status-icon-label">{{ manualControlMeta(manualControlState).label }}</span>
                      </button>
                      <!-- 总状态只读 badge (其他原因: probe / offer / binding) -->
                      <span
                        v-if="selectedModelObj.effective_state !== 'available' && selectedModelObj.effective_state !== 'manual_disabled'"
                        class="status-icon-info"
                        :title="selectedModelObj.model_disabled_reason || ''"
                      >
                        <StatusBadge :state="selectedModelObj.effective_state" :reason="selectedModelObj.model_disabled_reason" />
                      </span>
                      <!-- available 但无手工标记 = 纯自动 + 健康 -->
                      <span
                        v-else-if="manualControlState === 'manual_enabled'"
                        class="status-icon-info"
                        title="自动控制下当前可用,探测未标记 broken"
                      >
                        <StatusBadge state="available" reason="自动控制可用" />
                      </span>
                    </div>
                  </div>
                  <div v-if="windowLoading">加载中...</div>
                  <div v-else-if="!windowEntries.length" class="cell-muted">无数据</div>
                  <div v-else>
                    <div class="window-strip">
                      <div
                        v-for="(e, i) in windowEntries.slice(0, 100)"
                        :key="i"
                        class="window-cell"
                        :style="{
                          background: e.ok ? '#10b981' : '#ef4444',
                          opacity: 0.85,
                        }"
                        :title="`${e.ok ? '✓' : '✗'} ${e.lat}ms ${e.err || ''}`"
                      ></div>
                    </div>
                    <div class="window-stats">
                      <span>总计: <b>{{ windowEntries.length }}</b></span>
                      <span style="color:var(--success)">成功: <b>{{ windowEntries.filter(e => e.ok).length }}</b></span>
                      <span style="color:var(--danger)">失败: <b>{{ windowEntries.filter(e => !e.ok).length }}</b></span>
                      <span>失败率: <b>{{ ((windowEntries.filter(e => !e.ok).length / windowEntries.length) * 100).toFixed(1) }}%</b></span>
                    </div>
                  </div>
                </div>
              </div>

              <!-- 错误分布 -->
              <div class="drawer-section">
                <div class="drawer-section-title">错误分布</div>
                <div class="pie-wrap">
                  <canvas id="errorPieChart"></canvas>
                </div>
              </div>

              <!-- 并发槽位与指纹分配 (跨整列) -->
              <div class="drawer-section">
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
            </div>
          </div>
        </div>

        <!-- ════════════ Tab 3: 历史 (History) ════════════ -->
        <div v-else-if="detailActiveTab === 'history'" style="display:grid;grid-template-columns:1fr 1fr;gap:16px">
          <div class="drawer-section">
            <div class="drawer-section-title" style="display:flex;align-items:center;gap:8px">
              状态变化历史
              <span v-if="historyEvents.length" class="cell-sub">({{ historyEvents.length }})</span>
              <button
                class="btn btn-xs btn-ghost"
                :disabled="historyLoading || !selectedModel"
                style="margin-left:auto"
                @click="refreshSelectedModelHistory"
              >↻ 刷新</button>
            </div>
            <div v-if="!selectedModel" class="cell-muted">点击「模型可用性」tab 中的模型查看</div>
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
    </template>

    <!-- ═══ 确认弹窗 ×6（2026-09-13 手写遮罩+卡片迁 ui/AppModal sm） ═══ -->

    <!-- Demote Dialog -->
    <AppModal v-model="demoteDialogOpen" title="临时降级" size="sm">
      <div style="margin-bottom:16px">
        <label class="field-label">降级原因</label>
        <input v-model="demoteReason" class="field-input" placeholder="请输入原因" />
      </div>
      <div style="margin-bottom:16px">
        <label class="field-label">自动恢复时间 (小时)</label>
        <input v-model.number="demoteHours" type="number" min="0.5" step="0.5" class="field-input" />
      </div>
      <template #footer>
        <button class="btn btn-ghost" @click="demoteDialogOpen = false">取消</button>
        <button class="btn btn-danger" @click="submitDemote">确认降级</button>
      </template>
    </AppModal>

    <!-- Promote Dialog -->
    <AppModal v-model="promoteDialogOpen" title="恢复上线" size="sm">
      <div style="margin-bottom:16px">
        <label class="field-label">恢复原因</label>
        <input v-model="promoteReason" class="field-input" placeholder="请输入原因" />
      </div>
      <template #footer>
        <button class="btn btn-ghost" @click="promoteDialogOpen = false">取消</button>
        <button class="btn btn-success" @click="submitPromote">确认恢复</button>
      </template>
    </AppModal>

    <!-- Concurrency Dialog -->
    <AppModal v-model="concurrencyDialogOpen" title="手动调整并发自动值" size="sm">
      <div style="margin-bottom:16px">
        <label class="field-label">并发上限</label>
        <input v-model.number="concurrencyValue" type="number" min="1" class="field-input" />
      </div>
      <div style="margin-bottom:16px">
        <label class="field-label">调整原因</label>
        <input v-model="concurrencyReason" class="field-input" placeholder="请输入原因" />
      </div>
      <template #footer>
        <button class="btn btn-ghost" @click="concurrencyDialogOpen = false">取消</button>
        <button class="btn btn-primary" @click="submitConcurrency">确认</button>
      </template>
    </AppModal>

    <!-- 2026-06-23: per-model toggle dialog -->
    <AppModal v-model="toggleDialogOpen" :title="`确认${toggleTarget?.action === 'offline' ? '下线' : '上线'}`" size="sm">
      <div class="cell-sub" style="margin-bottom:12px">
        <code class="mono-sm">{{ toggleTarget?.rawModel }}</code> · 凭据 {{ selectedCred?.label || `#${toggleTarget?.credId}` }}
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
      <template #footer>
        <button class="btn btn-ghost" @click="toggleDialogOpen = false">取消</button>
        <button
          :class="toggleTarget?.action === 'offline' ? 'btn btn-danger' : 'btn btn-success'"
          :disabled="!toggleReason.trim()"
          @click="submitToggle"
        >确认{{ toggleTarget?.action === 'offline' ? '下线' : '上线' }}</button>
      </template>
    </AppModal>

    <!-- Clear manual_disabled dialog (2026-06-23) -->
    <AppModal v-model="clearDisabledDialogOpen" title="清除 manual_disabled" size="sm">
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
      <template #footer>
        <button class="btn btn-ghost" @click="clearDisabledDialogOpen = false">取消</button>
        <button
          class="btn btn-warning"
          :disabled="!clearDisabledReason.trim()"
          @click="submitClearDisabled"
        >确认清除</button>
      </template>
    </AppModal>

    <!-- Set manual_disabled dialog (2026-06-23) -->
    <AppModal v-model="setManualDisabledDialogOpen" :title="setManualDisabledTargetValue ? '禁用凭据' : '启用凭据'" size="sm">
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
      <template #footer>
        <button class="btn btn-ghost" @click="setManualDisabledDialogOpen = false">取消</button>
        <button
          :class="setManualDisabledTargetValue ? 'btn btn-danger' : 'btn btn-success'"
          :disabled="!setManualDisabledReason.trim()"
          @click="submitSetManualDisabled"
        >确认{{ setManualDisabledTargetValue ? '禁用' : '启用' }}</button>
      </template>
    </AppModal>
  </AppDrawer>
</template>

<style scoped>
/* 工具栏（原 drawer-header 的左标题/右控制布局迁移：标题进 AppDrawer 头部，
   供应商与控制项组成一行，保持 border-bottom 分隔视觉） */
.detail-toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding-bottom: 12px;
  margin-bottom: 8px;
  border-bottom: 1px solid var(--border);
}
.detail-toolbar-controls {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}

.detail-tabs-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding-bottom: 8px;
  margin-bottom: 8px;
  border-bottom: 1px solid var(--border);
}

.drawer-body {
  position: relative;
}

.drawer-body-loading {
  min-height: 320px;
}

.detail-loading-state {
  padding: 20px 16px 8px;
}

.detail-skeleton-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

.detail-skeleton-card {
  padding: 14px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--card);
}

.skeleton {
  border-radius: 999px;
  background: linear-gradient(90deg, var(--neutral-bg) 25%, rgba(139, 148, 158, 0.28) 50%, var(--neutral-bg) 75%);
  background-size: 200% 100%;
  animation: detail-skeleton-shimmer 1.2s ease-in-out infinite;
}

.skeleton-title {
  width: 36%;
  height: 12px;
  margin-bottom: 14px;
}

.skeleton-line {
  width: 100%;
  height: 10px;
  margin-bottom: 10px;
}

.skeleton-line.short {
  width: 68%;
  margin-bottom: 0;
}

.detail-loading-hint {
  margin-top: 14px;
  font-size: 12px;
  color: var(--muted);
}

.detail-refresh-mask {
  position: absolute;
  top: 10px;
  right: 16px;
  z-index: 2;
  pointer-events: none;
}

.detail-refresh-pill {
  padding: 5px 10px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: color-mix(in srgb, var(--card) 92%, var(--accent) 8%);
  color: var(--muted);
  font-size: 11px;
  box-shadow: 0 6px 18px var(--overlay-faint);
}

@keyframes detail-skeleton-shimmer {
  0% { background-position: 200% 0; }
  100% { background-position: -200% 0; }
}

/* Rate coloring（模型表用） */
.rate-cell { font-weight: 600; }
.rate-good { color: var(--success); }
.rate-warn { color: var(--warning); }
.rate-bad { color: var(--danger); }
.rate-none { color: var(--muted); }

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
  background: color-mix(in srgb, var(--kx-text) 4%, transparent);
}
.model-row-selected {
  background: color-mix(in srgb, var(--accent) 12%, transparent) !important;
}

/* 🆕 2026-06-23: declared 模型行置灰 (从未被路由实际调用) */
.model-row-declared {
  opacity: 0.55;
}
.model-row-declared:hover {
  opacity: 0.85;
}

/* 🆕 2026-06-23: 延迟 P95 色阶 (基于 ms 阈值) */
.p95-good { color: var(--success); font-weight: 600; }
.p95-warn { color: var(--warning); font-weight: 600; }
.p95-bad  { color: var(--danger); font-weight: 600; }

/* 🆕 2026-06-23: 数据来源 chip (live / declared) */
.source-chip {
  display: inline-block;
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 600;
  text-transform: lowercase;
  letter-spacing: 0.02em;
}
.source-live {
  background: var(--success-bg);
  color: var(--success);
}
.source-declared {
  background: var(--neutral-bg);
  color: var(--muted);
}

.mono-sm {
  font-family: 'SF Mono', Menlo, Consolas, monospace;
  font-size: 12px;
}

/* 🆕 2026-06-25: 模型名称右侧状态图标组 (详情页右列)
   - .model-status-icons: 横向排列,允许换行
   - .status-icon-btn: 手工控制可点击按钮 (3 态)
   - .status-icon-info: 只读 badge (总状态来自 StatusBadge) */
.model-status-icons {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  margin-left: 4px;
}
.status-icon-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px;
  border-radius: 99px;
  font-size: 11px;
  font-weight: 600;
  line-height: 16px;
  border: 1px solid;
  cursor: pointer;
  transition: filter 0.15s ease, transform 0.05s ease;
  background: transparent;
  user-select: none;
}
.status-icon-btn:hover:not(:disabled) {
  filter: brightness(1.15);
}
.status-icon-btn:active:not(:disabled) {
  transform: scale(0.97);
}
.status-icon-btn:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.status-icon-emoji {
  font-size: 11px;
  line-height: 1;
}
.status-icon-label {
  font-size: 11px;
  letter-spacing: 0.02em;
}
.status-icon-info {
  display: inline-flex;
  align-items: center;
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
.src-redis { background: var(--success-bg); color: var(--success); }
.src-rl { background: color-mix(in srgb, var(--accent) 15%, transparent); color: var(--accent-h); }

.cell-sub { font-size: 11px; color: var(--muted); }
.cell-muted { color: var(--muted); }

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
  background: color-mix(in srgb, var(--kx-text) 4%, transparent) !important;
}

/* ════════════════════════════════════════════════════════════════════════
   🆕 2026-06-24: models + monitoring 合并 tab 的三态布局 CSS
   - .models-grid-split       左右连动 (默认)
   - .models-grid-list-full   左 8 列全宽
   - .models-grid-monitor-full 右 3 section 全宽
   - .pane-anim               切换 200ms 微动画
   - .pane-collapsed          折叠后窄条样式
   ════════════════════════════════════════════════════════════════════════ */

/* models tab 外层 */
.models-tab {
  display: grid;
  gap: 10px;
  /* 默认 split 模板,具体模板由 modifier class 覆盖 */
  grid-template-columns: minmax(280px, 320px) 1fr;
  grid-template-areas:
    "toolbar  toolbar"
    "left     right";
}

/* 工具栏 (跨两列) */
.models-tab > .models-toolbar {
  grid-area: toolbar;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 6px 8px;
  border-bottom: 1px solid var(--border);
  flex-wrap: wrap;
}
.models-toolbar-left {
  display: flex;
  align-items: baseline;
  gap: 10px;
  min-width: 0;
  flex: 1;
}
.models-toolbar-right {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}
.models-title {
  margin: 0;
  padding: 0;
  border: 0;
  font-size: 13px;
}

/* 三态分段控件 */
.layout-picker {
  display: inline-flex;
  border: 1px solid var(--border);
  border-radius: 6px;
  overflow: hidden;
  background: var(--bg-subtle);
}
.layout-btn {
  background: transparent;
  border: 0;
  padding: 4px 10px;
  font-size: 12px;
  color: var(--muted);
  cursor: pointer;
  border-right: 1px solid var(--border);
  transition: background 120ms ease-out, color 120ms ease-out;
  font-family: inherit;
  line-height: 1.4;
}
.layout-btn:last-child { border-right: 0; }
.layout-btn:hover { background: color-mix(in srgb, var(--kx-text) 4%, transparent); color: var(--text); }
.layout-btn.active {
  background: color-mix(in srgb, var(--accent) 18%, transparent);
  color: var(--accent-h);
  font-weight: 600;
}

/* 折叠按钮 (在右列顶部) */
.fold-btn {
  width: 26px;
  height: 24px;
  padding: 0;
  font-size: 13px;
  line-height: 1;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}

/* 左右两列容器 */
.pane {
  min-width: 0;
  min-height: 0;
}
.pane-left  { grid-area: left;  }
.pane-right { grid-area: right; }

/* 三态 grid 模板 */
.models-grid-split {
  grid-template-columns: minmax(280px, 320px) 1fr;
  grid-template-areas:
    "toolbar  toolbar"
    "left     right";
}
.models-grid-list-full {
  grid-template-columns: 1fr 80px;
  grid-template-areas:
    "toolbar  toolbar"
    "left     right";
}
.models-grid-monitor-full {
  grid-template-columns: 200px 1fr;
  grid-template-areas:
    "toolbar  toolbar"
    "left     right";
}

/* 折叠窄条 */
.pane-collapsed {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 16px 8px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  min-height: 120px;
  text-align: center;
}
.pane-collapsed-label {
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--muted);
}
.pane-collapsed-value {
  word-break: break-all;
  font-size: 11px;
  max-width: 100%;
}

/* 右列监控 stack */
.monitor-stack {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

/* 滑动窗口条带 (从原 inline 样式抽出) */
.window-strip {
  display: flex;
  gap: 3px;
  overflow-x: auto;
  padding: 8px 0;
}
.window-cell {
  width: 5px;
  height: 40px;
  border-radius: 1px;
  flex-shrink: 0;
  transition: opacity 120ms ease-out;
}
.window-cell:hover { opacity: 1; }
.window-stats {
  display: flex;
  gap: 16px;
  margin-top: 8px;
  font-size: 12px;
  flex-wrap: wrap;
}
.window-stats b { font-weight: 700; }

/* 错误分布饼图容器 (抽出原 inline 200px) */
.pie-wrap {
  height: 220px;
  position: relative;
}
.models-grid-monitor-full .pie-wrap {
  height: 280px;
}

/* ═══ 切换动画 (200ms 渐入) ═══ */
.pane-anim .pane-left,
.pane-anim .pane-right {
  animation: pane-fade-in 200ms ease-out;
}
@keyframes pane-fade-in {
  0%   { opacity: 0.65; }
  100% { opacity: 1.0; }
}
@media (prefers-reduced-motion: reduce) {
  .pane-anim .pane-left,
  .pane-anim .pane-right { animation: none; }
  .skeleton { animation: none; }
}

/* ═══ 响应式: 极窄屏强制 list-full (老板视觉验收点；2026-09-13 P3 收敛 700→640) ═══ */
@media (max-width: 640px) {
  .detail-skeleton-grid {
    grid-template-columns: 1fr;
  }

  .models-grid-split,
  .models-grid-monitor-full {
    grid-template-columns: 1fr;
    grid-template-areas:
      "toolbar"
      "left"
      "right";
  }
  .pane-collapsed { display: none; }
}

/* 右列内 section 间距 (避免与监控内的 gap 重复) */
.models-tab .drawer-section {
  margin-bottom: 0;
}
</style>
