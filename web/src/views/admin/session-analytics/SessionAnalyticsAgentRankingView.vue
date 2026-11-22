<template>
  <!-- ★ L13 E2E 判据收敛：data-testid 是 flow 断言的**唯一真源**（与中文文案解耦）。 -->
  <div class="session-analytics-view" data-testid="sa-page-agent-ranking">
    <PageHeader :title="t('sessionAnalytics.title')" :subtitle="t('sessionAnalytics.nav.agentRanking')">
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
      :title="t('sessionAnalytics.error.agentRankingFailed')"
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
          <option value="sessions">{{ t('sessionAnalytics.sort.sessions') }}</option>
          <option value="users">{{ t('sessionAnalytics.sort.users') }}</option>
          <option value="cost">{{ t('sessionAnalytics.sort.cost') }}</option>
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

    <DataTable
      :loading="loading"
      :empty="!loading && rows.length === 0"
      :empty-text="t('sessionAnalytics.empty.noResultAgentRanking')"
      min-width="960px"
      data-testid="sa-agent-ranking-table"
    >
      <table class="sa-table">
        <thead>
          <tr>
            <th data-testid="sa-agent-ranking-col-rank">{{ t('sessionAnalytics.col.rank') }}</th>
            <th data-testid="sa-agent-ranking-col-agent">
              <button class="sa-sort" type="button" @click="toggleNameSort">
                {{ t('sessionAnalytics.col.agent') }}
                <span v-if="nameSort !== 'none'" class="sa-sort__arrow">
                  {{ nameSort === 'asc' ? '▲' : '▼' }}
                </span>
              </button>
            </th>
            <th data-testid="sa-agent-ranking-col-users">{{ t('sessionAnalytics.col.users') }}</th>
            <th data-testid="sa-agent-ranking-col-sessions">{{ t('sessionAnalytics.col.sessions') }}</th>
            <th data-testid="sa-agent-ranking-col-tasks">{{ t('sessionAnalytics.col.tasks') }}</th>
            <th>{{ t('sessionAnalytics.col.tokens') }}</th>
            <th>{{ t('sessionAnalytics.col.cost') }}</th>
            <th>{{ t('sessionAnalytics.col.avgDuration') }}</th>
            <th>{{ t('sessionAnalytics.col.lastUsed') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="a in sorted" :key="a.agent_name">
            <td>{{ a.rank }}</td>
            <td>{{ a.agent_name }}</td>
            <td>{{ fmt(a.user_count) }}</td>
            <td>{{ fmt(a.session_count) }}</td>
            <td>{{ fmt(a.task_count) }}</td>
            <td>{{ fmt(a.total_tokens) }}</td>
            <td>{{ money(a.total_cost) }}</td>
            <td>{{ t('sessionAnalytics.unit.seconds', { value: fmt(a.avg_duration_seconds) }) }}</td>
            <td>{{ formatTime(a.last_used_at) }}</td>
          </tr>
        </tbody>
      </table>
    </DataTable>
  </div>
</template>

<script setup lang="ts">
// SessionAnalyticsAgentRankingView.vue — 智能体排行页（2026-11-20）
//
// 排序：默认按服务端指标排行；名称列可点击切到本地名称排序（见 useNameSort.ts）。

import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import PageHeader from '../../../components/ui/PageHeader.vue'
import DataTable from '../../../components/ui/DataTable.vue'
import KxDateRangePicker from '../../../components/ui/KxDateRangePicker.vue'
import { rangeFromSpanDays } from '../../../components/ui/kxDatePresets'
import type { KxDateRange, KxDateRangePreset } from '../../../components/ui/kx-date-types'
import SaStateBanner from './components/SaStateBanner.vue'
import SaRangeHint from './components/SaRangeHint.vue'
import { useSessionAnalyticsLoader } from './useSessionAnalytics'
import {
  getAgentRanking,
  type AgentRankingRow,
  type AgentRankingSort,
} from '../../../api/sessionAnalytics'

const { t } = useI18n()
const { loading, error, stale, run } = useSessionAnalyticsLoader()

const range = ref<KxDateRange>(rangeFromSpanDays(7))
const draftRange = ref<KxDateRange>(range.value)

const sortBy = ref<AgentRankingSort>('sessions')
const order = ref<'asc' | 'desc'>('desc')

const rows = ref<AgentRankingRow[]>([])

/**
 * 名称列本地排序：none（默认，保持服务端指标排行）→ asc → desc → none。
 * ★ 用 localeCompare 而不是码点序：码点序会把中文名排得很乱
 *   （「火」排在「商」之后），人根本找不到自己要的人。
 */
const nameSort = ref<'none' | 'asc' | 'desc'>('none')

const sorted = computed<AgentRankingRow[]>(() => {
  if (nameSort.value === 'none') return rows.value
  const factor = nameSort.value === 'asc' ? 1 : -1
  return [...rows.value].sort((a, b) =>
    factor * (a.agent_name || '').localeCompare(b.agent_name || ''),
  )
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

function money(v: number | null | undefined): string {
  if (v === null || v === undefined || Number.isNaN(v)) return '—'
  return Number(v).toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  })
}

function formatTime(value: string): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return d.toLocaleString()
}

async function load(): Promise<void> {
  const res = await run(
    () =>
      getAgentRanking({
        start_date: range.value.start,
        end_date: range.value.end,
        sort_by: sortBy.value,
        order: order.value,
      }),
    'sessionAnalytics.error.agentRankingFailed',
  )
  rows.value = res?.agents ?? []
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
