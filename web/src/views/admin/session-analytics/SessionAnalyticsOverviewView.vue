<template>
  <div class="session-analytics-view">
    <PageHeader :title="t('sessionAnalytics.title')" :subtitle="t('sessionAnalytics.nav.overview')">
      <template #actions>
        <div class="sa-range">
          <span class="sa-range__label">{{ t('sessionAnalytics.range.label') }}</span>
          <KxDateRangePicker v-model="draftRange" :presets="presets" @apply="onApplyRange" />
          <SaRangeHint :start="range.start" :end="range.end" />
        </div>
        <button class="sa-btn" type="button" :disabled="loading" @click="load">
          {{ t('sessionAnalytics.action.refresh') }}
        </button>
      </template>
    </PageHeader>

    <SaStateBanner
      v-if="error"
      kind="error"
      :title="t('sessionAnalytics.error.overviewFailed')"
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

    <div v-if="loading && !summary" class="sa-loading">{{ t('sessionAnalytics.state.loading') }}</div>

    <template v-if="summary">
      <StatsRow :cols="{ mobile: 2, tablet: 2, desktop: 4 }">
        <StatCard
          :label="t('sessionAnalytics.metric.totalSessions')"
          :value="fmt(summary.total_sessions)"
          :sub="growthText(summary.total_sessions_growth)"
          :tone="toneOf(summary.total_sessions_growth)"
        />
        <StatCard
          :label="t('sessionAnalytics.metric.activeUsers')"
          :value="fmt(summary.active_users)"
          :sub="growthText(summary.active_users_growth)"
          :tone="toneOf(summary.active_users_growth)"
        />
        <StatCard
          :label="t('sessionAnalytics.metric.activeProjects')"
          :value="fmt(summary.active_projects)"
        />
        <StatCard
          :label="t('sessionAnalytics.metric.activeTasks')"
          :value="fmt(summary.active_tasks)"
        />
        <StatCard
          :label="t('sessionAnalytics.metric.avgTokens')"
          :value="fmt(summary.avg_tokens)"
        />
        <StatCard
          :label="t('sessionAnalytics.metric.avgTurns')"
          :value="fmt(summary.avg_turns)"
        />
        <StatCard
          :label="t('sessionAnalytics.metric.avgDuration')"
          :value="t('sessionAnalytics.unit.seconds', { value: fmt(summary.avg_duration_seconds) })"
        />
        <StatCard
          :label="t('sessionAnalytics.metric.successRate')"
          :value="t('sessionAnalytics.unit.percent', { value: fmt(summary.overall_success_rate) })"
        />
        <StatCard
          :label="t('sessionAnalytics.metric.cacheRate')"
          :value="t('sessionAnalytics.unit.percent', { value: fmt(summary.overall_cache_rate) })"
        />
        <StatCard
          :label="t('sessionAnalytics.metric.internalCost')"
          :value="t('sessionAnalytics.unit.usd', { value: money(internalCost) })"
        />
        <StatCard
          :label="t('sessionAnalytics.metric.externalCost')"
          :value="t('sessionAnalytics.unit.credits', { value: fmt(externalCost) })"
        />
      </StatsRow>

      <!-- 三个趋势图 -->
      <section class="sa-section">
        <h3 class="sa-section__title">{{ t('sessionAnalytics.trend.sessions') }}</h3>
        <SaTrendChart
          :points="sessionTrend"
          :format="(v: number) => fmt(v)"
          :empty-text="t('sessionAnalytics.empty.noResultOverview')"
        />
      </section>

      <section class="sa-section">
        <h3 class="sa-section__title">{{ t('sessionAnalytics.trend.tokens') }}</h3>
        <SaTrendChart
          :points="tokenTrend"
          color-token="--primary"
          :format="(v: number) => fmt(v)"
          :empty-text="t('sessionAnalytics.empty.noResultOverview')"
        />
      </section>

      <section class="sa-section">
        <h3 class="sa-section__title">{{ t('sessionAnalytics.trend.cost') }}</h3>
        <SaTrendChart
          :points="internalCostTrend"
          color-token="--warning"
          :format="(v: number) => money(v)"
          :empty-text="t('sessionAnalytics.empty.noResultOverview')"
        />
      </section>

      <!-- 三个榜单 -->
      <div class="sa-lists">
        <section class="sa-list">
          <h3 class="sa-section__title">{{ t('sessionAnalytics.section.topModels') }}</h3>
          <DataTable
            :loading="loading"
            :empty="topModels.length === 0"
            :empty-text="t('sessionAnalytics.empty.noResultOverview')"
            min-width="360px"
          >
            <table class="sa-table">
              <thead>
                <tr>
                  <th>{{ t('sessionAnalytics.col.model') }}</th>
                  <th>{{ t('sessionAnalytics.col.requests') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="m in topModels" :key="m.model">
                  <td>
                    <button
                      type="button"
                      class="sa-model-trend-link"
                      data-testid="sa-model-trend-link"
                      :title="t('sessionAnalytics.hint.modelTrend')"
                      @click="openModelTrend(m.model)"
                    >{{ m.model }}</button>
                  </td>
                  <td>{{ fmt(m.count) }}</td>
                </tr>
              </tbody>
            </table>
          </DataTable>
        </section>

        <section class="sa-list">
          <h3 class="sa-section__title">{{ t('sessionAnalytics.section.topUsers') }}</h3>
          <DataTable
            :loading="loading"
            :empty="topUsers.length === 0"
            :empty-text="t('sessionAnalytics.empty.noResultOverview')"
            min-width="520px"
          >
            <table class="sa-table">
              <thead>
                <tr>
                  <th>{{ t('sessionAnalytics.col.owner') }}</th>
                  <th>{{ t('sessionAnalytics.col.sessions') }}</th>
                  <th>{{ t('sessionAnalytics.col.tokens') }}</th>
                  <th>{{ t('sessionAnalytics.col.cost') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="u in topUsers" :key="u.owner">
                  <td>{{ u.display_name || u.owner }}</td>
                  <td>{{ fmt(u.session_count) }}</td>
                  <td>{{ fmt(u.total_tokens) }}</td>
                  <td>{{ money(u.total_cost) }}</td>
                </tr>
              </tbody>
            </table>
          </DataTable>
        </section>

        <section class="sa-list">
          <h3 class="sa-section__title">{{ t('sessionAnalytics.section.topProjects') }}</h3>
          <DataTable
            :loading="loading"
            :empty="topProjects.length === 0"
            :empty-text="t('sessionAnalytics.empty.noResultOverview')"
            min-width="520px"
          >
            <table class="sa-table">
              <thead>
                <tr>
                  <th>{{ t('sessionAnalytics.col.project') }}</th>
                  <th>{{ t('sessionAnalytics.col.sessions') }}</th>
                  <th>{{ t('sessionAnalytics.col.users') }}</th>
                  <th>{{ t('sessionAnalytics.col.cost') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="p in topProjects" :key="p.project">
                  <td>{{ p.project }}</td>
                  <td>{{ fmt(p.session_count) }}</td>
                  <td>{{ fmt(p.user_count) }}</td>
                  <td>{{ money(p.total_cost) }}</td>
                </tr>
              </tbody>
            </table>
          </DataTable>
        </section>
      </div>

      <p class="sa-note">{{ t('sessionAnalytics.costNote') }}</p>
    </template>
  </div>
</template>

<script setup lang="ts">
// SessionAnalyticsOverviewView.vue — 会话分析总览页（2026-11-20）
//
// 口径纪律（这页的每个数字都必须能说清它是什么）：
//   · `total_sessions_growth` 等 growth 字段是**百分比**（后端 calculateGrowth
//     返回 0-100 尺度），不是比率。
//   · `overall_success_rate` / `overall_cache_rate` 同为 0-100 百分比。
//   · 后端的 `total_cost` = internal_cost(美元) + external_cost(积分)，
//     **跨单位相加** ⇒ 页面不展示这个合计数，改成从同源的 cost 趋势里
//     分别求和出「内部成本（美元）」与「外部成本（积分）」两张卡。
//     （缺日不补零 ⇒ 缺日的贡献就是 0，求和与区间总额一致。）
//   · `avg_context_size` 后端是写死的 8192（queries.go:238 的 TODO）⇒ 不展示。

import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import PageHeader from '../../../components/ui/PageHeader.vue'
import StatsRow from '../../../components/ui/StatsRow.vue'
import StatCard from '../../../components/ui/StatCard.vue'
import DataTable from '../../../components/ui/DataTable.vue'
import KxDateRangePicker from '../../../components/ui/KxDateRangePicker.vue'
import { rangeFromSpanDays } from '../../../components/ui/kxDatePresets'
import type { KxDateRange, KxDateRangePreset } from '../../../components/ui/kx-date-types'
import SaTrendChart from './components/SaTrendChart.vue'
import SaStateBanner from './components/SaStateBanner.vue'
import SaRangeHint from './components/SaRangeHint.vue'
import { useSessionAnalyticsLoader } from './useSessionAnalytics'
import {
  getOverview,
  type OverviewTopModel,
  type OverviewTopProject,
  type OverviewTopUser,
  type SessionAnalyticsOverview,
} from '../../../api/sessionAnalytics'

const { t } = useI18n()
const router = useRouter()
const route = useRoute()
const { loading, error, stale, run } = useSessionAnalyticsLoader()

// 2026-11-21 对齐轮：支持 URL query 深链（start_date/end_date）——看板对齐行
// 的「打开会话分析」按钮按同一窗口落地，跨页对拍不用手动重选日期。
function rangeFromQuery(): KxDateRange | null {
  const sd = typeof route.query.start_date === 'string' ? route.query.start_date : ''
  const ed = typeof route.query.end_date === 'string' ? route.query.end_date : ''
  if (/^\d{4}-\d{2}-\d{2}$/.test(sd) && /^\d{4}-\d{2}-\d{2}$/.test(ed) && sd <= ed) {
    return { start: sd, end: ed }
  }
  return null
}
const initialRange = rangeFromQuery()
const range = ref<KxDateRange>(initialRange ?? rangeFromSpanDays(7))
const draftRange = ref<KxDateRange>(range.value)

const data = ref<SessionAnalyticsOverview | null>(null)

const presets = computed<KxDateRangePreset[]>(() => [
  { id: 'sa-7d', labelKey: 'sessionAnalytics.range.preset7', resolve: () => rangeFromSpanDays(7) },
  { id: 'sa-30d', labelKey: 'sessionAnalytics.range.preset30', resolve: () => rangeFromSpanDays(30) },
])

const summary = computed(() => data.value?.summary ?? null)
const topModels = computed<OverviewTopModel[]>(() => data.value?.top_models ?? [])

// 2026-11-21 统计口径对齐轮：top 模型直达 /admin/usage-trends 并自动带上
// 当前日期窗口（usage-trends 深链参数 start/end/model——跨页对拍与钻取）。
function openModelTrend(model: string) {
  void router.push({
    path: '/admin/usage-trends',
    query: { model, start: range.value.start, end: range.value.end },
  })
}
const topUsers = computed<OverviewTopUser[]>(() => data.value?.top_users ?? [])
const topProjects = computed<OverviewTopProject[]>(() => data.value?.top_projects ?? [])

const sessionTrend = computed(() =>
  (data.value?.trends?.sessions ?? []).map((p) => ({ label: p.date, value: p.count })),
)
const tokenTrend = computed(() =>
  (data.value?.trends?.tokens ?? []).map((p) => ({ label: p.date, value: p.total })),
)
const internalCostTrend = computed(() =>
  (data.value?.trends?.cost ?? []).map((p) => ({ label: p.date, value: p.internal })),
)

/** 区间内部成本合计（美元）与外部成本合计（积分）——分别求和，不跨单位相加。 */
const internalCost = computed(() =>
  (data.value?.trends?.cost ?? []).reduce((acc, p) => acc + (p.internal || 0), 0),
)
const externalCost = computed(() =>
  (data.value?.trends?.cost ?? []).reduce((acc, p) => acc + (p.external || 0), 0),
)

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

function growthText(growth: number): string {
  const rounded = Math.round((growth ?? 0) * 10) / 10
  const sign = rounded > 0 ? '+' : ''
  return t('sessionAnalytics.growth', { value: `${sign}${rounded}` })
}

function toneOf(growth: number): 'neutral' | 'success' | 'danger' {
  if (!growth) return 'neutral'
  return growth > 0 ? 'success' : 'danger'
}

async function load(): Promise<void> {
  const res = await run(
    () =>
      getOverview({
        start_date: range.value.start,
        end_date: range.value.end,
      }),
    'sessionAnalytics.error.overviewFailed',
  )
  if (res) data.value = res
}

function onApplyRange(next: KxDateRange): void {
  range.value = next
  void load()
}

function openUser(owner: string): void {
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

.sa-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.sa-loading {
  padding: 32px;
  text-align: center;
  color: var(--muted);
  font-size: 13px;
}

.sa-section {
  margin-top: 24px;
}

.sa-section__title {
  margin: 0 0 10px;
  font-size: 14px;
  color: var(--text);
}

.sa-lists {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
  gap: 20px;
  margin-top: 24px;
}

.sa-table {
  width: 100%;
  border-collapse: collapse;
}

.sa-table th {
  text-align: left;
  color: var(--muted);
  font-weight: 500;
}

.sa-table td {
  color: var(--text);
}

.sa-note {
  margin-top: 20px;
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

/* 2026-11-21 对齐轮：模型名 → usage-trends 深链 */
.sa-model-trend-link {
  background: none;
  border: none;
  padding: 0;
  color: var(--primary);
  cursor: pointer;
  font: inherit;
  text-align: left;
}
.sa-model-trend-link:hover {
  text-decoration: underline;
}
</style>
