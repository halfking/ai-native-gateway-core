<script setup lang="ts">
// AnalyticsSummaryCards — 核心指标卡片网格 (2026-11-20)
import type { AnalyticsSummary } from '../../api/board'

interface Props {
  summary: AnalyticsSummary
}

const props = defineProps<Props>()

function formatNumber(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return n.toLocaleString('en-US')
}

function formatCost(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return '$' + n.toFixed(2)
}

function formatPct(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return (n * 100).toFixed(1) + '%'
}

function formatLatency(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return n.toFixed(0) + 'ms'
}
</script>

<template>
  <div class="summary-cards">
    <div class="summary-card">
      <div class="card-label">{{ $t('analytics.summary.totalRequests') }}</div>
      <!-- ★ data-testid：★ 探针（maestro/tools/assert-analytics-tenant.mjs）要
           拿「页面显示的请求数」与「页面自己收到的响应」逐字对拍 ⇒ ★ 必须有
           **唯一锚点**：★ 扫全页 innerText 的旧写法在 total_requests=0 时
           恒真（★ 任何页面文本都含 "0"）⇒ ★ 判据形同虚设。 -->
      <div class="card-value" data-testid="analytics-total-requests">{{ formatNumber(summary.total_requests) }}</div>
      <div class="card-sub">
        <span class="success">{{ formatNumber(summary.success_count) }} {{ $t('analytics.summary.success') }}</span>
        <span class="error">{{ formatNumber(summary.error_count) }} {{ $t('analytics.summary.errors') }}</span>
      </div>
    </div>

    <div class="summary-card">
      <div class="card-label">{{ $t('analytics.summary.successRate') }}</div>
      <div class="card-value" :class="{ 'value-success': summary.success_rate >= 0.99, 'value-warning': summary.success_rate < 0.95 }">
        {{ formatPct(summary.success_rate) }}
      </div>
      <div class="card-sub">
        {{ formatNumber(summary.success_count) }} / {{ formatNumber(summary.total_requests) }}
      </div>
    </div>

    <div class="summary-card">
      <div class="card-label">{{ $t('analytics.summary.totalTokens') }}</div>
      <div class="card-value">{{ formatNumber(summary.total_tokens) }}</div>
      <div v-if="summary.total_prompt_tokens != null && summary.total_completion_tokens != null" class="card-sub">
        <span>{{ $t('analytics.summary.prompt') }}: {{ formatNumber(summary.total_prompt_tokens) }}</span>
        <span>{{ $t('analytics.summary.completion') }}: {{ formatNumber(summary.total_completion_tokens) }}</span>
      </div>
      <div v-else class="card-sub">
        {{ $t('analytics.summary.avgPerRequest', { value: formatNumber(summary.total_requests > 0 ? summary.total_tokens / summary.total_requests : 0) }) }}
      </div>
    </div>

    <div class="summary-card">
      <div class="card-label">{{ $t('analytics.summary.totalCost') }}</div>
      <div class="card-value">{{ formatCost(summary.total_cost_usd) }}</div>
      <div class="card-sub">
        {{ $t('analytics.summary.avgPerRequest', { value: formatCost(summary.total_requests > 0 ? summary.total_cost_usd / summary.total_requests : 0) }) }}
      </div>
    </div>

    <div class="summary-card">
      <div class="card-label">{{ $t('analytics.summary.totalCredits') }}</div>
      <div class="card-value">{{ formatNumber(summary.total_credits) }}</div>
      <div class="card-sub">
        {{ $t('analytics.summary.avgPerRequest', { value: formatNumber(summary.total_requests > 0 ? summary.total_credits / summary.total_requests : 0) }) }}
      </div>
    </div>

    <div class="summary-card">
      <div class="card-label">{{ $t('analytics.summary.avgLatency') }}</div>
      <div class="card-value">{{ formatLatency(summary.avg_latency_ms) }}</div>
      <div v-if="summary.p95_latency_ms != null || summary.p99_latency_ms != null" class="card-sub">
        <span v-if="summary.p95_latency_ms != null">P95: {{ formatLatency(summary.p95_latency_ms) }}</span>
        <span v-if="summary.p99_latency_ms != null">P99: {{ formatLatency(summary.p99_latency_ms) }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.summary-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: var(--space-3);
}

.summary-card {
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: var(--space-3);
  transition: box-shadow 0.15s, border-color 0.15s;
}

.summary-card:hover {
  border-color: var(--primary);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
}

.card-label {
  font-size: var(--text-sm);
  color: var(--text-muted);
  margin-bottom: var(--space-2);
  font-weight: var(--font-medium);
}

.card-value {
  font-size: var(--text-2xl);
  font-weight: var(--font-semibold);
  color: var(--text);
  font-variant-numeric: tabular-nums;
  margin-bottom: var(--space-1);
}

.value-success {
  color: var(--success);
}

.value-warning {
  color: var(--warning);
}

.card-sub {
  font-size: var(--text-xs);
  color: var(--text-muted);
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}

.card-sub span {
  display: block;
}

.success {
  color: var(--success);
}

.error {
  color: var(--danger);
}

@media (max-width: 768px) {
  .summary-cards {
    grid-template-columns: 1fr;
  }
}
</style>
