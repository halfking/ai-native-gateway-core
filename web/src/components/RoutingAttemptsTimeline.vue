<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RoutingAttempt } from '../api'
import type { RequestJourneyEvent } from '../api/request-journeys'

const props = defineProps<{
  summary?: string | null
  attempts?: RoutingAttempt[] | null
  journeyEvents?: RequestJourneyEvent[] | null
}>()

const { t, locale } = useI18n()

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

const orderedJourneyEvents = computed(() =>
  [...(props.journeyEvents ?? [])].sort((a, b) => a.seq - b.seq),
)

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

function eventClass(event: RequestJourneyEvent): string {
  if (event.event_type === 'request_succeeded' || event.event_type === 'attempt_succeeded') return 'success'
  if (event.event_type === 'observation_degraded' || event.event_type === 'retry_scheduled' || event.event_type.endsWith('_switched')) return 'warning'
  if (event.event_type === 'request_failed' || event.event_type === 'attempt_failed') return 'danger'
  if (event.event_type === 'request_canceled') return 'warning'
  return 'active'
}

function eventDetails(event: RequestJourneyEvent): string[] {
  const details: string[] = []
  if (event.attempt) details.push(t('requestJourneys.attempt', { number: event.attempt.attempt_no }))
  const model = event.attempt?.model || event.model || event.resolved_model
  if (model) details.push(t('requestJourneys.detail.model', { model }))
  const provider = event.attempt?.provider || event.provider || event.attempt?.provider_id || event.provider_id
  if (provider) details.push(t('requestJourneys.detail.provider', { provider }))
  const node = event.attempt?.credential_id || event.credential_id
  if (node) details.push(t('requestJourneys.detail.node', { node }))
  if (event.event_type === 'node_switched') {
    details.push(t('requestJourneys.detail.nodeSwitch', {
      from: event.from_credential_id ?? '—',
      to: event.to_credential_id ?? '—',
    }))
  }
  if (event.event_type === 'model_switched') {
    details.push(t('requestJourneys.detail.modelSwitch', {
      from: event.from_model ?? '—',
      to: event.to_model ?? '—',
    }))
  }
  if (event.error_kind) details.push(t('requestJourneys.detail.error', { kind: event.error_kind }))
  if (event.http_status) details.push(t('requestJourneys.detail.http', { status: event.http_status }))
  if (event.retry_reason) details.push(t('requestJourneys.detail.retry', { reason: event.retry_reason }))
  if (event.switch_reason) details.push(t('requestJourneys.detail.switchReason', { reason: event.switch_reason }))
  return details
}

function formatEventTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(locale.value, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    fractionalSecondDigits: 3,
  }).format(date)
}
</script>

<template>
  <section class="routing-attempts-timeline" :aria-label="t('requestJourneys.title')">
    <template v-if="orderedJourneyEvents.length">
      <ol class="journey-event-list">
        <li v-for="event in orderedJourneyEvents" :key="event.seq" class="journey-event">
          <span class="journey-marker" :class="eventClass(event)">{{ event.seq }}</span>
          <div class="journey-event-content">
            <div class="journey-event-header">
              <strong>{{ t(`requestJourneys.event.${event.event_type}`) }}</strong>
              <time :datetime="event.occurred_at">{{ formatEventTime(event.occurred_at) }}</time>
            </div>
            <div class="journey-event-meta">
              <span>{{ t(`requestJourneys.stage.${event.stage}`) }}</span>
              <span v-for="detail in eventDetails(event)" :key="detail">{{ detail }}</span>
            </div>
          </div>
        </li>
      </ol>
    </template>

    <template v-else>
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
    </template>
  </section>
</template>

<style scoped>
.routing-attempts-timeline { min-width: 0; padding: 12px 0; }
.routing-summary, .routing-empty {
  padding: 10px 12px;
  color: var(--text-muted, var(--kx-text-secondary));
  background: var(--bg-elevated, var(--kx-bg-elevated));
  border: 1px solid var(--border, var(--kx-border-light));
  border-radius: 6px;
}
.attempt-list { display: grid; gap: 12px; }
.attempt-item { display: grid; grid-template-columns: 28px minmax(0, 1fr); gap: 10px; position: relative; }
.attempt-item:not(:last-child)::after,
.journey-event:not(:last-child)::after {
  content: ''; position: absolute; left: 13px; top: 28px; bottom: -12px;
  width: 1px; background: var(--border, var(--kx-border-light));
}
.attempt-marker,
.journey-marker {
  z-index: 1; display: grid; place-items: center; width: 26px; height: 26px;
  border-radius: 50%; color: var(--kx-text-inverse); font-size: 11px; font-weight: 600;
}
.attempt-marker.success,
.journey-marker.success { background: var(--success, var(--kx-color-success)); }
.attempt-marker.warning,
.journey-marker.warning { background: var(--warning, var(--kx-color-warning)); }
.attempt-marker.danger,
.journey-marker.danger { background: var(--danger, var(--kx-color-error)); }
.journey-marker.active { background: var(--kx-primary); }
.attempt-card { min-width: 0; padding: 10px 12px; background: var(--bg-container, var(--kx-bg-container)); border: 1px solid var(--border, var(--kx-border-light)); border-radius: 6px; }
.attempt-header { display: flex; justify-content: space-between; gap: 12px; align-items: flex-start; color: var(--text, var(--kx-text-primary)); }
.attempt-result { flex: none; font-size: 12px; }
.attempt-result.success { color: var(--success, var(--kx-color-success)); }
.attempt-result.warning { color: var(--warning, var(--kx-color-warning)); }
.attempt-result.danger, .attempt-error { color: var(--danger, var(--kx-color-error)); }
.attempt-meta { display: flex; flex-wrap: wrap; gap: 6px 14px; margin-top: 6px; color: var(--text-muted, var(--kx-text-secondary)); font-size: 12px; }
.attempt-error { margin-top: 6px; font-size: 12px; word-break: break-word; }
.attempt-url { display: block; margin-top: 8px; padding-top: 8px; border-top: 1px solid var(--border, var(--kx-border-light)); color: var(--text-muted, var(--kx-text-secondary)); font-size: 11px; overflow-wrap: anywhere; }
.journey-event-list { display: grid; gap: 12px; margin: 0; padding: 0; list-style: none; }
.journey-event { position: relative; display: grid; grid-template-columns: 28px minmax(0, 1fr); gap: 10px; }
.journey-event-content { min-width: 0; padding: 3px 0 9px; border-bottom: 1px solid var(--kx-border); }
.journey-event-header { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; color: var(--kx-text); font-size: 12px; }
.journey-event-header time { flex: 0 0 auto; color: var(--kx-text-secondary); font-size: 11px; font-variant-numeric: tabular-nums; }
.journey-event-meta { display: flex; flex-wrap: wrap; gap: 4px 12px; margin-top: 4px; color: var(--kx-text-secondary); font-size: 11px; overflow-wrap: anywhere; }
@media (max-width: 640px) {
  .attempt-header,
  .journey-event-header { align-items: flex-start; flex-direction: column; gap: 4px; }
  .journey-event-header time { white-space: normal; }
}
</style>
