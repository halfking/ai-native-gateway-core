<script setup lang="ts">
import { computed } from 'vue'
import type { RequestLogDetail } from '../../api/logs'
import type { UnifiedRequestDetail } from '../../api/requestDetail'

const props = defineProps<{
  log: RequestLogDetail | null
  unified: UnifiedRequestDetail | null
}>()

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
</script>

<template>
  <div class="overview">
    <div class="grid">
      <div class="cell"><span class="lbl">请求ID</span><code>{{ log?.request_id || unified?.meta.request_id || '—' }}</code></div>
      <div class="cell"><span class="lbl">状态</span><span>{{ fmt(log?.request_status ?? unified?.meta.request_status) }}</span></div>
      <div class="cell"><span class="lbl">延迟</span><span>{{ fmt(log?.latency_ms ?? unified?.meta.latency_ms) }}ms</span></div>
      <div class="cell"><span class="lbl">客户端模型</span><span>{{ fmt(log?.client_model ?? unified?.meta.client_model) }}</span></div>
      <div class="cell"><span class="lbl">出站/规范模型</span><span>{{ fmt(log?.outbound_model || log?.canonical_model) }}</span></div>
      <div class="cell"><span class="lbl">供应商</span><span>{{ fmt(log?.provider_name || log?.provider_code) }}</span></div>
      <div class="cell"><span class="lbl">凭据</span><span>{{ fmt(log?.credential_label || log?.credential_id) }}</span></div>
      <div class="cell"><span class="lbl">Session</span><code>{{ fmt(log?.gw_session_id ?? unified?.meta.gw_session_id) }}</code></div>
      <div class="cell"><span class="lbl">Token</span><span>{{ fmt(log?.prompt_tokens) }} / {{ fmt(log?.completion_tokens) }}（总 {{ fmt(log?.total_tokens) }}）</span></div>
      <div class="cell"><span class="lbl">Cache</span><span>{{ fmt(log?.cache_read_tokens) }} / {{ fmt(log?.cache_write_tokens) }}</span></div>
      <div class="cell"><span class="lbl">Cost / Credits</span><span>{{ fmt(log?.cost_usd) }} / {{ fmt(log?.credits_charged) }}</span></div>
      <div class="cell"><span class="lbl">finish_reason</span><span>{{ fmt(log?.upstream_finish_reason) }}</span></div>
      <div class="cell"><span class="lbl">failure</span><span>{{ fmt(log?.failure_stage) }} · {{ fmt(log?.failure_detail_code || log?.error_kind) }}</span></div>
      <div class="cell"><span class="lbl">压缩</span><span>{{ fmt(log?.compression_strategy) }} · {{ fmt(log?.compression_reason) }}</span></div>
      <div class="cell"><span class="lbl">parent_request</span><code>{{ fmt(log?.parent_request_id) }}</code></div>
      <div class="cell"><span class="lbl">Agent</span><span>{{ fmt(log?.agent_name) }} · {{ fmt(log?.agent_type) }}</span></div>
      <div class="cell"><span class="lbl">Stream</span><span>首包 {{ fmt(log?.stream_first_chunk_ms) }}ms · chunks {{ fmt(log?.stream_chunk_count) }}</span></div>
      <div class="cell"><span class="lbl">亲和</span><span>{{ log?.affinity_hit == null ? '—' : (log.affinity_hit ? 'hit' : 'miss') }}</span></div>
      <div class="cell"><span class="lbl">应用 / Key</span><span>{{ fmt(log?.application_code) }} · {{ fmt(log?.api_key_prefix) }}</span></div>
      <div class="cell"><span class="lbl">附件</span><span>{{ fmt(log?.attachment_count ?? 0) }}</span></div>
      <div class="cell"><span class="lbl">数据源</span><span>{{ sourceLabel }} · {{ persistenceLabel }}</span></div>
      <div class="cell"><span class="lbl">轮次</span><span>{{ fmt(unified?.meta.turn_number) }}</span></div>
    </div>
    <p v-if="unified?.warning" class="warn">{{ unified.warning }}</p>
  </div>
</template>

<style scoped>
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 8px 12px;
}
.cell {
  display: flex; flex-direction: column; gap: 2px;
  font-size: 12px; padding: 8px; border: 1px solid var(--border); border-radius: 6px;
}
.lbl { color: var(--muted); font-size: 11px; }
code { font-size: 11px; word-break: break-all; }
.warn { color: var(--warning); font-size: 12px; margin-top: 8px; }
</style>
