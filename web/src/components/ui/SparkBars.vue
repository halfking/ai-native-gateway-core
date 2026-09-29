<script setup lang="ts">
// SparkBars.vue — KPI 卡迷你柱状 sparkline（2026-09-30 统计 UI 优化轮）
//
// 纯 SVG 渲染（非 canvas），jsdom 测试环境直接可见；颜色接收 --kx-* 令牌名，
// 运行时经 getComputedStyle 解析为字面量（暗/亮主题各自取值，主题切换后
// 由父级重挂载或重渲染刷新；本组件不自行监听，与全站 sparkline 语义一致）。
import { computed, onMounted, ref } from 'vue'

const props = withDefaults(defineProps<{
  /** 数值序列（空序列渲染空轨道） */
  data: number[]
  /** CSS 颜色令牌名（如 '--accent'）或字面量 hex */
  colorToken?: string
  /** 高度 px */
  height?: number
}>(), {
  colorToken: '--accent',
  height: 34,
})

const resolved = ref('')

onMounted(() => {
  if (typeof window === 'undefined' || !window.getComputedStyle) {
    resolved.value = '#5b8cff'
    return
  }
  const v = getComputedStyle(document.documentElement).getPropertyValue(props.colorToken).trim()
  resolved.value = v || '#5b8cff'
})

const W = 88
const bars = computed(() => {
  const data = props.data ?? []
  const max = Math.max(...data, 1)
  const bw = data.length > 0 ? W / data.length : W
  return data.map((v, i) => ({
    x: i * bw + 1,
    y: props.height - Math.max(2, (v / max) * (props.height - 4)),
    w: Math.max(1.5, bw - 2.5),
    h: Math.max(2, (v / max) * (props.height - 4)),
  }))
})
</script>

<template>
  <svg
    :viewBox="`0 0 ${W} ${height}`"
    preserveAspectRatio="none"
    :style="{ width: '100%', height: height + 'px', display: 'block' }"
    aria-hidden="true"
  >
    <rect
      v-for="(b, i) in bars"
      :key="i"
      :x="b.x"
      :y="b.y"
      :width="b.w"
      :height="b.h"
      :fill="resolved"
      rx="1.5"
      opacity="0.85"
    />
  </svg>
</template>
