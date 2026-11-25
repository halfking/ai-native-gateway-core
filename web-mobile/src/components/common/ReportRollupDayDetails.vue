<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { t } from '@/i18n'
import { fetchReportDailyPage, type ReportDailyGroup, type ReportDailyPage, type ReportGrainReport } from '@/api/reportRollup'

const props = defineProps<{ report: ReportGrainReport; trafficScope?: 'business' }>()
const groups = computed(() => [
  ['model', t('rr.dims.models')], ['provider', t('rr.dims.providers')], ['credential', t('rr.dims.credentials')],
  ['tenant', t('rr.dims.tenants')], ['person', t('rr.dims.persons')], ['apikey', t('rr.dims.apiKeys')],
] as const)
const dates = computed(() => props.report.days.flatMap(row => {
  if (row == null || typeof row !== 'object' || !('date' in row) || typeof row.date !== 'string') return []
  return [row.date]
}))
const day = ref('')
const group = ref<ReportDailyGroup>('model')
const number = ref(1)
const loading = ref(false)
const error = ref('')
const result = ref<ReportDailyPage | null>(null)
const page = computed(() => result.value?.page)
const pages = computed(() => page.value ? Math.max(1, Math.ceil(page.value.total / page.value.page_size)) : 1)
const money = computed(() => props.report.view === 'provider' || props.report.view === 'credential')

watch(dates, (options) => {
  if (!options.includes(day.value)) day.value = options.at(-1) ?? ''
}, { immediate: true })
watch([day, group, () => props.report, () => props.trafficScope], () => { number.value = 1 })
watch([day, group, number, () => props.report, () => props.trafficScope], async (_, __, onCleanup) => {
  result.value = null
  error.value = ''
  loading.value = false
  if (!day.value) return
  const request = new AbortController()
  onCleanup(() => request.abort())
  loading.value = true
  try {
    const next = await fetchReportDailyPage({
      start: props.report.start.slice(0, 10), end: props.report.end.slice(0, 10),
      view: props.report.view, ...(props.trafficScope && { trafficScope: props.trafficScope }),
      day: day.value, group: group.value, page: number.value,
    }, { signal: request.signal })
    if (!request.signal.aborted) result.value = next
  } catch (cause) {
    if (!request.signal.aborted) error.value = (cause as Error)?.message || String(cause)
  } finally {
    if (!request.signal.aborted) loading.value = false
  }
}, { immediate: true })
</script>

<template>
  <section class="day-detail" data-testid="rr-day-detail">
    <h2>{{ t('rr.query.detail') }}</h2>
    <div v-if="dates.length" class="day-controls">
      <label>{{ t('rr.detailPage.date') }} <select v-model="day" data-testid="rr-detail-date">
        <option v-for="date in dates" :key="date" :value="date">{{ date }}</option>
      </select></label>
      <label>{{ t('rr.detailPage.dimension') }} <select v-model="group" data-testid="rr-detail-group">
        <option v-for="[key, label] in groups" :key="key" :value="key">{{ label }}</option>
      </select></label>
    </div>
    <p v-if="loading" role="status">{{ t('common.loading') }}</p>
    <p v-else-if="error" role="alert">{{ error }}</p>
    <p v-else-if="!dates.length || (page && !page.total)">{{ t('rr.detailPage.empty') }}</p>
    <p v-if="result?.projection.pending_since" role="status">{{ t('rr.projectedPending') }}</p>
    <template v-if="page?.total">
      <div class="day-scroll">
        <table>
          <thead><tr>
            <th scope="col">{{ t('rr.detailPage.dimension') }}</th>
            <th scope="col">{{ t('rr.col.requestCount') }}</th>
            <th scope="col">{{ t('rr.col.totalTokens') }}</th>
            <th scope="col">{{ t('rr.col.errorRate') }}</th>
            <th scope="col">{{ t('rr.col.cacheHitRatio') }}</th>
            <th scope="col">{{ t('rr.col.latencyP95') }} (ms)</th>
            <th scope="col">{{ money ? t('rr.col.estimatedCost') : t('rr.col.creditsCharged') }}</th>
          </tr></thead>
          <tbody>
            <tr v-for="row in page.rows" :key="row.key">
              <th scope="row">{{ group === 'person' ? row.key.split('\u0000').join(' / ') : (row.name || (row.key === '-1' ? t('rr.detailPage.unassigned') : row.key)) }}</th>
              <td>{{ row.totals.request_count }}</td>
              <td>{{ row.totals.total_tokens }}</td>
              <td>{{ (row.totals.error_rate * 100).toFixed(1) }}%</td>
              <td>{{ row.totals.cache_hit_ratio == null ? '—' : `${(row.totals.cache_hit_ratio * 100).toFixed(1)}%` }}</td>
              <td>{{ row.totals.latency_measured_requests ? row.totals.latency_p95_ms : '—' }}</td>
              <td v-if="money">{{ row.totals.usd_measured_requests && row.totals.estimated_cost_cents != null ? `${row.totals.estimated_cost_cents} ${row.totals.currency}` : '—' }}</td>
              <td v-else>{{ row.totals.credits_charged }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-if="pages > 1" class="day-controls">
        <button type="button" :disabled="page.number <= 1" @click="number = page.number - 1">{{ t('rr.detailPage.previous') }}</button>
        <span>{{ page.number }} / {{ pages }}</span>
        <button type="button" data-testid="rr-detail-next" :disabled="page.number >= pages" @click="number = page.number + 1">{{ t('rr.detailPage.next') }}</button>
      </div>
    </template>
  </section>
</template>

<style scoped>
.day-detail { padding: 12px; }
.day-detail h2 { font-size: 16px; margin: 0 0 8px; }
.day-controls { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin: 8px 0; }
.day-controls label { display: flex; align-items: center; gap: 6px; }
.day-controls select, .day-controls button { min-height: 48px; }
.day-scroll { overflow-x: auto; }
table { border-collapse: collapse; }
th, td { padding: 8px; border-bottom: 1px solid var(--border-color, currentColor); text-align: start; }
</style>
