<script setup lang="ts">
// TurnsListView.vue — 跨会话轮次列表页（会话分组 / 分层展示，2026-08-10）
// 最外层是会话：展示会话主题（topic/title）、摘要（summary）、状态、用量汇总、
// 压缩汇总、失败/failover 次数、使用模型、时长等。
// 点击会话展开后内层展示该会话的轮次列表：每轮请求/响应摘要 + 压缩/缓存/超时/
// failover 明细，点击轮次跳转到会话详情页。
// 数据来自 GET /api/admin/turns/sessions（gateway.sessions + session_turns）。

import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElSelect, ElOption } from 'element-plus'
import {
  listTurnsSessions,
  listTurnsFilterOptions,
  type TurnsSessionGroup,
  type TurnsFilterOptions,
  type TurnGroupItem
} from '../api/turns'

const router = useRouter()
const loading = ref(false)
const items = ref<TurnsSessionGroup[]>([])
const hasMore = ref(false)
const nextCursor = ref('')
const error = ref('')
const expandedSessions = ref<Set<string>>(new Set())

// 筛选参数
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

// 各筛选维度的热门可选值（来自 /turns/sessions/filter-options）
const filterOptions = ref<TurnsFilterOptions>({
  projects: [],
  tasks: [],
  owners: [],
  clients: [],
  tags: [],
  models: [],
  providers: [],
  status_codes: []
})

// datetime-local 值 → RFC3339，非法值返回空
function toRFC3339(v: string): string {
  if (!v) return ''
  const d = new Date(v)
  return Number.isNaN(d.getTime()) ? '' : d.toISOString()
}

async function load(reset = true) {
  loading.value = true
  error.value = ''
  try {
    if (reset) {
      items.value = []
      nextCursor.value = ''
      expandedSessions.value = new Set()
    }
    const params: Parameters<typeof listTurnsSessions>[0] = { limit: 20 }
    if (nextCursor.value) params.cursor = nextCursor.value
    if (modelFilter.value) params.model = modelFilter.value
    if (providerFilter.value) params.provider = providerFilter.value
    if (statusCodeFilter.value) params.status_code = parseInt(statusCodeFilter.value, 10)
    if (projectFilter.value) params.project_id = projectFilter.value
    if (taskFilter.value) params.task_id = taskFilter.value
    if (searchFilter.value) params.search = searchFilter.value
    if (tagsFilter.value.length > 0) params.tags = tagsFilter.value.join(',')
    if (clientFilter.value) params.client = clientFilter.value
    if (ownerUserFilter.value) params.owner_user = ownerUserFilter.value
    const tsFrom = toRFC3339(dateFromFilter.value)
    const tsTo = toRFC3339(dateToFilter.value)
    if (tsFrom) params.ts_from = tsFrom
    if (tsTo) params.ts_to = tsTo

    const res = await listTurnsSessions(params)
    items.value = [...items.value, ...res.items]
    hasMore.value = res.has_more
    nextCursor.value = res.next_cursor
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

// 加载各筛选维度的热门可选值（近 30 天实际出现的取值），失败不影响列表
async function loadFilterOptions() {
  try {
    filterOptions.value = await listTurnsFilterOptions()
  } catch (e: unknown) {
    // 选项加载失败静默降级：下拉退化为自由输入（el-select 仍可输入）
    const msg = e instanceof Error ? e.message : String(e)
    console.warn('load filter options failed:', msg)
  }
}

function toggleSession(sessionId: string) {
  const next = new Set(expandedSessions.value)
  if (next.has(sessionId)) {
    next.delete(sessionId)
  } else {
    next.add(sessionId)
  }
  expandedSessions.value = next
}

function openTurn(session: TurnsSessionGroup, turn: TurnGroupItem) {
  router.push({
    path: `/admin/sessions/${session.session_id}`,
    query: { turn: String(turn.turn_no), focus: '1' }
  })
}

function openSession(session: TurnsSessionGroup) {
  router.push({ path: `/admin/sessions/${session.session_id}` })
}

// 会话标题优先取 title，回退到 topic / intent / session_id 前缀
function sessionTitle(s: TurnsSessionGroup): string {
  return s.title || s.topic || s.intent || s.session_id.slice(0, 12) + '...'
}

function sessionTopic(s: TurnsSessionGroup): string {
  if (s.topic) return s.topic
  if (s.intent) return s.intent
  return ''
}

// 客户端 / 智能体 展示：优先 application_code，其次 client_id / client_type
function sessionClient(s: TurnsSessionGroup): string {
  return s.application_code || s.client_id || s.client_type || ''
}

function formatMs(ms: number): string {
  if (!ms || ms <= 0) return '—'
  if (ms < 1000) return `${ms}ms`
  const sec = Math.round(ms / 1000)
  if (sec < 60) return `${sec}s`
  const min = Math.floor(sec / 60)
  const rem = sec % 60
  return `${min}m ${rem}s`
}

function formatBytesTokens(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`
  return String(n)
}

function statusLabel(state: string): string {
  const map: Record<string, string> = {
    active: '进行中',
    closed: '已关闭',
    archived: '已归档',
    deleted: '已删除'
  }
  return map[state] || state
}

function resetAndLoad() {
  load(true)
}

function resetAll() {
  modelFilter.value = ''
  providerFilter.value = ''
  statusCodeFilter.value = ''
  projectFilter.value = ''
  taskFilter.value = ''
  searchFilter.value = ''
  tagsFilter.value = []
  clientFilter.value = ''
  ownerUserFilter.value = ''
  dateFromFilter.value = ''
  dateToFilter.value = ''
  load(true)
}

onMounted(() => {
  load(true)
  loadFilterOptions()
})
</script>

<template>
  <div class="turns-list-view">
    <div class="header">
      <h1>会话与轮次</h1>
      <p class="subtitle">最外层为会话（主题 / 摘要 / 用量 / 压缩 / failover），展开查看该会话的每个轮次</p>
    </div>

    <!-- 筛选区 -->
    <div class="filter-bar">
      <el-select
        v-model="modelFilter"
        filterable
        allow-create
        default-first-option
        clearable
        placeholder="模型"
        class="filter-select"
        @change="resetAndLoad"
      >
        <el-option v-for="v in filterOptions.models" :key="v" :label="v" :value="v" />
      </el-select>
      <el-select
        v-model="providerFilter"
        filterable
        allow-create
        default-first-option
        clearable
        placeholder="供应商"
        class="filter-select"
        @change="resetAndLoad"
      >
        <el-option v-for="v in filterOptions.providers" :key="v" :label="v" :value="v" />
      </el-select>
      <el-select
        v-model="statusCodeFilter"
        filterable
        allow-create
        default-first-option
        clearable
        placeholder="状态码"
        class="filter-select filter-select-short"
        @change="resetAndLoad"
      >
        <el-option v-for="v in filterOptions.status_codes" :key="v" :label="v" :value="v" />
      </el-select>
      <el-select
        v-model="projectFilter"
        filterable
        allow-create
        default-first-option
        clearable
        placeholder="项目ID"
        class="filter-select filter-select-short"
        @change="resetAndLoad"
      >
        <el-option v-for="v in filterOptions.projects" :key="v" :label="v" :value="v" />
      </el-select>
      <el-select
        v-model="taskFilter"
        filterable
        allow-create
        default-first-option
        clearable
        placeholder="任务ID"
        class="filter-select filter-select-short"
        @change="resetAndLoad"
      >
        <el-option v-for="v in filterOptions.tasks" :key="v" :label="v" :value="v" />
      </el-select>
      <el-select
        v-model="clientFilter"
        filterable
        allow-create
        default-first-option
        clearable
        placeholder="客户端 / 智能体"
        class="filter-select"
        @change="resetAndLoad"
      >
        <el-option v-for="v in filterOptions.clients" :key="v" :label="v" :value="v" />
      </el-select>
      <el-select
        v-model="ownerUserFilter"
        filterable
        allow-create
        default-first-option
        clearable
        placeholder="属主用户"
        class="filter-select"
        @change="resetAndLoad"
      >
        <el-option v-for="v in filterOptions.owners" :key="v" :label="v" :value="v" />
      </el-select>
      <el-select
        v-model="tagsFilter"
        multiple
        filterable
        allow-create
        default-first-option
        clearable
        collapse-tags
        collapse-tags-tooltip
        placeholder="标签"
        class="filter-select filter-select-tags"
        @change="resetAndLoad"
      >
        <el-option v-for="v in filterOptions.tags" :key="v" :label="v" :value="v" />
      </el-select>
      <input v-model="dateFromFilter" type="datetime-local" class="filter-input" @change="resetAndLoad" />
      <input v-model="dateToFilter" type="datetime-local" class="filter-input" @change="resetAndLoad" />
      <input
        v-model="searchFilter"
        type="text"
        placeholder="搜索 标题/主题/摘要"
        class="filter-input filter-search"
        @keyup.enter="resetAndLoad"
      />
      <button class="btn btn-primary" @click="resetAndLoad">查询</button>
      <button class="btn btn-secondary" @click="resetAll">清空</button>
    </div>

    <!-- 错误提示 -->
    <div v-if="error" class="error-banner">{{ error }}</div>

    <!-- 会话分组列表 -->
    <div class="list">
      <div v-for="session in items" :key="session.session_id" class="session-card">
        <!-- 外层级：会话摘要，点击展开/收起 -->
        <div class="session-header" @click="toggleSession(session.session_id)">
          <div class="session-head-line">
            <span class="caret" :class="{ open: expandedSessions.has(session.session_id) }">▸</span>
            <span class="status-badge" :data-state="session.status">{{ statusLabel(session.status) }}</span>
            <span class="session-title">{{ sessionTitle(session) }}</span>
            <span v-if="sessionTopic(session) && session.topic !== session.title" class="session-topic">
              主题：{{ sessionTopic(session) }}
            </span>
          </div>

          <div class="session-summary" v-if="session.summary">{{ session.summary }}</div>
          <div v-else class="session-summary muted">暂无会话摘要</div>

          <div class="session-context">
            <span v-if="session.project_id" class="ctx-badge project">项目 {{ session.project_id }}</span>
            <span v-if="session.task_id" class="ctx-badge task">任务 {{ session.task_id }}</span>
            <span v-if="session.owner_user" class="ctx-badge owner">用户 {{ session.owner_user }}</span>
            <span v-if="sessionClient(session)" class="ctx-badge client">
              {{ session.application_code ? '智能体' : '客户端' }} {{ sessionClient(session) }}
            </span>
            <span v-if="session.start_time" class="ts">开始 {{ new Date(session.start_time).toLocaleString() }}</span>
            <template v-for="tag in session.user_tags" :key="tag">
              <span class="badge tag">#{{ tag }}</span>
            </template>
          </div>

          <div class="session-meta">
            <span class="badge">{{ session.total_turns }} 轮</span>
            <span class="badge">{{ formatBytesTokens(session.total_tokens) }} tok</span>
            <span class="badge cost">${{ session.total_cost_usd.toFixed(4) }}</span>
            <span class="badge">时长 {{ formatMs(session.duration_ms) }}</span>
            <span v-if="session.failover_count > 0" class="badge warn">failover ×{{ session.failover_count }}</span>
            <span v-if="session.error_count > 0" class="badge error">错误 ×{{ session.error_count }}</span>
            <span v-if="session.compression.applied_count > 0" class="badge compression">
              压缩 {{ session.compression.applied_count }} 次 · 省 {{ formatBytesTokens(session.compression.tokens_saved) }} tok
            </span>
            <template v-for="m in session.models_used" :key="m">
              <span class="badge model">{{ m }}</span>
            </template>
          </div>

          <div class="session-foot">
            <span class="ts">更新 {{ new Date(session.updated_at).toLocaleString() }}</span>
            <span class="link" @click.stop="openSession(session)">进入会话详情 →</span>
          </div>
        </div>

        <!-- 内层级：该会话的轮次 -->
        <div v-if="expandedSessions.has(session.session_id)" class="turns-block">
          <div v-if="session.turns.length === 0" class="empty">该会话暂无匹配轮次</div>
          <div
            v-for="turn in session.turns"
            :key="`${session.session_id}-${turn.turn_no}`"
            class="turn-row"
            @click="openTurn(session, turn)"
          >
            <div class="col col-req">
              <div class="meta">
                <span class="turn-no">#{{ turn.turn_no }}</span>
                <span class="ts">{{ new Date(turn.ts).toLocaleString() }}</span>
                <span v-if="turn.attempt_no > 0" class="mini-badge warn">failover#{{ turn.attempt_no }}</span>
                <span :class="['verdict', `tag-${turn.injection_verdict || 'skip'}`]">
                  inj: {{ turn.injection_verdict || 'skip' }}
                </span>
                <span v-if="turn.latency_ms !== undefined" class="ts">⏱ {{ formatMs(turn.latency_ms) }}</span>
              </div>
              <div class="preview">{{ turn.title || '(无请求摘要)' }}</div>
              <div class="badges">
                <span class="badge">req {{ formatBytesTokens(turn.request_tokens) }}</span>
                <span class="badge">Δ{{ turn.request_tokens }}</span>
                <span v-if="turn.cache_read_tokens > 0" class="badge cache">cache读 {{ formatBytesTokens(turn.cache_read_tokens) }}</span>
                <span v-if="turn.cache_write_tokens > 0" class="badge cache">cache写 {{ formatBytesTokens(turn.cache_write_tokens) }}</span>
                <span v-if="turn.attachment_count > 0" class="badge">📎 {{ turn.attachment_count }}</span>
                <span v-if="turn.submit_mode === 'delta'" class="badge submit">delta</span>
                <span class="badge model">{{ turn.model }}</span>
              </div>
            </div>
            <div class="col col-resp">
              <div class="meta">
                <span class="turn-no">#{{ turn.turn_no }}</span>
                <span :class="['verdict', `tag-${turn.output_verdict || 'skip'}`]">
                  out: {{ turn.output_verdict || 'skip' }}
                </span>
                <span v-if="turn.compression_applied" class="badge compression">
                  压缩{{ turn.compression_strategy ? '·' + turn.compression_strategy : '' }}
                  <span v-if="turn.compression_tokens_saved !== undefined"> 省{{ formatBytesTokens(turn.compression_tokens_saved) }}</span>
                </span>
              </div>
              <div class="preview">{{ turn.summary || '(无回复摘要)' }}</div>
              <div class="badges">
                <span class="badge">resp {{ formatBytesTokens(turn.response_tokens) }}</span>
                <span class="badge">Δ{{ turn.response_tokens }}</span>
                <span class="badge cost">${{ turn.cost_usd.toFixed(4) }}</span>
                <span
                  class="badge status"
                  :data-ok="turn.status_code < 400 ? 'true' : 'false'"
                >{{ turn.status_code }}</span>
                <span v-if="!turn.success && turn.error_kind" class="badge error" :title="turn.error_kind">
                  {{ (turn.error_kind || 'error').slice(0, 24) }}
                </span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div v-if="!loading && items.length === 0" class="empty">暂无会话记录</div>
      <div v-if="hasMore" class="load-more">
        <button class="btn btn-secondary" :disabled="loading" @click="load(false)">
          {{ loading ? '加载中...' : '加载更早的会话' }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.turns-list-view {
  background: #f3f4f6;
  min-height: 100vh;
  padding: 16px 24px;
  max-width: 1400px;
  margin: 0 auto;
}
.header {
  margin-bottom: 16px;
}
.header h1 {
  font-size: 24px;
  font-weight: 600;
  color: var(--text);
  margin: 0 0 8px 0;
}
.subtitle {
  font-size: 14px;
  color: var(--text-secondary);
  margin: 0;
}
.filter-bar {
  display: flex;
  gap: 8px;
  margin-bottom: 16px;
  flex-wrap: wrap;
}
.filter-input {
  height: 36px;
  padding: 4px 12px;
  background: white;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  font-size: 14px;
  min-width: 140px;
}
.filter-input:focus {
  outline: none;
  border-color: #3b82f6;
  box-shadow: 0 0 0 2px rgba(59, 130, 246, 0.1);
}
.filter-search {
  min-width: 220px;
}
/* 下拉选择器宽度按内容密度收敛，不铺满整行（rule 12 §5.1） */
.filter-select {
  width: 170px;
  flex: 0 0 170px;
}
.filter-select-short {
  width: 130px;
  flex-basis: 130px;
}
.filter-select-tags {
  width: 180px;
  flex: 0 0 180px;
}
.filter-select :deep(.el-select__wrapper) {
  min-height: 36px;
}
.btn {
  height: 36px;
  padding: 0 16px;
  border: none;
  border-radius: 6px;
  font-size: 14px;
  font-weight: 500;
  cursor: pointer;
  transition: background 0.15s;
}
.btn-primary {
  background: #3b82f6;
  color: white;
}
.btn-primary:hover {
  background: #2563eb;
}
.btn-secondary {
  background: white;
  color: #374151;
  border: 1px solid #d1d5db;
}
.btn-secondary:hover {
  background: #f9fafb;
}
.btn-secondary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.error-banner {
  background: #fee;
  border: 1px solid #fcc;
  border-radius: 6px;
  padding: 12px;
  margin-bottom: 16px;
  color: #c33;
  font-size: 14px;
}
.list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

/* ---- 会话卡片（外层） ---- */
.session-card {
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  background: white;
  overflow: hidden;
}
.session-header {
  padding: 12px 16px;
  cursor: pointer;
  transition: background 0.15s;
}
.session-header:hover {
  background: #f9fafb;
}
.session-head-line {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.caret {
  display: inline-block;
  transition: transform 0.15s;
  color: #9ca3af;
  font-size: 14px;
}
.caret.open {
  transform: rotate(90deg);
}
.status-badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
}
.status-badge[data-state="active"] {
  background: #d1fae5;
  color: #065f46;
}
.status-badge[data-state="closed"] {
  background: #e0e7ff;
  color: #3730a3;
}
.status-badge[data-state="archived"] {
  background: #f3f4f6;
  color: #6b7280;
}
.status-badge[data-state="deleted"] {
  background: #fee2e2;
  color: #991b1b;
}
.session-title {
  font-size: 15px;
  font-weight: 600;
  color: #111827;
}
.session-topic {
  font-size: 12px;
  color: #1e40af;
  background: #eff6ff;
  padding: 2px 8px;
  border-radius: 4px;
}
.session-summary {
  font-size: 13px;
  color: #374151;
  margin: 8px 0;
  line-height: 1.5;
}
.session-summary.muted {
  color: #9ca3af;
}
.session-meta {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.session-context {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  align-items: center;
  margin-bottom: 8px;
  font-size: 12px;
}
.ctx-badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 500;
  color: #374151;
  background: #f3f4f6;
}
.ctx-badge.project {
  background: #fef3c7;
  color: #92400e;
}
.ctx-badge.task {
  background: #e0e7ff;
  color: #3730a3;
}
.ctx-badge.owner {
  background: #ecfeff;
  color: #155e75;
}
.ctx-badge.client {
  background: #f0fdf4;
  color: #166534;
}
.badge.tag {
  background: #f5f3ff;
  color: #6d28d9;
}
.session-foot {
  margin-top: 8px;
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.link {
  font-size: 13px;
  color: #2563eb;
  cursor: pointer;
}
.link:hover {
  text-decoration: underline;
}

/* ---- 内层轮次 ---- */
.turns-block {
  border-top: 1px solid #e5e7eb;
  background: #fafafa;
  padding: 4px 0;
}
.turn-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  border-bottom: 1px solid #f0f0f0;
  padding: 10px 16px;
  cursor: pointer;
  transition: background 0.15s;
  background: white;
}
.turn-row:last-child {
  border-bottom: none;
}
.turn-row:hover {
  background: #f3f4f6;
}
.col {
  padding: 0 8px;
}
.col + .col {
  border-left: 1px dashed #e5e7eb;
}
.meta {
  display: flex;
  gap: 8px;
  font-size: 12px;
  color: #6b7280;
  margin-bottom: 6px;
  flex-wrap: wrap;
}
.turn-no {
  font-weight: 600;
  color: #374151;
}
.ts {
  color: #9ca3af;
}
.verdict {
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 500;
}
.tag-pass {
  background: #d1fae5;
  color: #065f46;
}
.tag-warn {
  background: #fef3c7;
  color: #92400e;
}
.tag-block {
  background: #fee2e2;
  color: #991b1b;
}
.tag-skip {
  background: #f3f4f6;
  color: #6b7280;
}
.preview {
  font-size: 13px;
  color: #374151;
  margin-bottom: 6px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.badges {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.badge {
  background: #f3f4f6;
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 11px;
  color: #6b7280;
}
.mini-badge {
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 500;
}
.badge.warn,
.mini-badge.warn {
  background: #fef3c7;
  color: #92400e;
}
.badge.error {
  background: #fee2e2;
  color: #991b1b;
}
.badge.compression {
  background: #ede9fe;
  color: var(--accent);
}
.badge.cache {
  background: #ecfeff;
  color: #155e75;
}
.badge.submit {
  background: #f0fdf4;
  color: #166534;
}
.badge.model {
  background: #eff6ff;
  color: #1e40af;
}
.badge.cost {
  background: #fef3c7;
  color: #92400e;
}
.badge.status[data-ok="true"] {
  background: #d1fae5;
  color: #065f46;
}
.badge.status[data-ok="false"] {
  background: #fee2e2;
  color: #991b1b;
}
.empty {
  text-align: center;
  color: #6b7280;
  padding: 24px 16px;
  font-size: 14px;
}
.load-more {
  display: flex;
  justify-content: center;
  margin-top: 12px;
}
</style>