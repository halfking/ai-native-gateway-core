<template>
  <!-- ★ L13 E2E 判据收敛：data-testid 是 flow 断言的**唯一真源**（与中文文案解耦）。 -->
  <div class="session-analytics-view" data-testid="sa-page-user-ranking">
    <PageHeader :title="t('sessionAnalytics.title')" :subtitle="t('sessionAnalytics.nav.userRanking')">
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
      :title="t('sessionAnalytics.error.userRankingFailed')"
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

    <div class="sa-filters" data-testid="sa-user-ranking-filters">
      <label class="sa-field">
        <span class="sa-field__label">{{ t('sessionAnalytics.filter.project') }}</span>
        <input
          v-model="projectFilter"
          class="sa-input"
          type="text"
          :placeholder="t('sessionAnalytics.filter.project')"
        />
      </label>
      <label class="sa-field">
        <span class="sa-field__label">{{ t('sessionAnalytics.filter.sortBy') }}</span>
        <select v-model="sortBy" class="sa-select">
          <option value="sessions">{{ t('sessionAnalytics.sort.sessions') }}</option>
          <option value="tokens">{{ t('sessionAnalytics.sort.tokens') }}</option>
          <option value="cost">{{ t('sessionAnalytics.sort.cost') }}</option>
          <option value="last_active">{{ t('sessionAnalytics.sort.lastActive') }}</option>
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
      <button v-if="hasFilters" class="sa-btn" type="button" @click="clearFilters">
        {{ t('sessionAnalytics.action.reset') }}
      </button>
    </div>

    <p class="sa-note">{{ t('sessionAnalytics.costNote') }}</p>

    <DataTable
      :loading="loading"
      :empty="!loading && rows.length === 0"
      :empty-text="emptyText"
      min-width="1040px"
      data-testid="sa-user-ranking-table"
    >
      <table class="sa-table">
        <thead>
          <tr>
            <th data-testid="sa-user-ranking-col-rank">{{ t('sessionAnalytics.col.rank') }}</th>
            <th data-testid="sa-user-ranking-col-owner">
              <button class="sa-sort" type="button" @click="toggleNameSort">
                {{ t('sessionAnalytics.col.owner') }}
                <span v-if="nameSort !== 'none'" class="sa-sort__arrow">
                  {{ nameSort === 'asc' ? '▲' : '▼' }}
                </span>
              </button>
            </th>
            <th data-testid="sa-user-ranking-col-email">{{ t('sessionAnalytics.col.email') }}</th>
            <th data-testid="sa-user-ranking-col-sessions">{{ t('sessionAnalytics.col.sessions') }}</th>
            <th data-testid="sa-user-ranking-col-projects">{{ t('sessionAnalytics.col.projects') }}</th>
            <th>{{ t('sessionAnalytics.col.tasks') }}</th>
            <th>{{ t('sessionAnalytics.col.tokens') }}</th>
            <th>{{ t('sessionAnalytics.col.cost') }}</th>
            <th>{{ t('sessionAnalytics.col.lastActive') }}</th>
            <th>{{ t('sessionAnalytics.col.latestTask') }}</th>
            <th>{{ t('sessionAnalytics.action.viewDetail') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in sortedRows" :key="u.owner">
            <td>{{ u.rank }}</td>
            <td>
              <button class="sa-link" type="button" @click="openDetail(u.owner)">
                {{ u.display_name || u.owner }}
              </button>
            </td>
            <td>{{ u.email || '—' }}</td>
            <td>{{ fmt(u.session_count) }}</td>
            <td>{{ fmt(u.project_count) }}</td>
            <td>{{ fmt(u.task_count) }}</td>
            <td>{{ fmt(u.total_tokens) }}</td>
            <td>
              <SaCostCell :internal-cost="u.internal_cost" :external-cost="u.external_cost" />
            </td>
            <td>{{ formatTime(u.last_active_at) }}</td>
            <td>{{ u.latest_task || '—' }}</td>
            <td>
              <button class="sa-link" type="button" @click="openDetail(u.owner)">
                {{ t('sessionAnalytics.action.viewDetail') }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </DataTable>

    <PaginationBar
      :page="page"
      :page-size="pageSize"
      :total="total"
      @prev="prevPage"
      @next="nextPage"
      @change-size="changePageSize"
    />
  </div>
</template>

<script setup lang="ts">
// SessionAnalyticsUserRankingView.vue — 人员排行页（2026-11-20）
//
// 排序口径：默认按**服务端**的指标排行（sort_by + order）。名称列可点击切换为
// 本地名称排序（升/降）——「有名称的列表按名称排序便于查找」是全站约定，
// 而指标排行是这个页面的主语义，两者用「默认/点击切换」共存而不是互相覆盖。

import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import PageHeader from '../../../components/ui/PageHeader.vue'
import DataTable from '../../../components/ui/DataTable.vue'
import PaginationBar from '../../../components/ui/PaginationBar.vue'
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
  getUserRanking,
  type UserRankingRow,
  type UserRankingSort,
} from '../../../api/sessionAnalytics'

const { t } = useI18n()
const router = useRouter()
const route = useRoute()
const { loading, error, stale, run } = useSessionAnalyticsLoader()

// ★ 2026-11-23 审计轮（P2-5）：**优先读 URL 上的 start_date / end_date**。
//   Top5 排行卡片点击跳过来时带的就是这两个参数（SaTop5Rankings.openUser），
//   而此前本页写死 rangeFromSpanDays(7) ⇒ 用户点的那段区间被**静默忽略**，
//   看到的还是「最近 7 天」⇒ 深链等于没带。
//   ⇒ 缺省 / 非法时才回退 7 天窗口（口径见 saQueryParams.ts）。
const range = ref<KxDateRange>(rangeFromQuery(route.query) ?? rangeFromSpanDays(7))
const draftRange = ref<KxDateRange>(range.value)

const projectFilter = ref('')
const appliedProject = ref('')
const sortBy = ref<UserRankingSort>('tokens')
const order = ref<'asc' | 'desc'>('desc')

const rows = ref<UserRankingRow[]>([])
const total = ref(0)
const pageSize = ref(50)
const offset = ref(0)

const nameSort = ref<'none' | 'asc' | 'desc'>('none')

const presets = computed<KxDateRangePreset[]>(() => [
  { id: 'sa-7d', labelKey: 'sessionAnalytics.range.preset7', resolve: () => rangeFromSpanDays(7) },
  { id: 'sa-30d', labelKey: 'sessionAnalytics.range.preset30', resolve: () => rangeFromSpanDays(30) },
])

const page = computed(() => Math.floor(offset.value / pageSize.value) + 1)
const hasFilters = computed(() => appliedProject.value !== '')

/** 空态文案：区分「确实没有」与「被筛选条件筛掉了」。 */
const emptyText = computed(() =>
  appliedProject.value
    ? t('sessionAnalytics.empty.filteredUserRanking')
    : t('sessionAnalytics.empty.noResultUserRanking'),
)

function nameOf(u: UserRankingRow): string {
  return (u.display_name || u.owner || '').trim()
}

/** 本地名称排序（默认不启用，点了名称列才启用）。 */
const sortedRows = computed<UserRankingRow[]>(() => {
  if (nameSort.value === 'none') return rows.value
  const dir = nameSort.value === 'asc' ? 1 : -1
  return [...rows.value].sort((a, b) => dir * nameOf(a).localeCompare(nameOf(b)))
})

function toggleNameSort(): void {
  nameSort.value = nameSort.value === 'none' ? 'asc' : nameSort.value === 'asc' ? 'desc' : 'none'
}

function fmt(v: number | null | undefined): string {
  if (v === null || v === undefined || Number.isNaN(v)) return '—'
  return Number(v).toLocaleString()
}

// ★ N9：money() 已删除 —— 成本列改走 SaCostCell（两个单位分列）。
//   旧实现把 total_cost（美元 + 积分 的跨单位合计）当成一个美元金额渲染。

/** RFC3339 → 本地可读时间；空串（后端无该行时间戳）显示占位符。 */
function formatTime(value: string): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return d.toLocaleString()
}

async function load(): Promise<void> {
  const res = await run(
    () =>
      getUserRanking({
        start_date: range.value.start,
        end_date: range.value.end,
        project: appliedProject.value || undefined,
        sort_by: sortBy.value,
        order: order.value,
        limit: pageSize.value,
        offset: offset.value,
      }),
    'sessionAnalytics.error.userRankingFailed',
  )
  if (!res) return
  rows.value = res.users ?? []
  total.value = res.total ?? 0
}

function applyFilters(): void {
  appliedProject.value = projectFilter.value.trim()
  offset.value = 0
  void load()
}

function clearFilters(): void {
  projectFilter.value = ''
  appliedProject.value = ''
  sortBy.value = 'tokens'
  order.value = 'desc'
  offset.value = 0
  void load()
}

function prevPage(): void {
  offset.value = Math.max(0, offset.value - pageSize.value)
  void load()
}

function nextPage(): void {
  if (offset.value + pageSize.value >= total.value) return
  offset.value += pageSize.value
  void load()
}

function changePageSize(size: number): void {
  pageSize.value = size
  offset.value = 0
  void load()
}

function onApplyRange(next: KxDateRange): void {
  range.value = next
  offset.value = 0
  void load()
}

function openDetail(owner: string): void {
  void router.push({ name: 'session-analytics-user-detail', params: { owner } })
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

.sa-input,
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

.sa-link {
  padding: 0;
  border: none;
  background: none;
  color: var(--accent);
  font: inherit;
  cursor: pointer;
  text-align: left;
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
