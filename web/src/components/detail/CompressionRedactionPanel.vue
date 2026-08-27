<script setup lang="ts">
// CompressionRedactionPanel — original / compressed / secured + placeholder matches.
import { computed, onUnmounted, ref, watch } from 'vue'
import { getSessionCompare, type TurnView } from '../../api/session'
import {
  getSanitizeMatches,
  type SanitizeMatchEntry,
} from '../../api/sanitizeMatches'
import TurnStageCard from '../TurnStageCard.vue'
import { formatJson } from './messageHelpers'
import { isSuperAdmin } from '../../store'
import {
  extractSensitivePlaceholders,
  truncateForPreview,
} from '../../utils/sensitivePlaceholders'

const props = defineProps<{
  sessionId: string | null
  requestId: string | null
  requestBody: unknown
  outboundBody: unknown
  responseBody: unknown
}>()

const loading = ref(false)
const matchLoading = ref(false)
const error = ref('')
const matchError = ref('')
const turn = ref<TurnView | null>(null)
const matches = ref<SanitizeMatchEntry[]>([])
const matchSource = ref('')
const showOriginal = computed(() => isSuperAdmin())

let abort: AbortController | null = null
let loadSeq = 0

watch(
  () => [props.sessionId, props.requestId] as const,
  async ([sid, rid]) => {
    turn.value = null
    matches.value = []
    matchSource.value = ''
    error.value = ''
    matchError.value = ''
    abort?.abort()
    abort = new AbortController()
    const seq = ++loadSeq
    if (!sid) return

    loading.value = true
    matchLoading.value = true
    try {
      const compareP = getSessionCompare(sid, undefined, { requestId: rid || undefined })
        .then((data) => {
          if (seq !== loadSeq) return
          const matched = (data.turns || []).find((t) => t.request_id === rid)
            || (data.turns || [])[0]
            || null
          turn.value = matched
        })
        .catch((e: unknown) => {
          if (seq !== loadSeq) return
          error.value = e instanceof Error ? e.message : String(e)
        })
        .finally(() => {
          if (seq === loadSeq) loading.value = false
        })

      const matchP = getSanitizeMatches(sid, { requestId: rid || undefined, signal: abort!.signal })
        .then((data) => {
          if (seq !== loadSeq) return
          matches.value = data.entries || []
          matchSource.value = data.source || 'empty'
        })
        .catch((e: unknown) => {
          if (seq !== loadSeq) return
          if (e instanceof DOMException && e.name === 'AbortError') return
          matchError.value = e instanceof Error ? e.message : String(e)
        })
        .finally(() => {
          if (seq === loadSeq) matchLoading.value = false
        })

      await Promise.all([compareP, matchP])
    } catch {
      /* per-promise handlers above */
    }
  },
  { immediate: true },
)

onUnmounted(() => {
  abort?.abort()
  loadSeq += 1
})

function safeFormat(v: unknown): string {
  return truncateForPreview(formatJson(v)).text
}

const fallbackOriginal = computed(() => safeFormat(props.requestBody))
const fallbackCompressed = computed(() => safeFormat(props.outboundBody ?? props.requestBody))
const fallbackSecured = computed(() => safeFormat(props.responseBody))

const bodyPlaceholders = computed(() => {
  const blob = [
    turn.value?.compressed?.send || '',
    turn.value?.secured?.send || '',
    typeof props.outboundBody === 'string' ? props.outboundBody : safeFormat(props.outboundBody),
  ].join('\n')
  return extractSensitivePlaceholders(blob)
})

const displayMatches = computed(() => {
  if (matches.value.length) return matches.value
  // Fallback: placeholders parsed from body when Redis map expired.
  return bodyPlaceholders.value.map((p) => ({
    placeholder: p.placeholder,
    type: p.type,
    index: p.index,
    value_masked: '—',
    in_request: true,
  }))
})
</script>

<template>
  <div class="cmp-panel">
    <section class="match-block" data-testid="sanitize-match-table">
      <div class="match-head">
        <strong>敏感信息 / 占位符匹配</strong>
        <span v-if="matchSource" class="text-muted">来源: {{ matchSource }}</span>
      </div>
      <div v-if="matchLoading" class="text-muted">加载匹配数据…</div>
      <div v-else-if="matchError" class="err">{{ matchError }}</div>
      <div v-else-if="!displayMatches.length" class="text-muted">本会话暂无占位符匹配记录（可能已过期或未触发脱敏）。</div>
      <table v-else class="match-table">
        <thead>
          <tr>
            <th>占位符</th>
            <th>类型</th>
            <th>索引</th>
            <th>掩码值</th>
            <th>本请求</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in displayMatches" :key="row.placeholder">
            <td><code class="ph">{{ row.placeholder }}</code></td>
            <td>{{ row.type }}</td>
            <td>{{ row.index }}</td>
            <td><code>{{ row.value_masked }}</code></td>
            <td>{{ row.in_request ? '是' : '—' }}</td>
          </tr>
        </tbody>
      </table>
    </section>

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
.match-block {
  margin-bottom: 14px;
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 10px 12px;
  background: var(--kx-surface, var(--surface));
}
.match-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 8px;
  font-size: 13px;
}
.match-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}
.match-table th,
.match-table td {
  text-align: left;
  padding: 4px 6px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}
.ph { color: var(--kx-warning); }
.gate {
  border: 1px dashed var(--border); border-radius: 8px; padding: 12px;
  font-size: 12px; color: var(--muted);
}
.err { color: var(--danger); font-size: 12px; }
.text-muted { color: var(--muted); font-size: 12px; }
</style>
