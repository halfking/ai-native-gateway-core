<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RequestTrace, TraceEvent } from '../../api/trace'

const props = defineProps<{ trace: RequestTrace | null }>()
const { t } = useI18n()

const events = computed(() => [...(props.trace?.events ?? [])].sort((a, b) => a.seq - b.seq))

function stageLabel(event: TraceEvent): string {
  const raw = (event.stage_name || event.stage || '').trim()
  const key = `requestDetail.flow.diagram.stages.${raw}`
  const translated = t(key)
  return translated === key ? raw || t('requestDetail.flow.diagram.event') : translated
}

function statusLabel(status: TraceEvent['status']): string {
  const key = `requestDetail.flow.diagram.status.${status}`
  const translated = t(key)
  return translated === key ? status : translated
}

function statusClass(status: TraceEvent['status']): string {
  return `flow-node--${status || 'unknown'}`
}

function durationLabel(event: TraceEvent): string {
  return typeof event.duration_ms === 'number' && Number.isFinite(event.duration_ms) && event.duration_ms > 0
    ? `${event.duration_ms}ms`
    : '—'
}

function detailsText(event: TraceEvent): string {
  const details = event.details || {}
  const flags: string[] = []
  const has = (key: string) => Object.prototype.hasOwnProperty.call(details, key)
  if (has('compression_strategy') || has('compression_applied') || /compress/i.test(event.stage)) flags.push(t('requestDetail.flow.diagram.flags.compression'))
  if (has('retry_reason') || has('retry') || /retry/i.test(event.stage)) flags.push(t('requestDetail.flow.diagram.flags.retry'))
  if (has('switch_reason') || has('to_credential_id') || has('to_model') || /switch|node/i.test(event.stage)) flags.push(t('requestDetail.flow.diagram.flags.nodeSwitch'))
  if (details.observation_status === 'observation_degraded' || has('observation_degraded')) flags.push(t('requestDetail.flow.diagram.flags.degraded'))
  return flags.join(' · ')
}

function eventTitle(event: TraceEvent): string {
  return [event.module, event.error, JSON.stringify(event.details || {})].filter(Boolean).join(' · ')
}
</script>

<template>
  <section class="rpf" data-testid="request-processing-flow">
    <div v-if="!trace" class="rpf-empty">{{ t('requestDetail.flow.diagram.noData') }}</div>
    <div v-else-if="!events.length" class="rpf-empty">{{ t('requestDetail.flow.diagram.noEvents') }}</div>
    <template v-else>
      <div class="rpf-track" role="list" aria-label="请求处理流程">
        <template v-for="(event, index) in events" :key="`${event.seq}-${event.stage}`">
          <span v-if="index" class="rpf-arrow" aria-hidden="true">→</span>
          <article class="flow-node" :class="statusClass(event.status)" role="listitem" :title="eventTitle(event)">
            <div class="flow-node__top">
              <span class="flow-node__seq">{{ event.seq }}</span>
              <strong>{{ stageLabel(event) }}</strong>
            </div>
            <div class="flow-node__meta">
              <span>{{ statusLabel(event.status) }}</span>
              <span>{{ durationLabel(event) }}</span>
            </div>
            <span v-if="detailsText(event)" class="flow-node__flags">{{ detailsText(event) }}</span>
            <span v-if="event.error" class="flow-node__error">{{ event.error }}</span>
          </article>
        </template>
      </div>
      <div class="rpf-legend">
        <span><i class="legend-dot legend-dot--success" />{{ t('requestDetail.flow.diagram.legend.success') }}</span>
        <span><i class="legend-dot legend-dot--failed" />{{ t('requestDetail.flow.diagram.legend.failed') }}</span>
        <span><i class="legend-dot legend-dot--special" />{{ t('requestDetail.flow.diagram.legend.special') }}</span>
      </div>
    </template>
  </section>
</template>

<style scoped>
.rpf { margin: 0 0 12px; padding: 12px; border: 1px solid var(--border); border-radius: 8px; background: var(--bg-subtle, var(--surface-secondary)); }
.rpf-track { display: flex; align-items: stretch; gap: 8px; overflow-x: auto; padding: 2px 2px 8px; }
.rpf-arrow { align-self: center; color: var(--muted); font-size: 18px; flex: 0 0 auto; }
.flow-node { min-width: 128px; max-width: 190px; padding: 8px 9px; border: 1px solid var(--border); border-top: 3px solid var(--muted); border-radius: 7px; background: var(--surface-primary, var(--card)); color: var(--text-primary, var(--text)); }
.flow-node--success { border-top-color: var(--success); }
.flow-node--failed, .flow-node--timeout { border-top-color: var(--danger); background: color-mix(in srgb, var(--danger) 5%, var(--surface-primary, var(--card))); }
.flow-node--skipped { opacity: .65; }
.flow-node__top { display: flex; gap: 6px; align-items: baseline; font-size: 12px; }
.flow-node__seq { color: var(--muted); font-variant-numeric: tabular-nums; }
.flow-node__meta { display: flex; justify-content: space-between; gap: 8px; margin-top: 6px; font-size: 11px; color: var(--muted); }
.flow-node__flags { display: block; margin-top: 6px; color: var(--accent); font-size: 10px; line-height: 1.3; }
.flow-node__error { display: block; margin-top: 5px; color: var(--danger); font-size: 10px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rpf-legend { display: flex; flex-wrap: wrap; gap: 8px 14px; color: var(--muted); font-size: 10px; }
.rpf-legend span { display: inline-flex; align-items: center; gap: 4px; }
.legend-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--muted); }
.legend-dot--success { background: var(--success); } .legend-dot--failed { background: var(--danger); } .legend-dot--special { background: var(--accent); }
.rpf-empty { padding: 16px; color: var(--muted); text-align: center; font-size: 12px; }
@media (max-width: 680px) { .rpf-track { display: grid; grid-template-columns: 1fr; overflow-x: visible; } .rpf-arrow { transform: rotate(90deg); justify-self: center; height: 16px; } .flow-node { max-width: none; } }
</style>
