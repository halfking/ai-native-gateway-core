<script setup lang="ts">
import { computed, inject, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  getMemoraContext,
  getSessionMessages,
  extractSessionToMemora,
  getSessionExtractionStatus,
  type MemoraContextResponse,
  type SessionMessagesResponse,
  type SessionExtractionStatusResponse,
} from '../../api'
import {
  displayKey,
  displayTitle,
  displayUser,
  fmtCost,
  fmtDateFull,
  fmtScore,
  fmtTime,
  tagClass,
  type useSessionFilters,
} from '../../composables/useSessionContext'

const route = useRoute()
const router = useRouter()
const filters = inject<ReturnType<typeof useSessionFilters>>('sessionContextFilters')!

const taskId = computed(() => decodeURIComponent(String(route.params.taskId || '')))
const isNoTopic = computed(() => taskId.value === '_no-topic')

const activeTab = ref<'facts' | 'timeline'>(
  route.query.tab === 'timeline' ? 'timeline' : 'facts',
)

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

const pageTitle = computed(() => {
  if (isNoTopic.value) return '无主题会话'
  return contextData.value?.title || taskId.value || '会话详情'
})

async function loadContext() {
  if (isNoTopic.value || !taskId.value) return
  contextLoading.value = true
  contextError.value = ''
  try {
    contextData.value = await getMemoraContext(taskId.value)
  } catch (e: unknown) {
    contextData.value = null
    contextError.value = e instanceof Error ? e.message : '加载 Memora 事实失败'
  } finally {
    contextLoading.value = false
  }
}

async function loadTimeline() {
  if (isNoTopic.value || !taskId.value) return
  messagesLoading.value = true
  messagesError.value = ''
  try {
    messagesData.value = await getSessionMessages(taskId.value)
  } catch (e: unknown) {
    messagesData.value = null
    messagesError.value = e instanceof Error ? e.message : '加载对话线索失败'
  } finally {
    messagesLoading.value = false
  }
}

async function loadExtractionStatus() {
  if (isNoTopic.value || !taskId.value) return
  try {
    extractionStatus.value = await getSessionExtractionStatus(taskId.value)
  } catch {
    extractionStatus.value = null
  }
}

async function doExtractToMemora() {
  if (isNoTopic.value || !taskId.value || extracting.value) return
  extracting.value = true
  extractResult.value = ''
  extractError.value = ''
  try {
    const resp = await extractSessionToMemora(taskId.value)
    extractResult.value = `已写入 ${resp.written} 条，跳过噪音 ${resp.skipped_noise}、重复 ${resp.skipped_duplicate}`
    await loadExtractionStatus()
    await loadContext()
  } catch (e: unknown) {
    extractError.value = e instanceof Error ? e.message : '提炼失败'
  } finally {
    extracting.value = false
  }
}

function switchTab(tab: 'facts' | 'timeline') {
  activeTab.value = tab
  router.replace({ query: { ...route.query, tab: tab === 'facts' ? undefined : tab } })
  if (tab === 'timeline' && !messagesData.value && !messagesLoading.value) loadTimeline()
}

onMounted(() => {
  if (!isNoTopic.value) {
    loadContext()
    loadExtractionStatus()
    if (activeTab.value === 'timeline') loadTimeline()
  }
})

watch(taskId, () => {
  contextData.value = null
  messagesData.value = null
  extractionStatus.value = null
  extractResult.value = ''
  extractError.value = ''
  activeTab.value = 'facts'
  if (!isNoTopic.value) {
    loadContext()
    loadExtractionStatus()
  }
})
</script>

<template>
  <div class="tab-content">
    <div class="card compact-card detail-head">
      <h3 class="detail-title">{{ pageTitle }}</h3>
      <template v-if="isNoTopic">
        <p class="text-muted">标签：{{ route.query.label || '—' }}</p>
        <p class="text-muted">Key 前缀：{{ route.query.prefix || '—' }}</p>
      </template>
      <template v-else>
        <div class="meta-grid">
          <div><span class="meta-lbl">Task</span><code>{{ taskId }}</code></div>
          <div v-if="contextData"><span class="meta-lbl">用户</span>{{ contextData.user_id || '—' }}</div>
          <div v-if="contextData"><span class="meta-lbl">请求数</span>{{ contextData.request_count }}</div>
          <div v-if="contextData?.latest_model"><span class="meta-lbl">模型</span>{{ contextData.latest_model }}</div>
        </div>
        <div class="detail-actions">
          <button
            class="btn btn-primary btn-sm"
            :disabled="extracting"
            @click="doExtractToMemora"
          >{{ extracting ? '提炼中…' : '提炼入 Memora' }}</button>
          <span
            v-if="extractionStatus?.extracted"
            class="badge badge-green"
            :title="extractionStatus.extracted_at"
          >已提炼 {{ extractionStatus.written ?? 0 }} 条</span>
          <a
            :href="`/request-logs?gw_task=${encodeURIComponent(taskId)}`"
            target="_blank"
            class="btn btn-ghost btn-sm"
          >原始日志 →</a>
        </div>
        <p v-if="extractResult" class="extract-ok">{{ extractResult }}</p>
        <p v-if="extractError" class="extract-err">{{ extractError }}</p>
      </template>
    </div>

    <template v-if="isNoTopic">
      <div class="card compact-card state-box">
        <p>无主题会话不存储 Memora 记忆，也无法查看对话线索。</p>
        <p class="text-muted">
          可通过
          <a :href="`/request-logs?hours=${route.query.hours || filters.hours.value}`" target="_blank">请求日志</a>
          按时间和 Key 前缀筛选查看。
        </p>
      </div>
    </template>

    <template v-else>
      <div class="seg-tabs detail-tabs">
        <button class="seg-tab" :class="{ active: activeTab === 'facts' }" @click="switchTab('facts')">
          Memora 事实
          <span v-if="contextData" class="badge badge-gray">{{ contextData.facts.length }}</span>
        </button>
        <button class="seg-tab" :class="{ active: activeTab === 'timeline' }" @click="switchTab('timeline')">
          对话线索
          <span v-if="messagesData" class="badge badge-gray">{{ messagesData.messages.length }}</span>
        </button>
      </div>

      <div v-if="activeTab === 'facts'" class="card compact-card">
        <div v-if="contextLoading" class="state-box">加载 Memora 事实…</div>
        <div v-else-if="contextError" class="state-box">
          <p>{{ contextError }}</p>
          <button class="btn btn-ghost btn-sm" @click="loadContext">重试</button>
        </div>
        <div v-else-if="!contextData || contextData.facts.length === 0" class="state-box">
          该会话暂无 Memora 记忆事实
        </div>
        <div v-else class="facts-list">
          <div v-for="(f, i) in contextData.facts" :key="f.id" class="fact-item">
            <div class="fact-head">
              <span class="fact-idx">#{{ i + 1 }}</span>
              <span v-if="f.score" class="badge badge-green">{{ fmtScore(f.score) }}</span>
              <span v-for="t in (f.tags || [])" :key="t" :class="'badge ' + tagClass(f.tags)">{{ t }}</span>
            </div>
            <div class="fact-body">{{ f.memory }}</div>
          </div>
        </div>
      </div>

      <div v-if="activeTab === 'timeline'" class="card compact-card">
        <div v-if="messagesLoading" class="state-box">加载对话线索…</div>
        <div v-else-if="messagesError" class="state-box">
          <p>{{ messagesError }}</p>
          <button class="btn btn-ghost btn-sm" @click="loadTimeline">重试</button>
        </div>
        <div v-else-if="!messagesData || messagesData.messages.length === 0" class="state-box">
          该会话暂无请求记录
        </div>
        <div v-else>
          <div class="timeline-summary text-muted">
            共 {{ messagesData.messages.length }} 条请求，
            {{ messagesData.total_prompt_tokens }} prompt tokens，
            {{ messagesData.total_completion_tokens }} completion tokens，
            {{ fmtCost(messagesData.total_cost_usd) }}
          </div>
          <div class="timeline">
            <div v-for="msg in messagesData.messages" :key="msg.request_id" class="timeline-row">
              <div class="timeline-side">
                <span class="seq">#{{ msg.seq }}</span>
                <span class="text-muted">{{ fmtTime(msg.ts) }}</span>
                <span>{{ msg.direction === 'user' ? '👤' : '🤖' }}</span>
              </div>
              <div class="timeline-main">
                <div class="prompt">{{ msg.prompt_preview || '—' }}</div>
                <div v-if="msg.response_preview" class="response">{{ msg.response_preview }}</div>
                <div class="meta-line text-muted">
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
    </template>
  </div>
</template>

<style scoped>
.tab-content { display: flex; flex-direction: column; gap: 8px; }
.compact-card { padding: 8px 10px; }
.detail-title { margin: 0 0 8px; font-size: 14px; }
.meta-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: 8px;
  font-size: 12px;
  margin-bottom: 8px;
}
.meta-lbl { display: block; font-size: 10px; color: var(--muted); text-transform: uppercase; }
.detail-actions { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
.extract-ok { margin: 6px 0 0; font-size: 11px; color: var(--success); }
.extract-err { margin: 6px 0 0; font-size: 11px; color: var(--danger); }
.detail-tabs { align-self: flex-start; }
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
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.seg-tab.active {
  background: var(--card);
  color: var(--text);
  font-weight: 600;
}
.facts-list { display: flex; flex-direction: column; gap: 8px; }
.fact-item { border: 1px solid var(--border); border-radius: 6px; padding: 8px; }
.fact-head { display: flex; gap: 6px; align-items: center; margin-bottom: 4px; }
.fact-idx { font-size: 11px; color: var(--muted); font-weight: 600; }
.fact-body { font-size: 12px; line-height: 1.55; white-space: pre-wrap; word-break: break-word; }
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
  align-items: center;
  gap: 2px;
  font-size: 10px;
}
.seq { font-weight: 600; color: var(--muted); }
.timeline-main { flex: 1; min-width: 0; }
.prompt { font-size: 12px; line-height: 1.5; white-space: pre-wrap; word-break: break-word; }
.response {
  margin-top: 4px;
  padding: 6px 8px;
  background: var(--bg-subtle);
  border-left: 3px solid var(--border);
  border-radius: 4px;
  font-size: 11px;
  color: var(--muted);
  white-space: pre-wrap;
}
.meta-line { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-top: 6px; font-size: 10px; }
.status-pill { padding: 0 6px; border-radius: 10px; font-size: 10px; }
.status-pill.ok { background: rgba(63, 185, 80, 0.15); color: var(--success); }
.status-pill.fail { background: rgba(248, 81, 73, 0.15); color: var(--danger); }
.status-pill.pending { background: rgba(210, 153, 34, 0.15); color: var(--warning); }
.fail-text { color: var(--danger); }
.state-box { padding: 24px; text-align: center; font-size: 12px; color: var(--muted); }
.text-muted { color: var(--muted); font-size: 11px; }
</style>
