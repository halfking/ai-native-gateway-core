<script setup lang="ts">
import type { RoutingAttempt } from '../api'

const props = defineProps<{
  summary?: string | null
  attempts?: RoutingAttempt[] | null
}>()

const resultLabels: Record<string, string> = {
  success: '成功',
  canceled: '取消',
  timeout: '超时',
  model_not_found: '模型未找到',
  rate_limit: '速率限制',
  unauthorized: '未授权',
  concurrent: '并发超限',
  empty_response: '空响应',
  stream_interrupted: '流中断',
  error: '错误',
}

function formatResult(result: string): string {
  return resultLabels[result] || result.replace(/_/g, ' ')
}

function formatLatency(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

function resultClass(result: string): string {
  if (result === 'success') return 'success'
  if (result === 'canceled' || result === 'rate_limit') return 'warning'
  return 'danger'
}
</script>

<template>
  <section class="routing-attempts-timeline" aria-label="路由尝试">
    <div v-if="props.summary" class="routing-summary">{{ props.summary }}</div>
    <div v-if="props.attempts?.length" class="attempt-list">
      <article v-for="attempt in props.attempts" :key="attempt.seq" class="attempt-item">
        <div class="attempt-marker" :class="resultClass(attempt.result)">{{ attempt.seq }}</div>
        <div class="attempt-card">
          <div class="attempt-header">
            <strong>{{ attempt.provider_name || `供应商 ${attempt.provider_id}` }}</strong>
            <span class="attempt-result" :class="resultClass(attempt.result)">
              {{ formatResult(attempt.result) }} · {{ formatLatency(attempt.latency_ms) }}
            </span>
          </div>
          <div class="attempt-meta">
            <span>模型: {{ attempt.raw_model || '—' }}</span>
            <span>凭据: {{ attempt.credential_id || '—' }}</span>
            <span v-if="attempt.http_status">HTTP {{ attempt.http_status }}</span>
          </div>
          <div v-if="attempt.error_message" class="attempt-error">{{ attempt.error_message }}</div>
          <code class="attempt-url">{{ attempt.upstream_url || '—' }}</code>
        </div>
      </article>
    </div>
    <div v-else class="routing-empty">暂无路由回退尝试记录</div>
  </section>
</template>

<style scoped>
.routing-attempts-timeline { padding: 12px 0; }
.routing-summary, .routing-empty {
  padding: 10px 12px;
  color: var(--text-muted, var(--kx-text-secondary));
  background: var(--bg-elevated, var(--kx-bg-elevated));
  border: 1px solid var(--border, var(--kx-border-light));
  border-radius: 6px;
}
.attempt-list { display: grid; gap: 12px; }
.attempt-item { display: grid; grid-template-columns: 28px minmax(0, 1fr); gap: 10px; position: relative; }
.attempt-item:not(:last-child)::after {
  content: ''; position: absolute; left: 13px; top: 28px; bottom: -12px;
  width: 1px; background: var(--border, var(--kx-border-light));
}
.attempt-marker {
  z-index: 1; display: grid; place-items: center; width: 26px; height: 26px;
  border-radius: 50%; color: var(--kx-text-inverse, #fff); font-size: 12px; font-weight: 600;
}
.attempt-marker.success { background: var(--success, var(--kx-color-success)); }
.attempt-marker.warning { background: var(--warning, var(--kx-color-warning)); }
.attempt-marker.danger { background: var(--danger, var(--kx-color-error)); }
.attempt-card { min-width: 0; padding: 10px 12px; background: var(--bg-container, var(--kx-bg-container)); border: 1px solid var(--border, var(--kx-border-light)); border-radius: 6px; }
.attempt-header { display: flex; justify-content: space-between; gap: 12px; align-items: flex-start; color: var(--text, var(--kx-text-primary)); }
.attempt-result { flex: none; font-size: 12px; }
.attempt-result.success { color: var(--success, var(--kx-color-success)); }
.attempt-result.warning { color: var(--warning, var(--kx-color-warning)); }
.attempt-result.danger, .attempt-error { color: var(--danger, var(--kx-color-error)); }
.attempt-meta { display: flex; flex-wrap: wrap; gap: 6px 14px; margin-top: 6px; color: var(--text-muted, var(--kx-text-secondary)); font-size: 12px; }
.attempt-error { margin-top: 6px; font-size: 12px; word-break: break-word; }
.attempt-url { display: block; margin-top: 8px; padding-top: 8px; border-top: 1px solid var(--border, var(--kx-border-light)); color: var(--text-muted, var(--kx-text-secondary)); font-size: 11px; overflow-wrap: anywhere; }
@media (max-width: 640px) { .attempt-header { flex-direction: column; gap: 4px; } }
</style>
