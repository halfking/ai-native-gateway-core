<script setup lang="ts">
// ReconciliationReport.vue —— 对账报表（2026-09-25 落地轮；2026-09-29 多维筛选轮重写）
//
// 供应商对帐（全流量/成本口径）与内部对帐（业务流量/积分+内部价口径）双视角。
// 相对旧版的三个结构性变化：
//   ① 筛选条件从「视角 + 日期」扩到六维（供应商 / 凭据 / 模型 / 租户 / 用户 /
//      apikey）+ 日期，且**两个视角通用**——后端最细粒度快照让任意组合都成立。
//   ② 四张固定表 → 一张「可切换分组维度」的表 + 指标列显隐；用户想看什么
//      就把那一维设成分组、把那几列勾出来，而不是在一堆固定表里找。
//   ③ 三个图表（趋势 / 每天各模型的量 / 主要错误）+ 汇总⇄按天明细切换。
//
// 「整个模型的统计列表」收进可折叠面板：它是筛选器的补充读物（挑模型名），
// 不是主表，导出也不含它（原则：导出只给对帐需要签字的维度）。
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
// 2026-09-28 修复：本仓 main.ts 不做 ElementPlus 全局注册（也无 unplugin 自动
// 导入），模板里的 el-* 必须在 <script setup> 显式 import，否则生产构建里
// resolveComponent 静默失败、组件退化为未知标签 —— el-table 列插槽被
// normalizeChildren 以无参调用，`{ row }` 解构 undefined 直接把页面打白。
import {
  ElAlert,
  ElBadge,
  ElButton,
  ElCard,
  ElCheckbox,
  ElCheckboxGroup,
  ElCollapse,
  ElCollapseItem,
  ElDatePicker,
  ElDropdown,
  ElMessage,
  ElOption,
  ElRadioButton,
  ElRadioGroup,
  ElSelect,
  ElTable,
  ElTableColumn,
  ElTooltip,
} from 'element-plus'
import { Download, Filter, Refresh } from '@element-plus/icons-vue'
import { useRoute } from 'vue-router'
import ReconciliationCharts from '../../components/reconciliation/ReconciliationCharts.vue'
import {
  downloadReportExport,
  getReportDimensions,
  getReportSummary,
  runReportRollup,
  UNASSIGNED_ID,
  type DimensionOptions,
  type GroupDim,
  type RangeReport,
  type ReportView,
} from '../../api/reportrollup'

const { t } = useI18n()
const route = useRoute()

const loading = ref(false)
const dimsLoading = ref(false)
const exporting = ref(false)
const rerunning = ref(false)
const errorText = ref('')

// 2026-09-26 审计轮：支持 ?view=internal 深链（「租户用户→结算报表」菜单
// 入口直开内部视角）。非法值回落 provider，与后端 view 校验同口径。
const initialView = route.query.view === 'internal' ? 'internal' : 'provider'
const view = ref<ReportView>(initialView)

// 默认区间：昨日往前 7 天（今日快照 T+1 凌晨才生成）。
function fmtDay(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}
function defaultRange(): [string, string] {
  return [fmtDay(new Date(Date.now() - 7 * 86400000)), fmtDay(new Date(Date.now() - 86400000))]
}
const range = ref<[string, string]>(defaultRange())

/** 汇总 ⇄ 按天明细。 */
const detail = ref(false)
/** 主表分组维度。 */
const groupDim = ref<GroupDim>('provider')
/** 筛选面板展开态（默认收起：六个下拉铺满一行太吵）。 */
const filtersOpen = ref(false)
// 模型统计折叠面板。modelsOpen 里出现 'models' 之前，模型清单那张表
// **完全不挂载**（模板上的 v-if）：Element Plus 的 el-collapse 这一版没有
// destroy-on-hide，折叠时内容仍留在 DOM 里，而模型清单实测可达 500+ 行，
// 每次开页都渲染一张这么大的表是白付的代价。挂在 modelsOpen 上也让
// 「只在点击按钮时展示」在 DOM 层面成立，而不只是视觉上收起。
//
// 注：这段说明不能写成模板里的 <!-- --> 注释——硬编码中文计数器
// （src/i18n/hardcodedCjk.ts）只跳过 trim 后以 // * /* 开头的整行注释，
// HTML 注释里的中文照样计数，会顶爆棘轮门。
const modelsOpen = ref<string[]>([])

const filters = ref<{
  provider_id?: number
  credential_id?: number
  api_key_id?: number
  tenant_id?: string
  person?: string
  model?: string
}>({})

const report = ref<RangeReport | null>(null)
const dims = ref<DimensionOptions | null>(null)

const hasData = computed(() => !!report.value && report.value.snapshot_dates.length > 0)

/** 可勾选的指标列（列显隐）。顺序即表格列顺序。 */
const ALL_COLUMNS = ['requests', 'success', 'errors', 'errorRate', 'quality', 'input', 'cache', 'output', 'cacheRate', 'cost', 'credits', 'topError'] as const
type ColumnKey = (typeof ALL_COLUMNS)[number]
const visibleColumns = ref<ColumnKey[]>(['requests', 'errors', 'errorRate', 'quality', 'input', 'cache', 'output', 'cacheRate', 'cost'])
function isColVisible(k: ColumnKey) {
  return visibleColumns.value.includes(k)
}
// 列标签用显式表而非 `t(\`reports.col.${c}\`)` 动态键：i18n 审计扫描器跳过
// 动态键，动态拼出来的 12 个键会全部落进「已定义但未被引用」清单，既污染报告
// 也让 strict 模式失去意义。
const COLUMN_LABELS: Record<ColumnKey, () => string> = {
  requests: () => t('reports.requests', '请求数'),
  success: () => t('reports.success', '成功'),
  errors: () => t('reports.errors', '失败'),
  errorRate: () => t('reports.errorRate', '失败率'),
  quality: () => t('reports.qualityScore', '质量评分'),
  input: () => t('reports.inputTokens', '输入 tokens'),
  cache: () => t('reports.cacheTokens', '缓存 tokens'),
  output: () => t('reports.outputTokens', '输出 tokens'),
  cacheRate: () => t('reports.cacheRate', '缓存率'),
  cost: () => t('reports.cost', '成本'),
  credits: () => t('reports.internalCredits', '内部积分'),
  topError: () => t('reports.topError', '主要错误'),
}

const activeFilterCount = computed(() => Object.values(filters.value).filter((v) => v !== undefined && v !== '').length)

// ── 数据装载 ────────────────────────────────────────────────────────────────
async function refresh() {
  if (!range.value || range.value.length !== 2) return
  loading.value = true
  errorText.value = ''
  try {
    report.value = await getReportSummary({
      start: range.value[0],
      end: range.value[1],
      view: view.value,
      detail: detail.value,
      ...filters.value,
    })
  } catch (e: any) {
    errorText.value = e?.message ?? String(e)
    report.value = null
  } finally {
    loading.value = false
  }
}

// 候选随区间/视角变化，**不随当前筛选变化**——否则选中一个供应商后其它
// 供应商从下拉里消失，用户没法横向切换（筛选栏最常见的自锁坑）。
async function refreshDimensions() {
  if (!range.value || range.value.length !== 2) return
  dimsLoading.value = true
  try {
    dims.value = await getReportDimensions({ start: range.value[0], end: range.value[1], view: view.value })
  } catch {
    dims.value = null
  } finally {
    dimsLoading.value = false
  }
}

function reload() {
  void Promise.all([refresh(), refreshDimensions()])
}

function clearFilters() {
  filters.value = {}
  void refresh()
}

watch([view, detail], reload)
// 筛选条件用「变更即查」：这类筛选器用户改完就等结果，没有再点一次确认的
// 理由（旧版只有一个维度时靠点刷新按钮，多了六个维度就没必要了）。
watch(filters, () => void refresh(), { deep: true })

async function exportXlsx() {
  if (!range.value || range.value.length !== 2) return
  exporting.value = true
  try {
    await downloadReportExport({
      start: range.value[0],
      end: range.value[1],
      view: view.value,
      detail: detail.value,
      // 导出口径跟着主表的分组维度走：界面上按租户分组看到的，导出就该是
      // 按租户拆的日表。早先导出恒按模型，交接时会拿到和屏幕不符的表。
      group: groupDim.value,
      ...filters.value,
    })
  } catch (e: any) {
    ElMessage.error(`${t('common.exportFailed', '导出失败')}: ${e?.message ?? e}`)
  } finally {
    exporting.value = false
  }
}

async function rerunEndDay() {
  rerunning.value = true
  try {
    const res = await runReportRollup(range.value?.[1] ?? '')
    ElMessage.success(`${t('reports.rerunDone', '重跑完成')}: ${res.date} rows=${res.rows_written}`)
    reload()
  } catch (e: any) {
    ElMessage.error(`${t('reports.rerunFailed', '重跑失败')}: ${e?.message ?? e}`)
  } finally {
    rerunning.value = false
  }
}

// ── 主表：按所选维度取对应分组 ────────────────────────────────────────────────
interface GroupCell {
  key: string
  name: string
  totals: RangeReport['totals']
  error_breakdown: Record<string, number>
  score?: number
}

const groupTitle = computed(() => {
  switch (groupDim.value) {
    case 'provider': return t('reports.byProvider', '按供应商')
    case 'credential': return t('reports.byCredential', '按凭据')
    case 'model': return t('reports.byModel', '按模型')
    case 'tenant': return t('reports.byTenant', '按租户')
    case 'person': return t('reports.byPerson', '按用户')
    case 'apikey': return t('reports.byApiKey', '按 apikey')
  }
})

const unassignedLabel = computed(() => t('reports.unassigned', '未落定'))

function idText(id: number | undefined, name?: string): string {
  if (id === undefined) return '-'
  if (id === UNASSIGNED_ID) return unassignedLabel.value
  return name || String(id)
}

const groupRows = computed<GroupCell[]>(() => {
  const r = report.value
  if (!r) return []
  switch (groupDim.value) {
    case 'provider':
      return (r.providers ?? []).map((p) => ({
        key: String(p.provider_id),
        name: idText(p.provider_id, p.provider_name),
        totals: p.totals,
        error_breakdown: p.error_breakdown ?? {},
        score: p.quality_score,
      }))
    case 'credential':
      return (r.credentials ?? []).map((c) => ({
        key: String(c.credential_id),
        name: idText(c.credential_id, c.credential_name),
        totals: c.totals,
        error_breakdown: c.error_breakdown ?? {},
        score: c.quality_score,
      }))
    case 'model':
      return (r.model_totals ?? r.models ?? []).map((m) => ({
        key: m.raw_model_name,
        name: m.raw_model_name || '-',
        totals: m.totals,
        error_breakdown: m.error_breakdown ?? {},
        score: m.quality_score,
      }))
    case 'tenant':
      return (r.tenants ?? []).map((t) => ({
        key: t.tenant_id,
        name: t.tenant_id || '-',
        totals: t.totals,
        error_breakdown: t.error_breakdown ?? {},
        score: t.quality_score,
      }))
    case 'person':
      return (r.persons ?? []).map((p) => ({
        key: `${p.tenant_id}\u0000${p.person}`,
        name: p.tenant_id ? `${p.person} @ ${p.tenant_id}` : p.person,
        totals: p.totals,
        error_breakdown: p.error_breakdown ?? {},
        score: p.quality_score,
      }))
    case 'apikey':
      return (r.api_keys ?? []).map((k) => ({
        key: String(k.api_key_id),
        name: idText(k.api_key_id, k.api_key_name),
        totals: k.totals,
        error_breakdown: k.error_breakdown ?? {},
        score: k.quality_score,
      }))
  }
})

// 明细模式：主表切到「按天 × 当前维度」，同一张表换个数据面。
const dayRows = computed<GroupCell[]>(() => {
  const r = report.value
  if (!r || !detail.value) return []
  // 明细行的名称：后端日行自带 name（key 是原始 id 键，与汇总行同口径，
  // 这里能真正 join 上）。name 缺失时回落到汇总行的名字，再回落到原始键
  // （显示 id / 未落定哨兵），任何一步都不会留空白。
  const nameOf = (key: string) => {
    const hit = groupRows.value.find((g) => g.key === key)
    return hit?.name ?? key
  }
  const src: {
    date: string
    key: string
    name?: string
    totals: RangeReport['totals']
    error_breakdown?: Record<string, number>
    quality_score?: number
  }[] =
    groupDim.value === 'provider' ? (r.daily_providers ?? [])
    : groupDim.value === 'credential' ? (r.daily_credentials ?? [])
    : groupDim.value === 'tenant' ? (r.daily_tenants ?? [])
    : groupDim.value === 'person' ? (r.daily_persons ?? [])
    : groupDim.value === 'apikey' ? (r.daily_api_keys ?? [])
    : (r.daily_models ?? []).map((m) => ({
        date: m.date,
        key: m.raw_model_name,
        totals: m.totals,
        error_breakdown: m.error_breakdown,
        quality_score: m.quality_score,
      }))
  return src.map((d) => ({
    key: `${d.date}\u0000${d.key}`,
    name: groupDim.value === 'model' ? d.key : `${d.date} · ${d.name || nameOf(d.key)}`,
    totals: d.totals,
    error_breakdown: d.error_breakdown ?? {},
    // 明细行也要带评分：漏了这一句，「质量评分」列在按天明细模式下整列为空，
    // 而该列正是需求里点名列出的指标之一。
    score: d.quality_score,
  }))
})

const tableRows = computed<GroupCell[]>(() => (detail.value ? dayRows.value : groupRows.value))

const dayTableRows = computed(() =>
  (report.value?.days ?? []).map((d) => ({
    date: d.date,
    totals: d.totals,
    error_breakdown: d.error_breakdown ?? {},
  })),
)

const modelStatsRows = computed(() =>
  (report.value?.model_totals ?? []).map((m) => ({
    model: m.raw_model_name,
    totals: m.totals,
    error_breakdown: m.error_breakdown ?? {},
  })),
)

// ── 格式化 ──────────────────────────────────────────────────────────────────
function fmtInt(n: number | undefined | null): string {
  return (n ?? 0).toLocaleString('en-US')
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
function topError(br: Record<string, number> | undefined): string {
  if (!br) return '-'
  const entries = Object.entries(br).filter(([, v]) => v > 0)
  if (!entries.length) return '-'
  entries.sort((a, b) => (b[1] === a[1] ? a[0].localeCompare(b[0]) : b[1] - a[1]))
  const [kind, n] = entries[0]
  return `${kind} (${n})`
}

const coverageText = computed(() => {
  if (!report.value) return ''
  return `${report.value.snapshot_dates.length} ${t('reports.daysCovered', '天快照')}`
})
const legacyDays = computed(() => report.value?.coverage?.legacy_dates ?? [])

// 维度下拉的选项文案：名称优先，其次键。带请求数便于快速判断该选哪个。
function optionLabel(key: string, name?: string): string {
  return name ? `${name} (${key})` : key
}
function dimOptionLabel(o: { key: string; name?: string; requests: number }): string {
  return `${optionLabel(o.key, o.name)} · ${fmtInt(o.requests)}`
}

onMounted(reload)
</script>

<template>
  <div class="report-page">
    <!--  -->
    <div class="toolbar">
      <el-radio-group v-model="view">
        <el-radio-button value="provider">{{ t('reports.providerView', '供应商对帐') }}</el-radio-button>
        <el-radio-button value="internal">{{ t('reports.internalView', '内部对帐') }}</el-radio-button>
      </el-radio-group>
      <el-date-picker
        v-model="range"
        type="daterange"
        value-format="YYYY-MM-DD"
        :clearable="false"
        :start-placeholder="t('common.startDate', '开始日期')"
        :end-placeholder="t('common.endDate', '结束日期')"
        style="width: 260px"
      />
      <el-radio-group v-model="detail">
        <el-radio-button :value="false">{{ t('reports.summaryOnly', '汇总') }}</el-radio-button>
        <el-radio-button :value="true">{{ t('reports.dailyDetail', '按天明细') }}</el-radio-button>
      </el-radio-group>
      <el-button type="primary" :icon="Refresh" :loading="loading" @click="reload">
        {{ t('common.refresh', '刷新') }}
      </el-button>
      <el-button :icon="Download" :loading="exporting" @click="exportXlsx">
        {{ t('reports.exportExcel', '导出 Excel') }}
      </el-button>
      <el-button :loading="rerunning" @click="rerunEndDay">
        {{ t('reports.rerun', '重跑结束日') }}
      </el-button>
      <span v-if="coverageText" class="coverage">{{ coverageText }}</span>
    </div>

    <!--  -->
    <el-card shadow="never" class="filter-card">
      <div class="filter-head">
        <el-button :icon="Filter" @click="filtersOpen = !filtersOpen">
          {{ t('reports.filters', '筛选条件') }}
          <el-badge v-if="activeFilterCount > 0" :value="activeFilterCount" class="filter-badge" />
        </el-button>
        <span v-if="activeFilterCount > 0" class="coverage">{{ t('reports.activeFilters', '已启用') }} {{ activeFilterCount }}</span>
        <el-button v-if="activeFilterCount > 0" link @click="clearFilters">
          {{ t('reports.clearFilters', '清除筛选') }}
        </el-button>
      </div>
      <div v-show="filtersOpen" class="filter-grid">
        <div class="filter-item">
          <label>{{ t('reports.provider', '供应商') }}</label>
          <el-select v-model="filters.provider_id" clearable filterable :loading="dimsLoading" :placeholder="t('reports.all', '全部')" style="width: 100%">
            <el-option
              v-for="o in dims?.providers ?? []"
              :key="o.key"
              :value="Number(o.key)"
              :label="dimOptionLabel(o)"
            />
          </el-select>
        </div>
        <div class="filter-item">
          <label>{{ t('reports.credential', '凭据') }}</label>
          <el-select v-model="filters.credential_id" clearable filterable :loading="dimsLoading" :placeholder="t('reports.all', '全部')" style="width: 100%">
            <el-option
              v-for="o in dims?.credentials ?? []"
              :key="o.key"
              :value="Number(o.key)"
              :label="dimOptionLabel(o)"
            />
          </el-select>
        </div>
        <div class="filter-item">
          <label>{{ t('reports.model', '模型') }}</label>
          <el-select v-model="filters.model" clearable filterable :loading="dimsLoading" :placeholder="t('reports.all', '全部')" style="width: 100%">
            <el-option v-for="o in dims?.models ?? []" :key="o.key" :value="o.key" :label="dimOptionLabel(o)" />
          </el-select>
        </div>
        <div class="filter-item">
          <label>{{ t('reports.tenant', '租户') }}</label>
          <el-select v-model="filters.tenant_id" clearable filterable :loading="dimsLoading" :placeholder="t('reports.all', '全部')" style="width: 100%">
            <el-option v-for="o in dims?.tenants ?? []" :key="o.key" :value="o.key" :label="dimOptionLabel(o)" />
          </el-select>
        </div>
        <div class="filter-item">
          <label>{{ t('reports.person', '用户') }}</label>
          <el-select v-model="filters.person" clearable filterable :loading="dimsLoading" :placeholder="t('reports.all', '全部')" style="width: 100%">
            <el-option v-for="o in dims?.persons ?? []" :key="o.key" :value="o.key" :label="dimOptionLabel(o)" />
          </el-select>
        </div>
        <div class="filter-item">
          <label>{{ t('reports.apiKey', 'apikey') }}</label>
          <el-select v-model="filters.api_key_id" clearable filterable :loading="dimsLoading" :placeholder="t('reports.all', '全部')" style="width: 100%">
            <el-option
              v-for="o in dims?.api_keys ?? []"
              :key="o.key"
              :value="Number(o.key)"
              :label="dimOptionLabel(o)"
            />
          </el-select>
        </div>
      </div>
    </el-card>

    <el-alert v-if="errorText" :title="errorText" type="error" show-icon :closable="false" class="block" />
    <el-alert
      v-else-if="report && !hasData"
      :title="t('reports.noSnapshots', '该区间没有报表快照（每日聚合任务在凌晨生成前一日数据，或用「重跑结束日」补算）')"
      type="info"
      show-icon
      :closable="false"
      class="block"
    />
    <el-alert
      v-else-if="legacyDays.length > 0"
      type="warning"
      show-icon
      :closable="false"
      class="block"
      :title="`${t('reports.legacyCoverage', '有 N 天为旧口径快照：')}${legacyDays.length}${t('reports.legacyCoverageRest', ' 天只计入总计与按天，凭据/用户/apikey 维度需回填后可见（点「重跑结束日」逐日补算）')}`"
    />

    <template v-if="report">
      <!--  -->
      <div class="cards">
        <div class="card">
          <div class="card-label">{{ t('reports.requests', '请求数') }}</div>
          <div class="card-value">{{ fmtInt(report.totals.request_count) }}</div>
          <div class="card-sub">
            {{ fmtInt(report.totals.success_count) }} ✓ / {{ fmtInt(report.totals.error_count) }} ✗（{{ fmtPct(report.totals.error_rate) }}）
          </div>
        </div>
        <div class="card">
          <div class="card-label">{{ t('reports.tokens', 'Token 拆分') }}</div>
          <div class="card-value">{{ fmtInt(report.totals.total_tokens) }}</div>
          <div class="card-sub">
            {{ t('reports.in', '入') }} {{ fmtInt(report.totals.input_tokens) }} ·
            {{ t('reports.out', '出') }} {{ fmtInt(report.totals.output_tokens) }} ·
            {{ t('reports.cacheRead', '缓存读') }} {{ fmtInt(report.totals.cache_read_tokens) }} ·
            {{ t('reports.cacheWrite', '缓存写') }} {{ fmtInt(report.totals.cache_write_tokens) }} ·
            {{ t('reports.cacheHit', '缓存命中') }} {{ fmtRatio(report.totals.cache_hit_ratio) }}
          </div>
        </div>
        <div class="card">
          <div class="card-label">{{ t('reports.providerCost', '供应商成本') }}</div>
          <div class="card-value">{{ fmtMoneyFromCents(report.totals.estimated_cost_cents) }}</div>
          <div class="card-sub">{{ report.totals.currency || 'USD' }}</div>
        </div>
        <div class="card">
          <div class="card-label">{{ t('reports.internalCredits', '内部积分 / 金额') }}</div>
          <div class="card-value">{{ fmtInt(report.totals.credits_charged) }}</div>
          <div class="card-sub">
            {{ fmtMoneyFromCents(report.totals.internal_cost_cents) }} {{ report.totals.internal_currency || 'CNY' }}
          </div>
        </div>
        <div class="card">
          <div class="card-label">{{ t('reports.topError', '主要错误') }}</div>
          <div class="card-value card-value-sm">{{ report.top_error_kind || '-' }}</div>
          <div class="card-sub">
            {{ report.top_error_count ? fmtInt(report.top_error_count) : t('reports.noErrors', '无失败记录') }}
          </div>
        </div>
      </div>

      <!--  -->
      <ReconciliationCharts
        :days="report.days"
        :daily-models="report.daily_models ?? []"
        :error-breakdown="report.error_breakdown ?? {}"
        :covered-dates="report.snapshot_dates ?? []"
      />

      <!-- + -->
      <el-card shadow="never">
        <template #header>
          <div class="table-head">
            <el-radio-group v-model="groupDim" size="small">
              <el-radio-button value="provider">{{ t('reports.provider', '供应商') }}</el-radio-button>
              <el-radio-button value="credential">{{ t('reports.credential', '凭据') }}</el-radio-button>
              <el-radio-button value="model">{{ t('reports.model', '模型') }}</el-radio-button>
              <el-radio-button value="tenant">{{ t('reports.tenant', '租户') }}</el-radio-button>
              <el-radio-button value="person">{{ t('reports.person', '用户') }}</el-radio-button>
              <el-radio-button value="apikey">{{ t('reports.apiKey', 'apikey') }}</el-radio-button>
            </el-radio-group>
            <el-dropdown trigger="click">
              <el-button size="small">
                {{ t('reports.columns', '显示列') }} ({{ visibleColumns.length }}/{{ ALL_COLUMNS.length }})
              </el-button>
              <template #dropdown>
                <el-checkbox-group v-model="visibleColumns" class="col-picker">
                  <el-checkbox v-for="c in ALL_COLUMNS" :key="c" :value="c" :label="COLUMN_LABELS[c]()" />
                </el-checkbox-group>
              </template>
            </el-dropdown>
            <span class="coverage">
              {{ detail ? `${groupTitle} · ${t('reports.byDay', '按天明细')}` : groupTitle }}
              · {{ tableRows.length }} {{ t('reports.rows', '行') }}
            </span>
          </div>
        </template>

        <el-table :data="tableRows" size="small" border stripe max-height="560" v-loading="loading">
          <el-table-column :label="detail ? t('reports.date', '日期 / 维度') : groupTitle" min-width="220" fixed>
            <template #default="{ row }">{{ row.name }}</template>
          </el-table-column>
          <el-table-column v-if="isColVisible('requests')" :label="t('reports.requests', '请求数')" width="110" align="right">
            <template #default="{ row }">{{ fmtInt(row.totals.request_count) }}</template>
          </el-table-column>
          <el-table-column v-if="isColVisible('success')" :label="t('reports.success', '成功')" width="100" align="right">
            <template #default="{ row }">{{ fmtInt(row.totals.success_count) }}</template>
          </el-table-column>
          <el-table-column v-if="isColVisible('errors')" :label="t('reports.errors', '失败')" width="130" align="right">
            <template #default="{ row }">
              {{ fmtInt(row.totals.error_count) }}（{{ fmtPct(row.totals.error_rate) }}）
            </template>
          </el-table-column>
          <el-table-column v-if="isColVisible('errorRate')" :label="t('reports.errorRate', '失败率')" width="100" align="right">
            <template #default="{ row }">{{ fmtPct(row.totals.error_rate) }}</template>
          </el-table-column>
          <el-table-column v-if="isColVisible('quality')" :label="t('reports.qualityScore', '质量评分')" width="110" align="right">
            <template #default="{ row }">
              <el-tooltip
                v-if="row.score != null"
                :content="t('reports.qualityHint', '成功率 × 时效因子（P95 越低越高）')"
              >
                <span>{{ row.score.toFixed(1) }}</span>
              </el-tooltip>
              <span v-else>-</span>
            </template>
          </el-table-column>
          <el-table-column v-if="isColVisible('input')" :label="t('reports.inputTokens', '输入 tokens')" width="120" align="right">
            <template #default="{ row }">{{ fmtInt(row.totals.input_tokens) }}</template>
          </el-table-column>
          <el-table-column v-if="isColVisible('cache')" :label="t('reports.cacheTokens', '缓存 tokens')" width="120" align="right">
            <template #default="{ row }">
              {{ fmtInt(row.totals.cache_read_tokens + row.totals.cache_write_tokens) }}
            </template>
          </el-table-column>
          <el-table-column v-if="isColVisible('output')" :label="t('reports.outputTokens', '输出 tokens')" width="120" align="right">
            <template #default="{ row }">{{ fmtInt(row.totals.output_tokens) }}</template>
          </el-table-column>
          <el-table-column v-if="isColVisible('cacheRate')" :label="t('reports.cacheHit', '缓存率')" width="100" align="right">
            <template #default="{ row }">{{ fmtRatio(row.totals.cache_hit_ratio) }}</template>
          </el-table-column>
          <el-table-column v-if="isColVisible('cost')" :label="t('reports.cost', '成本')" width="120" align="right">
            <template #default="{ row }">
              {{ fmtMoneyFromCents(row.totals.estimated_cost_cents) }} {{ row.totals.currency }}
            </template>
          </el-table-column>
          <el-table-column v-if="isColVisible('credits')" :label="t('reports.internalCredits', '内部积分')" width="120" align="right">
            <template #default="{ row }">{{ fmtInt(row.totals.credits_charged) }}</template>
          </el-table-column>
          <el-table-column v-if="isColVisible('topError')" :label="t('reports.topError', '主要错误')" min-width="200">
            <template #default="{ row }">{{ topError(row.error_breakdown) }}</template>
          </el-table-column>
        </el-table>
      </el-card>

      <!--  -->
      <el-card v-if="!detail" shadow="never" class="block-card">
        <template #header>{{ t('reports.byDay', '按天') }}</template>
        <el-table :data="dayTableRows" size="small" border stripe max-height="420" v-loading="loading">
          <el-table-column prop="date" :label="t('reports.date', '日期')" width="130" fixed />
          <el-table-column :label="t('reports.requests', '请求数')" width="110" align="right">
            <template #default="{ row }">{{ fmtInt(row.totals.request_count) }}</template>
          </el-table-column>
          <el-table-column :label="t('reports.errors', '失败')" width="130" align="right">
            <template #default="{ row }">
              {{ fmtInt(row.totals.error_count) }}（{{ fmtPct(row.totals.error_rate) }}）
            </template>
          </el-table-column>
          <el-table-column :label="t('reports.totalTokens', '总 tokens')" width="130" align="right">
            <template #default="{ row }">{{ fmtInt(row.totals.total_tokens) }}</template>
          </el-table-column>
          <el-table-column :label="t('reports.cacheHit', '缓存率')" width="100" align="right">
            <template #default="{ row }">{{ fmtRatio(row.totals.cache_hit_ratio) }}</template>
          </el-table-column>
          <el-table-column :label="t('reports.cost', '成本')" width="130" align="right">
            <template #default="{ row }">
              {{ fmtMoneyFromCents(row.totals.estimated_cost_cents) }} {{ row.totals.currency }}
            </template>
          </el-table-column>
          <el-table-column :label="t('reports.topError', '主要错误')" min-width="180">
            <template #default="{ row }">{{ topError(row.error_breakdown) }}</template>
          </el-table-column>
        </el-table>
      </el-card>

      <!--  -->
      <el-card shadow="never" class="block-card">
        <template #header>
          {{ t('reports.modelStats', '模型统计清单') }}
          <span class="coverage">（{{ modelStatsRows.length }} · {{ t('reports.modelStatsHint', '点击展开，可直接挑模型名做筛选') }}）</span>
        </template>
        <el-collapse v-model="modelsOpen">
          <el-collapse-item name="models">
            <template #title>{{ t('reports.modelStats', '模型统计清单') }}</template>
            <el-table
              v-if="modelsOpen.includes('models')"
              :data="modelStatsRows"
              size="small"
              border
              stripe
              max-height="420"
              v-loading="loading"
            >
              <el-table-column prop="model" :label="t('reports.model', '模型')" min-width="240" />
              <el-table-column :label="t('reports.requests', '请求数')" width="110" align="right">
                <template #default="{ row }">{{ fmtInt(row.totals.request_count) }}</template>
              </el-table-column>
              <el-table-column :label="t('reports.errors', '失败')" width="130" align="right">
                <template #default="{ row }">
                  {{ fmtInt(row.totals.error_count) }}（{{ fmtPct(row.totals.error_rate) }}）
                </template>
              </el-table-column>
              <el-table-column :label="t('reports.totalTokens', '总 tokens')" width="130" align="right">
                <template #default="{ row }">{{ fmtInt(row.totals.total_tokens) }}</template>
              </el-table-column>
              <el-table-column :label="t('reports.cacheHit', '缓存率')" width="100" align="right">
                <template #default="{ row }">{{ fmtRatio(row.totals.cache_hit_ratio) }}</template>
              </el-table-column>
              <el-table-column :label="t('reports.cost', '成本')" width="120" align="right">
                <template #default="{ row }">
                  {{ fmtMoneyFromCents(row.totals.estimated_cost_cents) }} {{ row.totals.currency }}
                </template>
              </el-table-column>
              <el-table-column :label="t('reports.topError', '主要错误')" min-width="180">
                <template #default="{ row }">{{ topError(row.error_breakdown) }}</template>
              </el-table-column>
              <el-table-column :label="t('reports.actions', '操作')" width="100" align="center">
                <template #default="{ row }">
                  <el-button link type="primary" @click="filters.model = row.model">{{ t('reports.filterBy', '筛选') }}</el-button>
                </template>
              </el-table-column>
            </el-table>
          </el-collapse-item>
        </el-collapse>
      </el-card>
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
  margin-bottom: 12px;
}
.coverage {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.block {
  margin-bottom: 16px;
}
.filter-card {
  margin-bottom: 16px;
}
.filter-head {
  display: flex;
  align-items: center;
  gap: 12px;
}
.filter-badge {
  margin-left: 6px;
}
.filter-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 12px;
  margin-top: 12px;
}
.filter-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.filter-item label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.cards {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
  margin-bottom: 16px;
}
.card {
  flex: 1;
  min-width: 200px;
  border: 1px solid var(--el-border-color-light);
  border-radius: 8px;
  padding: 12px 16px;
  background: var(--el-bg-color);
}
.card-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.card-value {
  font-size: 24px;
  font-weight: 600;
  margin: 4px 0;
}
.card-value-sm {
  font-size: 18px;
  word-break: break-all;
}
.card-sub {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.block-card {
  margin-top: 16px;
}
.table-head {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.col-picker {
  display: flex;
  flex-direction: column;
  padding: 8px 12px;
  gap: 2px;
}
</style>
