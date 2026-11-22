<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

// 2026-11-21 统计口径对齐轮：top 分类数据打通独立统计页——模型行直达
// /admin/usage-trends 并自动带上同一窗口条件（usage-trends 深链参数：
// start/end/model，缺省 metric=requests）。
// 2026-11-23 整合轮：添加 topUsers 和 topProviders 支持
const props = defineProps<{
  topClients: Array<{ client_id: string; session_count: number; total_cost: number; avg_health?: number | null }>
  topTasks: Array<{ task_id: string; session_count: number; total_cost: number; avg_health?: number | null }>
  modelUsage?: Array<{
    model: string
    session_count: number
    request_count: number
    total_cost: number
    success_rate: number
    avg_latency_ms: number
  }>
  /** 用户 Top5 排行（2026-11-23 新增） */
  topUsers?: Array<{
    owner: string
    display_name: string
    session_count: number
    total_tokens: number
    total_cost: number
  }>
  /** 供应商 Top5 排行（2026-11-23 新增） */
  topProviders?: Array<{
    provider: string
    model_count: number
    total_requests: number
    total_cost: number
  }>
  /** 趋势窗口（YYYY-MM-DD 日历日），模型行跳 usage-trends 时自动带上。 */
  trendWindow?: { start: string; end: string }
}>()

const { t } = useI18n()
const router = useRouter()

function openModelTrend(model: string) {
  const w = props.trendWindow
  const query: Record<string, string> = { model }
  if (w?.start && w?.end) {
    query.start = w.start
    query.end = w.end
  }
  void router.push({ path: '/admin/usage-trends', query })
}

function formatUsd(value: number) {
  return `$${value.toFixed(4)}`
}

function healthClass(score: number) {
  if (score >= 75) return 'success'
  if (score >= 60) return 'warning'
  return 'danger'
}

function openClient(id: string) {
  router.push(`/admin/session-analytics/clients/${id}`)
}
function openTask(id: string) {
  router.push(`/admin/session-analytics/tasks/${id}`)
}

// 2026-11-23 整合轮：新增用户和供应商跳转
function openUserDetail(owner: string) {
  const w = props.trendWindow
  const query: Record<string, string> = {}
  if (w?.start && w?.end) {
    query.start_date = w.start
    query.end_date = w.end
  }
  void router.push({
    path: `/admin/session-analytics/user-ranking/${encodeURIComponent(owner)}`,
    query
  })
}

function openProviderTrend(provider: string) {
  const w = props.trendWindow
  const query: Record<string, string> = { provider }
  if (w?.start && w?.end) {
    query.start = w.start
    query.end = w.end
  }
  void router.push({ path: '/admin/usage-trends', query })
}
</script>

<template>
  <div>
    <div v-if="modelUsage !== undefined" class="detail-card model-card">
      <div class="detail-card__header">{{ t('sessions.stats.topModels') }}</div>
      <div v-if="modelUsage.length" class="table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('sessions.stats.model') }}</th>
              <th>{{ t('sessions.stats.sessionCount') }}</th>
              <th>{{ t('sessions.stats.requestCount') }}</th>
              <th>{{ t('sessions.stats.totalCost') }}</th>
              <th>{{ t('sessions.stats.successRate') }}</th>
              <th>{{ t('sessions.stats.avgLatency') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in modelUsage" :key="row.model">
              <td>
                <button
                  type="button"
                  class="model-trend-link"
                  data-testid="stats-model-trend-link"
                  :title="t('sessions.stats.modelTrendHint')"
                  @click="openModelTrend(row.model)"
                >{{ row.model }}</button>
              </td>
              <td class="num">{{ row.session_count }}</td>
              <td class="num">{{ row.request_count }}</td>
              <td class="num">{{ formatUsd(row.total_cost ?? 0) }}</td>
              <td class="num">{{ ((row.success_rate ?? 0) * 100).toFixed(1) }}%</td>
              <td class="num">{{ Math.round(row.avg_latency_ms ?? 0) }}ms</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="empty-hint">{{ t('sessions.stats.topModelsEmpty') }}</p>
    </div>

  <div class="rankings-row">
    <div class="detail-card">
      <div class="detail-card__header">{{ t('sessions.stats.topClients') }}</div>
      <p v-if="!topClients.length" class="empty-hint">{{ t('sessions.stats.emptyNoResultTopClients') }}</p>
      <div v-else class="table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('sessions.stats.clientId') }}</th>
              <th>{{ t('sessions.stats.sessionCount') }}</th>
              <th>{{ t('sessions.stats.totalCost') }}</th>
              <th>{{ t('sessions.stats.avgHealth') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in topClients" :key="row.client_id">
              <td><button type="button" class="link-btn" @click="openClient(String(row.client_id))">{{ row.client_id }}</button></td>
              <td class="num">{{ row.session_count }}</td>
              <td class="num">{{ formatUsd(Number(row.total_cost ?? 0)) }}</td>
              <td class="num">{{ row.avg_health != null ? row.avg_health : '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
    <div class="detail-card">
      <div class="detail-card__header">{{ t('sessions.stats.topTasks') }}</div>
      <p v-if="!topTasks.length" class="empty-hint">{{ t('sessions.stats.emptyNoResultTopTasks') }}</p>
      <div v-else class="table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('sessions.stats.taskId') }}</th>
              <th>{{ t('sessions.stats.sessionCount') }}</th>
              <th>{{ t('sessions.stats.totalCost') }}</th>
              <th>{{ t('sessions.stats.avgHealth') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in topTasks" :key="row.task_id">
              <td><button type="button" class="link-btn" @click="openTask(String(row.task_id))">{{ row.task_id }}</button></td>
              <td class="num">{{ row.session_count }}</td>
              <td class="num">{{ formatUsd(Number(row.total_cost ?? 0)) }}</td>
              <td class="num">
                <span v-if="row.avg_health != null" :class="'health-pill health-pill--' + healthClass(Number(row.avg_health))">
                  {{ row.avg_health }}/100
                </span>
                <template v-else>—</template>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
    <!-- 2026-11-23 整合轮：用户 Top5 -->
    <div v-if="topUsers !== undefined" class="detail-card">
      <div class="detail-card__header">{{ t('sessions.stats.topUsers') }}</div>
      <p v-if="!topUsers.length" class="empty-hint">{{ t('sessions.stats.emptyNoResultTopUsers') }}</p>
      <div v-else class="table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('sessions.stats.user') }}</th>
              <th>{{ t('sessions.stats.sessions') }}</th>
              <th>{{ t('sessions.stats.tokens') }}</th>
              <th>{{ t('sessions.stats.totalCost') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in topUsers" :key="row.owner">
              <td><button type="button" class="link-btn" @click="openUserDetail(row.owner)">{{ row.display_name || row.owner }}</button></td>
              <td class="num">{{ row.session_count }}</td>
              <td class="num">{{ row.total_tokens.toLocaleString() }}</td>
              <td class="num">{{ formatUsd(row.total_cost) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
    <!-- 2026-11-23 整合轮：供应商 Top5 -->
    <div v-if="topProviders !== undefined" class="detail-card">
      <div class="detail-card__header">{{ t('sessions.stats.topProviders') }}</div>
      <p v-if="!topProviders.length" class="empty-hint">{{ t('sessions.stats.emptyNoResultTopProviders') }}</p>
      <div v-else class="table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('sessions.stats.provider') }}</th>
              <th>{{ t('sessions.stats.models') }}</th>
              <th>{{ t('sessions.stats.requests') }}</th>
              <th>{{ t('sessions.stats.totalCost') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in topProviders" :key="row.provider">
              <td><button type="button" class="link-btn" @click="openProviderTrend(row.provider)">{{ row.provider }}</button></td>
              <td class="num">{{ row.model_count }}</td>
              <td class="num">{{ row.total_requests.toLocaleString() }}</td>
              <td class="num">{{ formatUsd(row.total_cost) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
  </div>
</template>

<style scoped>
.rankings-row {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  margin-bottom: 16px;
  width: 100%;
}
@media (max-width: 1024px) { .rankings-row { grid-template-columns: 1fr; } }
.detail-card {
  border: 1px solid var(--border, var(--surface-secondary));
  border-radius: 10px;
  background: var(--card, var(--on-primary));
  padding: 12px 16px;
  min-width: 0;
}
.detail-card__header { font-weight: 600; font-size: 14px; margin-bottom: 12px; }
.empty-hint { margin: 0; font-size: 13px; color: var(--muted, var(--text-secondary)); }
.table-wrap { overflow-x: auto; }
.data-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.data-table th, .data-table td { padding: 8px 8px; border-bottom: 1px solid var(--border, var(--surface-secondary)); text-align: left; }
.data-table th { color: var(--muted, var(--text-secondary)); font-weight: 500; }
.data-table .num { text-align: right; font-variant-numeric: tabular-nums; }
.link-btn { border: 0; background: none; color: var(--accent, var(--accent)); cursor: pointer; padding: 0; font: inherit; }
.health-pill { display: inline-block; padding: 2px 4px; border-radius: 4px; font-size: 12px; background: var(--surface-secondary); }
.health-pill--success { background: var(--success-bg); }
.health-pill--warning { background: var(--warning-bg); }
.health-pill--danger { background: var(--danger-bg); }
.model-card { margin-bottom: 16px; }

/* 2026-11-21 对齐轮：模型名 → usage-trends 深链 */
.model-trend-link {
  background: none;
  border: none;
  padding: 0;
  color: var(--primary);
  cursor: pointer;
  font: inherit;
  text-align: left;
}
.model-trend-link:hover {
  text-decoration: underline;
}
</style>
