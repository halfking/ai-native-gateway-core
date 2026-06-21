<script setup lang="ts">
import { computed, inject, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  getMemoraContext,
  getSessionMessages,
  getNoTopicSessionMessages,
  extractSessionToMemora,
  extractNoTopicSessionToMemora,
  getSessionExtractionStatus,
  getNoTopicSessionExtractionStatus,
  summarizeSessionTitle,
  summarizeNoTopicSessionTitle,
  type MemoraContextResponse,
  type ReadableBlock,
  type SessionExtractionStatusResponse,
  type SessionMessagesResponse,
  type RequestMessage,
} from '../../api'
import {
  buildSessionQueryParams,
  fmtCost,
  fmtScore,
  fmtTime,
  listBackQueryFromRoute,
  noTopicParamsFromScope,
  parseSessionScopeFromRoute,
  sessionScopeToParams,
  tagClass,
  type useSessionFilters,
} from '../../composables/useSessionContext'
import RequestLogDrawer from '../../components/RequestLogDrawer.vue'

const route = useRoute()
const router = useRouter()
const filters = inject<ReturnType<typeof useSessionFilters>>('sessionContextFilters')!

function parseVirtualNoTopicHourStart(taskId: string): string | undefined {
  if (!taskId.startsWith('notopic:')) return undefined
  const parts = taskId.split(':')
  if (parts.length < 3) return undefined
  return parts.slice(2).join(':') || undefined
}

const taskId = computed(() => decodeURIComponent(String(route.params.taskId || '')))
const isNoTopic = computed(() => taskId.value === '_no-topic' || taskId.value.startsWith('notopic:'))

const sessionScope = computed(() => {
  const scope = parseSessionScopeFromRoute(route, filters.hours.value)
  if (isNoTopic.value && !scope.hour_start) {
    const fromTask = parseVirtualNoTopicHourStart(taskId.value)
    if (fromTask) scope.hour_start = fromTask
  }
  return scope
})
const noTopicParams = computed(() => noTopicParamsFromScope(sessionScope.value))
const listExpectedCount = computed(() => sessionScope.value.rc)

const contextData = ref<MemoraContextResponse | null>(null)
const messagesData = ref<SessionMessagesResponse | null>(null)
const contextLoading = ref(false)
const contextError = ref('')
const messagesLoading = ref(false)
const messagesError = ref('')

const extractionStatus = ref<SessionExtractionStatusResponse | null>(null)
const extracting = ref(false)
const extractResult = ref('')
const extractError = ref('')

const summarizingTitle = ref(false)
const titleResult = ref('')
const titleError = ref('')
const localTitle = ref('')

const activeRequestId = ref<string | null>(null)

const readableBlocks = computed<ReadableBlock[]>(() => {
  if (contextData.value?.readable_blocks?.length) {
    return contextData.value.readable_blocks
  }
  return (contextData.value?.facts || []).map(f => ({
    id: f.id,
    text: f.memory,
    kind: f.kind || 'text',
    source: f.source || 'task',
    tags: f.tags,
    score: f.score,
  }))
})

const hasReadableContent = computed(() => readableBlocks.value.length > 0)
const hasMessages = computed(() => (messagesData.value?.messages.length ?? 0) > 0)

function messageUserLine(msg: RequestMessage) {
  return (msg.user_turn || msg.prompt_preview || '—').trim()
}

function messageAssistantLine(msg: RequestMessage) {
  return (msg.assistant_text || msg.response_preview || '').trim()
}

function openRequestDrawer(requestId: string) {
  activeRequestId.value = requestId
}

function closeRequestDrawer() {
  activeRequestId.value = null
}

const requestLogsHref = computed(() => {
  const qs = new URLSearchParams()
  if (isNoTopic.value) {
    if (noTopicParams.value.prefix) qs.set('key_prefix', noTopicParams.value.prefix)
    if (sessionScope.value.hours) qs.set('hours', String(sessionScope.value.hours))
  } else {
    qs.set('gw_task', taskId.value)
    if (sessionScope.value.session_id) qs.set('gw_session_id', sessionScope.value.session_id)
    if (sessionScope.value.hours) qs.set('hours', String(sessionScope.value.hours))
  }
  return `/request-logs?${qs.toString()}`
})

const pageTitle = computed(() => {
  if (isNoTopic.value) {
    if (localTitle.value) return localTitle.value
    if (messagesData.value?.title) return messagesData.value.title
    return sessionScope.value.label || '无主题会话'
  }
  if (localTitle.value) return localTitle.value
  return contextData.value?.title || taskId.value || '会话详情'
})

function sourceLabel(source: string) {
  return source === 'gw-session' ? '会话总结' : '任务提炼'
}

async function loadContext() {
  if (isNoTopic.value || !taskId.value || taskId.value.startsWith('notopic:')) return
  contextLoading.value = true
  contextError.value = ''
  try {
    contextData.value = await getMemoraContext(taskId.value, sessionScopeToParams(sessionScope.value))
    if (contextData.value?.title && contextData.value.title !== '[无标题]') {
      localTitle.value = contextData.value.title
    }
  } catch (e: unknown) {
    contextData.value = null
    contextError.value = e instanceof Error ? e.message : '加载 Memora 可读内容失败'
  } finally {
    contextLoading.value = false
  }
}

async function loadMessages() {
  messagesLoading.value = true
  messagesError.value = ''
  try {
    if (isNoTopic.value) {
      if (!noTopicParams.value.prefix) {
        messagesData.value = null
        messagesError.value = '缺少 Key 前缀，请从列表重新进入'
        return
      }
      messagesData.value = await getNoTopicSessionMessages(noTopicParams.value)
      if (messagesData.value.title) localTitle.value = messagesData.value.title
    } else if (taskId.value) {
      messagesData.value = await getSessionMessages(taskId.value, sessionScopeToParams(sessionScope.value))
    }
  } catch (e: unknown) {
    messagesData.value = null
    messagesError.value = e instanceof Error ? e.message : '加载请求记录失败'
  } finally {
    messagesLoading.value = false
  }
}

async function loadExtractionStatus() {
  extractionStatus.value = null
  try {
    if (isNoTopic.value) {
      if (!noTopicParams.value.prefix) return
      extractionStatus.value = await getNoTopicSessionExtractionStatus(noTopicParams.value)
    } else if (taskId.value) {
      extractionStatus.value = await getSessionExtractionStatus(taskId.value)
    }
  } catch {
    extractionStatus.value = null
  }
}

async function doSummarizeTitle() {
  if (summarizingTitle.value) return
  summarizingTitle.value = true
  titleResult.value = ''
  titleError.value = ''
  try {
    if (isNoTopic.value) {
      if (!noTopicParams.value.prefix) throw new Error('缺少 Key 前缀')
      const resp = await summarizeNoTopicSessionTitle(noTopicParams.value)
      localTitle.value = resp.title
    } else {
      if (!taskId.value) return
      const resp = await summarizeSessionTitle(taskId.value, sessionScopeToParams(sessionScope.value))
      localTitle.value = resp.title
      if (contextData.value) contextData.value.title = resp.title
    }
    titleResult.value = '会话标题已更新'
  } catch (e: unknown) {
    titleError.value = e instanceof Error ? e.message : '标题生成失败'
  } finally {
    summarizingTitle.value = false
  }
}

async function doExtractToMemora() {
  if (extracting.value) return
  extracting.value = true
  extractResult.value = ''
  extractError.value = ''
  try {
    const resp = isNoTopic.value
      ? await extractNoTopicSessionToMemora(noTopicParams.value)
      : await extractSessionToMemora(taskId.value, sessionScopeToParams(sessionScope.value))
    extractResult.value = `已写入 ${resp.written} 条，跳过噪音 ${resp.skipped_noise}、重复 ${resp.skipped_duplicate}`
    await loadExtractionStatus()
    if (!isNoTopic.value) await loadContext()
  } catch (e: unknown) {
    extractError.value = e instanceof Error ? e.message : '提炼失败'
  } finally {
    extracting.value = false
  }
}

function reloadDetail() {
  contextData.value = null
  messagesData.value = null
  extractionStatus.value = null
  extractResult.value = ''
  extractError.value = ''
  titleResult.value = ''
  titleError.value = ''
  if (!isNoTopic.value) localTitle.value = ''
  loadMessages()
  loadExtractionStatus()
  if (!isNoTopic.value) loadContext()
}

onMounted(() => {
  if (route.query.tab === 'timeline') {
    router.replace({ query: { ...route.query, tab: undefined } })
  }
  reloadDetail()
})

watch(
  () => [
    taskId.value,
    route.query.hours,
    route.query.session_id,
    route.query.rc,
    route.query.prefix,
    route.query.hour_start,
    route.query.label,
  ] as const,
  reloadDetail,
)
</script>

<template>
  <div class="tab-content">
    <div class="card compact-card detail-head">
      <h3 class="detail-title">{{ pageTitle }}</h3>

      <template v-if="isNoTopic">
        <div class="meta-grid">
          <div><span class="meta-lbl">Key</span><code>{{ noTopicParams.prefix || '—' }}</code></div>
          <div v-if="sessionScope.hour_start"><span class="meta-lbl">时段</span>{{ fmtTime(sessionScope.hour_start) }}</div>
          <div v-if="messagesData"><span class="meta-lbl">请求数</span>{{ messagesData.request_count ?? messagesData.messages.length }}</div>
          <div><span class="meta-lbl">时间窗</span>{{ sessionScope.hours }}h</div>
          <div v-if="messagesData?.messages[0]?.client_model"><span class="meta-lbl">模型</span>{{ messagesData.messages[messagesData.messages.length - 1]?.client_model }}</div>
        </div>
      </template>

      <template v-else>
        <div class="meta-grid">
          <div><span class="meta-lbl">Task</span><code>{{ taskId }}</code></div>
          <div v-if="sessionScope.session_id"><span class="meta-lbl">Session</span><code>{{ sessionScope.session_id }}</code></div>
          <div v-if="contextData"><span class="meta-lbl">用户</span>{{ contextData.user_id || '—' }}</div>
          <div v-if="contextData"><span class="meta-lbl">请求数</span>{{ contextData.request_count }}</div>
          <div v-if="contextData"><span class="meta-lbl">时间窗</span>{{ contextData.hours ?? sessionScope.hours }}h</div>
          <div v-if="contextData?.latest_model"><span class="meta-lbl">模型</span>{{ contextData.latest_model }}</div>
        </div>
        <div v-if="contextData" class="stats-line text-muted">
          Memora 可读块 {{ readableBlocks.length }} 条
          · 已写入 {{ contextData.facts_written ?? extractionStatus?.written ?? 0 }} 条
          · 线索 {{ contextData.request_count }} 条（{{ sessionScope.hours }}h 窗）
        </div>
      </template>

      <div class="detail-actions">
        <button
          class="btn btn-primary btn-sm"
          :disabled="summarizingTitle || (isNoTopic && !noTopicParams.prefix)"
          @click="doSummarizeTitle"
        >
          <span v-if="summarizingTitle" class="btn-spinner" aria-hidden="true" />
          {{ summarizingTitle ? '生成中…' : '总结会话标题' }}
        </button>
        <button
          class="btn btn-primary btn-sm"
          :disabled="extracting || (isNoTopic && !noTopicParams.prefix)"
          @click="doExtractToMemora"
        >{{ extracting ? '提炼中…' : '提炼入 Memora' }}</button>
        <span
          v-if="extractionStatus?.extracted"
          class="badge badge-green"
          :title="extractionStatus.extracted_at"
        >已写入 {{ extractionStatus.written ?? 0 }} 条</span>
        <a :href="requestLogsHref" target="_blank" class="btn btn-ghost btn-sm">原始日志 →</a>
      </div>
      <p v-if="titleResult" class="extract-ok">{{ titleResult }}</p>
      <p v-if="titleError" class="extract-err">{{ titleError }}</p>
      <p v-if="extractResult" class="extract-ok">{{ extractResult }}</p>
      <p v-if="extractError" class="extract-err">{{ extractError }}</p>
    </div>

    <div class="card compact-card">
      <div class="section-head">
        <h4 class="section-title">原始请求记录</h4>
        <span v-if="messagesData" class="badge badge-gray">{{ messagesData.messages.length }}</span>
        <span
          v-if="listExpectedCount != null && messagesData"
          class="badge"
          :class="messagesData.messages.length === listExpectedCount ? 'badge-green' : 'badge-yellow'"
        >列表 {{ listExpectedCount }} 条</span>
      </div>

      <div v-if="messagesLoading" class="state-box">加载请求记录…</div>
      <div v-else-if="messagesError" class="state-box">
        <p>{{ messagesError }}</p>
        <button class="btn btn-ghost btn-sm" @click="loadMessages">重试</button>
      </div>
      <div v-else-if="!hasMessages" class="state-box">
        <p>该会话暂无请求记录</p>
        <p v-if="isNoTopic && !noTopicParams.prefix" class="text-muted">请从无主题会话列表点击进入，以携带 Key 前缀与时间桶参数。</p>
      </div>
      <div v-else>
        <div class="timeline-summary text-muted">
          <template v-if="listExpectedCount != null">
            与列表一致 {{ messagesData!.messages.length }}/{{ listExpectedCount }} 条（{{ messagesData!.hours ?? sessionScope.hours }}h 窗）；
          </template>
          <template v-else>
            共 {{ messagesData!.messages.length }} 条请求（{{ messagesData!.hours ?? sessionScope.hours }}h 窗）；
          </template>
          {{ messagesData!.total_prompt_tokens }} prompt tokens，
          {{ messagesData!.total_completion_tokens }} completion tokens，
          {{ fmtCost(messagesData!.total_cost_usd) }}
        </div>
        <div class="timeline">
          <div v-for="msg in messagesData!.messages" :key="msg.request_id" class="timeline-row">
            <div class="timeline-side">
              <span class="seq">#{{ msg.seq }}</span>
              <span class="text-muted">{{ fmtTime(msg.ts) }}</span>
              <span>{{ msg.direction === 'user' ? '👤' : '🤖' }}</span>
            </div>
            <div class="timeline-main">
              <div class="turn-user">{{ messageUserLine(msg) }}</div>
              <div v-if="messageAssistantLine(msg) || msg.tool_summary" class="turn-assistant">
                <div v-if="messageAssistantLine(msg)">{{ messageAssistantLine(msg) }}</div>
                <div v-if="msg.tool_summary" class="tool-summary text-muted">{{ msg.tool_summary }}</div>
              </div>
              <div class="meta-line text-muted">
                <button
                  type="button"
                  class="req-link-btn"
                  title="查看原始请求详情"
                  @click.stop="openRequestDrawer(msg.request_id)"
                >原始请求</button>
                <span class="badge badge-gray">{{ msg.client_model || '—' }}</span>
                <span>{{ msg.prompt_tokens }} tok</span>
                <span>{{ msg.latency_ms }}ms</span>
                <span v-if="msg.cost_usd > 0">{{ fmtCost(msg.cost_usd) }}</span>
                <span
                  class="status-pill"
                  :class="msg.status === 'success' ? 'ok' : msg.status === 'failure' ? 'fail' : 'pending'"
                >{{ msg.status || 'unknown' }}</span>
                <span v-if="msg.error_kind" class="fail-text">{{ msg.error_kind }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <div v-if="!isNoTopic" class="card compact-card">
      <div class="section-head">
        <h4 class="section-title">可读内容</h4>
        <span v-if="readableBlocks.length" class="badge badge-gray">{{ readableBlocks.length }}</span>
      </div>

      <div v-if="contextLoading" class="state-box">加载 Memora 可读内容…</div>
      <div v-else-if="contextError" class="state-box">
        <p>{{ contextError }}</p>
        <button class="btn btn-ghost btn-sm" @click="loadContext">重试</button>
      </div>
      <div v-else-if="!hasReadableContent" class="state-box empty-memora">
        <template v-if="contextData?.facts_search_error">
          <p class="empty-title extract-err">Memora 检索失败</p>
          <p class="extract-err">{{ contextData.facts_search_error }}</p>
        </template>
        <template v-else>
          <p class="empty-title">Memora中没有</p>
          <p v-if="(contextData?.facts_written ?? 0) > 0" class="text-muted">
            已写入 {{ contextData?.facts_written }} 条（检索可能仍在索引）
          </p>
          <p class="text-muted">点击上方「提炼入 Memora」将对话中的有用信息写入记忆。</p>
        </template>
      </div>
      <div v-else class="blocks-list">
        <div v-for="(block, i) in readableBlocks" :key="block.id || i" class="block-item">
          <div class="block-head">
            <span class="block-idx">#{{ i + 1 }}</span>
            <span class="badge badge-blue">{{ sourceLabel(block.source) }}</span>
            <span v-if="block.kind === 'json'" class="badge badge-purple">JSON</span>
            <span v-if="block.score" class="badge badge-green">{{ fmtScore(block.score) }}</span>
            <span v-for="t in (block.tags || [])" :key="t" :class="'badge ' + tagClass(block.tags ?? null)">{{ t }}</span>
          </div>
          <pre v-if="block.kind === 'json'" class="json-block">{{ block.text }}</pre>
          <div v-else class="block-body">{{ block.text }}</div>
        </div>
      </div>
    </div>

    <RequestLogDrawer :request-id="activeRequestId" @close="closeRequestDrawer" />
  </div>
</template>

<style scoped>
.tab-content { display: flex; flex-direction: column; gap: 8px; }
.compact-card { padding: 8px 10px; }
.detail-title {
  margin: 0 0 8px;
  font-size: 14px;
  font-weight: 600;
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.45;
}
.meta-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: 8px;
  font-size: 12px;
  margin-bottom: 8px;
}
.meta-lbl { display: block; font-size: 10px; color: var(--muted); text-transform: uppercase; }
.stats-line { margin-bottom: 8px; font-size: 11px; }
.detail-actions { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; margin-bottom: 4px; }
.btn-spinner {
  display: inline-block;
  width: 12px;
  height: 12px;
  margin-right: 4px;
  border: 2px solid rgba(255, 255, 255, 0.35);
  border-top-color: #fff;
  border-radius: 50%;
  animation: spin 0.7s linear infinite;
  vertical-align: -2px;
}
@keyframes spin { to { transform: rotate(360deg); } }
.badge-yellow { background: rgba(210, 153, 34, 0.2); color: var(--warning); }
.badge-purple { background: rgba(163, 113, 247, 0.2); color: #a371f7; }
.extract-ok { margin: 6px 0 0; font-size: 11px; color: var(--success); }
.extract-err { margin: 6px 0 0; font-size: 11px; color: var(--danger); }
.section-head { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.section-title { margin: 0; font-size: 13px; font-weight: 600; }
.blocks-list { display: flex; flex-direction: column; gap: 8px; }
.block-item { border: 1px solid var(--border); border-radius: 6px; padding: 8px; }
.block-head { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; margin-bottom: 4px; }
.block-idx { font-size: 11px; color: var(--muted); font-weight: 600; }
.block-body { font-size: 12px; line-height: 1.55; white-space: pre-wrap; word-break: break-word; }
.json-block {
  margin: 0;
  padding: 8px;
  background: var(--bg-subtle);
  border-radius: 4px;
  font-size: 11px;
  line-height: 1.45;
  overflow-x: auto;
  white-space: pre-wrap;
  word-break: break-word;
}
.empty-memora .empty-title { font-size: 14px; font-weight: 600; color: var(--muted); margin-bottom: 6px; }
.timeline-summary { margin-bottom: 8px; font-size: 11px; }
.timeline { display: flex; flex-direction: column; }
.timeline-row {
  display: flex;
  gap: 10px;
  padding: 8px 0;
  border-bottom: 1px solid var(--border);
}
.timeline-side {
  width: 52px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 10px;
  align-items: center;
}
.seq { font-weight: 600; color: var(--accent-h); }
.timeline-main { flex: 1; min-width: 0; }
.turn-user { font-size: 12px; line-height: 1.5; margin-bottom: 6px; font-weight: 500; white-space: pre-wrap; word-break: break-word; }
.turn-assistant {
  font-size: 11px;
  line-height: 1.45;
  color: var(--muted);
  margin-bottom: 6px;
  padding: 6px 8px;
  background: var(--bg-subtle);
  border-radius: 4px;
  border-left: 2px solid var(--border);
}
.tool-summary { margin-top: 4px; font-size: 10px; white-space: pre-wrap; word-break: break-word; }
.req-link-btn {
  border: none;
  background: none;
  padding: 0;
  color: var(--accent-h);
  font-size: inherit;
  font-weight: 600;
  cursor: pointer;
}
.req-link-btn:hover { text-decoration: underline; }
.meta-line { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; font-size: 10px; }
.status-pill { padding: 0 5px; border-radius: 8px; font-size: 10px; }
.status-pill.ok { background: rgba(46, 160, 67, 0.15); color: var(--success); }
.status-pill.fail { background: rgba(248, 81, 73, 0.15); color: var(--danger); }
.status-pill.pending { background: rgba(210, 153, 34, 0.15); color: var(--warning); }
.fail-text { color: var(--danger); }
.state-box { padding: 24px; text-align: center; font-size: 12px; color: var(--muted); }
.text-muted { color: var(--muted); font-size: 11px; }
</style>
