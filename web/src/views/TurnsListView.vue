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

// 「更多筛选」区是否展开：常用筛选（搜索/模型/时间）常驻，其余收进折叠行
const advancedExpanded = ref(false)
// 时间快捷预设：all=不限（默认最近更新）、custom=手动输入起止时间
const timePreset = ref('all')

const timePresets: { value: string; label: string; hours: number }[] = [
  { value: 'all', label: '时间不限', hours: 0 },
  { value: 'h1', label: '最近 1 小时', hours: 1 },
  { value: 'h24', label: '最近 24 小时', hours: 24 },
  { value: 'd3', label: '最近 3 天', hours: 72 },
  { value: 'd7', label: '最近 7 天', hours: 168 }
]

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

// 会话 AI 总结的操作信息：生成模型 + 时间（体现"会话总结"这一后台动作）
function summaryMeta(s: TurnsSessionGroup): string {
  const parts: string[] = []
  if (s.summary_model) parts.push(s.summary_model)
  if (s.summary_generated_at) {
    parts.push(new Date(s.summary_generated_at).toLocaleString())
  }
  return parts.join(' · ')
}

// 模型徽标最多展示 3 个，其余折叠为 +N（完整列表在 title 里）
function visibleModels(s: TurnsSessionGroup): string[] {
  return (s.models_used || []).slice(0, 3)
}
function hiddenModelCount(s: TurnsSessionGroup): number {
  return Math.max(0, (s.models_used || []).length - 3)
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

// 时间预设：all 清空起止时间；h1/h24/d3/d7 设置起始时间为 now-hours，结束留空
function applyTimePreset(value: string) {
  const preset = timePresets.find(p => p.value === value)
  if (!preset) return
  if (value === 'all') {
    dateFromFilter.value = ''
    dateToFilter.value = ''
  } else {
    const from = new Date(Date.now() - preset.hours * 3600 * 1000)
    // datetime-local 需要 yyyy-MM-ddTHH:mm 本地格式
    dateFromFilter.value = localDatetime(from)
    dateToFilter.value = ''
  }
  resetAndLoad()
}

// 手动修改起止时间 → 预设切换为"自定义"（不重新触发查询，等用户点查询或回车）
function onDatetimeManualChange() {
  timePreset.value = 'custom'
  resetAndLoad()
}

function localDatetime(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// 更多筛选中已生效的条件数（用于折叠按钮提示）
function advancedActiveCount(): number {
  let n = 0
  if (providerFilter.value) n++
  if (statusCodeFilter.value) n++
  if (projectFilter.value) n++
  if (taskFilter.value) n++
  if (clientFilter.value) n++
  if (ownerUserFilter.value) n++
  if (tagsFilter.value.length > 0) n++
  return n
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
  timePreset.value = 'all'
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
      <p class="subtitle">按最近更新排序 · 最外层为会话（主题 / 摘要 / 模型 / 用量 / 压缩 / failover），展开查看每个轮次的请求与返回</p>
    </div>

    <!-- 筛选区：常用筛选常驻（搜索 / 模型 / 时间），其余收进「更多筛选」 -->
    <div class="filter-section">
      <div class="filter-bar">
        <input
          v-model="searchFilter"
          type="text"
          placeholder="搜索 标题 / 主题 / 摘要"
          class="filter-input filter-search"
          @keyup.enter="resetAndLoad"
        />
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
        <select
          :value="timePreset"
          class="filter-input filter-preset"
          @change="timePreset = ($event.target as HTMLSelectElement).value; applyTimePreset(timePreset)"
        >
          <option v-for="p in timePresets" :key="p.value" :value="p.value">{{ p.label }}</option>
          <option v-if="timePreset === 'custom'" value="custom">自定义时间段</option>
        </select>
        <input
          v-model="dateFromFilter"
          type="datetime-local"
          class="filter-input filter-dt"
          title="会话开始时间起"
          @change="onDatetimeManualChange"
        />
        <span class="dt-sep">~</span>
        <input
          v-model="dateToFilter"
          type="datetime-local"
          class="filter-input filter-dt"
          title="会话开始时间止"
          @change="onDatetimeManualChange"
        />
        <button class="btn btn-primary" @click="resetAndLoad">查询</button>
        <button class="btn btn-secondary" @click="resetAll">清空</button>
        <button class="btn btn-secondary" @click="advancedExpanded = !advancedExpanded">
          更多筛选
          <span v-if="advancedActiveCount() > 0" class="adv-count">{{ advancedActiveCount() }}</span>
          <span class="caret" :class="{ open: advancedExpanded }">▸</span>
        </button>
      </div>

      <div v-if="advancedExpanded" class="filter-bar filter-advanced">
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
      </div>
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

          <div class="session-summary" v-if="session.summary">
            {{ session.summary }}
            <span v-if="summaryMeta(session)" class="summary-meta" title="会话 AI 总结生成信息">
              AI 总结 · {{ summaryMeta(session) }}
            </span>
          </div>
          <div v-else class="session-summary muted">暂无会话摘要（未生成总结）</div>

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
            <template v-for="m in visibleModels(session)" :key="m">
              <span class="badge model">{{ m }}</span>
            </template>
            <span
              v-if="hiddenModelCount(session) > 0"
              class="badge model"
              :title="(session.models_used || []).join(', ')"
            >+{{ hiddenModelCount(session) }}</span>
          </div>

          <div class="session-foot">
            <span class="ts">更新 {{ new Date(session.updated_at).toLocaleString() }}</span>
            <span class="link" @click.stop="openSession(session)">进入会话详情 →</span>
          </div>
        </div>

        <!-- 内层级：该会话的轮次 -->
        <div v-if="expandedSessions.has(session.session_id)" class="turns-block">
          <div v-if="(session.turns || []).length === 0" class="empty">该会话暂无匹配轮次</div>
          <div
            v-for="turn in session.turns || []"
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
  background: var(--bg);
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
.filter-section {
  margin-bottom: 16px;
}
.filter-bar {
  display: flex;
  gap: 8px;
  margin-bottom: 8px;
  flex-wrap: wrap;
  align-items: center;
}
.filter-advanced {
  padding: 8px;
  border: 1px dashed var(--border);
  border-radius: 6px;
  background: var(--surface-secondary);
  margin-bottom: 16px;
}
.filter-preset {
  min-width: 130px;
  flex: 0 0 130px;
  cursor: pointer;
}
.filter-dt {
  min-width: 180px;
  flex: 0 0 180px;
}
.dt-sep {
  color: var(--text-muted);
  font-size: 13px;
}
.adv-count {
  display: inline-block;
  min-width: 16px;
  padding: 0 4px;
  margin-left: 2px;
  border-radius: 8px;
  background: var(--accent);
  color: white;
  font-size: 11px;
  line-height: 16px;
}
.filter-input {
  height: 36px;
  padding: 4px 12px;
  background: var(--surface-primary);
  border: 1px solid var(--border);
  border-radius: 6px;
  font-size: 14px;
  min-width: 140px;
}
.filter-input:focus {
  outline: none;
  border-color: var(--accent);
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 10%, transparent);
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
  background: var(--accent);
  color: white;
}
.btn-primary:hover {
  background: var(--accent-h);
}
.btn-secondary {
  background: var(--surface-primary);
  color: var(--text-primary);
  border: 1px solid var(--border);
}
.btn-secondary:hover {
  background: var(--bg-hover);
}
.btn-secondary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.error-banner {
  background: var(--danger-soft);
  border: 1px solid color-mix(in srgb, var(--danger) 30%, var(--border));
  border-radius: 6px;
  padding: 12px;
  margin-bottom: 16px;
  color: var(--danger);
  font-size: 14px;
}
.list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

/* ---- 会话卡片（外层） ---- */
.session-card {
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--surface-primary);
  overflow: hidden;
}
.session-header {
  padding: 12px 16px;
  cursor: pointer;
  transition: background 0.15s;
}
.session-header:hover {
  background: var(--bg-hover);
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
  color: var(--text-muted);
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
  background: var(--success-soft);
  color: var(--success);
}
.status-badge[data-state="closed"] {
  background: var(--primary-soft);
  color: var(--accent);
}
.status-badge[data-state="archived"] {
  background: var(--surface-secondary);
  color: var(--text-secondary);
}
.status-badge[data-state="deleted"] {
  background: var(--danger-soft);
  color: var(--danger);
}
.session-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
}
.session-topic {
  font-size: 12px;
  color: var(--accent);
  background: var(--primary-soft);
  padding: 2px 8px;
  border-radius: 4px;
}
.session-summary {
  font-size: 13px;
  color: var(--text-secondary);
  margin: 8px 0;
  line-height: 1.5;
}
.session-summary.muted {
  color: var(--text-muted);
}
.summary-meta {
  display: inline-block;
  margin-left: 8px;
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 11px;
  color: var(--text-muted);
  background: var(--surface-secondary);
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
  color: var(--text-secondary);
  background: var(--surface-secondary);
}
.ctx-badge.project {
  background: var(--warning-soft);
  color: var(--warning);
}
.ctx-badge.task {
  background: var(--primary-soft);
  color: var(--accent);
}
.ctx-badge.owner {
  background: var(--primary-soft);
  color: var(--accent);
}
.ctx-badge.client {
  background: var(--success-soft);
  color: var(--success);
}
.badge.tag {
  background: var(--primary-soft);
  color: var(--accent);
}
.session-foot {
  margin-top: 8px;
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.link {
  font-size: 13px;
  color: var(--accent);
  cursor: pointer;
}
.link:hover {
  text-decoration: underline;
}

/* ---- 内层轮次 ---- */
.turns-block {
  border-top: 1px solid var(--border);
  background: var(--surface-secondary);
  padding: 4px 0;
}
.turn-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  border-bottom: 1px solid var(--border);
  padding: 10px 16px;
  cursor: pointer;
  transition: background 0.15s;
  background: var(--surface-primary);
}
.turn-row:last-child {
  border-bottom: none;
}
.turn-row:hover {
  background: var(--bg-hover);
}
.col {
  padding: 0 8px;
}
.col + .col {
  border-left: 1px dashed var(--border);
}
.meta {
  display: flex;
  gap: 8px;
  font-size: 12px;
  color: var(--text-secondary);
  margin-bottom: 6px;
  flex-wrap: wrap;
}
.turn-no {
  font-weight: 600;
  color: var(--text-primary);
}
.ts {
  color: var(--text-muted);
}
.verdict {
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 500;
}
.tag-pass {
  background: var(--success-soft);
  color: var(--success);
}
.tag-warn {
  background: var(--warning-soft);
  color: var(--warning);
}
.tag-block {
  background: var(--danger-soft);
  color: var(--danger);
}
.tag-skip {
  background: var(--surface-secondary);
  color: var(--text-secondary);
}
.preview {
  font-size: 13px;
  color: var(--text-secondary);
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
  background: var(--surface-secondary);
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 11px;
  color: var(--text-secondary);
}
.mini-badge {
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 500;
}
.badge.warn,
.mini-badge.warn {
  background: var(--warning-soft);
  color: var(--warning);
}
.badge.error {
  background: var(--danger-soft);
  color: var(--danger);
}
.badge.compression {
  background: var(--primary-soft);
  color: var(--accent);
}
.badge.cache {
  background: var(--primary-soft);
  color: var(--accent);
}
.badge.submit {
  background: var(--success-soft);
  color: var(--success);
}
.badge.model {
  background: var(--primary-soft);
  color: var(--accent);
}
.badge.cost {
  background: var(--warning-soft);
  color: var(--warning);
}
.badge.status[data-ok="true"] {
  background: var(--success-soft);
  color: var(--success);
}
.badge.status[data-ok="false"] {
  background: var(--danger-soft);
  color: var(--danger);
}
.empty {
  text-align: center;
  color: var(--text-secondary);
  padding: 24px 16px;
  font-size: 14px;
}
.load-more {
  display: flex;
  justify-content: center;
  margin-top: 12px;
}
</style>
