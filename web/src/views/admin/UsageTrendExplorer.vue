<script setup lang="ts">
// UsageTrendExplorer.vue — 全屏用量趋势分析（2026-10-02 看板轮）。
// 看板「用量趋势」卡的「更多」入口（BoardFilterBar 时间范围右侧按钮）落到本页；
// 菜单「模型与路由」组亦挂入口。过滤维度：时间范围 / 供应商 / 租户（超管）/
// API Key / 模型（标准 ModelPicker 多选，与 stream 页同款 compact 展示）/ 指标
// （请求数、Token、积分、成本）。全部过滤条件同步 URL query（深链/分享），
// 「清除全部」一键复位；自动刷新（30s/1m/5m）定时重拉序列，同样入深链。
// 图表按模型分线（top 10 + 长尾折叠，模型多选已定时服务端不折叠），下表为
// 模型汇总。路由 meta.fillViewport 让本页铺满主区。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { surfaceError } from '../../i18n/errorSurface'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  ElButton,
  ElOption,
  ElRadioButton,
  ElRadioGroup,
  ElSelect,
  ElTable,
  ElTableColumn,
} from 'element-plus'
import ModelTrendChart from '../../components/analytics/ModelTrendChart.vue'
import ModelPicker from '../../components/ModelPicker.vue'
import KxDateRangePicker from '../../components/ui/KxDateRangePicker.vue'
import { makeDateRangePresets, systemTzDateStr, systemTzNoonMs } from '../../components/ui/kxDatePresets'
import type { KxDateRange } from '../../components/ui/kx-date-types'
import {
  getUsageTrendSeries,
  type UsageTrendMetric,
  type UsageTrendModelSeries,
  getTopRankings,
  type TopRankingsResponse,
} from '../../api/usage'
import { getProviders } from '../../api/providers'
import ProviderPicker from '../../components/ProviderPicker.vue'
import { getTenants, type TenantSummary } from '../../api/tenants'
import { getKeys, type ApiKey } from '../../api/keys'
import { isSuperAdmin } from '../../store'
import {
  USAGE_TREND_METRICS,
  USAGE_TREND_OTHERS,
  compactTickValue,
  usageTrendMetricLabelKey,
  usageTrendSeriesTotal,
} from '../../utils/usageTrend'

const PAGE_TOP_MODELS = 10

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

// ── 过滤状态（初始值从 URL 深链恢复） ──

const range = ref<KxDateRange | null>(readRangeFromQuery())
const providerId = ref<number | null>(readNumFromQuery('provider'))
const tenantId = ref<string | null>(typeof route.query.tenant === 'string' && route.query.tenant ? route.query.tenant : null)
const apiKeyId = ref<number | null>(readNumFromQuery('apikey'))
const models = ref<string[]>(readModelsFromQuery())
const metric = ref<UsageTrendMetric>(readMetricFromQuery())

function readModelsFromQuery(): string[] {
  // 深链 model 既支持单值（model=a）也支持多值（model=a&model=b，与请求层
  // usageTrendQs 的序列化对称）；数组元素再做一遍字符串防护。
  const raw = route.query.model
  const list = Array.isArray(raw) ? raw : (typeof raw === 'string' && raw ? [raw] : [])
  return list.filter((m): m is string => typeof m === 'string' && !!m)
}

function readRangeFromQuery(): KxDateRange | null {
  const start = typeof route.query.start === 'string' ? route.query.start : ''
  const end = typeof route.query.end === 'string' ? route.query.end : ''
  if (/^\d{4}-\d{2}-\d{2}$/.test(start) && /^\d{4}-\d{2}-\d{2}$/.test(end) && start <= end) {
    return { start, end }
  }
  return null
}

function readNumFromQuery(key: string): number | null {
  const raw = route.query[key]
  const n = typeof raw === 'string' ? Number(raw) : NaN
  return Number.isInteger(n) && n > 0 ? n : null
}

function readMetricFromQuery(): UsageTrendMetric {
  const raw = route.query.metric
  return typeof raw === 'string' && (USAGE_TREND_METRICS as string[]).includes(raw)
    ? (raw as UsageTrendMetric)
    : 'requests'
}

const showTenantFilter = isSuperAdmin()
const presets = computed(() => makeDateRangePresets('date'))

const effectiveRange = computed<KxDateRange>(() => range.value ?? defaultRange())

function defaultRange(): KxDateRange {
  // 时区对齐轮（2026-11-20）L-TZ1：改系统时区日历，与后端 ParseSystemDate 口径一致
  const today = systemTzDateStr()
  const start = systemTzDateStr(Date.now() - 6 * 86_400_000)
  return { start, end: today }
}

// ── 过滤器选项 ──

const tenants = ref<TenantSummary[]>([])
const keys = ref<ApiKey[]>([])

const keyLabel = (k: ApiKey) => {
  const name = k.key_alias || k.owner_user || k.application_code || `#${k.id}`
  return `${name} · ${k.key_prefix}…`
}

// 2026-10-03：原来两处 `catch { /* 不阻塞主图 */ }`。
// 「不阻塞主图」是对的，但代价是**下拉变空**：页面上「没有供应商可选」
// 与「供应商没加载出来」完全同形。用户选了供应商 A 却查不到数据，
// 结论会是「这段时间 A 没有用量」，而真相是下拉里压根没有 A。
// 「不阻塞」只该描述数据面板，不该顺带把筛选器变成一句假话。
const filterOptionsError = ref('')

async function loadFilterOptions() {
  const problems: string[] = []
  try {
    // 2026-11-20 供应商选择统一轮：providers 列表改由 ProviderPicker 内聚加载；
    // 这里保留 getProviders 探测（失败 ⇒ 筛选条降级提示），keys 仍归本页。
    const [p, k] = await Promise.all([getProviders(), getKeys()])
    void p
    keys.value = k ?? []
  } catch {
    keys.value = []
    problems.push(t('usageTrend.filterProviderKeyFailed'))
  }
  if (showTenantFilter) {
    try {
      tenants.value = (await getTenants()) ?? []
    } catch {
      tenants.value = []
      problems.push(t('usageTrend.filterTenantFailed'))
    }
  }
  filterOptionsError.value = problems.join('；')
}

// ── 数据加载 ──

const series = ref<UsageTrendModelSeries[]>([])
const bucketMinutes = ref(60)
const loading = ref(false)
const error = ref<string | null>(null)
const source = ref('')

// Top5 排行数据
const rankings = ref<TopRankingsResponse | null>(null)
const rankingsLoading = ref(false)
const rankingsError = ref<string | null>(null)

const timeQuery = computed(() => ({ start: effectiveRange.value.start, end: effectiveRange.value.end }))

let seriesToken = 0

async function loadSeries() {
  const token = ++seriesToken
  loading.value = true
  error.value = null
  try {
    const resp = await getUsageTrendSeries({
      time: timeQuery.value,
      tenant_id: tenantId.value || undefined,
      provider_id: providerId.value || undefined,
      api_key_id: apiKeyId.value || undefined,
      model: models.value.length ? models.value : undefined,
      top: PAGE_TOP_MODELS,
    })
    if (token !== seriesToken) return
    series.value = resp.series ?? []
    bucketMinutes.value = resp.bucket_minutes || 60
    source.value = resp.source ?? ''
  } catch (err) {
    if (token !== seriesToken) return
    series.value = []
    error.value = surfaceError(err)
  } finally {
    if (token === seriesToken) loading.value = false
  }
}

async function loadRankings() {
  rankingsLoading.value = true
  rankingsError.value = null
  try {
    const resp = await getTopRankings(timeQuery.value)
    rankings.value = resp
  } catch (err) {
    rankingsError.value = surfaceError(err)
  } finally {
    rankingsLoading.value = false
  }
}

watch(
  [effectiveRange, providerId, tenantId, apiKeyId, models],
  () => {
    void loadSeries()
    void loadRankings()
    syncQuery()
  },
)

function syncQuery() {
  const next: Record<string, string | string[]> = {
    start: effectiveRange.value.start,
    end: effectiveRange.value.end,
    metric: metric.value,
  }
  // truthy 防护：el-select 清空在不同 EP 版本产出 undefined/''/null，统一按
  // 「有值才写入」处理，避免空参数（provider=）残留在深链里。
  if (providerId.value) next.provider = String(providerId.value)
  if (showTenantFilter && tenantId.value) next.tenant = tenantId.value
  if (apiKeyId.value) next.apikey = String(apiKeyId.value)
  // 单选传 string、多选传数组（序列化为重复 model 参数）；与后端多选解析对称。
  if (models.value.length === 1) next.model = models.value[0]
  else if (models.value.length > 1) next.model = models.value
  if (autoRefreshSec.value > 0) next.auto = String(autoRefreshSec.value)
  // 全量重建 query：清空的过滤器必须从深链里消失，不能沿用旧值。
  router.replace({ query: next }).catch(() => undefined)
}

// ── 模型多选回填 / 清除全部条件 ──

// 与 stream 页 onModelFilterPicked 同款防护：非数组（single 形态）按单值包裹，
// 空串剔除；空数组 = 清空模型过滤。
function onModelsPicked(value: string | string[]) {
  models.value = Array.isArray(value) ? value.filter((v) => !!v) : [value].filter((v) => !!v)
}

// 「有条件」= 任一过滤偏离初始态。判式与 syncQuery 的写入条件（truthy）对称：
// el-select 清空在部分 EP 版本产出 ''，null 判断会把「已清空」误判为「有条件」。
const hasActiveFilters = computed(() =>
  range.value != null
  || !!providerId.value
  || (showTenantFilter && !!tenantId.value)
  || !!apiKeyId.value
  || models.value.length > 0
  || metric.value !== 'requests',
)

function clearAllFilters() {
  range.value = null
  providerId.value = null
  tenantId.value = null
  apiKeyId.value = null
  models.value = []
  metric.value = 'requests'
}

// ── 自动刷新 ──

const AUTO_REFRESH_CHOICES = [0, 30, 60, 300] as const

function readAutoRefreshFromQuery(): number {
  const raw = typeof route.query.auto === 'string' ? Number(route.query.auto) : NaN
  return (AUTO_REFRESH_CHOICES as readonly number[]).includes(raw) ? raw : 0
}

const autoRefreshSec = ref<number>(readAutoRefreshFromQuery())

let refreshTimer: ReturnType<typeof setInterval> | null = null

function applyRefreshTimer() {
  if (refreshTimer != null) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
  if (autoRefreshSec.value > 0) {
    // 只重拉数据，不改时间范围（窗口固定，窗口内新桶自然出现）。
    refreshTimer = setInterval(() => { void loadSeries() }, autoRefreshSec.value * 1000)
  }
}

watch(autoRefreshSec, () => {
  applyRefreshTimer()
  syncQuery()
})

// ── 汇总表 ──

// el-table 插槽的 row 是松类型（DefaultRow），这里用最小字段形状接收。
type UsageTrendTotalsRow = {
  total_requests?: number
  total_tokens?: number
  total_credits?: number
  total_cost_usd?: number
}

function rowMetricTotal(row: UsageTrendTotalsRow): number {
  switch (metric.value) {
    case 'requests': return row.total_requests ?? 0
    case 'tokens': return row.total_tokens ?? 0
    case 'credits': return row.total_credits ?? 0
    case 'cost': return row.total_cost_usd ?? 0
  }
}

const metricTotal = computed(() => series.value.reduce((sum, s) => sum + usageTrendSeriesTotal(s, metric.value), 0))

function sharePct(row: UsageTrendTotalsRow): string {
  if (metricTotal.value <= 0) return '—'
  return `${((rowMetricTotal(row) / metricTotal.value) * 100).toFixed(1)}%`
}

function rowClass({ row }: { row: Record<string, unknown> }): string {
  return row.model === USAGE_TREND_OTHERS ? 'ute__others-row' : ''
}

// 指标是纯前端选列，不触发重查，但要同步深链。
watch(metric, () => syncQuery())

// detail 档（按 API Key 过滤）超时引导：handler 的 45s 截止在 writeInternalErr
// 被折叠成固定 op 文案（真实 err 只进服务端日志），前端以
// 「按 Key 过滤 + 错误含 query failed」作为可判信号，提示用户缩短时间范围。
const detailQueryFailed = computed(() => {
  if (!apiKeyId.value || !error.value) return false
  return /query failed/i.test(error.value)
})

const sourceLabel = computed(() => {
  switch (source.value) {
    case 'request_stats_dim_minute':
    case 'request_stats_minute':
      return t('dashboard.board.sourcePg')
    case 'request_logs_with_current_month': return t('usageTrend.sourceDetail')
    default: return source.value || '—'
  }
})

// 2026-11-22 审计轮修正（P1-6）：此前推的路径是 '/admin/request-logs'，
// 而真路由是 '/request-logs'（web/src/router.ts）⇒ router.ts 的
// '/:pathMatch(.*)*' 兜底把它重定向到 '/' ⇒ 6 个「查看全部」按钮全部跳首页。
// 时间参数同理：RequestLogsView 只读 preset / hours / from+to，
// 此前推的 start/end **不被任何代码读取** ⇒ 时间范围丢失。
// ⇒ 改推真路由 + preset=custom + from/to（系统时区日界，ISO 瞬间）。
function requestLogsRangeQuery(): Record<string, string> {
  const { start, end } = effectiveRange.value
  const DAY_MS = 86_400_000
  // systemTzNoonMs 给的是该日正午的挂钟瞬间；减半天即当日 00:00 的瞬间。
  const startMs = systemTzNoonMs(start) - 12 * 3600_000
  const endMs = systemTzNoonMs(end) - 12 * 3600_000 + DAY_MS - 1
  return {
    preset: 'custom',
    from: new Date(startMs).toISOString(),
    to: new Date(endMs).toISOString(),
  }
}

// 导航到请求日志页面，带上对应的筛选条件
function navigateToRequests(dimension: string, value: string) {
  const query: Record<string, string> = requestLogsRangeQuery()

  // 根据维度类型设置对应的筛选参数
  switch (dimension) {
    case 'model':
      query.model = value
      break
    case 'client':
      query.client_model = value
      break
    case 'user':
      query.end_user_id = value
      break
    case 'apikey':
      // API Key 名称格式是 "prefix...alias/owner"，需要提取前缀
      const prefix = value.split('...')[0]
      query.api_key_prefix = prefix
      break
    case 'project':
      query.application_code = value
      break
    case 'task':
      query.task_type = value
      break
  }

  router.push({ path: '/request-logs', query })
}

// 导航到请求日志页面，不带具体值过滤（查看全部该维度的请求）
function navigateToRequestsAll(dimension: string) {
  // 只带时间范围，不带具体维度值，让用户在请求日志页面查看完整列表
  router.push({ path: '/request-logs', query: requestLogsRangeQuery() })
}

onMounted(() => {
  void loadFilterOptions()
  void loadSeries()
  void loadRankings()
  applyRefreshTimer()
})

onBeforeUnmount(() => {
  if (refreshTimer != null) clearInterval(refreshTimer)
})
</script>

<template>
  <div class="ute">
    <div class="ute__head">
      <div>
        <h2 class="ute__title">{{ t('usageTrend.pageTitle') }}</h2>
        <p class="ute__sub">{{ t('usageTrend.pageSub') }}</p>
      </div>
      <span class="ute__src" :title="t('dashboard.board.sourceTitle')">
        {{ t('dashboard.board.sourceLabel') }}：<b>{{ sourceLabel }}</b>
      </span>
    </div>

    <div class="ute__filters">
      <div class="ute__filter">
        <label class="ute__label">{{ t('dashboard.board.filterRange') }}</label>
        <KxDateRangePicker
          :model-value="effectiveRange"
          :presets="presets"
          :max-span-days="92"
          :disabled="loading"
          @apply="range = $event"
        />
      </div>
      <!-- 2026-10-03：筛选选项没加载出来时说清楚，别让空下拉冒充「没有供应商」 -->
      <div v-if="filterOptionsError" class="ute__filter-error" role="status">{{ filterOptionsError }}</div>
      <div class="ute__filter">
        <label class="ute__label">{{ t('usageTrend.filterProvider') }}</label>
        <!-- 2026-11-20 供应商选择统一轮：el-select 换全局 ProviderPicker（id 模式）。
             truthy 空值防护（clearable el-select 在不同 EP 版本产出 undefined/''/null）
             由 ProviderPicker 的 null 契约统一承载。 -->
        <ProviderPicker
          v-model="providerId"
          value-mode="id"
          :title="t('usageTrend.filterProvider')"
          class="ute__select"
        />
      </div>
      <div v-if="showTenantFilter" class="ute__filter">
        <label class="ute__label">{{ t('usageTrend.filterTenant') }}</label>
        <el-select
          v-model="tenantId"
          clearable
          filterable
          :placeholder="t('usageTrend.filterAll')"
          class="ute__select"
        >
          <el-option v-for="tn in tenants" :key="tn.tenant_id" :value="tn.tenant_id" :label="tn.tenant_id" />
        </el-select>
      </div>
      <div class="ute__filter">
        <label class="ute__label">{{ t('usageTrend.filterApiKey') }}</label>
        <el-select
          v-model="apiKeyId"
          clearable
          filterable
          :placeholder="t('usageTrend.filterAll')"
          class="ute__select ute__select--wide"
        >
          <el-option v-for="k in keys" :key="k.id" :value="k.id" :label="keyLabel(k)" />
        </el-select>
      </div>
      <div class="ute__filter ute__filter--model" :class="{ 'ute__filter--model-active': models.length > 0 }">
        <label class="ute__label">{{ t('usageTrend.filterModel') }}</label>
        <!-- 与 dashboard stream 页筛选行同款：ModelPicker multi compact，
             已选态触发器亮 accent，「已选 N 个模型」+ 计数徽标。 -->
        <ModelPicker
          mode="multi"
          compact
          :model-value="models"
          :placeholder="t('usageTrend.filterAll')"
          :title="t('usageTrend.filterModel')"
          @update:model-value="onModelsPicked"
        />
      </div>
      <div class="ute__filter">
        <label class="ute__label">{{ t('usageTrend.filterMetric') }}</label>
        <el-radio-group v-model="metric" size="small">
          <el-radio-button v-for="m in USAGE_TREND_METRICS" :key="m" :value="m">
            {{ t(usageTrendMetricLabelKey(m)) }}
          </el-radio-button>
        </el-radio-group>
      </div>
      <div class="ute__filter">
        <label class="ute__label">{{ t('usageTrend.autoRefresh') }}</label>
        <el-select v-model="autoRefreshSec" class="ute__select ute__select--auto">
          <el-option :value="0" :label="t('usageTrend.autoRefreshOff')" />
          <el-option :value="30" :label="t('usageTrend.autoRefresh30s')" />
          <el-option :value="60" :label="t('usageTrend.autoRefresh1m')" />
          <el-option :value="300" :label="t('usageTrend.autoRefresh5m')" />
        </el-select>
      </div>
      <div class="ute__filter ute__filter--reset">
        <el-button :disabled="!hasActiveFilters" @click="clearAllFilters">
          {{ t('usageTrend.clearAll') }}
        </el-button>
      </div>
    </div>

    <div class="ute__chart">
      <ModelTrendChart
        :series="series"
        :metric="metric"
        :bucket-minutes="bucketMinutes"
        :loading="loading"
        fill
      />
      <div v-if="error" class="ute__err">
        {{ t('usageTrend.loadFailed') }}：{{ error }}
        <div v-if="detailQueryFailed" class="ute__err-hint">{{ t('usageTrend.detailTimeoutHint') }}</div>
      </div>
    </div>

    <div class="ute__table">
      <el-table :data="series" size="small" :row-class-name="rowClass">
        <el-table-column :label="t('usageTrend.filterModel')" min-width="220" prop="model">
          <template #default="{ row }">
            <span :class="{ 'ute__others': row.model === USAGE_TREND_OTHERS }">
              {{ row.model === USAGE_TREND_OTHERS ? t('dashboard.board.trendOthers') : row.model }}
            </span>
          </template>
        </el-table-column>
        <el-table-column :label="t('dashboard.board.trendRequests')" align="right" width="110">
          <template #default="{ row }">{{ compactTickValue(row.total_requests) }}</template>
        </el-table-column>
        <el-table-column :label="t('dashboard.board.trendTokens')" align="right" width="120">
          <template #default="{ row }">{{ compactTickValue(row.total_tokens) }}</template>
        </el-table-column>
        <el-table-column :label="t('dashboard.board.trendCredits')" align="right" width="110">
          <template #default="{ row }">{{ compactTickValue(row.total_credits) }}</template>
        </el-table-column>
        <el-table-column :label="t('dashboard.board.trendCost')" align="right" width="130">
          <template #default="{ row }">${{ row.total_cost_usd.toFixed(4) }}</template>
        </el-table-column>
        <el-table-column :label="t('usageTrend.shareByMetric')" align="right" width="110">
          <template #default="{ row }">{{ sharePct(row) }}</template>
        </el-table-column>
      </el-table>
    </div>

    <!-- Top5 用量排行 -->
    <div v-if="rankings" class="ute__rankings">
      <h3 class="ute__rankings-title">Top 5 用量排行</h3>
      <div v-if="rankingsError" class="ute__err">加载排行失败：{{ rankingsError }}</div>
      <div class="ute__rankings-grid">
        <!-- Top 模型 -->
        <div class="ute__ranking-card">
          <h4 class="ute__ranking-header">Top 5 模型</h4>
          <div v-if="rankingsLoading" class="ute__ranking-loading">加载中...</div>
          <div v-else-if="rankings.top_models.length === 0" class="ute__ranking-empty">暂无数据</div>
          <div v-else class="ute__ranking-list">
            <div v-for="(item, idx) in rankings.top_models" :key="idx" class="ute__ranking-item ute__ranking-item--clickable" @click="navigateToRequests('model', item.name)">
              <div class="ute__ranking-rank">{{ idx + 1 }}</div>
              <div class="ute__ranking-info">
                <div class="ute__ranking-name">{{ item.name }}</div>
                <div class="ute__ranking-stats">
                  请求: {{ compactTickValue(item.request_count) }} · 
                  Token: {{ compactTickValue(item.total_tokens) }} · 
                  成本: ${{ item.total_cost_usd.toFixed(4) }}
                </div>
              </div>
              <div class="ute__ranking-arrow">→</div>
            </div>
          </div>
          <div v-if="!rankingsLoading && rankings.top_models.length > 0" class="ute__ranking-more">
            <el-button size="small" text @click="navigateToRequestsAll('model')">查看全部模型</el-button>
          </div>
        </div>

        <!-- Top 客户端 -->
        <div class="ute__ranking-card">
          <h4 class="ute__ranking-header">Top 5 客户端</h4>
          <div v-if="rankingsLoading" class="ute__ranking-loading">加载中...</div>
          <div v-else-if="rankings.top_clients.length === 0" class="ute__ranking-empty">暂无数据</div>
          <div v-else class="ute__ranking-list">
            <div v-for="(item, idx) in rankings.top_clients" :key="idx" class="ute__ranking-item ute__ranking-item--clickable" @click="navigateToRequests('client', item.name)">
              <div class="ute__ranking-rank">{{ idx + 1 }}</div>
              <div class="ute__ranking-info">
                <div class="ute__ranking-name">{{ item.name }}</div>
                <div class="ute__ranking-stats">
                  请求: {{ compactTickValue(item.request_count) }} · 
                  Token: {{ compactTickValue(item.total_tokens) }} · 
                  成本: ${{ item.total_cost_usd.toFixed(4) }}
                </div>
              </div>
              <div class="ute__ranking-arrow">→</div>
            </div>
          </div>
          <div v-if="!rankingsLoading && rankings.top_clients.length > 0" class="ute__ranking-more">
            <el-button size="small" text @click="navigateToRequestsAll('client')">查看全部客户端</el-button>
          </div>
        </div>

        <!-- Top 用户 -->
        <div class="ute__ranking-card">
          <h4 class="ute__ranking-header">Top 5 用户</h4>
          <div v-if="rankingsLoading" class="ute__ranking-loading">加载中...</div>
          <div v-else-if="rankings.top_users.length === 0" class="ute__ranking-empty">暂无数据</div>
          <div v-else class="ute__ranking-list">
            <div v-for="(item, idx) in rankings.top_users" :key="idx" class="ute__ranking-item ute__ranking-item--clickable" @click="navigateToRequests('user', item.name)">
              <div class="ute__ranking-rank">{{ idx + 1 }}</div>
              <div class="ute__ranking-info">
                <div class="ute__ranking-name">{{ item.name }}</div>
                <div class="ute__ranking-stats">
                  请求: {{ compactTickValue(item.request_count) }} · 
                  Token: {{ compactTickValue(item.total_tokens) }} · 
                  成本: ${{ item.total_cost_usd.toFixed(4) }}
                </div>
              </div>
            </div>
          <div v-if="!rankingsLoading && rankings.top_users.length > 0" class="ute__ranking-more">
            <el-button size="small" text @click="navigateToRequestsAll('user')">查看全部用户</el-button>
          </div>
          </div>
        </div>

        <!-- Top API Keys -->
        <div class="ute__ranking-card">
          <h4 class="ute__ranking-header">Top 5 API Keys</h4>
          <div v-if="rankingsLoading" class="ute__ranking-loading">加载中...</div>
          <div v-else-if="rankings.top_api_keys.length === 0" class="ute__ranking-empty">暂无数据</div>
          <div v-else class="ute__ranking-list">
            <div v-for="(item, idx) in rankings.top_api_keys" :key="idx" class="ute__ranking-item ute__ranking-item--clickable" @click="navigateToRequests('apikey', item.name)">
              <div class="ute__ranking-rank">{{ idx + 1 }}</div>
              <div class="ute__ranking-info">
                <div class="ute__ranking-name">{{ item.name }}</div>
                <div class="ute__ranking-stats">
                  请求: {{ compactTickValue(item.request_count) }} · 
                  Token: {{ compactTickValue(item.total_tokens) }} · 
                  成本: ${{ item.total_cost_usd.toFixed(4) }}
                </div>
              </div>
            </div>
          <div v-if="!rankingsLoading && rankings.top_api_keys.length > 0" class="ute__ranking-more">
            <el-button size="small" text @click="navigateToRequestsAll('apikey')">查看全部API Keys</el-button>
          </div>
          </div>
        </div>

        <!-- Top 项目 -->
        <div class="ute__ranking-card">
          <h4 class="ute__ranking-header">Top 5 项目</h4>
          <div v-if="rankingsLoading" class="ute__ranking-loading">加载中...</div>
          <div v-else-if="rankings.top_projects.length === 0" class="ute__ranking-empty">暂无数据</div>
          <div v-else class="ute__ranking-list">
            <div v-for="(item, idx) in rankings.top_projects" :key="idx" class="ute__ranking-item ute__ranking-item--clickable" @click="navigateToRequests('project', item.name)">
              <div class="ute__ranking-rank">{{ idx + 1 }}</div>
              <div class="ute__ranking-info">
                <div class="ute__ranking-name">{{ item.name }}</div>
                <div class="ute__ranking-stats">
                  请求: {{ compactTickValue(item.request_count) }} · 
                  Token: {{ compactTickValue(item.total_tokens) }} · 
                  成本: ${{ item.total_cost_usd.toFixed(4) }}
                </div>
              </div>
            </div>
          <div v-if="!rankingsLoading && rankings.top_projects.length > 0" class="ute__ranking-more">
            <el-button size="small" text @click="navigateToRequestsAll('project')">查看全部项目</el-button>
          </div>
          </div>
        </div>

        <!-- Top 任务 -->
        <div class="ute__ranking-card">
          <h4 class="ute__ranking-header">Top 5 任务</h4>
          <div v-if="rankingsLoading" class="ute__ranking-loading">加载中...</div>
          <div v-else-if="rankings.top_tasks.length === 0" class="ute__ranking-empty">暂无数据</div>
          <div v-else class="ute__ranking-list">
            <div v-for="(item, idx) in rankings.top_tasks" :key="idx" class="ute__ranking-item ute__ranking-item--clickable" @click="navigateToRequests('task', item.name)">
              <div class="ute__ranking-rank">{{ idx + 1 }}</div>
              <div class="ute__ranking-info">
                <div class="ute__ranking-name">{{ item.name }}</div>
                <div class="ute__ranking-stats">
                  请求: {{ compactTickValue(item.request_count) }} · 
                  Token: {{ compactTickValue(item.total_tokens) }} · 
                  成本: ${{ item.total_cost_usd.toFixed(4) }}
                </div>
              </div>
            </div>
          <div v-if="!rankingsLoading && rankings.top_tasks.length > 0" class="ute__ranking-more">
            <el-button size="small" text @click="navigateToRequestsAll('task')">查看全部任务</el-button>
          </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script lang="ts">
export default { name: 'UsageTrendExplorer' }
</script>

.ute__filter-error {
  flex-basis: 100%;
  color: var(--warning);
  font-size: 12px;
}

<style scoped>
.ute {
  display: flex;
  flex-direction: column;
  gap: 12px;
  flex: 1 1 auto;
  min-height: 0;
  height: 100%;
  max-width: none;
  margin: 0;
  padding: 12px 16px 16px;
  box-sizing: border-box;
  overflow: hidden;
}
.ute__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  flex-shrink: 0;
}
.ute__title {
  font-size: 16px;
  font-weight: 700;
  margin: 0 0 4px;
}
.ute__sub {
  font-size: 12px;
  color: var(--text-muted);
  margin: 0;
}
.ute__src {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  color: var(--text-muted);
  border: 1px dashed var(--border);
  border-radius: 8px;
  padding: 3px 8px;
}
.ute__src b {
  color: var(--text);
}
.ute__filters {
  display: flex;
  align-items: flex-end;
  gap: 12px;
  flex-wrap: wrap;
  flex-shrink: 0;
  padding: 12px 12px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
}
.ute__filter {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.ute__label {
  font-size: 11px;
  font-weight: 600;
  color: var(--text-muted);
}
.ute__select {
  width: 190px;
}
.ute__select--wide {
  width: 260px;
}
.ute__filter--model {
  width: 260px;
  min-width: 200px;
}
.ute__filter--model :deep(.model-picker),
.ute__filter--model :deep(.mp-trigger) {
  width: 100%;
}
/* 已选模型时触发器亮 accent（与 stream 页 filter-model-picker--active 同式） */
.ute__filter--model-active :deep(.mp-trigger) {
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 12%, transparent);
}
.ute__select--auto {
  width: 118px;
}
.ute__filter--reset :deep(.el-button) {
  min-height: 32px;
}
.ute__chart,
.ute__table {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 12px 16px;
}
.ute__chart {
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.ute__table {
  flex: 0 1 34%;
  min-height: 160px;
  max-height: 38%;
  overflow: auto;
}
.ute__err {
  font-size: 12px;
  color: var(--danger);
  padding-top: 4px;
}
.ute__err-hint {
  font-size: 11px;
  color: var(--text-muted);
  padding-top: 2px;
}
.ute__others {
  color: var(--text-muted);
  font-style: italic;
}
:deep(.ute__others-row) {
  color: var(--text-muted);
}
@media (max-width: 768px) {
  .ute {
    padding: 12px;
    overflow: auto;
  }
  .ute__select,
  .ute__select--wide,
  .ute__filter--model {
    width: 100%;
  }
  .ute__filter {
    width: 100%;
  }
  .ute__chart {
    min-height: 280px;
    flex: 1 0 280px;
  }
  .ute__table {
    max-height: none;
    flex: 0 0 auto;
  }
}

/* Top5 排行样式 */
.ute__rankings {
  flex-shrink: 0;
  padding: 16px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
}
.ute__rankings-title {
  font-size: 15px;
  font-weight: 600;
  margin: 0 0 16px;
  color: var(--text);
}
.ute__rankings-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
  gap: 16px;
}
.ute__ranking-card {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px;
}
.ute__ranking-header {
  font-size: 13px;
  font-weight: 600;
  margin: 0 0 12px;
  color: var(--text);
  padding-bottom: 8px;
  border-bottom: 1px solid var(--border);
}
.ute__ranking-loading,
.ute__ranking-empty {
  text-align: center;
  padding: 20px;
  color: var(--text-muted);
  font-size: 12px;
}
.ute__ranking-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.ute__ranking-item {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 8px;
  border-radius: 6px;
  background: var(--card);
  transition: background 0.2s;
}
.ute__ranking-item--clickable {
  cursor: pointer;
}
.ute__ranking-item--clickable:hover {
  background: var(--hover-bg);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
}
.ute__ranking-item:hover {
  background: var(--hover-bg);
}
.ute__ranking-rank {
  flex-shrink: 0;
  width: 24px;
  height: 24px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--accent);
  color: white;
  border-radius: 50%;
  font-size: 12px;
  font-weight: 600;
}
.ute__ranking-info {
  flex: 1;
  min-width: 0;
}
.ute__ranking-name {
  font-size: 13px;
  font-weight: 500;
  color: var(--text);
  margin-bottom: 4px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ute__ranking-stats {
  font-size: 11px;
  color: var(--text-muted);
}
.ute__ranking-more {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--border);
  text-align: center;
}
.ute__ranking-arrow {
  flex-shrink: 0;
  margin-left: auto;
  color: var(--text-muted);
  font-size: 16px;
  opacity: 0;
  transition: opacity 0.2s, transform 0.2s;
}
.ute__ranking-item--clickable:hover .ute__ranking-arrow {
  opacity: 1;
  transform: translateX(4px);
}
@media (max-width: 768px) {
  .ute__rankings-grid {
    grid-template-columns: 1fr;
  }
}
</style>
