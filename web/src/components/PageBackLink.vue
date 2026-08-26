<script setup lang="ts">
import { computed } from 'vue'
import { useRouter, type RouteLocationRaw } from 'vue-router'

const props = defineProps<{
  to?: RouteLocationRaw
  label?: string
}>()

const router = useRouter()

const displayLabel = computed(() => props.label || '返回')

function goBack() {
  if (props.to) {
    router.push(props.to)
    return
  }
  if (window.history.length > 1) {
    router.back()
    return
  }
  router.push('/')
}
</script>

<template>
  <button type="button" class="page-back" @click="goBack">
    ← {{ displayLabel }}
  </button>
</template>

<style scoped>
.page-back {
  padding: 6px 12px;
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  cursor: pointer;
  font-size: 13px;
  white-space: nowrap;
  flex-shrink: 0;
}
.page-back:hover {
  background: rgba(255, 255, 255, 0.05);
}
</style>
