<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
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
const successFilter = ref<'' | 'success' | 'failure'>('')
const errorKindFilter = ref('')
const usageSourceFilter = ref<'' | 'llm' | 'estimated'>('')
const modelNameFilter = ref('')

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
  modelNameFilter.value = typeof name === 'string' ? name.trim() : ''
}

const ERROR_KIND_LABELS: Record<string, string> = {
  model_not_found: '模型未找到',
  provider_error: '供应商错误',
  auth_error: '认证失败',
  rate_limit: '限流',
  timeout: '超时',
  canceled: '已取消',
  upstream_error: '上游错误',
}

function statusLabel(row: RequestLogRow): string {
  if (row.success) return '成功'
  const kind = row.error_kind || ''
  if (ERROR_KIND_LABELS[kind]) return ERROR_KIND_LABELS[kind]
  return kind || '失败'
}

async function load() {
  loading.value = true
  error.value = null
  try {
    const range = timeRange()
    const successParam = successFilter.value === '' ? undefined : successFilter.value === 'success'
    const resp: RequestLogsResponse = await getRequestLogs({
      api_key_id: apiKeyId.value === '' ? undefined : Number(apiKeyId.value),
      from: range.from,
      to: range.to,
      q: keyword.value.trim() || undefined,
      success: successParam,
      error_kind: errorKindFilter.value.trim() || undefined,
      model: modelNameFilter.value || undefined,
      usage_source: usageSourceFilter.value === '' ? undefined : usageSourceFilter.value,
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
  if (q.success === 'success' || q.success === 'failure') {
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
          <option value="success">成功</option>
          <option value="failure">失败</option>
        </select>
        <select v-model="errorKindFilter" class="cf-select cf-error" title="错误类型">
          <option value="">全部错误</option>
          <option value="model_not_found">模型未找到</option>
          <option value="provider_error">供应商错误</option>
          <option value="timeout">超时</option>
          <option value="rate_limit">限流</option>
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
        <button class="btn btn-primary btn-sm" @click="resetPageAndLoad">查询</button>
      </div>
    </div>

    <p v-if="error" style="color:var(--danger);margin-bottom:12px">{{ error }}</p>

    <div class="card" style="overflow-x:auto">
      <table class="data-table" style="width:100%;font-size:12px">
        <thead>
          <tr>
            <th>时间</th>
            <th>Key</th>
            <th>客户端模型</th>
            <th>出站模型</th>
            <th>出站供应商</th>
            <th>出站凭据</th>
            <th>模式</th>
            <th>身份</th>
            <th>流式</th>
            <th>输入</th>
            <th>输出</th>
            <th>缓存读</th>
            <th>缓存写</th>
            <th>成本</th>
            <th>延迟</th>
            <th>状态</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading"><td colspan="17">加载中…</td></tr>
          <tr v-else-if="!rows.length"><td colspan="17">无记录</td></tr>
          <tr v-for="r in rows" :key="r.request_id + r.ts" style="cursor:pointer" @click="showDetail(r.request_id)">
            <td>{{ fmtTs(r.ts) }}</td>
            <td>{{ r.api_key_id ?? '—' }}</td>
            <td>{{ r.client_model ?? '—' }}</td>
            <td>{{ r.outbound_model ?? '—' }}</td>
            <td>
              <div>{{ r.provider_name ?? '—' }}</div>
              <div v-if="r.provider_id" style="color:var(--muted);font-size:11px">#{{ r.provider_id }} {{ r.provider_code ?? '' }}</div>
            </td>
            <td>
              <div>{{ r.credential_label ?? '—' }}</div>
              <div v-if="r.credential_id" style="color:var(--muted);font-size:11px">#{{ r.credential_id }}</div>
            </td>
            <td>{{ r.request_mode ?? r.client_profile ?? '—' }}</td>
            <td>
              <div>{{ shortHash(r.identity_hash) }}</div>
              <div v-if="r.virtual_ip || r.affinity_hit != null" style="color:var(--muted);font-size:11px">
                {{ r.virtual_ip ?? '—' }} / {{ r.affinity_hit ? 'affinity' : 'no-affinity' }}
              </div>
            </td>
            <td>
              <div v-if="r.stream_chunk_count != null">
                {{ r.stream_chunk_count }} chunks
              </div>
              <div v-if="r.stream_first_chunk_ms != null" style="color:var(--muted);font-size:11px">
                first {{ r.stream_first_chunk_ms }}ms / {{ r.stream_done_sent ? 'done' : (r.stream_interrupted ? 'interrupted' : 'pending') }}
              </div>
              <span v-if="r.stream_chunk_count == null">—</span>
            </td>
            <td :title="tokenTitle(r.usage_source)">{{ token(r.prompt_tokens, r.usage_source) }}</td>
            <td :title="tokenTitle(r.usage_source)">{{ token(r.completion_tokens, r.usage_source) }}</td>
            <td :title="tokenTitle(r.usage_source)">{{ token(r.cache_read_tokens, r.usage_source) }}</td>
            <td :title="tokenTitle(r.usage_source)">{{ token(r.cache_write_tokens, r.usage_source) }}</td>
            <td :title="tokenTitle(r.usage_source)">{{ costDisplay(r.cost_display ?? r.cost_usd, r.cost_currency) }}</td>
            <td>{{ r.latency_ms != null ? r.latency_ms + 'ms' : '—' }}</td>
            <td :style="{ color: r.success ? 'var(--success)' : 'var(--danger)' }" :title="r.error_kind || ''">{{ statusLabel(r) }}</td>
            <td><button class="btn btn-sm" @click.stop="showDetail(r.request_id)">查看</button></td>
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
              <span><strong>时间:</strong> {{ fmtTs(detail.ts) }}</span>
              <span><strong>客户端模型:</strong> {{ detail.client_model ?? '—' }}</span>
              <span><strong>出站模型:</strong> {{ detail.outbound_model ?? '—' }}</span>
              <span><strong>供应商:</strong> {{ detail.provider_name ?? '—' }}</span>
              <span><strong>状态:</strong> <span :style="{ color: detail.success ? 'var(--success)' : 'var(--danger)' }">{{ detail.success ? '成功' : (detail.error_kind ?? '失败') }}</span></span>
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

