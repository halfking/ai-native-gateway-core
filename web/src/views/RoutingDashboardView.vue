<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRoute } from 'vue-router'
import {
  getAutoRouteIndex, getAutoRouteDecisions, getAutoRouteAudit,
  getCustomerCost, getModelCost, refreshAutoRouteIndex, simulateAutoRoute,
  getAnalyticsMatrix, getAnalyticsFlow, getAnalyticsFunnel, getDecisionReplay,
  DEFAULT_PROFILE_WEIGHTS, TASK_TYPES, TASK_TAGS,
  SPECIFIED_MODEL_TASK_KEY, SPECIFIED_MODEL_DISPLAY_LABEL,
  type AutoRouteIndexEntry, type AutoRouteDecision, type AutoRouteAudit,
  type CustomerCostRow, type ModelCostRow, type ProfileWeights,
  type AnalyticsMatrix, type AnalyticsFlow, type AnalyticsMetric, type AnalyticsWindow,
  type AnalyticsRowDim, type AnalyticsFunnelStage, type DecisionReplayResponse,
} from '../api-autoroute'
import { getWorkTypeStats, type WorkTypeSyncMeta } from '../api-work-types'
import {
  getPolicy, patchPolicy, getScoringWeights, updateScoringWeights,
  resolveRouting,
  type RoutingPolicy, type ScoringWeights, type RoutingResolveResponse,
  type RoutingCandidate,
} from '../api'
import SixDimScoreBar from '../components/SixDimScoreBar.vue'
import ModelPicker from '../components/ModelPicker.vue'
import AnalyticsKpiBar from '../components/analytics/AnalyticsKpiBar.vue'
import HeatmapMatrix from '../components/analytics/HeatmapMatrix.vue'
import RouteFlowSankey from '../components/analytics/RouteFlowSankey.vue'
import { computeSankeyCardHeight, SANKEY_DOM_LEGEND_H, SANKEY_SECTION_HEAD_H } from '../components/analytics/sankeyLayout'
import ModelTaskIndexPanel from '../components/analytics/ModelTaskIndexPanel.vue'
import DecisionDetail from '../components/analytics/DecisionDetail.vue'
import CredentialFunnel from '../components/analytics/CredentialFunnel.vue'

const RESOLVE_LOG_KEY = 'llmgw_resolve_log'
const RESOLVE_LOG_MAX = 50

interface ResolveLogEntry {
  ts: string
  model: string
  profile: string
  path: string
  routable: number
  total: number
  top_cred: number | null
}

const route = useRoute()
const activeTab = ref<'analytics' | 'overview' | 'policy' | 'live' | 'resolve'>('analytics')

/** Map the synthetic __specified__ task key to its display label. */
function displayTaskKey(key: string): string {
  return key === SPECIFIED_MODEL_TASK_KEY ? SPECIFIED_MODEL_DISPLAY_LABEL : key
}

function tabFromQuery(q: unknown): typeof activeTab.value | null {
  if (q === 'analytics' || q === 'resolve' || q === 'overview' || q === 'policy' || q === 'live') return q
  return null
}

// ── Overview ──────────────────────────────────────────
const indexData = ref<AutoRouteIndexEntry[]>([])
const selectedTask = ref<string>('')
const selectedProfile = ref<'smart' | 'speed_first' | 'cost_first'>('smart')
const expandedModel = ref<string>('')
const indexLoading = ref(false)
const layer2Cache = ref<Record<string, RoutingResolveResponse | null>>({})
const layer2Loading = ref<string>('')

const audit = ref<AutoRouteAudit>({
  total_auto_requests: 0, success_rate: 0,
  task_distribution: {}, profile_distribution: {}, top_chosen_models: [],
})

// ── Analytics (Phase 2a) ─────────────────────────────
const analyticsWindow = ref<AnalyticsWindow>('7d')
const analyticsMetric = ref<AnalyticsMetric>('count')
const analyticsRowDim = ref<AnalyticsRowDim>('task_type')
const matrixData = ref<AnalyticsMatrix | null>(null)
const flowData = ref<AnalyticsFlow | null>(null)
const analyticsLoading = ref(false)

/** card-toolbar chrome (padding + border) above heatmap body. */
const HEATMAP_CARD_CHROME = 42

const heatmapContentHeight = computed(() => {
  const rows = matrixData.value?.rows?.length || 0
  return Math.max(rows ? (rows + 1) * 22 + 40 : 200, 400)
})

const sankeyCardHeight = computed(() =>
  Math.max(computeSankeyCardHeight(flowData.value), 400),
)

const heatmapCardHeight = computed(() => heatmapContentHeight.value + HEATMAP_CARD_CHROME)

const heatmapBodyMinHeight = computed(() =>
  Math.max(0, heatmapContentHeight.value),
)

const sankeySvgMinHeight = computed(() =>
  Math.max(0, sankeyCardHeight.value - SANKEY_SECTION_HEAD_H - SANKEY_DOM_LEGEND_H),
)

const cellDecisions = ref<AutoRouteDecision[]>([])
const cellModalOpen = ref(false)
const cellPopup = ref<{ row: string; col: string; value: number } | null>(null)
const cellLoading = ref(false)
const selectedHeatmapTask = ref('')
const selectedHeatmapModel = ref('')
const showModelTaskIndex = ref(true)
const funnelStages = ref<AnalyticsFunnelStage[]>([])
const funnelLoading = ref(false)
const funnelApproximate = ref(false)
const funnelDataSource = ref<'exact' | 'approximate' | 'mixed' | undefined>()
const funnelSampleN = ref(0)
const funnelConfidence = ref<'high' | 'medium' | 'low' | undefined>()
const funnelConfidenceHint = ref('')
const wtSyncMeta = ref<WorkTypeSyncMeta | null>(null)
const decisionReplayCache = ref<Record<string, DecisionReplayResponse | null>>({})
const decisionReplayLoading = ref('')
const modalDecisionId = ref('')

/**
 * analyticsEmpty drives the "no data at all" full-page empty state.
 *
 * Pre-2026-06-26 this check used three conditions ANDed together, which
 * caused a visible flash-and-disappear bug:
 *   - On initial mount `audit.total_requests` is undefined → `?? 0`
 *     evaluates to 0, so the empty state showed immediately.
 *   - When `loadAnalytics()` set `analyticsLoading = true`, the empty
 *     state hid and the heatmap card briefly appeared with "加载热力图…".
 *   - When the matrix query returned 0 rows (no recent request_logs),
 *     `analyticsEmpty` flipped back to true and the empty state replaced
 *     the heatmap card again — producing the "闪一下就消失了" flicker.
 *
 * Fix: only show the full-page empty state when BOTH the audit and the
 * initial matrix fetch have completed AND neither has any data. This
 * keeps the heatmap card on screen throughout the load lifecycle so the
 * user sees a stable layout. The heatmap card itself renders an inline
 * "暂无矩阵数据" hint via HeatmapMatrix's isEmpty computed, so the user
 * still gets a clear "no data" signal — just without the page flicker.
 */
const analyticsFetched = ref(false)
const auditFetched = ref(false)

const analyticsEmpty = computed(() =>
  auditFetched.value &&
  analyticsFetched.value &&
  (audit.value.total_requests ?? audit.value.total_auto_requests ?? 0) === 0 &&
  (!matrixData.value || matrixData.value.rows.length === 0)
)

async function loadAnalytics() {
  analyticsLoading.value = true
  try {
    const [matrix, flow] = await Promise.all([
      getAnalyticsMatrix(analyticsWindow.value, analyticsMetric.value, analyticsRowDim.value),
      getAnalyticsFlow(analyticsWindow.value),
    ])
    matrixData.value = matrix
    flowData.value = flow
  } catch (e) {
    console.error('loadAnalytics', e)
    // Preserve previous data on error so the user does not see the
    // heatmap flash to empty and back. The card's inline "暂无矩阵数据"
    // hint will be replaced by a clearer error message below.
  } finally {
    analyticsLoading.value = false
    analyticsFetched.value = true
  }
}

async function onMatrixCellClick(row: string, col: string, value: number) {
  // 2026-06-22 axis swap: row is the model, col is the task (or
  // work_type). The cell popup stores them as (model, task) so the
  // modal title and downstream filters map cleanly to the user's
  // mental model: "what did model X do on task Y?".
  cellPopup.value = { row, col, value }
  selectedHeatmapModel.value = row
  selectedHeatmapTask.value = analyticsRowDim.value === 'task_type' ? col : ''
  cellModalOpen.value = true
  cellLoading.value = true
  cellDecisions.value = []
  modalDecisionId.value = ''
  funnelStages.value = []
  funnelDataSource.value = undefined
  funnelSampleN.value = 0
  funnelConfidence.value = undefined
  funnelConfidenceHint.value = ''
  try {
    const isSpecified = col === SPECIFIED_MODEL_TASK_KEY
    // For the synthetic __specified__ column, request_logs.task_type
    // is NULL — we cannot pass a task filter; the model row alone
    // narrows the result set for the decisions modal.
    const taskArg = isSpecified ? undefined : (analyticsRowDim.value === 'task_type' ? col : undefined)
    const workTypeArg = analyticsRowDim.value === 'work_type' ? col : undefined
    cellDecisions.value = await getAutoRouteDecisions(
      10,
      taskArg,
      undefined,
      row,
      workTypeArg,
    )
    // Funnel is L2 credential-level routing data, which is shared
    // between auto and specified-model requests; load it for both.
    await loadFunnel(row)
  } catch (e) {
    console.error('onMatrixCellClick', e)
  } finally {
    cellLoading.value = false
  }
}

async function loadFunnel(model: string) {
  if (!model) {
    funnelStages.value = []
    return
  }
  funnelLoading.value = true
  try {
    const res = await getAnalyticsFunnel(model, analyticsWindow.value)
    funnelStages.value = res.stages
    funnelApproximate.value = res.meta?.approximate ?? false
    funnelDataSource.value = res.meta?.data_source
    funnelSampleN.value = res.meta?.sample_n ?? res.requests ?? 0
    funnelConfidence.value = res.meta?.confidence
    funnelConfidenceHint.value = res.meta?.confidence_hint ?? ''
  } catch (e) {
    console.error('loadFunnel', e)
    funnelStages.value = []
  } finally {
    funnelLoading.value = false
  }
}

async function loadDecisionReplay(requestId: string) {
  if (!requestId || decisionReplayCache.value[requestId] !== undefined) return
  decisionReplayLoading.value = requestId
  try {
    decisionReplayCache.value[requestId] = await getDecisionReplay(requestId)
  } catch {
    decisionReplayCache.value[requestId] = null
  } finally {
    decisionReplayLoading.value = ''
  }
}

function onExpandDecision(requestId: string) {
  expandedDecision.value = expandedDecision.value === requestId ? '' : requestId
  if (expandedDecision.value) loadDecisionReplay(requestId)
}

function openDecisionModal(requestId: string) {
  modalDecisionId.value = requestId
  loadDecisionReplay(requestId)
}

function closeCellModal() {
  cellModalOpen.value = false
  cellPopup.value = null
  cellDecisions.value = []
  modalDecisionId.value = ''
  selectedHeatmapTask.value = ''
  selectedHeatmapModel.value = ''
  funnelStages.value = []
}

watch([analyticsWindow, analyticsMetric, analyticsRowDim], () => {
  if (activeTab.value === 'analytics') loadAnalytics()
})

const profileScoreKey = computed(() => {
  if (selectedProfile.value === 'speed_first') return 'score_speed_first' as const
  if (selectedProfile.value === 'cost_first') return 'score_cost_first' as const
  return 'score_smart' as const
})

const profileLabel = computed(() => {
  if (selectedProfile.value === 'speed_first') return 'Speed'
  if (selectedProfile.value === 'cost_first') return 'Cost'
  return 'Smart'
})

const sortedIndex = computed(() => {
  const key = profileScoreKey.value
  return [...indexData.value].sort((a, b) => (b[key] ?? 0) - (a[key] ?? 0))
})

async function loadIndex() {
  indexLoading.value = true
  try { indexData.value = await getAutoRouteIndex(30) }
  catch (e) { console.error('loadIndex', e) }
  finally { indexLoading.value = false }
}

async function loadLayer2(modelName: string) {
  const key = modelName
  if (layer2Cache.value[key] !== undefined) return
  layer2Loading.value = key
  try {
    layer2Cache.value[key] = await resolveRouting(modelName)
  } catch {
    layer2Cache.value[key] = null
  } finally {
    layer2Loading.value = ''
  }
}

function toggleModel(m: AutoRouteIndexEntry) {
  const id = m.credential_id + ':' + m.raw_model
  if (expandedModel.value === id) {
    expandedModel.value = ''
    return
  }
  expandedModel.value = id
  loadLayer2(m.canonical_name || m.raw_model)
}

function scoreOf(m: AutoRouteIndexEntry): number {
  return m[profileScoreKey.value] ?? 0
}

// ── Policy ────────────────────────────────────────────
const policy = ref<RoutingPolicy | null>(null)
const policyDraft = ref<Partial<RoutingPolicy>>({})
const weights = ref<ScoringWeights>({ price: 10, session_load: 5, failure_penalty: 20, default_price_cny: 5, default_price_usd: 5 })
const weightsDraft = ref<ScoringWeights>({ ...weights.value })
const savingPolicy = ref(false)
const policyMsg = ref('')

const POLICY_FIELDS: { key: keyof RoutingPolicy; label: string; min?: number; max?: number; step?: number }[] = [
  { key: 'algorithm_version',         label: '算法版本', min: 1, max: 2, step: 1 },
  { key: 'retry_per_credential',      label: '同凭据重试', min: 0, max: 5, step: 1 },
  { key: 'tier_fallback_max',         label: '跨级回退', min: 1, max: 20, step: 1 },
  { key: 'circuit_open_seconds',      label: '熔断冷却(s)', min: 1, max: 3600, step: 1 },
  { key: 'circuit_failure_threshold', label: '熔断失败次数', min: 1, max: 50, step: 1 },
  { key: 'circuit_max_open_seconds',  label: '熔断上限(s)', min: 1, max: 86400, step: 1 },
]

async function loadPolicy() {
  try {
    policy.value = await getPolicy()
    policyDraft.value = { ...policy.value }
    weights.value = await getScoringWeights()
    weightsDraft.value = { ...weights.value }
  } catch (e) { console.error('loadPolicy', e) }
}

async function savePolicy() {
  savingPolicy.value = true
  policyMsg.value = ''
  try {
    const dirty: Partial<RoutingPolicy> = {}
    for (const f of POLICY_FIELDS) {
      if (policyDraft.value[f.key] !== policy.value![f.key]) (dirty as Record<string, unknown>)[f.key] = policyDraft.value[f.key]
    }
    if (Object.keys(dirty).length > 0) {
      policy.value = await patchPolicy(dirty)
      policyDraft.value = { ...policy.value }
    }
    policyMsg.value = '策略已保存'
  } catch (e) { policyMsg.value = '保存失败: ' + String(e) }
  finally { savingPolicy.value = false }
}

async function saveWeights() {
  savingPolicy.value = true
  policyMsg.value = ''
  try {
    await updateScoringWeights(weightsDraft.value)
    weights.value = { ...weightsDraft.value }
    policyMsg.value = '权重已保存'
  } catch (e) { policyMsg.value = '保存失败: ' + String(e) }
  finally { savingPolicy.value = false }
}

const customerCost = ref<CustomerCostRow[]>([])
const modelCost = ref<ModelCostRow[]>([])

async function loadCosts() {
  try {
    customerCost.value = await getCustomerCost(10)
    modelCost.value = await getModelCost(10)
  } catch (e) { console.error('loadCosts', e) }
}

// ── Live ──────────────────────────────────────────────
const decisions = ref<AutoRouteDecision[]>([])
const expandedDecision = ref<string>('')
const autoRefresh = ref(true)
let pollTimer: ReturnType<typeof setInterval> | null = null

const simPrompt = ref('用 Python 写一个快速排序')
const simProfile = ref('smart')
const simResult = ref<{ status: number; decision?: Record<string, unknown>; error?: string } | null>(null)
const simLoading = ref(false)

async function loadAudit() {
  try {
    audit.value = await getAutoRouteAudit()
  } catch (e) {
    console.error('loadAudit', e)
  } finally {
    auditFetched.value = true
  }
}
async function loadDecisions() {
  try { decisions.value = await getAutoRouteDecisions(15) } catch (e) { console.error('loadDecisions', e) }
}

async function runSim() {
  simLoading.value = true
  simResult.value = null
  try { simResult.value = await simulateAutoRoute(simPrompt.value, simProfile.value) }
  finally { simLoading.value = false }
}

function startPoll() {
  stopPoll()
  pollTimer = setInterval(() => { loadAudit(); loadDecisions() }, 5000)
}
function stopPoll() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null }
}

// ── Resolve (凭据路由) ─────────────────────────────────
const modelInput = ref('')
const clientProfile = ref('')
const resolution = ref<RoutingResolveResponse | null>(null)
const resolveCandidates = ref<RoutingCandidate[]>([])
const resolving = ref(false)
const resolveErr = ref('')
const resolved = ref(false)
const showUnavailable = ref(false)
const resolveLog = ref<ResolveLogEntry[]>([])

const resolveFunnelStages = computed<AnalyticsFunnelStage[]>(() => {
  if (!resolveCandidates.value.length) return []
  const total = resolveCandidates.value.length
  const routable = resolveCandidates.value.filter(c => c.routable).length
  return [
    { key: 'candidates', label: '总候选', value: total, hint: '本次解析候选数' },
    { key: 'routable', label: '可路由', value: routable, hint: '通过可用性检查' },
    { key: 'success', label: '首选凭据', value: routable > 0 ? 1 : 0, hint: 'Top 可路由凭据' },
  ]
})

const filteredResolveCandidates = computed(() =>
  showUnavailable.value ? resolveCandidates.value : resolveCandidates.value.filter(c => c.routable)
)
const resolveUnavailableCount = computed(() =>
  resolveCandidates.value.filter(c => !c.routable).length
)

function loadResolveLog() {
  try {
    const raw = localStorage.getItem(RESOLVE_LOG_KEY)
    resolveLog.value = raw ? JSON.parse(raw) : []
  } catch {
    resolveLog.value = []
  }
}

function saveResolveLog() {
  localStorage.setItem(RESOLVE_LOG_KEY, JSON.stringify(resolveLog.value.slice(0, RESOLVE_LOG_MAX)))
}

function appendResolveLog(res: RoutingResolveResponse, profile: string) {
  const routable = res.candidates.filter(c => c.routable).length
  const top = res.candidates.find(c => c.routable)
  resolveLog.value.unshift({
    ts: new Date().toISOString(),
    model: res.client_model,
    profile,
    path: res.resolution_path,
    routable,
    total: res.candidates.length,
    top_cred: top?.credential_id ?? null,
  })
  resolveLog.value = resolveLog.value.slice(0, RESOLVE_LOG_MAX)
  saveResolveLog()
}

function clearResolveLog() {
  resolveLog.value = []
  localStorage.removeItem(RESOLVE_LOG_KEY)
}

async function onModelPicked(value: string | string[]) {
  const name = typeof value === 'string' ? value.trim() : ''
  if (!name) return
  modelInput.value = name
  await doResolve()
}

async function doResolve() {
  if (!modelInput.value.trim()) return
  resolving.value = true
  resolveErr.value = ''
  try {
    const profile = clientProfile.value.trim()
    const res = await resolveRouting(modelInput.value.trim(), profile || undefined, true)
    resolution.value = res
    resolveCandidates.value = res.candidates
    resolved.value = true
    appendResolveLog(res, profile)
  } catch (e: unknown) {
    resolveErr.value = e instanceof Error ? e.message : '查询失败'
  } finally {
    resolving.value = false
  }
}

function replayFromLog(entry: ResolveLogEntry) {
  modelInput.value = entry.model
  clientProfile.value = entry.profile
  doResolve()
}

watch(autoRefresh, (v) => { v ? startPoll() : stopPoll() })
watch(activeTab, (tab) => {
  if (tab === 'analytics') { loadAudit(); loadAnalytics() }
  if (tab === 'live') { loadAudit(); loadDecisions(); if (autoRefresh.value) startPoll() }
  else stopPoll()
  if (tab === 'policy') { loadPolicy(); loadCosts() }
  if (tab === 'resolve') loadResolveLog()
})
watch(() => route.query.tab, (q) => {
  const t = tabFromQuery(q)
  if (t) activeTab.value = t
}, { immediate: true })

// ── Helpers ───────────────────────────────────────────
function fmt(n: number | undefined, digits = 2): string {
  if (n === undefined || n === null || isNaN(n)) return '-'
  return n.toFixed(digits)
}
function fmtMs(ms: number | undefined): string {
  if (!ms || ms <= 0) return '-'
  return ms < 1000 ? `${Math.round(ms)}ms` : `${(ms / 1000).toFixed(1)}s`
}
function fmtCost(n: number | undefined): string {
  if (!n || n <= 0) return '$0'
  return '$' + n.toFixed(4)
}
function maxDimValue(w: ProfileWeights): number {
  return Math.max(w.Price, w.Speed, w.Stability, w.Match, w.Pressure, w.ContextFit, 1)
}
function distEntries(d: Record<string, number>): Array<[string, number]> {
  return Object.entries(d).sort((a, b) => b[1] - a[1])
}
function distMax(d: Record<string, number>): number {
  return Math.max(...Object.values(d), 1)
}
function taskLabel(key: string): string {
  return TASK_TYPES.find(t => t.key === key)?.label ?? key
}

const L1_STEPS = ['Prompt', '8类分类', '6维评分', 'Profile', '选模型']
const L2_STEPS = ['模型解析', 'Tier回退', '计费轮次', 'P2C得分', '执行/熔断']

const heroChips = computed(() => {
  if (activeTab.value === 'analytics') {
    const topTask = distEntries(audit.value.task_distribution)[0]
    const topModel = audit.value.top_chosen_models[0]
    const chips = [
      { label: 'Auto', value: String(audit.value.total_auto_requests) },
      { label: '成功率', value: fmt(audit.value.success_rate * 100, 1) + '%' },
      { label: 'Top任务', value: topTask?.[0] || '-' },
      { label: 'Top模型', value: topModel?.model || '-' },
    ]
    if (wtSyncMeta.value) {
      chips.push({ label: '工作类型', value: String(wtSyncMeta.value.enabled_count) })
      chips.push({ label: '映射', value: String(wtSyncMeta.value.route_count) })
      if (wtSyncMeta.value.last_synced_at) {
        const d = new Date(wtSyncMeta.value.last_synced_at)
        chips.push({ label: 'ACC同步', value: d.toLocaleString() })
      }
    }
    return chips
  }
  if (activeTab.value === 'overview') {
    return [
      { label: '候选', value: String(indexData.value.length) },
      { label: '24h', value: String(audit.value.total_auto_requests) },
      { label: '成功率', value: fmt(audit.value.success_rate * 100, 1) + '%' },
    ]
  }
  if (activeTab.value === 'live') {
    const topTask = distEntries(audit.value.task_distribution)[0]
    const topModel = audit.value.top_chosen_models[0]
    return [
      { label: 'Auto', value: String(audit.value.total_auto_requests) },
      { label: 'Top任务', value: topTask?.[0] || '-' },
      { label: 'Top模型', value: topModel?.model || '-' },
    ]
  }
  if (activeTab.value === 'resolve') {
    const routable = resolveCandidates.value.filter(c => c.routable).length
    return [
      { label: '可路由', value: resolved.value ? String(routable) : '-' },
      { label: '候选', value: resolved.value ? String(resolveCandidates.value.length) : '-' },
      { label: '记录', value: String(resolveLog.value.length) },
    ]
  }
  return [
    { label: '算法', value: 'v' + (policy.value?.algorithm_version ?? '-') },
    { label: 'Tier回退', value: String(policy.value?.tier_fallback_max ?? '-') },
    { label: '熔断', value: (policy.value?.circuit_failure_threshold ?? '-') + '次' },
  ]
})

onMounted(async () => {
  const t = tabFromQuery(route.query.tab)
  if (t) activeTab.value = t
  const rowQ = route.query.row
  if (rowQ === 'work_type' || rowQ === 'task_type') analyticsRowDim.value = rowQ
  if (route.query.filter && typeof route.query.filter === 'string') {
    analyticsRowDim.value = 'work_type'
  }
  await loadIndex()
  await loadAudit()
  try {
    const wt = await getWorkTypeStats()
    wtSyncMeta.value = wt.sync_meta ?? null
  } catch { /* non-blocking */ }
  if (activeTab.value === 'analytics') await loadAnalytics()
  if (activeTab.value === 'resolve') loadResolveLog()
})
onUnmounted(() => stopPoll())
</script>

<template>
  <div class="routing-dashboard">
    <!-- Unified top: title + tabs + refresh -->
    <div class="top-bar">
      <div class="top-bar-head">
        <h2>路由全景</h2>
        <div class="seg-tabs">
          <button class="seg-tab" :class="{ active: activeTab === 'analytics' }" @click="activeTab = 'analytics'">数据分析</button>
          <button class="seg-tab" :class="{ active: activeTab === 'overview' }" @click="activeTab = 'overview'">两层路由</button>
          <button class="seg-tab" :class="{ active: activeTab === 'policy' }" @click="activeTab = 'policy'">策略配置</button>
          <button class="seg-tab" :class="{ active: activeTab === 'live' }" @click="activeTab = 'live'">实时决策</button>
          <button class="seg-tab" :class="{ active: activeTab === 'resolve' }" @click="activeTab = 'resolve'">凭据路由</button>
        </div>
        <div class="nav-chips">
          <router-link to="/routing-v2/work-types" class="nav-link-wt chip-link">
            工作类型
            <template v-if="wtSyncMeta">
              <span class="chip-inline">{{ wtSyncMeta.enabled_count }} 启用</span>
              <span class="chip-inline">{{ wtSyncMeta.route_count }} 映射</span>
            </template>
          </router-link>
          <router-link to="/routing-overview" class="nav-link-wt chip-link">
            模型路由全景
          </router-link>
          <router-link to="/routing-policy" class="nav-link-wt chip-link">
            路由策略
          </router-link>
        </div>
        <button class="btn btn-sm btn-ghost refresh-btn" @click="loadIndex(); loadAudit(); activeTab === 'analytics' && loadAnalytics(); activeTab === 'policy' && loadPolicy()" title="刷新">↻</button>
      </div>

      <!-- L1 / L2 pipeline -->
      <div class="pipeline">
        <div class="pipe-row l1-row">
          <span class="layer-tag l1">L1</span>
          <span class="pipe-title">选模型</span>
          <div class="pipe-steps">
            <template v-for="(s, i) in L1_STEPS" :key="'l1-' + s">
              <span class="pipe-step">{{ s }}</span>
              <span v-if="i < L1_STEPS.length - 1" class="pipe-dot">›</span>
            </template>
          </div>
        </div>
        <div class="pipe-bridge">↓</div>
        <div class="pipe-row l2-row">
          <span class="layer-tag l2">L2</span>
          <span class="pipe-title">选凭据</span>
          <div class="pipe-steps">
            <template v-for="(s, i) in L2_STEPS" :key="'l2-' + s">
              <span class="pipe-step">{{ s }}</span>
              <span v-if="i < L2_STEPS.length - 1" class="pipe-dot">›</span>
            </template>
          </div>
        </div>
      </div>

      <div class="hero-stats">
        <span v-for="c in heroChips" :key="c.label" class="chip">{{ c.label }} <strong>{{ c.value }}</strong></span>
      </div>
    </div>

    <!-- ═══ Tab: Analytics ═══ -->
    <div v-if="activeTab === 'analytics'" class="tab-content">
      <p class="analytics-hint text-muted">{{ analyticsRowDim === 'work_type' ? '工作类型' : '任务' }}×模型匹配统计 · 点击单元格查看决策明细与 L2 漏斗</p>
      <div v-if="!analyticsEmpty" class="card compact-card flat-card">
        <AnalyticsKpiBar :audit="audit" />
      </div>
      <div class="analytics-charts">
        <div class="card compact-card chart-card" :style="{ minHeight: heatmapCardHeight + 'px' }">
          <div class="card-toolbar">
            <div class="toolbar-left">
              <span class="toolbar-title">{{ analyticsRowDim === 'work_type' ? '工作类型' : '任务' }} × 模型热力图</span>
            </div>
            <div class="toolbar-filters">
              <button
                v-for="rd in (['task_type', 'work_type'] as AnalyticsRowDim[])"
                :key="rd"
                class="profile-pill"
                :class="{ active: analyticsRowDim === rd }"
                @click="analyticsRowDim = rd"
              >{{ rd === 'task_type' ? 'L1任务' : '工作类型' }}</button>
              <span class="toolbar-divider" />
              <button
                v-for="w in (['7d', '24h'] as AnalyticsWindow[])"
                :key="w"
                class="profile-pill"
                :class="{ active: analyticsWindow === w }"
                @click="analyticsWindow = w"
              >{{ w }}</button>
              <span class="toolbar-divider" />
              <button
                v-for="m in (['count', 'success_rate', 'p95_ms', 'cost_usd'] as AnalyticsMetric[])"
                :key="m"
                class="profile-pill"
                :class="{ active: analyticsMetric === m }"
                @click="analyticsMetric = m"
              >{{ m === 'count' ? '请求' : m === 'success_rate' ? '成功率' : m === 'p95_ms' ? 'P95' : '费用' }}</button>
            </div>
          </div>
          <HeatmapMatrix
            :data="matrixData"
            :metric="analyticsMetric"
            :row-aliases="matrixData?.meta?.row_aliases"
            :loading="analyticsLoading"
            :min-height="heatmapBodyMinHeight"
            @cell-click="onMatrixCellClick"
          />
        </div>
        <div class="card compact-card chart-card" :style="{ minHeight: sankeyCardHeight + 'px' }">
          <div class="section-head tight"><h3>路由流向</h3><span class="text-muted">任务 → 模型 → 供应商</span></div>
          <RouteFlowSankey :data="flowData" :loading="analyticsLoading" :min-height="sankeySvgMinHeight" />
        </div>
      </div>

      <div v-if="selectedHeatmapTask" class="card compact-card collapsible">
        <div class="card-toolbar clickable" @click="showModelTaskIndex = !showModelTaskIndex">
          <div class="toolbar-left">
            <span class="toolbar-title">模型任务指数</span>
            <span class="text-muted">{{ selectedHeatmapTask }}</span>
          </div>
          <span class="expand-icon">{{ showModelTaskIndex ? '▼' : '▶' }}</span>
        </div>
        <ModelTaskIndexPanel v-if="showModelTaskIndex" :task-type="selectedHeatmapTask" :top="10" />
      </div>

      <div v-if="selectedHeatmapModel" class="card compact-card">
        <div class="section-head tight">
          <h3>L2 凭据漏斗</h3>
          <span class="text-muted">{{ selectedHeatmapModel }} · {{ analyticsWindow }}</span>
        </div>
        <CredentialFunnel
          :stages="funnelStages"
          :model="selectedHeatmapModel"
          :approximate="funnelApproximate"
          :data-source="funnelDataSource"
          :sample-n="funnelSampleN"
          :confidence="funnelConfidence"
          :confidence-hint="funnelConfidenceHint"
          :loading="funnelLoading"
        />
      </div>

      <div v-if="cellModalOpen && cellPopup" class="modal-overlay" @click.self="closeCellModal">
        <div class="modal-panel card compact-card">
          <div class="card-toolbar">
            <div class="toolbar-left">
              <span class="toolbar-title">{{ cellPopup.row }} × {{ displayTaskKey(cellPopup.col) }}</span>
              <span class="text-muted">最近决策</span>
            </div>
            <button class="btn btn-ghost btn-sm" @click="closeCellModal">关闭</button>
          </div>
          <div v-if="cellLoading" class="loading-hint">加载…</div>
          <template v-else>
            <div v-if="cellDecisions.length" class="compact-decisions">
              <div
                v-for="d in cellDecisions"
                :key="d.request_id"
                class="dec-row clickable"
                :class="{
                  active: modalDecisionId === d.request_id,
                  'dec-row-specified': d.task_type === SPECIFIED_MODEL_TASK_KEY,
                }"
                @click="openDecisionModal(d.request_id)"
              >
                <span class="text-muted">{{ new Date(d.ts).toLocaleString() }}</span>
                <span
                  :class="d.task_type === SPECIFIED_MODEL_TASK_KEY ? 'badge badge-specified' : 'badge badge-blue'"
                >{{ d.task_type === SPECIFIED_MODEL_TASK_KEY ? SPECIFIED_MODEL_DISPLAY_LABEL : (d.task_type || '-') }}</span>
                <span v-if="d.work_type" class="badge badge-gray">{{ d.work_type }}</span>
                <span class="model-name">{{ d.outbound_model || d.auto_decision?.chosen_model || d.client_model || '-' }}</span>
                <span :class="d.success ? 'badge badge-green' : 'badge badge-red'">{{ d.success ? '✓' : '✗' }}</span>
                <span v-if="d.latency_ms" class="text-muted">{{ fmtMs(d.latency_ms) }}</span>
              </div>
            </div>
            <div v-else class="text-muted">该组合暂无最近决策</div>
            <DecisionDetail
              v-if="modalDecisionId"
              :request-id="modalDecisionId"
              :l1="decisionReplayCache[modalDecisionId]?.l1 ?? cellDecisions.find(x => x.request_id === modalDecisionId)?.auto_decision"
              :l2="decisionReplayCache[modalDecisionId]?.l2"
              :loading="decisionReplayLoading === modalDecisionId"
              compact
            />
          </template>
        </div>
      </div>
    </div>

    <!-- ═══ Tab A: Overview ═══ -->
    <div v-if="activeTab === 'overview'" class="tab-content">
      <div v-if="indexData.length > 0 || indexLoading" class="card compact-card">
        <div class="card-toolbar">
          <div class="toolbar-left">
            <span class="layer-tag l1">L1</span>
            <span class="toolbar-title">模型推荐</span>
            <span v-if="selectedTask" class="task-hint">{{ taskLabel(selectedTask) }}</span>
          </div>
          <div class="toolbar-filters">
            <button
              v-for="t in TASK_TYPES"
              :key="t.key"
              class="task-pill sm"
              :class="{ active: selectedTask === t.key }"
              :title="TASK_TAGS[t.key]?.join(', ') || ''"
              @click="selectedTask = selectedTask === t.key ? '' : t.key"
            >{{ t.icon }}</button>
            <span class="toolbar-divider" />
            <button class="profile-pill" :class="{ active: selectedProfile === 'smart' }" @click="selectedProfile = 'smart'">智能</button>
            <button class="profile-pill" :class="{ active: selectedProfile === 'speed_first' }" @click="selectedProfile = 'speed_first'">速度</button>
            <button class="profile-pill" :class="{ active: selectedProfile === 'cost_first' }" @click="selectedProfile = 'cost_first'">成本</button>
          </div>
        </div>
        <div v-if="indexLoading" class="loading-hint">加载索引…</div>
        <div v-else class="table-wrap">
          <table class="dense-table">
            <thead>
              <tr>
                <th>#</th><th>模型</th><th>{{ profileLabel }}</th><th>P95</th><th>成功率</th><th>入/出 $/1M</th><th>压力</th><th></th>
              </tr>
            </thead>
            <tbody>
              <template v-for="(m, i) in sortedIndex.slice(0, 10)" :key="m.credential_id + ':' + m.raw_model">
                <tr
                  class="model-row"
                  :class="{ expanded: expandedModel === m.credential_id + ':' + m.raw_model }"
                  @click="toggleModel(m)"
                >
                  <td class="num">{{ i + 1 }}</td>
                  <td>
                    <div class="model-name">{{ m.canonical_name || m.raw_model }}</div>
                    <div class="model-sub">{{ m.raw_model }}</div>
                  </td>
                  <td>
                    <span class="score-pill" :class="scoreOf(m) >= 70 ? 'good' : ''">{{ fmt(scoreOf(m), 1) }}</span>
                  </td>
                  <td>{{ fmtMs(m.p95_latency_ms) }}</td>
                  <td>{{ fmt((m.success_rate ?? 0) * 100, 0) }}%</td>
                  <td class="price-cell">{{ fmt(m.unit_price_in_per_1m, 2) }}/{{ fmt(m.unit_price_out_per_1m, 2) }}</td>
                  <td>
                    <span v-if="m.pressure_ratio" :class="m.pressure_ratio > 0.7 ? 'text-danger' : 'text-muted'">
                      {{ fmt(m.pressure_ratio * 100, 0) }}%
                    </span>
                    <span v-else class="text-muted">-</span>
                  </td>
                  <td class="expand-icon">{{ expandedModel === m.credential_id + ':' + m.raw_model ? '▼' : '▶' }}</td>
                </tr>
                <!-- L2 detail -->
                <tr v-if="expandedModel === m.credential_id + ':' + m.raw_model" class="detail-row">
                  <td colspan="8">
                    <div class="expand-grid">
                      <div class="layer-panel l2-accent">
                        <div class="section-head sm">
                          <span class="layer-tag l2">L2</span>
                          <span>凭据调度</span>
                          <span class="section-hint">Tier 1→2→3→9 · plan 优先 PAYG · P2C</span>
                        </div>
                        <div v-if="layer2Loading === (m.canonical_name || m.raw_model)" class="text-muted">加载…</div>
                        <template v-else-if="layer2Cache[m.canonical_name || m.raw_model]">
                          <div class="l2-meta mono">{{ layer2Cache[m.canonical_name || m.raw_model]!.resolution_path }}</div>
                          <div class="l2-creds">
                            <div
                              v-for="(c, ci) in layer2Cache[m.canonical_name || m.raw_model]!.candidates.filter(x => x.routable).slice(0, 4)"
                              :key="c.credential_id"
                              class="l2-cred"
                              :class="{ top: ci === 0 }"
                            >
                              <span class="l2-rank">#{{ ci + 1 }}</span>
                              <span class="l2-prov">{{ c.provider_name }}</span>
                              <span class="badge badge-gray">T{{ c.tier }}</span>
                              <span v-if="c.composite_score != null" class="score-pill sm">{{ c.composite_score.toFixed(1) }}</span>
                            </div>
                          </div>
                        </template>
                        <div v-else class="text-muted">无凭据</div>
                      </div>
                      <div class="layer-panel l1-accent">
                        <div class="section-head sm"><span class="layer-tag l1">L1</span><span>6维评分</span></div>
                        <SixDimScoreBar compact :scores="{
                          price_score: m.score_smart,
                          speed_score: m.score_speed_first,
                          stability_score: (m.success_rate ?? 0.9) * 100,
                          match_score: 50,
                          pressure_score: (1 - (m.pressure_ratio ?? 0)) * 100,
                          context_fit: m.context_window ? Math.min(100, m.context_window / 4096) : 50,
                        }" />
                      </div>
                    </div>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>
      </div>
      <div v-else-if="!indexLoading" class="card compact-card empty-hint">
        索引暂无数据 — 点击 ↻ 刷新
      </div>
    </div>

    <!-- ═══ Tab B: Policy ═══ -->
    <div v-if="activeTab === 'policy'" class="tab-content">
      <!-- Profile weights — flat 3-col -->
      <div class="card compact-card flat-card">
        <div class="section-head tight"><span class="layer-tag l1">L1</span><h3>Profile 权重矩阵</h3></div>
        <div class="profile-grid flat">
          <div v-for="(w, name) in DEFAULT_PROFILE_WEIGHTS" :key="name" class="profile-col">
            <div class="profile-col-head">{{ name === 'smart' ? '智能' : name === 'speed_first' ? '速度' : '成本' }}</div>
            <SixDimScoreBar compact :scores="{
              price_score: (w as ProfileWeights).Price / maxDimValue(w as ProfileWeights) * 100,
              speed_score: (w as ProfileWeights).Speed / maxDimValue(w as ProfileWeights) * 100,
              stability_score: (w as ProfileWeights).Stability / maxDimValue(w as ProfileWeights) * 100,
              match_score: (w as ProfileWeights).Match / maxDimValue(w as ProfileWeights) * 100,
              pressure_score: (w as ProfileWeights).Pressure / maxDimValue(w as ProfileWeights) * 100,
              context_fit: (w as ProfileWeights).ContextFit / maxDimValue(w as ProfileWeights) * 100,
            }" />
          </div>
        </div>
      </div>

      <!-- L2 policy + weights — single card, inline -->
      <div class="card compact-card flat-card">
        <div class="section-head tight"><span class="layer-tag l2">L2</span><h3>路由算法与得分系数</h3></div>
        <div class="policy-inline">
          <div class="policy-block">
            <div class="block-label">算法参数</div>
            <div class="inline-fields">
              <label v-for="f in POLICY_FIELDS" :key="f.key" class="inline-field">
                <span>{{ f.label }}</span>
                <input type="number" :min="f.min" :max="f.max" :step="f.step" v-model.number="policyDraft[f.key]" />
              </label>
            </div>
            <button class="btn btn-primary btn-sm" :disabled="savingPolicy" @click="savePolicy">保存策略</button>
          </div>
          <div class="policy-divider" />
          <div class="policy-block">
            <div class="block-label">P2C 综合得分</div>
            <div class="inline-fields">
              <label class="inline-field">
                <span>价格权重</span>
                <input type="number" min="0" max="100" v-model.number="weightsDraft.price" />
              </label>
              <label class="inline-field">
                <span>会话负载</span>
                <input type="number" min="0" max="100" v-model.number="weightsDraft.session_load" />
              </label>
              <label class="inline-field">
                <span>错误惩罚</span>
                <input type="number" min="0" max="100" v-model.number="weightsDraft.failure_penalty" />
              </label>
            </div>
            <button class="btn btn-primary btn-sm" :disabled="savingPolicy" @click="saveWeights">保存权重</button>
          </div>
        </div>
        <div v-if="policyMsg" class="policy-msg">{{ policyMsg }}</div>
      </div>

      <!-- Cost tables -->
      <div class="cost-grid">
        <div class="card compact-card">
          <div class="section-head"><h3>客户成本</h3></div>
          <table v-if="customerCost.length" class="dense-table">
            <thead><tr><th>Key</th><th>24h</th><th>7d</th><th>Auto</th></tr></thead>
            <tbody>
              <tr v-for="c in customerCost" :key="c.api_key_id">
                <td>#{{ c.api_key_id }}</td>
                <td>{{ fmtCost(c.cost_usd_24h) }}</td>
                <td>{{ fmtCost(c.cost_usd_7d) }}</td>
                <td>{{ c.total_auto_requests || 0 }}</td>
              </tr>
            </tbody>
          </table>
          <div v-else class="text-muted">暂无</div>
        </div>
        <div class="card compact-card">
          <div class="section-head"><h3>模型成本</h3></div>
          <table v-if="modelCost.length" class="dense-table">
            <thead><tr><th>模型</th><th>费用</th><th>成功率</th><th>请求</th></tr></thead>
            <tbody>
              <tr v-for="m in modelCost" :key="m.raw_model">
                <td class="model-name">{{ m.raw_model }}</td>
                <td>{{ fmtCost(m.total_cost_usd) }}</td>
                <td>{{ fmt((m.success_rate ?? 0) * 100, 0) }}%</td>
                <td>{{ m.total_requests || 0 }}</td>
              </tr>
            </tbody>
          </table>
          <div v-else class="text-muted">暂无</div>
        </div>
      </div>
    </div>

    <!-- ═══ Tab D: Resolve (凭据路由) ═══ -->
    <div v-if="activeTab === 'resolve'" class="tab-content">
      <div class="card compact-card flat-card">
        <div class="section-head tight">
          <span class="layer-tag l2">L2</span>
          <h3>凭据路由解析</h3>
        </div>
        <div class="resolve-row">
          <div class="resolve-picker">
            <ModelPicker
              v-model="modelInput"
              placeholder="选择模型…"
              title="凭据路由模型"
              @update:model-value="onModelPicked"
            />
          </div>
          <input
            v-model="clientProfile"
            class="resolve-profile"
            placeholder="client profile"
            title="cursor / roocode / cline"
          />
          <button class="btn btn-primary btn-sm" :disabled="resolving || !modelInput.trim()" @click="doResolve">
            {{ resolving ? '查询中…' : '查询路由' }}
          </button>
        </div>
        <div v-if="resolveErr" class="alert alert-danger compact-alert">{{ resolveErr }}</div>
      </div>

      <div v-if="resolution" class="card compact-card">
        <div class="section-head tight"><h3>模型解析</h3></div>
        <div class="resolve-meta">
          <span><span class="text-muted">客户端：</span><code>{{ resolution.client_model }}</code></span>
          <span><span class="text-muted">路径：</span>{{ resolution.resolution_path }}</span>
          <span v-if="resolution.canonical_name"><span class="text-muted">Canonical：</span>{{ resolution.canonical_name }}</span>
        </div>
        <div v-if="resolution.plan_order.length" class="plan-order">
          执行顺序（P2C+粘性）：
          <span v-for="(p, i) in resolution.plan_order" :key="p.credential_id">
            {{ i > 0 ? ' → ' : '' }}#{{ p.credential_id }} ({{ p.raw_model }})
          </span>
        </div>
      </div>

      <div v-if="resolved" class="card compact-card">
        <div class="card-toolbar">
          <div class="toolbar-left">
            <span class="layer-tag l2">L2</span>
            <span class="toolbar-title">路由候选 — {{ modelInput }}</span>
          </div>
          <label v-if="resolveUnavailableCount > 0" class="show-unavail">
            <input type="checkbox" v-model="showUnavailable" />
            不可用（{{ resolveUnavailableCount }}）
          </label>
        </div>
        <div v-if="resolveCandidates.length === 0" class="empty-hint">该模型暂无凭据配置</div>
        <div v-else-if="filteredResolveCandidates.length === 0" class="empty-hint">
          暂无可用凭据 — {{ resolveUnavailableCount }} 个不可用
        </div>
        <div v-else class="table-wrap">
          <table class="dense-table">
            <thead>
              <tr>
                <th>得分</th><th>供应商</th><th>凭据</th><th>上游</th><th>Tier</th><th>计费</th><th>状态</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="c in filteredResolveCandidates" :key="c.credential_id" :style="c.routable ? '' : 'opacity:0.55'">
                <td>
                  <span class="score-pill" :class="c.composite_score != null && c.composite_score >= 70 ? 'good' : ''">
                    {{ c.composite_score != null ? c.composite_score.toFixed(1) : '—' }}
                  </span>
                </td>
                <td>{{ c.provider_name }}</td>
                <td>
                  <div>#{{ c.credential_id }}</div>
                  <div class="text-muted">{{ c.credential_label }}</div>
                </td>
                <td><code class="mono-sm">{{ c.model_name }}</code></td>
                <td>T{{ c.tier }} · w{{ c.weight }}</td>
                <td>{{ c.billing_mode || 'token' }}<span v-if="c.billing_round === 2" class="text-muted"> R2</span></td>
                <td>
                  <span class="badge" :class="c.routable ? 'badge-green' : 'badge-red'">
                    {{ c.routable ? '可路由' : '不可用' }}
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div v-if="resolved && resolveCandidates.length" class="card compact-card">
        <div class="section-head tight"><h3>本次 L2 漏斗</h3></div>
        <CredentialFunnel :stages="resolveFunnelStages" :model="modelInput" />
      </div>

      <div v-if="resolveLog.length" class="card compact-card">
        <div class="card-toolbar">
          <div class="toolbar-left"><span class="toolbar-title">运行记录</span></div>
          <button class="btn btn-ghost btn-sm" @click="clearResolveLog">清空</button>
        </div>
        <div class="table-wrap">
          <table class="dense-table">
            <thead>
              <tr><th>时间</th><th>模型</th><th>Profile</th><th>路径</th><th>可路由</th><th>Top凭据</th><th></th></tr>
            </thead>
            <tbody>
              <tr v-for="(e, i) in resolveLog" :key="i">
                <td>{{ new Date(e.ts).toLocaleTimeString() }}</td>
                <td class="model-name">{{ e.model }}</td>
                <td>{{ e.profile || '—' }}</td>
                <td class="mono-sm text-muted">{{ e.path }}</td>
                <td>{{ e.routable }}/{{ e.total }}</td>
                <td>{{ e.top_cred != null ? '#' + e.top_cred : '—' }}</td>
                <td><button class="btn btn-ghost btn-sm" @click="replayFromLog(e)">重查</button></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <!-- ═══ Tab C: Live ═══ -->
    <div v-if="activeTab === 'live'" class="tab-content">
      <!-- Simulator + distributions merged -->
      <div class="card compact-card flat-card">
        <div class="live-grid">
          <div class="live-sim">
            <div class="section-head tight"><h3>路由模拟</h3></div>
            <div class="sim-row">
              <input v-model="simPrompt" placeholder="输入 prompt 测试 L1→L2…" class="sim-input" />
              <select v-model="simProfile" class="sim-select">
                <option value="smart">智能</option>
                <option value="speed_first">速度</option>
                <option value="cost_first">成本</option>
              </select>
              <button class="btn btn-primary btn-sm" :disabled="simLoading" @click="runSim">{{ simLoading ? '…' : '模拟' }}</button>
            </div>
            <div v-if="simResult" class="sim-out">
              <div v-if="simResult.error" class="alert alert-danger compact-alert">{{ simResult.error }}</div>
              <div v-else-if="simResult.decision" class="sim-pipeline">
                <span class="sim-step l1">L1 {{ (simResult.decision as Record<string, unknown>).task_type }}</span>
                <span class="pipe-dot">→</span>
                <span class="sim-step">{{ fmt(Number((simResult.decision as Record<string, unknown>).confidence) * 100, 0) }}%</span>
                <span class="pipe-dot">→</span>
                <span class="sim-step l2 win">{{ (simResult.decision as Record<string, unknown>).chosen_model }}</span>
                <span class="text-muted">cred #{{ (simResult.decision as Record<string, unknown>).chosen_credential_id }}</span>
              </div>
            </div>
          </div>
          <div class="live-dist">
            <div class="dist-mini">
              <div class="dist-col">
                <h4>任务</h4>
                <div v-for="[task, count] in distEntries(audit.task_distribution).slice(0, 5)" :key="task" class="dist-row">
                  <span class="dist-label">{{ task }}</span>
                  <div class="dist-bar-bg"><div class="dist-bar-fill" :style="{ width: (count / distMax(audit.task_distribution) * 100) + '%' }" /></div>
                  <span class="dist-count">{{ count }}</span>
                </div>
              </div>
              <div class="dist-col">
                <h4>Profile</h4>
                <div v-for="[p, count] in distEntries(audit.profile_distribution).slice(0, 5)" :key="p" class="dist-row">
                  <span class="dist-label">{{ p }}</span>
                  <div class="dist-bar-bg"><div class="dist-bar-fill accent" :style="{ width: (count / distMax(audit.profile_distribution) * 100) + '%' }" /></div>
                  <span class="dist-count">{{ count }}</span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Decisions -->
      <div class="card compact-card">
        <div class="section-head">
          <h3>最近决策</h3>
          <label class="auto-toggle"><input type="checkbox" v-model="autoRefresh" /> 5s</label>
        </div>
        <div class="table-wrap">
          <table v-if="decisions.length" class="dense-table">
            <thead><tr><th>时间</th><th>任务</th><th>Profile</th><th>模型</th><th>置信</th><th>状态</th><th></th></tr></thead>
            <tbody>
              <template v-for="d in decisions" :key="d.request_id">
                <tr
                  class="model-row"
                  :class="{ 'model-row-specified': d.task_type === SPECIFIED_MODEL_TASK_KEY }"
                  @click="onExpandDecision(d.request_id)"
                >
                  <td>{{ new Date(d.ts).toLocaleTimeString() }}</td>
                  <td>
                    <span
                      v-if="d.task_type === SPECIFIED_MODEL_TASK_KEY"
                      class="badge badge-specified"
                    >{{ SPECIFIED_MODEL_DISPLAY_LABEL }}</span>
                    <span v-else class="badge badge-blue">{{ d.task_type || d.auto_decision?.task_type || '-' }}</span>
                  </td>
                  <td>{{ d.auto_profile || d.auto_decision?.profile || (d.task_type === SPECIFIED_MODEL_TASK_KEY ? '—' : '-') }}</td>
                  <td class="model-name">{{ d.auto_decision?.chosen_model || d.outbound_model || d.client_model || '-' }}</td>
                  <td>{{ d.task_type === SPECIFIED_MODEL_TASK_KEY ? '—' : fmt((d.auto_confidence ?? d.auto_decision?.confidence ?? 0) * 100, 0) + '%' }}</td>
                  <td><span :class="d.success ? 'badge badge-green no-spec-tint' : 'badge badge-red no-spec-tint'">{{ d.success ? '✓' : '✗' }}</span></td>
                  <td class="expand-icon">{{ expandedDecision === d.request_id ? '▼' : '▶' }}</td>
                </tr>
                <tr v-if="expandedDecision === d.request_id" class="detail-row">
                  <td colspan="7">
                    <DecisionDetail
                      :request-id="d.request_id"
                      :l1="decisionReplayCache[d.request_id]?.l1 ?? d.auto_decision ?? undefined"
                      :l2="decisionReplayCache[d.request_id]?.l2"
                      :loading="decisionReplayLoading === d.request_id"
                      compact
                    />
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
          <div v-else class="text-muted">暂无最近决策</div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.routing-dashboard { max-width: 1200px; }

/* Unified top bar */
.top-bar {
  margin-bottom: 8px;
  padding: 8px 10px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.top-bar-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 6px;
}
.top-bar-head h2 { font-size: 15px; margin: 0; flex-shrink: 0; }
.refresh-btn { margin-left: auto; }
.nav-chips { display: inline-flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.nav-link-wt {
  font-size: 11px;
  color: var(--accent-h);
  text-decoration: none;
  padding: 3px 8px;
  border: 1px solid var(--border);
  border-radius: 4px;
  white-space: nowrap;
}
.nav-link-wt:hover { background: color-mix(in srgb, var(--accent) 8%, transparent); }
.chip-link { display: inline-flex; align-items: center; gap: 4px; }
.chip-inline {
  font-size: 9px;
  padding: 0 4px;
  border-radius: 3px;
  background: var(--bg-subtle);
  color: var(--muted);
  font-weight: 500;
}

/* Pipeline */
.pipeline { margin-bottom: 6px; }
.pipe-row {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  padding: 3px 0;
}
.pipe-row.l1-row { color: var(--accent-h); }
.pipe-row.l2-row { color: var(--success); }
.pipe-title { font-size: 11px; font-weight: 600; min-width: 42px; }
.pipe-steps { display: flex; align-items: center; flex-wrap: wrap; gap: 2px; }
.pipe-step {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 3px;
  background: color-mix(in srgb, currentColor 8%, transparent);
  color: var(--text);
}
.pipe-dot { font-size: 10px; color: var(--muted); padding: 0 1px; }
.pipe-bridge {
  text-align: center;
  font-size: 10px;
  color: var(--muted);
  line-height: 1;
  margin: -2px 0;
  padding-left: 8px;
}

.hero-stats { display: flex; flex-wrap: wrap; gap: 4px; }

/* Segmented tabs (inline in top bar) */
.seg-tabs {
  display: inline-flex;
  gap: 1px;
  padding: 2px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 6px;
}
.seg-tab {
  padding: 3px 10px;
  border: none;
  border-radius: 4px;
  background: transparent;
  font-size: 11px;
  color: var(--muted);
  cursor: pointer;
  transition: all .12s;
  white-space: nowrap;
}
.seg-tab:hover { color: var(--text); }
.seg-tab.active {
  background: var(--card);
  color: var(--text);
  font-weight: 600;
  box-shadow: 0 1px 2px rgba(0,0,0,.12);
}

.tab-content { display: flex; flex-direction: column; gap: 8px; }
.analytics-hint { font-size: 11px; margin: 0 0 4px 2px; }
.analytics-charts {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
  align-items: stretch;
}
.chart-card {
  display: flex;
  flex-direction: column;
  height: 100%;
}
@media (max-width: 900px) {
  .analytics-charts { grid-template-columns: 1fr; }
}
.card-toolbar.clickable { cursor: pointer; user-select: none; }
.collapsible .expand-icon { font-size: 10px; color: var(--muted); }
.decision-detail {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 6px 0;
}

.chip {
  display: inline-flex; align-items: center; gap: 3px;
  padding: 2px 8px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 99px;
  font-size: 10px;
  color: var(--muted);
}
.chip strong { color: var(--text); font-weight: 600; }

/* Cards */
.compact-card { padding: 8px 10px; }
.flat-card { box-shadow: none; }
.card-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  flex-wrap: wrap;
  margin-bottom: 6px;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--border);
}
.toolbar-left { display: flex; align-items: center; gap: 6px; }
.toolbar-title { font-size: 12px; font-weight: 600; }
.toolbar-filters { display: flex; align-items: center; gap: 3px; flex-wrap: wrap; }
.toolbar-divider { width: 1px; height: 14px; background: var(--border); margin: 0 3px; }
.loading-hint { padding: 12px; text-align: center; color: var(--muted); font-size: 11px; }

.section-head {
  display: flex; align-items: center; gap: 6px;
  margin-bottom: 6px;
}
.section-head.tight { margin-bottom: 4px; }
.section-head h3 { margin: 0; font-size: 12px; font-weight: 600; }
.section-head.sm { margin-bottom: 4px; font-size: 11px; }
.section-hint { font-size: 9px; color: var(--muted); font-weight: 400; margin-left: auto; }
.layer-tag {
  display: inline-flex; align-items: center; justify-content: center;
  width: 22px; height: 14px;
  border-radius: 3px;
  font-size: 8px; font-weight: 700;
  flex-shrink: 0;
}
.layer-tag.l1 { background: rgba(99,102,241,.22); color: var(--accent-h); }
.layer-tag.l2 { background: rgba(63,185,80,.22); color: var(--success); }
.task-hint { font-weight: 400; color: var(--muted); font-size: 10px; }

.task-pill {
  padding: 2px 7px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 99px;
  font-size: 11px;
  cursor: pointer;
  transition: all .12s;
  color: var(--text);
}
.task-pill.sm { padding: 1px 5px; font-size: 11px; }
.task-pill:hover { border-color: var(--accent); }
.task-pill.active {
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 12%, var(--bg-subtle));
}

.profile-pill {
  padding: 1px 7px;
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 99px;
  font-size: 10px;
  cursor: pointer;
  color: var(--muted);
}
.profile-pill.active {
  border-color: var(--accent);
  color: var(--accent-h);
  background: color-mix(in srgb, var(--accent) 10%, transparent);
}

.table-wrap { overflow-x: auto; -webkit-overflow-scrolling: touch; }
.dense-table { font-size: 11px; width: 100%; }
.dense-table thead th { padding: 3px 6px; font-size: 9px; white-space: nowrap; }
.dense-table tbody td { padding: 4px 6px; }
.dense-table .num { color: var(--muted); width: 20px; }
.model-name { font-weight: 500; font-size: 11px; }
.model-sub { font-size: 9px; color: var(--muted); }
.price-cell { font-variant-numeric: tabular-nums; font-size: 10px; }
.expand-icon { color: var(--muted); width: 16px; text-align: center; font-size: 9px; }

.score-pill {
  display: inline-block;
  padding: 0 5px;
  border-radius: 99px;
  font-size: 10px;
  font-weight: 600;
  background: rgba(139,148,158,.15);
  color: var(--muted);
}
.score-pill.good { background: rgba(63,185,80,.15); color: var(--success); }
.score-pill.sm { font-size: 9px; padding: 0 4px; }

.model-row { cursor: pointer; }
.model-row:hover { background: rgba(255,255,255,.03); }
.model-row.expanded { background: color-mix(in srgb, var(--accent) 5%, transparent); }
/* Use td:not(.no-spec-tint) so the success/fail badge (which uses
   its own badge-green / badge-red classes) keeps its semantic colour. */
.model-row-specified td:not(.no-spec-tint) { color: #6b7280; font-style: italic; }
.badge-specified {
  background: rgba(107, 114, 128, 0.15);
  color: #6b7280;
  border-left: 2px solid #6b7280;
  font-style: italic;
}
.dec-row-specified {
  background: rgba(107, 114, 128, 0.04);
  border-left: 2px solid #6b7280;
}
.detail-row td { padding: 6px; background: var(--bg-subtle); border-top: none; }

.expand-grid {
  display: grid;
  grid-template-columns: 1fr minmax(200px, 260px);
  gap: 8px;
  align-items: start;
}
.layer-panel {
  padding: 6px 8px;
  border-radius: 4px;
  background: var(--bg);
}
.layer-panel.l1-accent { border-left: 3px solid var(--accent); }
.layer-panel.l2-accent { border-left: 3px solid var(--success); }
.l2-meta { font-size: 10px; color: var(--muted); margin-bottom: 4px; word-break: break-all; }
.l2-meta.mono { font-family: ui-monospace, monospace; font-size: 9px; }
.l2-creds { display: flex; flex-wrap: wrap; gap: 4px; }
.l2-cred {
  display: flex; align-items: center; gap: 4px;
  padding: 2px 6px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 3px;
  font-size: 10px;
}
.l2-cred.top { border-color: var(--success); background: rgba(63,185,80,.08); }
.l2-rank { font-weight: 700; color: var(--muted); font-size: 9px; }
.l2-prov { font-weight: 500; }

/* Policy inline */
.policy-inline {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
  align-items: flex-start;
}
.policy-block { flex: 1; min-width: 220px; }
.policy-divider { width: 1px; background: var(--border); align-self: stretch; min-height: 60px; }
.block-label { font-size: 10px; color: var(--muted); margin-bottom: 4px; font-weight: 600; text-transform: uppercase; letter-spacing: .03em; }
.inline-fields {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 10px;
  margin-bottom: 6px;
}
.inline-field {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 72px;
}
.inline-field span { font-size: 9px; color: var(--muted); }
.inline-field input {
  width: 64px;
  padding: 2px 5px;
  font-size: 11px;
}
.policy-msg { font-size: 11px; color: var(--accent-h); margin-top: 6px; }

.profile-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
}
.profile-grid.flat .profile-col {
  padding: 4px 6px;
  border-right: 1px solid var(--border);
  background: transparent;
  border-radius: 0;
}
.profile-grid.flat .profile-col:last-child { border-right: none; }
.profile-col-head {
  font-size: 10px;
  font-weight: 600;
  margin-bottom: 4px;
  text-align: center;
}

.cost-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 8px;
}

.compact-alert { padding: 4px 8px; margin-bottom: 0; font-size: 11px; }
.empty-hint { text-align: center; color: var(--muted); font-size: 11px; padding: 16px; }
.empty-state {
  text-align: center;
  color: var(--muted);
  font-size: 12px;
  padding: 24px 16px;
}
.empty-state p { margin: 0; }
.link-btn {
  background: none;
  border: none;
  color: var(--accent-h);
  cursor: pointer;
  font-size: inherit;
  text-decoration: underline;
  padding: 0;
}
.cell-popup { margin-top: 4px; }
.compact-decisions { display: flex; flex-direction: column; gap: 4px; }
.dec-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  font-size: 10px;
  padding: 4px 0;
  border-bottom: 1px solid var(--border);
}
.dec-row:last-child { border-bottom: none; }
.dec-row.clickable { cursor: pointer; }
.dec-row.clickable:hover { background: var(--bg-subtle); }
.dec-row.active { background: color-mix(in srgb, var(--accent) 8%, transparent); }

.modal-overlay {
  position: fixed;
  inset: 0;
  z-index: 1000;
  background: rgba(0, 0, 0, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
}
.modal-panel {
  width: min(640px, 100%);
  max-height: 85vh;
  overflow-y: auto;
}

/* Live tab */
.live-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  align-items: start;
}
.sim-row { display: flex; gap: 4px; }
.sim-input { flex: 1; font-size: 11px; padding: 3px 6px; min-width: 0; }
.sim-select { width: 72px; font-size: 11px; padding: 3px 4px; flex-shrink: 0; }
.sim-out { margin-top: 6px; }
.sim-pipeline { display: flex; flex-wrap: wrap; align-items: center; gap: 4px; font-size: 11px; }
.sim-step {
  padding: 1px 6px;
  border-radius: 3px;
  font-size: 10px;
  background: var(--bg-subtle);
}
.sim-step.l1 { background: rgba(99,102,241,.12); color: var(--accent-h); }
.sim-step.l2.win { background: rgba(63,185,80,.15); color: var(--success); font-weight: 600; }

.dist-mini { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.dist-col h4 { font-size: 9px; text-transform: uppercase; color: var(--muted); margin: 0 0 4px; letter-spacing: .04em; }
.dist-row {
  display: grid;
  grid-template-columns: 56px 1fr 24px;
  align-items: center;
  gap: 4px;
  margin-bottom: 2px;
  font-size: 10px;
}
.dist-label { color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dist-bar-bg { height: 6px; background: color-mix(in srgb, var(--border) 30%, transparent); border-radius: 2px; overflow: hidden; }
.dist-bar-fill { height: 100%; background: var(--success); border-radius: 2px; }
.dist-bar-fill.accent { background: var(--accent); }
.dist-count { text-align: right; font-variant-numeric: tabular-nums; font-size: 9px; }

.auto-toggle { font-size: 10px; color: var(--muted); display: flex; align-items: center; gap: 3px; cursor: pointer; }
.decision-detail { display: flex; flex-direction: column; gap: 4px; }
.candidates-compact { display: flex; flex-wrap: wrap; gap: 3px; }
.cand-chip {
  padding: 1px 6px;
  background: var(--bg);
  border-radius: 3px;
  font-size: 10px;
}

.text-muted { color: var(--muted); font-size: 10px; }
.text-danger { color: var(--danger); font-size: 10px; }

.resolve-row { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
.resolve-picker { flex: 1; min-width: 200px; }
.resolve-profile { width: 120px; font-size: 11px; padding: 3px 6px; }
.resolve-meta { display: flex; flex-wrap: wrap; gap: 8px 16px; font-size: 11px; }
.resolve-meta code { font-size: 10px; }
.plan-order { margin-top: 6px; font-size: 10px; color: var(--muted); word-break: break-all; }
.mono-sm { font-family: ui-monospace, monospace; font-size: 9px; }
.show-unavail { display: flex; align-items: center; gap: 4px; font-size: 10px; color: var(--muted); cursor: pointer; }
.show-unavail input { width: auto; }

@media (max-width: 768px) {
  .top-bar-head { gap: 6px; }
  .seg-tabs { order: 3; width: 100%; justify-content: stretch; }
  .seg-tab { flex: 1; text-align: center; padding: 4px 6px; }
  .refresh-btn { order: 2; margin-left: auto; }
  .pipe-steps { display: none; }
  .pipe-title::after { content: ' · Prompt→Profile→模型 / 解析→Tier→P2C'; font-weight: 400; color: var(--muted); font-size: 9px; }
  .expand-grid { grid-template-columns: 1fr; }
  .profile-grid { grid-template-columns: 1fr; }
  .profile-grid.flat .profile-col { border-right: none; border-bottom: 1px solid var(--border); padding-bottom: 8px; }
  .live-grid { grid-template-columns: 1fr; }
  .dist-mini { grid-template-columns: 1fr; }
  .policy-inline { flex-direction: column; }
  .policy-divider { width: 100%; height: 1px; min-height: 0; }
  .dense-table thead { display: none; }
  .dense-table tbody tr.model-row td:nth-child(n+4):not(:last-child) { display: none; }
}

@media (max-width: 480px) {
  .toolbar-filters { width: 100%; }
  .inline-fields { gap: 4px; }
}
</style>
