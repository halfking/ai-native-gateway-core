<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  getMemoraSessions,
  getMemoraContext,
  getSessionMessages,
  type MemoraSession,
  type MemoraContextResponse,
  type SessionMessagesResponse,
  type RequestMessage,
} from '../api'

const view = ref<'list' | 'detail'>('list')

const sessions = ref<MemoraSession[]>([])
const loading = ref(false)
const error = ref('')

const searchQ = ref('')
const searchOwner = ref('')
const hours = ref(24)
const noTopicWindow = ref(1)
const showTopic = ref(true)
const showNoTopic = ref(true)

const topicSessions = computed(() => sessions.value.filter(s => !s.no_topic))
const noTopicSessions = computed(() => sessions.value.filter(s => s.no_topic))

async function loadSessions() {
  loading.value = true
  error.value = ''
  try {
    const resp = await getMemoraSessions({
      q: searchQ.value || undefined,
      owner_user: searchOwner.value || undefined,
      hours: hours.value,
      no_topic_window: noTopicWindow.value,
    })
    sessions.value = resp.sessions
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

function displayTitle(s: MemoraSession): string {
  if (s.no_topic) return s.no_topic_label || '[无主题会话]'
  return s.task_id || '[无标题]'
}

function displayModel(s: MemoraSession): string {
  return s.latest_model || '[空]'
}

function displayUser(s: MemoraSession): string {
  return s.api_key_owner_user || '[空]'
}

function displayKey(s: MemoraSession): string {
  return s.api_key_prefix || '[空]'
}

function fmtDate(v: string | null | undefined) {
  if (!v) return '—'
  return new Date(v).toLocaleString('zh-CN', { dateStyle: 'short', timeStyle: 'short' })
}

function fmtTime(v: string | null | undefined) {
  if (!v) return '—'
  return new Date(v).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function fmtDateFull(v: string | null | undefined) {
  if (!v) return '—'
  return new Date(v).toLocaleString('zh-CN')
}

function fmtScore(v: number) {
  return (v * 100).toFixed(0) + '%'
}

function fmtCost(v: number) {
  return '$' + v.toFixed(4)
}

function tagClass(tags: string[] | null): string {
  if (!tags || tags.length === 0) return 'badge-blue'
  const t = tags[0].toLowerCase()
  if (t.includes('pref') || t.includes('preference')) return 'badge-purple'
  if (t.includes('fact')) return 'badge-green'
  if (t.includes('context')) return 'badge-blue'
  return 'badge-blue'
}

// detail view state
const detailSession = ref<MemoraSession | null>(null)
const contextData = ref<MemoraContextResponse | null>(null)
const messagesData = ref<SessionMessagesResponse | null>(null)
const contextLoading = ref(false)
const messagesLoading = ref(false)
const activeTab = ref<'facts' | 'timeline'>('facts')

async function openDetail(s: MemoraSession) {
  detailSession.value = s
  contextData.value = null
  messagesData.value = null
  activeTab.value = 'facts'
  view.value = 'detail'

  if (!s.no_topic && s.task_id) {
    contextLoading.value = true
    try {
      contextData.value = await getMemoraContext(s.task_id)
    } catch {
      contextData.value = null
    } finally {
      contextLoading.value = false
    }
  }
}

function backToList() {
  view.value = 'list'
  detailSession.value = null
  contextData.value = null
  messagesData.value = null
}

async function loadTimeline() {
  if (!detailSession.value || detailSession.value.no_topic || !detailSession.value.task_id) return
  messagesLoading.value = true
  try {
    messagesData.value = await getSessionMessages(detailSession.value.task_id)
  } catch {
    messagesData.value = null
  } finally {
    messagesLoading.value = false
  }
}

async function switchTab(tab: 'facts' | 'timeline') {
  activeTab.value = tab
  if (tab === 'timeline' && !messagesData.value) {
    await loadTimeline()
  }
}

const visibleSessions = computed(() => {
  const all: MemoraSession[] = []
  if (showTopic.value) all.push(...topicSessions.value)
  if (showNoTopic.value) all.push(...noTopicSessions.value)
  return all
})

onMounted(loadSessions)
</script>

<template>
  <div class="page">
    <!-- ========== LIST VIEW ========== -->
    <template v-if="view === 'list'">
      <div class="page-header">
        <div class="header-title">
          <svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>
          </svg>
          <h2>会话上下文</h2>
        </div>
        <span class="sub">Memora L1 会话记忆 &amp; 对话线索</span>
      </div>

      <div v-if="error" class="alert alert-danger" style="margin-bottom:12px">{{ error }}</div>

      <div class="filter-bar">
        <input
          v-model="searchQ"
          placeholder="搜索 task / session / model…"
          class="filter-input"
          @keyup.enter="loadSessions"
        />
        <input
          v-model="searchOwner"
          placeholder="用户模糊匹配…"
          class="filter-input"
          style="width:160px"
          @keyup.enter="loadSessions"
        />
        <select v-model.number="hours" @change="loadSessions" class="filter-select">
          <option :value="6">6h</option>
          <option :value="24">24h</option>
          <option :value="72">3d</option>
          <option :value="168">7d</option>
        </select>
        <select v-model.number="noTopicWindow" @change="loadSessions" class="filter-select" style="width:70px">
          <option :value="1">1h</option>
          <option :value="2">2h</option>
          <option :value="6">6h</option>
        </select>
        <button class="btn btn-ghost btn-sm" @click="loadSessions" :disabled="loading">
          {{ loading ? '…' : '刷新' }}
        </button>
      </div>

      <div class="section-headers">
        <div class="section-header" @click="showTopic = !showTopic">
          <span class="toggle">{{ showTopic ? '▼' : '▶' }}</span>
          <svg class="icon-sm" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>
          </svg>
          有主题会话
          <span class="badge-count">{{ topicSessions.length }}</span>
        </div>
        <div class="section-header" @click="showNoTopic = !showNoTopic">
          <span class="toggle">{{ showNoTopic ? '▼' : '▶' }}</span>
          <svg class="icon-sm" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M9 5H7a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7a2 2 0 0 0-2-2h-2"/>
            <rect x="9" y="3" width="6" height="4" rx="1"/>
          </svg>
          无主题会话
          <span class="badge-count">{{ noTopicSessions.length }}</span>
        </div>
      </div>

      <div v-if="loading" class="empty">加载中…</div>
      <div v-else-if="visibleSessions.length === 0" class="empty">暂无会话数据</div>
      <template v-else>
        <div class="session-table">
          <div class="table-head">
            <div class="col-icon"></div>
            <div class="col-user">用户</div>
            <div class="col-key">Key</div>
            <div class="col-title">标题 / 会话</div>
            <div class="col-count">消息</div>
            <div class="col-model">模型</div>
            <div class="col-time">开始时间</div>
            <div class="col-action"></div>
          </div>

          <template v-if="showTopic">
            <div
              v-for="s in topicSessions"
              :key="s.task_id || Math.random()"
              class="table-row"
              @click="openDetail(s)"
            >
              <div class="col-icon">
                <svg class="icon-sm" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>
                </svg>
              </div>
              <div class="col-user">{{ displayUser(s) }}</div>
              <div class="col-key mono">{{ displayKey(s) }}</div>
              <div class="col-title mono">{{ displayTitle(s) }}</div>
              <div class="col-count">
                <span class="count-chip">{{ s.request_count }}</span>
                <span v-if="s.fail_count > 0" class="fail-chip">{{ s.fail_count }}失败</span>
              </div>
              <div class="col-model">
                <span v-if="s.latest_model" class="badge badge-blue">{{ s.latest_model }}</span>
                <span v-else class="empty-val">[空]</span>
              </div>
              <div class="col-time">{{ fmtDate(s.first_activity) }}</div>
              <div class="col-action">
                <button class="btn btn-ghost btn-sm">▶</button>
              </div>
            </div>
          </template>

          <template v-if="showNoTopic">
            <div
              v-for="s in noTopicSessions"
              :key="'nt-' + (s.no_topic_label || Math.random())"
              class="table-row no-topic"
              @click="openDetail(s)"
            >
              <div class="col-icon">
                <svg class="icon-sm" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M9 5H7a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7a2 2 0 0 0-2-2h-2"/>
                  <rect x="9" y="3" width="6" height="4" rx="1"/>
                </svg>
              </div>
              <div class="col-user">{{ displayUser(s) }}</div>
              <div class="col-key mono">{{ displayKey(s) }}</div>
              <div class="col-title">
                <span class="no-topic-label">{{ s.no_topic_label || '[无主题会话]' }}</span>
              </div>
              <div class="col-count">
                <span class="count-chip">{{ s.request_count }}</span>
                <span v-if="s.fail_count > 0" class="fail-chip">{{ s.fail_count }}失败</span>
              </div>
              <div class="col-model">
                <span v-if="s.latest_model" class="badge badge-blue">{{ s.latest_model }}</span>
                <span v-else class="empty-val">[空]</span>
              </div>
              <div class="col-time">{{ fmtDate(s.first_activity) }}</div>
              <div class="col-action">
                <button class="btn btn-ghost btn-sm">▶</button>
              </div>
            </div>
          </template>
        </div>
      </template>
    </template>

    <!-- ========== DETAIL VIEW ========== -->
    <template v-else-if="view === 'detail' && detailSession">
      <div class="detail-header">
        <button class="btn btn-ghost btn-sm back-btn" @click="backToList">
          ← 返回列表
        </button>
        <div class="detail-title-row">
          <svg v-if="!detailSession.no_topic" class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>
          </svg>
          <svg v-else class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M9 5H7a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7a2 2 0 0 0-2-2h-2"/>
            <rect x="9" y="3" width="6" height="4" rx="1"/>
          </svg>
          <h2>{{ detailSession.no_topic ? '无主题会话' : (contextData?.title || displayTitle(detailSession)) }}</h2>
        </div>
      </div>

      <div class="detail-meta card">
        <div class="meta-grid">
          <div class="meta-item">
            <span class="meta-label">Task</span>
            <code class="meta-value">{{ detailSession.task_id || '[空]' }}</code>
          </div>
          <div class="meta-item">
            <span class="meta-label">Session</span>
            <code class="meta-value">{{ detailSession.session_id || '[空]' }}</code>
          </div>
          <div class="meta-item">
            <span class="meta-label">用户</span>
            <span class="meta-value">{{ displayUser(detailSession) }}</span>
          </div>
          <div class="meta-item">
            <span class="meta-label">Key</span>
            <span class="meta-value mono">{{ displayKey(detailSession) }}</span>
          </div>
          <div class="meta-item">
            <span class="meta-label">应用</span>
            <span class="meta-value">{{ detailSession.application_code || '[空]' }}</span>
          </div>
          <div class="meta-item">
            <span class="meta-label">模型</span>
            <span v-if="detailSession.latest_model" class="badge badge-blue">{{ detailSession.latest_model }}</span>
            <span v-else class="meta-value">[空]</span>
          </div>
          <div class="meta-item">
            <span class="meta-label">消息数</span>
            <span class="meta-value">{{ detailSession.request_count }}</span>
          </div>
          <div class="meta-item">
            <span class="meta-label">成功</span>
            <span class="meta-value" style="color:#16a34a">{{ detailSession.ok_count }}</span>
            <span v-if="detailSession.fail_count > 0" class="meta-value" style="color:#dc2626;margin-left:8px">
              失败 {{ detailSession.fail_count }}
            </span>
          </div>
          <div class="meta-item">
            <span class="meta-label">开始</span>
            <span class="meta-value">{{ fmtDateFull(detailSession.first_activity) }}</span>
          </div>
          <div class="meta-item">
            <span class="meta-label">最后活跃</span>
            <span class="meta-value">{{ fmtDateFull(detailSession.last_activity) }}</span>
          </div>
        </div>
        <div class="detail-actions">
          <a
            v-if="!detailSession.no_topic && detailSession.task_id"
            :href="`/request-logs?gw_task=${encodeURIComponent(detailSession.task_id)}`"
            target="_blank"
            class="btn btn-ghost btn-sm"
          >
            原始日志 →
          </a>
        </div>
      </div>

      <template v-if="!detailSession.no_topic">
        <div class="sc-tabs">
          <button
            class="sc-tab"
            :class="{ active: activeTab === 'facts' }"
            @click="switchTab('facts')"
          >
            Memora 事实
            <span v-if="contextData" class="badge">{{ contextData.facts.length }}</span>
          </button>
          <button
            class="sc-tab"
            :class="{ active: activeTab === 'timeline' }"
            @click="switchTab('timeline')"
          >
            对话线索
            <span v-if="messagesData" class="badge">{{ messagesData.messages.length }}</span>
            <span v-else class="badge badge-gray">点击加载</span>
          </button>
        </div>

        <!-- Facts Tab -->
        <div v-if="activeTab === 'facts'" class="card">
          <div v-if="contextLoading" class="empty">加载中…</div>
          <div v-else-if="!contextData" class="empty">加载失败</div>
          <div v-else-if="contextData.facts.length === 0" class="empty">
            该会话暂无 Memora 记忆事实
          </div>
          <div v-else class="sc-facts">
            <div v-for="(f, i) in contextData.facts" :key="f.id" class="sc-fact">
              <div class="sc-fact-header">
                <span class="sc-fact-idx">#{{ i + 1 }}</span>
                <span v-if="f.score" class="badge badge-green">{{ fmtScore(f.score) }}</span>
                <span v-for="t in (f.tags || [])" :key="t" :class="'badge ' + tagClass(f.tags)">{{ t }}</span>
              </div>
              <div class="sc-fact-text">{{ f.memory }}</div>
            </div>
          </div>
        </div>

        <!-- Timeline Tab -->
        <div v-if="activeTab === 'timeline'" class="card">
          <div v-if="messagesLoading" class="empty">加载中…</div>
          <div v-else-if="!messagesData" class="empty">加载失败</div>
          <div v-else-if="messagesData.messages.length === 0" class="empty">
            该会话暂无请求记录
          </div>
          <div v-else>
            <div class="timeline-summary">
              共 {{ messagesData.messages.length }} 条请求，
              {{ messagesData.total_prompt_tokens }} prompt tokens，
              {{ messagesData.total_completion_tokens }} completion tokens，
              {{ fmtCost(messagesData.total_cost_usd) }}
            </div>
            <div class="sc-timeline">
              <div v-for="msg in messagesData.messages" :key="msg.request_id" class="sc-msg">
                <div class="sc-msg-left">
                  <div class="sc-msg-seq">#{{ msg.seq }}</div>
                  <div class="sc-msg-time">{{ fmtTime(msg.ts) }}</div>
                  <div class="sc-msg-dir" :class="msg.direction">
                    {{ msg.direction === 'user' ? '👤' : '🤖' }}
                  </div>
                </div>
                <div class="sc-msg-body">
                  <div class="sc-msg-prompt">{{ msg.prompt_preview || '[空]' }}</div>
                  <div v-if="msg.response_preview" class="sc-msg-response">
                    {{ msg.response_preview }}
                  </div>
                  <div class="sc-msg-meta">
                    <span class="badge badge-gray">{{ msg.client_model || '[空]' }}</span>
                    <span>{{ msg.prompt_tokens }} tok</span>
                    <span>{{ msg.latency_ms }}ms</span>
                    <span v-if="msg.cost_usd > 0">{{ fmtCost(msg.cost_usd) }}</span>
                    <span
                      class="status-badge"
                      :class="msg.status === 'success' ? 'success' : msg.status === 'failure' ? 'failure' : 'pending'"
                    >
                      {{ msg.status === 'success' ? '✓' : msg.status === 'failure' ? '✗' : '…' }}
                      {{ msg.status || 'unknown' }}
                    </span>
                    <span v-if="msg.error_kind" class="error-kind">{{ msg.error_kind }}</span>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </template>

      <template v-else>
        <div class="empty card" style="padding:40px">
          无主题会话不存储 Memora 记忆，也无法查看对话线索。
          <br />
          可通过
          <a :href="`/request-logs?hours=${hours}`" target="_blank">请求日志</a>
          页面按时间和 Key 前缀筛选查看。
        </div>
      </template>
    </template>
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 16px;
}
.header-title {
  display: flex;
  align-items: center;
  gap: 8px;
}
.header-title h2 {
  margin: 0;
  font-size: 18px;
}
.icon {
  width: 20px;
  height: 20px;
  color: #3b82f6;
  flex-shrink: 0;
}
.icon-sm {
  width: 14px;
  height: 14px;
  flex-shrink: 0;
}
.sub {
  font-size: 13px;
  color: #888;
}

.filter-bar {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
  align-items: center;
}
.filter-input {
  padding: 6px 10px;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  font-size: 13px;
  width: 200px;
}
.filter-select {
  padding: 6px 8px;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  font-size: 13px;
}

.section-headers {
  display: flex;
  gap: 16px;
  margin-bottom: 4px;
}
.section-header {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  font-weight: 600;
  color: #374151;
  cursor: pointer;
  user-select: none;
}
.toggle {
  font-size: 10px;
  color: #9ca3af;
}
.badge-count {
  background: #e5e7eb;
  color: #374151;
  border-radius: 10px;
  padding: 1px 6px;
  font-size: 11px;
}

.session-table {
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  overflow: hidden;
}
.table-head {
  display: flex;
  align-items: center;
  background: #f9fafb;
  border-bottom: 1px solid #e5e7eb;
  padding: 8px 12px;
  font-size: 12px;
  font-weight: 600;
  color: #6b7280;
  gap: 8px;
}
.table-row {
  display: flex;
  align-items: center;
  padding: 10px 12px;
  border-bottom: 1px solid #f3f4f6;
  cursor: pointer;
  font-size: 13px;
  gap: 8px;
}
.table-row:last-child { border-bottom: none; }
.table-row:hover { background: #f9fafb; }
.table-row.no-topic { background: #fffbeb; }
.table-row.no-topic:hover { background: #fef3c7; }

.col-icon { width: 24px; flex-shrink: 0; display: flex; align-items: center; justify-content: center; }
.col-user { width: 140px; flex-shrink: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.col-key { width: 100px; flex-shrink: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
.col-title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
.col-count { width: 80px; flex-shrink: 0; display: flex; gap: 4px; align-items: center; }
.col-model { width: 100px; flex-shrink: 0; }
.col-time { width: 130px; flex-shrink: 0; font-size: 12px; color: #888; }
.col-action { width: 32px; flex-shrink: 0; }

.mono { font-family: monospace; }
.count-chip {
  background: #dbeafe;
  color: #1d4ed8;
  border-radius: 10px;
  padding: 1px 6px;
  font-size: 11px;
}
.fail-chip {
  background: #fee2e2;
  color: #dc2626;
  border-radius: 10px;
  padding: 1px 6px;
  font-size: 11px;
}
.empty-val { color: #9ca3af; font-style: italic; }
.no-topic-label { color: #92400e; font-size: 12px; }

/* Detail view */
.detail-header {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 16px;
}
.back-btn {
  align-self: flex-start;
  margin-bottom: 4px;
}
.detail-title-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.detail-title-row h2 {
  margin: 0;
  font-size: 18px;
}

.detail-meta {
  padding: 16px;
  margin-bottom: 16px;
}
.meta-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 12px;
  margin-bottom: 12px;
}
.meta-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.meta-label {
  font-size: 11px;
  color: #9ca3af;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.meta-value {
  font-size: 13px;
  color: #374151;
}
.detail-actions {
  display: flex;
  gap: 8px;
}

.sc-tabs {
  display: flex;
  gap: 0;
  border-bottom: 2px solid #e5e7eb;
  margin-bottom: 16px;
}
.sc-tab {
  padding: 8px 16px;
  background: none;
  border: none;
  cursor: pointer;
  font-size: 13px;
  color: #666;
  border-bottom: 2px solid transparent;
  margin-bottom: -2px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.sc-tab.active {
  color: #1d4ed8;
  border-bottom-color: #1d4ed8;
  font-weight: 600;
}
.sc-tab .badge { margin-left: 4px; }
.sc-tab .badge-gray {
  background: #e5e7eb;
  color: #6b7280;
  border-radius: 10px;
  padding: 1px 6px;
  font-size: 11px;
  font-weight: 400;
}

.sc-facts { display: flex; flex-direction: column; gap: 10px; }
.sc-fact {
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  padding: 10px 12px;
}
.sc-fact-header {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 6px;
}
.sc-fact-idx { font-weight: 600; color: #6b7280; font-size: 12px; }
.sc-fact-text {
  font-size: 13px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
}

.timeline-summary {
  font-size: 12px;
  color: #888;
  margin-bottom: 12px;
  padding: 0 4px;
}

.sc-timeline { display: flex; flex-direction: column; gap: 0; }
.sc-msg {
  display: flex;
  gap: 12px;
  padding: 12px 4px;
  border-bottom: 1px solid #f3f4f6;
}
.sc-msg:last-child { border-bottom: none; }
.sc-msg-left {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
  width: 48px;
  flex-shrink: 0;
}
.sc-msg-seq { font-size: 11px; font-weight: 600; color: #9ca3af; }
.sc-msg-time { font-size: 11px; color: #d1d5db; }
.sc-msg-dir {
  width: 24px;
  height: 24px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  margin-top: 4px;
}
.sc-msg-dir.user { background: #dbeafe; }
.sc-msg-dir.assistant { background: #d1fae5; }

.sc-msg-body { flex: 1; min-width: 0; }
.sc-msg-prompt {
  font-size: 13px;
  color: #1f2937;
  white-space: pre-wrap;
  word-break: break-word;
  margin-bottom: 4px;
  line-height: 1.5;
}
.sc-msg-response {
  font-size: 12px;
  color: #6b7280;
  white-space: pre-wrap;
  word-break: break-word;
  margin-bottom: 6px;
  padding: 6px 8px;
  background: #f9fafb;
  border-radius: 4px;
  border-left: 3px solid #d1d5db;
}
.sc-msg-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  font-size: 12px;
  color: #9ca3af;
  align-items: center;
}
.status-badge {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  border-radius: 10px;
  padding: 1px 8px;
  font-size: 11px;
  font-weight: 500;
}
.status-badge.success { background: #d1fae5; color: #065f46; }
.status-badge.failure { background: #fee2e2; color: #991b1b; }
.status-badge.pending { background: #fef3c7; color: #92400e; }
.error-kind { color: #dc2626; font-size: 11px; }

.empty {
  padding: 40px;
  text-align: center;
  color: #9ca3af;
  font-size: 14px;
}
</style>
