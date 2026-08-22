<script setup lang="ts">
/**
 * QueueWaterfallTimeline — relative T0 waterfall (CSS grid, not ECharts).
 */
import { computed } from 'vue'
import type { WaterfallRequest } from '../api/dispatch'
import {
  WATERFALL_STAGES,
  emptyStateMessage,
  formatAxisMs,
  layoutRows,
  medianComposition,
} from '../utils/waterfallTimeline'
import DispatchWaterfallRow from './DispatchWaterfallRow.vue'

const props = defineProps<{
  requests: WaterfallRequest[]
  loading?: boolean
  wired?: boolean
  source?: string
  selectedId?: string | null
}>()

const emit = defineEmits<{
  select: [req: WaterfallRequest]
}>()

const emptyText = computed(() =>
  emptyStateMessage({ wired: props.wired, source: props.source, count: props.requests.length }),
)

const laid = computed(() => layoutRows(props.requests))
const composition = computed(() => medianComposition(laid.value.rows.map((r) => r.bars)))
const hasData = computed(() => props.requests.length > 0)
</script>

<template>
  <div class="qwt" :class="{ loading: props.loading }" data-testid="qwt">
    <div v-if="!hasData && !props.loading" class="qwt-empty" data-testid="qwt-empty">{{ emptyText }}</div>
    <template v-else-if="hasData">
      <div v-if="composition.length" class="qwt-comp" data-testid="qwt-composition">
        <span class="qwt-comp-k">样本中位构成</span>
        <div class="qwt-comp-bar">
          <span
            v-for="s in composition"
            :key="s.key"
            :style="{ width: s.pct + '%', background: s.color }"
            :title="`${s.label}: 中位 ${Math.round(s.ms)} ms · ${s.pct.toFixed(0)}%`"
          />
        </div>
      </div>
      <div class="qwt-legend" data-testid="qwt-legend">
        <span v-for="st in WATERFALL_STAGES" :key="st.key" class="chip">
          <i :style="{ background: st.color }" />{{ st.label }}
        </span>
      </div>
      <div class="qwt-head">
        <span>请求</span>
        <span>结果</span>
        <span class="num">总耗时</span>
        <span class="axis"><span>0</span><span>{{ formatAxisMs(laid.axisMax) }}</span></span>
        <span class="num">queue</span>
        <span class="num">ttfb</span>
      </div>
      <div class="qwt-body">
        <DispatchWaterfallRow
          v-for="row in laid.rows"
          :key="row.request.request_id"
          :row="row"
          :selected="row.request.request_id === props.selectedId"
          @select="emit('select', row.request)"
        />
      </div>
    </template>
  </div>
</template>

<style scoped>
.qwt {
  background: var(--kx-surface);
  border: 1px solid var(--kx-border);
  border-radius: var(--radius, 8px);
  padding: 12px;
}
.qwt.loading { opacity: 0.72; }
.qwt-empty {
  text-align: center;
  color: var(--kx-muted);
  padding: 48px 12px;
  font-size: 13px;
}
.qwt-comp {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}
.qwt-comp-k {
  font-size: 11px;
  color: var(--kx-muted);
  white-space: nowrap;
}
.qwt-comp-bar {
  display: flex;
  flex: 1;
  height: 10px;
  border-radius: 4px;
  overflow: hidden;
  background: var(--kx-bg);
}
.qwt-comp-bar span { display: block; min-width: 2px; height: 100%; }
.qwt-legend {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 12px;
  align-items: center;
  margin: 0 0 8px;
  padding: 0 8px;
}
.chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--kx-text);
  font-size: 11px;
}
.chip i {
  width: 8px;
  height: 8px;
  border-radius: 2px;
  display: inline-block;
}
.qwt-head,
.qwt-body :deep(.qwt-row) {
  display: grid;
  grid-template-columns: minmax(140px, 180px) 78px 72px minmax(220px, 1fr) 72px 72px;
  gap: 8px;
  align-items: center;
}
.qwt-head {
  padding: 4px 8px 8px;
  font-size: 11px;
  color: var(--kx-muted);
  border-bottom: 1px solid var(--kx-border);
  position: sticky;
  top: 0;
  background: var(--kx-surface);
  z-index: 1;
}
.qwt-head .num { text-align: right; font-variant-numeric: tabular-nums; }
.qwt-head .axis {
  display: flex;
  justify-content: space-between;
  font-variant-numeric: tabular-nums;
}
.qwt-body { max-height: calc(100vh - 320px); overflow: auto; }
</style>
