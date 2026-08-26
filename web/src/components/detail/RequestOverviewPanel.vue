<script setup lang="ts">
import { computed, ref } from 'vue'
import type { RequestLogDetail } from '../../api/logs'
import type { UnifiedRequestDetail } from '../../api/requestDetail'
import {
  extractAssistantReply,
  extractLastUserPrompt,
} from './messageHelpers'
import { statusToneClass } from './statusTone'

const props = defineProps<{
  log: RequestLogDetail | null
  unified: UnifiedRequestDetail | null
  requestBody?: unknown
  responseBody?: unknown
  sessionSnap?: Record<string, unknown> | null
}>()

const emit = defineEmits<{
  (e: 'goto', section: 'chat' | 'waterfall' | 'attempts' | 'flow'): void
}>()

const qaExpanded = ref(false)

const sourceLabel = computed(() => {
  const s = props.unified?.source
  if (!s) return '—'
  const map: Record<string, string> = {
    memory: '本机内存',
    file: '本机文件',
    request_logs: 'request_logs',
    session_turns: 'session_turns',
  }
  return map[s] || s
})

const persistenceLabel = computed(() =>
  props.unified?.persistence === 'in_flight' ? '在途' : '已落库',
)

const log = computed(() => props.log)

function fmt(v: unknown): string {
  if (v == null || v === '') return '—'
  return String(v)
}

const userPrompt = computed(() => {
  const fromBody = extractLastUserPrompt(props.requestBody)
  if (fromBody) return fromBody
  return String(props.log?.request_preview || '').trim()
})

const assistantReply = computed(() => {
  const fromBody = extractAssistantReply(props.responseBody)
  if (fromBody) return fromBody
  return String(props.log?.response_preview || '').trim()
})

const snapTitle = computed(() => {
  const t = props.sessionSnap?.title
  return typeof t === 'string' ? t : ''
})

const sessionTitle = computed(
  () => snapTitle.value || String(props.log?.session_title || '').trim() || '',
)

const requestStatus = computed(
  () => props.log?.request_status ?? props.unified?.meta.request_status ?? '',
)

const hasFailure = computed(() => {
  const stage = props.log?.failure_stage
  const code = props.log?.failure_detail_code || props.log?.error_kind
  return Boolean(stage || code)
})

function clip(text: string, max = 480): string {
  if (!text) return '—'
  if (qaExpanded.value || text.length <= max) return text
  return `${text.slice(0, max)}…`
}
</script>

<template>
  <div class="overview">
    <section class="qa-card" data-testid="overview-turn-qa">
      <div class="qa-head">
        <strong>本轮问答</strong>
        <button type="button" class="btn btn-sm" @click="emit('goto', 'chat')">查看完整对话</button>
      </div>
      <div class="qa-block qa-block--user">
        <span class="lbl">用户</span>
        <pre class="qa-pre">{{ clip(userPrompt) }}</pre>
      </div>
      <div class="qa-block qa-block--assistant">
        <span class="lbl">模型回复</span>
        <pre class="qa-pre">{{ clip(assistantReply) }}</pre>
      </div>
      <button
        v-if="(userPrompt.length > 480 || assistantReply.length > 480) && !qaExpanded"
        type="button"
        class="btn btn-sm linkish"
        @click="qaExpanded = true"
      >展开全文</button>
    </section>

    <div class="grid">
      <div class="cell"><span class="lbl">请求ID</span><code>{{ log?.request_id || unified?.meta.request_id || '—' }}</code></div>
      <div class="cell" :class="statusToneClass(requestStatus, 'cell')">
        <span class="lbl">状态</span>
        <span class="pill" :class="statusToneClass(requestStatus, 'pill')">{{ fmt(requestStatus) }}</span>
      </div>
      <div class="cell"><span class="lbl">延迟</span><span>{{ fmt(log?.latency_ms ?? unified?.meta.latency_ms) }}ms</span></div>
      <div class="cell"><span class="lbl">客户端模型</span><span>{{ fmt(log?.client_model ?? unified?.meta.client_model) }}</span></div>
      <div class="cell"><span class="lbl">出站/规范模型</span><span>{{ fmt(log?.outbound_model || log?.canonical_model) }}</span></div>
      <div class="cell"><span class="lbl">供应商</span><span>{{ fmt(log?.provider_name || log?.provider_code) }}</span></div>
      <div class="cell"><span class="lbl">凭据</span><span>{{ fmt(log?.credential_label || log?.credential_id) }}</span></div>
      <div class="cell"><span class="lbl">Session</span><code>{{ fmt(log?.gw_session_id ?? unified?.meta.gw_session_id) }}</code></div>
      <div class="cell"><span class="lbl">会话标题</span><span>{{ sessionTitle || '—' }}</span></div>
      <div class="cell"><span class="lbl">任务 ID</span><code>{{ fmt(log?.gw_task_id ?? unified?.meta.gw_task_id) }}</code></div>
      <div class="cell"><span class="lbl">End User</span><code>{{ fmt(log?.end_user_id) }}</code></div>
      <div class="cell"><span class="lbl">Token</span><span>{{ fmt(log?.prompt_tokens) }} / {{ fmt(log?.completion_tokens) }}（总 {{ fmt(log?.total_tokens) }}）</span></div>
      <div class="cell"><span class="lbl">Cache</span><span>{{ fmt(log?.cache_read_tokens) }} / {{ fmt(log?.cache_write_tokens) }}</span></div>
      <div class="cell"><span class="lbl">Cost / Credits</span><span>{{ fmt(log?.cost_usd) }} / {{ fmt(log?.credits_charged) }}</span></div>
      <div class="cell"><span class="lbl">finish_reason</span><span>{{ fmt(log?.upstream_finish_reason) }}</span></div>
      <div class="cell" :class="{ 'cell--err': hasFailure }">
        <span class="lbl">failure</span>
        <span>{{ fmt(log?.failure_stage) }} · {{ fmt(log?.failure_detail_code || log?.error_kind) }}</span>
      </div>
      <div class="cell"><span class="lbl">压缩</span><span>{{ fmt(log?.compression_strategy) }} · {{ fmt(log?.compression_reason) }}</span></div>
      <div class="cell"><span class="lbl">parent_request</span><code>{{ fmt(log?.parent_request_id) }}</code></div>
      <div class="cell"><span class="lbl">Agent</span><span>{{ fmt(log?.agent_name) }} · {{ fmt(log?.agent_type) }}</span></div>
      <div class="cell"><span class="lbl">快照模型/供应商</span><span>{{ fmt(sessionSnap?.last_model) }} · {{ fmt(sessionSnap?.last_provider) }}</span></div>
      <div class="cell"><span class="lbl">Stream</span><span>首包 {{ fmt(log?.stream_first_chunk_ms) }}ms · chunks {{ fmt(log?.stream_chunk_count) }}</span></div>
      <div
        class="cell"
        :class="log?.affinity_hit == null ? '' : statusToneClass(log.affinity_hit ? 'hit' : 'miss', 'cell')"
      >
        <span class="lbl">亲和</span>
        <span>{{ log?.affinity_hit == null ? '—' : (log.affinity_hit ? 'hit' : 'miss') }}</span>
      </div>
      <div class="cell"><span class="lbl">应用 / Key</span><span>{{ fmt(log?.application_code) }} · {{ fmt(log?.api_key_prefix) }}</span></div>
      <div class="cell"><span class="lbl">附件</span><span>{{ fmt(log?.attachment_count ?? 0) }}</span></div>
      <div class="cell"><span class="lbl">数据源</span><span>{{ sourceLabel }} · {{ persistenceLabel }}</span></div>
      <div class="cell"><span class="lbl">轮次</span><span>{{ fmt(unified?.meta.turn_number) }}</span></div>
    </div>
    <p class="flow-links">
      <button type="button" class="btn btn-sm" @click="emit('goto', 'waterfall')">调度瀑布</button>
      <button type="button" class="btn btn-sm" @click="emit('goto', 'attempts')">路由与重试</button>
      <button type="button" class="btn btn-sm" @click="emit('goto', 'flow')">流程 Trace</button>
    </p>
    <p v-if="unified?.warning" class="warn">{{ unified.warning }}</p>
  </div>
</template>

<style scoped>
.qa-card {
  margin-bottom: 14px; padding: 12px; border: 1px solid var(--border);
  border-radius: 8px; background: var(--bg-subtle, var(--surface-secondary));
}
.qa-head {
  display: flex; align-items: center; justify-content: space-between;
  gap: 8px; margin-bottom: 8px;
}
.qa-block { margin-bottom: 8px; padding: 8px; border-radius: 6px; border-left: 3px solid var(--border); }
.qa-block--user { border-left-color: var(--info, #2563eb); background: color-mix(in srgb, var(--info, #2563eb) 8%, transparent); }
.qa-block--assistant { border-left-color: var(--success, #16a34a); background: color-mix(in srgb, var(--success, #16a34a) 8%, transparent); }
.qa-pre {
  margin: 4px 0 0; white-space: pre-wrap; word-break: break-word;
  font-size: 12px; line-height: 1.45; max-height: 220px; overflow: auto;
}
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 8px 12px;
}
.cell {
  display: flex; flex-direction: column; gap: 2px;
  font-size: 12px; padding: 8px; border: 1px solid var(--border); border-radius: 6px;
}
.cell--ok { border-color: color-mix(in srgb, var(--success, #16a34a) 45%, var(--border)); }
.cell--err { border-color: color-mix(in srgb, var(--danger, #dc2626) 55%, var(--border)); background: color-mix(in srgb, var(--danger, #dc2626) 6%, transparent); }
.cell--warn { border-color: color-mix(in srgb, var(--warning, #d97706) 50%, var(--border)); }
.cell--info { border-color: color-mix(in srgb, var(--info, #2563eb) 45%, var(--border)); }
.pill {
  display: inline-block; width: fit-content; padding: 1px 8px; border-radius: 999px;
  font-size: 11px; font-weight: 600;
}
.pill--ok { color: var(--success, #16a34a); background: color-mix(in srgb, var(--success, #16a34a) 14%, transparent); }
.pill--err { color: var(--danger, #dc2626); background: color-mix(in srgb, var(--danger, #dc2626) 14%, transparent); }
.pill--warn { color: var(--warning, #d97706); background: color-mix(in srgb, var(--warning, #d97706) 16%, transparent); }
.pill--info { color: var(--info, #2563eb); background: color-mix(in srgb, var(--info, #2563eb) 14%, transparent); }
.pill--muted { color: var(--muted); background: var(--bg-subtle, var(--surface-secondary)); }
.lbl { color: var(--muted); font-size: 11px; }
code { font-size: 11px; word-break: break-all; }
.flow-links { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 12px; }
.warn { color: var(--warning); font-size: 12px; margin-top: 8px; }
.linkish { margin-top: 4px; }
</style>
