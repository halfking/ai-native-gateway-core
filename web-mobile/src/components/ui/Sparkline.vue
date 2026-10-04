<script setup lang="ts">
// Sparkline.vue — 趋势迷你折线（纯 SVG，无图表库依赖）。
// 数据缺失时渲染占位线，不假装有趋势（13 §1 原则同源）。
import { computed } from 'vue'

const props = withDefaults(defineProps<{ points: number[]; width?: number; height?: number }>(), {
  width: 320,
  height: 56,
})

const path = computed(() => {
  const pts = props.points.filter((n) => typeof n === 'number' && Number.isFinite(n))
  if (pts.length < 2) return ''
  const max = Math.max(...pts, 1)
  const min = Math.min(...pts, 0)
  const span = max - min || 1
  const stepX = props.width / (pts.length - 1)
  return pts
    .map((v, i) => {
      const x = i * stepX
      const y = props.height - ((v - min) / span) * (props.height - 4) - 2
      return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
})
</script>

<template>
  <svg
    :width="props.width"
    :height="props.height"
    :viewBox="`0 0 ${props.width} ${props.height}`"
    fill="none"
    role="img"
    aria-hidden="true"
  >
    <path v-if="path" :d="path" stroke="var(--app-primary)" stroke-width="2" stroke-linejoin="round" />
    <line v-else x1="0" :y1="props.height - 2" :x2="props.width" :y2="props.height - 2" stroke="var(--app-border)" stroke-width="2" />
  </svg>
</template>
