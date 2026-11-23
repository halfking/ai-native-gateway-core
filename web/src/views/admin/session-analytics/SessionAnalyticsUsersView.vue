<template>
  <!-- ★ L13 E2E 判据收敛：data-testid 是 flow 断言的**唯一真源**（与中文文案解耦）。
       语义约定：`sa-` 前缀 + 页面/区块/列三级。改动约定见
       web/src/views/admin/session-analytics/saTestIds.contract.spec.ts（钉住 flow ↔ 源码）。 -->
  <div class="session-analytics-users-view" data-testid="sa-page-users">
    <PageHeader :title="t('sessionAnalytics.title')" :subtitle="t('nav.item.sessionAnalyticsUsers')">
      <template #actions>
        <button
          class="sa-btn"
          type="button"
          data-testid="sa-refresh-btn"
          :disabled="loading || statsLoading"
          @click="refreshAll"
        >
          {{ t('sessionAnalytics.action.refresh') }}
        </button>
      </template>
    </PageHeader>

    <!-- 会话统计面板 (来自 Dashboard) -->
    <div class="stats-panel-wrapper">
      <div v-if="statsLoading" class="section-loading-overlay" data-testid="stats-panel-loading">
        <div class="loading-spinner"></div>
        <span class="loading-text">{{ t('sessionAnalytics.state.loadingStats') }}</span>
      </div>
      <!-- ★ 2026-11-23 日期范围对齐轮：把本页的时间窗交给面板（受控）⇒
           面板的会话统计与本页的 Top5 卡片**同一个区间**（此前面板自持
           一个 days 滚动窗口，与本页的日历日窗口是两把尺）。 -->
      <SessionStatsPanel
        ref="statsPanelRef"
        :range="range"
        style="margin-bottom: 24px;"
        @update:range="onApplyRange"
      />
    </div>

    <!-- ★ 2026-11-23 审计轮：Top5 四件套（模型/供应商/用户/任务）——
         口径与 /admin/session-analytics 总览页完全一致（同一物化视图族），
         每行可点击直达对应详情列表。 -->
    <section class="top5-section" data-testid="sa-top5-section">
      <div class="section-header">
        <h2 class="section-title">{{ t('sessionAnalytics.top5.title') }}</h2>
      </div>
      <p class="sa-note">{{ t('sessionAnalytics.top5.scopeNote') }}</p>
      <!-- ★ 2026-11-23 审计轮（P1-3）：深链带来的任务过滤必须**可见**。
           「筛了但界面说没筛」与「点了 Top5 任务行跳过来却什么都没发生」同族。 -->
      <div v-if="taskFilter" class="sa-task-filter" data-testid="sa-task-filter">
        <span class="badge badge-blue">{{ t('sessionAnalytics.col.task') }}: {{ taskFilter }}</span>
        <button
          type="button"
          class="sa-link"
          data-testid="sa-task-filter-clear"
          :aria-label="t('sessionAnalytics.action.reset')"
          @click="clearTaskFilter"
        >×</button>
      </div>
      <SaTop5Rankings
        v-model:model-rows="modelRows"
        v-model:provider-rows="providerRows"
        v-model:user-rows="userRows"
        v-model:task-rows="visibleTaskRows"
        :start-date="range.start"
        :end-date="range.end"
        :loading="loading"
        :error="top5Error"
        :dimension-errors="top5DimensionErrors"
        @retry="loadTop5"
      />
    </section>

    <!-- Top10 用户排行 -->
    <div class="top-users-section">
      <div class="section-header">
        <h2 class="section-title">{{ t('sessionAnalytics.topUsersTitle') }}</h2>
        <div class="section-actions">
          <div class="sa-range">
            <span class="sa-range__label" data-testid="sa-range-label">{{ t('sessionAnalytics.range.label') }}</span>
            <KxDateRangePicker v-model="draftRange" :presets="presets" @apply="onApplyRange" />
            <SaRangeHint :start="range.start" :end="range.end" />
          </div>
        </div>
      </div>

      <SaStateBanner
        v-if="error"
        kind="error"
        :title="t('sessionAnalytics.error.userRankingFailed')"
        :reason="error"
        :action-label="t('sessionAnalytics.action.retry')"
        @action="loadTopUsers"
      />

      <SaStateBanner
        v-if="stale"
        kind="stale"
        :title="t('sessionAnalytics.state.staleTitle')"
        :reason="t('sessionAnalytics.state.staleReason')"
        :action-label="t('sessionAnalytics.action.refresh')"
        @action="loadTopUsers"
      />

      <p class="sa-note">{{ t('sessionAnalytics.costNote') }}</p>

      <DataTable
        :loading="loading"
        :empty="!loading && topUsers.length === 0"
        :empty-text="t('sessionAnalytics.empty.noResultUserRanking')"
        min-width="1040px"
        data-testid="sa-users-table"
      >
        <table class="sa-table">
          <thead>
            <tr>
              <th data-testid="sa-users-col-rank">{{ t('sessionAnalytics.col.rank') }}</th>
              <th data-testid="sa-users-col-owner">{{ t('sessionAnalytics.col.owner') }}</th>
              <th data-testid="sa-users-col-email">{{ t('sessionAnalytics.col.email') }}</th>
              <th data-testid="sa-users-col-sessions">{{ t('sessionAnalytics.col.sessions') }}</th>
              <th data-testid="sa-users-col-projects">{{ t('sessionAnalytics.col.projects') }}</th>
              <th>{{ t('sessionAnalytics.col.tasks') }}</th>
              <th>{{ t('sessionAnalytics.col.tokens') }}</th>
              <th>{{ t('sessionAnalytics.col.cost') }}</th>
              <th>{{ t('sessionAnalytics.col.lastActive') }}</th>
              <th>{{ t('sessionAnalytics.action.viewDetail') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="u in topUsers" :key="u.owner">
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
              <td>{{ money(u.total_cost) }}</td>
              <td>{{ formatTime(u.last_active_at) }}</td>
              <td>
                <button class="sa-link" type="button" @click="openDetail(u.owner)">
                  {{ t('sessionAnalytics.action.viewDetail') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </DataTable>

      <div v-if="topUsers.length > 0" class="view-all-link">
        <router-link to="/admin/session-analytics/user-ranking" class="sa-link">
          {{ t('sessionAnalytics.action.viewAllUsers') }} →
        </router-link>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
// SessionAnalyticsUsersView.vue — 会话分析用户页面
// 整合 Dashboard 的"会话与统计"内容 + Top10 用户排行

import { computed, onMounted, ref, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import PageHeader from '../../../components/ui/PageHeader.vue'
import SessionStatsPanel from '../../../components/SessionStatsPanel.vue'
import DataTable from '../../../components/ui/DataTable.vue'
import KxDateRangePicker from '../../../components/ui/KxDateRangePicker.vue'
import { rangeFromSpanDays } from '../../../components/ui/kxDatePresets'
import type { KxDateRange, KxDateRangePreset } from '../../../components/ui/kx-date-types'
import SaStateBanner from './components/SaStateBanner.vue'
import SaRangeHint from './components/SaRangeHint.vue'
import SaTop5Rankings from './components/SaTop5Rankings.vue'
import { useSessionAnalyticsLoader } from './useSessionAnalytics'
import { rangeFromQuery, taskFilterFromQuery } from './saQueryParams'
import { surfaceError } from '../../../i18n/errorSurface'
import {
  getUserRanking,
  getModelRanking,
  getProviderRanking,
  getTaskRanking,
  type UserRankingRow,
  type ModelRankingRow,
  type ProviderRankingRow,
  type TaskRankingRow,
} from '../../../api/sessionAnalytics'
import { formatDateTime } from '../../../utils/datetime'

const { t } = useI18n()
const router = useRouter()
const route = useRoute()
const { loading, error, stale, run } = useSessionAnalyticsLoader()

// Stats panel ref and loading state
const statsPanelRef = ref()
const statsLoading = computed(() => {
  return statsPanelRef.value?.loading?.value ?? false
})

// ★ 2026-11-23 审计轮（P2-5 / P1-3）：本页也吃 URL 上的深链参数：
//   · start_date / end_date —— Top5 卡片点击跳过来时带的就是它们；
//     缺省才回退 7 天窗口（口径见 saQueryParams.ts）。
//   · task —— Top5「任务」行点击跳过来时带的是它（见下方 taskFilter）。
const range = ref<KxDateRange>(rangeFromQuery(route.query) ?? rangeFromSpanDays(7))
const draftRange = ref<KxDateRange>(range.value)
// ★ P1-3：Top5「任务」行点击 ⇒ 跳到本页并带 ?task=（SaTop5Rankings.openTask）。
//   此前本页**连 useRoute 都没有** ⇒ 参数无人消费 ⇒ 点过去「什么也没发生」。
//   ⇒ 这里消费它：无参数时行为与从前**完全一致**（不过滤、不显示 chip）。
const taskFilter = ref(taskFilterFromQuery(route.query))
const topUsers = ref<UserRankingRow[]>([])

const presets = computed<KxDateRangePreset[]>(() => [
  { id: 'sa-7d', labelKey: 'sessionAnalytics.range.preset7', resolve: () => rangeFromSpanDays(7) },
  { id: 'sa-30d', labelKey: 'sessionAnalytics.range.preset30', resolve: () => rangeFromSpanDays(30) },
])

function onApplyRange(newRange: KxDateRange) {
  range.value = newRange
  draftRange.value = newRange
  void loadTopUsers()
  void loadTop5()
}

// ★ 2026-11-23 审计轮：Top5 四件套（模型/供应商/用户/任务）全部取自
//   session-analytics 物化视图 ⇒ 与本族其它页面同一口径、同一新鲜度。
//   四次请求并行；任一失败不影响其它三个维度（各自独立 catch）。
const modelRows = ref<ModelRankingRow[]>([])
const providerRows = ref<ProviderRankingRow[]>([])
const userRows = ref<UserRankingRow[]>([])
const taskRows = ref<TaskRankingRow[]>([])
const top5Error = ref<string | null>(null)

/**
 * 每个维度的失败文案（键 = 维度）。
 * ★ 为什么要**逐维度**记：此前失败维度被写成空数组 ⇒ 卡片显示「无结果」
 *   —— 把「加载失败」伪装成「没有数据」（用户会去查业务，而真相是接口挂了）。
 *   ⇒ 失败维度必须显式呈现为加载失败（由 SaTop5Rankings 的 dimension-errors 渲染）。
 */
const top5DimensionErrors = ref<Partial<Record<'models' | 'providers' | 'users' | 'tasks', string>>>({})

// ★ 2026-11-23 审计轮（P2-2）：四维并发加载**必须有请求序号**，否则旧响应会
//   覆盖新数据（口径漂移：用户刚改了时间窗，页面却显示上一轮的数）。
//   ⇒ 写法沿用 views/admin/UsageTrendExplorer.vue:286-289 的 seriesToken 范式
//   （取号 → await → 比对，不一致就丢弃这次响应）。
let seriesToken = 0

async function loadTop5() {
  const token = ++seriesToken
  const params = {
    start_date: range.value.start,
    end_date: range.value.end,
  }
  const results = await Promise.allSettled([
    getModelRanking({ ...params, sort_by: 'requests', order: 'desc' }),
    getProviderRanking({ ...params, sort_by: 'requests', order: 'desc' }),
    getUserRanking({ ...params, sort_by: 'tokens', order: 'desc', limit: 10, offset: 0 }),
    getTaskRanking({ ...params, sort_by: 'sessions', order: 'desc' }),
  ])
  // ★ 已被更新的一轮取代 ⇒ 丢弃本次响应（不写任何状态）。
  if (token !== seriesToken) return
  const [models, providers, users, tasks] = results
  modelRows.value = models.status === 'fulfilled' ? models.value.models || [] : []
  providerRows.value = providers.status === 'fulfilled' ? providers.value.providers || [] : []
  userRows.value = users.status === 'fulfilled' ? users.value.users || [] : []
  taskRows.value = tasks.status === 'fulfilled' ? tasks.value.tasks || [] : []
  // ★ 失败维度显式记账（不与「无结果」混淆）。
  const dimensionErrors: Partial<Record<'models' | 'providers' | 'users' | 'tasks', string>> = {}
  if (models.status === 'rejected') dimensionErrors.models = surfaceError(models.reason)
  if (providers.status === 'rejected') dimensionErrors.providers = surfaceError(providers.reason)
  if (users.status === 'rejected') dimensionErrors.users = surfaceError(users.reason)
  if (tasks.status === 'rejected') dimensionErrors.tasks = surfaceError(tasks.reason)
  top5DimensionErrors.value = dimensionErrors
  const failed = results.filter((r) => r.status === 'rejected')
  top5Error.value =
    failed.length === results.length && failed.length > 0
      ? surfaceError((failed[0] as PromiseRejectedResult).reason)
      : null
}

/**
 * ★ 2026-11-23 审计轮（P1-3）：任务深链过滤后的 Top5 任务行。
 *   ★ 后端 user-ranking **没有** task 维度过滤（只有 project，见
 *     admin/session_analytics/handlers.go 的 userRankingParams）⇒
 *     不能假装「按 task 筛用户」⇒ 这里只让**任务维度**被定位到那个任务，
 *     用户排行仍是全量（口径不被伪装）。
 */
const visibleTaskRows = computed<TaskRankingRow[]>({
  // getter：命中深链任务 ⇒ 只留那一行；无 task 参数 ⇒ 原样返回（行为与从前完全一致）
  get: () => (taskFilter.value ? taskRows.value.filter((r) => r.task === taskFilter.value) : taskRows.value),
  // setter：组件侧回写仍落回原始行集（v-model 契约不变）
  set: (rows) => {
    taskRows.value = rows
  },
})

/** 清除任务深链过滤：本地状态 + URL（URL 必须同步，否则刷新又把 ?task= 读回来）。 */
function clearTaskFilter() {
  taskFilter.value = ''
  const next = { ...route.query }
  delete next.task
  void router.replace({ path: route.path, query: next })
}

async function loadTopUsers() {
  const result = await run(
    async () =>
      await getUserRanking({
        start_date: range.value.start,
        end_date: range.value.end,
        sort_by: 'tokens',
        order: 'desc',
        limit: 10,
        offset: 0,
      }),
    'sessionAnalytics.error.userRankingFailed',
  )
  if (result) {
    topUsers.value = result.users || []
  }
}

async function refreshAll() {
  // Refresh both panels asynchronously
  await Promise.all([
    loadTopUsers(),
    loadTop5(),
    statsPanelRef.value?.refresh() ?? Promise.resolve(),
  ])
}

function openDetail(owner: string) {
  void router.push(`/admin/session-analytics/user-ranking/${owner}`)
}

function fmt(value: number | null | undefined): string {
  if (value == null) return '0'
  return value.toLocaleString()
}

function money(value: number | null | undefined): string {
  if (value == null) return '$0.00'
  // 内部成本（美元）
  return `$${value.toFixed(2)}`
}

function formatTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  return formatDateTime(iso)
}

onMounted(() => {
  void loadTopUsers()
  void loadTop5()
})
</script>

<style scoped>
.session-analytics-users-view {
  padding: 20px;
  max-width: 100%;
}

.top-users-section {
  background: var(--kx-bg-card, #fff);
  border-radius: 8px;
  padding: 24px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.1);
}

.section-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
  flex-wrap: wrap;
  gap: 16px;
}

.section-title {
  font-size: 18px;
  font-weight: 600;
  margin: 0;
  color: var(--kx-text-primary, #303133);
}

.section-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

.sa-range {
  display: flex;
  align-items: center;
  gap: 8px;
}

.sa-range__label {
  font-size: 14px;
  color: var(--kx-text-secondary, #606266);
  white-space: nowrap;
}

.sa-btn {
  padding: 8px 16px;
  border: 1px solid var(--kx-border-base, #dcdfe6);
  border-radius: 4px;
  background: var(--kx-bg-base, #fff);
  color: var(--kx-text-primary, #303133);
  font-size: 14px;
  cursor: pointer;
  transition: all 0.2s;
}

.sa-btn:hover:not(:disabled) {
  border-color: var(--kx-primary, #409eff);
  color: var(--kx-primary, #409eff);
}

.sa-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.sa-note {
  margin: 0 0 16px 0;
  padding: 12px;
  background: var(--kx-bg-light, #f5f7fa);
  border-radius: 4px;
  font-size: 13px;
  color: var(--kx-text-secondary, #606266);
}

/* ★ 2026-11-23 审计轮（P1-3）：深链带来的任务过滤必须**可见**
   （「筛了但界面说没筛」与「点了 Top5 任务行跳过去却什么都没发生」同族）。 */
.sa-task-filter {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0 0 12px 0;
  font-size: 12px;
}

.sa-table {
  width: 100%;
  border-collapse: collapse;
}

.sa-table th,
.sa-table td {
  padding: 12px 16px;
  text-align: left;
  border-bottom: 1px solid var(--kx-border-lighter, #e4e7ed);
}

.sa-table th {
  background: var(--kx-bg-light, #f5f7fa);
  font-weight: 600;
  font-size: 14px;
  color: var(--kx-text-primary, #303133);
}

.sa-table td {
  font-size: 14px;
  color: var(--kx-text-regular, #606266);
}

.sa-table tbody tr:hover {
  background: var(--kx-bg-lighter, #fafafa);
}

.sa-link {
  color: var(--kx-primary, #409eff);
  background: none;
  border: none;
  padding: 0;
  cursor: pointer;
  text-decoration: none;
  font-size: inherit;
  transition: color 0.2s;
}

.sa-link:hover {
  color: var(--kx-primary-light, #66b1ff);
  text-decoration: underline;
}

.view-all-link {
  margin-top: 16px;
  text-align: center;
  padding-top: 16px;
  border-top: 1px solid var(--kx-border-lighter, #e4e7ed);
}

.view-all-link .sa-link {
  font-size: 14px;
  font-weight: 500;
}

.stats-panel-wrapper {
  position: relative;
  min-height: 200px;
}

.section-loading-overlay {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(255, 255, 255, 0.9);
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  z-index: 10;
  border-radius: 8px;
}

.loading-spinner {
  width: 24px;
  height: 24px;
  border: 3px solid var(--kx-border-base, #dcdfe6);
  border-top-color: var(--kx-primary, #409eff);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

.loading-text {
  font-size: 14px;
  color: var(--kx-text-secondary, #606266);
}
</style>
