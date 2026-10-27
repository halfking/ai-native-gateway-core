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
      <span v-if="tab.icon" class="seg-tab-icon">
        <!-- ★ 第 29 增量：icon 允许是**图标组件**（DESIGN.md §6：图标是画出来的，
             不许用 emoji / Unicode 字形冒充图标系统）⇒ 旧的字符串形态保留兼容。 -->
        <component :is="tab.icon" v-if="typeof tab.icon !== 'string'" />
        <template v-else>{{ tab.icon }}</template>
      </span>
      {{ tab.label }}
      <span v-if="tab.badge !== undefined && tab.badge > 0" class="seg-tab-badge">{{ tab.badge }}</span>
    </button>
  </div>
</template>

<script setup lang="ts">
import type { Component } from 'vue'

export interface SegTab {
  value: string
  label: string
  /** ★ 允许图标组件（推荐）或旧字符串形态（兼容） */
  icon?: string | Component
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
  transition: color 0.12s var(--kx-motion-ease-out), background 0.12s var(--kx-motion-ease-out), box-shadow 0.12s var(--kx-motion-ease-out);
  white-space: nowrap;
  font-family: inherit;
}
.seg-tab:hover { color: var(--text); }
.seg-tab.active {
  background: var(--card);
  color: var(--text);
  font-weight: 600;
  box-shadow: var(--kx-shadow-xs);
}
.seg-tab-icon { font-size: 12px; line-height: 1; }
.seg-tab-badge {
  display: inline-block;
  min-width: 16px;
  padding: 0 4px;
  border-radius: 99px;
  background: var(--accent);
  color: white;
  font-size: 11px;
  font-weight: 700;
  line-height: 14px;
  text-align: center;
}
</style>
