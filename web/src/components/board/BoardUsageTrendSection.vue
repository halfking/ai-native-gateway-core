<script setup lang="ts">
// BoardUsageTrendSection.vue — 用量趋势卡（2026-10-02 按模型分线轮）。
// 数据按模型拆分（top 6 + 长尾折叠线），指标（请求数/Token/积分/成本）单选过滤；
// 序列来自 /api/admin/usage/trend-series（rollup 级 dim_minute，跟随看板时间范围），
// 不进 board 缓存载荷 —— 卡片自取数，时间范围变化即刷；board 轮询刷新时按
// 最小间隔节流跟随。全页分析入口按钮在 BoardFilterBar 时间范围右侧。
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElRadioButton, ElRadioGroup } from 'element-plus'
import ModelTrendChart from '../analytics/ModelTrendChart.vue'
import type { BoardPayload } from '../../api/board'
import { getUsageTrendSeries, type UsageTrendMetric, type UsageTrendModelSeries } from '../../api/usage'
import { toBoardTimeQuery, formatBoardRangeLabel, boardTrendBucketMinutes, type BoardTimeRange } from '../../utils/boardTimeRange'
import { USAGE_TREND_METRICS, usageTrendMetricLabelKey } from '../../utils/usageTrend'

const props = defineProps<{
  board: BoardPayload | null | undefined
  timeRange: BoardTimeRange
  loading?: boolean
}>()

const { t } = useI18n()

const CARD_TOP_MODELS = 6
// board 轮询 ~10s 一轮；序列卡跟随但至少间隔 60s，避免每次轮询都打一遍 dim 聚合。
const SERIES_REFRESH_MIN_MS = 60_000

const metric = ref<UsageTrendMetric>('requests')
const series = ref<UsageTrendModelSeries[]>([])
const bucketMinutes = ref(5)
const seriesLoading = ref(false)
const seriesError = ref<string | null>(null)
/**
 * 趋势序列降级（服务端没算出来，而不是「这段时间真的零用量」）。
 *
 * 2026-10-03 补：`usage_trend_series.go` 的两个端点早就恒发
 * `degraded` / `missing_view`（T1 第一批就加了），类型里也声明了，
 * 但这一格只读 `resp.series ?? []` —— 降级时空数组被**当真值**，
 * 图表画成一条零线，用户读到「这段时间一点用量都没有」。
 * 与饼图逐维度降级、错误下钻失败、credits 降级显示 0 全是同族：
 * **把「不知道」渲染成「知道」比不显示更糟**。
 *
 * 与 seriesError 的区别：那是**请求失败**（HTTP 错误），
 * 这是**请求成功但数据源缺失**（200 + degraded）。两者要分开显示。
 */
const seriesDegraded = ref(false)
const seriesMissingView = ref<string>('')

const periodLabel = computed(() => formatBoardRangeLabel(props.timeRange, t))

const bucketLabel = computed(() => {
  const mins = boardTrendBucketMinutes(props.timeRange)
  if (mins >= 60) return t('dashboard.board.granHour')
  return t('dashboard.board.granMinutes', { n: mins })
})

let loadToken = 0
let lastLoadedAt = 0

async function loadSeries(throttle = false) {
  if (throttle && Date.now() - lastLoadedAt < SERIES_REFRESH_MIN_MS) return
  const token = ++loadToken
  seriesLoading.value = true
  seriesError.value = null
  seriesDegraded.value = false
  seriesMissingView.value = ''
  try {
    const resp = await getUsageTrendSeries({ time: toBoardTimeQuery(props.timeRange), top: CARD_TOP_MODELS })
    if (token !== loadToken) return
    series.value = resp.series ?? []
    bucketMinutes.value = resp.bucket_minutes || boardTrendBucketMinutes(props.timeRange)
    // 降级标记与空序列**必须分开读**：degraded 为真时空序列是
    // 「没算出来」，不是「零用量」。判据只认服务端自报的 degraded，
    // 不从数组长度推断 —— 那正是 T1 要挡的那个形状。
    seriesDegraded.value = resp.degraded === true
    seriesMissingView.value = resp.missing_view || ''
    lastLoadedAt = Date.now()
  } catch (err) {
    if (token !== loadToken) return
    series.value = []
    seriesDegraded.value = false
    seriesMissingView.value = ''
    seriesError.value = err instanceof Error ? err.message : String(err)
  } finally {
    if (token === loadToken) seriesLoading.value = false
  }
}

// 时间范围变化立即重取；board 轮询载荷变化时节流跟随（SSE 实时增量不含按模型序列）。
watch(() => props.timeRange, () => void loadSeries(false), { deep: true })
watch(() => props.board, () => void loadSeries(true))
onMounted(() => void loadSeries(false))
</script>

<template>
  <section class="trend-sec">
    <div class="trend-sec__head">
      <div class="trend-sec__titlewrap">
        <h5 class="trend-sec__title">{{ t('dashboard.board.trendTitle') }}</h5>
        <span class="trend-sec__cs">{{ periodLabel }} · {{ bucketLabel }}</span>
      </div>
      <el-radio-group v-model="metric" size="small" class="trend-sec__metric">
        <el-radio-button v-for="m in USAGE_TREND_METRICS" :key="m" :value="m">
          {{ t(usageTrendMetricLabelKey(m)) }}
        </el-radio-button>
      </el-radio-group>
    </div>
    <ModelTrendChart
      :series="series"
      :metric="metric"
      :bucket-minutes="bucketMinutes"
      :loading="seriesLoading"
      :height="268"
    />
    <!--
      降级与「请求失败」是两件不同的事，必须分开显示：
      seriesError = HTTP 失败；seriesDegraded = 200 但数据源缺失。
      两者都没显示的话，图表上的零线会被读成「这段时间零用量」。
    -->
    <div v-if="seriesDegraded" class="trend-sec__degraded" role="status">
      {{ t('usageTrend.degraded', { view: seriesMissingView }) }}
    </div>
    <div v-if="seriesError" class="trend-sec__err">{{ t('usageTrend.loadFailed') }}：{{ seriesError }}</div>
  </section>
</template>

<style scoped>
.trend-sec {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 13px 15px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}
.trend-sec__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
  flex-wrap: wrap;
}
.trend-sec__titlewrap {
  display: flex;
  align-items: baseline;
  gap: 10px;
  flex-wrap: wrap;
}
.trend-sec__title {
  font-size: 13.5px;
  font-weight: 700;
  margin: 0;
}
.trend-sec__cs {
  font-size: 11px;
  color: var(--text-muted);
}
.trend-sec__metric {
  flex-shrink: 0;
}
.trend-sec__err {
  font-size: 11px;
  color: var(--danger);
}
/* 降级用 warning 而非 danger：请求成功、数据源缺失。
   刻意区别于 err（请求失败）——两者的排查方向完全不同。 */
.trend-sec__degraded {
  font-size: 11px;
  color: var(--warning);
  line-height: 1.5;
}
</style>
