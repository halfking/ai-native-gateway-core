<script setup lang="ts">
import {
  EXEC_STAGE_KEYS,
  QUEUE_STAGE_KEYS,
  WATERFALL_STAGES,
} from '../utils/waterfallTimeline'

const queueStages = WATERFALL_STAGES.filter((s) => QUEUE_STAGE_KEYS.includes(s.key))
const execStages = WATERFALL_STAGES.filter((s) => EXEC_STAGE_KEYS.includes(s.key))
</script>

<template>
  <div class="dwf-legend" data-testid="qwt-legend">
    <div class="dwf-legend-row">
      <span class="dwf-legend-k">排队</span>
      <span v-for="st in queueStages" :key="st.key" class="chip">
        <i :style="{ background: st.color }" />{{ st.label }}
      </span>
    </div>
    <div class="dwf-legend-row">
      <span class="dwf-legend-k">执行</span>
      <span v-for="st in execStages" :key="st.key" class="chip">
        <i :style="{ background: st.color }" />{{ st.label }}
      </span>
    </div>
  </div>
</template>

<style scoped>
.dwf-legend {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 6px;
}
.dwf-legend-row {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 10px;
  align-items: center;
}
.dwf-legend-k {
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--kx-muted);
  min-width: 28px;
}
/* .chip / .chip--legend 从全局 styles/pill-chip.css 继承（P1-8）。 */
</style>
