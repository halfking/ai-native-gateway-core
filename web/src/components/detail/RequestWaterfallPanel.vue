<script setup lang="ts">
// RequestWaterfallPanel — T0–T9 + Attempts using waterfallTimeline SSOT.
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

const props = defineProps<{
  selected: WaterfallRequest | null
  attempts?: WaterfallAttempt[]
  loading?: boolean
  error?: string
  source?: string
}>()

const laid = computed(() => (props.selected ? layoutRows([props.selected]) : null))
const stageRows = computed(() => (props.selected ? stageDetailRows(props.selected) : []))
const heroBars = computed(() => laid.value?.rows[0]?.bars ?? [])
const attemptList = computed(
  () => props.attempts?.length ? props.attempts : (props.selected?.attempts ?? []),
)

const { t } = useI18n()

const { labelRevision } = useCredentialLabels()
function credentialLabel(id: number): string {
  void labelRevision.value
  return credentialDisplayName(id)
}
</script>

<template>
  <div class="rwf">
    <div v-if="loading" class="muted">{{ t('requestDetail.waterfall.loading') }}</div>
    <div v-else-if="error && !selected" class="err">{{ error }}</div>
    <template v-else-if="selected">
      <p v-if="source" class="src">{{ t('requestDetail.waterfall.source', { src: source }) }}</p>
      <section class="hero">
        <div class="hero-axis">
          <span>T0</span>
          <span>{{ formatAxisMs(laid?.axisMax ?? 0) }}</span>
        </div>
        <DispatchWaterfallTrack :bars="heroBars" tall />
      </section>
      <section class="stage-table-wrap">
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
    </template>
    <div v-else class="muted">{{ t('requestDetail.waterfall.empty') }}</div>

    <div v-if="attemptList.length" class="attempts">
      <h4>Attempts ({{ attemptList.length }})</h4>
      <ul>
        <li
          v-for="a in attemptList"
          :key="a.attempt_id || a.attempt_no"
          class="attempt-row"
          :class="statusToneClass(a.outcome, 'attempt')"
        >
          <span>#{{ a.attempt_no }}</span>
          <span>{{ a.model || '—' }}</span>
          <span :title="`credential_id: ${a.credential_id}`">{{ credentialLabel(a.credential_id) }}</span>
          <span class="pill" :class="statusToneClass(a.outcome, 'pill')">{{ a.outcome || '—' }}</span>
          <span v-if="a.error_kind" class="err">{{ a.error_kind }}</span>
        </li>
      </ul>
    </div>
    <p v-else-if="!loading" class="muted">{{ t('requestDetail.waterfall.noAttempts') }}</p>
  </div>
</template>

<style scoped>
.rwf { font-size: 13px; }
.muted { color: var(--muted, var(--kx-muted)); font-size: 12px; }
.err { color: var(--danger, var(--kx-danger)); font-size: 12px; }
.src { font-size: 11px; color: var(--muted); margin: 0 0 8px; }
.hero {
  margin: 0 0 16px; padding: 10px 12px;
  border: 1px solid var(--border, var(--kx-border)); border-radius: 8px;
  background: var(--bg-subtle, var(--kx-bg));
}
.hero-axis {
  display: flex; justify-content: space-between; font-size: 11px;
  color: var(--muted); margin-bottom: 6px; font-variant-numeric: tabular-nums;
}
.stage-table-wrap h4, .attempts h4 { margin: 0 0 8px; font-size: 13px; }
.stage-table {
  width: 100%; border-collapse: collapse; font-size: 12px;
}
.stage-table th, .stage-table td {
  padding: 6px 8px; border-bottom: 1px solid var(--border, var(--kx-border)); text-align: left;
}
.stage-table th.num, .stage-table td.num {
  text-align: right; font-variant-numeric: tabular-nums;
}
.stage-table tr.routing td { color: var(--muted); }
.dot {
  display: inline-block; width: 8px; height: 8px; border-radius: 2px;
}
.tag {
  font-size: 10px; padding: 1px 6px; border-radius: 999px;
  border: 1px solid var(--border); background: var(--bg-card, var(--kx-surface));
}
.tag.muted { color: var(--muted); }
.attempts {
  margin-top: 16px; border-top: 1px dashed var(--border); padding-top: 12px;
}
.attempts ul {
  list-style: none; margin: 0; padding: 0; display: grid; gap: 4px;
}
.attempts li {
  display: grid; grid-template-columns: 40px 1fr 90px 100px auto;
  gap: 10px; font-size: 12px; padding: 4px 6px; border-radius: 4px;
  background: var(--bg-subtle, var(--kx-bg));
  border-left: 3px solid transparent;
}
.attempt--ok { border-left-color: var(--kx-success); }
.attempt--err { border-left-color: var(--kx-error); background: color-mix(in srgb, var(--kx-error) 6%, transparent); }
.attempt--warn { border-left-color: var(--kx-warning); }
.attempt--info { border-left-color: var(--kx-primary); }
/* .pill / .pill--* 全部从全局 styles/pill-chip.css 继承（P1-8）。 */
</style>
