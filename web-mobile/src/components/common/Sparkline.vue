<script setup lang="ts">
// Sparkline — 依赖零的 SVG 迷你趋势线（移动端快查不需要完整图表库）。
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    points: number[]
    width?: number
    height?: number
    tone?: string
    fill?: boolean
  }>(),
  { width: 120, height: 36, tone: 'var(--app-primary)', fill: true },
)

const path = computed<string>(() => {
  const pts = props.points
  const w = props.width
  const h = props.height
  if (pts.length < 2) return ''
  const max = Math.max(...pts, 1)
  const min = Math.min(...pts, 0)
  const span = max - min || 1
  const step = w / (pts.length - 1)
  return pts
    .map((v, i) => {
      const x = i * step
      const y = h - 3 - ((v - min) / span) * (h - 6)
      return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)} ${y.toFixed(1)}`
    })
    .join(' ')
})

const areaPath = computed<string>(() => {
  if (!path.value || !props.fill) return ''
  return `${path.value} L${props.width} ${props.height} L0 ${props.height} Z`
})
</script>

<template>
  <svg :width="width" :height="height" :viewBox="`0 0 ${width} ${height}`" class="sparkline" aria-hidden="true">
    <path v-if="areaPath" :d="areaPath" :fill="tone" opacity="0.12" />
    <path :d="path" fill="none" :stroke="tone" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
  </svg>
</template>

<style scoped>
.sparkline {
  display: block;
}
</style>
