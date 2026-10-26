<script setup lang="ts">
import type { FilterChip } from '../composables/useFilterChips'
import { Close } from '@element-plus/icons-vue'

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
      {{ chip.label }} <span class="chip-remove"><Close /></span>
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

/* ── 2026-10-27 第 17 增量（AI 痕迹门 V3）：图标尺寸 ──
   element-plus 图标是裸 <svg viewBox="0 0 1024 1024">，**不带 width/height**
   ⇒ 浏览器按包含块宽度渲染（会撑爆布局）⇒ 必须显式定尺寸。
   尺寸沿用原字形的 font-size 观感（字形墨迹 ≈ 0.7em）。 */
.chip-remove > svg { width: 10px; height: 10px; }
</style>