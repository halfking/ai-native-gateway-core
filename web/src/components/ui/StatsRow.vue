<script setup lang="ts">
/**
 * StatsRow.vue — 统计卡响应式栅格容器（2026-09-13，方案 §4.5.3）。
 *
 * 子项即 StatCard，栅格随**容器宽度**（container query，非视口）切换列数，
 * 抽屉/面板内也能正确降列。默认 mobile 2 列 → tablet 2 列 → desktop 4 列，
 * 经 cols prop 可配。不含容器查询能力的浏览器按 mobile 档 2 列兜底渲染。
 *
 *   <StatsRow :cols="{ mobile: 2, tablet: 2, desktop: 4 }">
 *     <StatCard :label="…" :value="…" tone="success" />
 *   </StatsRow>
 */
withDefaults(defineProps<{
  /** 各档位列数（容器宽度 768/1024 为切换点，取值来自 breakpoints.ts） */
  cols?: { mobile?: number; tablet?: number; desktop?: number }
}>(), {
  cols: undefined,
})
</script>

<template>
  <div
    class="stats-row-outer"
    :style="{
      '--sr-cols-mobile': String(cols?.mobile ?? 2),
      '--sr-cols-tablet': String(cols?.tablet ?? 2),
      '--sr-cols-desktop': String(cols?.desktop ?? 4),
    }"
  >
    <div class="stats-row">
      <slot />
    </div>
  </div>
</template>

<style scoped>
/* 容器查询的宿主：以本元素宽度（而非视口）决定子栅格列数 */
.stats-row-outer {
  container-type: inline-size;
  width: 100%;
}

.stats-row {
  display: grid;
  grid-template-columns: repeat(var(--sr-cols-mobile, 2), minmax(0, 1fr));
  gap: var(--kx-space-3);
}

/* 断点数值对齐 src/config/breakpoints.ts（tablet=768 / desktop=1024） */
@container (min-width: 768px) {
  .stats-row {
    grid-template-columns: repeat(var(--sr-cols-tablet, 2), minmax(0, 1fr));
  }
}
@container (min-width: 1024px) {
  .stats-row {
    grid-template-columns: repeat(var(--sr-cols-desktop, 4), minmax(0, 1fr));
  }
}
</style>
