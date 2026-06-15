<script setup lang="ts">
import { computed } from 'vue'
import type { AnalyticsMatrix, AnalyticsMetric } from '../../api-autoroute'

const props = defineProps<{
  data: AnalyticsMatrix | null
  metric: AnalyticsMetric
  colAliases?: Record<string, string[]>
  loading?: boolean
  minHeight?: number
}>()

const emit = defineEmits<{
  cellClick: [row: string, col: string, value: number]
}>()

const flatValues = computed(() => {
  if (!props.data?.cells?.length) return []
  return props.data.cells.flat().filter(v => v > 0)
})

const maxVal = computed(() => Math.max(...flatValues.value, 1))
const minVal = computed(() => {
  const vals = flatValues.value
  return vals.length ? Math.min(...vals) : 0
})

function cellColor(value: number): string {
  if (!value || value <= 0) return 'transparent'
  const t = maxVal.value === minVal.value
    ? 0.5
    : (value - minVal.value) / (maxVal.value - minVal.value)
  const pct = Math.round(t * 100)
  return `color-mix(in srgb, var(--accent) ${12 + pct * 0.55}%, var(--bg-subtle))`
}

function fmtValue(value: number): string {
  if (!value) return ''
  switch (props.metric) {
    case 'success_rate':
      return (value * 100).toFixed(0) + '%'
    case 'p95_ms':
      return value < 1000 ? Math.round(value) + 'ms' : (value / 1000).toFixed(1) + 's'
    case 'cost_usd':
      return '$' + value.toFixed(value < 0.01 ? 4 : 2)
    default:
      return String(Math.round(value))
  }
}

function colTitle(col: string): string {
  const aliases = props.colAliases?.[col]
  if (!aliases?.length) return col
  const extras = aliases.filter(a => a !== col)
  if (!extras.length) return col
  return `${col} (别名: ${extras.join(', ')})`
}

const metricLabel = computed(() => {
  const m: Record<AnalyticsMetric, string> = {
    count: '请求数',
    success_rate: '成功率',
    p95_ms: 'P95 延迟',
    cost_usd: '费用 (USD)',
  }
  return m[props.metric]
})

const isEmpty = computed(() =>
  !props.loading && (!props.data || !props.data.rows.length || !props.data.cols.length)
)
</script>

<template>
  <div class="heatmap-wrap" :style="minHeight ? { minHeight: minHeight + 'px' } : undefined">
    <div v-if="loading" class="heatmap-hint">加载热力图…</div>
    <div v-else-if="isEmpty" class="heatmap-hint">暂无矩阵数据 — 等待 Auto 路由流量写入 request_logs</div>
    <div v-else class="table-wrap">
      <table class="heatmap-table">
        <thead>
          <tr>
            <th class="corner">{{ metricLabel }}</th>
            <th v-for="col in data!.cols" :key="col" class="col-head" :title="colTitle(col)">{{ col }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(row, ri) in data!.rows" :key="row">
            <th class="row-head" :title="row">{{ row }}</th>
            <td
              v-for="(col, ci) in data!.cols"
              :key="col"
              class="heat-cell"
              :class="{ clickable: data!.cells[ri][ci] > 0 }"
              :style="{ background: cellColor(data!.cells[ri][ci]) }"
              :title="`${row} × ${col}\n${metricLabel}: ${fmtValue(data!.cells[ri][ci]) || '0'}`"
              @click="data!.cells[ri][ci] > 0 && emit('cellClick', row, col, data!.cells[ri][ci])"
            >
              {{ fmtValue(data!.cells[ri][ci]) }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.heatmap-wrap { width: 100%; }
.heatmap-hint {
  padding: 16px;
  text-align: center;
  color: var(--muted);
  font-size: 11px;
}
.heatmap-table {
  width: 100%;
  border-collapse: separate;
  border-spacing: 0;
  font-size: 10px;
}
.heatmap-table th,
.heatmap-table td {
  border: 1px solid var(--border);
  padding: 3px 5px;
  text-align: center;
  font-variant-numeric: tabular-nums;
}
.corner {
  font-size: 9px;
  color: var(--muted);
  background: var(--bg-subtle);
  min-width: 72px;
}
.col-head, .row-head {
  font-size: 9px;
  font-weight: 500;
  color: var(--muted);
  background: var(--bg-subtle);
  max-width: 100px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.row-head { text-align: left; }
.heat-cell {
  min-width: 44px;
  transition: background 0.12s;
}
.heat-cell.clickable { cursor: pointer; }
.heat-cell.clickable:hover {
  outline: 1px solid var(--accent);
  outline-offset: -1px;
}
</style>
