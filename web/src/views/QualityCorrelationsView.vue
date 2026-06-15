<script setup lang="ts">
// QualityCorrelationsView.vue — P8.2: request-feature × outcome analytics.
//
// Where CorrelationsView surfaces MODEL-level breakdowns
// (model × task, strategy × task), this view surfaces REQUEST-level
// correlations: which request features (prompt length, tools, images,
// code block) predict the outcome (success / quality).
//
// The "insights" section at the bottom ranks predictors by Pearson
// correlation with avg_quality — useful for finding "what's making
// our requests fail" without manually cross-tabbing.

import { ref, computed, onMounted } from 'vue'
import {
  getQualityCorrelations,
  type QualityCorrelationRow,
  type QualityCorrelationInsight,
  type QualityCorrelationResponse,
} from '../api'

// ── State ───────────────────────────────────────────────────────
const resp = ref<QualityCorrelationResponse | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
const days = ref(7)
const by = ref<'prompt_length' | 'tools' | 'images' | 'code_block'>('prompt_length')

async function load() {
  loading.value = true
  error.value = null
  try {
    resp.value = await getQualityCorrelations({
      days: days.value,
      by: by.value,
    })
  } catch (e: any) {
    error.value = e?.message ?? String(e)
  } finally {
    loading.value = false
  }
}

// ── Helpers ─────────────────────────────────────────────────────
function qualityColor(q: number): string {
  if (q >= 0.85) return '#22c55e'
  if (q >= 0.7) return '#84cc16'
  if (q >= 0.55) return '#eab308'
  if (q >= 0.4) return '#f97316'
  return '#ef4444'
}

function correlationColor(r: number): string {
  const abs = Math.abs(r)
  if (abs >= 0.7) return r > 0 ? '#22c55e' : '#ef4444'
  if (abs >= 0.4) return r > 0 ? '#84cc16' : '#f97316'
  if (abs >= 0.2) return '#eab308'
  return '#888'
}

function fmtPct(v: number, digits = 1): string {
  return (v * 100).toFixed(digits) + '%'
}

function fmtMs(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

const totalSamples = computed(() => {
  if (!resp.value) return 0
  return resp.value.breakdown.reduce((acc, r) => acc + r.samples, 0)
})

onMounted(load)
</script>

<template>
  <div class="qc-view">
    <h1>Quality Correlations</h1>
    <p class="subtitle">
      Cross-references request features (prompt length, tool count,
      image count, code block presence) with outcome (success,
      latency, quality). Useful for finding "what's making our
      requests fail" without manually cross-tabbing.
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
        <label>Bucket by:
          <select v-model="by" @change="load">
            <option value="prompt_length">Prompt length</option>
            <option value="tools">Tool count</option>
            <option value="images">Has image</option>
            <option value="code_block">Has code block</option>
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
        <span>Generated: {{ resp.generated_at }}</span>
        <span>Window: {{ resp.window_days }} days</span>
        <span>Total samples: {{ totalSamples.toLocaleString() }}</span>
        <span v-if="totalSamples < 30" class="warn">⚠️ Need ≥ 30 samples for insights</span>
      </p>

      <!-- ── Breakdown table ─────────────────────────────────── -->
      <section class="card">
        <h2>Breakdown by {{ byLabel(by) }}</h2>
        <table v-if="resp.breakdown.length > 0" class="qc-table">
          <thead>
            <tr>
              <th>Bucket</th>
              <th>Samples</th>
              <th>Success</th>
              <th>Latency</th>
              <th>Quality</th>
              <th>Cost</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in resp.breakdown" :key="r.bucket">
              <td><span class="tag tag-bucket">{{ r.bucket }}</span></td>
              <td>{{ r.samples.toLocaleString() }}</td>
              <td :style="{ color: qualityColor(r.success_rate), fontWeight: 600 }">
                {{ fmtPct(r.success_rate) }}
              </td>
              <td>{{ fmtMs(r.avg_latency_ms) }}</td>
              <td :style="{ color: qualityColor(r.avg_quality), fontWeight: 600 }">
                {{ fmtPct(r.avg_quality) }}
              </td>
              <td>${{ r.avg_cost_usd.toFixed(4) }}</td>
            </tr>
          </tbody>
        </table>
        <p v-else class="empty">No data — try lowering the days or expanding the filter.</p>
      </section>

      <!-- ── Insights panel ───────────────────────────────────── -->
      <section class="card insights-card">
        <h2>What predicts quality? (Pearson correlation)</h2>
        <p class="hint">
          Each predictor is correlated with avg_quality across
          buckets. Stronger |r| means the feature is a better
          predictor of quality. Useful for asking: "if we focus
          on one improvement, which one moves the needle most?"
        </p>

        <ol v-if="resp.insights.length > 0" class="insights-list">
          <li v-for="(ins, i) in resp.insights" :key="ins.predictor" class="insight-item">
            <div class="insight-rank">#{{ i + 1 }}</div>
            <div class="insight-content">
              <div class="insight-header">
                <span class="insight-predictor">{{ ins.predictor }}</span>
                <span class="insight-correlation"
                      :style="{ background: correlationColor(ins.correlation) }">
                  r = {{ ins.correlation.toFixed(3) }}
                </span>
              </div>
              <div class="insight-interpretation">{{ ins.interpretation }}</div>
              <div class="insight-meta">
                {{ ins.buckets }} buckets · {{ ins.samples.toLocaleString() }} samples
              </div>
            </div>
          </li>
        </ol>
        <p v-else class="empty">
          <span v-if="totalSamples < 30">Need at least 30 samples to compute meaningful correlations.</span>
          <span v-else>No insights (unexpected). Check server logs.</span>
        </p>
      </section>
    </template>
  </div>
</template>

<script lang="ts">
// Helper for the template — expose byLabel as a method.
export default {
  methods: {
    byLabel(b: string): string {
      switch (b) {
        case 'prompt_length': return 'prompt length'
        case 'tools':         return 'tool count'
        case 'images':        return 'has image'
        case 'code_block':    return 'code block presence'
        default: return b
      }
    },
  },
}
</script>

<style scoped>
.qc-view {
  padding: 24px;
  max-width: 1200px;
  margin: 0 auto;
  color: var(--text, #e6e6e6);
}
h1 {
  margin: 0 0 8px;
  font-size: 24px;
}
h2 {
  margin: 0 0 12px;
  font-size: 18px;
  border-bottom: 1px solid var(--border, #2a2a2a);
  padding-bottom: 8px;
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
  margin-bottom: 16px;
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
.filter-bar select {
  padding: 4px 8px;
  background: #0e0e0e;
  border: 1px solid #2a2a2a;
  color: inherit;
  border-radius: 4px;
  font-size: 13px;
  min-width: 140px;
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
.filter-bar button:disabled { opacity: 0.5; cursor: not-allowed; }
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
.meta .warn { color: #eab308; }
.empty {
  color: #888;
  font-size: 13px;
  font-style: italic;
}
.hint {
  color: #888;
  font-size: 13px;
  margin: 0 0 12px;
}
.qc-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.qc-table th {
  text-align: left;
  padding: 8px 10px;
  background: #0e0e0e;
  border-bottom: 1px solid #2a2a2a;
  color: #aaa;
  font-weight: 500;
}
.qc-table td {
  padding: 8px 10px;
  border-bottom: 1px solid #1f1f1f;
}
.tag {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 11px;
  padding: 2px 8px;
  background: #1e293b;
  color: #93c5fd;
  border-radius: 3px;
}
.insights-list {
  list-style: none;
  padding: 0;
  margin: 0;
}
.insight-item {
  display: grid;
  grid-template-columns: 48px 1fr;
  gap: 12px;
  align-items: center;
  padding: 12px 0;
  border-bottom: 1px solid #1f1f1f;
}
.insight-item:last-child {
  border-bottom: none;
}
.insight-rank {
  font-size: 20px;
  font-weight: 700;
  color: #93c5fd;
  text-align: center;
  font-family: 'SF Mono', Menlo, monospace;
}
.insight-header {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 4px;
}
.insight-predictor {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 14px;
  color: #e6e6e6;
  font-weight: 600;
}
.insight-correlation {
  padding: 3px 10px;
  border-radius: 4px;
  color: #fff;
  font-size: 12px;
  font-weight: 600;
  font-family: 'SF Mono', Menlo, monospace;
}
.insight-interpretation {
  font-size: 13px;
  color: #ccc;
  margin-bottom: 2px;
}
.insight-meta {
  font-size: 11px;
  color: #666;
}
</style>
