<script setup lang="ts">
import type { AutoRouteAudit } from '../../api-autoroute'

defineProps<{
  audit: AutoRouteAudit
}>()

function fmt(n: number, digits = 1): string {
  if (n === undefined || n === null || isNaN(n)) return '-'
  return n.toFixed(digits)
}

function topEntry(d: Record<string, number>): string {
  const entries = Object.entries(d).sort((a, b) => b[1] - a[1])
  return entries[0]?.[0] ?? '-'
}
</script>

<template>
  <div class="kpi-bar">
    <div class="kpi-chip">
      <span class="kpi-label">Auto 请求</span>
      <strong class="kpi-value">{{ audit.total_auto_requests }}</strong>
    </div>
    <div class="kpi-chip">
      <span class="kpi-label">成功率</span>
      <strong class="kpi-value">{{ fmt(audit.success_rate * 100, 1) }}%</strong>
    </div>
    <div class="kpi-chip">
      <span class="kpi-label">Top 任务</span>
      <strong class="kpi-value">{{ topEntry(audit.task_distribution) }}</strong>
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
  grid-template-columns: repeat(4, minmax(0, 1fr));
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
@media (max-width: 640px) {
  .kpi-bar { grid-template-columns: repeat(2, 1fr); }
}
</style>
