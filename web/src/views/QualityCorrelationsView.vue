<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getQualityCorrelations,
  type QualityCorrelationResponse,
} from '../api'

const { t } = useI18n()

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
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

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

function byLabel(b: string): string {
  const key = `qualityCorrelations.filter.by.${b}` as 'qualityCorrelations.filter.by.prompt_length'
  if (b === 'prompt_length' || b === 'tools' || b === 'images' || b === 'code_block') {
    return t(key)
  }
  return b
}

const totalSamples = computed(() => {
  if (!resp.value) return 0
  return resp.value.breakdown.reduce((acc, r) => acc + r.samples, 0)
})

onMounted(load)
</script>

<template>
  <div class="qc-view">
    <h1>{{ t('qualityCorrelations.title') }}</h1>
    <p class="subtitle">{{ t('qualityCorrelations.subtitle') }}</p>

    <section class="card filter-card">
      <div class="filter-bar">
        <label>{{ t('qualityCorrelations.filter.window') }}:
          <select v-model.number="days" @change="load">
            <option :value="1">{{ t('qualityCorrelations.filter.days.d1') }}</option>
            <option :value="7">{{ t('qualityCorrelations.filter.days.d7') }}</option>
            <option :value="30">{{ t('qualityCorrelations.filter.days.d30') }}</option>
            <option :value="90">{{ t('qualityCorrelations.filter.days.d90') }}</option>
          </select>
        </label>
        <label>{{ t('qualityCorrelations.filter.bucketBy') }}:
          <select v-model="by" @change="load">
            <option value="prompt_length">{{ t('qualityCorrelations.filter.by.prompt_length') }}</option>
            <option value="tools">{{ t('qualityCorrelations.filter.by.tools') }}</option>
            <option value="images">{{ t('qualityCorrelations.filter.by.images') }}</option>
            <option value="code_block">{{ t('qualityCorrelations.filter.by.code_block') }}</option>
          </select>
        </label>
        <button @click="load" :disabled="loading">
          {{ loading ? t('qualityCorrelations.filter.loading') : t('qualityCorrelations.filter.refresh') }}
        </button>
      </div>
      <p v-if="error" class="error">⚠️ {{ error }}</p>
    </section>

    <template v-if="resp">
      <p class="meta">
        <span>{{ t('qualityCorrelations.meta.generated') }}: {{ resp.generated_at }}</span>
        <span>{{ t('qualityCorrelations.meta.window') }}: {{ t('qualityCorrelations.meta.windowDays', { n: resp.window_days }) }}</span>
        <span>{{ t('qualityCorrelations.meta.totalSamples') }}: {{ totalSamples.toLocaleString() }}</span>
        <span v-if="totalSamples < 30" class="warn">⚠️ {{ t('qualityCorrelations.meta.needSamples') }}</span>
      </p>

      <section class="card">
        <h2>{{ t('qualityCorrelations.breakdown.title', { by: byLabel(by) }) }}</h2>
        <table v-if="resp.breakdown.length > 0" class="qc-table">
          <thead>
            <tr>
              <th>{{ t('qualityCorrelations.breakdown.headers.bucket') }}</th>
              <th>{{ t('qualityCorrelations.breakdown.headers.samples') }}</th>
              <th>{{ t('qualityCorrelations.breakdown.headers.success') }}</th>
              <th>{{ t('qualityCorrelations.breakdown.headers.latency') }}</th>
              <th>{{ t('qualityCorrelations.breakdown.headers.quality') }}</th>
              <th>{{ t('qualityCorrelations.breakdown.headers.cost') }}</th>
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
        <p v-else class="empty">{{ t('qualityCorrelations.breakdown.empty') }}</p>
      </section>

      <section class="card insights-card">
        <h2>{{ t('qualityCorrelations.insights.title') }}</h2>
        <p class="hint">{{ t('qualityCorrelations.insights.hint') }}</p>

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
                {{ t('qualityCorrelations.insights.buckets', { n: ins.buckets }) }}
                · {{ t('qualityCorrelations.insights.samples', { n: ins.samples }) }}
              </div>
            </div>
          </li>
        </ol>
        <p v-else class="empty">
          <span v-if="totalSamples < 30">{{ t('qualityCorrelations.insights.emptyInsufficient') }}</span>
          <span v-else>{{ t('qualityCorrelations.insights.emptyUnexpected') }}</span>
        </p>
      </section>
    </template>
  </div>
</template>

<style scoped>
.qc-view {
  padding: 24px;
  max-width: 1200px;
  margin: 0 auto;
  color: var(--text, #e6e6e6);
}
h1 { margin: 0 0 8px; font-size: 24px; }
h2 {
  margin: 0 0 12px;
  font-size: 18px;
  border-bottom: 1px solid var(--border, #2a2a2a);
  padding-bottom: 8px;
}
.subtitle { margin: 0 0 24px; color: #888; font-size: 14px; }
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
.error { color: #ef4444; font-size: 13px; margin-top: 8px; }
.meta {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
  font-size: 12px;
  color: #888;
  margin: 0 0 16px;
}
.meta .warn { color: #eab308; }
.empty { color: #888; font-size: 13px; font-style: italic; }
.hint { color: #888; font-size: 13px; margin: 0 0 12px; }
.qc-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.qc-table th {
  text-align: left;
  padding: 8px 10px;
  background: #0e0e0e;
  border-bottom: 1px solid #2a2a2a;
  color: #aaa;
  font-weight: 500;
}
.qc-table td { padding: 8px 10px; border-bottom: 1px solid #1f1f1f; }
.tag {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 11px;
  padding: 2px 8px;
  background: #1e293b;
  color: #93c5fd;
  border-radius: 3px;
}
.insights-list { list-style: none; padding: 0; margin: 0; }
.insight-item {
  display: grid;
  grid-template-columns: 48px 1fr;
  gap: 12px;
  align-items: center;
  padding: 12px 0;
  border-bottom: 1px solid #1f1f1f;
}
.insight-item:last-child { border-bottom: none; }
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
.insight-interpretation { font-size: 13px; color: #ccc; margin-bottom: 2px; }
.insight-meta { font-size: 11px; color: #666; }
</style>
