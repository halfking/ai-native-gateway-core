<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  listWorkTypes, getWorkType, createWorkType, updateWorkType, deleteWorkType,
  putWorkTypeRoutes, getWorkTypeStats, syncWorkTypesFromACC,
  L1_TASK_TYPES, PROFILES, CATEGORIES,
  type WorkTypeConfig, type WorkTypeStats, type ModelRoute,
} from '../api-work-types'
import {
  getAutoRouteAudit, getAutoRouteDecisions,
  type AutoRouteAudit, type AutoRouteDecision,
} from '../api-autoroute'

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

// ── Settings / CRUD ───────────────────────────────────
const workTypes = ref<WorkTypeConfig[]>([])
const settingsLoading = ref(false)
const syncMsg = ref('')

const showModal = ref(false)
const editing = ref<WorkTypeConfig | null>(null)
const form = ref({
  key: '', label: '', category: '通用', l1_task_type: 'chat',
  default_profile: 'smart' as 'smart' | 'speed_first' | 'cost_first',
  tags: '', prompt_keywords: '', sort_order: 0, enabled: true,
})
const saveError = ref('')

const detail = ref<WorkTypeConfig | null>(null)
const routesDraft = ref<ModelRoute[]>([])
const routesSaving = ref(false)

async function loadSettings() {
  settingsLoading.value = true
  try {
    workTypes.value = await listWorkTypes(true)
    if (detailKey.value) {
      detail.value = await getWorkType(detailKey.value)
      routesDraft.value = (detail.value.model_routes ?? []).map(r => ({ ...r }))
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
  editing.value = null
  form.value = {
    key: '', label: '', category: '通用', l1_task_type: 'chat',
    default_profile: 'smart', tags: '', prompt_keywords: '',
    sort_order: workTypes.value.length + 1, enabled: true,
  }
  saveError.value = ''
  showModal.value = true
}

function openEdit(wt: WorkTypeConfig) {
  editing.value = wt
  form.value = {
    key: wt.key,
    label: wt.label,
    category: wt.category,
    l1_task_type: wt.l1_task_type,
    default_profile: wt.default_profile,
    tags: wt.tags.join(', '),
    prompt_keywords: wt.prompt_keywords.join(', '),
    sort_order: wt.sort_order,
    enabled: wt.enabled,
  }
  saveError.value = ''
  showModal.value = true
}

async function saveForm() {
  saveError.value = ''
  const tags = form.value.tags.split(/[,，]/).map(s => s.trim()).filter(Boolean)
  const kw = form.value.prompt_keywords.split(/[,，]/).map(s => s.trim()).filter(Boolean)
  try {
    if (editing.value) {
      await updateWorkType(editing.value.key, {
        label: form.value.label,
        category: form.value.category,
        l1_task_type: form.value.l1_task_type,
        default_profile: form.value.default_profile,
        tags, prompt_keywords: kw,
        sort_order: form.value.sort_order,
        enabled: form.value.enabled,
      })
    } else {
      await createWorkType({
        key: form.value.key.trim(),
        label: form.value.label.trim(),
        category: form.value.category,
        l1_task_type: form.value.l1_task_type,
        default_profile: form.value.default_profile,
        tags, prompt_keywords: kw,
        sort_order: form.value.sort_order,
        enabled: form.value.enabled,
      })
    }
    showModal.value = false
    await loadSettings()
  } catch (e) {
    saveError.value = String(e)
  }
}

async function disableWorkType(wt: WorkTypeConfig) {
  if (!confirm(`禁用工作类型「${wt.label}」？`)) return
  await deleteWorkType(wt.key)
  if (detailKey.value === wt.key) router.push('/routing-v2/work-types/settings')
  await loadSettings()
}

async function saveRoutes() {
  if (!detailKey.value) return
  routesSaving.value = true
  try {
    await putWorkTypeRoutes(detailKey.value, routesDraft.value)
    detail.value = await getWorkType(detailKey.value)
    routesDraft.value = (detail.value.model_routes ?? []).map(r => ({ ...r }))
  } catch (e) {
    console.error('saveRoutes', e)
  } finally {
    routesSaving.value = false
  }
}

function addRouteRow() {
  routesDraft.value.push({ canonical_name: '', weight: 1, min_score: 0, enabled: true })
}

async function doSyncACC() {
  syncMsg.value = ''
  try {
    const r = await syncWorkTypesFromACC()
    syncMsg.value = r.message
  } catch (e) {
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
}, { immediate: false })

onMounted(async () => {
  await loadOverview()
  if (activeTab.value === 'settings') await loadSettings()
})
watch(activeTab, (tab) => {
  if (tab === 'settings') loadSettings()
})
</script>

<template>
  <div class="work-types-view">
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
      </div>
    </div>

    <!-- ═══ Overview ═══ -->
    <div v-if="activeTab === 'overview'" class="tab-content">
      <div class="overview-grid">
        <!-- Auto 总统计 -->
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

        <!-- 工作类型分布 -->
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
              <button class="btn btn-ghost btn-sm wt-analytics-btn" title="在数据分析中查看" @click.stop="router.push({ path: '/routing-v2', query: { tab: 'analytics', row: 'work_type', filter: e.key } })">分析</button>
            </div>
          </div>
          <div v-else class="text-muted">暂无 24h 数据</div>
        </div>

        <!-- 模型 Top -->
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
          <div v-else-if="audit.top_chosen_models.length" class="dist-col full">
            <div v-for="m in audit.top_chosen_models.slice(0, 8)" :key="m.model" class="dist-row">
              <span class="dist-label">{{ m.model }}</span>
              <div class="dist-bar-bg"><div class="dist-bar-fill" :style="{ width: (m.count / distMax(Object.fromEntries(audit.top_chosen_models.map(x => [x.model, x.count]))) * 100) + '%' }" /></div>
              <span class="dist-count">{{ m.count }}</span>
            </div>
          </div>
          <div v-else class="text-muted">暂无</div>
        </div>

        <!-- 最近路由决策 -->
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

    <!-- ═══ Settings ═══ -->
    <div v-if="activeTab === 'settings'" class="tab-content">
      <!-- Detail panel -->
      <div v-if="detailKey && detail" class="card compact-card">
        <div class="section-head">
          <button class="btn btn-sm btn-ghost" @click="router.push('/routing-v2/work-types/settings')">← 列表</button>
          <h3>{{ detail.label }}</h3>
          <code class="key-code">{{ detail.key }}</code>
        </div>
        <div class="detail-meta">
          <span class="chip">分类 <strong>{{ detail.category }}</strong></span>
          <span class="chip">L1 <strong>{{ l1Label(detail.l1_task_type) }}</strong></span>
          <span class="chip">Profile <strong>{{ profileLabel(detail.default_profile) }}</strong></span>
          <span v-if="!detail.enabled" class="badge badge-red">已禁用</span>
        </div>
        <div class="kw-block">
          <div class="block-label">Prompt 关键词</div>
          <div class="kw-tags">
            <span v-for="k in detail.prompt_keywords" :key="k" class="task-pill sm">{{ k }}</span>
            <span v-if="!detail.prompt_keywords.length" class="text-muted">无</span>
          </div>
        </div>
        <div class="kw-block">
          <div class="block-label">Tags</div>
          <div class="kw-tags">
            <span v-for="t in detail.tags" :key="t" class="task-pill sm">{{ t }}</span>
          </div>
        </div>
        <div class="section-head tight" style="margin-top:8px">
          <span class="layer-tag l2">L2</span>
          <h3>模型映射</h3>
          <button class="btn btn-sm btn-ghost" @click="addRouteRow">+ 行</button>
          <button class="btn btn-primary btn-sm" :disabled="routesSaving" @click="saveRoutes">保存映射</button>
        </div>
        <table class="dense-table">
          <thead><tr><th>canonical_name</th><th>weight</th><th>min_score</th><th>enabled</th><th></th></tr></thead>
          <tbody>
            <tr v-for="(rt, i) in routesDraft" :key="i">
              <td><input v-model="rt.canonical_name" class="cell-input" placeholder="gpt-4o" /></td>
              <td><input v-model.number="rt.weight" type="number" step="0.1" class="cell-input sm" /></td>
              <td><input v-model.number="rt.min_score" type="number" step="0.1" class="cell-input sm" /></td>
              <td><input type="checkbox" v-model="rt.enabled" /></td>
              <td><button class="btn btn-sm btn-ghost" @click="routesDraft.splice(i, 1)">×</button></td>
            </tr>
          </tbody>
        </table>
        <div v-if="!routesDraft.length" class="text-muted">暂无模型映射 — 点击 + 行添加</div>
      </div>

      <!-- List table -->
      <div v-else class="card compact-card">
        <div class="card-toolbar">
          <div class="toolbar-left">
            <span class="layer-tag l1">WT</span>
            <span class="toolbar-title">工作类型列表</span>
            <span class="text-muted">({{ workTypes.length }})</span>
          </div>
          <div class="toolbar-filters">
            <button class="btn btn-sm btn-ghost" @click="doSyncACC">ACC 同步</button>
            <button class="btn btn-primary btn-sm" @click="openCreate">+ 新建</button>
          </div>
        </div>
        <div v-if="syncMsg" class="policy-msg">{{ syncMsg }}</div>
        <div v-if="settingsLoading" class="loading-hint">加载…</div>
        <div v-else class="table-wrap">
          <table class="dense-table">
            <thead>
              <tr>
                <th>#</th><th>Key</th><th>名称</th><th>分类</th><th>L1</th><th>Profile</th><th>状态</th><th></th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="(wt, i) in workTypes"
                :key="wt.key"
                class="model-row"
                :class="{ disabled: !wt.enabled }"
                @click="openDetail(wt.key)"
              >
                <td class="num">{{ wt.sort_order || i + 1 }}</td>
                <td><code class="key-code">{{ wt.key }}</code></td>
                <td>{{ wt.label }}</td>
                <td><span class="badge badge-gray">{{ wt.category }}</span></td>
                <td>{{ l1Label(wt.l1_task_type) }}</td>
                <td>{{ profileLabel(wt.default_profile) }}</td>
                <td><span :class="wt.enabled ? 'badge badge-green' : 'badge badge-red'">{{ wt.enabled ? '启用' : '禁用' }}</span></td>
                <td @click.stop>
                  <button class="btn btn-sm btn-ghost" @click="openEdit(wt)">编辑</button>
                  <button v-if="wt.enabled" class="btn btn-sm btn-ghost" @click="disableWorkType(wt)">禁用</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <!-- Modal -->
    <div v-if="showModal" class="modal-overlay" @click.self="showModal = false">
      <div class="modal-card">
        <h3>{{ editing ? '编辑工作类型' : '新建工作类型' }}</h3>
        <div class="form-grid">
          <label v-if="!editing">Key <input v-model="form.key" placeholder="my_work_type" /></label>
          <label>名称 <input v-model="form.label" /></label>
          <label>分类
            <select v-model="form.category">
              <option v-for="c in CATEGORIES" :key="c" :value="c">{{ c }}</option>
            </select>
          </label>
          <label>L1 任务
            <select v-model="form.l1_task_type">
              <option v-for="t in L1_TASK_TYPES" :key="t.key" :value="t.key">{{ t.label }}</option>
            </select>
          </label>
          <label>Profile
            <select v-model="form.default_profile">
              <option v-for="p in PROFILES" :key="p.key" :value="p.key">{{ p.label }}</option>
            </select>
          </label>
          <label>排序 <input v-model.number="form.sort_order" type="number" /></label>
          <label class="span-2">Tags（逗号分隔）<input v-model="form.tags" /></label>
          <label class="span-2">Prompt 关键词 <input v-model="form.prompt_keywords" /></label>
          <label v-if="editing"><input type="checkbox" v-model="form.enabled" /> 启用</label>
        </div>
        <div v-if="saveError" class="alert alert-danger compact-alert">{{ saveError }}</div>
        <div class="modal-actions">
          <button class="btn btn-ghost" @click="showModal = false">取消</button>
          <button class="btn btn-primary" @click="saveForm">保存</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.work-types-view { max-width: 1200px; }

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
.wt-analytics-btn { margin-left: 4px; padding: 0 4px; font-size: 9px; }
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
.model-name { font-weight: 500; font-size: 11px; }
.model-row { cursor: pointer; }
.model-row:hover { background: rgba(255,255,255,.03); }
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

.task-pill {
  padding: 1px 6px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 99px;
  font-size: 10px;
}
.task-pill.sm { font-size: 9px; }

.detail-meta { display: flex; flex-wrap: wrap; gap: 4px; margin-bottom: 8px; }
.kw-block { margin-bottom: 6px; }
.kw-tags { display: flex; flex-wrap: wrap; gap: 4px; }
.block-label { font-size: 9px; color: var(--muted); margin-bottom: 3px; font-weight: 600; text-transform: uppercase; }

.cell-input { width: 100%; font-size: 11px; padding: 2px 4px; }
.cell-input.sm { width: 56px; }

.loading-hint { padding: 12px; text-align: center; color: var(--muted); font-size: 11px; }
.policy-msg { font-size: 11px; color: var(--accent-h); margin-bottom: 4px; }

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

@media (max-width: 720px) {
  .overview-grid { grid-template-columns: 1fr; }
  .overview-grid .span-2 { grid-column: span 1; }
}
</style>
