<script setup lang="ts">
/**
 * ReconciliationCharts.vue —— 对帐页图表区（2026-09-29 多维筛选轮）
 *
 * 三张图，全部由服务端一次 GROUPING SETS 聚合的结果驱动（前端不再发请求）：
 *   ① 请求量与失败率趋势 —— 双轴折线，回答「这周哪天开始不对劲」
 *   ② 每天各模型的量     —— 堆叠柱（Top N 模型 + 其它），对帐人最常用的那张图
 *   ③ 错误类型 Top N     —— 横向条，回答「主要是谁在报错」
 *
 * 为什么模型图封顶 N 个系列：区间可到 366 天、模型名实测 527 个，全量堆叠
 * 既看不清也拖慢浏览器。封顶后其余归入「其它」，图例标注真实覆盖数，
 * 不让「只画了 Top 8」这件事静默发生。
 */
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import * as echarts from 'echarts'
import type { EChartsOption } from 'echarts'
import { ElCard, ElEmpty } from 'element-plus'
import { pickCoveredDays } from './coveredDays'

export interface ChartDay {
  date: string
  totals: {
    request_count: number
    error_rate: number
    success_count: number
    error_count: number
    estimated_cost_cents: number
    currency: string
  }
}

export interface ChartDayModel {
  date: string
  raw_model_name: string
  totals: { request_count: number }
}

const props = defineProps<{
  days: ChartDay[]
  dailyModels: ChartDayModel[]
  errorBreakdown: Record<string, number>
  topModels?: number
  /**
   * 真正有快照的日期集合。传入时把不在集合里的日期从趋势图剔除。
   *
   * 为什么必须有：后端为了「每天明细」表格连续，会把区间内还没聚合的日期补一行
   * 0。趋势图直接画这行 0 会在区间末日造成一条断崖——看着像流量崩了，其实
   * 那天的 rollup 还没跑。页面默认 end=今天，所以这条假断崖在生产上每天都会出现。
   * 传空/undefined 表示不裁剪（保留旧的「全画」行为，供无 coverage 的调用方用）。
   */
  coveredDates?: string[]
}>()

const { t } = useI18n()

const MAX_SERIES = computed(() => props.topModels ?? 8)

const trendRef = ref<HTMLElement | null>(null)
const modelRef = ref<HTMLElement | null>(null)
const errorRef = ref<HTMLElement | null>(null)

let trendChart: echarts.ECharts | null = null
let modelChart: echarts.ECharts | null = null
let errorChart: echarts.ECharts | null = null
let ro: ResizeObserver | null = null
let disposed = false

const hasDays = computed(() => props.days.some((d) => (d.totals?.request_count ?? 0) > 0))

/** 选请求量最大的 N 个模型，其余归入「其它」；返回 [保留名集合, 是否折叠]。 */
const modelSeries = computed<{ names: string[]; folded: number }>(() => {
  const total = new Map<string, number>()
  for (const d of props.dailyModels) {
    total.set(d.raw_model_name, (total.get(d.raw_model_name) ?? 0) + (d.totals?.request_count ?? 0))
  }
  const sorted = [...total.entries()].sort((a, b) => b[1] - a[1])
  const names = sorted.slice(0, MAX_SERIES.value).map(([n]) => n)
  return { names, folded: Math.max(0, sorted.length - names.length) }
})

const otherLabel = computed(() => t('reports.chartOther', '其它'))

/** 趋势图实际绘制用的天：剔除「区间内还没聚合」的补零日（见 coveredDays.ts）。 */
const trendDays = computed<ChartDay[]>(() => pickCoveredDays(props.days, props.coveredDates))

function trendOption(): EChartsOption {
  return {
    tooltip: { trigger: 'axis' },
    // 图例放底部：放顶部会与 y 轴的 name 抢同一条垂直空间（实测压字）。
    // 底部同时要抬 grid.bottom，否则反过来压住 x 轴日期标签。
    legend: { bottom: 0, left: 'center' },
    grid: { left: 48, right: 56, top: 32, bottom: 60 },
    xAxis: { type: 'category', data: trendDays.value.map((d) => d.date) },
    yAxis: [
      { type: 'value', name: t('reports.requests', '请求数') },
      { type: 'value', name: t('reports.errorRate', '失败率'), axisLabel: { formatter: (v: number) => `${(v * 100).toFixed(0)}%` } },
    ],
    series: [
      {
        name: t('reports.requests', '请求数'),
        type: 'line',
        smooth: true,
        areaStyle: { opacity: 0.15 },
        itemStyle: { color: '#409eff' },
        data: trendDays.value.map((d) => d.totals?.request_count ?? 0),
      },
      {
        name: t('reports.errorRate', '失败率'),
        type: 'line',
        yAxisIndex: 1,
        smooth: true,
        itemStyle: { color: '#f56c6c' },
        data: trendDays.value.map((d) => d.totals?.error_rate ?? 0),
      },
    ],
  }
}

function modelOption(): EChartsOption {
  const { names, folded } = modelSeries.value
  const dates = [...new Set(props.dailyModels.map((d) => d.date))].sort()
  const byDateModel = new Map<string, number>()
  for (const d of props.dailyModels) {
    const k = `${d.date}\u0000${d.raw_model_name}`
    byDateModel.set(k, (byDateModel.get(k) ?? 0) + (d.totals?.request_count ?? 0))
  }
  const series = names.map((name) => ({
    name,
    type: 'bar' as const,
    stack: 'requests',
    emphasis: { focus: 'series' as const },
    data: dates.map((date) => byDateModel.get(`${date}\u0000${name}`) ?? 0),
  }))
  if (folded > 0) {
    series.push({
      name: otherLabel.value,
      type: 'bar' as const,
      stack: 'requests',
      data: dates.map((date) => {
        let sum = 0
        for (const d of props.dailyModels) {
          if (d.date !== date || names.includes(d.raw_model_name)) continue
          sum += d.totals?.request_count ?? 0
        }
        return sum
      }),
    } as (typeof series)[number])
  }
  return {
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    legend: { type: 'scroll', top: 0 },
    // grid.top 必须 > 图例高度 + y 轴 name 的 nameGap(15)，否则轴名会画进图例里
    // （实测「请求数」压在 deepseek-v3 图例文字上）。
    grid: { left: 48, right: 16, top: 56, bottom: 44 },
    xAxis: { type: 'category', data: dates, axisLabel: { rotate: dates.length > 14 ? 40 : 0 } },
    yAxis: { type: 'value', name: t('reports.requests', '请求数') },
    series,
  }
}

function errorOption(): EChartsOption {
  const entries = Object.entries(props.errorBreakdown ?? {})
    .filter(([, v]) => v > 0)
    .sort((a, b) => b[1] - a[1])
    .slice(0, 10)
    .reverse()
  return {
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    grid: { left: 140, right: 32, top: 16, bottom: 24 },
    xAxis: { type: 'value' },
    yAxis: { type: 'category', data: entries.map(([k]) => k) },
    series: [
      {
        type: 'bar',
        itemStyle: { color: '#e6a23c' },
        label: { show: true, position: 'right' },
        data: entries.map(([, v]) => v),
      },
    ],
  }
}

function render() {
  if (disposed) return
  const pairs: [HTMLElement | null, echarts.ECharts | null, () => EChartsOption][] = [
    [trendRef.value, trendChart, trendOption],
    [modelRef.value, modelChart, modelOption],
    [errorRef.value, errorChart, errorOption],
  ]
  for (const [el, chart, opt] of pairs) {
    if (!el) continue
    const inst = chart ?? echarts.init(el)
    inst.setOption(opt(), true)
    if (!chart) {
      if (el === trendRef.value) trendChart = inst
      else if (el === modelRef.value) modelChart = inst
      else errorChart = inst
    }
  }
}

function observeResize() {
  const targets = [trendRef.value, modelRef.value, errorRef.value].filter(Boolean) as HTMLElement[]
  if (!targets.length || typeof ResizeObserver === 'undefined') return
  ro = new ResizeObserver(() => {
    trendChart?.resize()
    modelChart?.resize()
    errorChart?.resize()
  })
  targets.forEach((el) => ro?.observe(el))
}

onMounted(() => {
  render()
  observeResize()
})

watch(
  () => [props.days, props.dailyModels, props.errorBreakdown],
  () => render(),
  { deep: true },
)

onUnmounted(() => {
  disposed = true
  ro?.disconnect()
  trendChart?.dispose()
  modelChart?.dispose()
  errorChart?.dispose()
})
</script>

<template>
  <div class="charts">
    <el-card shadow="never" class="chart-card">
      <template #header>{{ t('reports.chartTrend', '请求量与失败率趋势') }}</template>
      <div v-show="hasDays" ref="trendRef" class="chart" />
      <el-empty v-if="!hasDays" :description="t('reports.noData', '暂无数据')" :image-size="60" />
    </el-card>

    <el-card shadow="never" class="chart-card">
      <template #header>
        <span>{{ t('reports.chartByModel', '每天各模型的量') }}</span>
        <span v-if="modelSeries.folded > 0" class="chart-note">
          {{ t('reports.chartFolded', '仅显示请求量前') }} {{ MAX_SERIES }} {{ t('reports.chartFoldedRest', '个模型，其余归入「其它」') }}
        </span>
      </template>
      <div v-show="dailyModels.length > 0" ref="modelRef" class="chart" />
      <el-empty v-if="dailyModels.length === 0" :description="t('reports.noData', '暂无数据')" :image-size="60" />
    </el-card>

    <el-card shadow="never" class="chart-card">
      <template #header>{{ t('reports.chartErrors', '主要错误类型 Top 10') }}</template>
      <div v-show="Object.keys(errorBreakdown || {}).length > 0" ref="errorRef" class="chart" />
      <el-empty v-if="Object.keys(errorBreakdown || {}).length === 0" :description="t('reports.noErrors', '无失败记录')" :image-size="60" />
    </el-card>
  </div>
</template>

<style scoped>
.charts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(420px, 1fr));
  gap: 16px;
  margin-bottom: 16px;
}
.chart {
  height: 280px;
  width: 100%;
}
/* 3 张卡在两列 auto-fit 网格下会让第 3 张的右半屏整片留白；让它跨满整行。
   单列窄屏下 grid-column: 1 / -1 等价于不生效。 */
.charts > :nth-child(3) {
  grid-column: 1 / -1;
}
.chart-note {
  margin-left: 8px;
  font-size: 12px;
  font-weight: 400;
  color: var(--el-text-color-secondary);
}
</style>
