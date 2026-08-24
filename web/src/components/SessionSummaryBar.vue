<script setup lang="ts">
// SessionSummaryBar.vue — V2-P4 + V2-P5 (2026-07-24)
// Sticky top bar with session title, summary, aggregate metrics, and
// an "即时总结" button. Trigger posts to /instant-summary and then
// polls /snapshot for up to 30s, looking for a fresh
// summary_generated_at timestamp.

import { onBeforeUnmount, ref, watch } from 'vue'
import {
  triggerInstantSummary,
  getSessionSnapshot,
} from '../api/sessions_v2'

const props = defineProps<{
  sessionId: string
  title?: string
  summary?: string
  totalTurns?: number
  totalCost?: number
  summaryGeneratedAt?: string
}>()

const emit = defineEmits<{
  (event: 'summary-updated', snapshot: Record<string, unknown>): void
}>()

const status = ref<'idle' | 'pending' | 'done' | 'failed'>('idle')
const errMsg = ref('')
let pollGeneration = 0
let pollTimer: ReturnType<typeof setTimeout> | null = null

function clearPollTimer() {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = null
}

async function poll(sessionId: string, generation: number, previousGeneratedAt?: string): Promise<void> {
  for (let i = 0; i < 15; i++) {
    await new Promise<void>(resolve => {
      pollTimer = setTimeout(() => {
        pollTimer = null
        resolve()
      }, 2000)
    })
    if (generation !== pollGeneration || sessionId !== props.sessionId) return
    try {
      const snap = (await getSessionSnapshot(sessionId)) as Record<string, unknown>
      const generatedAt = typeof snap.summary_generated_at === 'string' ? snap.summary_generated_at : ''
      const timestamp = generatedAt ? Date.parse(generatedAt) : NaN
      const previous = previousGeneratedAt ? Date.parse(previousGeneratedAt) : NaN
      if (generatedAt && (!previousGeneratedAt || (Number.isFinite(timestamp) && timestamp > previous))) {
        emit('summary-updated', snap)
        if (generation === pollGeneration && sessionId === props.sessionId) status.value = 'done'
        return
      }
    } catch {
      // transient: keep polling
    }
  }
  if (generation === pollGeneration && sessionId === props.sessionId) {
    status.value = 'failed'
    errMsg.value = '超时未生成'
  }
}

async function trigger() {
  if (status.value === 'pending') return
  const sessionId = props.sessionId
  const generation = ++pollGeneration
  const previousGeneratedAt = props.summaryGeneratedAt
  clearPollTimer()
  status.value = 'pending'
  errMsg.value = ''
  try {
    const result = await triggerInstantSummary(sessionId)
    if (generation !== pollGeneration || sessionId !== props.sessionId) return
    if (result && typeof result === 'object' && 'summary' in result) {
      emit('summary-updated', result)
      status.value = 'done'
      return
    }
    await poll(sessionId, generation, previousGeneratedAt)
  } catch (e) {
    if (generation !== pollGeneration || sessionId !== props.sessionId) return
    status.value = 'failed'
    errMsg.value = e instanceof Error ? e.message : String(e)
  }
}

watch(() => props.sessionId, () => {
  pollGeneration++
  clearPollTimer()
  status.value = 'idle'
  errMsg.value = ''
})

onBeforeUnmount(() => {
  pollGeneration++
  clearPollTimer()
})
</script>

<template>
  <div class="summary-bar">
    <div class="left">
      <h2>{{ title || '(未命名会话)' }}</h2>
      <p>{{ summary || '点击「即时总结」生成摘要' }}</p>
      <div class="metrics">
        <span>{{ totalTurns || 0 }} turns</span>
        <span class="dot">&middot;</span>
        <span>${{ (totalCost || 0).toFixed(4) }}</span>
      </div>
    </div>
    <div class="right">
      <el-button
        :loading="status === 'pending'"
        :disabled="status === 'pending'"
        @click="trigger"
      >
        {{
          status === 'pending'
            ? '生成中&hellip;'
            : status === 'done'
              ? '重新总结'
              : '即时总结'
        }}
      </el-button>
      <span v-if="status === 'failed'" class="err">{{ errMsg }}</span>
      <span v-else-if="status === 'done'" class="ok">&check; 已总结</span>
    </div>
  </div>
</template>

<style scoped>
.summary-bar {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  padding: 16px 24px;
  background: white;
  border-bottom: 1px solid var(--surface-secondary);
  position: sticky;
  top: 0;
  z-index: 10;
  gap: 16px;
}
.left { min-width: 0; flex: 1; }
h2 {
  margin: 0 0 4px;
  font-size: 18px;
  color: var(--kx-text);
}
p { margin: 0 0 6px; color: var(--muted); font-size: 13px; }
.metrics { color: var(--muted); font-size: 13px; display: flex; gap: 6px; }
.dot { color: var(--border); }
.right { display: flex; gap: 8px; align-items: center; }
.err { color: var(--danger); font-size: 12px; }
.ok { color: var(--success); font-size: 12px; }
</style>
