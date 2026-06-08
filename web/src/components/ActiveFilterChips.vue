<script setup lang="ts">
import type { FilterChip } from '../composables/useFilterChips'

defineProps<{
  chips: FilterChip[]
}>()
</script>

<template>
  <div v-if="chips.length" class="active-filters">
    <span class="active-filters-label">已选条件</span>
    <button
      v-for="chip in chips"
      :key="chip.key"
      type="button"
      class="active-filter-chip"
      :class="chip.className"
      @click="chip.onRemove()"
      :title="`移除 ${chip.label}`"
    >
      {{ chip.label }} <span class="chip-remove">×</span>
    </button>
  </div>
</template>

<style scoped>
.active-filters {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}

.active-filters-label {
  font-size: 12px;
  color: var(--text-muted, var(--muted));
  font-weight: 600;
}

.active-filter-chip {
  border: 1px solid var(--border);
  background: var(--bg);
  border-radius: 999px;
  padding: 4px 10px;
  font-size: 12px;
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.chip-remove {
  font-size: 13px;
  line-height: 1;
}
</style>