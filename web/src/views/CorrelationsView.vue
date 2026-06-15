<script setup lang="ts">
// CorrelationsView.vue — Auto-route correlation analysis (P7.4).
//
// Visualises the 5 tables returned by
//   GET /api/admin/auto-route/correlations?days=7
// (see admin/auto_route_correlations.go).
//
// The 5 tables are:
//   1. by_model       — per-model success/latency/cost
//   2. by_strategy    — per-strategy success/latency/cost
//   3. by_task_type   — per-task_type success/latency/cost
//   4. by_model_task  — per-(model, task_type) detail
//   5. verdict        — top-3 models per task type, ranked

import { ref, computed, onMounted } from 'vue'
import {
  getAutoRouteCorrelations,
  type AutoRouteCorrelationsResponse,
  type CorrelationRow,
  type CorrelationRowMT,
  type CorrelationVerdict,
} from '../api'

// ── State ────────────────────────────────────────────────────────
const resp = ref<AutoRouteCorrelationsResponse | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
const days = ref(7)
const minSamples = ref(20)

async function load() {
  loading.value = true
  error.value = null
  try {
    resp.value = await getAutoRouteCorrelations({
      days: days.value,
      min_samples: minSamples.value,
    })
  } catch (e: any) {
    error.value = e?.message ?? String(e)
  } finally {
    loading.value = false
  }
}

// ── Helpers ──────────────────────────────────────────────────────
function successColor(rate: number): string {
  if (rate >= 0.95) return '#22c55e'
  if (rate >= 0.85) return '#84cc16'
  if (rate >= 0.7) return '#eab308'
  if (rate >= 0.5) return '#f97316'
  return '#ef4444'
}

function fmtPct(v: number, digits = 1): string {
  return (v * 100).toFixed(digits) + '%'
}

function fmtMs(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

function fmtUsd(v: number): string {
  if (v === 0) return '$0'
  if (v < 0.001) return `$${(v * 1000).toFixed(2)}m`
  return `$${v.toFixed(4)}`
}

// Group verdict by task_type so we can render as nested cards.
const verdictByTask = computed(() => {
  if (!resp.value) return []
  const map = new Map<string, CorrelationVerdict[]>()
  for (const v of resp.value.verdict) {
    if (!map.has(v.task_type)) map.set(v.task_type, [])
    map.get(v.task_type)!.push(v)
  }
  return Array.from(map.entries()).map(([taskType, verdicts]) => ({
    taskType,
    verdicts: verdicts.sort((a, b) => a.rank - b.rank),
  }))
})

// Sort model-task by success desc, then by model+task
const sortedModelTask = computed(() => {
  if (!resp.value) return [] as CorrelationRowMT[]
  return [...resp.value.by_model_task].sort((a, b) => {
    if (a.success_rate !== b.success_rate) return b.success_rate - a.success_rate
    return a.model.localeCompare(b.model) || a.task_type.localeCompare(b.task_type)
  })
})

onMounted(load)
</script>

<template>
  <div class="correlations-view">
    <h1>Auto-Route 关联分析 (Correlations)</h1>
    <p class="subtitle">
      Cross-references the promoted columns on
      <code>request_logs</code> (P7.2) to surface correlations between
      strategy / model / task type. Useful for answering "which model
      should we blacklist on reasoning?" or "is the LLM fallback path
      actually helping?"
    </p>

    <!-- ── Filter bar ────────────────────────────────────── -->
    <section class="card filter-card">
      <div class="filter-bar">
        <label>Window:
          <select v-model.number="days" @change="load">
            <option :value="1">1 day</option>
            <option :value="7">7 days</option>
            <option :value="30">30 days</option>
            <option :value="90">90 days</option>
          </select>
        </label>
        <label>Min samples:
          <select v-model.number="minSamples" @change="load">
            <option :value="5">5</option>
            <option :value="20">20</option>
            <option :value="50">50</option>
            <option :value="100">100</option>
          </select>
        </label>
        <button @click="load" :disabled="loading">
          {{ loading ? 'Loading…' : 'Refresh' }}
        </button>
      </div>
      <p v-if="error" class="error">⚠️ {{ error }}</p>
    </section>

    <template v-if="resp">
      <p class="meta">
        <span>Generated at: {{ resp.generated_at }}</span>
        <span>Window: {{ resp.window_days }} days</span>
        <span>Min samples: {{ minSamples }}</span>
      </p>

      <!-- ── 1. By model ───────────────────────────────────── -->
      <section class="card">
        <h2>By model</h2>
        <p class="hint">Per-model success / latency / cost across all auto requests.</p>
        <table v-if="resp.by_model.length > 0" class="corr-table">
          <thead>
            <tr><th>Model</th><th>Samples</th><th>Success</th><th>Latency</th><th>Cost</th></tr>
          </thead>
          <tbody>
            <tr v-for="r in resp.by_model" :key="r.label">
              <td><span class="tag tag-model">{{ r.label }}</span></td>
              <td>{{ r.samples.toLocaleString() }}</td>
              <td :style="{ color: successColor(r.success_rate), fontWeight: 600 }">
                {{ fmtPct(r.success_rate) }}
              </td>
              <td>{{ fmtMs(r.avg_latency_ms) }}</td>
              <td>{{ fmtUsd(r.avg_cost_usd) }}</td>
            </tr>
          </tbody>
        </table>
        <p v-else class="empty">No data — try lowering min_samples or expanding the window.</p>
      </section>

      <!-- ── 2. By strategy ────────────────────────────────── -->
      <section class="card">
        <h2>By strategy</h2>
        <p class="hint">
          Per-strategy success / latency. Useful for confirming the
          pattern_layered strategy actually outperforms
          baseline_heuristic in production traffic.
        </p>
        <table v-if="resp.by_strategy.length > 0" class="corr-table">
          <thead>
            <tr><th>Strategy</th><th>Samples</th><th>Success</th><th>Latency</th><th>Cost</th></tr>
          </thead>
          <tbody>
            <tr v-for="r in resp.by_strategy" :key="r.label">
              <td><span class="tag tag-strategy">{{ r.label }}</span></td>
              <td>{{ r.samples.toLocaleString() }}</td>
              <td :style="{ color: successColor(r.success_rate), fontWeight: 600 }">
                {{ fmtPct(r.success_rate) }}
              </td>
              <td>{{ fmtMs(r.avg_latency_ms) }}</td>
              <td>{{ fmtUsd(r.avg_cost_usd) }}</td>
            </tr>
          </tbody>
        </table>
        <p v-else class="empty">No data — A/B test may not be enabled or traffic too low.</p>
      </section>

      <!-- ── 3. By task type ──────────────────────────────── -->
      <section class="card">
        <h2>By task type</h2>
        <p class="hint">Per-task_type success / latency / cost.</p>
        <table v-if="resp.by_task_type.length > 0" class="corr-table">
          <thead>
            <tr><th>Task type</th><th>Samples</th><th>Success</th><th>Latency</th><th>Cost</th></tr>
          </thead>
          <tbody>
            <tr v-for="r in resp.by_task_type" :key="r.label">
              <td><span class="tag tag-task">{{ r.label }}</span></td>
              <td>{{ r.samples.toLocaleString() }}</td>
              <td :style="{ color: successColor(r.success_rate), fontWeight: 600 }">
                {{ fmtPct(r.success_rate) }}
              </td>
              <td>{{ fmtMs(r.avg_latency_ms) }}</td>
              <td>{{ fmtUsd(r.avg_cost_usd) }}</td>
            </tr>
          </tbody>
        </table>
        <p v-else class="empty">No data.</p>
      </section>

      <!-- ── 4. By (model, task) — outlier detector ────────── -->
      <section class="card">
        <h2>By (model, task type) — outlier detector</h2>
        <p class="hint">
          A model that performs well on chat but poorly on reasoning
          is a candidate to blacklist via routing overrides. Look for
          rows with success &lt; 70% in a sea of &gt; 95% rows.
        </p>
        <details>
          <summary>{{ sortedModelTask.length }} (model, task_type) pairs</summary>
          <table v-if="sortedModelTask.length > 0" class="corr-table compact">
            <thead>
              <tr><th>Model</th><th>Task type</th><th>Samples</th><th>Success</th><th>Latency</th></tr>
            </thead>
            <tbody>
              <tr v-for="r in sortedModelTask" :key="`${r.model}-${r.task_type}`">
                <td><span class="tag tag-model">{{ r.model }}</span></td>
                <td><span class="tag tag-task">{{ r.task_type }}</span></td>
                <td>{{ r.samples.toLocaleString() }}</td>
                <td :style="{ color: successColor(r.success_rate), fontWeight: 600 }">
                  {{ fmtPct(r.success_rate) }}
                </td>
                <td>{{ fmtMs(r.avg_latency_ms) }}</td>
              </tr>
            </tbody>
          </table>
        </details>
      </section>

      <!-- ── 5. Verdict: top-3 per task type ────────────────── -->
      <section class="card">
        <h2>Top-3 models per task type</h2>
        <p class="hint">
          Ranked by success rate (ties broken by latency). Use this
          when designing routing overrides or weight profiles.
        </p>
        <div v-if="verdictByTask.length > 0" class="verdict-grid">
          <div v-for="group in verdictByTask" :key="group.taskType" class="verdict-card">
            <h3 class="verdict-task-type">{{ group.taskType }}</h3>
            <ol class="verdict-list">
              <li v-for="v in group.verdicts" :key="`${v.task_type}-${v.rank}`"
                  :class="['verdict-item', `rank-${v.rank}`]">
                <span class="verdict-rank">#{{ v.rank }}</span>
                <span class="verdict-model">{{ v.model }}</span>
                <span :style="{ color: successColor(v.success_rate), fontWeight: 600 }">
                  {{ fmtPct(v.success_rate) }}
                </span>
                <span class="verdict-latency">{{ fmtMs(v.avg_latency_ms) }}</span>
              </li>
            </ol>
          </div>
        </div>
        <p v-else class="empty">No data — need at least {{ minSamples }} samples per (task, model) pair.</p>
      </section>
    </template>
  </div>
</template>

<style scoped>
.correlations-view {
  padding: 24px;
  max-width: 1400px;
  margin: 0 auto;
  color: var(--text, #e6e6e6);
}
h1 {
  margin: 0 0 8px;
  font-size: 24px;
}
.subtitle {
  margin: 0 0 24px;
  color: #888;
  font-size: 14px;
}
.card {
  background: var(--card-bg, #1a1a1a);
  border: 1px solid var(--border, #2a2a2a);
  border-radius: 8px;
  padding: 20px;
  margin-bottom: 24px;
}
.card h2 {
  margin: 0 0 8px;
  font-size: 18px;
  border-bottom: 1px solid var(--border, #2a2a2a);
  padding-bottom: 8px;
}
.filter-card { padding: 16px 20px; }
.filter-bar {
  display: flex;
  gap: 16px;
  align-items: center;
  flex-wrap: wrap;
}
.filter-bar label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: #aaa;
}
.filter-bar select,
.filter-bar input {
  padding: 4px 8px;
  background: #0e0e0e;
  border: 1px solid #2a2a2a;
  color: inherit;
  border-radius: 4px;
  font-size: 13px;
}
.filter-bar button {
  padding: 6px 14px;
  background: #2563eb;
  color: #fff;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 13px;
}
.filter-bar button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.error {
  color: #ef4444;
  font-size: 13px;
  margin-top: 8px;
}
.meta {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
  font-size: 12px;
  color: #888;
  margin: 0 0 16px;
}
.hint {
  color: #888;
  font-size: 13px;
  margin: 0 0 12px;
}
.empty {
  color: #888;
  font-size: 13px;
  font-style: italic;
}
.corr-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.corr-table.compact {
  margin-top: 8px;
}
.corr-table th {
  text-align: left;
  padding: 8px 10px;
  background: #0e0e0e;
  border-bottom: 1px solid #2a2a2a;
  color: #aaa;
  font-weight: 500;
}
.corr-table td {
  padding: 8px 10px;
  border-bottom: 1px solid #1f1f1f;
}
details {
  margin-top: 8px;
}
details summary {
  cursor: pointer;
  color: #93c5fd;
  font-size: 13px;
  padding: 4px 0;
}
details summary:hover {
  color: #bfdbfe;
}
.tag {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 11px;
  padding: 2px 6px;
  border-radius: 3px;
  display: inline-block;
}
.tag-model {
  background: #1e293b;
  color: #93c5fd;
}
.tag-strategy {
  background: #422006;
  color: #fb923c;
}
.tag-task {
  background: #14532d;
  color: #86efac;
}
.verdict-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 16px;
}
.verdict-card {
  background: #0e0e0e;
  border: 1px solid #2a2a2a;
  border-radius: 6px;
  padding: 12px 16px;
}
.verdict-task-type {
  margin: 0 0 8px;
  font-size: 13px;
  color: #86efac;
  font-family: 'SF Mono', Menlo, monospace;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.verdict-list {
  list-style: none;
  padding: 0;
  margin: 0;
}
.verdict-item {
  display: grid;
  grid-template-columns: 28px 1fr auto auto;
  gap: 8px;
  align-items: center;
  padding: 6px 0;
  border-bottom: 1px solid #1f1f1f;
  font-size: 13px;
}
.verdict-item:last-child {
  border-bottom: none;
}
.verdict-item.rank-1 {
  background: linear-gradient(90deg, rgba(34,197,94,0.08), transparent);
  padding-left: 8px;
  margin-left: -8px;
  border-radius: 4px;
}
.verdict-rank {
  font-weight: 700;
  color: #93c5fd;
  font-family: 'SF Mono', Menlo, monospace;
}
.verdict-model {
  color: #e6e6e6;
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 12px;
}
.verdict-latency {
  color: #888;
  font-size: 12px;
}
</style>
