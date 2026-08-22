<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

defineProps<{
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
}>()

const { t } = useI18n()
const router = useRouter()

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
    <div v-if="modelUsage?.length" class="detail-card model-card">
      <div class="detail-card__header">{{ t('sessions.stats.topModels') }}</div>
      <div class="table-wrap">
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
              <td>{{ row.model }}</td>
              <td class="num">{{ row.session_count }}</td>
              <td class="num">{{ row.request_count }}</td>
              <td class="num">{{ formatUsd(row.total_cost ?? 0) }}</td>
              <td class="num">{{ ((row.success_rate ?? 0) * 100).toFixed(1) }}%</td>
              <td class="num">{{ Math.round(row.avg_latency_ms ?? 0) }}ms</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

  <div class="rankings-row">
    <div class="detail-card">
      <div class="detail-card__header">{{ t('sessions.stats.topClients') }}</div>
      <p v-if="!topClients.length" class="empty-hint">{{ t('dashboard.noData') }}</p>
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
      <p v-if="!topTasks.length" class="empty-hint">{{ t('dashboard.noData') }}</p>
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
@media (max-width: 900px) { .rankings-row { grid-template-columns: 1fr; } }
.detail-card {
  border: 1px solid var(--border, #e5e7eb);
  border-radius: 10px;
  background: var(--card, #fff);
  padding: 14px 16px;
  min-width: 0;
}
.detail-card__header { font-weight: 600; font-size: 14px; margin-bottom: 12px; }
.empty-hint { margin: 0; font-size: 13px; color: var(--muted, #909399); }
.table-wrap { overflow-x: auto; }
.data-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.data-table th, .data-table td { padding: 8px 10px; border-bottom: 1px solid var(--border, #f0f0f0); text-align: left; }
.data-table th { color: var(--muted, #909399); font-weight: 500; }
.data-table .num { text-align: right; font-variant-numeric: tabular-nums; }
.link-btn { border: 0; background: none; color: var(--accent, #409eff); cursor: pointer; padding: 0; font: inherit; }
.health-pill { display: inline-block; padding: 2px 6px; border-radius: 4px; font-size: 12px; background: #f3f4f6; }
.health-pill--success { background: #dcfce7; }
.health-pill--warning { background: #fef3c7; }
.health-pill--danger { background: #fee2e2; }
.model-card { margin-bottom: 16px; }
</style>
