<script setup lang="ts">
// WaterfallRequestDetailContent — shared selected-request waterfall body used
// by the dispatch drawer and the inline request-detail waterfall tab.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { WaterfallAttempt, WaterfallRequest } from '../../api/dispatch'
import {
  formatAxisMs,
  layoutRows,
  stageDetailRows,
} from '../../utils/waterfallTimeline'
import { credentialDisplayName, useCredentialLabels } from '../../composables/useCredentialLabels'
import DispatchWaterfallTrack from '../DispatchWaterfallTrack.vue'
import { statusToneClass } from './statusTone'

const props = withDefaults(defineProps<{
  request: WaterfallRequest
  /** Used only when a durable waterfall row no longer has its ring attempts. */
  fallbackAttempts?: WaterfallAttempt[]
  showRequestSummary?: boolean
}>(), {
  fallbackAttempts: undefined,
  showRequestSummary: false,
})

const { t } = useI18n()
const laid = computed(() => layoutRows([props.request]))
const stageRows = computed(() => stageDetailRows(props.request))
const heroBars = computed(() => laid.value.rows[0]?.bars ?? [])
const attemptList = computed(() =>
  props.request.attempts !== undefined ? props.request.attempts : (props.fallbackAttempts ?? []),
)

const { labelRevision } = useCredentialLabels()
function credentialLabel(id: number): string {
  void labelRevision.value
  return credentialDisplayName(id)
}
</script>

<template>
  <article class="waterfall-request-detail" data-testid="waterfall-request-detail">
    <p
      v-if="showRequestSummary"
      class="request-summary"
      data-testid="waterfall-request-summary"
    >
      <code>{{ request.request_id }}</code>
      · {{ request.model || '—' }}
      · {{ request.result || '—' }}
    </p>

    <section class="hero" data-testid="waterfall-hero">
      <div class="hero-axis">
        <span>T0</span>
        <span>{{ formatAxisMs(laid.axisMax) }}</span>
      </div>
      <DispatchWaterfallTrack :bars="heroBars" tall />
    </section>

    <section class="stage-table-wrap" data-testid="waterfall-stage-table">
      <h4>{{ t('requestDetail.waterfall.stageHeader') }}</h4>
      <table class="stage-table">
        <thead>
          <tr>
            <th scope="col" />
            <th scope="col">{{ t('requestDetail.waterfall.cols.stage') }}</th>
            <th scope="col" class="num">{{ t('requestDetail.waterfall.cols.duration') }}</th>
            <th scope="col">{{ t('requestDetail.waterfall.cols.source') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="row in stageRows"
            :key="row.key"
            :class="{ routing: !row.showBar }"
          >
            <td>
              <i v-if="row.showBar" class="dot" :style="{ background: row.color }" />
            </td>
            <td>{{ row.label }}</td>
            <td class="num">{{ row.ms == null ? '—' : `${row.ms} ms` }}</td>
            <td>
              <span v-if="!row.showBar">—</span>
              <span v-else-if="row.synthesized" class="tag">{{ t('requestDetail.waterfall.syntheticTag') }}</span>
              <span v-else class="tag muted">{{ t('requestDetail.waterfall.measuredTag') }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </section>

    <section v-if="attemptList.length" class="attempts" data-testid="waterfall-attempts">
      <h4>Attempts ({{ attemptList.length }})</h4>
      <ul>
        <li
          v-for="attempt in attemptList"
          :key="attempt.attempt_id || attempt.attempt_no"
          class="attempt-row"
          :class="statusToneClass(attempt.outcome, 'attempt')"
        >
          <span>#{{ attempt.attempt_no }}</span>
          <span>{{ attempt.model || '—' }}</span>
          <span :title="`credential_id: ${attempt.credential_id}`">{{ credentialLabel(attempt.credential_id) }}</span>
          <span class="pill" :class="statusToneClass(attempt.outcome, 'pill')">{{ attempt.outcome || '—' }}</span>
          <span v-if="attempt.error_kind" class="err">{{ attempt.error_kind }}</span>
        </li>
      </ul>
    </section>
    <p v-else class="muted no-attempts">{{ t('requestDetail.waterfall.noAttempts') }}</p>
  </article>
</template>

<style scoped>
.waterfall-request-detail { font-size: 13px; }
.request-summary {
  margin: 0 0 8px;
  font-size: 11px;
  color: var(--muted, var(--kx-muted));
}
.request-summary code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 11px;
  word-break: break-all;
}
.hero {
  margin: 0 0 16px;
  padding: 10px 12px;
  border: 1px solid var(--border, var(--kx-border));
  border-radius: 8px;
  background: var(--bg-subtle, var(--kx-bg));
}
.hero-axis {
  display: flex;
  justify-content: space-between;
  margin-bottom: 6px;
  font-size: 11px;
  color: var(--muted, var(--kx-muted));
  font-variant-numeric: tabular-nums;
}
.stage-table-wrap h4,
.attempts h4 {
  margin: 0 0 8px;
  font-size: 13px;
}
.stage-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}
.stage-table th,
.stage-table td {
  padding: 6px 8px;
  border-bottom: 1px solid var(--border, var(--kx-border));
  text-align: left;
}
.stage-table th.num,
.stage-table td.num {
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.stage-table tr.routing td { color: var(--muted, var(--kx-muted)); }
.dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 2px;
}
.tag {
  font-size: 10px;
  padding: 1px 6px;
  border: 1px solid var(--border, var(--kx-border));
  border-radius: 999px;
  background: var(--bg-card, var(--kx-surface));
}
.tag.muted,
.muted { color: var(--muted, var(--kx-muted)); }
.attempts {
  margin-top: 16px;
  padding-top: 12px;
  border-top: 1px dashed var(--border, var(--kx-border));
}
.attempts ul {
  display: grid;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.attempt-row {
  display: grid;
  grid-template-columns: 40px minmax(0, 1fr) 90px 100px auto;
  gap: 10px;
  padding: 4px 6px;
  border-left: 3px solid transparent;
  border-radius: 4px;
  background: var(--bg-subtle, var(--kx-bg));
  font-size: 12px;
}
.attempt--ok { border-left-color: var(--kx-success); }
.attempt--err {
  border-left-color: var(--kx-danger);
  background: color-mix(in srgb, var(--kx-danger) 6%, transparent);
}
.attempt--warn { border-left-color: var(--kx-warning); }
.attempt--info { border-left-color: var(--kx-primary); }
.err { color: var(--danger, var(--kx-danger)); }
.no-attempts { margin: 12px 0 0; font-size: 12px; }
</style>
