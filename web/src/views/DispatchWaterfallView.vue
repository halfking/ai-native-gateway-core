<script setup lang="ts">
/**
 * DispatchWaterfallView — admin page for 9-stage queue waterfall.
 */
import { ref, onMounted, onBeforeUnmount, computed } from 'vue'
import { useRouter } from 'vue-router'
import {
  fetchDispatchWaterfall,
  fetchDispatchQueues,
  type WaterfallSnapshot,
  type WaterfallRequest,
  type DispatchQueuesSnapshot,
} from '../api/dispatch'
import QueueWaterfallTimeline from '../components/QueueWaterfallTimeline.vue'
import DispatchWaterfallToolbar from '../components/DispatchWaterfallToolbar.vue'
import DispatchWaterfallStatus from '../components/DispatchWaterfallStatus.vue'
import DispatchWaterfallDetail from '../components/DispatchWaterfallDetail.vue'

const router = useRouter()
const loading = ref(false)
const error = ref<string | null>(null)
const snap = ref<WaterfallSnapshot | null>(null)
const queues = ref<DispatchQueuesSnapshot | null>(null)
const selected = ref<WaterfallRequest | null>(null)
const selectedId = ref<string | null>(null)

const limit = ref(50)
const modelFilter = ref('')
const credentialFilter = ref('')
const autoRefresh = ref(true)
let timer: number | undefined

const sourceLabel = computed(() => {
  switch (snap.value?.source) {
    case 'memory': return '内存 ring'
    case 'memory+db': return '内存 + DB'
    case 'db': return 'DB 回填'
    case 'none': return '无样本'
    default: return snap.value?.source || '—'
  }
})

function parsedCredentialId(): number | undefined {
  const raw = credentialFilter.value.trim()
  if (!raw) return undefined
  const n = Number(raw)
  return Number.isFinite(n) && n > 0 ? Math.trunc(n) : undefined
}

async function load(opts: { silent?: boolean } = {}) {
  if (!opts.silent) loading.value = true
  error.value = null
  try {
    const [wf, q] = await Promise.all([
      fetchDispatchWaterfall({
        limit: limit.value,
        model: modelFilter.value.trim() || undefined,
        credential_id: parsedCredentialId(),
      }),
      fetchDispatchQueues().catch(() => null),
    ])
    snap.value = wf
    if (q) queues.value = q
    if (selectedId.value) {
      const again = wf.requests.find((r) => r.request_id === selectedId.value)
      selected.value = again ?? selected.value
    }
  } catch (e) {
    error.value = (e as Error).message || String(e)
  } finally {
    if (!opts.silent) loading.value = false
  }
}

function onSelect(r: WaterfallRequest) {
  selected.value = r
  selectedId.value = r.request_id
}

function openSession() {
  const sid = selected.value?.session_id?.trim()
  if (!sid) return
  selected.value = null
  selectedId.value = null
  void router.push(`/admin/sessions/${encodeURIComponent(sid)}`)
}

function openFullscreen() {
  const id = selected.value?.request_id?.trim()
  if (!id) return
  selected.value = null
  selectedId.value = null
  void router.push({ name: 'request-detail', params: { requestId: id }, query: { tab: 'waterfall' } })
}

function startPoll() {
  stopPoll()
  if (!autoRefresh.value) return
  timer = window.setInterval(() => { void load({ silent: true }) }, 5000)
}

function stopPoll() {
  if (timer !== undefined) {
    clearInterval(timer)
    timer = undefined
  }
}

onMounted(async () => {
  await load()
  startPoll()
})

onBeforeUnmount(stopPoll)
</script>

<template>
  <div class="dw-page">
    <header class="dw-header">
      <div>
        <h1>队列瀑布图</h1>
        <p class="sub">相对 T0 的 9 段耗时构成 · 内存 ring 优先，不足时从 request_logs_hot 回填</p>
      </div>
      <DispatchWaterfallToolbar
        v-model:limit="limit"
        v-model:model-filter="modelFilter"
        v-model:credential-filter="credentialFilter"
        v-model:auto-refresh="autoRefresh"
        :loading="loading"
        @load="load()"
        @poll-change="startPoll"
      />
    </header>

    <div v-if="error" class="dw-error">{{ error }}</div>

    <DispatchWaterfallStatus
      :snap="snap"
      :queues="queues"
      :source-label="sourceLabel"
    />

    <QueueWaterfallTimeline
      :requests="snap?.requests || []"
      :loading="loading"
      :wired="snap?.wired"
      :source="snap?.source"
      :selected-id="selectedId"
      @select="onSelect"
    />

    <DispatchWaterfallDetail
      v-if="selected"
      :selected="selected"
      @close="selected = null; selectedId = null"
      @open-session="openSession"
      @open-fullscreen="openFullscreen"
    />
  </div>
</template>

<style scoped>
.dw-page {
  padding: 16px 20px 32px;
  color: var(--kx-text);
  background: var(--kx-bg);
  min-height: 100%;
}
.dw-header {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  align-items: flex-start;
  margin-bottom: 16px;
  flex-wrap: wrap;
}
.dw-header h1 {
  font-size: 20px;
  font-weight: 600;
  margin: 0;
}
.dw-header .sub {
  margin: 4px 0 0;
  color: var(--kx-muted);
  font-size: 13px;
}
.dw-error {
  background: var(--kx-danger-soft);
  color: var(--kx-danger);
  border: 1px solid var(--kx-danger);
  border-radius: 8px;
  padding: 10px 12px;
  margin-bottom: 12px;
  font-size: 13px;
}
</style>
