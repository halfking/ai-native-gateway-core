<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRoute } from 'vue-router'
import {
  getRequestLogs,
  getRequestLogDetail,
  getSessionSummary,
  sessionSummaryToMemora,
  getKeys,
  type RequestLogRow,
  type RequestLogDetail,
  type ApiKey,
  type RequestLogsResponse,
  type SessionSummaryResponse,
  type SessionSummaryToMemoraResponse,
} from '../api'
import ModelPicker from '../components/ModelPicker.vue'
import { isSuperAdmin, isDefaultTenant, getCurrentTenantId } from '../store'

const rows = ref<RequestLogRow[]>([])
const keys = ref<ApiKey[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const apiKeyId = ref<number | ''>('')
const keyword = ref('')
const modelFilter = ref('')
const hours = ref(24)
const successFilter = ref<'' | 'success' | 'failure' | 'in_progress'>('')
const errorKindFilter = ref('')
const usageSourceFilter = ref<'' | 'llm' | 'estimated'>('')
const gwSessionFilter = ref('')
const gwTaskFilter = ref('')
const summaryLoading = ref(false)
const summaryError = ref<string | null>(null)
const summaryResult = ref<SessionSummaryResponse | null>(null)
const memoraLoading = ref(false)
const memoraError = ref<string | null>(null)
const memoraResult = ref<SessionSummaryToMemoraResponse['memora'] | null>(null)

const page = ref(1)
const pageSize = ref(50)
const total = ref(0)

const detailVisible = ref(false)
const detailLoading = ref(false)
const detail = ref<RequestLogDetail | null>(null)
const detailTab = ref<'request' | 'outbound' | 'response'>('request')

// Tenant info for display
const tenantLabel = computed(() => {
  const tenantId = getCurrentTenantId()
  const isAdmin = isSuperAdmin()
  const isDefault = isDefaultTenant()
  
  if (isAdmin && isDefault) {
    return '整站数据'
  } else if (isDefault) {
    return '默认租户'
  } else {
    return `租户: ${tenantId}`
  }
})

// Non-default tenants can only view last 3 days (72 hours)
const maxHoursForTenant = computed(() => isDefaultTenant() ? 168 : 72)

// Validate hours when tenant changes
function validateHours() {
  const maxHours = maxHoursForTenant.value
  if (hours.value > maxHours) {
    hours.value = maxHours
  }
}

async function loadKeys() {
  try {
    keys.value = await getKeys()
  } catch {
    keys.value = []
  }
}

function timeRange() {
  const end = new Date()
  const start = new Date(end.getTime() - hours.value * 3600 * 1000)
  return { from: start.toISOString(), to: end.toISOString() }
}

function onModelFilterChange(name: string | string[]) {
  modelFilter.value = typeof name === 'string' ? name.trim() : ''
}

const ERROR_KIND_LABELS: Record<string, string> = {
  model_not_found: '模型未找到',
  provider_error: '供应商错误',
  auth_error: '认证失败',
  missing_key: '无Key',
  invalid_key: 'Key无效',
  auth_unavailable: '鉴权不可用',
  body_read_error: 'Body读取失败',
  body_too_large: 'Body过大',
  json_parse_error: 'JSON无效',
  rate_limit: '供应商限流',
  rate_limit_exceeded: '网关RPM限流',
  key_throttled: '密钥节流',
  budget_exhausted: '预算耗尽',
  insufficient_credits: '积分不足',
  timeout: '超时',
  canceled: '已取消',
  upstream_error: '上游错误',
  stream_error: '流中断',
  no_candidate: '无可用路由',
  session_forbidden: '会话无权',
  executor_unavailable: '执行器不可用',
}

// 2026-06-19 T-NEW-7: labels for actual gateway failure codes (the only
// values that should ever appear in failure_detail_code now that
// upstream_finish_reason has been split out). eof_without_done and
// client_cancel are kept as "successful with caveat" in the status
// column; only the "真" gateway errors get a Chinese label here.
const FAILURE_DETAIL_LABELS: Record<string, string> = {
  gw_rpm_exceeded: '网关RPM限流',
  gw_concurrent_exceeded: '网关并发限流',
  gw_tpm_exceeded: '网关TPM限流',
  gw_key_throttled: '密钥节流',
  gw_budget_exhausted: '预算耗尽',
  gw_no_candidate: '无可用路由',
  gw_session_forbidden: '会话无权',
  eof_without_done: '上游EOF无[DONE]',
  stream_timeout: '流超时',
  client_cancel: '客户端取消',
  client_disconnected: '客户端断连',
  no_deltas: '无内容块',
  invalid_first_chunk: '首块无效',
  invalid_json: 'JSON无效',
  upstream_5xx: '上游5xx',
  upstream_4xx: '上游4xx',
  unexpected_status: '状态异常',
  connection_reset: '连接重置',
  write_failed: '写入失败',
  hangup: '远端挂断',
  body_too_large: 'Body过大',
  eof_mid_tool_call: '工具调用中断',
  first_byte_timeout: '首字节超时',
}

// 2026-06-19 T-NEW-7: labels for the SOLE home of the upstream
// finish_reason (stop, tool_calls, length, end_turn, …). These are NOT
// failures; the UI surfaces them as informational metadata in the
// request detail panel, not as a "失败详情" pill.
const UPSTREAM_FINISH_REASON_LABELS: Record<string, string> = {
  stop: '正常完成',
  tool_calls: '工具调用',
  function_call: '函数调用',
  length: '达到长度上限',
  end_turn: '轮次结束',
  max_tokens: '达到 max_tokens',
}

function upstreamFinishReasonLabel(v: string | null | undefined): string {
  if (!v) return ''
  return UPSTREAM_FINISH_REASON_LABELS[v] ?? v
}

function statusLabel(row: RequestLogRow): string {
  if (row.request_status === 'in_progress') return '请求中'
  if (row.request_status === 'success' || row.success) return '成功'
  // 2026-06-19 T-NEW-7: failure_detail_code now contains ONLY real failure
  // codes. upstream_finish_reason is informational and should never be
  // read as a failure label.
  const detail = row.failure_detail_code || ''
  if (FAILURE_DETAIL_LABELS[detail]) return FAILURE_DETAIL_LABELS[detail]
  const kind = row.error_kind || ''
  if (ERROR_KIND_LABELS[kind]) return ERROR_KIND_LABELS[kind]
  if (detail.startsWith('gw_')) return `网关:${detail.slice(3)}`
  return kind || detail || '失败'
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
  if (row.request_status === 'in_progress') return 'var(--warning, #f59e0b)'
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
    'mode_1_auto_threshold': '自动阈值',
    'mode_2_on_4xx': '4xx 重试',
    'sliding_window_token': '滑动窗口·token',
    'sliding_window_count': '滑动窗口·消息数',
    'sliding_window_idle': '滑动窗口·空闲',
    'sliding_window_mechanical_trim': '滑动窗口·机械',
  }
  const strategyMap: Record<string, string> = {
    'mechanical_trim': '机械裁剪',
    'memora_l1_inject': 'Memora 注入',
    'llm_summary': 'LLM 摘要',
    'noop': '未压缩',
    // v3 (2026-06-19) session-level strategies
    'delta_append': '增量拼接',
    'sliding_window_token': '滑动·token',
    'sliding_window_count': '滑动·消息数',
    'sliding_window_idle': '滑动·空闲',
  }
  // For v3 sliding-window triggered entries, the "reason" label is the
  // window trigger (stored in compression_strategy) and the v7 reason
  // column is empty. Display the trigger as the reason in that case.
  let reason = reasonMap[row.compression_reason || ''] || row.compression_reason || ''
  let strategy = strategyMap[row.compression_strategy || ''] || (row.compression_strategy || '?')
  // Special case: v3 delta_append has compression_reason empty + strategy = 'delta_append'.
  // Treat as '增量拼接' strategy with reason '同会话增量'.
  if (row.compression_strategy === 'delta_append' && !row.compression_reason) {
    reason = '同会话增量'
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

const taskSummary = computed(() => {
  if (!traceMode.value || !rows.value.length) return null
  let ok = 0
  let fail = 0
  let pending = 0
  for (const r of rows.value) {
    if (r.request_status === 'in_progress') pending++
    else if (r.request_status === 'success' || r.success) ok++
    else fail++
  }
  return { total: rows.value.length, ok, fail, pending }
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
  // 脉络视图拉宽时间窗与页大小，避免同脉络记录落在默认 24h/50 条外
  if (hours.value < 168) hours.value = 168
  if (pageSize.value < 200) pageSize.value = 200
  resetPageAndLoad()
}

function filterByTask(taskId: string | null | undefined) {
  if (!taskId) return
  gwTaskFilter.value = taskId
  gwSessionFilter.value = ''
  if (hours.value < 168) hours.value = 168
  if (pageSize.value < 200) pageSize.value = 200
  resetPageAndLoad()
}

function filterBySession(sessionId: string | null | undefined) {
  if (!sessionId) return
  gwSessionFilter.value = sessionId
  gwTaskFilter.value = ''
  if (hours.value < 168) hours.value = 168
  if (pageSize.value < 200) pageSize.value = 200
  resetPageAndLoad()
}

function clearTraceFilter() {
  gwTaskFilter.value = ''
  gwSessionFilter.value = ''
  summaryError.value = null
  summaryResult.value = null
  memoraError.value = null
  memoraResult.value = null
  resetPageAndLoad()
}

const canSummarizeSession = computed(() => gwSessionFilter.value.trim().length > 0)

async function generateSessionSummary() {
  const sid = gwSessionFilter.value.trim()
  if (!sid) return
  summaryLoading.value = true
  summaryError.value = null
  summaryResult.value = null
  try {
    summaryResult.value = await getSessionSummary(sid)
  } catch (e: unknown) {
    summaryError.value = e instanceof Error ? e.message : String(e)
  } finally {
    summaryLoading.value = false
  }
}

function summaryExportContent(data: SessionSummaryResponse): string {
  const lines: string[] = []
  lines.push('# 会话总结')
  lines.push('')
  lines.push(`- Session ID: ${data.meta.session_id}`)
  lines.push(`- 时间范围: ${fmtTs(data.meta.data_from)} ~ ${fmtTs(data.meta.data_to)}`)
  lines.push(`- 日志条数: ${data.meta.log_count}`)
  lines.push(`- 生成时间: ${fmtTs(data.meta.generated_at)}`)
  lines.push('')
  lines.push('## 摘要')
  lines.push(data.summary)
  if (data.key_points && data.key_points.length) {
    lines.push('')
    lines.push('## 关键要点')
    for (const p of data.key_points) lines.push(`- ${p}`)
  }
  return lines.join('\n')
}

function exportSessionSummary(format: 'md' | 'txt' = 'md') {
  if (!summaryResult.value) return
  const content = summaryExportContent(summaryResult.value)
  const blob = new Blob([content], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  const day = new Date().toISOString().slice(0, 10)
  a.href = url
  a.download = `session-summary-${summaryResult.value.meta.session_id}-${day}.${format}`
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}

async function writeSummaryToMemora() {
  const sid = gwSessionFilter.value.trim()
  if (!sid) return
  memoraLoading.value = true
  memoraError.value = null
  memoraResult.value = null
  try {
    const resp = await sessionSummaryToMemora(sid)
    summaryResult.value = { summary: resp.summary, key_points: resp.key_points, meta: resp.meta }
    memoraResult.value = resp.memora
  } catch (e: unknown) {
    memoraError.value = e instanceof Error ? e.message : String(e)
  } finally {
    memoraLoading.value = false
  }
}

function routeProviderLine(r: RequestLogRow): string {
  const parts: string[] = []
  if (r.provider_name) parts.push(r.provider_name)
  else if (r.provider_code) parts.push(r.provider_code)
  if (r.credential_label) parts.push(r.credential_label)
  if (parts.length) return parts.join(' · ')
  if (r.error_kind === 'missing_key' || r.error_kind === 'invalid_key') return '—'
  return '—'
}

function routeModelLine(r: RequestLogRow): string {
  const requestModel = r.canonical_name || r.client_model || '—'
  const providerModel = (r.provider_model || r.outbound_model || '').trim()
  if (!providerModel || providerModel.toLowerCase() === requestModel.toLowerCase()) {
    return requestModel
  }
  return `${requestModel} → ${providerModel}`
}

function routeModelTitle(r: RequestLogRow): string {
  const requestModel = r.canonical_name || r.client_model || '—'
  const providerModel = (r.provider_model || r.outbound_model || '').trim()
  if (!providerModel || providerModel.toLowerCase() === requestModel.toLowerCase()) {
    return `请求模型: ${requestModel}`
  }
  return `请求模型: ${requestModel} → 供应商模型: ${providerModel}`
}

function outboundModelDisplay(r: Pick<RequestLogRow, 'provider_model' | 'outbound_model'> | null | undefined): string {
  if (!r) return '—'
  const v = (r.provider_model || r.outbound_model || '').trim()
  return v || '—'
}

function outboundModelTitle(r: Pick<RequestLogRow, 'provider_model' | 'outbound_model'> | null | undefined): string {
  if (!r) return ''
  const shown = outboundModelDisplay(r)
  const recorded = (r.outbound_model || '').trim()
  if (r.provider_model && recorded && r.provider_model !== recorded) {
    return `出站模型: ${shown}（记录值: ${recorded}）`
  }
  return `出站模型: ${shown}`
}

function ellipsize(value: string | null | undefined, max = 28): string {
  const s = (value ?? '').trim()
  if (!s) return '—'
  if (s.length <= max) return s
  return s.slice(0, Math.max(1, max - 1)) + '…'
}

function callerUserLine(r: RequestLogRow): string {
  if (r.api_key_owner_user) return r.api_key_owner_user
  if (r.end_user_id) return r.end_user_id
  if (r.application_code) return r.application_code
  return '—'
}

function callerUserTitle(r: RequestLogRow): string {
  const parts: string[] = []
  if (r.api_key_owner_user) parts.push(`用户: ${r.api_key_owner_user}`)
  if (r.end_user_id) parts.push(`终端用户: ${r.end_user_id}`)
  if (r.application_code) parts.push(`应用: ${r.application_code}`)
  return parts.join(' · ') || '—'
}

function callerKeyLine(r: RequestLogRow): string {
  const key = r.api_key_prefix ?? (r.api_key_id != null ? `key#${r.api_key_id}` : '无key')
  if (r.application_code && r.api_key_owner_user) return `${key} · ${r.application_code}`
  if (r.application_code) return `${key} · ${r.application_code}`
  return key
}

function callerKeyTitle(r: RequestLogRow): string {
  const parts: string[] = []
  if (r.api_key_prefix) parts.push(`Key: ${r.api_key_prefix}`)
  else if (r.api_key_id != null) parts.push(`Key ID: ${r.api_key_id}`)
  else parts.push('Key: 无')
  if (r.application_code) parts.push(`应用: ${r.application_code}`)
  return parts.join(' · ')
}

function traceSessionTitle(id: string) {
  return `会话 ID（点击筛选同脉络）\n${id}`
}

function traceTaskTitle(id: string) {
  return `任务 ID（点击仅筛此任务）\n${id}`
}

async function load() {
  loading.value = true
  error.value = null
  try {
    const range = timeRange()
    const resp: RequestLogsResponse = await getRequestLogs({
      api_key_id: apiKeyId.value === '' ? undefined : Number(apiKeyId.value),
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
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}

function fmtDate(ts: string) {
  return new Date(ts).toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' })
}

function fmtTime(ts: string) {
  return new Date(ts).toLocaleTimeString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function token(v: number | null | undefined, usageSource?: 'llm' | 'estimated' | null) {
  if (v == null) return '—'
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
  if (usageSource === 'estimated') return '估算值（上游未返回 usage，本地按字符/单词启发式估算）'
  if (usageSource === 'llm') return 'LLM 返回值'
  return ''
}

function costDisplay(v: number | string | null | undefined, currency: string | null | undefined) {
  if (v == null) return currency ? `待定价(${currency})` : '待定价'
  const amount = Number(v).toFixed(6)
  return currency ? `${amount} ${currency}` : amount
}

function creditsDisplay(v: number | null | undefined): string {
  if (v == null || v <= 0) return '—'
  return v.toLocaleString()
}

function shortHash(v: string | null | undefined) {
  return v ? `${v.slice(0, 12)}…` : '—'
}

async function showDetail(requestId: string) {
  detailVisible.value = true
  detailLoading.value = true
  detail.value = null
  detailTab.value = 'request'
  try {
    detail.value = await getRequestLogDetail(requestId)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    detailLoading.value = false
  }
}

function closeDetail() {
  detailVisible.value = false
  detail.value = null
}

function formatJson(obj: any): string {
  if (obj == null) return '(无数据)'
  try {
    return JSON.stringify(obj, null, 2)
  } catch {
    return String(obj)
  }
}

function extractMessagesFromBody(body: any): any[] {
  if (body == null) return []
  if (Array.isArray(body)) return body
  if (typeof body === 'string') {
    try { body = JSON.parse(body) } catch { return [] }
  }
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

// v3 (2026-06-19) session-level outbound body helpers.
// Returns true when the row has an outbound_body that differs from the
// client request_body (i.e. v3 ran for this request).
function hasOutboundBody(row: any): boolean {
  if (!row?.outbound_body) return false
  // Count messages via outbound_msg_count column (cheaper than parsing body).
  if (typeof row.outbound_msg_count === 'number') return row.outbound_msg_count > 0
  const msgs = extractMessagesFromBody(row.outbound_body)
  return msgs.length > 0
}

// Cheap equality check: outbound body stringified bytes match request body bytes.
function outboundEqualsRequest(row: any): boolean {
  if (!row?.outbound_body) return false
  const reqStr = JSON.stringify(row.request_body ?? '')
  const outStr = JSON.stringify(row.outbound_body ?? '')
  return reqStr === outStr
}

// Returns outbound_msg_count - request message count (rough delta indicator).
function outboundMsgDelta(row: any): string {
  const out = typeof row?.outbound_msg_count === 'number' ? row.outbound_msg_count : null
  if (out == null) return ''
  const reqMsgs = extractMessagesFromBody(row?.request_body)
  const reqCount = reqMsgs.length
  const diff = out - reqCount
  if (diff === 0) return '0'
  return diff > 0 ? `+${diff}` : `${diff}`
}

// Returns the smm_v1 summary marker (if present in compression_meta).
function outboundSummaryMarker(row: any): string {
  const meta = row?.compression_meta
  if (!meta || typeof meta !== 'object') return ''
  return typeof meta.summary_marker === 'string' ? meta.summary_marker : ''
}

// True if the given message looks like a gateway-injected compaction summary
// (content starts with the smm_v1 marker prefix).
function isSummaryMarkerMessage(msg: any): boolean {
  const content = msg?.content
  if (typeof content === 'string') return content.startsWith('[smm_v1:')
  if (Array.isArray(content)) {
    for (const p of content) {
      if (typeof p?.text === 'string' && p.text.startsWith('[smm_v1:')) return true
    }
  }
  return false
}

function truncate(s: string, n: number): string {
  if (!s) return ''
  return s.length > n ? s.slice(0, n) + '…' : s
}

function roleColor(role: string): string {
  switch (role) {
    case 'user': return 'var(--info, #3b82f6)'
    case 'assistant': return 'var(--success, #22c55e)'
    case 'system': return 'var(--warning, #f59e0b)'
    case 'tool': return 'var(--muted, #94a3b8)'
    default: return 'inherit'
  }
}

const route = useRoute()

onMounted(async () => {
  const q = route.query
  if (q.success === 'success' || q.success === 'failure' || q.success === 'in_progress') {
    successFilter.value = q.success
  }
  if (typeof q.error_kind === 'string' && q.error_kind.trim()) {
    errorKindFilter.value = q.error_kind.trim()
  }
  if (typeof q.hours === 'string' && /^\d+$/.test(q.hours)) {
    hours.value = Number(q.hours)
  }
  await loadKeys()
  await load()
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
        <button class="btn btn-primary btn-sm" :disabled="loading" @click="load">刷新</button>
      </div>
    </div>

    <div v-if="!isDefaultTenant()" class="tenant-notice" style="margin-bottom:12px;padding:8px 12px;background:rgba(59,130,246,0.1);border:1px solid rgba(59,130,246,0.3);border-radius:6px;font-size:12px;color:#3b82f6">
      非 default 租户只能查看最近 3 天的请求日志
    </div>

    <div class="compact-filter-bar compact-filter-bar--stacked">
      <div class="cf-row">
        <select v-model="apiKeyId" class="cf-select cf-cred" title="API Key">
          <option value="">全部 Key</option>
          <option v-for="k in keys" :key="k.id" :value="k.id">{{ k.key_prefix }} ({{ k.application_code }})</option>
        </select>
        <select v-model="hours" class="cf-select cf-hours" title="时间范围" @change="validateHours">
          <option :value="1">1小时</option>
          <option :value="6">6小时</option>
          <option :value="24">24小时</option>
          <option :value="72">3天</option>
          <option :value="168" :disabled="!isDefaultTenant()">7天</option>
        </select>
        <select v-model="successFilter" class="cf-select cf-status" title="结果">
          <option value="">全部</option>
          <option value="in_progress">请求中</option>
          <option value="success">成功</option>
          <option value="failure">失败</option>
        </select>
        <select v-model="errorKindFilter" class="cf-select cf-error" title="错误类型">
          <option value="">全部错误</option>
          <option value="model_not_found">模型未找到</option>
          <option value="provider_error">供应商错误</option>
          <option value="timeout">超时</option>
          <option value="rate_limit">供应商限流</option>
          <option value="rate_limit_exceeded">网关RPM限流</option>
          <option value="key_throttled">密钥节流</option>
        </select>
        <select
          v-model="usageSourceFilter"
          class="cf-select cf-source"
          title="estimated = 本地估算（上游未返回 usage）"
        >
          <option value="">Token来源</option>
          <option value="llm">LLM返回</option>
          <option value="estimated">本地估算</option>
        </select>
        <span class="cf-meta">共 {{ total }} 条</span>
      </div>
      <div class="cf-row cf-row--secondary">
        <div class="cf-field cf-field--model">
          <span class="cf-label">模型（可选）</span>
          <ModelPicker
            v-model="modelFilter"
            placeholder="选择模型…"
            title="筛选请求日志模型"
            @update:model-value="onModelFilterChange"
          />
        </div>
        <div class="cf-field cf-field--grow">
          <span class="cf-label">消息片段</span>
          <input
            v-model="keyword"
            type="text"
            class="cf-input"
            placeholder="搜索请求消息内容…"
            @keyup.enter="resetPageAndLoad"
          />
        </div>
        <div class="cf-field cf-field--grow">
          <span class="cf-label">会话 ID</span>
          <input
            v-model="gwSessionFilter"
            type="text"
            class="cf-input"
            placeholder="X-Gw-Session-Id…"
            @keyup.enter="resetPageAndLoad"
          />
        </div>
        <div class="cf-field cf-field--grow">
          <span class="cf-label">任务 ID</span>
          <input
            v-model="gwTaskFilter"
            type="text"
            class="cf-input"
            placeholder="X-Gw-Task-Id…"
            @keyup.enter="resetPageAndLoad"
          />
        </div>
        <button class="btn btn-primary btn-sm" @click="resetPageAndLoad">查询</button>
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
      <span v-if="taskSummary.pending" style="color:var(--warning, #f59e0b)">进行中 {{ taskSummary.pending }}</span>
      <span v-if="gwTaskFilter" style="color:var(--muted)">任务: {{ gwTaskFilter }}</span>
      <span v-if="gwSessionFilter" style="color:var(--muted)">会话: {{ shortHash(gwSessionFilter) }}</span>
      <button class="btn btn-ghost btn-sm" style="margin-left:auto" @click="clearTraceFilter">清除脉络筛选</button>
    </div>

    <div v-if="canSummarizeSession" class="card" style="margin-bottom:12px;padding:12px">
      <div style="display:flex;gap:10px;align-items:center;flex-wrap:wrap">
        <strong>会话总结</strong>
        <span style="color:var(--muted);font-size:12px">仅在会话 ID 筛选下可用</span>
        <button class="btn btn-primary btn-sm" :disabled="summaryLoading" @click="generateSessionSummary">
          {{ summaryLoading ? '总结中…' : '生成总结' }}
        </button>
        <button
          v-if="summaryResult"
          class="btn btn-ghost btn-sm"
          @click="exportSessionSummary('md')"
        >导出 Markdown</button>
        <button
          v-if="summaryResult"
          class="btn btn-ghost btn-sm"
          @click="exportSessionSummary('txt')"
        >导出 TXT</button>
        <button
          class="btn btn-ghost btn-sm"
          :disabled="memoraLoading"
          @click="writeSummaryToMemora"
        >{{ memoraLoading ? '写入中…' : '写入 Memora' }}</button>
      </div>
      <p v-if="summaryError" style="margin:8px 0 0;color:var(--danger)">{{ summaryError }}</p>
      <p v-if="memoraError" style="margin:8px 0 0;color:var(--danger)">Memora: {{ memoraError }}</p>
      <p v-if="memoraResult" style="margin:8px 0 0;color:var(--success, #22c55e)">
        已写入 Memora：{{ memoraResult.written }} 条（{{ memoraResult.status }}）
      </p>
      <div v-if="summaryResult" style="margin-top:10px;font-size:12px">
        <div style="color:var(--muted);margin-bottom:6px">
          范围：{{ fmtTs(summaryResult.meta.data_from) }} ~ {{ fmtTs(summaryResult.meta.data_to) }} · {{ summaryResult.meta.log_count }} 条
        </div>
        <div style="white-space:pre-wrap;line-height:1.6">{{ summaryResult.summary }}</div>
        <ul v-if="summaryResult.key_points?.length" style="margin:8px 0 0;padding-left:18px">
          <li v-for="(p, i) in summaryResult.key_points" :key="i">{{ p }}</li>
        </ul>
      </div>
    </div>

    <div v-if="!loading && total > 0" class="pagination-bar">
      <div class="pagination-info">
        <span>共 {{ total }} 条</span>
        <span v-if="total > 0">· 第 {{ page }} / {{ Math.max(1, Math.ceil(total / pageSize)) }} 页</span>
        <select v-model.number="pageSize" @change="resetPageAndLoad" class="page-size-select">
          <option :value="50">50 / 页</option>
          <option :value="100">100 / 页</option>
          <option :value="200">200 / 页</option>
          <option :value="500">500 / 页</option>
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
            <th class="col-caller">调用方</th>
            <th class="col-route">路由</th>
            <th class="col-tokens">Token</th>
            <th v-if="!isDefaultTenant()" class="col-credits">积分</th>
            <th class="col-lat">延迟</th>
            <th class="col-compress">压缩</th>
            <th class="col-status">状态</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading"><td :colspan="traceMode ? (isDefaultTenant() ? 9 : 10) : (isDefaultTenant() ? 8 : 9)">加载中…</td></tr>
          <tr v-else-if="!rows.length"><td :colspan="traceMode ? (isDefaultTenant() ? 9 : 10) : (isDefaultTenant() ? 8 : 9)">无记录</td></tr>
          <tr
            v-for="r in rows"
            :key="r.request_id + r.ts"
            class="request-log-row"
            :class="{ 'row-failure': r.request_status === 'failure' || (!r.success && r.request_status !== 'in_progress') }"
            @click="showDetail(r.request_id)"
          >
            <td v-if="traceMode" class="col-seq">
              <span class="cell-line1">{{ r.trace_seq ?? '—' }}</span>
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
              <div class="cell-line1">{{ r.latency_ms != null ? r.latency_ms + 'ms' : '—' }}</div>
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
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="!loading && total > 0" class="pagination-bar">
      <div class="pagination-info">
        <span>共 {{ total }} 条</span>
        <span>· 第 {{ page }} / {{ Math.max(1, Math.ceil(total / pageSize)) }} 页</span>
        <select v-model.number="pageSize" @change="resetPageAndLoad" class="page-size-select">
          <option :value="50">50 / 页</option>
          <option :value="100">100 / 页</option>
          <option :value="200">200 / 页</option>
          <option :value="500">500 / 页</option>
        </select>
      </div>
      <div class="pagination-controls">
        <button class="btn btn-ghost btn-sm" :disabled="page <= 1" @click="changePage(-1)">上一页</button>
        <button class="btn btn-ghost btn-sm" :disabled="page >= Math.ceil(total / pageSize)" @click="changePage(1)">下一页</button>
      </div>
    </div>

    <!-- Detail Modal -->
    <div v-if="detailVisible" class="drawer-backdrop" @click="closeDetail">
      <div class="drawer-panel card drawer-panel-wide" @click.stop>
        <div class="drawer-header">
          <h3 style="margin:0">请求详情</h3>
          <button class="btn btn-sm" @click="closeDetail">关闭</button>
        </div>

        <div v-if="detailLoading" style="text-align:center;padding:40px">加载中…</div>

        <template v-else-if="detail">
          <div class="drawer-section">
            <div style="display:flex;gap:16px;flex-wrap:wrap;margin-bottom:12px;font-size:12px">
              <span><strong>请求ID:</strong> {{ detail.request_id }}</span>
              <span><strong>会话:</strong> {{ detail.gw_session_id ?? '—' }}</span>
              <span><strong>任务:</strong> {{ detail.gw_task_id ?? '—' }}</span>
              <span><strong>Key:</strong> {{ detail.api_key_prefix ?? (detail.api_key_id != null ? '#' + detail.api_key_id : '无key') }}</span>
              <span v-if="detail.api_key_owner_user"><strong>Key用户:</strong> {{ detail.api_key_owner_user }}</span>
              <span v-if="detail.application_code"><strong>应用:</strong> {{ detail.application_code }}</span>
              <span><strong>时间:</strong> {{ fmtTs(detail.ts) }}</span>
              <span><strong>客户端模型:</strong> {{ detail.client_model ?? '—' }}</span>
              <span :title="outboundModelTitle(detail)"><strong>出站模型:</strong> {{ outboundModelDisplay(detail) }}</span>
              <span><strong>供应商:</strong> {{ detail.provider_name ?? '—' }}</span>
              <span><strong>状态:</strong> <span :style="{ color: detail.success ? 'var(--success)' : 'var(--danger)' }">{{ detail.success ? '成功' : statusLabel(detail) }}</span></span>
              <span v-if="detail.failure_stage"><strong>失败阶段:</strong> {{ detail.failure_stage }}</span>
              <span v-if="detail.failure_detail_code">
                <strong>失败详情:</strong>
                {{ FAILURE_DETAIL_LABELS[detail.failure_detail_code] ?? detail.failure_detail_code }}
              </span>
              <!-- 2026-06-19 T-NEW-7: surface the upstream finish_reason
                   separately from failure_detail_code so a successful
                   `tool_calls` response stops looking like a failure. -->
              <span v-if="detail.upstream_finish_reason" :title="`上游 finish_reason（不等于失败）`">
                <strong>结束原因:</strong>
                {{ upstreamFinishReasonLabel(detail.upstream_finish_reason) }}
              </span>
              <span><strong>延迟:</strong> {{ detail.latency_ms ?? '—' }}ms</span>
              <span><strong>Token:</strong> {{ token(detail.prompt_tokens) }} / {{ token(detail.completion_tokens) }}</span>
              <span v-if="!isDefaultTenant()"><strong>积分消耗:</strong> {{ creditsDisplay(detail.credits_charged) }}</span>
              <!-- v3 (2026-06-19) session-level outbound metadata.
                   Displayed when v3 ran for this request (compression_strategy
                   in {delta_append, sliding_window_*, mechanical_trim}). -->
              <template v-if="hasOutboundBody(detail)">
                <span><strong>转发消息数:</strong> {{ detail.outbound_msg_count ?? '—' }}</span>
                <span><strong>转发 token 估算:</strong> {{ detail.outbound_token_est ?? '—' }}</span>
                <span v-if="outboundSummaryMarker(detail)">
                  <strong>摘要标记:</strong>
                  <span class="summary-marker-badge" :title="outboundSummaryMarker(detail)">{{ truncate(outboundSummaryMarker(detail), 24) }}</span>
                </span>
              </template>
            </div>
          </div>

          <div class="drawer-section">
            <div style="display:flex;gap:8px;margin-bottom:12px">
              <button class="btn btn-sm" :class="{ 'btn-primary': detailTab === 'request' }" @click="detailTab = 'request'">请求消息</button>
              <!-- v3 outbound tab: only shown when the row has an outbound body. -->
              <button v-if="hasOutboundBody(detail)" class="btn btn-sm" :class="{ 'btn-primary': detailTab === 'outbound' }" @click="detailTab = 'outbound'">
                转发消息
                <span class="outbound-diff-badge" :class="{ unchanged: outboundEqualsRequest(detail) }">
                  {{ outboundEqualsRequest(detail) ? '= 请求' : `Δ${outboundMsgDelta(detail)}` }}
                </span>
              </button>
              <button class="btn btn-sm" :class="{ 'btn-primary': detailTab === 'response' }" @click="detailTab = 'response'">响应内容</button>
            </div>
          </div>

          <div class="drawer-section" style="flex:1;overflow:auto;border:1px solid var(--border, #333);border-radius:6px;padding:12px;background:var(--surface-secondary, #1a1a2e);font-size:12px">
            <template v-if="detailTab === 'request'">
              <template v-if="extractMessagesFromBody(detail.request_body).length">
                <div v-for="(msg, i) in extractMessagesFromBody(detail.request_body)" :key="i" style="margin-bottom:12px">
                  <div style="margin-bottom:4px">
                    <span :style="{ color: roleColor(msg.role || ''), fontWeight: 600 }">[{{ msg.role || 'unknown' }}]</span>
                  </div>
                  <pre style="margin:0;white-space:pre-wrap;word-break:break-all;max-height:300px;overflow:auto;font-size:11px;line-height:1.5">{{ formatJson(msg.content ?? msg) }}</pre>
                  <div v-if="msg.tool_calls" style="margin-top:6px">
                    <div style="color:var(--muted);font-size:11px;margin-bottom:4px">工具调用:</div>
                    <pre v-for="(tc, j) in msg.tool_calls" :key="j" style="margin:0 0 4px;white-space:pre-wrap;word-break:break-all;font-size:11px;padding:4px;background:var(--surface-primary, #16213e);border-radius:4px">{{ formatJson(tc) }}</pre>
                  </div>
                </div>
              </template>
              <div v-else style="color:var(--muted)">(无请求数据)</div>
            </template>

            <template v-else-if="detailTab === 'outbound'">
              <!-- v3 outbound body: shows what was actually forwarded to the
                   upstream LLM after delta-append / sliding-window summary. -->
              <div v-if="detail.outbound_body" style="margin-bottom:8px;padding:6px 10px;background:var(--surface-primary, #16213e);border-radius:4px;color:var(--text-secondary);font-size:11px">
                <strong>v3 转发体</strong> · 消息数 {{ detail.outbound_msg_count }} · 估算 {{ detail.outbound_token_est }} tokens
                <span v-if="outboundSummaryMarker(detail)" style="margin-left:8px">
                  <span class="summary-marker-badge">{{ truncate(outboundSummaryMarker(detail), 24) }}</span>
                  <span style="color:var(--muted);font-size:10px">(含 LLM 摘要边界)</span>
                </span>
              </div>
              <template v-if="hasOutboundBody(detail)">
                <div v-for="(msg, i) in extractMessagesFromBody(detail.outbound_body)" :key="i" style="margin-bottom:12px">
                  <div style="margin-bottom:4px">
                    <span :style="{ color: roleColor(msg.role || ''), fontWeight: 600 }">[{{ msg.role || 'unknown' }}]</span>
                    <span v-if="isSummaryMarkerMessage(msg)" style="margin-left:6px;font-size:10px;color:#1d4ed8">(smm_v1 摘要边界)</span>
                  </div>
                  <pre style="margin:0;white-space:pre-wrap;word-break:break-all;max-height:300px;overflow:auto;font-size:11px;line-height:1.5">{{ formatJson(msg.content ?? msg) }}</pre>
                  <div v-if="msg.tool_calls" style="margin-top:6px">
                    <div style="color:var(--muted);font-size:11px;margin-bottom:4px">工具调用:</div>
                    <pre v-for="(tc, j) in msg.tool_calls" :key="j" style="margin:0 0 4px;white-space:pre-wrap;word-break:break-all;font-size:11px;padding:4px;background:var(--surface-primary, #16213e);border-radius:4px">{{ formatJson(tc) }}</pre>
                  </div>
                </div>
              </template>
              <div v-else style="color:var(--muted)">(该请求未触发 v3 会话压缩：转发体 == 客户端请求体)</div>
            </template>

            <template v-else>
              <template v-if="detail.response_body">
                <template v-if="detail.response_body.choices">
                  <div v-for="(choice, i) in detail.response_body.choices" :key="i" style="margin-bottom:12px">
                    <div style="margin-bottom:4px">
                      <span style="font-weight:600">Choice {{ i }}</span>
                      <span v-if="choice.finish_reason" style="color:var(--muted);margin-left:8px">finish: {{ choice.finish_reason }}</span>
                    </div>
                    <div v-if="choice.message" style="margin-bottom:6px">
                      <span :style="{ color: roleColor(choice.message.role || ''), fontWeight: 600 }">[{{ choice.message.role || 'unknown' }}]</span>
                      <pre v-if="choice.message.content" style="margin:4px 0;white-space:pre-wrap;word-break:break-all;max-height:300px;overflow:auto;font-size:11px;line-height:1.5">{{ choice.message.content }}</pre>
                      <div v-if="choice.message.tool_calls" style="margin-top:6px">
                        <div style="color:var(--muted);font-size:11px;margin-bottom:4px">工具调用:</div>
                        <pre v-for="(tc, j) in choice.message.tool_calls" :key="j" style="margin:0 0 4px;white-space:pre-wrap;word-break:break-all;font-size:11px;padding:4px;background:var(--surface-primary, #16213e);border-radius:4px">{{ formatJson(tc) }}</pre>
                      </div>
                    </div>
                  </div>
                  <div v-if="detail.response_body.usage" style="margin-top:8px;padding:8px;background:var(--surface-primary, #16213e);border-radius:4px">
                    <strong>Usage:</strong> prompt={{ detail.response_body.usage.prompt_tokens }} completion={{ detail.response_body.usage.completion_tokens }} total={{ detail.response_body.usage.total_tokens }}
                  </div>
                </template>
                <pre v-else style="white-space:pre-wrap;word-break:break-all;font-size:11px;line-height:1.5">{{ formatJson(detail.response_body) }}</pre>
              </template>
              <div v-else style="color:var(--muted)">(无响应数据 — 流式响应暂不记录完整内容)</div>
            </template>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.request-log-row {
  cursor: pointer;
}
.request-log-row:hover td {
  background: color-mix(in srgb, var(--accent, #3b82f6) 8%, transparent);
}
.request-log-row.row-failure td {
  background: color-mix(in srgb, var(--danger, #ef4444) 4%, transparent);
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
  color: var(--accent, #3b82f6);
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
  color: var(--accent, #3b82f6);
  text-decoration: underline;
}
.trace-full {
  white-space: normal;
  word-break: break-all;
  overflow-wrap: anywhere;
}
.trace-summary {
  border-left: 3px solid var(--accent, #3b82f6);
}
.tenant-badge {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  background: var(--surface-secondary, #f3f4f6);
  color: var(--text-secondary, #6b7280);
}
.tenant-badge--admin {
  background: rgba(59, 130, 246, 0.1);
  color: #3b82f6;
}
.tenant-badge--default {
  background: rgba(34, 197, 94, 0.1);
  color: #22c55e;
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
  background: var(--surface-secondary, #f3f4f6);
  color: var(--text-primary, #111827);
}
.compression-badge .badge-sep {
  color: var(--text-secondary, #9ca3af);
}
.compression-badge.strategy-mechanical_trim {
  background: rgba(245, 158, 11, 0.1);
  color: #b45309;
}
.compression-badge.strategy-memora_l1_inject {
  background: rgba(139, 92, 246, 0.1);
  color: #6d28d9;
}
.compression-badge.strategy-llm_summary {
  background: rgba(59, 130, 246, 0.1);
  color: #1d4ed8;
}
.compression-badge.strategy-noop {
  background: rgba(107, 114, 128, 0.1);
  color: #4b5563;
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
  background: rgba(168, 85, 247, 0.12);
  color: #7e22ce;
  border: 1px solid rgba(168, 85, 247, 0.3);
}
.col-compress {
  max-width: 180px;
  min-width: 120px;
}
/* v3 Outbound tab — highlight when outbound differs from request. */
.outbound-diff-badge {
  display: inline-block;
  padding: 1px 6px;
  border-radius: 6px;
  font-size: 10px;
  font-weight: 500;
  margin-left: 6px;
  background: rgba(20, 184, 166, 0.1);
  color: #0f766e;
}
.outbound-diff-badge.unchanged {
  background: rgba(107, 114, 128, 0.08);
  color: #6b7280;
}
.summary-marker-badge {
  display: inline-block;
  padding: 1px 6px;
  border-radius: 6px;
  font-size: 10px;
  font-weight: 500;
  margin-left: 6px;
  background: rgba(59, 130, 246, 0.1);
  color: #1d4ed8;
  font-family: var(--mono-font, ui-monospace, monospace);
}
.parent-id {
  color: var(--text-secondary, #6b7280);
  font-size: 10px;
  margin-top: 2px;
  font-family: var(--mono-font, ui-monospace, monospace);
}
.parent-id-clickable {
  cursor: pointer;
  color: var(--accent, #3b82f6);
}
.parent-id-clickable:hover {
  text-decoration: underline;
}
.cell-line1.muted {
  color: var(--text-secondary, #9ca3af);
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
  flex-wrap: wrap;
}
.pagination-info {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--muted);
  font-size: 12px;
  flex-wrap: wrap;
  min-width: 0;
}
.pagination-controls {
  display: flex;
  gap: 8px;
  flex-wrap: nowrap;
  flex-shrink: 0;
}
.page-size-select {
  padding: 2px 6px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  font-size: 12px;
}
</style>

