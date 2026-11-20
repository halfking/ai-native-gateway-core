<template>
  <div class="session-analytics-view">
    <PageHeader :title="t('sessionAnalytics.title')" :subtitle="t('sessionAnalytics.nav.userDetail')">
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
      <section class="sa-identity">
        <div class="sa-identity__main">
          <div class="sa-identity__name">{{ detail.user.display_name || detail.user.owner }}</div>
          <div class="sa-identity__meta">
            <span>{{ detail.user.email || '—' }}</span>
            <span v-if="detail.user.tenant_id">{{ detail.user.tenant_id }}</span>
          </div>
        </div>
        <button class="sa-btn" type="button" @click="goBack">
          {{ t('sessionAnalytics.action.back') }}
        </button>
      </section>

      <StatsRow :cols="{ mobile: 2, tablet: 2, desktop: 4 }">
        <StatCard
          :label="t('sessionAnalytics.metric.sessionCount')"
          :value="fmt(detail.summary.session_count)"
        />
        <StatCard
          :label="t('sessionAnalytics.metric.taskCount')"
          :value="fmt(detail.summary.task_count)"
        />
        <StatCard
          :label="t('sessionAnalytics.metric.totalCost')"
          :value="money(detail.summary.total_cost)"
        />
        <StatCard
          :label="t('sessionAnalytics.metric.rank')"
          :value="fmt(detail.summary.rank)"
          :sub="rankChangeText"
        />
      </StatsRow>

      <p class="sa-note">{{ t('sessionAnalytics.costNote') }}</p>

      <section class="sa-section">
        <h3 class="sa-section__title">{{ t('sessionAnalytics.trend.sessions') }}</h3>
        <SaTrendChart
          :points="sessionTrend"
          :format="(v: number) => fmt(v)"
          :empty-text="t('sessionAnalytics.empty.noResultUserDetail')"
        />
      </section>

      <section class="sa-section">
        <h3 class="sa-section__title">{{ t('sessionAnalytics.trend.tasks') }}</h3>
        <SaTrendChart
          :points="taskTrend"
          color-token="--primary"
          :format="(v: number) => fmt(v)"
          :empty-text="t('sessionAnalytics.empty.noResultUserDetail')"
        />
      </section>

      <section class="sa-section">
        <h3 class="sa-section__title">{{ t('sessionAnalytics.trend.cost') }}</h3>
        <SaTrendChart
          :points="costTrend"
          color-token="--warning"
          :format="(v: number) => money(v)"
          :empty-text="t('sessionAnalytics.empty.noResultUserDetail')"
        />
      </section>

      <section class="sa-section">
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
          :class="{ 'sa-tab--active': tab === 'projects' }"
          type="button"
          @click="tab = 'projects'"
        >
          {{ t('sessionAnalytics.section.projects') }}
        </button>
        <button
          class="sa-tab"
          :class="{ 'sa-tab--active': tab === 'tasks' }"
          type="button"
          :disabled="!selectedProject"
          @click="tab = 'tasks'"
        >
          {{ t('sessionAnalytics.section.tasks') }}
        </button>
      </div>

      <section v-if="tab === 'projects'" class="sa-section">
        <DataTable
          :loading="projectsLoading"
          :empty="!projectsLoading && projects.length === 0"
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
                <td>{{ money(p.total_cost) }}</td>
                <td>{{ formatTime(p.last_active_at) }}</td>
              </tr>
            </tbody>
          </table>
        </DataTable>
      </section>

      <section v-else class="sa-section">
        <DataTable
          :loading="tasksLoading"
          :empty="!tasksLoading && tasks.length === 0"
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
                <td>{{ money(task.total_cost) }}</td>
                <td>{{ formatTime(task.last_active_at) }}</td>
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
import { useSessionAnalyticsLoader } from './useSessionAnalytics'
import {
  getProjectTasks,
  getUserDetail,
  getUserProjects,
  type ProjectTaskRow,
  type UserDetailResponse,
  type UserProjectRow,
} from '../../../api/sessionAnalytics'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const detailLoader = useSessionAnalyticsLoader()
const projectsLoader = useSessionAnalyticsLoader()
const tasksLoader = useSessionAnalyticsLoader()

const loading = computed(() => detailLoader.loading.value)
const error = computed(() => detailLoader.error.value)
const stale = computed(() => detailLoader.stale.value)
const projectsLoading = computed(() => projectsLoader.loading.value)
const tasksLoading = computed(() => tasksLoader.loading.value)

const range = ref<KxDateRange>(rangeFromSpanDays(7))
const draftRange = ref<KxDateRange>(range.value)

const detail = ref<UserDetailResponse | null>(null)
const projects = ref<UserProjectRow[]>([])
const tasks = ref<ProjectTaskRow[]>([])
const selectedProject = ref('')
const tab = ref<'projects' | 'tasks'>('projects')

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
}

function goBack(): void {
  void router.push({ name: 'session-analytics-user-ranking' })
}

watch(owner, () => {
  detail.value = null
  projects.value = []
  tasks.value = []
  selectedProject.value = ''
  tab.value = 'projects'
  void load()
  void loadProjects()
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
