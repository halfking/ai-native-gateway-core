<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RequestTrace, TraceEvent } from '../../api/trace'
import type { RequestJourney } from '../../api/request-journeys'
import type { WaterfallRequest } from '../../api/dispatch'

const props = defineProps<{
  trace: RequestTrace | null
  journey?: RequestJourney | null
  waterfall?: WaterfallRequest | null
}>()
const { t } = useI18n()

const events = computed(() => [...(props.trace?.events ?? [])].sort((a, b) => a.seq - b.seq))
const journeyEvents = computed(() => [...(props.journey?.events ?? [])].sort((a, b) => a.seq - b.seq))
const waterfallAttempts = computed(() => props.waterfall?.attempts ?? [])

function journeyDetail(event: (typeof journeyEvents.value)[number]): string {
  const bits = [
    event.attempt ? `attempt #${event.attempt.attempt_no}` : '',
    event.provider || event.attempt?.provider || '',
    event.model || event.resolved_model || '',
    event.retry_reason || event.switch_reason || '',
  ].filter(Boolean)
  return bits.join(' · ')
}

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

// Canonical stage set — keep in sync with domains/requestjourney/contract.go
// JourneyStage constants (and trace.internal/trace.Stage where the event was
// lifted from trace.events). These strings appear in event.stage / event.stage_name;
// the locale key under requestDetail.flow.diagram.stages must cover each one.
const COMPRESSION_STAGES = new Set(['compression'])
const RETRY_STAGES = new Set(['retrying'])
const NODE_SWITCH_STAGES = new Set(['node_selection'])

function detailsText(event: TraceEvent): string {
  const details = event.details || {}
  const stage = event.stage || ''
  const stageName = event.stage_name || ''
  const flags: string[] = []
  const has = (key: string) => Object.prototype.hasOwnProperty.call(details, key)
  if (has('compression_strategy') || has('compression_applied') || COMPRESSION_STAGES.has(stage) || COMPRESSION_STAGES.has(stageName)) {
    flags.push(t('requestDetail.flow.diagram.flags.compression'))
  }
  if (has('retry_reason') || has('retry') || RETRY_STAGES.has(stage) || RETRY_STAGES.has(stageName)) {
    flags.push(t('requestDetail.flow.diagram.flags.retry'))
  }
  if (has('switch_reason') || has('to_credential_id') || has('to_model') || NODE_SWITCH_STAGES.has(stage) || NODE_SWITCH_STAGES.has(stageName)) {
    flags.push(t('requestDetail.flow.diagram.flags.nodeSwitch'))
  }
  if (details.observation_status === 'observation_degraded' || has('observation_degraded')) {
    flags.push(t('requestDetail.flow.diagram.flags.degraded'))
  }
  return flags.join(' · ')
}

function eventTitle(event: TraceEvent): string {
  return [event.module, event.error, JSON.stringify(event.details || {})].filter(Boolean).join(' · ')
}
</script>

<template>
  <section class="rpf" data-testid="request-processing-flow" :aria-label="t('requestDetail.flow.diagram.ariaLabel')">
    <div v-if="!trace && !journey && !waterfall" class="rpf-empty">{{ t('requestDetail.flow.diagram.noData') }}</div>
    <div v-else-if="!events.length && !journeyEvents.length && !waterfallAttempts.length" class="rpf-empty">{{ t('requestDetail.flow.diagram.noEvents') }}</div>
    <template v-else>
      <div class="rpf-track" role="list" :aria-label="t('requestDetail.flow.diagram.trackLabel')">
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
      <section v-if="journeyEvents.length || waterfallAttempts.length" class="rpf-evidence" :aria-label="t('requestDetail.flow.diagram.evidenceLabel')">
        <h4>{{ t('requestDetail.flow.diagram.evidenceTitle') }}</h4>
        <ul v-if="journeyEvents.length" class="evidence-list">
          <li v-for="event in journeyEvents" :key="`journey-${event.seq}`" class="evidence-item">
            <strong>{{ t(`requestJourneys.event.${event.event_type}`) }}</strong>
            <span>{{ journeyDetail(event) || t('requestDetail.flow.diagram.noDetails') }}</span>
            <span v-if="event.observation_status === 'observation_degraded'" class="degraded">{{ t('requestDetail.flow.diagram.degraded') }}</span>
          </li>
        </ul>
        <ul v-if="waterfallAttempts.length" class="evidence-list">
          <li v-for="attempt in waterfallAttempts" :key="`waterfall-${attempt.attempt_id || attempt.attempt_no}`" class="evidence-item">
            <strong>{{ t('requestDetail.flow.diagram.waterfallAttempt', { number: attempt.attempt_no }) }}</strong>
            <span>{{ [attempt.model, attempt.vendor, attempt.outcome, attempt.error_kind].filter(Boolean).join(' · ') || t('requestDetail.flow.diagram.noDetails') }}</span>
          </li>
        </ul>
      </section>
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
.rpf-evidence { margin-top: 12px; padding-top: 10px; border-top: 1px solid var(--border); }
.rpf-evidence h4 { margin: 0 0 6px; font-size: 12px; }
.evidence-list { display: grid; gap: 5px; list-style: none; margin: 0; padding: 0; }
.evidence-item { display: flex; flex-wrap: wrap; gap: 6px; font-size: 11px; color: var(--muted); }
.evidence-item strong { color: var(--text-primary, var(--text)); }
.degraded { color: var(--warning, var(--accent)); }
.rpf-legend { display: flex; flex-wrap: wrap; gap: 8px 14px; color: var(--muted); font-size: 10px; }
.rpf-legend span { display: inline-flex; align-items: center; gap: 4px; }
.legend-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--muted); }
.legend-dot--success { background: var(--success); } .legend-dot--failed { background: var(--danger); } .legend-dot--special { background: var(--accent); }
.rpf-empty { padding: 16px; color: var(--muted); text-align: center; font-size: 12px; }
@media (max-width: 680px) { .rpf-track { display: grid; grid-template-columns: 1fr; overflow-x: visible; } .rpf-arrow { transform: rotate(90deg); justify-self: center; height: 16px; } .flow-node { max-width: none; } }
</style>
