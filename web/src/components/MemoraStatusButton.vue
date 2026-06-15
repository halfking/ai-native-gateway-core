<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import {
  getMemoraStatus,
  pingMemora,
  controlMemoraSink,
  type MemoraStatus,
} from '../api'

const status = ref<MemoraStatus | null>(null)
const panelOpen = ref(false)
const loading = ref(false)
const actionLoading = ref(false)
const lastPingMs = ref<number | null>(null)
const lastPingError = ref<string | null>(null)
let pollTimer: ReturnType<typeof setInterval> | null = null

const sinkPaused = computed(() =>
  !!(status.value?.sink_paused || status.value?.sink?.paused),
)

const state = computed<'disabled' | 'paused' | 'ok' | 'error' | 'loading'>(() => {
  if (loading.value && !status.value) return 'loading'
  if (!status.value?.enabled) return 'disabled'
  if (sinkPaused.value) return 'paused'
  if (status.value.connected) return 'ok'
  return 'error'
})

const stateLabel = computed(() => {
  switch (state.value) {
    case 'disabled': return '未启用'
    case 'paused': return '已断开'
    case 'ok': return '已连接'
    case 'error': return '连接失败'
    default: return '检测中'
  }
})

const dotClass = computed(() => `dot dot--${state.value}`)

async function loadStatus() {
  try {
    status.value = await getMemoraStatus()
  } catch {
    /* non-blocking */
  }
}

function togglePanel() {
  panelOpen.value = !panelOpen.value
  if (panelOpen.value && !status.value) void loadStatus()
}

function closePanel() {
  panelOpen.value = false
}

async function handlePing() {
  actionLoading.value = true
  lastPingError.value = null
  try {
    const r = await pingMemora()
    lastPingMs.value = r.latency_ms
    if (!r.connected) lastPingError.value = r.error || '连接失败'
    await loadStatus()
  } catch (e: unknown) {
    lastPingError.value = e instanceof Error ? e.message : '检测失败'
  } finally {
    actionLoading.value = false
  }
}

async function handleDisconnect() {
  actionLoading.value = true
  try {
    await controlMemoraSink('pause')
    await loadStatus()
  } finally {
    actionLoading.value = false
  }
}

async function handleReconnect() {
  actionLoading.value = true
  try {
    if (sinkPaused.value) await controlMemoraSink('resume')
    await handlePing()
  } finally {
    actionLoading.value = false
  }
}

function fmtDate(v: string | null | undefined) {
  if (!v) return '—'
  return new Date(v).toLocaleString('zh-CN', { dateStyle: 'short', timeStyle: 'short' })
}

onMounted(() => {
  void loadStatus()
  pollTimer = setInterval(() => { void loadStatus() }, 30000)
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<template>
  <div v-if="status?.enabled !== false" class="memora-wrap">
    <button
      type="button"
      class="memora-btn"
      :class="`memora-btn--${state}`"
      :title="`Memora ${stateLabel}`"
      @click="togglePanel"
    >
      <span :class="dotClass" aria-hidden="true" />
      <span class="memora-label">Memora</span>
    </button>

    <div v-if="panelOpen" class="memora-backdrop" @click="closePanel" />
    <div v-if="panelOpen" class="memora-panel" role="dialog" aria-label="Memora 连接状态">
      <div class="panel-head">
        <span class="panel-title">Memora 连接</span>
        <span class="panel-badge" :class="`panel-badge--${state}`">{{ stateLabel }}</span>
        <button type="button" class="panel-close" aria-label="关闭" @click="closePanel">×</button>
      </div>

      <dl class="panel-meta">
        <div v-if="status?.base_url" class="meta-row">
          <dt>服务地址</dt>
          <dd><code>{{ status.base_url }}</code></dd>
        </div>
        <div v-if="lastPingMs != null" class="meta-row">
          <dt>最近延迟</dt>
          <dd>{{ lastPingMs }} ms</dd>
        </div>
        <div v-if="status?.last_error" class="meta-row meta-row--error">
          <dt>错误</dt>
          <dd><code>{{ status.last_error }}</code></dd>
        </div>
        <div v-if="lastPingError" class="meta-row meta-row--error">
          <dt>检测</dt>
          <dd>{{ lastPingError }}</dd>
        </div>
        <template v-if="status?.sink">
          <div class="meta-row">
            <dt>写入队列</dt>
            <dd>{{ status.sink.queue_len }} / {{ status.sink.queue_cap }}</dd>
          </div>
          <div class="meta-row">
            <dt>已处理 / 失败</dt>
            <dd>{{ status.sink.processed }} / {{ status.sink.errored }}</dd>
          </div>
          <div v-if="status.sink.consecutive_errors > 0" class="meta-row meta-row--warn">
            <dt>连续失败</dt>
            <dd>{{ status.sink.consecutive_errors }}</dd>
          </div>
          <div v-if="status.sink.last_error" class="meta-row meta-row--error">
            <dt>最近写入错误</dt>
            <dd>
              <code>{{ status.sink.last_error }}</code>
              <span v-if="status.sink.last_error_at" class="meta-time">{{ fmtDate(status.sink.last_error_at) }}</span>
            </dd>
          </div>
        </template>
      </dl>

      <div class="panel-actions">
        <button
          type="button"
          class="btn btn-sm btn-primary"
          :disabled="actionLoading"
          @click="handleReconnect"
        >{{ actionLoading ? '处理中…' : (sinkPaused ? '重连' : '检测连通性') }}</button>
        <button
          v-if="!sinkPaused"
          type="button"
          class="btn btn-sm btn-ghost"
          :disabled="actionLoading"
          @click="handleDisconnect"
        >断开写入</button>
        <button
          type="button"
          class="btn btn-sm btn-ghost"
          :disabled="loading"
          @click="loadStatus"
        >刷新</button>
        <RouterLink
          to="/session-context"
          class="btn btn-sm btn-ghost"
          @click="closePanel"
        >会话上下文</RouterLink>
      </div>
    </div>
  </div>
</template>

<style scoped>
.memora-wrap {
  position: relative;
  flex-shrink: 0;
}

.memora-btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 4px 10px;
  border-radius: 99px;
  border: 1px solid var(--border);
  background: var(--bg);
  font-size: 12px;
  color: var(--muted);
  cursor: pointer;
  transition: border-color .15s, color .15s;
}
.memora-btn:hover {
  border-color: var(--accent);
  color: var(--text);
}
.memora-btn--ok { border-color: rgba(63, 185, 80, 0.35); color: var(--success); }
.memora-btn--error { border-color: rgba(248, 81, 73, 0.4); color: var(--danger); }
.memora-btn--paused { border-color: rgba(210, 153, 34, 0.45); color: var(--warning); }

.dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--muted);
}
.dot--ok { background: var(--success); box-shadow: 0 0 0 2px rgba(63, 185, 80, 0.25); }
.dot--error { background: var(--danger); box-shadow: 0 0 0 2px rgba(248, 81, 73, 0.25); }
.dot--paused { background: var(--warning); }
.dot--loading { animation: pulse 1.2s ease-in-out infinite; }
@keyframes pulse { 50% { opacity: 0.35; } }

.memora-label { font-weight: 600; letter-spacing: 0.02em; }

.memora-backdrop {
  position: fixed;
  inset: 0;
  z-index: 200;
}

.memora-panel {
  position: absolute;
  top: calc(100% + 8px);
  left: 0;
  z-index: 201;
  width: min(300px, 88vw);
  padding: 12px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.45);
}

.panel-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}
.panel-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--text);
}
.panel-badge {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 99px;
  border: 1px solid var(--border);
  color: var(--muted);
}
.panel-badge--ok { color: var(--success); border-color: rgba(63, 185, 80, 0.35); }
.panel-badge--error { color: var(--danger); border-color: rgba(248, 81, 73, 0.35); }
.panel-badge--paused { color: var(--warning); border-color: rgba(210, 153, 34, 0.35); }
.panel-close {
  margin-left: auto;
  border: none;
  background: transparent;
  color: var(--muted);
  font-size: 16px;
  line-height: 1;
  cursor: pointer;
  padding: 0 2px;
}
.panel-close:hover { color: var(--text); }

.panel-meta {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 0 0 12px;
  font-size: 11px;
}
.meta-row {
  display: grid;
  grid-template-columns: 72px 1fr;
  gap: 6px;
  align-items: start;
}
.meta-row dt { margin: 0; color: var(--muted); }
.meta-row dd { margin: 0; color: var(--text); word-break: break-all; }
.meta-row code {
  font-size: 10px;
  background: var(--bg);
  padding: 1px 4px;
  border-radius: 4px;
}
.meta-row--error dd { color: var(--danger); }
.meta-row--warn dd { color: var(--warning); }
.meta-time {
  display: block;
  margin-top: 2px;
  font-size: 10px;
  color: var(--muted);
}

.panel-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
</style>
