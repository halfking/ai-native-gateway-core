<script setup lang="ts">
import { ref, onMounted, computed, onBeforeUnmount, watch } from 'vue'
import { localeRef } from '../i18n'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  getRequestLogs,
  getBodyCacheStats,
  getKeys,
  type RequestLogRow,
  type BodyCacheStats,
  type ApiKey,
  type RequestLogsResponse,
  type RequestLogsAggregate,
} from '../api'
import { getCredentialMonitorSummary } from '../api/credential-monitor'
import { getProviders, getProviderCredentials } from '../api/providers'
import ModelPicker from '../components/ModelPicker.vue'
import RequestLogDrawer from '../components/RequestLogDrawer.vue'
import SessionSummaryDrawer from '../components/SessionSummaryDrawer.vue'
import { isSuperAdmin, isDefaultTenant, getCurrentTenantId } from '../store'
import { openRequestDetailPage } from '../utils/openRequestDetailPage'

const rows = ref<RequestLogRow[]>([])
const keys = ref<ApiKey[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
// 2026-08-17: 详情 body 缓存统计（/api/admin/logs/body-cache-stats），
// super admin 可观测条用；非 super admin 或端点失败时保持 null 不渲染。
const bodyCache = ref<BodyCacheStats | null>(null)
const apiKeyId = ref<number | ''>('')
const keyword = ref('')
const modelFilter = ref('')
// 2026-08-09: 时间筛选升级为 preset + 自定义范围，替代旧的固定小时数下拉。
// 类型覆盖原 [1h/6h/24h/3d/7d] 与新增的 [今天/本周/本月/今年/自定义]。
type TimePreset =
  | 'h1' | 'h6' | 'h24' | 'd3' | 'd7'
  | 'today' | 'thisWeek' | 'thisMonth' | 'thisYear'
  | 'custom'
type DateRange = [Date | string, Date | string]
const timePreset = ref<TimePreset>('h24')
const customDateRange = ref<DateRange | null>(null)
const successFilter = ref<'' | 'success' | 'failure' | 'rate_limited' | 'in_progress'>('')
const errorKindFilter = ref('')
const usageSourceFilter = ref<'' | 'llm' | 'estimated'>('')
const gwSessionFilter = ref('')
const gwTaskFilter = ref('')
const summaryDrawerOpen = ref(false)

const page = ref(1)
const pageSize = ref(50)
const total = ref(0)
const aggregate = ref<RequestLogsAggregate | null>(null)
const autoRefresh = ref(false)
let autoRefreshTimer: ReturnType<typeof setInterval> | null = null

// 供应商/凭据下拉筛选：单一数据源 /api/credentials/monitor-summary，
// 该端点允许 tenant_admin 调用，前端从 credentials 数组派生 provider 列表。
// 切换 provider 时若当前 credentialFilter 不在派生选项里则清空，避免错位。
const providerFilter = ref<number | ''>('')
const credentialFilter = ref<number | ''>('')
const providerOptions = ref<{ id: number; name: string }[]>([])
const credentialOptions = ref<{ id: number; providerId: number; label: string }[]>([])

function startAutoRefresh() {
  stopAutoRefresh()
  autoRefreshTimer = setInterval(() => {
    if (!loading.value) {
      load()
    }
  }, 30000)
}

function stopAutoRefresh() {
  if (autoRefreshTimer !== null) {
    clearInterval(autoRefreshTimer)
    autoRefreshTimer = null
  }
}

watch(autoRefresh, (enabled) => {
  if (enabled) {
    startAutoRefresh()
  } else {
    stopAutoRefresh()
  }
})

onBeforeUnmount(() => {
  stopAutoRefresh()
})

const showCompressionGuide = ref(false)
// 2026-08-09: 按模型分组统计的收拢/展开状态，默认收拢以节省空间
const showByModelStats = ref(false)
// 2026-08-10: 统计概览 + 按模型统计合并为同一行卡片，两段各自独立展开/收拢。
const showOverviewStats = ref(false)
// 2026-08-10: 筛选条件区可折叠，默认收拢以最大化列表展示空间。
const showFilters = ref(false)

// Compute compression statistics for the info bar at the top.
const compressionStats = computed(() => {
  const present = rows.value.filter(r => r.compression_strategy || r.outbound_body)
  const delta = present.filter(r => r.compression_strategy === 'delta_append')
  const sliding = present.filter(r => r.compression_strategy && r.compression_strategy.startsWith('sliding_window'))
  const v7 = present.filter(r => r.compression_reason)
  const mechanical = present.filter(r => r.compression_strategy === 'mechanical_trim')
  return {
    totalCompressed: present.length,
    deltaCount: delta.length,
    slidingCount: sliding.length,
    v7Count: v7.length,
    mechanicalCount: mechanical.length,
  }
})

const activeRequestId = ref<string | null>(null)
const openDetailWithTrace = ref(false)

// 2026-07-02: 接入 vue-i18n，附件相关文案走 t() 键
// （键定义在 web/src/locales/*.ts，对齐参考文档 §6）。
const { t } = useI18n()

// Tenant info for display
const tenantLabel = computed(() => {
  const tenantId = getCurrentTenantId()
  const isAdmin = isSuperAdmin()
  const isDefault = isDefaultTenant()

  if (isAdmin && isDefault) {
    return t('requests.defaultTenantOptions.whole')
  } else if (isDefault) {
    return t('requests.defaultTenantOptions.defaultTenant')
  } else {
    return t('requests.defaultTenantOptions.tenantPrefix', { id: tenantId })
  }
})

// 2026-08-09: 非 default 租户最多查看最近 3 天；宽于 3 天的 preset 自动降级。
// default 租户可使用全部 preset，包括 d7/thisWeek/thisMonth/thisYear。
const PRESETS_NON_DEFAULT_DOWNSHIFT: Partial<Record<TimePreset, TimePreset>> = {
  thisWeek: 'd3',
  thisMonth: 'd3',
  thisYear: 'd3',
  d7: 'd3',
}

// Validate preset when tenant changes / user toggles preset.
// Non-default tenants can't widen past 3 days; if they hold a wider preset,
// silently drop to d3 instead of trampling their selection with an error.
function clampCustomDateRange() {
  if (timePreset.value !== 'custom' || !customDateRange.value || isDefaultTenant()) return
  const [startValue, endValue] = customDateRange.value
  const start = new Date(startValue)
  const end = new Date(endValue)
  if (Number.isNaN(start.getTime()) || Number.isNaN(end.getTime()) || end <= start) return
  const maxEnd = new Date(start.getTime() + 3 * 24 * 3600 * 1000)
  if (end > maxEnd) customDateRange.value = [start, maxEnd]
}

function normalizeTimePresetForTenant() {
  if (isDefaultTenant()) return
  const next = PRESETS_NON_DEFAULT_DOWNSHIFT[timePreset.value]
  if (next) timePreset.value = next
  clampCustomDateRange()
}

function onTimePresetChange() {
  normalizeTimePresetForTenant()
  if (timePreset.value !== 'custom') resetPageAndLoad()
}

// 2026-08-10: 时间范围预设项（滑动窗口 + 自然日历）。
// 非 default 租户最多查看 3 天，跨上限的选项（thisWeek/thisMonth/thisYear/d7）禁用。
interface TimePresetOption {
  value: TimePreset
  label: string
  group: 'window' | 'calendar'
  disabled?: boolean
  separator?: boolean
}
const timePresetOptions = computed<TimePresetOption[]>(() => [
  { value: 'h1', label: t('requests.list.filter.timeOptions.h1'), group: 'window' },
  { value: 'h6', label: t('requests.list.filter.timeOptions.h6'), group: 'window' },
  { value: 'h24', label: t('requests.list.filter.timeOptions.h24'), group: 'window' },
  { value: 'd3', label: t('requests.list.filter.timeOptions.d3'), group: 'window' },
  { value: 'd7', label: t('requests.list.filter.timeOptions.d7'), group: 'window', disabled: !isDefaultTenant() },
  { value: 'today', label: t('requests.list.filter.timeOptions.today'), group: 'calendar', separator: true },
  { value: 'thisWeek', label: t('requests.list.filter.timeOptions.thisWeek'), group: 'calendar', disabled: !isDefaultTenant() },
  { value: 'thisMonth', label: t('requests.list.filter.timeOptions.thisMonth'), group: 'calendar', disabled: !isDefaultTenant() },
  { value: 'thisYear', label: t('requests.list.filter.timeOptions.thisYear'), group: 'calendar', disabled: !isDefaultTenant() },
  { value: 'custom', label: t('requests.list.filter.timeOptions.custom'), group: 'calendar' },
])

// 2026-08-10: 「按模型统计」段是否因选择了单一模型过滤而隐藏。只有
// 该场景下才隐藏按钮；无模型过滤（含宽时间窗导致 by_model 为空）时
// 仍保持可见可展开。
const isSingleModelFiltered = computed(() => modelFilter.value !== '')

// 2026-08-10: 当前生效的筛选条件数（不含时间范围——h24 是始终存在的默认，
// 也不含分页），用于折叠时头部 badge 提示。
const activeFilterCount = computed(() => {
  let count = 0
  if (apiKeyId.value !== '') count++
  if (providerFilter.value !== '') count++
  if (credentialFilter.value !== '') count++
  if (successFilter.value !== '') count++
  if (errorKindFilter.value !== '') count++
  if (usageSourceFilter.value !== '') count++
  if (modelFilter.value !== '') count++
  if (keyword.value.trim() !== '') count++
  if (gwSessionFilter.value.trim() !== '') count++
  if (gwTaskFilter.value.trim() !== '') count++
  return count
})

// 点击预设 chip 选择时间范围，复用 onTimePresetChange 的租户降级逻辑。
function selectPreset(value: TimePreset) {
  if (value === timePreset.value) return
  timePreset.value = value
  onTimePresetChange()
}

// 一键清空全部条件：重置到默认 h24 并重载。
function clearAllFilters() {
  apiKeyId.value = ''
  providerFilter.value = ''
  credentialFilter.value = ''
  successFilter.value = ''
  errorKindFilter.value = ''
  usageSourceFilter.value = ''
  modelFilter.value = ''
  keyword.value = ''
  gwSessionFilter.value = ''
  gwTaskFilter.value = ''
  resetTimeFilter()
}

async function loadKeys() {
  try {
    keys.value = await getKeys()
  } catch {
    keys.value = []
  }
}

// 供应商/凭据下拉数据源按角色分流：
//   - super_admin 调 /api/providers + /api/providers/{id}/credentials，
//     数据契约与 /providers 页面一致；
//   - tenant_admin 调 /api/credentials/monitor-summary，后端对该端点加
//     了 c.tenant_id 过滤，仅返回当前租户范围内的凭据；
// 任一来源失败都不阻塞日志列表渲染，下拉回退为空。
async function loadCredentialOptions() {
  if (isSuperAdmin()) {
    try {
      const providers = await getProviders()
      providerOptions.value = providers
        .map((p) => ({ id: p.id, name: p.display_name || p.catalog_code }))
        .sort((a, b) => a.name.localeCompare(b.name))
      // 并发拉每个 provider 的 credential 列表。provider 数量一般较小（数十），
      // 这里全量加载与 /providers 页面策略一致。失败时该 provider 的凭据下拉
      // 退化为空，但其它 provider 不受影响。
      const allCreds: { id: number; providerId: number; label: string }[] = []
      await Promise.all(
        providers.map(async (p) => {
          try {
            const creds = await getProviderCredentials(p.id)
            for (const c of creds) {
              allCreds.push({ id: c.id, providerId: c.provider_id, label: c.label })
            }
          } catch {
            // 单个 provider 的凭据失败不影响其它 provider；保持当前已收集项。
          }
        }),
      )
      credentialOptions.value = allCreds
    } catch {
      providerOptions.value = []
      credentialOptions.value = []
    }
    return
  }

  // tenant_admin 路径
  try {
    const resp = await getCredentialMonitorSummary()
    const creds = resp.credentials ?? []
    credentialOptions.value = creds.map((c) => ({
      id: c.id,
      providerId: c.provider_id,
      label: c.label,
    }))
    const map = new Map<number, string>()
    for (const c of creds) {
      if (!map.has(c.provider_id)) {
        map.set(c.provider_id, c.provider_name)
      }
    }
    providerOptions.value = Array.from(map.entries())
      .map(([id, name]) => ({ id, name }))
      .sort((a, b) => a.name.localeCompare(b.name))
  } catch {
    providerOptions.value = []
    credentialOptions.value = []
  }
}

// 凭据下拉按当前 providerFilter 收敛；切 provider 时若 credentialFilter
// 已不在新选项里，自动清空，避免请求带一个与 provider 不匹配的 credential_id。
const filteredCredentialOptions = computed(() => {
  if (providerFilter.value === '') return credentialOptions.value
  return credentialOptions.value.filter((c) => c.providerId === providerFilter.value)
})

function onProviderFilterChange() {
  if (credentialFilter.value === '') return
  const stillVisible = filteredCredentialOptions.value.some((c) => c.id === credentialFilter.value)
  if (!stillVisible) credentialFilter.value = ''
}

// 2026-08-09: 用户自定义日期范围变更后自动重发请求。
// 注意 el-date-picker 的 value-format="YYYY-MM-DDTHH:mm:ssZ" 给的是 ISO 字符串，
// timeRange() 中再 .toISOString() 一次是幂等的（毫秒精度不会有偏移）。
function onCustomRangeChange() {
  normalizeTimePresetForTenant()
  resetPageAndLoad()
}

// 一键回到默认 24h
function resetTimeFilter() {
  timePreset.value = 'h24'
  customDateRange.value = null
  resetPageAndLoad()
}

// 脉络模式（按 gw_session_id / gw_task_id 追踪）应单独看一组请求，
// 不与供应商/凭据过滤叠加，避免出现"按供应商筛选后点击会话脉络，结果为 0 条"
// 这种矛盾组合。脉络进入与脉络清除都强制重置这两个过滤。
function clearProviderCredentialFilter() {
  providerFilter.value = ''
  credentialFilter.value = ''
}

// 2026-08-09: 从 preset + customRange 计算 from/to。
// - h1/h6/h24/d3/d7 → 滑窗
// - today/thisWeek/thisMonth/thisYear → 自然日历年边界（本地时区）
// - custom → 走 customDateRange，未选时回退到 24h 以保证请求不空
function timeRange() {
  const now = new Date()
  const end = new Date(now)
  let start: Date
  switch (timePreset.value) {
    case 'h1':
      start = new Date(end.getTime() - 1 * 3600 * 1000); break
    case 'h6':
      start = new Date(end.getTime() - 6 * 3600 * 1000); break
    case 'h24':
      start = new Date(end.getTime() - 24 * 3600 * 1000); break
    case 'd3':
      start = new Date(end.getTime() - 3 * 24 * 3600 * 1000); break
    case 'd7':
      start = new Date(end.getTime() - 7 * 24 * 3600 * 1000); break
    case 'today':
      start = new Date(); start.setHours(0, 0, 0, 0)
      end.setHours(23, 59, 59, 999); break
    case 'thisWeek': {
      // 本地时区的"周一到今天"。getDay(): 0=Sun..6=Sat；映射到 ISO 周一基准。
      start = new Date()
      const dow = start.getDay() || 7
      start.setDate(start.getDate() - (dow - 1))
      start.setHours(0, 0, 0, 0)
      break
    }
    case 'thisMonth':
      start = new Date(now.getFullYear(), now.getMonth(), 1); break
    case 'thisYear':
      start = new Date(now.getFullYear(), 0, 1); break
    case 'custom': {
      if (customDateRange.value) {
        const [s, e] = customDateRange.value
        return {
          from: new Date(s).toISOString(),
          to: new Date(e).toISOString(),
        }
      }
      // 自定义但未选范围时的兜底，避免发出一个无 from/to 的请求
      start = new Date(end.getTime() - 24 * 3600 * 1000)
      break
    }
  }
  return { from: start.toISOString(), to: end.toISOString() }
}

function onModelFilterChange(name: string | string[]) {
  const next = typeof name === 'string' ? name.trim() : ''
  // 选择模型后立即重发请求。ModelPicker 是单选交互（用户点完即期望生效），
  // 不像 provider/credential 下拉那样依赖手动点击「查询」按钮。
  // 跳过空值→空值的赋值（v-model 在弹窗关闭/重渲时也会触发），避免无意义请求。
  if (next === modelFilter.value) return
  modelFilter.value = next
  resetPageAndLoad()
}

const ERROR_KIND_LABELS: Record<string, string> = {
  model_not_found: t('requests.errorKind.model_not_found'),
  provider_error: t('requests.errorKind.provider_error'),
  auth_error: t('requests.errorKind.auth_error'),
  missing_key: t('requests.errorKind.missing_key'),
  invalid_key: t('requests.errorKind.invalid_key'),
  auth_unavailable: t('requests.errorKind.auth_unavailable'),
  body_read_error: t('requests.errorKind.body_read_error'),
  body_too_large: t('requests.errorKind.body_too_large'),
  json_parse_error: t('requests.errorKind.json_parse_error'),
  rate_limit: t('requests.errorKind.rate_limit'),
  rate_limit_exceeded: t('requests.errorKind.rate_limit_exceeded'),
  key_throttled: t('requests.errorKind.key_throttled'),
  budget_exhausted: t('requests.errorKind.budget_exhausted'),
  insufficient_credits: t('requests.errorKind.insufficient_credits'),
  timeout: t('requests.errorKind.timeout'),
  canceled: t('requests.errorKind.canceled'),
  upstream_error: t('requests.errorKind.upstream_error'),
  stream_error: t('requests.errorKind.stream_error'),
  no_candidate: t('requests.errorKind.no_candidate'),
  session_forbidden: t('requests.errorKind.session_forbidden'),
  executor_unavailable: t('requests.errorKind.executor_unavailable'),
  empty_response: t('requests.errorKind.unknown_failure'),
  empty_upstream_response: t('requests.errorKind.unknown_failure'),
}

// 2026-06-19 T-NEW-7: labels for actual gateway failure codes (the only
// values that should ever appear in failure_detail_code now that
// upstream_finish_reason has been split out). eof_without_done and
// client_cancel are kept as "successful with caveat" in the status
// column; only the "真" gateway errors get a Chinese label here.
const FAILURE_DETAIL_LABELS: Record<string, string> = {
  gw_rpm_exceeded: t('requests.gwErrorKind.gw_rpm_exceeded'),
  gw_concurrent_exceeded: t('requests.gwErrorKind.gw_concurrent_exceeded'),
  gw_tpm_exceeded: t('requests.gwErrorKind.gw_tpm_exceeded'),
  gw_key_throttled: t('requests.gwErrorKind.gw_key_throttled'),
  gw_budget_exhausted: t('requests.gwErrorKind.gw_budget_exhausted'),
  gw_no_candidate: t('requests.gwErrorKind.gw_no_candidate'),
  gw_session_forbidden: t('requests.gwErrorKind.gw_session_forbidden'),
  eof_without_done: t('requests.gwErrorKind.eof_without_done'),
  stream_timeout: t('requests.gwErrorKind.stream_timeout'),
  client_cancel: t('requests.gwErrorKind.client_cancel'),
  client_disconnected: t('requests.gwErrorKind.client_disconnected'),
  no_deltas: t('requests.gwErrorKind.no_deltas'),
  invalid_first_chunk: t('requests.gwErrorKind.invalid_first_chunk'),
  invalid_json: t('requests.gwErrorKind.invalid_json'),
  upstream_5xx: t('requests.gwErrorKind.upstream_5xx'),
  upstream_4xx: t('requests.gwErrorKind.upstream_4xx'),
  unexpected_status: t('requests.gwErrorKind.unexpected_status'),
  connection_reset: t('requests.gwErrorKind.connection_reset'),
  write_failed: t('requests.gwErrorKind.write_failed'),
  hangup: t('requests.gwErrorKind.hangup'),
  body_too_large: t('requests.gwErrorKind.body_too_large'),
  eof_mid_tool_call: t('requests.gwErrorKind.eof_mid_tool_call'),
  first_byte_timeout: t('requests.gwErrorKind.first_byte_timeout'),
}

function statusLabel(row: RequestLogRow): string {
  if (row.request_status === 'in_progress') return t('requests.resultInProgress')
  if (row.request_status === 'rate_limited') return t('requests.list.filter.resultRateLimited') || '限流'
  if (row.request_status === 'success' || row.success) return t('requests.resultSuccess')
  // 2026-06-19 T-NEW-7: failure_detail_code now contains ONLY real failure
  // codes. upstream_finish_reason is informational and should never be
  // read as a failure label.
  const detail = row.failure_detail_code || ''
  if (FAILURE_DETAIL_LABELS[detail]) return FAILURE_DETAIL_LABELS[detail]
  const kind = row.error_kind || ''
  if (ERROR_KIND_LABELS[kind]) return ERROR_KIND_LABELS[kind]
  if (detail.startsWith('gw_')) return `网关:${detail.slice(3)}`
  return kind || detail || t('requests.resultFailure')
}

function statusTitle(row: RequestLogRow): string {
  const parts: string[] = []
  if (row.failure_stage) parts.push(`stage=${row.failure_stage}`)
  if (row.error_kind) parts.push(`error_kind=${row.error_kind}`)
  if (row.failure_detail_code) parts.push(`detail=${row.failure_detail_code}`)
  // 2026-06-19 T-NEW-7: surface the upstream finish_reason separately so
  // operators can still see it on a successful row (and confirm it really
  // is a normal `stop` / `tool_calls` finish, not a disguised failure).
  if (row.upstream_finish_reason) {
    parts.push(`finish=${row.upstream_finish_reason}`)
  }
  return parts.join(' · ') || ''
}

function statusColor(row: RequestLogRow): string {
  if (row.request_status === 'in_progress') return 'var(--warning)'
  if (row.request_status === 'success' || row.success) return 'var(--success)'
  return 'var(--error)'
}

// jumpToParent filters the list down to the parent of the current
// compressed row. Lets operators click "← <prefix>" in the compression
// badge and immediately see the original (pre-compression) request.
//
// Round 47 compression v7 Q5: the parent breadcrumb click handler.
// We use gw_task_id (which is stable across the original + retry
// attempts) so the parent row appears in the filtered list alongside
// any sibling compressed rows for the same task. If the row has no
// task_id, we fall back to scrolling the badge title into view (no
// filter is applied — the meta popover already shows the full id).
function jumpToParent(row: RequestLogRow) {
  if (!row.parent_request_id) return
  if (row.gw_task_id) {
    gwTaskFilter.value = row.gw_task_id
  }
  // No requestIdFilter exists in this view; the badge's title attr
  // already exposes the full parent id for copy/paste.
}

// Round 47 compression v7 + v3 session-level (2026-06-19): badge + label
// for the compression_reason / compression_strategy pair. Returns null
// when the request was not compressed at all (neither v7 nor v3 fired).
function compressionLabel(row: RequestLogRow): { reason: string; strategy: string; tip: string } | null {
  // v3 strategies have no compression_reason (they're proactive, not 4xx-triggered).
  // v7 strategies always have compression_reason. Either way, a non-null
  // compression_strategy means something fired.
  if (!row.compression_reason && !row.compression_strategy) return null
  const reasonMap: Record<string, string> = {
    'mode_1_auto_threshold': t('requests.mode_1_auto_threshold'),
    'mode_2_on_4xx': t('requests.mode_2_on_4xx'),
    'sliding_window_token': t('requests.sliding_window_token'),
    'sliding_window_count': t('requests.sliding_window_count'),
    'sliding_window_idle': t('requests.sliding_window_idle'),
    'sliding_window_mechanical_trim': t('requests.sliding_window_mechanical_trim'),
  }
  const strategyMap: Record<string, string> = {
    'mechanical_trim': t('requests.mechanical_trim'),
    'memora_l1_inject': t('requests.memora_l1_inject'),
    'llm_summary': t('requests.llm_summary'),
    'noop': t('requests.noop'),
    // v3 (2026-06-19) session-level strategies
    'delta_append': t('requests.delta_append'),
    'sliding_window_token': t('requests.sliding_window_token'),
    'sliding_window_count': t('requests.sliding_window_count'),
    'sliding_window_idle': t('requests.sliding_window_idle'),
  }
  // For v3 sliding-window triggered entries, the "reason" label is the
  // window trigger (stored in compression_strategy) and the v7 reason
  // column is empty. Display the trigger as the reason in that case.
  let reason = reasonMap[row.compression_reason || ''] || row.compression_reason || ''
  let strategy = strategyMap[row.compression_strategy || ''] || (row.compression_strategy || '?')
  // Special case: v3 delta_append has compression_reason empty + strategy = 'delta_append'.
  // Treat as t('requests.delta_append') strategy with reason t('requests.same_session_delta').
  if (row.compression_strategy === 'delta_append' && !row.compression_reason) {
    reason = t('requests.same_session_delta')
  }
  // Build a tooltip with byte/token deltas from compression_meta when present.
  let tip = `原因: ${reason}\n策略: ${strategy}`
  const meta = row.compression_meta as Record<string, any> | null
  if (meta) {
    if (meta.tokens_before && meta.tokens_after) {
      const ratio = Math.round((meta.tokens_after / meta.tokens_before) * 100)
      tip += `\nTokens: ${meta.tokens_before} → ${meta.tokens_after} (${ratio}%)`
    }
    if (meta.bytes_before && meta.bytes_after) {
      const kbBefore = Math.round(meta.bytes_before / 1024)
      const kbAfter = Math.round(meta.bytes_after / 1024)
      tip += `\nBytes: ${kbBefore}KB → ${kbAfter}KB`
    }
    if (meta.latency_ms) {
      tip += `\n延迟: ${meta.latency_ms}ms`
    }
    // v3 fields: window_triggered + summary_marker
    if (meta.window_triggered) {
      tip += `\n触发: ${meta.window_triggered}`
    }
    if (meta.summary_marker) {
      tip += `\n摘要标记: ${String(meta.summary_marker).slice(0, 24)}…`
    }
  }
  // v3 outbound counts (always when outbound body was set, regardless of meta)
  if (typeof row.outbound_msg_count === 'number') {
    tip += `\n转发消息数: ${row.outbound_msg_count}`
    if (typeof row.outbound_token_est === 'number') {
      tip += ` (≈${row.outbound_token_est} tokens)`
    }
  }
  if (row.parent_request_id) {
    tip += `\n父请求: ${row.parent_request_id}`
  }
  return { reason, strategy, tip }
}

const traceMode = computed(() =>
  Boolean(gwTaskFilter.value.trim() || gwSessionFilter.value.trim()),
)

// listColCount — 列表表头/空态占位的列数。基础 10 列（时间/脉络/会话标题/调用方/
// 路由/Token/延迟/压缩/状态/附件），trace 模式多一个序号列，非默认租户多一个
// 积分列，super_admin 额外多一个「流程详情」列。
// 2026-08-06: 加了「会话标题」列 (col-title) — session_titles.title 注入。
const listColCount = computed(() =>
  10 + (traceMode.value ? 1 : 0) + (isDefaultTenant() ? 0 : 1) + (isSuperAdmin() ? 1 : 0),
)

const taskSummary = computed(() => {
  if (!traceMode.value || !rows.value.length) return null
  let ok = 0
  let fail = 0
  let pending = 0
  let rateLimited = 0
  for (const r of rows.value) {
    if (r.request_status === 'in_progress') pending++
    else if (r.request_status === 'rate_limited') rateLimited++
    else if (r.request_status === 'success' || r.success) ok++
    else fail++
  }
  return { total: rows.value.length, ok, fail, pending, rateLimited }
})

/** 点击脉络：优先按会话聚合（同一会话含多步请求）；无会话时按任务 ID */
function filterByTrace(row: RequestLogRow) {
  if (row.gw_session_id) {
    gwSessionFilter.value = row.gw_session_id
    gwTaskFilter.value = ''
  } else if (row.gw_task_id) {
    gwTaskFilter.value = row.gw_task_id
    gwSessionFilter.value = ''
  } else {
    return
  }
  widenRangeForTrace()
  clearProviderCredentialFilter()
  resetPageAndLoad()
}

function filterByTask(taskId: string | null | undefined) {
  if (!taskId) return
  gwTaskFilter.value = taskId
  gwSessionFilter.value = ''
  widenRangeForTrace()
  clearProviderCredentialFilter()
  resetPageAndLoad()
}

function filterBySession(sessionId: string | null | undefined) {
  if (!sessionId) return
  gwSessionFilter.value = sessionId
  gwTaskFilter.value = ''
  widenRangeForTrace()
  clearProviderCredentialFilter()
  resetPageAndLoad()
}

// 2026-08-09: 脉络视图拉宽时间窗与页大小到「本月」/200 条。
// 用户已选 custom 时保留精确范围；非 default 租户仍由 normalizeTimePresetForTenant()
// 强制遵守最近 3 天上限。
function widenRangeForTrace() {
  if (timePreset.value !== 'custom' && !['thisMonth', 'thisYear'].includes(timePreset.value)) {
    timePreset.value = 'thisMonth'
  }
  normalizeTimePresetForTenant()
  if (pageSize.value < 200) pageSize.value = 200
}

function clearTraceFilter() {
  gwTaskFilter.value = ''
  gwSessionFilter.value = ''
  resetPageAndLoad()
}

const canSummarizeSession = computed(() => gwSessionFilter.value.trim().length > 0)

const summaryDrawerTitle = computed(() => {
  const sid = gwSessionFilter.value.trim()
  if (!sid) return null
  const row = rows.value.find(r => r.gw_session_id === sid && r.session_title)
  return row?.session_title ?? null
})

function openSessionSummaryDrawer() {
  if (!canSummarizeSession.value) return
  summaryDrawerOpen.value = true
}

function onDrawerFilterSession(sessionId: string) {
  filterBySession(sessionId)
}

function onDrawerOpenRequest(requestId: string) {
  showDetail(requestId)
}

function routeProviderLine(r: RequestLogRow): string {
  const parts: string[] = []
  if (r.provider_name) parts.push(r.provider_name)
  else if (r.provider_code) parts.push(r.provider_code)
  if (r.credential_label) parts.push(r.credential_label)
  if (parts.length) return parts.join(' · ')
  if (r.error_kind === 'missing_key' || r.error_kind === 'invalid_key') return t('requests.none')
  return t('requests.none')
}

function routeModelLine(r: RequestLogRow): string {
  const requestModel = r.canonical_name || r.client_model || t('requests.none')
  const providerModel = (r.provider_model || r.outbound_model || '').trim()
  if (!providerModel || providerModel.toLowerCase() === requestModel.toLowerCase()) {
    return requestModel
  }
  return `${requestModel} → ${providerModel}`
}

function routeModelTitle(r: RequestLogRow): string {
  const requestModel = r.canonical_name || r.client_model || t('requests.none')
  const providerModel = (r.provider_model || r.outbound_model || '').trim()
  if (!providerModel || providerModel.toLowerCase() === requestModel.toLowerCase()) {
    return `请求模型: ${requestModel}`
  }
  return `请求模型: ${requestModel} → 供应商模型: ${providerModel}`
}

function ellipsize(value: string | null | undefined, max = 28): string {
  const s = (value ?? '').trim()
  if (!s) return t('requests.none')
  if (s.length <= max) return s
  return s.slice(0, Math.max(1, max - 1)) + '…'
}

function callerUserLine(r: RequestLogRow): string {
  if (r.api_key_owner_user) return r.api_key_owner_user
  if (r.end_user_id) return r.end_user_id
  if (r.application_code) return r.application_code
  return t('requests.none')
}

function callerUserTitle(r: RequestLogRow): string {
  const parts: string[] = []
  if (r.api_key_owner_user) parts.push(`用户: ${r.api_key_owner_user}`)
  if (r.end_user_id) parts.push(`终端用户: ${r.end_user_id}`)
  if (r.application_code) parts.push(`应用: ${r.application_code}`)
  return parts.join(' · ') || t('requests.none')
}

function callerKeyLine(r: RequestLogRow): string {
  const key = r.api_key_prefix ?? (r.api_key_id != null ? `key#${r.api_key_id}` : t('requests.noKey'))
  if (r.application_code && r.api_key_owner_user) return `${key} · ${r.application_code}`
  if (r.application_code) return `${key} · ${r.application_code}`
  return key
}

function callerKeyTitle(r: RequestLogRow): string {
  const parts: string[] = []
  if (r.api_key_prefix) parts.push(`Key: ${r.api_key_prefix}`)
  else if (r.api_key_id != null) parts.push(`Key ID: ${r.api_key_id}`)
  else parts.push(t('requests.noKeyDetail'))
  if (r.application_code) parts.push(`应用: ${r.application_code}`)
  return parts.join(' · ')
}

function traceSessionTitle(id: string) {
  return `会话 ID（点击筛选同脉络）\n${id}`
}

function traceTaskTitle(id: string) {
  return `任务 ID（点击仅筛此任务）\n${id}`
}

// 详情 body 缓存统计随列表刷新一起拉取。纯诊断信息：失败静默置 null
// （不渲染该条），不阻塞、不污染列表加载状态。
async function loadBodyCache() {
  if (!isSuperAdmin()) return
  try {
    bodyCache.value = await getBodyCacheStats()
  } catch {
    bodyCache.value = null
  }
}

// 命中率：无流量（hits+misses=0）时显示 "—"，避免冷启动误读为 0%。
const bodyCacheHitRate = computed(() => {
  if (!bodyCache.value) return '—'
  const { hits, misses, hit_rate: rate } = bodyCache.value
  return hits + misses > 0 ? `${(rate * 100).toFixed(1)}%` : '—'
})

// tooltip 与容量分母都取后端回显的 cap，不在前端硬编码 LRU 上限。
const bodyCacheTitle = computed(
  () =>
    `/api/logs/{id} 详情 body 抓取的进程内缓存（LRU ${bodyCache.value?.cap ?? '—'} × TTL 5min）。未命中走热分区（亚毫秒）或列存冷路径（秒级）；条目指当前缓存内 request 数。`,
)

async function load() {
  loading.value = true
  error.value = null
  void loadBodyCache()
  try {
    const range = timeRange()
    const resp: RequestLogsResponse = await getRequestLogs({
      api_key_id: apiKeyId.value === '' ? undefined : Number(apiKeyId.value),
      provider_id: providerFilter.value === '' ? undefined : Number(providerFilter.value),
      credential_id: credentialFilter.value === '' ? undefined : Number(credentialFilter.value),
      from: range.from,
      to: range.to,
      q: keyword.value.trim() || undefined,
      request_status: successFilter.value === '' ? undefined : successFilter.value,
      error_kind: errorKindFilter.value.trim() || undefined,
      model: modelFilter.value || undefined,
      usage_source: usageSourceFilter.value === '' ? undefined : usageSourceFilter.value,
      gw_session_id: gwSessionFilter.value.trim() || undefined,
      gw_task_id: gwTaskFilter.value.trim() || undefined,
      chrono: traceMode.value || undefined,
      page: page.value,
      page_size: pageSize.value,
    })
    rows.value = resp.items
    total.value = resp.count
    aggregate.value = resp.aggregate ?? null
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function changePage(delta: number) {
  const max = Math.max(1, Math.ceil(total.value / pageSize.value))
  const next = page.value + delta
  if (next < 1 || next > max) return
  page.value = next
  load()
}

function resetPageAndLoad() {
  page.value = 1
  load()
}

function fmtTs(ts: string) {
  return new Date(ts).toLocaleString(localeRef.value, { hour12: false })
}

function fmtDate(ts: string) {
  return new Date(ts).toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' })
}

function fmtTime(ts: string) {
  return new Date(ts).toLocaleTimeString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function token(v: number | null | undefined, usageSource?: 'llm' | 'estimated' | null) {
  if (v == null) return t('requests.none')
  const formatted = v.toLocaleString()
  // Mark estimated values with a tilde prefix + tooltip to distinguish from
  // upstream-reported counts. Estimated values come from local text heuristics
  // when the provider (e.g. minimax) does not return a usage block.
  if (usageSource === 'estimated') {
    return `~${formatted}`
  }
  return formatted
}

function tokenTitle(usageSource?: 'llm' | 'estimated' | null): string {
  if (usageSource === 'estimated') return t('requests.estimatedNote')
  if (usageSource === 'llm') return t('requests.llmReported')
  return ''
}

function costDisplay(v: number | string | null | undefined, currency: string | null | undefined) {
  if (v == null) return currency ? `待定价(${currency})` : t('requests.pendingPricingNoCurrency')
  const amount = Number(v).toFixed(6)
  return currency ? `${amount} ${currency}` : amount
}

function creditsDisplay(v: number | null | undefined): string {
  if (v == null || v <= 0) return t('requests.none')
  return v.toLocaleString()
}

// 统计卡数字格式化：null/0 都显示 —，与其它 token 列保持一致的可读风格。
function formatStatNumber(v: number | null | undefined): string {
  if (v == null) return '—'
  return v.toLocaleString()
}

function formatStatCost(v: number | null | undefined): string {
  if (v == null) return '—'
  return Number(v).toFixed(4)
}

function shortHash(v: string | null | undefined) {
  return v ? `${v.slice(0, 12)}…` : t('requests.none')
}

const route = useRoute()
const router = useRouter()

function showDetail(requestId: string) {
  openRequestDetailPage(requestId, undefined, router)
}

function closeDetail() {
  activeRequestId.value = null
  openDetailWithTrace.value = false
}

function syncSessionTitle({
  taskId,
  sessionId,
  title,
}: {
  taskId: string
  sessionId: string | null
  title: string | null
}) {
  for (const row of rows.value) {
    if (row.gw_task_id === taskId && row.gw_session_id === sessionId) {
      row.session_title = title
    }
  }
}

function summaryEnvelopeMessage(summary: any): any[] | null {
  if (!summary || typeof summary !== 'object') return null
  const bytes = typeof summary.bytes === 'number' ? summary.bytes : Number(summary.bytes || 0)
  const truncated = summary.head_truncated === true
  return [{
    role: 'gateway',
    content: truncated
      ? `[已摘要化: 原始 ${bytes} bytes, head 已截断]`
      : `[已摘要化: 原始 ${bytes} bytes]`,
  }]
}

function extractMessagesFromBody(body: any): any[] {
  if (body == null) return []
  if (Array.isArray(body)) return body
  if (typeof body === 'string') {
    try { body = JSON.parse(body) } catch { return [] }
  }
  const summaryMessage = summaryEnvelopeMessage(body?._gw_body_summary)
  if (summaryMessage) return summaryMessage
  if (body.messages && Array.isArray(body.messages)) return body.messages
  if (body.choices && Array.isArray(body.choices)) {
    const msgs: any[] = []
    for (const c of body.choices) {
      if (c.message) msgs.push(c.message)
    }
    return msgs
  }
  return [body]
}

// v3 savings helpers (2026-06-20). Compute human-readable byte/token
// savings between request_body and outbound_body.
function bodyBytes(obj: any): number {
  if (!obj) return 0
  if (typeof obj === 'string') return new Blob([obj]).size
  return new Blob([JSON.stringify(obj)]).size
}

function calcSavingDetail(row: any): { savingStr: string; tokenSavingStr: string; msgReductionStr: string; hasSaving: boolean } {
  const hasOutbound = !!row.outbound_body
  if (!hasOutbound) return { savingStr: '', tokenSavingStr: '', msgReductionStr: '', hasSaving: false }
  const reqBytes = bodyBytes(row.request_body)
  const outBytes = bodyBytes(row.outbound_body)
  const savingBytes = reqBytes - outBytes
  const savingPct = reqBytes > 0 ? Math.round((savingBytes / reqBytes) * 100) : 0

  // Token saving: estimate from request vs outbound
  const reqTok = row.outbound_token_est ? Math.round(row.outbound_token_est * (reqBytes / (outBytes || 1))) : 0
  const outTok = row.outbound_token_est || 0
  const tokDiff = reqTok - outTok
  const tokPct = reqTok > 0 ? Math.round((tokDiff / reqTok) * 100) : 0

  // Message reduction: count messages from request vs outbound
  const reqMsgs = extractMessagesFromBody(row.request_body).length
  const outMsgs = row.outbound_msg_count ?? extractMessagesFromBody(row.outbound_body).length
  const msgDiff = reqMsgs - outMsgs
  const msgPct = reqMsgs > 0 ? Math.round((msgDiff / reqMsgs) * 100) : 0

  const fmtBytes = (b: number) => b > 1024 ? `${(b / 1024).toFixed(1)}KB` : `${b}B`
  const savingStr = reqBytes > outBytes ? `-${fmtBytes(savingBytes)} (${savingPct}%)` : '≈0'
  const tokenSavingStr = tokDiff > 0 ? `-${tokDiff} (${tokPct}%)` : '≈0'
  const msgReductionStr = msgDiff > 0 ? `-${msgDiff} (${msgPct}%)` : `${msgDiff >= 0 ? '0' : '+' + Math.abs(msgDiff)}`

  return { savingStr, tokenSavingStr, msgReductionStr, hasSaving: true }
}

// super_admin 在每条日志行可直接打开共享请求详情，并展开流程面板。
function gotoTrace(requestId: string) {
  openRequestDetailPage(requestId, { tab: 'flow' }, router)
}

onMounted(async () => {
  const q = route.query
  if (q.success === 'success' || q.success === 'failure' || q.success === 'rate_limited' || q.success === 'in_progress') {
    successFilter.value = q.success
  }
  if (typeof q.error_kind === 'string' && q.error_kind.trim()) {
    errorKindFilter.value = q.error_kind.trim()
  }
  // 2026-08-09: 时间范围 query 兼容。
  // 优先级: 新版 ?preset=... > 旧版 ?hours=N (向后兼容, 仅识别 1/6/24/72/168) > 自定义 from/to。
  const hourToPreset: Record<number, TimePreset> = {
    1: 'h1', 6: 'h6', 24: 'h24', 72: 'd3', 168: 'd7',
  }
  if (typeof q.preset === 'string' && (['h1','h6','h24','d3','d7','today','thisWeek','thisMonth','thisYear','custom'] as TimePreset[]).includes(q.preset as TimePreset)) {
    timePreset.value = q.preset as TimePreset
  } else if (typeof q.hours === 'string' && /^\d+$/.test(q.hours)) {
    const mapped = hourToPreset[Number(q.hours)]
    if (mapped) timePreset.value = mapped
  }
  if (timePreset.value === 'custom' && typeof q.from === 'string' && typeof q.to === 'string') {
    const s = new Date(q.from)
    const e = new Date(q.to)
    if (!isNaN(s.getTime()) && !isNaN(e.getTime())) {
      customDateRange.value = [s, e]
    }
  }
  normalizeTimePresetForTenant()
  // 2026-08-06: 允许从其他视图（如 Dashboard 的"会话总结"按钮）通过 query 预填
  // 会话/任务筛选。任一参数存在即拉宽时间窗与页大小，避免汇总结果落在默认
  // 24h/50 条外。
  const fromQuery = (key: string) =>
    typeof q[key] === 'string' && (q[key] as string).trim() ? (q[key] as string).trim() : ''
  const sessionId = fromQuery('gw_session_id')
  const taskId = fromQuery('gw_task_id')
  if (sessionId || taskId) {
    if (sessionId) {
      gwSessionFilter.value = sessionId
      gwTaskFilter.value = ''
    } else {
      gwTaskFilter.value = taskId
      gwSessionFilter.value = ''
    }
    if (!['thisMonth', 'thisYear'].includes(timePreset.value)) {
      timePreset.value = 'thisMonth'
    }
    normalizeTimePresetForTenant()
    if (pageSize.value < 200) pageSize.value = 200
  }
  // 2026-07-03: 添加错误处理，确保即使 API 失败页面也能正常显示
  try {
    await loadKeys()
  } catch (e) {
    console.error('Failed to load keys:', e)
    keys.value = []
  }

  // 供应商/凭据下拉加载失败由 loadCredentialOptions() 内部兜底，
  // 此处不需再包一层 try/catch。
  await loadCredentialOptions()

  try {
    await load()
  } catch (e) {
    console.error('Failed to load request logs:', e)
    error.value = e instanceof Error ? e.message : String(e)
    loading.value = false
  }

  if (sessionId && (q.open_summary === '1' || q.open_summary === 'true')) {
    summaryDrawerOpen.value = true
  }
})
</script>

<template>
  <div>
    <div class="page-header" style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px">
      <h2 style="margin:0">请求日志</h2>
      <div style="display:flex;gap:8px;align-items:center">
        <span class="tenant-badge" :class="{ 'tenant-badge--admin': isSuperAdmin(), 'tenant-badge--default': isDefaultTenant() }">
          {{ tenantLabel }}
        </span>
        <label style="display:flex;align-items:center;gap:4px;font-size:12px;cursor:pointer;user-select:none">
          <input type="checkbox" v-model="autoRefresh" style="cursor:pointer" />
          <span>自动刷新</span>
        </label>
        <button class="btn btn-primary btn-sm" :disabled="loading" @click="load">刷新</button>
      </div>
    </div>

    <!-- 2026-08-17: 详情 body 缓存可观测条（super admin 专属，随列表刷新更新）。
         低命中率说明重复点击少（miss 走热分区亚毫秒，代价低）；evictions > 0
         说明容量吃紧，需要评估调大 LRU 上限。容量分母取后端 cap 字段（audit:
         不硬编码 1024）；无流量时命中率显示 "—" 而非误导性的 0.0%。 -->
    <div v-if="isSuperAdmin() && bodyCache" class="body-cache-strip" :title="bodyCacheTitle">
      <span class="body-cache-strip__label">⚡ 详情缓存</span>
      <span>命中率 <strong>{{ bodyCacheHitRate }}</strong></span>
      <span>命中 {{ bodyCache.hits }}</span>
      <span>未命中 {{ bodyCache.misses }}</span>
      <span>条目 {{ bodyCache.size }}<template v-if="bodyCache.cap">/{{ bodyCache.cap }}</template></span>
      <span>逐出 {{ bodyCache.evictions }}</span>
    </div>

    <div v-if="!isDefaultTenant()" class="tenant-notice" style="margin-bottom:12px;padding:8px 12px;background:rgba(59,130,246,0.1);border:1px solid rgba(59,130,246,0.3);border-radius:6px;font-size:12px;color:#3b82f6">
      非 default 租户只能查看最近 3 天的请求日志
    </div>

    <!-- 2026-08-10: 统计概览 + 按模型统计合并为同一行卡片。
         头部两段各自独立展开/收拢，默认收拢只占一行，最大化列表展示空间。
         数据由 /api/logs 的 aggregate 字段返回，与分页无关。 -->
    <div
      v-if="aggregate"
      class="stats-card"
      style="margin-bottom:12px;border:1px solid var(--border);border-radius:8px;overflow:hidden;font-size:12px"
    >
      <div style="display:flex;align-items:stretch">
        <!-- 段1：统计概览 -->
        <div
          class="stats-segment"
          :class="{ 'stats-segment--active': showOverviewStats }"
          :style="{ flex: '1', borderRight: !isSingleModelFiltered ? '1px solid var(--border)' : 'none' }"
          role="button"
          :aria-expanded="showOverviewStats"
          @click="showOverviewStats = !showOverviewStats"
        >
          <span style="font-weight:600;display:flex;align-items:center;gap:6px;white-space:nowrap">
            <span>📊 统计概览</span>
            <span class="badge badge-blue" style="font-size:10px;padding:2px 6px;white-space:nowrap">
              {{ formatStatNumber(aggregate.total_requests) }} 请求
            </span>
          </span>
          <span style="color:var(--text-secondary);font-size:11px;white-space:nowrap">{{ showOverviewStats ? '收拢 ▲' : '展开 ▼' }}</span>
        </div>
        <!-- 段2：按模型统计 -->
        <div
          v-if="!isSingleModelFiltered"
          class="stats-segment"
          :class="{ 'stats-segment--active': showByModelStats }"
          style="flex:1"
          role="button"
          :aria-expanded="showByModelStats"
          @click="showByModelStats = !showByModelStats"
        >
          <span style="font-weight:600;display:flex;align-items:center;gap:6px;white-space:nowrap">
            <span>📈 按模型统计</span>
            <span v-if="aggregate.by_model && aggregate.by_model.length" class="badge badge-blue" style="font-size:10px;padding:2px 6px;white-space:nowrap">
              {{ aggregate.by_model.length }} 个模型
            </span>
          </span>
          <span style="color:var(--text-secondary);font-size:11px;white-space:nowrap">{{ showByModelStats ? '收拢 ▲' : '展开 ▼' }}</span>
        </div>
      </div>

      <!-- 概览详情：三联指标 + token 拆分/成本 -->
      <div v-if="showOverviewStats" style="padding:12px;border-top:1px solid var(--border)">
        <div
          style="display:grid;gap:10px"
          :style="{ gridTemplateColumns: isDefaultTenant() ? 'repeat(3, 1fr)' : 'repeat(2, 1fr)' }"
        >
          <div class="stat-overview-card" data-stat="total-requests">
            <div class="stat-overview-label">{{ t('requests.list.stats.totalRequests') }}</div>
            <div class="stat-overview-value">{{ aggregate.total_requests.toLocaleString() }}</div>
            <div class="stat-overview-sub">{{ t('requests.list.stats.scopeAll') }}</div>
          </div>
          <div class="stat-overview-card" data-stat="total-tokens">
            <div class="stat-overview-label">{{ t('requests.list.stats.totalTokens') }}</div>
            <div class="stat-overview-value">{{ formatStatNumber(aggregate.total_tokens) }}</div>
            <div class="stat-overview-sub">{{ t('requests.list.stats.scopeAll') }}</div>
          </div>
          <div
            v-if="!isDefaultTenant()"
            class="stat-overview-card"
            data-stat="total-credits"
            :title="t('requests.list.stats.totalCreditsTitle')"
          >
            <div class="stat-overview-label">{{ t('requests.list.stats.totalCredits') }}</div>
            <div class="stat-overview-value">{{ formatStatNumber(aggregate.credits_charged) }}</div>
            <div class="stat-overview-sub">{{ t('requests.list.stats.scopeAll') }}</div>
          </div>
        </div>
        <div class="stats-grid stats-grid--compact" style="margin-top:10px;display:grid;grid-template-columns:repeat(auto-fit,minmax(120px,1fr));gap:8px">
          <div class="stat-card stat-card--compact">
            <div style="color:var(--text-secondary);font-size:11px">{{ t('requests.list.filter.inputTokenLabel') }}</div>
            <div style="font-size:16px;font-weight:600;margin-top:2px">{{ formatStatNumber(aggregate.prompt_tokens) }}</div>
          </div>
          <div class="stat-card stat-card--compact">
            <div style="color:var(--text-secondary);font-size:11px">{{ t('requests.list.filter.outputTokenLabel') }}</div>
            <div style="font-size:16px;font-weight:600;margin-top:2px">{{ formatStatNumber(aggregate.completion_tokens) }}</div>
          </div>
          <div class="stat-card stat-card--compact">
            <div style="color:var(--text-secondary);font-size:11px">{{ t('requests.list.filter.cacheReadLabel') }}</div>
            <div style="font-size:16px;font-weight:600;margin-top:2px">{{ formatStatNumber(aggregate.cache_read_tokens) }}</div>
          </div>
          <div class="stat-card stat-card--compact">
            <div style="color:var(--text-secondary);font-size:11px">{{ t('requests.list.filter.cacheWriteLabel') }}</div>
            <div style="font-size:16px;font-weight:600;margin-top:2px">{{ formatStatNumber(aggregate.cache_write_tokens) }}</div>
          </div>
          <div class="stat-card stat-card--compact">
            <div style="color:var(--text-secondary);font-size:11px">{{ t('requests.list.filter.costLabel') }}</div>
            <div style="font-size:16px;font-weight:600;margin-top:2px">{{ formatStatCost(aggregate.cost_usd) }}</div>
          </div>
        </div>
      </div>

      <!-- 按模型详情 -->
      <div v-if="showByModelStats && !isSingleModelFiltered" style="padding:12px;border-top:1px solid var(--border)">
        <div v-if="!aggregate.by_model || !aggregate.by_model.length" style="color:var(--text-secondary);font-size:12px;text-align:center;padding:8px">
          当前条件无按模型统计数据
        </div>
        <div v-else style="display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:10px">
          <div
            v-for="m in aggregate.by_model"
            :key="m.model"
            class="model-stat-card"
            style="border:1px solid var(--border);border-radius:6px;padding:10px;background:var(--surface-primary)"
          >
            <div style="font-weight:600;margin-bottom:6px;color:var(--accent);font-size:13px" :title="m.model">
              {{ m.model.length > 24 ? m.model.slice(0, 24) + '…' : m.model }}
            </div>
            <div style="display:grid;grid-template-columns:repeat(2,1fr);gap:6px;font-size:11px">
              <div>
                <div style="color:var(--text-secondary)">请求次数</div>
                <div style="font-weight:600;margin-top:2px">{{ m.requests.toLocaleString() }}</div>
              </div>
              <div>
                <div style="color:var(--text-secondary)">总Token</div>
                <div style="font-weight:600;margin-top:2px">{{ formatStatNumber(m.total_tokens) }}</div>
              </div>
              <div>
                <div style="color:var(--text-secondary)">输入Token</div>
                <div style="font-weight:600;margin-top:2px">{{ formatStatNumber(m.prompt_tokens) }}</div>
              </div>
              <div>
                <div style="color:var(--text-secondary)">输出Token</div>
                <div style="font-weight:600;margin-top:2px">{{ formatStatNumber(m.completion_tokens) }}</div>
              </div>
            </div>
            <div v-if="m.cost_usd > 0" style="margin-top:6px;padding-top:6px;border-top:1px solid var(--border);font-size:11px">
              <div style="color:var(--text-secondary)">成本 (USD)</div>
              <div style="font-weight:600;margin-top:2px">${{ m.cost_usd.toFixed(4) }}</div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- v3 压缩说明卡片 (2026-06-20) -->
    <div class="compression-guide-card" style="margin-bottom:12px;border:1px solid var(--border);border-radius:8px;overflow:hidden;font-size:12px">
      <div
        style="display:flex;justify-content:space-between;align-items:center;padding:8px 12px;cursor:pointer;background:var(--surface-secondary)"
        @click="showCompressionGuide = !showCompressionGuide"
      >
        <span style="font-weight:600;display:flex;align-items:center;gap:6px">
          <span>🤖 压缩与会话缓存说明</span>
          <span v-if="compressionStats.totalCompressed > 0" class="badge" style="font-size:10px;padding:2px 6px">
            本页 {{ compressionStats.totalCompressed }} 条已压缩
            <template v-if="compressionStats.deltaCount"> · 增量拼接 {{ compressionStats.deltaCount }}</template>
            <template v-if="compressionStats.slidingCount"> · 滑动窗口 {{ compressionStats.slidingCount }}</template>
          </span>
        </span>
        <span style="color:var(--text-secondary);font-size:11px">{{ showCompressionGuide ? t('requests.collapse') : t('requests.expand') }}</span>
      </div>
      <div v-if="showCompressionGuide" style="padding:8px 12px 12px;border-top:1px solid var(--border);line-height:1.7">
        <p style="margin:0 0 6px"><strong>会话缓存机制</strong>：网关按 <code>X-Gw-Session-Id</code> 维度缓存会话历史。
        同一会话的多轮请求不再重复发送完整历史，只发送新增部分。缓存分三级：
        L1 进程内存 / L2 Redis / L3 数据库兜底。</p>
        <p style="margin:0 0 6px"><strong>压缩策略说明</strong>：</p>
        <table style="width:100%;border-collapse:collapse;font-size:11px">
          <tr>
            <th style="text-align:left;padding:3px 6px;border:1px solid var(--border);background:var(--surface-primary);white-space:nowrap">策略</th>
            <th style="text-align:left;padding:3px 6px;border:1px solid var(--border);background:var(--surface-primary)">触发条件</th>
            <th style="text-align:left;padding:3px 6px;border:1px solid var(--border);background:var(--surface-primary)">效果</th>
          </tr>
          <tr>
            <td style="padding:3px 6px;border:1px solid var(--border);white-space:nowrap;color:var(--success)">增量拼接 (delta_append)</td>
            <td style="padding:3px 6px;border:1px solid var(--border)">同会话有新增消息</td>
            <td style="padding:3px 6px;border:1px solid var(--border)">只转发新增的消息（已压缩历史保留在缓存中）</td>
          </tr>
          <tr>
            <td style="padding:3px 6px;border:1px solid var(--border);white-space:nowrap;color:var(--warning)">滑动窗口 (sliding_window)</td>
            <td style="padding:3px 6px;border:1px solid var(--border)">消息数 ≥ 50 / Token超阈值 / 空闲 ≥ 5 分钟</td>
            <td style="padding:3px 6px;border:1px solid var(--border)">触发 LLM 无损摘要（保留关键事实、路径、ID、错误等）→ 摘要失败时降级为机械裁剪</td>
          </tr>
          <tr>
            <td style="padding:3px 6px;border:1px solid var(--border);white-space:nowrap;color:#b45309">机械裁剪 (mechanical_trim)</td>
            <td style="padding:3px 6px;border:1px solid var(--border)">上游 4xx context_length / 滑动窗口摘要失败</td>
            <td style="padding:3px 6px;border:1px solid var(--border)">从最早消息开始逐对裁剪，保留 system + 首条 user + 最近 N 对</td>
          </tr>
          <tr>
            <td style="padding:3px 6px;border:1px solid var(--border);white-space:nowrap;color:#6d28d9">Memora 注入</td>
            <td style="padding:3px 6px;border:1px solid var(--border)">上下文超限时检索 Memora L1 事实</td>
            <td style="padding:3px 6px;border:1px solid var(--border)">将历史事实作为"动态上下文"注入请求</td>
          </tr>
        </table>
      </div>
    </div>

    <!-- 2026-08-10: 筛选条件区可折叠卡片。
         头部栏常驻（折叠开关 + 生效条件数 + 总数 + 查询 + 清空），
         展开后：时间范围独立成行（预设 chips + custom 日期选择器），
         其余条件按自然宽度 flex-wrap 流式排布，放满自动折行。 -->
    <div class="filter-section" style="margin-bottom:16px;border:1px solid var(--border);border-radius:8px;overflow:hidden;background:var(--surface-primary)">
      <div
        class="filter-bar"
        style="display:flex;align-items:center;gap:8px;padding:8px 12px;cursor:pointer;background:var(--surface-secondary)"
        @click="showFilters = !showFilters"
      >
        <span style="font-weight:600;display:flex;align-items:center;gap:6px;white-space:nowrap">
          <span>⚙️ 筛选条件</span>
          <span v-if="activeFilterCount > 0" class="badge badge-blue" style="font-size:10px;padding:2px 6px;white-space:nowrap">{{ activeFilterCount }} 项生效</span>
        </span>
        <span style="flex:1"></span>
        <span style="color:var(--text-secondary);font-size:12px;white-space:nowrap">共 {{ total }} 条</span>
        <button class="btn btn-primary btn-sm" style="cursor:pointer;white-space:nowrap" @click.stop="resetPageAndLoad">🔍 查询</button>
        <button class="btn btn-sm" style="cursor:pointer;white-space:nowrap" title="清空全部条件" @click.stop="clearAllFilters">⟲ 清空条件</button>
        <span style="color:var(--text-secondary);font-size:11px;white-space:nowrap">{{ showFilters ? '收拢 ▲' : '展开 ▼' }}</span>
      </div>

      <div v-show="showFilters" style="padding:12px;border-top:1px solid var(--border)">
        <!-- 时间范围独立行：预设 chips + custom 日期选择器 -->
        <div style="display:flex;gap:6px;flex-wrap:wrap;align-items:center;margin-bottom:10px">
          <span style="font-size:11px;color:var(--text-secondary);white-space:nowrap">时间范围</span>
          <template v-for="opt in timePresetOptions" :key="opt.value">
            <span v-if="opt.separator" aria-hidden="true" style="width:1px;height:22px;background:var(--border);margin:0 4px"></span>
            <button
              v-else
              class="preset-chip"
              :class="{ 'preset-chip--active': timePreset === opt.value, 'preset-chip--disabled': opt.disabled }"
              :disabled="opt.disabled"
              :title="opt.disabled ? '非 default 租户最多查看最近 3 天' : ''"
              @click="selectPreset(opt.value)"
            >
              {{ opt.label }}
            </button>
          </template>
          <el-date-picker
            v-if="timePreset === 'custom'"
            v-model="customDateRange"
            type="datetimerange"
            :placeholder="t('requests.list.dateRangePlaceholder')"
            range-separator="→"
            format="YYYY-MM-DD HH:mm"
            value-format="YYYY-MM-DDTHH:mm:ssZ"
            :clearable="false"
            style="height: 32px; width: 360px"
            @change="onCustomRangeChange"
          />
          <button v-if="timePreset !== 'h24'" class="btn btn-sm" style="cursor:pointer;white-space:nowrap" title="重置为24小时" @click="resetTimeFilter">
            ⟲ 重置
          </button>
        </div>

        <!-- 条件流式行：各控件按自然宽度排布，放满自动折行 -->
        <div style="display:flex;gap:8px;flex-wrap:wrap;align-items:center">
          <select v-model="apiKeyId" class="filter-select" style="width:180px" title="按API密钥筛选">
            <option value="">{{ t('requests.list.filter.keyAll') }}</option>
            <option v-for="k in keys" :key="k.id" :value="k.id">{{ k.key_prefix }} ({{ k.application_code }})</option>
          </select>
          <select v-model="providerFilter" class="filter-select" style="width:130px" title="按供应商筛选" @change="onProviderFilterChange">
            <option value="">{{ t('requests.list.filter.providerAll') }}</option>
            <option v-for="p in providerOptions" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
          <select v-model="credentialFilter" class="filter-select" style="width:130px" title="按凭据筛选">
            <option value="">{{ t('requests.list.filter.credentialAll') }}</option>
            <option v-for="c in filteredCredentialOptions" :key="c.id" :value="c.id">{{ c.label }}</option>
          </select>
          <select v-model="successFilter" class="filter-select" style="width:100px" title="按状态筛选">
            <option value="">{{ t('requests.list.filter.resultAll') }}</option>
            <option value="in_progress">请求中</option>
            <option value="success">成功</option>
            <option value="failure">失败</option>
            <option value="rate_limited">限流</option>
          </select>
          <select v-model="errorKindFilter" class="filter-select" style="width:120px" title="按错误类型筛选">
            <option value="">{{ t('requests.list.filter.errorAll') }}</option>
            <option value="model_not_found">模型未找到</option>
            <option value="provider_error">供应商错误</option>
            <option value="timeout">超时</option>
            <option value="rate_limit">供应商限流</option>
            <option value="rate_limit_exceeded">网关RPM限流</option>
            <option value="key_throttled">密钥节流</option>
          </select>
          <select v-model="usageSourceFilter" class="filter-select" style="width:110px" :title="t('requests.list.filter.tokenSourceTitle')">
            <option value="">{{ t('requests.list.filter.tokenSourceAll') }}</option>
            <option value="llm">{{ t('requests.list.filter.tokenSourceLlm') }}</option>
            <option value="estimated">{{ t('requests.list.filter.tokenSourceEstimated') }}</option>
          </select>
          <div style="width:240px">
            <ModelPicker
              v-model="modelFilter"
              placeholder="选择模型…"
              title="筛选请求日志模型"
              @update:model-value="onModelFilterChange"
            />
          </div>
          <input
            v-model="keyword"
            type="text"
            class="filter-input"
            style="width:220px"
            placeholder="搜索请求消息内容…"
            title="消息片段"
            @keyup.enter="resetPageAndLoad"
          />
          <input
            v-model="gwSessionFilter"
            type="text"
            class="filter-input"
            style="width:190px"
            placeholder="X-Gw-Session-Id…"
            title="会话 ID"
            @keyup.enter="resetPageAndLoad"
          />
          <input
            v-model="gwTaskFilter"
            type="text"
            class="filter-input"
            style="width:190px"
            placeholder="X-Gw-Task-Id…"
            title="任务 ID"
            @keyup.enter="resetPageAndLoad"
          />
        </div>
      </div>
    </div>

    <p v-if="error" style="color:var(--danger);margin-bottom:12px">{{ error }}</p>

    <div
      v-if="traceMode && taskSummary"
      class="card trace-summary"
      style="margin-bottom:12px;padding:10px 14px;font-size:12px;display:flex;gap:16px;align-items:center;flex-wrap:wrap"
    >
      <span style="font-weight:600">任务脉络</span>
      <span>共 {{ total }} 步（本页 {{ taskSummary.total }}）</span>
      <span style="color:var(--success)">成功 {{ taskSummary.ok }}</span>
      <span style="color:var(--danger)">失败 {{ taskSummary.fail }}</span>
      <span v-if="taskSummary.pending" style="color:var(--warning)">进行中 {{ taskSummary.pending }}</span>
      <span v-if="gwTaskFilter" style="color:var(--muted)">任务: {{ gwTaskFilter }}</span>
      <span v-if="gwSessionFilter" style="color:var(--muted)">会话: {{ shortHash(gwSessionFilter) }}</span>
      <button class="btn btn-ghost btn-sm" style="margin-left:auto" @click="clearTraceFilter">清除脉络筛选</button>
    </div>

    <div v-if="canSummarizeSession" class="card session-summary-entry" style="margin-bottom:12px;padding:12px">
      <div style="display:flex;gap:10px;align-items:center;flex-wrap:wrap">
        <strong>会话总结</strong>
        <span style="color:var(--muted);font-size:12px">{{ t('requests.list.trace.sessionSummaryHint') }}</span>
        <span style="color:var(--muted);font-size:12px">会话: {{ shortHash(gwSessionFilter) }}</span>
        <button class="btn btn-primary btn-sm" style="margin-left:auto" @click="openSessionSummaryDrawer">
          打开会话总结
        </button>
      </div>
    </div>

    <div v-if="!loading && total > 0" class="pagination-bar">
      <div class="pagination-info">
        <span>共 {{ total }} 条</span>
        <span v-if="total > 0">· 第 {{ page }} / {{ Math.max(1, Math.ceil(total / pageSize)) }} 页</span>
        <span class="pagination-divider">·</span>
        <span class="page-size-label">每页</span>
        <select v-model.number="pageSize" @change="resetPageAndLoad" class="page-size-select">
          <option :value="50">50</option>
          <option :value="100">100</option>
          <option :value="200">200</option>
          <option :value="500">500</option>
        </select>
      </div>
      <div class="pagination-controls">
        <button class="btn btn-ghost btn-sm" :disabled="page <= 1" @click="changePage(-1)">上一页</button>
        <button class="btn btn-ghost btn-sm" :disabled="page >= Math.ceil(total / pageSize)" @click="changePage(1)">下一页</button>
      </div>
    </div>

    <div class="card" style="overflow-x:auto">
      <table class="data-table request-log-table" style="width:100%;font-size:12px">
        <thead>
          <tr>
            <th v-if="traceMode" class="col-seq">#</th>
            <th class="col-time">时间</th>
            <th class="col-trace">脉络</th>
            <th class="col-title">会话标题</th>
            <th class="col-caller">调用方</th>
            <th class="col-route">路由</th>
            <th class="col-tokens">Token</th>
            <th v-if="!isDefaultTenant()" class="col-credits">积分</th>
            <th class="col-lat">延迟</th>
            <th class="col-compress">压缩</th>
            <th class="col-status">状态</th>
            <th class="col-attach">附件</th>
            <th v-if="isSuperAdmin()" class="col-trace-action">流程</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading"><td :colspan="listColCount">加载中…</td></tr>
          <tr v-else-if="!rows.length"><td :colspan="listColCount">无记录</td></tr>
          <tr
            v-for="r in rows"
            :key="r.request_id + r.ts"
            class="request-log-row"
            :class="{ 'row-failure': r.request_status === 'failure' || (!r.success && r.request_status !== 'in_progress' && r.request_status !== 'rate_limited'), 'row-rate-limited': r.request_status === 'rate_limited' }"
            @click="showDetail(r.request_id)"
          >
            <td v-if="traceMode" class="col-seq">
              <span class="cell-line1">{{ r.trace_seq ?? t('requests.none') }}</span>
            </td>
            <td class="col-time" :title="`${r.request_id} · ${fmtTs(r.ts)}`">
              <div class="cell-line1">{{ fmtDate(r.ts) }}</div>
              <div class="cell-line2">{{ fmtTime(r.ts) }}</div>
            </td>
            <td class="col-trace" @click.stop="filterByTrace(r)">
              <div
                v-if="r.gw_session_id"
                class="trace-link trace-full"
                :title="traceSessionTitle(r.gw_session_id)"
              >会话 {{ ellipsize(r.gw_session_id, 36) }}</div>
              <div
                v-if="r.gw_task_id"
                class="trace-sub trace-full"
                :title="traceTaskTitle(r.gw_task_id)"
                @click.stop="filterByTask(r.gw_task_id)"
              >任务 {{ ellipsize(r.gw_task_id, 36) }}</div>
              <span v-if="!r.gw_task_id && !r.gw_session_id" class="cell-line2" style="color:var(--muted)">—</span>
            </td>
            <td class="col-title" :title="r.session_title || '尚无会话标题 — 点击详情查看'">
              <div v-if="r.session_title" class="cell-line1 cell-clip title-text">{{ r.session_title }}</div>
              <div v-else class="cell-line1 muted">—</div>
            </td>
            <td class="col-caller">
              <div class="cell-line1 cell-clip" :title="callerUserTitle(r)">{{ ellipsize(callerUserLine(r), 18) }}</div>
              <div class="cell-line2 cell-clip" :title="callerKeyTitle(r)">{{ ellipsize(callerKeyLine(r), 22) }}</div>
            </td>
            <td class="col-route">
              <div class="cell-line1 cell-clip" :title="routeProviderLine(r)">{{ ellipsize(routeProviderLine(r), 24) }}</div>
              <div class="cell-line2 cell-clip" :title="routeModelTitle(r)">{{ ellipsize(routeModelLine(r), 32) }}</div>
            </td>
            <td class="col-tokens" :title="tokenTitle(r.usage_source)">
              <div class="cell-line1">
                读 {{ token(r.prompt_tokens, r.usage_source) }} / 写 {{ token(r.completion_tokens, r.usage_source) }}
              </div>
              <div class="cell-line2">
                缓读 {{ token(r.cache_read_tokens, r.usage_source) }} / 缓写 {{ token(r.cache_write_tokens, r.usage_source) }}
              </div>
            </td>
            <td v-if="!isDefaultTenant()" class="col-credits" title="本次请求扣除的积分">
              <div class="cell-line1">{{ creditsDisplay(r.credits_charged) }}</div>
            </td>
            <td class="col-lat">
              <div class="cell-line1">{{ r.latency_ms != null ? r.latency_ms + 'ms' : t('requests.none') }}</div>
              <div v-if="r.request_mode" class="cell-line2">{{ r.request_mode }}</div>
            </td>
            <td class="col-compress">
              <template v-if="compressionLabel(r)">
                <span
                  class="compression-badge"
                  :class="['strategy-' + (r.compression_strategy || 'noop')]"
                  :title="compressionLabel(r)!.tip"
                >
                  <span class="badge-reason">{{ compressionLabel(r)!.reason }}</span>
                  <span class="badge-sep">·</span>
                  <span class="badge-strategy">{{ compressionLabel(r)!.strategy }}</span>
                </span>
                <div v-if="calcSavingDetail(r).hasSaving" class="cell-line2 saving-text">
                  {{ calcSavingDetail(r).savingStr }}
                  <span v-if="calcSavingDetail(r).tokenSavingStr" class="saving-token"> · {{ calcSavingDetail(r).tokenSavingStr }}</span>
                </div>
                <div
                  v-if="r.parent_request_id"
                  class="cell-line2 parent-id parent-id-clickable"
                  :title="'跳转到父请求 ' + r.parent_request_id"
                  @click="jumpToParent(r)"
                >
                  ← {{ r.parent_request_id.slice(0, 8) }}
                </div>
              </template>
              <template v-else>
                <span class="cell-line1 muted">—</span>
              </template>
            </td>
            <td class="col-status" :style="{ color: statusColor(r) }" :title="statusTitle(r)">
              <div class="cell-line1">{{ statusLabel(r) }}</div>
              <div v-if="r.error_kind && r.request_status === 'failure'" class="cell-line2">{{ r.error_kind }}</div>
            </td>
            <td class="col-attach" :title="r.attachment_count ? `${r.attachment_count} ${t('requests.list.table.attachmentsTitle')}` : t('requests.list.table.noAttachments')">
              <span
                v-if="r.attachment_count && r.attachment_count > 0"
                class="attach-badge"
              >📎 {{ r.attachment_count }}</span>
              <span v-else class="cell-line1 muted">—</span>
            </td>
            <td v-if="isSuperAdmin()" class="col-trace-action">
              <button
                class="btn btn-sm btn-ghost trace-action-btn"
                :title="`查看 ${r.request_id} 的请求链路`"
                @click.stop="gotoTrace(r.request_id)"
              >🔍 流程详情</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="!loading && total > 0" class="pagination-bar">
      <div class="pagination-info">
        <span>共 {{ total }} 条</span>
        <span>· 第 {{ page }} / {{ Math.max(1, Math.ceil(total / pageSize)) }} 页</span>
        <span class="pagination-divider">·</span>
        <span class="page-size-label">每页</span>
        <select v-model.number="pageSize" @change="resetPageAndLoad" class="page-size-select">
          <option :value="50">50</option>
          <option :value="100">100</option>
          <option :value="200">200</option>
          <option :value="500">500</option>
        </select>
      </div>
      <div class="pagination-controls">
        <button class="btn btn-ghost btn-sm" :disabled="page <= 1" @click="changePage(-1)">上一页</button>
        <button class="btn btn-ghost btn-sm" :disabled="page >= Math.ceil(total / pageSize)" @click="changePage(1)">下一页</button>
      </div>
    </div>

    <RequestLogDrawer
      :request-id="activeRequestId"
      mode="request-logs"
      :initial-trace-open="openDetailWithTrace"
      @close="closeDetail"
      @session-title-changed="syncSessionTitle"
      @filter-session="onDrawerFilterSession"
      @open-request="onDrawerOpenRequest"
    />

    <SessionSummaryDrawer
      :open="summaryDrawerOpen"
      :session-id="gwSessionFilter.trim() || null"
      :session-title="summaryDrawerTitle"
      @close="summaryDrawerOpen = false"
      @filter-session="onDrawerFilterSession"
      @open-request="onDrawerOpenRequest"
    />
  </div>
</template>

<style scoped>
/* 2026-08-17: 详情 body 缓存可观测条（super admin）。轻量单行 chip，不抢
   列表空间；数值用 tabular-nums 防刷新时跳动。 */
.body-cache-strip {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 14px;
  margin-bottom: 12px;
  padding: 5px 12px;
  background: color-mix(in srgb, var(--accent) 4%, transparent);
  border: 1px solid var(--border);
  border-radius: 6px;
  font-size: 11px;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}
.body-cache-strip__label {
  font-weight: 600;
  white-space: nowrap;
}
.body-cache-strip strong {
  color: var(--text-primary, inherit);
}
.request-log-row {
  cursor: pointer;
}
.request-log-row:hover td {
  background: color-mix(in srgb, var(--accent) 8%, transparent);
}
.request-log-row.row-failure td {
  background: color-mix(in srgb, var(--danger) 4%, transparent);
}
.request-log-table th,
.request-log-table td {
  padding: 5px 7px;
  vertical-align: top;
}
.col-seq {
  width: 2.2rem;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.col-time {
  width: 4.8rem;
  white-space: nowrap;
}
.col-trace {
  min-width: 9rem;
  max-width: 14rem;
  cursor: pointer;
}
/* 2026-08-06: 会话标题列（session_titles.title joined）。 */
/* 设计思路：紧凑显示，hover 显示完整 title；为空时显示 "—" */
/* 不放按钮入口 — 编辑入口在请求详情抽屉里（点行打开），避免列表噪声。 */
.col-title {
  min-width: 9rem;
  max-width: 16rem;
}
.title-text {
  color: var(--kx-text-primary);
  font-weight: 500;
}

.col-caller {
  min-width: 7rem;
  max-width: 11rem;
}
.col-route {
  min-width: 9rem;
  max-width: 16rem;
}
.col-tokens {
  min-width: 8.5rem;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}
.col-credits {
  min-width: 4.5rem;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}
.col-lat {
  width: 4.5rem;
  white-space: nowrap;
}
.col-status {
  min-width: 4.5rem;
  max-width: 7rem;
}
.cell-line1 {
  font-size: 12px;
  line-height: 1.35;
}
.cell-line2 {
  color: var(--muted);
  font-size: 10px;
  line-height: 1.35;
  margin-top: 2px;
}
.cell-clip {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 100%;
}
.trace-link {
  color: var(--accent);
  cursor: pointer;
  font-size: 11px;
}
.trace-link:hover {
  text-decoration: underline;
}
.trace-sub {
  color: var(--muted);
  margin-top: 2px;
  font-size: 10px;
  cursor: pointer;
}
.trace-sub:hover {
  color: var(--accent);
  text-decoration: underline;
}
.trace-full {
  white-space: normal;
  word-break: break-all;
  overflow-wrap: anywhere;
}
.trace-summary {
  border-left: 3px solid var(--accent);
}
.col-trace-action {
  white-space: nowrap;
  text-align: center;
}
.trace-action-btn {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 4px;
  border: 1px solid var(--border);
  background: color-mix(in srgb, var(--accent) 8%, transparent);
  color: var(--accent);
  cursor: pointer;
}
.trace-action-btn:hover {
  background: color-mix(in srgb, var(--accent) 18%, transparent);
  border-color: var(--accent);
}

.tenant-badge {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  background: var(--surface-secondary);
  color: var(--text-secondary);
}
.tenant-badge--admin {
  background: var(--info-bg);
  color: var(--accent);
}
.tenant-badge--default {
  background: var(--success-bg);
  color: var(--success);
}

/* Round 47 compression v7: parent-child chain badge. */
.compression-badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 3px 8px;
  border-radius: 10px;
  font-size: 11px;
  font-weight: 500;
  white-space: nowrap;
  background: var(--surface-secondary);
  color: var(--text-primary);
}
.compression-badge .badge-sep {
  color: var(--text-secondary);
}
.compression-badge.strategy-mechanical_trim {
  background: var(--warning-bg);
  color: var(--warning-dark);
}
.compression-badge.strategy-memora_l1_inject {
  background: color-mix(in srgb, var(--accent) 10%, transparent);
  color: #6d28d9;
}
.compression-badge.strategy-llm_summary {
  background: var(--info-bg);
  color: var(--accent);
}
.compression-badge.strategy-noop {
  background: rgba(107, 114, 128, 0.1);
  color: var(--muted);
}
/* v3 (2026-06-19) session-level compression strategies.
   Different color palette from v7 to make them visually distinguishable
   in the logs table. */
.compression-badge.strategy-delta_append {
  background: rgba(20, 184, 166, 0.12);
  color: #0f766e;
  border: 1px solid rgba(20, 184, 166, 0.3);
}
.compression-badge.strategy-sliding_window_token,
.compression-badge.strategy-sliding_window_count,
.compression-badge.strategy-sliding_window_idle {
  background: color-mix(in srgb, var(--magenta) 12%, transparent);
  color: #7e22ce;
  border: 1px solid color-mix(in srgb, var(--magenta) 30%, transparent);
}
.col-compress {
  max-width: 180px;
  min-width: 120px;
}
/* 2026-07-01 (migration 325): 附件列 + 列表角标 */
.col-attach {
  min-width: 3rem;
  text-align: center;
}
.attach-badge {
  display: inline-block;
  padding: 1px 6px;
  border-radius: 8px;
  font-size: 10px;
  font-weight: 600;
  background: color-mix(in srgb, var(--magenta) 12%, transparent);
  color: var(--purple);
}
.parent-id {
  color: var(--text-secondary);
  font-size: 10px;
  margin-top: 2px;
  font-family: var(--mono-font, ui-monospace, monospace);
}
.parent-id-clickable {
  cursor: pointer;
  color: var(--accent);
}
.parent-id-clickable:hover {
  text-decoration: underline;
}
.cell-line1.muted {
  color: var(--text-secondary);
}
.pagination-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 12px;
  padding: 8px 12px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  flex-wrap: nowrap;
}
.pagination-info {
  display: flex;
  align-items: center;
  gap: 10px;
  color: var(--muted);
  font-size: 12px;
  flex-wrap: nowrap;
  white-space: nowrap;
  flex-shrink: 0;
  min-width: 0;
}
.pagination-controls {
  display: flex;
  gap: 8px;
  flex-wrap: nowrap;
  flex-shrink: 0;
}
.page-size-select {
  width: auto;
  min-width: 0;
  max-width: 96px;
  padding: 2px 6px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  font-size: 12px;
}
.page-size-label {
  color: var(--muted);
  font-size: 12px;
}
.pagination-divider {
  color: var(--muted);
  opacity: 0.6;
}
@media (max-width: 720px) {
  .pagination-bar {
    flex-wrap: wrap;
  }
  .pagination-info,
  .pagination-controls {
    width: 100%;
    justify-content: space-between;
  }
}

/* v3 compression savings text in the table compression column */
.cell-line2.saving-text {
  font-size: 10px;
  color: var(--success);
  margin-top: 2px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.cell-line2.saving-text .saving-token {
  color: var(--warning);
}

/* Compression guide card in-page styling */
.compression-guide-card code {
  font-size: 10px;
  padding: 1px 4px;
  border-radius: 3px;
  background: var(--surface-primary, var(--bg-card));
}

/* 2026-08-09: 新筛选区样式 */
.filter-section .filter-select {
  height: 32px;
  padding: 4px 8px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-size: 13px;
  cursor: pointer;
}
.filter-section .filter-select:focus {
  outline: none;
  border-color: var(--accent);
  box-shadow: 0 0 0 2px var(--info-bg);
}
.filter-section .filter-input {
  width: 100%;
  height: 32px;
  padding: 4px 8px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-size: 13px;
}
.filter-section .filter-input:focus {
  outline: none;
  border-color: var(--accent);
  box-shadow: 0 0 0 2px var(--info-bg);
}
.filter-section .filter-input::placeholder {
  color: var(--text-secondary);
  opacity: 0.6;
}

/* 2026-08-10: 统计卡片（概览 + 按模型）合并单行两段式头部 */
.stats-card .stats-segment {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  cursor: pointer;
  background: var(--surface-primary);
  transition: background 0.12s ease;
}
.stats-card .stats-segment:hover { background: var(--surface-secondary); }
.stats-card .stats-segment--active {
  background: color-mix(in srgb, var(--accent) 6%, var(--surface-primary));
}

/* 2026-08-10: 时间范围预设 chips */
.preset-chip {
  height: 30px;
  padding: 0 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg);
  color: var(--text);
  font-size: 12px;
  cursor: pointer;
  white-space: nowrap;
  transition: border-color 0.12s ease, color 0.12s ease, background 0.12s ease;
}
.preset-chip:hover { border-color: var(--accent); color: var(--accent-h); }
.preset-chip--active {
  border-color: var(--accent);
  color: var(--accent-h);
  background: var(--info-bg);
}
.preset-chip--disabled { opacity: 0.45; cursor: not-allowed; }
.preset-chip--disabled:hover { border-color: var(--border); color: var(--text); }

/* 2026-08-10: 折叠筛选头部栏按钮去除按住拖拽文本选择 */
.filter-section .filter-bar .btn { user-select: none; }
</style>
