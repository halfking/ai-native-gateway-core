<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import TrendLineChart from './analytics/TrendLineChart.vue'
import type { BoardTrendPoint } from '../api/board'
import type { ProviderUsageRow, ProviderUsageSummary, ProviderModelUsage, ProviderDailyModelUsage, UsageTrendPeriod } from '../api/usage'
import {
  getUsageByProvider,
  getProviderUsageSummary,
  getProviderUsageTrend,
  getProviderUsageModels,
  getProviderDailyModels,
  downloadProviderUsageExport,
  downloadProviderDetailExport,
} from '../api/usage'

const props = defineProps<{
  open: boolean
  days: number
}>()

const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()

const loading = ref(false)
const rows = ref<ProviderUsageRow[]>([])
const search = ref('')
const selected = ref<ProviderUsageRow | null>(null)
const summary = ref<ProviderUsageSummary | null>(null)
const trendPeriod = ref<UsageTrendPeriod>('day')
const trendData = ref<BoardTrendPoint[]>([])
const models = ref<ProviderModelUsage[]>([])
const dailyModels = ref<ProviderDailyModelUsage[]>([])
const detailLoading = ref(false)
const exportLoading = ref(false)

const filteredRows = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return rows.value
  return rows.value.filter((r) =>
    r.provider_name.toLowerCase().includes(q) ||
    r.provider_code.toLowerCase().includes(q) ||
    String(r.provider_id).includes(q),
  )
})

function fmtNum(n: number | undefined) {
  if (n == null) return '—'
  return n.toLocaleString()
}

function fmtCost(n: number | undefined) {
  if (n == null) return '—'
  return '$' + n.toFixed(4)
}

function fmtPct(n: number | undefined) {
  if (n == null) return '—'
  return (n * 100).toFixed(1) + '%'
}

async function loadList() {
  loading.value = true
  try {
    rows.value = await getUsageByProvider(props.days, 200)
  } finally {
    loading.value = false
  }
}

async function loadDetail(row: ProviderUsageRow) {
  selected.value = row
  detailLoading.value = true
  try {
    const [s, trend, m, daily] = await Promise.all([
      getProviderUsageSummary(row.provider_id, props.days),
      getProviderUsageTrend(row.provider_id, trendPeriod.value, props.days),
      getProviderUsageModels(row.provider_id, props.days),
      getProviderDailyModels(row.provider_id, props.days),
    ])
    summary.value = s
    trendData.value = trend.map((p) => ({
      bucket: p.period,
      requests: p.requests,
      tokens: p.total_tokens,
      credits: 0,
      cost_usd: p.cost_usd,
    }))
    models.value = m
    dailyModels.value = daily
  } finally {
    detailLoading.value = false
  }
}

async function onTrendPeriodChange(period: UsageTrendPeriod) {
  trendPeriod.value = period
  if (!selected.value) return
  detailLoading.value = true
  try {
    const trend = await getProviderUsageTrend(selected.value.provider_id, period, props.days)
    trendData.value = trend.map((p) => ({
      bucket: p.period,
      requests: p.requests,
      tokens: p.total_tokens,
      credits: 0,
      cost_usd: p.cost_usd,
    }))
  } finally {
    detailLoading.value = false
  }
}

function backToList() {
  selected.value = null
  summary.value = null
}

async function exportAll() {
  exportLoading.value = true
  try {
    await downloadProviderUsageExport(props.days)
  } finally {
    exportLoading.value = false
  }
}

async function exportDetail() {
  if (!selected.value) return
  exportLoading.value = true
  try {
    await downloadProviderDetailExport(selected.value.provider_id, props.days)
  } finally {
    exportLoading.value = false
  }
}

watch(() => props.open, (v) => {
  if (v) {
    selected.value = null
    search.value = ''
    void loadList()
  }
})

watch(() => props.days, () => {
  if (props.open) void loadList()
})
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="pue-overlay" @click.self="emit('close')">
      <div class="pue-dialog" role="dialog" :aria-label="t('dashboard.providerUsage.title')">
        <header class="pue-header">
          <div>
            <h3 class="pue-title">{{ t('dashboard.providerUsage.title') }}</h3>
            <p class="pue-sub">{{ t('dashboard.providerUsage.subtitle', { days }) }}</p>
          </div>
          <div class="pue-header-actions">
            <button type="button" class="btn-secondary" :disabled="exportLoading" @click="exportAll">
              {{ t('dashboard.providerUsage.exportAll') }}
            </button>
            <button type="button" class="pue-close" aria-label="关闭" @click="emit('close')">×</button>
          </div>
        </header>

        <template v-if="!selected">
          <div class="pue-toolbar">
            <input v-model="search" class="pue-search" :placeholder="t('dashboard.providerUsage.search')" />
          </div>
          <div v-loading="loading" class="pue-body">
            <table v-if="filteredRows.length" class="pue-table">
              <thead>
                <tr>
                  <th>{{ t('dashboard.providerUsage.colName') }}</th>
                  <th>{{ t('dashboard.providerUsage.colCode') }}</th>
                  <th>{{ t('dashboard.providerUsage.colRequests') }}</th>
                  <th>{{ t('dashboard.providerUsage.colTokens') }}</th>
                  <th>{{ t('dashboard.providerUsage.colCost') }}</th>
                  <th>{{ t('dashboard.providerUsage.colSuccess') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in filteredRows" :key="row.provider_id" class="clickable" @click="loadDetail(row)">
                  <td>{{ row.provider_name }}</td>
                  <td>{{ row.provider_code }}</td>
                  <td>{{ fmtNum(row.request_count) }}</td>
                  <td>{{ fmtNum(row.prompt_tokens + row.completion_tokens) }}</td>
                  <td>{{ fmtCost(row.total_cost_usd) }}</td>
                  <td>{{ fmtPct(row.success_rate) }}</td>
                </tr>
              </tbody>
            </table>
            <div v-else-if="!loading" class="pue-empty">{{ t('dashboard.board.empty') }}</div>
          </div>
        </template>

        <template v-else>
          <div class="pue-detail">
            <button type="button" class="btn-link" @click="backToList">← {{ t('dashboard.providerUsage.back') }}</button>
            <div class="pue-detail-head">
              <div>
                <h4>{{ selected.provider_name }}</h4>
                <span class="muted">{{ selected.provider_code }} · ID {{ selected.provider_id }}</span>
              </div>
              <button type="button" class="btn-secondary" :disabled="exportLoading" @click="exportDetail">
                {{ t('dashboard.providerUsage.exportDetail') }}
              </button>
            </div>

            <div v-if="summary" class="pue-stats">
              <div class="stat"><span>{{ t('dashboard.providerUsage.colRequests') }}</span><strong>{{ fmtNum(summary.request_count) }}</strong></div>
              <div class="stat"><span>{{ t('dashboard.providerUsage.colTokens') }}</span><strong>{{ fmtNum(summary.total_tokens) }}</strong></div>
              <div class="stat"><span>{{ t('dashboard.providerUsage.colCost') }}</span><strong>{{ fmtCost(summary.total_cost_usd) }}</strong></div>
              <div class="stat"><span>{{ t('dashboard.v2.modelCount') }}</span><strong>{{ fmtNum(summary.unique_models) }}</strong></div>
            </div>

            <div class="pue-period-tabs">
              <button type="button" :class="{ active: trendPeriod === 'day' }" @click="onTrendPeriodChange('day')">{{ t('dashboard.providerUsage.periodDay') }}</button>
              <button type="button" :class="{ active: trendPeriod === 'week' }" @click="onTrendPeriodChange('week')">{{ t('dashboard.providerUsage.periodWeek') }}</button>
              <button type="button" :class="{ active: trendPeriod === 'month' }" @click="onTrendPeriodChange('month')">{{ t('dashboard.providerUsage.periodMonth') }}</button>
            </div>
            <TrendLineChart :data="trendData" :loading="detailLoading" />

            <h5>{{ t('dashboard.providerUsage.modelBreakdown') }}</h5>
            <table v-if="models.length" class="pue-table pue-table--compact">
              <thead>
                <tr>
                  <th>{{ t('dashboard.providerUsage.colModel') }}</th>
                  <th>{{ t('dashboard.providerUsage.colRequests') }}</th>
                  <th>{{ t('dashboard.providerUsage.colTokens') }}</th>
                  <th>{{ t('dashboard.providerUsage.colCost') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="m in models" :key="m.model">
                  <td>{{ m.model }}</td>
                  <td>{{ fmtNum(m.request_count) }}</td>
                  <td>{{ fmtNum(m.total_tokens) }}</td>
                  <td>{{ fmtCost(m.cost_usd) }}</td>
                </tr>
              </tbody>
            </table>

            <h5>{{ t('dashboard.providerUsage.dailyBreakdown') }}</h5>
            <div class="pue-daily-scroll">
              <table v-if="dailyModels.length" class="pue-table pue-table--compact">
                <thead>
                  <tr>
                    <th>{{ t('dashboard.providerUsage.colDate') }}</th>
                    <th>{{ t('dashboard.providerUsage.colModel') }}</th>
                    <th>{{ t('dashboard.providerUsage.colRequests') }}</th>
                    <th>{{ t('dashboard.providerUsage.colTokens') }}</th>
                    <th>{{ t('dashboard.providerUsage.colCost') }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="(d, i) in dailyModels" :key="`${d.date}-${d.model}-${i}`">
                    <td>{{ d.date }}</td>
                    <td>{{ d.model }}</td>
                    <td>{{ fmtNum(d.request_count) }}</td>
                    <td>{{ fmtNum(d.total_tokens) }}</td>
                    <td>{{ fmtCost(d.cost_usd) }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </template>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.pue-overlay {
  position: fixed;
  inset: 0;
  z-index: 1400;
  background: rgba(0, 0, 0, 0.55);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px 12px;
}
.pue-dialog {
  width: min(960px, 100%);
  max-height: min(90vh, 900px);
  display: flex;
  flex-direction: column;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  overflow: hidden;
}
.pue-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 12px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border);
}
.pue-title { margin: 0; font-size: 17px; }
.pue-sub { margin: 4px 0 0; font-size: 12px; color: var(--text-muted); }
.pue-header-actions { display: flex; gap: 8px; align-items: center; }
.pue-close { border: 0; background: transparent; font-size: 22px; cursor: pointer; color: var(--text-muted); }
.pue-toolbar { padding: 10px 16px 0; }
.pue-search { width: 100%; padding: 8px 10px; border: 1px solid var(--border); border-radius: 6px; background: var(--bg); color: var(--text); }
.pue-body { flex: 1; overflow: auto; padding: 12px 16px 16px; }
.pue-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.pue-table th, .pue-table td { padding: 8px 10px; border-bottom: 1px solid var(--border); text-align: left; }
.pue-table th { color: var(--text-muted); font-weight: 600; }
.pue-table tr.clickable { cursor: pointer; }
.pue-table tr.clickable:hover { background: color-mix(in srgb, var(--accent) 8%, transparent); }
.pue-table--compact { font-size: 12px; }
.pue-empty { text-align: center; color: var(--text-muted); padding: 40px; }
.pue-detail { flex: 1; overflow: auto; padding: 12px 16px 20px; display: flex; flex-direction: column; gap: 12px; }
.pue-detail-head { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.pue-detail-head h4 { margin: 0; }
.muted { color: var(--text-muted); font-size: 12px; }
.pue-stats { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; }
.stat { border: 1px solid var(--border); border-radius: 8px; padding: 8px 10px; display: flex; flex-direction: column; gap: 4px; font-size: 12px; }
.stat strong { font-size: 16px; }
.pue-period-tabs { display: flex; gap: 6px; }
.pue-period-tabs button {
  font-size: 12px; padding: 4px 10px; border-radius: 6px; border: 1px solid var(--border); background: transparent; color: var(--text); cursor: pointer;
}
.pue-period-tabs button.active { border-color: var(--accent); color: var(--accent); }
.pue-daily-scroll { max-height: 220px; overflow: auto; }
.btn-secondary { font-size: 12px; padding: 6px 10px; border-radius: 6px; border: 1px solid var(--border); background: var(--bg-subtle); cursor: pointer; }
.btn-link { border: 0; background: transparent; color: var(--accent); cursor: pointer; font-size: 13px; padding: 0; }
h5 { margin: 8px 0 4px; font-size: 13px; }
@media (max-width: 720px) {
  .pue-stats { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
</style>
