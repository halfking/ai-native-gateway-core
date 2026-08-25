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
      <div class="cell"><span class="lbl">模型</span><span>{{ fmt(log?.client_model ?? unified?.meta.client_model) }}</span></div>
      <div class="cell"><span class="lbl">Session</span><code>{{ fmt(log?.gw_session_id ?? unified?.meta.gw_session_id) }}</code></div>
      <div class="cell"><span class="lbl">Token</span><span>{{ fmt(log?.prompt_tokens) }} / {{ fmt(log?.completion_tokens) }}</span></div>
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
