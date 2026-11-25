<template>
  <!-- ★ L13 E2E 判据收敛：data-testid 是 flow 断言的**唯一真源**（与中文文案解耦）。
       语义约定：`sa-` 前缀 + 页面/区块/列三级。改动约定见
       web/src/views/admin/session-analytics/saTestIds.contract.spec.ts（钉住 flow ↔ 源码）。 -->
  <div class="session-analytics-users-view" data-testid="sa-page-users">
    <PageHeader :title="t('sessionAnalytics.title')" :subtitle="t('nav.item.sessionAnalyticsUsers')">
      <template #actions>
        <!-- ★ 2026-11-24 统一轮：时间范围控件与「看板」「会话与统计」页
             **完全同一个组件**（固定档位 + 自定义起止，见
             components/analytics/StatsDateRangePicker.vue）。
             ★ 同时**上移到页头**：本页的时间范围同时管着会话统计面板、Top5 四件套
               与 Top10 排行三块内容，此前控件却埋在第二块（Top10 标题行）里
               ⇒ 上面两块的内容改了区间、控件却在下面，找不到也够不着。 -->
        <div class="sa-range">
          <span class="sa-range__label" data-testid="sa-range-label">{{ t('sessionAnalytics.range.label') }}</span>
          <StatsDateRangePicker v-model="draftRange" @apply="onApplyRange" />
        </div>
        <!-- 「统计说明」提示符：点击才展开（与仪表盘页同一枚、同一份文案）。 -->
        <StatsScopeHint :days="scopeDays" />
        <!-- 「统计标准」抽屉：口径正文（原先由面板提供，现与刷新按钮同处页头）。 -->
        <PageHelpDrawer
          route-path="/admin/session-analytics/users"
          content-id="dashboard-stats-standards"
          :label="t('pageHelp.button.standards')"
        />
        <router-link
          class="sa-btn"
          data-testid="sa-user-profiles-link"
          :to="{ path: '/admin/session-analytics/user-profiles', query: { start_date: range.start, end_date: range.end } }"
        >
          {{ t('sessions.userProfile.title') }}
        </router-link>
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

      <!-- ★ 2026-11-24 统一轮：当前生效区间的文字披露放在页头**下一行**
           （操作区只留控件本身 ⇒ 头部不被长文本撑开；控件显示的可能是草稿值，
           这一行才是**真正生效**的区间）。 -->
      <SaRangeHint :start="range.start" :end="range.end" />
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
        :show-standards-help="false"
        style="margin-bottom: 24px;"
        @update:range="onApplyRange"
      />
    </div>

    <!-- 模型、供应商和任务的请求用量来自请求投影。 -->
    <section class="top5-section" data-testid="sa-top5-section">
      <div class="section-header">
        <h2 class="section-title">{{ t('sessionAnalytics.top5.title') }}</h2>
      </div>
      <p class="sa-note">{{ t('sessionAnalytics.top5.scopeNote') }}</p>
      <p v-if="requestRankingsPending" class="sa-note" role="status">{{ t('reports.projectedPending') }}</p>
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
        <!-- ★ 2026-11-24：时间范围控件已上移到页头（与刷新按钮同一行）——
             它管的是**整页**三块内容，不只是下面这块排行。 -->
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
            <tr v-for="u in topUsers" :key="JSON.stringify([u.tenant_id, u.owner])">
              <td>{{ u.rank }}</td>
              <td>
                <button class="sa-link" type="button" :title="u.tenant_id" @click="openDetail(u)">
                  {{ u.display_name || u.owner }}
                </button>
              </td>
              <td>{{ u.email || '—' }}</td>
              <td>{{ fmt(u.session_count) }}</td>
              <td>{{ fmt(u.project_count) }}</td>
              <td>{{ fmt(u.task_count) }}</td>
              <td>{{ fmt(u.total_tokens) }}</td>
              <td>
                <SaCostCell :internal-cost="u.total_cost" :external-cost="u.credits_charged" projected />
              </td>
              <td>{{ formatTime(u.last_active_at) }}</td>
              <td>
                <button class="sa-link" type="button" @click="openDetail(u)">
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
import StatsDateRangePicker from '../../../components/analytics/StatsDateRangePicker.vue'
import StatsScopeHint from '../../../components/analytics/StatsScopeHint.vue'
import PageHelpDrawer from '../../../components/help/PageHelpDrawer.vue'
import { rangeFromSpanDays, rangeSpanDays } from '../../../components/ui/kxDatePresets'
import type { KxDateRange } from '../../../components/ui/kx-date-types'
import SaStateBanner from './components/SaStateBanner.vue'
import SaRangeHint from './components/SaRangeHint.vue'
import SaTop5Rankings from './components/SaTop5Rankings.vue'
// ★ N9：成本列走 SaCostCell（内部成本美元 / 外部成本积分分列）——
//   total_cost 是**跨单位合计**，渲染成一个带 $ 的数等于凭空造出美元金额。
import SaCostCell from './components/SaCostCell.vue'
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

// ★ 2026-11-24 统一轮：本页不再自持一套预设（原先只有「近 7 天 / 近 30 天」
//   两个档位，且与看板/会话统计页形态不同）⇒ 改用全站统一的
//   StatsDateRangePicker（固定档位 = 今天/昨天/近7/14/30天/本月/上月 + 自定义起止）。

/** 当前生效区间的日历日跨度（含首尾）——「统计说明」正文里的 {days} 用它。 */
const scopeDays = computed(() => Math.max(1, rangeSpanDays(range.value)))

function onApplyRange(newRange: KxDateRange) {
  range.value = newRange
  draftRange.value = newRange
  void loadTopUsers()
  void loadTop5()
}

// 四类排行使用相同的请求投影范围，各自处理加载错误。
const modelRows = ref<ModelRankingRow[]>([])
const providerRows = ref<ProviderRankingRow[]>([])
const userRows = ref<UserRankingRow[]>([])
const taskRows = ref<TaskRankingRow[]>([])
const top5Error = ref<string | null>(null)
const requestRankingsPending = ref(false)

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
  requestRankingsPending.value = (models.status === 'fulfilled' && !!models.value.pending_since) ||
    (providers.status === 'fulfilled' && !!providers.value.pending_since) ||
    (tasks.status === 'fulfilled' && !!tasks.value.pending_since)
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

function openDetail(user: UserRankingRow) {
  void router.push({
    name: 'session-analytics-user-detail',
    params: { owner: user.owner },
    query: { tenant_id: user.tenant_id, start_date: range.value.start, end_date: range.value.end },
  })
}

function fmt(value: number | null | undefined): string {
  if (value == null) return '0'
  return value.toLocaleString()
}

// ★ N9：money() 已删除 —— 成本列改走 SaCostCell（两个单位分列）。
//   旧实现把 total_cost（美元 + 积分 的跨单位合计）渲染成 $12.34，
//   等于凭空造出一个不存在的美元金额，而同页脚注又明说两种单位不能相加。

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
