<script setup lang="ts">
// MaaSUsageView.vue — /tenant/usage 页面（我的消耗 / 管理员只读消耗视图）。
// 2026-07-12: 文案全面接入 i18n。
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { localeRef } from '../../i18n'
import { RouterLink } from 'vue-router'
import {
  getMaasLedger,
  getAdminMaasLedger,
  getMaasUsageSummary,
  getAdminMaasUsageSummary,
  MAAS_LEDGER_TYPE_LABELS,
} from '../../api'
import type { MaasLedgerEntry, MaasUsageSummary } from '../../api'
import { useMaasTenantContext } from '../../composables/useMaasTenantContext'
import PageBackLink from '../../components/PageBackLink.vue'
import FeeCostCell from '../../components/FeeCostCell.vue'

const { t } = useI18n()

const { tenantLabel, tenantCode, isAdminTenantView, pageTitle: ctxPageTitle, maasBackLink } = useMaasTenantContext()
const pageTitle = computed(() =>
  ctxPageTitle(isAdminTenantView.value ? t('tenants.usage.adminTitle') : t('tenants.usage.title')),
)
const backLink = computed(() => maasBackLink('usage'))

const days = ref(7)
const limit = ref(50)
const summary = ref<MaasUsageSummary | null>(null)
const ledger = ref<MaasLedgerEntry[]>([])
const loading = ref(false)
const error = ref('')

const consumeTotal = computed(() =>
  ledger.value
    .filter((e) => e.entry_type === 'consume')
    .reduce((sum, e) => sum + Math.abs(e.amount), 0),
)

const recentConsumeCount = computed(() =>
  ledger.value.filter((e) => e.entry_type === 'consume').length,
)

const maxModelCredits = computed(() => {
  const rows = summary.value?.by_model ?? []
  return Math.max(1, ...rows.map((r) => r.credits))
})

const maxTrendCredits = computed(() => {
  const rows = summary.value?.trend ?? []
  return Math.max(1, ...rows.map((r) => r.credits))
})

const maxTrendRequests = computed(() => {
  const rows = summary.value?.trend ?? []
  return Math.max(1, ...rows.map((r) => r.requests))
})

const pricingLink = computed(() =>
  isAdminTenantView.value
    ? { path: '/tenant/pricing', query: { tenant: tenantCode.value } }
    : { path: '/tenant/pricing' },
)

function fmtCredits(n: number) {
  const sign = n > 0 ? '+' : ''
  return sign + n.toLocaleString(localeRef.value)
}

function fmtNum(n: number | undefined) {
  if (n === undefined || n === null) return '—'
  return n.toLocaleString(localeRef.value)
}

function fmtTime(s: string) {
  if (!s) return '—'
  return new Date(s).toLocaleString(localeRef.value)
}

function typeLabel(entryType: string) {
  return MAAS_LEDGER_TYPE_LABELS[entryType] || entryType
}

function typeBadgeClass(entryType: string) {
  if (entryType === 'consume') return 'badge-red'
  if (entryType === 'topup') return 'badge-green'
  return 'badge-blue'
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [sum, led] = await Promise.all([
      isAdminTenantView.value
        ? getAdminMaasUsageSummary(tenantCode.value, days.value, 10)
        : getMaasUsageSummary(days.value, 10),
      isAdminTenantView.value
        ? getAdminMaasLedger(tenantCode.value, limit.value)
        : getMaasLedger(limit.value),
    ])
    summary.value = {
      ...sum,
      by_model: sum.by_model ?? [],
      trend: sum.trend ?? [],
    }
    ledger.value = led.items ?? []
  } catch (e: unknown) {
    summary.value = null
    ledger.value = []
    error.value = e instanceof Error ? e.message : t('tenants.usage.loadFailed')
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <PageBackLink v-if="backLink" :to="backLink.to" :label="backLink.label" />
      <h2>{{ pageTitle }}</h2>
      <div class="page-header-actions">
        <span class="tenant-badge tenant-badge--admin">{{ tenantLabel }}</span>
        <select v-model.number="days" class="limit-select" @change="load">
          <option :value="7">{{ t('tenants.usage.days7') }}</option>
          <option :value="30">{{ t('tenants.usage.days30') }}</option>
        </select>
        <select v-model.number="limit" class="limit-select" @change="load">
          <option :value="50">{{ t('tenants.usage.ledgerLimit50') }}</option>
          <option :value="100">{{ t('tenants.usage.ledgerLimit100') }}</option>
          <option :value="200">{{ t('tenants.usage.ledgerLimit200') }}</option>
        </select>
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
          {{ loading ? t('tenants.usage.loading') : t('tenants.usage.refresh') }}
        </button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-else-if="loading && !summary" class="empty">{{ t('tenants.usage.loading') }}</div>

    <div v-if="summary" class="stat-cards">
      <div class="stat-card card">
        <div class="stat-label">{{ t('tenants.usage.statCreditsConsumed') }}</div>
        <div class="stat-value stat-value--fee">
          <FeeCostCell
            inline
            :credits="summary.total_credits"
            :cost-usd="summary.total_cost_usd"
            :show-cost="isAdminTenantView"
          />
        </div>
        <div class="stat-hint">
          {{ t('tenants.usage.statCreditsConsumedHint', { days, n: fmtNum(summary.total_requests) }) }}
        </div>
      </div>
      <div class="stat-card card">
        <div class="stat-label">{{ t('tenants.maasUsageView.ledgerTotalLabel') }}</div>
        <div class="stat-value">{{ fmtNum(consumeTotal) }} <span class="unit">{{ t('common.unit.credits') }}</span></div>
        <div class="stat-hint">
          {{ t('tenants.maasUsageView.ledgerTotalHint', { limit, count: recentConsumeCount }) }}
        </div>
      </div>
    </div>

    <div v-if="summary" class="card chart-card">
      <div class="card-title">
        {{ t('tenants.usage.trendTitle') }}
        <span class="hint">{{ t('tenants.usage.trendCredits') }} · {{ t('tenants.usage.trendRequests') }}</span>
      </div>
      <div v-if="!summary.trend.length" class="empty">
        {{ t('tenants.usage.emptyTrend', { days }) }}
        <RouterLink v-if="!isAdminTenantView" :to="pricingLink">{{ t('tenants.usage.emptyTrendBuyCredits') }}</RouterLink>
        <span v-else>{{ t('tenants.usage.emptyTrendAdminHint') }}</span>
      </div>
      <div v-else class="trend-grid">
        <div class="trend-section">
          <div class="trend-label">{{ t('tenants.usage.trendCredits') }}</div>
          <div class="trend-bars">
            <div
              v-for="row in summary.trend"
              :key="'c-' + row.date"
              class="trend-col"
              :title="`${row.date}: ${row.credits} ${t('common.unit.credits')}`"
            >
              <span
                class="trend-bar credits"
                :style="{ height: (row.credits / maxTrendCredits * 100) + '%' }"
              />
              <span class="trend-date">{{ row.date.slice(5) }}</span>
            </div>
          </div>
        </div>
        <div class="trend-section">
          <div class="trend-label">{{ t('tenants.usage.trendRequests') }}</div>
          <div class="trend-bars">
            <div
              v-for="row in summary.trend"
              :key="'r-' + row.date"
              class="trend-col"
              :title="`${row.date}: ${row.requests} ${t('common.unit.requests')}`"
            >
              <span
                class="trend-bar requests"
                :style="{ height: (row.requests / maxTrendRequests * 100) + '%' }"
              />
              <span class="trend-date">{{ row.date.slice(5) }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>

    <div v-if="summary" class="card chart-card">
      <div class="card-title">
        {{ t('tenants.usage.byModel') }}
        <span class="hint">{{ t('tenants.usage.byModelHint') }}</span>
      </div>
      <div v-if="!summary.by_model.length" class="empty">{{ t('tenants.usage.emptyByModel') }}</div>
      <div v-else class="bar-chart">
        <div v-for="row in summary.by_model" :key="row.model" class="bar-row">
          <span class="bar-label" :title="row.model">{{ row.model }}</span>
          <span class="bar-track">
            <span
              class="bar-fill credits"
              :style="{ width: (row.credits / maxModelCredits * 100) + '%' }"
            />
          </span>
          <span class="bar-meta">
            <FeeCostCell
              inline
              :credits="row.credits"
              :cost-usd="row.cost_usd"
              :show-cost="isAdminTenantView"
            />
            · {{ fmtNum(row.requests) }} {{ t('common.unit.requests') }}
          </span>
        </div>
      </div>
    </div>

    <div class="card table-card">
      <h3 class="table-title">{{ t('tenants.usage.ledgerTitle') }}</h3>
      <table class="table" style="width:100%">
        <thead>
          <tr>
            <th>{{ t('tenants.usage.colTime') }}</th>
            <th>{{ t('tenants.usage.colType') }}</th>
            <th style="text-align:right">{{ t('tenants.usage.colDelta') }}</th>
            <th style="text-align:right">{{ t('tenants.usage.colBalance') }}</th>
            <th>{{ t('tenants.usage.colRef') }}</th>
            <th>{{ t('tenants.usage.colNote') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in ledger" :key="e.id">
            <td class="mono">{{ fmtTime(e.created_at) }}</td>
            <td>
              <span class="badge" :class="typeBadgeClass(e.entry_type)">{{ typeLabel(e.entry_type) }}</span>
            </td>
            <td class="num" :class="{ 'amount-neg': e.amount < 0, 'amount-pos': e.amount > 0 }">
              {{ fmtCredits(e.amount) }}
            </td>
            <td class="num">{{ fmtNum(e.balance_after) }}</td>
            <td class="mono ref-cell">
              <span v-if="e.ref_type">{{ e.ref_type }}</span>
              <span v-if="e.ref_id" class="ref-id">{{ e.ref_id }}</span>
              <span v-if="!e.ref_type && !e.ref_id">—</span>
            </td>
            <td>{{ e.note || '—' }}</td>
          </tr>
          <tr v-if="!loading && ledger.length === 0">
            <td colspan="6" class="empty">
              {{ t('tenants.usage.emptyLedger') }}
              <RouterLink v-if="!isAdminTenantView" :to="pricingLink">{{ t('tenants.usage.goBuyCredits') }}</RouterLink>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.page-header-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.limit-select {
  padding: 4px 8px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  font-size: 13px;
}
.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
}
.stat-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 12px;
  margin-bottom: 20px;
}
.stat-card {
  padding: 16px;
  text-align: center;
}
.stat-label {
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 6px;
}
.stat-value {
  font-size: 26px;
  font-weight: 700;
  color: var(--text);
}
.stat-value--fee :deep(.fee-main) {
  font-size: inherit;
  font-weight: inherit;
}
.stat-value--fee :deep(.fee-cost-sub) {
  font-size: 11px;
  font-weight: 400;
}
.stat-value .unit {
  font-size: 13px;
  font-weight: 500;
  color: var(--muted);
}
.stat-hint {
  font-size: 11px;
  color: var(--muted);
  margin-top: 6px;
}
.chart-card {
  padding: 16px;
  margin-bottom: 16px;
}
.card-title {
  font-size: 14px;
  margin: 0 0 12px;
  color: var(--muted);
}
.card-title .hint {
  font-size: 11px;
  font-weight: 400;
  margin-left: 6px;
}
.trend-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}
.trend-label {
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 8px;
}
.trend-bars {
  display: flex;
  align-items: flex-end;
  gap: 4px;
  min-height: 120px;
}
.trend-col {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  min-width: 0;
}
.trend-bar {
  display: block;
  width: 100%;
  max-width: 28px;
  min-height: 2px;
  border-radius: 3px 3px 0 0;
}
.trend-bar.credits { background: var(--accent); }
.trend-bar.requests { background: var(--success); }
.trend-date {
  font-size: 10px;
  color: var(--muted);
  margin-top: 4px;
}
.bar-chart {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.bar-row {
  display: grid;
  grid-template-columns: minmax(80px, 1fr) auto;
  gap: 8px;
  align-items: center;
}
.bar-label {
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.bar-track {
  grid-column: 1 / -1;
  height: 8px;
  background: var(--border);
  border-radius: 4px;
  overflow: hidden;
}
.bar-fill {
  display: block;
  height: 100%;
  border-radius: 4px;
}
.bar-fill.credits { background: var(--accent); }
.bar-meta {
  font-size: 11px;
  color: var(--muted);
  text-align: right;
}
.table-card {
  padding: 16px;
}
.table-title {
  font-size: 14px;
  margin: 0 0 12px;
  color: var(--muted);
}
.mono {
  font-family: 'SF Mono', 'Fira Code', monospace;
  font-size: 12px;
}
.num {
  text-align: right;
  font-family: 'SF Mono', 'Fira Code', monospace;
  font-size: 13px;
}
.amount-neg { color: var(--danger); }
.amount-pos { color: var(--success); }
.ref-cell {
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ref-id {
  display: block;
  font-size: 11px;
  color: var(--muted);
}
.badge {
  padding: 2px 8px;
  border-radius: 8px;
  font-size: 11px;
}
.badge-red { background: var(--danger-bg); color: var(--danger); }
.badge-green { background: var(--success-bg); color: var(--success); }
.badge-blue { background: var(--info-bg); color: var(--accent); }
.empty {
  text-align: center;
  padding: 40px;
  color: var(--muted);
}
.alert-danger {
  padding: 8px 12px;
  border-radius: 4px;
  background: var(--danger-bg);
  color: var(--danger);
  margin-bottom: 12px;
}
.tenant-badge {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  background: var(--surface-secondary);
  color: var(--text-secondary);
}
.tenant-badge--admin {
  background: var(--info-bg);
  color: var(--accent);
}
@media (max-width: 768px) {
  .trend-grid {
    grid-template-columns: 1fr;
  }
}
</style>