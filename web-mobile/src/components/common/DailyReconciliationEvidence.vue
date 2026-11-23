<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  dailySections, EvidenceScopeError, fetchDailyEvidence, fetchSupplierDayInvoice, listSupplierDayInvoices,
  type DailyEvidence, type EvidenceFields, type SupplierDayInvoice, type SupplierDayInvoiceEvidence, type SupplierDayInvoices,
} from '@/api/dailyReconciliation'
import { t } from '@/i18n'
import { formatApiError } from '@/utils/errors'
import type { DailyReconciliationCalendar } from '@/api/featureCapabilities'
import { dailyEvidenceLayouts, invoiceEvidenceLayout } from './dailyReconciliationFields'
import DailyReconciliationValues from './DailyReconciliationValues.vue'

const props = defineProps<{ initialDay: string; primaryCalendar?: DailyReconciliationCalendar }>()
const day = ref(props.primaryCalendar?.day || props.initialDay)
const section = ref<(typeof dailySections)[number]>('coverage')
const tenant = ref('')
const tenantId = computed(() => section.value === 'invoices' ? undefined : tenant.value.trim() || undefined)
const report = ref<DailyEvidence | null>(null)
const invoices = ref<SupplierDayInvoices | null>(null)
const selectedInvoice = ref<SupplierDayInvoiceEvidence | null>(null)
const loading = ref(false)
const error = ref('')
const pageSize = 20
let request: AbortController | undefined

const canRead = computed(() => !!day.value && (section.value !== 'internal' || !!tenantId.value))
const page = computed(() => selectedInvoice.value ?? report.value ?? invoices.value)
const layout = computed(() => report.value ? dailyEvidenceLayouts[report.value.section] : invoiceEvidenceLayout)
const detail = computed(() => selectedInvoice.value ?? report.value)
const rows = computed(() => selectedInvoice.value?.lines ?? report.value?.rows ?? [])
const canNext = computed(() => {
  if (!page.value || report.value?.section === 'coverage') return false
  const total = detail.value?.summary[layout.value.count]
  if (typeof total === 'number' && Number.isSafeInteger(total)) {
    return page.value.offset + rows.value.length < total
  }
  return (detail.value ? rows.value.length : invoices.value?.items.length) === page.value.limit
})

watch([day, section, tenantId], () => {
  request?.abort()
  report.value = null
  invoices.value = null
  selectedInvoice.value = null
  error.value = ''
  loading.value = false
}, { flush: 'sync' })
onBeforeUnmount(() => request?.abort())
onMounted(() => { if (props.primaryCalendar) void load() })

function rowTitle(row: EvidenceFields, index: number): string {
  const id = row[layout.value.key]
  if (typeof id === 'string' && id) return id
  if (typeof id === 'number' && Number.isSafeInteger(id)) return String(id)
  return t('rr.evidence.rowNumber', { number: (page.value?.offset ?? 0) + index + 1 })
}

async function run(action: (signal: AbortSignal) => Promise<void>) {
  if (loading.value) return
  const aborter = new AbortController()
  request = aborter
  loading.value = true
  error.value = ''
  try {
    await action(aborter.signal)
  } catch (reason) {
    if (!aborter.signal.aborted) {
      error.value = reason instanceof EvidenceScopeError ? t('rr.evidence.scopeError') : formatApiError(reason)
    }
  } finally {
    if (!aborter.signal.aborted) loading.value = false
  }
}

function load(offset = 0) {
  if (!canRead.value || loading.value) return
  const view = section.value
  const query = {
    day: day.value, offset, limit: pageSize,
    timezone: props.primaryCalendar?.timezone ?? report.value?.timezone ?? invoices.value?.timezone,
  }
  if (view === 'invoices') {
    selectedInvoice.value = null
    return run(async (signal) => {
      const result = await listSupplierDayInvoices(query, { signal })
      if (!signal.aborted) invoices.value = result
    })
  }
  const scoped = { ...query, section: view, tenantId: tenantId.value }
  return run(async (signal) => {
    const result = await fetchDailyEvidence(scoped, { signal })
    if (!signal.aborted) report.value = result
  })
}

function openInvoice(invoice: SupplierDayInvoice, offset = 0) {
  return run(async (signal) => {
    const result = await fetchSupplierDayInvoice(invoice, { limit: pageSize, offset }, { signal })
    if (!signal.aborted) selectedInvoice.value = result
  })
}

function goPage(direction: -1 | 1) {
  if (!page.value) return
  const offset = Math.max(0, page.value.offset + direction * page.value.limit)
  if (selectedInvoice.value) void openInvoice(selectedInvoice.value.invoice, offset)
  else void load(offset)
}

function backToInvoices() {
  selectedInvoice.value = null
  error.value = ''
}
</script>

<template>
  <details class="day-evidence" data-testid="rr-daily-evidence" :open="!!primaryCalendar">
    <summary>{{ t('rr.evidence.title') }}</summary>
    <form class="day-evidence__form" @submit.prevent="load()">
      <label>{{ t('rr.evidence.section') }}
        <select v-model="section" data-testid="rr-evidence-section">
          <option v-for="value in dailySections" :key="value" :value="value">{{ t(`rr.evidence.sections.${value}`) }}</option>
        </select>
      </label>
      <label>{{ t('rr.evidence.date') }}<input v-model="day" type="date" required /></label>
      <label v-if="section !== 'invoices'">{{ t(section === 'internal' ? 'rr.evidence.tenantRequired' : 'rr.evidence.tenantOptional') }}
        <input v-model="tenant" data-testid="rr-evidence-tenant" :required="section === 'internal'" autocomplete="off" />
      </label>
      <button type="submit" :disabled="!canRead || loading">{{ t('rr.evidence.load') }}</button>
    </form>
    <p v-if="loading" role="status">{{ t('common.loading') }}</p>
    <p v-if="error" role="alert" class="day-evidence__error">{{ error }}</p>
    <p v-if="report" class="day-evidence__note">{{ report.timezone }} · {{ report.start }} – {{ report.end }}</p>
    <template v-if="selectedInvoice">
      <button type="button" class="day-evidence__button" data-testid="rr-evidence-invoices-back" :disabled="loading" @click="backToInvoices">{{ t('rr.evidence.backToInvoices') }}</button>
      <p class="day-evidence__note">{{ selectedInvoice.invoice.invoice_ref }} · {{ selectedInvoice.invoice.currency }} · {{ selectedInvoice.invoice.window_start }} – {{ selectedInvoice.invoice.window_end }}</p>
    </template>
    <template v-if="detail">
      <p class="day-evidence__note">{{ t('rr.evidence.readAt', { time: detail.read_at }) }}</p>
      <DailyReconciliationValues :fields="layout.summary" :values="detail.summary" />
      <p v-if="report?.section === 'coverage'" class="day-evidence__note">{{ t('rr.evidence.scope') }}</p>
      <template v-else>
        <p v-if="!rows.length" class="day-evidence__note">{{ t('rr.evidence.noRows') }}</p>
        <ul v-else class="day-evidence__list" :aria-label="t('rr.evidence.rows')">
          <li v-for="(row, index) in rows" :key="`${detail.read_at}-${page?.offset}-${index}`">
            <details>
              <summary class="day-evidence__row">{{ rowTitle(row, index) }}</summary>
              <DailyReconciliationValues :fields="layout.rows" :values="row" />
            </details>
          </li>
        </ul>
      </template>
    </template>
    <template v-else-if="invoices">
      <p class="day-evidence__note">{{ invoices.day }} · {{ invoices.timezone }}</p>
      <p v-if="!invoices.items.length" class="day-evidence__note">{{ t('rr.evidence.noInvoices') }}</p>
      <ul v-else class="day-evidence__list">
        <li v-for="item in invoices.items" :key="item.invoice_id">
          <button type="button" class="day-evidence__button" data-testid="rr-evidence-invoice" :disabled="loading" @click="openInvoice(item)">{{ item.invoice_ref }}</button>
          <span class="day-evidence__note"> {{ item.account_ref }} · {{ item.stated_total }} {{ item.currency }}</span>
        </li>
      </ul>
    </template>
    <div v-if="page && (page.offset > 0 || canNext)" class="day-evidence__pager" :aria-label="t('rr.evidence.pages')">
      <button v-if="page.offset > 0" type="button" class="day-evidence__button" data-testid="rr-evidence-prev" :disabled="loading" @click="goPage(-1)">{{ t('rr.evidence.previous') }}</button>
      <button v-if="canNext" type="button" class="day-evidence__button" data-testid="rr-evidence-next" :disabled="loading" @click="goPage(1)">{{ t('rr.evidence.next') }}</button>
    </div>
  </details>
</template>

<style scoped>
.day-evidence { padding: var(--app-space-3); border: 1px solid var(--app-border); border-radius: var(--app-radius); }
.day-evidence summary { font-weight: 600; cursor: pointer; }
.day-evidence__form { display: flex; flex-wrap: wrap; gap: var(--app-space-2); align-items: end; margin-block-start: var(--app-space-2); }
.day-evidence__form label { display: grid; min-inline-size: 0; gap: var(--app-space-1); }
.day-evidence__form :is(input, select, button), .day-evidence__button { min-height: 48px; max-inline-size: 100%; padding: var(--app-space-2); font: inherit; color: var(--app-text); background: var(--app-surface); border: 1px solid var(--app-border); border-radius: var(--app-radius); }
.day-evidence__list { margin: 0; padding: 0; list-style: none; }
.day-evidence__list li { padding-block: var(--app-space-1); border-block-start: 1px solid var(--app-border); }
.day-evidence__row { min-height: 48px; box-sizing: border-box; align-content: center; overflow-wrap: anywhere; }
.day-evidence__pager { display: flex; flex-wrap: wrap; gap: var(--app-space-2); margin-block-start: var(--app-space-2); }
.day-evidence__note { color: var(--app-text-muted); overflow-wrap: anywhere; }
.day-evidence__error { color: var(--app-danger); }
</style>
