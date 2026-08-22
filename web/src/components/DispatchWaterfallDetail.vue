<script setup lang="ts">
import { computed } from 'vue'
import type { WaterfallRequest } from '../api/dispatch'
import {
  formatAxisMs,
  layoutRows,
  stageDetailRows,
} from '../utils/waterfallTimeline'
import DispatchWaterfallTrack from './DispatchWaterfallTrack.vue'

const props = defineProps<{
  selected: WaterfallRequest
}>()

const emit = defineEmits<{
  close: []
  'open-session': []
}>()

const laid = computed(() => layoutRows([props.selected]))
const stageRows = computed(() => stageDetailRows(props.selected))
const heroBars = computed(() => laid.value.rows[0]?.bars ?? [])
</script>

<template>
  <div class="drawer-backdrop" @click="emit('close')">
    <div class="drawer-panel card drawer-panel-wide dwf-detail" @click.stop>
      <div class="drawer-header">
        <div>
          <h3 style="margin: 0">请求详情</h3>
          <p class="sub">
            <code>{{ selected.request_id }}</code>
            · {{ selected.model || '—' }}
            · {{ selected.result }}
          </p>
        </div>
        <div class="actions">
          <button
            v-if="selected.session_id"
            class="btn btn-sm"
            type="button"
            @click="emit('open-session')"
          >打开会话</button>
          <button class="btn btn-sm" type="button" @click="emit('close')">关闭</button>
        </div>
      </div>

      <section class="hero">
        <div class="hero-axis">
          <span>T0</span>
          <span>{{ formatAxisMs(laid.axisMax) }}</span>
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
              <td class="num">{{ row.ms }} ms</td>
              <td>
                <span v-if="!row.showBar">—</span>
                <span v-else-if="row.synthesized" class="tag">合成</span>
                <span v-else class="tag muted">实测</span>
              </td>
            </tr>
          </tbody>
        </table>
      </section>

      <div v-if="selected.attempts?.length" class="attempts">
        <h4>Attempts ({{ selected.attempts.length }})</h4>
        <ul>
          <li v-for="a in selected.attempts" :key="a.attempt_id || a.attempt_no">
            <span>#{{ a.attempt_no }}</span>
            <span>{{ a.model || '—' }}</span>
            <span>cred {{ a.credential_id }}</span>
            <span>{{ a.outcome || '—' }}</span>
            <span v-if="a.error_kind" class="err">{{ a.error_kind }}</span>
          </li>
        </ul>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dwf-detail .sub {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--kx-muted);
}
.dwf-detail code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 11px;
}
.actions { display: flex; gap: 8px; }
.hero {
  margin: 12px 0 16px;
  padding: 10px 12px;
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  background: var(--kx-bg);
}
.hero-axis {
  display: flex;
  justify-content: space-between;
  font-size: 11px;
  color: var(--kx-muted);
  margin-bottom: 6px;
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
  border-bottom: 1px solid var(--kx-border);
  text-align: left;
}
.stage-table th.num,
.stage-table td.num {
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.stage-table tr.routing td { color: var(--kx-muted); }
.dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 2px;
}
.tag {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 999px;
  border: 1px solid var(--kx-border);
  background: var(--kx-surface);
}
.tag.muted { color: var(--kx-muted); }
.attempts {
  margin-top: 16px;
  border-top: 1px dashed var(--kx-border);
  padding-top: 12px;
}
.attempts ul {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: 4px;
}
.attempts li {
  display: grid;
  grid-template-columns: 40px 1fr 90px 100px auto;
  gap: 10px;
  font-size: 12px;
  padding: 4px 6px;
  border-radius: 4px;
  background: var(--kx-bg);
}
.attempts .err { color: var(--kx-danger); }
</style>
