<script setup lang="ts">
/**
 * SaTop5Rankings.vue — 会话分析「Top5 四件套」（2026-11-23 审计轮新增）
 *
 * 模型 / 供应商 / 用户 / 任务 四个维度的 Top5 排行，点击任一行直达对应详情列表。
 *
 * ★ 口径纪律（四维全部来自 session-analytics 物化视图，与 /admin/session-analytics
 *   总览页**同一实现**）：
 *   · 模型   → model-ranking   → model_usage_aggregates（请求级）
 *   · 供应商 → provider-ranking → model_usage_aggregates（请求级，按 provider 聚合）
 *   · 用户   → user-ranking     → user_session_aggregates（会话级）
 *   · 任务   → task-ranking     → project_task_aggregates（会话级）
 *   ⇒ 请求级（模型/供应商）与会话级（用户/任务）**不可相加**（framework §3.2）。
 *
 * ★ 为什么供应商必须由后端聚合：model_usage_aggregates 的 model 列是纯模型名
 *   （不含 provider/ 前缀），前端 split('/')[0] 会把模型名当 provider。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import DataTable from '../../../../components/ui/DataTable.vue'
// ★ N9：四张 Top5 表的成本格一律走 SaCostCell（内部成本美元 / 外部成本积分分列）——
//   total_cost 是**跨单位合计**，渲染成一个带 $ 的数等于凭空造出美元金额。
import SaCostCell from './SaCostCell.vue'
import {
  getModelRanking,
  getProviderRanking,
  getUserRanking,
  getTaskRanking,
  type ModelRankingRow,
  type ProviderRankingRow,
  type UserRankingRow,
  type TaskRankingRow,
} from '../../../../api/sessionAnalytics'

const props = defineProps<{
  startDate: string
  endDate: string
  loading?: boolean
  error?: string | null
  /**
   * ★ 2026-11-23 审计轮（P2-2）：**逐维度**的加载失败文案。
   *   键 = 维度（models / providers / users / tasks）。
   *   ★ 为什么必须逐维度：此前失败维度被写成空数组 ⇒ 卡片显示「无结果」
   *   ⇒ 把「接口挂了」伪装成「这段时间没有数据」（用户会去查业务，
   *   而真相是这一腿没取到）⇒ 失败维度必须显式呈现为加载失败。
   */
  dimensionErrors?: Partial<Record<'models' | 'providers' | 'users' | 'tasks', string>>
}>()

const { t } = useI18n()
const router = useRouter()

const emit = defineEmits<{
  (e: 'retry'): void
}>()

const models = computed<ModelRankingRow[]>(() => modelRows.value.slice(0, 5))
const providers = computed<ProviderRankingRow[]>(() => providerRows.value.slice(0, 5))
const users = computed<UserRankingRow[]>(() => userRows.value.slice(0, 5))
const tasks = computed<TaskRankingRow[]>(() => taskRows.value.slice(0, 5))

// 由父组件通过 v-model 注入的原始行（避免本组件重复发请求）。
const modelRows = defineModel<ModelRankingRow[]>('modelRows', { default: () => [] })
const providerRows = defineModel<ProviderRankingRow[]>('providerRows', { default: () => [] })
const userRows = defineModel<UserRankingRow[]>('userRows', { default: () => [] })
const taskRows = defineModel<TaskRankingRow[]>('taskRows', { default: () => [] })

function fmt(value: number | null | undefined): string {
  if (value == null) return '0'
  return value.toLocaleString()
}

// ★ N9：money() 已删除 —— 四张表的成本格改走 SaCostCell（两个单位分列）。
//   旧实现把 total_cost（美元 + 积分 的跨单位合计）渲染成 $12.34，
//   等于凭空造出一个不存在的美元金额。

function pct(value: number | null | undefined): string {
  if (value == null) return '—'
  return `${value.toFixed(1)}%`
}

// ── 点击跳转：四个维度各自直达对应详情列表 ──

function openModel(model: string) {
  void router.push({
    path: '/admin/usage-trends',
    query: { model, start: props.startDate, end: props.endDate },
  })
}

function openProvider(provider: string) {
  void router.push({
    path: '/admin/usage-trends',
    query: { provider, start: props.startDate, end: props.endDate },
  })
}

function openUser(owner: string) {
  void router.push({
    path: `/admin/session-analytics/user-ranking/${encodeURIComponent(owner)}`,
    query: { start_date: props.startDate, end_date: props.endDate },
  })
}

function openTask(task: string) {
  // 任务详情走会话分析的用户详情页的「项目/任务」子视图；
  // 仓内无独立的任务详情路由 ⇒ 直达用户详情页（带任务名作为定位参数）。
  void router.push({
    path: '/admin/session-analytics/users',
    query: { task, start_date: props.startDate, end_date: props.endDate },
  })
}
</script>

<template>
  <div class="sa-top5">
    <div v-if="error" class="sa-top5__error" role="alert" data-testid="sa-top5-error">
      {{ error }}
      <button type="button" class="sa-top5__retry" @click="emit('retry')">
        {{ t('sessionAnalytics.action.retry') }}
      </button>
    </div>

    <div class="sa-top5__grid">
      <!-- Top5 模型 -->
      <section class="sa-top5__card" data-testid="sa-top5-models">
        <h3 class="sa-top5__title">{{ t('sessionAnalytics.top5.models') }}</h3>
        <!-- ★ 2026-11-23（P2-2）：这一维度加载失败 ⇒ 显式报失败，不画成「无结果」。 -->
        <div
          v-if="props.dimensionErrors?.models"
          class="sa-top5__error"
          role="alert"
          data-testid="sa-top5-models-error"
        >
          {{ props.dimensionErrors.models }}
          <button type="button" class="sa-top5__retry" @click="emit('retry')">
            {{ t('sessionAnalytics.action.retry') }}
          </button>
        </div>
        <DataTable
          v-else
          :loading="loading"
          :empty="models.length === 0"
          :empty-text="t('sessionAnalytics.empty.noResultOverview')"
          min-width="420px"
        >
          <table class="sa-top5__table">
            <thead>
              <tr>
                <th>{{ t('sessionAnalytics.col.model') }}</th>
                <th>{{ t('sessionAnalytics.col.provider') }}</th>
                <th>{{ t('sessionAnalytics.col.requests') }}</th>
                <th>{{ t('sessionAnalytics.col.cost') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in models" :key="row.model">
                <td>
                  <button
                    type="button"
                    class="sa-top5__link"
                    data-testid="sa-top5-model-link"
                    :title="t('sessionAnalytics.hint.modelTrend')"
                    @click="openModel(row.model)"
                  >{{ row.model }}</button>
                </td>
                <td>{{ row.provider || '—' }}</td>
                <td class="num">{{ fmt(row.request_count) }}</td>
                <td class="num">
                  <SaCostCell :internal-cost="row.internal_cost" :external-cost="row.external_cost" />
                </td>
              </tr>
            </tbody>
          </table>
        </DataTable>
        <div class="sa-top5__more">
          <button type="button" class="sa-top5__more-btn" @click="router.push('/admin/session-analytics/model-ranking')">
            {{ t('sessionAnalytics.action.viewAllModels') }} →
          </button>
        </div>
      </section>

      <!-- Top5 供应商 -->
      <section class="sa-top5__card" data-testid="sa-top5-providers">
        <h3 class="sa-top5__title">{{ t('sessionAnalytics.top5.providers') }}</h3>
        <!-- ★ 2026-11-23（P2-2）：这一维度加载失败 ⇒ 显式报失败，不画成「无结果」。 -->
        <div
          v-if="props.dimensionErrors?.providers"
          class="sa-top5__error"
          role="alert"
          data-testid="sa-top5-providers-error"
        >
          {{ props.dimensionErrors.providers }}
          <button type="button" class="sa-top5__retry" @click="emit('retry')">
            {{ t('sessionAnalytics.action.retry') }}
          </button>
        </div>
        <DataTable
          v-else
          :loading="loading"
          :empty="providers.length === 0"
          :empty-text="t('sessionAnalytics.empty.noResultOverview')"
          min-width="420px"
        >
          <table class="sa-top5__table">
            <thead>
              <tr>
                <th>{{ t('sessionAnalytics.col.provider') }}</th>
                <th>{{ t('sessionAnalytics.col.modelsCount') }}</th>
                <th>{{ t('sessionAnalytics.col.requests') }}</th>
                <th>{{ t('sessionAnalytics.col.cost') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in providers" :key="row.provider">
                <td>
                  <button
                    type="button"
                    class="sa-top5__link"
                    data-testid="sa-top5-provider-link"
                    @click="openProvider(row.provider)"
                  >{{ row.provider }}</button>
                </td>
                <td class="num">{{ fmt(row.model_count) }}</td>
                <td class="num">{{ fmt(row.request_count) }}</td>
                <td class="num">
                  <SaCostCell :internal-cost="row.internal_cost" :external-cost="row.external_cost" />
                </td>
              </tr>
            </tbody>
          </table>
        </DataTable>
      </section>

      <!-- Top5 用户 -->
      <section class="sa-top5__card" data-testid="sa-top5-users">
        <h3 class="sa-top5__title">{{ t('sessionAnalytics.top5.users') }}</h3>
        <!-- ★ 2026-11-23（P2-2）：这一维度加载失败 ⇒ 显式报失败，不画成「无结果」。 -->
        <div
          v-if="props.dimensionErrors?.users"
          class="sa-top5__error"
          role="alert"
          data-testid="sa-top5-users-error"
        >
          {{ props.dimensionErrors.users }}
          <button type="button" class="sa-top5__retry" @click="emit('retry')">
            {{ t('sessionAnalytics.action.retry') }}
          </button>
        </div>
        <DataTable
          v-else
          :loading="loading"
          :empty="users.length === 0"
          :empty-text="t('sessionAnalytics.empty.noResultOverview')"
          min-width="420px"
        >
          <table class="sa-top5__table">
            <thead>
              <tr>
                <th>{{ t('sessionAnalytics.col.owner') }}</th>
                <th>{{ t('sessionAnalytics.col.sessions') }}</th>
                <th>{{ t('sessionAnalytics.col.tokens') }}</th>
                <th>{{ t('sessionAnalytics.col.cost') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in users" :key="row.owner">
                <td>
                  <button
                    type="button"
                    class="sa-top5__link"
                    data-testid="sa-top5-user-link"
                    @click="openUser(row.owner)"
                  >{{ row.display_name || row.owner }}</button>
                </td>
                <td class="num">{{ fmt(row.session_count) }}</td>
                <td class="num">{{ fmt(row.total_tokens) }}</td>
                <td class="num">
                  <SaCostCell :internal-cost="row.internal_cost" :external-cost="row.external_cost" />
                </td>
              </tr>
            </tbody>
          </table>
        </DataTable>
        <div class="sa-top5__more">
          <button type="button" class="sa-top5__more-btn" @click="router.push('/admin/session-analytics/user-ranking')">
            {{ t('sessionAnalytics.action.viewAllUsers') }} →
          </button>
        </div>
      </section>

      <!-- Top5 任务 -->
      <section class="sa-top5__card" data-testid="sa-top5-tasks">
        <h3 class="sa-top5__title">{{ t('sessionAnalytics.top5.tasks') }}</h3>
        <!-- ★ 2026-11-23（P2-2）：这一维度加载失败 ⇒ 显式报失败，不画成「无结果」。 -->
        <div
          v-if="props.dimensionErrors?.tasks"
          class="sa-top5__error"
          role="alert"
          data-testid="sa-top5-tasks-error"
        >
          {{ props.dimensionErrors.tasks }}
          <button type="button" class="sa-top5__retry" @click="emit('retry')">
            {{ t('sessionAnalytics.action.retry') }}
          </button>
        </div>
        <DataTable
          v-else
          :loading="loading"
          :empty="tasks.length === 0"
          :empty-text="t('sessionAnalytics.empty.noResultOverview')"
          min-width="420px"
        >
          <table class="sa-top5__table">
            <thead>
              <tr>
                <th>{{ t('sessionAnalytics.col.task') }}</th>
                <th>{{ t('sessionAnalytics.col.sessions') }}</th>
                <th>{{ t('sessionAnalytics.col.users') }}</th>
                <th>{{ t('sessionAnalytics.col.cost') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in tasks" :key="row.task">
                <td>
                  <button
                    type="button"
                    class="sa-top5__link"
                    data-testid="sa-top5-task-link"
                    @click="openTask(row.task)"
                  >{{ row.task }}</button>
                </td>
                <td class="num">{{ fmt(row.session_count) }}</td>
                <td class="num">{{ fmt(row.owner_count) }}</td>
                <td class="num">
                  <SaCostCell :internal-cost="row.internal_cost" :external-cost="row.external_cost" />
                </td>
              </tr>
            </tbody>
          </table>
        </DataTable>
      </section>
    </div>
  </div>
</template>

<style scoped>
.sa-top5 {
  width: 100%;
}
.sa-top5__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
}
@media (max-width: 1024px) {
  .sa-top5__grid { grid-template-columns: 1fr; }
}
.sa-top5__card {
  border: 1px solid var(--border, var(--surface-secondary));
  border-radius: 10px;
  background: var(--card, var(--on-primary));
  padding: 12px 16px;
  min-width: 0;
}
.sa-top5__title {
  font-weight: 600;
  font-size: 14px;
  margin: 0 0 12px;
}
.sa-top5__table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.sa-top5__table th,
.sa-top5__table td {
  padding: 8px;
  border-bottom: 1px solid var(--border, var(--surface-secondary));
  text-align: left;
}
.sa-top5__table th {
  color: var(--muted, var(--text-secondary));
  font-weight: 500;
}
.sa-top5__table .num {
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.sa-top5__link {
  border: 0;
  background: none;
  padding: 0;
  color: var(--primary);
  cursor: pointer;
  font: inherit;
  text-align: left;
}
.sa-top5__link:hover { text-decoration: underline; }
.sa-top5__more { margin-top: 10px; }
.sa-top5__more-btn {
  border: 0;
  background: none;
  padding: 0;
  color: var(--primary);
  cursor: pointer;
  font-size: 13px;
}
.sa-top5__more-btn:hover { text-decoration: underline; }
.sa-top5__error {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  margin-bottom: 12px;
  border-radius: 6px;
  background: var(--danger-bg);
  border: 1px solid var(--danger-bd);
  color: var(--danger);
}
.sa-top5__retry {
  padding: 4px 12px;
  border: 1px solid var(--danger-bd);
  border-radius: 4px;
  background: white;
  cursor: pointer;
}
</style>
