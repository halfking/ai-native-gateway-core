<script setup lang="ts">
// SixDimScoreBar — 6 维度水平条形图（纯 SVG，零依赖）
// 复用 KeyDetailView.vue 的 viewBox + path 计算模式

const props = withDefaults(defineProps<{
  scores: {
    price_score?: number
    speed_score?: number
    stability_score?: number
    match_score?: number
    pressure_score?: number
    context_fit?: number
  }
  compact?: boolean
}>(), {
  compact: false,
})

const DIMS = [
  { key: 'price_score',     label: '价格',   color: 'var(--success)' },
  { key: 'speed_score',     label: '速度',   color: 'var(--accent)' },
  { key: 'stability_score', label: '稳定性', color: '#3fb950' },
  { key: 'match_score',     label: '匹配度', color: '#d29922' },
  { key: 'pressure_score',  label: '压力',   color: '#f85149' },
  { key: 'context_fit',     label: '上下文', color: '#a371f7' },
] as const

function val(k: string): number {
  const v = (props.scores as Record<string, number | undefined>)[k]
  if (v === undefined || v === null || isNaN(v)) return 0
  return Math.max(0, Math.min(100, v))
}
</script>

<template>
  <div class="six-dim" :class="{ compact }">
    <div v-for="dim in DIMS" :key="dim.key" class="dim-row">
      <span class="dim-label">{{ dim.label }}</span>
      <div class="dim-bar-bg">
        <div
          class="dim-bar-fill"
          :style="{ width: val(dim.key) + '%', background: dim.color }"
        />
      </div>
      <span class="dim-value">{{ val(dim.key).toFixed(0) }}</span>
    </div>
  </div>
</template>

<style scoped>
.six-dim {
  display: grid;
  gap: 4px;
}
.six-dim.compact {
  gap: 1px;
}
.dim-row {
  display: grid;
  grid-template-columns: 52px 1fr 24px;
  align-items: center;
  gap: 4px;
  font-size: 11px;
}
.compact .dim-row {
  font-size: 9px;
  grid-template-columns: 40px 1fr 20px;
  gap: 3px;
}
.dim-label {
  color: var(--muted);
  text-align: right;
  white-space: nowrap;
}
.dim-bar-bg {
  height: 10px;
  background: color-mix(in srgb, var(--border) 40%, transparent);
  border-radius: 3px;
  overflow: hidden;
}
.compact .dim-bar-bg {
  height: 5px;
}
.dim-bar-fill {
  height: 100%;
  border-radius: 3px;
  transition: width 0.3s ease;
  min-width: 2px;
}
.dim-value {
  color: var(--text);
  font-variant-numeric: tabular-nums;
  text-align: right;
  font-size: 10px;
}
</style>