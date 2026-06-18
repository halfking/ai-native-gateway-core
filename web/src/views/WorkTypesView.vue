<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  listWorkTypes, getWorkType, createWorkType, updateWorkType, deleteWorkType,
  putWorkTypeRoutes, getWorkTypeStats, syncWorkTypesFromACC,
  L1_TASK_TYPES, PROFILES, CATEGORIES,
  type WorkTypeConfig, type WorkTypeStats, type ModelRoute, type WorkTypeSyncMeta,
} from '../api-work-types'
import {
  getAutoRouteAudit, getAutoRouteDecisions,
  type AutoRouteAudit, type AutoRouteDecision,
} from '../api-autoroute'
import { probeModel, type ProbeResult } from '../api'
import ModelPicker from '../components/ModelPicker.vue'

const MAX_ROUTES = 3

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
function l1Label(key: string): string {
  return L1_TASK_TYPES.find(t => t.key === key)?.label ?? key
}
function profileLabel(key: string): string {
  return PROFILES.find(p => p.key === key)?.label ?? key
}

function routeSummary(wt: WorkTypeConfig): string[] {
  const routes = wt.model_routes ?? []
  return routes.filter(r => r.enabled !== false && r.canonical_name).map(r => r.canonical_name).slice(0, MAX_ROUTES)
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
})
const createError = ref('')

const detail = ref<WorkTypeConfig | null>(null)
const detailForm = ref({
  label: '', category: '通用', l1_task_type: 'chat',
  default_profile: 'smart' as 'smart' | 'speed_first' | 'cost_first',
  tags: '', prompt_keywords: '', sort_order: 0,
})
const detailSaving = ref(false)
const detailMsg = ref('')

const routesDraft = ref<ModelRoute[]>([])
const routesSaving = ref(false)
const routesMsg = ref('')

const testResults = ref<Record<string, ProbeResult>>({})
const testErrors = ref<Record<string, string>>({})
const testingModel = ref<string | null>(null)
const testingAll = ref(false)

function syncDetailForm(wt: WorkTypeConfig) {
  detailForm.value = {
    label: wt.label,
    category: wt.category,
    l1_task_type: wt.l1_task_type,
    default_profile: wt.default_profile,
    tags: wt.tags.join(', '),
    prompt_keywords: wt.prompt_keywords.join(', '),
    sort_order: wt.sort_order,
  }
}

async function loadSettings() {
  settingsLoading.value = true
  try {
    workTypes.value = await listWorkTypes(true)
    if (detailKey.value) {
      detail.value = await getWorkType(detailKey.value)
      syncDetailForm(detail.value)
      routesDraft.value = (detail.value.model_routes ?? []).slice(0, MAX_ROUTES).map(r => ({ ...r }))
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
  }
  createError.value = ''
  showCreateModal.value = true
}

async function saveCreate() {
  createError.value = ''
  const tags = createForm.value.tags.split(/[,，]/).map(s => s.trim()).filter(Boolean)
  const kw = createForm.value.prompt_keywords.split(/[,，]/).map(s => s.trim()).filter(Boolean)
  try {
    const wt = await createWorkType({
      key: createForm.value.key.trim(),
      label: createForm.value.label.trim(),
      category: createForm.value.category,
      l1_task_type: createForm.value.l1_task_type,
      default_profile: createForm.value.default_profile,
      tags, prompt_keywords: kw,
      sort_order: createForm.value.sort_order,
      enabled: createForm.value.enabled,
    })
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
  const kw = detailForm.value.prompt_keywords.split(/[,，]/).map(s => s.trim()).filter(Boolean)
  try {
    detail.value = await updateWorkType(detailKey.value, {
      label: detailForm.value.label.trim(),
      category: detailForm.value.category,
      l1_task_type: detailForm.value.l1_task_type,
      default_profile: detailForm.value.default_profile,
      tags, prompt_keywords: kw,
      sort_order: detailForm.value.sort_order,
    })
    syncDetailForm(detail.value)
    detailMsg.value = '基本配置已保存'
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
  const msg = next ? '启用' : '禁用'
  if (!next && !confirm(`确定${msg}工作类型「${detail.value.label}」？`)) return
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

async function saveRoutes() {
  if (!detailKey.value) return
  const payload = routesDraft.value
    .filter(r => r.canonical_name.trim())
    .slice(0, MAX_ROUTES)
  routesSaving.value = true
  routesMsg.value = ''
  try {
    await putWorkTypeRoutes(detailKey.value, payload)
    detail.value = await getWorkType(detailKey.value)
    routesDraft.value = (detail.value.model_routes ?? []).slice(0, MAX_ROUTES).map(r => ({ ...r }))
    routesMsg.value = '模型路由已保存'
    await loadSettings()
  } catch (e) {
    routesMsg.value = String(e)
  } finally {
    routesSaving.value = false
  }
}

function addRouteRow() {
  if (routesDraft.value.length >= MAX_ROUTES) return
  routesDraft.value.push({ canonical_name: '', weight: 1, min_score: 0, enabled: true })
}

function removeRouteRow(i: number) {
  routesDraft.value.splice(i, 1)
}

async function testRoute(rt: ModelRoute) {
  const name = rt.canonical_name.trim()
  if (!name) return
  testingModel.value = name
  delete testErrors.value[name]
  try {
    testResults.value[name] = await probeModel(name, [{ role: 'user', content: 'ping' }], 8)
  } catch (e) {
    testErrors.value[name] = e instanceof Error ? e.message : '测试失败'
    delete testResults.value[name]
  } finally {
    if (testingModel.value === name) testingModel.value = null
  }
}

async function testAllRoutes() {
  testingAll.value = true
  for (const rt of routesDraft.value.filter(r => r.enabled !== false && r.canonical_name.trim())) {
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
})
watch(activeTab, (tab) => {
  if (tab === 'settings') loadSettings()
})
</script>

<template>
  <div class="work-types-view" :class="{ 'work-types-view--detail': isDetailView }">
    <div class="top-bar">
      <div class="top-bar-head">
        <router-link to="/routing-v2" class="back-link">← 路由全景</router-link>
        <h2>工作类型</h2>
        <div class="seg-tabs">
          <button class="seg-tab" :class="{ active: activeTab === 'overview' }" @click="goTab('overview')">概览</button>
          <button class="seg-tab" :class="{ active: activeTab === 'settings' }" @click="goTab('settings')">配置</button>
        </div>
        <button class="btn btn-sm btn-ghost refresh-btn" @click="activeTab === 'overview' ? loadOverview() : loadSettings()" title="刷新">↻</button>
      </div>
      <div class="hero-stats">
        <span class="chip">Auto 24h <strong>{{ stats?.total_auto ?? audit.total_auto_requests }}</strong></span>
        <span class="chip">类型 <strong>{{ workTypes.length || wtStatsEntries.length }}</strong></span>
        <span class="chip">成功率 <strong>{{ fmt(audit.success_rate * 100, 1) }}%</strong></span>
        <span v-if="syncMeta?.last_synced_at" class="chip">上次同步 <strong>{{ new Date(syncMeta.last_synced_at).toLocaleString() }}</strong></span>
      </div>
    </div>

    <!-- ═══ Overview ═══ -->
    <div v-if="activeTab === 'overview'" class="tab-content">
      <div class="overview-grid">
        <div class="card compact-card">
          <div class="section-head tight">
            <span class="layer-tag l1">L1</span>
            <h3>Auto 总统计</h3>
          </div>
          <div class="stat-row">
            <div class="stat-block">
              <div class="stat-val">{{ audit.total_auto_requests }}</div>
              <div class="stat-lbl">7d 请求</div>
            </div>
            <div class="stat-block">
              <div class="stat-val">{{ fmt(audit.success_rate * 100, 1) }}%</div>
              <div class="stat-lbl">成功率</div>
            </div>
          </div>
          <div class="dist-mini">
            <div class="dist-col">
              <h4>L1 任务</h4>
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
            <h3>工作类型分布 (24h)</h3>
          </div>
          <div v-if="loading" class="loading-hint">加载…</div>
          <div v-else-if="wtStatsEntries.length" class="dist-col full">
            <div v-for="e in wtStatsEntries" :key="e.key" class="dist-row clickable" @click="router.push({ path: '/routing-v2', query: { tab: 'analytics', row: 'work_type', filter: e.key } })">
              <span class="dist-label" :title="e.key">{{ e.label }}</span>
              <div class="dist-bar-bg"><div class="dist-bar-fill accent" :style="{ width: (e.count_24h / wtStatsMax * 100) + '%' }" /></div>
              <span class="dist-count">{{ e.count_24h }}</span>
            </div>
          </div>
          <div v-else class="text-muted">暂无 24h 数据</div>
        </div>

        <div class="card compact-card">
          <div class="section-head tight"><h3>模型 Top (24h)</h3></div>
          <table v-if="stats?.top_models?.length" class="dense-table">
            <thead><tr><th>模型</th><th>次数</th></tr></thead>
            <tbody>
              <tr v-for="m in stats.top_models.slice(0, 8)" :key="m.model">
                <td class="model-name">{{ m.model }}</td>
                <td>{{ m.count }}</td>
              </tr>
            </tbody>
          </table>
          <div v-else class="text-muted">暂无</div>
        </div>

        <div class="card compact-card span-2">
          <div class="section-head tight"><h3>最近路由决策</h3></div>
          <div class="table-wrap">
            <table v-if="decisions.length" class="dense-table">
              <thead><tr><th>时间</th><th>L1</th><th>Profile</th><th>模型</th><th>状态</th></tr></thead>
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
            <div v-else class="text-muted">暂无 auto 决策</div>
          </div>
        </div>
      </div>
    </div>

    <!-- ═══ Settings — Detail ═══ -->
    <div v-if="isDetailView && detail" class="tab-content detail-layout">
      <div class="detail-header card">
        <button class="btn btn-sm btn-ghost" @click="router.push('/routing-v2/work-types/settings')">← 返回列表</button>
        <div class="detail-title-block">
          <h3>{{ detail.label }}</h3>
          <code class="key-code">{{ detail.key }}</code>
        </div>
        <span :class="detail.enabled ? 'badge badge-green' : 'badge badge-red'">
          {{ detail.enabled ? '已启用' : '已禁用' }}
        </span>
        <button
          class="btn btn-sm"
          :class="detail.enabled ? 'btn-ghost' : 'btn-primary'"
          @click="toggleEnabled"
        >
          {{ detail.enabled ? '禁用' : '启用' }}
        </button>
      </div>

      <div class="detail-grid">
        <section class="card detail-section">
          <div class="section-head">
            <span class="layer-tag l1">WT</span>
            <h3>基本配置</h3>
            <button class="btn btn-primary btn-sm" :disabled="detailSaving" @click="saveDetailMeta">
              {{ detailSaving ? '保存中…' : '保存' }}
            </button>
          </div>
          <div v-if="detailMsg" class="inline-msg">{{ detailMsg }}</div>
          <div class="detail-form">
            <label>名称<input v-model="detailForm.label" class="input" /></label>
            <label>分类
              <select v-model="detailForm.category" class="input">
                <option v-for="c in CATEGORIES" :key="c" :value="c">{{ c }}</option>
              </select>
            </label>
            <label>L1 任务
              <select v-model="detailForm.l1_task_type" class="input">
                <option v-for="t in L1_TASK_TYPES" :key="t.key" :value="t.key">{{ t.label }}</option>
              </select>
            </label>
            <label>Profile
              <select v-model="detailForm.default_profile" class="input">
                <option v-for="p in PROFILES" :key="p.key" :value="p.key">{{ p.label }}</option>
              </select>
            </label>
            <label>排序<input v-model.number="detailForm.sort_order" type="number" class="input" /></label>
            <label class="span-2">Tags（逗号分隔）<input v-model="detailForm.tags" class="input" /></label>
            <label class="span-2">Prompt 关键词<input v-model="detailForm.prompt_keywords" class="input" /></label>
          </div>
        </section>

        <section class="card detail-section detail-section--routes">
          <div class="section-head">
            <span class="layer-tag l2">L2</span>
            <h3>模型类型路由</h3>
            <span class="text-muted route-hint">最多 {{ MAX_ROUTES }} 个模型 · 点击选择</span>
            <button
              class="btn btn-ghost btn-sm"
              :disabled="routesDraft.length >= MAX_ROUTES"
              @click="addRouteRow"
            >+ 添加</button>
            <button class="btn btn-primary btn-sm" :disabled="routesSaving" @click="saveRoutes">
              {{ routesSaving ? '保存中…' : '保存路由' }}
            </button>
          </div>
          <div v-if="routesMsg" class="inline-msg">{{ routesMsg }}</div>

          <div v-if="!routesDraft.length" class="empty-routes">
            尚未配置模型路由 — 点击「添加」选择最多 3 个标准模型
          </div>

          <div class="route-cards">
            <div
              v-for="(rt, i) in routesDraft"
              :key="i"
              class="route-card"
              :class="{ 'route-card--disabled': rt.enabled === false }"
            >
              <div class="route-card-head">
                <span class="route-index">#{{ i + 1 }}</span>
                <label class="route-enabled">
                  <input type="checkbox" v-model="rt.enabled" />
                  启用
                </label>
                <button class="btn btn-ghost btn-sm route-remove" @click="removeRouteRow(i)">移除</button>
              </div>
              <div class="route-picker-row">
                <span class="field-label">标准模型</span>
                <ModelPicker
                  v-model="rt.canonical_name"
                  placeholder="点击选择模型…"
                  :title="`工作类型 ${detail.label} · 路由 #${i + 1}`"
                />
              </div>
              <div class="route-fields">
                <label>权重
                  <input v-model.number="rt.weight" type="number" step="0.1" min="0.1" class="input compact" />
                </label>
                <label>最低分
                  <input v-model.number="rt.min_score" type="number" step="0.1" class="input compact" />
                </label>
                <button
                  class="btn btn-ghost btn-sm"
                  :disabled="!rt.canonical_name.trim() || testingModel === rt.canonical_name"
                  @click="testRoute(rt)"
                >
                  {{ testingModel === rt.canonical_name ? '测试中…' : '测试' }}
                </button>
              </div>
              <div v-if="testErrors[rt.canonical_name]" class="test-result test-result--fail">
                {{ testErrors[rt.canonical_name] }}
              </div>
              <div v-else-if="testResults[rt.canonical_name]" class="test-result" :class="testResults[rt.canonical_name].success ? 'test-result--ok' : 'test-result--fail'">
                <span>{{ testResults[rt.canonical_name].success ? '成功' : '失败' }}</span>
                <span v-if="testResults[rt.canonical_name].latency_ms != null">{{ testResults[rt.canonical_name].latency_ms }}ms</span>
                <span v-if="testResults[rt.canonical_name].model_name">{{ testResults[rt.canonical_name].provider_name }}</span>
                <span v-if="testResults[rt.canonical_name].error" class="test-err">{{ testResults[rt.canonical_name].error }}</span>
              </div>
            </div>
          </div>

          <div v-if="routesDraft.length" class="route-actions">
            <button class="btn btn-ghost btn-sm" :disabled="testingAll" @click="testAllRoutes">
              {{ testingAll ? '批量测试中…' : '测试全部启用路由' }}
            </button>
          </div>
        </section>
      </div>
    </div>

    <div v-else-if="activeTab === 'settings' && settingsLoading && detailKey" class="loading-hint">加载详情…</div>

    <!-- ═══ Settings — List ═══ -->
    <div v-else-if="activeTab === 'settings'" class="tab-content">
      <div class="card compact-card">
        <div class="card-toolbar">
          <div class="toolbar-left">
            <span class="layer-tag l1">WT</span>
            <span class="toolbar-title">工作类型列表</span>
            <span class="text-muted">({{ workTypes.length }})</span>
          </div>
          <div class="toolbar-filters">
            <button class="btn btn-sm btn-ghost" @click="doSyncACC" title="从 ACC 拉取工作类型配置">从 ACC 同步</button>
            <button class="btn btn-primary btn-sm" @click="openCreate">+ 新建</button>
          </div>
        </div>
        <p class="list-hint">点击行进入详情，配置基本属性与模型类型路由（最多 3 个）。</p>
        <div v-if="syncMsg" class="policy-msg" :class="{ 'sync-ok': syncOk, 'sync-err': syncOk === false }">{{ syncMsg }}</div>
        <div v-if="settingsLoading" class="loading-hint">加载…</div>
        <div v-else class="table-wrap">
          <table class="dense-table list-table">
            <thead>
              <tr>
                <th>#</th>
                <th>Key</th>
                <th>名称</th>
                <th>分类</th>
                <th>L1</th>
                <th>Profile</th>
                <th>模型路由</th>
                <th>状态</th>
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
                  <span v-if="!routeSummary(wt).length" class="text-muted">未配置</span>
                  <span v-for="m in routeSummary(wt)" :key="m" class="route-chip">{{ m }}</span>
                </td>
                <td><span :class="wt.enabled ? 'badge badge-green' : 'badge badge-red'">{{ wt.enabled ? '启用' : '禁用' }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <!-- Create Modal -->
    <div v-if="showCreateModal" class="modal-overlay" @click.self="showCreateModal = false">
      <div class="modal-card">
        <h3>新建工作类型</h3>
        <div class="form-grid">
          <label class="span-2">Key <input v-model="createForm.key" placeholder="my_work_type" /></label>
          <label>名称 <input v-model="createForm.label" /></label>
          <label>分类
            <select v-model="createForm.category">
              <option v-for="c in CATEGORIES" :key="c" :value="c">{{ c }}</option>
            </select>
          </label>
          <label>L1 任务
            <select v-model="createForm.l1_task_type">
              <option v-for="t in L1_TASK_TYPES" :key="t.key" :value="t.key">{{ t.label }}</option>
            </select>
          </label>
          <label>Profile
            <select v-model="createForm.default_profile">
              <option v-for="p in PROFILES" :key="p.key" :value="p.key">{{ p.label }}</option>
            </select>
          </label>
          <label>排序 <input v-model.number="createForm.sort_order" type="number" /></label>
          <label class="span-2">Tags（逗号分隔）<input v-model="createForm.tags" /></label>
          <label class="span-2">Prompt 关键词 <input v-model="createForm.prompt_keywords" /></label>
        </div>
        <div v-if="createError" class="alert alert-danger compact-alert">{{ createError }}</div>
        <div class="modal-actions">
          <button class="btn btn-ghost" @click="showCreateModal = false">取消</button>
          <button class="btn btn-primary" @click="saveCreate">创建并进入详情</button>
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
.layer-tag.l1 { background: rgba(99,102,241,.22); color: var(--accent-h); }
.layer-tag.l2 { background: rgba(63,185,80,.22); color: var(--success); }

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
  background: rgba(99,102,241,.12);
  color: var(--accent-h);
  border: 1px solid rgba(99,102,241,.25);
}

.loading-hint { padding: 12px; text-align: center; color: var(--muted); font-size: 11px; }
.text-muted { color: var(--muted); }
.policy-msg { font-size: 11px; color: var(--accent-h); margin-bottom: 4px; }
.policy-msg.sync-ok { color: var(--success); }
.policy-msg.sync-err { color: var(--danger, #f85149); }

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
.route-card {
  padding: 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg-subtle);
}
.route-card--disabled { opacity: 0.65; }
.route-card-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}
.route-index {
  font-size: 11px;
  font-weight: 700;
  color: var(--muted);
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
