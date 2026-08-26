<template>
  <div class="seg-tabs" role="tablist">
    <button
      v-for="tab in tabs"
      :key="tab.value"
      class="seg-tab"
      :class="{ active: modelValue === tab.value }"
      role="tab"
      :aria-selected="modelValue === tab.value"
      :data-tab="tab.value"
      @click="$emit('update:modelValue', tab.value)"
    >
      <span v-if="tab.icon" class="seg-tab-icon">{{ tab.icon }}</span>
      {{ tab.label }}
      <span v-if="tab.badge !== undefined && tab.badge > 0" class="seg-tab-badge">{{ tab.badge }}</span>
    </button>
  </div>
</template>

<script setup lang="ts">
export interface SegTab {
  value: string
  label: string
  icon?: string
  badge?: number
}

withDefaults(
  defineProps<{
    tabs: SegTab[]
    modelValue: string
  }>(),
  {},
)

defineEmits<{
  (e: 'update:modelValue', value: string): void
}>()
</script>

<style scoped>
/* Segmented tabs (2026-06-23) — mirrors RoutingDashboardView.vue:1270-1296
   so the visual density matches the existing /routing-v2 dashboard. */
.seg-tabs {
  display: inline-flex;
  gap: 1px;
  padding: 2px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 6px;
}
.seg-tab {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 12px;
  border: none;
  border-radius: 4px;
  background: transparent;
  font-size: 12px;
  color: var(--muted);
  cursor: pointer;
  transition: all 0.12s;
  white-space: nowrap;
  font-family: inherit;
}
.seg-tab:hover { color: var(--text); }
.seg-tab.active {
  background: var(--card);
  color: var(--text);
  font-weight: 600;
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.12);
}
.seg-tab-icon { font-size: 12px; line-height: 1; }
.seg-tab-badge {
  display: inline-block;
  min-width: 16px;
  padding: 0 4px;
  border-radius: 99px;
  background: var(--accent);
  color: white;
  font-size: 10px;
  font-weight: 700;
  line-height: 14px;
  text-align: center;
}
</style>
