<script setup lang="ts">
// ErrorTrendPanel — 供应商错误趋势面板（R12 候选18 UI 接入）。
// 挂载于凭据监控页 tab（/routing-v2/credentials?tab=error-trend）。
// 数据来自 GET /api/errors/trend：supplier_error_stats 预聚合优先，
// 聚合器刚部署/窗口太新时后端自动回退 supplier_errors_unified 实时聚合。
// 时间分布用纯 CSS 柱状条渲染，不引入图表库依赖。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getErrorsTrend,
  type ErrorsTrendHours,
  type ErrorsTrendResponse,
} from '../../api/errors-trend'
import { formatDateTime, formatCompactDateTime } from '../../utils/datetime'

const { t } = useI18n()
const et = (key: string): string => t(`errorTrend.${key}`)

const hours = ref<ErrorsTrendHours>('24')
const loading = ref(false)
const error = ref('')
const data = ref<ErrorsTrendResponse | null>(null)
let requestSequence = 0
let requestController: AbortController | null = null

const summary = computed(() => data.value?.summary ?? null)
const series = computed(() => data.value?.time_series ?? [])
const maxCount = computed(() => series.value.reduce((m, p) => Math.max(m, p.error_count), 0))
const hasData = computed(() => series.value.length > 0 && (summary.value?.total_errors ?? 0) > 0)

const sourceLabel = computed(() => {
  if (!data.value) return ''
  return data.value.source === 'fallback' ? et('sourceFallback') : et('sourceStats')
})

function barHeight(count: number): string {
  if (maxCount.value <= 0) return '0%'
  return `${Math.max(4, Math.round((count / maxCount.value) * 100))}%`
}

async function loadData() {
  const sequence = ++requestSequence
  requestController?.abort()
  requestController = null
  const controller = new AbortController()
  requestController = controller
  loading.value = true
  error.value = ''
  try {
    const result = await getErrorsTrend(hours.value, { signal: controller.signal })
    if (sequence === requestSequence) data.value = result
  } catch (err: unknown) {
    if (sequence !== requestSequence || controller.signal.aborted) return
    data.value = null
    error.value = err instanceof Error ? err.message : et('loadFailed')
  } finally {
    if (sequence === requestSequence) {
      loading.value = false
      requestController = null
    }
  }
}

watch(hours, loadData)
onMounted(loadData)
onBeforeUnmount(() => {
  requestSequence++
  requestController?.abort()
  requestController = null
})
</script>

<template>
  <div class="error-trend-panel">
    <div class="toolbar">
      <span class="panel-title">{{ et('title') }}</span>
      <div class="toolbar-controls">
        <select v-model="hours" class="cf-select" :disabled="loading" :title="et('windowTitle')">
          <option value="1">{{ et('hours1') }}</option>
          <option value="24">{{ et('hours24') }}</option>
          <option value="168">{{ et('hours168') }}</option>
        </select>
        <button type="button" class="refresh-btn" :disabled="loading" @click="loadData">{{ et('refresh') }}</button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="loading && !data" class="empty-hint">{{ et('loading') }}</div>

    <template v-if="data && !loading">
      <div class="summary-grid">
        <div class="summary-item"><span>{{ et('totalErrors') }}</span><strong>{{ summary?.total_errors ?? 0 }}</strong></div>
        <div class="summary-item"><span>{{ et('uniqueRequests') }}</span><strong>{{ summary?.unique_requests ?? 0 }}</strong></div>
        <div class="summary-item"><span>{{ et('affectedCredentials') }}</span><strong>{{ summary?.affected_credentials ?? 0 }}</strong></div>
        <div class="summary-item"><span>{{ et('dataSource') }}</span><strong>{{ sourceLabel }}</strong></div>
      </div>

      <template v-if="hasData">
        <section class="section-block">
          <h3>{{ et('timeline') }} <span class="granularity">({{ data.granularity }})</span></h3>
          <div class="trend-bars">
            <div
              v-for="point in series"
              :key="point.timestamp"
              class="trend-bar-col"
              :title="`${formatDateTime(point.timestamp)} — ${point.error_count}`"
            >
              <div class="trend-bar" :style="{ height: barHeight(point.error_count) }"></div>
              <span class="trend-bar-label">{{ formatCompactDateTime(point.timestamp) }}</span>
              <span class="trend-bar-count">{{ point.error_count }}</span>
            </div>
          </div>
        </section>

        <div class="breakdown-row">
          <section class="section-block">
            <h3>{{ et('topErrorTypes') }}</h3>
            <table v-if="summary?.top_error_types?.length" class="data-table">
              <thead><tr><th>{{ et('errorType') }}</th><th>{{ et('count') }}</th></tr></thead>
              <tbody><tr v-for="row in summary.top_error_types" :key="row.key"><td>{{ row.key }}</td><td>{{ row.count }}</td></tr></tbody>
            </table>
            <div v-else class="empty-hint">{{ et('noData') }}</div>
          </section>
          <section class="section-block">
            <h3>{{ et('topSuppliers') }}</h3>
            <table v-if="summary?.top_suppliers?.length" class="data-table">
              <thead><tr><th>{{ et('supplier') }}</th><th>{{ et('count') }}</th></tr></thead>
              <tbody><tr v-for="row in summary.top_suppliers" :key="row.key"><td>{{ row.key }}</td><td>{{ row.count }}</td></tr></tbody>
            </table>
            <div v-else class="empty-hint">{{ et('noData') }}</div>
          </section>
        </div>
      </template>
      <div v-else class="empty-hint">{{ et('noData') }}</div>
    </template>
  </div>
</template>

<style scoped>
.error-trend-panel { font-size: 12px; }
.toolbar { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.toolbar-controls { display: flex; gap: 8px; align-items: center; }
.panel-title, h3 { font-size: 14px; font-weight: 600; }
.granularity { color: var(--muted); font-weight: 400; font-size: 12px; }
.refresh-btn {
  padding: 4px 10px; border: 1px solid var(--border-color); border-radius: var(--radius);
  background: var(--card-bg); color: inherit; cursor: pointer; font: inherit;
}
.refresh-btn:disabled { opacity: 0.6; cursor: default; }
.summary-grid { display: flex; gap: 12px; align-items: stretch; flex-wrap: wrap; margin-bottom: 12px; }
.summary-item { min-width: 130px; padding: 10px 12px; border: 1px solid var(--border-color); background: var(--card-bg); }
.summary-item span { display: block; color: var(--muted); margin-bottom: 4px; }
.section-block { margin-top: 12px; }
.trend-bars { display: flex; gap: 4px; align-items: flex-end; min-height: 140px; padding: 8px 0; overflow-x: auto; }
.trend-bar-col { display: flex; flex-direction: column; align-items: center; min-width: 34px; flex: 1 0 34px; }
.trend-bar { width: 70%; min-height: 2px; background: var(--danger, #d64545); border-radius: 2px 2px 0 0; }
.trend-bar-label { margin-top: 4px; color: var(--muted); font-size: 10px; white-space: nowrap; }
.trend-bar-count { color: inherit; font-size: 11px; font-weight: 600; }
.breakdown-row { display: flex; gap: 16px; flex-wrap: wrap; }
.breakdown-row > .section-block { flex: 1 1 280px; margin-top: 12px; }
.data-table { width: 100%; font-size: 12px; }
.empty-hint { color: var(--muted); text-align: center; padding: 24px; }
@media (max-width: 768px) { .summary-item { flex: 1 1 42%; } }
</style>
