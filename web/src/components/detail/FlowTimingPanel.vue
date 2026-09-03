<script setup lang="ts">
// FlowTimingPanel — shows each trace stage with duration_ms (— when unknown).
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getRequestTrace, type RequestTrace, type TraceEvent } from '../../api/trace'
import { getRequestJourney, type RequestJourney } from '../../api/request-journeys'
import { fetchDispatchJournal, type DispatchJournalSnapshot } from '../../api/dispatchJournal'
import { fetchWaterfallByRequestId, type WaterfallRequest } from '../../api/dispatch'
import RequestProcessingFlowDiagram from './RequestProcessingFlowDiagram.vue'

const props = defineProps<{ requestId: string | null }>()
const emit = defineEmits<{
  (e: 'goto', section: 'waterfall' | 'attempts'): void
}>()

const loading = ref(false)
const error = ref('')
const trace = ref<RequestTrace | null>(null)
const journey = ref<RequestJourney | null>(null)
const waterfall = ref<WaterfallRequest | null>(null)
const waterfallResultSource = ref('')
const journal = ref<DispatchJournalSnapshot | null>(null)
const journeyError = ref('')
const waterfallError = ref('')
const journalError = ref('')
let loadSequence = 0

const { t } = useI18n()

watch(
  () => props.requestId,
  async (id) => {
    trace.value = null
    journey.value = null
    waterfall.value = null
    waterfallResultSource.value = ''
    journal.value = null
    error.value = ''
    journeyError.value = ''
    waterfallError.value = ''
    journalError.value = ''
    if (!id) return
    const sequence = ++loadSequence
    loading.value = true
    const results = await Promise.allSettled([
      getRequestTrace(id),
      getRequestJourney(id),
      fetchWaterfallByRequestId(id),
    ])
    if (sequence !== loadSequence) return
    const [traceResult, journeyResult, waterfallResult] = results
    if (traceResult.status === 'fulfilled') trace.value = traceResult.value
    else error.value = traceResult.reason instanceof Error ? traceResult.reason.message : String(traceResult.reason)
    if (journeyResult.status === 'fulfilled') journey.value = journeyResult.value
    else journeyError.value = journeyResult.reason instanceof Error ? journeyResult.reason.message : String(journeyResult.reason)
    if (waterfallResult.status === 'fulfilled') {
      waterfall.value = waterfallResult.value.request
      waterfallResultSource.value = waterfallResult.value.source || ''
    } else {
      waterfallError.value = waterfallResult.reason instanceof Error ? waterfallResult.reason.message : String(waterfallResult.reason)
    }
    if (journeyResult.status === 'fulfilled' && journeyResult.value.tenant_id) {
      try {
        const nextJournal = await fetchDispatchJournal(journeyResult.value.tenant_id, id)
        if (sequence === loadSequence) journal.value = nextJournal
      } catch (e: unknown) {
        if (sequence === loadSequence) journalError.value = e instanceof Error ? e.message : String(e)
      }
    }
    if (sequence !== loadSequence) return
    loading.value = false
  },
  { immediate: true },
)

const events = computed(() => trace.value?.events ?? [])

function durationLabel(e: TraceEvent): string {
  if (typeof e.duration_ms !== 'number' || !Number.isFinite(e.duration_ms)) return '—'
  return `${e.duration_ms}ms`
}

function statusClass(status: string): string {
  if (status === 'failed' || status === 'timeout') return 'bad'
  if (status === 'skipped') return 'muted'
  return 'ok'
}
</script>

<template>
  <div class="flow-panel">
    <div v-if="loading" class="text-muted">{{ t('requestDetail.flow.loading') }}</div>
    <div v-else-if="error && !journey && !waterfall" class="err" role="alert">{{ error }}</div>
    <template v-else-if="trace || journey || waterfall">
      <div class="flow-summary">
        <template v-if="trace">
          <span>{{ t('requestDetail.flow.total') }} <strong>{{
            typeof trace.total_duration_ms === 'number' ? `${trace.total_duration_ms}ms` : '—'
          }}</strong></span>
          <span>{{ t('requestDetail.flow.status', { value: trace.final_status || t('requestDetail.flow.inProgress') }) }}</span>
          <span v-if="trace.failed_at_stage" class="bad">{{ t('requestDetail.flow.failedAt', { stage: trace.failed_at_stage }) }}</span>
          <span class="muted">{{ t('requestDetail.flow.source', { src: trace.source }) }}</span>
        </template>
        <span v-if="journey?.observation_status === 'observation_degraded' || journeyError || waterfallError" class="degraded" role="status">
          {{ t('requestDetail.flow.observationDegraded') }}
        </span>
        <span v-if="journal" class="muted" data-testid="journal-summary">
          Journal {{ journal.entries.length }} decisions<span v-if="journal.truncated"> ({{ journal.truncated_count }} truncated)</span>
        </span>
        <span v-if="journalError" class="degraded" role="status" data-testid="journal-degraded">
          Journal unavailable
        </span>
        <span v-if="journal?.entries?.length" class="journal-actions" aria-label="Journal decisions">
          <span v-for="entry in journal.entries.slice(-4)" :key="entry.seq" class="journal-chip">
            #{{ entry.seq }} {{ entry.action }}<span v-if="entry.model"> · {{ entry.model }}</span>
          </span>
        </span>
        <button type="button" class="btn btn-sm link" @click="emit('goto', 'waterfall')">{{ t('requestDetail.flow.viewWaterfall') }}</button>

        <button type="button" class="btn btn-sm link" @click="emit('goto', 'attempts')">{{ t('requestDetail.flow.viewRoutingRetry') }}</button>
      </div>
      <RequestProcessingFlowDiagram
        :trace="trace"
        :journey="journey"
        :waterfall="waterfall"
        :waterfall-source="waterfallResultSource"
      />
      <ul class="flow-list">
        <li
          v-for="e in events"
          :key="e.seq"
          class="flow-row"
          :class="statusClass(e.status)"
        >
          <span class="stage">{{ e.stage_name || e.stage }}</span>
          <span class="status">{{ e.status }}</span>
          <span class="dur">{{ durationLabel(e) }}</span>
          <span v-if="e.error" class="err-snip">{{ e.error }}</span>
        </li>
      </ul>
      <p v-if="!events.length" class="text-muted">{{ t('requestDetail.flow.empty') }}</p>
    </template>
    <div v-else class="text-muted">{{ t('requestDetail.flow.noData') }}</div>
  </div>
</template>

<style scoped>
.flow-summary {
  display: flex; flex-wrap: wrap; gap: 12px;
  font-size: 12px; margin-bottom: 10px; color: var(--text-secondary);
}
.journal-actions { display: inline-flex; flex-wrap: wrap; gap: 4px; }
.journal-chip { border: 1px solid var(--border); border-radius: 999px; padding: 1px 6px; font-size: 10px; color: var(--text-secondary); }
.degraded { color: var(--warning, #d97706); }
.flow-list { list-style: none; margin: 0; padding: 0; }
.flow-row {
  display: grid;
  grid-template-columns: minmax(120px, 1.4fr) 72px 72px 1fr;
  gap: 8px; align-items: baseline;
  padding: 6px 8px; border-bottom: 1px solid var(--border);
  font-size: 12px;
}
.flow-row.bad .stage, .flow-row.bad .dur { color: var(--danger); }
.flow-row.muted { opacity: 0.65; }
.stage { font-weight: 600; }
.dur { font-variant-numeric: tabular-nums; text-align: right; font-weight: 600; }
.status { color: var(--muted); }
.err-snip { color: var(--danger); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.err { color: var(--danger); }
.muted, .text-muted { color: var(--muted); font-size: 12px; }
.link { margin-left: 4px; }
</style>
