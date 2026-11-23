<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { exportSupplierInvoice, listDailySupplierInvoices, readSupplierInvoice, type EvidenceExport,
  type SupplierInvoice, type SupplierInvoiceReport } from '../../api/requestReconciliation'
import { surfaceError } from '../../i18n/errorSurface'

const props = defineProps<{ day: string; timezone: string }>()
const emit = defineEmits<{ browse: [] }>()
const { t, locale } = useI18n()
const list = ref<SupplierInvoice[] | null>(null)
const report = ref<SupplierInvoiceReport | null>(null)
const listOffset = ref(0)
const busy = ref(false)
const errorText = ref('')
let operation: AbortController | undefined
const pageSize = 30
const metrics = ['invoice_total', 'matched_lines', 'bucket_matched_lines', 'review_lines', 'adjustment_total',
  'estimate_comparable_invoice_amount', 'comparable_estimated_amount', 'estimated_minus_invoiced_amount',
  'reported_comparable_invoice_amount', 'comparable_reported_amount', 'reported_minus_invoiced_amount']
const columns = computed(() => ['line_ref', 'kind', 'model', 'provider_request_id', 'match_status', 'invoice_amount',
  'bucket_sent_attempts', 'bucket_observed_sends', 'bucket_input_tokens', 'bucket_observed_input',
  'bucket_output_tokens', 'bucket_observed_output',
  ...['generated_images', 'audio_input_duration_us', 'audio_billed_duration_us', 'audio_output_duration_us', 'video_output_duration_us']
    .flatMap(meter => {
      const declared = `bucket_${meter}`
      const observed = `bucket_observed_${meter}`
      return report.value?.lines.some(line => line[declared] != null || line[observed] != null) ? [declared, observed] : []
    }),
  'comparable_estimate', 'comparable_reported', 'bucket_sends_without_comparable_estimate', 'bucket_sends_without_comparable_report'])
const label = (key: string) => t(`reports.daily.invoices.fields.${key}`)

function value(field: string, data: unknown) {
  if (data == null || data === '') return '—'
  if (typeof data === 'number') return Number.isSafeInteger(data) ? data.toLocaleString(locale.value) : t('reports.daily.exactInCSV')
  if (typeof data !== 'string') return '—'
  return field === 'match_status' || field === 'kind' ? t(`reports.daily.invoices.values.${data}`, data) : data
}

watch(() => [props.day, props.timezone], () => {
  operation?.abort()
  list.value = null
  report.value = null
  listOffset.value = 0
  busy.value = false
  errorText.value = ''
}, { flush: 'sync' })
onBeforeUnmount(() => operation?.abort())

async function run(action: (signal: AbortSignal) => Promise<void>, errorKey = 'reports.daily.loadFailed') {
  if (busy.value) return
  const controller = new AbortController()
  operation = controller
  busy.value = true
  errorText.value = ''
  try {
    await action(controller.signal)
  } catch (error) {
    if (!controller.signal.aborted) errorText.value = surfaceError(error, { key: errorKey })
  } finally {
    if (!controller.signal.aborted) busy.value = false
  }
}

function loadDay(offset = 0) {
  emit('browse')
  return run(async (signal) => {
    const { day, timezone } = props
    report.value = null
    const data = await listDailySupplierInvoices(day, timezone, pageSize, offset, { signal })
    if (signal.aborted) return
    if (data.day !== day || data.timezone !== timezone || data.offset !== offset || !Array.isArray(data.items)) {
      throw new Error('supplier invoice list scope changed')
    }
    listOffset.value = offset
    list.value = data.items
  })
}

function loadInvoice(invoice: SupplierInvoice, offset = 0) {
  return run(async (signal) => {
    const data = await readSupplierInvoice(invoice.invoice_id, pageSize, offset, { signal })
    if (signal.aborted) return
    if (data.invoice.invoice_id !== invoice.invoice_id || data.offset !== offset
      || data.invoice.window_start !== invoice.window_start || data.invoice.window_end !== invoice.window_end) {
      throw new Error('supplier invoice scope changed')
    }
    report.value = data
  })
}

function download(format: EvidenceExport) {
  const selected = report.value
  if (selected) return run((signal) => exportSupplierInvoice(selected, format, { signal }), 'reports.daily.exportFailed')
}
</script>

<template>
  <div class="invoices" data-testid="daily-supplier-invoices" :aria-busy="busy">
    <div class="invoice-actions">
      <button type="button" class="btn btn-primary" data-testid="invoice-load-day" :disabled="!day || !timezone || busy" @click="loadDay()">{{ t('reports.daily.invoices.load') }}</button>
      <button v-if="listOffset > 0" type="button" class="btn btn-secondary" :disabled="busy" @click="loadDay(listOffset - pageSize)">{{ t('reports.daily.previousPage') }}</button>
      <button v-if="list?.length === pageSize" type="button" class="btn btn-secondary" :disabled="busy" @click="loadDay(listOffset + pageSize)">{{ t('reports.daily.nextPage') }}</button>
    </div>
    <p v-if="busy" role="status">{{ t('reports.daily.loading') }}</p>
    <p v-if="errorText" role="alert">{{ errorText }}</p>
    <p v-if="list?.length === 0" role="status">{{ t('reports.daily.invoices.noInvoices') }}</p>
    <ul v-if="list?.length" class="invoice-list">
      <li v-for="invoice in list" :key="invoice.invoice_id">
        <button type="button" class="btn btn-secondary" :disabled="busy" :aria-pressed="report?.invoice.invoice_id === invoice.invoice_id" @click="loadInvoice(invoice)">{{ invoice.invoice_ref }}</button>
        <span>{{ t('reports.daily.invoices.account', { provider: invoice.provider_id, account: invoice.account_ref }) }} · {{ invoice.stated_total }} {{ invoice.currency }}</span>
      </li>
    </ul>

    <template v-if="report">
      <p class="invoice-period">{{ report.invoice.invoice_ref }} · {{ report.invoice.currency }} · {{ t('reports.daily.window', { start: report.invoice.window_start, end: report.invoice.window_end, zone: timezone }) }} · {{ t('reports.daily.readAt', { at: report.read_at }) }} · {{ report.version }}</p>
      <dl class="invoice-metrics">
        <div v-for="field in metrics" :key="field"><dt>{{ label(field) }}</dt><dd>{{ value(field, report.summary[field]) }}</dd></div>
      </dl>
      <div class="invoice-table" role="region" tabindex="0" :aria-label="t('reports.daily.details')">
        <table>
          <thead><tr><th v-for="column in columns" :key="column" scope="col">{{ label(column) }}</th></tr></thead>
          <tbody><tr v-for="line in report.lines" :key="String(line.line_ref)">
            <td v-for="column in columns" :key="column">{{ value(column, line[column]) }}</td>
          </tr></tbody>
        </table>
      </div>
      <div class="invoice-actions">
        <button v-if="report.offset > 0" type="button" class="btn btn-secondary" :disabled="busy" @click="loadInvoice(report.invoice, Math.max(0, report.offset - report.limit))">{{ t('reports.daily.previousPage') }}</button>
        <button v-if="report.offset + report.lines.length < Number(report.summary.invoice_lines)" type="button" class="btn btn-secondary" :disabled="busy" @click="loadInvoice(report.invoice, report.offset + report.limit)">{{ t('reports.daily.nextPage') }}</button>
        <button type="button" class="btn btn-secondary" :disabled="busy" @click="download('csv-summary')">{{ t('reports.daily.exportSummary') }}</button>
        <button type="button" class="btn btn-secondary" :disabled="busy || !report.lines.length" @click="download('csv')">{{ t('reports.daily.exportPage') }}</button>
      </div>
    </template>
  </div>
</template>

<style scoped>
.invoices { min-inline-size: 0; display: grid; gap: var(--space-3); }
.invoice-actions, .invoice-list li { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); }
.invoice-list { display: grid; gap: var(--space-2); padding: 0; margin: 0; list-style: none; }
.invoice-list :is(button, span), .invoice-period { overflow-wrap: anywhere; }
.invoice-metrics { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 10rem), 1fr)); gap: var(--space-3); margin: 0; }
.invoice-metrics dt { color: var(--text-secondary); }
.invoice-metrics dd { margin: 0; font-weight: 600; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.invoice-table { min-inline-size: 0; overflow-x: auto; }
.invoice-table table { width: 100%; border-collapse: collapse; }
.invoice-table th, .invoice-table td { padding: var(--space-2); border-block-end: 1px solid var(--border); text-align: start; white-space: nowrap; }
</style>
