<script setup lang="ts">
// SkeletonList.vue — 骨架屏（UI 规范 13 §1：initialLoading 用骨架，不留白屏）。
withDefaults(defineProps<{ lines?: number; height?: string }>(), { lines: 3, height: '3.2rem' })
</script>

<template>
  <div class="skeleton-list" aria-hidden="true">
    <div
      v-for="i in lines"
      :key="i"
      class="m-skeleton"
      :style="{ height }"
    ></div>
  </div>
</template>

<style scoped>
.skeleton-list {
  padding: var(--app-space-2) 0;
  display: grid;
  gap: var(--app-space-2);
}
</style>
