<script setup lang="ts">
// ReconciliationReport.vue — 对账报表（2026-09-25 落地轮；2026-09-30 统计 UI 优化轮重排版）
// 供应商对帐（全流量/成本口径）与内部对帐（业务流量/积分+内部价口径）双视角。
// 数据来自每日凌晨自动聚合的 report_snapshots，区间查询不回扫原始日志。
//
// 本轮对标数据看板的信息架构重排（视觉全走 --kx-* 令牌）：
//   KPI 主副指标卡 ×6 → 双趋势图（请求成本 / Token 构成+命中率）→
//   分布表（占比条 + 按 Token/按金额切换 + 行点击下钻）→ 失败原因徽章化 →
//   按人员（internal）→ 按天明细（可折叠）。
// 后端 summary 的 provider_id/tenant_id/model 过滤参数此前一直存在，本轮首次接线。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
// 2026-09-28 修复：本仓 main.ts 不做 ElementPlus 全局注册（也无 unplugin 自动
// 导入），模板里的 el-* 必须在 <script setup> 显式 import，否则生产构建里
// resolveComponent 静默失败、组件退化为未知标签 —— el-table 列插槽被
// normalizeChildren 以无参调用，`{ row }` 解构 undefined 直接把页面打白
// （element:check 审计此前已标红本文件 7 项）。与 ClientAnalyticsView 等
// 正常页面的写法对齐。
import {
  ElAlert,
  ElButton,
  ElMessage,
  ElOption,
  ElRadioButton,
  ElRadioGroup,
  ElSelect,
  ElTable,
  ElTableColumn,
} from 'element-plus'
import { Download, Refresh } from '@element-plus/icons-vue'
import { useRoute, useRouter } from 'vue-router'
import {
  downloadReportExport,
  getReportSummary,
  runReportRollup,
  type RangeReport,
  type ReportProviderRow,
  type ReportTenantRow,
  type ReportView,
} from '../../api/reportrollup'
import { chartColors, createComboChartConfig, useChart } from '../../composables/useChart'
import StatCard from '../../components/ui/StatCard.vue'
import BarCell from '../../components/ui/BarCell.vue'
import KxDateRangePicker from '../../components/ui/KxDateRangePicker.vue'
import type { KxDateRange } from '../../components/ui/kx-date-types'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const loading = ref(false)
const exporting = ref(false)
const rerunning = ref(false)
// 2026-09-26 审计轮：支持 ?view=internal 深链（「租户用户→结算报表」菜单
// 入口直开内部视角）。非法值回落 provider，与后端 reportViewFilter 同口径。
const initialView = route.query.view === 'internal' ? 'internal' : 'provider'
const view = ref<ReportView>(initialView)

// 默认区间：昨日往前 7 天（今日快照 T+1 凌晨才生成）。
function fmtDay(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}
function defaultRange(): [string, string] {
  const end = new Date(Date.now() - 86400000)
  const start = new Date(Date.now() - 7 * 86400000)
  return [fmtDay(start), fmtDay(end)]
}
const range = ref<[string, string]>(defaultRange())

const report = ref<RangeReport | null>(null)
const errorText = ref('')
// 维度筛选（2026-09-30 新接线）：provider 视角筛供应商、internal 视角筛租户，
// 模型筛选双视角通用。选项列表从「未过滤的响应」缓存，避免过滤后选项塌缩成一项。
const filterProviderId = ref<number | null>(null)
const filterTenantId = ref<string>('')
const filterModel = ref<string>('')
const providerOptions = ref<Array<{ id: number; name: string }>>([])
const tenantOptions = ref<string[]>([])
const modelOptions = ref<string[]>([])
// 分布表指标切换（对标参考看板「按 Token / 按实际消费」）。
const metric = ref<'token' | 'money'>('token')
// 按天明细默认折叠（趋势图承载时序阅读）。
const showDays = ref(false)
// 失败原因联动：选中某原因时，模型表只保留 error_breakdown 含该原因的行。
const selectedReason = ref<string>('')

const hasData = computed(() => !!report.value && report.value.snapshot_dates.length > 0)

// 分组表行：provider/internal 两种行形按视角二选一。列插槽按 view 的 v-if
// 分支只访问对应行形的字段，运行时安全；这里收窄成单一联合别名供 ElTable
// 泛型推断（provider 字段在 internal 视角不渲染，交叉为可选即可通过类型检查）。
type GroupRow = ReportProviderRow & Partial<ReportTenantRow>
const groupRows = computed<GroupRow[]>(() => {
  const src: ReportProviderRow[] | ReportTenantRow[] | undefined =
    view.value === 'provider' ? report.value?.providers : report.value?.tenants
  return (src ?? []) as GroupRow[]
})

// ── 快捷区间 ─────────────────────────────────────────────
function quickYesterday() {
  const y = fmtDay(new Date(Date.now() - 86400000))
  range.value = [y, y]
  void refresh()
}
function quickDays(n: number) {
  range.value = defaultRangeFor(n)
  void refresh()
}
function defaultRangeFor(n: number): [string, string] {
  const end = new Date(Date.now() - 86400000)
  const start = new Date(Date.now() - n * 86400000)
  return [fmtDay(start), fmtDay(end)]
}
function quickThisMonth() {
  const now = new Date()
  const monthStart = new Date(now.getFullYear(), now.getMonth(), 1)
  const end = new Date(Date.now() - 86400000)
  const start = monthStart > end ? end : monthStart
  range.value = [fmtDay(start), fmtDay(end)]
  void refresh()
}

// 2026-09-30 统一日历轮：daterange 控件换 KxDateRangePicker（快捷 chips 保留，
// 面板纯自定义；报表窗口惯例截止昨日，预设语义交由 chips 表达）。
const kxRange = computed<KxDateRange>(() => ({ start: range.value[0], end: range.value[1] }))
function onKxRangeApply(value: KxDateRange) {
  range.value = [value.start, value.end]
  void refresh()
}

// ── 筛选 ────────────────────────────────────────────────
const filterActive = computed(
  () =>
    filterProviderId.value != null ||
    filterTenantId.value !== '' ||
    filterModel.value !== '',
)
function clearFilters() {
  filterProviderId.value = null
  filterTenantId.value = ''
  filterModel.value = ''
  selectedReason.value = ''
  void refresh()
}
function onFilterChange() {
  selectedReason.value = ''
  void refresh()
}
function switchView(v: string | number | boolean | undefined) {
  view.value = v === 'internal' ? 'internal' : 'provider'
  // 视角切换后维度语义不同（供应商↔租户），整体重置。
  filterProviderId.value = null
  filterTenantId.value = ''
  filterModel.value = ''
  providerOptions.value = []
  tenantOptions.value = []
  modelOptions.value = []
  selectedReason.value = ''
  void refresh()
}
// 行点击下钻：分布表行 → 锁定该维度过滤重查；模型表行 → 锁定模型。
function onDistRowClick(row: unknown) {
  const r = row as GroupRow
  if (view.value === 'provider') drillProvider(r)
  else drillTenant(r)
}
function onModelRowClick(row: unknown) {
  drillModel((row as { raw_model_name: string }).raw_model_name)
}
function drillProvider(row: GroupRow) {
  if (view.value !== 'provider') return
  if (filterProviderId.value === row.provider_id) return
  filterProviderId.value = row.provider_id
  onFilterChange()
}
function drillTenant(row: GroupRow) {
  if (view.value !== 'internal') return
  const tid = row.tenant_id
  if (!tid || filterTenantId.value === tid) return
  filterTenantId.value = tid
  onFilterChange()
}
function drillModel(model: string) {
  if (filterModel.value === model) return
  filterModel.value = model
  onFilterChange()
}

// ── 数据加载 ────────────────────────────────────────────
async function refresh() {
  if (!range.value || range.value.length !== 2) return
  loading.value = true
  errorText.value = ''
  try {
    const res = await getReportSummary({
      start: range.value[0],
      end: range.value[1],
      view: view.value,
      provider_id: filterProviderId.value ?? undefined,
      tenant_id: filterTenantId.value || undefined,
      model: filterModel.value || undefined,
    })
    report.value = res
    cacheFilterOptions(res)
    syncQuery()
  } catch (e: any) {
    errorText.value = e?.message ?? String(e)
    report.value = null
  } finally {
    loading.value = false
  }
}

// 过滤选项只从「该维度未激活」的响应取，保证选项始终是全集。
function cacheFilterOptions(res: RangeReport) {
  if (view.value === 'provider') {
    if (filterProviderId.value == null && res.providers) {
      providerOptions.value = res.providers.map((p) => ({ id: p.provider_id, name: p.provider_name || String(p.provider_id) }))
    }
  } else {
    if (!filterTenantId.value && res.tenants) {
      tenantOptions.value = res.tenants.map((x) => x.tenant_id)
    }
  }
  if (!filterModel.value) {
    const names = res.models.map((m) => m.raw_model_name)
    // 与既有选项并集去重，跨区间切换不丢失。
    modelOptions.value = Array.from(new Set([...modelOptions.value, ...names])).sort()
  }
}

// 筛选状态同步进 URL query（支持分享/刷新保真；replace 不污染历史）。
// 只回写本页管辖的四个键，不带出无关 query。
function syncQuery() {
  const query: Record<string, string> = {}
  if (view.value === 'internal') query.view = 'internal'
  if (filterProviderId.value != null) query.provider_id = String(filterProviderId.value)
  if (filterTenantId.value) query.tenant_id = filterTenantId.value
  if (filterModel.value) query.model = filterModel.value
  router.replace({ query }).catch(() => { /* duplicated navigation 忽略 */ })
}
// 挂载时从深链恢复筛选（菜单入口只带 view，老深链不受影响）。
function restoreFiltersFromQuery() {
  const pid = Number(route.query.provider_id)
  if (view.value === 'provider' && Number.isFinite(pid) && pid > 0) filterProviderId.value = pid
  if (view.value === 'internal' && typeof route.query.tenant_id === 'string' && route.query.tenant_id) {
    filterTenantId.value = route.query.tenant_id
  }
  if (typeof route.query.model === 'string' && route.query.model) filterModel.value = route.query.model
}

async function exportXlsx() {
  if (!range.value || range.value.length !== 2) return
  exporting.value = true
  try {
    await downloadReportExport({
      start: range.value[0],
      end: range.value[1],
      view: view.value,
      provider_id: filterProviderId.value ?? undefined,
      tenant_id: filterTenantId.value || undefined,
      model: filterModel.value || undefined,
    })
  } catch (e: any) {
    ElMessage.error(`${t('common.exportFailed', '导出失败')}: ${e?.message ?? e}`)
  } finally {
    exporting.value = false
  }
}

async function rerunYesterday() {
  rerunning.value = true
  try {
    const res = await runReportRollup(range.value?.[1] ?? '')
    ElMessage.success(`${t('reports.rerunDone', '重跑完成')}: ${res.date} rows=${res.rows_written}`)
    await refresh()
  } catch (e: any) {
    ElMessage.error(`${t('reports.rerunFailed', '重跑失败')}: ${e?.message ?? e}`)
  } finally {
    rerunning.value = false
  }
}

// ── 格式化 ──────────────────────────────────────────────
function fmtInt(n: number | undefined | null): string {
  return (n ?? 0).toLocaleString('en-US')
}
function fmtTokensCompact(n: number | undefined | null): string {
  const v = n ?? 0
  if (v >= 1e9) return `${(v / 1e9).toFixed(2)}B`
  if (v >= 1e6) return `${(v / 1e6).toFixed(2)}M`
  if (v >= 1e3) return `${(v / 1e3).toFixed(1)}K`
  return String(v)
}
function fmtMoneyFromCents(cents: number | undefined | null): string {
  return ((cents ?? 0) / 100).toFixed(2)
}
function fmtPct(v: number | undefined | null): string {
  return `${((v ?? 0) * 100).toFixed(2)}%`
}
function fmtRatio(v: number | null | undefined): string {
  return v == null ? '-' : `${(v * 100).toFixed(1)}%`
}
function fmtSec(ms: number | undefined | null): string {
  if (ms == null) return '-'
  return ms >= 10000 ? `${(ms / 1000).toFixed(0)}s` : `${(ms / 1000).toFixed(1)}s`
}
function fmtAxisCompact(v: number): string {
  if (Math.abs(v) >= 1e9) return `${(v / 1e9).toFixed(1)}B`
  if (Math.abs(v) >= 1e6) return `${(v / 1e6).toFixed(1)}M`
  if (Math.abs(v) >= 1e3) return `${(v / 1e3).toFixed(0)}K`
  return String(Math.round(v))
}
const coverageText = computed(() => {
  if (!report.value) return ''
  const days = report.value.snapshot_dates.length
  return `${days} ${t('reports.daysCovered', '天快照')}`
})

// ── KPI（对标参考看板：主值 + 副指标） ──────────────────
const totals = computed(() => report.value?.totals)
// 质量评分按请求量加权（providers 行现算分，区间汇总口径）。
const qualityAvg = computed(() => {
  const rows = report.value?.providers ?? []
  let w = 0
  let acc = 0
  for (const p of rows) {
    acc += (p.quality_score ?? 0) * p.totals.request_count
    w += p.totals.request_count
  }
  return w > 0 ? acc / w : null
})
const daysCount = computed(() => report.value?.snapshot_dates.length ?? 0)
const dailyAvg = computed(() =>
  daysCount.value > 0 ? Math.round((totals.value?.request_count ?? 0) / daysCount.value) : null,
)
const personCount = computed(() => report.value?.persons?.length ?? 0)
const creditsPerReq = computed(() => {
  const req = totals.value?.request_count ?? 0
  const credits = totals.value?.credits_charged ?? 0
  return req > 0 ? credits / req : null
})

// ── 趋势图 ──────────────────────────────────────────────
const dayLabels = computed(() => (report.value?.days ?? []).map((d) => d.date.slice(5)))
const hasDays = computed(() => (report.value?.days ?? []).length > 0)

const reqChartConfig = computed(() => {
  const days = report.value?.days ?? []
  const moneyData =
    view.value === 'provider'
      ? days.map((d) => (d.totals.estimated_cost_cents ?? 0) / 100)
      : days.map((d) => d.totals.credits_charged ?? 0)
  return createComboChartConfig(
    'bar',
    dayLabels.value,
    [
      {
        label: t('reports.legendSuccess', '成功'),
        data: days.map((d) => d.totals.success_count),
        backgroundColor: chartColors.blue + 'cc',
        borderColor: chartColors.blue,
        stack: 'req',
      },
      {
        label: t('reports.legendFail', '失败'),
        data: days.map((d) => d.totals.error_count),
        backgroundColor: chartColors.red + 'cc',
        borderColor: chartColors.red,
        stack: 'req',
      },
      {
        label: view.value === 'provider' ? t('reports.legendCost', '成本 (USD)') : t('reports.legendCredits', '积分'),
        data: moneyData,
        type: 'line',
        yAxisID: 'y1',
        borderColor: chartColors.orange,
        backgroundColor: 'transparent',
        pointRadius: 2,
      },
    ],
    {
      scales: {
        y: { stacked: true, beginAtZero: true },
        y1: { position: 'right', beginAtZero: true, grid: { drawOnChartArea: false }, ticks: { callback: (v: string | number) => fmtAxisCompact(Number(v)) } },
      },
    },
  )
})

const tokChartConfig = computed(() => {
  const days = report.value?.days ?? []
  return createComboChartConfig(
    'line',
    dayLabels.value,
    [
      {
        label: t('reports.legendIn', '输入'),
        data: days.map((d) => d.totals.input_tokens),
        borderColor: chartColors.blue,
        backgroundColor: 'rgba(64, 158, 255, 0.28)',
        fill: true,
        stack: 'tok',
      },
      {
        label: t('reports.legendOut', '输出'),
        data: days.map((d) => d.totals.output_tokens),
        borderColor: chartColors.green,
        backgroundColor: 'rgba(103, 194, 58, 0.28)',
        fill: true,
        stack: 'tok',
      },
      {
        label: t('reports.legendCacheRead', '缓存读'),
        data: days.map((d) => d.totals.cache_read_tokens),
        borderColor: chartColors.cyan,
        backgroundColor: 'rgba(52, 152, 219, 0.28)',
        fill: true,
        stack: 'tok',
      },
      {
        label: t('reports.legendCacheWrite', '缓存写'),
        data: days.map((d) => d.totals.cache_write_tokens),
        borderColor: chartColors.purple,
        backgroundColor: 'rgba(155, 89, 182, 0.28)',
        fill: true,
        stack: 'tok',
      },
      {
        label: t('reports.legendHitRate', '命中率'),
        data: days.map((d) => (d.totals.cache_hit_ratio == null ? 0 : d.totals.cache_hit_ratio * 100)),
        yAxisID: 'y1',
        borderColor: chartColors.warning,
        backgroundColor: 'transparent',
        borderDash: [5, 4],
        pointRadius: 2,
      },
    ],
    {
      scales: {
        x: { stacked: true },
        y: { stacked: true, beginAtZero: true, ticks: { callback: (v: string | number) => fmtAxisCompact(Number(v)) } },
        y1: { position: 'right', beginAtZero: true, max: 100, grid: { drawOnChartArea: false }, ticks: { callback: (v: string | number) => `${Number(v).toFixed(0)}%` } },
      },
    },
  )
})

const reqCanvas = ref<HTMLCanvasElement | null>(null)
const tokCanvas = ref<HTMLCanvasElement | null>(null)
const { initChart: initReqChart, destroyChart: destroyReqChart, isDisposed: reqDisposed } = useChart(reqCanvas, reqChartConfig)
const { initChart: initTokChart, destroyChart: destroyTokChart, isDisposed: tokDisposed } = useChart(tokCanvas, tokChartConfig)

let alive = true
async function refreshCharts() {
  if (!alive) return
  if (!hasDays.value) {
    destroyReqChart()
    destroyTokChart()
    return
  }
  await nextTick()
  if (!alive) return
  if (!reqDisposed()) initReqChart()
  if (!tokDisposed()) initTokChart()
}
watch([reqChartConfig, tokChartConfig], () => void refreshCharts(), { deep: true })
onBeforeUnmount(() => {
  alive = false
})

// ── 分布表（占比条 + 指标切换） ─────────────────────────
// 指标口径：token → total_tokens；money → provider 视角成本 cents / internal 积分。
function metricValue(tot: { total_tokens: number; estimated_cost_cents: number; credits_charged: number }): number {
  if (metric.value === 'token') return tot.total_tokens
  return view.value === 'provider' ? tot.estimated_cost_cents : tot.credits_charged
}
const distSorted = computed(() => {
  const rows = [...groupRows.value]
  rows.sort((a, b) => metricValue(b.totals) - metricValue(a.totals))
  return rows
})
const distMaxMetric = computed(() =>
  distSorted.value.reduce((m, r) => Math.max(m, metricValue(r.totals)), 0),
)
const distTotalReq = computed(() => groupRows.value.reduce((a, r) => a + r.totals.request_count, 0))
// el-table 列插槽的 row 是 element-plus 的 DefaultRow（Record<string, any>），
// 结构化收窄到具体行形无法通过模板传参（本仓无 lint no-explicit-any 门禁，
// 沿用旧版页面"插槽内直接属性访问"的宽松约定）。
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function distPct(row: any): number {
  return distMaxMetric.value > 0 ? (metricValue(row.totals) / distMaxMetric.value) * 100 : 0
}

const modelSorted = computed(() => {
  let rows = [...(report.value?.models ?? [])]
  if (selectedReason.value) {
    rows = rows.filter((m) => (m.error_breakdown?.[selectedReason.value] ?? 0) > 0)
  }
  rows.sort((a, b) => metricValue(b.totals) - metricValue(a.totals))
  return rows
})
const modelMaxMetric = computed(() =>
  modelSorted.value.reduce((m, r) => Math.max(m, metricValue(r.totals)), 0),
)
// 同 distPct：DefaultRow 宽松收参（见上注释）。
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function modelPct(row: any): number {
  return modelMaxMetric.value > 0 ? (metricValue(row.totals) / modelMaxMetric.value) * 100 : 0
}

// ── 失败原因（徽章 + 占比条，联动模型表） ──────────────
const reasonList = computed<Array<[string, number]>>(() => {
  // 失败原因汇总在 RangeReport 顶层（非 totals 内），各分组行另有自己的 breakdown。
  const br: Record<string, number> = report.value?.error_breakdown ?? {}
  return Object.entries(br)
    .filter(([, v]) => v > 0)
    .sort((a, b) => b[1] - a[1]) as Array<[string, number]>
})
const reasonMax = computed(() => reasonList.value[0]?.[1] ?? 0)
const reasonTotal = computed(() => reasonList.value.reduce((a, [, v]) => a + v, 0))
function reasonBadgeClass(code: string): string {
  if (/429|rate/i.test(code)) return 'badge-yellow'
  if (/timeout|504/i.test(code)) return 'badge-red'
  if (/quota/i.test(code)) return 'badge-purple'
  if (/5\d\d|upstream/i.test(code)) return 'badge-red'
  return 'badge-gray'
}
function toggleReason(code: string) {
  selectedReason.value = selectedReason.value === code ? '' : code
}
function reasonBadges(br: Record<string, number> | undefined): Array<[string, number]> {
  if (!br) return []
  return Object.entries(br)
    .filter(([, v]) => v > 0)
    .sort((a, b) => b[1] - a[1])
    .slice(0, 2)
}

// ── 按人员（internal） ──────────────────────────────────
const personSorted = computed(() =>
  [...(report.value?.persons ?? [])].sort((a, b) => metricValue(b.totals) - metricValue(a.totals)),
)
const personMaxMetric = computed(() =>
  personSorted.value.reduce((m, r) => Math.max(m, metricValue(r.totals)), 0),
)

onMounted(() => {
  restoreFiltersFromQuery()
  void refresh()
})
</script>

<template>
  <div class="report-page">
    <div class="toolbar">
      <el-radio-group :model-value="view" @change="switchView">
        <el-radio-button value="provider">{{ t('reports.providerView', '供应商对帐') }}</el-radio-button>
        <el-radio-button value="internal">{{ t('reports.internalView', '内部对帐') }}</el-radio-button>
      </el-radio-group>
      <KxDateRangePicker
        :model-value="kxRange"
        :presets="[]"
        @apply="onKxRangeApply"
      />
      <div class="quick-chips">
        <button class="chip" type="button" @click="quickYesterday">{{ t('reports.quickYesterday', '昨天') }}</button>
        <button class="chip" type="button" @click="quickDays(7)">{{ t('reports.quick7d', '近 7 天') }}</button>
        <button class="chip" type="button" @click="quickDays(30)">{{ t('reports.quick30d', '近 30 天') }}</button>
        <button class="chip" type="button" @click="quickThisMonth">{{ t('reports.quickMonth', '本月') }}</button>
      </div>
      <el-select
        v-if="view === 'provider'"
        :model-value="filterProviderId"
        clearable
        filterable
        :placeholder="t('reports.allProviders', '全部供应商')"
        style="width: 170px"
        @update:model-value="(v: number | undefined) => { filterProviderId = v ?? null; onFilterChange() }"
      >
        <el-option v-for="p in providerOptions" :key="p.id" :value="p.id" :label="p.name" />
      </el-select>
      <el-select
        v-else
        :model-value="filterTenantId"
        clearable
        filterable
        :placeholder="t('reports.allTenants', '全部租户')"
        style="width: 170px"
        @update:model-value="(v: string | undefined) => { filterTenantId = v ?? ''; onFilterChange() }"
      >
        <el-option v-for="tt in tenantOptions" :key="tt" :value="tt" :label="tt" />
      </el-select>
      <el-select
        :model-value="filterModel"
        clearable
        filterable
        :placeholder="t('reports.allModels', '全部模型')"
        style="width: 200px"
        @update:model-value="(v: string | undefined) => { filterModel = v ?? ''; onFilterChange() }"
      >
        <el-option v-for="m in modelOptions" :key="m" :value="m" :label="m" />
      </el-select>
      <el-button v-if="filterActive" link type="primary" @click="clearFilters">
        {{ t('reports.clearFilter', '清除筛选') }}
      </el-button>
      <el-button type="primary" :icon="Refresh" :loading="loading" @click="refresh">
        {{ t('common.refresh', '刷新') }}
      </el-button>
      <el-button :icon="Download" :loading="exporting" @click="exportXlsx">
        {{ t('reports.exportExcel', '导出 Excel') }}
      </el-button>
      <el-button :loading="rerunning" @click="rerunYesterday">
        {{ t('reports.rerun', '重跑结束日') }}
      </el-button>
      <span v-if="coverageText" class="coverage">{{ coverageText }}</span>
    </div>

    <el-alert v-if="errorText" :title="errorText" type="error" show-icon :closable="false" class="block" />
    <el-alert
      v-else-if="report && !hasData"
      :title="t('reports.noSnapshots', '该区间没有报表快照（每日聚合任务在凌晨生成前一日数据，或用「重跑结束日」补算）')"
      type="info"
      show-icon
      :closable="false"
      class="block"
    />

    <template v-if="report">
      <!-- ══ KPI：主值 + 副指标（对标参考看板） ══ -->
      <div class="kpi-grid">
        <StatCard
          :label="t('reports.requests', '请求数')"
          :value="fmtInt(totals?.request_count)"
          icon="📥"
        >
          <template #sub>
            <span class="kpi-ok">{{ t('reports.success', '成功') }} {{ fmtInt(totals?.success_count) }}</span>
            <span class="kpi-sep">·</span>
            <span class="kpi-err">{{ t('reports.errors', '失败') }} {{ fmtInt(totals?.error_count) }}（{{ fmtPct(totals?.error_rate) }}）</span>
          </template>
        </StatCard>
        <StatCard
          :label="t('reports.totalTokens', '总 tokens')"
          :value="fmtTokensCompact(totals?.total_tokens)"
          icon="🔢"
        >
          <template #sub>
            {{ t('reports.in', '入') }} {{ fmtTokensCompact(totals?.input_tokens) }} ·
            {{ t('reports.out', '出') }} {{ fmtTokensCompact(totals?.output_tokens) }} ·
            {{ t('reports.cacheRead', '缓存读') }} {{ fmtTokensCompact(totals?.cache_read_tokens) }} ·
            {{ t('reports.cacheWrite', '缓存写') }} {{ fmtTokensCompact(totals?.cache_write_tokens) }}
          </template>
        </StatCard>
        <StatCard
          v-if="view === 'provider'"
          :label="t('reports.providerCost', '供应商成本')"
          :value="`$${fmtMoneyFromCents(totals?.estimated_cost_cents)}`"
          icon="💸"
          tone="success"
        >
          <template #sub>
            {{ totals?.currency || 'USD' }} ·
            {{ t('reports.cacheHit', '缓存命中') }} {{ fmtRatio(totals?.cache_hit_ratio) }}
          </template>
        </StatCard>
        <StatCard
          v-else
          :label="t('reports.internalCredits', '内部积分')"
          :value="fmtInt(totals?.credits_charged)"
          icon="🪙"
          tone="success"
        >
          <template #sub>
            {{ t('reports.internalCost', '内部金额') }} {{ fmtMoneyFromCents(totals?.internal_cost_cents) }}
            {{ totals?.internal_currency || 'CNY' }}
          </template>
        </StatCard>
        <StatCard
          v-if="view === 'provider'"
          :label="t('reports.qualityScore', '质量评分')"
          :value="qualityAvg != null ? qualityAvg.toFixed(1) : '-'"
          icon="🏅"
        >
          <template #sub>{{ (report.providers?.length ?? 0) }} {{ t('reports.qualitySub', '家供应商 · 成功率×时效因子') }}</template>
        </StatCard>
        <StatCard v-else icon="🏠" :label="t('reports.kpiTenants', '结算租户')">
          <template #value>{{ report.tenants?.length ?? 0 }}</template>
          <template #sub>{{ (report.tenants ?? []).slice(0, 3).map((x) => x.tenant_id).join(' · ') }}</template>
        </StatCard>
        <StatCard
          :label="t('reports.kpiLatency', '平均耗时')"
          :value="fmtSec(totals?.latency_p50_ms)"
          icon="⏱️"
        >
          <template #sub>P50 {{ fmtSec(totals?.latency_p50_ms) }} · P95 {{ fmtSec(totals?.latency_p95_ms) }}</template>
        </StatCard>
        <StatCard
          v-if="view === 'provider'"
          :label="t('reports.kpiDaily', '日均请求')"
          :value="dailyAvg != null ? fmtInt(dailyAvg) : '-'"
          icon="📈"
        >
          <template #sub>{{ t('reports.coveragePrefix', '覆盖') }} {{ daysCount }} {{ t('reports.daysCovered', '天快照') }}</template>
        </StatCard>
        <StatCard v-else icon="👤" :label="t('reports.kpiPersons', '活跃人员')">
          <template #value>{{ personCount }}</template>
          <template #sub>
            {{ t('reports.kpiPerPersonReq', '人均请求') }}
            {{ personCount > 0 ? fmtInt(Math.round((totals?.request_count ?? 0) / personCount)) : '-' }}
          </template>
        </StatCard>
        <StatCard
          v-if="view === 'internal'"
          :label="t('reports.kpiCreditsPerReq', '积分/请求')"
          :value="creditsPerReq != null ? creditsPerReq.toFixed(1) : '-'"
          icon="📊"
        >
          <template #sub>{{ t('reports.coveragePrefix', '覆盖') }} {{ daysCount }} {{ t('reports.daysCovered', '天快照') }}</template>
        </StatCard>
      </div>

      <!-- ══ 趋势：请求成本 / Token 构成（对标参考看板 Token 使用趋势） ══ -->
      <div v-if="hasDays" class="trend-grid">
        <div class="trend-card">
          <div class="trend-title">
            {{ view === 'provider' ? t('reports.reqCostTrend', '请求与成本趋势') : t('reports.reqCreditsTrend', '请求与积分趋势') }}
          </div>
          <div class="trend-body"><canvas ref="reqCanvas" /></div>
        </div>
        <div class="trend-card">
          <div class="trend-title">{{ t('reports.tokenTrend', 'Token 构成与缓存命中') }}</div>
          <div class="trend-body"><canvas ref="tokCanvas" /></div>
        </div>
      </div>

      <!-- ══ 分布：占比条 + 指标切换 ══ -->
      <div class="dist-header">
        <h3 class="section">
          {{ view === 'provider' ? t('reports.byProvider', '按供应商') : t('reports.byTenant', '按租户') }}
        </h3>
        <span class="dist-hint">{{ t('reports.distHint', '点击行下钻 · 占比条为当前指标占比') }}</span>
        <span class="spacer" />
        <div class="quick-chips">
          <button
            class="chip"
            :class="{ active: metric === 'token' }"
            type="button"
            @click="metric = 'token'"
          >{{ t('reports.byToken', '按 Token') }}</button>
          <button
            class="chip"
            :class="{ active: metric === 'money' }"
            type="button"
            @click="metric = 'money'"
          >{{ t('reports.byMoney', '按金额') }}</button>
        </div>
      </div>
      <div class="dist-grid">
        <el-table
          :data="distSorted"
          size="small"
          border
          stripe
          class="dist-table"
          row-class-name="row-click"
          @row-click="onDistRowClick"
        >
          <el-table-column v-if="view === 'provider'" :label="t('reports.provider', '供应商')" min-width="170">
            <template #default="{ row }">
              <BarCell :pct="distPct(row)">
                <template #name>
                  <span class="cell-main">{{ row.provider_name || row.provider_id }}</span>
                </template>
              </BarCell>
            </template>
          </el-table-column>
          <el-table-column v-else :label="t('reports.tenant', '租户')" min-width="170">
            <template #default="{ row }">
              <BarCell :pct="distPct(row)" tone="success">
                <template #name><span class="cell-main mono">{{ row.tenant_id }}</span></template>
              </BarCell>
            </template>
          </el-table-column>
          <el-table-column :label="t('reports.requests', '请求数')" width="110" align="right">
            <template #default="{ row }">
              <div>{{ fmtInt(row.totals.request_count) }}</div>
              <div class="cell-sub">{{ distTotalReq > 0 ? ((row.totals.request_count / distTotalReq) * 100).toFixed(1) : '0.0' }}%</div>
            </template>
          </el-table-column>
          <el-table-column :label="t('reports.totalTokens', '总 tokens')" width="120" align="right">
            <template #default="{ row }">{{ fmtTokensCompact(row.totals.total_tokens) }}</template>
          </el-table-column>
          <el-table-column v-if="view === 'provider'" :label="t('reports.cost', '成本 (USD)')" width="120" align="right">
            <template #default="{ row }">{{ fmtMoneyFromCents(row.totals.estimated_cost_cents) }} {{ row.totals.currency }}</template>
          </el-table-column>
          <el-table-column v-else :label="t('reports.internalCredits', '内部积分')" width="120" align="right">
            <template #default="{ row }">
              <div>{{ fmtInt(row.totals.credits_charged) }}</div>
              <div class="cell-sub">¥{{ fmtMoneyFromCents(row.totals.internal_cost_cents) }}</div>
            </template>
          </el-table-column>
          <el-table-column :label="t('reports.errors', '失败')" width="110" align="right">
            <template #default="{ row }">
              <span class="badge" :class="row.totals.error_rate > 0.1 ? 'badge-red' : row.totals.error_rate > 0.07 ? 'badge-yellow' : 'badge-green'">
                {{ fmtInt(row.totals.error_count) }}（{{ fmtPct(row.totals.error_rate) }}）
              </span>
            </template>
          </el-table-column>
          <el-table-column v-if="view === 'provider'" :label="t('reports.qualityScore', '质量')" width="90" align="right">
            <template #default="{ row }">{{ row.quality_score != null ? row.quality_score.toFixed(1) : '-' }}</template>
          </el-table-column>
          <el-table-column v-else :label="t('reports.sharePct', '占比')" width="90" align="right">
            <template #default="{ row }">
              {{ distTotalReq > 0 ? ((row.totals.request_count / distTotalReq) * 100).toFixed(1) : '0.0' }}%
            </template>
          </el-table-column>
        </el-table>

        <!-- 失败原因：徽章化 + 占比条，点击联动模型表 -->
        <div class="reason-card">
          <div class="reason-title">
            {{ t('reports.reasonTitle', '失败原因分布（区间 Top）') }}
            <span v-if="selectedReason" class="badge badge-blue reason-filter">{{ selectedReason }} ✕</span>
          </div>
          <div v-if="reasonList.length === 0" class="reason-empty">{{ t('reports.noErrors', '区间无失败请求') }}</div>
          <div
            v-for="[code, count] in reasonList.slice(0, 6)"
            :key="code"
            class="reason-row"
            :class="{ selected: selectedReason === code }"
            role="button"
            tabindex="0"
            @click="toggleReason(code)"
            @keyup.enter="toggleReason(code)"
          >
            <span class="badge mono reason-badge" :class="reasonBadgeClass(code)">{{ code }}</span>
            <span class="reason-track"><span class="reason-fill" :style="{ width: reasonMax > 0 ? `${(count / reasonMax) * 100}%` : '0%' }" /></span>
            <span class="reason-count mono">{{ fmtInt(count) }}</span>
          </div>
          <div v-if="reasonList.length" class="reason-total">
            {{ t('reports.reasonTotalPrefix', '合计') }} {{ fmtInt(reasonTotal) }} {{ t('reports.reasonTotalSuffix', '次失败') }}
            <template v-if="selectedReason"> · {{ t('reports.reasonFilterHint', '已按该原因过滤下方模型表') }}</template>
          </div>
        </div>
      </div>

      <!-- ══ 按模型 + 按人员 ══ -->
      <div class="dist-grid dist-grid--models">
        <div>
          <h3 class="section">
            {{ t('reports.byModel', '按模型') }}
            <span v-if="selectedReason" class="badge badge-blue section-badge">{{ selectedReason }}</span>
          </h3>
          <el-table
            :data="modelSorted"
            size="small"
            border
            stripe
            row-class-name="row-click"
            @row-click="onModelRowClick"
          >
            <el-table-column :label="t('reports.model', '模型')" min-width="230">
              <template #default="{ row }">
                <BarCell :pct="modelPct(row)">
                  <template #name>
                    <span class="mono cell-main">{{ row.raw_model_name }}</span>
                    <span v-if="row.provider_name" class="cell-sub">{{ row.provider_name }}</span>
                  </template>
                  <template v-if="reasonBadges(row.error_breakdown).length">
                    <span
                      v-for="[rc, rn] in reasonBadges(row.error_breakdown)"
                      :key="rc"
                      class="badge badge-outline reason-mini"
                    >{{ rc }} ×{{ rn }}</span>
                  </template>
                </BarCell>
              </template>
            </el-table-column>
            <el-table-column :label="t('reports.requests', '请求数')" width="100" align="right">
              <template #default="{ row }">{{ fmtInt(row.totals.request_count) }}</template>
            </el-table-column>
            <el-table-column :label="t('reports.totalTokens', '总 tokens')" width="110" align="right">
              <template #default="{ row }">{{ fmtTokensCompact(row.totals.total_tokens) }}</template>
            </el-table-column>
            <el-table-column v-if="view === 'provider'" :label="t('reports.cost', '成本 (USD)')" width="110" align="right">
              <template #default="{ row }">{{ fmtMoneyFromCents(row.totals.estimated_cost_cents) }}</template>
            </el-table-column>
            <el-table-column v-else :label="t('reports.internalCredits', '内部积分')" width="110" align="right">
              <template #default="{ row }">{{ fmtInt(row.totals.credits_charged) }}</template>
            </el-table-column>
            <el-table-column label="P95" width="80" align="right">
              <template #default="{ row }">{{ fmtSec(row.totals.latency_p95_ms) }}</template>
            </el-table-column>
            <el-table-column :label="t('reports.cacheHit', '缓存命中')" width="95" align="right">
              <template #default="{ row }">{{ fmtRatio(row.totals.cache_hit_ratio) }}</template>
            </el-table-column>
          </el-table>
        </div>
        <div v-if="view === 'internal'">
          <h3 class="section">{{ t('reports.byPerson', '按人员') }}</h3>
          <el-table :data="personSorted" size="small" border stripe>
            <el-table-column :label="t('reports.person', '人员')" min-width="170">
              <template #default="{ row }">
                <BarCell
                  :pct="personMaxMetric > 0 ? (metricValue(row.totals) / personMaxMetric) * 100 : 0"
                  tone="cyan"
                >
                  <template #name>
                    <span class="cell-main">{{ row.person }}</span>
                    <span class="cell-sub mono">{{ row.tenant_id }}</span>
                  </template>
                </BarCell>
              </template>
            </el-table-column>
            <el-table-column :label="t('reports.requests', '请求数')" width="100" align="right">
              <template #default="{ row }">{{ fmtInt(row.totals.request_count) }}</template>
            </el-table-column>
            <el-table-column :label="t('reports.totalTokens', '总 tokens')" width="110" align="right">
              <template #default="{ row }">{{ fmtTokensCompact(row.totals.total_tokens) }}</template>
            </el-table-column>
            <el-table-column :label="t('reports.internalCredits', '内部积分')" width="110" align="right">
              <template #default="{ row }">{{ fmtInt(row.totals.credits_charged) }}</template>
            </el-table-column>
            <el-table-column :label="t('reports.internalCost', '内部金额')" width="110" align="right">
              <template #default="{ row }">¥{{ fmtMoneyFromCents(row.totals.internal_cost_cents) }}</template>
            </el-table-column>
          </el-table>
        </div>
      </div>

      <!-- ══ 按天明细（可折叠） ══ -->
      <div class="days-header">
        <h3 class="section section--inline">{{ t('reports.byDay', '按天明细') }}</h3>
        <el-button link type="primary" @click="showDays = !showDays">
          {{ showDays ? t('reports.collapse', '收起') : t('reports.expand', '展开') }}
        </el-button>
      </div>
      <el-table v-show="showDays" :data="report.days" size="small" border stripe>
        <el-table-column prop="date" :label="t('reports.date', '日期')" width="120" />
        <el-table-column :label="t('reports.requests', '请求数')" width="110" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.request_count) }}</template>
        </el-table-column>
        <el-table-column :label="t('reports.success', '成功')" width="100" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.success_count) }}</template>
        </el-table-column>
        <el-table-column :label="t('reports.errors', '失败')" width="110" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.error_count) }}（{{ fmtPct(row.totals.error_rate) }}）</template>
        </el-table-column>
        <el-table-column :label="t('reports.totalTokens', '总 tokens')" width="120" align="right">
          <template #default="{ row }">{{ fmtTokensCompact(row.totals.total_tokens) }}</template>
        </el-table-column>
        <el-table-column v-if="view === 'provider'" :label="t('reports.cost', '成本 (USD)')" width="120" align="right">
          <template #default="{ row }">{{ fmtMoneyFromCents(row.totals.estimated_cost_cents) }} {{ row.totals.currency }}</template>
        </el-table-column>
        <el-table-column v-else :label="t('reports.internalCredits', '内部积分')" width="120" align="right">
          <template #default="{ row }">{{ fmtInt(row.totals.credits_charged) }}</template>
        </el-table-column>
      </el-table>
    </template>
  </div>
</template>

<style scoped>
.report-page {
  padding: 16px;
}
.toolbar {
  display: flex;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
  margin-bottom: 16px;
}
.coverage {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.block {
  margin-bottom: 16px;
}
.quick-chips {
  display: inline-flex;
  gap: 6px;
}
.chip {
  border: 1px solid var(--border);
  background: transparent;
  color: var(--muted);
  border-radius: 999px;
  padding: 3px 12px;
  font-size: 12.5px;
  cursor: pointer;
}
.chip:hover {
  color: var(--accent);
  border-color: var(--accent);
}
.chip.active {
  background: var(--bg-subtle);
  border-color: var(--accent);
  color: var(--accent);
  font-weight: 600;
}
.kpi-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}
.kpi-ok { color: var(--success); }
.kpi-err { color: var(--danger); }
.kpi-sep { margin: 0 4px; color: var(--muted); }

.trend-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 14px;
  margin-bottom: 4px;
}
@media (max-width: 1080px) {
  .trend-grid { grid-template-columns: 1fr; }
}
.trend-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px;
}
.trend-title {
  font-weight: 600;
  font-size: 14px;
  margin-bottom: 8px;
}
.trend-body {
  height: 260px;
  position: relative;
}
.trend-body canvas {
  width: 100% !important;
  height: 100% !important;
}

.dist-header {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 18px 0 8px;
  flex-wrap: wrap;
}
.dist-hint {
  color: var(--muted);
  font-size: 12px;
}
.spacer { flex: 1; }
.dist-grid {
  display: grid;
  grid-template-columns: 1.6fr 1fr;
  gap: 14px;
  margin-bottom: 4px;
  align-items: start;
}
.dist-grid--models {
  grid-template-columns: 1.6fr 1fr;
}
@media (max-width: 1080px) {
  .dist-grid, .dist-grid--models { grid-template-columns: 1fr; }
}
.section {
  margin: 18px 0 8px;
  font-size: 15px;
}
.section--inline { margin: 0; }
.section-badge { margin-left: 8px; font-weight: 400; }
.cell-main { font-weight: 600; }
.cell-sub {
  font-size: 11.5px;
  color: var(--muted);
}
:deep(.row-click) { cursor: pointer; }

.reason-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px 14px;
}
.reason-title {
  font-weight: 600;
  font-size: 14px;
  margin-bottom: 10px;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.reason-filter { cursor: pointer; }
.reason-empty {
  color: var(--muted);
  font-size: 13px;
  padding: 24px 0;
  text-align: center;
}
.reason-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 8px;
  cursor: pointer;
  padding: 2px 4px;
  border-radius: 6px;
}
.reason-row:hover { background: var(--row-hover); }
.reason-row.selected { background: var(--bg-subtle); }
.reason-badge {
  min-width: 130px;
  justify-content: center;
  font-size: 11px;
  text-align: center;
}
.reason-track {
  flex: 1;
  height: 6px;
  border-radius: 3px;
  background: var(--border);
  overflow: hidden;
  display: inline-block;
}
.reason-fill {
  display: block;
  height: 100%;
  border-radius: 3px;
  background: var(--danger);
}
.reason-count {
  min-width: 56px;
  text-align: right;
  font-size: 12px;
}
.reason-total {
  color: var(--muted);
  font-size: 12px;
  margin-top: 6px;
}
.reason-mini {
  font-size: 10.5px;
  font-weight: 400;
}
.days-header {
  display: flex;
  align-items: center;
  gap: 10px;
}
.mono {
  font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
  font-variant-numeric: tabular-nums;
}
</style>
