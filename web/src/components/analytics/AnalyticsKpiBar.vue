<script setup lang="ts">
import type { AutoRouteAudit } from '../../api-autoroute'
import { SPECIFIED_MODEL_TASK_KEY, SPECIFIED_MODEL_DISPLAY_LABEL } from '../../api-autoroute'

defineProps<{
  audit: AutoRouteAudit
}>()

function fmt(n: number, digits = 1): string {
  if (n === undefined || n === null || isNaN(n)) return '-'
  return n.toFixed(digits)
}

/** Returns the display label for a task key in the task_distribution map. */
function displayTaskLabel(key: string): string {
  return key === SPECIFIED_MODEL_TASK_KEY ? SPECIFIED_MODEL_DISPLAY_LABEL : key
}

function topEntry(d: Record<string, number>): string {
  const entries = Object.entries(d).sort((a, b) => b[1] - a[1])
  return entries[0]?.[0] ?? '-'
}
</script>

<template>
  <div class="kpi-bar">
    <div class="kpi-chip">
      <span class="kpi-label">总请求</span>
      <strong class="kpi-value">{{ audit.total_requests ?? audit.total_auto_requests }}</strong>
    </div>
    <div class="kpi-chip">
      <span class="kpi-label">Auto</span>
      <strong class="kpi-value">{{ audit.total_auto_requests }}</strong>
    </div>
    <div class="kpi-chip kpi-chip-specified">
      <span class="kpi-label">指定模型</span>
      <strong class="kpi-value">{{ audit.specified_model_requests ?? 0 }}</strong>
    </div>
    <div class="kpi-chip">
      <span class="kpi-label">成功率</span>
      <strong class="kpi-value">{{ fmt(audit.success_rate * 100, 1) }}%</strong>
    </div>
    <div class="kpi-chip">
      <span class="kpi-label">Top 任务</span>
      <strong class="kpi-value">{{ displayTaskLabel(topEntry(audit.task_distribution)) }}</strong>
    </div>
    <div class="kpi-chip">
      <span class="kpi-label">Top 模型</span>
      <strong class="kpi-value">{{ audit.top_chosen_models[0]?.model ?? '-' }}</strong>
    </div>
  </div>
</template>

<style scoped>
.kpi-bar {
  display: grid;
  grid-template-columns: repeat(6, minmax(0, 1fr));
  gap: 6px;
}
.kpi-chip {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 6px 8px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 6px;
}
.kpi-chip-specified {
  border-left: 3px solid #6b7280;
}
.kpi-chip-specified .kpi-value {
  color: #6b7280;
  font-style: italic;
}
.kpi-label {
  font-size: 9px;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.03em;
}
.kpi-value {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
@media (max-width: 1024px) {
  .kpi-bar { grid-template-columns: repeat(3, minmax(0, 1fr)); }
}
@media (max-width: 640px) {
  .kpi-bar { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
</style>
