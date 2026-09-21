<script setup lang="ts">
/**
 * CredentialMonitorFilters — 凭据监控第二行工具栏（2026-09-13 P3-5 第二批，
 * 自 CredentialMonitorView.vue 拆出，方案 §4.7-4 大文件拆分）：
 * 可用性/健康筛选 + 快捷过滤 + 批量操作入口。
 */
defineProps<{
  availStateFilter: string
  healthFilter: string
  quickFilter: 'none' | 'broken' | 'low-rate'
  selectedCount: number
  canManage: boolean
}>()

const emit = defineEmits<{
  'update:availStateFilter': [value: string]
  'update:healthFilter': [value: string]
  'update:quickFilter': [value: 'none' | 'broken' | 'low-rate']
  batch: [action: 'promote' | 'demote']
}>()
</script>

<template>
  <div class="top-bar top-bar-secondary">
    <span class="label">可用性</span>
    <select
      :value="availStateFilter"
      class="field-input"
      @change="emit('update:availStateFilter', ($event.target as HTMLSelectElement).value)"
    >
      <option value="">全部</option>
      <option value="ready">ready</option>
      <option value="degraded">degraded</option>
      <option value="cooling">cooling</option>
      <option value="unreachable">unreachable</option>
    </select>
    <span class="label">健康</span>
    <select
      :value="healthFilter"
      class="field-input"
      @change="emit('update:healthFilter', ($event.target as HTMLSelectElement).value)"
    >
      <option value="">全部</option>
      <option value="healthy">healthy</option>
      <option value="warning">warning</option>
      <option value="unreachable">unreachable</option>
    </select>
    <div class="quick-filter-group">
      <button class="btn btn-sm btn-ghost" :class="quickFilter === 'none' ? 'qf-active' : ''" @click="emit('update:quickFilter', 'none')">全部</button>
      <button class="btn btn-sm btn-ghost" :class="quickFilter === 'broken' ? 'qf-active qf-bad' : ''" @click="emit('update:quickFilter', 'broken')">只看 broken</button>
      <button class="btn btn-sm btn-ghost" :class="quickFilter === 'low-rate' ? 'qf-active qf-warn' : ''" @click="emit('update:quickFilter', 'low-rate')">成功率&lt;50%</button>
    </div>
    <span class="spacer"></span>
    <button class="btn btn-sm btn-success" :disabled="!canManage || selectedCount === 0" @click="emit('batch', 'promote')">
      批量恢复 ({{ selectedCount }})
    </button>
    <button class="btn btn-sm btn-danger" :disabled="!canManage || selectedCount === 0" @click="emit('batch', 'demote')">
      批量降级 ({{ selectedCount }})
    </button>
  </div>
</template>

<style scoped>
.top-bar {
  padding: 6px 10px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  font-size: 11px;
  color: var(--muted);
}
.top-bar > * { flex-shrink: 0; }

/* Secondary row: allow wrapping if needed */
.top-bar-secondary {
  flex-wrap: wrap;
}
.top-bar > .label {
  font-size: 11px;
}
.top-bar .field-input {
  width: auto;
  max-width: 120px;
  font-size: 11px;
  padding: 2px 6px;
}
.top-bar .spacer { flex: 1; }
.top-bar .btn-sm { font-size: 11px; padding: 2px 8px; }
.top-bar .quick-filter-group { display: inline-flex; gap: 4px; flex-wrap: nowrap; }

/* Quick filter pills */
.quick-filter-group {
  display: inline-flex;
  gap: 4px;
}
.qf-active {
  border-color: var(--accent);
  color: var(--accent-h);
}
.qf-active.qf-bad { border-color: var(--danger); color: var(--danger); }
.qf-active.qf-warn { border-color: var(--warning); color: var(--warning); }
</style>
