<script setup lang="ts">
// SaTrendChart.vue — 会话分析趋势折线图（纯 SVG，2026-11-20）
//
// 为什么不用 ECharts：会话分析的三个趋势图（会话数 / 花费 / Token）都是
// 单序列折线 + 无交互需求，ECharts 的配置与体积在这里是纯开销；纯 SVG 也
// 让颜色完全走 CSS 变量（色板切换自动跟随主题）。
//
// 口径提醒：横轴是**日期字符串**，由后端按其自身的日界口径产出；前端
// **不再做时区换算**，否则同一个趋势图在两端会错位一天。

import { computed } from 'vue'

export interface SaTrendPoint {
  /** X 轴标签（YYYY-MM-DD） */
  label: string
  value: number
}

const props = withDefaults(
  defineProps<{
    points: SaTrendPoint[]
    /** 图表高度（px） */
    height?: number
    /** 颜色令牌名（CSS 变量名） */
    colorToken?: string
    /** 数值格式化（用于 Y 轴刻度与端点标签） */
    format?: (value: number) => string
    /** 无数据时的提示文案（已翻译） */
    emptyText?: string
  }>(),
  {
    height: 180,
    colorToken: '--accent',
    format: (value: number) => String(value),
    emptyText: '',
  },
)

const W = 720
const PAD_LEFT = 52
const PAD_RIGHT = 14
const PAD_TOP = 14
const PAD_BOTTOM = 26

const innerW = computed(() => W - PAD_LEFT - PAD_RIGHT)
const innerH = computed(() => props.height - PAD_TOP - PAD_BOTTOM)

/** 上界：至少为 1，避免全零序列导致除零。 */
const maxValue = computed(() => {
  const m = props.points.reduce((acc, p) => (p.value > acc ? p.value : acc), 0)
  return m > 0 ? m : 1
})

/** X 坐标均分；单点时落在中点。 */
function xAt(index: number): number {
  const n = props.points.length
  if (n <= 1) return PAD_LEFT + innerW.value / 2
  return PAD_LEFT + (innerW.value * index) / (n - 1)
}

/** Y 坐标：值域 [0, max] 映射到 [底, 顶]。 */
function yAt(value: number): number {
  const ratio = Math.max(0, Math.min(1, value / maxValue.value))
  return PAD_TOP + innerH.value * (1 - ratio)
}

const linePoints = computed(() =>
  props.points.map((p, i) => `${xAt(i).toFixed(2)},${yAt(p.value).toFixed(2)}`).join(' '),
)

/** 面积填充：折线与基线围成的闭合多边形。 */
const areaPoints = computed(() => {
  if (props.points.length === 0) return ''
  const baseline = (PAD_TOP + innerH.value).toFixed(2)
  const first = `${xAt(0).toFixed(2)},${baseline}`
  const last = `${xAt(props.points.length - 1).toFixed(2)},${baseline}`
  return `${first} ${linePoints.value} ${last}`
})

/** Y 轴刻度：0 / 中值 / 上界。 */
const yTicks = computed(() => {
  const mid = maxValue.value / 2
  return [
    { value: 0, y: yAt(0), label: props.format(0) },
    { value: mid, y: yAt(mid), label: props.format(Math.round(mid)) },
    { value: maxValue.value, y: yAt(maxValue.value), label: props.format(Math.round(maxValue.value)) },
  ]
})

/** X 轴标签：首 / 中 / 末（多于 3 个点才显示中间那个）。 */
const xTicks = computed(() => {
  const n = props.points.length
  if (n === 0) return []
  const idx = n === 1 ? [0] : n === 2 ? [0, 1] : [0, Math.floor((n - 1) / 2), n - 1]
  return idx.map((i) => ({ x: xAt(i), label: props.points[i].label }))
})

/** 末端点标记（最后一个数据点）。 */
const endPoint = computed(() => {
  const n = props.points.length
  if (n === 0) return null
  const p = props.points[n - 1]
  return { x: xAt(n - 1), y: yAt(p.value), value: p.value }
})
</script>

<template>
  <div class="sa-trend">
    <svg
      v-if="points.length > 0"
      class="sa-trend__svg"
      :viewBox="`0 0 ${W} ${height}`"
      role="img"
      :aria-label="`trend chart, ${points.length} points`"
      preserveAspectRatio="none"
    >
      <!-- 网格与 Y 轴刻度 -->
      <g class="sa-trend__grid">
        <line
          v-for="tick in yTicks"
          :key="`g${tick.value}`"
          :x1="PAD_LEFT"
          :x2="W - PAD_RIGHT"
          :y1="tick.y"
          :y2="tick.y"
        />
      </g>
      <g class="sa-trend__axis-label">
        <text
          v-for="tick in yTicks"
          :key="`t${tick.value}`"
          :x="PAD_LEFT - 8"
          :y="tick.y + 4"
          text-anchor="end"
        >
          {{ tick.label }}
        </text>
      </g>

      <!-- 面积 + 折线 -->
      <polygon class="sa-trend__area" :points="areaPoints" :style="{ fill: `var(${colorToken})` }" />
      <polyline
        class="sa-trend__line"
        :points="linePoints"
        :style="{ stroke: `var(${colorToken})` }"
        fill="none"
      />

      <!-- 末端点 -->
      <circle
        v-if="endPoint"
        class="sa-trend__end"
        :cx="endPoint.x"
        :cy="endPoint.y"
        r="4"
        :style="{ fill: `var(${colorToken})` }"
      />

      <!-- X 轴标签 -->
      <g class="sa-trend__axis-label">
        <text
          v-for="tick in xTicks"
          :key="tick.label"
          :x="tick.x"
          :y="height - 8"
          text-anchor="middle"
        >
          {{ tick.label }}
        </text>
      </g>
    </svg>
    <div v-else class="sa-trend__empty">{{ emptyText }}</div>
  </div>
</template>

<style scoped>
.sa-trend {
  width: 100%;
}

.sa-trend__svg {
  width: 100%;
  height: auto;
  display: block;
  overflow: visible;
}

.sa-trend__grid line {
  stroke: var(--border);
  stroke-width: 1;
  stroke-dasharray: 3 3;
}

.sa-trend__axis-label text {
  fill: var(--muted);
  font-size: 11px;
  font-family: var(--font-mono, monospace);
}

.sa-trend__area {
  opacity: 0.12;
}

.sa-trend__line {
  stroke-width: 2;
  stroke-linejoin: round;
  stroke-linecap: round;
}

.sa-trend__end {
  stroke: var(--surface);
  stroke-width: 2;
}

.sa-trend__empty {
  padding: 24px;
  text-align: center;
  color: var(--muted);
  font-size: 13px;
}
</style>
