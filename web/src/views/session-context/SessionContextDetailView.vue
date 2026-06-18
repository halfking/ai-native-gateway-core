<script setup lang="ts">
import { computed, inject, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  getMemoraContext,
  extractSessionToMemora,
  getSessionExtractionStatus,
  summarizeSessionTitle,
  type MemoraContextResponse,
  type ReadableBlock,
  type SessionExtractionStatusResponse,
} from '../../api'
import {
  buildSessionQueryParams,
  fmtScore,
  sessionScopeToParams,
  listBackQueryFromRoute,
  parseSessionScopeFromRoute,
  tagClass,
  type useSessionFilters,
} from '../../composables/useSessionContext'

const route = useRoute()
const router = useRouter()
const filters = inject<ReturnType<typeof useSessionFilters>>('sessionContextFilters')!

const taskId = computed(() => decodeURIComponent(String(route.params.taskId || '')))
const isNoTopic = computed(() => taskId.value === '_no-topic')

const sessionScope = computed(() =>
  parseSessionScopeFromRoute(route, filters.hours.value),
)
const backListQuery = computed(() => listBackQueryFromRoute(route))

const contextData = ref<MemoraContextResponse | null>(null)
const contextLoading = ref(false)
const contextError = ref('')

const extractionStatus = ref<SessionExtractionStatusResponse | null>(null)
const extracting = ref(false)
const extractResult = ref('')
const extractError = ref('')

const summarizingTitle = ref(false)
const titleResult = ref('')
const titleError = ref('')
const localTitle = ref('')

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

const requestLogsHref = computed(() => {
  const qs = new URLSearchParams()
  qs.set('gw_task', taskId.value)
  if (sessionScope.value.session_id) qs.set('gw_session_id', sessionScope.value.session_id)
  if (sessionScope.value.hours) qs.set('hours', String(sessionScope.value.hours))
  return `/request-logs?${qs.toString()}`
})

const pageTitle = computed(() => {
  if (isNoTopic.value) return '无主题会话'
  if (localTitle.value) return localTitle.value
  return contextData.value?.title || taskId.value || '会话详情'
})

function sourceLabel(source: string) {
  return source === 'gw-session' ? '会话总结' : '任务提炼'
}

async function loadContext() {
  if (isNoTopic.value || !taskId.value) return
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

async function loadExtractionStatus() {
  if (isNoTopic.value || !taskId.value) return
  try {
    extractionStatus.value = await getSessionExtractionStatus(taskId.value)
  } catch {
    extractionStatus.value = null
  }
}

async function doSummarizeTitle() {
  if (isNoTopic.value || !taskId.value || summarizingTitle.value) return
  summarizingTitle.value = true
  titleResult.value = ''
  titleError.value = ''
  try {
    const resp = await summarizeSessionTitle(taskId.value, sessionScopeToParams(sessionScope.value))
    localTitle.value = resp.title
    if (contextData.value) contextData.value.title = resp.title
    titleResult.value = '会话标题已更新'
  } catch (e: unknown) {
    titleError.value = e instanceof Error ? e.message : '标题生成失败'
  } finally {
    summarizingTitle.value = false
  }
}

async function doExtractToMemora() {
  if (isNoTopic.value || !taskId.value || extracting.value) return
  extracting.value = true
  extractResult.value = ''
  extractError.value = ''
  try {
    const resp = await extractSessionToMemora(taskId.value, sessionScopeToParams(sessionScope.value))
    extractResult.value = `已写入 ${resp.written} 条，跳过噪音 ${resp.skipped_noise}、重复 ${resp.skipped_duplicate}`
    await loadExtractionStatus()
    await loadContext()
  } catch (e: unknown) {
    extractError.value = e instanceof Error ? e.message : '提炼失败'
  } finally {
    extracting.value = false
  }
}

onMounted(() => {
  if (route.query.tab === 'timeline') {
    router.replace({ query: { ...route.query, tab: undefined } })
  }
  if (!isNoTopic.value) {
    loadContext()
    loadExtractionStatus()
  }
})

watch(
  () => [taskId.value, route.query.hours, route.query.session_id, route.query.rc] as const,
  () => {
    contextData.value = null
    extractionStatus.value = null
    extractResult.value = ''
    extractError.value = ''
    localTitle.value = ''
    titleResult.value = ''
    titleError.value = ''
    if (!isNoTopic.value) {
      loadContext()
      loadExtractionStatus()
    }
  },
)
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
        <div class="detail-actions">
          <button
            class="btn btn-primary btn-sm"
            :disabled="summarizingTitle"
            @click="doSummarizeTitle"
          >
            <span v-if="summarizingTitle" class="btn-spinner" aria-hidden="true" />
            {{ summarizingTitle ? '生成中…' : '总结会话标题' }}
          </button>
          <button
            class="btn btn-primary btn-sm"
            :disabled="extracting"
            @click="doExtractToMemora"
          >{{ extracting ? '提炼中…' : '提炼入 Memora' }}</button>
          <span
            v-if="extractionStatus?.extracted"
            class="badge badge-green"
            :title="extractionStatus.extracted_at"
          >已写入 {{ extractionStatus.written ?? 0 }} 条</span>
        </div>
        <p v-if="titleResult" class="extract-ok">{{ titleResult }}</p>
        <p v-if="titleError" class="extract-err">{{ titleError }}</p>
        <p v-if="extractResult" class="extract-ok">{{ extractResult }}</p>
        <p v-if="extractError" class="extract-err">{{ extractError }}</p>
      </template>
    </div>

    <template v-if="isNoTopic">
      <div class="card compact-card state-box">
        <p>无主题会话不存储 Memora 记忆，也无法查看可读内容。</p>
        <p class="text-muted">
          可通过
          <a :href="`/request-logs?hours=${route.query.hours || filters.hours.value}`" target="_blank">请求日志</a>
          按时间和 Key 前缀筛选查看。
        </p>
      </div>
    </template>

    <template v-else>
      <div class="card compact-card">
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
          <p class="empty-title">Memora中没有</p>
          <p v-if="(contextData?.facts_written ?? 0) > 0" class="text-muted">
            已写入 {{ contextData?.facts_written }} 条（检索可能仍在索引，或超过可见上限）
          </p>
          <p v-if="contextData?.facts_search_error" class="extract-err">{{ contextData.facts_search_error }}</p>
          <p v-else class="text-muted">点击上方「提炼入 Memora」将对话中的有用信息写入记忆。</p>
        </div>
        <div v-else class="blocks-list">
          <div v-for="(block, i) in readableBlocks" :key="block.id || i" class="block-item">
            <div class="block-head">
              <span class="block-idx">#{{ i + 1 }}</span>
              <span class="badge badge-blue">{{ sourceLabel(block.source) }}</span>
              <span v-if="block.kind === 'json'" class="badge badge-purple">JSON</span>
              <span v-if="block.score" class="badge badge-green">{{ fmtScore(block.score) }}</span>
              <span v-for="t in (block.tags || [])" :key="t" :class="'badge ' + tagClass(block.tags)">{{ t }}</span>
            </div>
            <pre v-if="block.kind === 'json'" class="json-block">{{ block.text }}</pre>
            <div v-else class="block-body">{{ block.text }}</div>
          </div>
        </div>

        <div v-if="contextData" class="raw-logs-link">
          <a :href="requestLogsHref" target="_blank" class="btn btn-ghost btn-sm">
            查看原始请求日志 ({{ contextData.request_count }} 条) →
          </a>
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
.raw-logs-link { margin-top: 12px; padding-top: 8px; border-top: 1px solid var(--border); }
.empty-memora .empty-title { font-size: 14px; font-weight: 600; color: var(--muted); margin-bottom: 6px; }
.state-box { padding: 24px; text-align: center; font-size: 12px; color: var(--muted); }
.text-muted { color: var(--muted); font-size: 11px; }
</style>
