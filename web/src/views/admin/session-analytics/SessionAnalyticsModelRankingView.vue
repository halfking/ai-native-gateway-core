<template>
  <!-- ★ L13 E2E 判据收敛：data-testid 是 flow 断言的**唯一真源**（与中文文案解耦）。 -->
  <div class="session-analytics-view" data-testid="sa-page-model-ranking">
    <PageHeader :title="t('sessionAnalytics.title')" :subtitle="t('sessionAnalytics.nav.modelRanking')">
      <template #actions>
        <div class="sa-range">
          <span class="sa-range__label" data-testid="sa-range-label">{{ t('sessionAnalytics.range.label') }}</span>
          <KxDateRangePicker v-model="draftRange" :presets="presets" @apply="onApplyRange" />
          <SaRangeHint :start="range.start" :end="range.end" />
        </div>
        <button
          class="sa-btn"
          type="button"
          data-testid="sa-refresh-btn"
          :disabled="loading"
          @click="load"
        >
          {{ t('sessionAnalytics.action.refresh') }}
        </button>
      </template>
    </PageHeader>

    <SaStateBanner
      v-if="error"
      kind="error"
      :title="t('sessionAnalytics.error.modelRankingFailed')"
      :reason="error"
      :action-label="t('sessionAnalytics.action.retry')"
      @action="load"
    />

    <SaStateBanner
      v-if="stale"
      kind="stale"
      :title="t('sessionAnalytics.state.staleTitle')"
      :reason="t('sessionAnalytics.state.staleReason')"
      :action-label="t('sessionAnalytics.action.refresh')"
      @action="load"
    />

    <div class="sa-filters">
      <label class="sa-field">
        <span class="sa-field__label">{{ t('sessionAnalytics.filter.sortBy') }}</span>
        <select v-model="sortBy" class="sa-select">
          <option value="requests">{{ t('sessionAnalytics.sort.requests') }}</option>
          <option value="tokens">{{ t('sessionAnalytics.sort.tokens') }}</option>
          <option value="cost">{{ t('sessionAnalytics.sort.cost') }}</option>
          <option value="success_rate">{{ t('sessionAnalytics.sort.successRate') }}</option>
          <option value="cache_rate">{{ t('sessionAnalytics.sort.cacheRate') }}</option>
          <option value="last_used">{{ t('sessionAnalytics.sort.lastUsed') }}</option>
        </select>
      </label>
      <label class="sa-field">
        <span class="sa-field__label">{{ t('sessionAnalytics.filter.order') }}</span>
        <select v-model="order" class="sa-select">
          <option value="desc">{{ t('sessionAnalytics.order.desc') }}</option>
          <option value="asc">{{ t('sessionAnalytics.order.asc') }}</option>
        </select>
      </label>
      <button class="sa-btn sa-btn--primary" type="button" @click="applyFilters">
        {{ t('sessionAnalytics.filter.apply') }}
      </button>
    </div>

    <p class="sa-note">{{ t('sessionAnalytics.costNote') }}</p>
    <p v-if="projectionPending" class="sa-note" role="status">{{ t('reports.projectedPending') }}</p>
    <p v-if="usdUnmeasured" class="sa-note">{{ t('reports.projectedMissingUSD', { n: usdUnmeasured }) }}</p>

    <DataTable
      :loading="loading"
      :empty="!loading && rows.length === 0"
      :empty-text="t('sessionAnalytics.empty.noResultModelRanking')"
      min-width="1040px"
      data-testid="sa-model-ranking-table"
    >
      <table class="sa-table">
        <thead>
          <tr>
            <th data-testid="sa-model-ranking-col-rank">{{ t('sessionAnalytics.col.rank') }}</th>
            <th data-testid="sa-model-ranking-col-model">
              <button class="sa-sort" type="button" @click="toggleNameSort">
                {{ t('sessionAnalytics.col.model') }}
                <span v-if="nameSort !== 'none'" class="sa-sort__arrow">
                  {{ nameSort === 'asc' ? '▲' : '▼' }}
                </span>
              </button>
            </th>
            <th data-testid="sa-model-ranking-col-provider">{{ t('sessionAnalytics.col.provider') }}</th>
            <th data-testid="sa-model-ranking-col-requests">{{ t('sessionAnalytics.col.requests') }}</th>
            <th data-testid="sa-model-ranking-col-success-rate">{{ t('sessionAnalytics.col.successRate') }}</th>
            <th>{{ t('sessionAnalytics.col.tokens') }}</th>
            <th>{{ t('sessionAnalytics.col.cacheRate') }}</th>
            <th>{{ t('sessionAnalytics.col.avgDuration') }}</th>
            <th>{{ t('dashboard.charts.costUSD') }}</th>
            <th>{{ t('sessionAnalytics.col.users') }}</th>
            <th>{{ t('sessionAnalytics.col.lastUsed') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="m in sorted" :key="`${m.provider}/${m.model}`">
            <td>{{ m.rank }}</td>
            <td>{{ m.model }}</td>
            <td>{{ m.provider || '—' }}</td>
            <td>{{ fmt(m.request_count) }}</td>
            <td>{{ t('sessionAnalytics.unit.percent', { value: fmt(m.success_rate) }) }}</td>
            <td>{{ fmt(m.total_tokens) }}</td>
            <td>{{ t('sessionAnalytics.unit.percent', { value: fmt(m.cache_rate) }) }}</td>
            <td>{{ m.avg_duration_ms == null ? '—' : t('sessionAnalytics.unit.milliseconds', { value: fmt(m.avg_duration_ms) }) }}</td>
            <td>
              <SaCostCell :internal-cost="m.internal_cost" :external-cost="m.external_cost" :terminal-cost="m.usd_unmeasured_requests === undefined ? undefined : m.total_cost" :usage-credits="m.credits_charged" />
            </td>
            <td>{{ fmt(m.user_count) }}</td>
            <td>{{ formatTime(m.last_used_at) }}</td>
          </tr>
        </tbody>
      </table>
    </DataTable>
  </div>
</template>

<script setup lang="ts">
// SessionAnalyticsModelRankingView.vue — 模型排行页（2026-11-20）
//
// 口径：`success_rate` / `cache_rate` 是 0-100 的百分比（后端 SQL 里乘了 100），
// 不是 0-1 的比率；`avg_duration_ms` 是毫秒。

import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import PageHeader from '../../../components/ui/PageHeader.vue'
import DataTable from '../../../components/ui/DataTable.vue'
import KxDateRangePicker from '../../../components/ui/KxDateRangePicker.vue'
import { rangeFromSpanDays } from '../../../components/ui/kxDatePresets'
import type { KxDateRange, KxDateRangePreset } from '../../../components/ui/kx-date-types'
import SaStateBanner from './components/SaStateBanner.vue'
import SaRangeHint from './components/SaRangeHint.vue'
// ★ N9：成本列走 SaCostCell（内部成本美元 / 外部成本积分分列）——
//   total_cost 是**跨单位合计**，渲染成一个带 $ 的数等于凭空造出美元金额。
import SaCostCell from './components/SaCostCell.vue'
import { useSessionAnalyticsLoader } from './useSessionAnalytics'
import { rangeFromQuery } from './saQueryParams'
import {
  getModelRanking,
  type ModelRankingRow,
  type ModelRankingSort,
} from '../../../api/sessionAnalytics'

const { t } = useI18n()
const route = useRoute()
const { loading, error, stale, run } = useSessionAnalyticsLoader()

// ★ 2026-11-23 审计轮（P2-5）：**优先读 URL 上的 start_date / end_date**
//   （Top5 排行卡片点击跳过来时带的就是这两个参数），缺省才回退 7 天窗口。
//   此前本页写死 rangeFromSpanDays(7) ⇒ 深链带的区间被**静默忽略**。
const range = ref<KxDateRange>(rangeFromQuery(route.query) ?? rangeFromSpanDays(7))
const draftRange = ref<KxDateRange>(range.value)

const sortBy = ref<ModelRankingSort>('requests')
const order = ref<'asc' | 'desc'>('desc')

const rows = ref<ModelRankingRow[]>([])
const projectionPending = ref(false)
const usdUnmeasured = computed(() => rows.value.reduce((total, row) => total + (row.usd_unmeasured_requests ?? 0), 0))

/**
 * 名称列本地排序：none（默认，保持服务端指标排行）→ asc → desc → none。
 * ★ 用 localeCompare 而不是码点序：码点序会把中文名排得很乱。
 */
const nameSort = ref<'none' | 'asc' | 'desc'>('none')

const sorted = computed<ModelRankingRow[]>(() => {
  if (nameSort.value === 'none') return rows.value
  const factor = nameSort.value === 'asc' ? 1 : -1
  return [...rows.value].sort((a, b) => factor * (a.model || '').localeCompare(b.model || ''))
})

function toggleNameSort(): void {
  nameSort.value =
    nameSort.value === 'none' ? 'asc' : nameSort.value === 'asc' ? 'desc' : 'none'
}

const presets = computed<KxDateRangePreset[]>(() => [
  { id: 'sa-7d', labelKey: 'sessionAnalytics.range.preset7', resolve: () => rangeFromSpanDays(7) },
  { id: 'sa-30d', labelKey: 'sessionAnalytics.range.preset30', resolve: () => rangeFromSpanDays(30) },
])

function fmt(v: number | null | undefined): string {
  if (v === null || v === undefined || Number.isNaN(v)) return '—'
  return Number(v).toLocaleString()
}

// ★ N9：money() 已删除 —— 成本列改走 SaCostCell（两个单位分列）。
//   旧实现把 total_cost（美元 + 积分 的跨单位合计）当成一个美元金额渲染。

function formatTime(value: string): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return d.toLocaleString()
}

async function load(): Promise<void> {
  const res = await run(
    () =>
      getModelRanking({
        start_date: range.value.start,
        end_date: range.value.end,
        sort_by: sortBy.value,
        order: order.value,
      }),
    'sessionAnalytics.error.modelRankingFailed',
  )
  rows.value = res?.models ?? []
  projectionPending.value = !!res?.pending_since
}

function applyFilters(): void {
  void load()
}

function onApplyRange(next: KxDateRange): void {
  range.value = next
  void load()
}

onMounted(load)
</script>

<style scoped>
.session-analytics-view {
  padding: 20px;
}

.sa-range {
  display: flex;
  align-items: center;
  gap: 8px;
}

.sa-range__label {
  color: var(--muted);
  font-size: 12px;
}

.sa-btn {
  padding: 6px 14px;
  border-radius: var(--radius-card, 8px);
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text);
  font-size: 13px;
  cursor: pointer;
}

.sa-btn--primary {
  border-color: var(--accent);
  background: var(--accent);
  color: var(--surface);
}

.sa-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.sa-filters {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 12px;
  margin-bottom: 12px;
}

.sa-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.sa-field__label {
  color: var(--muted);
  font-size: 12px;
}

.sa-select {
  padding: 6px 10px;
  border-radius: var(--radius-card, 8px);
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text);
  font-size: 13px;
}

.sa-table {
  width: 100%;
  border-collapse: collapse;
}

.sa-table th {
  text-align: left;
  color: var(--muted);
  font-weight: 500;
  white-space: nowrap;
}

.sa-table td {
  color: var(--text);
  white-space: nowrap;
}

.sa-sort {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 0;
  border: none;
  background: none;
  color: inherit;
  font: inherit;
  cursor: pointer;
}

.sa-sort__arrow {
  font-size: 10px;
}

.sa-note {
  margin: 0 0 12px;
  color: var(--muted);
  font-size: 12px;
  line-height: 1.6;
}

@media (max-width: 768px) {
  .session-analytics-view {
    padding: 12px;
  }

  .sa-range {
    flex-direction: column;
    align-items: flex-start;
  }
}
</style>
