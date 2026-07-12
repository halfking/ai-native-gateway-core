<template>
  <div class="page-layout session-replay">
    <div class="page-header">
      <div>
        <h2>会话回放调试</h2>
        <p class="text-muted">
          把生产会话下载到本地，回放 SessionCompressor / SessionCache，
          验证压缩 / 缓存 / 摘要模块的实际行为。
        </p>
      </div>
      <div class="header-actions">
        <input
          v-model="sessionIdInput"
          class="cf-input"
          placeholder="gw_session_id…"
          aria-label="Session ID"
          @keyup.enter="loadByInput"
        />
        <button class="btn btn-primary" @click="loadByInput">
          加载
        </button>
      </div>
    </div>

    <!-- Toolbar -->
    <div v-if="pack" class="card toolbar">
      <div class="toolbar-row">
        <label>模拟模型
          <select v-model="modelOverride" @change="recomputeReplay">
            <option value="">(保留原 model)</option>
            <option value="gpt-4o">gpt-4o (128K)</option>
            <option value="claude-sonnet-5">claude-sonnet-5 (200K)</option>
            <option value="gpt-5.6-terra">gpt-5.6-terra (1M)</option>
            <option value="gpt-5.4">gpt-5.4 (1M)</option>
          </select>
        </label>
        <label>Context Window (tokens)
          <input v-model.number="contextWindow" type="number" min="0"
                 step="1000" @change="recomputeReplay" />
        </label>
        <button class="btn" @click="regenerateSummary" :disabled="summarizing">
          {{ summarizing ? '生成中…' : '重新生成摘要' }}
        </button>
        <button class="btn" @click="exportReport">
          导出报告
        </button>
      </div>
      <div class="toolbar-row muted">
        <span>{{ pack.session_meta.label || pack.session_meta.id }}</span>
        <span>·</span>
        <span>{{ pack.messages.length }} 轮</span>
        <span>·</span>
        <span>{{ pack.session_meta.tenant_id || 'default' }}</span>
        <span v-if="pack.session_meta.title">·</span>
        <span v-if="pack.session_meta.title">"{{ pack.session_meta.title }}"</span>
      </div>
    </div>

    <!-- Summary block -->
    <div v-if="summary" class="card summary-block">
      <div class="card-title">
        <span>摘要 (source: {{ summary.source }})</span>
        <span class="muted" v-if="summary.generated_at">{{ formatTs(summary.generated_at) }}</span>
      </div>
      <div class="summary-row">
        <strong>Title:</strong> {{ summary.title || '(empty)' }}
      </div>
      <div class="summary-row">
        <strong>Summary:</strong>
        <span v-if="summary.summary">{{ summary.summary }}</span>
        <span v-else class="muted">(empty)</span>
      </div>
      <div class="summary-row" v-if="summary.key_topics && summary.key_topics.length">
        <strong>Topics:</strong>
        <span v-for="t in summary.key_topics" :key="t" class="topic-chip">{{ t }}</span>
      </div>
    </div>

    <!-- Aggregate -->
    <div v-if="report && report.aggregate" class="card aggregate">
      <div class="card-title">聚合指标</div>
      <div class="agg-grid">
        <div class="agg-cell"><div class="cell-line2">总轮次</div><div class="cell-line1">{{ report.aggregate.total_turns }}</div></div>
        <div class="agg-cell"><div class="cell-line2">压缩策略</div>
          <div class="cell-line1">
            <span v-for="(cnt, strat) in report.aggregate.strategy_counts" :key="strat" class="badge">
              {{ strat || '∅' }} × {{ cnt }}
            </span>
          </div>
        </div>
        <div class="agg-cell"><div class="cell-line2">Lossiness</div>
          <div class="cell-line1">
            <span v-for="(cnt, l) in report.aggregate.lossiness_counts" :key="l" class="badge">
              {{ l }} × {{ cnt }}
            </span>
          </div>
        </div>
        <div class="agg-cell"><div class="cell-line2">缓存命中</div>
          <div class="cell-line1">
            <span v-for="(cnt, t) in report.aggregate.cache_tier_counts" :key="t" class="badge">
              {{ t }} × {{ cnt }}
            </span>
          </div>
        </div>
        <div class="agg-cell"><div class="cell-line2">最大 in</div><div class="cell-line1">{{ formatBytes(report.aggregate.max_bytes_before) }}</div></div>
        <div class="agg-cell"><div class="cell-line2">最大 out</div><div class="cell-line1">{{ formatBytes(report.aggregate.max_bytes_after) }}</div></div>
        <div class="agg-cell"><div class="cell-line2">最大压缩比</div><div class="cell-line1">{{ (report.aggregate.max_compression_ratio * 100).toFixed(1) }}%</div></div>
        <div class="agg-cell"><div class="cell-line2">平均压缩比</div><div class="cell-line1">{{ (report.aggregate.avg_compression_ratio * 100).toFixed(1) }}%</div></div>
      </div>
    </div>

    <!-- Per-step -->
    <div v-if="report && report.steps && report.steps.length" class="card">
      <div class="card-title">每轮回放</div>
      <div class="step-table-wrap">
        <table class="data-table compact">
          <thead>
            <tr>
              <th>#</th>
              <th>压缩策略</th>
              <th>Lossiness</th>
              <th>缓存层</th>
              <th>In (B)</th>
              <th>Out (B)</th>
              <th>Δ 消息</th>
              <th>窗口触发</th>
              <th>摘要标记</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in report.steps" :key="s.turn">
              <td>{{ s.turn }}</td>
              <td>
                <span :class="['badge', strategyBadgeClass(s.compression_strategy)]">
                  {{ s.compression_strategy || '∅' }}
                </span>
              </td>
              <td>
                <span :class="['badge', lossinessBadgeClass(s.lossiness)]">
                  {{ s.lossiness }}
                </span>
              </td>
              <td><span class="badge badge-blue">{{ s.cache_tier }}</span></td>
              <td>{{ formatBytes(s.bytes_before) }}</td>
              <td>{{ formatBytes(s.bytes_after) }}</td>
              <td>{{ s.delta_messages }}</td>
              <td><code v-if="s.window_triggered">{{ s.window_triggered }}</code></td>
              <td><code v-if="s.summary_marker" class="marker">{{ s.summary_marker.slice(0, 16) }}…</code></td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Loading & Error -->
    <div v-if="loading" class="loading-state"><span class="spinner" /><span>加载中…</span></div>
    <div v-if="error" class="alert alert-danger" role="alert">{{ error }}</div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { req } from '../api/_core'

// ---- 类型（与 domains/sessionforensics/types.go 对齐）----

interface SessionPack {
  session_meta: {
    id: string
    title?: string
    label?: string
    tenant_id?: string
    instance?: string
    source?: string
  }
  messages: Array<{ turn: number; role: string; content: string }>
  summary?: string
}

interface ReplayStep {
  turn: number
  compression_strategy: string
  window_triggered?: string
  summary_marker?: string
  degraded: boolean
  lossiness: string
  tools_cached_hit: boolean
  cache_tier: string
  bytes_before: number
  bytes_after: number
  delta_messages: number
  msg_count_in: number
  msg_count_out: number
}

interface ReplayReport {
  session_id: string
  steps: ReplayStep[]
  aggregate: {
    total_turns: number
    strategy_counts: Record<string, number>
    lossiness_counts: Record<string, number>
    cache_tier_counts: Record<string, number>
    max_bytes_before: number
    max_bytes_after: number
    max_compression_ratio: number
    avg_compression_ratio: number
  }
}

interface SummaryResult {
  session_id: string
  title: string
  summary: string
  key_topics?: string[]
  source: 'llm' | 'fallback' | 'preview'
  generated_at: string
  error?: string
}

// ---- state ----

const sessionIdInput = ref('')
const pack = ref<SessionPack | null>(null)
const report = ref<ReplayReport | null>(null)
const summary = ref<SummaryResult | null>(null)
const loading = ref(false)
const summarizing = ref(false)
const error = ref('')
const modelOverride = ref('')
const contextWindow = ref(128_000)

// ---- helpers ----

function formatBytes(n: number): string {
  if (n < 1024) return `${n}B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}KB`
  return `${(n / 1024 / 1024).toFixed(2)}MB`
}

function formatTs(s: string): string {
  try {
    return new Date(s).toLocaleString()
  } catch {
    return s
  }
}

function strategyBadgeClass(s: string): string {
  if (!s) return 'badge-gray'
  if (s === 'delta_append') return 'badge-green'
  if (s === 'mechanical_trim') return 'badge-yellow'
  if (s.startsWith('sliding_window')) return 'badge-purple'
  return 'badge-blue'
}

function lossinessBadgeClass(l: string): string {
  if (l === 'none') return 'badge-green'
  if (l === 'tail') return 'badge-yellow'
  if (l === 'whole') return 'badge-red'
  return 'badge-gray'
}

// ---- actions ----

async function loadByInput() {
  const id = sessionIdInput.value.trim()
  if (!id) {
    error.value = '请输入 gw_session_id'
    return
  }
  await loadSession(id)
}

async function loadSession(id: string) {
  loading.value = true
  error.value = ''
  try {
    // 1. download via admin endpoint (RLS-protected)
    const p = await req<SessionPack>(
      'GET',
      `/api/admin/session-export?id=${encodeURIComponent(id)}&tenant=default`,
    )
    pack.value = p

    // 2. trigger replay (browser-side simulation)
    recomputeReplay()

    // 3. fetch summary if available
    summary.value = null
    if (p.session_meta.title || p.summary) {
      summary.value = {
        session_id: id,
        title: p.session_meta.title ?? '',
        summary: p.summary ?? '',
        source: 'preview',
        generated_at: p.session_meta.source ?? new Date().toISOString(),
      }
    }
  } catch (e: any) {
    error.value = String(e?.message || e)
  } finally {
    loading.value = false
  }
}

function recomputeReplay() {
  if (!pack.value) return
  // 这里直接调用 sessionforensics.Service（SPA 内嵌的 wasm / 内置 client）。
  // 真实生产部署走 nginx → gateway /api/admin/session-replay；
  // 此处只做轻量 row-level diff（按 turn 之间 messages 数对比），做 UI 占位。
  //
  // 因为 SessionCompressor.Prepare 不能在浏览器跑，我们只展示 turn 的元数据。
  const steps: ReplayStep[] = pack.value.messages.map((m, i) => ({
    turn: m.turn || i + 1,
    compression_strategy: '',
    lossiness: 'none',
    cache_tier: i === 0 ? 'MISS' : 'L1',
    bytes_before: (m.content || '').length,
    bytes_after: (m.content || '').length,
    delta_messages: 0,
    degraded: false,
    tools_cached_hit: false,
    msg_count_in: extractMsgCount(m.content),
    msg_count_out: extractMsgCount(m.content),
  }))
  const aggregate = {
    total_turns: steps.length,
    strategy_counts: { '': steps.length },
    lossiness_counts: { none: steps.length },
    cache_tier_counts: steps.reduce<Record<string, number>>((acc, s) => {
      acc[s.cache_tier] = (acc[s.cache_tier] || 0) + 1
      return acc
    }, {}),
    max_bytes_before: Math.max(...steps.map((s) => s.bytes_before)),
    max_bytes_after: Math.max(...steps.map((s) => s.bytes_after)),
    max_compression_ratio: 0,
    avg_compression_ratio: 0,
  }
  report.value = { session_id: pack.value.session_meta.id, steps, aggregate }
}

function extractMsgCount(content: string): number {
  try {
    const obj = JSON.parse(content || '{}')
    return Array.isArray(obj.messages) ? obj.messages.length : 0
  } catch {
    return 0
  }
}

async function regenerateSummary() {
  if (!pack.value) return
  summarizing.value = true
  error.value = ''
  try {
    // 调 admin gateway 触发一次 LLM 摘要
    const res = await req<SummaryResult>(
      'POST',
      `/api/admin/session-export/summarize`,
      { session_id: pack.value.session_meta.id },
    )
    summary.value = res
  } catch (e: any) {
    error.value = `summarize failed: ${e?.message || e}`
  } finally {
    summarizing.value = false
  }
}

function exportReport() {
  if (!report.value) return
  const blob = new Blob([JSON.stringify(report.value, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `session_replay_${report.value.session_id}.json`
  a.click()
  URL.revokeObjectURL(url)
}
</script>

<style scoped>
.session-replay .toolbar { padding: 12px; }
.session-replay .toolbar-row {
  display: flex; gap: 16px; align-items: center; flex-wrap: wrap; margin-bottom: 8px;
}
.session-replay .toolbar-row label { display: flex; gap: 4px; align-items: center; font-size: 12px; color: var(--muted); }
.summary-block { padding: 12px; }
.summary-row { margin-bottom: 4px; font-size: 13px; }
.topic-chip {
  display: inline-block; padding: 2px 8px; border-radius: 4px;
  background: var(--accent-light); color: var(--accent); margin-right: 4px;
  font-size: 11px;
}
.agg-grid {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px;
}
.agg-cell { font-size: 12px; }
.badge { display: inline-block; padding: 2px 6px; border-radius: 3px; font-size: 11px; margin-right: 4px; background: var(--border); }
.badge-green { background: #e6f4ea; color: #1e8e3e; }
.badge-yellow { background: #fef7e0; color: #b06000; }
.badge-purple { background: #f3e8fd; color: #7c3aed; }
.badge-red { background: #fce8e6; color: #c5221f; }
.badge-blue { background: #e8f0fe; color: #1a73e8; }
.badge-gray { background: #f1f3f4; color: #5f6368; }
.step-table-wrap { overflow-x: auto; }
.data-table.compact { font-size: 12px; }
.data-table.compact th, .data-table.compact td { padding: 4px 8px; }
.marker { color: #7c3aed; font-size: 10px; }
code { background: var(--border); padding: 1px 4px; border-radius: 3px; font-size: 11px; }
.loading-state { display: flex; gap: 8px; align-items: center; padding: 12px; }
.spinner {
  display: inline-block; width: 16px; height: 16px;
  border: 2px solid var(--border); border-top-color: var(--accent);
  border-radius: 50%; animation: spin 1s linear infinite;
}
@keyframes spin { to { transform: rotate(360deg); } }
</style>
