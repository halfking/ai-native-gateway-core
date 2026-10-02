<script setup lang="ts">
// UsageTrendExplorer.vue — 全页用量趋势分析（2026-10-02 看板轮）。
// 看板「用量趋势」卡的「更多」入口（BoardFilterBar 时间范围右侧按钮）落到本页；
// 菜单「模型与路由」组亦挂入口。过滤维度：时间范围 / 供应商 / 租户（超管）/
// API Key / 模型 / 指标（请求数、Token、积分、成本）。全部过滤条件同步 URL
// query（深链/分享），图表按模型分线（top 10 + 长尾折叠），下表为模型汇总。
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  ElOption,
  ElRadioButton,
  ElRadioGroup,
  ElSelect,
  ElTable,
  ElTableColumn,
} from 'element-plus'
import ModelTrendChart from '../../components/analytics/ModelTrendChart.vue'
import KxDateRangePicker from '../../components/ui/KxDateRangePicker.vue'
import { makeDateRangePresets } from '../../components/ui/kxDatePresets'
import type { KxDateRange } from '../../components/ui/kx-date-types'
import {
  getUsageTrendModels,
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
const model = ref<string | null>(typeof route.query.model === 'string' && route.query.model ? route.query.model : null)
const metric = ref<UsageTrendMetric>(readMetricFromQuery())

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
const modelOptions = ref<{ model: string; requests: number }[]>([])

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
      model: model.value || undefined,
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

let modelsToken = 0

async function loadModelOptions() {
  const token = ++modelsToken
  try {
    const resp = await getUsageTrendModels({
      time: timeQuery.value,
      tenant_id: tenantId.value || undefined,
      provider_id: providerId.value || undefined,
      api_key_id: apiKeyId.value || undefined,
    })
    if (token !== modelsToken) return
    modelOptions.value = (resp.models ?? []).map((m) => ({ model: m.model, requests: m.requests }))
  } catch {
    if (token === modelsToken) modelOptions.value = []
  }
}

// 模型选项跟随时间/供应商/租户/apikey 变化（不含 model 自身）。
watch([effectiveRange, providerId, tenantId, apiKeyId], () => void loadModelOptions())

watch(
  [effectiveRange, providerId, tenantId, apiKeyId, model],
  () => {
    void loadSeries()
    syncQuery()
  },
)

function syncQuery() {
  const next: Record<string, string> = {
    start: effectiveRange.value.start,
    end: effectiveRange.value.end,
    metric: metric.value,
  }
  // truthy 防护：el-select 清空在不同 EP 版本产出 undefined/''/null，统一按
  // 「有值才写入」处理，避免空参数（provider=）残留在深链里。
  if (providerId.value) next.provider = String(providerId.value)
  if (showTenantFilter && tenantId.value) next.tenant = tenantId.value
  if (apiKeyId.value) next.apikey = String(apiKeyId.value)
  if (model.value) next.model = model.value
  // 全量重建 query：清空的过滤器必须从深链里消失，不能沿用旧值。
  router.replace({ query: next }).catch(() => undefined)
}

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
  void loadModelOptions()
  void loadSeries()
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
      <div class="ute__filter">
        <label class="ute__label">{{ t('usageTrend.filterModel') }}</label>
        <el-select
          v-model="model"
          clearable
          filterable
          :placeholder="t('usageTrend.filterAll')"
          class="ute__select ute__select--wide"
        >
          <el-option
            v-for="m in modelOptions"
            :key="m.model"
            :value="m.model"
            :label="m.model === USAGE_TREND_OTHERS ? t('dashboard.board.trendOthers') : `${m.model} (${compactTickValue(m.requests)})`"
          />
        </el-select>
      </div>
      <div class="ute__filter">
        <label class="ute__label">{{ t('usageTrend.filterMetric') }}</label>
        <el-radio-group v-model="metric" size="small">
          <el-radio-button v-for="m in USAGE_TREND_METRICS" :key="m" :value="m">
            {{ t(usageTrendMetricLabelKey(m)) }}
          </el-radio-button>
        </el-radio-group>
      </div>
    </div>

    <div class="ute__chart">
      <ModelTrendChart
        :series="series"
        :metric="metric"
        :bucket-minutes="bucketMinutes"
        :loading="loading"
        :height="440"
      />
      <div v-if="error" class="ute__err">{{ t('usageTrend.loadFailed') }}：{{ error }}</div>
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
  gap: 14px;
  max-width: 1440px;
  margin: 0 auto;
  padding: 18px 20px 40px;
}
.ute__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 14px;
  flex-wrap: wrap;
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
.ute__chart,
.ute__table {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 14px 16px;
}
.ute__err {
  font-size: 12px;
  color: var(--danger, #f56c6c);
  padding-top: 6px;
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
    padding: 12px 12px 32px;
  }
  .ute__select,
  .ute__select--wide {
    width: 100%;
  }
  .ute__filter {
    width: 100%;
  }
}
</style>
