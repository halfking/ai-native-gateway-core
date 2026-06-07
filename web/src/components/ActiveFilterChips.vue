<script setup lang="ts">
import type { FilterChip } from '../composables/useFilterChips'

defineProps<{
  chips: FilterChip[]
}>()
</script>

<template>
  <div v-if="chips.length" class="active-filter-chips" style="display:flex;gap:6px;flex-wrap:wrap;margin-bottom:12px">
    <span v-for="chip in chips" :key="chip.key"
          :class="['chip', chip.className || '']"
          style="display:inline-flex;align-items:center;gap:4px;padding:4px 8px;border-radius:12px;font-size:12px;background:var(--surface-secondary);border:1px solid var(--border)">
      {{ chip.label }}
      <button @click="chip.onRemove()" style="background:none;border:none;cursor:pointer;padding:0;font-size:14px;color:var(--muted);line-height:1">×</button>
    </span>
  </div>
</template>
