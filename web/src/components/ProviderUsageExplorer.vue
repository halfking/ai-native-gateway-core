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

const isDetailView = computed(() => selected.value != null)

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
  trendPeriod.value = 'day'
  detailLoading.value = true
  try {
    const [s, trend, m, daily] = await Promise.all([
      getProviderUsageSummary(row.provider_id, props.days),
      getProviderUsageTrend(row.provider_id, 'day', props.days),
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
  models.value = []
  dailyModels.value = []
  trendData.value = []
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
    backToList()
    search.value = ''
    void loadList()
  }
})

watch(() => props.days, () => {
  if (props.open) {
    if (selected.value) {
      void loadDetail(selected.value)
    } else {
      void loadList()
    }
  }
})
</script>

<template>
  <Teleport to="body">
    <Transition name="pue-fade">
      <div v-if="open" class="pue-overlay" @click.self="emit('close')">
        <Transition name="pue-slide">
          <aside
            v-if="open"
            class="pue-drawer"
            role="dialog"
            :aria-label="isDetailView ? selected!.provider_name : t('dashboard.providerUsage.title')"
            @click.stop
          >
            <header class="pue-header">
              <div class="pue-header-main">
                <button
                  v-if="isDetailView"
                  type="button"
                  class="pue-back"
                  @click="backToList"
                >
                  ← {{ t('dashboard.providerUsage.back') }}
                </button>
                <div>
                  <h3 class="pue-title">
                    {{ isDetailView ? selected!.provider_name : t('dashboard.providerUsage.title') }}
                  </h3>
                  <p class="pue-sub">
                    <template v-if="isDetailView">
                      <span class="muted">{{ selected!.provider_code }} · ID {{ selected!.provider_id }}</span>
                    </template>
                    <template v-else>
                      {{ t('dashboard.providerUsage.subtitle', { days }) }}
                    </template>
                  </p>
                </div>
              </div>
              <div class="pue-header-actions">
                <button
                  v-if="isDetailView"
                  type="button"
                  class="btn-secondary"
                  :disabled="exportLoading"
                  @click="exportDetail"
                >
                  {{ t('dashboard.providerUsage.exportDetail') }}
                </button>
                <button
                  v-else
                  type="button"
                  class="btn-secondary"
                  :disabled="exportLoading"
                  @click="exportAll"
                >
                  {{ t('dashboard.providerUsage.exportAll') }}
                </button>
                <button type="button" class="pue-close" aria-label="关闭" @click="emit('close')">×</button>
              </div>
            </header>

            <div class="pue-body">
              <Transition name="pue-pane" mode="out-in">
                <div v-if="!isDetailView" key="list" class="pue-pane">
                  <div class="pue-toolbar">
                    <input v-model="search" class="pue-search" :placeholder="t('dashboard.providerUsage.search')" />
                  </div>
                  <div v-loading="loading" class="pue-list">
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
                        <tr
                          v-for="row in filteredRows"
                          :key="row.provider_id"
                          class="clickable"
                          @click="loadDetail(row)"
                        >
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
                </div>

                <div v-else key="detail" class="pue-pane pue-detail">
                  <div v-loading="detailLoading" class="pue-detail-inner">
                    <div v-if="summary" class="pue-stats">
                      <div class="stat">
                        <span>{{ t('dashboard.providerUsage.colRequests') }}</span>
                        <strong>{{ fmtNum(summary.request_count) }}</strong>
                      </div>
                      <div class="stat">
                        <span>{{ t('dashboard.providerUsage.colTokens') }}</span>
                        <strong>{{ fmtNum(summary.total_tokens) }}</strong>
                      </div>
                      <div class="stat">
                        <span>{{ t('dashboard.providerUsage.colCost') }}</span>
                        <strong>{{ fmtCost(summary.total_cost_usd) }}</strong>
                      </div>
                      <div class="stat">
                        <span>{{ t('dashboard.v2.modelCount') }}</span>
                        <strong>{{ fmtNum(summary.unique_models) }}</strong>
                      </div>
                    </div>

                    <div class="pue-period-tabs">
                      <button type="button" :class="{ active: trendPeriod === 'day' }" @click="onTrendPeriodChange('day')">
                        {{ t('dashboard.providerUsage.periodDay') }}
                      </button>
                      <button type="button" :class="{ active: trendPeriod === 'week' }" @click="onTrendPeriodChange('week')">
                        {{ t('dashboard.providerUsage.periodWeek') }}
                      </button>
                      <button type="button" :class="{ active: trendPeriod === 'month' }" @click="onTrendPeriodChange('month')">
                        {{ t('dashboard.providerUsage.periodMonth') }}
                      </button>
                    </div>
                    <TrendLineChart :data="trendData" :loading="detailLoading" />

                    <section class="pue-section">
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
                    </section>

                    <section class="pue-section pue-section--grow">
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
                    </section>
                  </div>
                </div>
              </Transition>
            </div>
          </aside>
        </Transition>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.pue-overlay {
  position: fixed;
  inset: 0;
  z-index: 1400;
  background: rgba(0, 0, 0, 0.55);
  display: flex;
  justify-content: flex-end;
  align-items: stretch;
}

.pue-drawer {
  width: min(1120px, 94vw);
  height: 100%;
  display: flex;
  flex-direction: column;
  background: var(--card);
  border-left: 1px solid var(--border);
  box-shadow: -8px 0 32px rgba(0, 0, 0, 0.35);
}

.pue-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border);
  flex-shrink: 0;
}

.pue-header-main {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}

.pue-back {
  align-self: flex-start;
  border: 0;
  background: transparent;
  color: var(--accent);
  cursor: pointer;
  font-size: 13px;
  padding: 0;
}

.pue-title {
  margin: 0;
  font-size: 18px;
  line-height: 1.3;
}

.pue-sub {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--text-muted);
}

.pue-header-actions {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-shrink: 0;
}

.pue-close {
  width: 32px;
  height: 32px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg);
  font-size: 20px;
  line-height: 1;
  cursor: pointer;
  color: var(--text-muted);
}

.pue-close:hover {
  color: var(--danger, #f85149);
  border-color: var(--danger, #f85149);
}

.pue-body {
  flex: 1;
  min-height: 0;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.pue-pane {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

.pue-toolbar {
  padding: 12px 20px 0;
  flex-shrink: 0;
}

.pue-search {
  width: 100%;
  padding: 9px 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--bg);
  color: var(--text);
}

.pue-list {
  flex: 1;
  overflow: auto;
  padding: 12px 20px 20px;
}

.pue-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.pue-table th,
.pue-table td {
  padding: 10px 12px;
  border-bottom: 1px solid var(--border);
  text-align: left;
}

.pue-table th {
  position: sticky;
  top: 0;
  background: var(--card);
  color: var(--text-muted);
  font-weight: 600;
  z-index: 1;
}

.pue-table tr.clickable {
  cursor: pointer;
}

.pue-table tr.clickable:hover {
  background: color-mix(in srgb, var(--accent) 8%, transparent);
}

.pue-table--compact {
  font-size: 12px;
}

.pue-empty {
  text-align: center;
  color: var(--text-muted);
  padding: 48px 20px;
}

.pue-detail {
  overflow: hidden;
}

.pue-detail-inner {
  flex: 1;
  overflow: auto;
  padding: 16px 20px 24px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.muted {
  color: var(--text-muted);
  font-size: 12px;
}

.pue-stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 10px;
}

.stat {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
}

.stat strong {
  font-size: 17px;
}

.pue-period-tabs {
  display: flex;
  gap: 8px;
}

.pue-period-tabs button {
  font-size: 12px;
  padding: 5px 12px;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: transparent;
  color: var(--text);
  cursor: pointer;
}

.pue-period-tabs button.active {
  border-color: var(--accent);
  color: var(--accent);
  background: color-mix(in srgb, var(--accent) 10%, transparent);
}

.pue-section h5 {
  margin: 0 0 8px;
  font-size: 13px;
}

.pue-section--grow {
  flex: 1;
  min-height: 180px;
  display: flex;
  flex-direction: column;
}

.pue-daily-scroll {
  flex: 1;
  min-height: 160px;
  max-height: 360px;
  overflow: auto;
  border: 1px solid var(--border);
  border-radius: 8px;
}

.pue-daily-scroll .pue-table th {
  background: var(--bg-subtle, var(--card));
}

.btn-secondary {
  font-size: 12px;
  padding: 7px 12px;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: var(--bg-subtle);
  cursor: pointer;
  white-space: nowrap;
}

.pue-fade-enter-active,
.pue-fade-leave-active {
  transition: opacity 0.2s ease;
}

.pue-fade-enter-from,
.pue-fade-leave-to {
  opacity: 0;
}

.pue-slide-enter-active {
  transition: transform 0.28s cubic-bezier(0.22, 1, 0.36, 1);
}

.pue-slide-leave-active {
  transition: transform 0.22s ease-in;
}

.pue-slide-enter-from,
.pue-slide-leave-to {
  transform: translateX(100%);
}

.pue-pane-enter-active,
.pue-pane-leave-active {
  transition: opacity 0.18s ease, transform 0.18s ease;
}

.pue-pane-enter-from {
  opacity: 0;
  transform: translateX(12px);
}

.pue-pane-leave-to {
  opacity: 0;
  transform: translateX(-8px);
}

@media (max-width: 720px) {
  .pue-drawer {
    width: 100vw;
  }

  .pue-stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (prefers-reduced-motion: reduce) {
  .pue-slide-enter-active,
  .pue-slide-leave-active,
  .pue-pane-enter-active,
  .pue-pane-leave-active {
    transition: none;
  }
}
</style>
