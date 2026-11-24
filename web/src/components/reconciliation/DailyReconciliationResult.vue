<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { EvidenceFields, DailyReport } from '../../api/requestReconciliation'
import { dailyFields } from './dailyFields'

const props = defineProps<{ report: DailyReport }>()
const { t, locale } = useI18n()
const fields = computed(() => dailyFields[props.report.section])
const integers = computed(() => new Intl.NumberFormat(locale.value))
const labels = (field: string) => t(`reports.daily.fields.${field}`)
const enums = new Set(['status', 'traffic_class', 'delivery', 'outcome', 'bill_comparison', 'entry_type', 'reported_semantics', 'estimate_basis'])
const amounts = computed<(EvidenceFields & { strength: string })[]>(() => [
  ['contract_estimated_amount', props.report.summary.estimates_by_traffic_and_currency],
  ['reported_amount', props.report.summary.reported_by_traffic_currency_and_semantics],
].flatMap(([strength, rows]) => Array.isArray(rows) ? rows.map((row: EvidenceFields) => ({
  ...row, strength: strength === 'contract_estimated_amount' && row.estimate_basis !== 'supplier_contract' ? 'legacy_estimated_amount' : String(strength),
})) : []))

function value(v: unknown, field: string): string {
  if (v == null || v === '') return '—'
  if (typeof v === 'number') return Number.isSafeInteger(v) ? integers.value.format(v) : t('reports.daily.exactInCSV')
  if (typeof v === 'boolean') return t(v ? 'reports.daily.yes' : 'reports.daily.no')
  if (typeof v === 'string') return enums.has(field) ? t(`reports.daily.values.${v}`, v) : v
  return '—'
}
</script>

<template>
  <div class="daily-result">
    <p class="daily-meta">
      {{ t('reports.daily.window', { start: report.start, end: report.end, zone: report.timezone }) }}
      · {{ t('reports.daily.readAt', { at: report.read_at }) }}
      · {{ report.version }}
    </p>
    <p class="daily-meta">{{ t(`reports.daily.cohorts.${report.section}`) }}</p>
    <dl class="daily-metrics">
      <div v-for="field in fields.summary" :key="field">
        <dt>{{ labels(field) }}</dt><dd>{{ value(report.summary[field], field) }}</dd>
      </div>
    </dl>

    <div v-if="amounts.length" class="daily-table" tabindex="0" role="region" :aria-label="t('reports.daily.amountBreakdown')">
      <table>
        <thead><tr><th scope="col">{{ labels('traffic_class') }}</th><th scope="col">{{ labels('amount_source') }}</th><th scope="col">{{ labels('amount_samples') }}</th><th scope="col">{{ labels('amount') }}</th><th scope="col">{{ labels('reported_semantics') }}</th></tr></thead>
        <tbody>
          <tr v-for="(row, index) in amounts" :key="index">
            <td>{{ value(row.traffic_class, 'traffic_class') }}</td><td>{{ labels(row.strength) }}</td>
            <td>{{ value(row.attempts, 'amount_samples') }}</td><td>{{ value(row.amount, 'amount') }} {{ row.currency }}</td><td>{{ value(row.semantics, 'reported_semantics') }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <p v-if="report.rows.length === 0" role="status">{{ t('reports.noData') }}</p>
    <div v-else class="daily-table" tabindex="0" role="region" :aria-label="t('reports.daily.details')">
      <table>
        <thead><tr><th v-for="field in fields.columns" :key="field" scope="col">{{ labels(field) }}</th></tr></thead>
        <tbody>
          <tr v-for="(row, index) in report.rows" :key="index">
            <td v-for="field in fields.columns" :key="field">{{ value(row[field], field) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.daily-result { display: grid; min-inline-size: 0; gap: var(--space-3); }
.daily-meta { margin: 0; color: var(--text-secondary); overflow-wrap: anywhere; }
.daily-metrics { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 10rem), 1fr)); gap: var(--space-3); margin: 0; }
.daily-metrics dt { color: var(--text-secondary); }
.daily-metrics dd { margin: 0; font-weight: 600; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.daily-table { min-inline-size: 0; overflow-x: auto; }
.daily-table table { width: 100%; border-collapse: collapse; }
.daily-table th, .daily-table td { padding: var(--space-2); border-block-end: 1px solid var(--border); text-align: start; white-space: nowrap; }
</style>
