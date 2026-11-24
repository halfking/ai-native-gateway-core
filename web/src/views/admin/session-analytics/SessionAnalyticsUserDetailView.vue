<template>
  <!-- ★ L13 E2E 判据收敛：data-testid 是 flow 断言的**唯一真源**（与中文文案解耦）。 -->
  <div class="session-analytics-view" data-testid="sa-page-user-detail">
    <PageHeader :title="t('sessionAnalytics.title')" :subtitle="t('sessionAnalytics.nav.userDetail')">
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
      :title="t('sessionAnalytics.error.userDetailFailed')"
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

    <div v-if="loading && !detail" class="sa-loading">{{ t('sessionAnalytics.state.loading') }}</div>

    <template v-if="detail">
      <section class="sa-identity" data-testid="sa-user-detail-identity">
        <div class="sa-identity__main">
          <div class="sa-identity__name">{{ detail.user.display_name || detail.user.owner }}</div>
          <div class="sa-identity__meta">
            <span>{{ detail.user.email || '—' }}</span>
            <span v-if="detail.user.tenant_id">{{ detail.user.tenant_id }}</span>
          </div>
        </div>
        <button class="sa-btn" type="button" data-testid="sa-back-btn" @click="goBack">
          {{ t('sessionAnalytics.action.back') }}
        </button>
      </section>

      <!-- ★ N9：成本组由**一张**指标卡拆成**两张**（内部成本美元 / 外部成本积分）——
           summary.total_cost 是两个单位**跨单位相加**的结果，渲染成一个金额等于
           凭空造出一个不存在的美元金额（framework §3.2：两种单位禁止相加）。
           ★ 锚点 sa-metric-total-cost 仍留在「成本组」容器上：它的语义从
             「一个合计金额」变成「成本这一组」（Maestro flow 断的就是它）。 -->
      <StatsRow :cols="{ mobile: 2, tablet: 2, desktop: 5 }">
        <StatCard
          data-testid="sa-metric-session-count"
          :label="t('sessionAnalytics.metric.sessionCount')"
          :value="fmt(detail.summary.session_count)"
        />
        <StatCard
          data-testid="sa-metric-task-count"
          :label="t('sessionAnalytics.metric.taskCount')"
          :value="fmt(detail.summary.task_count)"
        />
        <div class="sa-cost-group" data-testid="sa-metric-total-cost">
          <StatCard
            :label="t('sessionAnalytics.metric.internalCost')"
            :value="internalCostText"
          />
          <StatCard
            :label="t('sessionAnalytics.metric.externalCost')"
            :value="externalCostText"
          />
        </div>
        <StatCard
          data-testid="sa-metric-rank"
          :label="t('sessionAnalytics.metric.rank')"
          :value="fmt(detail.summary.rank)"
          :sub="rankChangeText"
        />
      </StatsRow>

      <p class="sa-note">{{ t('sessionAnalytics.costNote') }}</p>

      <section class="sa-section" data-testid="sa-trend-sessions">
        <h3 class="sa-section__title">{{ t('sessionAnalytics.trend.sessions') }}</h3>
        <SaTrendChart
          :points="sessionTrend"
          :format="(v: number) => fmt(v)"
          :empty-text="t('sessionAnalytics.empty.noResultUserDetail')"
        />
      </section>

      <section class="sa-section" data-testid="sa-trend-tasks">
        <h3 class="sa-section__title">{{ t('sessionAnalytics.trend.tasks') }}</h3>
        <SaTrendChart
          :points="taskTrend"
          color-token="--primary"
          :format="(v: number) => fmt(v)"
          :empty-text="t('sessionAnalytics.empty.noResultUserDetail')"
        />
      </section>

      <section class="sa-section" data-testid="sa-trend-cost">
        <h3 class="sa-section__title">{{ t('sessionAnalytics.trend.cost') }}</h3>
        <SaTrendChart
          :points="costTrend"
          color-token="--warning"
          :format="(v: number) => money(v)"
          :empty-text="t('sessionAnalytics.empty.noResultUserDetail')"
        />
      </section>

      <section class="sa-section" data-testid="sa-trend-rank">
        <h3 class="sa-section__title">{{ t('sessionAnalytics.trend.rank') }}</h3>
        <SaTrendChart
          :points="rankTrend"
          color-token="--success"
          :format="(v: number) => `#${fmt(v)}`"
          :empty-text="t('sessionAnalytics.empty.noResultUserDetail')"
        />
      </section>

      <div class="sa-tabs">
        <button
          class="sa-tab"
          data-testid="sa-tab-projects"
          :class="{ 'sa-tab--active': tab === 'projects' }"
          type="button"
          @click="tab = 'projects'"
        >
          {{ t('sessionAnalytics.section.projects') }}
        </button>
        <button
          class="sa-tab"
          data-testid="sa-tab-tasks"
          :class="{ 'sa-tab--active': tab === 'tasks' }"
          type="button"
          :disabled="!selectedProject"
          @click="tab = 'tasks'"
        >
          {{ t('sessionAnalytics.section.tasks') }}
        </button>
        <button
          class="sa-tab"
          data-testid="sa-tab-models"
          :class="{ 'sa-tab--active': tab === 'models' }"
          type="button"
          @click="tab = 'models'"
        >
          {{ t('sessionAnalytics.section.models') }}
        </button>
        <button
          class="sa-tab"
          data-testid="sa-tab-apikeys"
          :class="{ 'sa-tab--active': tab === 'apiKeys' }"
          type="button"
          @click="tab = 'apiKeys'"
        >
          {{ t('sessionAnalytics.section.apiKeys') }}
        </button>
      </div>

      <!-- ★★ 2026-11-23（L16）：tab 级错误/陈旧态的出口。
           ★ 此前 tab 自己的 loader 报错**没有任何承接** ⇒ 端点 500 被渲染成
           「暂无数据」空态 ⇒ 用户看不出后端挂了。★ 同一时刻只有一个 tab 可见，
           所以**一个共用横幅**即可（不必每个 tab 各写一份）。
           ★ 文案**复用既有键**（`sessionAnalytics.error.*` / `sessionAnalytics.state.stale*`
           / `sessionAnalytics.action.retry|refresh`）⇒ **零新增 i18n 键**。 -->
      <SaStateBanner
        v-if="tabError"
        kind="error"
        :title="t('sessionAnalytics.error.userDetailFailed')"
        :reason="tabError"
        :action-label="t('sessionAnalytics.action.retry')"
        @action="reloadActiveTab"
      />

      <SaStateBanner
        v-if="tabStale"
        kind="stale"
        :title="t('sessionAnalytics.state.staleTitle')"
        :reason="t('sessionAnalytics.state.staleReason')"
        :action-label="t('sessionAnalytics.action.refresh')"
        @action="reloadActiveTab"
      />

      <section v-if="tab === 'projects'" class="sa-section">
        <DataTable
          :loading="projectsLoading"
          :empty="!projectsLoading && !tabError && !tabStale && projects.length === 0"
          :empty-text="t('sessionAnalytics.empty.noResultUserProjects')"
          min-width="720px"
        >
          <table class="sa-table">
            <thead>
              <tr>
                <th>
                  <button class="sa-sort" type="button" @click="toggleProjectSort">
                    {{ t('sessionAnalytics.col.project') }}
                    <span v-if="projectSort !== 'none'" class="sa-sort__arrow">
                      {{ projectSort === 'asc' ? '▲' : '▼' }}
                    </span>
                  </button>
                </th>
                <th>{{ t('sessionAnalytics.col.sessions') }}</th>
                <th>{{ t('sessionAnalytics.col.tasks') }}</th>
                <th>{{ t('sessionAnalytics.col.tokens') }}</th>
                <th>{{ t('sessionAnalytics.col.cost') }}</th>
                <th>{{ t('sessionAnalytics.col.lastActive') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="p in sortedProjects" :key="p.project">
                <td>
                  <button class="sa-link" type="button" @click="selectProject(p.project)">
                    {{ p.project }}
                  </button>
                </td>
                <td>{{ fmt(p.session_count) }}</td>
                <td>{{ fmt(p.task_count) }}</td>
                <td>{{ fmt(p.total_tokens) }}</td>
                <td>
                  <SaCostCell :internal-cost="p.internal_cost" :external-cost="p.external_cost" />
                </td>
                <td>{{ formatTime(p.last_active_at) }}</td>
              </tr>
            </tbody>
          </table>
        </DataTable>
      </section>

      <section v-else-if="tab === 'tasks'" class="sa-section">
        <DataTable
          :loading="tasksLoading"
          :empty="!tasksLoading && !tabError && !tabStale && tasks.length === 0"
          :empty-text="t('sessionAnalytics.empty.noResultProjectTasks')"
          min-width="720px"
        >
          <table class="sa-table">
            <thead>
              <tr>
                <th>
                  <button class="sa-sort" type="button" @click="toggleTaskSort">
                    {{ t('sessionAnalytics.col.task') }}
                    <span v-if="taskSort !== 'none'" class="sa-sort__arrow">
                      {{ taskSort === 'asc' ? '▲' : '▼' }}
                    </span>
                  </button>
                </th>
                <th>{{ t('sessionAnalytics.col.sessions') }}</th>
                <th>{{ t('sessionAnalytics.col.requests') }}</th>
                <th>{{ t('sessionAnalytics.col.tokens') }}</th>
                <th>{{ t('sessionAnalytics.col.cost') }}</th>
                <th>{{ t('sessionAnalytics.col.lastActive') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="task in sortedTasks" :key="task.task">
                <td>{{ task.task }}</td>
                <td>{{ fmt(task.session_count) }}</td>
                <td>{{ fmt(task.request_count) }}</td>
                <td>{{ fmt(task.total_tokens) }}</td>
                <td>
                  <SaCostCell :internal-cost="task.internal_cost" :external-cost="task.external_cost" />
                </td>
                <td>{{ formatTime(task.last_active_at) }}</td>
              </tr>
            </tbody>
          </table>
        </DataTable>
      </section>

      <section v-else-if="tab === 'models'" class="sa-section">
        <DataTable
          :loading="modelsLoading"
          :empty="!modelsLoading && !tabError && !tabStale && models.length === 0"
          :empty-text="t('sessionAnalytics.empty.noResultUserModels')"
          min-width="900px"
          data-testid="sa-user-models-table"
        >
          <table class="sa-table">
            <thead>
              <tr>
                <th data-testid="sa-user-models-col-model">{{ t('sessionAnalytics.col.model') }}</th>
                <th data-testid="sa-user-models-col-requests">{{ t('sessionAnalytics.col.requests') }}</th>
                <th data-testid="sa-user-models-col-tokens">{{ t('sessionAnalytics.col.tokens') }}</th>
                <th data-testid="sa-user-models-col-cost">{{ t('sessionAnalytics.col.cost') }}</th>
                <th data-testid="sa-user-models-col-success-rate">{{ t('sessionAnalytics.col.successRate') }}</th>
                <th data-testid="sa-user-models-col-cache-rate">{{ t('sessionAnalytics.col.cacheRate') }}</th>
                <th data-testid="sa-user-models-col-avg-response-time">{{ t('sessionAnalytics.col.avgResponseTime') }}</th>
                <th>{{ t('sessionAnalytics.col.lastUsed') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="m in models" :key="m.model">
                <td>{{ m.model }}</td>
                <td>{{ fmt(m.request_count) }}</td>
                <td>{{ fmt(m.total_tokens) }}</td>
                <!-- ★ N9：模型用量这一格**有意保持原样**（不拆美元/积分两栏）——
                     该端点（/user-ranking/:owner/models）的 internal/external 语义
                     与会话分析族**相反**（后端已单独登记）⇒ 口径澄清前不贴
                     「美元 / 积分」标签，否则就是把错的单位贴在错的数上。 -->
                <td>{{ money(m.total_cost) }}</td>
                <td>{{ percent(m.success_rate) }}</td>
                <td>{{ percent(m.cache_hit_rate) }}</td>
                <td>{{ fmt(Math.round(m.avg_response_time_ms)) }} ms</td>
                <td>{{ formatTime(m.last_used_at) }}</td>
              </tr>
            </tbody>
          </table>
        </DataTable>
      </section>

      <section v-else-if="tab === 'apiKeys'" class="sa-section">
        <DataTable
          :loading="apiKeysLoading"
          :empty="!apiKeysLoading && !tabError && !tabStale && apiKeys.length === 0"
          :empty-text="t('sessionAnalytics.empty.noResultUserAPIKeys')"
          min-width="900px"
          data-testid="sa-user-apikeys-table"
        >
          <table class="sa-table">
            <thead>
              <tr>
                <th data-testid="sa-user-apikeys-col-alias">{{ t('sessionAnalytics.col.keyAlias') }}</th>
                <th data-testid="sa-user-apikeys-col-key-id">{{ t('sessionAnalytics.col.keyID') }}</th>
                <th data-testid="sa-user-apikeys-col-requests">{{ t('sessionAnalytics.col.requests') }}</th>
                <th data-testid="sa-user-apikeys-col-tokens">{{ t('sessionAnalytics.col.tokens') }}</th>
                <th data-testid="sa-user-apikeys-col-cost">{{ t('sessionAnalytics.col.cost') }}</th>
                <th data-testid="sa-user-apikeys-col-project-count">{{ t('sessionAnalytics.col.projectCount') }}</th>
                <th data-testid="sa-user-apikeys-col-top-project">{{ t('sessionAnalytics.col.topProject') }}</th>
                <th>{{ t('sessionAnalytics.col.lastUsed') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="k in apiKeys" :key="k.key_id">
                <td>{{ k.key_alias || '-' }}</td>
                <td>{{ k.key_id }}</td>
                <td>{{ fmt(k.request_count) }}</td>
                <td>{{ fmt(k.total_tokens) }}</td>
                <!-- ★ N9：API Key 这一格**有意保持原样**（不拆美元/积分两栏）——
                     该端点（/user-ranking/:owner/api-keys）的 internal/external 语义
                     与会话分析族**相反**（后端已单独登记）⇒ 口径澄清前不贴
                     「美元 / 积分」标签，否则就是把错的单位贴在错的数上。 -->
                <td>{{ money(k.total_cost) }}</td>
                <td>{{ fmt(k.project_count) }}</td>
                <td>{{ k.top_project || '-' }}</td>
                <td>{{ formatTime(k.last_used_at) }}</td>
              </tr>
            </tbody>
          </table>
        </DataTable>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
// SessionAnalyticsUserDetailView.vue — 人员详情页（2026-11-20）
//
// 三个端点串成一条 drill-down：
//   /user-ranking/:owner              → 身份卡 + 指标卡 + 四条趋势
//   /user-ranking/:owner/projects     → 项目列表（点项目切到任务列表）
//   /user-ranking/:owner/projects/:p/tasks → 该项目的任务列表
//
// `owner` / `project` 是**路径段**：客户端必须编码（API client 里已 encodeURIComponent）。

import { computed, onMounted, ref, watch } from 'vue'
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
// ★ N9：项目 / 任务两格走 SaCostCell（两个单位分列）；数值口径见 saCostSplit.ts。
import SaCostCell from './components/SaCostCell.vue'
import { creditsCostText, usdCostText } from './saCostSplit'
import { useSessionAnalyticsLoader } from './useSessionAnalytics'
import {
  getProjectTasks,
  getUserDetail,
  getUserProjects,
  getUserModelUsage,
  getUserAPIKeyUsage,
  type ProjectTaskRow,
  type UserDetailResponse,
  type UserProjectRow,
  type UserModelUsage,
  type UserAPIKeyUsage,
} from '../../../api/sessionAnalytics'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const detailLoader = useSessionAnalyticsLoader()
const projectsLoader = useSessionAnalyticsLoader()
const tasksLoader = useSessionAnalyticsLoader()
const modelsLoader = useSessionAnalyticsLoader()
const apiKeysLoader = useSessionAnalyticsLoader()

// 跟踪是否已加载过（用于懒加载判断）
const modelsLoaded = ref(false)
const apiKeysLoaded = ref(false)

const loading = computed(() => detailLoader.loading.value)
const error = computed(() => detailLoader.error.value)
const stale = computed(() => detailLoader.stale.value)
const projectsLoading = computed(() => projectsLoader.loading.value)
const tasksLoading = computed(() => tasksLoader.loading.value)
const modelsLoading = computed(() => modelsLoader.loading.value)
const apiKeysLoading = computed(() => apiKeysLoader.loading.value)

const range = ref<KxDateRange>(rangeFromSpanDays(7))
const draftRange = ref<KxDateRange>(range.value)

const detail = ref<UserDetailResponse | null>(null)
const projects = ref<UserProjectRow[]>([])
const tasks = ref<ProjectTaskRow[]>([])
const models = ref<UserModelUsage[]>([])
const apiKeys = ref<UserAPIKeyUsage[]>([])
const selectedProject = ref('')
const tab = ref<'projects' | 'tasks' | 'models' | 'apiKeys'>('projects')


// ★★ 2026-11-23（L16 修复轮）：**tab 级 loader 的错误/陈旧态此前完全没有出口**。
//
// ★ 事故机制（实测，详见 docs/audit/2026-11-20-会话分析系统前端实施报告.md §7.11）：
//   ① 页面级 `error` 绑定的是 **detailLoader.error** ⇒ tab 自己的 loader 报错时
//      **没有任何横幅承接**；
//   ② DataTable 的 `empty` 条件是「`!loading && 行数===0`」⇒ 加载**失败**后
//      `loading=false`、数据数组仍为空 ⇒ **`empty=true`** ⇒ 渲染出
//      **「暂无数据」空态**。
//   ⇒⇒ 合起来：**端点 500 被渲染成「该 owner 恰好没数据」** ⇒ 用户完全看不出后端挂了。
//
// ★ 所以修法有**两半**，缺一不可：
//   ① **让 tab 级错误可见**：按当前 tab 取它自己的 loader 的 error/stale，渲染横幅；
//   ② **禁止在错误态下渲染空态**：`:empty` 必须额外要求「无 error 且无 stale」。
//   ⇒ ★ 只做 ① 不做 ② ⇒ 横幅下面还会挂一个「暂无数据」，自相矛盾；
//   ⇒ ★ 只做 ② 不做 ① ⇒ 表格区域全空，用户更懵。
const tabLoader = computed(() => {
  switch (tab.value) {
    case 'projects': return projectsLoader
    case 'tasks': return tasksLoader
    case 'models': return modelsLoader
    case 'apiKeys': return apiKeysLoader
    default: return null
  }
})
const tabError = computed(() => tabLoader.value?.error.value ?? '')
const tabStale = computed(() => tabLoader.value?.stale.value ?? false)

/** 重载当前 tab（横幅的「重试」动作）—— 只重载该 tab，不动页面级数据。 */
function reloadActiveTab(): void {
  switch (tab.value) {
    case 'projects': void loadProjects(); break
    case 'tasks': void loadTasks(); break
    case 'models': void loadModels(); break
    case 'apiKeys': void loadAPIKeys(); break
    default: void load()
  }
}

/**
 * 项目表 / 任务表的名称列本地排序：none（默认，服务端按会话数排）→ asc → desc → none。
 * ★ 用 localeCompare 而不是码点序：码点序会把中文名排得很乱。
 */
const projectSort = ref<'none' | 'asc' | 'desc'>('none')
const taskSort = ref<'none' | 'asc' | 'desc'>('none')

const sortedProjects = computed<UserProjectRow[]>(() => {
  if (projectSort.value === 'none') return projects.value
  const factor = projectSort.value === 'asc' ? 1 : -1
  return [...projects.value].sort((a, b) =>
    factor * (a.project || '').localeCompare(b.project || ''),
  )
})

const sortedTasks = computed<ProjectTaskRow[]>(() => {
  if (taskSort.value === 'none') return tasks.value
  const factor = taskSort.value === 'asc' ? 1 : -1
  return [...tasks.value].sort((a, b) => factor * (a.task || '').localeCompare(b.task || ''))
})

function toggleProjectSort(): void {
  projectSort.value =
    projectSort.value === 'none' ? 'asc' : projectSort.value === 'asc' ? 'desc' : 'none'
}

function toggleTaskSort(): void {
  taskSort.value =
    taskSort.value === 'none' ? 'asc' : taskSort.value === 'asc' ? 'desc' : 'none'
}

const owner = computed(() => String(route.params.owner ?? ''))

const presets = computed<KxDateRangePreset[]>(() => [
  { id: 'sa-7d', labelKey: 'sessionAnalytics.range.preset7', resolve: () => rangeFromSpanDays(7) },
  { id: 'sa-30d', labelKey: 'sessionAnalytics.range.preset30', resolve: () => rangeFromSpanDays(30) },
])

const sessionTrend = computed(() =>
  (detail.value?.trends?.sessions ?? []).map((p) => ({ label: p.date, value: p.count })),
)
const taskTrend = computed(() =>
  (detail.value?.trends?.tasks ?? []).map((p) => ({ label: p.date, value: p.count })),
)
const costTrend = computed(() =>
  (detail.value?.trends?.cost ?? []).map((p) => ({ label: p.date, value: p.internal })),
)
/** 排名趋势：数值越小越靠前 ⇒ 曲线按原值画即可（下降 = 上升）。 */
const rankTrend = computed(() =>
  (detail.value?.trends?.rank ?? []).map((p) => ({ label: p.date, value: p.rank })),
)

const rankChangeText = computed(() => {
  const change = detail.value?.summary.rank_change ?? 0
  if (!change) return ''
  const sign = change > 0 ? '+' : ''
  return `${t('sessionAnalytics.metric.rankChange')} ${sign}${change}`
})

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

/**
 * ★ N9：指标卡里的两个成本值。
 *   · 内部成本（美元）：真被测到的量，0 是合法测量值 ⇒ 照常渲染。
 *   · 外部成本（积分）：积分写侧未落地时后端给的是 0 ⇒ 渲染「—」而不是
 *     「0 积分」（把「没采到」讲成「测得是 0」）。
 *   ⇒ 规则真源在同目录 saCostSplit.ts（与 SaCostCell 用同一套）。
 */
const internalCostText = computed(() => usdCostText(detail.value?.summary.internal_cost, t))
const externalCostText = computed(() => creditsCostText(detail.value?.summary.external_cost, t))

function percent(v: number | null | undefined): string {
  if (v === null || v === undefined || Number.isNaN(v)) return '—'
  return (v * 100).toFixed(1) + '%'
}

function formatTime(value: string): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return d.toLocaleString()
}

async function load(): Promise<void> {
  if (!owner.value) return
  const res = await detailLoader.run(
    () => getUserDetail(owner.value, {
      start_date: range.value.start,
      end_date: range.value.end,
    }),
    'sessionAnalytics.error.userDetailFailed',
  )
  if (res) detail.value = res
}

async function loadProjects(): Promise<void> {
  if (!owner.value) return
  const res = await projectsLoader.run(
    () => getUserProjects(owner.value, {
      start_date: range.value.start,
      end_date: range.value.end,
      sort_by: 'sessions',
      order: 'desc',
    }),
    'sessionAnalytics.error.userProjectsFailed',
  )
  projects.value = res?.projects ?? []
}

async function loadTasks(): Promise<void> {
  if (!owner.value || !selectedProject.value) return
  const res = await tasksLoader.run(
    () => getProjectTasks(owner.value, selectedProject.value, {
      start_date: range.value.start,
      end_date: range.value.end,
      sort_by: 'sessions',
    }),
    'sessionAnalytics.error.projectTasksFailed',
  )
  tasks.value = res?.tasks ?? []
}

async function loadModels(): Promise<void> {
  if (!owner.value) return
  const res = await modelsLoader.run(
    () => getUserModelUsage(owner.value, {
      start_date: range.value.start,
      end_date: range.value.end,
    }),
    'sessionAnalytics.error.userModelsFailed',
  )
  models.value = res?.models ?? []
  modelsLoaded.value = true
}

async function loadAPIKeys(): Promise<void> {
  if (!owner.value) return
  const res = await apiKeysLoader.run(
    () => getUserAPIKeyUsage(owner.value, {
      start_date: range.value.start,
      end_date: range.value.end,
    }),
    'sessionAnalytics.error.userAPIKeysFailed',
  )
  apiKeys.value = res?.api_keys ?? []
  apiKeysLoaded.value = true
}

function selectProject(project: string): void {
  selectedProject.value = project
  tab.value = 'tasks'
  void loadTasks()
}

function onApplyRange(next: KxDateRange): void {
  range.value = next
  void load()
  void loadProjects()
  if (selectedProject.value) void loadTasks()
  if (tab.value === 'models') void loadModels()
  if (tab.value === 'apiKeys') void loadAPIKeys()
}

function goBack(): void {
  void router.push({ name: 'session-analytics-user-ranking' })
}

watch(owner, () => {
  detail.value = null
  projects.value = []
  tasks.value = []
  models.value = []
  apiKeys.value = []
  modelsLoaded.value = false
  apiKeysLoaded.value = false
  selectedProject.value = ''
  tab.value = 'projects'
  void load()
  void loadProjects()
})

// 2026-11-22: 添加tab切换监听，自动加载对应数据
watch(tab, (newTab) => {
  if (newTab === 'models' && !modelsLoaded.value) {
    void loadModels()
  } else if (newTab === 'apiKeys' && !apiKeysLoaded.value) {
    void loadAPIKeys()
  }
})

onMounted(() => {
  void load()
  void loadProjects()
})
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

.sa-identity {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 16px;
  border: 1px solid var(--border);
  border-radius: var(--radius-card, 8px);
  background: var(--surface);
  margin-bottom: 16px;
}

.sa-identity__name {
  color: var(--text);
  font-size: 16px;
  font-weight: 600;
}

.sa-identity__meta {
  display: flex;
  gap: 12px;
  margin-top: 4px;
  color: var(--muted);
  font-size: 12px;
}

.sa-section {
  margin-top: 24px;
}

.sa-section__title {
  margin: 0 0 10px;
  font-size: 14px;
  color: var(--text);
}

.sa-tabs {
  display: flex;
  gap: 8px;
  margin-top: 24px;
}

.sa-tab {
  padding: 6px 14px;
  border-radius: var(--radius-card, 8px);
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--muted);
  font-size: 13px;
  cursor: pointer;
}

.sa-tab--active {
  border-color: var(--accent);
  color: var(--text);
  background: var(--primary-soft, var(--surface));
}

.sa-tab:disabled {
  opacity: 0.5;
  cursor: not-allowed;
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
  margin: 12px 0 0;
  color: var(--muted);
  font-size: 12px;
  line-height: 1.6;
}

/* ★ N9：成本组容器 —— 两张成本卡并排，且在 StatsRow 栅格里占两列
   （desktop 5 列 = 会话数 + 任务数 + 成本组×2 + 排名，正好一行）。 */
.sa-cost-group {
  grid-column: span 2;
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--kx-space-3);
  min-width: 0;
}

@media (max-width: 768px) {
  .session-analytics-view {
    padding: 12px;
  }

  .sa-range {
    flex-direction: column;
    align-items: flex-start;
  }

  .sa-identity {
    flex-direction: column;
    align-items: flex-start;
  }
}
</style>
