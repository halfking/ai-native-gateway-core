<script setup lang="ts">
// CompressionRedactionPanel — original / compressed / secured comparison.
import { computed, ref, watch } from 'vue'
import { getSessionCompare, type TurnView } from '../../api/session'
import TurnStageCard from '../TurnStageCard.vue'
import { formatJson } from './messageHelpers'
import { isSuperAdmin } from '../../store'

const props = defineProps<{
  sessionId: string | null
  requestId: string | null
  requestBody: unknown
  outboundBody: unknown
  responseBody: unknown
}>()

const loading = ref(false)
const error = ref('')
const turn = ref<TurnView | null>(null)
const showOriginal = computed(() => isSuperAdmin())

watch(
  () => [props.sessionId, props.requestId] as const,
  async ([sid, rid]) => {
    turn.value = null
    error.value = ''
    if (!sid) return
    loading.value = true
    try {
      const data = await getSessionCompare(sid, undefined, { requestId: rid || undefined })
      const matched = (data.turns || []).find((t) => t.request_id === rid)
        || (data.turns || [])[0]
        || null
      turn.value = matched
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)

const fallbackOriginal = computed(() => formatJson(props.requestBody))
const fallbackCompressed = computed(() => formatJson(props.outboundBody ?? props.requestBody))
const fallbackSecured = computed(() => formatJson(props.responseBody))
</script>

<template>
  <div class="cmp-panel">
    <div v-if="loading" class="text-muted">加载压缩/脱敏对比…</div>
    <div v-else-if="error && !turn" class="err">{{ error }}</div>
    <div v-else class="cmp-grid">
      <TurnStageCard
        v-if="showOriginal"
        stage-label="原始"
        send-label="发送"
        receive-label="接收"
        :send="turn?.original?.send || fallbackOriginal"
        :receive="turn?.original?.receive || '—'"
        :tokens="turn?.original?.tokens"
      />
      <div v-else class="gate">
        <p>原始正文仅超级管理员可见。当前展示压缩转发与安全处理后的内容。</p>
      </div>
      <TurnStageCard
        stage-label="压缩转发"
        send-label="发送"
        receive-label="接收"
        :send="turn?.compressed?.send || fallbackCompressed"
        :receive="turn?.compressed?.receive || '—'"
        :tokens="turn?.compressed?.tokens"
        :range="turn?.compressed?.range_start != null
          ? `覆盖轮次 ${turn.compressed.range_start}–${turn.compressed.range_end}`
          : undefined"
        :tags="turn?.compressed?.applied_tags"
      />
      <TurnStageCard
        stage-label="安全/脱敏"
        send-label="发送"
        receive-label="接收"
        :send="turn?.secured?.send || '—'"
        :receive="turn?.secured?.receive || fallbackSecured"
        :tokens="turn?.secured?.tokens"
        :tags="turn?.secured?.applied_tags"
        :pii-stripped="turn?.pii_stripped_this_turn"
      />
    </div>
    <p v-if="!sessionId" class="text-muted">无会话 ID，仅展示本请求 request / outbound / response 回退内容。</p>
  </div>
</template>

<style scoped>
.cmp-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 10px;
}
.gate {
  border: 1px dashed var(--border); border-radius: 8px; padding: 12px;
  font-size: 12px; color: var(--muted);
}
.err { color: var(--danger); font-size: 12px; }
.text-muted { color: var(--muted); font-size: 12px; }
</style>
