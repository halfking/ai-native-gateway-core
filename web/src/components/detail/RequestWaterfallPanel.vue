<script setup lang="ts">
// RequestWaterfallPanel — T0–T9 + Attempts using waterfallTimeline SSOT.
import { computed } from 'vue'
import type { WaterfallAttempt, WaterfallRequest } from '../../api/dispatch'
import {
  formatAxisMs,
  layoutRows,
  stageDetailRows,
} from '../../utils/waterfallTimeline'
import { credentialDisplayName, useCredentialLabels } from '../../composables/useCredentialLabels'
import DispatchWaterfallTrack from '../DispatchWaterfallTrack.vue'

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

const { labelRevision } = useCredentialLabels()
function credentialLabel(id: number): string {
  void labelRevision.value
  return credentialDisplayName(id)
}
</script>

<template>
  <div class="rwf">
    <div v-if="loading" class="muted">加载调度瀑布…</div>
    <div v-else-if="error && !selected" class="err">{{ error }}</div>
    <template v-else-if="selected">
      <p v-if="source" class="src">数据源：{{ source }}</p>
      <section class="hero">
        <div class="hero-axis">
          <span>T0</span>
          <span>{{ formatAxisMs(laid?.axisMax ?? 0) }}</span>
        </div>
        <DispatchWaterfallTrack :bars="heroBars" tall />
      </section>
      <section class="stage-table-wrap">
        <h4>T0–T9 阶段</h4>
        <table class="stage-table">
          <thead>
            <tr>
              <th scope="col" />
              <th scope="col">阶段</th>
              <th scope="col" class="num">耗时</th>
              <th scope="col">来源</th>
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
                <span v-else-if="row.synthesized" class="tag">合成</span>
                <span v-else class="tag muted">实测</span>
              </td>
            </tr>
          </tbody>
        </table>
      </section>
    </template>
    <div v-else class="muted">暂无瀑布时间线（请求可能尚未进入调度环或 DB 无 T0）。</div>

    <div v-if="attemptList.length" class="attempts">
      <h4>Attempts ({{ attemptList.length }})</h4>
      <ul>
        <li v-for="a in attemptList" :key="a.attempt_id || a.attempt_no">
          <span>#{{ a.attempt_no }}</span>
          <span>{{ a.model || '—' }}</span>
          <span :title="`credential_id: ${a.credential_id}`">{{ credentialLabel(a.credential_id) }}</span>
          <span>{{ a.outcome || '—' }}</span>
          <span v-if="a.error_kind" class="err">{{ a.error_kind }}</span>
        </li>
      </ul>
    </div>
    <p v-else-if="!loading" class="muted">无 Attempts 记录。</p>
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
}
</style>
