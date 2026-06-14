<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRoute } from 'vue-router'
import {
  getRequestLogs,
  getRequestLogDetail,
  getKeys,
  type RequestLogRow,
  type RequestLogDetail,
  type ApiKey,
  type RequestLogsResponse,
} from '../api'
import ModelPicker from '../components/ModelPicker.vue'

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

const page = ref(1)
const pageSize = ref(50)
const total = ref(0)

const detailVisible = ref(false)
const detailLoading = ref(false)
const detail = ref<RequestLogDetail | null>(null)
const detailTab = ref<'request' | 'response'>('request')

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
  timeout: '超时',
  canceled: '已取消',
  upstream_error: '上游错误',
  stream_error: '流中断',
  no_candidate: '无可用路由',
  session_forbidden: '会话无权',
  executor_unavailable: '执行器不可用',
}

const FAILURE_DETAIL_LABELS: Record<string, string> = {
  gw_rpm_exceeded: '网关RPM限流',
  gw_concurrent_exceeded: '网关并发限流',
  gw_tpm_exceeded: '网关TPM限流',
  gw_key_throttled: '密钥节流',
  gw_budget_exhausted: '预算耗尽',
  gw_no_candidate: '无可用路由',
  gw_session_forbidden: '会话无权',
}

function statusLabel(row: RequestLogRow): string {
  if (row.request_status === 'in_progress') return '请求中'
  if (row.request_status === 'success' || row.success) return '成功'
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
  return parts.join(' · ') || ''
}

function statusColor(row: RequestLogRow): string {
  if (row.request_status === 'in_progress') return 'var(--warning, #f59e0b)'
  if (row.request_status === 'success' || row.success) return 'var(--success)'
  return 'var(--danger)'
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

function filterByTask(taskId: string | null | undefined) {
  if (!taskId) return
  gwTaskFilter.value = taskId
  gwSessionFilter.value = ''
  resetPageAndLoad()
}

function filterBySession(sessionId: string | null | undefined) {
  if (!sessionId) return
  gwSessionFilter.value = sessionId
  resetPageAndLoad()
}

function clearTraceFilter() {
  gwTaskFilter.value = ''
  gwSessionFilter.value = ''
  resetPageAndLoad()
}

function routeLabel(r: RequestLogRow): string {
  if (r.provider_name) return r.provider_name
  if (r.error_kind === 'missing_key' || r.error_kind === 'invalid_key') return '—'
  return '—'
}

function modelArrow(r: RequestLogRow): string {
  const inM = r.client_model ?? '—'
  const outM = r.outbound_model
  if (!outM || outM === inM) return inM
  return `${inM} → ${outM}`
}

function callerLabel(r: RequestLogRow): string {
  const parts: string[] = []
  if (r.api_key_owner_user) parts.push(r.api_key_owner_user)
  else if (r.api_key_prefix) parts.push(r.api_key_prefix)
  if (r.application_code) parts.push(r.application_code)
  return parts.length ? parts.join(' · ') : (r.api_key_prefix ?? '无key')
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
      <button class="btn btn-primary btn-sm" :disabled="loading" @click="load">刷新</button>
    </div>

    <div class="compact-filter-bar compact-filter-bar--stacked">
      <div class="cf-row">
        <select v-model="apiKeyId" class="cf-select cf-cred" title="API Key">
          <option value="">全部 Key</option>
          <option v-for="k in keys" :key="k.id" :value="k.id">{{ k.key_prefix }} ({{ k.application_code }})</option>
        </select>
        <select v-model="hours" class="cf-select cf-hours" title="时间范围">
          <option :value="1">1小时</option>
          <option :value="6">6小时</option>
          <option :value="24">24小时</option>
          <option :value="168">7天</option>
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
      <span>本页 {{ taskSummary.total }} 步</span>
      <span style="color:var(--success)">成功 {{ taskSummary.ok }}</span>
      <span style="color:var(--danger)">失败 {{ taskSummary.fail }}</span>
      <span v-if="taskSummary.pending" style="color:var(--warning, #f59e0b)">进行中 {{ taskSummary.pending }}</span>
      <span v-if="gwTaskFilter" style="color:var(--muted)">任务: {{ gwTaskFilter }}</span>
      <span v-if="gwSessionFilter" style="color:var(--muted)">会话: {{ shortHash(gwSessionFilter) }}</span>
      <button class="btn btn-ghost btn-sm" style="margin-left:auto" @click="clearTraceFilter">清除脉络筛选</button>
    </div>

    <div class="card" style="overflow-x:auto">
      <table class="data-table request-log-table" style="width:100%;font-size:12px">
        <thead>
          <tr>
            <th v-if="traceMode" class="col-seq">#</th>
            <th class="col-time">时间</th>
            <th class="col-trace">脉络</th>
            <th class="col-caller">调用方</th>
            <th class="col-model">模型</th>
            <th class="col-route">路由</th>
            <th class="col-tokens">Token</th>
            <th class="col-lat">延迟</th>
            <th class="col-status">状态</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading"><td :colspan="traceMode ? 9 : 8">加载中…</td></tr>
          <tr v-else-if="!rows.length"><td :colspan="traceMode ? 9 : 8">无记录</td></tr>
          <tr
            v-for="r in rows"
            :key="r.request_id + r.ts"
            class="request-log-row"
            :class="{ 'row-failure': r.request_status === 'failure' || (!r.success && r.request_status !== 'in_progress') }"
            @click="showDetail(r.request_id)"
          >
            <td v-if="traceMode" class="col-seq" style="color:var(--muted);font-variant-numeric:tabular-nums">
              {{ r.trace_seq ?? '—' }}
            </td>
            <td class="col-time" :title="r.request_id">{{ fmtTs(r.ts) }}</td>
            <td class="col-trace">
              <div
                v-if="r.gw_task_id"
                class="trace-link"
                :title="r.gw_task_id"
                @click.stop="filterByTask(r.gw_task_id)"
              >{{ shortHash(r.gw_task_id) }}</div>
              <div
                v-if="r.gw_session_id"
                class="trace-link trace-sub"
                :title="r.gw_session_id"
                @click.stop="filterBySession(r.gw_session_id)"
              >{{ shortHash(r.gw_session_id) }}</div>
              <span v-if="!r.gw_task_id && !r.gw_session_id" style="color:var(--muted)">—</span>
            </td>
            <td class="col-caller" :title="callerLabel(r)">
              <div>{{ callerLabel(r) }}</div>
              <div v-if="r.identity_hash" class="cell-sub" :title="r.identity_hash">{{ shortHash(r.identity_hash) }}</div>
            </td>
            <td class="col-model" :title="modelArrow(r)">{{ modelArrow(r) }}</td>
            <td class="col-route" :title="r.credential_label || ''">
              <div>{{ routeLabel(r) }}</div>
              <div v-if="r.request_mode" class="cell-sub">{{ r.request_mode }}</div>
            </td>
            <td class="col-tokens" :title="tokenTitle(r.usage_source)">
              <span v-if="r.prompt_tokens != null || r.completion_tokens != null">
                {{ token(r.prompt_tokens, r.usage_source) }}/{{ token(r.completion_tokens, r.usage_source) }}
              </span>
              <span v-else>—</span>
            </td>
            <td class="col-lat">{{ r.latency_ms != null ? r.latency_ms + 'ms' : '—' }}</td>
            <td class="col-status" :style="{ color: statusColor(r) }" :title="statusTitle(r)">{{ statusLabel(r) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="!loading" style="display:flex;gap:12px;align-items:center;justify-content:space-between;margin-top:12px;flex-wrap:wrap">
      <div style="display:flex;align-items:center;gap:8px;color:var(--muted);font-size:12px">
        <span>共 {{ total }} 条</span>
        <span v-if="total > 0">· 第 {{ page }} / {{ Math.max(1, Math.ceil(total / pageSize)) }} 页</span>
        <select v-model.number="pageSize" @change="resetPageAndLoad" style="padding:2px 6px;background:var(--bg);border:1px solid var(--border);border-radius:4px;color:var(--text);font-size:12px">
          <option :value="50">50 / 页</option>
          <option :value="100">100 / 页</option>
          <option :value="200">200 / 页</option>
          <option :value="500">500 / 页</option>
        </select>
      </div>
      <div style="display:flex;gap:8px">
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
              <span><strong>出站模型:</strong> {{ detail.outbound_model ?? '—' }}</span>
              <span><strong>供应商:</strong> {{ detail.provider_name ?? '—' }}</span>
              <span><strong>状态:</strong> <span :style="{ color: detail.success ? 'var(--success)' : 'var(--danger)' }">{{ detail.success ? '成功' : statusLabel(detail) }}</span></span>
              <span v-if="detail.failure_stage"><strong>失败阶段:</strong> {{ detail.failure_stage }}</span>
              <span v-if="detail.failure_detail_code"><strong>失败详情:</strong> {{ detail.failure_detail_code }}</span>
              <span><strong>延迟:</strong> {{ detail.latency_ms ?? '—' }}ms</span>
              <span><strong>Token:</strong> {{ token(detail.prompt_tokens) }} / {{ token(detail.completion_tokens) }}</span>
            </div>
          </div>

          <div class="drawer-section">
            <div style="display:flex;gap:8px;margin-bottom:12px">
              <button class="btn btn-sm" :class="{ 'btn-primary': detailTab === 'request' }" @click="detailTab = 'request'">请求消息</button>
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
  padding: 6px 8px;
  vertical-align: top;
  white-space: nowrap;
}
.col-model {
  white-space: normal;
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
}
.col-caller {
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
}
.cell-sub {
  color: var(--muted);
  font-size: 10px;
  margin-top: 2px;
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
}
.trace-summary {
  border-left: 3px solid var(--accent, #3b82f6);
}
</style>

