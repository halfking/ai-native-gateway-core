<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { downloadReportExport, getReportDailyPage, type GroupDim, type ReportDailyPage, type ReportQuery } from '../../api/reportrollup'
import { surfaceError } from '../../i18n/errorSurface'
import { systemTzDateStr } from '../ui/kxDatePresets'

const props = defineProps<{ query: ReportQuery; dates: string[]; dayThrough: string; showCost: boolean; disabled: boolean }>()
const { t } = useI18n()
const groups = [
  ['model', 'reports.model'], ['provider', 'reports.provider'], ['credential', 'reports.credential'],
  ['tenant', 'reports.tenant'], ['person', 'reports.person'], ['apikey', 'reports.apiKey'],
] as const satisfies readonly (readonly [GroupDim, string])[]
const day = ref('')
const group = ref<GroupDim>('model')
const number = ref(1)
const shown = ref(false)
const loading = ref(false)
const exporting = ref(false)
const error = ref('')
const exportError = ref('')
const result = ref<ReportDailyPage | null>(null)
const dates = computed(() => {
  const through = systemTzDateStr(Date.parse(props.dayThrough))
  return props.dates.filter(date => date < through)
})
const page = computed(() => result.value?.page)
const pages = computed(() => page.value ? Math.max(1, Math.ceil(page.value.total / page.value.page_size)) : 1)
const priceMode = computed(() => props.showCost && props.query.view === 'provider')

watch(dates, (options) => {
  if (!options.includes(day.value)) day.value = options.at(-1) ?? ''
}, { immediate: true })
watch([day, group, () => props.query], () => { number.value = 1 })
watch([shown, day, group, number, () => props.query, () => props.disabled], async (_, __, onCleanup) => {
  result.value = null
  error.value = ''
  loading.value = false
  if (!shown.value || !day.value || props.disabled) return
  const request = new AbortController()
  onCleanup(() => request.abort())
  loading.value = true
  try {
    const next = await getReportDailyPage({ ...props.query, day: day.value, group: group.value, page: number.value }, request.signal)
    if (!request.signal.aborted) result.value = next
  } catch (cause) {
    if (!request.signal.aborted) error.value = surfaceError(cause)
  } finally {
    if (!request.signal.aborted) loading.value = false
  }
})

async function exportRange() {
  exporting.value = true
  exportError.value = ''
  try {
    await downloadReportExport({ ...props.query, group: group.value, detail: true })
  } catch (cause) {
    exportError.value = surfaceError(cause)
  } finally {
    exporting.value = false
  }
}
</script>

<template>
  <section class="detail-panel">
    <button type="button" class="btn btn-secondary" data-testid="report-detail-toggle" :aria-expanded="shown" @click="shown = !shown">
      {{ t('reports.dailyDetails') }}
    </button>
    <template v-if="shown">
      <div v-if="dates.length" class="detail-controls">
        <label>{{ t('reports.date') }} <select v-model="day" data-testid="report-detail-day">
          <option v-for="date in dates" :key="date" :value="date">{{ date }}</option>
        </select></label>
        <label>{{ t('reports.dimension') }} <select v-model="group" data-testid="report-detail-group">
          <option v-for="[key, label] in groups" :key="key" :value="key">{{ t(label) }}</option>
        </select></label>
        <button type="button" class="btn btn-secondary" data-testid="report-detail-export" :disabled="exporting || disabled" @click="exportRange">
          {{ t('reports.exportDailyDetails') }}
        </button>
      </div>
      <p v-if="exportError" role="alert">{{ exportError }}</p>
      <p v-if="loading" role="status">{{ t('common.loading') }}</p>
      <p v-else-if="error" role="alert">{{ error }}</p>
      <p v-else-if="!dates.length">{{ t('reports.projectedToday') }}</p>
      <p v-else-if="result?.projection.pending_since" role="status">{{ t('reports.projectedPending') }}</p>
      <p v-if="page && !page.total">{{ t('reports.projectedEmpty') }}</p>
      <template v-if="page?.total">
        <div class="detail-scroll">
          <table>
            <thead><tr>
              <th scope="col">{{ t('reports.dimension') }}</th>
              <th scope="col">{{ t('reports.requests') }}</th>
              <th scope="col">{{ t('reports.totalTokens') }}</th>
              <th scope="col">{{ t('reports.errorRate') }}</th>
              <th scope="col">{{ t('reports.cacheHit') }}</th>
              <th scope="col">P95 (ms)</th>
              <th scope="col">{{ t(priceMode ? 'reports.legendCost' : 'reports.internalCredits') }}</th>
            </tr></thead>
            <tbody>
              <tr v-for="row in page.rows" :key="row.key">
                <th scope="row">{{ group === 'person' ? row.key.split('\u0000').join(' / ') : (row.name || (row.key === '-1' ? t('reports.unassigned') : row.key)) }}</th>
                <td>{{ row.totals.request_count }}</td>
                <td>{{ row.totals.total_tokens }}</td>
                <td>{{ (row.totals.error_rate * 100).toFixed(1) }}%</td>
                <td>{{ row.totals.cache_hit_ratio == null ? '—' : `${(row.totals.cache_hit_ratio * 100).toFixed(1)}%` }}</td>
                <td>{{ row.totals.latency_measured_requests ? row.totals.latency_p95_ms : '—' }}</td>
                <td v-if="priceMode">{{ row.totals.usd_measured_requests && row.totals.estimated_cost_cents != null ? `${row.totals.currency} ${row.totals.estimated_cost_cents / 100}` : '—' }}</td>
                <td v-else>{{ row.totals.credits_charged }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="pages > 1" class="detail-controls">
          <button type="button" class="btn btn-secondary" :disabled="page.number <= 1" @click="number = page.number - 1">{{ t('common.pagination.previous') }}</button>
          <span>{{ page.number }} / {{ pages }}</span>
          <button type="button" class="btn btn-secondary" data-testid="report-detail-next" :disabled="page.number >= pages" @click="number = page.number + 1">{{ t('common.pagination.next') }}</button>
        </div>
      </template>
    </template>
  </section>
</template>

<style scoped>
.detail-panel { margin-top: var(--space-3); }
.detail-controls { display: flex; align-items: center; flex-wrap: wrap; gap: var(--space-2); margin: var(--space-2) 0; }
.detail-controls select { margin-inline-start: var(--space-1); }
.detail-scroll { overflow-x: auto; }
table { width: 100%; border-collapse: collapse; }
th, td { padding: var(--space-2); border-bottom: 1px solid var(--border); text-align: start; }
</style>
