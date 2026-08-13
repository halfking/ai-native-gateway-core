<script setup lang="ts">
/**
 * DispatchWaterfallView — admin page for 9-stage queue waterfall.
 */
import { ref, onMounted, onBeforeUnmount, computed } from 'vue'
import {
  fetchDispatchWaterfall,
  fetchDispatchQueues,
  type WaterfallSnapshot,
  type WaterfallRequest,
  type DispatchQueuesSnapshot,
} from '../api/dispatch'
import QueueWaterfallTimeline from '../components/QueueWaterfallTimeline.vue'

const loading = ref(false)
const error = ref<string | null>(null)
const snap = ref<WaterfallSnapshot | null>(null)
const queues = ref<DispatchQueuesSnapshot | null>(null)
const selected = ref<WaterfallRequest | null>(null)

const limit = ref(50)
const modelFilter = ref('')
const autoRefresh = ref(true)
let timer: number | undefined

const diagnosis = computed(() => snap.value?.bottleneck_diagnosis)
const diagnosisTone = computed(() => {
  const b = diagnosis.value?.bottleneck
  if (!b || b === 'none') return 'ok'
  if (b === 'routing') return 'warn'
  return 'danger'
})

async function load() {
  loading.value = true
  error.value = null
  try {
    const [wf, q] = await Promise.all([
      fetchDispatchWaterfall({
        limit: limit.value,
        model: modelFilter.value.trim() || undefined,
      }),
      fetchDispatchQueues().catch(() => null),
    ])
    snap.value = wf
    if (q) queues.value = q
  } catch (e) {
    error.value = (e as Error).message || String(e)
  } finally {
    loading.value = false
  }
}

function onSelect(r: WaterfallRequest) {
  selected.value = r
}

function startPoll() {
  stopPoll()
  if (!autoRefresh.value) return
  timer = window.setInterval(() => { void load() }, 5000)
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
        <p class="sub">9 阶段调度时间线（T0–T9）· 最近完成请求内存样本</p>
      </div>
      <div class="dw-actions">
        <label class="field">
          <span>Limit</span>
          <select v-model.number="limit" @change="load">
            <option :value="20">20</option>
            <option :value="50">50</option>
            <option :value="100">100</option>
            <option :value="200">200</option>
          </select>
        </label>
        <label class="field">
          <span>Model</span>
          <input v-model="modelFilter" placeholder="可选筛选" @keyup.enter="load" />
        </label>
        <label class="check">
          <input v-model="autoRefresh" type="checkbox" @change="startPoll" />
          自动刷新 5s
        </label>
        <button class="btn" :disabled="loading" @click="load">刷新</button>
      </div>
    </header>

    <div v-if="error" class="dw-error">{{ error }}</div>

    <section class="dw-status">
      <div class="card">
        <div class="k">Pipeline</div>
        <div class="v">
          <span :class="['pill', snap?.wired ? 'ok' : 'muted']">{{ snap?.wired ? 'wired' : 'not wired' }}</span>
          <span :class="['pill', snap?.enabled ? 'ok' : 'muted']">{{ snap?.enabled ? 'enabled' : 'disabled' }}</span>
        </div>
      </div>
      <div class="card">
        <div class="k">样本数</div>
        <div class="v num">{{ snap?.requests?.length ?? 0 }}</div>
      </div>
      <div class="card">
        <div class="k">模型队列</div>
        <div class="v num">{{ queues?.models?.reduce((s, m) => s + (m.depth || 0), 0) ?? '—' }}</div>
      </div>
      <div class="card">
        <div class="k">凭据队列</div>
        <div class="v num">{{ queues?.credentials?.reduce((s, c) => s + (c.depth || 0), 0) ?? '—' }}</div>
      </div>
      <div class="card grow" :class="'tone-' + diagnosisTone">
        <div class="k">瓶颈诊断</div>
        <div class="v diag">
          <strong>{{ diagnosis?.bottleneck || 'none' }}</strong>
          <span>{{ diagnosis?.message || '—' }}</span>
          <em v-if="diagnosis?.suggestion">{{ diagnosis?.suggestion }}</em>
        </div>
      </div>
    </section>

    <QueueWaterfallTimeline
      :requests="snap?.requests || []"
      :loading="loading"
      @select="onSelect"
    />

    <aside v-if="selected" class="dw-detail">
      <header>
        <h3>请求详情</h3>
        <button class="btn ghost" @click="selected = null">关闭</button>
      </header>
      <dl>
        <div><dt>request_id</dt><dd><code>{{ selected.request_id }}</code></dd></div>
        <div><dt>model</dt><dd>{{ selected.model || '—' }}</dd></div>
        <div><dt>credential</dt><dd>{{ selected.credential_id ?? '—' }}</dd></div>
        <div><dt>result</dt><dd>{{ selected.result }}</dd></div>
        <div><dt>queue wait</dt><dd>{{ selected.queue_wait_ms }} ms</dd></div>
        <div><dt>total queue</dt><dd>{{ selected.waiting_in_total_ms }} ms</dd></div>
        <div><dt>model queue</dt><dd>{{ selected.waiting_in_model_ms }} ms</dd></div>
        <div><dt>cred queue</dt><dd>{{ selected.waiting_in_node_ms }} ms</dd></div>
        <div><dt>routing</dt><dd>{{ selected.routing_ms }} ms</dd></div>
        <div><dt>upstream TTFB</dt><dd>{{ selected.upstream_latency_ms }} ms</dd></div>
        <div><dt>streaming</dt><dd>{{ selected.streaming_duration_ms }} ms</dd></div>
        <div><dt>total</dt><dd>{{ selected.total_ms }} ms</dd></div>
      </dl>
    </aside>
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
.dw-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  align-items: center;
}
.field {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--kx-muted);
}
.field input, .field select {
  border: 1px solid var(--kx-border);
  background: var(--kx-surface);
  color: var(--kx-text);
  border-radius: 6px;
  padding: 4px 8px;
  min-width: 100px;
}
.check {
  display: inline-flex;
  gap: 6px;
  align-items: center;
  font-size: 12px;
  color: var(--kx-muted);
}
.btn {
  border: 1px solid var(--kx-primary);
  background: var(--kx-primary);
  color: #fff;
  border-radius: 6px;
  padding: 6px 12px;
  cursor: pointer;
  font-size: 13px;
}
.btn:disabled { opacity: 0.6; cursor: not-allowed; }
.btn.ghost {
  background: transparent;
  color: var(--kx-primary);
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
.dw-status {
  display: grid;
  grid-template-columns: repeat(4, minmax(100px, 1fr)) 2fr;
  gap: 10px;
  margin-bottom: 14px;
}
@media (max-width: 1100px) {
  .dw-status { grid-template-columns: repeat(2, 1fr); }
}
.card {
  background: var(--kx-surface);
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  padding: 10px 12px;
}
.card .k {
  font-size: 11px;
  color: var(--kx-muted);
  margin-bottom: 6px;
}
.card .v { font-size: 14px; }
.card .num { font-size: 20px; font-weight: 600; }
.card .diag {
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 13px;
}
.card .diag em {
  font-style: normal;
  color: var(--kx-muted);
  font-size: 12px;
}
.card.tone-ok { border-color: color-mix(in srgb, var(--kx-success) 40%, var(--kx-border)); }
.card.tone-warn { border-color: color-mix(in srgb, var(--kx-warning) 50%, var(--kx-border)); }
.card.tone-danger { border-color: color-mix(in srgb, var(--kx-danger) 50%, var(--kx-border)); }
.pill {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 11px;
  margin-right: 6px;
  background: var(--kx-bg);
  color: var(--kx-muted);
  border: 1px solid var(--kx-border);
}
.pill.ok {
  color: var(--kx-success);
  border-color: color-mix(in srgb, var(--kx-success) 40%, var(--kx-border));
  background: var(--kx-success-soft);
}
.dw-detail {
  margin-top: 14px;
  background: var(--kx-surface);
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  padding: 12px 14px;
}
.dw-detail header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 10px;
}
.dw-detail h3 { margin: 0; font-size: 14px; }
.dw-detail dl {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 8px 16px;
  margin: 0;
}
.dw-detail dt {
  font-size: 11px;
  color: var(--kx-muted);
}
.dw-detail dd {
  margin: 2px 0 0;
  font-size: 13px;
}
.dw-detail code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
</style>
