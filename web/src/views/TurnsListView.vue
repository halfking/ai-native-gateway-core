<script setup lang="ts">
// TurnsListView — 多维层级会话工作台（项目/用户/客户端/API Key + 总结资产化）
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  extractNoTopicSessionToMemora,
  extractSessionToMemora,
} from '../api/memora'
import { fetchSessionTurnsTree, type SessionTurnTreeItem } from '../api/sessionTurnsTree'
import { triggerInstantSummary } from '../api/sessions_v2'
import {
  listTurnsFilterOptions,
  listTurnsSessions,
  type TurnGroupItem,
  type TurnsFilterOptions,
  type TurnsSessionGroup,
} from '../api/turns'
import TurnsFilterBar from '../components/turns/TurnsFilterBar.vue'
import TurnsSessionCard from '../components/turns/TurnsSessionCard.vue'
import {
  buildDisplayGroups,
  buildTurnsParams,
  formatTokens,
  type TurnsFilterState,
  type TurnsGroupBy,
} from '../components/turns/turnsListHelpers'

const router = useRouter()
const loading = ref(false)
const loadingMore = ref(false)
const loaded = ref(false)
const items = ref<TurnsSessionGroup[]>([])
const hasMore = ref(false)
const nextCursor = ref('')
const error = ref('')
const expandedSessions = ref<Set<string>>(new Set())
const collapsedGroups = ref<Set<string>>(new Set())
const collapsedTasks = ref<Set<string>>(new Set())
const selectedIds = ref<Set<string>>(new Set())
const summaryExpanded = ref<Set<string>>(new Set())
const groupBy = ref<TurnsGroupBy>('project')
const advancedExpanded = ref(false)
const requestVersion = ref(0)
let controller: AbortController | null = null

const filterState = reactive<TurnsFilterState>({
  search: '', model: '', provider: '', statusCode: '',
  projectId: '', taskId: '', client: '', ownerUser: '',
  apiKeyId: '', status: '', tags: [],
  dateFrom: '', dateTo: '', timePreset: 'all',
})

const filterOptions = ref<TurnsFilterOptions>({
  projects: [], tasks: [], owners: [], clients: [], tags: [],
  models: [], providers: [], status_codes: [], api_keys: [],
})

const childOpsMap = ref<Record<string, SessionTurnTreeItem[]>>({})
const childOpsLoading = ref<Record<string, boolean>>({})
const summaryBusy = ref<Record<string, boolean>>({})
const assetBusy = ref<Record<string, boolean>>({})
const assetMsg = ref<Record<string, string>>({})
const batchBusy = ref(false)

const displayGroups = computed(() => buildDisplayGroups(items.value, groupBy.value))
const selectedCount = computed(() => selectedIds.value.size)

async function load(reset = true) {
  const { params, error: paramError } = buildTurnsParams(
    filterState,
    reset ? undefined : nextCursor.value || undefined,
  )
  if (!params) {
    error.value = paramError
    return
  }
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
    collapsedGroups.value = new Set()
    collapsedTasks.value = new Set()
    selectedIds.value = new Set()
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
  try {
    const opts = await listTurnsFilterOptions()
    filterOptions.value = { ...opts, api_keys: opts.api_keys || [] }
  } catch (cause) {
    console.warn('load filter options failed', cause)
  }
}

function toggleSession(id: string) {
  const next = new Set(expandedSessions.value)
  if (next.has(id)) next.delete(id)
  else {
    next.add(id)
    void loadChildOps(id)
  }
  expandedSessions.value = next
}

async function loadChildOps(sessionId: string) {
  if (childOpsMap.value[sessionId] || childOpsLoading.value[sessionId]) return
  childOpsLoading.value = { ...childOpsLoading.value, [sessionId]: true }
  try {
    const resp = await fetchSessionTurnsTree(sessionId, { limit: 100 })
    childOpsMap.value = { ...childOpsMap.value, [sessionId]: resp.turns || [] }
  } catch {
    childOpsMap.value = { ...childOpsMap.value, [sessionId]: [] }
  } finally {
    childOpsLoading.value = { ...childOpsLoading.value, [sessionId]: false }
  }
}

function toggleGroup(id: string) {
  const next = new Set(collapsedGroups.value)
  next.has(id) ? next.delete(id) : next.add(id)
  collapsedGroups.value = next
}
function toggleTask(id: string) {
  const next = new Set(collapsedTasks.value)
  next.has(id) ? next.delete(id) : next.add(id)
  collapsedTasks.value = next
}
function taskKey(project: string, task: string) { return `${project}::${task}` }

function setSelected(id: string, checked: boolean) {
  const next = new Set(selectedIds.value)
  checked ? next.add(id) : next.delete(id)
  selectedIds.value = next
}

function openSession(session: TurnsSessionGroup) {
  router.push({ path: `/admin/sessions/${session.session_id}` })
}
function openSessionById(id: string) {
  router.push({ path: `/admin/sessions/${id}` })
}
function openTurn(session: TurnsSessionGroup, turn: TurnGroupItem) {
  router.push({
    path: `/admin/sessions/${session.session_id}`,
    query: { turn: String(turn.turn_no), focus: '1' },
  })
}

async function refreshSessionSummary(sessionId: string) {
  try {
    const resp = await listTurnsSessions({ search: sessionId, limit: 5 })
    const hit = resp.items.find(s => s.session_id === sessionId)
    if (!hit) return
    items.value = items.value.map(s => (s.session_id === sessionId ? { ...hit, turns: hit.turns?.length ? hit.turns : s.turns } : s))
  } catch { /* keep existing */ }
}

async function resummarize(session: TurnsSessionGroup) {
  summaryBusy.value = { ...summaryBusy.value, [session.session_id]: true }
  assetMsg.value = { ...assetMsg.value, [session.session_id]: '' }
  try {
    await triggerInstantSummary(session.session_id)
    await refreshSessionSummary(session.session_id)
    assetMsg.value = { ...assetMsg.value, [session.session_id]: '已触发重新归纳' }
  } catch (cause) {
    assetMsg.value = { ...assetMsg.value, [session.session_id]: cause instanceof Error ? cause.message : '归纳失败' }
  } finally {
    summaryBusy.value = { ...summaryBusy.value, [session.session_id]: false }
  }
}

async function extractAsset(session: TurnsSessionGroup) {
  assetBusy.value = { ...assetBusy.value, [session.session_id]: true }
  assetMsg.value = { ...assetMsg.value, [session.session_id]: '' }
  try {
    const taskId = session.task_id && !session.task_id.startsWith('auto') ? session.task_id : ''
    const result = taskId
      ? await extractSessionToMemora(taskId, { session_id: session.session_id })
      : await extractNoTopicSessionToMemora({ prefix: session.session_id, hours: 168 })
    assetMsg.value = {
      ...assetMsg.value,
      [session.session_id]: result.error
        ? result.error
        : `已沉淀 ${result.written} 条资产${result.skipped_duplicate ? `（跳过重复 ${result.skipped_duplicate}）` : ''}`,
    }
  } catch (cause) {
    assetMsg.value = { ...assetMsg.value, [session.session_id]: cause instanceof Error ? cause.message : '沉淀失败' }
  } finally {
    assetBusy.value = { ...assetBusy.value, [session.session_id]: false }
  }
}

async function runBatch(kind: 'summary' | 'asset') {
  const ids = [...selectedIds.value]
  if (!ids.length || batchBusy.value) return
  batchBusy.value = true
  const queue = [...ids]
  const workers = [0, 1].map(async () => {
    while (queue.length) {
      const id = queue.shift()
      if (!id) break
      const session = items.value.find(s => s.session_id === id)
      if (!session) continue
      if (kind === 'summary') await resummarize(session)
      else await extractAsset(session)
    }
  })
  await Promise.all(workers)
  batchBusy.value = false
}

function clearChip(key: string) {
  const map: Record<string, Partial<TurnsFilterState>> = {
    search: { search: '' },
    model: { model: '' },
    time: { timePreset: 'all', dateFrom: '', dateTo: '' },
    provider: { provider: '' },
    statusCode: { statusCode: '' },
    projectId: { projectId: '' },
    taskId: { taskId: '' },
    client: { client: '' },
    ownerUser: { ownerUser: '' },
    apiKeyId: { apiKeyId: '' },
    status: { status: '' },
    tags: { tags: [] },
  }
  Object.assign(filterState, map[key] || {})
  void load(true)
}

function resetAll() {
  Object.assign(filterState, {
    search: '', model: '', provider: '', statusCode: '',
    projectId: '', taskId: '', client: '', ownerUser: '',
    apiKeyId: '', status: '', tags: [],
    dateFrom: '', dateTo: '', timePreset: 'all',
  })
  void load(true)
}

function toggleSummary(id: string) {
  const next = new Set(summaryExpanded.value)
  next.has(id) ? next.delete(id) : next.add(id)
  summaryExpanded.value = next
}

onMounted(() => { void load(true); void loadFilterOptions() })
onBeforeUnmount(() => controller?.abort())
</script>

<template>
  <div class="turns-list-view">
    <div class="header">
      <h1>会话与轮次</h1>
      <p class="subtitle">多维层级浏览 · 展开查看全部轮次摘要 · 可重新归纳并沉淀为资产</p>
    </div>

    <TurnsFilterBar
      :state="filterState"
      :filter-options="filterOptions"
      :group-by="groupBy"
      :advanced-expanded="advancedExpanded"
      @update:state="Object.assign(filterState, $event)"
      @update:group-by="groupBy = $event"
      @update:advanced-expanded="advancedExpanded = $event"
      @query="load(true)"
      @reset="resetAll"
      @clear-chip="clearChip"
    />

    <div v-if="selectedCount" class="batch-bar">
      <span>已选 {{ selectedCount }} 个会话</span>
      <button class="btn btn-secondary" type="button" :disabled="batchBusy" @click="runBatch('summary')">批量重新归纳</button>
      <button class="btn btn-primary" type="button" :disabled="batchBusy" @click="runBatch('asset')">批量沉淀资产</button>
    </div>

    <div v-if="error" class="error-banner" role="alert">
      <span>{{ error }}</span>
      <button class="btn btn-secondary" type="button" @click="load(true)">重试</button>
    </div>

    <div class="list">
      <div v-if="loading && !loaded" class="session-skeleton" data-testid="turns-skeleton">
        <div v-for="n in 4" :key="n" class="skeleton-card" />
      </div>
      <div v-else-if="error && !items.length" class="empty" data-testid="turns-error-state">加载会话失败，请重试</div>
      <div v-else-if="loaded && items.length === 0" class="empty" data-testid="turns-empty">暂无符合条件的会话</div>
      <div v-else class="turns-results">
        <div v-for="group in displayGroups" :key="group.key" class="group-card">
          <button
            v-if="groupBy !== 'flat'"
            class="group-header"
            type="button"
            :aria-expanded="!collapsedGroups.has(group.key)"
            @click="toggleGroup(group.key)"
          >
            <span class="caret" :class="{ open: !collapsedGroups.has(group.key) }">▸</span>
            <span class="group-title">{{ group.label }}</span>
            <span class="badge">{{ group.sessionCount }} 会话</span>
            <span class="badge">{{ group.totalTurns }} 轮</span>
            <span class="badge">{{ formatTokens(group.totalTokens) }} tok</span>
            <span v-if="group.totalCost > 0" class="badge cost">${{ group.totalCost.toFixed(4) }}</span>
          </button>
          <div v-if="groupBy === 'flat' || !collapsedGroups.has(group.key)" class="group-body">
            <div v-for="task in group.tasks" :key="taskKey(group.key, task.key)">
              <button
                v-if="groupBy === 'project' && task.label"
                class="task-header"
                type="button"
                :aria-expanded="!collapsedTasks.has(taskKey(group.key, task.key))"
                @click="toggleTask(taskKey(group.key, task.key))"
              >
                <span class="caret" :class="{ open: !collapsedTasks.has(taskKey(group.key, task.key)) }">▸</span>
                <span class="task-title">{{ task.label }}</span>
                <span class="badge">{{ task.sessions.length }} 会话</span>
              </button>
              <div
                v-if="groupBy !== 'project' || !task.label || !collapsedTasks.has(taskKey(group.key, task.key))"
                class="task-sessions"
                :class="{ nested: groupBy === 'project' && !!task.label }"
              >
                <TurnsSessionCard
                  v-for="session in task.sessions"
                  :key="session.session_id"
                  :session="session"
                  :expanded="expandedSessions.has(session.session_id)"
                  :selected="selectedIds.has(session.session_id)"
                  :summary-busy="!!summaryBusy[session.session_id]"
                  :asset-busy="!!assetBusy[session.session_id]"
                  :asset-msg="assetMsg[session.session_id]"
                  :child-ops="childOpsMap[session.session_id]"
                  :child-ops-loading="!!childOpsLoading[session.session_id]"
                  :summary-expanded="summaryExpanded.has(session.session_id)"
                  @toggle="toggleSession(session.session_id)"
                  @select="setSelected(session.session_id, $event)"
                  @open-session="openSession(session)"
                  @open-turn="openTurn(session, $event)"
                  @open-parent="openSessionById"
                  @resummarize="resummarize(session)"
                  @extract-asset="extractAsset(session)"
                  @toggle-summary="toggleSummary(session.session_id)"
                />
              </div>
            </div>
          </div>
        </div>
      </div>
      <div v-if="hasMore" class="load-more">
        <button class="btn btn-secondary" type="button" :disabled="loadingMore" @click="load(false)">
          {{ loadingMore ? '加载中...' : '加载更早的会话' }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.turns-list-view { background: var(--bg); min-height: 100vh; padding: 16px 24px; max-width: 1400px; margin: 0 auto; }
.header { margin-bottom: 16px; }
.header h1 { font-size: 24px; font-weight: 600; color: var(--text); margin: 0 0 8px; }
.subtitle { font-size: 14px; color: var(--text-secondary); margin: 0; }
.batch-bar { display: flex; gap: 8px; align-items: center; margin-bottom: 12px; padding: 8px 12px; border-radius: 6px; background: var(--primary-soft); color: var(--accent); }
.error-banner { display: flex; justify-content: space-between; gap: 12px; align-items: center; background: var(--danger-soft); border: 1px solid var(--danger); border-radius: 6px; padding: 12px; margin-bottom: 16px; color: var(--danger); }
.list { display: flex; flex-direction: column; gap: 12px; }
.session-skeleton { display: grid; gap: 10px; }
.skeleton-card { height: 150px; border-radius: 8px; background: var(--surface-secondary); animation: turns-pulse 1.2s ease-in-out infinite alternate; }
@keyframes turns-pulse { from { opacity: .55; } to { opacity: 1; } }
.group-card { border: 1px solid var(--border); border-radius: 8px; background: var(--surface-primary); overflow: hidden; }
.group-header, .task-header { display: flex; align-items: center; gap: 8px; width: 100%; padding: 10px 16px; cursor: pointer; text-align: left; border: 0; background: var(--surface-secondary); color: var(--text-primary); }
.task-header { padding: 6px 8px; background: transparent; }
.group-header:hover, .task-header:hover { background: var(--bg-hover); }
.group-body { padding: 10px 12px 12px; display: flex; flex-direction: column; gap: 10px; }
.task-sessions { display: flex; flex-direction: column; gap: 10px; }
.task-sessions.nested { padding-left: 18px; }
.caret { display: inline-block; transition: transform .15s; color: var(--text-muted); }
.caret.open { transform: rotate(90deg); }
.badge { padding: 2px 6px; border-radius: 4px; font-size: 11px; background: var(--surface-secondary); color: var(--text-secondary); }
.badge.cost { background: var(--warning-soft); color: var(--warning); }
.empty { text-align: center; color: var(--text-secondary); padding: 24px 16px; }
.load-more { display: flex; justify-content: center; margin-top: 12px; }
.btn { height: 36px; padding: 0 16px; border-radius: 6px; font-size: 14px; font-weight: 500; cursor: pointer; }
.btn-primary { background: var(--accent); color: white; border: 0; }
.btn-secondary { background: var(--surface-primary); color: var(--text-primary); border: 1px solid var(--border); }
.btn-secondary:disabled, .btn-primary:disabled { opacity: .5; cursor: not-allowed; }
@media (max-width: 760px) { .turns-list-view { padding: 12px; } }
</style>
