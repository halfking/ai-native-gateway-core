<script setup lang="ts">
// StatCard — 汇总指标卡（值 tabular-nums；hint 放降级口径提示）。
defineProps<{
  label: string
  value: string
  hint?: string
}>()
</script>

<template>
  <div class="stat-card data-card">
    <div class="stat-card__label">{{ label }}</div>
    <div class="stat-card__value num">{{ value }}</div>
    <div v-if="hint" class="stat-card__hint">{{ hint }}</div>
  </div>
</template>

<style scoped>
.stat-card__label {
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.stat-card__value {
  font-size: 1.25rem;
  font-weight: 600;
  margin-top: 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.stat-card__hint {
  font-size: 0.6875rem;
  color: var(--app-warning);
  margin-top: 2px;
}
</style>
