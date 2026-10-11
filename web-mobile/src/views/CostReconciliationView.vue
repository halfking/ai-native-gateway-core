<script setup lang="ts">
// CostReconciliationView — 供应商费用对账。
//
// 2026-10-11（第二百零二批）：三件事一起改。
// 1. 月份选择原为 `<input type="month">`，iOS 同时弹键盘与转盘、半屏被压；
//    改为年份 + 月份两个 `<select>`，iOS 转盘不触发键盘，且月字段支持「全部月份」。
// 2. 「查询」按钮原本嵌在 page body 的 `.page__header`，与顶栏标题同排、独占一行；
//    现在改为顶栏右侧 actions（与 ProvidersView / ConnectionsView 同款），移除页面内那一行。
// 3. `<header class="page__header">` 同时被砍掉，标题由 AppTopbar 单一来源（06 §7）。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { fetchCostReconciliation, type CostReconciliationRecord } from '@/api/costReconciliation'
import { useHyperPage } from '@/hyper'
import { useTopbarAction } from '@/components/shell/topbarActions'
import { t, getLocale } from '@/i18n'
import AppStateView from '@/components/common/AppStateView.vue'
import { formatApiError } from '@/utils/errors'

function currentYearMonth(): { year: number; month: number } {
  const now = new Date()
  return { year: now.getUTCFullYear(), month: now.getUTCMonth() + 1 }
}

// 月份下拉的取值：'all' 表示「全部月份」由前端聚合；其余为 1..12 的字符串形式，
// 拼接成 YYYY-MM 喂给 fetchCostReconciliation。
const MONTH_OPTIONS: Array<{ value: string; labelKey: string }> = [
  { value: 'all', labelKey: 'reconciliation.allMonths' },
  { value: '1', labelKey: 'common.month.jan' },
  { value: '2', labelKey: 'common.month.feb' },
  { value: '3', labelKey: 'common.month.mar' },
  { value: '4', labelKey: 'common.month.apr' },
  { value: '5', labelKey: 'common.month.may' },
  { value: '6', labelKey: 'common.month.jun' },
  { value: '7', labelKey: 'common.month.jul' },
  { value: '8', labelKey: 'common.month.aug' },
  { value: '9', labelKey: 'common.month.sep' },
  { value: '10', labelKey: 'common.month.oct' },
  { value: '11', labelKey: 'common.month.nov' },
  { value: '12', labelKey: 'common.month.dec' },
]

const initial = currentYearMonth()
const year = ref(initial.year)
const monthValue = ref(String(initial.month))

const month = computed(() => {
  if (monthValue.value === 'all') return 'all'
  return `${year.value}-${monthValue.value.padStart(2, '0')}`
})

// 年份范围：当前 ±3 年（覆盖 36 个月缓冲）。年度选择器是可选信息，避免让用户翻日历。
const yearOptions = computed(() => {
  const cy = new Date().getUTCFullYear()
  const list: number[] = []
  for (let y = cy - 3; y <= cy + 1; y += 1) list.push(y)
  return list
})

useHyperPage({ title: () => t('reconciliation.title') })
let generation = 0
let aborter: AbortController | null = null
watch([year, monthValue], () => { ++generation; aborter?.abort(); records.value = []; selected.value = null; error.value = ''; loading.value = false })
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
    // 「全部月份」由前端并发拿 12 个月聚合；monthValue='all' 时 year 只作范围限定。
    if (monthValue.value === 'all') {
      const targets = MONTH_OPTIONS
        .filter((option) => option.value !== 'all')
        .map((option) => `${year.value}-${option.value.padStart(2, '0')}`)
      const settled = await Promise.allSettled(targets.map((m) => fetchCostReconciliation(m, { signal: aborter!.signal })))
      if (current !== generation) return
      const merged: CostReconciliationRecord[] = []
      for (const result of settled) {
        if (result.status === 'fulfilled') merged.push(...result.value.items)
      }
      records.value = merged
      selected.value = null
    } else {
      const response = await fetchCostReconciliation(month.value as string, { signal: aborter.signal })
      if (current !== generation) return
      records.value = response.items
      selected.value = null
    }
  } catch (reason) {
    if (current !== generation) return
    error.value = reason instanceof Error ? formatApiError(reason) : t('reconciliation.loadFailed')
  } finally {
    if (current === generation) loading.value = false
  }
}

// 顶栏右侧「刷新」actions：把原本嵌在 `.page__header` 的查询按钮迁过来。
// 用 disabled 函数把 loading 状态绑到 actions 上，避免组件卸载后按钮还能点。
useTopbarAction({
  key: 'reconciliation-refresh',
  label: () => t('reconciliation.refresh'),
  icon: 'refresh',
  disabled: () => loading.value,
  onClick: () => { void load() },
})

onMounted(() => { void load() })
</script>

<template>
  <div class="view-root reconciliation">
    <div class="reconciliation__filters">
      <label><span>{{ t('reconciliation.monthYear') }}</span><select v-model.number="year" data-testid="reconciliation-year">
        <option v-for="y in yearOptions" :key="y" :value="y">{{ y }}</option>
      </select></label>
      <label><span>{{ t('reconciliation.month') }}</span><select v-model="monthValue" data-testid="reconciliation-month">
        <option v-for="option in MONTH_OPTIONS" :key="option.value" :value="option.value">{{ t(option.labelKey) }}</option>
      </select></label>
    </div>
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
.reconciliation__filters select { min-height: 48px; padding: 0 var(--app-space-2); border: 1px solid var(--app-border); border-radius: var(--app-radius); background: var(--app-surface); color: var(--app-text); font: inherit; }
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
