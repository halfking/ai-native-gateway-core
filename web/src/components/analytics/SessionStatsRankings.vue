<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

// 2026-11-21 统计口径对齐轮：top 分类数据打通独立统计页——模型行直达
// /admin/usage-trends 并自动带上同一窗口条件（usage-trends 深链参数：
// start/end/model，缺省 metric=requests）。
//
// 2026-11-23 审计轮（★ 撤销上一轮的实现）：
//   上一轮在此加了 topUsers / topProviders 两个 prop 与卡片，现已撤销并迁到
//   /admin/session-analytics/users 的 SaTop5Rankings 组件。理由：
//   ① 本组件的数据腿是 dashboard 实时查询，而用户/供应商榜的真源是物化视图
//      （15min 缓存）⇒ 同一区块混排两种时间口径，读者无法判断哪个数字是哪个口径；
//   ② 本组件在 Dashboard 上对所有登录用户可见，而 session-analytics 系端点
//      要求 admin 角色 ⇒ 普通用户只会拿到两个空卡片；
//   ③ 供应商维度必须由 SQL 按 provider 列聚合，前端 split('/') 会把模型名
//      当 provider（见 api/sessionAnalytics.ts 的 ProviderRankingRow 注释）。
const props = withDefaults(defineProps<{
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
  /** 趋势窗口（YYYY-MM-DD 日历日），模型行跳 usage-trends 时自动带上。 */
  trendWindow?: { start: string; end: string }
  /**
   * ★ F1：后端在总览响应里披露「模型腿是否按会话展开」。
   *   缺省 true —— 后端还没上报该字段时，当前实现**就是**按会话展开的
   *   （一个会话用了 N 个模型 ⇒ 该会话的整段成本在 N 个模型行各计一次）
   *   ⇒ 合计大于 KPI、不可与 KPI 相加 ⇒ 必须披露。
   *   后端明确回报 false 时（改成不展开）才撤下披露。
   */
  modelUsageExpanded?: boolean
  /** ★ F1：后端给出的展开原因（可读文案），有则一并显示。 */
  modelUsageExpansionReason?: string
}>(), {
  modelUsageExpanded: true,
  modelUsageExpansionReason: '',
})

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
</script>

<template>
  <div>
    <div v-if="modelUsage !== undefined" class="detail-card model-card">
      <div class="detail-card__header">{{ t('sessions.stats.topModels') }}</div>
      <!-- ★ F1：模型腿按**会话**展开（一个会话用了多个模型 ⇒ 整段成本在每个
           模型行各计一次）⇒ 本列合计**大于**面板 KPI、也不可与 KPI 相加。
           数字本轮不改（口径归后端裁决），但必须**可见披露**，否则读者会
           拿这一列去和 KPI 求和、对不上时只会怀疑数据不会怀疑口径。
           ★ 判据取自**响应**（后端披露的展开标志/原因），不是前端猜的。 -->
      <p
        v-if="props.modelUsageExpanded"
        class="model-cost-note"
        data-testid="stats-model-cost-note"
      >
        {{ t('sessions.stats.modelCostNote') }}
        <span v-if="props.modelUsageExpansionReason" class="model-cost-note__reason">{{ props.modelUsageExpansionReason }}</span>
      </p>
      <div v-if="modelUsage.length" class="table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('sessions.stats.model') }}</th>
              <th>{{ t('sessions.stats.sessionCount') }}</th>
              <th>{{ t('sessions.stats.requestCount') }}</th>
              <th>{{ t('sessions.stats.costUsd') }}</th>
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
              <th>{{ t('sessions.stats.costUsd') }}</th>
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
              <th>{{ t('sessions.stats.costUsd') }}</th>
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
/* ★ F1：重复计数披露 —— 与表头同色系的小字，别抢数字的注意力，但不能没有。 */
.model-cost-note {
  margin: 0 0 10px;
  color: var(--muted, var(--text-secondary));
  font-size: 12px;
  line-height: 1.6;
}
.model-cost-note__reason { color: var(--muted, var(--text-secondary)); }

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
