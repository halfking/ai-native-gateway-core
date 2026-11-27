<script setup lang="ts">
import type { EvidenceFields } from '@/api/dailyReconciliation'
import { getLocale, t } from '@/i18n'

defineProps<{ fields: readonly string[]; values: EvidenceFields }>()

function metric(field: string, value: unknown) {
  if (typeof value === 'string') {
    if (!value) return '—'
    if (['kind', 'match_status', 'traffic_class', 'status', 'projected_status', 'outcome', 'delivery', 'estimate_basis'].includes(field)) {
      const key = `rr.evidence.values.${value}`
      const label = t(key)
      return label === key ? value : label
    }
    return value
  }
  if (typeof value === 'boolean') return t(value ? 'rr.evidence.yes' : 'rr.evidence.no')
  if (typeof value === 'number') {
    return Number.isSafeInteger(value) ? value.toLocaleString(getLocale()) : t('rr.evidence.exactInCsv')
  }
  return '—'
}
</script>

<template>
  <dl class="daily-values">
    <div v-for="field in fields" :key="field">
      <dt>{{ t(`rr.evidence.fields.${field}`) }}</dt><dd>{{ metric(field, values[field]) }}</dd>
    </div>
  </dl>
</template>

<style scoped>
.daily-values { display: grid; gap: var(--app-space-2); }
.daily-values div { display: flex; justify-content: space-between; gap: var(--app-space-2); }
.daily-values dt { flex: 1; min-inline-size: 0; overflow-wrap: anywhere; }
.daily-values dd { max-inline-size: 55%; margin: 0; text-align: end; overflow-wrap: anywhere; font-weight: 600; font-variant-numeric: tabular-nums; }
</style>
