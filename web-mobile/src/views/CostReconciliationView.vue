<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { fetchCostReconciliation, type CostReconciliationRecord } from '@/api/costReconciliation'
import { useHyperPage } from '@/hyper'
import { t, getLocale } from '@/i18n'
import AppStateView from '@/components/common/AppStateView.vue'
import { formatApiError } from '@/utils/errors'

function currentMonth(): string {
  const now = new Date()
  return `${now.getUTCFullYear()}-${String(now.getUTCMonth() + 1).padStart(2, '0')}`
}

useHyperPage({ title: () => t('reconciliation.title') })
const month = ref(currentMonth())
let generation = 0
let aborter: AbortController | null = null
watch(month, () => { ++generation; aborter?.abort(); records.value = []; selected.value = null; error.value = ''; loading.value = false })
onBeforeUnmount(() => { ++generation; aborter?.abort() })
const loading = ref(false)
const error = ref('')
const records = ref<CostReconciliationRecord[]>([])
const selected = ref<CostReconciliationRecord | null>(null)
const totalGatewayCost = computed(() => records.value.reduce((sum, record) => sum + record.gateway_total_cost, 0))
const totalProviderCost = computed(() => records.value.reduce((sum, record) => sum + record.provider_total_cost, 0))
const driftCount = computed(() => records.value.filter(isDrift).length)

function hasBill(record: CostReconciliationRecord): boolean {
  return record.data_source !== '' || record.provider_total_tokens !== 0 || record.provider_total_cost !== 0
}

function isDrift(record: CostReconciliationRecord): boolean {
  return hasBill(record) && ((record.provider_total_cost === 0 && record.gateway_total_cost !== 0) || (record.provider_total_tokens === 0 && record.gateway_total_tokens !== 0) || Math.abs(record.cost_diff_rate) > 0.05 || Math.abs(record.token_diff_rate) > 0.05)
}

function money(value: number): string {
  return new Intl.NumberFormat(getLocale(), { style: 'currency', currency: 'USD', maximumFractionDigits: 4 }).format(value)
}

function number(value: number): string { return new Intl.NumberFormat(getLocale()).format(value) }
function percent(value: number): string { return new Intl.NumberFormat(getLocale(), { style: 'percent', maximumFractionDigits: 2 }).format(value) }

async function load(): Promise<void> {
  aborter?.abort()
  aborter = new AbortController()
  const current = ++generation
  records.value = []
  selected.value = null
  loading.value = true
  error.value = ''
  try {
    const response = await fetchCostReconciliation(month.value, { signal: aborter.signal })
    if (current !== generation) return
    records.value = response.items
    selected.value = null
  } catch (reason) {
    if (current !== generation) return
    error.value = reason instanceof Error ? formatApiError(reason) : t('reconciliation.loadFailed')
  } finally {
    if (current === generation) loading.value = false
  }
}

onMounted(() => { void load() })
</script>

<template>
  <div class="view-root reconciliation">
    <header class="page__header">
      <div class="reconciliation__filters">
        <label><span>{{ t('reconciliation.month') }}</span><input v-model="month" type="month" /></label>
        <button type="button" class="btn btn--primary" :disabled="loading" @click="load">{{ loading ? t('common.loading') : t('common.search') }}</button>
      </div>
    </header>
    <p class="reconciliation__scope">{{ t('reconciliation.scope') }}</p>
    <AppStateView v-if="loading && !records.length" :loading="true" :loading-text="t('common.loading')" />
    <AppStateView v-else-if="error && !records.length" :error="error" @retry="load" />
    <template v-else>
      <p v-if="error" class="reconciliation__error" role="alert">{{ error }}</p>
      <section class="reconciliation__summary">
        <article><span>{{ t('reconciliation.gatewayTotal') }}</span><strong>{{ money(totalGatewayCost) }}</strong></article>
        <article><span>{{ t('reconciliation.providerTotal') }}</span><strong>{{ records.some((record) => !hasBill(record)) ? '—' : money(totalProviderCost) }}</strong></article>
        <article><span>{{ t('reconciliation.driftCount') }}</span><strong>{{ number(driftCount) }}</strong></article>
      </section>
      <p v-if="!records.length" class="reconciliation__empty">{{ t('common.empty') }}</p>
      <button v-for="record in records" :key="record.id" type="button" class="reconciliation__card" @click="selected = selected?.id === record.id ? null : record">
        <div class="reconciliation__card-head">
          <strong>{{ t('reconciliation.providerId', { id: record.provider_id }) }}</strong>
          <span v-if="hasBill(record)" class="badge" :class="isDrift(record) ? 'badge--warning' : 'badge--success'">{{ t('reconciliation.costDiff', { rate: record.provider_total_cost === 0 ? '—' : percent(record.cost_diff_rate) }) }}</span>
          <span v-else class="badge badge--muted">{{ t('reconciliation.noBill') }}</span>
        </div>
        <div class="reconciliation__costs"><span>{{ t('reconciliation.gateway') }} {{ money(record.gateway_total_cost) }}</span><span>{{ t('reconciliation.provider') }} {{ hasBill(record) ? money(record.provider_total_cost) : '—' }}</span></div>
        <div v-if="selected?.id === record.id" class="reconciliation__details">
          <dl>
            <div><dt>{{ t('reconciliation.gatewayTokens') }}</dt><dd>{{ number(record.gateway_total_tokens) }}</dd></div>
            <div><dt>{{ t('reconciliation.providerTokens') }}</dt><dd>{{ hasBill(record) ? number(record.provider_total_tokens) : '—' }}</dd></div>
            <div><dt>{{ t('reconciliation.tokenDiff') }}</dt><dd>{{ !hasBill(record) || record.provider_total_tokens === 0 ? '—' : percent(record.token_diff_rate) }}</dd></div>
            <div><dt>{{ t('reconciliation.inputOutput') }}</dt><dd>{{ number(record.gateway_input_tokens) }} / {{ number(record.gateway_output_tokens) }}</dd></div>
            <div><dt>{{ t('reconciliation.source') }}</dt><dd>{{ record.data_source || '—' }}</dd></div>
            <div><dt>{{ t('reconciliation.updated') }}</dt><dd>{{ new Date(record.updated_at).toLocaleString(getLocale()) }}</dd></div>
            <div v-if="record.notes"><dt>{{ t('reconciliation.notes') }}</dt><dd>{{ record.notes }}</dd></div>
          </dl>
        </div>
      </button>
    </template>
  </div>
</template>

<style scoped>
.reconciliation { display: flex; flex-direction: column; gap: var(--app-space-3); }
.reconciliation__filters { display: flex; align-items: end; gap: var(--app-space-2); flex-wrap: wrap; }
.reconciliation__filters label { display: flex; flex-direction: column; gap: var(--app-space-1); color: var(--app-text-muted); font-size: 0.75rem; }
.reconciliation__filters input { min-height: 48px; padding: 0 var(--app-space-2); border: 1px solid var(--app-border); border-radius: var(--app-radius); background: var(--app-surface); color: var(--app-text); font: inherit; }
.reconciliation__scope { margin: 0; color: var(--app-text-muted); font-size: 0.8125rem; }
.reconciliation__summary { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: var(--app-space-2); }
.reconciliation__summary article { display: flex; min-width: 0; flex-direction: column; gap: var(--app-space-2); padding: var(--app-space-3); border: 1px solid var(--app-border-subtle); border-radius: var(--app-radius); background: var(--app-surface); }
.reconciliation__summary span { color: var(--app-text-muted); font-size: 0.75rem; }
.reconciliation__summary strong { overflow-wrap: anywhere; font-size: 0.95rem; }
.reconciliation__card { width: 100%; padding: var(--app-space-3); border: 1px solid var(--app-border); border-radius: var(--app-radius); background: var(--app-surface); color: var(--app-text); text-align: start; }
.reconciliation__card-head, .reconciliation__costs { display: flex; align-items: center; justify-content: space-between; gap: var(--app-space-2); }
.reconciliation__costs { justify-content: flex-start; flex-wrap: wrap; margin-top: var(--app-space-2); color: var(--app-text-secondary); font-size: 0.8125rem; }
.reconciliation__details { margin-top: var(--app-space-3); border-top: 1px solid var(--app-border-subtle); }
.reconciliation__details dl { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--app-space-2); padding-top: var(--app-space-2); margin: 0; }
.reconciliation__details dl div { min-width: 0; }
.reconciliation__details dt { color: var(--app-text-muted); font-size: 0.75rem; }
.reconciliation__details dd { margin: 2px 0 0; overflow-wrap: anywhere; }
.reconciliation__empty { text-align: center; color: var(--app-text-muted); }
.reconciliation__error { color: var(--app-danger); }
@media (max-width: 380px) { .reconciliation__summary { grid-template-columns: 1fr; } }
</style>
