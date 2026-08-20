<script setup lang="ts">
// TurnsListView.vue — 会话优先的跨会话轮次列表。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElOption, ElSelect } from 'element-plus'
import {
  listTurnsFilterOptions,
  listTurnsSessions,
  type TurnGroupItem,
  type TurnsFilterOptions,
  type TurnsSessionGroup
} from '../api/turns'

const router = useRouter()
const loading = ref(false)
const loadingMore = ref(false)
const loaded = ref(false)
const items = ref<TurnsSessionGroup[]>([])
const hasMore = ref(false)
const nextCursor = ref('')
const error = ref('')
const expandedSessions = ref<Set<string>>(new Set())
const viewMode = ref<'flat' | 'tree'>('flat')
const collapsedProjects = ref<Set<string>>(new Set())
const collapsedTasks = ref<Set<string>>(new Set())
const advancedExpanded = ref(false)
const timePreset = ref('all')
const requestVersion = ref(0)
let controller: AbortController | null = null

const modelFilter = ref('')
const providerFilter = ref('')
const statusCodeFilter = ref('')
const projectFilter = ref('')
const taskFilter = ref('')
const searchFilter = ref('')
const tagsFilter = ref<string[]>([])
const clientFilter = ref('')
const ownerUserFilter = ref('')
const dateFromFilter = ref('')
const dateToFilter = ref('')

const filterOptions = ref<TurnsFilterOptions>({
  projects: [], tasks: [], owners: [], clients: [], tags: [], models: [], providers: [], status_codes: []
})

const timePresets = [
  { value: 'all', label: '时间不限', hours: 0 },
  { value: 'h1', label: '最近 1 小时', hours: 1 },
  { value: 'h24', label: '最近 24 小时', hours: 24 },
  { value: 'd3', label: '最近 3 天', hours: 72 },
  { value: 'd7', label: '最近 7 天', hours: 168 }
]

interface TaskGroup { key: string; label: string; sessions: TurnsSessionGroup[] }
interface ProjectGroup { key: string; label: string; tasks: TaskGroup[]; sessionCount: number; totalTurns: number; totalTokens: number; totalCost: number }

const displayGroups = computed<ProjectGroup[]>(() => {
  if (viewMode.value === 'flat') {
    return [{ key: '__all__', label: '全部会话', tasks: [{ key: '__all__', label: '', sessions: items.value }], sessionCount: items.value.length, totalTurns: items.value.reduce((n, s) => n + s.total_turns, 0), totalTokens: items.value.reduce((n, s) => n + s.total_tokens, 0), totalCost: items.value.reduce((n, s) => n + s.total_cost_usd, 0) }]
  }
  const map = new Map<string, ProjectGroup>()
  for (const session of items.value) {
    const projectKey = session.project_id || ''
    const taskKey = session.task_id || ''
    let project = map.get(projectKey)
    if (!project) {
      project = { key: projectKey, label: session.project_id || '未分类项目', tasks: [], sessionCount: 0, totalTurns: 0, totalTokens: 0, totalCost: 0 }
      map.set(projectKey, project)
    }
    let task = project.tasks.find(value => value.key === taskKey)
    if (!task) {
      task = { key: taskKey, label: taskKey || '未分类任务', sessions: [] }
      project.tasks.push(task)
    }
    task.sessions.push(session)
  }
  for (const project of map.values()) {
    const sessions = project.tasks.flatMap(task => task.sessions)
    project.sessionCount = sessions.length
    project.totalTurns = sessions.reduce((n, s) => n + s.total_turns, 0)
    project.totalTokens = sessions.reduce((n, s) => n + s.total_tokens, 0)
    project.totalCost = sessions.reduce((n, s) => n + s.total_cost_usd, 0)
  }
  return [...map.values()]
})

function toRFC3339(value: string): string {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '' : date.toISOString()
}

function buildParams(cursor?: string): Parameters<typeof listTurnsSessions>[0] | null {
  const params: Parameters<typeof listTurnsSessions>[0] = { limit: 20 }
  if (cursor) params.cursor = cursor
  if (modelFilter.value.trim()) params.model = modelFilter.value.trim()
  if (providerFilter.value.trim()) params.provider = providerFilter.value.trim()
  if (statusCodeFilter.value.trim()) {
    const code = Number(statusCodeFilter.value)
    if (!Number.isInteger(code) || code < 100 || code > 599) {
      error.value = '状态码必须是 100 到 599 的整数'
      return null
    }
    params.status_code = code
  }
  if (projectFilter.value.trim()) params.project_id = projectFilter.value.trim()
  if (taskFilter.value.trim()) params.task_id = taskFilter.value.trim()
  if (searchFilter.value.trim()) params.search = searchFilter.value.trim()
  if (tagsFilter.value.length) params.tags = tagsFilter.value.join(',')
  if (clientFilter.value.trim()) params.client = clientFilter.value.trim()
  if (ownerUserFilter.value.trim()) params.owner_user = ownerUserFilter.value.trim()
  const from = toRFC3339(dateFromFilter.value)
  const to = toRFC3339(dateToFilter.value)
  if (from) params.ts_from = from
  if (to) params.ts_to = to
  return params
}

async function load(reset = true) {
  const params = buildParams(reset ? undefined : nextCursor.value || undefined)
  if (!params) return
  const version = ++requestVersion.value
  if (reset) {
    controller?.abort()
    controller = new AbortController()
    loading.value = true
    error.value = ''
    items.value = []
    nextCursor.value = ''
    hasMore.value = false
    expandedSessions.value = new Set()
    collapsedProjects.value = new Set()
    collapsedTasks.value = new Set()
  } else {
    loadingMore.value = true
  }
  try {
    const response = await listTurnsSessions(params, { signal: controller?.signal })
    if (version !== requestVersion.value) return
    items.value = reset ? response.items : [...items.value, ...response.items]
    hasMore.value = response.has_more
    nextCursor.value = response.next_cursor
    loaded.value = true
  } catch (cause: unknown) {
    if (version !== requestVersion.value || (cause instanceof DOMException && cause.name === 'AbortError')) return
    error.value = cause instanceof Error ? cause.message : String(cause)
    if (reset) items.value = []
  } finally {
    if (version === requestVersion.value) {
      loading.value = false
      loadingMore.value = false
    }
  }
}

async function loadFilterOptions() {
  try { filterOptions.value = await listTurnsFilterOptions() } catch (cause) { console.warn('load filter options failed', cause) }
}
function toggleSession(id: string) { const next = new Set(expandedSessions.value); next.has(id) ? next.delete(id) : next.add(id); expandedSessions.value = next }
function toggleProject(id: string) { const next = new Set(collapsedProjects.value); next.has(id) ? next.delete(id) : next.add(id); collapsedProjects.value = next }
function toggleTask(id: string) { const next = new Set(collapsedTasks.value); next.has(id) ? next.delete(id) : next.add(id); collapsedTasks.value = next }
function taskKey(project: string, task: string) { return `${project}::${task}` }
function openSession(session: TurnsSessionGroup) { router.push({ path: `/admin/sessions/${session.session_id}` }) }
function openSessionById(id?: string) { if (id) router.push({ path: `/admin/sessions/${id}` }) }
function openTurn(session: TurnsSessionGroup, turn: TurnGroupItem) { router.push({ path: `/admin/sessions/${session.session_id}`, query: { turn: String(turn.turn_no), focus: '1' } }) }
function sessionTitle(session: TurnsSessionGroup) { return session.title || session.topic || session.intent || `${session.session_id.slice(0, 12)}…` }
function sessionTopic(session: TurnsSessionGroup) { return session.topic || session.intent || '' }
function sessionClient(session: TurnsSessionGroup) { return session.application_code || session.client_id || session.client_type || '' }
function relationLabel(value?: string) { return ({ handoff: '轮换自', auto_title: '标题分支 ←', auto_summary: '总结分支 ←' } as Record<string, string>)[value || ''] || '父会话' }
function shortSessionId(value?: string) { return value && value.length > 14 ? `${value.slice(0, 14)}…` : value || '' }
function statusLabel(value: string) { return ({ active: '进行中', closed: '已关闭', archived: '已归档', deleted: '已删除' } as Record<string, string>)[value] || value }
function summaryMeta(session: TurnsSessionGroup) { return [session.summary_model, session.summary_generated_at && new Date(session.summary_generated_at).toLocaleString()].filter(Boolean).join(' · ') }
function visibleModels(session: TurnsSessionGroup) { return (session.models_used || []).slice(0, 3) }
function hiddenModelCount(session: TurnsSessionGroup) { return Math.max(0, (session.models_used || []).length - 3) }
function formatMs(value: number) { if (!value || value <= 0) return '—'; if (value < 1000) return `${value}ms`; const seconds = Math.round(value / 1000); return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s` }
function formatTokens(value: number) { return value >= 1000 ? `${(value / 1000).toFixed(1)}k` : String(value) }
function resetAndLoad() { load(true) }
function onPresetChange(event: Event) { const value = (event.target as HTMLSelectElement).value; timePreset.value = value; const preset = timePresets.find(item => item.value === value); if (!preset) return; dateFromFilter.value = value === 'all' ? '' : localDatetime(new Date(Date.now() - preset.hours * 3600 * 1000)); dateToFilter.value = ''; load(true) }
function onDatetimeManualChange() { timePreset.value = 'custom' }
function localDatetime(date: Date) { const pad = (value: number) => String(value).padStart(2, '0'); return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}` }
function advancedActiveCount() { return [providerFilter.value, statusCodeFilter.value, projectFilter.value, taskFilter.value, clientFilter.value, ownerUserFilter.value].filter(Boolean).length + (tagsFilter.value.length ? 1 : 0) }
function resetAll() { modelFilter.value = ''; providerFilter.value = ''; statusCodeFilter.value = ''; projectFilter.value = ''; taskFilter.value = ''; searchFilter.value = ''; tagsFilter.value = []; clientFilter.value = ''; ownerUserFilter.value = ''; dateFromFilter.value = ''; dateToFilter.value = ''; timePreset.value = 'all'; load(true) }

onMounted(() => { load(true); loadFilterOptions() })
onBeforeUnmount(() => controller?.abort())
</script>

<template>
  <div class="turns-list-view">
    <div class="header">
      <h1>会话与轮次</h1>
      <p class="subtitle">按最近更新排序 · 最外层为会话，展开查看每个轮次的请求、返回、缓存、压缩和故障切换</p>
    </div>

    <div class="filter-section">
      <div class="filter-bar">
        <input v-model="searchFilter" type="text" placeholder="搜索标题 / 主题 / 摘要" class="filter-input filter-search" @keyup.enter="resetAndLoad" />
        <el-select v-model="modelFilter" filterable allow-create default-first-option clearable placeholder="模型" class="filter-select"><el-option v-for="value in filterOptions.models" :key="value" :label="value" :value="value" /></el-select>
        <select :value="timePreset" class="filter-input filter-preset" @change="onPresetChange"><option v-for="preset in timePresets" :key="preset.value" :value="preset.value">{{ preset.label }}</option><option v-if="timePreset === 'custom'" value="custom">自定义时间段</option></select>
        <input v-model="dateFromFilter" type="datetime-local" class="filter-input filter-dt" title="会话开始时间起" @change="onDatetimeManualChange" /><span class="dt-sep">~</span><input v-model="dateToFilter" type="datetime-local" class="filter-input filter-dt" title="会话开始时间止" @change="onDatetimeManualChange" />
        <button class="btn btn-primary" type="button" @click="resetAndLoad">查询</button><button class="btn btn-secondary" type="button" @click="resetAll">清空</button><button class="btn btn-secondary" type="button" @click="advancedExpanded = !advancedExpanded">更多筛选 <span v-if="advancedActiveCount()" class="adv-count">{{ advancedActiveCount() }}</span> <span class="caret" :class="{ open: advancedExpanded }" aria-hidden="true">▸</span></button>
      </div>
      <div v-if="advancedExpanded" class="filter-bar filter-advanced">
        <el-select v-model="providerFilter" filterable allow-create default-first-option clearable placeholder="供应商" class="filter-select"><el-option v-for="value in filterOptions.providers" :key="value" :label="value" :value="value" /></el-select>
        <el-select v-model="statusCodeFilter" filterable allow-create default-first-option clearable placeholder="状态码" class="filter-select filter-select-short"><el-option v-for="value in filterOptions.status_codes" :key="value" :label="value" :value="value" /></el-select>
        <el-select v-model="projectFilter" filterable allow-create default-first-option clearable placeholder="项目 ID" class="filter-select filter-select-short"><el-option v-for="value in filterOptions.projects" :key="value" :label="value" :value="value" /></el-select>
        <el-select v-model="taskFilter" filterable allow-create default-first-option clearable placeholder="任务 ID" class="filter-select filter-select-short"><el-option v-for="value in filterOptions.tasks" :key="value" :label="value" :value="value" /></el-select>
        <el-select v-model="clientFilter" filterable allow-create default-first-option clearable placeholder="客户端 / 智能体" class="filter-select"><el-option v-for="value in filterOptions.clients" :key="value" :label="value" :value="value" /></el-select>
        <el-select v-model="ownerUserFilter" filterable allow-create default-first-option clearable placeholder="属主用户" class="filter-select"><el-option v-for="value in filterOptions.owners" :key="value" :label="value" :value="value" /></el-select>
        <el-select v-model="tagsFilter" multiple filterable allow-create default-first-option clearable collapse-tags collapse-tags-tooltip placeholder="标签" class="filter-select filter-select-tags"><el-option v-for="value in filterOptions.tags" :key="value" :label="value" :value="value" /></el-select>
      </div>
    </div>

    <div v-if="error" class="error-banner" role="alert"><span>{{ error }}</span><button class="btn btn-secondary" type="button" @click="resetAndLoad">重试</button></div>
    <div class="list">
      <div class="view-toggle" aria-label="会话列表视图"><button class="toggle-btn" :class="{ active: viewMode === 'flat' }" type="button" @click="viewMode = 'flat'">会话列表</button><button class="toggle-btn" :class="{ active: viewMode === 'tree' }" type="button" @click="viewMode = 'tree'">按项目分组</button></div>
      <div v-if="loading && !loaded" class="session-skeleton" data-testid="turns-skeleton" aria-label="正在加载会话"><div v-for="n in 4" :key="n" class="skeleton-card" /></div>
      <div v-else-if="error" class="empty" data-testid="turns-error-state">加载会话失败，请重试</div>
      <div v-else-if="loaded && items.length === 0" class="empty" data-testid="turns-empty">暂无符合条件的会话</div>
      <div v-else class="turns-results">
        <div v-for="project in displayGroups" :key="project.key" class="group-card">
          <button v-if="viewMode === 'tree'" class="group-header" type="button" :aria-expanded="!collapsedProjects.has(project.key)" @click="toggleProject(project.key)"><span class="caret" :class="{ open: !collapsedProjects.has(project.key) }" aria-hidden="true">▸</span><span class="group-title">{{ project.label }}</span><span class="badge">{{ project.sessionCount }} 会话</span><span class="badge">{{ project.totalTurns }} 轮</span><span class="badge">{{ formatTokens(project.totalTokens) }} tok</span><span v-if="project.totalCost > 0" class="badge cost">${{ project.totalCost.toFixed(4) }}</span></button>
          <div v-if="viewMode === 'flat' || !collapsedProjects.has(project.key)" class="group-body">
            <div v-for="task in project.tasks" :key="taskKey(project.key, task.key)">
              <button v-if="viewMode === 'tree' && task.label" class="task-header" type="button" :aria-expanded="!collapsedTasks.has(taskKey(project.key, task.key))" @click="toggleTask(taskKey(project.key, task.key))"><span class="caret" :class="{ open: !collapsedTasks.has(taskKey(project.key, task.key)) }" aria-hidden="true">▸</span><span class="task-title">{{ task.label }}</span><span class="badge">{{ task.sessions.length }} 会话</span></button>
              <div v-if="viewMode === 'flat' || !task.label || !collapsedTasks.has(taskKey(project.key, task.key))" class="task-sessions">
                <div v-for="session in task.sessions" :key="session.session_id" class="session-card">
                  <div class="session-header" role="button" tabindex="0" :aria-expanded="expandedSessions.has(session.session_id)" @click="toggleSession(session.session_id)" @keydown.enter.prevent="toggleSession(session.session_id)" @keydown.space.prevent="toggleSession(session.session_id)">
                    <div class="session-head-line"><span class="caret" :class="{ open: expandedSessions.has(session.session_id) }" aria-hidden="true">▸</span><span class="status-badge" :data-state="session.status">{{ statusLabel(session.status) }}</span><span class="session-title" :title="sessionTitle(session)">{{ sessionTitle(session) }}</span><span v-if="sessionTopic(session)" class="session-topic" :title="sessionTopic(session)">主题：{{ sessionTopic(session) }}</span><button v-if="session.parent_session_id" class="ctx-badge parent-link" type="button" @click.stop="openSessionById(session.parent_session_id)">↳ {{ relationLabel(session.parent_relation) }} {{ shortSessionId(session.parent_session_id) }}</button></div>
                    <div v-if="session.summary" class="session-summary" :title="session.summary">{{ session.summary }}<span v-if="summaryMeta(session)" class="summary-meta">AI 总结 · {{ summaryMeta(session) }}</span></div><div v-else class="session-summary muted">暂无会话摘要（未生成总结）</div>
                    <div class="session-context"><span v-if="session.project_id" class="ctx-badge project" :title="session.project_id">项目 {{ session.project_id }}</span><span v-if="session.task_id" class="ctx-badge task" :title="session.task_id">任务 {{ session.task_id }}</span><span v-if="session.owner_user" class="ctx-badge owner">用户 {{ session.owner_user }}</span><span v-if="sessionClient(session)" class="ctx-badge client">{{ session.application_code ? '智能体' : '客户端' }} {{ sessionClient(session) }}</span><span v-if="session.start_time" class="ts">开始 {{ new Date(session.start_time).toLocaleString() }}</span><span v-for="tag in session.user_tags" :key="tag" class="badge tag">#{{ tag }}</span></div>
                    <div class="session-meta"><span class="badge">{{ session.total_turns }} 轮</span><span class="badge">{{ formatTokens(session.total_tokens) }} tok</span><span class="badge cost">${{ session.total_cost_usd.toFixed(4) }}</span><span class="badge">时长 {{ formatMs(session.duration_ms) }}</span><span v-if="session.failover_count" class="badge warn">failover ×{{ session.failover_count }}</span><span v-if="session.error_count" class="badge error">错误 ×{{ session.error_count }}</span><span v-if="session.compression.applied_count" class="badge compression">压缩 {{ session.compression.applied_count }} 次 · 省 {{ formatTokens(session.compression.tokens_saved) }} tok</span><span v-for="model in visibleModels(session)" :key="model" class="badge model">{{ model }}</span><span v-if="hiddenModelCount(session)" class="badge model">+{{ hiddenModelCount(session) }}</span></div>
                    <div class="session-foot"><span class="ts">更新 {{ new Date(session.updated_at).toLocaleString() }}</span><button class="link" type="button" @click.stop="openSession(session)">进入会话详情 →</button></div>
                  </div>
                  <div v-if="expandedSessions.has(session.session_id)" class="turns-block"><div v-if="!session.turns.length" class="empty">该会话暂无匹配轮次</div><div v-for="turn in session.turns" :key="`${session.session_id}-${turn.turn_no}`" class="turn-row" role="button" tabindex="0" @click="openTurn(session, turn)" @keydown.enter.prevent="openTurn(session, turn)" @keydown.space.prevent="openTurn(session, turn)"><div class="col col-req"><div class="meta"><span class="turn-no">#{{ turn.turn_no }}</span><span class="ts">{{ new Date(turn.ts).toLocaleString() }}</span><span v-if="turn.attempt_no" class="mini-badge warn">failover#{{ turn.attempt_no }}</span><span :class="['verdict', `tag-${turn.injection_verdict || 'skip'}`]">inj: {{ turn.injection_verdict || 'skip' }}</span><span v-if="turn.latency_ms !== undefined" class="ts">⏱ {{ formatMs(turn.latency_ms) }}</span></div><div class="preview" :title="turn.title || '(无请求摘要)'">{{ turn.title || '(无请求摘要)' }}</div><div class="badges"><span class="badge">req {{ formatTokens(turn.request_tokens) }}</span><span v-if="turn.cache_read_tokens" class="badge cache">cache读 {{ formatTokens(turn.cache_read_tokens) }}</span><span v-if="turn.cache_write_tokens" class="badge cache">cache写 {{ formatTokens(turn.cache_write_tokens) }}</span><span v-if="turn.attachment_count" class="badge">📎 {{ turn.attachment_count }}</span><span v-if="turn.submit_mode === 'delta'" class="badge submit">delta</span><span class="badge model">{{ turn.model || '未指定模型' }}</span></div></div><div class="col col-resp"><div class="meta"><span class="turn-no">#{{ turn.turn_no }}</span><span :class="['verdict', `tag-${turn.output_verdict || 'skip'}`]">out: {{ turn.output_verdict || 'skip' }}</span><span v-if="turn.compression_applied" class="badge compression">压缩{{ turn.compression_strategy ? `·${turn.compression_strategy}` : '' }}<span v-if="turn.compression_tokens_saved !== undefined"> 省{{ formatTokens(turn.compression_tokens_saved) }}</span></span></div><div class="preview" :title="turn.summary || '(无回复摘要)'">{{ turn.summary || '(无回复摘要)' }}</div><div class="badges"><span class="badge">resp {{ formatTokens(turn.response_tokens) }}</span><span class="badge cost">${{ turn.cost_usd.toFixed(4) }}</span><span class="badge status" :data-ok="turn.status_code < 400 ? 'true' : 'false'">{{ turn.status_code }}</span><span v-if="!turn.success && turn.error_kind" class="badge error">{{ turn.error_kind.slice(0, 24) }}</span></div></div></div></div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
      <div v-if="hasMore" class="load-more"><button class="btn btn-secondary" type="button" :disabled="loadingMore" @click="load(false)">{{ loadingMore ? '加载中...' : '加载更早的会话' }}</button></div>
    </div>
  </div>
</template>

<style scoped>
.turns-list-view { background: var(--bg); min-height: 100vh; padding: 16px 24px; max-width: 1400px; margin: 0 auto; }
.header { margin-bottom: 16px; }
.header h1 { font-size: 24px; font-weight: 600; color: var(--text); margin: 0 0 8px; }
.subtitle { font-size: 14px; color: var(--text-secondary); margin: 0; }
.filter-section { margin-bottom: 16px; }
.filter-bar { display: flex; gap: 8px; margin-bottom: 8px; flex-wrap: wrap; align-items: center; }
.filter-advanced { padding: 8px; border: 1px dashed var(--border); border-radius: 6px; background: var(--surface-secondary); }
.filter-input { height: 36px; padding: 4px 12px; background: var(--surface-primary); border: 1px solid var(--border); border-radius: 6px; font-size: 14px; min-width: 140px; }
.filter-search { min-width: 220px; }
.filter-preset { min-width: 130px; }
.filter-dt { min-width: 180px; }
.dt-sep { color: var(--text-muted); }
.filter-select { width: 170px; flex: 0 0 170px; }
.filter-select-short { width: 130px; flex-basis: 130px; }
.filter-select-tags { width: 180px; flex-basis: 180px; }
.filter-select :deep(.el-select__wrapper) { min-height: 36px; }
.btn { height: 36px; padding: 0 16px; border-radius: 6px; font-size: 14px; font-weight: 500; cursor: pointer; }
.btn-primary { background: var(--accent); color: white; border: 0; }
.btn-secondary { background: var(--surface-primary); color: var(--text-primary); border: 1px solid var(--border); }
.btn-secondary:disabled { opacity: .5; cursor: not-allowed; }
.adv-count { display: inline-block; min-width: 16px; padding: 0 4px; border-radius: 8px; background: var(--accent); color: white; font-size: 11px; line-height: 16px; }
.error-banner { display: flex; justify-content: space-between; gap: 12px; align-items: center; background: var(--danger-soft); border: 1px solid var(--danger); border-radius: 6px; padding: 12px; margin-bottom: 16px; color: var(--danger); }
.list { display: flex; flex-direction: column; gap: 12px; }
.view-toggle { display: flex; gap: 8px; }
.toggle-btn { height: 30px; padding: 0 14px; border: 1px solid var(--border); border-radius: 15px; background: var(--surface-primary); color: var(--text-secondary); cursor: pointer; }
.toggle-btn.active { background: var(--accent); border-color: var(--accent); color: white; }
.session-skeleton { display: grid; gap: 10px; }
.skeleton-card { height: 150px; border-radius: 8px; background: var(--surface-secondary); animation: turns-pulse 1.2s ease-in-out infinite alternate; }
@keyframes turns-pulse { from { opacity: .55; } to { opacity: 1; } }
.group-card, .session-card { border: 1px solid var(--border); border-radius: 8px; background: var(--surface-primary); overflow: hidden; }
.group-header, .task-header { display: flex; align-items: center; gap: 8px; width: 100%; padding: 10px 16px; cursor: pointer; text-align: left; border: 0; background: var(--surface-secondary); color: var(--text-primary); }
.task-header { padding: 6px 8px; background: transparent; }
.group-header:hover, .task-header:hover, .session-header:hover, .turn-row:hover { background: var(--bg-hover); }
.group-body { padding: 10px 12px 12px; display: flex; flex-direction: column; gap: 10px; }
.task-sessions { display: flex; flex-direction: column; gap: 10px; padding-left: 18px; }
.session-header { padding: 12px 16px; cursor: pointer; outline: none; }
.session-header:focus-visible, .turn-row:focus-visible, .group-header:focus-visible, .task-header:focus-visible, .btn:focus-visible, .link:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.session-head-line, .session-context, .session-meta, .badges, .meta { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.session-head-line { gap: 8px; }
.caret { display: inline-block; transition: transform .15s; color: var(--text-muted); }
.caret.open { transform: rotate(90deg); }
.status-badge, .verdict, .badge, .ctx-badge, .mini-badge { padding: 2px 6px; border-radius: 4px; font-size: 11px; }
.status-badge { font-weight: 600; }
.status-badge[data-state="active"] { background: var(--success-soft); color: var(--success); }
.status-badge[data-state="closed"] { background: var(--primary-soft); color: var(--accent); }
.status-badge[data-state="deleted"] { background: var(--danger-soft); color: var(--danger); }
.session-title { font-size: 15px; font-weight: 600; color: var(--text-primary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.session-topic { color: var(--accent); background: var(--primary-soft); }
.session-summary { margin: 8px 0; color: var(--text-secondary); line-height: 1.5; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.session-summary.muted, .ts { color: var(--text-muted); }
.summary-meta { margin-left: 8px; color: var(--text-muted); font-size: 11px; }
.session-context { margin-bottom: 8px; font-size: 12px; }
.ctx-badge { color: var(--text-secondary); background: var(--surface-secondary); }
.ctx-badge.project { background: var(--warning-soft); color: var(--warning); }
.ctx-badge.task, .ctx-badge.owner, .badge.model, .badge.cache, .badge.compression, .badge.tag { background: var(--primary-soft); color: var(--accent); }
.ctx-badge.client, .badge.submit { background: var(--success-soft); color: var(--success); }
.parent-link, .link { cursor: pointer; }
.session-foot { display: flex; justify-content: space-between; align-items: center; margin-top: 8px; }
.link { color: var(--accent); background: transparent; border: 0; padding: 0; }
.turns-block { border-top: 1px solid var(--border); background: var(--surface-secondary); }
.turn-row { display: grid; grid-template-columns: 1fr 1fr; padding: 10px 16px; cursor: pointer; background: var(--surface-primary); border-bottom: 1px solid var(--border); }
.col { padding: 0 8px; }
.col + .col { border-left: 1px dashed var(--border); }
.preview { margin-bottom: 6px; color: var(--text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.badge { background: var(--surface-secondary); color: var(--text-secondary); }
.badge.warn, .mini-badge.warn { background: var(--warning-soft); color: var(--warning); }
.badge.error { background: var(--danger-soft); color: var(--danger); }
.badge.cost { background: var(--warning-soft); color: var(--warning); }
.badge.status[data-ok="true"] { background: var(--success-soft); color: var(--success); }
.badge.status[data-ok="false"] { background: var(--danger-soft); color: var(--danger); }
.empty { text-align: center; color: var(--text-secondary); padding: 24px 16px; }
.load-more { display: flex; justify-content: center; margin-top: 12px; }
@media (max-width: 760px) {
  .turns-list-view { padding: 12px; }
  .filter-search, .filter-preset, .filter-dt, .filter-select, .filter-select-short, .filter-select-tags { width: 100%; min-width: 0; flex-basis: 100%; }
  .dt-sep { display: none; }
  .turn-row { grid-template-columns: 1fr; gap: 10px; padding: 10px 12px; }
  .col { padding: 0; }
  .col + .col { padding-top: 10px; border-left: 0; border-top: 1px dashed var(--border); }
  .session-foot { align-items: flex-start; flex-direction: column; gap: 6px; }
}
</style>
