<script setup lang="ts">
import { computed } from 'vue'
import type { AnalyticsFunnelStage } from '../../api-autoroute'

const props = defineProps<{
  stages: AnalyticsFunnelStage[]
  model?: string
  approximate?: boolean
  loading?: boolean
}>()

const maxVal = computed(() => Math.max(...props.stages.map(s => s.value), 1))

function widthPct(v: number): string {
  return `${Math.max(8, (v / maxVal.value) * 100)}%`
}
</script>

<template>
  <div class="funnel-wrap">
    <div v-if="loading" class="empty-hint">加载漏斗…</div>
    <div v-else-if="!stages.length" class="empty-hint">暂无 L2 漏斗数据</div>
    <template v-else>
      <div v-if="model" class="funnel-title">
        <span>{{ model }}</span>
        <span v-if="approximate" class="badge badge-muted">近似</span>
      </div>
      <div class="funnel-stages">
        <div
          v-for="(s, i) in stages"
          :key="s.key"
          class="funnel-stage"
          :title="s.hint"
        >
          <div
            class="funnel-bar"
            :style="{ width: widthPct(s.value), zIndex: stages.length - i }"
          >
            <span class="funnel-label">{{ s.label }}</span>
            <strong class="funnel-value">{{ s.value }}</strong>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.funnel-wrap { width: 100%; }
.funnel-title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  font-weight: 600;
  margin-bottom: 8px;
}
.funnel-stages {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
}
.funnel-stage {
  width: 100%;
  display: flex;
  justify-content: center;
}
.funnel-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  min-height: 32px;
  padding: 4px 12px;
  background: color-mix(in srgb, var(--accent) 22%, var(--bg-subtle));
  border: 1px solid color-mix(in srgb, var(--accent) 35%, var(--border));
  clip-path: polygon(4% 0%, 96% 0%, 100% 100%, 0% 100%);
  transition: width 0.25s ease;
}
.funnel-stage:nth-child(2) .funnel-bar {
  background: color-mix(in srgb, var(--success) 18%, var(--bg-subtle));
  border-color: color-mix(in srgb, var(--success) 30%, var(--border));
}
.funnel-stage:nth-child(3) .funnel-bar {
  background: color-mix(in srgb, #3fb950 22%, var(--bg-subtle));
  border-color: color-mix(in srgb, #3fb950 35%, var(--border));
}
.funnel-label { font-size: 10px; color: var(--muted); }
.funnel-value { font-size: 13px; font-variant-numeric: tabular-nums; }
.badge-muted {
  font-size: 9px;
  padding: 1px 5px;
  border-radius: 3px;
  background: var(--bg-subtle);
  color: var(--muted);
  font-weight: 500;
}
.empty-hint {
  padding: 12px;
  text-align: center;
  color: var(--muted);
  font-size: 11px;
}
</style>
