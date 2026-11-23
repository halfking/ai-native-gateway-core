<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { exportDailyReport, readDailyReport, type EvidenceExport, type DailyQuery, type DailyReconciliationCalendar, type DailyReport, type DailySection } from '../../api/requestReconciliation'
import { surfaceError } from '../../i18n/errorSurface'
import { onSystemTimezoneChange, systemTimezoneState } from '../../utils/systemTimezone'
import KxDatePicker from '../ui/KxDatePicker.vue'
import { dailyFields } from './dailyFields'
import DailyReconciliationResult from './DailyReconciliationResult.vue'
import DailySupplierInvoices from './DailySupplierInvoices.vue'

const props = defineProps<{ tenantScope?: string; primaryCalendar?: DailyReconciliationCalendar }>()
const { t } = useI18n()
const tenantOnly = computed(() => props.tenantScope !== undefined)
const timezone = ref(props.primaryCalendar?.timezone || systemTimezoneState().timezone || 'Asia/Shanghai')
const day = ref(props.primaryCalendar?.day || previousDay(timezone.value))
type DailyView = DailySection | 'supplier_invoices'
const section = ref<DailyView>(tenantOnly.value ? 'internal' : 'coverage')
const invoicesView = computed(() => section.value === 'supplier_invoices' && !tenantOnly.value)
const tenant = ref('')
const boundTenant = computed(() => tenantOnly.value ? props.tenantScope : tenant.value.trim())
const report = ref<DailyReport | null>(null)
const displayedQuery = ref<DailyQuery | null>(null)
const busy = ref(false)
const exporting = ref(false)
const errorText = ref('')
let request: AbortController | undefined
let download: AbortController | undefined
let periodEdited = !!props.primaryCalendar

function markPeriodEdited() {
  periodEdited = true
}

function previousDay(zone: string): string {
  const fields = new Intl.DateTimeFormat('en', { timeZone: zone, year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(new Date())
  const localDate = ['year', 'month', 'day'].map((kind) => fields.find((part) => part.type === kind)?.value).join('-')
  const calendar = new Date(`${localDate}T00:00:00Z`)
  calendar.setUTCDate(calendar.getUTCDate() - 1)
  return calendar.toISOString().slice(0, 10)
}

const allowed = computed<DailyView[]>(() => tenantOnly.value ? ['internal'] : ['coverage', 'requests', 'attempts', 'sessions', 'postings', 'internal', 'supplier_invoices'])
const canRead = computed(() => allowed.value.includes(section.value) && /^\d{4}-\d{2}-\d{2}$/.test(day.value)
  && timezone.value.trim() !== '' && (section.value !== 'internal' || !!boundTenant.value))
const hasNext = computed(() => {
  if (!report.value) return false
  const { section, summary, limit, offset, rows } = report.value
  const total = summary[dailyFields[section].count]
  return typeof total === 'number' ? offset + rows.length < total : rows.length >= limit
})

function clearReport() {
  request?.abort()
  download?.abort()
  report.value = null
  displayedQuery.value = null
  busy.value = false
  exporting.value = false
  errorText.value = ''
}

watch([day, timezone, section, boundTenant, tenantOnly], clearReport, { flush: 'sync' })
const unsubscribeTimezone = onSystemTimezoneChange(() => {
  if (periodEdited) return
  timezone.value = systemTimezoneState().timezone || 'Asia/Shanghai'
  day.value = previousDay(timezone.value)
})
onBeforeUnmount(() => { unsubscribeTimezone(); request?.abort(); download?.abort() })
onMounted(() => { if (props.primaryCalendar) void load() })

async function load(offset = 0) {
  if (!canRead.value || section.value === 'supplier_invoices') return
  periodEdited = true
  request?.abort()
  download?.abort()
  exporting.value = false
  const controller = new AbortController()
  request = controller
  const query: DailyQuery = { day: day.value, timezone: timezone.value.trim(), section: section.value,
    tenantId: boundTenant.value || undefined, limit: 50, offset }
  busy.value = true
  errorText.value = ''
  try {
    const data = await readDailyReport(query, { signal: controller.signal })
    if (controller.signal.aborted) return
    if (data.section !== query.section || data.day !== query.day || data.timezone !== query.timezone || data.offset !== offset
      || (!tenantOnly.value && data.platform_scope !== true) || (data.tenant_id || undefined) !== query.tenantId) {
      throw new Error('reconciliation scope changed')
    }
    report.value = data
    displayedQuery.value = { ...query, limit: data.limit, offset: data.offset }
  } catch (error) {
    if (!controller.signal.aborted) errorText.value = surfaceError(error, { key: 'reports.daily.loadFailed' })
  } finally {
    if (!controller.signal.aborted) busy.value = false
  }
}

async function downloadCSV(format: EvidenceExport) {
  if (!displayedQuery.value || busy.value || exporting.value) return
  const controller = new AbortController()
  download = controller
  errorText.value = ''
  exporting.value = true
  try {
    await exportDailyReport(displayedQuery.value, format, { signal: controller.signal })
  } catch (error) {
    if (!controller.signal.aborted) errorText.value = surfaceError(error, { key: 'reports.daily.exportFailed' })
  } finally {
    if (!controller.signal.aborted) exporting.value = false
  }
}
</script>

<template>
  <details class="daily-panel" :open="!!primaryCalendar">
    <summary>{{ t('reports.daily.title') }}</summary>
    <div class="daily-panel__body" :aria-busy="busy">
      <form class="daily-panel__controls" @submit.prevent="load()">
        <label v-if="!tenantOnly">
          {{ t('reports.daily.section') }}
          <select v-model="section" class="input" data-testid="daily-section">
            <option v-for="key in allowed" :key="key" :value="key">{{ t(`reports.daily.sections.${key}`) }}</option>
          </select>
        </label>
        <label>{{ t('reports.daily.day') }}<KxDatePicker v-model="day" :clearable="false" :aria-label="t('reports.daily.day')" @change="markPeriodEdited" /></label>
        <label>{{ t('reports.daily.timezone') }}<input v-model="timezone" class="input" data-testid="daily-timezone" autocomplete="off" required @input="markPeriodEdited" /></label>
        <label v-if="!tenantOnly && !invoicesView">
          {{ t(section === 'internal' ? 'reports.daily.tenantRequired' : 'reports.daily.tenantOptional') }}
          <input v-model="tenant" class="input" data-testid="daily-tenant" :required="section === 'internal'" autocomplete="off" />
        </label>
        <button v-if="!invoicesView" type="submit" class="btn btn-primary" data-testid="daily-load" :disabled="!canRead || busy">{{ t('reports.daily.load') }}</button>
      </form>
      <p v-if="section === 'internal'" class="daily-panel__note">{{ t('reports.daily.internalNote') }}</p>
      <p v-if="busy" role="status">{{ t('reports.daily.loading') }}</p>
      <p v-if="errorText" class="daily-panel__error" role="alert">{{ errorText }}</p>
      <DailySupplierInvoices v-if="invoicesView" :day="day" :timezone="timezone.trim()" @browse="markPeriodEdited" />

      <template v-if="report">
        <DailyReconciliationResult :report="report" />
        <div class="daily-panel__actions">
          <button v-if="report.offset > 0" type="button" class="btn btn-secondary" :disabled="busy || exporting" @click="load(Math.max(0, report.offset - report.limit))">{{ t('reports.daily.previousPage') }}</button>
          <button v-if="hasNext" type="button" class="btn btn-secondary" data-testid="daily-next" :disabled="busy || exporting" @click="load(report.offset + report.limit)">{{ t('reports.daily.nextPage') }}</button>
          <button type="button" class="btn btn-secondary" data-testid="daily-export-summary" :disabled="busy || exporting" @click="downloadCSV('csv-summary')">{{ t('reports.daily.exportSummary') }}</button>
          <button type="button" class="btn btn-secondary" data-testid="daily-export-page" :disabled="busy || exporting || !report.rows.length" @click="downloadCSV('csv')">{{ t('reports.daily.exportPage') }}</button>
        </div>
      </template>
    </div>
  </details>
</template>

<style scoped>
.daily-panel { min-inline-size: 0; margin-block: var(--space-4); }
.daily-panel summary { cursor: pointer; font-weight: 600; }
.daily-panel__body { display: grid; min-inline-size: 0; gap: var(--space-3); margin-block-start: var(--space-3); }
.daily-panel__controls, .daily-panel__actions { display: flex; flex-wrap: wrap; align-items: end; gap: var(--space-2); }
.daily-panel__controls label { display: grid; grid-template-columns: minmax(0, 1fr); flex: 1 1 9rem; min-inline-size: 0; gap: var(--space-1); }
.daily-panel__controls :is(input, select) { width: 100%; }
.daily-panel__controls :deep(.el-date-editor.kx-date-picker) { inline-size: 100%; min-inline-size: 0; max-inline-size: 100%; }
.daily-panel__note { margin: 0; color: var(--text-secondary); }
.daily-panel__error { margin: 0; color: var(--danger); }
</style>
