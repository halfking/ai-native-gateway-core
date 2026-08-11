<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  listWorkTypes, getWorkType, createWorkType, updateWorkType, deleteWorkType,
  putWorkTypeRoutes, getWorkTypeStats, syncWorkTypesFromACC,
  PROFILES, CATEGORIES,
  groupRoutesByLayer, normalizeRouteTier,
  type WorkTypeConfig, type WorkTypeStats, type ModelRoute, type ModelRouteTier, type WorkTypeSyncMeta,
} from '../api-work-types'
import {
  getAutoRouteAudit, getAutoRouteDecisions,
  type AutoRouteAudit, type AutoRouteDecision,
} from '../api-autoroute'
import { probeModel, type ProbeResult } from '../api'
import { useL1TaskTypes } from '../composables/useL1TaskTypes'
import ModelPicker from '../components/ModelPicker.vue'

const { t } = useI18n()


// Per-layer cap. Two layers (primary/secondary) × 5 models = 10 routes max
// per work type, which is well below the historical 3-row hard limit but
// generous enough for operators to build a real priority sequence.
const MAX_ROUTES_PER_LAYER = 5

// Tiers exposed in the UI. The DB also has 'fallback' (reserved for future
// tertiary routes / emergency degradations), but the editor only manages
// the two primary layers operators interact with day-to-day.
const EDITABLE_TIERS: ModelRouteTier[] = ['primary', 'secondary']

const route = useRoute()
const router = useRouter()

const activeTab = computed<'overview' | 'settings'>(() => {
  if (route.path.endsWith('/settings')) return 'settings'
  return 'overview'
})

const detailKey = computed(() => {
  const p = route.params.key
  if (typeof p === 'string' && p && p !== 'settings') return p
  return ''
})

const isDetailView = computed(() => activeTab.value === 'settings' && !!detailKey.value)

// ── Overview data ─────────────────────────────────────
const audit = ref<AutoRouteAudit>({
  total_auto_requests: 0, success_rate: 0,
  task_distribution: {}, profile_distribution: {}, top_chosen_models: [],
})
const stats = ref<WorkTypeStats | null>(null)
const decisions = ref<AutoRouteDecision[]>([])
const loading = ref(false)

async function loadOverview() {
  loading.value = true
  try {
    const [a, s, d] = await Promise.all([
      getAutoRouteAudit(),
      getWorkTypeStats(),
      getAutoRouteDecisions(10),
    ])
    audit.value = a
    stats.value = s
    syncMeta.value = s.sync_meta ?? null
    decisions.value = d
  } catch (e) {
    console.error('loadOverview', e)
  } finally {
    loading.value = false
  }
}

const wtStatsEntries = computed(() => {
  if (!stats.value) return []
  return Object.values(stats.value.by_work_type)
    .sort((a, b) => b.count_24h - a.count_24h)
    .slice(0, 10)
})

const wtStatsMax = computed(() => Math.max(...wtStatsEntries.value.map(e => e.count_24h), 1))

function distEntries(d: Record<string, number>): Array<[string, number]> {
  return Object.entries(d).sort((a, b) => b[1] - a[1])
}
function distMax(d: Record<string, number>): number {
  return Math.max(...Object.values(d), 1)
}
function fmt(n: number | undefined, digits = 1): string {
  if (n === undefined || n === null || isNaN(n)) return '-'
  return n.toFixed(digits)
}
// L1 task types come from useL1TaskTypes composable (DB-backed, refreshed on
// mount via listL1TaskTypes()). The composable seeds canonical 8 immediately
// so first paint isn't blank, then swaps in the live DB-derived list once
// the response lands.
const { l1TaskTypes, l1Label, refreshL1TaskTypes } = useL1TaskTypes()
function profileLabel(key: string): string {
  return PROFILES.find(p => p.key === key)?.label ?? key
}

function routeSummary(wt: WorkTypeConfig): string[] {
  const routes = wt.model_routes ?? []
  return routes
    .filter(r => r.enabled !== false && r.canonical_name)
    .sort((a, b) => {
      // Mirror the backend ordering (tier rank ASC, weight DESC) so the
      // summary chip strip matches what the editor shows.
      const tierRank: Record<ModelRouteTier, number> = { primary: 0, secondary: 1, fallback: 2 }
      const ta = tierRank[normalizeRouteTier(a)] ?? 1
      const tb = tierRank[normalizeRouteTier(b)] ?? 1
      if (ta !== tb) return ta - tb
      return b.weight - a.weight
    })
    .map(r => r.canonical_name)
}

function routeTierCount(wt: WorkTypeConfig, tier: ModelRouteTier): number {
  const routes = wt.model_routes ?? []
  return routes.filter(r => r.enabled !== false && r.canonical_name && normalizeRouteTier(r) === tier).length
}

// ── Settings / CRUD ───────────────────────────────────
const workTypes = ref<WorkTypeConfig[]>([])
const settingsLoading = ref(false)
const syncMsg = ref('')
const syncOk = ref<boolean | null>(null)
const syncMeta = ref<WorkTypeSyncMeta | null>(null)

const showCreateModal = ref(false)
const createForm = ref({
  key: '', label: '', category: '通用', l1_task_type: 'chat',
  default_profile: 'smart' as 'smart' | 'speed_first' | 'cost_first',
  tags: '', prompt_keywords: '', sort_order: 0, enabled: true,
  acc_task_type: '',
})
const createError = ref('')

const detail = ref<WorkTypeConfig | null>(null)
const detailForm = ref({
  label: '', category: '通用', l1_task_type: 'chat',
  default_profile: 'smart' as 'smart' | 'speed_first' | 'cost_first',
  tags: '', prompt_keywords: [] as string[], sort_order: 0,
  acc_task_type: '',
})
const detailSaving = ref(false)
const detailMsg = ref('')

const keywordInput = ref('')

function addKeyword() {
  const raw = keywordInput.value.trim()
  if (!raw) return
  const parts = raw.split(/[,，]/).map(s => s.trim()).filter(Boolean)
  for (const kw of parts) {
    if (!detailForm.value.prompt_keywords.includes(kw)) {
      detailForm.value.prompt_keywords.push(kw)
    }
  }
  keywordInput.value = ''
}

function removeKeyword(kw: string) {
  const idx = detailForm.value.prompt_keywords.indexOf(kw)
  if (idx >= 0) detailForm.value.prompt_keywords.splice(idx, 1)
}

function onKeywordKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' || e.key === ',') {
    e.preventDefault()
    addKeyword()
  } else if (e.key === 'Backspace' && !keywordInput.value && detailForm.value.prompt_keywords.length) {
    removeKeyword(detailForm.value.prompt_keywords[detailForm.value.prompt_keywords.length - 1])
  }
}

// routesDraft is keyed by editable tier so each layer is independent.
// Within a layer, priority is order in the array (weight is derived
// when saving so we never lose data even if the operator drags rows
// around).
const routesDraft = ref<Record<ModelRouteTier, ModelRoute[]>>({
  primary: [],
  secondary: [],
  fallback: [],
})
const routesSaving = ref(false)
const routesMsg = ref('')

const testResults = ref<Record<string, ProbeResult>>({})
const testErrors = ref<Record<string, string>>({})
const testingModel = ref<string | null>(null)
const testingAll = ref(false)

// Drag state — one shared "from" index per tier so two layers can be
// re-ordered independently without colliding.
const dragState = ref<{ tier: ModelRouteTier | null; index: number | null }>({
  tier: null,
  index: null,
})

function freshRouteRow(tier: ModelRouteTier): ModelRoute {
  return {
    canonical_name: '',
    weight: 1,
    min_score: 0,
    enabled: true,
    tier,
    task_quality_score: 0,
  }
}

function onRouteDragStart(tier: ModelRouteTier, index: number) {
  dragState.value = { tier, index }
}

function onRouteDragOver(event: DragEvent, tier: ModelRouteTier, index: number) {
  event.preventDefault()
  const { tier: fromTier, index: fromIndex } = dragState.value
  if (fromTier === null || fromIndex === null) return
  if (fromTier !== tier) return
  if (fromIndex === index) return
  const list = [...routesDraft.value[tier]]
  const dragged = list[fromIndex]
  list.splice(fromIndex, 1)
  list.splice(index, 0, dragged)
  routesDraft.value = { ...routesDraft.value, [tier]: list }
  dragState.value = { tier, index }
}

function onRouteDragEnd() {
  dragState.value = { tier: null, index: null }
}

function addRouteRow(tier: ModelRouteTier) {
  if (routesDraft.value[tier].length >= MAX_ROUTES_PER_LAYER) return
  routesDraft.value = {
    ...routesDraft.value,
    [tier]: [...routesDraft.value[tier], freshRouteRow(tier)],
  }
}

function removeRouteRow(tier: ModelRouteTier, index: number) {
  const list = [...routesDraft.value[tier]]
  list.splice(index, 1)
  routesDraft.value = { ...routesDraft.value, [tier]: list }
  // Clear any stale test result for the removed model so a future row
  // picking the same canonical_name does not show a misleading ✔.
  const removed = routesDraft.value[tier][index]
  if (removed?.canonical_name) {
    delete testResults.value[removed.canonical_name]
    delete testErrors.value[removed.canonical_name]
  }
}

function totalRouteCount(): number {
  return (
    routesDraft.value.primary.length +
    routesDraft.value.secondary.length
  )
}

function syncDetailForm(wt: WorkTypeConfig) {
  detailForm.value = {
    label: wt.label,
    category: wt.category,
    l1_task_type: wt.l1_task_type,
    default_profile: wt.default_profile,
    tags: wt.tags.join(', '),
    prompt_keywords: [...wt.prompt_keywords],
    sort_order: wt.sort_order,
    acc_task_type: wt.acc_task_type ?? '',
  }
}

async function loadSettings() {
  settingsLoading.value = true
  try {
    workTypes.value = await listWorkTypes(true)
    if (detailKey.value) {
      detail.value = await getWorkType(detailKey.value)
      syncDetailForm(detail.value)
      // Bucket routes by tier. Fallback-tier rows (reserved) are loaded
      // so a Save round-trip doesn't silently drop them, but the editor
      // does not render them — see template.
      const grouped = groupRoutesByLayer(detail.value.model_routes)
      routesDraft.value = {
        primary: grouped.primary.map(r => ({ ...r })),
        secondary: grouped.secondary.map(r => ({ ...r })),
        // Defensive: if someone wrote a fallback row via API directly,
        // surface it in the secondary layer's bucket so the operator can
        // see and move it. This shouldn't normally happen.
        fallback: grouped.fallback.map(r => ({ ...r })),
      }
      testResults.value = {}
      testErrors.value = {}
    } else {
      detail.value = null
    }
  } catch (e) {
    console.error('loadSettings', e)
  } finally {
    settingsLoading.value = false
  }
}

function openCreate() {
  createForm.value = {
    key: '', label: '', category: '通用', l1_task_type: 'chat',
    default_profile: 'smart', tags: '', prompt_keywords: '',
    sort_order: workTypes.value.length + 1, enabled: true,
    acc_task_type: '',
  }
  createError.value = ''
  showCreateModal.value = true
}

async function saveCreate() {
  createError.value = ''
  const tags = createForm.value.tags.split(/[,，]/).map(s => s.trim()).filter(Boolean)
  const kw = createForm.value.prompt_keywords.split(/[,，]/).map(s => s.trim()).filter(Boolean)
  try {
    const payload: Partial<WorkTypeConfig> & { key: string; label: string; category: string; l1_task_type: string } = {
      key: createForm.value.key.trim(),
      label: createForm.value.label.trim(),
      category: createForm.value.category,
      l1_task_type: createForm.value.l1_task_type,
      default_profile: createForm.value.default_profile,
      tags, prompt_keywords: kw,
      sort_order: createForm.value.sort_order,
      enabled: createForm.value.enabled,
    }
    const accType = createForm.value.acc_task_type.trim()
    if (accType) payload.acc_task_type = accType
    else payload.acc_task_type = null
    const wt = await createWorkType(payload)
    showCreateModal.value = false
    router.push(`/routing-v2/work-types/${wt.key}`)
  } catch (e) {
    createError.value = String(e)
  }
}

async function saveDetailMeta() {
  if (!detailKey.value) return
  detailSaving.value = true
  detailMsg.value = ''
  const tags = detailForm.value.tags.split(/[,，]/).map(s => s.trim()).filter(Boolean)
  // Flush any pending keyword input before save
  if (keywordInput.value.trim()) addKeyword()
  try {
    const payload: Partial<WorkTypeConfig> = {
      label: detailForm.value.label.trim(),
      category: detailForm.value.category,
      l1_task_type: detailForm.value.l1_task_type,
      default_profile: detailForm.value.default_profile,
      tags,
      prompt_keywords: [...detailForm.value.prompt_keywords],
      sort_order: detailForm.value.sort_order,
    }
    const accType = detailForm.value.acc_task_type.trim()
    if (accType) payload.acc_task_type = accType
    else payload.acc_task_type = null
    detail.value = await updateWorkType(detailKey.value, payload)
    syncDetailForm(detail.value)
    detailMsg.value = t('workTypes.savedOk')
    await loadSettings()
  } catch (e) {
    detailMsg.value = String(e)
  } finally {
    detailSaving.value = false
  }
}

async function toggleEnabled() {
  if (!detail.value || !detailKey.value) return
  const next = !detail.value.enabled
  const action = next ? t('workTypes.detail.errors.confirmEnable') : t('workTypes.detail.errors.confirmDisable')
  if (!next && !confirm(t('workTypes.detail.errors.toggleConfirm', { action, name: detail.value.label }))) return
  try {
    if (next) {
      await updateWorkType(detailKey.value, { enabled: true })
    } else {
      await deleteWorkType(detailKey.value)
    }
    await loadSettings()
  } catch (e) {
    detailMsg.value = String(e)
  }
}

// saveRoutes flattens the two-tier draft back into a single ordered
// ModelRoute[] payload. Within each tier, list order is the priority
// (top = highest) and we materialise that ordering into `weight` so the
// DB's ORDER BY weight DESC reproduces the operator's drag-and-drop
// intent across a page reload. We also drop half-filled rows that have
// no canonical_name yet.
async function saveRoutes() {
  if (!detailKey.value) return
  const payload: ModelRoute[] = []
  for (const tier of EDITABLE_TIERS) {
    const rows = routesDraft.value[tier]
    const n = rows.length
    rows.forEach((rt, i) => {
      const name = rt.canonical_name.trim()
      if (!name) return
      // weight is per-tier: top row gets the largest value, bottom row
      // gets 1.0. Concretely weight = (n - i) so dragging row 0 of 3
      // yields weight 3, row 2 yields weight 1.
      const weight = n - i
      payload.push({
        ...rt,
        canonical_name: name,
        tier,
        weight,
        // Clamp task_quality_score so we don't ship NaN/negative values.
        task_quality_score: Number.isFinite(rt.task_quality_score)
          ? Math.max(0, Math.min(100, rt.task_quality_score))
          : 0,
      })
    })
  }
  routesSaving.value = true
  routesMsg.value = ''
  try {
    await putWorkTypeRoutes(detailKey.value, payload)
    detail.value = await getWorkType(detailKey.value)
    // Re-bucket the freshly persisted rows so the draft mirrors the
    // server's tier-aware ORDER BY (primary first, then secondary).
    const grouped = groupRoutesByLayer(detail.value.model_routes)
    routesDraft.value = {
      primary: grouped.primary.map(r => ({ ...r })),
      secondary: grouped.secondary.map(r => ({ ...r })),
      fallback: grouped.fallback.map(r => ({ ...r })),
    }
    routesMsg.value = t('workTypes.savedOk')
    await loadSettings()
  } catch (e) {
    routesMsg.value = String(e)
  } finally {
    routesSaving.value = false
  }
}

async function testRoute(rt: ModelRoute) {
  const name = rt.canonical_name.trim()
  if (!name) return
  testingModel.value = name
  delete testErrors.value[name]
  try {
    testResults.value[name] = await probeModel(name, [{ role: 'user', content: 'ping' }], 8)
  } catch (e) {
    testErrors.value[name] = e instanceof Error ? e.message : t('workTypes.testFailedShort')
    delete testResults.value[name]
  } finally {
    if (testingModel.value === name) testingModel.value = null
  }
}

async function testAllRoutes() {
  testingAll.value = true
  const all: ModelRoute[] = []
  for (const tier of EDITABLE_TIERS) {
    for (const rt of routesDraft.value[tier]) {
      if (rt.enabled !== false && rt.canonical_name.trim()) all.push(rt)
    }
  }
  for (const rt of all) {
    await testRoute(rt)
  }
  testingAll.value = false
}

async function doSyncACC() {
  syncMsg.value = ''
  syncOk.value = null
  try {
    const r = await syncWorkTypesFromACC()
    syncOk.value = r.synced
    syncMsg.value = r.message
    syncMeta.value = r.sync_meta ?? syncMeta.value
    await loadSettings()
    await loadOverview()
  } catch (e) {
    syncOk.value = false
    syncMsg.value = String(e)
  }
}

function goTab(tab: 'overview' | 'settings') {
  router.push(tab === 'settings' ? '/routing-v2/work-types/settings' : '/routing-v2/work-types')
}

function openDetail(key: string) {
  router.push(`/routing-v2/work-types/${key}`)
}

watch(() => route.fullPath, () => {
  if (activeTab.value === 'settings') loadSettings()
})

onMounted(async () => {
  await loadOverview()
  if (activeTab.value === 'settings') await loadSettings()
  // Refresh L1 taxonomy in background so dropdown reflects operator-added
  // categories. Composable seeds canonical 8 immediately so first paint
  // is never blank; this just upgrades the list once the response arrives.
  void refreshL1TaskTypes()
})
watch(activeTab, (tab) => {
  if (tab === 'settings') loadSettings()
})
</script>

<template>
  <div class="work-types-view" :class="{ 'work-types-view--detail': isDetailView }">
    <div class="top-bar">
      <div class="top-bar-head">
        <router-link to="/routing-v2" class="back-link">{{ t('workTypes.topBar.backToRouting') }}</router-link>
        <h2>{{ t('workTypes.topBar.title') }}</h2>
        <div class="seg-tabs">
          <button class="seg-tab" :class="{ active: activeTab === 'overview' }" @click="goTab('overview')">{{ t('workTypes.topBar.tabOverview') }}</button>
          <button class="seg-tab" :class="{ active: activeTab === 'settings' }" @click="goTab('settings')">{{ t('workTypes.topBar.tabSettings') }}</button>
        </div>
        <button class="btn btn-sm btn-ghost refresh-btn" @click="activeTab === 'overview' ? loadOverview() : loadSettings()" :title="t('workTypes.topBar.refresh')">↻</button>
      </div>
      <div class="hero-stats">
        <span class="chip">{{ t('workTypes.topBar.chipAuto24h') }} <strong>{{ stats?.total_auto ?? audit.total_auto_requests }}</strong></span>
        <span class="chip">{{ t('workTypes.topBar.chipType') }} <strong>{{ workTypes.length || wtStatsEntries.length }}</strong></span>
        <span class="chip">{{ t('workTypes.topBar.chipSuccessRate') }} <strong>{{ fmt(audit.success_rate * 100, 1) }}%</strong></span>
        <span v-if="syncMeta?.last_synced_at" class="chip">{{ t('workTypes.topBar.chipLastSync') }} <strong>{{ new Date(syncMeta.last_synced_at).toLocaleString() }}</strong></span>
      </div>
    </div>

    <!-- ═══ Overview ═══ -->
    <div v-if="activeTab === 'overview'" class="tab-content">
      <div class="overview-grid">
        <div class="card compact-card">
          <div class="section-head tight">
            <span class="layer-tag l1">L1</span>
            <h3>{{ t('workTypes.overview.autoStatsTitle') }}</h3>
          </div>
          <div class="stat-row">
            <div class="stat-block">
              <div class="stat-val">{{ audit.total_auto_requests }}</div>
              <div class="stat-lbl">{{ t('workTypes.overview.req7d') }}</div>
            </div>
            <div class="stat-block">
              <div class="stat-val">{{ fmt(audit.success_rate * 100, 1) }}%</div>
              <div class="stat-lbl">{{ t('workTypes.overview.successRate') }}</div>
            </div>
          </div>
          <div class="dist-mini">
            <div class="dist-col">
              <h4>{{ t('workTypes.overview.l1TasksTitle') }}</h4>
              <div v-for="[task, count] in distEntries(audit.task_distribution).slice(0, 5)" :key="task" class="dist-row">
                <span class="dist-label">{{ l1Label(task) }}</span>
                <div class="dist-bar-bg"><div class="dist-bar-fill" :style="{ width: (count / distMax(audit.task_distribution) * 100) + '%' }" /></div>
                <span class="dist-count">{{ count }}</span>
              </div>
            </div>
          </div>
        </div>

        <div class="card compact-card">
          <div class="section-head tight">
            <span class="layer-tag l1">WT</span>
            <h3>{{ t('workTypes.overview.wtDistTitle') }}</h3>
          </div>
          <div v-if="loading" class="loading-hint">{{ t('workTypes.loading') }}</div>
          <div v-else-if="wtStatsEntries.length" class="dist-col full">
            <div v-for="e in wtStatsEntries" :key="e.key" class="dist-row clickable" @click="router.push({ path: '/routing-v2', query: { tab: 'analytics', row: 'work_type', filter: e.key } })">
              <span class="dist-label" :title="e.key">{{ e.label }}</span>
              <div class="dist-bar-bg"><div class="dist-bar-fill accent" :style="{ width: (e.count_24h / wtStatsMax * 100) + '%' }" /></div>
              <span class="dist-count">{{ e.count_24h }}</span>
            </div>
          </div>
          <div v-else class="text-muted">{{ t('workTypes.overview.wtNoData') }}</div>
        </div>

        <div class="card compact-card">
          <div class="section-head tight"><h3>{{ t('workTypes.overview.modelTopTitle') }}</h3></div>
          <table v-if="stats?.top_models?.length" class="dense-table">
            <thead><tr><th>{{ t('workTypes.overview.modelTopTableModel') }}</th><th>{{ t('workTypes.overview.modelTopTableCount') }}</th></tr></thead>
            <tbody>
              <tr v-for="m in stats.top_models.slice(0, 8)" :key="m.model">
                <td class="model-name">{{ m.model }}</td>
                <td>{{ m.count }}</td>
              </tr>
            </tbody>
          </table>
          <div v-else class="text-muted">{{ t('workTypes.overview.noDataText') }}</div>
        </div>

        <div class="card compact-card span-2">
          <div class="section-head tight"><h3>{{ t('workTypes.overview.recentDecisionsTitle') }}</h3></div>
          <div class="table-wrap">
            <table v-if="decisions.length" class="dense-table">
              <thead><tr><th>{{ t('workTypes.overview.decisionsTableTime') }}</th><th>{{ t('workTypes.overview.decisionsTableL1') }}</th><th>{{ t('workTypes.overview.decisionsTableProfile') }}</th><th>{{ t('workTypes.overview.decisionsTableModel') }}</th><th>{{ t('workTypes.overview.decisionsTableStatus') }}</th></tr></thead>
              <tbody>
                <tr v-for="d in decisions" :key="d.request_id">
                  <td>{{ new Date(d.ts).toLocaleTimeString() }}</td>
                  <td><span class="badge badge-blue">{{ d.task_type || '-' }}</span></td>
                  <td>{{ d.auto_profile || '-' }}</td>
                  <td class="model-name">{{ d.outbound_model || d.auto_decision?.chosen_model || '-' }}</td>
                  <td><span :class="d.success ? 'badge badge-green' : 'badge badge-red'">{{ d.success ? '✓' : '✗' }}</span></td>
                </tr>
              </tbody>
            </table>
            <div v-else class="text-muted">{{ t('workTypes.overview.decisionsNoData') }}</div>
          </div>
        </div>
      </div>
    </div>

    <!-- ═══ Settings — Detail ═══ -->
    <div v-if="isDetailView && detail" class="tab-content detail-layout">
      <div class="detail-header card">
        <button class="btn btn-sm btn-ghost" @click="router.push('/routing-v2/work-types/settings')">{{ t('workTypes.detail.backToList') }}</button>
        <div class="detail-title-block">
          <h3>{{ detail.label }}</h3>
          <code class="key-code">{{ detail.key }}</code>
        </div>
        <span :class="detail.enabled ? 'badge badge-green' : 'badge badge-red'">
          {{ detail.enabled ? t('workTypes.enabled') : t('workTypes.disabled') }}
        </span>
        <button
          class="btn btn-sm"
          :class="detail.enabled ? 'btn-ghost' : 'btn-primary'"
          @click="toggleEnabled"
        >
          {{ detail.enabled ? t('workTypes.disable') : t('workTypes.enable') }}
        </button>
      </div>

      <div class="detail-grid">
        <section class="card detail-section">
          <div class="section-head">
            <span class="layer-tag l1">WT</span>
            <h3>{{ t('workTypes.detail.basicConfig') }}</h3>
            <button class="btn btn-primary btn-sm" :disabled="detailSaving" @click="saveDetailMeta">
              {{ detailSaving ? t('workTypes.saving') : t('workTypes.save') }}
            </button>
          </div>
          <div v-if="detailMsg" class="inline-msg">{{ detailMsg }}</div>
          <div class="detail-form">
            <label>{{ t('workTypes.modal.fields.name') }}<input v-model="detailForm.label" class="input" /></label>
            <label>{{ t('workTypes.modal.fields.category') }}
              <select v-model="detailForm.category" class="input">
                <option v-for="c in CATEGORIES" :key="c" :value="c">{{ c }}</option>
              </select>
            </label>
            <label>{{ t('workTypes.modal.fields.l1Task') }}
              <select v-model="detailForm.l1_task_type" class="input">
                <option v-for="t in l1TaskTypes" :key="t.key" :value="t.key">{{ t.label }}</option>
              </select>
            </label>
            <label>{{ t('workTypes.modal.fields.profile') }}
              <select v-model="detailForm.default_profile" class="input">
                <option v-for="p in PROFILES" :key="p.key" :value="p.key">{{ p.label }}</option>
              </select>
            </label>
            <label>{{ t('workTypes.modal.fields.sortOrder') }}<input v-model.number="detailForm.sort_order" type="number" class="input" /></label>
            <label class="span-2">{{ t('workTypes.modal.fields.tags') }}<input v-model="detailForm.tags" class="input" /></label>
          </div>
        </section>

        <section class="card detail-section detail-section--intent">
          <div class="section-head">
            <span class="layer-tag intent-tag">IR</span>
            <h3>{{ t('workTypes.detail.intentRecogTitle') }}</h3>
            <span class="text-muted">{{ t('workTypes.detail.keywordCountLabel', { n: detailForm.prompt_keywords.length }) }}</span>
          </div>
          <p class="text-muted intent-hint">{{ t('workTypes.detail.intentRecogHint') }}</p>
          <div class="keyword-chips">
            <span
              v-for="kw in detailForm.prompt_keywords"
              :key="kw"
              class="kw-chip"
            >
              <span class="kw-chip-label">{{ kw }}</span>
              <button
                type="button"
                class="kw-chip-x"
                :title="t('workTypes.detail.removeKeyword')"
                :aria-label="`${t('workTypes.detail.removeKeyword')}: ${kw}`"
                @click="removeKeyword(kw)"
              >×</button>
            </span>
            <input
              v-model="keywordInput"
              class="kw-input"
              :placeholder="detailForm.prompt_keywords.length ? '' : t('workTypes.detail.promptKeywordsPlaceholder')"
              @keydown="onKeywordKeydown"
              @blur="addKeyword"
            />
          </div>
          <div class="acc-mapping">
            <label>
              <span class="field-label">{{ t('workTypes.detail.accTaskTypeLabel') }}</span>
              <input
                v-model="detailForm.acc_task_type"
                class="input"
                :placeholder="t('workTypes.detail.accTaskTypePlaceholder')"
              />
            </label>
            <p class="text-muted field-hint">{{ t('workTypes.detail.accTaskTypeHint') }}</p>
          </div>
          <div class="intent-actions">
            <button class="btn btn-primary btn-sm" :disabled="detailSaving" @click="saveDetailMeta">
              {{ detailSaving ? t('workTypes.saving') : t('workTypes.save') }}
            </button>
          </div>
        </section>

        <section class="card detail-section detail-section--routes">
          <div class="section-head">
            <span class="layer-tag l2">L2</span>
            <h3>{{ t('workTypes.detail.modelTypeRoutes') }}</h3>
            <span class="text-muted route-hint">{{ t('workTypes.layers.maxRoutesPerLayer', { n: MAX_ROUTES_PER_LAYER }) }}</span>
            <button class="btn btn-primary btn-sm" :disabled="routesSaving" @click="saveRoutes">
              {{ routesSaving ? t('workTypes.saving') : t('workTypes.saveBtn') }}
            </button>
          </div>
          <div v-if="routesMsg" class="inline-msg">{{ routesMsg }}</div>

          <div v-if="!totalRouteCount()" class="empty-routes">
            {{ t('workTypes.detail.emptyRoutes') }}
          </div>

          <div class="layer-stack">
            <div
              v-for="tier in EDITABLE_TIERS"
              :key="tier"
              class="layer-block"
              :class="`layer-block--${tier}`"
            >
              <header class="layer-head">
                <span class="layer-pill" :class="`layer-pill--${tier}`">
                  {{ t(`workTypes.layers.${tier}`) }}
                </span>
                <span class="layer-count">{{ routesDraft[tier].length }}/{{ MAX_ROUTES_PER_LAYER }}</span>
                <span class="layer-hint">{{ t(`workTypes.layers.${tier}Hint`) }}</span>
                <span class="layer-spacer"></span>
                <button
                  class="btn btn-ghost btn-sm"
                  :disabled="routesDraft[tier].length >= MAX_ROUTES_PER_LAYER"
                  @click="addRouteRow(tier)"
                >{{ t(`workTypes.layers.add${tier === 'primary' ? 'Primary' : 'Secondary'}`) }}</button>
              </header>

              <div v-if="!routesDraft[tier].length" class="layer-empty">
                {{ t(`workTypes.layers.empty${tier === 'primary' ? 'Primary' : 'Secondary'}`) }}
              </div>

              <ol class="route-cards">
                <li
                  v-for="(rt, i) in routesDraft[tier]"
                  :key="`${tier}-${i}-${rt.canonical_name}`"
                  class="route-card"
                  :class="{
                    'route-card--disabled': rt.enabled === false,
                    'route-card--dragging': dragState.tier === tier && dragState.index === i,
                  }"
                  draggable="true"
                  @dragstart="onRouteDragStart(tier, i)"
                  @dragover="onRouteDragOver($event, tier, i)"
                  @dragend="onRouteDragEnd"
                >
                  <div class="route-card-head">
                    <span class="route-drag-handle" :title="t('workTypes.layers.dragToReorder')" aria-hidden="true">☰</span>
                    <span class="route-index">{{ t('workTypes.detail.routeNum', { n: i + 1 }) }}</span>
                    <span class="route-tier-tag">{{ t('workTypes.detail.routeTier') }}: {{ t(`workTypes.layers.${tier}`) }}</span>
                    <label class="route-enabled">
                      <input type="checkbox" v-model="rt.enabled" />
                      {{ t('workTypes.detail.routeEnabled') }}
                    </label>
                    <button class="btn btn-ghost btn-sm route-remove" @click="removeRouteRow(tier, i)">{{ t('workTypes.detail.removeRoute') }}</button>
                  </div>
                  <div class="route-picker-row">
                    <span class="field-label">{{ t('workTypes.detail.canonicalModel') }}</span>
                    <ModelPicker
                      v-model="rt.canonical_name"
                      :placeholder="t('workTypes.detail.selectModelPlaceholder')"
                      :title="`${detail.label} · ${t(`workTypes.layers.${tier}`)} · ${t('workTypes.detail.routeNum', { n: i + 1 })}`"
                    />
                  </div>
                  <div class="route-fields">
                    <label>{{ t('workTypes.detail.weight') }}
                      <input v-model.number="rt.weight" type="number" step="0.1" min="0.1" class="input compact" />
                    </label>
                    <label>{{ t('workTypes.detail.minScore') }}
                      <input v-model.number="rt.min_score" type="number" step="0.1" class="input compact" />
                    </label>
                    <button
                      class="btn btn-ghost btn-sm"
                      :disabled="!rt.canonical_name.trim() || testingModel === rt.canonical_name"
                      @click="testRoute(rt)"
                    >
                      {{ testingModel === rt.canonical_name ? t('workTypes.testing') : t('workTypes.test') }}
                    </button>
                  </div>
                  <div v-if="testErrors[rt.canonical_name]" class="test-result test-result--fail">
                    {{ testErrors[rt.canonical_name] }}
                  </div>
                  <div v-else-if="testResults[rt.canonical_name]" class="test-result" :class="testResults[rt.canonical_name].success ? 'test-result--ok' : 'test-result--fail'">
                    <span>{{ testResults[rt.canonical_name].success ? t('workTypes.testOk') : t('workTypes.testFail') }}</span>
                    <span v-if="testResults[rt.canonical_name].latency_ms != null">{{ testResults[rt.canonical_name].latency_ms }}ms</span>
                    <span v-if="testResults[rt.canonical_name].model_name">{{ testResults[rt.canonical_name].provider_name }}</span>
                    <span v-if="testResults[rt.canonical_name].error" class="test-err">{{ testResults[rt.canonical_name].error }}</span>
                  </div>
                </li>
              </ol>
            </div>
          </div>

          <div v-if="totalRouteCount()" class="route-actions">
            <button class="btn btn-ghost btn-sm" :disabled="testingAll" @click="testAllRoutes">
              {{ testingAll ? t('workTypes.testingAll') : t('workTypes.testAll') }}
            </button>
          </div>
        </section>
      </div>
    </div>

    <div v-else-if="activeTab === 'settings' && settingsLoading && detailKey" class="loading-hint">{{ t('workTypes.loadingDetail') }}</div>

    <!-- ═══ Settings — List ═══ -->
    <div v-else-if="activeTab === 'settings'" class="tab-content">
      <div class="card compact-card">
        <div class="card-toolbar">
          <div class="toolbar-left">
            <span class="layer-tag l1">WT</span>
            <span class="toolbar-title">{{ t('workTypes.list.title') }}</span>
            <span class="text-muted">({{ workTypes.length }})</span>
          </div>
          <div class="toolbar-filters">
            <button class="btn btn-sm btn-ghost" @click="doSyncACC" :title="t('workTypes.list.syncFromACCTooltip')">{{ t('workTypes.list.syncFromACC') }}</button>
            <button class="btn btn-primary btn-sm" @click="openCreate">{{ t('workTypes.list.create') }}</button>
          </div>
        </div>
        <p class="list-hint">{{ t('workTypes.list.hint') }}</p>
        <div v-if="syncMsg" class="policy-msg" :class="{ 'sync-ok': syncOk, 'sync-err': syncOk === false }">{{ syncMsg }}</div>
        <div v-if="settingsLoading" class="loading-hint">{{ t('workTypes.loading') }}</div>
        <div v-else class="table-wrap">
          <table class="dense-table list-table">
            <thead>
              <tr>
                <th>{{ t('workTypes.list.tableHeaders.index') }}</th>
                <th>{{ t('workTypes.list.tableHeaders.key') }}</th>
                <th>{{ t('workTypes.list.tableHeaders.name') }}</th>
                <th>{{ t('workTypes.list.tableHeaders.category') }}</th>
                <th>{{ t('workTypes.list.tableHeaders.l1') }}</th>
                <th>{{ t('workTypes.list.tableHeaders.profile') }}</th>
                <th>{{ t('workTypes.list.tableHeaders.routes') }}</th>
                <th>{{ t('workTypes.list.tableHeaders.status') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="(wt, i) in workTypes"
                :key="wt.key"
                class="model-row"
                :class="{ disabled: !wt.enabled }"
                tabindex="0"
                @click="openDetail(wt.key)"
                @keydown.enter="openDetail(wt.key)"
              >
                <td class="num">{{ wt.sort_order || i + 1 }}</td>
                <td><code class="key-code">{{ wt.key }}</code></td>
                <td>{{ wt.label }}</td>
                <td><span class="badge badge-gray">{{ wt.category }}</span></td>
                <td>{{ l1Label(wt.l1_task_type) }}</td>
                <td>{{ profileLabel(wt.default_profile) }}</td>
                <td class="route-cell">
                  <span v-if="!routeSummary(wt).length" class="text-muted">{{ t('workTypes.list.notConfigured') }}</span>
                  <template v-else>
                    <span class="route-tier-summary">
                      <span class="route-tier-summary__pill route-tier-summary__pill--primary">
                        {{ routeTierCount(wt, 'primary') }} {{ t('workTypes.layers.primary') }}
                      </span>
                      <span class="route-tier-summary__pill route-tier-summary__pill--secondary">
                        {{ routeTierCount(wt, 'secondary') }} {{ t('workTypes.layers.secondary') }}
                      </span>
                    </span>
                    <span v-for="m in routeSummary(wt)" :key="m" class="route-chip">{{ m }}</span>
                  </template>
                </td>
                <td><span :class="wt.enabled ? 'badge badge-green' : 'badge badge-red'">{{ wt.enabled ? t('workTypes.list.rowEnabled') : t('workTypes.list.rowDisabled') }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <!-- Create Modal -->
    <div v-if="showCreateModal" class="modal-overlay" @click.self="showCreateModal = false">
      <div class="modal-card">
        <h3>{{ t('workTypes.modal.title') }}</h3>
        <div class="form-grid">
          <label class="span-2">{{ t('workTypes.modal.fields.key') }} <input v-model="createForm.key" :placeholder="t('workTypes.modal.keyPlaceholder')" /></label>
          <label>{{ t('workTypes.modal.fields.name') }} <input v-model="createForm.label" /></label>
          <label>{{ t('workTypes.modal.fields.category') }}
            <select v-model="createForm.category">
              <option v-for="c in CATEGORIES" :key="c" :value="c">{{ c }}</option>
            </select>
          </label>
          <label>{{ t('workTypes.modal.fields.l1Task') }}
            <select v-model="createForm.l1_task_type">
              <option v-for="t in l1TaskTypes" :key="t.key" :value="t.key">{{ t.label }}</option>
            </select>
          </label>
          <label>{{ t('workTypes.modal.fields.profile') }}
            <select v-model="createForm.default_profile">
              <option v-for="p in PROFILES" :key="p.key" :value="p.key">{{ p.label }}</option>
            </select>
          </label>
          <label>{{ t('workTypes.modal.fields.sortOrder') }} <input v-model.number="createForm.sort_order" type="number" /></label>
          <label class="span-2">{{ t('workTypes.modal.fields.tags') }}<input v-model="createForm.tags" /></label>
          <label class="span-2">{{ t('workTypes.modal.fields.promptKeywords') }} <input v-model="createForm.prompt_keywords" /></label>
          <label class="span-2">
            <span class="field-label">{{ t('workTypes.detail.accTaskTypeLabel') }}</span>
            <input v-model="createForm.acc_task_type" :placeholder="t('workTypes.detail.accTaskTypePlaceholder')" />
          </label>
        </div>
        <div v-if="createError" class="alert alert-danger compact-alert">{{ createError }}</div>
        <div class="modal-actions">
          <button class="btn btn-ghost" @click="showCreateModal = false">{{ t('workTypes.modal.cancel') }}</button>
          <button class="btn btn-primary" @click="saveCreate">{{ t('workTypes.modal.submit') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.work-types-view { max-width: 1200px; }
.work-types-view--detail { max-width: min(1400px, 96vw); }

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
.top-bar-head h2 { font-size: 15px; margin: 0; }
.back-link { font-size: 11px; color: var(--muted); text-decoration: none; }
.back-link:hover { color: var(--accent-h); }
.refresh-btn { margin-left: auto; }

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
}
.seg-tab.active {
  background: var(--card);
  color: var(--text);
  font-weight: 600;
  box-shadow: 0 1px 2px rgba(0,0,0,.12);
}

.hero-stats { display: flex; flex-wrap: wrap; gap: 4px; }
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

.tab-content { display: flex; flex-direction: column; gap: 8px; }

.overview-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}
.overview-grid .span-2 { grid-column: span 2; }

.compact-card { padding: 8px 10px; }
.section-head {
  display: flex; align-items: center; gap: 6px;
  margin-bottom: 6px;
  flex-wrap: wrap;
}
.section-head.tight { margin-bottom: 4px; }
.section-head h3 { margin: 0; font-size: 12px; font-weight: 600; }

.layer-tag {
  display: inline-flex; align-items: center; justify-content: center;
  width: 22px; height: 14px;
  border-radius: 3px;
  font-size: 8px; font-weight: 700;
}
.layer-tag.l1 { background: color-mix(in srgb, var(--accent) 22%, transparent); color: var(--accent-h); }
.layer-tag.l2 { background: rgba(63,185,80,.22); color: var(--success); }
.layer-tag.intent-tag { background: rgba(210,153,34,.22); color: var(--warning); width: 26px; }

.detail-section--intent { display: flex; flex-direction: column; gap: 8px; }
.intent-hint { margin: 0; font-size: 11px; line-height: 1.4; }
.keyword-chips {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  min-height: 38px;
  padding: 6px 8px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 6px;
}
.kw-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 4px 2px 8px;
  background: rgba(210,153,34,.15);
  border: 1px solid rgba(210,153,34,.4);
  border-radius: 99px;
  font-size: 11px;
  color: var(--warning);
  max-width: 100%;
}
.kw-chip-label { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 240px; }
.kw-chip-x {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  padding: 0;
  border: none;
  border-radius: 50%;
  background: transparent;
  color: inherit;
  font-size: 14px;
  line-height: 1;
  cursor: pointer;
}
.kw-chip-x:hover { background: rgba(210,153,34,.3); }
.kw-input {
  flex: 1;
  min-width: 160px;
  padding: 4px 6px;
  background: transparent;
  border: none;
  outline: none;
  color: var(--text);
  font-size: 12px;
}
.kw-input:focus { outline: none; }
.acc-mapping { margin-top: 4px; }
.acc-mapping label { display: flex; flex-direction: column; gap: 4px; }
.acc-mapping .field-label { font-size: 11px; color: var(--muted); }
.field-hint { margin: 6px 0 0; font-size: 11px; line-height: 1.4; }
.intent-actions { display: flex; justify-content: flex-end; margin-top: 6px; }

.stat-row { display: flex; gap: 16px; margin-bottom: 8px; }
.stat-block { text-align: center; }
.stat-val { font-size: 18px; font-weight: 700; }
.stat-lbl { font-size: 9px; color: var(--muted); }

.dist-mini { display: grid; grid-template-columns: 1fr; gap: 8px; }
.dist-col.full { width: 100%; }
.dist-col h4 { font-size: 9px; text-transform: uppercase; color: var(--muted); margin: 0 0 4px; }
.dist-row.clickable { cursor: pointer; }
.dist-row.clickable:hover { background: var(--bg-subtle); }
.dist-row {
  display: grid;
  grid-template-columns: 72px 1fr 28px;
  align-items: center;
  gap: 4px;
  margin-bottom: 2px;
  font-size: 10px;
}
.dist-label { color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dist-bar-bg { height: 6px; background: color-mix(in srgb, var(--border) 30%, transparent); border-radius: 2px; overflow: hidden; }
.dist-bar-fill { height: 100%; background: var(--success); border-radius: 2px; }
.dist-bar-fill.accent { background: var(--accent); }
.dist-count { text-align: right; font-variant-numeric: tabular-nums; }

.dense-table { font-size: 11px; width: 100%; }
.dense-table thead th { padding: 3px 6px; font-size: 9px; }
.dense-table tbody td { padding: 4px 6px; }
.dense-table .num { color: var(--muted); width: 24px; }
.list-table tbody td { padding: 8px 6px; }
.model-name { font-weight: 500; font-size: 11px; }
.model-row { cursor: pointer; }
.model-row:hover { background: rgba(255,255,255,.04); }
.model-row:focus-visible { outline: 1px solid var(--accent); outline-offset: -1px; }
.model-row.disabled { opacity: 0.55; }

.key-code { font-size: 10px; font-family: ui-monospace, monospace; color: var(--accent-h); }

.card-toolbar {
  display: flex; align-items: center; justify-content: space-between;
  gap: 6px; flex-wrap: wrap;
  margin-bottom: 6px; padding-bottom: 6px;
  border-bottom: 1px solid var(--border);
}
.toolbar-left { display: flex; align-items: center; gap: 6px; }
.toolbar-title { font-size: 12px; font-weight: 600; }
.toolbar-filters { display: flex; gap: 4px; }
.list-hint { font-size: 11px; color: var(--muted); margin: 0 0 8px; }

.route-cell { display: flex; flex-wrap: wrap; gap: 4px; max-width: 280px; }
.route-chip {
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 10px;
  font-family: ui-monospace, monospace;
  background: color-mix(in srgb, var(--accent) 12%, transparent);
  color: var(--accent-h);
  border: 1px solid color-mix(in srgb, var(--accent) 25%, transparent);
}

.loading-hint { padding: 12px; text-align: center; color: var(--muted); font-size: 11px; }
.text-muted { color: var(--muted); }
.policy-msg { font-size: 11px; color: var(--accent-h); margin-bottom: 4px; }
.policy-msg.sync-ok { color: var(--success); }
.policy-msg.sync-err { color: var(--danger); }

/* Detail layout */
.detail-layout { gap: 12px; }
.detail-header {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  padding: 12px 16px;
}
.detail-title-block { flex: 1; min-width: 200px; }
.detail-title-block h3 { margin: 0 0 4px; font-size: 18px; }
.detail-grid {
  display: grid;
  grid-template-columns: minmax(320px, 1fr) minmax(420px, 1.4fr);
  gap: 12px;
  align-items: start;
}
.detail-section { padding: 16px; }
.detail-section--routes { min-height: 360px; }
.route-hint { font-size: 11px; margin-right: auto; }

.detail-form {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
.detail-form label {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 11px;
  color: var(--muted);
}
.detail-form label.span-2 { grid-column: span 2; }
.detail-form .input { font-size: 13px; }
.detail-form .input.compact { max-width: 120px; }

.inline-msg {
  font-size: 11px;
  color: var(--accent-h);
  margin-bottom: 8px;
}

.route-cards { display: flex; flex-direction: column; gap: 12px; }

/* Two-layer UI: one .layer-block per tier (primary, secondary). Each
 * block has its own header (pill + count + hint + add button) and its
 * own list of .route-card items, draggable within the block. */
.layer-stack { display: flex; flex-direction: column; gap: 16px; }
.layer-block {
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 10px 12px 12px;
  background: var(--card);
}
.layer-block--primary { border-left: 3px solid var(--success); }
.layer-block--secondary { border-left: 3px solid color-mix(in srgb, var(--accent) 60%, transparent); }
.layer-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}
.layer-pill {
  display: inline-flex; align-items: center;
  padding: 2px 8px;
  border-radius: 99px;
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.04em;
  text-transform: uppercase;
}
.layer-pill--primary {
  background: rgba(63,185,80,.18);
  color: var(--success);
  border: 1px solid rgba(63,185,80,.4);
}
.layer-pill--secondary {
  background: color-mix(in srgb, var(--accent) 18%, transparent);
  color: var(--accent-h);
  border: 1px solid color-mix(in srgb, var(--accent) 35%, transparent);
}
.layer-count {
  font-size: 10px;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}
.layer-hint {
  font-size: 10px;
  color: var(--muted);
  flex: 0 1 auto;
}
.layer-spacer { flex: 1 1 auto; }
.layer-empty {
  padding: 12px;
  text-align: center;
  color: var(--muted);
  font-size: 11px;
  border: 1px dashed var(--border);
  border-radius: var(--radius);
}

.route-card {
  padding: 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg-subtle);
  list-style: none;
}
.route-card--disabled { opacity: 0.65; }
.route-card--dragging {
  opacity: 0.55;
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 8%, var(--bg-subtle));
}
.route-card[draggable="true"] { cursor: grab; }
.route-card[draggable="true"]:active { cursor: grabbing; }
.route-card-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
  flex-wrap: wrap;
}
.route-drag-handle {
  font-size: 14px;
  line-height: 1;
  color: var(--muted);
  padding: 2px 6px;
  border-radius: 4px;
  user-select: none;
  cursor: grab;
}
.route-drag-handle:hover { background: var(--bg); color: var(--text); }
.route-index {
  font-size: 11px;
  font-weight: 700;
  color: var(--muted);
}
.route-tier-tag {
  font-size: 10px;
  color: var(--muted);
  background: var(--bg);
  border: 1px solid var(--border);
  padding: 1px 6px;
  border-radius: 99px;
}
.route-enabled {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  color: var(--muted);
  margin-right: auto;
}
.route-remove { margin-left: auto; }
.route-picker-row {
  margin-bottom: 10px;
}
.field-label {
  display: block;
  font-size: 10px;
  color: var(--muted);
  margin-bottom: 4px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.route-fields {
  display: flex;
  align-items: flex-end;
  gap: 12px;
  flex-wrap: wrap;
}
.route-fields label {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 11px;
  color: var(--muted);
}

.route-tier-summary { display: inline-flex; gap: 4px; margin-right: 4px; }
.route-tier-summary__pill {
  padding: 1px 6px;
  border-radius: 99px;
  font-size: 10px;
  font-weight: 600;
  border: 1px solid var(--border);
}
.route-tier-summary__pill--primary {
  background: rgba(63,185,80,.12);
  color: var(--success);
  border-color: rgba(63,185,80,.35);
}
.route-tier-summary__pill--secondary {
  background: color-mix(in srgb, var(--accent) 10%, transparent);
  color: var(--accent-h);
  border-color: color-mix(in srgb, var(--accent) 25%, transparent);
}
.empty-routes {
  padding: 24px;
  text-align: center;
  color: var(--muted);
  font-size: 13px;
  border: 1px dashed var(--border);
  border-radius: var(--radius);
  margin-bottom: 12px;
}
.route-actions { margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--border); }

.test-result {
  margin-top: 8px;
  padding: 8px 10px;
  border-radius: 6px;
  font-size: 11px;
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}
.test-result--ok {
  background: rgba(63,185,80,.12);
  border: 1px solid rgba(63,185,80,.35);
  color: var(--success);
}
.test-result--fail {
  background: rgba(248,81,73,.1);
  border: 1px solid rgba(248,81,73,.35);
  color: var(--danger);
}
.test-err { flex: 1 1 100%; word-break: break-word; }

.modal-overlay {
  position: fixed; inset: 0;
  background: rgba(0,0,0,.5);
  display: flex; align-items: center; justify-content: center;
  z-index: 1000;
}
.modal-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 16px;
  width: min(480px, 92vw);
}
.modal-card h3 { margin: 0 0 12px; font-size: 14px; }
.form-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
}
.form-grid label {
  display: flex; flex-direction: column; gap: 3px;
  font-size: 10px; color: var(--muted);
}
.form-grid label.span-2 { grid-column: span 2; }
.form-grid input, .form-grid select { font-size: 12px; padding: 4px 6px; }
.modal-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 12px; }
.compact-alert { margin-top: 8px; padding: 8px; font-size: 11px; }

@media (max-width: 960px) {
  .overview-grid { grid-template-columns: 1fr; }
  .overview-grid .span-2 { grid-column: span 1; }
  .detail-grid { grid-template-columns: 1fr; }
}
</style>
