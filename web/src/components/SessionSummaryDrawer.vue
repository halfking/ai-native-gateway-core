<script setup lang="ts">
// SessionSummaryDrawer — 独立会话总结界面：生成过程、结果、脉络与操作。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { localeRef } from '../i18n'
import {
  getRequestLogs,
  getSessionSummary,
  sessionSummaryToMemora,
  type RequestLogRow,
  type SessionSummaryResponse,
  type SessionSummaryToMemoraResponse,
} from '../api/logs'
import { downloadSessionSummaryExport } from '../utils/sessionSummaryExport'

const props = defineProps<{
  open: boolean
  sessionId: string | null
  sessionTitle?: string | null
  taskId?: string | null
}>()

const emit = defineEmits<{
  close: []
  filterSession: [sessionId: string]
  openRequest: [requestId: string]
}>()

const { t } = useI18n()

type StepId = 'logs' | 'corpus' | 'llm' | 'done'
type StepState = 'pending' | 'active' | 'done' | 'error'

const summaryLoading = ref(false)
const summaryError = ref<string | null>(null)
const summaryResult = ref<SessionSummaryResponse | null>(null)
const memoraLoading = ref(false)
const memoraError = ref<string | null>(null)
const memoraResult = ref<SessionSummaryToMemoraResponse['memora'] | null>(null)
const logRows = ref<RequestLogRow[]>([])
const logTotal = ref(0)
const logsError = ref<string | null>(null)
const activeStep = ref<StepId>('logs')
const stepError = ref(false)
let loadSeq = 0

const steps = computed(() => [
  { id: 'logs' as StepId, label: '加载会话日志' },
  { id: 'corpus' as StepId, label: '整理语料' },
  { id: 'llm' as StepId, label: '生成摘要' },
  { id: 'done' as StepId, label: '完成' },
])

function stepState(id: StepId): StepState {
  if (stepError.value && id === activeStep.value) return 'error'
  const order: StepId[] = ['logs', 'corpus', 'llm', 'done']
  const ai = order.indexOf(activeStep.value)
  const ii = order.indexOf(id)
  if (ii < ai) return 'done'
  if (ii === ai) return summaryLoading.value || (id !== 'done' && !stepError.value) ? 'active' : 'done'
  return 'pending'
}

function fmtTs(ts: string) {
  return new Date(ts).toLocaleString(localeRef.value, { hour12: false })
}

function shortId(id: string) {
  if (id.length <= 16) return id
  return `${id.slice(0, 8)}…${id.slice(-4)}`
}

function resetState(bumpSeq = true) {
  if (bumpSeq) loadSeq++
  summaryLoading.value = false
  summaryError.value = null
  summaryResult.value = null
  memoraLoading.value = false
  memoraError.value = null
  memoraResult.value = null
  logRows.value = []
  logTotal.value = 0
  logsError.value = null
  activeStep.value = 'logs'
  stepError.value = false
}

async function loadSessionLogs(sid: string, seq: number) {
  activeStep.value = 'logs'
  logsError.value = null
  try {
    const resp = await getRequestLogs({
      gw_session_id: sid,
      chrono: true,
      page: 1,
      page_size: 80,
    })
    if (seq !== loadSeq) return
    logRows.value = resp.items ?? []
    logTotal.value = resp.count ?? logRows.value.length
  } catch (e: unknown) {
    if (seq !== loadSeq) return
    logsError.value = e instanceof Error ? e.message : String(e)
  }
}

async function generateSummary(force = false) {
  const sid = (props.sessionId ?? '').trim()
  if (!sid) return
  if (!force && summaryResult.value?.meta.session_id === sid) {
    activeStep.value = 'done'
    return
  }
  const seq = loadSeq
  summaryLoading.value = true
  summaryError.value = null
  stepError.value = false
  memoraResult.value = null
  memoraError.value = null
  activeStep.value = 'corpus'
  try {
    await new Promise(r => setTimeout(r, 120))
    if (seq !== loadSeq) return
    activeStep.value = 'llm'
    const result = await getSessionSummary(sid)
    if (seq !== loadSeq) return
    summaryResult.value = result
    activeStep.value = 'done'
  } catch (e: unknown) {
    if (seq !== loadSeq) return
    stepError.value = true
    activeStep.value = 'llm'
    summaryError.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (seq === loadSeq) summaryLoading.value = false
  }
}

async function runFullLoad() {
  const sid = (props.sessionId ?? '').trim()
  if (!sid) return
  const seq = ++loadSeq
  resetState(false)
  await loadSessionLogs(sid, seq)
  if (seq !== loadSeq) return
  await generateSummary(false)
}

watch(
  [() => props.open, () => props.sessionId],
  ([open, sid]) => {
    if (open && sid) void runFullLoad()
    if (!open) resetState()
  },
  { immediate: true },
)

function closeDrawer() {
  emit('close')
}

function exportSummary(format: 'md' | 'txt') {
  if (!summaryResult.value) return
  downloadSessionSummaryExport(summaryResult.value, {
    docTitle: t('requests.list.summary.title'),
    summaryHeading: t('requests.list.summary.summaryHeading'),
    keyPointsHeading: t('requests.list.summary.keyPointsHeading'),
  }, format)
}

async function writeMemora() {
  const sid = (props.sessionId ?? '').trim()
  if (!sid) return
  memoraLoading.value = true
  memoraError.value = null
  try {
    const resp = await sessionSummaryToMemora(sid)
    summaryResult.value = { summary: resp.summary, key_points: resp.key_points, meta: resp.meta }
    memoraResult.value = resp.memora
  } catch (e: unknown) {
    memoraError.value = e instanceof Error ? e.message : String(e)
  } finally {
    memoraLoading.value = false
  }
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open && sessionId" class="ssd-backdrop" @click="closeDrawer">
      <aside class="ssd-panel card" role="dialog" aria-label="会话总结" @click.stop>
        <header class="ssd-header">
          <div class="ssd-header-main">
            <h3 class="ssd-title">{{ sessionTitle || '会话总结' }}</h3>
            <p class="ssd-sub">
              <span>Session {{ shortId(sessionId!) }}</span>
              <span v-if="taskId" class="ssd-dot">·</span>
              <span v-if="taskId">Task {{ shortId(taskId) }}</span>
            </p>
          </div>
          <button class="btn btn-sm" type="button" @click="closeDrawer">关闭</button>
        </header>

        <section class="ssd-process" aria-label="总结过程">
          <ol class="ssd-steps">
            <li
              v-for="s in steps"
              :key="s.id"
              class="ssd-step"
              :class="`ssd-step--${stepState(s.id)}`"
            >
              <span class="ssd-step-dot" />
              <span>{{ s.label }}</span>
            </li>
          </ol>
        </section>

        <section class="ssd-toolbar">
          <button class="btn btn-primary btn-sm" type="button" :disabled="summaryLoading" @click="generateSummary(true)">
            {{ summaryLoading ? t('requests.list.trace.generating') : (summaryResult ? '重新生成' : t('requests.list.trace.generate')) }}
          </button>
          <button class="btn btn-ghost btn-sm" type="button" :disabled="!summaryResult" @click="exportSummary('md')">
            {{ t('requests.list.trace.exportMd') }}
          </button>
          <button class="btn btn-ghost btn-sm" type="button" :disabled="!summaryResult" @click="exportSummary('txt')">
            {{ t('requests.list.trace.exportTxt') }}
          </button>
          <button class="btn btn-ghost btn-sm" type="button" :disabled="memoraLoading" @click="writeMemora">
            {{ memoraLoading ? t('requests.list.trace.writingMemora') : t('requests.list.trace.writeMemora') }}
          </button>
          <button class="btn btn-ghost btn-sm" type="button" @click="emit('filterSession', sessionId!)">
            在请求日志中筛选
          </button>
        </section>

        <p v-if="summaryError" class="ssd-error">{{ summaryError }}</p>
        <p v-if="logsError" class="ssd-error">日志加载：{{ logsError }}</p>
        <p v-if="memoraError" class="ssd-error">Memora：{{ memoraError }}</p>
        <p v-if="memoraResult" class="ssd-ok">
          {{ t('requests.list.trace.memoraWritten', { n: memoraResult.written, status: memoraResult.status }) }}
        </p>

        <section v-if="summaryResult" class="ssd-result">
          <div class="ssd-meta-line">
            {{ t('requests.list.trace.summaryRange', {
              from: fmtTs(summaryResult.meta.data_from),
              to: fmtTs(summaryResult.meta.data_to),
              n: summaryResult.meta.log_count,
            }) }}
            <span v-if="summaryResult.meta.model" class="ssd-model">· {{ summaryResult.meta.model }}</span>
          </div>
          <h4 class="ssd-section-title">{{ t('requests.list.summary.summaryHeading') }}</h4>
          <div class="ssd-body">{{ summaryResult.summary }}</div>
          <template v-if="summaryResult.key_points?.length">
            <h4 class="ssd-section-title">{{ t('requests.list.summary.keyPointsHeading') }}</h4>
            <ul class="ssd-points">
              <li v-for="(p, i) in summaryResult.key_points" :key="i">{{ p }}</li>
            </ul>
          </template>
        </section>
        <p v-else-if="summaryLoading" class="ssd-muted">正在生成会话摘要…</p>

        <section class="ssd-timeline">
          <div class="ssd-timeline-head">
            <h4 class="ssd-section-title">会话脉络</h4>
            <span class="ssd-muted">共 {{ logTotal }} 条{{ logRows.length < logTotal ? `（展示 ${logRows.length}）` : '' }}</span>
          </div>
          <ul v-if="logRows.length" class="ssd-log-list">
            <li v-for="row in logRows" :key="row.request_id" class="ssd-log-item">
              <button type="button" class="ssd-log-btn" @click="emit('openRequest', row.request_id)">
                <span class="ssd-log-time">{{ fmtTs(row.ts) }}</span>
                <span class="ssd-log-model">{{ row.canonical_name || row.client_model || '—' }}</span>
                <span :class="row.success ? 'ssd-ok-text' : 'ssd-error-text'">{{ row.success ? '成功' : '失败' }}</span>
                <span class="ssd-log-preview">{{ row.request_preview || row.response_preview || '—' }}</span>
              </button>
            </li>
          </ul>
          <p v-else-if="!logsError" class="ssd-muted">暂无同会话请求记录</p>
        </section>
      </aside>
    </div>
  </Teleport>
</template>

<style scoped>
.ssd-backdrop {
  position: fixed;
  inset: 0;
  z-index: 3200;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  justify-content: flex-end;
}
.ssd-panel {
  width: min(720px, 96vw);
  height: 100vh;
  overflow-y: auto;
  border-radius: 0;
  padding: 16px 20px 24px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.ssd-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 12px;
  padding-bottom: 10px;
  border-bottom: 1px solid var(--border);
}
.ssd-title { margin: 0; font-size: 18px; font-weight: 600; }
.ssd-sub { margin: 4px 0 0; font-size: 12px; color: var(--muted); display: flex; gap: 6px; flex-wrap: wrap; }
.ssd-dot { opacity: 0.5; }
.ssd-process { padding: 8px 10px; background: var(--bg-subtle); border-radius: 8px; border: 1px solid var(--border); }
.ssd-steps { list-style: none; margin: 0; padding: 0; display: flex; flex-wrap: wrap; gap: 10px 16px; font-size: 12px; }
.ssd-step { display: inline-flex; align-items: center; gap: 6px; color: var(--muted); }
.ssd-step--active { color: var(--accent); font-weight: 600; }
.ssd-step--done { color: var(--success); }
.ssd-step--error { color: var(--danger); font-weight: 600; }
.ssd-step-dot {
  width: 8px; height: 8px; border-radius: 50%;
  background: currentColor; opacity: 0.35;
}
.ssd-step--active .ssd-step-dot { opacity: 1; box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 25%, transparent); }
.ssd-toolbar { display: flex; flex-wrap: wrap; gap: 8px; }
.ssd-result, .ssd-timeline {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px 14px;
  background: var(--surface-primary, var(--card));
}
.ssd-section-title { margin: 0 0 8px; font-size: 13px; font-weight: 600; }
.ssd-meta-line { font-size: 12px; color: var(--muted); margin-bottom: 10px; }
.ssd-model { font-family: ui-monospace, monospace; }
.ssd-body { white-space: pre-wrap; line-height: 1.6; font-size: 13px; }
.ssd-points { margin: 0; padding-left: 18px; font-size: 13px; line-height: 1.5; }
.ssd-timeline-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; margin-bottom: 8px; }
.ssd-log-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; max-height: 280px; overflow-y: auto; }
.ssd-log-item { margin: 0; }
.ssd-log-btn {
  width: 100%; text-align: left; border: 1px solid var(--border); border-radius: 6px;
  background: var(--bg); padding: 6px 8px; font-size: 11px; cursor: pointer;
  display: grid; grid-template-columns: auto auto auto 1fr; gap: 8px; align-items: center;
}
.ssd-log-btn:hover { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 6%, transparent); }
.ssd-log-time { white-space: nowrap; color: var(--muted); font-variant-numeric: tabular-nums; }
.ssd-log-model { font-weight: 600; max-width: 14ch; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ssd-log-preview { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--muted); }
.ssd-error { color: var(--danger); font-size: 12px; margin: 0; }
.ssd-ok { color: var(--success); font-size: 12px; margin: 0; }
.ssd-ok-text { color: var(--success); }
.ssd-error-text { color: var(--danger); }
.ssd-muted { color: var(--muted); font-size: 12px; margin: 0; }
</style>
