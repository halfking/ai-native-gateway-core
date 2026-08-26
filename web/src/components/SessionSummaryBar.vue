<script setup lang="ts">
// SessionSummaryBar.vue — V2-P4 + V2-P5 (2026-07-24)
// Sticky top bar with session title, summary, aggregate metrics, and
// an "即时总结" button. Trigger posts to /instant-summary and then
// polls /snapshot for up to 30s, looking for a fresh
// summary_generated_at timestamp.

import { ref } from 'vue'
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

const status = ref<'idle' | 'pending' | 'done' | 'failed'>('idle')
const errMsg = ref('')

async function poll(): Promise<void> {
  for (let i = 0; i < 15; i++) {
    await new Promise((r) => setTimeout(r, 2000))
    try {
      const snap = (await getSessionSnapshot(props.sessionId)) as {
        title?: string
        summary_generated_at?: string
      }
      if (snap.title && snap.summary_generated_at) {
        const ts = Date.parse(snap.summary_generated_at)
        // "fresh" if generated within the last 60s — matches our trigger
        if (ts > Date.now() - 60_000) {
          status.value = 'done'
          return
        }
      }
    } catch (e) {
      // transient: keep polling
    }
  }
  status.value = 'failed'
  errMsg.value = '超时未生成'
}

async function trigger() {
  if (status.value === 'pending') return
  status.value = 'pending'
  errMsg.value = ''
  try {
    await triggerInstantSummary(props.sessionId)
    await poll()
  } catch (e) {
    status.value = 'failed'
    errMsg.value = e instanceof Error ? e.message : String(e)
  }
}
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
  border-bottom: 1px solid #e5e7eb;
  position: sticky;
  top: 0;
  z-index: 10;
  gap: 16px;
}
.left { min-width: 0; flex: 1; }
h2 {
  margin: 0 0 4px;
  font-size: 18px;
  color: #111827;
}
p { margin: 0 0 6px; color: #4b5563; font-size: 13px; }
.metrics { color: #6b7280; font-size: 13px; display: flex; gap: 6px; }
.dot { color: #d1d5db; }
.right { display: flex; gap: 8px; align-items: center; }
.err { color: #ef4444; font-size: 12px; }
.ok { color: #10b981; font-size: 12px; }
</style>
