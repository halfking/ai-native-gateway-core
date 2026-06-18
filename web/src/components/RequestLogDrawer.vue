<script setup lang="ts">
import { ref, watch } from 'vue'
import { getRequestLogDetail, type RequestLogDetail } from '../api'

const props = defineProps<{
  requestId: string | null
}>()

const emit = defineEmits<{
  close: []
}>()

const loading = ref(false)
const detail = ref<RequestLogDetail | null>(null)
const error = ref('')
const tab = ref<'request' | 'response'>('request')

watch(
  () => props.requestId,
  async (id) => {
    detail.value = null
    error.value = ''
    tab.value = 'request'
    if (!id) return
    loading.value = true
    try {
      detail.value = await getRequestLogDetail(id)
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : '加载失败'
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)

function fmtTs(v: string | null | undefined) {
  if (!v) return '—'
  return new Date(v).toLocaleString('zh-CN')
}

function formatJson(obj: unknown): string {
  if (obj == null) return '(无数据)'
  try {
    return JSON.stringify(obj, null, 2)
  } catch {
    return String(obj)
  }
}

function extractMessagesFromBody(body: unknown): Record<string, unknown>[] {
  if (body == null) return []
  let parsed: unknown = body
  if (typeof parsed === 'string') {
    try { parsed = JSON.parse(parsed) } catch { return [] }
  }
  if (Array.isArray(parsed)) return parsed as Record<string, unknown>[]
  if (typeof parsed === 'object' && parsed !== null) {
    const o = parsed as Record<string, unknown>
    if (Array.isArray(o.messages)) return o.messages as Record<string, unknown>[]
    if (Array.isArray(o.choices)) {
      const msgs: Record<string, unknown>[] = []
      for (const c of o.choices as Record<string, unknown>[]) {
        if (c.message) msgs.push(c.message as Record<string, unknown>)
      }
      return msgs
    }
    return [o]
  }
  return []
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

function statusLabel(row: RequestLogDetail): string {
  if (row.request_status === 'in_progress') return '请求中'
  if (row.request_status === 'failure') return row.error_kind || '失败'
  return row.success ? '成功' : '失败'
}

function outboundModelDisplay(row: RequestLogDetail | null): string {
  if (!row) return '—'
  return row.provider_model || row.outbound_model || '—'
}
</script>

<template>
  <div v-if="requestId" class="drawer-backdrop" @click="emit('close')">
    <div class="drawer-panel card drawer-panel-wide" @click.stop>
      <div class="drawer-header">
        <h3 style="margin:0">原始请求详情</h3>
        <button class="btn btn-sm" type="button" @click="emit('close')">关闭</button>
      </div>

      <div v-if="loading" class="drawer-loading">加载中…</div>
      <div v-else-if="error" class="drawer-error">{{ error }}</div>

      <template v-else-if="detail">
        <div class="drawer-section">
          <div class="meta-line">
            <span><strong>请求ID:</strong> <code>{{ detail.request_id }}</code></span>
            <span><strong>时间:</strong> {{ fmtTs(detail.ts) }}</span>
            <span><strong>模型:</strong> {{ detail.client_model ?? '—' }}</span>
            <span><strong>出站:</strong> {{ outboundModelDisplay(detail) }}</span>
            <span><strong>状态:</strong>
              <span :style="{ color: detail.success ? 'var(--success)' : 'var(--danger)' }">
                {{ detail.success ? '成功' : statusLabel(detail) }}
              </span>
            </span>
            <span><strong>延迟:</strong> {{ detail.latency_ms ?? '—' }}ms</span>
            <span><strong>Token:</strong> {{ detail.prompt_tokens ?? '—' }} / {{ detail.completion_tokens ?? '—' }}</span>
            <span v-if="detail.gw_session_id"><strong>Session:</strong> {{ detail.gw_session_id }}</span>
            <span v-if="detail.gw_task_id"><strong>Task:</strong> {{ detail.gw_task_id }}</span>
          </div>
        </div>

        <div class="drawer-section">
          <div class="tab-row">
            <button class="btn btn-sm" type="button" :class="{ 'btn-primary': tab === 'request' }" @click="tab = 'request'">请求消息</button>
            <button class="btn btn-sm" type="button" :class="{ 'btn-primary': tab === 'response' }" @click="tab = 'response'">响应内容</button>
          </div>
        </div>

        <div class="drawer-body-scroll">
          <template v-if="tab === 'request'">
            <template v-if="extractMessagesFromBody(detail.request_body).length">
              <div v-for="(msg, i) in extractMessagesFromBody(detail.request_body)" :key="i" class="msg-block">
                <div class="msg-role" :style="{ color: roleColor(String(msg.role || '')) }">[{{ msg.role || 'unknown' }}]</div>
                <pre class="msg-pre">{{ formatJson(msg.content ?? msg) }}</pre>
                <div v-if="msg.tool_calls" class="tool-block">
                  <div class="tool-label">工具调用:</div>
                  <pre v-for="(tc, j) in (msg.tool_calls as unknown[])" :key="j" class="tool-pre">{{ formatJson(tc) }}</pre>
                </div>
              </div>
            </template>
            <div v-else class="text-muted">(无请求数据)</div>
          </template>

          <template v-else>
            <template v-if="detail.response_body">
              <template v-if="(detail.response_body as Record<string, unknown>).choices">
                <div
                  v-for="(choice, i) in ((detail.response_body as Record<string, unknown>).choices as Record<string, unknown>[])"
                  :key="i"
                  class="msg-block"
                >
                  <div class="msg-role">Choice {{ i }}
                    <span v-if="choice.finish_reason" class="text-muted"> · finish: {{ choice.finish_reason }}</span>
                  </div>
                  <template v-if="choice.message">
                    <div :style="{ color: roleColor(String((choice.message as Record<string, unknown>).role || '')) }">
                      [{{ (choice.message as Record<string, unknown>).role || 'unknown' }}]
                    </div>
                    <pre v-if="(choice.message as Record<string, unknown>).content" class="msg-pre">{{ (choice.message as Record<string, unknown>).content }}</pre>
                    <div v-if="(choice.message as Record<string, unknown>).tool_calls" class="tool-block">
                      <div class="tool-label">工具调用:</div>
                      <pre
                        v-for="(tc, j) in ((choice.message as Record<string, unknown>).tool_calls as unknown[])"
                        :key="j"
                        class="tool-pre"
                      >{{ formatJson(tc) }}</pre>
                    </div>
                  </template>
                </div>
              </template>
              <pre v-else class="msg-pre">{{ formatJson(detail.response_body) }}</pre>
            </template>
            <div v-else class="text-muted">(无响应数据)</div>
          </template>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.drawer-loading, .drawer-error {
  padding: 32px;
  text-align: center;
  font-size: 13px;
}
.drawer-error { color: var(--danger); }
.meta-line {
  display: flex;
  flex-wrap: wrap;
  gap: 10px 16px;
  font-size: 12px;
  margin-bottom: 8px;
}
.tab-row { display: flex; gap: 8px; margin-bottom: 8px; }
.drawer-body-scroll {
  flex: 1;
  overflow: auto;
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 12px;
  background: var(--bg-subtle);
  font-size: 12px;
  max-height: calc(100vh - 220px);
}
.msg-block { margin-bottom: 12px; }
.msg-role { font-weight: 600; margin-bottom: 4px; }
.msg-pre {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 320px;
  overflow: auto;
  font-size: 11px;
  line-height: 1.5;
}
.tool-block { margin-top: 6px; }
.tool-label { color: var(--muted); font-size: 11px; margin-bottom: 4px; }
.tool-pre {
  margin: 0 0 4px;
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 11px;
  padding: 4px;
  background: var(--card);
  border-radius: 4px;
}
.text-muted { color: var(--muted); }
</style>
