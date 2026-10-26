<script setup lang="ts">
// SessionSummaryBar.vue — V2-P4 + V2-P5 (2026-07-24)
// Sticky top bar with session title, summary, aggregate metrics, and
// an "即时总结" button. Trigger posts to /instant-summary and then
// polls /snapshot for up to 30s, looking for a fresh
// summary_generated_at timestamp.

import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { surfaceError } from '../i18n/errorSurface'
import {
  triggerInstantSummary,
  getSessionSnapshot,
} from '../api/sessions_v2'


// 2026-09-13 P5：补齐模板使用的 el-* 组件注册（修复运行时 resolve 失败）
import { ElButton } from 'element-plus'
const props = defineProps<{
  sessionId: string
  title?: string
  summary?: string
  totalTurns?: number
  totalCost?: number
  summaryGeneratedAt?: string
  // durationMs 是 sessions.duration_ms —— 网关每轮刷新的**已流逝时长**
  // （877 起不再是恒 NULL 的死列）。它与 created_at/updated_at 不是冗余：
  // 那两个是行时间戳，本列是网关自己维护的时长口径（写点与读点同源）。
  durationMs?: number
}>()

const emit = defineEmits<{
  (event: 'summary-updated', snapshot: Record<string, unknown>): void
}>()

const status = ref<'idle' | 'pending' | 'done' | 'failed'>('idle')

// durationMs → 人类可读的耗时。
//
// 口径：<1s 显示毫秒；<60s 显示秒；<1h 显示分秒；否则小时分。
// 用 null 而不是 0 表示"后端没给"——0ms 是一个**假的**"瞬时完成"。
const durationText = computed(() => {
  const ms = props.durationMs
  if (typeof ms !== 'number' || !Number.isFinite(ms) || ms < 0) return ''
  if (ms < 1000) return `${Math.round(ms)}ms`
  const totalSec = Math.floor(ms / 1000)
  if (totalSec < 60) return `${totalSec}s`
  if (totalSec < 3600) {
    const m = Math.floor(totalSec / 60)
    const sec = totalSec % 60
    return sec ? `${m}m${sec}s` : `${m}m`
  }
  const h = Math.floor(totalSec / 3600)
  const m = Math.floor((totalSec % 3600) / 60)
  return m ? `${h}h${m}m` : `${h}h`
})
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
    errMsg.value = '摘要没能生成：已超时，请重试'
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
    errMsg.value = surfaceError(e)
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
        <!-- 已流逝时长：sessions.duration_ms 的消费点。
             v-if 挡的是"后端没给"，与 0ms（假的瞬时完成）区分开。 -->
        <template v-if="durationText">
          <span class="dot">&middot;</span>
          <span :title="'已流逝时长（sessions.duration_ms）'">{{ durationText }}</span>
        </template>
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
  background: var(--bg-card, var(--card, var(--kx-surface)));
  border-bottom: 1px solid var(--border, var(--surface-secondary));
  color: var(--text, var(--kx-text));
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
