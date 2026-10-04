<script setup lang="ts">
// StatusDot.vue — 状态徽标（soft 底色 + 点 + 文案，绝不只靠色相，UI 规范 13 §1）。
defineProps<{ tone: 'success' | 'warning' | 'danger' | 'info'; label: string }>()
</script>

<template>
  <span class="m-chip" :class="`m-chip--${tone}`">
    <span class="m-dot"></span>
    {{ label }}
  </span>
</template>

<style scoped>
.m-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: currentColor;
  flex: none;
}
</style>
