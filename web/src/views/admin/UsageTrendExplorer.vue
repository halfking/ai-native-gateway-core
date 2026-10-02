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
import { makeDateRangePresets } from '../../components/ui/kxDatePresets'
import type { KxDateRange } from '../../components/ui/kx-date-types'
import {
  getUsageTrendSeries,
  type UsageTrendMetric,
  type UsageTrendModelSeries,
} from '../../api/usage'
import { getProviders, type Provider } from '../../api/providers'
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
  const today = new Date().toISOString().slice(0, 10)
  const start = new Date(Date.parse(`${today}T00:00:00Z`) - 6 * 86_400_000).toISOString().slice(0, 10)
  return { start, end: today }
}

// ── 过滤器选项 ──

const providers = ref<Provider[]>([])
const tenants = ref<TenantSummary[]>([])
const keys = ref<ApiKey[]>([])

const keyLabel = (k: ApiKey) => {
  const name = k.key_alias || k.owner_user || k.application_code || `#${k.id}`
  return `${name} · ${k.key_prefix}…`
}

async function loadFilterOptions() {
  try {
    const [p, k] = await Promise.all([getProviders(), getKeys()])
    providers.value = p ?? []
    keys.value = k ?? []
  } catch {
    /* 过滤选项失败不阻塞主图 */
  }
  if (showTenantFilter) {
    try {
      tenants.value = (await getTenants()) ?? []
    } catch {
      /* non-blocking */
    }
  }
}

// ── 数据加载 ──

const series = ref<UsageTrendModelSeries[]>([])
const bucketMinutes = ref(60)
const loading = ref(false)
const error = ref<string | null>(null)
const source = ref('')

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
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    if (token === seriesToken) loading.value = false
  }
}

watch(
  [effectiveRange, providerId, tenantId, apiKeyId, models],
  () => {
    void loadSeries()
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

onMounted(() => {
  void loadFilterOptions()
  void loadSeries()
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
      <div class="ute__filter">
        <label class="ute__label">{{ t('usageTrend.filterProvider') }}</label>
        <el-select
          v-model="providerId"
          clearable
          filterable
          :placeholder="t('usageTrend.filterAll')"
          class="ute__select"
        >
          <el-option
            v-for="p in providers"
            :key="p.id"
            :value="p.id"
            :label="p.display_name || p.catalog_code"
          />
        </el-select>
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
  </div>
</template>

<script lang="ts">
export default { name: 'UsageTrendExplorer' }
</script>

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
  gap: 14px;
  flex-wrap: wrap;
  flex-shrink: 0;
}
.ute__title {
  font-size: 18px;
  font-weight: 700;
  margin: 0 0 4px;
}
.ute__sub {
  font-size: 12.5px;
  color: var(--text-muted);
  margin: 0;
}
.ute__src {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 10.5px;
  color: var(--text-muted);
  border: 1px dashed var(--border);
  border-radius: 8px;
  padding: 3px 9px;
}
.ute__src b {
  color: var(--text);
}
.ute__filters {
  display: flex;
  align-items: flex-end;
  gap: 14px;
  flex-wrap: wrap;
  flex-shrink: 0;
  padding: 12px 14px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
}
.ute__filter {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.ute__label {
  font-size: 11.5px;
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
  padding: 14px 16px;
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
  padding-top: 6px;
}
.ute__err-hint {
  font-size: 11.5px;
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
@media (max-width: 900px) {
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
</style>
