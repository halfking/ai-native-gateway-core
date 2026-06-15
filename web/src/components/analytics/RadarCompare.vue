<script setup lang="ts">
import { computed } from 'vue'

export interface RadarCandidate {
  model: string
  composite_score?: number
  price_score?: number
  speed_score?: number
  stability_score?: number
  match_score?: number
  pressure_score?: number
  context_fit?: number
}

const props = withDefaults(defineProps<{
  candidates: RadarCandidate[]
  size?: number
}>(), {
  size: 200,
})

const DIMS = [
  { key: 'price_score', label: '价格' },
  { key: 'speed_score', label: '速度' },
  { key: 'stability_score', label: '稳定' },
  { key: 'match_score', label: '匹配' },
  { key: 'pressure_score', label: '压力' },
  { key: 'context_fit', label: '上下文' },
] as const

const COLORS = ['var(--accent)', 'var(--success)', '#d29922']

const top3 = computed(() => props.candidates.slice(0, 3))

function val(c: RadarCandidate, key: string): number {
  const v = (c as Record<string, number | undefined>)[key]
  if (v === undefined || v === null || isNaN(v)) return 0
  return Math.max(0, Math.min(100, v)) / 100
}

const cx = computed(() => props.size / 2)
const cy = computed(() => props.size / 2)
const R = computed(() => props.size * 0.38)

function point(angleIdx: number, ratio: number): string {
  const angle = (Math.PI * 2 * angleIdx) / DIMS.length - Math.PI / 2
  const x = cx.value + Math.cos(angle) * R.value * ratio
  const y = cy.value + Math.sin(angle) * R.value * ratio
  return `${x.toFixed(1)},${y.toFixed(1)}`
}

function polyPath(c: RadarCandidate): string {
  return DIMS.map((_, i) => point(i, val(c, DIMS[i].key))).join(' ')
}

const gridLevels = [0.25, 0.5, 0.75, 1]
</script>

<template>
  <div class="radar-wrap">
    <div v-if="!top3.length" class="empty-hint">暂无 Top3 候选对比数据</div>
    <template v-else>
      <svg :width="size" :height="size" :viewBox="`0 0 ${size} ${size}`" class="radar-svg">
        <g v-for="lvl in gridLevels" :key="lvl">
          <polygon
            :points="DIMS.map((_, i) => point(i, lvl)).join(' ')"
            class="grid-poly"
          />
        </g>
        <line
          v-for="(_, i) in DIMS"
          :key="'axis-' + i"
          :x1="cx"
          :y1="cy"
          :x2="point(i, 1).split(',')[0]"
          :y2="point(i, 1).split(',')[1]"
          class="axis-line"
        />
        <polygon
          v-for="(c, ci) in top3"
          :key="c.model"
          :points="polyPath(c)"
          class="data-poly"
          :style="{ stroke: COLORS[ci], fill: `color-mix(in srgb, ${COLORS[ci]} 18%, transparent)` }"
        />
        <text
          v-for="(dim, i) in DIMS"
          :key="dim.key"
          :x="point(i, 1.18).split(',')[0]"
          :y="point(i, 1.18).split(',')[1]"
          class="axis-label"
          text-anchor="middle"
          dominant-baseline="middle"
        >{{ dim.label }}</text>
      </svg>
      <div class="radar-legend">
        <span v-for="(c, i) in top3" :key="c.model" class="legend-item">
          <span class="legend-dot" :style="{ background: COLORS[i] }" />
          #{{ i + 1 }} {{ c.model }}
          <span class="text-muted">({{ (c.composite_score ?? 0).toFixed(1) }})</span>
        </span>
      </div>
    </template>
  </div>
</template>

<style scoped>
.radar-wrap {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
}
.radar-svg { display: block; }
.grid-poly {
  fill: none;
  stroke: var(--border);
  stroke-width: 0.5;
}
.axis-line {
  stroke: color-mix(in srgb, var(--border) 70%, transparent);
  stroke-width: 0.5;
}
.data-poly {
  fill-opacity: 0.35;
  stroke-width: 1.5;
}
.axis-label {
  font-size: 8px;
  fill: var(--muted);
}
.radar-legend {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  justify-content: center;
  font-size: 10px;
}
.legend-item { display: flex; align-items: center; gap: 4px; }
.legend-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}
.empty-hint {
  padding: 12px;
  color: var(--muted);
  font-size: 11px;
  text-align: center;
}
</style>
