<script setup lang="ts">
/**
 * QueueWaterfallTimeline — 9-stage dispatch lifecycle waterfall (ECharts custom series).
 */
import { ref, computed, watch, onMounted, onBeforeUnmount, nextTick } from 'vue'
import * as echarts from 'echarts'
import type { WaterfallRequest } from '../api/dispatch'
import { WATERFALL_STAGES, resolveStageBars, emptyStateMessage } from '../utils/waterfallTimeline'

const props = defineProps<{
  requests: WaterfallRequest[]
  loading?: boolean
  height?: number
  wired?: boolean
  source?: string
}>()

const emit = defineEmits<{
  select: [req: WaterfallRequest]
}>()

const chartRef = ref<HTMLDivElement>()
let chart: echarts.ECharts | null = null
const destroyed = ref(false)

function msColor(ms: number): string {
  if (ms < 1000) return 'var(--kx-success)'
  if (ms < 3000) return 'var(--kx-warning)'
  return 'var(--kx-danger)'
}

const emptyText = computed(() =>
  emptyStateMessage({ wired: props.wired, source: props.source, count: props.requests.length }),
)

const resolved = computed(() =>
  props.requests.map((r) => ({ request: r, bars: resolveStageBars(r) })),
)

const yLabels = computed(() =>
  props.requests.map((r) => {
    const id = r.request_id.length > 12 ? r.request_id.slice(0, 10) + '…' : r.request_id
    const model = r.model ? ` · ${r.model}` : ''
    return `${id}${model}`
  }),
)

const timeBounds = computed(() => {
  let min = Number.POSITIVE_INFINITY
  let max = Number.NEGATIVE_INFINITY
  for (const row of resolved.value) {
    for (const b of row.bars) {
      if (b.start < min) min = b.start
      if (b.end > max) max = b.end
    }
  }
  if (!Number.isFinite(min) || !Number.isFinite(max)) {
    const now = Date.now()
    return { min: now - 5000, max: now }
  }
  if (max <= min) max = min + 1000
  const pad = Math.max(50, (max - min) * 0.05)
  return { min: min - pad, max: max + pad }
})

type BarDatum = {
  name: string
  value: [number, number, number, number, string, string, number]
  request: WaterfallRequest
}

const seriesData = computed(() => {
  const data: BarDatum[] = []
  resolved.value.forEach((row, yIdx) => {
    for (const b of row.bars) {
      data.push({
        name: b.label,
        value: [yIdx, b.start, b.end, b.end - b.start, b.color, b.label, b.ms],
        request: row.request,
      })
    }
  })
  return data
})

function renderItem(_params: unknown, api: any) {
  const yIdx = Number(api.value(0))
  const start = api.coord([api.value(1), yIdx]) as number[]
  const end = api.coord([api.value(2), yIdx]) as number[]
  const size = api.size([0, 1]) as number[] | number
  const height = Array.isArray(size) ? size[1] : 18
  const barH = Math.max(8, height * 0.55)
  const x = start[0]
  const width = Math.max(2, end[0] - start[0])
  const y = start[1] - barH / 2
  const color = String(api.value(4))
  return {
    type: 'rect' as const,
    shape: { x, y, width, height: barH, r: 2 },
    style: { fill: color, opacity: 0.92 },
  }
}

const chartOption = computed(() => {
  const { min, max } = timeBounds.value
  return {
    animation: false,
    tooltip: {
      trigger: 'item' as const,
      backgroundColor: 'var(--kx-surface)',
      borderColor: 'var(--kx-border)',
      textStyle: { color: 'var(--kx-text)', fontSize: 12 },
      formatter: (p: any) => {
        const v = p?.value
        if (!v) return ''
        const label = v[5] ?? p.name
        const ms = v[6] ?? 0
        const req: WaterfallRequest | undefined = p?.data?.request
        return [
          `<b>${label}</b>: ${ms} ms`,
          req ? `req: ${req.request_id}` : '',
          req?.model ? `model: ${req.model}` : '',
          req?.result ? `result: ${req.result}` : '',
          req ? `queue: ${req.queue_wait_ms} ms · total: ${req.total_ms} ms` : '',
        ].filter(Boolean).join('<br/>')
      },
    },
    grid: { left: 160, right: 24, top: 28, bottom: 40 },
    legend: {
      top: 0,
      data: WATERFALL_STAGES.map((s) => s.label),
      textStyle: { color: 'var(--kx-muted)', fontSize: 11 },
    },
    xAxis: {
      type: 'time' as const,
      min,
      max,
      axisLabel: {
        color: 'var(--kx-muted)',
        formatter: (val: number) => {
          const d = new Date(val)
          const pad = (n: number) => String(n).padStart(2, '0')
          return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
        },
      },
      axisLine: { lineStyle: { color: 'var(--kx-border)' } },
      splitLine: { show: true, lineStyle: { color: 'var(--kx-border)', type: 'dashed' as const, opacity: 0.5 } },
    },
    yAxis: {
      type: 'category' as const,
      data: yLabels.value,
      inverse: true,
      axisLabel: { color: 'var(--kx-text)', fontSize: 11, width: 150, overflow: 'truncate' as const },
      axisLine: { show: false },
      axisTick: { show: false },
      splitLine: { show: false },
    },
    dataZoom: [
      { type: 'inside' as const, xAxisIndex: 0, filterMode: 'none' as const },
      { type: 'slider' as const, xAxisIndex: 0, height: 18, bottom: 8, borderColor: 'var(--kx-border)', fillerColor: 'rgba(64,158,255,0.15)' },
    ],
    series: WATERFALL_STAGES.map((st) => ({
      name: st.label,
      type: 'custom' as const,
      renderItem,
      encode: { x: [1, 2], y: 0 },
      data: seriesData.value
        .filter((d) => d.name === st.label)
        .map((d) => ({ ...d, itemStyle: { color: st.color } })),
      itemStyle: { color: st.color },
    })),
  }
})

function init() {
  if (!chartRef.value || destroyed.value) return
  if (chart) {
    chart.dispose()
    chart = null
  }
  chart = echarts.init(chartRef.value)
  chart.setOption(chartOption.value, true)
  chart.on('click', (params: any) => {
    const req = params?.data?.request as WaterfallRequest | undefined
    if (req) emit('select', req)
  })
}

function update() {
  if (!chart || destroyed.value) return
  chart.setOption(chartOption.value, true)
  if (props.loading) chart.showLoading('default', { text: '', color: '#409EFF', maskColor: 'transparent' })
  else chart.hideLoading()
}

function onResize() {
  chart?.resize()
}

watch(() => props.requests, () => nextTick(update), { deep: true })
watch(() => props.loading, () => update())

onMounted(() => {
  init()
  window.addEventListener('resize', onResize)
})

onBeforeUnmount(() => {
  destroyed.value = true
  window.removeEventListener('resize', onResize)
  chart?.dispose()
  chart = null
})
</script>

<template>
  <div class="qwt">
    <div v-if="!requests.length && !loading" class="qwt-empty">{{ emptyText }}</div>
    <div
      ref="chartRef"
      class="qwt-chart"
      :style="{ height: (height || Math.max(280, requests.length * 36 + 80)) + 'px', display: requests.length ? 'block' : 'none' }"
    />
    <div class="qwt-legend-hint">
      <span v-for="st in WATERFALL_STAGES" :key="st.key" class="qwt-chip">
        <i :style="{ background: st.color }" />{{ st.label }}
      </span>
      <span class="qwt-hint">缺时间戳时按 ms 合成条带 · 颜色：&lt;1s 绿 · 1–3s 黄 · &gt;3s 红</span>
    </div>
    <ul v-if="requests.length" class="qwt-summary">
      <li v-for="r in requests.slice(0, 8)" :key="r.request_id" @click="emit('select', r)">
        <code>{{ r.request_id.slice(0, 12) }}</code>
        <span class="model">{{ r.model || '—' }}</span>
        <span class="ms" :style="{ color: msColor(r.queue_wait_ms) }">queue {{ r.queue_wait_ms }}ms</span>
        <span class="ms" :style="{ color: msColor(r.upstream_latency_ms) }">ttfb {{ r.upstream_latency_ms }}ms</span>
        <span class="ms" :style="{ color: msColor(r.total_ms) }">total {{ r.total_ms }}ms</span>
        <span class="result">{{ r.result }}</span>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.qwt {
  background: var(--kx-surface);
  border: 1px solid var(--kx-border);
  border-radius: var(--radius, 8px);
  padding: 12px;
}
.qwt-chart { width: 100%; min-height: 240px; }
.qwt-empty {
  text-align: center;
  color: var(--kx-muted);
  padding: 48px 12px;
  font-size: 13px;
}
.qwt-legend-hint {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  align-items: center;
  margin-top: 8px;
  font-size: 12px;
  color: var(--kx-muted);
}
.qwt-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--kx-text);
}
.qwt-chip i {
  width: 10px;
  height: 10px;
  border-radius: 2px;
  display: inline-block;
}
.qwt-hint { margin-left: auto; opacity: 0.85; }
.qwt-summary {
  list-style: none;
  margin-top: 12px;
  border-top: 1px dashed var(--kx-border);
  padding-top: 8px;
  display: grid;
  gap: 4px;
}
.qwt-summary li {
  display: grid;
  grid-template-columns: 110px 1fr repeat(3, auto) auto;
  gap: 10px;
  align-items: center;
  font-size: 12px;
  padding: 4px 6px;
  border-radius: 4px;
  cursor: pointer;
  color: var(--kx-text);
}
.qwt-summary li:hover { background: var(--kx-bg-accent); }
.qwt-summary code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--kx-primary); }
.qwt-summary .model { color: var(--kx-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.qwt-summary .result { color: var(--kx-muted); text-transform: lowercase; }
</style>
